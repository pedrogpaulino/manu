package evaluation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pedrogpaulino/manu/internal/analysis"
	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/evidence"
	"github.com/pedrogpaulino/manu/internal/source"
)

func TestNewRealExtractorRequiresExplicitCorpusBinding(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if _, err := NewRealExtractor(RealCorpusConfig{SourceID: "source"}); !errors.Is(err, ErrInvalidRealCorpus) {
		t.Fatalf("NewRealExtractor() error = %v, want ErrInvalidRealCorpus", err)
	}
	if _, err := NewRealExtractor(realTestCorpusConfig(root)); err != nil {
		t.Fatalf("NewRealExtractor() error = %v", err)
	}
}

func TestRealExtractorUsesBoundedAnalyzersAndMapsLocators(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	pathName := filepath.Join(root, "app", "src", "main", "java", "BookingService.java")
	writeRealTestFile(t, pathName, "package sample;\npublic class BookingService {\n  public void createBooking() {}\n}\n")
	before := fileDigest(t, pathName)

	config := realTestCorpusConfig(root)
	config.Includes = []string{"app/src/main/java/**"}
	extractor, err := NewRealExtractor(config)
	if err != nil {
		t.Fatal(err)
	}
	item := EvaluationCase{
		CaseID:         "TM-REAL-01",
		CorpusID:       config.CorpusID,
		CorpusRevision: config.CorpusRevision,
		SourceID:       config.SourceID,
		SourceRevision: config.SourceRevision,
		ExpectedEvidence: []ExpectedEvidence{{
			EvidenceID: "expected-service",
			Kind:       "service",
			Locator:    contractLocatorForTest("app/src/main/java/BookingService.java", "BookingService", 2, 4),
		}},
	}
	input, mapping, err := extractor.Extract(context.Background(), item)
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	if err := input.Validate(); err != nil {
		t.Fatalf("real bundle validation error = %v", err)
	}
	if len(input.Artifacts) != 1 || len(input.Contributions) == 0 || len(input.Evidence) == 0 {
		t.Fatalf("real bundle counts = artifacts %d, contributions %d, evidence %d", len(input.Artifacts), len(input.Contributions), len(input.Evidence))
	}
	if mapping["expected-service"] == "" {
		t.Fatalf("expected locator was not mapped: %#v", mapping)
	}
	if input.Manifest.Source.Revision != config.SourceRevision || input.Manifest.Organization.ID != config.OrganizationID {
		t.Fatalf("bundle scope = %#v", input.Manifest)
	}
	for _, unit := range input.Evidence {
		if unit.ExternalTransfer != evidence.DecisionDeny {
			t.Fatalf("real local evidence transfer = %q, want deny", unit.ExternalTransfer)
		}
		if unit.Contribution.AnalyzerID == "evaluation-simulator" || unit.Content == "" {
			t.Fatalf("evidence was not produced by a real bounded analyzer: %#v", unit)
		}
	}
	if after := fileDigest(t, pathName); before != after {
		t.Fatalf("source changed: before %s, after %s", before, after)
	}
}

func TestRealBundleRunsThroughLocalIngestion(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeRealTestFile(t, filepath.Join(root, "Sample.java"), "package sample;\npublic class Sample {\n  public void run() {}\n}\n")
	config := realTestCorpusConfig(root)
	extractor, err := NewRealExtractor(config)
	if err != nil {
		t.Fatal(err)
	}
	item := EvaluationCase{
		CaseID:             "REAL-INGEST-01",
		CorpusID:           config.CorpusID,
		CorpusRevision:     config.CorpusRevision,
		SourceID:           config.SourceID,
		SourceRevision:     config.SourceRevision,
		CompetenceQuestion: "Sample",
	}
	input, mapping, err := extractor.Extract(context.Background(), item)
	if err != nil {
		t.Fatal(err)
	}
	workspace := newSimulationWorkspace(input, mapping, time.Unix(0, 0).UTC())
	if err := workspace.ingest(context.Background(), input, time.Unix(0, 0).UTC(), "first"); err != nil {
		t.Fatalf("local ingestion of real bundle failed: %v", err)
	}
	if workspace.vectorAvailable {
		t.Fatal("real local evidence unexpectedly enabled vector projection")
	}
	fused, _, _, err := workspace.retrieveAndCompose(context.Background(), item, 5)
	if err != nil {
		t.Fatalf("local textual retrieval of real bundle failed: %v", err)
	}
	if fused.Telemetry.VectorAvailable {
		t.Fatal("real local retrieval unexpectedly reported vector availability")
	}
	degraded := false
	for _, reason := range fused.DegradationReasons {
		if reason == "vector_unavailable" {
			degraded = true
			break
		}
	}
	if !degraded {
		t.Fatalf("real local retrieval degradation = %#v", fused.DegradationReasons)
	}
}

