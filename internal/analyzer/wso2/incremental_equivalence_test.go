package wso2

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/pedrogpaulino/manu/internal/analysis"
	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/derivation"
	"github.com/pedrogpaulino/manu/internal/fact"
	"github.com/pedrogpaulino/manu/internal/identity"
	"github.com/pedrogpaulino/manu/internal/normalization"
	"github.com/pedrogpaulino/manu/internal/persistence"
	"github.com/pedrogpaulino/manu/internal/source"
)

func TestWSO2IncrementalUpdateMatchesFullRebuild(t *testing.T) {
	first := buildWSO2IncrementalScenario(t, "snapshot-wso2-incremental-first", "fixture.car", readFixture(t, "testdata/api-v1.xml"))
	second := buildWSO2IncrementalScenario(t, "snapshot-wso2-incremental-second", "fixture.car", readFixture(t, "testdata/api-v1.xml"))
	rules := wso2IncrementalRules()

	firstObservedBefore := cloneWSO2Facts(first.Observed)
	secondObservedBefore := cloneWSO2Facts(second.Observed)
	firstFull := wso2IncrementalFullRebuild(t, first.Observed, rules)
	secondFull := wso2IncrementalFullRebuild(t, second.Observed, rules)
	repeatedFirstFull := wso2IncrementalFullRebuild(t, first.Observed, rules)
	repeatedSecondFull := wso2IncrementalFullRebuild(t, second.Observed, rules)
	if !reflect.DeepEqual(first.Observed, firstObservedBefore) || !reflect.DeepEqual(second.Observed, secondObservedBefore) {
		t.Fatal("full factual rebuild mutated observed WSO2 facts")
	}
	if !reflect.DeepEqual(firstFull, repeatedFirstFull) || !reflect.DeepEqual(secondFull, repeatedSecondFull) {
		t.Fatal("full factual rebuild was not deterministic")
	}
	assertWSO2SemanticEquivalence(t, firstFull, secondFull)
	assertWSO2NoSemanticDuplication(t, firstFull)
	assertWSO2NoSemanticDuplication(t, secondFull)

	previous := wso2IncrementalRevision(first, firstFull, rules)
	current := wso2IncrementalRevision(second, second.Observed, rules)
	previousBefore := cloneWSO2IncrementalRevision(previous)
	currentBefore := cloneWSO2IncrementalRevision(current)

	delta, err := persistence.PlanFactualSnapshotUpdate(context.Background(), previous, current)
	if err != nil {
		t.Fatalf("PlanFactualSnapshotUpdate() error: %v", err)
	}
	if !reflect.DeepEqual(previous, previousBefore) || !reflect.DeepEqual(current, currentBefore) {
		t.Fatal("PlanFactualSnapshotUpdate() mutated its input revisions")
	}

	observedCount := len(first.Observed)
	derivedCount := len(firstFull) - observedCount
	if len(delta.ObservedReused) != observedCount || len(delta.ObservedReprocessIDs) != 0 || len(delta.ObservedRemovedIDs) != 0 {
		t.Fatalf("observed classification = reused:%d reprocess:%d removed:%d, want %d/0/0", len(delta.ObservedReused), len(delta.ObservedReprocessIDs), len(delta.ObservedRemovedIDs), observedCount)
	}
	if len(delta.DerivedReusableIDs) != derivedCount || len(delta.DerivedReevaluateIDs) != 0 || delta.FanoutReevaluated != 0 {
		t.Fatalf("derived classification = reusable:%d reevaluate:%d fanout:%d, want %d/0/0", len(delta.DerivedReusableIDs), len(delta.DerivedReevaluateIDs), delta.FanoutReevaluated, derivedCount)
	}
	if len(delta.Invalidations) != 0 || len(delta.ArtifactAddedPaths) != 0 || len(delta.ArtifactChangedPaths) != 0 || len(delta.ArtifactRemovedPaths) != 0 {
		t.Fatalf("unchanged snapshot delta = %#v, want no invalidations or artifact changes", delta)
	}
	if got := len(delta.ObservedReused) + len(delta.ObservedReprocessIDs); got != len(second.Observed) {
		t.Fatalf("current observed classifications = %d, want %d", got, len(second.Observed))
	}
	if got := len(delta.ObservedReused) + len(delta.ObservedRemovedIDs); got != len(first.Observed) {
		t.Fatalf("previous observed classifications = %d, want %d", got, len(first.Observed))
	}
	if got := len(delta.DerivedReusableIDs) + len(delta.DerivedReevaluateIDs); got != derivedCount {
		t.Fatalf("previous derived classifications = %d, want %d", got, derivedCount)
	}

	t.Logf("WSO2 incremental volumes: observed reused=%d reprocessed=%d removed=%d; derived reused=%d reevaluated=%d; fanout=%d", len(delta.ObservedReused), len(delta.ObservedReprocessIDs), len(delta.ObservedRemovedIDs), len(delta.DerivedReusableIDs), len(delta.DerivedReevaluateIDs), delta.FanoutReevaluated)

	repeated, err := persistence.PlanFactualSnapshotUpdate(context.Background(), previous, current)
	if err != nil {
		t.Fatalf("repeated PlanFactualSnapshotUpdate() error: %v", err)
	}
	if !reflect.DeepEqual(delta, repeated) {
		t.Fatalf("PlanFactualSnapshotUpdate() is not deterministic:\nfirst=%#v\nsecond=%#v", delta, repeated)
	}
}

