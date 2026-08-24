package persistence

import (
	"bytes"
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/fact"
)

const factualIncrementalNeutralSnapshotID = "factual-incremental-neutral-snapshot"

type factualIncrementalRevision struct {
	scope               fact.Scope
	configurationID     string
	artifactsByPath     map[string]contract.Artifact
	manifestsByProducer map[string]fact.FrontendManifest
	manifestsByFamily   map[string][]fact.FrontendManifest
	rulesByKey          map[string]factualIncrementalRule
}

type factualIncrementalRule struct {
	ruleID               string
	version              string
	implementationDigest string
	configuration        json.RawMessage
}

type factualIncrementalReasons map[string]map[FactualInvalidationReason]struct{}

// PlanFactualSnapshotUpdate compares two validated factual revisions without
// reading or writing persistence. It reports reusable observations and the
// previous derived fanout that must be evaluated again; it never copies fact
// payloads or source content into the result.
func PlanFactualSnapshotUpdate(
	ctx context.Context,
	previous FactualSnapshotRevision,
	current FactualSnapshotRevision,
) (FactualSnapshotDelta, error) {
	if err := factualIncrementalContext(ctx); err != nil {
		return FactualSnapshotDelta{}, err
	}
	previousRevision, err := prepareFactualIncrementalRevision(ctx, previous)
	if err != nil {
		return FactualSnapshotDelta{}, err
	}
	currentRevision, err := prepareFactualIncrementalRevision(ctx, current)
	if err != nil {
		return FactualSnapshotDelta{}, err
	}
	if err := factualIncrementalContext(ctx); err != nil {
		return FactualSnapshotDelta{}, err
	}
	if previousRevision.scope.OrganizationID != currentRevision.scope.OrganizationID || previousRevision.scope.SourceID != currentRevision.scope.SourceID || previousRevision.scope.SnapshotID == currentRevision.scope.SnapshotID {
		return FactualSnapshotDelta{}, ErrInvalidFactualIncremental
	}

	delta := FactualSnapshotDelta{
		PreviousScope: previousRevision.scope,
		CurrentScope:  currentRevision.scope,
	}
	if err := classifyFactualArtifacts(ctx, previousRevision.artifactsByPath, currentRevision.artifactsByPath, &delta); err != nil {
		return FactualSnapshotDelta{}, err
	}

	previousFacts, err := buildFactualIncrementalFacts(ctx, previous.Snapshot.Facts)
	if err != nil {
		return FactualSnapshotDelta{}, err
	}
	currentFacts, err := buildFactualIncrementalFacts(ctx, current.Snapshot.Facts)
	if err != nil {
		return FactualSnapshotDelta{}, err
	}

	outputInvalidations := make(factualIncrementalReasons)
	previousInvalidations := make(factualIncrementalReasons)
	if err := classifyFactualObserved(
		ctx,
		previousRevision,
		currentRevision,
		previousFacts,
		currentFacts,
		&delta,
		outputInvalidations,
		previousInvalidations,
	); err != nil {
		return FactualSnapshotDelta{}, err
	}

	dependents, err := factualIncrementalDependents(ctx, previousFacts.derived)
	if err != nil {
		return FactualSnapshotDelta{}, err
	}
	if err := seedFactualDerivedInvalidations(ctx, previousRevision, currentRevision, previousFacts.derived, previousInvalidations); err != nil {
		return FactualSnapshotDelta{}, err
	}
	if err := propagateFactualInvalidations(ctx, dependents, previousInvalidations); err != nil {
		return FactualSnapshotDelta{}, err
	}
	if err := classifyFactualDerived(ctx, previousFacts.derived, previousInvalidations, &delta, outputInvalidations); err != nil {
		return FactualSnapshotDelta{}, err
	}

	delta.Invalidations = flattenFactualInvalidations(outputInvalidations)
	if err := factualIncrementalContext(ctx); err != nil {
		return FactualSnapshotDelta{}, err
	}
	if err := delta.Validate(); err != nil {
		return FactualSnapshotDelta{}, ErrInvalidFactualIncremental
	}
	return delta.Clone(), nil
}

