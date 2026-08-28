package evaluation

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
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
	if cases.Version != LegacyVersion {
		t.Fatalf("case set version = %q, want %q", cases.Version, LegacyVersion)
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

func TestEvaluationCaseValidateSupportsLegacyAndCurrentItems(t *testing.T) {
	legacy := loadEvaluationFixture(t).Cases[0]
	if err := legacy.Validate(); err != nil {
		t.Fatalf("legacy EvaluationCase.Validate() error = %v", err)
	}
	current := loadCurrentEvaluationFixture(t).Cases[0]
	if err := current.Validate(); err != nil {
		t.Fatalf("current EvaluationCase.Validate() error = %v", err)
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
			data: strings.Replace(string(encoded), `"case_id":`, `"raw_content":"forbidden","case_id":`, 1),
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

func loadCurrentEvaluationFixture(t *testing.T) CaseSet {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "evaluation", CurrentCasesFileName)
	cases, err := LoadCases(path)
	if err != nil {
		t.Fatalf("LoadCases(%q) error = %v", path, err)
	}
	return cases
}

func TestLoadCurrentCasesFixtureContainsEvaluationMetadata(t *testing.T) {
	cases := loadCurrentEvaluationFixture(t)
	if cases.Version != Version {
		t.Fatalf("case set version = %q, want %q", cases.Version, Version)
	}
	if len(cases.Cases) != 1 {
		t.Fatalf("case count = %d, want 1", len(cases.Cases))
	}
	item := cases.Cases[0]
	if item.Task.Kind != TaskKindLocalization || item.Task.Objective == "" {
		t.Fatalf("task metadata = %#v", item.Task)
	}
	if len(item.Variants) != 3 {
		t.Fatalf("variant metadata = %d, want 3", len(item.Variants))
	}
	if len(item.Tools) == 0 || len(item.Configurations) == 0 || len(item.Limitations) == 0 {
		t.Fatalf("reproducibility metadata = %#v", item)
	}
	for _, variant := range item.Variants {
		if len(variant.ToolIDs) == 0 || variant.ConfigurationID == "" {
			t.Fatalf("variant references = %#v", variant)
		}
	}
	if len(item.Criteria.Items) != 2 || item.ReferenceAnswer.Summary == "" || len(item.ApplicableAnalyzers) != 2 {
		t.Fatalf("acceptance metadata = criteria:%d answer:%#v analyzers:%d", len(item.Criteria.Items), item.ReferenceAnswer, len(item.ApplicableAnalyzers))
	}
	if item.Policy.ExternalTransfer != "deny" || len(item.Policy.Permissions) != 2 {
		t.Fatalf("policy metadata = %#v", item.Policy)
	}
	if err := cases.Validate(); err != nil {
		t.Fatalf("current fixture validation = %v", err)
	}
}

func TestDecodeLegacyCasesPreservesEnvelopeVersionAndMetadataAbsence(t *testing.T) {
	fixturePath := filepath.Join("..", "..", "testdata", "evaluation", CasesFileName)
	original, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", fixturePath, err)
	}
	first, err := DecodeCases(bytes.NewReader(original))
	if err != nil {
		t.Fatalf("DecodeCases(legacy) error = %v", err)
	}
	second, err := DecodeCases(bytes.NewReader(original))
	if err != nil {
		t.Fatalf("DecodeCases(legacy second) error = %v", err)
	}
	if first.Version != LegacyVersion || second.Version != LegacyVersion {
		t.Fatalf("legacy versions = %q and %q, want %q", first.Version, second.Version, LegacyVersion)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("legacy migration is not deterministic")
	}
	for _, item := range first.Cases {
		if item.hasExtendedMetadata() {
			t.Fatalf("legacy case acquired unrecorded metadata: %#v", item)
		}
	}
	encoded, err := MarshalCases(first)
	if err != nil {
		t.Fatalf("MarshalCases(migrated) error = %v", err)
	}
	encodedAgain, err := MarshalCases(second)
	if err != nil {
		t.Fatalf("MarshalCases(migrated second) error = %v", err)
	}
	if !bytes.Equal(encoded, encodedAgain) {
		t.Fatal("legacy encoding changed between runs")
	}
	var envelope struct {
		Version string            `json:"version"`
		Cases   []json.RawMessage `json:"cases"`
	}
	if err := json.Unmarshal(encoded, &envelope); err != nil {
		t.Fatalf("legacy encoded JSON = %v", err)
	}
	if envelope.Version != LegacyVersion || len(envelope.Cases) == 0 {
		t.Fatalf("encoded legacy envelope = version:%q cases:%d", envelope.Version, len(envelope.Cases))
	}
	var encodedFields map[string]json.RawMessage
	if err := json.Unmarshal(envelope.Cases[0], &encodedFields); err != nil {
		t.Fatalf("legacy encoded case = %v", err)
	}
	for _, name := range []string{"task", "variants", "tools", "configurations", "limitations", "applicable_analyzers", "criteria", "reference_answer", "policy", "created_at", "updated_at", "supersedes"} {
		if _, exists := encodedFields[name]; exists {
			t.Fatalf("legacy encoding fabricated field %q", name)
		}
	}
	unchanged, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("ReadFile(%q) after decode error = %v", fixturePath, err)
	}
	if !bytes.Equal(original, unchanged) {
		t.Fatal("legacy fixture was modified while decoding")
	}
}

