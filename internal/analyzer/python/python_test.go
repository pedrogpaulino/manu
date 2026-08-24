package python

import (
	"context"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/pedrogpaulino/manu/internal/analysis"
	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/source"
)

func TestDescriptorDeclaresSafeStaticPythonFrontend(t *testing.T) {
	descriptor := New().Descriptor()
	if descriptor.ID != AnalyzerID || descriptor.Version != AnalyzerVersion || descriptor.Method != AnalyzerMethod {
		t.Fatalf("descriptor identity = %#v", descriptor)
	}
	if len(descriptor.ArtifactTypes) != 1 || descriptor.ArtifactTypes[0] != analysis.ArtifactTypePython {
		t.Fatalf("descriptor artifact types = %#v", descriptor.ArtifactTypes)
	}
	if !containsString(descriptor.Capabilities, "safe-static") {
		t.Fatalf("descriptor capabilities = %#v, want safe-static", descriptor.Capabilities)
	}
}

func TestAnalyzeFrappe17FixtureEmitsStructuralContributions(t *testing.T) {
	input, closeRoot := fixtureInput(t, "doctype.py", true)
	defer closeRoot()

	output, err := New().Analyze(context.Background(), input)
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	byType := contributionsByType(output.Contributions)
	for _, typ := range []string{ArtifactContributionType, SymbolContributionType, ImportContributionType, RelationContributionType, ConfigurationContributionType} {
		if len(byType[typ]) == 0 {
			t.Fatalf("missing %s contributions", typ)
		}
	}

	assertPayload(t, byType[ArtifactContributionType][0], map[string]any{"path": "doctype.py", "type": analysis.ArtifactTypePython})
	if got := byType[ArtifactContributionType][0].Locator; got.StartLine != 1 || got.EndLine != 1 {
		t.Fatalf("artifact locator = %#v, want line 1", got)
	}

	qualified := make(map[string]map[string]any)
	for _, contribution := range byType[SymbolContributionType] {
		var payload map[string]any
		decodePayload(t, contribution, &payload)
		qualified[payload["qualified_name"].(string)] = payload
		if contribution.Locator.StartLine <= 0 || contribution.Locator.EndLine < contribution.Locator.StartLine {
			t.Fatalf("symbol locator = %#v", contribution.Locator)
		}
	}
	for _, name := range []string{"SalesOrder", "SalesOrder.load_status", "SalesOrder.refresh"} {
		if _, ok := qualified[name]; !ok {
			t.Fatalf("missing qualified symbol %q in %#v", name, qualified)
		}
	}
	if got := qualified["SalesOrder.load_status"]["kind"]; got != "function" {
		t.Fatalf("load_status kind = %v, want function", got)
	}
	if got := qualified["SalesOrder.refresh"]["kind"]; got != "async_function" {
		t.Fatalf("refresh kind = %v, want async_function", got)
	}

	imports := make(map[string]map[string]any)
	for _, contribution := range byType[ImportContributionType] {
		var payload map[string]any
		decodePayload(t, contribution, &payload)
		imports[payload["module"].(string)+":"+stringValue(payload["name"])+":"+stringValue(payload["alias"])] = payload
	}
	for _, key := range []string{"frappe::", "frappe.model.document::document_module", "frappe:get_all:list_documents", "frappe.model.document:Document:"} {
		if _, ok := imports[key]; !ok {
			t.Fatalf("missing import %q in %#v", key, imports)
		}
	}

	relations := make(map[string]map[string]any)
	for _, contribution := range byType[RelationContributionType] {
		var payload map[string]any
		decodePayload(t, contribution, &payload)
		relations[payload["callee"].(string)+":"+payload["target"].(string)] = payload
		if payload["source_symbol"] != "SalesOrder.load_status" && payload["callee"] != "frappe.get_doc" {
			t.Fatalf("relation source symbol = %#v", payload)
		}
	}
	for _, key := range []string{"frappe.get_doc:Sales Order", "frappe.get_all:Sales Order", "frappe.db.get_value:Sales Order", "frappe.get_doc:Delivery Note"} {
		if _, ok := relations[key]; !ok {
			t.Fatalf("missing literal relation %q in %#v", key, relations)
		}
	}
	if _, ok := relations["frappe.get_doc:Fake DocType"]; ok {
		t.Fatal("docstring relation was emitted")
	}
	if _, ok := relations["frappe.get_all:Ignored DocType"]; ok {
		t.Fatal("string/comment relation was emitted")
	}

	if len(output.Evidence) != len(output.Contributions) {
		t.Fatalf("evidence count = %d, contributions = %d", len(output.Evidence), len(output.Contributions))
	}
	for _, draft := range output.Evidence {
		if draft.ContributionID == "" || draft.Locator.StartLine <= 0 {
			t.Fatalf("invalid evidence draft = %#v", draft)
		}
		if strings.Contains(draft.Content, "ignored_import") || strings.Contains(draft.Content, "Fake DocType") {
			t.Fatalf("evidence retained ignored source string: %q", draft.Content)
		}
	}
	if !hasGap(output.Gaps, SemanticGapCode) || !hasGap(output.Gaps, DynamicRelationGapCode) {
		t.Fatalf("gaps = %#v, want semantic and dynamic-target gaps", output.Gaps)
	}
	if !hasCoverage(output.Coverage, contract.DimensionEntitiesAndRelationships, contract.CoverageIncomplete) {
		t.Fatalf("coverage = %#v, want incomplete structural coverage", output.Coverage)
	}
}

