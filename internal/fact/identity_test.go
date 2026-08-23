package fact_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/fact"
)

func TestCanonicalBytesUsesStableJSONShape(t *testing.T) {
	t.Parallel()

	got, err := fact.CanonicalBytes(validFact())
	if err != nil {
		t.Fatalf("CanonicalBytes() error = %v", err)
	}
	want := `{"version":"v1alpha1","id":"fact-1c32dbb6374c98ddf6e21878342e1d0f10801cf7547b7dc9b166697f29240b02","scope":{"organization_id":"organization-local","source_id":"source-app","snapshot_id":"snapshot-1"},"predicate":"definition","subject":{"kind":"symbol","id":"symbol-main"},"object":null,"value":null,"qualifiers":[{"name":"origin","value":{"kind":"string","string":"observed"}}],"producer":{"id":"java-frontend","version":"1.0","method":"lexical-symbols"},"evidence":[{"id":"evidence-1","locator":{"source_id":"source-app","artifact_id":"artifact-main","path":"src/Main.java","start_line":12,"end_line":12}}],"lineage":null}`
	if string(got) != want {
		t.Fatalf("CanonicalBytes() = %s, want %s", got, want)
	}

	var decoded fact.CanonicalFact
	if err := json.Unmarshal(got, &decoded); err != nil {
		t.Fatalf("canonical bytes are not JSON: %v", err)
	}
	if err := decoded.Validate(); err != nil {
		t.Fatalf("decoded canonical fact validation error = %v", err)
	}
}

func TestFactIDDerivesBeforeAssignmentAndFinalEncodingRequiresIt(t *testing.T) {
	t.Parallel()

	candidate := structuralFact()
	if candidate.ID != "" {
		t.Fatalf("structural fixture ID = %q, want empty", candidate.ID)
	}

	derived, err := fact.DeriveID(candidate)
	if err != nil {
		t.Fatalf("DeriveID(structural fact) error = %v", err)
	}
	if candidate.ID != "" {
		t.Fatalf("DeriveID mutated the fact ID to %q", candidate.ID)
	}

	candidate.ID = derived
	if err := candidate.Validate(); err != nil {
		t.Fatalf("derived fact validation error = %v", err)
	}

	candidate.ID = "fact-inconsistent"
	if err := candidate.Validate(); !errors.Is(err, fact.ErrInvalidIdentity) {
		t.Fatalf("Validate(inconsistent ID) error = %v, want invalid identity", err)
	}
	if _, err := fact.CanonicalBytes(candidate); !errors.Is(err, fact.ErrInvalidIdentity) {
		t.Fatalf("CanonicalBytes(inconsistent ID) error = %v, want invalid identity", err)
	}

	candidate.ID = ""
	if err := candidate.Validate(); !errors.Is(err, fact.ErrInvalidIdentity) {
		t.Fatalf("Validate(empty ID) error = %v, want invalid identity", err)
	}
	if _, err := fact.CanonicalBytes(candidate); !errors.Is(err, fact.ErrInvalidIdentity) {
		t.Fatalf("CanonicalBytes(empty ID) error = %v, want invalid identity", err)
	}
}