func TestRealEvaluationDoesNotCountPolicyAbstentionAsExpected(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeRealTestFile(t, filepath.Join(root, "app", "src", "main", "java", "tech", "buildrun", "controller", "BookingController.java"), javaClassAtLine("BookingController", 20))
	writeRealTestFile(t, filepath.Join(root, "app", "src", "main", "java", "tech", "buildrun", "service", "BookingService.java"), javaClassAtLine("BookingService", 32))
	writeRealTestFile(t, filepath.Join(root, "app", "src", "main", "java", "tech", "buildrun", "entity", "SeatEntity.java"), javaClassAtLine("SeatEntity", 6))

	cases := runnerFixture(t)
	var item EvaluationCase
	for _, candidate := range cases.Cases {
		if candidate.CaseID == "TM-FLOW-01" {
			item = candidate
			break
		}
	}
	if item.CaseID == "" {
		t.Fatal("TM-FLOW-01 was not found")
	}
	config := realTestCorpusConfig(root)
	config.CorpusID = item.CorpusID
	config.CorpusRevision = item.CorpusRevision
	config.SourceID = item.SourceID
	config.SourceRevision = item.SourceRevision
	config.Includes = []string{"app/src/main/java/**"}
	extractor, err := NewRealExtractor(config)
	if err != nil {
		t.Fatal(err)
	}
	report, err := Run(context.Background(), Config{
		Cases:       CaseSet{Version: cases.Version, Cases: []EvaluationCase{item}},
		RunID:       "real-policy-abstention-regression",
		TopK:        100,
		Repeat:      false,
		Extractor:   extractor,
		ToolVersion: defaultRealToolVersion,
		Now:         func() time.Time { return time.Unix(0, 0).UTC() },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Cases) != 1 {
		t.Fatalf("report cases = %d, want 1", len(report.Cases))
	}
	caseReport := report.Cases[0]
	if !caseReport.Generation.Abstained || caseReport.Generation.ExpectedAbstention || caseReport.Generation.AbstentionCorrect {
		t.Fatalf("policy abstention classification = %#v", caseReport.Generation)
	}
	if caseReport.Policy.Status != StageLimited || caseReport.Policy.ErrorCode != "transfer_not_authorized" || caseReport.Policy.Items == 0 {
		t.Fatalf("policy attribution = %#v", caseReport.Policy)
	}
	if caseReport.FailureStage != FailureStagePolicy || caseReport.FailureCode != "transfer_not_authorized" || caseReport.Outcome != "partial" {
		t.Fatalf("policy failure attribution = %#v", caseReport)
	}
	if report.Summary.ExpectedAbstentions != 0 || report.Summary.CorrectAbstentions != 0 {
		t.Fatalf("summary counted policy abstention as expected: %#v", report.Summary)
	}
}

func javaClassAtLine(name string, line int) string {
	if line < 1 {
		line = 1
	}
	return strings.Repeat("\n", line-1) + "package sample;\npublic class " + name + " {\n  public void run() {}\n}\n"
}

func TestRealExtractorRejectsCaseRevisionMismatch(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	extractor, err := NewRealExtractor(realTestCorpusConfig(root))
	if err != nil {
		t.Fatal(err)
	}
	item := EvaluationCase{
		CaseID:         "case",
		CorpusID:       "corpus",
		CorpusRevision: "wrong",
		SourceID:       "source",
		SourceRevision: "wrong",
	}
	_, _, err = extractor.Extract(context.Background(), item)
	if !errors.Is(err, ErrRealCorpusUnavailable) {
		t.Fatalf("Extract() error = %v, want ErrRealCorpusUnavailable", err)
	}
}

func TestMeasureCorpusReportsScaleMetadataWithoutSourceContent(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	pathName := filepath.Join(root, "module", "README.md")
	writeRealTestFile(t, pathName, "local corpus marker that must not appear in a measurement")
	config := realTestCorpusConfig(root)
	config.SourceRole = "scale"
	config.Includes = []string{"module/**"}
	extractor, err := NewRealExtractor(config)
	if err != nil {
		t.Fatal(err)
	}
	measurement, err := extractor.MeasureCorpus(context.Background(), config.SourceID)
	if err != nil {
		t.Fatalf("MeasureCorpus() error = %v", err)
	}
	if measurement.Extraction.Status != StageCompleted || measurement.Volume.Artifacts != 1 || measurement.Volume.EvidenceUnits == 0 {
		t.Fatalf("measurement = %#v", measurement)
	}
	if measurement.FactualDigest == "" || measurement.BytesRead == 0 || measurement.EffectiveConcurrency == 0 {
		t.Fatalf("measurement identity/metrics = %#v", measurement)
	}
	if len(measurement.NonApplicableStages) != 4 {
		t.Fatalf("non-applicable stages = %#v", measurement.NonApplicableStages)
	}
	encoded, err := json.Marshal(measurement)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "local corpus marker") || strings.Contains(string(encoded), root) {
		t.Fatalf("measurement contains source material or path: %s", encoded)
	}
}