func TestAnalyzeHooksEmitsKeysWithoutValues(t *testing.T) {
	input, closeRoot := fixtureInput(t, "hooks.py", true)
	defer closeRoot()

	output, err := New().Analyze(context.Background(), input)
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	keys := make(map[string]map[string]any)
	serialized, _ := json.Marshal(output)
	for _, secret := range []string{"safe_app", "Safe title", "safe_app.events.sales_order", "CONFIG_KEY"} {
		if strings.Contains(string(serialized), secret) {
			t.Fatalf("serialized output retained configuration value %q: %s", secret, serialized)
		}
	}
	for _, contribution := range contributionsByType(output.Contributions)[ConfigurationContributionType] {
		var payload map[string]any
		decodePayload(t, contribution, &payload)
		keys[payload["key"].(string)] = payload
		if len(payload) != 3 || payload["path"] != "hooks.py" {
			t.Fatalf("configuration payload = %#v, want key/kind/path only", payload)
		}
	}
	for _, key := range []string{"app_name", "app_title", "doc_events", "Sales Order", "Delivery Note", "scheduler_events", "daily", "ERP_MODE"} {
		if _, ok := keys[key]; !ok {
			t.Fatalf("missing configuration key %q in %#v", key, keys)
		}
	}
	if _, ok := keys["dynamic_key"]; !ok {
		t.Fatalf("missing safe hooks assignment key in %#v", keys)
	}
	if !hasGap(output.Gaps, DynamicConfigurationGapCode) {
		t.Fatalf("gaps = %#v, want dynamic configuration gap", output.Gaps)
	}
}

func TestAnalyzeIsDeterministicAndDoesNotMutateSource(t *testing.T) {
	input, closeRoot := fixtureInput(t, "doctype.py", true)
	defer closeRoot()

	before, err := source.ReadTextInRoot(context.Background(), input.RootHandle, input.Artifact.Path, 1<<20)
	if err != nil {
		t.Fatalf("read before = %v", err)
	}
	first, err := New().Analyze(context.Background(), input)
	if err != nil {
		t.Fatalf("first Analyze() error = %v", err)
	}
	second, err := New().Analyze(context.Background(), input)
	if err != nil {
		t.Fatalf("second Analyze() error = %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("repeated Analyze() changed output")
	}
	after, err := source.ReadTextInRoot(context.Background(), input.RootHandle, input.Artifact.Path, 1<<20)
	if err != nil {
		t.Fatalf("read after = %v", err)
	}
	if before.Content != after.Content || before.BytesRead != after.BytesRead {
		t.Fatal("Analyze() changed source content")
	}
}

func TestAnalyzeHonorsCancellationAndBoundedTruncation(t *testing.T) {
	input, closeRoot := fixtureInput(t, "doctype.py", false)
	defer closeRoot()
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := New().Analyze(cancelled, input); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Analyze() error = %v, want context.Canceled", err)
	}

	input.Limits.MaxExtractionBytes = 64
	output, err := New().Analyze(context.Background(), input)
	if err != nil {
		t.Fatalf("truncated Analyze() error = %v", err)
	}
	if !hasGap(output.Gaps, SourceTruncatedGapCode) {
		t.Fatalf("gaps = %#v, want bounded truncation gap", output.Gaps)
	}
	for _, contribution := range output.Contributions {
		if len(contribution.Value) > 4096 {
			t.Fatalf("bounded contribution unexpectedly large: %d", len(contribution.Value))
		}
	}
}

func TestAnalyzeBinaryReportsExplicitCoverageAndGap(t *testing.T) {
	temporary := t.TempDir()
	path := filepath.Join(temporary, "binary.py")
	if err := os.WriteFile(path, []byte("\x00\x01\x02"), 0o600); err != nil {
		t.Fatalf("write binary fixture: %v", err)
	}
	root, err := os.OpenRoot(temporary)
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	defer root.Close()
	input := analysis.ArtifactInput{
		SourceID:   "python-source",
		RootHandle: root,
		Artifact:   contract.Artifact{ID: "python-binary", SourceID: "python-source", Path: "binary.py", Type: analysis.ArtifactTypePython},
	}
	output, err := New().Analyze(context.Background(), input)
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	if !hasGap(output.Gaps, NotTextGapCode) || !hasCoverage(output.Coverage, contract.DimensionEntitiesAndRelationships, contract.CoverageNotApplicable) {
		t.Fatalf("binary output coverage/gaps = %#v/%#v", output.Coverage, output.Gaps)
	}
	if len(contributionsByType(output.Contributions)[SymbolContributionType]) != 0 {
		t.Fatal("binary artifact produced symbols")
	}
}

