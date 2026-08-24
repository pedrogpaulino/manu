package persistence

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/fact"
	"github.com/pedrogpaulino/manu/internal/identity"
)

func TestPlanFactualSnapshotUpdateReusesReidentifiedObservedAndDerived(t *testing.T) {
	previous, _ := factualIncrementalGraphFixture(t)
	current := factualIncrementalCurrent(t, previous, "snapshot-current", previous.Artifacts, false, nil)

	delta, err := PlanFactualSnapshotUpdate(context.Background(), previous, current)
	if err != nil {
		t.Fatalf("PlanFactualSnapshotUpdate() error = %v", err)
	}
	if got, want := len(delta.ObservedReused), 3; got != want {
		t.Fatalf("observed reuse count = %d, want %d", got, want)
	}
	for _, pair := range delta.ObservedReused {
		if pair.PreviousFactID == pair.CurrentFactID {
			t.Fatalf("reused fact was not reidentified across snapshots: %#v", pair)
		}
	}
	if len(delta.ObservedReprocessIDs) != 0 || len(delta.ObservedRemovedIDs) != 0 {
		t.Fatalf("observed classifications = %#v/%#v, want empty", delta.ObservedReprocessIDs, delta.ObservedRemovedIDs)
	}
	if got, want := sortedIDs(delta.DerivedReusableIDs), sortedIDs(previousDerivedIDs(previous)); !reflect.DeepEqual(got, want) {
		t.Fatalf("reusable derived IDs = %#v, want %#v", got, want)
	}
	if len(delta.DerivedReevaluateIDs) != 0 || len(delta.Invalidations) != 0 || delta.FanoutReevaluated != 0 {
		t.Fatalf("derived invalidation = %#v, want none", delta)
	}
	if len(delta.ArtifactAddedPaths) != 0 || len(delta.ArtifactChangedPaths) != 0 || len(delta.ArtifactRemovedPaths) != 0 {
		t.Fatalf("artifact classifications = %#v/%#v/%#v, want empty", delta.ArtifactAddedPaths, delta.ArtifactChangedPaths, delta.ArtifactRemovedPaths)
	}
	if err := delta.Validate(); err != nil {
		t.Fatalf("delta.Validate() error = %v", err)
	}
}

func TestPlanFactualSnapshotUpdateReprocessesLocalizedArtifactAndOnlyReverseFanout(t *testing.T) {
	previous, previousFacts := factualIncrementalGraphFixture(t)
	changedArtifacts := append([]contract.Artifact(nil), previous.Artifacts...)
	changedArtifacts[0].Hash = strings.Repeat("f", 64)
	changedArtifacts[0].ID = contract.ArtifactID(changedArtifacts[0].SourceID, changedArtifacts[0].Path, changedArtifacts[0].Hash)
	current := factualIncrementalCurrent(t, previous, "snapshot-current", changedArtifacts, false, func(candidate *fact.CanonicalFact) {
		if candidate.Lineage == nil && candidate.Subject.ID == "observed-a" {
			candidate.Value = &fact.TypedValue{Kind: fact.ValueString, String: "changed-a"}
		}
	})

	delta, err := PlanFactualSnapshotUpdate(context.Background(), previous, current)
	if err != nil {
		t.Fatalf("PlanFactualSnapshotUpdate() error = %v", err)
	}
	changedObserved := previousFacts["observed-a"]
	if len(delta.ObservedReprocessIDs) != 1 || delta.ObservedReprocessIDs[0] == changedObserved.ID {
		t.Fatalf("reprocessed observed IDs = %#v, want one new affected ID", delta.ObservedReprocessIDs)
	}
	if !containsString(delta.ObservedRemovedIDs, changedObserved.ID) {
		t.Fatalf("removed observed IDs = %#v, want previous affected ID %q", delta.ObservedRemovedIDs, changedObserved.ID)
	}
	if len(delta.ObservedReused) != 2 || !containsObservedSubject(delta, previousFacts["observed-b"].ID) || !containsObservedSubject(delta, previousFacts["observed-c"].ID) {
		t.Fatalf("observed reuse = %#v, want only independent observations", delta.ObservedReused)
	}
	if !reflect.DeepEqual(delta.ArtifactChangedPaths, []string{"a.java"}) {
		t.Fatalf("changed artifact paths = %#v", delta.ArtifactChangedPaths)
	}
	wantReevaluate := []string{previousFacts["derived-ab"].ID, previousFacts["derived-ab-final"].ID}
	if !reflect.DeepEqual(sortedIDs(delta.DerivedReevaluateIDs), sortedIDs(wantReevaluate)) {
		t.Fatalf("derived reevaluate IDs = %#v, want %#v", delta.DerivedReevaluateIDs, wantReevaluate)
	}
	if !reflect.DeepEqual(delta.DerivedReusableIDs, []string{previousFacts["derived-c"].ID}) {
		t.Fatalf("derived reusable IDs = %#v, want independent branch", delta.DerivedReusableIDs)
	}
	if delta.FanoutReevaluated != 2 {
		t.Fatalf("FanoutReevaluated = %d, want 2", delta.FanoutReevaluated)
	}
	assertInvalidationReason(t, delta, changedObserved.ID, FactualInvalidationArtifactHashChanged)
	assertInvalidationReason(t, delta, previousFacts["derived-ab"].ID, FactualInvalidationUpstreamLineage)
	assertInvalidationReason(t, delta, previousFacts["derived-ab-final"].ID, FactualInvalidationUpstreamLineage)
	if hasInvalidationReason(delta, previousFacts["derived-c"].ID, FactualInvalidationUpstreamLineage) {
		t.Fatalf("independent derived branch was invalidated: %#v", delta.Invalidations)
	}
}

