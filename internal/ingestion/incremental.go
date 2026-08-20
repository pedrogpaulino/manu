package ingestion

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/pedrogpaulino/manu/internal/bundle"
	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/evidence"
	"github.com/pedrogpaulino/manu/internal/retrieval"
)

// IncrementalFactKind identifies one factual family compared between two
// immutable snapshots. The values are deliberately stable because reports are
// persisted by later evaluation work.
type IncrementalFactKind string

const (
	IncrementalFactArtifact    IncrementalFactKind = "artifact"
	IncrementalFactObservation IncrementalFactKind = "observation"
	IncrementalFactEvidence    IncrementalFactKind = "evidence"
	IncrementalFactCoverage    IncrementalFactKind = "coverage"
	IncrementalFactGap         IncrementalFactKind = "gap"
	IncrementalFactFailure     IncrementalFactKind = "failure"
)

// IncrementalProjection identifies a rebuildable projection affected by a
// factual change.
type IncrementalProjection string

const (
	IncrementalProjectionTextual    IncrementalProjection = "textual"
	IncrementalProjectionRelational IncrementalProjection = "relational"
	IncrementalProjectionEmbedding  IncrementalProjection = "embedding"
)

var (
	// ErrIncrementalInvalid identifies a malformed comparison input. It never
	// contains bundle content or a driver/provider diagnostic.
	ErrIncrementalInvalid = errors.New("ingestion: invalid incremental comparison")
	// ErrIncrementalScopeMismatch identifies snapshots from different
	// organization/source scopes.
	ErrIncrementalScopeMismatch = errors.New("ingestion: incremental snapshot scope mismatch")
	// ErrIncrementalSnapshotImmutable identifies an attempt to compare an
	// update using the same snapshot identity.
	ErrIncrementalSnapshotImmutable = errors.New("ingestion: snapshot identity is immutable")
	// ErrIncrementalAmbiguousIdentity identifies a stable key that maps to more
	// than one fact in either snapshot. Ambiguous facts are never reused.
	ErrIncrementalAmbiguousIdentity = errors.New("ingestion: ambiguous incremental identity")
	// ErrIncrementalProfileMismatch identifies an invalid embedding profile
	// comparison. A changed profile is represented in the report, whereas a
	// malformed profile is rejected before any work starts.
	ErrIncrementalProfileMismatch = errors.New("ingestion: embedding profile mismatch")
)

// IncrementalOptions controls compatibility of derived work. A nil profile
// means that the comparison does not claim vector reuse. When only
// EmbeddingProfile is supplied, it is treated as the profile on both sides.
// Supplying both side-specific profiles makes a profile change explicit and
// prevents vectors from different spaces from being marked reusable.
type IncrementalOptions struct {
	EmbeddingProfile         *retrieval.EmbeddingProfile
	PreviousEmbeddingProfile *retrieval.EmbeddingProfile
	CurrentEmbeddingProfile  *retrieval.EmbeddingProfile
}

// FactReference is a safe, content-free reference to one factual item in a
// delta report. Digests are hashes, not source content. PreviousID and
// CurrentID are external contract identities; canonical relational IDs stay
// in persistence adapters.
type FactReference struct {
	Kind           IncrementalFactKind `json:"kind"`
	Key            string              `json:"key"`
	PreviousID     string              `json:"previous_id,omitempty"`
	CurrentID      string              `json:"current_id,omitempty"`
	PreviousDigest string              `json:"previous_digest,omitempty"`
	CurrentDigest  string              `json:"current_digest,omitempty"`
}

// ProjectionImpact records only the derived identities that need work for a
// new snapshot. A key can be impacted even when its factual item is reused:
// for example, an unchanged Evidence Unit receives a new snapshot-scoped
// canonical ID after its parent artifact changed. Removed contains old
// stable keys that must not be copied into the new projection.
type ProjectionImpact struct {
	Projection IncrementalProjection `json:"projection"`
	ProfileID  string                `json:"profile_id,omitempty"`
	Affected   []string              `json:"affected,omitempty"`
	Removed    []string              `json:"removed,omitempty"`
	Reusable   []string              `json:"reusable,omitempty"`
}

// WorkSaved is an explicit estimate of work avoided by a localized update.
// It is based only on deterministic item/impact counts and is suitable for a
// later evaluation report; it is not a timing or cost promise.
type WorkSaved struct {
	Facts      int `json:"facts"`
	Textual    int `json:"textual"`
	Relational int `json:"relational"`
	Embeddings int `json:"embeddings"`
}

