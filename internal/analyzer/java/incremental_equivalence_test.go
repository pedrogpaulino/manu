package java

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/pedrogpaulino/manu/internal/analysis"
	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/derivation"
	"github.com/pedrogpaulino/manu/internal/fact"
	"github.com/pedrogpaulino/manu/internal/identity"
	"github.com/pedrogpaulino/manu/internal/normalization"
	"github.com/pedrogpaulino/manu/internal/persistence"
)

func TestJavaQuarkusIncrementalUpdateMatchesCompleteRebuild(t *testing.T) {
	fixturePath := javaIntegrationFixturePath(t)
	root, err := os.OpenRoot(filepath.Dir(fixturePath))
	if err != nil {
		t.Fatalf("open fixture root: %v", err)
	}
	defer root.Close()

	fixtureName := filepath.Base(fixturePath)
	fixtureBytes := javaIntegrationReadFixture(t, root, fixtureName)
	hash := sha256.Sum256(fixtureBytes)
	artifact := contract.Artifact{
		SourceID: "source-quarkus3",
		Path:     fixtureName,
		Type:     analysis.ArtifactTypeJava,
		Hash:     hex.EncodeToString(hash[:]),
		Size:     int64(len(fixtureBytes)),
	}
	artifact.ID = contract.ArtifactID(artifact.SourceID, artifact.Path, artifact.Hash)

	analyzed, err := New().Analyze(context.Background(), analysis.ArtifactInput{
		SourceID:   artifact.SourceID,
		RootHandle: root,
		Artifact:   artifact,
		Evidence: analysis.EvidenceInput{
			Enabled: true,
			Limits:  analysis.DefaultEvidenceLimits(),
		},
	})
	if err != nil {
		t.Fatalf("Analyze() error: %v", err)
	}
	manifest := javaIntegrationManifest()
	registrations, err := NormalizerRegistrations(manifest)
	if err != nil {
		t.Fatalf("NormalizerRegistrations() error: %v", err)
	}
	normalizer, err := normalization.NewRegistry(registrations...)
	if err != nil {
		t.Fatalf("NewRegistry() error: %v", err)
	}

	previousScope := fact.Scope{
		OrganizationID: "organization-quarkus3",
		SourceID:       artifact.SourceID,
		SnapshotID:     "snapshot-quarkus3-booking-previous",
	}
	currentScope := previousScope
	currentScope.SnapshotID = "snapshot-quarkus3-booking-current"
	previousObserved := javaIncrementalNormalize(t, normalizer, analyzed, previousScope, manifest)
	currentObserved := javaIncrementalNormalize(t, normalizer, analyzed, currentScope, manifest)
	if !reflect.DeepEqual(javaSemanticFactSet(t, previousObserved), javaSemanticFactSet(t, currentObserved)) {
		t.Fatal("same Java fixture produced different observed facts after neutralizing only snapshot identity")
	}

	ruleRegistry, err := derivation.NewRegistry(
		derivation.MembershipRuleRegistration(),
		derivation.DependencyRuleRegistration(),
	)
	if err != nil {
		t.Fatalf("NewRegistry(derivation) error: %v", err)
	}
	deriver, err := derivation.NewDeriver(ruleRegistry)
	if err != nil {
		t.Fatalf("NewDeriver() error: %v", err)
	}
	previousComplete, err := deriver.Derive(context.Background(), previousObserved)
	if err != nil {
		t.Fatalf("previous complete derivation error: %v", err)
	}
	currentComplete, err := deriver.Derive(context.Background(), currentObserved)
	if err != nil {
		t.Fatalf("current complete derivation error: %v", err)
	}
	if len(previousComplete) <= len(previousObserved) || len(currentComplete) <= len(currentObserved) {
		t.Fatalf("real Java derivation produced no derived facts: previous=%d/%d current=%d/%d", len(previousComplete), len(previousObserved), len(currentComplete), len(currentObserved))
	}

	rules := javaIncrementalRuleVersions()
	previousSnapshot := javaIncrementalSnapshotInput(previousScope, manifest, rules, previousComplete)
	currentSnapshot := javaIncrementalSnapshotInput(currentScope, manifest, rules, currentObserved)
	previousRevision := persistence.FactualSnapshotRevision{
		Snapshot:        previousSnapshot,
		Artifacts:       []contract.Artifact{artifact},
		ConfigurationID: "java-quarkus-safe-static-v1",
	}
	currentRevision := persistence.FactualSnapshotRevision{
		Snapshot:        currentSnapshot,
		Artifacts:       []contract.Artifact{artifact},
		ConfigurationID: "java-quarkus-safe-static-v1",
	}

	firstDelta, err := persistence.PlanFactualSnapshotUpdate(context.Background(), previousRevision, currentRevision)
	if err != nil {
		t.Fatalf("PlanFactualSnapshotUpdate() error: %v", err)
	}
	secondDelta, err := persistence.PlanFactualSnapshotUpdate(context.Background(), previousRevision, currentRevision)
	if err != nil {
		t.Fatalf("repeated PlanFactualSnapshotUpdate() error: %v", err)
	}
	if !reflect.DeepEqual(firstDelta, secondDelta) {
		t.Fatal("repeated PlanFactualSnapshotUpdate() changed its deterministic delta")
	}

	previousObservedByID, previousDerivedByID := javaIncrementalFactsByKind(previousComplete)
	currentObservedByID, currentDerivedByID := javaIncrementalFactsByKind(currentComplete)
	if len(currentObservedByID) != len(previousObservedByID) {
		t.Fatalf("observed fact volumes differ for unchanged fixture: previous=%d current=%d", len(previousObservedByID), len(currentObservedByID))
	}
	if len(currentDerivedByID) != len(previousDerivedByID) {
		t.Fatalf("derived fact volumes differ for unchanged fixture: previous=%d current=%d", len(previousDerivedByID), len(currentDerivedByID))
	}

	if got, want := len(firstDelta.ObservedReused), len(previousObservedByID); got != want {
		t.Fatalf("observed reused = %d, want %d", got, want)
	}
	if len(firstDelta.ObservedReprocessIDs) != 0 || len(firstDelta.ObservedRemovedIDs) != 0 {
		t.Fatalf("unchanged fixture was reprocessed or removed: reprocess=%v removed=%v", firstDelta.ObservedReprocessIDs, firstDelta.ObservedRemovedIDs)
	}
	if got, want := len(firstDelta.DerivedReusableIDs), len(previousDerivedByID); got != want {
		t.Fatalf("derived reusable = %d, want %d", got, want)
	}
	if len(firstDelta.DerivedReevaluateIDs) != 0 || firstDelta.FanoutReevaluated != 0 {
		t.Fatalf("unchanged fixture reevaluated derived fanout: ids=%v count=%d", firstDelta.DerivedReevaluateIDs, firstDelta.FanoutReevaluated)
	}

	javaAssertIncrementalClassifications(t, firstDelta, previousObservedByID, currentObservedByID, previousDerivedByID)

	// The planner exposes no payload-copy or rebase operation. For this
	// unchanged-source case, the semantic incremental result is therefore the
	// current observations plus the previous derived facts classified as
	// reusable. The complete current derivation remains the comparison oracle.
	incrementalSemantic := make([]fact.CanonicalFact, 0, len(currentObservedByID)+len(firstDelta.DerivedReusableIDs))
	for _, candidate := range currentObservedByID {
		incrementalSemantic = append(incrementalSemantic, candidate)
	}
	for _, factID := range firstDelta.DerivedReusableIDs {
		incrementalSemantic = append(incrementalSemantic, previousDerivedByID[factID])
	}
	javaAssertSemanticFactSet(t, "incremental result", incrementalSemantic)
	javaAssertSemanticFactSet(t, "complete previous rebuild", previousComplete)
	javaAssertSemanticFactSet(t, "complete current rebuild", currentComplete)

	incrementalSet := javaSemanticFactSet(t, incrementalSemantic)
	previousSet := javaSemanticFactSet(t, previousComplete)
	currentSet := javaSemanticFactSet(t, currentComplete)
	if !reflect.DeepEqual(incrementalSet, currentSet) {
		t.Fatalf("incremental semantic facts differ from complete current rebuild: incremental=%d current=%d", len(incrementalSet), len(currentSet))
	}
	if !reflect.DeepEqual(previousSet, currentSet) {
		t.Fatalf("complete rebuilds differ semantically after neutralizing only snapshot identity: previous=%d current=%d", len(previousSet), len(currentSet))
	}
	if !reflect.DeepEqual(previousSet, incrementalSet) {
		t.Fatal("incremental semantic facts differ from complete previous rebuild")
	}

	t.Logf("Java/Quarkus incremental equivalence: observed total=%d reused=%d reprocessed=%d; derived total=%d reused=%d reevaluated=%d; semantic facts=%d", len(currentObservedByID), len(firstDelta.ObservedReused), len(firstDelta.ObservedReprocessIDs), len(currentDerivedByID), len(firstDelta.DerivedReusableIDs), firstDelta.FanoutReevaluated, len(currentSet))
}

