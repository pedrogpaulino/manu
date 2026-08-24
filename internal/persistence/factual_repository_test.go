package persistence

import (
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

func TestPrepareFactualSnapshotCanonicalizesWithoutMutatingInput(t *testing.T) {
	input := factualSnapshotFixture(t)
	original := cloneFactualSnapshotInput(input)

	prepared, err := PrepareFactualSnapshot(input)
	if err != nil {
		t.Fatalf("PrepareFactualSnapshot() error = %v", err)
	}
	if !reflect.DeepEqual(input, original) {
		t.Fatal("PrepareFactualSnapshot() mutated its input")
	}
	if len(prepared.FrontendManifests) != 2 || len(prepared.RuleVersions) != 2 || len(prepared.Facts) != 2 {
		t.Fatalf("prepared counts = manifests:%d rules:%d facts:%d", len(prepared.FrontendManifests), len(prepared.RuleVersions), len(prepared.Facts))
	}
	if prepared.FrontendManifests[0].Manifest.ID != "java" || prepared.FrontendManifests[1].Manifest.ID != "wso2" {
		t.Fatalf("manifest order = %q, %q", prepared.FrontendManifests[0].Manifest.ID, prepared.FrontendManifests[1].Manifest.ID)
	}
	if prepared.RuleVersions[0].RuleID != "dependency" || prepared.RuleVersions[1].RuleID != "membership" {
		t.Fatalf("rule order = %q, %q", prepared.RuleVersions[0].RuleID, prepared.RuleVersions[1].RuleID)
	}
	if prepared.Facts[0].ExternalID >= prepared.Facts[1].ExternalID {
		t.Fatalf("fact order is not canonical: %q, %q", prepared.Facts[0].ExternalID, prepared.Facts[1].ExternalID)
	}

	observed := findPreparedFact(t, prepared, input.Facts[0].ID)
	if observed.Kind != factualFactKindObserved || observed.FrontendManifestID == "" || observed.RuleVersionID != "" {
		t.Fatalf("observed prepared fact = %#v", observed)
	}
	derived := findPreparedFact(t, prepared, input.Facts[1].ID)
	if derived.Kind != factualFactKindDerived || derived.FrontendManifestID != "" || derived.RuleVersionID == "" {
		t.Fatalf("derived prepared fact = %#v", derived)
	}
	if len(derived.Inputs) != 1 || derived.Inputs[0].InputFactID != observed.ID || derived.Inputs[0].Ordinal != 0 {
		t.Fatalf("derived inputs = %#v, want one input to %q", derived.Inputs, observed.ID)
	}
	if string(prepared.RuleVersions[0].Configuration) != `{"a":2,"z":1}` {
		t.Fatalf("canonical rule configuration = %s", prepared.RuleVersions[0].Configuration)
	}
	if len(prepared.FrontendManifests[0].CanonicalJSON) == 0 || len(prepared.FrontendManifests[0].Digest) != 64 {
		t.Fatalf("manifest canonical representation missing: json=%s digest=%q", prepared.FrontendManifests[0].CanonicalJSON, prepared.FrontendManifests[0].Digest)
	}
	var decoded map[string]any
	if err := json.Unmarshal(prepared.FrontendManifests[0].CanonicalJSON, &decoded); err != nil {
		t.Fatalf("canonical manifest JSON is invalid: %v", err)
	}
}

func TestPrepareFactualSnapshotIsPermutationInvariant(t *testing.T) {
	input := factualSnapshotFixture(t)
	permuted := cloneFactualSnapshotInput(input)
	sort.Slice(permuted.FrontendManifests, func(left, right int) bool {
		return permuted.FrontendManifests[left].ID > permuted.FrontendManifests[right].ID
	})
	sort.Slice(permuted.RuleVersions, func(left, right int) bool {
		return permuted.RuleVersions[left].RuleID > permuted.RuleVersions[right].RuleID
	})
	sort.Slice(permuted.Facts, func(left, right int) bool { return permuted.Facts[left].ID > permuted.Facts[right].ID })
	for index := range permuted.Facts {
		permuteFactCollections(&permuted.Facts[index])
	}

	first, err := PrepareFactualSnapshot(input)
	if err != nil {
		t.Fatalf("first preparation error = %v", err)
	}
	second, err := PrepareFactualSnapshot(permuted)
	if err != nil {
		t.Fatalf("permuted preparation error = %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("permutations changed prepared output:\nfirst=%#v\nsecond=%#v", first, second)
	}
}

func TestPrepareFactualSnapshotRejectsInvalidPureInputs(t *testing.T) {
	base := factualSnapshotFixture(t)
	secret := "secret-should-not-appear"
	tests := []struct {
		name   string
		input  func() FactualSnapshotInput
		secret string
	}{
		{
			name: "invalid relational uuid",
			input: func() FactualSnapshotInput {
				input := cloneFactualSnapshotInput(base)
				input.OrganizationID = "not-a-uuid"
				return input
			},
		},
		{
			name: "invalid source uuid",
			input: func() FactualSnapshotInput {
				input := cloneFactualSnapshotInput(base)
				input.SourceID = "not-a-uuid"
				return input
			},
		},
		{
			name: "invalid snapshot uuid",
			input: func() FactualSnapshotInput {
				input := cloneFactualSnapshotInput(base)
				input.SnapshotID = "not-a-uuid"
				return input
			},
		},
		{
			name: "noncanonical relational uuid",
			input: func() FactualSnapshotInput {
				input := cloneFactualSnapshotInput(base)
				input.OrganizationID = "00000000-0000-4000-8000-000000000001"
				return input
			},
		},
		{
			name: "invalid scope",
			input: func() FactualSnapshotInput {
				input := cloneFactualSnapshotInput(base)
				input.Scope.SnapshotID = ""
				return input
			},
		},
		{
			name: "scope mismatch",
			input: func() FactualSnapshotInput {
				input := cloneFactualSnapshotInput(base)
				input.Facts[0] = validFact(input.Scope, "scope-mismatch", javaProducer(), nil)
				input.Facts[0].Scope.SourceID = "other-source"
				input.Facts[0].ID = mustFactID(input.Facts[0])
				return input
			},
		},
		{
			name: "duplicate fact identity",
			input: func() FactualSnapshotInput {
				input := cloneFactualSnapshotInput(base)
				input.Facts = append(input.Facts, input.Facts[0])
				return input
			},
		},
		{
			name: "duplicate rule identity",
			input: func() FactualSnapshotInput {
				input := cloneFactualSnapshotInput(base)
				input.RuleVersions = append(input.RuleVersions, input.RuleVersions[0])
				return input
			},
		},
		{
			name: "duplicate frontend manifest identity",
			input: func() FactualSnapshotInput {
				input := cloneFactualSnapshotInput(base)
				input.FrontendManifests = append(input.FrontendManifests, input.FrontendManifests[0])
				return input
			},
		},
		{
			name: "invalid frontend manifest",
			input: func() FactualSnapshotInput {
				input := cloneFactualSnapshotInput(base)
				input.FrontendManifests[0].ManifestVersion = "unsupported"
				return input
			},
		},
		{
			name: "invalid canonical fact",
			input: func() FactualSnapshotInput {
				input := cloneFactualSnapshotInput(base)
				input.Facts[0].Predicate = fact.Predicate("unsupported")
				return input
			},
		},
		{
			name: "observed fact without matching manifest",
			input: func() FactualSnapshotInput {
				input := cloneFactualSnapshotInput(base)
				input.FrontendManifests = input.FrontendManifests[:1]
				return input
			},
		},
		{
			name: "derived fact without matching rule",
			input: func() FactualSnapshotInput {
				input := cloneFactualSnapshotInput(base)
				input.RuleVersions = nil
				return input
			},
		},
		{
			name: "missing lineage input",
			input: func() FactualSnapshotInput {
				input := cloneFactualSnapshotInput(base)
				input.Facts[1].Lineage = &fact.Lineage{RuleID: "dependency", RuleVersion: "1", InputFactIDs: []string{"fact-missing"}}
				input.Facts[1].ID = mustFactID(input.Facts[1])
				return input
			},
		},
		{
			name: "malformed rule JSON does not echo payload",
			input: func() FactualSnapshotInput {
				input := cloneFactualSnapshotInput(base)
				input.RuleVersions[0].Configuration = json.RawMessage(`{"secret":"` + secret)
				return input
			},
			secret: secret,
		},
		{
			name: "invalid rule digest",
			input: func() FactualSnapshotInput {
				input := cloneFactualSnapshotInput(base)
				input.RuleVersions[0].ImplementationDigest = "not-a-digest"
				return input
			},
		},
		{
			name: "invalid rule id",
			input: func() FactualSnapshotInput {
				input := cloneFactualSnapshotInput(base)
				input.RuleVersions[0].RuleID = ""
				return input
			},
		},
		{
			name: "invalid rule version",
			input: func() FactualSnapshotInput {
				input := cloneFactualSnapshotInput(base)
				input.RuleVersions[0].Version = ""
				return input
			},
		},
		{
			name: "rule configuration is not an object",
			input: func() FactualSnapshotInput {
				input := cloneFactualSnapshotInput(base)
				input.RuleVersions[0].Configuration = json.RawMessage(`["secret"]`)
				return input
			},
			secret: "secret",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := PrepareFactualSnapshot(tt.input())
			if err == nil {
				t.Fatal("PrepareFactualSnapshot() error = nil")
			}
			if !errors.Is(err, ErrInvalidFactualSnapshot) {
				t.Fatalf("error = %v, want ErrInvalidFactualSnapshot", err)
			}
			if !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("error = %v, want ErrInvalidInput classification", err)
			}
			if tt.secret != "" && strings.Contains(err.Error(), tt.secret) {
				t.Fatalf("error leaked secret payload: %v", err)
			}
		})
	}
}