func TestCanonicalEncodingSortsCollectionsWithoutMutatingFact(t *testing.T) {
	t.Parallel()

	first := validFact()
	first.Qualifiers = []fact.Qualifier{
		{Name: fact.QualifierTemporal, Value: fact.TypedValue{Kind: fact.ValueString, String: "snapshot"}},
		{Name: fact.QualifierOrigin, Value: fact.TypedValue{Kind: fact.ValueString, String: "observed"}},
	}
	first.Evidence = []fact.EvidenceRef{
		{ID: "evidence-2", Locator: contract.Locator{SourceID: "source-app", Path: "src/Other.java", StartLine: 2, EndLine: 2}},
		first.Evidence[0],
	}
	first.Lineage = &fact.Lineage{RuleID: "rule", RuleVersion: "1", InputFactIDs: []string{"input-b", "input-a"}}
	derivedID, err := fact.DeriveID(first)
	if err != nil {
		t.Fatalf("DeriveID(first) error = %v", err)
	}
	first.ID = derivedID

	second := first
	second.Qualifiers = append([]fact.Qualifier(nil), first.Qualifiers...)
	second.Evidence = append([]fact.EvidenceRef(nil), first.Evidence...)
	second.Lineage = &fact.Lineage{RuleID: "rule", RuleVersion: "1", InputFactIDs: []string{"input-a", "input-b"}}
	second.Qualifiers[0], second.Qualifiers[1] = second.Qualifiers[1], second.Qualifiers[0]
	second.Evidence[0], second.Evidence[1] = second.Evidence[1], second.Evidence[0]

	firstBytes, err := fact.CanonicalBytes(first)
	if err != nil {
		t.Fatalf("CanonicalBytes(first) error = %v", err)
	}
	secondBytes, err := fact.CanonicalBytes(second)
	if err != nil {
		t.Fatalf("CanonicalBytes(second) error = %v", err)
	}
	if !bytes.Equal(firstBytes, secondBytes) {
		t.Fatalf("canonical bytes changed with collection order:\nfirst:  %s\nsecond: %s", firstBytes, secondBytes)
	}

	if first.Qualifiers[0].Name != fact.QualifierTemporal || first.Evidence[0].ID != "evidence-2" || first.Lineage.InputFactIDs[0] != "input-b" {
		t.Fatal("CanonicalBytes mutated the input fact")
	}
}

func TestFactIdentityExcludesSupportMetadata(t *testing.T) {
	t.Parallel()

	base := validFact()
	withOtherEvidence := base
	withOtherEvidence.Evidence = []fact.EvidenceRef{{
		ID: "evidence-other",
		Locator: contract.Locator{
			SourceID: "source-app",
			Path:     "src/Other.java",
		},
	}}
	withOtherLineage := base
	withOtherLineage.Lineage = &fact.Lineage{
		RuleID:       "rule-membership",
		RuleVersion:  "1",
		InputFactIDs: []string{"input-fact"},
	}
	withDifferentSuppliedID := base
	withDifferentSuppliedID.ID = "fact-supplied-alternative"

	baseID, err := fact.FactID(base)
	if err != nil {
		t.Fatalf("FactID(base) error = %v", err)
	}
	evidenceID, err := fact.FactID(withOtherEvidence)
	if err != nil {
		t.Fatalf("FactID(withOtherEvidence) error = %v", err)
	}
	lineageID, err := fact.FactID(withOtherLineage)
	if err != nil {
		t.Fatalf("FactID(withOtherLineage) error = %v", err)
	}
	suppliedID, err := fact.FactID(withDifferentSuppliedID)
	if err != nil {
		t.Fatalf("FactID(withDifferentSuppliedID) error = %v", err)
	}
	if baseID != evidenceID || baseID != lineageID || baseID != suppliedID {
		t.Fatalf("non-semantic metadata changed fact identity: base=%q evidence=%q lineage=%q supplied=%q", baseID, evidenceID, lineageID, suppliedID)
	}
	identityBytes, err := fact.IdentityBytes(withOtherLineage)
	if err != nil {
		t.Fatalf("IdentityBytes() error = %v", err)
	}
	for _, excluded := range []string{"\"id\":\"" + base.ID + "\"", "\"id\":\"fact-supplied-alternative\"", "\"evidence\"", "\"lineage\""} {
		if bytes.Contains(identityBytes, []byte(excluded)) {
			t.Fatalf("IdentityBytes() unexpectedly contains %s: %s", excluded, identityBytes)
		}
	}

	baseCanonical, err := fact.CanonicalDigest(base)
	if err != nil {
		t.Fatalf("CanonicalDigest(base) error = %v", err)
	}
	evidenceCanonical, err := fact.CanonicalDigest(withOtherEvidence)
	if err != nil {
		t.Fatalf("CanonicalDigest(withOtherEvidence) error = %v", err)
	}
	lineageCanonical, err := fact.CanonicalDigest(withOtherLineage)
	if err != nil {
		t.Fatalf("CanonicalDigest(withOtherLineage) error = %v", err)
	}
	if baseCanonical == evidenceCanonical || baseCanonical == lineageCanonical {
		t.Fatal("transport metadata did not change canonical transport digest")
	}
}

