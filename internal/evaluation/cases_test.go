package evaluation

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pedrogpaulino/manu/internal/contract"
)

func loadEvaluationFixture(t *testing.T) CaseSet {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "evaluation", CasesFileName)
	cases, err := LoadCases(path)
	if err != nil {
		t.Fatalf("LoadCases(%q) error = %v", path, err)
	}
	return cases
}

func TestLoadCasesFixtureContainsStableInventoryFlowProvenanceAndAbstention(t *testing.T) {
	cases := loadEvaluationFixture(t)
	if cases.Version != Version {
		t.Fatalf("case set version = %q, want %q", cases.Version, Version)
	}
	if len(cases.Cases) != 7 {
		t.Fatalf("case count = %d, want 7", len(cases.Cases))
	}
	for index := 1; index < len(cases.Cases); index++ {
		previous, current := cases.Cases[index-1], cases.Cases[index]
		if caseIdentity(previous) >= caseIdentity(current) {
			t.Fatalf("cases are not ordered: %q before %q", previous.CaseID, current.CaseID)
		}
	}
	kinds := make(map[CaseKind]bool)
	stages := make(map[AttributionStage]bool)
	for _, item := range cases.Cases {
		if item.State != CaseStateDraft {
			t.Errorf("case %q state = %q, want draft", item.CaseID, item.State)
		}
		if len(item.Authors) != 1 || item.Authors[0] != "manu-change-9-1" {
			t.Errorf("case %q authors = %v, want technical change author", item.CaseID, item.Authors)
		}
		if len(item.Reviewers) != 1 || item.Reviewers[0] != "manu-contract-validation" {
			t.Errorf("case %q reviewers = %v, want contract validation", item.CaseID, item.Reviewers)
		}
		kinds[item.Kind] = true
		for _, attribution := range item.FailureAttribution {
			stages[attribution.Stage] = true
		}
		if item.SourceRevision == "" || len(item.AcceptableClaims) == 0 || len(item.ExpectedEvidence) == 0 || len(item.ExpectedGaps) == 0 {
			t.Fatalf("incomplete case = %+v", item)
		}
		for _, evidence := range item.ExpectedEvidence {
			if evidence.Locator != nil && filepath.IsAbs(evidence.Locator.Path) {
				t.Fatalf("absolute locator path = %q", evidence.Locator.Path)
			}
		}
	}
	for _, kind := range []CaseKind{CaseKindInventory, CaseKindProvenance, CaseKindPossibleFlow, CaseKindAbstention} {
		if !kinds[kind] {
			t.Fatalf("fixture does not cover kind %q", kind)
		}
	}
	for _, stage := range []AttributionStage{AttributionExtraction, AttributionRetrieval, AttributionGeneration, AttributionPolicy} {
		if !stages[stage] {
			t.Fatalf("fixture does not cover attribution stage %q", stage)
		}
	}
}

func TestLoadCasesNormalizesDeterministicallyAndDoesNotMutateInput(t *testing.T) {
	cases := loadEvaluationFixture(t)
	original := append([]EvaluationCase(nil), cases.Cases...)
	reversed := cases
	reversed.Cases = append([]EvaluationCase(nil), cases.Cases...)
	for left, right := 0, len(reversed.Cases)-1; left < right; left, right = left+1, right-1 {
		reversed.Cases[left], reversed.Cases[right] = reversed.Cases[right], reversed.Cases[left]
	}
	first, err := MarshalCases(cases)
	if err != nil {
		t.Fatalf("MarshalCases() error = %v", err)
	}
	second, err := MarshalCases(reversed)
	if err != nil {
		t.Fatalf("MarshalCases(reversed) error = %v", err)
	}
	if string(first) != string(second) {
		t.Fatal("normalized case encoding changed with input order")
	}
	for index := range original {
		if cases.Cases[index].CaseID != original[index].CaseID {
			t.Fatalf("MarshalCases mutated case order at index %d", index)
		}
	}
	decoded, err := DecodeCases(strings.NewReader(string(first)))
	if err != nil {
		t.Fatalf("DecodeCases(canonical) error = %v", err)
	}
	third, err := MarshalCases(decoded)
	if err != nil || string(third) != string(first) {
		t.Fatalf("canonical round trip = %v / equal=%v", err, string(third) == string(first))
	}
}