// Total returns the number of reusable factual and derived items represented
// by this report.
func (w WorkSaved) Total() int { return w.Facts + w.Textual + w.Relational + w.Embeddings }

func (w WorkSaved) validate() error {
	if w.Facts < 0 || w.Textual < 0 || w.Relational < 0 || w.Embeddings < 0 {
		return ErrIncrementalInvalid
	}
	return nil
}

// IncrementalReport is the deterministic result of comparing two immutable
// snapshots from one organization/source. No raw source text is retained.
type IncrementalReport struct {
	OrganizationID             string             `json:"organization_id"`
	SourceID                   string             `json:"source_id"`
	PreviousSnapshotID         string             `json:"previous_snapshot_id"`
	CurrentSnapshotID          string             `json:"current_snapshot_id"`
	PreviousFactualDigest      string             `json:"previous_factual_digest"`
	CurrentFactualDigest       string             `json:"current_factual_digest"`
	ConfigurationCompatible    bool               `json:"configuration_compatible"`
	EmbeddingProfileCompatible bool               `json:"embedding_profile_compatible"`
	EmbeddingProfileConfigured bool               `json:"embedding_profile_configured"`
	Reused                     []FactReference    `json:"reused,omitempty"`
	Changed                    []FactReference    `json:"changed,omitempty"`
	Added                      []FactReference    `json:"added,omitempty"`
	Removed                    []FactReference    `json:"removed,omitempty"`
	Impacts                    []ProjectionImpact `json:"impacts,omitempty"`
	WorkSaved                  WorkSaved          `json:"work_saved"`
}

// IncrementalDelta is a readable alias for IncrementalReport.
type IncrementalDelta = IncrementalReport

// IncrementalFactKeys returns the stable, content-free comparison key for
// each external factual ID in a validated bundle. Persistence adapters use
// this same map for factual_identities so a compatible fact keeps one logical
// identity key across snapshot-scoped rows.
func IncrementalFactKeys(ctx context.Context, input bundle.Bundle) (map[IncrementalFactKind]map[string]string, error) {
	if err := incrementalContext(ctx); err != nil {
		return nil, err
	}
	if err := validateIncrementalBundle(input); err != nil {
		return nil, err
	}
	facts, err := bundleFacts(ctx, input)
	if err != nil {
		return nil, err
	}
	keys := make(map[IncrementalFactKind]map[string]string, len(facts))
	for kind, values := range facts {
		byID := make(map[string]string, len(values))
		counts := make(map[string]int, len(values))
		for _, fact := range values {
			counts[fact.key]++
		}
		for _, fact := range values {
			key := fact.key
			if counts[fact.key] > 1 {
				key = ambiguousFactKey(fact.key, fact.id)
			}
			byID[fact.id] = key
		}
		keys[IncrementalFactKind(kind)] = byID
	}
	return keys, nil
}