func TestPlanFactualSnapshotUpdateReprocessesHashOnlyArtifactChanges(t *testing.T) {
	previous, previousFacts := factualIncrementalGraphFixture(t)
	changedArtifacts := append([]contract.Artifact(nil), previous.Artifacts...)
	changedArtifacts[0].Hash = strings.Repeat("f", 64)
	changedArtifacts[0].ID = contract.ArtifactID(changedArtifacts[0].SourceID, changedArtifacts[0].Path, changedArtifacts[0].Hash)
	// The observed fact itself remains semantically identical. Only the
	// artifact hash and the evidence locator's snapshot-scoped artifact ID
	// change in the current revision.
	current := factualIncrementalCurrent(t, previous, "snapshot-current", changedArtifacts, false, nil)

	delta, err := PlanFactualSnapshotUpdate(context.Background(), previous, current)
	if err != nil {
		t.Fatalf("PlanFactualSnapshotUpdate() error = %v", err)
	}
	if len(delta.ObservedReprocessIDs) != 1 || len(delta.ObservedRemovedIDs) != 0 {
		t.Fatalf("hash-only observed classifications = reprocess:%#v removed:%#v", delta.ObservedReprocessIDs, delta.ObservedRemovedIDs)
	}
	if len(delta.ObservedReused) != 2 || !containsObservedSubject(delta, previousFacts["observed-b"].ID) || !containsObservedSubject(delta, previousFacts["observed-c"].ID) {
		t.Fatalf("hash-only observed reuse = %#v, want independent observations", delta.ObservedReused)
	}
	assertInvalidationReason(t, delta, delta.ObservedReprocessIDs[0], FactualInvalidationArtifactHashChanged)
	wantReevaluate := []string{previousFacts["derived-ab"].ID, previousFacts["derived-ab-final"].ID}
	if !reflect.DeepEqual(sortedIDs(delta.DerivedReevaluateIDs), sortedIDs(wantReevaluate)) || !reflect.DeepEqual(delta.DerivedReusableIDs, []string{previousFacts["derived-c"].ID}) {
		t.Fatalf("hash-only derived classifications = reevaluate:%#v reusable:%#v", delta.DerivedReevaluateIDs, delta.DerivedReusableIDs)
	}
	if delta.FanoutReevaluated != 2 {
		t.Fatalf("hash-only FanoutReevaluated = %d, want 2", delta.FanoutReevaluated)
	}
}

func TestPlanFactualSnapshotUpdateTracksArtifactAddAndRemove(t *testing.T) {
	previous, previousFacts := factualIncrementalObservedFixture(t)
	added := factualIncrementalArtifact(previous.Snapshot.Scope, "c.java", "c")
	currentArtifacts := []contract.Artifact{previous.Artifacts[0], added}
	current := factualIncrementalCurrent(t, previous, "snapshot-current", currentArtifacts, false, nil)
	current.Snapshot.Facts = append(current.Snapshot.Facts, factualIncrementalFactForArtifact(t, current.Snapshot.Scope, added, "observed-c", "c"))

	delta, err := PlanFactualSnapshotUpdate(context.Background(), previous, current)
	if err != nil {
		t.Fatalf("PlanFactualSnapshotUpdate() error = %v", err)
	}
	if !reflect.DeepEqual(delta.ArtifactAddedPaths, []string{"c.java"}) || !reflect.DeepEqual(delta.ArtifactRemovedPaths, []string{"b.java"}) || len(delta.ArtifactChangedPaths) != 0 {
		t.Fatalf("artifact delta = added:%#v changed:%#v removed:%#v", delta.ArtifactAddedPaths, delta.ArtifactChangedPaths, delta.ArtifactRemovedPaths)
	}
	if len(delta.ObservedReused) != 1 || delta.ObservedReused[0].PreviousFactID != previousFacts["observed-a"].ID {
		t.Fatalf("observed reuse = %#v, want only a.java", delta.ObservedReused)
	}
	if !containsString(delta.ObservedRemovedIDs, previousFacts["observed-b"].ID) {
		t.Fatalf("removed observed IDs = %#v, want %q", delta.ObservedRemovedIDs, previousFacts["observed-b"].ID)
	}
	if len(delta.ObservedReprocessIDs) != 1 {
		t.Fatalf("reprocessed observed IDs = %#v, want added c.java fact", delta.ObservedReprocessIDs)
	}
	assertInvalidationReason(t, delta, previousFacts["observed-b"].ID, FactualInvalidationArtifactRemoved)
}

