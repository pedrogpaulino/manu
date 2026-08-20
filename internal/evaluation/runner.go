package evaluation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/pedrogpaulino/manu/internal/bundle"
	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/evidence"
	"github.com/pedrogpaulino/manu/internal/ingestion"
	"github.com/pedrogpaulino/manu/internal/query"
	"github.com/pedrogpaulino/manu/internal/retrieval"
)

const (
	// ReportVersion identifies the versioned evaluation report shared by
	// simulated and explicitly authorized live runs.
	ReportVersion = "v1alpha1"
	// DefaultCasesPath is used only when callers do not inject a CaseSet or
	// provide a path. It is a repository fixture path, not a source path.
	DefaultCasesPath = "testdata/evaluation/cases.json"
	// defaultEvaluationOrganization is a synthetic organization boundary. It
	// is never persisted outside the local simulated run.
	defaultEvaluationOrganization = "evaluation-fixture"
	defaultTopK                   = 5
)

// Mode identifies the evaluation execution mode. Run remains simulation-only;
// RunLive is the separately gated live entry point.
type Mode string

const (
	ModeSimulated Mode = "simulated"
	ModeLive      Mode = "live"
)

// FailureStage is the controlled attribution vocabulary used by the report.
// Ingestion is kept separate from extraction because a valid bundle may fail
// while being projected without implying an analyzer failure.
type FailureStage string

const (
	FailureStageExtraction FailureStage = "extraction"
	FailureStageIngestion  FailureStage = "ingestion"
	FailureStageRetrieval  FailureStage = "retrieval"
	FailureStageGeneration FailureStage = "generation"
	FailureStagePolicy     FailureStage = "policy"
)

// StageStatus is deliberately small so reports from different runs remain
// comparable without exposing provider diagnostics.
type StageStatus string

const (
	StageCompleted StageStatus = "completed"
	StageLimited   StageStatus = "limited"
	StageFailed    StageStatus = "failed"
	StageSkipped   StageStatus = "skipped"
)

// Extractor is the local extraction port consumed by the simulated runner.
// The map associates each metadata-only case evidence ID with the canonical
// Evidence Unit ID materialized in the returned bundle. Implementations must
// not put source content in errors.
type Extractor interface {
	Extract(context.Context, EvaluationCase) (bundle.Bundle, map[string]string, error)
}

// Config controls one reproducible simulated evaluation. Cases may be
// injected directly by tests and tooling; otherwise CasesPath is loaded with
// the strict 9.1 loader. No field contains a provider credential.
type Config struct {
	Cases       CaseSet
	CasesPath   string
	Mode        Mode
	RunID       string
	TopK        int
	Repeat      bool
	ToolVersion string
	Now         func() time.Time
	Extractor   Extractor
}

// EvaluationConfig is the descriptive spelling used by CLI integration.
type EvaluationConfig = Config

// Environment records safe runtime metadata needed to interpret local
// measurements. It deliberately omits hostname, paths and credentials.
type Environment struct {
	GoVersion string `json:"go_version"`
	GOOS      string `json:"goos"`
	GOARCH    string `json:"goarch"`
}

// StageMetric contains duration and aggregate work counters for one stage.
// Durations are measurements of this local process and are not SLA claims.
type StageMetric struct {
	Status       StageStatus   `json:"status"`
	Duration     time.Duration `json:"duration"`
	Items        int           `json:"items,omitempty"`
	Reused       int           `json:"reused,omitempty"`
	Reprocessed  int           `json:"reprocessed,omitempty"`
	ErrorCode    string        `json:"error_code,omitempty"`
	FailureStage FailureStage  `json:"failure_stage,omitempty"`
}

// VolumeMetric contains counts only; no source content is serialized.
type VolumeMetric struct {
	Artifacts        int   `json:"artifacts"`
	Contributions    int   `json:"contributions"`
	EvidenceUnits    int   `json:"evidence_units"`
	TextCandidates   int   `json:"text_candidates"`
	VectorCandidates int   `json:"vector_candidates"`
	PackageUnits     int   `json:"package_units"`
	ContentBytes     int64 `json:"content_bytes"`
}

