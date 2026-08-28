package evaluation

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

type multistackFamilySpec struct {
	corpusRevision string
	sourceID       string
	sourceRevision string
	analyzerID     string
	artifacts      []string
}

func TestContextEfficiencyFixtureHasCompleteThreeByThreeMatrix(t *testing.T) {
	cases := loadContextEfficiencyFixture(t)
	if cases.Version != Version {
		t.Fatalf("fixture version = %q, want %q", cases.Version, Version)
	}
	if len(cases.Cases) != 9 {
		t.Fatalf("fixture cases = %d, want 9", len(cases.Cases))
	}
	if err := cases.Validate(); err != nil {
		t.Fatalf("fixture validation = %v", err)
	}

	expectedFamilies := map[string]multistackFamilySpec{
		"java-quarkus3": {
			corpusRevision: "java-quarkus3-factual-v1", sourceID: "source-quarkus3",
			sourceRevision: "528a6670e30f2074548c63516046b58a61b5bca2e38ce9974e176f09c3554efb", analyzerID: "java",
			artifacts: []string{"internal/analyzer/java/testdata/quarkus3/BookingResource.java"},
		},
		"wso2-integration": {
			corpusRevision: "wso2-declarative-v1", sourceID: "wso2-integration-source",
			sourceRevision: "1861914ca608c8fca2c4add57f7d7b43e6a711703431ecb41c3d7e6fb80a86db", analyzerID: "wso2",
			artifacts: []string{"internal/analyzer/wso2/testdata/api-v1.xml", "internal/analyzer/wso2/testdata/shared-v1.xml"},
		},
		"python-frappe17": {
			corpusRevision: "python-frappe17-factual-v1", sourceID: "source-python-integration",
			sourceRevision: "9eab722a13516e68b60d097b5c425e0dc1247df6ad330a23175249cc927ef9ab", analyzerID: "python",
			artifacts: []string{"internal/analyzer/python/testdata/frappe17/doctype.py", "internal/analyzer/python/testdata/frappe17/hooks.py"},
		},
	}
	expectedTasks := map[string]map[TaskKind]bool{
		"java-quarkus3":    {TaskKindLocalization: true, TaskKindExplanation: true, TaskKindImpact: true},
		"wso2-integration": {TaskKindLocalization: true, TaskKindExplanation: true, TaskKindImpact: true},
		"python-frappe17":  {TaskKindLocalization: true, TaskKindExplanation: true, TaskKindImpact: true},
	}
	seenMatrix := make(map[string]struct{}, len(cases.Cases))
	for _, item := range cases.Cases {
		spec, ok := expectedFamilies[item.CorpusID]
		if !ok {
			t.Fatalf("unknown corpus family %q", item.CorpusID)
		}
		if !expectedTasks[item.CorpusID][item.Task.Kind] {
			t.Fatalf("unexpected task %q for family %q", item.Task.Kind, item.CorpusID)
		}
		matrixKey := item.CorpusID + "\x00" + string(item.Task.Kind)
		if _, exists := seenMatrix[matrixKey]; exists {
			t.Fatalf("duplicate family/task combination %q", matrixKey)
		}
		seenMatrix[matrixKey] = struct{}{}
		if item.State != CaseStateCurated || item.CorpusRevision != spec.corpusRevision || item.SourceID != spec.sourceID || item.SourceRevision != spec.sourceRevision {
			t.Fatalf("incoherent identity for %s: %#v", item.CaseID, item)
		}
		if len(item.ExpectedEvidence) == 0 || len(item.ExpectedGaps) == 0 || len(item.Limitations) == 0 || len(item.FailureAttribution) == 0 {
			t.Fatalf("incomplete evidence/gap metadata for %s", item.CaseID)
		}
		if len(item.Variants) != 3 || len(item.Tools) == 0 || len(item.Configurations) == 0 {
			t.Fatalf("variant configuration metadata for %s: variants=%d tools=%d configs=%d", item.CaseID, len(item.Variants), len(item.Tools), len(item.Configurations))
		}
		configurations := make(map[string]EvaluationConfiguration, len(item.Configurations))
		for _, configuration := range item.Configurations {
			configurations[configuration.ID] = configuration
			if configuration.Settings["network"] != "disabled" || configuration.Settings["source_access"] != "read-only" {
				t.Fatalf("configuration %s for %s is not equivalent read-only local configuration: %#v", configuration.ID, item.CaseID, configuration.Settings)
			}
		}
		variantKinds := make(map[VariantKind]struct{}, len(item.Variants))
		variantIDs := make(map[string]struct{}, len(item.Variants))
		for _, variant := range item.Variants {
			variantKinds[variant.Kind] = struct{}{}
			variantIDs[variant.ID] = struct{}{}
			if _, exists := configurations[variant.ConfigurationID]; !exists {
				t.Fatalf("variant %s for %s references missing configuration %q", variant.ID, item.CaseID, variant.ConfigurationID)
			}
		}
		for _, kind := range []VariantKind{VariantDirectSource, VariantTextRetrieval, VariantManuContext} {
			if _, exists := variantKinds[kind]; !exists {
				t.Fatalf("missing %s variant for %s", kind, item.CaseID)
			}
		}
		if _, exists := variantKinds[VariantExternalContext]; exists {
			t.Fatalf("fixture unexpectedly includes external comparison for %s", item.CaseID)
		}
		if len(variantIDs) != 3 {
			t.Fatalf("variant IDs for %s are not unique: %#v", item.CaseID, variantIDs)
		}
		if item.Policy.SourceAccess != "read-only" || item.Policy.ExternalTransfer != "deny" || item.Policy.NetworkAccess != "disabled" || item.Policy.MutationAccess != "disabled" {
			t.Fatalf("unsafe policy for %s: %#v", item.CaseID, item.Policy)
		}
		criteriaKinds := make(map[CriterionKind]bool, len(item.Criteria.Items))
		for _, criterion := range item.Criteria.Items {
			criteriaKinds[criterion.Kind] = criterion.Required
			if criterion.Kind == CriterionAuthorization && criterion.Required {
				t.Fatalf("authorization criterion must not be required in %s", item.CaseID)
			}
		}
		for _, kind := range []CriterionKind{CriterionCorrectness, CriterionCompletion, CriterionEvidence, CriterionCitation, CriterionGap} {
			if !criteriaKinds[kind] {
				t.Fatalf("required %s criterion missing for %s", kind, item.CaseID)
			}
		}
		if item.Task.Kind == TaskKindImpact && item.Kind != CaseKindPossibleFlow {
			t.Fatalf("impact case %s kind = %q, want possible_flow", item.CaseID, item.Kind)
		}
		if item.Task.Kind != TaskKindImpact && item.Kind == CaseKindPossibleFlow {
			t.Fatalf("non-impact case %s unexpectedly declares possible_flow", item.CaseID)
		}
		if !equalStringSet(item.Scope.Artifacts, spec.artifacts) {
			t.Fatalf("scope artifacts for %s = %#v, want %#v", item.CaseID, item.Scope.Artifacts, spec.artifacts)
		}
		if len(item.ApplicableAnalyzers) != 1 || item.ApplicableAnalyzers[0].ID != spec.analyzerID || item.ApplicableAnalyzers[0].Status != AnalyzerApplicable {
			t.Fatalf("analyzer metadata for %s = %#v", item.CaseID, item.ApplicableAnalyzers)
		}
	}
	if len(seenMatrix) != 9 {
		t.Fatalf("matrix combinations = %d, want 9", len(seenMatrix))
	}
}