func TestPlanFactualSnapshotUpdateInvalidatesFrontendVersionAndBaseManifest(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(*FactualSnapshotRevision, *fact.CanonicalFact)
		wantReason FactualInvalidationReason
	}{
		{
			name: "frontend version",
			mutate: func(current *FactualSnapshotRevision, candidate *fact.CanonicalFact) {
				current.Snapshot.FrontendManifests[0].Version = "2"
				if candidate.Lineage == nil {
					candidate.Producer.Version = "2"
				}
			},
			wantReason: FactualInvalidationFrontendVersionChanged,
		},
		{
			name: "base manifest",
			mutate: func(current *FactualSnapshotRevision, _ *fact.CanonicalFact) {
				current.Snapshot.FrontendManifests[0].Limitations = append(current.Snapshot.FrontendManifests[0].Limitations, "changed-base")
			},
			wantReason: FactualInvalidationFrontendManifestChanged,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			previous, _ := factualIncrementalObservedFixture(t)
			current := factualIncrementalCurrent(t, previous, "snapshot-current", previous.Artifacts, false, func(candidate *fact.CanonicalFact) {
				// The current revision is passed through the closure below after
				// its manifest has been changed.
				_ = candidate
			})
			tt.mutate(&current, &current.Snapshot.Facts[0])
			if tt.name == "frontend version" {
				for index := range current.Snapshot.Facts {
					if current.Snapshot.Facts[index].Lineage == nil {
						current.Snapshot.Facts[index].Producer.Version = "2"
						current.Snapshot.Facts[index].ID = mustFactID(current.Snapshot.Facts[index])
					}
				}
			}
			delta, err := PlanFactualSnapshotUpdate(context.Background(), previous, current)
			if err != nil {
				t.Fatalf("PlanFactualSnapshotUpdate() error = %v", err)
			}
			if len(delta.ObservedReused) != 0 || len(delta.ObservedReprocessIDs) != len(previous.Snapshot.Facts) {
				t.Fatalf("observed classifications = reused:%#v reprocess:%#v", delta.ObservedReused, delta.ObservedReprocessIDs)
			}
			assertInvalidationReason(t, delta, delta.ObservedReprocessIDs[0], tt.wantReason)
		})
	}
}

func TestPlanFactualSnapshotUpdateInvalidatesOnlyExtensionSchemaDigestChange(t *testing.T) {
	previous, _ := factualIncrementalObservedFixture(t)
	previous.Snapshot.FrontendManifests[0].Extensions = []fact.ExtensionSchema{{ID: "java-extra", Version: "1", Digest: fact.ExtensionDigest([]byte("schema-v1"))}}
	current := factualIncrementalCurrent(t, previous, "snapshot-current", previous.Artifacts, false, nil)
	current.Snapshot.FrontendManifests[0].Extensions[0].Digest = fact.ExtensionDigest([]byte("schema-v2"))

	delta, err := PlanFactualSnapshotUpdate(context.Background(), previous, current)
	if err != nil {
		t.Fatalf("PlanFactualSnapshotUpdate() error = %v", err)
	}
	if len(delta.ObservedReused) != 0 || len(delta.ObservedReprocessIDs) != 2 {
		t.Fatalf("observed schema classifications = reused:%#v reprocess:%#v", delta.ObservedReused, delta.ObservedReprocessIDs)
	}
	for _, id := range delta.ObservedReprocessIDs {
		assertInvalidationReason(t, delta, id, FactualInvalidationSchemaDigestChanged)
		if hasInvalidationReason(delta, id, FactualInvalidationFrontendManifestChanged) {
			t.Fatalf("schema-only change also invalidated base manifest for %q: %#v", id, delta.Invalidations)
		}
	}
}

func TestPlanFactualSnapshotUpdateInvalidatesConfigurationID(t *testing.T) {
	previous, _ := factualIncrementalGraphFixture(t)
	current := factualIncrementalCurrent(t, previous, "snapshot-current", previous.Artifacts, false, nil)
	current.ConfigurationID = "configuration-2"

	delta, err := PlanFactualSnapshotUpdate(context.Background(), previous, current)
	if err != nil {
		t.Fatalf("PlanFactualSnapshotUpdate() error = %v", err)
	}
	if len(delta.ObservedReused) != 0 || len(delta.ObservedReprocessIDs) != 3 || delta.FanoutReevaluated != 3 {
		t.Fatalf("configuration delta = %#v", delta)
	}
	for _, id := range delta.ObservedReprocessIDs {
		assertInvalidationReason(t, delta, id, FactualInvalidationConfigurationChanged)
	}
	if len(delta.DerivedReusableIDs) != 0 || len(delta.DerivedReevaluateIDs) != 3 {
		t.Fatalf("configuration derived classifications = reusable:%#v reevaluate:%#v", delta.DerivedReusableIDs, delta.DerivedReevaluateIDs)
	}
	for _, id := range previousDerivedIDs(previous) {
		if !containsString(delta.DerivedReevaluateIDs, id) {
			t.Fatalf("configuration change did not reevaluate derived fact %q", id)
		}
		assertInvalidationReason(t, delta, id, FactualInvalidationConfigurationChanged)
	}
}