func TestWSO2IncrementalLocalizedArtifactReprocessesOnlyAffectedFanout(t *testing.T) {
	baseAPI := readFixture(t, "testdata/api-v1.xml")
	changedAPI := bytes.Replace(baseAPI, []byte("OrdersAPI"), []byte("OrdersAPIChanged"), 1)
	if bytes.Equal(baseAPI, changedAPI) {
		t.Fatal("localized WSO2 fixture mutation did not change the API member")
	}
	previousA := buildWSO2IncrementalScenario(t, "snapshot-wso2-incremental-localized-first", "fixture-a.car", baseAPI)
	previousB := buildWSO2IncrementalScenario(t, "snapshot-wso2-incremental-localized-first", "fixture-b.car", baseAPI)
	currentA := buildWSO2IncrementalScenario(t, "snapshot-wso2-incremental-localized-second", "fixture-a.car", baseAPI)
	currentB := buildWSO2IncrementalScenario(t, "snapshot-wso2-incremental-localized-second", "fixture-b.car", changedAPI)
	rules := wso2IncrementalRules()

	previousObserved := append(cloneWSO2Facts(previousA.Observed), previousB.Observed...)
	currentObserved := append(cloneWSO2Facts(currentA.Observed), currentB.Observed...)
	previousFull := wso2IncrementalFullRebuild(t, previousObserved, rules)
	currentFull := wso2IncrementalFullRebuild(t, currentObserved, rules)
	assertWSO2NoSemanticDuplication(t, previousFull)
	assertWSO2NoSemanticDuplication(t, currentFull)

	previous := wso2IncrementalCombinedRevision("snapshot-wso2-incremental-localized-first", []wso2IncrementalFixture{previousA, previousB}, previousFull, rules)
	current := wso2IncrementalCombinedRevision("snapshot-wso2-incremental-localized-second", []wso2IncrementalFixture{currentA, currentB}, currentObserved, rules)
	previousBefore := cloneWSO2IncrementalRevision(previous)
	currentBefore := cloneWSO2IncrementalRevision(current)
	delta, err := persistence.PlanFactualSnapshotUpdate(context.Background(), previous, current)
	if err != nil {
		t.Fatalf("PlanFactualSnapshotUpdate(localized) error: %v", err)
	}
	if !reflect.DeepEqual(previous, previousBefore) || !reflect.DeepEqual(current, currentBefore) {
		t.Fatal("PlanFactualSnapshotUpdate(localized) mutated its input revisions")
	}

	previousDerived := len(previousFull) - len(previousObserved)
	unchangedDerived := len(wso2IncrementalFullRebuild(t, cloneWSO2Facts(previousA.Observed), rules)) - len(previousA.Observed)
	if len(delta.ObservedReused) != len(previousA.Observed) || len(delta.ObservedReprocessIDs) != len(currentB.Observed) || len(delta.ObservedRemovedIDs) != len(previousB.Observed) {
		t.Fatalf("localized observed classification = reused:%d reprocess:%d removed:%d, want %d/%d/%d", len(delta.ObservedReused), len(delta.ObservedReprocessIDs), len(delta.ObservedRemovedIDs), len(previousA.Observed), len(currentB.Observed), len(previousB.Observed))
	}
	if len(delta.DerivedReusableIDs) != unchangedDerived || len(delta.DerivedReevaluateIDs) != previousDerived-unchangedDerived || delta.FanoutReevaluated != previousDerived-unchangedDerived {
		t.Fatalf("localized derived classification = reusable:%d reevaluate:%d fanout:%d, want %d/%d/%d", len(delta.DerivedReusableIDs), len(delta.DerivedReevaluateIDs), delta.FanoutReevaluated, unchangedDerived, previousDerived-unchangedDerived, previousDerived-unchangedDerived)
	}
	if !reflect.DeepEqual(delta.ArtifactChangedPaths, []string{"fixture-b.car"}) || len(delta.ArtifactAddedPaths) != 0 || len(delta.ArtifactRemovedPaths) != 0 {
		t.Fatalf("localized artifact classification = changed:%v added:%v removed:%v", delta.ArtifactChangedPaths, delta.ArtifactAddedPaths, delta.ArtifactRemovedPaths)
	}
	for _, invalidation := range delta.Invalidations {
		for _, candidate := range previousFull {
			if candidate.ID == invalidation.FactID && candidate.Evidence[0].Locator.Path == "fixture-a.car" {
				t.Fatalf("unchanged artifact fact %q was invalidated: %#v", invalidation.FactID, invalidation.Reasons)
			}
		}
	}
	if !wso2DeltaHasReason(delta, persistence.FactualInvalidationArtifactHashChanged) || !wso2DeltaHasReason(delta, persistence.FactualInvalidationUpstreamLineage) {
		t.Fatalf("localized invalidation reasons = %#v, want artifact hash and upstream lineage", delta.Invalidations)
	}
	if got := len(delta.ObservedReused) + len(delta.ObservedReprocessIDs); got != len(currentObserved) {
		t.Fatalf("localized current observed classifications = %d, want %d", got, len(currentObserved))
	}
	if got := len(delta.ObservedReused) + len(delta.ObservedRemovedIDs); got != len(previousObserved) {
		t.Fatalf("localized previous observed classifications = %d, want %d", got, len(previousObserved))
	}
	if got := len(delta.DerivedReusableIDs) + len(delta.DerivedReevaluateIDs); got != previousDerived {
		t.Fatalf("localized previous derived classifications = %d, want %d", got, previousDerived)
	}
	t.Logf("WSO2 localized volumes: observed reused=%d reprocessed=%d removed=%d; derived reused=%d reevaluated=%d; fanout=%d", len(delta.ObservedReused), len(delta.ObservedReprocessIDs), len(delta.ObservedRemovedIDs), len(delta.DerivedReusableIDs), len(delta.DerivedReevaluateIDs), delta.FanoutReevaluated)
}

