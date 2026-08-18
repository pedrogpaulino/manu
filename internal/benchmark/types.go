// Package benchmark executes and reports the first deterministic runtime
// microcut. It deliberately keeps benchmark output outside the source root.
package benchmark

import (
	"time"

	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/source"
)

const (
	// Version identifies the benchmark report schema independently from the
	// result contract written by the analysis engine.
	Version = "v1alpha1"

	ScenarioFirstAnalysis   Scenario = "first_analysis"
	ScenarioRepeatUnchanged Scenario = "repeat_unchanged"
	ScenarioLocalizedUpdate Scenario = "localized_update"
)

// Scenario identifies one of the three required benchmark phases.
type Scenario string

// Config describes a reproducible benchmark invocation. Output is a separate
// destination and must not be inside Root. Update.Path is optional; when it is
// empty, the first textual artifact (or the first artifact) is selected by
// stable path order.
type Config struct {
	Source            contract.Source `json:"source"`
	Root              string          `json:"root"`
	Output            string          `json:"output"`
	Includes          []string        `json:"includes,omitempty"`
	Excludes          []string        `json:"excludes,omitempty"`
	SensitivePatterns []string        `json:"sensitive_patterns,omitempty"`
	IncludeSensitive  bool            `json:"include_sensitive,omitempty"`
	Limits            source.Limits   `json:"limits"`
	Update            UpdateConfig    `json:"update"`
	ToolVersion       string          `json:"tool_version,omitempty"`
}

// UpdateConfig controls the localized overlay. The original source is never
// edited. A temporary regular-file staging tree is used because the runtime
// rejects symlinks and must see the update as a normal authorized source.
type UpdateConfig struct {
	Path   string `json:"path,omitempty"`
	Marker string `json:"marker,omitempty"`
}

// Report is the structured output of one benchmark invocation. Timestamps,
// run IDs, host and paths identify the local experiment; they are not SLA or
// capacity claims.
type Report struct {
	ContractVersion       string           `json:"contract_version"`
	BenchmarkVersion      string           `json:"benchmark_version"`
	RunID                 string           `json:"run_id"`
	StartedAt             time.Time        `json:"started_at"`
	FinishedAt            time.Time        `json:"finished_at"`
	Source                contract.Source  `json:"source"`
	Configuration         Configuration    `json:"configuration"`
	Environment           Environment      `json:"environment"`
	Integrity             IntegrityReport  `json:"integrity"`
	Scenarios             []ScenarioReport `json:"scenarios"`
	RepeatEquivalentFacts bool             `json:"repeat_equivalent_facts"`
	Partial               bool             `json:"partial"`
	Limitations           []string         `json:"limitations,omitempty"`
	Unavailable           []string         `json:"unavailable,omitempty"`
}

// Configuration is the normalized, non-secret configuration recorded in the
// report. It intentionally omits any source content and credentials.
type Configuration struct {
	ID                string        `json:"id"`
	Root              string        `json:"root"`
	Output            string        `json:"output"`
	SourceID          string        `json:"source_id"`
	Revision          string        `json:"revision,omitempty"`
	Includes          []string      `json:"includes,omitempty"`
	Excludes          []string      `json:"excludes,omitempty"`
	SensitivePatterns []string      `json:"sensitive_patterns,omitempty"`
	IncludeSensitive  bool          `json:"include_sensitive,omitempty"`
	Limits            source.Limits `json:"limits"`
	UpdatePath        string        `json:"update_path,omitempty"`
	OverlayMethod     string        `json:"overlay_method"`
}

// Environment records values needed to interpret a local measurement.
type Environment struct {
	GOOS       string `json:"goos"`
	GOARCH     string `json:"goarch"`
	GoVersion  string `json:"go_version"`
	GOMAXPROCS int    `json:"gomaxprocs"`
	CPUCount   int    `json:"cpu_count"`
	Hostname   string `json:"hostname,omitempty"`
}

// IntegrityReport compares source metadata before and after all scenarios.
// The digest covers relative names, types, modes, sizes, modification times
// and symlink targets, not source content. Git revision/hash verification for
// the curated corpus is performed by the explicit read-only execution record.
type IntegrityReport struct {
	Method    string `json:"method"`
	Before    string `json:"before,omitempty"`
	After     string `json:"after,omitempty"`
	Unchanged bool   `json:"unchanged"`
	Error     string `json:"error,omitempty"`
}

// ScenarioReport contains one phase result and its measurements.
type ScenarioReport struct {
	Name                   Scenario `json:"name"`
	ResultPath             string   `json:"result_path"`
	RunID                  string   `json:"run_id"`
	Partial                bool     `json:"partial"`
	FactualDigest          string   `json:"factual_digest,omitempty"`
	EquivalentFactsToFirst *bool    `json:"equivalent_facts_to_first,omitempty"`
	Error                  string   `json:"error,omitempty"`
	Metrics                Metrics  `json:"metrics"`
}

// Metrics are measurements for a scenario. Durations are integer nanoseconds
// so JSON consumers do not depend on Go's duration string formatting.
// PersistedVolumeBytes is the logical size of result and state files after
// writing; it is not a syscall-level count of bytes sent to storage.
type Metrics struct {
	Durations            StageDurations `json:"durations"`
	BytesRead            int64          `json:"bytes_read"`
	PersistedVolumeBytes int64          `json:"persisted_volume_bytes"`
	OutputBytes          int64          `json:"output_bytes"`
	EffectiveConcurrency int            `json:"effective_concurrency"`
	ArtifactsDiscovered  int            `json:"artifacts_discovered"`
	ArtifactsReused      int            `json:"artifacts_reused"`
	ArtifactsReprocessed int            `json:"artifacts_reprocessed"`
	Failures             int            `json:"failures"`
	Limited              int            `json:"limited"`
	Heap                 HeapMetrics    `json:"heap_go"`
	Unavailable          []string       `json:"unavailable,omitempty"`
	Limitations          []string       `json:"limitations,omitempty"`
}

// StageDurations separates the measured portions of one scenario. Analysis
// excludes discovery and result serialization; state_write is the incremental
// cache write performed inside the runner.
type StageDurations struct {
	DiscoveryNanos  int64 `json:"discovery_nanos"`
	AnalysisNanos   int64 `json:"analysis_nanos"`
	StateWriteNanos int64 `json:"state_write_nanos,omitempty"`
	WritingNanos    int64 `json:"writing_nanos"`
	TotalNanos      int64 `json:"total_nanos"`
}

// HeapMetrics reports Go heap samples and the Linux process high-water mark
// when /proc exposes it. Heap values are end-of-scenario samples, not a
// continuous peak profile. MaxRSS is the cumulative high-water mark of the
// benchmark process, sampled at the end of this scenario; it is not an
// isolated per-stage or child-process peak.
type HeapMetrics struct {
	HeapAllocBytes uint64 `json:"heap_alloc_bytes"`
	HeapInuseBytes uint64 `json:"heap_inuse_bytes"`
	MaxRSSBytes    uint64 `json:"max_rss_bytes,omitempty"`
	MaxRSSMethod   string `json:"max_rss_method"`
}
