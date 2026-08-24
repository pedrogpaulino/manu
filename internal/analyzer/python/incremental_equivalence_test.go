package python

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
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

const (
	pythonIncrementalOrganization = "organization-python-incremental"
	pythonIncrementalSource       = "source-python-integration"
	pythonIncrementalRuleDigest   = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

func TestPythonFactualIncrementalEquivalenceAgainstCompleteRebuild(t *testing.T) {
	previousRoot := filepath.Join("testdata", "frappe17")
	unchangedRoot := pythonIncrementalFixtureRoot(t, false)
	changedRoot := pythonIncrementalFixtureRoot(t, true)

	previous := pythonIncrementalRevisionForTest(t, previousRoot, "snapshot-python-incremental-previous")
	unchanged := pythonIncrementalRevisionForTest(t, unchangedRoot, "snapshot-python-incremental-unchanged")
	changed := pythonIncrementalRevisionForTest(t, changedRoot, "snapshot-python-incremental-changed")
	previous.revision.Snapshot.Facts = clonePythonFacts(previous.complete)

	t.Run("sem alteração reutiliza o snapshot completo", func(t *testing.T) {
		assertPythonIncrementalScenario(t, previous, unchanged, false)
	})
	t.Run("alteração localizada reprocessa somente o fanout", func(t *testing.T) {
		assertPythonIncrementalScenario(t, previous, changed, true)
	})
}

type pythonIncrementalRevision struct {
	revision persistence.FactualSnapshotRevision
	observed []fact.CanonicalFact
	complete []fact.CanonicalFact
}

func pythonIncrementalRevisionForTest(t *testing.T, rootPath, snapshotID string) pythonIncrementalRevision {
	t.Helper()

	manifest := Manifest()
	registry := pythonRegistry(t, manifest)
	scope := fact.Scope{
		OrganizationID: pythonIncrementalOrganization,
		SourceID:       pythonIncrementalSource,
		SnapshotID:     snapshotID,
	}

	fixtureNames := []string{"doctype.py", "hooks.py"}
	artifacts := make([]contract.Artifact, 0, len(fixtureNames))
	inputs := make([]normalization.Input, 0, 32)
	for _, fixtureName := range fixtureNames {
		artifact, analyzed := analyzePythonIncrementalFixture(t, rootPath, fixtureName, scope.SourceID)
		artifacts = append(artifacts, artifact)

		drafts := draftsByPythonContribution(analyzed.Evidence)
		for _, contribution := range analyzed.Contributions {
			if _, supported := pythonIntegrationDimensions[contribution.Type]; !supported {
				continue
			}
			_, ok := drafts[contribution.ID]
			if !ok {
				t.Fatalf("contribution %q has no evidence draft", contribution.ID)
			}
			inputs = append(inputs, normalization.Input{
				Scope:        scope,
				Manifest:     clonePythonManifest(manifest),
				Contribution: contribution,
				Evidence: []fact.EvidenceRef{{
					ID: "evidence-" + contribution.ID,
					Locator: contract.Locator{
						SourceID:    scope.SourceID,
						ArtifactID:  contribution.ArtifactID,
						Path:        contribution.Locator.Path,
						Member:      contribution.Locator.Member,
						StartLine:   contribution.Locator.StartLine,
						StartColumn: contribution.Locator.StartColumn,
						EndLine:     contribution.Locator.EndLine,
						EndColumn:   contribution.Locator.EndColumn,
						ByteOffset:  contribution.Locator.ByteOffset,
						ByteLength:  contribution.Locator.ByteLength,
					},
				}},
			})
		}
	}

	normalized, err := registry.NormalizeAll(context.Background(), inputs)
	if err != nil {
		t.Fatalf("NormalizeAll(%q) error: %v", snapshotID, err)
	}
	if len(normalized.Facts) == 0 {
		t.Fatalf("NormalizeAll(%q) produced no facts", snapshotID)
	}

	complete := pythonIncrementalCompleteFacts(t, normalized.Facts)
	ruleVersions := []persistence.RuleVersion{{
		RuleID:               derivation.MembershipRuleID,
		Version:              derivation.MembershipRuleVersion,
		ImplementationDigest: pythonIncrementalRuleDigest,
		Configuration:        json.RawMessage(`{}`),
	}}
	input := persistence.FactualSnapshotInput{
		OrganizationID:    identity.CanonicalUUID("organization", scope.OrganizationID),
		SourceID:          identity.CanonicalUUID("source", scope.OrganizationID, scope.SourceID),
		SnapshotID:        identity.CanonicalUUID("snapshot", scope.OrganizationID, scope.SourceID, scope.SnapshotID),
		Scope:             scope,
		FrontendManifests: []fact.FrontendManifest{clonePythonManifest(manifest)},
		RuleVersions:      clonePythonRuleVersions(ruleVersions),
		Facts:             clonePythonFacts(normalized.Facts),
	}
	return pythonIncrementalRevision{
		revision: persistence.FactualSnapshotRevision{
			Snapshot:        input,
			Artifacts:       append([]contract.Artifact(nil), artifacts...),
			ConfigurationID: "python-safe-static-v1",
		},
		observed: clonePythonFacts(normalized.Facts),
		complete: clonePythonFacts(complete),
	}
}

func assertPythonIncrementalScenario(t *testing.T, previous, current pythonIncrementalRevision, localizedChange bool) {
	t.Helper()

	previousBefore := clonePythonIncrementalRevision(previous.revision)
	currentBefore := clonePythonIncrementalRevision(current.revision)
	delta, err := persistence.PlanFactualSnapshotUpdate(context.Background(), previous.revision, pythonObservedRevision(current.revision, current.observed))
	if err != nil {
		t.Fatalf("PlanFactualSnapshotUpdate() error: %v", err)
	}
	if !reflect.DeepEqual(previous.revision, previousBefore) || !reflect.DeepEqual(current.revision, currentBefore) {
		t.Fatal("PlanFactualSnapshotUpdate() mutated its revisions")
	}
	if err := delta.Validate(); err != nil {
		t.Fatalf("delta.Validate() error: %v", err)
	}
	repeated, err := persistence.PlanFactualSnapshotUpdate(context.Background(), previous.revision, pythonObservedRevision(current.revision, current.observed))
	if err != nil {
		t.Fatalf("repeated PlanFactualSnapshotUpdate() error: %v", err)
	}
	if !reflect.DeepEqual(delta, repeated) {
		t.Fatalf("PlanFactualSnapshotUpdate() is not deterministic:\nfirst=%#v\nrepeated=%#v", delta, repeated)
	}

	if !localizedChange {
		if len(delta.ArtifactAddedPaths) != 0 || len(delta.ArtifactChangedPaths) != 0 || len(delta.ArtifactRemovedPaths) != 0 {
			t.Fatalf("unchanged artifact classification = %#v/%#v/%#v", delta.ArtifactAddedPaths, delta.ArtifactChangedPaths, delta.ArtifactRemovedPaths)
		}
		if len(delta.ObservedReprocessIDs) != 0 || len(delta.ObservedRemovedIDs) != 0 || len(delta.DerivedReevaluateIDs) != 0 {
			t.Fatalf("unchanged invalidation classification = %#v/%#v/%#v", delta.ObservedReprocessIDs, delta.ObservedRemovedIDs, delta.DerivedReevaluateIDs)
		}
		if len(delta.ObservedReused) != len(current.observed) || len(delta.DerivedReusableIDs) != len(previous.complete)-len(previous.observed) {
			t.Fatalf("unchanged reuse = observed %d/%d derived %d/%d", len(delta.ObservedReused), len(current.observed), len(delta.DerivedReusableIDs), len(previous.complete)-len(previous.observed))
		}
	} else {
		if !reflect.DeepEqual(delta.ArtifactChangedPaths, []string{"doctype.py"}) {
			t.Fatalf("localized changed paths = %#v, want [doctype.py]", delta.ArtifactChangedPaths)
		}
		if len(delta.ArtifactAddedPaths) != 0 || len(delta.ArtifactRemovedPaths) != 0 {
			t.Fatalf("localized added/removed paths = %#v/%#v", delta.ArtifactAddedPaths, delta.ArtifactRemovedPaths)
		}
		assertPythonIncrementalLocalizedClassification(t, previous, current, delta)
	}

	incremental := pythonApplyIncrementalPlan(t, previous, current, delta)
	if err := assertPythonFactSetEquivalent(t, incremental, current.complete); err != nil {
		t.Fatal(err)
	}
	assertPythonFactsNoSemanticDuplicates(t, incremental)
	if len(delta.Invalidations) != len(delta.ObservedReprocessIDs)+len(delta.ObservedRemovedIDs)+len(delta.DerivedReevaluateIDs) {
		t.Fatalf("invalidation classification is incomplete: invalidations=%d observed_reprocess=%d observed_removed=%d derived_reevaluate=%d", len(delta.Invalidations), len(delta.ObservedReprocessIDs), len(delta.ObservedRemovedIDs), len(delta.DerivedReevaluateIDs))
	}
	t.Logf("Python incremental localized=%t: observed reutilizados=%d, observados reprocessados=%d, observados removidos=%d, derivados reutilizados=%d, fanout reavaliado=%d, invalidações=%d", localizedChange, len(delta.ObservedReused), len(delta.ObservedReprocessIDs), len(delta.ObservedRemovedIDs), len(delta.DerivedReusableIDs), delta.FanoutReevaluated, len(delta.Invalidations))
}

func assertPythonIncrementalLocalizedClassification(t *testing.T, previous, current pythonIncrementalRevision, delta persistence.FactualSnapshotDelta) {
	t.Helper()

	currentFacts := make(map[string]fact.CanonicalFact, len(current.observed))
	for _, candidate := range current.observed {
		currentFacts[candidate.ID] = candidate
	}
	previousFacts := make(map[string]fact.CanonicalFact, len(previous.complete))
	for _, candidate := range previous.complete {
		previousFacts[candidate.ID] = candidate
	}
	for _, reuse := range delta.ObservedReused {
		candidate, ok := currentFacts[reuse.CurrentFactID]
		if !ok || !pythonFactEvidencePath(candidate, "hooks.py") {
			t.Fatalf("observed reuse is not isolated to unchanged hooks.py: %#v", reuse)
		}
	}
	for _, id := range delta.ObservedReprocessIDs {
		candidate, ok := currentFacts[id]
		if !ok || !pythonFactEvidencePath(candidate, "doctype.py") {
			t.Fatalf("observed reprocess is not isolated to changed doctype.py: %q", id)
		}
	}
	for _, id := range delta.ObservedRemovedIDs {
		candidate, ok := previousFacts[id]
		if !ok || !pythonFactEvidencePath(candidate, "doctype.py") {
			t.Fatalf("observed removal is not isolated to changed doctype.py: %q", id)
		}
	}
	previousDerived := 0
	for _, candidate := range previous.complete {
		if candidate.Lineage == nil {
			continue
		}
		previousDerived++
		if !pythonFactEvidencePath(candidate, "doctype.py") {
			t.Fatalf("membership derived fact unexpectedly depends on hooks.py: %q", candidate.ID)
		}
	}
	if previousDerived == 0 {
		t.Fatal("membership rule produced no derived facts for Python definitions")
	}
	if len(delta.DerivedReusableIDs) != 0 || len(delta.DerivedReevaluateIDs) != previousDerived || delta.FanoutReevaluated != previousDerived {
		t.Fatalf("localized derived fanout = reusable %d reevaluate %d fanout %d, want 0/%d/%d", len(delta.DerivedReusableIDs), len(delta.DerivedReevaluateIDs), delta.FanoutReevaluated, previousDerived, previousDerived)
	}
}

func pythonObservedRevision(revision persistence.FactualSnapshotRevision, observed []fact.CanonicalFact) persistence.FactualSnapshotRevision {
	result := clonePythonIncrementalRevision(revision)
	result.Snapshot.Facts = clonePythonFacts(observed)
	return result
}

func pythonApplyIncrementalPlan(t *testing.T, previous, current pythonIncrementalRevision, delta persistence.FactualSnapshotDelta) []fact.CanonicalFact {
	t.Helper()

	result := clonePythonFacts(current.observed)
	previousByID := make(map[string]fact.CanonicalFact, len(previous.complete))
	for _, candidate := range previous.complete {
		previousByID[candidate.ID] = candidate
	}
	previousNeutral := pythonNeutralizeSnapshot(t, previous.complete, current.revision.Snapshot.Scope)
	previousNeutralByID := make(map[string]fact.CanonicalFact, len(previous.complete))
	for index, candidate := range previous.complete {
		previousNeutralByID[candidate.ID] = previousNeutral[index]
	}
	for _, previousID := range delta.DerivedReusableIDs {
		candidate, ok := previousByID[previousID]
		if !ok || candidate.Lineage == nil {
			t.Fatalf("reusable derived fact %q is missing from previous complete rebuild", previousID)
		}
		neutral, ok := previousNeutralByID[previousID]
		if !ok {
			t.Fatalf("reusable derived fact %q is missing after snapshot neutralization", previousID)
		}
		result = append(result, neutral)
	}
	if delta.FanoutReevaluated > 0 {
		currentDerived := make([]fact.CanonicalFact, 0, len(current.complete))
		for _, candidate := range current.complete {
			if candidate.Lineage != nil {
				currentDerived = append(currentDerived, candidate)
			}
		}
		if len(currentDerived) != delta.FanoutReevaluated {
			t.Fatalf("complete rebuild derived count = %d, want planner fanout %d", len(currentDerived), delta.FanoutReevaluated)
		}
		result = append(result, clonePythonFacts(currentDerived)...)
	}
	sort.Slice(result, func(left, right int) bool { return result[left].ID < result[right].ID })
	return result
}

func assertPythonFactSetEquivalent(t *testing.T, got, want []fact.CanonicalFact) error {
	t.Helper()
	gotNeutral := pythonNeutralizeSnapshot(t, got, want[0].Scope)
	wantNeutral := pythonNeutralizeSnapshot(t, want, want[0].Scope)
	gotNeutral = pythonCanonicalizeFactsForComparison(gotNeutral)
	wantNeutral = pythonCanonicalizeFactsForComparison(wantNeutral)
	sort.Slice(gotNeutral, func(left, right int) bool { return gotNeutral[left].ID < gotNeutral[right].ID })
	sort.Slice(wantNeutral, func(left, right int) bool { return wantNeutral[left].ID < wantNeutral[right].ID })
	if !reflect.DeepEqual(gotNeutral, wantNeutral) {
		return &pythonIncrementalEquivalenceError{got: gotNeutral, want: wantNeutral}
	}
	return nil
}

func pythonCanonicalizeFactsForComparison(values []fact.CanonicalFact) []fact.CanonicalFact {
	result := clonePythonFacts(values)
	for index := range result {
		sort.SliceStable(result[index].Qualifiers, func(left, right int) bool {
			return result[index].Qualifiers[left].Name < result[index].Qualifiers[right].Name
		})
		sort.SliceStable(result[index].Evidence, func(left, right int) bool {
			return result[index].Evidence[left].ID < result[index].Evidence[right].ID
		})
		if result[index].Lineage != nil {
			sort.Strings(result[index].Lineage.InputFactIDs)
		}
	}
	sort.Slice(result, func(left, right int) bool { return result[left].ID < result[right].ID })
	return result
}

type pythonIncrementalEquivalenceError struct {
	got  []fact.CanonicalFact
	want []fact.CanonicalFact
}

func (e *pythonIncrementalEquivalenceError) Error() string {
	for index := 0; index < len(e.got) && index < len(e.want); index++ {
		if !reflect.DeepEqual(e.got[index], e.want[index]) {
			return fmt.Sprintf("incremental factual result differs from complete rebuild after snapshot-only neutralization at index %d: got=%#v want=%#v", index, e.got[index], e.want[index])
		}
	}
	return fmt.Sprintf("incremental factual result differs from complete rebuild after snapshot-only neutralization: got=%d want=%d", len(e.got), len(e.want))
}

func assertPythonFactsNoSemanticDuplicates(t *testing.T, facts []fact.CanonicalFact) {
	t.Helper()
	seen := make(map[string]struct{}, len(facts))
	for _, candidate := range facts {
		neutral := candidate
		neutral.Scope.SnapshotID = "snapshot-python-incremental-semantic-check"
		neutral.ID = ""
		key, err := fact.IdentityDigest(neutral)
		if err != nil {
			t.Fatalf("IdentityDigest(%q) error: %v", candidate.ID, err)
		}
		if _, exists := seen[key]; exists {
			t.Fatalf("semantic duplicate in incremental result: %q", candidate.ID)
		}
		seen[key] = struct{}{}
	}
}

func pythonFactEvidencePath(candidate fact.CanonicalFact, expected string) bool {
	if len(candidate.Evidence) == 0 {
		return false
	}
	for _, evidence := range candidate.Evidence {
		if evidence.Locator.Path != expected {
			return false
		}
	}
	return true
}

func pythonIncrementalCompleteFacts(t *testing.T, observed []fact.CanonicalFact) []fact.CanonicalFact {
	t.Helper()
	ruleRegistry, err := derivation.NewRegistry(derivation.MembershipRuleRegistration())
	if err != nil {
		t.Fatalf("NewRegistry(membership) error: %v", err)
	}
	executor, err := derivation.NewExecutor(ruleRegistry)
	if err != nil {
		t.Fatalf("NewExecutor(membership) error: %v", err)
	}
	complete, err := executor.Derive(context.Background(), observed)
	if err != nil {
		t.Fatalf("Derive(membership) error: %v", err)
	}
	if len(complete) < len(observed) {
		t.Fatalf("complete rebuild lost observed facts: complete=%d observed=%d", len(complete), len(observed))
	}
	return complete
}

func analyzePythonIncrementalFixture(t *testing.T, rootPath, name, sourceID string) (contract.Artifact, analysis.Output) {
	t.Helper()
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatalf("open fixture root %q: %v", rootPath, err)
	}
	defer root.Close()
	input := analysis.ArtifactInput{
		SourceID:   sourceID,
		RootHandle: root,
		Limits:     source.Limits{MaxExtractionBytes: 1 << 20},
		Evidence:   analysis.EvidenceInput{Enabled: true},
		Artifact: contract.Artifact{
			ID:       "artifact-" + strings.TrimSuffix(name, filepath.Ext(name)),
			SourceID: sourceID,
			Path:     name,
			Type:     analysis.ArtifactTypePython,
		},
	}
	return analyzePythonFixture(t, input)
}