type wso2IncrementalFixture struct {
	Artifact contract.Artifact
	Scope    fact.Scope
	Manifest fact.FrontendManifest
	Observed []fact.CanonicalFact
}

func buildWSO2IncrementalScenario(t *testing.T, snapshotID, artifactPath string, apiXML []byte) wso2IncrementalFixture {
	t.Helper()
	// The CAR fixture's two original members form a cycle. A terminal member
	// makes the real dependency rule produce an observable A -> B -> C result,
	// while retaining the versioned CAR members and analyzer path unchanged.
	sharedXML := readFixture(t, "testdata/shared-v1.xml")
	if !bytes.Contains(sharedXML, []byte("</sequence>")) {
		t.Fatal("shared WSO2 fixture has no sequence terminator")
	}
	sharedXML = bytes.Replace(sharedXML, []byte("</sequence>"), []byte("<include>synapse/target-v1.xml</include></sequence>"), 1)
	archiveBytes := makeCAR(t, map[string][]byte{
		"synapse/api-v1.xml":    apiXML,
		"synapse/shared-v1.xml": sharedXML,
		"synapse/target-v1.xml": []byte(`<sequence name="targetSequence"><message type="json"/></sequence>`),
	})
	rootPath := t.TempDir()
	if err := os.WriteFile(filepath.Join(rootPath, filepath.Base(artifactPath)), archiveBytes, 0o600); err != nil {
		t.Fatalf("write WSO2 CAR fixture: %v", err)
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatalf("open WSO2 fixture root: %v", err)
	}
	t.Cleanup(func() { _ = root.Close() })

	hash := sha256.Sum256(archiveBytes)
	artifact := contract.Artifact{
		SourceID: "wso2-incremental-source",
		Path:     artifactPath,
		Type:     analysis.ArtifactTypeCAR,
		Hash:     hex.EncodeToString(hash[:]),
		Size:     int64(len(archiveBytes)),
	}
	artifact.ID = contract.ArtifactID(artifact.SourceID, artifact.Path, artifact.Hash)
	input := analysis.ArtifactInput{
		SourceID:   artifact.SourceID,
		RootHandle: root,
		Artifact:   artifact,
		Limits: source.Limits{
			MaxArchiveMembers:         16,
			MaxArchiveBytes:           1 << 20,
			MaxArchiveMemberBytes:     1 << 20,
			MaxArchiveCompressedBytes: 1 << 20,
			MaxExpansionRatio:         100,
			MaxExtractionBytes:        1 << 20,
		},
		Evidence: analysis.EvidenceInput{Enabled: true, Limits: analysis.DefaultEvidenceLimits()},
	}
	analyzed, err := New().Analyze(context.Background(), input)
	if err != nil {
		t.Fatalf("Analyze(%s) error: %v", artifactPath, err)
	}
	manifest := Manifest()
	registrations, err := NormalizerRegistrations(manifest)
	if err != nil {
		t.Fatalf("NormalizerRegistrations() error: %v", err)
	}
	registry, err := normalization.NewRegistry(registrations...)
	if err != nil {
		t.Fatalf("NewRegistry() error: %v", err)
	}
	scope := fact.Scope{
		OrganizationID: "organization-wso2-incremental",
		SourceID:       artifact.SourceID,
		SnapshotID:     snapshotID,
	}
	inputs, contributions, evidenceLocators := wso2IntegrationInputs(t, analyzed, scope, manifest)
	normalized, err := registry.NormalizeAll(context.Background(), inputs)
	if err != nil {
		t.Fatalf("NormalizeAll() error: %v", err)
	}
	assertWSO2IntegrationOutput(t, normalized, inputs, scope, manifest, evidenceLocators)
	assertWSO2MemberIncludeCorrelation(t, normalized, inputs, contributions, artifact.ID)
	return wso2IncrementalFixture{
		Artifact: artifact,
		Scope:    scope,
		Manifest: manifest,
		Observed: cloneWSO2Facts(normalized.Facts),
	}
}

