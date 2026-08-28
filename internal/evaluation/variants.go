package evaluation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
)

const (
	// VariantExecutionVersion identifies the versioned orchestration contract.
	// It is separate from both the case-set version and the legacy report.
	VariantExecutionVersion = "v1alpha1"
)

var (
	// ErrInvalidVariantRegistry identifies a malformed or incomplete executor
	// registry. Built-in variants are checked before a runner is returned.
	ErrInvalidVariantRegistry = errors.New("evaluation: invalid variant executor registry")
	// ErrDuplicateVariantExecutor identifies a repeated exact executor entry.
	ErrDuplicateVariantExecutor = errors.New("evaluation: duplicate variant executor")
	// ErrInvalidVariantRequest identifies a request that does not preserve the
	// case's resolved task, scope, policy, tools, and configuration.
	ErrInvalidVariantRequest = errors.New("evaluation: invalid variant request")
	// ErrInvalidVariantResult identifies content, digest, or metadata that
	// cannot cross the content-free result boundary.
	ErrInvalidVariantResult = errors.New("evaluation: invalid variant result")
	// ErrVariantConfiguration identifies a missing built-in executor or an
	// incompatible registration. It is never used for optional external gaps.
	ErrVariantConfiguration = errors.New("evaluation: invalid variant configuration")
)

// VariantExecutionStatus is the controlled state returned by one variant
// executor. It contains no provider or source payload.
type VariantExecutionStatus string

const (
	VariantStatusCompleted   VariantExecutionStatus = "completed"
	VariantStatusLimited     VariantExecutionStatus = "limited"
	VariantStatusFailed      VariantExecutionStatus = "failed"
	VariantStatusUnavailable VariantExecutionStatus = "unavailable"
	VariantStatusCancelled   VariantExecutionStatus = "cancelled"
)

// VariantConclusion is a closed, content-free conclusion state. A textual
// answer is intentionally not representable in this contract.
type VariantConclusion string

const (
	VariantConclusionPassed       VariantConclusion = "passed"
	VariantConclusionPartial      VariantConclusion = "partial"
	VariantConclusionFailed       VariantConclusion = "failed"
	VariantConclusionAbstained    VariantConclusion = "abstained"
	VariantConclusionNotEvaluated VariantConclusion = "not-evaluated"
)

// VariantExecutionResult is the safe result boundary for one executor. It
// records only state, a SHA-256 digest, stable evidence/citation identities,
// and bounded limitations; response and source content have no field here.
type VariantExecutionResult struct {
	Version      string                 `json:"version"`
	Status       VariantExecutionStatus `json:"status"`
	Conclusion   VariantConclusion      `json:"conclusion"`
	OutputDigest string                 `json:"output_digest,omitempty"`
	EvidenceIDs  []string               `json:"evidence_ids,omitempty"`
	ClaimIDs     []string               `json:"claim_ids,omitempty"`
	GapIDs       []string               `json:"gap_ids,omitempty"`
	Citations    []VariantCitation      `json:"citations,omitempty"`
	Limitations  []string               `json:"limitations,omitempty"`
	Metrics      *VariantMetrics        `json:"metrics,omitempty"`
}

// VariantCitation identifies one claim/evidence support edge without
// carrying either response or source content across the executor boundary.
type VariantCitation struct {
	ID         string `json:"id"`
	ClaimID    string `json:"claim_id"`
	EvidenceID string `json:"evidence_id"`
}

// Clone returns a detached result, including optional metric observations.
func (r VariantExecutionResult) Clone() VariantExecutionResult {
	clone := r
	clone.EvidenceIDs = cloneCaseStrings(r.EvidenceIDs)
	clone.ClaimIDs = cloneCaseStrings(r.ClaimIDs)
	clone.GapIDs = cloneCaseStrings(r.GapIDs)
	clone.Citations = append([]VariantCitation(nil), r.Citations...)
	clone.Limitations = cloneCaseStrings(r.Limitations)
	if r.Metrics != nil {
		metrics := r.Metrics.Clone()
		clone.Metrics = &metrics
	}
	return clone
}

// Validate checks a result without exposing an executor error or payload.
func (r VariantExecutionResult) Validate() error {
	_, err := r.Normalize()
	return err
}

// Normalize validates and returns a detached deterministic result. An omitted
// version is upgraded to the current orchestration version for executor
// adapters that do not need to repeat a protocol constant.
func (r VariantExecutionResult) Normalize() (VariantExecutionResult, error) {
	result := r
	if result.Version == "" {
		result.Version = VariantExecutionVersion
	}
	if result.Version != VariantExecutionVersion {
		return VariantExecutionResult{}, ErrInvalidVariantResult
	}
	if !validVariantStatus(result.Status) {
		return VariantExecutionResult{}, ErrInvalidVariantResult
	}
	if result.Conclusion == "" {
		switch result.Status {
		case VariantStatusCompleted:
			result.Conclusion = VariantConclusionPassed
		case VariantStatusLimited:
			result.Conclusion = VariantConclusionPartial
		default:
			result.Conclusion = VariantConclusionNotEvaluated
		}
	}
	if !validVariantConclusion(result.Conclusion) {
		return VariantExecutionResult{}, ErrInvalidVariantResult
	}
	if result.Status == VariantStatusCompleted || result.Status == VariantStatusLimited {
		if !isVariantSHA256(result.OutputDigest) {
			return VariantExecutionResult{}, ErrInvalidVariantResult
		}
	}
	if result.OutputDigest != "" {
		if !isVariantSHA256(result.OutputDigest) {
			return VariantExecutionResult{}, ErrInvalidVariantResult
		}
		result.OutputDigest = strings.ToLower(result.OutputDigest)
	}
	var err error
	result.EvidenceIDs, err = normalizeVariantIDs(result.EvidenceIDs)
	if err != nil {
		return VariantExecutionResult{}, err
	}
	result.ClaimIDs, err = normalizeVariantIDs(result.ClaimIDs)
	if err != nil {
		return VariantExecutionResult{}, err
	}
	result.GapIDs, err = normalizeVariantIDs(result.GapIDs)
	if err != nil {
		return VariantExecutionResult{}, err
	}
	result.Citations, err = normalizeVariantCitations(result.Citations)
	if err != nil {
		return VariantExecutionResult{}, err
	}
	claimIDs := make(map[string]struct{}, len(result.ClaimIDs))
	for _, id := range result.ClaimIDs {
		claimIDs[id] = struct{}{}
	}
	evidenceIDs := make(map[string]struct{}, len(result.EvidenceIDs))
	for _, id := range result.EvidenceIDs {
		evidenceIDs[id] = struct{}{}
	}
	for _, citation := range result.Citations {
		if _, ok := claimIDs[citation.ClaimID]; !ok {
			return VariantExecutionResult{}, ErrInvalidVariantResult
		}
		if _, ok := evidenceIDs[citation.EvidenceID]; !ok {
			return VariantExecutionResult{}, ErrInvalidVariantResult
		}
	}
	result.Limitations, err = normalizeVariantLimitations(result.Limitations)
	if err != nil {
		return VariantExecutionResult{}, err
	}
	if result.Metrics != nil {
		normalizedMetrics, metricsErr := result.Metrics.Normalize()
		if metricsErr != nil {
			return VariantExecutionResult{}, metricsErr
		}
		result.Metrics = &normalizedMetrics
	}
	return result, nil
}