func TestFactIdentityIncludesScopeAndProducer(t *testing.T) {
	t.Parallel()

	baseID, err := fact.FactID(validFact())
	if err != nil {
		t.Fatalf("FactID(base) error = %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*fact.CanonicalFact)
	}{
		{
			name: "different organization",
			mutate: func(candidate *fact.CanonicalFact) {
				candidate.Scope.OrganizationID = "organization-other"
			},
		},
		{
			name: "different source",
			mutate: func(candidate *fact.CanonicalFact) {
				candidate.Scope.SourceID = "source-other"
				candidate.Evidence[0].Locator.SourceID = "source-other"
			},
		},
		{
			name: "different snapshot",
			mutate: func(candidate *fact.CanonicalFact) {
				candidate.Scope.SnapshotID = "snapshot-other"
			},
		},
		{
			name: "different predicate",
			mutate: func(candidate *fact.CanonicalFact) {
				candidate.Predicate = fact.PredicateReference
			},
		},
		{
			name: "different participant",
			mutate: func(candidate *fact.CanonicalFact) {
				candidate.Subject.ID = "symbol-other"
			},
		},
		{
			name: "different producer",
			mutate: func(candidate *fact.CanonicalFact) {
				candidate.Producer.ID = "python-frontend"
			},
		},
		{
			name: "different qualifier",
			mutate: func(candidate *fact.CanonicalFact) {
				candidate.Qualifiers[0].Value.String = "curated"
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidate := validFact()
			tt.mutate(&candidate)
			candidateID, err := fact.FactID(candidate)
			if err != nil {
				t.Fatalf("FactID() error = %v", err)
			}
			if candidateID == baseID {
				t.Fatalf("FactID() = %q, same as base identity", candidateID)
			}
		})
	}
}

func TestFactIdentityValidationAndRepetition(t *testing.T) {
	t.Parallel()

	candidate := validFact()
	derived, err := fact.DeriveID(candidate)
	if err != nil {
		t.Fatalf("DeriveID() error = %v", err)
	}
	candidate.ID = derived
	if err := candidate.ValidateIdentity(); err != nil {
		t.Fatalf("ValidateIdentity() error = %v", err)
	}
	candidate.ID = "fact-inconsistent"
	if err := candidate.ValidateIdentity(); !errors.Is(err, fact.ErrInvalidIdentity) {
		t.Fatalf("ValidateIdentity() error = %v, want invalid identity", err)
	}

	first, err := fact.IdentityDigest(validFact())
	if err != nil {
		t.Fatalf("IdentityDigest() error = %v", err)
	}
	second, err := fact.IdentityDigest(validFact())
	if err != nil {
		t.Fatalf("second IdentityDigest() error = %v", err)
	}
	if first != second {
		t.Fatalf("IdentityDigest() changed across repeated calls: %q != %q", first, second)
	}
}

func TestFactIdentityIsSafeForConcurrentCalls(t *testing.T) {
	t.Parallel()

	candidate := validFact()
	want, err := fact.FactID(candidate)
	if err != nil {
		t.Fatalf("FactID() error = %v", err)
	}

	const workers = 16
	const repetitions = 50
	var wait sync.WaitGroup
	wait.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wait.Done()
			for j := 0; j < repetitions; j++ {
				got, err := fact.FactID(candidate)
				if err != nil {
					t.Errorf("FactID() error = %v", err)
					return
				}
				if got != want {
					t.Errorf("FactID() = %q, want %q", got, want)
					return
				}
			}
		}()
	}
	wait.Wait()
}