func TestParseSymbolsKeepsMultilineSignaturesSmall(t *testing.T) {
	parsed := parseSource("class Service(\n    Document,\n):\n    async def fetch(\n        self,\n        name: str,\n    ):\n        return name\n", "service.py")
	var signatures []string
	for _, item := range parsed.observations {
		if item.typ != SymbolContributionType {
			continue
		}
		payload, ok := item.value["signature"].(string)
		if !ok || len(payload) > 256 {
			t.Fatalf("symbol signature = %#v, want bounded string", item.value["signature"])
		}
		signatures = append(signatures, payload)
		if item.end < item.line {
			t.Fatalf("symbol locator range = %d-%d", item.line, item.end)
		}
	}
	if len(signatures) != 2 || signatures[0] != "(Document,)" || signatures[1] != "(self,name: str,)" {
		t.Fatalf("multiline signatures = %#v", signatures)
	}
}

func TestParseFrappeCallsRequiresRootFrappeName(t *testing.T) {
	parsed := parseSource("other.frappe.get_doc(\"Not a Frappe relation\")\nfrappe.get_doc(\"Sales Order\")\n", "module.py")
	var targets []string
	for _, item := range parsed.observations {
		if item.typ != RelationContributionType {
			continue
		}
		targets = append(targets, item.value["target"].(string))
	}
	if !reflect.DeepEqual(targets, []string{"Sales Order"}) {
		t.Fatalf("Frappe call targets = %#v", targets)
	}
}

func TestAnalyzerSourceHasNoExecutionOrAmbientAccessImports(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob source files: %v", err)
	}
	banned := map[string]bool{
		"os": true, "os/exec": true, "net": true, "net/http": true,
		"plugin": true, "os/command": true,
	}
	for _, filename := range files {
		if strings.HasSuffix(filename, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), filename, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", filename, err)
		}
		for _, importSpec := range file.Imports {
			path := strings.Trim(importSpec.Path.Value, `"`)
			if banned[path] {
				t.Fatalf("production analyzer imports prohibited package %q", path)
			}
		}
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			if selector, ok := call.Fun.(*ast.SelectorExpr); ok && (selector.Sel.Name == "WriteFile" || selector.Sel.Name == "Mkdir" || selector.Sel.Name == "Run" || selector.Sel.Name == "Command") {
				t.Fatalf("production analyzer uses ambient/write operation %s", selector.Sel.Name)
			}
			return true
		})
	}
}

func fixtureInput(t *testing.T, name string, evidence bool) (analysis.ArtifactInput, func()) {
	t.Helper()
	rootPath := filepath.Join("testdata", "frappe17")
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatalf("open fixture root: %v", err)
	}
	input := analysis.ArtifactInput{
		SourceID:   "python-source",
		RootHandle: root,
		Limits:     source.Limits{MaxExtractionBytes: 1 << 20},
		Evidence:   analysis.EvidenceInput{Enabled: evidence},
		Artifact: contract.Artifact{
			ID:       "artifact-" + strings.TrimSuffix(name, filepath.Ext(name)),
			SourceID: "python-source",
			Path:     name,
			Type:     analysis.ArtifactTypePython,
		},
	}
	return input, func() { _ = root.Close() }
}

func contributionsByType(contributions []contract.Contribution) map[string][]contract.Contribution {
	result := make(map[string][]contract.Contribution)
	for _, contribution := range contributions {
		result[contribution.Type] = append(result[contribution.Type], contribution)
	}
	return result
}

func decodePayload(t *testing.T, contribution contract.Contribution, target any) {
	t.Helper()
	if err := json.Unmarshal(contribution.Value, target); err != nil {
		t.Fatalf("decode %s payload: %v", contribution.Type, err)
	}
}

func assertPayload(t *testing.T, contribution contract.Contribution, expected map[string]any) {
	t.Helper()
	var got map[string]any
	decodePayload(t, contribution, &got)
	for key, value := range expected {
		if got[key] != value {
			t.Fatalf("%s payload[%q] = %#v, want %#v", contribution.Type, key, got[key], value)
		}
	}
}

func hasGap(gaps []contract.Gap, code string) bool {
	for _, gap := range gaps {
		if gap.Code == code {
			return true
		}
	}
	return false
}

func hasCoverage(values []contract.Coverage, dimension contract.Dimension, state contract.CoverageState) bool {
	for _, value := range values {
		if value.Dimension == string(dimension) && value.State == state {
			return true
		}
	}
	return false
}

func containsString(values []string, wanted string) bool {
	ordered := append([]string(nil), values...)
	sort.Strings(ordered)
	index := sort.SearchStrings(ordered, wanted)
	return index < len(ordered) && ordered[index] == wanted
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}