// CompareBundles compares two validated bundles without writing either one.
// It requires equal organization/source external identities and distinct
// snapshot identities. Facts are matched by stable provenance keys rather
// than snapshot-scoped IDs, so changed content is classified as changed when
// its stable location remains compatible. Ambiguous keys are conservatively
// treated as added/removed and are never reused.
func CompareBundles(ctx context.Context, previous, current bundle.Bundle, options ...IncrementalOptions) (IncrementalReport, error) {
	if err := incrementalContext(ctx); err != nil {
		return IncrementalReport{}, err
	}
	if len(options) > 1 {
		return IncrementalReport{}, ErrIncrementalInvalid
	}
	if err := validateIncrementalBundle(previous); err != nil {
		return IncrementalReport{}, err
	}
	if err := validateIncrementalBundle(current); err != nil {
		return IncrementalReport{}, err
	}
	if err := incrementalContext(ctx); err != nil {
		return IncrementalReport{}, err
	}
	previousManifest, currentManifest := previous.Manifest, current.Manifest
	if previousManifest.Organization.ID != currentManifest.Organization.ID ||
		previousManifest.Source.ID != currentManifest.Source.ID ||
		previousManifest.Snapshot.SourceID != previousManifest.Source.ID ||
		currentManifest.Snapshot.SourceID != currentManifest.Source.ID {
		return IncrementalReport{}, ErrIncrementalScopeMismatch
	}
	if previousManifest.Snapshot.ID == currentManifest.Snapshot.ID {
		return IncrementalReport{}, ErrIncrementalSnapshotImmutable
	}

	profileState, err := normalizeIncrementalProfiles(options)
	if err != nil {
		return IncrementalReport{}, err
	}
	previousFacts, err := bundleFacts(ctx, previous)
	if err != nil {
		return IncrementalReport{}, err
	}
	currentFacts, err := bundleFacts(ctx, current)
	if err != nil {
		return IncrementalReport{}, err
	}

	report := IncrementalReport{
		OrganizationID:             previousManifest.Organization.ID,
		SourceID:                   previousManifest.Source.ID,
		PreviousSnapshotID:         previousManifest.Snapshot.ID,
		CurrentSnapshotID:          currentManifest.Snapshot.ID,
		PreviousFactualDigest:      previousManifest.FactualDigest,
		CurrentFactualDigest:       currentManifest.FactualDigest,
		ConfigurationCompatible:    previousManifest.Analysis.ConfigurationID == currentManifest.Analysis.ConfigurationID,
		EmbeddingProfileCompatible: profileState.compatible,
		EmbeddingProfileConfigured: profileState.configured,
	}
	if !report.ConfigurationCompatible {
		// A configuration can change analyzer semantics even when a particular
		// serialized value happens to be equal. Reuse is therefore blocked for
		// every factual family, not inferred from coincidental digests.
		previousFacts, currentFacts = markConfigurationChange(previousFacts, currentFacts)
	}
	var compareErr error
	report.Reused, report.Changed, report.Added, report.Removed, compareErr = compareFactSets(ctx, previousFacts, currentFacts, report.ConfigurationCompatible)
	if compareErr != nil {
		return IncrementalReport{}, compareErr
	}
	report.Impacts, compareErr = deriveProjectionImpacts(ctx, previousFacts, currentFacts, report, profileState)
	if compareErr != nil {
		return IncrementalReport{}, compareErr
	}
	report.WorkSaved = calculateWorkSaved(report, previousFacts, currentFacts, profileState)
	sortIncrementalReport(&report)
	if err := report.Validate(); err != nil {
		return IncrementalReport{}, err
	}
	return report, nil
}

// CompareSnapshots is the domain-oriented spelling of CompareBundles.
func CompareSnapshots(ctx context.Context, previous, current bundle.Bundle, options ...IncrementalOptions) (IncrementalReport, error) {
	return CompareBundles(ctx, previous, current, options...)
}

// Validate checks report invariants and ensures the report remains a safe,
// deterministic transport value. It intentionally does not validate bundle
// content because reports carry only identities and hashes.
func (r IncrementalReport) Validate() error {
	if strings.TrimSpace(r.OrganizationID) == "" || strings.TrimSpace(r.SourceID) == "" ||
		strings.TrimSpace(r.PreviousSnapshotID) == "" || strings.TrimSpace(r.CurrentSnapshotID) == "" ||
		r.PreviousSnapshotID == r.CurrentSnapshotID {
		return ErrIncrementalInvalid
	}
	for _, digest := range []string{r.PreviousFactualDigest, r.CurrentFactualDigest} {
		if !isIncrementalDigest(digest) {
			return ErrIncrementalInvalid
		}
	}
	if err := r.WorkSaved.validate(); err != nil {
		return err
	}
	seen := make(map[string]string)
	for _, group := range []struct {
		name string
		refs []FactReference
	}{
		{name: "reused", refs: r.Reused}, {name: "changed", refs: r.Changed},
		{name: "added", refs: r.Added}, {name: "removed", refs: r.Removed},
	} {
		for _, ref := range group.refs {
			if ref.Kind == "" || strings.TrimSpace(ref.Key) == "" {
				return ErrIncrementalInvalid
			}
			identity := string(ref.Kind) + "\x00" + ref.Key
			if previous, exists := seen[identity]; exists {
				return fmt.Errorf("%w: fact appears in %s and %s", ErrIncrementalInvalid, previous, group.name)
			}
			seen[identity] = group.name
			for _, digest := range []string{ref.PreviousDigest, ref.CurrentDigest} {
				if digest != "" && !isIncrementalDigest(digest) {
					return ErrIncrementalInvalid
				}
			}
		}
	}
	return nil
}

type incrementalProfileState struct {
	compatible bool
	configured bool
	profileID  string
}