// VariantExecutionRequest is the only input an executor receives. It carries
// the defensive case projection, task, revisions, policy, selected variant,
// resolved tools, and resolved configuration. It has no source or response
// content field.
type VariantExecutionRequest struct {
	Case           EvaluationCase          `json:"case"`
	Task           EvaluationTask          `json:"task"`
	CorpusID       string                  `json:"corpus_id"`
	CorpusRevision string                  `json:"corpus_revision"`
	SourceID       string                  `json:"source_id"`
	SourceRevision string                  `json:"source_revision"`
	Policy         EvaluationPolicy        `json:"policy"`
	Variant        EvaluationVariant       `json:"variant"`
	Tools          []EvaluationTool        `json:"tools"`
	Configuration  EvaluationConfiguration `json:"configuration"`
}

// Validate ensures every duplicated field in a request agrees with the case
// and that tools/configuration are the exact resolved references.
func (r VariantExecutionRequest) Validate() error {
	if err := r.Case.Validate(); err != nil {
		return ErrInvalidVariantRequest
	}
	if err := r.Task.validate(true); err != nil || r.Task != r.Case.Task {
		return ErrInvalidVariantRequest
	}
	if r.CorpusID != r.Case.CorpusID || r.CorpusRevision != r.Case.CorpusRevision ||
		r.SourceID != r.Case.SourceID || r.SourceRevision != r.Case.SourceRevision {
		return ErrInvalidVariantRequest
	}
	if err := r.Policy.validate(true); err != nil || !sameEvaluationPolicy(r.Policy, r.Case.Policy) {
		return ErrInvalidVariantRequest
	}
	if err := validateSingleVariant(r.Case, r.Variant); err != nil {
		return ErrInvalidVariantRequest
	}
	if err := validateIdentifier("variant request corpus", r.CorpusID, maxCaseIDBytes); err != nil {
		return ErrInvalidVariantRequest
	}
	if err := validateIdentifier("variant request corpus revision", r.CorpusRevision, maxCaseIDBytes); err != nil {
		return ErrInvalidVariantRequest
	}
	if err := validateIdentifier("variant request source", r.SourceID, maxCaseIDBytes); err != nil {
		return ErrInvalidVariantRequest
	}
	if err := validateIdentifier("variant request source revision", r.SourceRevision, maxCaseIDBytes); err != nil {
		return ErrInvalidVariantRequest
	}
	knownTools, err := validateTools(r.Tools, true)
	if err != nil || len(knownTools) != len(r.Variant.ToolIDs) {
		return ErrInvalidVariantRequest
	}
	for _, id := range r.Variant.ToolIDs {
		if _, ok := knownTools[id]; !ok {
			return ErrInvalidVariantRequest
		}
	}
	for _, item := range r.Case.Tools {
		selected := false
		for _, id := range r.Variant.ToolIDs {
			if item.ID == id {
				selected = true
				break
			}
		}
		if selected {
			found := false
			for _, resolved := range r.Tools {
				if sameEvaluationTool(item, resolved) {
					found = true
					break
				}
			}
			if !found {
				return ErrInvalidVariantRequest
			}
		}
	}
	configuration, ok := findEvaluationConfiguration(r.Case.Configurations, r.Variant.ConfigurationID)
	if !ok || !sameEvaluationConfiguration(configuration, r.Configuration) {
		return ErrInvalidVariantRequest
	}
	if _, err := configurationDigest(r.Configuration); err != nil {
		return ErrInvalidVariantRequest
	}
	return nil
}

// Clone returns a fully detached request. Executors may retain or mutate the
// request without affecting another variant or the case set.
func (r VariantExecutionRequest) Clone() VariantExecutionRequest {
	clone := r
	clone.Case = cloneCase(r.Case)
	clone.Policy.Permissions = cloneCaseStrings(r.Policy.Permissions)
	clone.Variant.ToolIDs = cloneCaseStrings(r.Variant.ToolIDs)
	clone.Variant.Capabilities = cloneCaseStrings(r.Variant.Capabilities)
	clone.Variant.Limitations = cloneCaseStrings(r.Variant.Limitations)
	clone.Tools = make([]EvaluationTool, len(r.Tools))
	for index, item := range r.Tools {
		clone.Tools[index] = item
		clone.Tools[index].Capabilities = cloneCaseStrings(item.Capabilities)
		clone.Tools[index].Limitations = cloneCaseStrings(item.Limitations)
	}
	clone.Configuration.Settings = cloneCaseStringMap(r.Configuration.Settings)
	return clone
}

// VariantExecutor is the version-independent port used by the orchestration
// layer. Implementations must return a content-free result and must honor ctx.
type VariantExecutor interface {
	Execute(context.Context, VariantExecutionRequest) (VariantExecutionResult, error)
}

// VariantExecutorFunc adapts a function to VariantExecutor.
type VariantExecutorFunc func(context.Context, VariantExecutionRequest) (VariantExecutionResult, error)

// Execute implements VariantExecutor.
func (f VariantExecutorFunc) Execute(ctx context.Context, request VariantExecutionRequest) (VariantExecutionResult, error) {
	if f == nil {
		return VariantExecutionResult{}, ErrInvalidVariantResult
	}
	return f(ctx, request)
}

var _ VariantExecutor = VariantExecutorFunc(nil)