// RetrievalMetric reports rank quality using metadata-only Evidence IDs.
type RetrievalMetric struct {
	K                     int     `json:"k"`
	ExpectedEvidence      int     `json:"expected_evidence"`
	RetrievedEvidence     int     `json:"retrieved_evidence"`
	RelevantAtK           int     `json:"relevant_at_k"`
	EvidenceRecallAtK     float64 `json:"evidence_recall_at_k"`
	EvidencePrecisionAtK  float64 `json:"evidence_precision_at_k"`
	FirstEvidencePosition int     `json:"first_evidence_position"`
	// FirstEvidenceLatency is the elapsed wall-clock time of the complete
	// retrieval/ranking stage when a relevant result is present. It is not a
	// timestamp and does not claim that ranking stopped at the first hit.
	FirstEvidenceLatency    time.Duration `json:"first_evidence_latency"`
	IdentityProvenanceValid int           `json:"identity_provenance_valid"`
	EmbeddingAvailable      bool          `json:"embedding_available"`
	EmbeddingProfileID      string        `json:"embedding_profile_id,omitempty"`
	DegradationReasons      []string      `json:"degradation_reasons,omitempty"`
}

// LiveEvidenceRef identifies content that was eligible for external
// transfer. It intentionally carries only the stable ID and content hash;
// reports never serialize the transferred text.
type LiveEvidenceRef struct {
	EvidenceID  string `json:"evidence_id"`
	ContentHash string `json:"content_hash"`
}

// LiveCallMetric records one externally authorized call without provider
// payloads, credentials, prompts, or response text.
type LiveCallMetric struct {
	CaseID              string            `json:"case_id"`
	RequestID           string            `json:"request_id"`
	Provider            string            `json:"provider"`
	Model               string            `json:"model"`
	InputTokens         int               `json:"input_tokens"`
	OutputTokens        int               `json:"output_tokens"`
	EstimatedCostUSD    float64           `json:"estimated_cost_usd"`
	ActualCostUSD       float64           `json:"actual_cost_usd"`
	Latency             time.Duration     `json:"latency"`
	Outcome             string            `json:"outcome"`
	Termination         string            `json:"termination"`
	ErrorCode           string            `json:"error_code,omitempty"`
	OutputDigest        string            `json:"output_digest,omitempty"`
	TransferredEvidence []LiveEvidenceRef `json:"transferred_evidence,omitempty"`
}

// LiveBudgetConfig is the positive, independent budget for one live
// evaluation. Every dimension is required even when the provider is expected
// to be inexpensive; zero means that live execution is not authorized.
type LiveBudgetConfig struct {
	MaxRequests     int     `json:"max_requests"`
	MaxInputTokens  int     `json:"max_input_tokens"`
	MaxOutputTokens int     `json:"max_output_tokens"`
	MaxCostUSD      float64 `json:"max_cost_usd"`
}

// LivePriceTable is an explicit, versioned price table. Manu never invents a
// price; a missing version or missing per-token prices blocks live execution.
type LivePriceTable struct {
	Version        string  `json:"version"`
	Provider       string  `json:"provider"`
	Model          string  `json:"model"`
	InputTokenUSD  float64 `json:"input_token_usd"`
	OutputTokenUSD float64 `json:"output_token_usd"`
}

// LiveUsageSnapshot contains only counters and cost calculated from the
// explicit price table. Reserved counters are conservative reservations made
// before calls; actual counters are provider-reported usage.
type LiveUsageSnapshot struct {
	Requests             int     `json:"requests"`
	ReservedInputTokens  int     `json:"reserved_input_tokens"`
	ReservedOutputTokens int     `json:"reserved_output_tokens"`
	ReservedCostUSD      float64 `json:"reserved_cost_usd"`
	ActualInputTokens    int     `json:"actual_input_tokens"`
	ActualOutputTokens   int     `json:"actual_output_tokens"`
	ActualCostUSD        float64 `json:"actual_cost_usd"`
}

// LiveReport contains the safe audit envelope for an explicitly authorized
// live evaluation. It has no provider credential or raw transfer content.
type LiveReport struct {
	Provider            string            `json:"provider"`
	RequestedModel      string            `json:"requested_model"`
	TransferPolicy      string            `json:"transfer_policy"`
	PolicyConfirmed     bool              `json:"policy_confirmed"`
	TransferConfirmed   bool              `json:"transfer_confirmed"`
	PriceTable          LivePriceTable    `json:"price_table"`
	Budget              LiveBudgetConfig  `json:"budget"`
	Usage               LiveUsageSnapshot `json:"usage"`
	Calls               []LiveCallMetric  `json:"calls,omitempty"`
	TransferredEvidence []LiveEvidenceRef `json:"transferred_evidence,omitempty"`
	Halted              bool              `json:"halted"`
	HaltCode            string            `json:"halt_code,omitempty"`
}