func normalizeIncrementalProfiles(options []IncrementalOptions) (incrementalProfileState, error) {
	if len(options) == 0 {
		return incrementalProfileState{}, nil
	}
	option := options[0]
	previous := option.PreviousEmbeddingProfile
	current := option.CurrentEmbeddingProfile
	if option.EmbeddingProfile != nil {
		if previous != nil || current != nil {
			return incrementalProfileState{}, ErrIncrementalProfileMismatch
		}
		previous, current = option.EmbeddingProfile, option.EmbeddingProfile
	}
	if previous == nil && current == nil {
		return incrementalProfileState{}, nil
	}
	if previous == nil || current == nil {
		profile := previous
		if profile == nil {
			profile = current
		}
		normalized, err := profile.Normalize()
		if err != nil {
			return incrementalProfileState{}, ErrIncrementalProfileMismatch
		}
		return incrementalProfileState{configured: true, profileID: normalized.ID}, nil
	}
	left, err := previous.Normalize()
	if err != nil {
		return incrementalProfileState{}, ErrIncrementalProfileMismatch
	}
	right, err := current.Normalize()
	if err != nil {
		return incrementalProfileState{}, ErrIncrementalProfileMismatch
	}
	return incrementalProfileState{
		compatible: sameIncrementalProfile(left, right), configured: true, profileID: right.ID,
	}, nil
}

func sameIncrementalProfile(left, right retrieval.EmbeddingProfile) bool {
	return left.ID == right.ID && left.OrganizationID == right.OrganizationID &&
		left.Provider == right.Provider && left.Model == right.Model &&
		left.Dimension == right.Dimension && left.Normalization == right.Normalization &&
		left.ConfigurationVersion == right.ConfigurationVersion &&
		left.ConfigurationDigest == right.ConfigurationDigest &&
		string(left.Configuration) == string(right.Configuration)
}

type incrementalFact struct {
	kind         IncrementalFactKind
	key          string
	id           string
	digest       string
	dependencies []string
	projections  []IncrementalProjection
}

func validateIncrementalBundle(input bundle.Bundle) error {
	if err := input.Validate(); err != nil {
		return ErrIncrementalInvalid
	}
	if strings.TrimSpace(input.Manifest.Organization.ID) == "" || strings.TrimSpace(input.Manifest.Source.ID) == "" || strings.TrimSpace(input.Manifest.Snapshot.ID) == "" {
		return ErrIncrementalInvalid
	}
	return nil
}