func TestRealExtractorRejectsRelativeRoot(t *testing.T) {
	t.Parallel()
	config := realTestCorpusConfig(t.TempDir())
	config.Root = "relative/source"
	if _, err := NewRealExtractor(config); !errors.Is(err, ErrInvalidRealCorpus) {
		t.Fatalf("NewRealExtractor() error = %v, want ErrInvalidRealCorpus", err)
	}
}

// TestConfiguredRealCorpora is an explicit, opt-in execution hook for the
// authorized local corpora. It remains skipped in normal CI and never carries
// a repository path in the emitted JSON. The caller supplies roots and
// revisions as execution configuration instead of coupling them to Manu.
func TestConfiguredRealCorpora(t *testing.T) {
	configurationPath := strings.TrimSpace(os.Getenv("MANU_REAL_EVAL_CONFIG"))
	if configurationPath == "" {
		t.Skip("MANU_REAL_EVAL_CONFIG is not set")
	}
	file, err := os.Open(configurationPath)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var config struct {
		CasesPath string             `json:"cases_path"`
		RunID     string             `json:"run_id"`
		Corpora   []RealCorpusConfig `json:"corpora"`
	}
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		t.Fatal(err)
	}
	extractor, err := NewRealExtractor(config.Corpora...)
	if err != nil {
		t.Fatal(err)
	}
	cases, err := LoadCases(config.CasesPath)
	if err != nil {
		t.Fatal(err)
	}
	selected := make([]EvaluationCase, 0, len(cases.Cases))
	for _, item := range cases.Cases {
		if _, ok := extractor.Corpus(item.SourceID); ok {
			selected = append(selected, item)
		}
	}
	if len(selected) == 0 {
		t.Fatal("no evaluation cases match configured corpora")
	}
	report, err := Run(context.Background(), Config{
		Cases:       CaseSet{Version: cases.Version, Cases: selected},
		RunID:       config.RunID,
		Repeat:      true,
		Extractor:   extractor,
		ToolVersion: defaultRealToolVersion,
		Now:         func() time.Time { return time.Unix(0, 0).UTC() },
	})
	if err != nil {
		t.Fatal(err)
	}
	measurements := make([]RealCorpusMeasurement, 0, len(config.Corpora))
	for _, corpus := range config.Corpora {
		measurement, err := extractor.MeasureCorpus(context.Background(), corpus.SourceID)
		if err != nil {
			t.Fatal(err)
		}
		measurements = append(measurements, measurement)
	}
	output := struct {
		Report       Report                  `json:"report"`
		Measurements []RealCorpusMeasurement `json:"measurements"`
	}{Report: report, Measurements: measurements}
	encoded, err := json.Marshal(output)
	if err != nil {
		t.Fatal(err)
	}
	t.Log(string(encoded))
}

func realTestCorpusConfig(root string) RealCorpusConfig {
	return RealCorpusConfig{
		CorpusID:       "corpus",
		CorpusRevision: "manifest-revision",
		SourceID:       "source",
		SourceRevision: "source-revision",
		SourceName:     "test corpus",
		SourceRole:     "reference",
		OrganizationID: "evaluation-test",
		Root:           root,
		Limits:         sourceLimitsForTest(),
		EvidenceLimits: analysisEvidenceLimitsForTest(),
	}
}

func sourceLimitsForTest() source.Limits {
	return source.Limits{
		MaxFiles:                  16,
		MaxBytes:                  1 << 20,
		MaxFileBytes:              1 << 20,
		MaxConcurrency:            1,
		MaxProbeBytes:             4096,
		MaxExtractionBytes:        4096,
		MaxArchiveMembers:         64,
		MaxArchiveBytes:           1 << 20,
		MaxArchiveMemberBytes:     1 << 20,
		MaxArchiveCompressedBytes: 1 << 20,
		MaxExpansionRatio:         100,
	}
}

func analysisEvidenceLimitsForTest() analysis.EvidenceLimits {
	return analysis.EvidenceLimits{MaxUnitsPerArtifact: 4, MaxBytesPerUnit: 512, MaxCharactersPerUnit: 512}
}

func writeRealTestFile(t *testing.T, filePath, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filePath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func fileDigest(t *testing.T, filePath string) string {
	t.Helper()
	content, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])
}

// contractLocatorForTest keeps test setup local while the production matcher
// consumes the canonical contract.Locator shape.
func contractLocatorForTest(pathName, member string, startLine, endLine int) *contract.Locator {
	return &contract.Locator{Path: pathName, Member: member, StartLine: startLine, EndLine: endLine}
}