// GenerationMetric reports response and abstention validation without
// persisting generated text or prompts.
type GenerationMetric struct {
	Status             StageStatus   `json:"status"`
	Duration           time.Duration `json:"duration"`
	Termination        string        `json:"termination"`
	Provider           string        `json:"provider"`
	Model              string        `json:"model"`
	Profile            string        `json:"profile"`
	InputTokens        int           `json:"input_tokens"`
	OutputTokens       int           `json:"output_tokens"`
	LocalOnly          bool          `json:"local_only"`
	ValidClaims        int           `json:"valid_claims"`
	TotalClaims        int           `json:"total_claims"`
	ValidCitations     int           `json:"valid_citations"`
	TotalCitations     int           `json:"total_citations"`
	Abstained          bool          `json:"abstained"`
	ExpectedAbstention bool          `json:"expected_abstention"`
	AbstentionCorrect  bool          `json:"abstention_correct"`
	UnsupportedClaims  int           `json:"unsupported_claims"`
	ErrorCode          string        `json:"error_code,omitempty"`
}

// ReuseMetric describes work reused by the second deterministic pass.
type ReuseMetric struct {
	EvidenceReused        int `json:"evidence_reused"`
	EvidenceReprocessed   int `json:"evidence_reprocessed"`
	EmbeddingsReused      int `json:"embeddings_reused"`
	EmbeddingsReprocessed int `json:"embeddings_reprocessed"`
}

// RetrievedEvidence is a content-free ranked result retained for audit.
type RetrievedEvidence struct {
	EvidenceID  string `json:"evidence_id"`
	ContentHash string `json:"content_hash"`
	Rank        int    `json:"rank"`
	Relevant    bool   `json:"relevant"`
	InPackage   bool   `json:"in_package"`
}

// CaseReport contains all measurements for one versioned evaluation case.
type CaseReport struct {
	CaseID            string              `json:"case_id"`
	CaseVersion       int                 `json:"case_version"`
	CorpusID          string              `json:"corpus_id"`
	CorpusRevision    string              `json:"corpus_revision"`
	SourceID          string              `json:"source_id"`
	SourceRevision    string              `json:"source_revision"`
	SnapshotID        string              `json:"snapshot_id,omitempty"`
	Mode              Mode                `json:"mode"`
	Outcome           string              `json:"outcome"`
	FailureStage      FailureStage        `json:"failure_stage,omitempty"`
	FailureCode       string              `json:"failure_code,omitempty"`
	Extraction        StageMetric         `json:"extraction"`
	Ingestion         StageMetric         `json:"ingestion"`
	Retrieval         StageMetric         `json:"retrieval"`
	Generation        GenerationMetric    `json:"generation"`
	Policy            StageMetric         `json:"policy"`
	RetrievalMetrics  RetrievalMetric     `json:"retrieval_metrics"`
	Volume            VolumeMetric        `json:"volume"`
	Reuse             ReuseMetric         `json:"reuse"`
	RetrievedEvidence []RetrievedEvidence `json:"retrieved_evidence,omitempty"`
	Limitations       []string            `json:"limitations,omitempty"`
}

// Summary aggregates count and quality metrics without averaging away case
// identity. The denominator fields make empty or inapplicable sets explicit.
type Summary struct {
	Cases                    int     `json:"cases"`
	Completed                int     `json:"completed"`
	Failed                   int     `json:"failed"`
	ExpectedAbstentions      int     `json:"expected_abstentions"`
	CorrectAbstentions       int     `json:"correct_abstentions"`
	EvidenceRecallAtKMean    float64 `json:"evidence_recall_at_k_mean"`
	EvidencePrecisionAtKMean float64 `json:"evidence_precision_at_k_mean"`
	ValidClaims              int     `json:"valid_claims"`
	ValidCitations           int     `json:"valid_citations"`
	ContentBytes             int64   `json:"content_bytes"`
	EvidenceReused           int     `json:"evidence_reused"`
	EvidenceReprocessed      int     `json:"evidence_reprocessed"`
}