func TestLegacyCasesRejectCurrentOnlyMetadata(t *testing.T) {
	legacy := loadEvaluationFixture(t)
	legacy.Version = LegacyVersion
	legacy.Cases[0].Task = EvaluationTask{Kind: TaskKindLocalization, Objective: "metadata"}
	if err := legacy.Validate(); !errors.Is(err, ErrUnsupportedVersion) {
		t.Fatalf("legacy current metadata error = %v, want unsupported version", err)
	}
}

func TestCurrentCaseMetadataRejectsInvalidEnumsDuplicatesAndSecrets(t *testing.T) {
	base := loadCurrentEvaluationFixture(t)
	tests := []struct {
		name   string
		modify func(*EvaluationCase)
		want   error
	}{
		{
			name: "task enum",
			modify: func(item *EvaluationCase) {
				item.Task.Kind = TaskKind("other")
			},
			want: ErrInvalidTaskKind,
		},
		{
			name: "variant enum",
			modify: func(item *EvaluationCase) {
				item.Variants[0].Kind = VariantKind("other")
			},
			want: ErrInvalidVariantKind,
		},
		{
			name: "analyzer enum",
			modify: func(item *EvaluationCase) {
				item.ApplicableAnalyzers[0].Status = AnalyzerStatus("other")
			},
			want: ErrInvalidAnalyzerStatus,
		},
		{
			name: "criterion enum",
			modify: func(item *EvaluationCase) {
				item.Criteria.Items[0].Kind = CriterionKind("other")
			},
			want: ErrInvalidCriterionKind,
		},
		{
			name: "duplicate variant",
			modify: func(item *EvaluationCase) {
				item.Variants[1].ID = item.Variants[0].ID
			},
			want: ErrDuplicateCase,
		},
		{
			name: "duplicate tool",
			modify: func(item *EvaluationCase) {
				item.Tools[1].ID = item.Tools[0].ID
			},
			want: ErrDuplicateCase,
		},
		{
			name: "duplicate configuration",
			modify: func(item *EvaluationCase) {
				item.Configurations[1].ID = item.Configurations[0].ID
			},
			want: ErrDuplicateCase,
		},
		{
			name: "secret configuration key",
			modify: func(item *EvaluationCase) {
				item.Configurations[0].Settings["api_key"] = "redacted"
			},
			want: ErrUnsafeCase,
		},
		{
			name: "secret configuration value",
			modify: func(item *EvaluationCase) {
				item.Configurations[0].Settings["mode"] = "token=hidden"
			},
			want: ErrUnsafeCase,
		},
		{
			name: "invalid policy",
			modify: func(item *EvaluationCase) {
				item.Policy.NetworkAccess = "remote"
			},
			want: ErrInvalidEvaluationPolicy,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			modified := cloneCaseSet(base)
			test.modify(&modified.Cases[0])
			encoded, err := json.Marshal(modified)
			if err != nil {
				t.Fatalf("json.Marshal() error = %v", err)
			}
			_, err = DecodeCases(bytes.NewReader(encoded))
			if !errors.Is(err, test.want) {
				t.Fatalf("DecodeCases() error = %v, want %v", err, test.want)
			}
			if strings.Contains(err.Error(), "hidden") {
				t.Fatal("metadata error leaked secret material")
			}
		})
	}
}