func bundleFacts(ctx context.Context, input bundle.Bundle) (map[string][]incrementalFact, error) {
	facts := make(map[string][]incrementalFact)
	artifacts := make(map[string]string, len(input.Artifacts))
	for _, artifact := range input.Artifacts {
		if err := incrementalContext(ctx); err != nil {
			return nil, err
		}
		key := stableArtifactKey(artifact)
		artifactDigest, err := incrementalDigest(struct {
			Path string `json:"path"`
			Type string `json:"type"`
			Hash string `json:"hash"`
			Size int64  `json:"size"`
			Kind string `json:"kind,omitempty"`
		}{artifact.Path, artifact.Type, artifact.Hash, artifact.Size, artifact.Kind})
		if err != nil {
			return nil, ErrIncrementalInvalid
		}
		facts[string(IncrementalFactArtifact)] = append(facts[string(IncrementalFactArtifact)], incrementalFact{
			kind: IncrementalFactArtifact, key: key, id: artifact.ID, digest: artifactDigest,
			projections: []IncrementalProjection{IncrementalProjectionRelational},
		})
		artifacts[artifact.ID] = key
	}
	contributions := make(map[string]string, len(input.Contributions))
	for _, contribution := range input.Contributions {
		if err := incrementalContext(ctx); err != nil {
			return nil, err
		}
		artifactKey := artifacts[contribution.ArtifactID]
		key := stableContributionKey(contribution, artifactKey)
		contributionDigest, err := contributionFactDigest(contribution, artifactKey)
		if err != nil {
			return nil, ErrIncrementalInvalid
		}
		facts[string(IncrementalFactObservation)] = append(facts[string(IncrementalFactObservation)], incrementalFact{
			kind: IncrementalFactObservation, key: key, id: contribution.ID, digest: contributionDigest,
			dependencies: []string{factMapKey(IncrementalFactArtifact, artifactKey)},
			projections:  []IncrementalProjection{IncrementalProjectionRelational},
		})
		contributions[contribution.ID] = key
	}
	for _, unit := range input.Evidence {
		if err := incrementalContext(ctx); err != nil {
			return nil, err
		}
		artifactKey := artifacts[unit.ArtifactID]
		contributionKey := contributions[unit.Contribution.ID]
		key := stableEvidenceKey(unit, artifactKey, contributionKey)
		digest, err := evidenceFactDigest(unit, artifactKey, contributionKey)
		if err != nil {
			return nil, ErrIncrementalInvalid
		}
		facts[string(IncrementalFactEvidence)] = append(facts[string(IncrementalFactEvidence)], incrementalFact{
			kind: IncrementalFactEvidence, key: key, id: unit.ID, digest: digest,
			dependencies: []string{
				factMapKey(IncrementalFactArtifact, artifactKey),
				factMapKey(IncrementalFactObservation, contributionKey),
			},
			projections: []IncrementalProjection{IncrementalProjectionTextual, IncrementalProjectionEmbedding},
		})
	}
	for _, coverage := range input.Manifest.Coverage {
		if err := incrementalContext(ctx); err != nil {
			return nil, err
		}
		key := stableCoverageKey(coverage)
		digest, err := incrementalDigest(struct {
			Dimension  string                 `json:"dimension"`
			Scope      string                 `json:"scope,omitempty"`
			State      contract.CoverageState `json:"state"`
			AnalyzerID string                 `json:"analyzer_id,omitempty"`
			Message    string                 `json:"message,omitempty"`
			Locator    *contract.Locator      `json:"locator,omitempty"`
		}{coverage.Dimension, coverage.Scope, coverage.State, coverage.AnalyzerID, coverage.Message, coverage.Locator})
		if err != nil {
			return nil, ErrIncrementalInvalid
		}
		facts[string(IncrementalFactCoverage)] = append(facts[string(IncrementalFactCoverage)], incrementalFact{kind: IncrementalFactCoverage, key: key, id: coverage.ID, digest: digest})
	}
	for _, gap := range input.Manifest.Gaps {
		if err := incrementalContext(ctx); err != nil {
			return nil, err
		}
		key := stableGapKey(gap)
		digest, err := incrementalDigest(struct {
			Code       string            `json:"code"`
			Dimension  string            `json:"dimension,omitempty"`
			Scope      string            `json:"scope,omitempty"`
			Message    string            `json:"message"`
			AnalyzerID string            `json:"analyzer_id,omitempty"`
			Locator    *contract.Locator `json:"locator,omitempty"`
		}{gap.Code, gap.Dimension, gap.Scope, gap.Message, gap.AnalyzerID, gap.Locator})
		if err != nil {
			return nil, ErrIncrementalInvalid
		}
		facts[string(IncrementalFactGap)] = append(facts[string(IncrementalFactGap)], incrementalFact{kind: IncrementalFactGap, key: key, id: gap.ID, digest: digest})
	}
	for _, failure := range input.Manifest.Failures {
		if err := incrementalContext(ctx); err != nil {
			return nil, err
		}
		artifactKey := artifacts[failure.ArtifactID]
		key := stableFailureKey(failure, artifactKey)
		digest, err := incrementalDigest(struct {
			Code        string            `json:"code"`
			Operation   string            `json:"operation"`
			ArtifactKey string            `json:"artifact_key,omitempty"`
			AnalyzerID  string            `json:"analyzer_id,omitempty"`
			Message     string            `json:"message"`
			Partial     bool              `json:"partial,omitempty"`
			Locator     *contract.Locator `json:"locator,omitempty"`
		}{failure.Code, failure.Operation, artifactKey, failure.AnalyzerID, failure.Message, failure.Partial, failure.Locator})
		if err != nil {
			return nil, ErrIncrementalInvalid
		}
		facts[string(IncrementalFactFailure)] = append(facts[string(IncrementalFactFailure)], incrementalFact{
			kind: IncrementalFactFailure, key: key, id: failure.ID, digest: digest,
			dependencies: []string{factMapKey(IncrementalFactArtifact, artifactKey)},
		})
	}
	for _, values := range facts {
		sort.Slice(values, func(i, j int) bool {
			if values[i].key != values[j].key {
				return values[i].key < values[j].key
			}
			return values[i].id < values[j].id
		})
	}
	return facts, nil
}

func markConfigurationChange(previous, current map[string][]incrementalFact) (map[string][]incrementalFact, map[string][]incrementalFact) {
	// Keep the original facts; the compatibility flag passed to comparison is
	// false and therefore prevents all factual reuse. This helper documents the
	// intentional policy boundary and leaves dependency identities intact.
	return previous, current
}