func pythonIncrementalFixtureRoot(t *testing.T, localizedChange bool) string {
	t.Helper()
	rootPath := t.TempDir()
	for _, name := range []string{"doctype.py", "hooks.py"} {
		content, err := os.ReadFile(filepath.Join("testdata", "frappe17", name))
		if err != nil {
			t.Fatalf("read Python fixture %q: %v", name, err)
		}
		if localizedChange && name == "doctype.py" {
			content = append(content, []byte("\n# localized incremental edit\n")...)
		}
		if err := os.WriteFile(filepath.Join(rootPath, name), content, 0o600); err != nil {
			t.Fatalf("write Python fixture %q: %v", name, err)
		}
	}
	return rootPath
}

func clonePythonRuleVersions(values []persistence.RuleVersion) []persistence.RuleVersion {
	result := make([]persistence.RuleVersion, len(values))
	for index, value := range values {
		result[index] = value
		result[index].Configuration = append(json.RawMessage(nil), value.Configuration...)
	}
	return result
}

func clonePythonIncrementalRevision(value persistence.FactualSnapshotRevision) persistence.FactualSnapshotRevision {
	result := value
	result.Snapshot = value.Snapshot
	result.Snapshot.FrontendManifests = make([]fact.FrontendManifest, len(value.Snapshot.FrontendManifests))
	for index, manifest := range value.Snapshot.FrontendManifests {
		result.Snapshot.FrontendManifests[index] = clonePythonManifest(manifest)
	}
	result.Snapshot.RuleVersions = clonePythonRuleVersions(value.Snapshot.RuleVersions)
	result.Snapshot.Facts = clonePythonFacts(value.Snapshot.Facts)
	result.Artifacts = append([]contract.Artifact(nil), value.Artifacts...)
	return result
}