func javaIncrementalNormalize(
	t *testing.T,
	normalizer *normalization.Registry,
	analyzed analysis.Output,
	scope fact.Scope,
	manifest fact.FrontendManifest,
) []fact.CanonicalFact {
	t.Helper()
	inputs, _, _, _ := javaIntegrationInputs(t, analyzed, scope, manifest)
	output, err := normalizer.NormalizeAll(context.Background(), inputs)
	if err != nil {
		t.Fatalf("NormalizeAll() error for %q: %v", scope.SnapshotID, err)
	}
	if len(output.Facts) == 0 {
		t.Fatalf("NormalizeAll() produced no facts for %q", scope.SnapshotID)
	}
	return output.Facts
}

func javaIncrementalRuleVersions() []persistence.RuleVersion {
	const implementationDigest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	return []persistence.RuleVersion{
		{
			RuleID:               derivation.MembershipRuleID,
			Version:              derivation.MembershipRuleVersion,
			ImplementationDigest: implementationDigest,
			Configuration:        json.RawMessage(`{}`),
		},
		{
			RuleID:               derivation.DependencyRuleID,
			Version:              derivation.DependencyRuleVersion,
			ImplementationDigest: implementationDigest,
			Configuration:        json.RawMessage(`{}`),
		},
	}
}