func TestErrInvalidFactualSnapshotPreservesPersistenceClassification(t *testing.T) {
	if !errors.Is(ErrInvalidFactualSnapshot, ErrInvalidInput) {
		t.Fatal("ErrInvalidFactualSnapshot does not preserve ErrInvalidInput")
	}
	if !errors.Is(invalidFactualComponent("test", 0), ErrInvalidInput) {
		t.Fatal("component validation error does not preserve ErrInvalidInput")
	}
}

func TestPrepareFactualSnapshotUsesCanonicalUUIDsForRows(t *testing.T) {
	input := factualSnapshotFixture(t)
	prepared, err := PrepareFactualSnapshot(input)
	if err != nil {
		t.Fatalf("PrepareFactualSnapshot() error = %v", err)
	}
	manifest := prepared.FrontendManifests[0]
	wantManifestID := identity.CanonicalUUID("frontend-manifest", input.Scope.OrganizationID, input.Scope.SourceID, input.Scope.SnapshotID, manifest.Manifest.ID, manifest.Manifest.Version, manifest.Manifest.Method)
	if manifest.ID != wantManifestID {
		t.Fatalf("manifest ID = %q, want %q", manifest.ID, wantManifestID)
	}
	rule := prepared.RuleVersions[0]
	wantRuleID := identity.CanonicalUUID("rule-version", input.Scope.OrganizationID, rule.RuleID, rule.Version)
	if rule.ID != wantRuleID {
		t.Fatalf("rule ID = %q, want %q", rule.ID, wantRuleID)
	}
}