func TestPlanFactualSnapshotUpdateInvalidatesRuleVersionImplementationAndConfiguration(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(*RuleVersion)
		reevaluate FactualInvalidationReason
	}{
		{
			name:   "same semantic configuration is reusable",
			mutate: func(rule *RuleVersion) { rule.Configuration = json.RawMessage(`{"z":2,"a":1}`) },
		},
		{
			name:       "rule version",
			mutate:     func(rule *RuleVersion) { rule.Version = "2" },
			reevaluate: FactualInvalidationRuleVersionChanged,
		},
		{
			name:       "implementation digest",
			mutate:     func(rule *RuleVersion) { rule.ImplementationDigest = strings.Repeat("c", 64) },
			reevaluate: FactualInvalidationRuleImplementationOrConfigurationChanged,
		},
		{
			name:       "semantic configuration",
			mutate:     func(rule *RuleVersion) { rule.Configuration = json.RawMessage(`{"z":3,"a":1}`) },
			reevaluate: FactualInvalidationRuleImplementationOrConfigurationChanged,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			previous, _ := factualIncrementalGraphFixture(t)
			current := factualIncrementalCurrent(t, previous, "snapshot-current", previous.Artifacts, false, nil)
			tt.mutate(&current.Snapshot.RuleVersions[0])
			delta, err := PlanFactualSnapshotUpdate(context.Background(), previous, current)
			if err != nil {
				t.Fatalf("PlanFactualSnapshotUpdate() error = %v", err)
			}
			if len(delta.ObservedReused) != 3 || len(delta.ObservedReprocessIDs) != 0 {
				t.Fatalf("observed rule classifications = %#v/%#v", delta.ObservedReused, delta.ObservedReprocessIDs)
			}
			if tt.reevaluate == "" {
				if len(delta.DerivedReusableIDs) != 3 || len(delta.DerivedReevaluateIDs) != 0 || len(delta.Invalidations) != 0 {
					t.Fatalf("canonical-equivalent rule configuration was not reusable: %#v", delta)
				}
				return
			}
			if len(delta.DerivedReusableIDs) != 0 || len(delta.DerivedReevaluateIDs) != 3 || delta.FanoutReevaluated != 3 {
				t.Fatalf("rule delta = %#v", delta)
			}
			for _, id := range delta.DerivedReevaluateIDs {
				assertInvalidationReason(t, delta, id, tt.reevaluate)
			}
		})
	}
}

func TestPlanFactualSnapshotUpdateNeverReusesAmbiguousStableKey(t *testing.T) {
	// A valid canonical snapshot cannot contain two identical semantic facts:
	// FactID is deterministic and PrepareFactualSnapshot rejects duplicate
	// identities. Exercise the planner's ambiguity branch directly as well,
	// using two distinct facts deliberately assigned to one stable-key bucket.
	previous, _ := factualIncrementalObservedFixture(t)
	artifact := previous.Artifacts[0]
	first := factualIncrementalFactForArtifact(t, previous.Snapshot.Scope, artifact, "ambiguous", "one")
	second := factualIncrementalFactForArtifact(t, previous.Snapshot.Scope, artifact, "ambiguous", "two")
	currentScope := previous.Snapshot.Scope
	currentScope.SnapshotID = "snapshot-current"
	currentFact := factualIncrementalFactForArtifact(t, currentScope, artifact, "ambiguous", "one")
	previousFacts := factualIncrementalFacts{
		observed:            []fact.CanonicalFact{first, second},
		observedByStableKey: map[string][]fact.CanonicalFact{"ambiguous": {first, second}},
	}
	currentFacts := factualIncrementalFacts{
		observed:            []fact.CanonicalFact{currentFact},
		observedByStableKey: map[string][]fact.CanonicalFact{"ambiguous": {currentFact}},
	}
	previousRevision := factualIncrementalRevision{scope: previous.Snapshot.Scope, artifactsByPath: map[string]contract.Artifact{"a.java": artifact}}
	currentRevision := factualIncrementalRevision{scope: currentScope, artifactsByPath: map[string]contract.Artifact{"a.java": artifact}}
	delta := FactualSnapshotDelta{}
	outputInvalidations := make(factualIncrementalReasons)
	previousInvalidations := make(factualIncrementalReasons)
	if err := classifyFactualObserved(context.Background(), previousRevision, currentRevision, previousFacts, currentFacts, &delta, outputInvalidations, previousInvalidations); err != nil {
		t.Fatalf("classifyFactualObserved() error = %v", err)
	}
	if len(delta.ObservedReused) != 0 {
		t.Fatalf("ambiguous stable key was reused: %#v", delta.ObservedReused)
	}
	if len(delta.ObservedReprocessIDs) != 1 || len(delta.ObservedRemovedIDs) != 2 {
		t.Fatalf("ambiguous classifications = reprocess:%#v removed:%#v", delta.ObservedReprocessIDs, delta.ObservedRemovedIDs)
	}
}