func TestDecodeCasesRejectsUnknownFieldsTrailingValuesAndDuplicates(t *testing.T) {
	cases := loadEvaluationFixture(t)
	encoded, err := json.Marshal(cases)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	tests := []struct {
		name string
		data string
		want error
	}{
		{
			name: "unknown top-level field",
			data: strings.Replace(string(encoded), `{"version":`, `{"unknown":true,"version":`, 1),
			want: ErrInvalidCases,
		},
		{
			name: "unknown nested field",
			data: strings.Replace(string(encoded), `{"case_id":`, `{"raw_content":"forbidden","case_id":`, 1),
			want: ErrInvalidCases,
		},
		{
			name: "trailing JSON value",
			data: string(encoded) + `{"version":"v1alpha1"}`,
			want: ErrInvalidCases,
		},
		{
			name: "duplicate case",
			data: duplicateFirstCase(t, cases),
			want: ErrDuplicateCase,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := DecodeCases(strings.NewReader(test.data))
			if !errors.Is(err, test.want) {
				t.Fatalf("DecodeCases() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestDecodeCasesRejectsInvalidVersionKindStageAndReferences(t *testing.T) {
	base := loadEvaluationFixture(t)
	tests := []struct {
		name   string
		modify func(*CaseSet)
		want   error
	}{
		{
			name: "unsupported envelope version",
			modify: func(cases *CaseSet) {
				cases.Version = "v9"
			},
			want: ErrUnsupportedVersion,
		},
		{
			name: "unsupported case kind",
			modify: func(cases *CaseSet) {
				cases.Cases[0].Kind = CaseKind("observed_execution")
			},
			want: ErrInvalidCaseKind,
		},
		{
			name: "unsupported attribution stage",
			modify: func(cases *CaseSet) {
				cases.Cases[0].ExpectedGaps[0].AttributionStage = AttributionStage("runtime")
			},
			want: ErrInvalidAttributionStage,
		},
		{
			name: "orphan claim evidence",
			modify: func(cases *CaseSet) {
				cases.Cases[0].AcceptableClaims[0].EvidenceIDs = []string{"missing-evidence"}
			},
			want: ErrInvalidCases,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			modified := cloneCaseSet(base)
			test.modify(&modified)
			encoded, err := json.Marshal(modified)
			if err != nil {
				t.Fatalf("json.Marshal() error = %v", err)
			}
			_, err = DecodeCases(strings.NewReader(string(encoded)))
			if !errors.Is(err, test.want) {
				t.Fatalf("DecodeCases() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestDecodeCasesRejectsTraversalSecretsAndRawContent(t *testing.T) {
	base := loadEvaluationFixture(t)
	tests := []struct {
		name   string
		modify func(*CaseSet)
	}{
		{
			name: "relative traversal",
			modify: func(cases *CaseSet) {
				locator := contractLocator("../outside.txt")
				cases.Cases[0].ExpectedEvidence[0].Locator = &locator
			},
		},
		{
			name: "absolute path",
			modify: func(cases *CaseSet) {
				locator := contractLocator("/tmp/outside.txt")
				cases.Cases[0].ExpectedEvidence[0].Locator = &locator
			},
		},
		{
			name: "secret assignment",
			modify: func(cases *CaseSet) {
				cases.Cases[0].AcceptableClaims[0].Statement = "password=do-not-store"
			},
		},
		{
			name: "raw source fragment",
			modify: func(cases *CaseSet) {
				cases.Cases[0].AcceptableClaims[0].Statement = "public class Example {}"
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			modified := cloneCaseSet(base)
			test.modify(&modified)
			encoded, err := json.Marshal(modified)
			if err != nil {
				t.Fatalf("json.Marshal() error = %v", err)
			}
			_, err = DecodeCases(strings.NewReader(string(encoded)))
			if !errors.Is(err, ErrUnsafeCase) {
				t.Fatalf("DecodeCases() error = %v, want unsafe case", err)
			}
			if strings.Contains(err.Error(), "do-not-store") {
				t.Fatal("loader error leaked secret material")
			}
		})
	}
}

func TestDecodeCasesAcceptsPatternOnlyEvidenceAndRejectsDuplicateNestedIDs(t *testing.T) {
	cases := loadEvaluationFixture(t)
	patternOnly := cloneCaseSet(cases)
	item := &patternOnly.Cases[0]
	item.ExpectedEvidence = []ExpectedEvidence{{
		Kind:    "artifact",
		Pattern: &EvidencePattern{PathPattern: "analyzers/*.java", Symbol: "Sample"},
	}}
	item.AcceptableClaims[0].EvidenceIDs = nil
	item.AcceptableClaims[0].GapIDs = []string{item.ExpectedGaps[0].GapID}
	if _, err := MarshalCases(patternOnly); err != nil {
		t.Fatalf("pattern-only evidence was rejected: %v", err)
	}

	duplicate := cloneCaseSet(cases)
	duplicate.Cases[0].ExpectedGaps = append(duplicate.Cases[0].ExpectedGaps, duplicate.Cases[0].ExpectedGaps[0])
	if _, err := MarshalCases(duplicate); !errors.Is(err, ErrDuplicateCase) {
		t.Fatalf("duplicate nested gap error = %v, want duplicate", err)
	}
}

func TestEvaluationFixtureContainsNoAbsolutePathsSecretsOrSourceContent(t *testing.T) {
	path := filepath.Join("..", "..", "testdata", "evaluation", CasesFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", path, err)
	}
	text := string(data)
	for _, forbidden := range []string{"/home/", "password=", "-----BEGIN", "public class", "package tech."} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("evaluation fixture contains forbidden material %q", forbidden)
		}
	}
}

func duplicateFirstCase(t *testing.T, cases CaseSet) string {
	t.Helper()
	cases.Cases = append(cases.Cases, cases.Cases[0])
	encoded, err := json.Marshal(cases)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	return string(encoded)
}

func contractLocator(path string) contract.Locator { return contract.Locator{Path: path} }