// Report is the versioned, safe output of one simulated or live evaluation. Digest is
// computed from stable case identity and metric values, excluding local clock
// measurements so equivalent runs can be compared reproducibly.
type Report struct {
	Version               string       `json:"version"`
	Mode                  Mode         `json:"mode"`
	RunID                 string       `json:"run_id"`
	CasesVersion          string       `json:"cases_version"`
	EngineVersion         string       `json:"engine_version"`
	BundleVersion         string       `json:"bundle_version"`
	ContractVersion       string       `json:"contract_version"`
	StartedAt             time.Time    `json:"started_at"`
	FinishedAt            time.Time    `json:"finished_at"`
	Environment           Environment  `json:"environment"`
	Cases                 []CaseReport `json:"cases"`
	Summary               Summary      `json:"summary"`
	Live                  *LiveReport  `json:"live,omitempty"`
	ReproducibilityDigest string       `json:"reproducibility_digest"`
	Limitations           []string     `json:"limitations"`
}

// Run executes the configured simulated mode. Live providers remain behind
// the separately validated RunLive entry point so the default stays local.
func Run(ctx context.Context, config Config) (Report, error) {
	if ctx == nil {
		return Report{}, errors.New("evaluation: nil context")
	}
	if err := ctx.Err(); err != nil {
		return Report{}, err
	}
	if config.Mode == "" {
		config.Mode = ModeSimulated
	}
	if config.Mode != ModeSimulated {
		return Report{}, fmt.Errorf("evaluation: mode %q is not available", config.Mode)
	}
	cases, err := loadConfiguredCases(config)
	if err != nil {
		return Report{}, err
	}
	now := time.Now().UTC()
	if config.Now != nil {
		now = config.Now().UTC()
	}
	runID := strings.TrimSpace(config.RunID)
	if runID == "" {
		runID = "eval-" + digestString(casesIdentity(cases))[:16]
	}
	topK := config.TopK
	if topK == 0 {
		topK = defaultTopK
	}
	if topK < 1 || topK > retrieval.MaxFusionCandidates {
		return Report{}, errors.New("evaluation: top-k is outside the bounded range")
	}

	report := Report{
		Version:         ReportVersion,
		Mode:            config.Mode,
		RunID:           runID,
		CasesVersion:    cases.Version,
		EngineVersion:   config.ToolVersion,
		BundleVersion:   bundle.Version,
		ContractVersion: contract.Version,
		StartedAt:       now,
		Environment:     Environment{GoVersion: runtime.Version(), GOOS: runtime.GOOS, GOARCH: runtime.GOARCH},
		Limitations: []string{
			"execução local sem provedor externo; latências não são SLA",
			"custo externo não é medido porque não há provedor externo",
		},
	}
	if report.EngineVersion == "" {
		report.EngineVersion = "evaluation-simulator-v1"
	}
	if !config.Repeat {
		report.Limitations = append(report.Limitations, "repetição desabilitada; métricas de reuso permanecem zero")
	}

	extractor := config.Extractor
	if extractor == nil {
		extractor = simulatedExtractor{}
		report.Limitations = append(report.Limitations, "o extrator padrão materializa metadados da fixture, não analisa uma fonte externa")
	} else {
		report.Limitations = append(report.Limitations, "o extrator injetado analisa a fonte local por bounded analyzers; a política externa permanece sem transferência")
	}
	for _, item := range cases.Cases {
		if err := ctx.Err(); err != nil {
			return Report{}, err
		}
		caseReport := runCase(ctx, item, extractor, topK, config.Repeat, now)
		report.Cases = append(report.Cases, caseReport)
		if err := ctx.Err(); err != nil {
			return Report{}, err
		}
	}
	report.FinishedAt = time.Now().UTC()
	if config.Now != nil {
		report.FinishedAt = config.Now().UTC()
	}
	report.Summary = summarize(report.Cases)
	report.ReproducibilityDigest = reportDigest(report)
	return report, nil
}

// RunSimulated is an explicit spelling useful to callers that want to make
// the no-network policy visible at the call site.
func RunSimulated(ctx context.Context, config Config) (Report, error) {
	config.Mode = ModeSimulated
	return Run(ctx, config)
}