func TestPlanFactualSnapshotUpdateIsDeterministicDefensiveAndCloneSafe(t *testing.T) {
	previous, _ := factualIncrementalGraphFixture(t)
	current := factualIncrementalCurrent(t, previous, "snapshot-current", previous.Artifacts, false, nil)
	previousBefore := cloneFactualIncrementalRevision(previous)
	currentBefore := cloneFactualIncrementalRevision(current)

	first, err := PlanFactualSnapshotUpdate(context.Background(), previous, current)
	if err != nil {
		t.Fatalf("first PlanFactualSnapshotUpdate() error = %v", err)
	}
	permutedPrevious := cloneFactualIncrementalRevision(previous)
	permutedCurrent := cloneFactualIncrementalRevision(current)
	sort.Slice(permutedPrevious.Snapshot.FrontendManifests, func(left, right int) bool {
		return permutedPrevious.Snapshot.FrontendManifests[left].ID > permutedPrevious.Snapshot.FrontendManifests[right].ID
	})
	sort.Slice(permutedPrevious.Snapshot.RuleVersions, func(left, right int) bool {
		return permutedPrevious.Snapshot.RuleVersions[left].RuleID > permutedPrevious.Snapshot.RuleVersions[right].RuleID
	})
	sort.Slice(permutedPrevious.Snapshot.Facts, func(left, right int) bool {
		return permutedPrevious.Snapshot.Facts[left].ID > permutedPrevious.Snapshot.Facts[right].ID
	})
	sort.Slice(permutedCurrent.Snapshot.Facts, func(left, right int) bool {
		return permutedCurrent.Snapshot.Facts[left].ID > permutedCurrent.Snapshot.Facts[right].ID
	})
	sort.Slice(permutedCurrent.Artifacts, func(left, right int) bool {
		return permutedCurrent.Artifacts[left].Path > permutedCurrent.Artifacts[right].Path
	})
	second, err := PlanFactualSnapshotUpdate(context.Background(), permutedPrevious, permutedCurrent)
	if err != nil {
		t.Fatalf("permuted PlanFactualSnapshotUpdate() error = %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("permutation changed delta:\nfirst=%#v\nsecond=%#v", first, second)
	}
	if !reflect.DeepEqual(previous, previousBefore) || !reflect.DeepEqual(current, currentBefore) {
		t.Fatal("PlanFactualSnapshotUpdate() mutated caller inputs")
	}

	clone := first.Clone()
	if !reflect.DeepEqual(first, clone) {
		t.Fatalf("Clone() changed delta: first=%#v clone=%#v", first, clone)
	}
	if len(clone.Invalidations) > 0 {
		clone.Invalidations[0].Reasons[0] = "mutated"
		if first.Invalidations[0].Reasons[0] == "mutated" {
			t.Fatal("Clone() shared invalidation reasons")
		}
	}
	if len(clone.ObservedReused) > 0 {
		clone.ObservedReused[0].PreviousFactID = "mutated"
		if first.ObservedReused[0].PreviousFactID == "mutated" {
			t.Fatal("Clone() shared reuse pairs")
		}
	}
	withInvalidation := first.Clone()
	withInvalidation.ObservedReprocessIDs = []string{"observed"}
	withInvalidation.Invalidations = []FactualInvalidation{{FactID: "observed", Reasons: []FactualInvalidationReason{FactualInvalidationConfigurationChanged}}}
	clonedInvalidation := withInvalidation.Clone()
	clonedInvalidation.Invalidations[0].Reasons[0] = FactualInvalidationSchemaDigestChanged
	if withInvalidation.Invalidations[0].Reasons[0] == FactualInvalidationSchemaDigestChanged {
		t.Fatal("Clone() shared invalidation reason storage")
	}
}

func TestPlanFactualSnapshotUpdateRejectsCancellationScopeAndMalformedInputsSafely(t *testing.T) {
	previous, _ := factualIncrementalObservedFixture(t)
	current := factualIncrementalCurrent(t, previous, "snapshot-current", previous.Artifacts, false, nil)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := PlanFactualSnapshotUpdate(cancelled, previous, current); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled plan error = %v, want context.Canceled", err)
	}
	if _, err := PlanFactualSnapshotUpdate(nil, previous, current); !errors.Is(err, ErrInvalidFactualIncremental) {
		t.Fatalf("nil-context plan error = %v, want ErrInvalidFactualIncremental", err)
	}

	tests := []struct {
		name   string
		mutate func(*FactualSnapshotRevision, *FactualSnapshotRevision)
	}{
		{
			name: "same snapshot",
			mutate: func(left, right *FactualSnapshotRevision) {
				right.Snapshot.Scope.SnapshotID = left.Snapshot.Scope.SnapshotID
				right.Snapshot.SnapshotID = left.Snapshot.SnapshotID
				for index := range right.Snapshot.Facts {
					right.Snapshot.Facts[index].Scope = left.Snapshot.Scope
					right.Snapshot.Facts[index].ID = mustFactID(right.Snapshot.Facts[index])
				}
			},
		},
		{
			name:   "invalid previous scope",
			mutate: func(left, _ *FactualSnapshotRevision) { left.Snapshot.Scope.SnapshotID = "" },
		},
		{
			name: "malformed artifact",
			mutate: func(_, right *FactualSnapshotRevision) {
				const secret = "sensitive-incremental-path"
				right.Artifacts[0].Path = "../" + secret
				right.Artifacts[0].SourceID = "wrong-" + secret
				right.Artifacts[0].ID = contract.ArtifactID(right.Artifacts[0].SourceID, right.Artifacts[0].Path, right.Artifacts[0].Hash)
			},
		},
		{
			name: "missing derived lineage",
			mutate: func(left, _ *FactualSnapshotRevision) {
				left.Snapshot.Facts = append(left.Snapshot.Facts, fact.CanonicalFact{})
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			left := cloneFactualIncrementalRevision(previous)
			right := cloneFactualIncrementalRevision(current)
			tt.mutate(&left, &right)
			_, err := PlanFactualSnapshotUpdate(context.Background(), left, right)
			if err == nil || !errors.Is(err, ErrInvalidFactualIncremental) {
				t.Fatalf("error = %v, want sanitized ErrInvalidFactualIncremental", err)
			}
			if strings.Contains(err.Error(), "sensitive-incremental-path") {
				t.Fatalf("error leaked caller input: %v", err)
			}
		})
	}
}