func TestContextEfficiencyFixtureLocatorsPointToVersionedFixtures(t *testing.T) {
	cases := loadContextEfficiencyFixture(t)
	repoRoot := repositoryRoot()
	if repoRoot == "" {
		t.Fatal("could not determine repository root from test file")
	}
	for _, item := range cases.Cases {
		scopeArtifacts := make(map[string]struct{}, len(item.Scope.Artifacts))
		for _, artifact := range item.Scope.Artifacts {
			scopeArtifacts[artifact] = struct{}{}
		}
		for _, expected := range item.ExpectedEvidence {
			if expected.Locator == nil {
				t.Fatalf("evidence %q in %s has no locator", expected.EvidenceID, item.CaseID)
			}
			locator := *expected.Locator
			if locator.Path == "" || filepath.IsAbs(filepath.FromSlash(locator.Path)) || strings.Contains(filepath.ToSlash(locator.Path), "../") {
				t.Fatalf("non-portable locator for %q: %#v", expected.EvidenceID, locator)
			}
			path := filepath.Clean(filepath.Join(repoRoot, filepath.FromSlash(locator.Path)))
			if path != repoRoot && !strings.HasPrefix(path, repoRoot+string(os.PathSeparator)) {
				t.Fatalf("locator escaped repository for %q: %q", expected.EvidenceID, locator.Path)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatalf("locator %q path %q: %v", expected.EvidenceID, locator.Path, err)
			}
			if !info.Mode().IsRegular() {
				t.Fatalf("locator %q path is not regular: %q", expected.EvidenceID, locator.Path)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read locator %q: %v", expected.EvidenceID, err)
			}
			lines := strings.Split(string(data), "\n")
			if _, exists := scopeArtifacts[locator.Path]; !exists {
				t.Fatalf("locator %q path %q is outside case scope", expected.EvidenceID, locator.Path)
			}
			if locator.StartLine <= 0 || locator.EndLine < locator.StartLine {
				t.Fatalf("locator %q has invalid line range: %#v", expected.EvidenceID, locator)
			}
			if locator.StartLine > len(lines) {
				t.Fatalf("locator %q starts past file end: %#v", expected.EvidenceID, locator)
			}
			if locator.EndLine > len(lines) {
				t.Fatalf("locator %q ends past file end: %#v", expected.EvidenceID, locator)
			}
			rangeText := strings.Join(lines[locator.StartLine-1:locator.EndLine], "\n")
			if strings.TrimSpace(rangeText) == "" {
				t.Fatalf("locator %q points to an empty line range: %#v", expected.EvidenceID, locator)
			}
			for _, token := range expectedEvidenceTokens(expected) {
				if !strings.Contains(rangeText, token) {
					t.Fatalf("locator %q token %q is outside line range %d-%d in %q", expected.EvidenceID, token, locator.StartLine, locator.EndLine, locator.Path)
				}
			}
		}
	}
}