func loadConfiguredCases(config Config) (CaseSet, error) {
	if len(config.Cases.Cases) != 0 || config.Cases.Version != "" {
		return config.Cases.Normalize()
	}
	path := strings.TrimSpace(config.CasesPath)
	if path == "" {
		path = DefaultCasesPath
	}
	return LoadCases(path)
}

func runCase(ctx context.Context, item EvaluationCase, extractor Extractor, topK int, repeat bool, now time.Time) CaseReport {
	result := CaseReport{
		CaseID: item.CaseID, CaseVersion: item.CaseVersion, CorpusID: item.CorpusID,
		CorpusRevision: item.CorpusRevision, SourceID: item.SourceID, SourceRevision: item.SourceRevision,
		Mode: ModeSimulated, Outcome: "failed",
		Extraction: StageMetric{Status: StageSkipped}, Ingestion: StageMetric{Status: StageSkipped},
		Retrieval: StageMetric{Status: StageSkipped}, Generation: GenerationMetric{Status: StageSkipped}, Policy: StageMetric{Status: StageSkipped},
		RetrievalMetrics: RetrievalMetric{K: topK, FirstEvidencePosition: -1, ExpectedEvidence: len(item.ExpectedEvidence)},
	}
	started := time.Now()
	bundleInput, expectedToCanonical, err := extractor.Extract(ctx, item)
	result.Extraction.Duration = time.Since(started)
	if err != nil {
		result.Extraction = failedStage(result.Extraction.Duration, FailureStageExtraction, "extraction_failed")
		result.FailureStage, result.FailureCode = FailureStageExtraction, "extraction_failed"
		result.Limitations = append(result.Limitations, "a extração não produziu um bundle utilizável")
		return result
	}
	if err := bundleInput.Validate(); err != nil {
		result.Extraction = failedStage(result.Extraction.Duration, FailureStageExtraction, "bundle_invalid")
		result.FailureStage, result.FailureCode = FailureStageExtraction, "bundle_invalid"
		return result
	}
	result.SnapshotID = bundleInput.Manifest.Snapshot.ID
	result.Extraction.Status = StageCompleted
	result.Extraction.Items = len(bundleInput.Evidence)
	result.Extraction.Reprocessed = len(bundleInput.Evidence)
	result.Volume = VolumeMetric{Artifacts: len(bundleInput.Artifacts), Contributions: len(bundleInput.Contributions), EvidenceUnits: len(bundleInput.Evidence), ContentBytes: evidenceBytes(bundleInput.Evidence)}

	workspace := newSimulationWorkspace(bundleInput, expectedToCanonical, now)
	firstIngestion := time.Now()
	if err := workspace.ingest(ctx, bundleInput, now, "first"); err != nil {
		result.Ingestion = failedStage(time.Since(firstIngestion), FailureStageIngestion, "ingestion_failed")
		result.FailureStage, result.FailureCode = FailureStageIngestion, "ingestion_failed"
		return result
	}
	result.Ingestion = StageMetric{Status: StageCompleted, Duration: time.Since(firstIngestion), Items: len(bundleInput.Evidence), Reprocessed: len(bundleInput.Evidence)}
	if repeat {
		repeatStarted := time.Now()
		if err := workspace.ingest(ctx, bundleInput, now, "repeat"); err != nil {
			result.Ingestion.Status = StageLimited
			result.Ingestion.ErrorCode = "repeat_ingestion_failed"
			result.Limitations = append(result.Limitations, "a repetição não foi concluída")
		} else {
			result.Ingestion.Duration += time.Since(repeatStarted)
			result.Ingestion.Reused = workspace.reusedEvidence
			result.Ingestion.Reprocessed = len(bundleInput.Evidence)
			result.Reuse = ReuseMetric{
				EvidenceReused:        workspace.reusedEvidence,
				EvidenceReprocessed:   len(bundleInput.Evidence),
				EmbeddingsReused:      workspace.embeddingReused,
				EmbeddingsReprocessed: workspace.embeddingReprocessed,
			}
		}
	}

	retrievalStarted := time.Now()
	fused, composition, packageScope, err := workspace.retrieveAndCompose(ctx, item, topK)
	result.Retrieval.Duration = time.Since(retrievalStarted)
	if err != nil {
		result.Retrieval = failedStage(result.Retrieval.Duration, FailureStageRetrieval, "retrieval_failed")
		result.FailureStage, result.FailureCode = FailureStageRetrieval, "retrieval_failed"
		return result
	}
	result.Retrieval.Status = StageCompleted
	result.Retrieval.Items = len(fused.Candidates)
	result.RetrievalMetrics = retrievalMetrics(item, fused, workspace, topK, result.Retrieval.Duration)
	result.RetrievalMetrics.DegradationReasons = append([]string(nil), fused.DegradationReasons...)
	result.RetrievalMetrics.EmbeddingAvailable = fused.Telemetry.VectorAvailable
	if len(fused.Candidates) > 0 {
		for _, signal := range fused.Candidates[0].Signals {
			if signal.ProfileID != "" {
				result.RetrievalMetrics.EmbeddingProfileID = signal.ProfileID
				break
			}
		}
	}
	result.Volume.TextCandidates = fused.Telemetry.TextualInputCount
	result.Volume.VectorCandidates = fused.Telemetry.VectorInputCount
	result.Volume.PackageUnits = composition.UnitCount
	result.Policy = StageMetric{Status: StageCompleted, Duration: 0, Items: composition.UnitCount}
	policyExcluded := workspace.policyExcluded(item, fused, composition)
	localOnlyEvidenceCount := workspace.localOnlyEvidenceCount()
	if policyExcluded > 0 || localOnlyEvidenceCount > 0 {
		result.Policy.Status = StageLimited
		result.Policy.ErrorCode = "transfer_not_authorized"
		result.Policy.FailureStage = FailureStagePolicy
		result.Policy.Items = policyExcluded
		if result.Policy.Items == 0 {
			result.Policy.Items = localOnlyEvidenceCount
		}
		if policyExcluded > 0 {
			result.Limitations = append(result.Limitations, "parte da evidência recuperada não foi autorizada para transferência")
		} else {
			result.Limitations = append(result.Limitations, "há evidência local não autorizada para transferência")
		}
	}

	generationStarted := time.Now()
	generation, response, err := workspace.generate(ctx, item, composition, packageScope, fused, localOnlyEvidenceCount)
	result.Generation.Duration = time.Since(generationStarted)
	if err != nil {
		result.Generation.Status = StageFailed
		result.Generation.ErrorCode = "generation_failed"
		result.FailureStage, result.FailureCode = FailureStageGeneration, "generation_failed"
		return result
	}
	result.Generation = generation
	if err := (query.ResponseValidationContext{Package: composition.ValidationPackage, QueryID: queryID(item), QueryDigest: queryDigest(item.CompetenceQuestion)}).Validate(response); err != nil {
		result.Generation.Status = StageFailed
		result.Generation.ErrorCode = "response_invalid"
		result.FailureStage, result.FailureCode = FailureStageGeneration, "response_invalid"
		return result
	}
	result.RetrievedEvidence = workspace.retrievedEvidence(fused, composition)
	result.Generation.Status = StageCompleted
	if result.RetrievalMetrics.EvidenceRecallAtK < 1 {
		result.FailureStage, result.FailureCode = FailureStageRetrieval, "evidence_recall_below_expected"
	} else if item.Kind == CaseKindAbstention && !result.Generation.AbstentionCorrect {
		result.FailureStage, result.FailureCode = FailureStageGeneration, "abstention_mismatch"
	} else if result.Generation.Abstained && !result.Generation.ExpectedAbstention {
		if policyExcluded > 0 || localOnlyEvidenceCount > 0 {
			result.FailureStage, result.FailureCode = FailureStagePolicy, "transfer_not_authorized"
		} else {
			result.FailureStage, result.FailureCode = FailureStageGeneration, "unexpected_abstention"
		}
	} else if policyExcluded > 0 || localOnlyEvidenceCount > 0 {
		result.FailureStage, result.FailureCode = FailureStagePolicy, "transfer_not_authorized"
	}
	if result.FailureStage == "" {
		result.Outcome = "passed"
	} else {
		result.Outcome = "partial"
	}
	return result
}