func factualSnapshotFixture(t *testing.T) FactualSnapshotInput {
	t.Helper()
	scope := fact.Scope{
		OrganizationID: "organization-local",
		SourceID:       "source-app",
		SnapshotID:     "snapshot-1",
	}
	observed := validFact(scope, "observed", javaProducer(), nil)
	derived := validFact(scope, "derived", fact.Producer{ID: "rule-engine", Version: "1", Method: "dependency"}, &fact.Lineage{
		RuleID:       "dependency",
		RuleVersion:  "1",
		InputFactIDs: []string{observed.ID},
	})
	return FactualSnapshotInput{
		OrganizationID: identity.CanonicalUUID("organization", scope.OrganizationID),
		SourceID:       identity.CanonicalUUID("source", scope.OrganizationID, scope.SourceID),
		SnapshotID:     identity.CanonicalUUID("snapshot", scope.OrganizationID, scope.SourceID, scope.SnapshotID),
		Scope:          scope,
		FrontendManifests: []fact.FrontendManifest{
			validFrontendManifest("wso2", "2", "elements"),
			validFrontendManifest("java", "1", "symbols"),
		},
		RuleVersions: []RuleVersion{
			{RuleID: "membership", Version: "1", ImplementationDigest: strings.Repeat("b", 64), Configuration: json.RawMessage(`{"z":1}`)},
			{RuleID: "dependency", Version: "1", ImplementationDigest: strings.Repeat("a", 64), Configuration: json.RawMessage(`{"z":1,"a":2}`)},
		},
		Facts: []fact.CanonicalFact{observed, derived},
	}
}

func validFrontendManifest(id, version, method string) fact.FrontendManifest {
	return fact.FrontendManifest{
		ManifestVersion: fact.FrontendManifestVersion,
		ID:              id,
		Version:         version,
		Method:          method,
		SourceTypes:     []string{"repository"},
		Families:        []string{"jvm"},
		Versions:        []string{"17"},
		Capabilities:    []contract.Dimension{contract.DimensionEntitiesAndRelationships, contract.DimensionLandscapeInventoryStructure},
		Limitations:     []string{"limited-depth", "safe-static"},
		Predicates:      []fact.Predicate{fact.PredicateDefinition, fact.PredicateReference},
		Execution:       fact.ExecutionProfileSafeStatic,
	}
}