func TestFactualSnapshotDeltaValidateRejectsUnclassifiedInvalidationAndFanoutMismatch(t *testing.T) {
	previous, _ := factualIncrementalObservedFixture(t)
	current := factualIncrementalCurrent(t, previous, "snapshot-current", previous.Artifacts, false, nil)
	base := FactualSnapshotDelta{PreviousScope: previous.Snapshot.Scope, CurrentScope: current.Snapshot.Scope}
	tests := []struct {
		name   string
		mutate func(*FactualSnapshotDelta)
	}{
		{
			name: "invalidation without classification",
			mutate: func(delta *FactualSnapshotDelta) {
				delta.Invalidations = []FactualInvalidation{{FactID: "missing", Reasons: []FactualInvalidationReason{FactualInvalidationObservedChangedOrMissing}}}
			},
		},
		{
			name: "fanout mismatch",
			mutate: func(delta *FactualSnapshotDelta) {
				delta.DerivedReevaluateIDs = []string{"derived"}
				delta.FanoutReevaluated = 0
			},
		},
		{
			name: "unknown invalidation reason",
			mutate: func(delta *FactualSnapshotDelta) {
				delta.ObservedReprocessIDs = []string{"observed"}
				delta.Invalidations = []FactualInvalidation{{FactID: "observed", Reasons: []FactualInvalidationReason{"unknown"}}}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidate := base.Clone()
			tt.mutate(&candidate)
			if err := candidate.Validate(); !errors.Is(err, ErrInvalidFactualIncremental) {
				t.Fatalf("Validate() error = %v, want ErrInvalidFactualIncremental", err)
			}
		})
	}
}

type factualIncrementalFixtureFacts map[string]fact.CanonicalFact

func factualIncrementalGraphFixture(t *testing.T) (FactualSnapshotRevision, factualIncrementalFixtureFacts) {
	t.Helper()
	scope := fact.Scope{OrganizationID: "incremental-org", SourceID: "incremental-source", SnapshotID: "snapshot-previous"}
	artifacts := []contract.Artifact{
		factualIncrementalArtifact(scope, "a.java", "a"),
		factualIncrementalArtifact(scope, "b.java", "b"),
		factualIncrementalArtifact(scope, "c.java", "c"),
	}
	facts := make(factualIncrementalFixtureFacts)
	facts["observed-a"] = factualIncrementalFactForArtifact(t, scope, artifacts[0], "observed-a", "a")
	facts["observed-b"] = factualIncrementalFactForArtifact(t, scope, artifacts[1], "observed-b", "b")
	facts["observed-c"] = factualIncrementalFactForArtifact(t, scope, artifacts[2], "observed-c", "c")
	facts["derived-ab"] = factualIncrementalDerived(t, scope, "derived-ab", artifacts[0], facts["observed-a"].ID, facts["observed-b"].ID)
	facts["derived-ab-final"] = factualIncrementalDerived(t, scope, "derived-ab-final", artifacts[0], facts["derived-ab"].ID)
	facts["derived-c"] = factualIncrementalDerived(t, scope, "derived-c", artifacts[2], facts["observed-c"].ID)
	ordered := []fact.CanonicalFact{
		facts["derived-c"], facts["observed-b"], facts["derived-ab-final"], facts["observed-c"], facts["derived-ab"], facts["observed-a"],
	}
	rules := []RuleVersion{{RuleID: "dependency", Version: "1", ImplementationDigest: strings.Repeat("a", 64), Configuration: json.RawMessage(`{"a":1,"z":2}`)}}
	revision := factualIncrementalRevisionWith(t, scope, artifacts, ordered, "configuration-1", rules, nil)
	return revision, facts
}