func javaIncrementalSnapshotInput(
	scope fact.Scope,
	manifest fact.FrontendManifest,
	rules []persistence.RuleVersion,
	facts []fact.CanonicalFact,
) persistence.FactualSnapshotInput {
	return persistence.FactualSnapshotInput{
		OrganizationID:    identity.CanonicalUUID("organization", scope.OrganizationID),
		SourceID:          identity.CanonicalUUID("source", scope.OrganizationID, scope.SourceID),
		SnapshotID:        identity.CanonicalUUID("snapshot", scope.OrganizationID, scope.SourceID, scope.SnapshotID),
		Scope:             scope,
		FrontendManifests: []fact.FrontendManifest{manifest},
		RuleVersions:      append([]persistence.RuleVersion(nil), rules...),
		Facts:             append([]fact.CanonicalFact(nil), facts...),
	}
}

func javaIncrementalFactsByKind(facts []fact.CanonicalFact) (map[string]fact.CanonicalFact, map[string]fact.CanonicalFact) {
	observed := make(map[string]fact.CanonicalFact)
	derived := make(map[string]fact.CanonicalFact)
	for _, candidate := range facts {
		if candidate.Lineage == nil {
			observed[candidate.ID] = candidate
			continue
		}
		derived[candidate.ID] = candidate
	}
	return observed, derived
}