func javaProducer() fact.Producer {
	return fact.Producer{ID: "java", Version: "1", Method: "symbols"}
}

func validFact(scope fact.Scope, subjectID string, producer fact.Producer, lineage *fact.Lineage) fact.CanonicalFact {
	candidate := fact.CanonicalFact{
		Version:   fact.Version,
		Scope:     scope,
		Predicate: fact.PredicateDefinition,
		Subject:   fact.Participant{Kind: fact.ParticipantSymbol, ID: subjectID},
		Value:     &fact.TypedValue{Kind: fact.ValueString, String: subjectID},
		Qualifiers: []fact.Qualifier{
			{Name: fact.QualifierOrigin, Value: fact.TypedValue{Kind: fact.ValueString, String: "observed"}},
			{Name: fact.QualifierCoverage, Value: fact.TypedValue{Kind: fact.ValueString, String: "complete"}},
		},
		Producer: producer,
		Evidence: []fact.EvidenceRef{{ID: "evidence-" + subjectID, Locator: contract.Locator{Path: subjectID + ".java", StartLine: 1, EndLine: 1}}},
		Lineage:  lineage,
	}
	candidate.ID = mustFactID(candidate)
	return candidate
}

func mustFactID(candidate fact.CanonicalFact) string {
	id, err := fact.FactID(candidate)
	if err != nil {
		panic(err)
	}
	return id
}

func findPreparedFact(t *testing.T, prepared PreparedFactualSnapshot, id string) PreparedCanonicalFact {
	t.Helper()
	for _, candidate := range prepared.Facts {
		if candidate.ExternalID == id {
			return candidate
		}
	}
	t.Fatalf("prepared fact %q not found", id)
	return PreparedCanonicalFact{}
}

func permuteFactCollections(candidate *fact.CanonicalFact) {
	for left, right := 0, len(candidate.Qualifiers)-1; left < right; left, right = left+1, right-1 {
		candidate.Qualifiers[left], candidate.Qualifiers[right] = candidate.Qualifiers[right], candidate.Qualifiers[left]
	}
	for left, right := 0, len(candidate.Evidence)-1; left < right; left, right = left+1, right-1 {
		candidate.Evidence[left], candidate.Evidence[right] = candidate.Evidence[right], candidate.Evidence[left]
	}
	if candidate.Lineage != nil {
		for left, right := 0, len(candidate.Lineage.InputFactIDs)-1; left < right; left, right = left+1, right-1 {
			candidate.Lineage.InputFactIDs[left], candidate.Lineage.InputFactIDs[right] = candidate.Lineage.InputFactIDs[right], candidate.Lineage.InputFactIDs[left]
		}
	}
}

func cloneFactualSnapshotInput(input FactualSnapshotInput) FactualSnapshotInput {
	clone := input
	clone.FrontendManifests = append([]fact.FrontendManifest(nil), input.FrontendManifests...)
	for index := range clone.FrontendManifests {
		canonical, err := fact.CanonicalFrontendManifest(clone.FrontendManifests[index])
		if err == nil {
			clone.FrontendManifests[index] = canonical
		}
	}
	clone.RuleVersions = append([]RuleVersion(nil), input.RuleVersions...)
	for index := range clone.RuleVersions {
		clone.RuleVersions[index].Configuration = append(json.RawMessage(nil), input.RuleVersions[index].Configuration...)
	}
	clone.Facts = make([]fact.CanonicalFact, len(input.Facts))
	for index := range input.Facts {
		clone.Facts[index] = cloneCanonicalFact(input.Facts[index])
		// Keep the fixture's original collection order in this test clone; the
		// production helper intentionally canonicalizes those collections.
		clone.Facts[index].Qualifiers = append([]fact.Qualifier(nil), input.Facts[index].Qualifiers...)
		clone.Facts[index].Evidence = append([]fact.EvidenceRef(nil), input.Facts[index].Evidence...)
		if input.Facts[index].Lineage != nil {
			lineage := *input.Facts[index].Lineage
			lineage.InputFactIDs = append([]string(nil), input.Facts[index].Lineage.InputFactIDs...)
			clone.Facts[index].Lineage = &lineage
		}
	}
	return clone
}