func factualIncrementalObservedFixture(t *testing.T) (FactualSnapshotRevision, factualIncrementalFixtureFacts) {
	t.Helper()
	scope := fact.Scope{OrganizationID: "incremental-org", SourceID: "incremental-source", SnapshotID: "snapshot-previous"}
	artifacts := []contract.Artifact{
		factualIncrementalArtifact(scope, "a.java", "a"),
		factualIncrementalArtifact(scope, "b.java", "b"),
	}
	facts := factualIncrementalFixtureFacts{
		"observed-a": factualIncrementalFactForArtifact(t, scope, artifacts[0], "observed-a", "a"),
		"observed-b": factualIncrementalFactForArtifact(t, scope, artifacts[1], "observed-b", "b"),
	}
	rules := []RuleVersion{{RuleID: "dependency", Version: "1", ImplementationDigest: strings.Repeat("a", 64), Configuration: json.RawMessage(`{"a":1,"z":2}`)}}
	return factualIncrementalRevisionWith(t, scope, artifacts, []fact.CanonicalFact{facts["observed-b"], facts["observed-a"]}, "configuration-1", rules, nil), facts
}

func factualIncrementalCurrent(t *testing.T, previous FactualSnapshotRevision, snapshotID string, artifacts []contract.Artifact, includeDerived bool, mutate func(*fact.CanonicalFact)) FactualSnapshotRevision {
	t.Helper()
	scope := previous.Snapshot.Scope
	scope.SnapshotID = snapshotID
	input := cloneFactualSnapshotInput(previous.Snapshot)
	input.Scope = scope
	input.SnapshotID = identity.CanonicalUUID("snapshot", scope.OrganizationID, scope.SourceID, scope.SnapshotID)
	input.Facts = nil
	oldToNew := make(map[string]string, len(previous.Snapshot.Facts))
	for _, candidate := range previous.Snapshot.Facts {
		if candidate.Lineage != nil {
			continue
		}
		if !factualIncrementalHasArtifactPath(artifacts, candidate) {
			continue
		}
		cloned := factualIncrementalFactForRevision(t, candidate, scope, artifacts, nil)
		if mutate != nil {
			mutate(&cloned)
			cloned.ID = mustFactID(cloned)
		}
		input.Facts = append(input.Facts, cloned)
		oldToNew[candidate.ID] = cloned.ID
	}
	if includeDerived {
		for _, candidate := range previous.Snapshot.Facts {
			if candidate.Lineage == nil {
				continue
			}
			cloned := factualIncrementalFactForRevision(t, candidate, scope, artifacts, oldToNew)
			input.Facts = append(input.Facts, cloned)
			oldToNew[candidate.ID] = cloned.ID
		}
	}
	return FactualSnapshotRevision{Snapshot: input, Artifacts: cloneArtifacts(artifacts), ConfigurationID: previous.ConfigurationID}
}

func factualIncrementalHasArtifactPath(artifacts []contract.Artifact, candidate fact.CanonicalFact) bool {
	for _, evidence := range candidate.Evidence {
		for _, artifact := range artifacts {
			if evidence.Locator.Path == artifact.Path {
				return true
			}
		}
	}
	return false
}

func factualIncrementalRevisionWith(t *testing.T, scope fact.Scope, artifacts []contract.Artifact, facts []fact.CanonicalFact, configurationID string, rules []RuleVersion, manifests []fact.FrontendManifest) FactualSnapshotRevision {
	t.Helper()
	if manifests == nil {
		manifests = []fact.FrontendManifest{validFrontendManifest("java", "1", "symbols")}
	}
	input := FactualSnapshotInput{
		OrganizationID:    identity.CanonicalUUID("organization", scope.OrganizationID),
		SourceID:          identity.CanonicalUUID("source", scope.OrganizationID, scope.SourceID),
		SnapshotID:        identity.CanonicalUUID("snapshot", scope.OrganizationID, scope.SourceID, scope.SnapshotID),
		Scope:             scope,
		FrontendManifests: cloneFrontendManifestsForIncremental(manifests),
		RuleVersions:      cloneRuleVersionsForIncremental(rules),
		Facts:             append([]fact.CanonicalFact(nil), facts...),
	}
	return FactualSnapshotRevision{Snapshot: input, Artifacts: cloneArtifacts(artifacts), ConfigurationID: configurationID}
}