func clonePythonFacts(values []fact.CanonicalFact) []fact.CanonicalFact {
	result := make([]fact.CanonicalFact, len(values))
	for index, value := range values {
		result[index] = value
		result[index].Object = nil
		if value.Object != nil {
			object := *value.Object
			result[index].Object = &object
		}
		result[index].Value = nil
		if value.Value != nil {
			scalar := *value.Value
			result[index].Value = &scalar
		}
		result[index].Qualifiers = append([]fact.Qualifier(nil), value.Qualifiers...)
		result[index].Evidence = append([]fact.EvidenceRef(nil), value.Evidence...)
		if value.Lineage != nil {
			lineage := *value.Lineage
			lineage.InputFactIDs = append([]string(nil), value.Lineage.InputFactIDs...)
			result[index].Lineage = &lineage
		}
	}
	return result
}

func pythonNeutralizeSnapshot(t *testing.T, values []fact.CanonicalFact, target fact.Scope) []fact.CanonicalFact {
	t.Helper()
	result := clonePythonFacts(values)
	ids := make(map[string]string, len(result))
	for index := range result {
		previousID := result[index].ID
		result[index].Scope.SnapshotID = target.SnapshotID
		result[index].ID = ""
		id, err := fact.FactID(result[index])
		if err != nil {
			t.Fatalf("FactID(%q) after snapshot neutralization: %v", values[index].ID, err)
		}
		result[index].ID = id
		ids[previousID] = id
	}
	for index := range result {
		if result[index].Lineage == nil {
			continue
		}
		for inputIndex, inputID := range result[index].Lineage.InputFactIDs {
			if neutralID, ok := ids[inputID]; ok {
				result[index].Lineage.InputFactIDs[inputIndex] = neutralID
			}
		}
	}
	return result
}