func TestContextEfficiencyFixtureRoundTripsDeterministicallyWithoutResults(t *testing.T) {
	path := contextEfficiencyFixturePath()
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"\"result\"", "\"metrics\"", "\"analysis_snapshot\"", "user:pass", "secret-value", "password=", "secret=", "token=", "TODO:", "FIXME:", "placeholder", "${"} {
		if bytes.Contains(bytes.ToLower(original), bytes.ToLower([]byte(forbidden))) {
			t.Fatalf("fixture contains forbidden material %q", forbidden)
		}
	}
	cases, err := LoadCases(path)
	if err != nil {
		t.Fatal(err)
	}
	first, err := MarshalCases(cases)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeCases(bytes.NewReader(first))
	if err != nil {
		t.Fatal(err)
	}
	second, err := MarshalCases(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("fixture round-trip is not deterministic:\n%s\n%s", first, second)
	}
	secondLoad, err := LoadCases(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cases, secondLoad) {
		t.Fatal("repeated fixture load changed normalized cases")
	}
	encoded, err := json.Marshal(cases)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("analysis_snapshot")) || bytes.Contains(encoded, []byte("metrics")) || bytes.Contains(encoded, []byte("result")) {
		t.Fatalf("case metadata contains execution output: %s", encoded)
	}
}