func javaAssertIncrementalClassifications(
	t *testing.T,
	delta persistence.FactualSnapshotDelta,
	previousObserved map[string]fact.CanonicalFact,
	currentObserved map[string]fact.CanonicalFact,
	previousDerived map[string]fact.CanonicalFact,
) {
	t.Helper()
	reusedPrevious := make(map[string]struct{}, len(delta.ObservedReused))
	reusedCurrent := make(map[string]struct{}, len(delta.ObservedReused))
	removedPrevious := make(map[string]struct{}, len(delta.ObservedRemovedIDs))
	for _, reuse := range delta.ObservedReused {
		if _, exists := previousObserved[reuse.PreviousFactID]; !exists {
			t.Fatalf("delta reused unknown previous observed fact %q", reuse.PreviousFactID)
		}
		if _, exists := currentObserved[reuse.CurrentFactID]; !exists {
			t.Fatalf("delta reused unknown current observed fact %q", reuse.CurrentFactID)
		}
		reusedPrevious[reuse.PreviousFactID] = struct{}{}
		reusedCurrent[reuse.CurrentFactID] = struct{}{}
	}
	for _, factID := range delta.ObservedRemovedIDs {
		if _, exists := previousObserved[factID]; !exists {
			t.Fatalf("delta removed unknown previous observed fact %q", factID)
		}
		if _, reused := reusedPrevious[factID]; reused {
			t.Fatalf("previous observed fact %q appears as both reused and removed", factID)
		}
		removedPrevious[factID] = struct{}{}
	}
	for factID := range previousObserved {
		if _, reused := reusedPrevious[factID]; reused {
			continue
		}
		if _, removed := removedPrevious[factID]; !removed {
			t.Fatalf("previous observed fact %q was not classified as reused or removed", factID)
		}
	}
	reprocessedCurrent := make(map[string]struct{}, len(delta.ObservedReprocessIDs))
	for _, factID := range delta.ObservedReprocessIDs {
		if _, exists := currentObserved[factID]; !exists {
			t.Fatalf("delta reprocessed unknown current observed fact %q", factID)
		}
		if _, reused := reusedCurrent[factID]; reused {
			t.Fatalf("current observed fact %q appears as both reused and reprocessed", factID)
		}
		reprocessedCurrent[factID] = struct{}{}
	}
	for factID := range currentObserved {
		if _, reused := reusedCurrent[factID]; !reused {
			if _, reprocessed := reprocessedCurrent[factID]; !reprocessed {
				t.Fatalf("current observed fact %q was not classified as reused or reprocessed", factID)
			}
		}
	}

	classifiedDerived := make(map[string]struct{}, len(delta.DerivedReusableIDs)+len(delta.DerivedReevaluateIDs))
	for _, factID := range delta.DerivedReusableIDs {
		if _, exists := previousDerived[factID]; !exists {
			t.Fatalf("delta marked unknown derived fact %q as reusable", factID)
		}
		classifiedDerived[factID] = struct{}{}
	}
	for _, factID := range delta.DerivedReevaluateIDs {
		if _, exists := previousDerived[factID]; !exists {
			t.Fatalf("delta marked unknown derived fact %q for reevaluation", factID)
		}
		if _, duplicate := classifiedDerived[factID]; duplicate {
			t.Fatalf("derived fact %q appears in both reusable and reevaluate classifications", factID)
		}
		classifiedDerived[factID] = struct{}{}
	}
	for factID := range previousDerived {
		if _, classified := classifiedDerived[factID]; !classified {
			t.Fatalf("previous derived fact %q was left outside the delta", factID)
		}
	}
}

func javaAssertSemanticFactSet(t *testing.T, label string, facts []fact.CanonicalFact) {
	t.Helper()
	_ = javaSemanticFactSet(t, facts)
	if len(facts) == 0 {
		t.Fatalf("%s is empty", label)
	}
}

func javaSemanticFactSet(t *testing.T, facts []fact.CanonicalFact) map[string]struct{} {
	t.Helper()
	set := make(map[string]struct{}, len(facts))
	for _, candidate := range facts {
		semantic := candidate
		semantic.Scope.SnapshotID = "java-incremental-neutral-snapshot"
		digest, err := fact.IdentityDigest(semantic)
		if err != nil {
			t.Fatalf("semantic digest for fact %q: %v", candidate.ID, err)
		}
		if _, duplicate := set[digest]; duplicate {
			t.Fatalf("duplicate semantically identical fact %q in comparison set", candidate.ID)
		}
		set[digest] = struct{}{}
	}
	return set
}