func failedStage(duration time.Duration, stage FailureStage, code string) StageMetric {
	return StageMetric{Status: StageFailed, Duration: duration, ErrorCode: code, FailureStage: stage}
}

func evidenceBytes(units []evidence.EvidenceUnit) int64 {
	var total int64
	for _, unit := range units {
		total += int64(len([]byte(unit.Content)))
	}
	return total
}

func retrievalMetrics(item EvaluationCase, fused retrieval.FusionResponse, workspace *simulationWorkspace, topK int, latency time.Duration) RetrievalMetric {
	relevant := make(map[string]struct{}, len(item.ExpectedEvidence))
	for index, expected := range item.ExpectedEvidence {
		if canonical := workspace.expectedCanonical[caseEvidenceKey(expected, index)]; canonical != "" {
			relevant[canonical] = struct{}{}
		}
	}
	limit := topK
	if len(fused.Candidates) < limit {
		limit = len(fused.Candidates)
	}
	relevantAtK := 0
	first := -1
	validProvenance := 0
	for index, candidate := range fused.Candidates {
		if _, ok := relevant[candidate.EvidenceID]; ok && first == -1 {
			first = index + 1
		}
		if candidate.Provenance.EvidenceID == candidate.EvidenceID &&
			candidate.Provenance.OrganizationID == candidate.OrganizationID &&
			candidate.Provenance.SourceID == candidate.SourceID &&
			candidate.Provenance.SnapshotID == candidate.SnapshotID {
			validProvenance++
		}
	}
	retrieved := len(fused.Candidates)
	if retrieved > limit {
		retrieved = limit
	}
	relevantAtK = 0
	for index := 0; index < limit; index++ {
		if _, ok := relevant[fused.Candidates[index].EvidenceID]; ok {
			relevantAtK++
		}
	}
	expected := len(item.ExpectedEvidence)
	recall := 0.0
	if expected > 0 {
		recall = float64(relevantAtK) / float64(expected)
	}
	precision := 0.0
	if retrieved > 0 {
		precision = float64(relevantAtK) / float64(retrieved)
	}
	if first == -1 {
		latency = 0
	}
	return RetrievalMetric{
		K: topK, ExpectedEvidence: expected, RetrievedEvidence: retrieved,
		RelevantAtK: relevantAtK, EvidenceRecallAtK: recall, EvidencePrecisionAtK: precision,
		FirstEvidencePosition: first, FirstEvidenceLatency: latency,
		IdentityProvenanceValid: validProvenance,
		EmbeddingAvailable:      fused.Telemetry.VectorAvailable,
	}
}