func compareFactSets(ctx context.Context, previous, current map[string][]incrementalFact, allowReuse bool) ([]FactReference, []FactReference, []FactReference, []FactReference, error) {
	reused := make([]FactReference, 0)
	changed := make([]FactReference, 0)
	added := make([]FactReference, 0)
	removed := make([]FactReference, 0)
	kinds := []IncrementalFactKind{IncrementalFactArtifact, IncrementalFactObservation, IncrementalFactEvidence, IncrementalFactCoverage, IncrementalFactGap, IncrementalFactFailure}
	for _, kind := range kinds {
		if err := incrementalContext(ctx); err != nil {
			return reused, changed, added, removed, err
		}
		left, leftAmbiguous := uniqueFacts(previous[string(kind)])
		right, rightAmbiguous := uniqueFacts(current[string(kind)])
		for _, fact := range leftAmbiguous {
			removed = append(removed, FactReference{
				Kind: kind, Key: ambiguousFactKey(fact.key, fact.id),
				PreviousID: fact.id, PreviousDigest: fact.digest,
			})
		}
		for _, fact := range rightAmbiguous {
			added = append(added, FactReference{
				Kind: kind, Key: ambiguousFactKey(fact.key, fact.id),
				CurrentID: fact.id, CurrentDigest: fact.digest,
			})
		}
		keys := make(map[string]struct{}, len(left)+len(right))
		for key := range left {
			keys[key] = struct{}{}
		}
		for key := range right {
			keys[key] = struct{}{}
		}
		ordered := make([]string, 0, len(keys))
		for key := range keys {
			ordered = append(ordered, key)
		}
		sort.Strings(ordered)
		for _, key := range ordered {
			oldFact, oldOK := left[key]
			newFact, newOK := right[key]
			switch {
			case oldOK && newOK && allowReuse && oldFact.digest == newFact.digest:
				reused = append(reused, FactReference{Kind: kind, Key: key, PreviousID: oldFact.id, CurrentID: newFact.id, PreviousDigest: oldFact.digest, CurrentDigest: newFact.digest})
			case oldOK && newOK:
				changed = append(changed, FactReference{Kind: kind, Key: key, PreviousID: oldFact.id, CurrentID: newFact.id, PreviousDigest: oldFact.digest, CurrentDigest: newFact.digest})
			case newOK:
				added = append(added, FactReference{Kind: kind, Key: key, CurrentID: newFact.id, CurrentDigest: newFact.digest})
			case oldOK:
				removed = append(removed, FactReference{Kind: kind, Key: key, PreviousID: oldFact.id, PreviousDigest: oldFact.digest})
			}
		}
	}
	return reused, changed, added, removed, nil
}

func uniqueFacts(values []incrementalFact) (map[string]incrementalFact, []incrementalFact) {
	result := make(map[string]incrementalFact, len(values))
	counts := make(map[string]int, len(values))
	for _, value := range values {
		counts[value.key]++
	}
	ambiguous := make([]incrementalFact, 0)
	for _, value := range values {
		if counts[value.key] == 1 {
			result[value.key] = value
		} else {
			ambiguous = append(ambiguous, value)
		}
	}
	return result, ambiguous
}

func deriveProjectionImpacts(ctx context.Context, previous, current map[string][]incrementalFact, report IncrementalReport, profile incrementalProfileState) ([]ProjectionImpact, error) {
	previousByKey := flattenIncrementalFacts(previous)
	currentByKey := flattenIncrementalFacts(current)
	changedKeys := make(map[string]struct{})
	removedKeys := make(map[string]struct{})
	for _, ref := range append(append(append([]FactReference{}, report.Changed...), report.Added...), report.Removed...) {
		if ref.Kind == "" {
			continue
		}
		factKey := comparableIncrementalFactKey(ref.Key)
		if ref.CurrentID != "" {
			changedKeys[factMapKey(ref.Kind, factKey)] = struct{}{}
		}
		if ref.PreviousID != "" && ref.CurrentID == "" {
			removedKeys[factMapKey(ref.Kind, factKey)] = struct{}{}
		}
	}
	impacts := map[IncrementalProjection]*ProjectionImpact{
		IncrementalProjectionTextual:    {Projection: IncrementalProjectionTextual},
		IncrementalProjectionRelational: {Projection: IncrementalProjectionRelational},
		IncrementalProjectionEmbedding:  {Projection: IncrementalProjectionEmbedding, ProfileID: profile.profileID},
	}
	for key, fact := range currentByKey {
		if _, changed := changedKeys[key]; changed {
			for _, projection := range fact.projections {
				if projection == IncrementalProjectionEmbedding && !profile.configured {
					continue
				}
				appendUnique(&impacts[projection].Affected, fact.key)
			}
		}
		if hasChangedDependency(fact, changedKeys) {
			for _, projection := range fact.projections {
				if projection == IncrementalProjectionEmbedding && !profile.configured {
					continue
				}
				appendUnique(&impacts[projection].Affected, fact.key)
			}
		}
	}
	if profile.configured && !profile.compatible {
		// A profile change invalidates only the vector space. Textual and
		// relational projections remain compatible with the same facts and
		// should continue to be copied forward when their content is unchanged.
		for _, fact := range currentByKey {
			if fact.kind == IncrementalFactEvidence {
				appendUnique(&impacts[IncrementalProjectionEmbedding].Affected, fact.key)
			}
		}
	}
	for key, fact := range previousByKey {
		if _, removed := removedKeys[key]; removed {
			for _, projection := range fact.projections {
				if projection == IncrementalProjectionEmbedding && !profile.configured {
					continue
				}
				appendUnique(&impacts[projection].Removed, fact.key)
			}
		}
	}
	for _, ref := range report.Reused {
		if err := incrementalContext(ctx); err != nil {
			return nil, err
		}
		fact, exists := currentByKey[factMapKey(ref.Kind, ref.Key)]
		if !exists || hasChangedDependency(fact, changedKeys) {
			continue
		}
		for _, projection := range fact.projections {
			if projection == IncrementalProjectionEmbedding && (!profile.configured || !profile.compatible) {
				continue
			}
			appendUnique(&impacts[projection].Reusable, fact.key)
		}
	}
	for _, impact := range impacts {
		if impact.Projection == IncrementalProjectionEmbedding && (!profile.configured || !profile.compatible) {
			// Existing vectors remain valid for their own profile/snapshot but
			// cannot be reused by this update.
			impact.Reusable = nil
		}
		sort.Strings(impact.Affected)
		sort.Strings(impact.Removed)
		sort.Strings(impact.Reusable)
	}
	result := make([]ProjectionImpact, 0, len(impacts))
	for _, projection := range []IncrementalProjection{IncrementalProjectionTextual, IncrementalProjectionRelational, IncrementalProjectionEmbedding} {
		impact := impacts[projection]
		if len(impact.Affected) == 0 && len(impact.Removed) == 0 && len(impact.Reusable) == 0 {
			continue
		}
		result = append(result, *impact)
	}
	return result, nil
}