func factualIncrementalContext(ctx context.Context) error {
	if ctx == nil {
		return ErrInvalidFactualIncremental
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func prepareFactualIncrementalRevision(ctx context.Context, revision FactualSnapshotRevision) (factualIncrementalRevision, error) {
	if err := factualIncrementalContext(ctx); err != nil {
		return factualIncrementalRevision{}, err
	}
	prepared, err := PrepareFactualSnapshot(revision.Snapshot)
	if err != nil {
		return factualIncrementalRevision{}, ErrInvalidFactualIncremental
	}
	if err := factualIncrementalContext(ctx); err != nil {
		return factualIncrementalRevision{}, err
	}

	result := factualIncrementalRevision{
		scope:               prepared.Scope,
		configurationID:     revision.ConfigurationID,
		artifactsByPath:     make(map[string]contract.Artifact, len(revision.Artifacts)),
		manifestsByProducer: make(map[string]fact.FrontendManifest, len(prepared.FrontendManifests)),
		manifestsByFamily:   make(map[string][]fact.FrontendManifest),
		rulesByKey:          make(map[string]factualIncrementalRule, len(prepared.RuleVersions)),
	}
	seenArtifactIDs := make(map[string]struct{}, len(revision.Artifacts))
	for _, artifact := range revision.Artifacts {
		if err := factualIncrementalContext(ctx); err != nil {
			return factualIncrementalRevision{}, err
		}
		if err := artifact.Validate(); err != nil || artifact.SourceID != prepared.Scope.SourceID {
			return factualIncrementalRevision{}, ErrInvalidFactualIncremental
		}
		if _, exists := seenArtifactIDs[artifact.ID]; exists {
			return factualIncrementalRevision{}, ErrInvalidFactualIncremental
		}
		if _, exists := result.artifactsByPath[artifact.Path]; exists {
			return factualIncrementalRevision{}, ErrInvalidFactualIncremental
		}
		seenArtifactIDs[artifact.ID] = struct{}{}
		result.artifactsByPath[artifact.Path] = artifact
	}

	for _, manifest := range prepared.FrontendManifests {
		if err := factualIncrementalContext(ctx); err != nil {
			return factualIncrementalRevision{}, err
		}
		canonical, err := fact.CanonicalFrontendManifest(manifest.Manifest)
		if err != nil {
			return factualIncrementalRevision{}, ErrInvalidFactualIncremental
		}
		producerKey := factualIncrementalProducerKey(canonical.ID, canonical.Version, canonical.Method)
		familyKey := factualIncrementalProducerFamilyKey(canonical.ID, canonical.Method)
		result.manifestsByProducer[producerKey] = canonical
		result.manifestsByFamily[familyKey] = append(result.manifestsByFamily[familyKey], canonical)
	}
	for familyKey := range result.manifestsByFamily {
		sort.Slice(result.manifestsByFamily[familyKey], func(left, right int) bool {
			return result.manifestsByFamily[familyKey][left].Version < result.manifestsByFamily[familyKey][right].Version
		})
	}

	for _, rule := range prepared.RuleVersions {
		if err := factualIncrementalContext(ctx); err != nil {
			return factualIncrementalRevision{}, err
		}
		configuration, err := canonicalJSONObject("incremental rule configuration", rule.Configuration)
		if err != nil {
			return factualIncrementalRevision{}, ErrInvalidFactualIncremental
		}
		preparedRule := factualIncrementalRule{
			ruleID:               rule.RuleID,
			version:              rule.Version,
			implementationDigest: rule.ImplementationDigest,
			configuration:        append(json.RawMessage(nil), configuration...),
		}
		result.rulesByKey[factualIncrementalRuleKey(rule.RuleID, rule.Version)] = preparedRule
	}
	return result, nil
}

func buildFactualIncrementalFacts(ctx context.Context, facts []fact.CanonicalFact) (factualIncrementalFacts, error) {
	result := factualIncrementalFacts{
		factsByID:           make(map[string]fact.CanonicalFact, len(facts)),
		observedByStableKey: make(map[string][]fact.CanonicalFact),
	}
	for _, candidate := range facts {
		if err := factualIncrementalContext(ctx); err != nil {
			return factualIncrementalFacts{}, err
		}
		detached := cloneCanonicalFact(candidate)
		if _, exists := result.factsByID[detached.ID]; exists {
			return factualIncrementalFacts{}, ErrInvalidFactualIncremental
		}
		result.factsByID[detached.ID] = detached
		if detached.Lineage == nil {
			result.observed = append(result.observed, detached)
			key, err := factualIncrementalStableKey(detached)
			if err != nil {
				return factualIncrementalFacts{}, ErrInvalidFactualIncremental
			}
			result.observedByStableKey[key] = append(result.observedByStableKey[key], detached)
			continue
		}
		result.derived = append(result.derived, detached)
	}
	sort.Slice(result.observed, func(left, right int) bool { return result.observed[left].ID < result.observed[right].ID })
	sort.Slice(result.derived, func(left, right int) bool { return result.derived[left].ID < result.derived[right].ID })
	for key := range result.observedByStableKey {
		sort.Slice(result.observedByStableKey[key], func(left, right int) bool {
			return result.observedByStableKey[key][left].ID < result.observedByStableKey[key][right].ID
		})
	}
	return result, nil
}

type factualIncrementalFacts struct {
	factsByID           map[string]fact.CanonicalFact
	observed            []fact.CanonicalFact
	derived             []fact.CanonicalFact
	observedByStableKey map[string][]fact.CanonicalFact
}

func factualIncrementalStableKey(candidate fact.CanonicalFact) (string, error) {
	detached := cloneCanonicalFact(candidate)
	detached.Scope.SnapshotID = factualIncrementalNeutralSnapshotID
	detached.ID = ""
	return fact.IdentityDigest(detached)
}

func classifyFactualArtifacts(ctx context.Context, previous, current map[string]contract.Artifact, delta *FactualSnapshotDelta) error {
	paths := make(map[string]struct{}, len(previous)+len(current))
	for path := range previous {
		paths[path] = struct{}{}
	}
	for path := range current {
		paths[path] = struct{}{}
	}
	ordered := make([]string, 0, len(paths))
	for path := range paths {
		ordered = append(ordered, path)
	}
	sort.Strings(ordered)
	for _, path := range ordered {
		if err := factualIncrementalContext(ctx); err != nil {
			return err
		}
		previousArtifact, previousExists := previous[path]
		currentArtifact, currentExists := current[path]
		switch {
		case !previousExists && currentExists:
			delta.ArtifactAddedPaths = append(delta.ArtifactAddedPaths, path)
		case previousExists && !currentExists:
			delta.ArtifactRemovedPaths = append(delta.ArtifactRemovedPaths, path)
		case previousArtifact.Hash != currentArtifact.Hash:
			delta.ArtifactChangedPaths = append(delta.ArtifactChangedPaths, path)
		}
	}
	return nil
}

func classifyFactualObserved(
	ctx context.Context,
	previousRevision factualIncrementalRevision,
	currentRevision factualIncrementalRevision,
	previous factualIncrementalFacts,
	current factualIncrementalFacts,
	delta *FactualSnapshotDelta,
	outputInvalidations factualIncrementalReasons,
	previousInvalidations factualIncrementalReasons,
) error {
	keys := make(map[string]struct{}, len(previous.observedByStableKey)+len(current.observedByStableKey))
	for key := range previous.observedByStableKey {
		keys[key] = struct{}{}
	}
	for key := range current.observedByStableKey {
		keys[key] = struct{}{}
	}
	orderedKeys := make([]string, 0, len(keys))
	for key := range keys {
		orderedKeys = append(orderedKeys, key)
	}
	sort.Strings(orderedKeys)

	for _, key := range orderedKeys {
		if err := factualIncrementalContext(ctx); err != nil {
			return err
		}
		previousCandidates := previous.observedByStableKey[key]
		currentCandidates := current.observedByStableKey[key]
		if len(previousCandidates) == 1 && len(currentCandidates) == 1 {
			previousFact := previousCandidates[0]
			currentFact := currentCandidates[0]
			reasons := factualObservedReasons(previousRevision, currentRevision, previousFact, false)
			for reason := range factualObservedReasons(currentRevision, previousRevision, currentFact, true) {
				reasons[reason] = struct{}{}
			}
			if !factualObservedPathsCompatible(previousRevision, currentRevision, previousFact, currentFact) && len(reasons) == 0 {
				reasons[FactualInvalidationObservedChangedOrMissing] = struct{}{}
			}
			if previousRevision.configurationID != currentRevision.configurationID {
				reasons[FactualInvalidationConfigurationChanged] = struct{}{}
			}
			if len(reasons) == 0 {
				delta.ObservedReused = append(delta.ObservedReused, FactualFactReuse{PreviousFactID: previousFact.ID, CurrentFactID: currentFact.ID})
				continue
			}
			delta.ObservedReprocessIDs = append(delta.ObservedReprocessIDs, currentFact.ID)
			addFactualReasons(outputInvalidations, currentFact.ID, reasons)
			addFactualReasons(previousInvalidations, previousFact.ID, reasons)
			continue
		}

		for _, currentFact := range currentCandidates {
			reasons := factualObservedReasons(currentRevision, previousRevision, currentFact, true)
			if previousRevision.configurationID != currentRevision.configurationID {
				reasons[FactualInvalidationConfigurationChanged] = struct{}{}
			}
			if len(reasons) == 0 {
				reasons[FactualInvalidationObservedChangedOrMissing] = struct{}{}
			}
			delta.ObservedReprocessIDs = append(delta.ObservedReprocessIDs, currentFact.ID)
			addFactualReasons(outputInvalidations, currentFact.ID, reasons)
		}
		for _, previousFact := range previousCandidates {
			reasons := factualObservedReasons(previousRevision, currentRevision, previousFact, false)
			if previousRevision.configurationID != currentRevision.configurationID {
				reasons[FactualInvalidationConfigurationChanged] = struct{}{}
			}
			if len(reasons) == 0 {
				reasons[FactualInvalidationObservedChangedOrMissing] = struct{}{}
			}
			delta.ObservedRemovedIDs = append(delta.ObservedRemovedIDs, previousFact.ID)
			addFactualReasons(outputInvalidations, previousFact.ID, reasons)
			addFactualReasons(previousInvalidations, previousFact.ID, reasons)
		}
	}

	sort.Slice(delta.ObservedReused, func(left, right int) bool {
		if delta.ObservedReused[left].PreviousFactID != delta.ObservedReused[right].PreviousFactID {
			return delta.ObservedReused[left].PreviousFactID < delta.ObservedReused[right].PreviousFactID
		}
		return delta.ObservedReused[left].CurrentFactID < delta.ObservedReused[right].CurrentFactID
	})
	sort.Strings(delta.ObservedReprocessIDs)
	sort.Strings(delta.ObservedRemovedIDs)
	return nil
}

func factualObservedReasons(
	candidateRevision factualIncrementalRevision,
	otherRevision factualIncrementalRevision,
	candidate fact.CanonicalFact,
	candidateIsCurrent bool,
) factualIncrementalReasonsForFact {
	reasons := make(factualIncrementalReasonsForFact)
	for _, reason := range factualArtifactReasons(candidateRevision, otherRevision, candidate, candidateIsCurrent) {
		reasons[reason] = struct{}{}
	}
	for _, reason := range factualManifestReasons(otherRevision, candidateRevision, candidate.Producer) {
		reasons[reason] = struct{}{}
	}
	return reasons
}

type factualIncrementalReasonsForFact map[FactualInvalidationReason]struct{}

func factualArtifactReasons(
	candidateRevision factualIncrementalRevision,
	otherRevision factualIncrementalRevision,
	candidate fact.CanonicalFact,
	candidateIsCurrent bool,
) []FactualInvalidationReason {
	paths := factualObservedPaths(candidate)
	reasons := make(map[FactualInvalidationReason]struct{})
	for _, path := range paths {
		candidateArtifact, candidateExists := candidateRevision.artifactsByPath[path]
		otherArtifact, otherExists := otherRevision.artifactsByPath[path]
		switch {
		case candidateExists && otherExists && candidateArtifact.Hash != otherArtifact.Hash:
			reasons[FactualInvalidationArtifactHashChanged] = struct{}{}
		case candidateExists != otherExists:
			if candidateIsCurrent == candidateExists {
				reasons[FactualInvalidationArtifactAdded] = struct{}{}
			} else {
				reasons[FactualInvalidationArtifactRemoved] = struct{}{}
			}
		}
	}
	return sortedFactualReasons(reasons)
}

func factualObservedPaths(candidate fact.CanonicalFact) []string {
	paths := make(map[string]struct{}, len(candidate.Evidence))
	for _, evidence := range candidate.Evidence {
		path := evidence.Locator.Path
		if strings.TrimSpace(path) != "" {
			paths[path] = struct{}{}
		}
	}
	result := make([]string, 0, len(paths))
	for path := range paths {
		result = append(result, path)
	}
	sort.Strings(result)
	return result
}

// factualObservedPathsCompatible is deliberately stricter than the reason
// classifier: an observed fact is reusable only when it carries at least one
// source path and every path mentioned by either side exists in both artifact
// inventories with the same hash. Evidence is support metadata and is not
// part of the stable fact key, so both sides must be checked explicitly.
func factualObservedPathsCompatible(
	previousRevision factualIncrementalRevision,
	currentRevision factualIncrementalRevision,
	previousFact fact.CanonicalFact,
	currentFact fact.CanonicalFact,
) bool {
	paths := make(map[string]struct{})
	for _, path := range factualObservedPaths(previousFact) {
		paths[path] = struct{}{}
	}
	for _, path := range factualObservedPaths(currentFact) {
		paths[path] = struct{}{}
	}
	if len(paths) == 0 {
		return false
	}
	for path := range paths {
		previousArtifact, previousExists := previousRevision.artifactsByPath[path]
		currentArtifact, currentExists := currentRevision.artifactsByPath[path]
		if !previousExists || !currentExists || previousArtifact.Hash != currentArtifact.Hash {
			return false
		}
	}
	return true
}

func factualManifestReasons(
	previousRevision factualIncrementalRevision,
	currentRevision factualIncrementalRevision,
	producer fact.Producer,
) []FactualInvalidationReason {
	previousManifest, previousExists := previousRevision.manifestsByProducer[factualIncrementalProducerKey(producer.ID, producer.Version, producer.Method)]
	currentManifest, currentExists := currentRevision.manifestsByProducer[factualIncrementalProducerKey(producer.ID, producer.Version, producer.Method)]
	if previousExists && currentExists {
		reasons := make([]FactualInvalidationReason, 0, 2)
		previousBase, previousSchemas := factualManifestBaseAndSchemas(previousManifest)
		currentBase, currentSchemas := factualManifestBaseAndSchemas(currentManifest)
		if previousBase != currentBase {
			reasons = append(reasons, FactualInvalidationFrontendManifestChanged)
		}
		if previousSchemas != currentSchemas {
			reasons = append(reasons, FactualInvalidationSchemaDigestChanged)
		}
		return sortedFactualReasonsSet(reasons)
	}

	familyKey := factualIncrementalProducerFamilyKey(producer.ID, producer.Method)
	if previousCandidates, exists := previousRevision.manifestsByFamily[familyKey]; exists {
		for _, manifest := range previousCandidates {
			if manifest.Version != producer.Version {
				return []FactualInvalidationReason{FactualInvalidationFrontendVersionChanged}
			}
		}
	}
	if currentCandidates, exists := currentRevision.manifestsByFamily[familyKey]; exists {
		for _, manifest := range currentCandidates {
			if manifest.Version != producer.Version {
				return []FactualInvalidationReason{FactualInvalidationFrontendVersionChanged}
			}
		}
	}
	return []FactualInvalidationReason{FactualInvalidationFrontendManifestChanged}
}

func factualManifestBaseAndSchemas(manifest fact.FrontendManifest) (string, string) {
	base := manifest
	base.Extensions = nil
	baseBytes, err := fact.CanonicalFrontendManifestBytes(base)
	if err != nil {
		return "", ""
	}
	schemas := make([]string, 0, len(manifest.Extensions))
	for _, schema := range manifest.Extensions {
		schemas = append(schemas, schema.ID+"\x00"+schema.Version+"\x00"+schema.Digest)
	}
	sort.Strings(schemas)
	return string(baseBytes), strings.Join(schemas, "\x00")
}

func factualIncrementalDependents(ctx context.Context, derived []fact.CanonicalFact) (map[string][]string, error) {
	dependents := make(map[string][]string)
	for _, candidate := range derived {
		if err := factualIncrementalContext(ctx); err != nil {
			return nil, err
		}
		if candidate.Lineage == nil {
			return nil, ErrInvalidFactualIncremental
		}
		for _, inputFactID := range candidate.Lineage.InputFactIDs {
			dependents[inputFactID] = append(dependents[inputFactID], candidate.ID)
		}
	}
	for inputFactID := range dependents {
		sort.Strings(dependents[inputFactID])
	}
	return dependents, nil
}

func seedFactualDerivedInvalidations(
	ctx context.Context,
	previousRevision factualIncrementalRevision,
	currentRevision factualIncrementalRevision,
	derived []fact.CanonicalFact,
	invalidations factualIncrementalReasons,
) error {
	for _, candidate := range derived {
		if err := factualIncrementalContext(ctx); err != nil {
			return err
		}
		if candidate.Lineage == nil {
			return ErrInvalidFactualIncremental
		}
		reasons := make(factualIncrementalReasonsForFact)
		if previousRevision.configurationID != currentRevision.configurationID {
			reasons[FactualInvalidationConfigurationChanged] = struct{}{}
		}
		key := factualIncrementalRuleKey(candidate.Lineage.RuleID, candidate.Lineage.RuleVersion)
		previousRule, previousExists := previousRevision.rulesByKey[key]
		currentRule, currentExists := currentRevision.rulesByKey[key]
		if !previousExists || !currentExists {
			reasons[FactualInvalidationRuleVersionChanged] = struct{}{}
		} else if previousRule.implementationDigest != currentRule.implementationDigest || !bytes.Equal(previousRule.configuration, currentRule.configuration) {
			reasons[FactualInvalidationRuleImplementationOrConfigurationChanged] = struct{}{}
		}
		if len(reasons) > 0 {
			addFactualReasons(invalidations, candidate.ID, reasons)
		}
	}
	return nil
}

func propagateFactualInvalidations(ctx context.Context, dependents map[string][]string, invalidations factualIncrementalReasons) error {
	queue := make([]string, 0, len(invalidations))
	for factID := range invalidations {
		queue = append(queue, factID)
	}
	sort.Strings(queue)
	enqueued := make(map[string]struct{}, len(queue))
	for _, factID := range queue {
		enqueued[factID] = struct{}{}
	}
	for len(queue) > 0 {
		if err := factualIncrementalContext(ctx); err != nil {
			return err
		}
		factID := queue[0]
		queue = queue[1:]
		for _, dependentID := range dependents[factID] {
			addFactualReasons(invalidations, dependentID, factualIncrementalReasonsForFact{
				FactualInvalidationUpstreamLineage: struct{}{},
			})
			if _, exists := enqueued[dependentID]; !exists {
				enqueued[dependentID] = struct{}{}
				queue = append(queue, dependentID)
				sort.Strings(queue)
			}
		}
	}
	return nil
}

func classifyFactualDerived(
	ctx context.Context,
	derived []fact.CanonicalFact,
	invalidations factualIncrementalReasons,
	delta *FactualSnapshotDelta,
	outputInvalidations factualIncrementalReasons,
) error {
	for _, candidate := range derived {
		if err := factualIncrementalContext(ctx); err != nil {
			return err
		}
		reasons, invalid := invalidations[candidate.ID]
		if !invalid || len(reasons) == 0 {
			delta.DerivedReusableIDs = append(delta.DerivedReusableIDs, candidate.ID)
			continue
		}
		delta.DerivedReevaluateIDs = append(delta.DerivedReevaluateIDs, candidate.ID)
		addFactualReasons(outputInvalidations, candidate.ID, factualIncrementalReasonsForFact(reasons))
	}
	sort.Strings(delta.DerivedReusableIDs)
	sort.Strings(delta.DerivedReevaluateIDs)
	delta.FanoutReevaluated = len(delta.DerivedReevaluateIDs)
	return nil
}

func addFactualReasons(target factualIncrementalReasons, factID string, reasons factualIncrementalReasonsForFact) {
	if len(reasons) == 0 {
		return
	}
	if target[factID] == nil {
		target[factID] = make(map[FactualInvalidationReason]struct{}, len(reasons))
	}
	for reason := range reasons {
		target[factID][reason] = struct{}{}
	}
}

func flattenFactualInvalidations(reasons factualIncrementalReasons) []FactualInvalidation {
	factIDs := make([]string, 0, len(reasons))
	for factID := range reasons {
		factIDs = append(factIDs, factID)
	}
	sort.Strings(factIDs)
	result := make([]FactualInvalidation, 0, len(factIDs))
	for _, factID := range factIDs {
		result = append(result, FactualInvalidation{FactID: factID, Reasons: sortedFactualReasons(reasons[factID])})
	}
	return result
}

func sortedFactualReasons(reasons map[FactualInvalidationReason]struct{}) []FactualInvalidationReason {
	result := make([]FactualInvalidationReason, 0, len(reasons))
	for reason := range reasons {
		result = append(result, reason)
	}
	sort.Slice(result, func(left, right int) bool { return result[left] < result[right] })
	return result
}

func sortedFactualReasonsSet(reasons []FactualInvalidationReason) []FactualInvalidationReason {
	set := make(map[FactualInvalidationReason]struct{}, len(reasons))
	for _, reason := range reasons {
		set[reason] = struct{}{}
	}
	return sortedFactualReasons(set)
}

func factualIncrementalProducerKey(id, version, method string) string {
	return id + "\x00" + version + "\x00" + method
}

func factualIncrementalProducerFamilyKey(id, method string) string {
	return id + "\x00" + method
}

func factualIncrementalRuleKey(ruleID, version string) string {
	return ruleID + "\x00" + version
}