func factualIncrementalFactForArtifact(t *testing.T, scope fact.Scope, artifact contract.Artifact, subjectID, value string) fact.CanonicalFact {
	t.Helper()
	candidate := validFact(scope, subjectID, javaProducer(), nil)
	candidate.Value = &fact.TypedValue{Kind: fact.ValueString, String: value}
	candidate.Evidence = []fact.EvidenceRef{{ID: "evidence-" + subjectID, Locator: contract.Locator{SourceID: scope.SourceID, ArtifactID: artifact.ID, Path: artifact.Path, StartLine: 1, EndLine: 1}}}
	candidate.ID = mustFactID(candidate)
	return candidate
}

func factualIncrementalDerived(t *testing.T, scope fact.Scope, subjectID string, artifact contract.Artifact, inputs ...string) fact.CanonicalFact {
	t.Helper()
	candidate := validFact(scope, subjectID, fact.Producer{ID: "rule-engine", Version: "1", Method: "dependency"}, &fact.Lineage{RuleID: "dependency", RuleVersion: "1", InputFactIDs: append([]string(nil), inputs...)})
	candidate.Evidence = []fact.EvidenceRef{{ID: "evidence-" + subjectID, Locator: contract.Locator{SourceID: scope.SourceID, ArtifactID: artifact.ID, Path: artifact.Path, StartLine: 1, EndLine: 1}}}
	candidate.ID = mustFactID(candidate)
	return candidate
}

func factualIncrementalFactForRevision(t *testing.T, candidate fact.CanonicalFact, scope fact.Scope, artifacts []contract.Artifact, oldToNew map[string]string) fact.CanonicalFact {
	t.Helper()
	cloned := cloneCanonicalFact(candidate)
	cloned.Scope = scope
	for index := range cloned.Evidence {
		cloned.Evidence[index].Locator.SourceID = scope.SourceID
		for _, artifact := range artifacts {
			if artifact.Path == cloned.Evidence[index].Locator.Path {
				cloned.Evidence[index].Locator.ArtifactID = artifact.ID
				break
			}
		}
	}
	if cloned.Lineage != nil && oldToNew != nil {
		for index, inputID := range cloned.Lineage.InputFactIDs {
			if replacement, exists := oldToNew[inputID]; exists {
				cloned.Lineage.InputFactIDs[index] = replacement
			}
		}
	}
	cloned.ID = mustFactID(cloned)
	return cloned
}

func factualIncrementalArtifact(scope fact.Scope, path, hashValue string) contract.Artifact {
	hash := strings.Repeat(hashValue, 64)
	artifact := contract.Artifact{SourceID: scope.SourceID, Path: path, Type: "java", Hash: hash, Size: 1}
	artifact.ID = contract.ArtifactID(artifact.SourceID, artifact.Path, artifact.Hash)
	return artifact
}

func cloneFactualIncrementalRevision(input FactualSnapshotRevision) FactualSnapshotRevision {
	clone := input
	clone.Snapshot = cloneFactualSnapshotInput(input.Snapshot)
	clone.Artifacts = cloneArtifacts(input.Artifacts)
	return clone
}

func cloneArtifacts(input []contract.Artifact) []contract.Artifact {
	return append([]contract.Artifact(nil), input...)
}

func cloneFrontendManifestsForIncremental(input []fact.FrontendManifest) []fact.FrontendManifest {
	result := make([]fact.FrontendManifest, len(input))
	for index, manifest := range input {
		canonical, err := fact.CanonicalFrontendManifest(manifest)
		if err == nil {
			result[index] = canonical
		} else {
			result[index] = manifest
		}
	}
	return result
}

func cloneRuleVersionsForIncremental(input []RuleVersion) []RuleVersion {
	result := make([]RuleVersion, len(input))
	for index, rule := range input {
		result[index] = rule
		result[index].Configuration = append(json.RawMessage(nil), rule.Configuration...)
	}
	return result
}

func previousDerivedIDs(revision FactualSnapshotRevision) []string {
	result := make([]string, 0)
	for _, candidate := range revision.Snapshot.Facts {
		if candidate.Lineage != nil {
			result = append(result, candidate.ID)
		}
	}
	return result
}

func sortedIDs(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	return result
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func containsObservedSubject(delta FactualSnapshotDelta, previousID string) bool {
	for _, pair := range delta.ObservedReused {
		if pair.PreviousFactID == previousID {
			return true
		}
	}
	return false
}

func assertInvalidationReason(t *testing.T, delta FactualSnapshotDelta, factID string, want FactualInvalidationReason) {
	t.Helper()
	if !hasInvalidationReason(delta, factID, want) {
		t.Fatalf("invalidation for %q lacks %q: %#v", factID, want, delta.Invalidations)
	}
}

func hasInvalidationReason(delta FactualSnapshotDelta, factID string, want FactualInvalidationReason) bool {
	for _, invalidation := range delta.Invalidations {
		if invalidation.FactID != factID {
			continue
		}
		for _, reason := range invalidation.Reasons {
			if reason == want {
				return true
			}
		}
	}
	return false
}