func hasChangedDependency(fact incrementalFact, changed map[string]struct{}) bool {
	for _, dependency := range fact.dependencies {
		if _, exists := changed[dependency]; exists {
			return true
		}
	}
	return false
}

func calculateWorkSaved(report IncrementalReport, previous, current map[string][]incrementalFact, profile incrementalProfileState) WorkSaved {
	work := WorkSaved{Facts: len(report.Reused)}
	for _, impact := range report.Impacts {
		reusable := len(impact.Reusable)
		if reusable == 0 {
			// A factual reuse is projection-reusable only when it is not affected
			// by a changed dependency. Evidence is the unit relevant to text and
			// vectors; observations/artifacts represent relational work.
			for _, ref := range report.Reused {
				if ref.Kind == IncrementalFactEvidence && impact.Projection == IncrementalProjectionEmbedding && profile.compatible {
					if !containsString(impact.Affected, ref.Key) {
						reusable++
					}
				} else if ref.Kind == IncrementalFactEvidence && impact.Projection == IncrementalProjectionTextual {
					if !containsString(impact.Affected, ref.Key) {
						reusable++
					}
				} else if (ref.Kind == IncrementalFactObservation || ref.Kind == IncrementalFactArtifact) && impact.Projection == IncrementalProjectionRelational {
					if !containsString(impact.Affected, ref.Key) {
						reusable++
					}
				}
			}
		}
		switch impact.Projection {
		case IncrementalProjectionTextual:
			work.Textual += reusable
		case IncrementalProjectionRelational:
			work.Relational += reusable
		case IncrementalProjectionEmbedding:
			work.Embeddings += reusable
		}
	}
	_ = previous
	_ = current
	return work
}

func flattenIncrementalFacts(values map[string][]incrementalFact) map[string]incrementalFact {
	result := make(map[string]incrementalFact)
	for _, facts := range values {
		for _, fact := range facts {
			result[factMapKey(fact.kind, fact.key)] = fact
		}
	}
	return result
}

func factMapKey(kind IncrementalFactKind, key string) string { return string(kind) + "\x00" + key }

func comparableIncrementalFactKey(key string) string {
	if index := strings.Index(key, "\x00ambiguous\x00"); index >= 0 {
		return key[:index]
	}
	return key
}

func ambiguousFactKey(key, id string) string {
	return key + "\x00ambiguous\x00" + id
}

func appendUnique(values *[]string, value string) {
	if value == "" || containsString(*values, value) {
		return
	}
	*values = append(*values, value)
}