func wso2IncrementalFullRebuild(t *testing.T, observed []fact.CanonicalFact, rules []persistence.RuleVersion) []fact.CanonicalFact {
	t.Helper()
	registrations := make([]derivation.Registration, 0, len(rules))
	for _, rule := range rules {
		switch {
		case rule.RuleID == derivation.MembershipRuleID && rule.Version == derivation.MembershipRuleVersion:
			registrations = append(registrations, derivation.MembershipRuleRegistration())
		case rule.RuleID == derivation.DependencyRuleID && rule.Version == derivation.DependencyRuleVersion:
			registrations = append(registrations, derivation.DependencyRuleRegistration())
		default:
			t.Fatalf("WSO2 fixture requested unsupported real rule %s@%s", rule.RuleID, rule.Version)
		}
	}
	registry, err := derivation.NewRegistry(registrations...)
	if err != nil {
		t.Fatalf("NewRegistry() error: %v", err)
	}
	executor, err := derivation.NewExecutor(registry)
	if err != nil {
		t.Fatalf("NewExecutor() error: %v", err)
	}
	result, err := executor.Derive(context.Background(), observed)
	if err != nil {
		t.Fatalf("full factual rebuild with rules %v: %v", factualRuleNames(rules), err)
	}
	if len(result) < len(observed) {
		t.Fatalf("full rebuild returned %d facts for %d observed facts", len(result), len(observed))
	}
	if len(result) == len(observed) {
		t.Fatalf("full rebuild produced no derived WSO2 facts; dependency rule was not exercised")
	}
	return result
}