func caseEvidenceKey(expected ExpectedEvidence, index int) string {
	if expected.EvidenceID != "" {
		return expected.EvidenceID
	}
	return fmt.Sprintf("expected-%03d", index+1)
}

func (w *simulationWorkspace) retrievedEvidence(fused retrieval.FusionResponse, composition query.Composition) []RetrievedEvidence {
	inPackage := make(map[string]struct{}, len(composition.ValidationPackage.Evidence))
	for _, item := range composition.ValidationPackage.Evidence {
		for _, candidate := range fused.Candidates {
			packaged, ok := w.packageUnitForCanonical(candidate.EvidenceID)
			if ok && packaged.ID == item.ID {
				inPackage[candidate.EvidenceID] = struct{}{}
			}
		}
	}
	result := make([]RetrievedEvidence, 0, len(fused.Candidates))
	for _, candidate := range fused.Candidates {
		_, included := inPackage[candidate.EvidenceID]
		_, relevant := w.expectedCanonicalEvidence(candidate.EvidenceID)
		result = append(result, RetrievedEvidence{EvidenceID: candidate.EvidenceID, ContentHash: candidate.Provenance.EvidenceContentHash, Rank: candidate.Rank, Relevant: relevant, InPackage: included})
	}
	return result
}

func summarize(cases []CaseReport) Summary {
	summary := Summary{Cases: len(cases)}
	for _, item := range cases {
		switch item.Outcome {
		case "passed":
			summary.Completed++
		case "failed", "partial":
			summary.Failed++
		}
		if item.Generation.ExpectedAbstention {
			summary.ExpectedAbstentions++
			if item.Generation.AbstentionCorrect {
				summary.CorrectAbstentions++
			}
		}
		summary.EvidenceRecallAtKMean += item.RetrievalMetrics.EvidenceRecallAtK
		summary.EvidencePrecisionAtKMean += item.RetrievalMetrics.EvidencePrecisionAtK
		summary.ValidClaims += item.Generation.ValidClaims
		summary.ValidCitations += item.Generation.ValidCitations
		summary.ContentBytes += item.Volume.ContentBytes
		summary.EvidenceReused += item.Reuse.EvidenceReused
		summary.EvidenceReprocessed += item.Reuse.EvidenceReprocessed
	}
	if len(cases) > 0 {
		summary.EvidenceRecallAtKMean /= float64(len(cases))
		summary.EvidencePrecisionAtKMean /= float64(len(cases))
	}
	return summary
}