func containsString(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func sortIncrementalReport(report *IncrementalReport) {
	compare := func(left, right FactReference) bool {
		if left.Kind != right.Kind {
			return left.Kind < right.Kind
		}
		return left.Key < right.Key
	}
	for _, values := range [][]FactReference{report.Reused, report.Changed, report.Added, report.Removed} {
		sort.Slice(values, func(i, j int) bool { return compare(values[i], values[j]) })
	}
	sort.Slice(report.Impacts, func(i, j int) bool { return report.Impacts[i].Projection < report.Impacts[j].Projection })
}

func incrementalContext(ctx context.Context) error {
	if ctx == nil {
		return ErrIncrementalInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func incrementalDigest(value any) (string, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:]), nil
}

func isIncrementalDigest(value string) bool {
	if len(value) != sha256.Size*2 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func stableArtifactKey(artifact contract.Artifact) string {
	return "artifact\x00" + strings.TrimSpace(artifact.Path) + "\x00" + strings.TrimSpace(artifact.Type)
}

func stableContributionKey(contribution contract.Contribution, artifactKey string) string {
	return "observation\x00" + artifactKey + "\x00" + strings.Join([]string{
		contribution.AnalyzerID, contribution.AnalyzerVersion, contribution.Method, contribution.Type,
		stableLocatorKey(contribution.Locator),
	}, "\x00")
}

func stableEvidenceKey(unit evidence.EvidenceUnit, artifactKey, contributionKey string) string {
	return "evidence\x00" + artifactKey + "\x00" + contributionKey + "\x00" + stableLocatorKey(unit.Locator)
}

func stableCoverageKey(value contract.Coverage) string {
	return "coverage\x00" + strings.Join([]string{value.Dimension, value.Scope, value.AnalyzerID}, "\x00")
}

func stableGapKey(value contract.Gap) string {
	return "gap\x00" + strings.Join([]string{value.Code, value.Dimension, value.Scope, value.AnalyzerID}, "\x00")
}

func stableFailureKey(value contract.Failure, artifactKey string) string {
	return "failure\x00" + strings.Join([]string{value.Code, value.Operation, artifactKey, value.AnalyzerID}, "\x00")
}

func locatorKey(locator contract.Locator) string {
	value, _ := json.Marshal(locator)
	return string(value)
}

func stableLocatorKey(locator contract.Locator) string {
	// Source and artifact IDs are snapshot facts and can legitimately change
	// when an artifact content hash changes. Path/member/position remain the
	// compatible provenance boundary; the enclosing stable artifact key keeps
	// the comparison scoped to one artifact.
	locator.SourceID = ""
	locator.ArtifactID = ""
	return locatorKey(locator)
}

func contributionFactDigest(contribution contract.Contribution, artifactKey string) (string, error) {
	var value any
	if len(contribution.Value) != 0 {
		if err := json.Unmarshal(contribution.Value, &value); err != nil {
			return "", err
		}
	}
	return incrementalDigest(struct {
		ArtifactKey     string `json:"artifact_key"`
		AnalyzerID      string `json:"analyzer_id"`
		AnalyzerVersion string `json:"analyzer_version"`
		Method          string `json:"method"`
		Type            string `json:"type"`
		Locator         string `json:"locator"`
		Value           any    `json:"value,omitempty"`
	}{artifactKey, contribution.AnalyzerID, contribution.AnalyzerVersion, contribution.Method, contribution.Type, stableLocatorKey(contribution.Locator), value})
}

func evidenceFactDigest(unit evidence.EvidenceUnit, artifactKey, contributionKey string) (string, error) {
	findings := append([]string(nil), unit.Findings...)
	sort.Strings(findings)
	return incrementalDigest(struct {
		ArtifactKey           string                  `json:"artifact_key"`
		ContributionKey       string                  `json:"contribution_key"`
		Locator               string                  `json:"locator"`
		ContentState          evidence.ContentState   `json:"content_state"`
		ContentHash           string                  `json:"content_hash"`
		RetainedContentDigest string                  `json:"retained_content_digest"`
		Truncated             bool                    `json:"truncated"`
		RedactionReason       string                  `json:"redaction_reason,omitempty"`
		ContentBytes          int64                   `json:"content_bytes"`
		ContentCharacters     int64                   `json:"content_characters"`
		Persist               evidence.Decision       `json:"persist"`
		ExternalTransfer      evidence.Decision       `json:"external_transfer"`
		Classification        evidence.Classification `json:"classification,omitempty"`
		Findings              []string                `json:"findings,omitempty"`
	}{artifactKey, contributionKey, stableLocatorKey(unit.Locator), unit.ContentState, unit.ContentHash, evidence.ContentDigest(unit.Content), unit.Truncated, unit.RedactionReason, unit.ContentBytes, unit.ContentCharacters, unit.Persist, unit.ExternalTransfer, unit.Classification, findings})
}