func wso2IncrementalRules() []persistence.RuleVersion {
	return []persistence.RuleVersion{
		{
			RuleID:               derivation.DependencyRuleID,
			Version:              derivation.DependencyRuleVersion,
			ImplementationDigest: fact.ExtensionDigest([]byte("manu:derivation:dependency:v1")),
			Configuration:        json.RawMessage(`{"mode":"transitive"}`),
		},
		{
			RuleID:               derivation.MembershipRuleID,
			Version:              derivation.MembershipRuleVersion,
			ImplementationDigest: fact.ExtensionDigest([]byte("manu:derivation:membership:v1")),
			Configuration:        json.RawMessage(`{"mode":"conservative"}`),
		},
	}
}

func factualRuleNames(rules []persistence.RuleVersion) []string {
	result := make([]string, 0, len(rules))
	for _, rule := range rules {
		result = append(result, rule.RuleID+"@"+rule.Version)
	}
	sort.Strings(result)
	return result
}

func wso2IncrementalRevision(scenario wso2IncrementalFixture, facts []fact.CanonicalFact, rules []persistence.RuleVersion) persistence.FactualSnapshotRevision {
	return wso2IncrementalCombinedRevision(scenario.Scope.SnapshotID, []wso2IncrementalFixture{scenario}, facts, rules)
}

func wso2IncrementalCombinedRevision(snapshotID string, scenarios []wso2IncrementalFixture, facts []fact.CanonicalFact, rules []persistence.RuleVersion) persistence.FactualSnapshotRevision {
	if len(scenarios) == 0 {
		panic("wso2 incremental revision requires one fixture")
	}
	scope := scenarios[0].Scope
	scope.SnapshotID = snapshotID
	artifacts := make([]contract.Artifact, 0, len(scenarios))
	for _, scenario := range scenarios {
		artifacts = append(artifacts, scenario.Artifact)
	}
	sort.Slice(artifacts, func(left, right int) bool { return artifacts[left].Path < artifacts[right].Path })
	return persistence.FactualSnapshotRevision{
		Snapshot: persistence.FactualSnapshotInput{
			OrganizationID:    identity.CanonicalUUID("organization", scope.OrganizationID),
			SourceID:          identity.CanonicalUUID("source", scope.OrganizationID, scope.SourceID),
			SnapshotID:        identity.CanonicalUUID("snapshot", scope.OrganizationID, scope.SourceID, scope.SnapshotID),
			Scope:             scope,
			FrontendManifests: []fact.FrontendManifest{cloneWSO2IntegrationManifest(scenarios[0].Manifest)},
			RuleVersions:      cloneWSO2RuleVersions(rules),
			Facts:             cloneWSO2Facts(facts),
		},
		Artifacts:       artifacts,
		ConfigurationID: "wso2-incremental-configuration",
	}
}