func TestCurrentCaseRequiresCoreMetadata(t *testing.T) {
	base := loadCurrentEvaluationFixture(t)
	tests := []struct {
		name   string
		modify func(*EvaluationCase)
	}{
		{name: "task", modify: func(item *EvaluationCase) { item.Task = EvaluationTask{} }},
		{name: "variants", modify: func(item *EvaluationCase) { item.Variants = nil }},
		{name: "tools", modify: func(item *EvaluationCase) { item.Tools = nil }},
		{name: "configurations", modify: func(item *EvaluationCase) { item.Configurations = nil }},
		{name: "limitations", modify: func(item *EvaluationCase) { item.Limitations = nil }},
		{name: "analyzers", modify: func(item *EvaluationCase) { item.ApplicableAnalyzers = nil }},
		{name: "criteria", modify: func(item *EvaluationCase) { item.Criteria.Items = nil }},
		{name: "no required criterion", modify: func(item *EvaluationCase) {
			for index := range item.Criteria.Items {
				item.Criteria.Items[index].Required = false
			}
		}},
		{name: "reference answer", modify: func(item *EvaluationCase) { item.ReferenceAnswer = ReferenceAnswer{} }},
		{name: "reference answer without claim", modify: func(item *EvaluationCase) { item.ReferenceAnswer.ClaimIDs = nil }},
		{name: "policy", modify: func(item *EvaluationCase) { item.Policy = EvaluationPolicy{} }},
		{name: "created at", modify: func(item *EvaluationCase) { item.CreatedAt = "" }},
		{name: "updated at", modify: func(item *EvaluationCase) { item.UpdatedAt = "" }},
		{name: "without direct source variant", modify: func(item *EvaluationCase) {
			for index := range item.Variants {
				item.Variants[index].Kind = VariantManuContext
			}
		}},
		{name: "without comparison variant", modify: func(item *EvaluationCase) {
			for index := range item.Variants {
				item.Variants[index].Kind = VariantDirectSource
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			modified := cloneCaseSet(base)
			test.modify(&modified.Cases[0])
			if _, err := MarshalCases(modified); err == nil {
				t.Fatal("MarshalCases() error = nil, want missing core metadata error")
			}
		})
	}
}

func TestCurrentCaseRejectsOrphanVariantReferencesAndLegacyComparatorField(t *testing.T) {
	base := loadCurrentEvaluationFixture(t)
	tests := []struct {
		name   string
		modify func(*EvaluationCase)
	}{
		{name: "orphan tool", modify: func(item *EvaluationCase) { item.Variants[0].ToolIDs = []string{"missing-tool"} }},
		{name: "orphan configuration", modify: func(item *EvaluationCase) { item.Variants[0].ConfigurationID = "missing-configuration" }},
		{name: "duplicate variant tool", modify: func(item *EvaluationCase) {
			item.Variants[0].ToolIDs = []string{"filesystem-search", "filesystem-search"}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			modified := cloneCaseSet(base)
			test.modify(&modified.Cases[0])
			encoded, err := json.Marshal(modified)
			if err != nil {
				t.Fatalf("json.Marshal() error = %v", err)
			}
			if _, err := DecodeCases(bytes.NewReader(encoded)); err == nil {
				t.Fatal("DecodeCases() error = nil, want invalid reference")
			}
		})
	}

	encoded, err := json.Marshal(base)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	withComparator := strings.Replace(string(encoded), `"tools":`, `"comparators":[],"tools":`, 1)
	if _, err := DecodeCases(strings.NewReader(withComparator)); !errors.Is(err, ErrInvalidCases) {
		t.Fatalf("legacy comparator field error = %v, want invalid cases", err)
	}
}

func TestCurrentCaseMetadataNormalizationIsDeterministicAndDefensive(t *testing.T) {
	cases := loadCurrentEvaluationFixture(t)
	original := cloneCaseSet(cases)
	reversed := cloneCaseSet(cases)
	item := &reversed.Cases[0]
	for left, right := 0, len(item.Variants)-1; left < right; left, right = left+1, right-1 {
		item.Variants[left], item.Variants[right] = item.Variants[right], item.Variants[left]
	}
	item.Policy.Permissions = []string{"filesystem.read", "context.read"}
	first, err := MarshalCases(cases)
	if err != nil {
		t.Fatalf("MarshalCases() error = %v", err)
	}
	second, err := MarshalCases(reversed)
	if err != nil {
		t.Fatalf("MarshalCases(reversed) error = %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("current metadata encoding changed with input order")
	}
	if !reflect.DeepEqual(cases, original) {
		t.Fatal("MarshalCases mutated current case metadata")
	}
}