func reportDigest(report Report) string {
	stableCases := make([]CaseReport, len(report.Cases))
	for index, item := range report.Cases {
		stableCases[index] = item
		stableCases[index].Extraction.Duration = 0
		stableCases[index].Ingestion.Duration = 0
		stableCases[index].Retrieval.Duration = 0
		stableCases[index].Generation.Duration = 0
		stableCases[index].Policy.Duration = 0
		stableCases[index].RetrievalMetrics.FirstEvidenceLatency = 0
	}
	stable := struct {
		Version         string       `json:"version"`
		Mode            Mode         `json:"mode"`
		CasesVersion    string       `json:"cases_version"`
		EngineVersion   string       `json:"engine_version"`
		BundleVersion   string       `json:"bundle_version"`
		ContractVersion string       `json:"contract_version"`
		Cases           []CaseReport `json:"cases"`
		Summary         Summary      `json:"summary"`
	}{Version: report.Version, Mode: report.Mode, CasesVersion: report.CasesVersion, EngineVersion: report.EngineVersion, BundleVersion: report.BundleVersion, ContractVersion: report.ContractVersion, Cases: stableCases, Summary: report.Summary}
	data, _ := json.Marshal(stable)
	return digestString(string(data))
}

// MarshalReport returns the canonical indented JSON representation of a
// report. Source content and provider diagnostics are not representable in
// Report, so serialization cannot accidentally publish them.
func MarshalReport(report Report) ([]byte, error) {
	if report.Version != ReportVersion || (report.Mode != ModeSimulated && report.Mode != ModeLive) || strings.TrimSpace(report.RunID) == "" {
		return nil, errors.New("evaluation: invalid report identity")
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return nil, errors.New("evaluation: report serialization failed")
	}
	return append(data, '\n'), nil
}

// WriteReport writes the canonical report representation without taking
// ownership of the destination.
func WriteReport(writer io.Writer, report Report) error {
	if writer == nil {
		return errors.New("evaluation: nil report writer")
	}
	data, err := MarshalReport(report)
	if err != nil {
		return err
	}
	if _, err := writer.Write(data); err != nil {
		return errors.New("evaluation: report write failed")
	}
	return nil
}

func casesIdentity(cases CaseSet) string {
	ids := make([]string, 0, len(cases.Cases))
	for _, item := range cases.Cases {
		ids = append(ids, fmt.Sprintf("%s:%d:%s:%s", item.CaseID, item.CaseVersion, item.SourceID, item.SourceRevision))
	}
	sort.Strings(ids)
	return cases.Version + "\x00" + strings.Join(ids, "\x00")
}

func digestString(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func queryID(item EvaluationCase) string {
	return "query-" + digestString(item.CaseID + "\x00" + fmt.Sprint(item.CaseVersion))[:24]
}

func queryDigest(question string) string { return digestString(question) }

// Keep these compile-time checks close to the runner's public ports.
var _ Extractor = simulatedExtractor{}
var _ ingestion.BundleLoader = (*simulationLoader)(nil)
var _ ingestion.CanonicalPersister = (*simulationCanonical)(nil)
var _ ingestion.TextProjector = (*simulationProjection)(nil)
var _ ingestion.RelationalValidator = (*simulationProjection)(nil)
var _ ingestion.SnapshotActivator = (*simulationProjection)(nil)
var _ ingestion.EmbeddingEvidenceSource = (*simulationCanonical)(nil)
var _ retrieval.TextStore = (*simulationProjection)(nil)
var _ retrieval.EmbeddingStore = (*simulationEmbeddings)(nil)