func wso2DeltaHasReason(delta persistence.FactualSnapshotDelta, want persistence.FactualInvalidationReason) bool {
	for _, invalidation := range delta.Invalidations {
		for _, reason := range invalidation.Reasons {
			if reason == want {
				return true
			}
		}
	}
	return false
}

func assertWSO2SemanticEquivalence(t *testing.T, first, second []fact.CanonicalFact) {
	t.Helper()
	firstKeys := wso2SemanticKeys(t, first)
	secondKeys := wso2SemanticKeys(t, second)
	if !reflect.DeepEqual(firstKeys, secondKeys) {
		t.Fatalf("full rebuilds differ after neutralizing snapshot only:\nfirst=%v\nsecond=%v", firstKeys, secondKeys)
	}
}

func assertWSO2NoSemanticDuplication(t *testing.T, facts []fact.CanonicalFact) {
	t.Helper()
	keys := wso2SemanticKeys(t, facts)
	for index := 1; index < len(keys); index++ {
		if keys[index-1] == keys[index] {
			t.Fatalf("full rebuild contains duplicate semantic fact %q", keys[index])
		}
	}
}

func wso2SemanticKeys(t *testing.T, facts []fact.CanonicalFact) []string {
	t.Helper()
	keys := make([]string, 0, len(facts))
	for _, candidate := range facts {
		neutral := cloneWSO2Fact(candidate)
		neutral.Scope.SnapshotID = "wso2-semantic-neutral-snapshot"
		digest, err := fact.IdentityDigest(neutral)
		if err != nil {
			t.Fatalf("IdentityDigest() error: %v", err)
		}
		keys = append(keys, digest)
	}
	sort.Strings(keys)
	return keys
}

func cloneWSO2Facts(input []fact.CanonicalFact) []fact.CanonicalFact {
	result := make([]fact.CanonicalFact, len(input))
	for index, candidate := range input {
		result[index] = cloneWSO2Fact(candidate)
	}
	return result
}

func cloneWSO2Fact(candidate fact.CanonicalFact) fact.CanonicalFact {
	clone := candidate
	if candidate.Object != nil {
		object := *candidate.Object
		clone.Object = &object
	}
	if candidate.Value != nil {
		value := *candidate.Value
		clone.Value = &value
	}
	clone.Qualifiers = append([]fact.Qualifier(nil), candidate.Qualifiers...)
	clone.Evidence = append([]fact.EvidenceRef(nil), candidate.Evidence...)
	if candidate.Lineage != nil {
		lineage := *candidate.Lineage
		lineage.InputFactIDs = append([]string(nil), candidate.Lineage.InputFactIDs...)
		clone.Lineage = &lineage
	}
	return clone
}

func cloneWSO2RuleVersions(input []persistence.RuleVersion) []persistence.RuleVersion {
	result := make([]persistence.RuleVersion, len(input))
	for index, rule := range input {
		result[index] = rule
		result[index].Configuration = append(json.RawMessage(nil), rule.Configuration...)
	}
	return result
}

func cloneWSO2IncrementalRevision(input persistence.FactualSnapshotRevision) persistence.FactualSnapshotRevision {
	clone := input
	clone.Snapshot = input.Snapshot
	clone.Snapshot.FrontendManifests = make([]fact.FrontendManifest, len(input.Snapshot.FrontendManifests))
	for index, manifest := range input.Snapshot.FrontendManifests {
		clone.Snapshot.FrontendManifests[index] = cloneWSO2IntegrationManifest(manifest)
	}
	clone.Snapshot.RuleVersions = cloneWSO2RuleVersions(input.Snapshot.RuleVersions)
	clone.Snapshot.Facts = cloneWSO2Facts(input.Snapshot.Facts)
	clone.Artifacts = append([]contract.Artifact(nil), input.Artifacts...)
	return clone
}