func TestContextEfficiencyFixtureReferencesAreCoherentAndImpactGapsAreExplicit(t *testing.T) {
	cases := loadContextEfficiencyFixture(t)
	for _, item := range cases.Cases {
		evidenceIDs := make(map[string]struct{}, len(item.ExpectedEvidence))
		for _, evidence := range item.ExpectedEvidence {
			evidenceIDs[evidence.EvidenceID] = struct{}{}
		}
		gapIDs := make(map[string]struct{}, len(item.ExpectedGaps))
		for _, gap := range item.ExpectedGaps {
			gapIDs[gap.GapID] = struct{}{}
		}
		if len(item.AcceptableClaims) != 1 || len(item.ReferenceAnswer.ClaimIDs) != 1 || len(item.ReferenceAnswer.GapIDs) == 0 {
			t.Fatalf("reference metadata for %s = %#v", item.CaseID, item.ReferenceAnswer)
		}
		for _, claim := range item.AcceptableClaims {
			for _, evidenceID := range claim.EvidenceIDs {
				if _, ok := evidenceIDs[evidenceID]; !ok {
					t.Fatalf("claim %q references unknown evidence %q", claim.ClaimID, evidenceID)
				}
			}
			for _, gapID := range claim.GapIDs {
				if _, ok := gapIDs[gapID]; !ok {
					t.Fatalf("claim %q references unknown gap %q", claim.ClaimID, gapID)
				}
			}
		}
		if item.Task.Kind == TaskKindImpact {
			hasRuntimeGap := false
			hasTransitivityGap := false
			for _, gap := range item.ExpectedGaps {
				statement := strings.ToLower(gap.Statement)
				hasRuntimeGap = hasRuntimeGap || strings.Contains(statement, "runtime")
				hasTransitivityGap = hasTransitivityGap || strings.Contains(statement, "transitiv")
			}
			if item.Kind != CaseKindPossibleFlow || !hasRuntimeGap || !hasTransitivityGap {
				t.Fatalf("impact case %s does not declare runtime/transitivity gap: %#v", item.CaseID, item.ExpectedGaps)
			}
			if strings.Contains(strings.ToLower(item.ReferenceAnswer.Summary), "execução observada") || strings.Contains(strings.ToLower(item.ReferenceAnswer.Summary), "certeza") {
				t.Fatalf("impact reference overclaims execution/certainty: %#v", item.ReferenceAnswer)
			}
		}
	}
}

func loadContextEfficiencyFixture(t *testing.T) CaseSet {
	t.Helper()
	cases, err := LoadCases(contextEfficiencyFixturePath())
	if err != nil {
		t.Fatalf("LoadCases(context-efficiency.v1alpha2.json) error = %v", err)
	}
	return cases
}

func contextEfficiencyFixturePath() string {
	return filepath.Join(repositoryRoot(), "testdata", "evaluation", "context-efficiency.v1alpha2.json")
}

func equalStringSet(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	leftSet := make(map[string]struct{}, len(left))
	rightSet := make(map[string]struct{}, len(right))
	for _, value := range left {
		leftSet[value] = struct{}{}
	}
	for _, value := range right {
		rightSet[value] = struct{}{}
	}
	return reflect.DeepEqual(leftSet, rightSet)
}

func repositoryRoot() string {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		return ""
	}
	return filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))
}

func expectedEvidenceTokens(expected ExpectedEvidence) []string {
	tokens := make([]string, 0, 3)
	if expected.Locator != nil && expected.Locator.Member != "" {
		tokens = append(tokens, expected.Locator.Member)
	}
	if expected.Pattern != nil {
		for _, token := range []string{expected.Pattern.Member, expected.Pattern.Symbol, expected.Pattern.Attribute} {
			if token != "" && !containsString(tokens, token) {
				tokens = append(tokens, token)
			}
		}
	}
	if len(tokens) == 0 {
		switch expected.Kind {
		case "symbol", "definition", "configuration", "endpoint", "named_element", "dependency", "reference", "call", "message":
			return []string{"<missing-locator-token>"}
		}
	}
	return tokens
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
