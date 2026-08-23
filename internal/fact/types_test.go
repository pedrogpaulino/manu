package fact_test

import (
	"encoding/json"
	"errors"
	"math"
	"testing"

	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/fact"
)

func TestCanonicalFactValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*fact.CanonicalFact)
		want   error
	}{
		{name: "valid observed fact"},
		{
			name: "valid derived fact",
			mutate: func(candidate *fact.CanonicalFact) {
				candidate.Lineage = &fact.Lineage{
					RuleID:       "rule-membership",
					RuleVersion:  "1",
					InputFactIDs: []string{"fact-input"},
				}
			},
		},
		{
			name: "unsupported version",
			mutate: func(candidate *fact.CanonicalFact) {
				candidate.Version = "v0"
			},
			want: fact.ErrUnsupportedVersion,
		},
		{
			name: "missing identity",
			mutate: func(candidate *fact.CanonicalFact) {
				candidate.ID = ""
			},
			want: fact.ErrInvalid,
		},
		{
			name: "identity with whitespace",
			mutate: func(candidate *fact.CanonicalFact) {
				candidate.ID = "fact with spaces"
			},
			want: fact.ErrInvalid,
		},
		{
			name: "missing organization scope",
			mutate: func(candidate *fact.CanonicalFact) {
				candidate.Scope.OrganizationID = ""
			},
			want: fact.ErrInvalidScope,
		},
		{
			name: "snapshot scope with control character",
			mutate: func(candidate *fact.CanonicalFact) {
				candidate.Scope.SnapshotID = "snapshot\x00bad"
			},
			want: fact.ErrInvalidScope,
		},
		{
			name: "unknown predicate",
			mutate: func(candidate *fact.CanonicalFact) {
				candidate.Predicate = fact.Predicate("future")
			},
			want: fact.ErrInvalidPredicate,
		},
		{
			name: "unknown subject kind",
			mutate: func(candidate *fact.CanonicalFact) {
				candidate.Subject.Kind = fact.ParticipantKind("future")
			},
			want: fact.ErrInvalidParticipant,
		},
		{
			name: "object and value together",
			mutate: func(candidate *fact.CanonicalFact) {
				candidate.Object = &fact.Participant{Kind: fact.ParticipantArtifact, ID: "artifact-target"}
				candidate.Value = &fact.TypedValue{Kind: fact.ValueString, String: "value"}
			},
			want: fact.ErrInvalid,
		},
		{
			name: "malformed value",
			mutate: func(candidate *fact.CanonicalFact) {
				candidate.Value = &fact.TypedValue{Kind: fact.ValueNumber, Number: math.NaN()}
			},
			want: fact.ErrInvalidValue,
		},
		{
			name: "duplicate qualifier",
			mutate: func(candidate *fact.CanonicalFact) {
				qualifier := fact.Qualifier{Name: fact.QualifierOrigin, Value: fact.TypedValue{Kind: fact.ValueString, String: "observed"}}
				candidate.Qualifiers = []fact.Qualifier{qualifier, qualifier}
			},
			want: fact.ErrInvalidQualifier,
		},
		{
			name: "malformed producer",
			mutate: func(candidate *fact.CanonicalFact) {
				candidate.Producer.Method = ""
			},
			want: fact.ErrInvalidProducer,
		},
		{
			name: "missing evidence",
			mutate: func(candidate *fact.CanonicalFact) {
				candidate.Evidence = nil
			},
			want: fact.ErrInvalidEvidence,
		},
		{
			name: "evidence from another source",
			mutate: func(candidate *fact.CanonicalFact) {
				candidate.Evidence[0].Locator.SourceID = "source-other"
			},
			want: fact.ErrInvalidEvidence,
		},
		{
			name: "empty evidence locator",
			mutate: func(candidate *fact.CanonicalFact) {
				candidate.Evidence[0].Locator = contract.Locator{}
			},
			want: fact.ErrInvalidEvidence,
		},
		{
			name: "duplicate evidence identity",
			mutate: func(candidate *fact.CanonicalFact) {
				candidate.Evidence = append(candidate.Evidence, candidate.Evidence[0])
			},
			want: fact.ErrInvalidEvidence,
		},
		{
			name: "lineage without inputs",
			mutate: func(candidate *fact.CanonicalFact) {
				candidate.Lineage = &fact.Lineage{RuleID: "rule", RuleVersion: "1"}
			},
			want: fact.ErrInvalidLineage,
		},
		{
			name: "lineage self reference",
			mutate: func(candidate *fact.CanonicalFact) {
				candidate.Lineage = &fact.Lineage{RuleID: "rule", RuleVersion: "1", InputFactIDs: []string{candidate.ID}}
			},
			want: fact.ErrInvalidLineage,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidate := validFact()
			if tt.mutate != nil {
				tt.mutate(&candidate)
			}
			err := candidate.Validate()
			if tt.want == nil {
				if err != nil {
					t.Fatalf("CanonicalFact.Validate() error = %v", err)
				}
				return
			}
			if !errors.Is(err, tt.want) {
				t.Fatalf("CanonicalFact.Validate() error = %v, want errors.Is(..., %v)", err, tt.want)
			}
		})
	}
}

func TestTypedValueValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value fact.TypedValue
		valid bool
	}{
		{name: "empty string", value: fact.TypedValue{Kind: fact.ValueString}, valid: true},
		{name: "zero integer", value: fact.TypedValue{Kind: fact.ValueInteger}, valid: true},
		{name: "zero number", value: fact.TypedValue{Kind: fact.ValueNumber}, valid: true},
		{name: "false boolean", value: fact.TypedValue{Kind: fact.ValueBoolean}, valid: true},
		{name: "null", value: fact.TypedValue{Kind: fact.ValueNull}, valid: true},
		{name: "unknown kind", value: fact.TypedValue{}, valid: false},
		{name: "nan", value: fact.TypedValue{Kind: fact.ValueNumber, Number: math.NaN()}, valid: false},
		{name: "infinity", value: fact.TypedValue{Kind: fact.ValueNumber, Number: math.Inf(1)}, valid: false},
		{name: "string with integer", value: fact.TypedValue{Kind: fact.ValueString, Integer: 1}, valid: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.value.Validate()
			if (err == nil) != tt.valid {
				t.Fatalf("TypedValue.Validate() error = %v, valid = %t", err, tt.valid)
			}
		})
	}
}

func TestEvidenceRefValidate(t *testing.T) {
	t.Parallel()

	evidence := validFact().Evidence[0]
	if err := evidence.Validate(); err != nil {
		t.Fatalf("EvidenceRef.Validate() error = %v", err)
	}
	if err := evidence.Validate(validFact().Scope); err != nil {
		t.Fatalf("EvidenceRef.Validate(scope) error = %v", err)
	}
	if err := evidence.Validate(validFact().Scope, validFact().Scope); !errors.Is(err, fact.ErrInvalidEvidence) {
		t.Fatalf("EvidenceRef.Validate(two scopes) error = %v, want invalid evidence", err)
	}
}

func TestLineageValidate(t *testing.T) {
	t.Parallel()

	lineage := fact.Lineage{RuleID: "rule", RuleVersion: "1", InputFactIDs: []string{"input"}}
	if err := lineage.Validate(); err != nil {
		t.Fatalf("Lineage.Validate() error = %v", err)
	}
	if err := lineage.Validate("derived"); err != nil {
		t.Fatalf("Lineage.Validate(fact id) error = %v", err)
	}
}

func TestCanonicalFactJSONRoundTrip(t *testing.T) {
	t.Parallel()

	want := validFact()
	payload, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	var got fact.CanonicalFact
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("round-tripped fact validation error = %v", err)
	}
	if got.ID != want.ID || got.Scope != want.Scope || got.Predicate != want.Predicate {
		t.Fatalf("round-tripped identity differs: got %#v, want %#v", got, want)
	}
}

func validFact() fact.CanonicalFact {
	candidate := structuralFact()
	id, err := fact.DeriveID(candidate)
	if err != nil {
		panic(err)
	}
	candidate.ID = id
	return candidate
}

func structuralFact() fact.CanonicalFact {
	return fact.CanonicalFact{
		Version: fact.Version,
		Scope: fact.Scope{
			OrganizationID: "organization-local",
			SourceID:       "source-app",
			SnapshotID:     "snapshot-1",
		},
		Predicate: fact.PredicateDefinition,
		Subject:   fact.Participant{Kind: fact.ParticipantSymbol, ID: "symbol-main"},
		Producer: fact.Producer{
			ID:      "java-frontend",
			Version: "1.0",
			Method:  "lexical-symbols",
		},
		Qualifiers: []fact.Qualifier{
			{Name: fact.QualifierOrigin, Value: fact.TypedValue{Kind: fact.ValueString, String: "observed"}},
		},
		Evidence: []fact.EvidenceRef{
			{
				ID: "evidence-1",
				Locator: contract.Locator{
					SourceID:   "source-app",
					ArtifactID: "artifact-main",
					Path:       "src/Main.java",
					StartLine:  12,
					EndLine:    12,
				},
			},
		},
	}
}