// VariantExecutorRegistration binds one executor to a built-in kind or an
// exact variant ID. External registrations always require VariantID; they are
// never selected by kind fallback.
type VariantExecutorRegistration struct {
	// VariantID is optional only for a built-in default registration.
	VariantID string          `json:"variant_id,omitempty"`
	Kind      VariantKind     `json:"kind"`
	Executor  VariantExecutor `json:"-"`
}

// VariantExecutorRegistry stores exact and built-in-default executor entries.
// It has no package-global state, and registrations are returned in stable
// order for inspection.
type VariantExecutorRegistry struct {
	mu       sync.RWMutex
	byID     map[string]registeredVariantExecutor
	byKind   map[VariantKind]registeredVariantExecutor
	allKinds map[VariantKind]struct{}
}

type registeredVariantExecutor struct {
	registration VariantExecutorRegistration
}

// NewVariantExecutorRegistry validates registrations and requires one
// executor for each built-in kind. Optional external executors may be omitted.
func NewVariantExecutorRegistry(registrations ...VariantExecutorRegistration) (*VariantExecutorRegistry, error) {
	registry := &VariantExecutorRegistry{
		byID: make(map[string]registeredVariantExecutor), byKind: make(map[VariantKind]registeredVariantExecutor), allKinds: make(map[VariantKind]struct{}),
	}
	for _, registration := range registrations {
		if err := registry.Register(registration); err != nil {
			return nil, err
		}
	}
	if err := registry.validateRequiredKinds(); err != nil {
		return nil, err
	}
	return registry, nil
}

// Register adds an executor without replacing an existing exact entry or
// built-in default. A caller changing a registry should construct a new
// runner after registration.
func (r *VariantExecutorRegistry) Register(registration VariantExecutorRegistration) error {
	if r == nil || r.byID == nil || r.byKind == nil || r.allKinds == nil {
		return ErrInvalidVariantRegistry
	}
	normalized, variantID, err := normalizeVariantRegistration(registration)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if variantID != "" {
		if _, exists := r.byID[variantID]; exists {
			return ErrDuplicateVariantExecutor
		}
	}
	if variantID == "" {
		if _, exists := r.byKind[normalized.Kind]; exists {
			return ErrDuplicateVariantExecutor
		}
		r.byKind[normalized.Kind] = registeredVariantExecutor{registration: normalized}
	} else {
		r.byID[variantID] = registeredVariantExecutor{registration: normalized}
	}
	r.allKinds[normalized.Kind] = struct{}{}
	return nil
}

// Validate verifies that all required built-in kinds remain registered.
func (r *VariantExecutorRegistry) Validate() error {
	if r == nil {
		return ErrInvalidVariantRegistry
	}
	return r.validateRequiredKinds()
}

func (r *VariantExecutorRegistry) validateRequiredKinds() error {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, kind := range requiredVariantKinds() {
		if _, ok := r.allKinds[kind]; !ok {
			return fmt.Errorf("%w: missing %s executor", ErrInvalidVariantRegistry, kind)
		}
	}
	return nil
}

// Registrations returns detached registration metadata in deterministic order.
func (r *VariantExecutorRegistry) Registrations() []VariantExecutorRegistration {
	if r == nil {
		return []VariantExecutorRegistration{}
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]VariantExecutorRegistration, 0, len(r.byID)+len(r.byKind))
	for _, item := range r.byID {
		result = append(result, item.registration)
	}
	for _, item := range r.byKind {
		result = append(result, item.registration)
	}
	sort.SliceStable(result, func(left, right int) bool {
		leftID := result[left].VariantID
		rightID := result[right].VariantID
		if leftID != rightID {
			return leftID < rightID
		}
		return result[left].Kind < result[right].Kind
	})
	return result
}

func (r *VariantExecutorRegistry) executorFor(variant EvaluationVariant) (VariantExecutor, error) {
	if r == nil {
		return nil, ErrInvalidVariantRegistry
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if registered, exists := r.byID[variant.ID]; exists {
		if registered.registration.Kind != variant.Kind {
			return nil, ErrVariantConfiguration
		}
		return registered.registration.Executor, nil
	}
	if variant.Kind == VariantExternalContext {
		return nil, nil
	}
	registered, exists := r.byKind[variant.Kind]
	if !exists {
		return nil, ErrVariantConfiguration
	}
	return registered.registration.Executor, nil
}

// VariantDifference records only metadata differences from direct-source.
// Configuration values are deliberately absent; changed keys reveal no value.
type VariantDifference struct {
	BaselineVariantID            string   `json:"baseline_variant_id"`
	BaselineConfigurationID      string   `json:"baseline_configuration_id"`
	BaselineConfigurationVersion string   `json:"baseline_configuration_version"`
	ConfigurationID              string   `json:"configuration_id"`
	ConfigurationVersion         string   `json:"configuration_version"`
	ConfigurationIDChanged       bool     `json:"configuration_id_changed"`
	ConfigurationVersionChanged  bool     `json:"configuration_version_changed"`
	ToolIDsAdded                 []string `json:"tool_ids_added,omitempty"`
	ToolIDsRemoved               []string `json:"tool_ids_removed,omitempty"`
	ConfigurationKeysAdded       []string `json:"configuration_keys_added,omitempty"`
	ConfigurationKeysRemoved     []string `json:"configuration_keys_removed,omitempty"`
	ConfigurationKeysChanged     []string `json:"configuration_keys_changed,omitempty"`
}

// VariantExecutionRecord is one isolated execution and its content-free
// result. No output or executor error message is representable.
type VariantExecutionRecord struct {
	Version              string                 `json:"version"`
	CaseID               string                 `json:"case_id"`
	CaseVersion          int                    `json:"case_version"`
	CorpusID             string                 `json:"corpus_id"`
	CorpusRevision       string                 `json:"corpus_revision"`
	SourceID             string                 `json:"source_id"`
	SourceRevision       string                 `json:"source_revision"`
	VariantID            string                 `json:"variant_id"`
	VariantKind          VariantKind            `json:"variant_kind"`
	ToolIDs              []string               `json:"tool_ids"`
	ConfigurationID      string                 `json:"configuration_id"`
	ConfigurationVersion string                 `json:"configuration_version"`
	ConfigurationDigest  string                 `json:"configuration_digest"`
	Outcome              VariantOutcome         `json:"outcome"`
	ErrorCode            string                 `json:"error_code,omitempty"`
	Result               VariantExecutionResult `json:"result"`
	Quality              *VariantQuality        `json:"quality"`
	Differences          *VariantDifference     `json:"differences,omitempty"`
}

// VariantOutcome is the report-level controlled outcome.
type VariantOutcome string

const (
	VariantOutcomeCompleted   VariantOutcome = "completed"
	VariantOutcomeLimited     VariantOutcome = "limited"
	VariantOutcomeFailed      VariantOutcome = "failed"
	VariantOutcomeUnavailable VariantOutcome = "unavailable"
	VariantOutcomeCancelled   VariantOutcome = "cancelled"
)

// VariantCaseReport keeps executions for one case separate from all other
// cases and variants.
type VariantCaseReport struct {
	CaseID         string                   `json:"case_id"`
	CaseVersion    int                      `json:"case_version"`
	CorpusID       string                   `json:"corpus_id"`
	CorpusRevision string                   `json:"corpus_revision"`
	SourceID       string                   `json:"source_id"`
	SourceRevision string                   `json:"source_revision"`
	Executions     []VariantExecutionRecord `json:"executions"`
	Limitations    []string                 `json:"limitations,omitempty"`
}

// VariantExecutionReport is the versioned result of one case-set
// orchestration. Cases and variant results are never merged.
type VariantExecutionReport struct {
	Version      string                  `json:"version"`
	CasesVersion string                  `json:"cases_version"`
	Cases        []VariantCaseReport     `json:"cases"`
	Efficiency   VariantEfficiencyReport `json:"efficiency"`
}

// VariantRunner orchestrates v1alpha2 cases through a validated executor
// registry. It intentionally does not call the legacy Run/RunLive runner.
type VariantRunner struct {
	registry *VariantExecutorRegistry
}

// NewVariantRunner creates an orchestration runner over a complete registry.
func NewVariantRunner(registry *VariantExecutorRegistry) (*VariantRunner, error) {
	if registry == nil {
		return nil, ErrInvalidVariantRegistry
	}
	if err := registry.Validate(); err != nil {
		return nil, err
	}
	return &VariantRunner{registry: registry}, nil
}

// Registry returns the executor registry. Callers should treat it as immutable
// while a run is in progress.
func (r *VariantRunner) Registry() *VariantExecutorRegistry {
	if r == nil {
		return nil
	}
	return r.registry
}

// Run normalizes a complete v1alpha2 case set, validates all variant plans,
// then executes them in deterministic case/variant order.
func (r *VariantRunner) Run(ctx context.Context, cases CaseSet) (VariantExecutionReport, error) {
	if err := validateVariantContext(ctx); err != nil {
		return VariantExecutionReport{}, err
	}
	if r == nil || r.registry == nil {
		return VariantExecutionReport{}, ErrInvalidVariantRegistry
	}
	if err := r.registry.Validate(); err != nil {
		return VariantExecutionReport{}, err
	}
	normalized, err := cases.Normalize()
	if err != nil {
		return VariantExecutionReport{}, err
	}
	if normalized.Version != Version {
		return VariantExecutionReport{}, fmt.Errorf("%w: variants require %s cases", ErrUnsupportedVersion, Version)
	}
	report := VariantExecutionReport{Version: VariantExecutionVersion, CasesVersion: normalized.Version, Cases: make([]VariantCaseReport, 0, len(normalized.Cases))}
	for _, item := range normalized.Cases {
		if err := validateVariantContext(ctx); err != nil {
			return VariantExecutionReport{}, err
		}
		caseReport, err := r.RunCase(ctx, item)
		if err != nil {
			return VariantExecutionReport{}, err
		}
		report.Cases = append(report.Cases, caseReport)
	}
	efficiency, err := deriveVariantEfficiency(report.Cases)
	if err != nil {
		return VariantExecutionReport{}, err
	}
	report.Efficiency = efficiency
	if err := report.Validate(); err != nil {
		return VariantExecutionReport{}, err
	}
	return report, nil
}

// RunCase normalizes one current case through the same case-set boundary used
// by Run, preventing an unsorted or aliased case from changing execution.
func (r *VariantRunner) RunCase(ctx context.Context, item EvaluationCase) (VariantCaseReport, error) {
	if err := validateVariantContext(ctx); err != nil {
		return VariantCaseReport{}, err
	}
	normalized, err := (CaseSet{Version: Version, Cases: []EvaluationCase{item}}).Normalize()
	if err != nil {
		return VariantCaseReport{}, err
	}
	if len(normalized.Cases) != 1 {
		return VariantCaseReport{}, ErrVariantConfiguration
	}
	return r.runNormalizedCase(ctx, normalized.Cases[0])
}

func (r *VariantRunner) runNormalizedCase(ctx context.Context, item EvaluationCase) (VariantCaseReport, error) {
	if r == nil || r.registry == nil {
		return VariantCaseReport{}, ErrInvalidVariantRegistry
	}
	if err := r.registry.Validate(); err != nil {
		return VariantCaseReport{}, err
	}
	plans, baseline, err := prepareVariantPlans(item)
	if err != nil {
		return VariantCaseReport{}, err
	}
	for index := range plans {
		executor, resolveErr := r.registry.executorFor(plans[index].variant)
		if resolveErr != nil {
			return VariantCaseReport{}, resolveErr
		}
		plans[index].executor = executor
	}
	caseReport := VariantCaseReport{
		CaseID: item.CaseID, CaseVersion: item.CaseVersion, CorpusID: item.CorpusID,
		CorpusRevision: item.CorpusRevision, SourceID: item.SourceID, SourceRevision: item.SourceRevision,
		Executions: make([]VariantExecutionRecord, 0, len(plans)),
	}
	for index := range plans {
		if err := validateVariantContext(ctx); err != nil {
			return VariantCaseReport{}, err
		}
		if plans[index].executor == nil && plans[index].variant.Kind != VariantExternalContext {
			return VariantCaseReport{}, fmt.Errorf("%w: %s", ErrVariantConfiguration, plans[index].variant.Kind)
		}
	}
	for index := range plans {
		if err := validateVariantContext(ctx); err != nil {
			return VariantCaseReport{}, err
		}
		record := newVariantExecutionRecord(item, plans[index], baseline)
		if plans[index].executor == nil {
			record.Outcome = VariantOutcomeUnavailable
			record.ErrorCode = "executor_unavailable"
			record.Result = unavailableVariantResult()
			if err := attachVariantQuality(item, &record); err != nil {
				return VariantCaseReport{}, err
			}
			caseReport.Executions = append(caseReport.Executions, record)
			continue
		}
		result, executionErr := plans[index].executor.Execute(ctx, plans[index].request.Clone())
		if err := ctx.Err(); err != nil {
			return VariantCaseReport{}, err
		}
		if executionErr != nil {
			if errors.Is(executionErr, context.Canceled) || errors.Is(executionErr, context.DeadlineExceeded) {
				return VariantCaseReport{}, executionErr
			}
			record.Outcome = VariantOutcomeFailed
			record.ErrorCode = "executor_failed"
			record.Result = failedVariantResult("executor_failed")
			if err := attachVariantQuality(item, &record); err != nil {
				return VariantCaseReport{}, err
			}
			caseReport.Executions = append(caseReport.Executions, record)
			continue
		}
		normalizedResult, resultErr := result.Normalize()
		if resultErr != nil {
			record.Outcome = VariantOutcomeFailed
			record.ErrorCode = "invalid_executor_result"
			record.Result = failedVariantResult("invalid_executor_result")
			if err := attachVariantQuality(item, &record); err != nil {
				return VariantCaseReport{}, err
			}
			caseReport.Executions = append(caseReport.Executions, record)
			continue
		}
		record.Result = normalizedResult
		record.Outcome = outcomeForStatus(normalizedResult.Status)
		if normalizedResult.Status == VariantStatusFailed {
			record.ErrorCode = "executor_reported_failure"
		} else if normalizedResult.Status == VariantStatusUnavailable {
			record.ErrorCode = "executor_unavailable"
		} else if normalizedResult.Status == VariantStatusCancelled {
			record.ErrorCode = "cancelled"
		}
		if err := attachVariantQuality(item, &record); err != nil {
			return VariantCaseReport{}, err
		}
		caseReport.Executions = append(caseReport.Executions, record)
	}
	if err := caseReport.Validate(); err != nil {
		return VariantCaseReport{}, err
	}
	return caseReport, nil
}

type variantPlan struct {
	variant             EvaluationVariant
	request             VariantExecutionRequest
	configuration       EvaluationConfiguration
	configurationDigest string
	executor            VariantExecutor
}

func prepareVariantPlans(item EvaluationCase) ([]variantPlan, variantPlan, error) {
	if err := item.Validate(); err != nil {
		return nil, variantPlan{}, err
	}
	if item.CaseID == "" || item.Task.isZero() {
		return nil, variantPlan{}, ErrVariantConfiguration
	}
	counts := make(map[VariantKind]int)
	for _, variant := range item.Variants {
		counts[variant.Kind]++
	}
	for _, kind := range requiredVariantKinds() {
		if counts[kind] != 1 {
			return nil, variantPlan{}, fmt.Errorf("%w: case requires one %s variant", ErrVariantConfiguration, kind)
		}
	}
	plans := make([]variantPlan, 0, len(item.Variants))
	var baseline variantPlan
	for _, variant := range item.Variants {
		request, err := buildVariantRequest(item, variant)
		if err != nil {
			return nil, variantPlan{}, err
		}
		digest, err := configurationDigest(request.Configuration)
		if err != nil {
			return nil, variantPlan{}, ErrVariantConfiguration
		}
		plan := variantPlan{variant: request.Variant, request: request, configuration: request.Configuration, configurationDigest: digest}
		if variant.Kind == VariantDirectSource {
			baseline = plan
		}
		plans = append(plans, plan)
	}
	return plans, baseline, nil
}

func buildVariantRequest(item EvaluationCase, variant EvaluationVariant) (VariantExecutionRequest, error) {
	if err := validateSingleVariant(item, variant); err != nil {
		return VariantExecutionRequest{}, err
	}
	configuration, ok := findEvaluationConfiguration(item.Configurations, variant.ConfigurationID)
	if !ok {
		return VariantExecutionRequest{}, ErrVariantConfiguration
	}
	toolByID := make(map[string]EvaluationTool, len(item.Tools))
	for _, tool := range item.Tools {
		toolByID[tool.ID] = tool
	}
	tools := make([]EvaluationTool, 0, len(variant.ToolIDs))
	for _, id := range variant.ToolIDs {
		tool, ok := toolByID[id]
		if !ok {
			return VariantExecutionRequest{}, ErrVariantConfiguration
		}
		tools = append(tools, tool)
	}
	request := VariantExecutionRequest{
		Case: cloneCase(item), Task: item.Task,
		CorpusID: item.CorpusID, CorpusRevision: item.CorpusRevision,
		SourceID: item.SourceID, SourceRevision: item.SourceRevision,
		Policy: cloneEvaluationPolicy(item.Policy), Variant: cloneEvaluationVariant(variant),
		Tools: tools, Configuration: cloneEvaluationConfiguration(configuration),
	}
	if err := request.Validate(); err != nil {
		return VariantExecutionRequest{}, err
	}
	return request.Clone(), nil
}

func newVariantExecutionRecord(item EvaluationCase, plan variantPlan, baseline variantPlan) VariantExecutionRecord {
	record := VariantExecutionRecord{
		Version: VariantExecutionVersion, CaseID: item.CaseID, CaseVersion: item.CaseVersion,
		CorpusID: item.CorpusID, CorpusRevision: item.CorpusRevision, SourceID: item.SourceID, SourceRevision: item.SourceRevision,
		VariantID: plan.variant.ID, VariantKind: plan.variant.Kind, ToolIDs: cloneCaseStrings(plan.variant.ToolIDs),
		ConfigurationID: plan.configuration.ID, ConfigurationVersion: plan.configuration.Version, ConfigurationDigest: plan.configurationDigest,
		Result: VariantExecutionResult{Version: VariantExecutionVersion},
	}
	if plan.variant.Kind != VariantDirectSource {
		difference := compareVariantToBaseline(plan, baseline)
		record.Differences = &difference
	}
	return record
}

func compareVariantToBaseline(plan, baseline variantPlan) VariantDifference {
	return VariantDifference{
		BaselineVariantID: baseline.variant.ID, BaselineConfigurationID: baseline.configuration.ID,
		BaselineConfigurationVersion: baseline.configuration.Version, ConfigurationID: plan.configuration.ID,
		ConfigurationVersion: plan.configuration.Version, ConfigurationIDChanged: plan.configuration.ID != baseline.configuration.ID,
		ConfigurationVersionChanged: plan.configuration.Version != baseline.configuration.Version,
		ToolIDsAdded:                differenceStrings(plan.variant.ToolIDs, baseline.variant.ToolIDs),
		ToolIDsRemoved:              differenceStrings(baseline.variant.ToolIDs, plan.variant.ToolIDs),
		ConfigurationKeysAdded:      differenceStrings(configurationKeys(plan.configuration), configurationKeys(baseline.configuration)),
		ConfigurationKeysRemoved:    differenceStrings(configurationKeys(baseline.configuration), configurationKeys(plan.configuration)),
		ConfigurationKeysChanged:    changedConfigurationKeys(plan.configuration, baseline.configuration),
	}
}

func (r VariantExecutionRecord) Validate() error {
	if r.Version != VariantExecutionVersion || !validEvaluationIdentity(r.CaseID) || r.CaseVersion < 1 ||
		!validEvaluationIdentity(r.CorpusID) || !validEvaluationIdentity(r.CorpusRevision) ||
		!validEvaluationIdentity(r.SourceID) || !validEvaluationIdentity(r.SourceRevision) ||
		!validEvaluationIdentity(r.VariantID) || !validVariantKind(r.VariantKind) ||
		!validEvaluationIdentity(r.ConfigurationID) || !validEvaluationIdentity(r.ConfigurationVersion) ||
		!isVariantSHA256(r.ConfigurationDigest) || !validVariantOutcome(r.Outcome) {
		return ErrInvalidVariantResult
	}
	if err := validateStringList(r.ToolIDs, "variant record tool", maxCaseIDBytes); err != nil {
		return ErrInvalidVariantResult
	}
	if r.ErrorCode != "" && !validVariantErrorCode(r.ErrorCode) {
		return ErrInvalidVariantResult
	}
	if err := r.Result.Validate(); err != nil {
		return ErrInvalidVariantResult
	}
	if r.Quality == nil || r.Quality.Validate() != nil {
		return ErrInvalidVariantResult
	}
	if r.Quality.Evidence.Retrieved != len(r.Result.EvidenceIDs) ||
		r.Quality.Citations.Total != len(r.Result.Citations) ||
		r.Quality.Gaps.Recognized > len(r.Result.GapIDs) {
		return ErrInvalidVariantResult
	}
	abstentionActual := r.Result.Conclusion == VariantConclusionAbstained
	if r.Quality.Abstention.Actual != abstentionActual {
		return ErrInvalidVariantResult
	}
	evaluableCompletion := r.Result.Status == VariantStatusCompleted &&
		(r.Result.Conclusion == VariantConclusionPassed || abstentionActual)
	expectedCompleted := r.Result.Status == VariantStatusCompleted &&
		(r.Result.Conclusion == VariantConclusionPassed ||
			(r.Result.Conclusion == VariantConclusionAbstained && r.Quality.Abstention.Expected))
	expectedAppropriate := evaluableCompletion && r.Quality.Abstention.Expected == abstentionActual
	if r.Quality.Completed != expectedCompleted || r.Quality.Abstention.Appropriate != expectedAppropriate {
		return ErrInvalidVariantResult
	}
	if VariantExecutionStatus(r.Outcome) != r.Result.Status {
		return ErrInvalidVariantResult
	}
	switch r.Outcome {
	case VariantOutcomeCompleted, VariantOutcomeLimited:
		if r.ErrorCode != "" {
			return ErrInvalidVariantResult
		}
	case VariantOutcomeFailed:
		if r.ErrorCode != "executor_failed" && r.ErrorCode != "invalid_executor_result" && r.ErrorCode != "executor_reported_failure" {
			return ErrInvalidVariantResult
		}
	case VariantOutcomeUnavailable:
		if r.ErrorCode != "executor_unavailable" {
			return ErrInvalidVariantResult
		}
	case VariantOutcomeCancelled:
		if r.ErrorCode != "cancelled" {
			return ErrInvalidVariantResult
		}
	}
	if r.Differences != nil {
		if err := r.Differences.Validate(); err != nil {
			return ErrInvalidVariantResult
		}
	}
	return nil
}

func (d VariantDifference) Validate() error {
	for _, value := range []string{d.BaselineVariantID, d.BaselineConfigurationID, d.BaselineConfigurationVersion, d.ConfigurationID, d.ConfigurationVersion} {
		if !validEvaluationIdentity(value) {
			return ErrInvalidVariantResult
		}
	}
	for _, values := range [][]string{d.ToolIDsAdded, d.ToolIDsRemoved, d.ConfigurationKeysAdded, d.ConfigurationKeysRemoved, d.ConfigurationKeysChanged} {
		if err := validateStringList(values, "variant difference", maxCaseIDBytes); err != nil {
			return ErrInvalidVariantResult
		}
	}
	return nil
}

func (r VariantCaseReport) Validate() error {
	if !validEvaluationIdentity(r.CaseID) || r.CaseVersion < 1 || !validEvaluationIdentity(r.CorpusID) ||
		!validEvaluationIdentity(r.CorpusRevision) || !validEvaluationIdentity(r.SourceID) || !validEvaluationIdentity(r.SourceRevision) || len(r.Executions) == 0 {
		return ErrInvalidVariantResult
	}
	if err := validateVariantLimitations(r.Limitations); err != nil {
		return ErrInvalidVariantResult
	}
	seen := make(map[string]struct{}, len(r.Executions))
	var baseline *VariantExecutionRecord
	for _, execution := range r.Executions {
		if execution.CaseID != r.CaseID || execution.CaseVersion != r.CaseVersion || execution.CorpusID != r.CorpusID ||
			execution.CorpusRevision != r.CorpusRevision || execution.SourceID != r.SourceID || execution.SourceRevision != r.SourceRevision {
			return ErrInvalidVariantResult
		}
		if _, exists := seen[execution.VariantID]; exists {
			return ErrInvalidVariantResult
		}
		seen[execution.VariantID] = struct{}{}
		if err := execution.Validate(); err != nil {
			return err
		}
		if execution.VariantKind == VariantDirectSource {
			if baseline != nil || execution.Differences != nil {
				return ErrInvalidVariantResult
			}
			copy := execution
			baseline = &copy
		}
	}
	if baseline == nil {
		return ErrInvalidVariantResult
	}
	for _, execution := range r.Executions {
		if execution.VariantKind == VariantDirectSource {
			continue
		}
		difference := execution.Differences
		if difference == nil || difference.BaselineVariantID != baseline.VariantID ||
			difference.BaselineConfigurationID != baseline.ConfigurationID ||
			difference.BaselineConfigurationVersion != baseline.ConfigurationVersion ||
			difference.ConfigurationID != execution.ConfigurationID ||
			difference.ConfigurationVersion != execution.ConfigurationVersion {
			return ErrInvalidVariantResult
		}
	}
	return nil
}

func validateVariantLimitations(values []string) error {
	_, err := normalizeVariantLimitations(values)
	return err
}

func (r VariantExecutionReport) Validate() error {
	if r.Version != VariantExecutionVersion || r.CasesVersion != Version || len(r.Cases) == 0 {
		return ErrInvalidVariantResult
	}
	seen := make(map[string]struct{}, len(r.Cases))
	for _, item := range r.Cases {
		key := item.CaseID + "\x00" + fmt.Sprint(item.CaseVersion)
		if _, exists := seen[key]; exists {
			return ErrInvalidVariantResult
		}
		seen[key] = struct{}{}
		if err := item.Validate(); err != nil {
			return err
		}
	}
	normalizedEfficiency, err := r.Efficiency.Normalize()
	if err != nil {
		return ErrInvalidVariantResult
	}
	expectedEfficiency, err := deriveVariantEfficiency(r.Cases)
	if err != nil || !reflect.DeepEqual(normalizedEfficiency, expectedEfficiency) {
		return ErrInvalidVariantResult
	}
	return nil
}

func validateSingleVariant(item EvaluationCase, variant EvaluationVariant) error {
	if !validVariantKind(variant.Kind) || !validEvaluationIdentity(variant.ID) || len(variant.ToolIDs) == 0 ||
		!validEvaluationIdentity(variant.ConfigurationID) {
		return ErrVariantConfiguration
	}
	knownTools, err := validateTools(item.Tools, true)
	if err != nil {
		return ErrVariantConfiguration
	}
	if err := validateReferences(variant.ToolIDs, knownTools, "variant tool"); err != nil {
		return ErrVariantConfiguration
	}
	knownConfigurations, err := validateConfigurations(item.Configurations, true)
	if err != nil {
		return ErrVariantConfiguration
	}
	if _, ok := knownConfigurations[variant.ConfigurationID]; !ok {
		return ErrVariantConfiguration
	}
	for _, candidate := range item.Variants {
		if candidate.ID == variant.ID {
			if candidate.Kind != variant.Kind || candidate.ConfigurationID != variant.ConfigurationID || !sameStringSet(candidate.ToolIDs, variant.ToolIDs) {
				return ErrVariantConfiguration
			}
			return nil
		}
	}
	return ErrVariantConfiguration
}

func normalizeVariantRegistration(input VariantExecutorRegistration) (VariantExecutorRegistration, string, error) {
	if !validVariantKind(input.Kind) || isNilVariantExecutor(input.Executor) {
		return VariantExecutorRegistration{}, "", ErrInvalidVariantRegistry
	}
	variantID := strings.TrimSpace(input.VariantID)
	if variantID != "" {
		if err := validateIdentifier("variant executor id", variantID, maxCaseIDBytes); err != nil {
			return VariantExecutorRegistration{}, "", ErrInvalidVariantRegistry
		}
	} else if input.Kind == VariantExternalContext {
		return VariantExecutorRegistration{}, "", ErrInvalidVariantRegistry
	}
	input.VariantID = variantID
	return input, variantID, nil
}

func requiredVariantKinds() []VariantKind {
	return []VariantKind{VariantDirectSource, VariantTextRetrieval, VariantManuContext}
}

func validVariantKind(kind VariantKind) bool {
	switch kind {
	case VariantDirectSource, VariantTextRetrieval, VariantManuContext, VariantExternalContext:
		return true
	default:
		return false
	}
}

func validVariantStatus(status VariantExecutionStatus) bool {
	switch status {
	case VariantStatusCompleted, VariantStatusLimited, VariantStatusFailed, VariantStatusUnavailable, VariantStatusCancelled:
		return true
	default:
		return false
	}
}

func validVariantConclusion(conclusion VariantConclusion) bool {
	switch conclusion {
	case VariantConclusionPassed, VariantConclusionPartial, VariantConclusionFailed, VariantConclusionAbstained, VariantConclusionNotEvaluated:
		return true
	default:
		return false
	}
}

func validVariantOutcome(outcome VariantOutcome) bool {
	switch outcome {
	case VariantOutcomeCompleted, VariantOutcomeLimited, VariantOutcomeFailed, VariantOutcomeUnavailable, VariantOutcomeCancelled:
		return true
	default:
		return false
	}
}

func validVariantErrorCode(code string) bool {
	switch code {
	case "executor_unavailable", "executor_failed", "invalid_executor_result", "executor_reported_failure", "cancelled":
		return true
	default:
		return false
	}
}

func outcomeForStatus(status VariantExecutionStatus) VariantOutcome {
	return VariantOutcome(status)
}

func unavailableVariantResult() VariantExecutionResult {
	return VariantExecutionResult{Version: VariantExecutionVersion, Status: VariantStatusUnavailable, Conclusion: VariantConclusionNotEvaluated, Limitations: []string{"executor_unavailable"}}
}

func failedVariantResult(code string) VariantExecutionResult {
	return VariantExecutionResult{Version: VariantExecutionVersion, Status: VariantStatusFailed, Conclusion: VariantConclusionNotEvaluated, Limitations: []string{code}}
}

func validateVariantContext(ctx context.Context) error {
	if ctx == nil {
		return ErrInvalidVariantRequest
	}
	return ctx.Err()
}

func isNilVariantExecutor(executor VariantExecutor) bool {
	if executor == nil {
		return true
	}
	value := reflect.ValueOf(executor)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func normalizeVariantIDs(values []string) ([]string, error) {
	if len(values) > maxListItems {
		return nil, ErrInvalidVariantResult
	}
	if values == nil {
		return nil, nil
	}
	result := append([]string(nil), values...)
	for _, value := range result {
		if err := validateIdentifier("variant result identity", value, maxCaseIDBytes); err != nil {
			return nil, ErrInvalidVariantResult
		}
	}
	sort.Strings(result)
	if err := validateUniqueList(result); err != nil {
		return nil, ErrInvalidVariantResult
	}
	return result, nil
}

func normalizeVariantCitations(values []VariantCitation) ([]VariantCitation, error) {
	if len(values) > maxListItems {
		return nil, ErrInvalidVariantResult
	}
	if values == nil {
		return nil, nil
	}
	result := append([]VariantCitation(nil), values...)
	for _, citation := range result {
		if err := validateIdentifier("variant citation", citation.ID, maxCaseIDBytes); err != nil {
			return nil, ErrInvalidVariantResult
		}
		if err := validateIdentifier("variant citation claim", citation.ClaimID, maxCaseIDBytes); err != nil {
			return nil, ErrInvalidVariantResult
		}
		if err := validateIdentifier("variant citation evidence", citation.EvidenceID, maxCaseIDBytes); err != nil {
			return nil, ErrInvalidVariantResult
		}
	}
	sort.SliceStable(result, func(left, right int) bool {
		if result[left].ID != result[right].ID {
			return result[left].ID < result[right].ID
		}
		if result[left].ClaimID != result[right].ClaimID {
			return result[left].ClaimID < result[right].ClaimID
		}
		return result[left].EvidenceID < result[right].EvidenceID
	})
	seen := make(map[string]struct{}, len(result))
	for _, citation := range result {
		if _, exists := seen[citation.ID]; exists {
			return nil, ErrInvalidVariantResult
		}
		seen[citation.ID] = struct{}{}
	}
	return result, nil
}

func normalizeVariantLimitations(values []string) ([]string, error) {
	if len(values) > maxListItems {
		return nil, ErrInvalidVariantResult
	}
	if values == nil {
		return nil, nil
	}
	result := append([]string(nil), values...)
	for _, value := range result {
		if err := validateSafeText("variant limitation", value, maxTextBytes); err != nil {
			return nil, ErrInvalidVariantResult
		}
	}
	sort.Strings(result)
	if err := validateUniqueList(result); err != nil {
		return nil, ErrInvalidVariantResult
	}
	return result, nil
}

func validEvaluationIdentity(value string) bool {
	return validateIdentifier("variant identity", value, maxCaseIDBytes) == nil
}

func isVariantSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func configurationDigest(configuration EvaluationConfiguration) (string, error) {
	if err := configuration.Validate(); err != nil || configuration.isZero() {
		return "", ErrVariantConfiguration
	}
	type setting struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	}
	settings := make([]setting, 0, len(configuration.Settings))
	for key, value := range configuration.Settings {
		settings = append(settings, setting{Key: key, Value: value})
	}
	sort.SliceStable(settings, func(left, right int) bool { return settings[left].Key < settings[right].Key })
	canonical := struct {
		ID       string    `json:"id"`
		Version  string    `json:"version"`
		Settings []setting `json:"settings"`
	}{ID: configuration.ID, Version: configuration.Version, Settings: settings}
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return "", ErrVariantConfiguration
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

// ConfigurationDigest returns the deterministic SHA-256 identity of a
// validated non-secret evaluation configuration.
func ConfigurationDigest(configuration EvaluationConfiguration) (string, error) {
	return configurationDigest(configuration)
}

// Digest returns the deterministic SHA-256 identity of this configuration.
func (c EvaluationConfiguration) Digest() (string, error) {
	return configurationDigest(c)
}

func cloneEvaluationVariant(input EvaluationVariant) EvaluationVariant {
	input.ToolIDs = cloneCaseStrings(input.ToolIDs)
	input.Capabilities = cloneCaseStrings(input.Capabilities)
	input.Limitations = cloneCaseStrings(input.Limitations)
	return input
}

func cloneEvaluationConfiguration(input EvaluationConfiguration) EvaluationConfiguration {
	input.Settings = cloneCaseStringMap(input.Settings)
	return input
}

func cloneEvaluationPolicy(input EvaluationPolicy) EvaluationPolicy {
	input.Permissions = cloneCaseStrings(input.Permissions)
	return input
}

func findEvaluationConfiguration(values []EvaluationConfiguration, id string) (EvaluationConfiguration, bool) {
	for _, value := range values {
		if value.ID == id {
			return cloneEvaluationConfiguration(value), true
		}
	}
	return EvaluationConfiguration{}, false
}

func sameEvaluationConfiguration(left, right EvaluationConfiguration) bool {
	if left.ID != right.ID || left.Version != right.Version || len(left.Settings) != len(right.Settings) {
		return false
	}
	for key, value := range left.Settings {
		if right.Settings[key] != value {
			return false
		}
	}
	return true
}

func sameEvaluationTool(left, right EvaluationTool) bool {
	return left.ID == right.ID && left.Version == right.Version && left.Role == right.Role &&
		sameStringSet(left.Capabilities, right.Capabilities) && sameStringSet(left.Limitations, right.Limitations)
}

func sameEvaluationPolicy(left, right EvaluationPolicy) bool {
	return left.SourceAccess == right.SourceAccess && left.ExternalTransfer == right.ExternalTransfer &&
		left.NetworkAccess == right.NetworkAccess && left.MutationAccess == right.MutationAccess &&
		sameStringSet(left.Permissions, right.Permissions)
}

func sameStringSet(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	return len(differenceStrings(left, right)) == 0 && len(differenceStrings(right, left)) == 0
}

func differenceStrings(left, right []string) []string {
	known := make(map[string]struct{}, len(right))
	for _, value := range right {
		known[value] = struct{}{}
	}
	result := make([]string, 0)
	seen := make(map[string]struct{})
	for _, value := range left {
		if _, ok := known[value]; ok {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func configurationKeys(configuration EvaluationConfiguration) []string {
	keys := make([]string, 0, len(configuration.Settings))
	for key := range configuration.Settings {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func changedConfigurationKeys(left, right EvaluationConfiguration) []string {
	result := make([]string, 0)
	for key, value := range left.Settings {
		if other, ok := right.Settings[key]; ok && value != other {
			result = append(result, key)
		}
	}
	sort.Strings(result)
	return result
}
