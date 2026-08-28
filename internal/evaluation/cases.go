package evaluation

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/pedrogpaulino/manu/internal/contract"
)

const (
	// Version identifies the physical JSON representation of an evaluation
	// case set. It is independent from the Analysis Bundle contract.
	Version = "v1alpha2"

	// LegacyVersion identifies the case representation preserved from the first
	// evaluation slice.
	LegacyVersion = "v1alpha1"
	// VersionV1Alpha1 and VersionV1Alpha2 make the two physical representations
	// explicit for callers that do not use the Version/LegacyVersion aliases.
	VersionV1Alpha1 = LegacyVersion
	VersionV1Alpha2 = Version

	// CasesFileName is the canonical filename used by the repository fixture.
	CasesFileName = "cases.json"

	// CurrentCasesFileName identifies the fixture that exercises the current
	// versioned schema explicitly.
	CurrentCasesFileName = "cases.v1alpha2.json"

	maxCasesFileBytes = 2 << 20
	maxCases          = 512
	maxCaseIDBytes    = 128
	maxTextBytes      = 4 << 10
	maxListItems      = 512
)

var (
	// ErrInvalidCases identifies a malformed or incomplete case set.
	ErrInvalidCases = errors.New("evaluation: invalid cases")
	// ErrUnsupportedVersion identifies a case representation that this
	// package cannot interpret.
	ErrUnsupportedVersion = errors.New("evaluation: unsupported version")
	// ErrDuplicateCase identifies duplicate case, claim, evidence, or gap
	// identities in one case set.
	ErrDuplicateCase = errors.New("evaluation: duplicate case identity")
	// ErrInvalidCaseKind identifies a kind outside the controlled vocabulary.
	ErrInvalidCaseKind = errors.New("evaluation: invalid case kind")
	// ErrInvalidTaskKind identifies a task outside the controlled vocabulary.
	ErrInvalidTaskKind = errors.New("evaluation: invalid task kind")
	// ErrInvalidVariantKind identifies a comparison variant outside the
	// controlled vocabulary.
	ErrInvalidVariantKind = errors.New("evaluation: invalid variant kind")
	// ErrInvalidCriterionKind identifies a success criterion outside the
	// controlled vocabulary.
	ErrInvalidCriterionKind = errors.New("evaluation: invalid criterion kind")
	// ErrInvalidAnalyzerStatus identifies an analyzer applicability state
	// outside the controlled vocabulary.
	ErrInvalidAnalyzerStatus = errors.New("evaluation: invalid analyzer status")
	// ErrInvalidEvaluationPolicy identifies unsafe or incomplete evaluation
	// permissions.
	ErrInvalidEvaluationPolicy = errors.New("evaluation: invalid evaluation policy")
	// ErrInvalidAttributionStage identifies an uncontrolled failure stage.
	ErrInvalidAttributionStage = errors.New("evaluation: invalid attribution stage")
	// ErrUnsafeCase identifies traversal, secret, or raw-content material in a
	// case definition.
	ErrUnsafeCase = errors.New("evaluation: unsafe case material")
	// ErrCaseLimitExceeded identifies a case file or collection above its
	// bounded contract.
	ErrCaseLimitExceeded = errors.New("evaluation: case limit exceeded")
)

// CaseState describes the curation lifecycle of an evaluation case.
type CaseState string

const (
	CaseStateDraft   CaseState = "draft"
	CaseStateCurated CaseState = "curated"
	CaseStateRetired CaseState = "retired"
)

// CaseKind identifies the competency exercised by a case. Possible Flow is a
// static reconstruction and is deliberately distinct from execution evidence.
type CaseKind string

const (
	CaseKindInventory    CaseKind = "inventory"
	CaseKindProvenance   CaseKind = "provenance"
	CaseKindPossibleFlow CaseKind = "possible_flow"
	CaseKindAbstention   CaseKind = "abstention"
)

// ClaimOrigin identifies the epistemic state expected for an acceptable claim.
type ClaimOrigin string

const (
	ClaimOriginObserved  ClaimOrigin = "observed"
	ClaimOriginGenerated ClaimOrigin = "generated"
	ClaimOriginCurated   ClaimOrigin = "curated"
)

// ClaimMatchMode makes clear that acceptable claims are semantic criteria,
// not byte-for-byte answer strings.
type ClaimMatchMode string

const ClaimMatchSemantic ClaimMatchMode = "semantic"

// AttributionStage identifies the stage to which an evaluation failure is
// attributed. The vocabulary is intentionally closed for comparable reports.
type AttributionStage string

const (
	AttributionExtraction AttributionStage = "extraction"
	AttributionRetrieval  AttributionStage = "retrieval"
	AttributionGeneration AttributionStage = "generation"
	AttributionPolicy     AttributionStage = "policy"
)

// TaskKind identifies the user task exercised by a case. It is independent
// from CaseKind, which describes the epistemic shape of the expected answer.
type TaskKind string

const (
	TaskKindLocalization TaskKind = "localization"
	TaskKindExplanation  TaskKind = "explanation"
	TaskKindImpact       TaskKind = "impact"
	TaskKindChange       TaskKind = "change"
)

// EvaluationTask fixes the objective category and bounded objective prose for
// one case. It never contains an instruction for the engine to execute.
type EvaluationTask struct {
	Kind      TaskKind `json:"kind"`
	Objective string   `json:"objective"`
}

// Task is a concise alias for EvaluationTask.
type Task = EvaluationTask

// VariantKind identifies one controlled execution configuration reserved for
// later evaluation runners.
type VariantKind string

const (
	VariantDirectSource    VariantKind = "direct-source"
	VariantTextRetrieval   VariantKind = "text-retrieval"
	VariantManuContext     VariantKind = "manu-context"
	VariantExternalContext VariantKind = "external-context"
)

// EvaluationVariant describes a comparison variant without storing its
// execution result.
type EvaluationVariant struct {
	ID              string      `json:"id"`
	Kind            VariantKind `json:"kind"`
	ToolIDs         []string    `json:"tool_ids"`
	ConfigurationID string      `json:"configuration_id"`
	Capabilities    []string    `json:"capabilities"`
	Limitations     []string    `json:"limitations"`
}

// Variant is a concise alias for EvaluationVariant.
type Variant = EvaluationVariant

// AnalyzerStatus identifies whether an analyzer is part of a case's
// applicable coverage.
type AnalyzerStatus string

const (
	AnalyzerApplicable    AnalyzerStatus = "applicable"
	AnalyzerNotApplicable AnalyzerStatus = "not_applicable"
	AnalyzerUnsupported   AnalyzerStatus = "unsupported"
)

// AnalyzerApplicability records capability and limitation metadata without
// claiming semantic completeness.
type AnalyzerApplicability struct {
	ID           string         `json:"id"`
	Status       AnalyzerStatus `json:"status"`
	Version      string         `json:"version"`
	Capabilities []string       `json:"capabilities"`
	Reason       string         `json:"reason"`
}

// Analyzer is a concise alias for AnalyzerApplicability.
type Analyzer = AnalyzerApplicability

// EvaluationConfiguration identifies the non-secret configuration used by a
// variant. Settings are metadata, never credentials.
type EvaluationConfiguration struct {
	ID       string            `json:"id"`
	Version  string            `json:"version"`
	Settings map[string]string `json:"settings"`
}

// Configuration is a concise alias for EvaluationConfiguration.
type Configuration = EvaluationConfiguration

// CriterionKind identifies an independently evaluated success dimension.
type CriterionKind string

const (
	CriterionCorrectness   CriterionKind = "correctness"
	CriterionCompletion    CriterionKind = "completion"
	CriterionEvidence      CriterionKind = "evidence"
	CriterionCitation      CriterionKind = "citation"
	CriterionGap           CriterionKind = "gap"
	CriterionAbstention    CriterionKind = "abstention"
	CriterionAuthorization CriterionKind = "authorization"
)

// SuccessCriterion is a semantic acceptance rule, not an execution result or
// a scalar confidence score.
type SuccessCriterion struct {
	ID          string        `json:"id"`
	Kind        CriterionKind `json:"kind"`
	Description string        `json:"description"`
	Required    bool          `json:"required"`
	EvidenceIDs []string      `json:"evidence_ids"`
	GapIDs      []string      `json:"gap_ids"`
}

// SuccessCriteria groups independent acceptance dimensions.
type SuccessCriteria struct {
	Items []SuccessCriterion `json:"items"`
}

// Criteria is a descriptive alias for SuccessCriteria.
type Criteria = SuccessCriteria

// ReferenceAnswer stores bounded curation metadata. It is deliberately not a
// complete answer and contains no source excerpt.
type ReferenceAnswer struct {
	Summary  string   `json:"summary"`
	ClaimIDs []string `json:"claim_ids"`
	GapIDs   []string `json:"gap_ids"`
}

// EvaluationPolicy fixes the permissions under which comparable variants are
// interpreted. The zero value remains accepted for v1alpha1 compatibility.
type EvaluationPolicy struct {
	SourceAccess     string   `json:"source_access"`
	ExternalTransfer string   `json:"external_transfer"`
	NetworkAccess    string   `json:"network_access"`
	MutationAccess   string   `json:"mutation_access"`
	Permissions      []string `json:"permissions"`
}

// EvaluationTool identifies an agent, model, or retrieval tool without
// retaining prompts, responses, credentials, or source content.
type EvaluationTool struct {
	ID           string   `json:"id"`
	Version      string   `json:"version"`
	Role         string   `json:"role"`
	Capabilities []string `json:"capabilities"`
	Limitations  []string `json:"limitations"`
}

// Tool is a concise alias for EvaluationTool.
type Tool = EvaluationTool

// Policy is a concise alias for EvaluationPolicy.
type Policy = EvaluationPolicy

// CaseSet is the versioned, deterministic envelope stored by the evaluation
// corpus. LoadCases returns its cases sorted by stable identity.
type CaseSet struct {
	Version string           `json:"version"`
	Cases   []EvaluationCase `json:"cases"`
}

// Cases is a concise alias for CaseSet.
type Cases = CaseSet

// EvaluationCase is one immutable competency case. It stores semantic
// acceptance criteria and references, never source code or a full answer.
type EvaluationCase struct {
	CaseID              string                    `json:"case_id"`
	CaseVersion         int                       `json:"case_version"`
	State               CaseState                 `json:"state"`
	CorpusID            string                    `json:"corpus_id"`
	CorpusRevision      string                    `json:"corpus_revision"`
	SourceID            string                    `json:"source_id"`
	SourceRevision      string                    `json:"source_revision"`
	Scope               CaseScope                 `json:"scope"`
	Inclusions          []ScopeItem               `json:"inclusions"`
	Exclusions          []ScopeItem               `json:"exclusions"`
	AuthorizationRef    string                    `json:"authorization_ref"`
	Audience            string                    `json:"audience"`
	CompetenceQuestion  string                    `json:"competence_question"`
	Kind                CaseKind                  `json:"kind"`
	AcceptableClaims    []AcceptableClaim         `json:"acceptable_claims"`
	ExpectedEvidence    []ExpectedEvidence        `json:"expected_evidence"`
	ExpectedGaps        []ExpectedGap             `json:"expected_gaps"`
	Authors             []string                  `json:"authors"`
	Reviewers           []string                  `json:"reviewers"`
	ReviewedAt          string                    `json:"reviewed_at"`
	FailureAttribution  []FailureAttribution      `json:"failure_attribution"`
	Task                EvaluationTask            `json:"task"`
	Variants            []EvaluationVariant       `json:"variants"`
	Tools               []EvaluationTool          `json:"tools"`
	Configurations      []EvaluationConfiguration `json:"configurations"`
	Limitations         []string                  `json:"limitations"`
	ApplicableAnalyzers []AnalyzerApplicability   `json:"applicable_analyzers"`
	Criteria            SuccessCriteria           `json:"criteria"`
	ReferenceAnswer     ReferenceAnswer           `json:"reference_answer"`
	Policy              EvaluationPolicy          `json:"policy"`
	CreatedAt           string                    `json:"created_at"`
	UpdatedAt           string                    `json:"updated_at"`
	Supersedes          string                    `json:"supersedes"`
}

// MarshalJSON keeps the legacy physical representation free of fields that
// were introduced by v1alpha2. CaseSet.Normalize validates the envelope
// version before invoking this method, so a current case cannot silently omit
// its required metadata through MarshalCases.
func (c EvaluationCase) MarshalJSON() ([]byte, error) {
	type plain EvaluationCase
	encoded, err := json.Marshal(plain(c))
	if err != nil {
		return nil, err
	}
	if c.hasExtendedMetadata() {
		return encoded, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		return nil, err
	}
	for _, name := range []string{
		"task", "variants", "tools", "configurations", "limitations",
		"applicable_analyzers", "criteria", "reference_answer", "policy",
		"created_at", "updated_at", "supersedes",
	} {
		delete(fields, name)
	}
	return json.Marshal(fields)
}

// Case is a concise alias for EvaluationCase.
type Case = EvaluationCase

// CaseScope records the dimensions intentionally attempted by a case.
type CaseScope struct {
	Purpose    string   `json:"purpose"`
	Dimensions []string `json:"dimensions"`
	Artifacts  []string `json:"artifacts"`
}

// ScopeItem identifies an included or excluded logical item and its reason.
type ScopeItem struct {
	Ref    string `json:"ref"`
	Reason string `json:"reason"`
}

// AcceptableClaim is a semantic acceptance criterion. Statement is reference
// prose for reviewers; the loader never compares generated text to it.
type AcceptableClaim struct {
	ClaimID     string         `json:"claim_id"`
	Statement   string         `json:"statement"`
	Origin      ClaimOrigin    `json:"origin"`
	Match       ClaimMatchMode `json:"match"`
	EvidenceIDs []string       `json:"evidence_ids"`
	GapIDs      []string       `json:"gap_ids"`
}

// ExpectedEvidence identifies evidence by stable ID, locator, or a bounded
// metadata pattern. It never carries an excerpt or source content.
type ExpectedEvidence struct {
	EvidenceID string            `json:"evidence_id,omitempty"`
	Kind       string            `json:"kind"`
	Locator    *contract.Locator `json:"locator,omitempty"`
	Pattern    *EvidencePattern  `json:"pattern,omitempty"`
}

// EvidencePattern describes metadata to locate without copying a source
// fragment. Values are paths, symbols, members, attributes, or XPath-like
// selectors only.
type EvidencePattern struct {
	PathPattern  string `json:"path_pattern,omitempty"`
	Member       string `json:"member,omitempty"`
	Symbol       string `json:"symbol,omitempty"`
	Attribute    string `json:"attribute,omitempty"`
	XPath        string `json:"xpath,omitempty"`
	ValuePattern string `json:"value_pattern,omitempty"`
}

// ExpectedGap is a mandatory limitation that a correct evaluation must keep
// visible, together with the stage responsible for recognizing it.
type ExpectedGap struct {
	GapID            string           `json:"gap_id"`
	Code             string           `json:"code"`
	Statement        string           `json:"statement"`
	AttributionStage AttributionStage `json:"attribution_stage"`
}

// FailureAttribution records a known failure/limitation classification for a
// case. It is metadata, not a result produced by the future evaluation runner.
type FailureAttribution struct {
	Stage  AttributionStage `json:"stage"`
	Code   string           `json:"code"`
	Reason string           `json:"reason"`
}

// Validate checks the complete case envelope without changing its order.
func (s CaseSet) Validate() error {
	switch s.Version {
	case Version, LegacyVersion:
	default:
		return fmt.Errorf("%w: case set version", ErrUnsupportedVersion)
	}
	if len(s.Cases) == 0 {
		return fmt.Errorf("%w: no cases", ErrInvalidCases)
	}
	if len(s.Cases) > maxCases {
		return ErrCaseLimitExceeded
	}
	seen := make(map[string]struct{}, len(s.Cases))
	for index, item := range s.Cases {
		if s.Version == LegacyVersion && item.hasExtendedMetadata() {
			return fmt.Errorf("%w: legacy case has current metadata", ErrUnsupportedVersion)
		}
		if err := item.validate(s.Version == Version); err != nil {
			return fmt.Errorf("%w: case %d: %w", ErrInvalidCases, index, err)
		}
		identity := caseIdentity(item)
		if _, exists := seen[identity]; exists {
			return ErrDuplicateCase
		}
		seen[identity] = struct{}{}
	}
	return nil
}

// Validate checks one case's required identity, curation, references, and
// bounded metadata.
func (c EvaluationCase) Validate() error {
	return c.validate(c.hasExtendedMetadata())
}

func (c EvaluationCase) validate(current bool) error {
	if err := validateIdentifier("case_id", c.CaseID, maxCaseIDBytes); err != nil {
		return err
	}
	if c.CaseVersion < 1 {
		return fmt.Errorf("%w: case version", ErrInvalidCases)
	}
	if c.CaseVersion > 10_000 {
		return fmt.Errorf("%w: case version", ErrInvalidCases)
	}
	switch c.State {
	case CaseStateDraft, CaseStateCurated, CaseStateRetired:
	default:
		return fmt.Errorf("%w: case state", ErrInvalidCases)
	}
	switch c.Kind {
	case CaseKindInventory, CaseKindProvenance, CaseKindPossibleFlow, CaseKindAbstention:
	default:
		return ErrInvalidCaseKind
	}
	for name, value := range map[string]string{
		"corpus_id":         c.CorpusID,
		"corpus_revision":   c.CorpusRevision,
		"source_id":         c.SourceID,
		"source_revision":   c.SourceRevision,
		"authorization_ref": c.AuthorizationRef,
	} {
		if err := validateIdentifier(name, value, maxCaseIDBytes); err != nil {
			return err
		}
	}
	if err := c.Scope.Validate(); err != nil {
		return err
	}
	if err := validateSafeText("audience", c.Audience, maxTextBytes); err != nil {
		return err
	}
	if err := validateSafeText("competence_question", c.CompetenceQuestion, maxTextBytes); err != nil {
		return err
	}
	if err := validateScopeItems(c.Inclusions, "inclusion"); err != nil {
		return err
	}
	if err := validateScopeItems(c.Exclusions, "exclusion"); err != nil {
		return err
	}
	if len(c.AcceptableClaims) == 0 || len(c.AcceptableClaims) > maxListItems {
		return fmt.Errorf("%w: acceptable claims", ErrInvalidCases)
	}
	if len(c.ExpectedEvidence) == 0 || len(c.ExpectedEvidence) > maxListItems {
		return fmt.Errorf("%w: expected evidence", ErrInvalidCases)
	}
	if len(c.ExpectedGaps) == 0 || len(c.ExpectedGaps) > maxListItems {
		return fmt.Errorf("%w: expected gaps", ErrInvalidCases)
	}
	if len(c.FailureAttribution) == 0 || len(c.FailureAttribution) > maxListItems {
		return fmt.Errorf("%w: failure attribution", ErrInvalidCases)
	}
	if err := validateReview(c.Authors, c.Reviewers, c.ReviewedAt); err != nil {
		return err
	}

	evidenceIDs, err := validateExpectedEvidence(c.ExpectedEvidence)
	if err != nil {
		return err
	}
	gapIDs, err := validateExpectedGaps(c.ExpectedGaps)
	if err != nil {
		return err
	}
	if err := validateClaims(c.AcceptableClaims, evidenceIDs, gapIDs); err != nil {
		return err
	}
	if err := validateAttribution(c.FailureAttribution); err != nil {
		return err
	}
	if c.Kind == CaseKindAbstention && !hasClaimGap(c.AcceptableClaims) {
		return fmt.Errorf("%w: abstention case needs a gap claim", ErrInvalidCases)
	}
	if current {
		if err := c.validateExtendedMetadata(evidenceIDs, gapIDs); err != nil {
			return err
		}
	}
	return nil
}

// hasExtendedMetadata reports whether a case carries fields introduced by the
// current schema. It is used to keep the legacy representation strict: an
// input declaring v1alpha1 cannot smuggle current-only metadata through the
// compatibility path.
func (c EvaluationCase) hasExtendedMetadata() bool {
	return !c.Task.isZero() || len(c.Variants) != 0 || len(c.Tools) != 0 ||
		len(c.Configurations) != 0 ||
		len(c.Limitations) != 0 || len(c.ApplicableAnalyzers) != 0 ||
		len(c.Criteria.Items) != 0 || !c.ReferenceAnswer.isZero() || !c.Policy.isZero() ||
		c.CreatedAt != "" || c.UpdatedAt != "" || c.Supersedes != ""
}

func (c EvaluationCase) validateExtendedMetadata(evidenceIDs, gapIDs map[string]struct{}) error {
	if err := c.Task.validate(true); err != nil {
		return err
	}
	toolIDs, err := validateTools(c.Tools, true)
	if err != nil {
		return err
	}
	configurationIDs, err := validateConfigurations(c.Configurations, true)
	if err != nil {
		return err
	}
	if err := validateVariants(c.Variants, toolIDs, configurationIDs, true); err != nil {
		return err
	}
	if len(c.Limitations) == 0 {
		return fmt.Errorf("%w: case limitations are required", ErrInvalidCases)
	}
	if len(c.Limitations) > maxListItems {
		return fmt.Errorf("%w: case limitations", ErrCaseLimitExceeded)
	}
	if err := validateStringList(c.Limitations, "case limitation", maxTextBytes); err != nil {
		return err
	}
	if err := validateAnalyzers(c.ApplicableAnalyzers, true); err != nil {
		return err
	}
	if err := c.Criteria.validate(evidenceIDs, gapIDs, true); err != nil {
		return err
	}
	if err := c.ReferenceAnswer.validate(c.AcceptableClaims, gapIDs, true); err != nil {
		return err
	}
	if err := c.Policy.validate(true); err != nil {
		return err
	}
	if err := validateCaseLifecycle(c.CreatedAt, c.UpdatedAt, true); err != nil {
		return err
	}
	if c.Supersedes != "" {
		if err := validateIdentifier("supersedes", c.Supersedes, maxCaseIDBytes); err != nil {
			return err
		}
	}
	return nil
}

// Validate checks a task declaration. The zero value is accepted for the
// legacy case representation; current case sets use the required form.
func (t EvaluationTask) Validate() error {
	return t.validate(false)
}

func (t EvaluationTask) validate(required bool) error {
	if t.isZero() {
		if required {
			return fmt.Errorf("%w: task is required", ErrInvalidCases)
		}
		return nil
	}
	switch t.Kind {
	case TaskKindLocalization, TaskKindExplanation, TaskKindImpact, TaskKindChange:
	default:
		return ErrInvalidTaskKind
	}
	return validateSafeText("task objective", t.Objective, maxTextBytes)
}

func (t EvaluationTask) isZero() bool { return t.Kind == "" && t.Objective == "" }

func validateVariants(items []EvaluationVariant, toolIDs, configurationIDs map[string]struct{}, required bool) error {
	if len(items) == 0 {
		if required {
			return fmt.Errorf("%w: variants are required", ErrInvalidCases)
		}
		return nil
	}
	if len(items) > maxListItems {
		return fmt.Errorf("%w: variants", ErrCaseLimitExceeded)
	}
	hasDirectSource := false
	hasNonBaseline := false
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		if err := validateIdentifier("variant_id", item.ID, maxCaseIDBytes); err != nil {
			return err
		}
		if _, exists := seen[item.ID]; exists {
			return ErrDuplicateCase
		}
		seen[item.ID] = struct{}{}
		switch item.Kind {
		case VariantDirectSource, VariantTextRetrieval, VariantManuContext, VariantExternalContext:
			hasDirectSource = hasDirectSource || item.Kind == VariantDirectSource
			hasNonBaseline = hasNonBaseline || item.Kind != VariantDirectSource
		default:
			return ErrInvalidVariantKind
		}
		if len(item.ToolIDs) == 0 {
			return fmt.Errorf("%w: variant tools", ErrInvalidCases)
		}
		if err := validateReferences(item.ToolIDs, toolIDs, "variant tool"); err != nil {
			return err
		}
		if err := validateIdentifier("variant configuration", item.ConfigurationID, maxCaseIDBytes); err != nil {
			return err
		}
		if _, exists := configurationIDs[item.ConfigurationID]; !exists {
			return fmt.Errorf("%w: variant configuration reference", ErrInvalidCases)
		}
		if err := validateMetadataList(item.Capabilities, "variant capability"); err != nil {
			return err
		}
		if err := validateMetadataList(item.Limitations, "variant limitation"); err != nil {
			return err
		}
	}
	if required && (!hasDirectSource || !hasNonBaseline) {
		return fmt.Errorf("%w: variants require direct-source and a comparison variant", ErrInvalidCases)
	}
	return nil
}

func (c EvaluationConfiguration) Validate() error {
	if c.isZero() {
		return nil
	}
	if err := validateIdentifier("configuration_id", c.ID, maxCaseIDBytes); err != nil {
		return err
	}
	if err := validateMetadataText("configuration version", c.Version, maxCaseIDBytes); err != nil {
		return err
	}
	if len(c.Settings) > maxListItems {
		return fmt.Errorf("%w: configuration settings", ErrCaseLimitExceeded)
	}
	for key, value := range c.Settings {
		if err := validateIdentifier("configuration setting", key, maxCaseIDBytes); err != nil {
			return err
		}
		if sensitiveMetadataKey(key) {
			return ErrUnsafeCase
		}
		if err := validateSafeText("configuration value", value, maxTextBytes); err != nil {
			return err
		}
	}
	return nil
}

func (c EvaluationConfiguration) isZero() bool {
	return c.ID == "" && c.Version == "" && len(c.Settings) == 0
}

func validateTools(items []EvaluationTool, required bool) (map[string]struct{}, error) {
	known := make(map[string]struct{}, len(items))
	if len(items) == 0 {
		if required {
			return nil, fmt.Errorf("%w: tools are required", ErrInvalidCases)
		}
		return known, nil
	}
	if len(items) > maxListItems {
		return nil, fmt.Errorf("%w: tools", ErrCaseLimitExceeded)
	}
	for _, item := range items {
		if err := validateIdentifier("tool_id", item.ID, maxCaseIDBytes); err != nil {
			return nil, err
		}
		if _, exists := known[item.ID]; exists {
			return nil, ErrDuplicateCase
		}
		known[item.ID] = struct{}{}
		if err := validateMetadataText("tool version", item.Version, maxCaseIDBytes); err != nil {
			return nil, err
		}
		if err := validateMetadataText("tool role", item.Role, maxCaseIDBytes); err != nil {
			return nil, err
		}
		if err := validateMetadataList(item.Capabilities, "tool capability"); err != nil {
			return nil, err
		}
		if err := validateMetadataList(item.Limitations, "tool limitation"); err != nil {
			return nil, err
		}
	}
	return known, nil
}

func validateConfigurations(items []EvaluationConfiguration, required bool) (map[string]struct{}, error) {
	known := make(map[string]struct{}, len(items))
	if len(items) == 0 {
		if required {
			return nil, fmt.Errorf("%w: configurations are required", ErrInvalidCases)
		}
		return known, nil
	}
	if len(items) > maxListItems {
		return nil, fmt.Errorf("%w: configurations", ErrCaseLimitExceeded)
	}
	for _, item := range items {
		if item.isZero() {
			return nil, fmt.Errorf("%w: configuration is required", ErrInvalidCases)
		}
		if err := item.Validate(); err != nil {
			return nil, err
		}
		if _, exists := known[item.ID]; exists {
			return nil, ErrDuplicateCase
		}
		known[item.ID] = struct{}{}
	}
	return known, nil
}

func validateAnalyzers(items []AnalyzerApplicability, required bool) error {
	if len(items) == 0 {
		if required {
			return fmt.Errorf("%w: applicable analyzers are required", ErrInvalidCases)
		}
		return nil
	}
	if len(items) > maxListItems {
		return fmt.Errorf("%w: applicable analyzers", ErrCaseLimitExceeded)
	}
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		if err := validateIdentifier("analyzer_id", item.ID, maxCaseIDBytes); err != nil {
			return err
		}
		if _, exists := seen[item.ID]; exists {
			return ErrDuplicateCase
		}
		seen[item.ID] = struct{}{}
		switch item.Status {
		case AnalyzerApplicable, AnalyzerNotApplicable, AnalyzerUnsupported:
		default:
			return ErrInvalidAnalyzerStatus
		}
		if err := validateMetadataText("analyzer version", item.Version, maxCaseIDBytes); err != nil {
			return err
		}
		if err := validateMetadataList(item.Capabilities, "analyzer capability"); err != nil {
			return err
		}
		if err := validateSafeText("analyzer reason", item.Reason, maxTextBytes); err != nil {
			return err
		}
	}
	return nil
}

func (c SuccessCriteria) Validate(evidenceIDs, gapIDs map[string]struct{}) error {
	return c.validate(evidenceIDs, gapIDs, false)
}

func (c SuccessCriteria) validate(evidenceIDs, gapIDs map[string]struct{}, required bool) error {
	if len(c.Items) == 0 {
		if required {
			return fmt.Errorf("%w: success criteria are required", ErrInvalidCases)
		}
		return nil
	}
	if len(c.Items) > maxListItems {
		return fmt.Errorf("%w: success criteria", ErrCaseLimitExceeded)
	}
	hasRequired := false
	seen := make(map[string]struct{}, len(c.Items))
	for _, item := range c.Items {
		hasRequired = hasRequired || item.Required
		if err := validateIdentifier("criterion_id", item.ID, maxCaseIDBytes); err != nil {
			return err
		}
		if _, exists := seen[item.ID]; exists {
			return ErrDuplicateCase
		}
		seen[item.ID] = struct{}{}
		switch item.Kind {
		case CriterionCorrectness, CriterionCompletion, CriterionEvidence, CriterionCitation,
			CriterionGap, CriterionAbstention, CriterionAuthorization:
		default:
			return ErrInvalidCriterionKind
		}
		if err := validateSafeText("criterion description", item.Description, maxTextBytes); err != nil {
			return err
		}
		if err := validateReferences(item.EvidenceIDs, evidenceIDs, "criterion evidence"); err != nil {
			return err
		}
		if err := validateReferences(item.GapIDs, gapIDs, "criterion gap"); err != nil {
			return err
		}
	}
	if required && !hasRequired {
		return fmt.Errorf("%w: success criteria need a required item", ErrInvalidCases)
	}
	return nil
}

func (a ReferenceAnswer) Validate(claims []AcceptableClaim, gapIDs map[string]struct{}) error {
	return a.validate(claims, gapIDs, false)
}

func (a ReferenceAnswer) validate(claims []AcceptableClaim, gapIDs map[string]struct{}, required bool) error {
	if a.isZero() {
		if required {
			return fmt.Errorf("%w: reference answer is required", ErrInvalidCases)
		}
		return nil
	}
	if err := validateSafeText("reference answer summary", a.Summary, maxTextBytes); err != nil {
		return err
	}
	if required && len(a.ClaimIDs) == 0 {
		return fmt.Errorf("%w: reference answer claim is required", ErrInvalidCases)
	}
	claimIDs := make(map[string]struct{}, len(claims))
	for _, claim := range claims {
		claimIDs[claim.ClaimID] = struct{}{}
	}
	if err := validateReferences(a.ClaimIDs, claimIDs, "reference answer claim"); err != nil {
		return err
	}
	if err := validateReferences(a.GapIDs, gapIDs, "reference answer gap"); err != nil {
		return err
	}
	return nil
}

func (a ReferenceAnswer) isZero() bool {
	return a.Summary == "" && len(a.ClaimIDs) == 0 && len(a.GapIDs) == 0
}

func (p EvaluationPolicy) Validate() error {
	return p.validate(false)
}

func (p EvaluationPolicy) validate(required bool) error {
	if p.isZero() {
		if required {
			return ErrInvalidEvaluationPolicy
		}
		return nil
	}
	if !validPolicyValue(p.SourceAccess, "read-only", "metadata-only", "direct-read-only", "none") ||
		!validPolicyValue(p.ExternalTransfer, "deny", "redact", "allow") ||
		!validPolicyValue(p.NetworkAccess, "disabled", "local-only", "allow") ||
		!validPolicyValue(p.MutationAccess, "disabled", "read-only") {
		return ErrInvalidEvaluationPolicy
	}
	if len(p.Permissions) == 0 || len(p.Permissions) > maxListItems {
		return fmt.Errorf("%w: permissions", ErrInvalidEvaluationPolicy)
	}
	for _, permission := range p.Permissions {
		if err := validateIdentifier("permission", permission, maxCaseIDBytes); err != nil {
			return ErrInvalidEvaluationPolicy
		}
		if !validEvaluationPermission(permission) {
			return ErrInvalidEvaluationPolicy
		}
	}
	if err := validateUniqueList(p.Permissions); err != nil {
		return fmt.Errorf("%w: permissions", ErrInvalidEvaluationPolicy)
	}
	return nil
}

func (p EvaluationPolicy) isZero() bool {
	return p.SourceAccess == "" && p.ExternalTransfer == "" && p.NetworkAccess == "" &&
		p.MutationAccess == "" && len(p.Permissions) == 0
}

func validPolicyValue(value string, allowed ...string) bool {
	if value == "" {
		return false
	}
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func validEvaluationPermission(value string) bool {
	switch value {
	case "filesystem.read", "context.read", "evidence.read", "metadata.read", "source.read", "corpus.read", "manifest.read":
		return true
	default:
		return false
	}
}

func validateMetadataList(values []string, label string) error {
	if len(values) > maxListItems {
		return fmt.Errorf("%w: %s count", ErrCaseLimitExceeded, label)
	}
	return validateStringList(values, label, maxTextBytes)
}

func validateMetadataText(name, value string, maxBytes int) error {
	if value == "" {
		return fmt.Errorf("%w: %s", ErrInvalidCases, name)
	}
	return validateSafeText(name, value, maxBytes)
}

func validateUniqueList(values []string) error {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if _, exists := seen[value]; exists {
			return ErrDuplicateCase
		}
		seen[value] = struct{}{}
	}
	return nil
}

func validateCaseLifecycle(createdAt, updatedAt string, required bool) error {
	if createdAt == "" && updatedAt == "" {
		if required {
			return fmt.Errorf("%w: case lifecycle timestamps", ErrInvalidCases)
		}
		return nil
	}
	if createdAt == "" || updatedAt == "" {
		return fmt.Errorf("%w: case lifecycle timestamps", ErrInvalidCases)
	}
	created, createdErr := time.Parse(time.RFC3339, createdAt)
	updated, updatedErr := time.Parse(time.RFC3339, updatedAt)
	if createdErr != nil || updatedErr != nil || updated.Before(created) {
		return fmt.Errorf("%w: case lifecycle timestamps", ErrInvalidCases)
	}
	return nil
}

func sensitiveMetadataKey(value string) bool {
	lower := strings.ToLower(value)
	for _, marker := range []string{"password", "passwd", "secret", "token", "api_key", "api-key", "credential", "authorization", "bearer"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// Validate checks scope prose and dimensions without reading the source.
func (s CaseScope) Validate() error {
	if err := validateSafeText("scope purpose", s.Purpose, maxTextBytes); err != nil {
		return err
	}
	if len(s.Dimensions) == 0 || len(s.Dimensions) > maxListItems {
		return fmt.Errorf("%w: scope dimensions", ErrInvalidCases)
	}
	if err := validateStringList(s.Dimensions, "scope dimension", maxTextBytes); err != nil {
		return err
	}
	if len(s.Artifacts) > maxListItems {
		return fmt.Errorf("%w: scope artifacts", ErrInvalidCases)
	}
	return validateStringList(s.Artifacts, "scope artifact", maxTextBytes)
}

// Normalize validates and returns a reproducibly sorted defensive copy.
func (s CaseSet) Normalize() (CaseSet, error) {
	if err := s.Validate(); err != nil {
		return CaseSet{}, err
	}
	normalized := cloneCaseSet(s)
	sort.SliceStable(normalized.Cases, func(i, j int) bool {
		left, right := normalized.Cases[i], normalized.Cases[j]
		if left.CaseID != right.CaseID {
			return left.CaseID < right.CaseID
		}
		return left.CaseVersion < right.CaseVersion
	})
	for index := range normalized.Cases {
		normalizeCaseCollections(&normalized.Cases[index], normalized.Version == Version)
	}
	return normalized, nil
}

// DecodeCases reads one bounded JSON document, rejects unknown fields and
// trailing values, then validates and sorts it.
func DecodeCases(reader io.Reader) (CaseSet, error) {
	if reader == nil {
		return CaseSet{}, fmt.Errorf("%w: nil reader", ErrInvalidCases)
	}
	data, err := io.ReadAll(io.LimitReader(reader, maxCasesFileBytes+1))
	if err != nil {
		return CaseSet{}, fmt.Errorf("%w: reading cases", ErrInvalidCases)
	}
	if len(data) > maxCasesFileBytes {
		return CaseSet{}, ErrCaseLimitExceeded
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var cases CaseSet
	if err := decoder.Decode(&cases); err != nil {
		return CaseSet{}, fmt.Errorf("%w: decoding cases", ErrInvalidCases)
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); err != io.EOF {
		return CaseSet{}, fmt.Errorf("%w: multiple JSON values", ErrInvalidCases)
	}
	return cases.Normalize()
}

// Decode is an alias for DecodeCases.
func Decode(reader io.Reader) (CaseSet, error) { return DecodeCases(reader) }

// LoadCases reads one case file without requiring or exposing an absolute
// corpus path.
func LoadCases(filePath string) (CaseSet, error) {
	if strings.TrimSpace(filePath) == "" {
		return CaseSet{}, fmt.Errorf("%w: empty path", ErrInvalidCases)
	}
	file, err := os.Open(filePath)
	if err != nil {
		return CaseSet{}, fmt.Errorf("%w: opening cases", ErrInvalidCases)
	}
	defer file.Close()
	return DecodeCases(file)
}

// Load is an alias for LoadCases.
func Load(filePath string) (CaseSet, error) { return LoadCases(filePath) }

// ReadCases is an alias for LoadCases.
func ReadCases(filePath string) (CaseSet, error) { return LoadCases(filePath) }

// MarshalCases returns normalized, indented JSON with a final newline. It is
// the canonical representation used to compare case fixtures byte-for-byte.
func MarshalCases(cases CaseSet) ([]byte, error) {
	normalized, err := cases.Normalize()
	if err != nil {
		return nil, err
	}
	encoded, err := json.MarshalIndent(normalized, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("%w: encoding cases", ErrInvalidCases)
	}
	return append(encoded, '\n'), nil
}

// WriteCases writes a normalized case set for fixture and local tooling use.
// It does not create or modify any corpus source.
func WriteCases(filePath string, cases CaseSet) error {
	data, err := MarshalCases(cases)
	if err != nil {
		return err
	}
	if strings.TrimSpace(filePath) == "" {
		return fmt.Errorf("%w: empty path", ErrInvalidCases)
	}
	if err := os.WriteFile(filePath, data, 0o644); err != nil {
		return fmt.Errorf("%w: writing cases", ErrInvalidCases)
	}
	return nil
}

func validateExpectedEvidence(items []ExpectedEvidence) (map[string]struct{}, error) {
	seen := make(map[string]struct{}, len(items))
	for index, item := range items {
		identity := "anonymous\x00" + evidenceSortKey(item)
		if item.EvidenceID != "" {
			if err := validateIdentifier("evidence_id", item.EvidenceID, maxCaseIDBytes); err != nil {
				return nil, err
			}
			if _, exists := seen[item.EvidenceID]; exists {
				return nil, ErrDuplicateCase
			}
			seen[item.EvidenceID] = struct{}{}
			identity = "id\x00" + item.EvidenceID
		}
		if err := validateSafeText("evidence kind", item.Kind, 256); err != nil {
			return nil, err
		}
		if item.Locator == nil && item.Pattern == nil {
			return nil, fmt.Errorf("%w: evidence %d has no locator or pattern", ErrInvalidCases, index)
		}
		if item.Locator != nil {
			if err := validateCaseLocator(*item.Locator); err != nil {
				return nil, err
			}
		}
		if item.Pattern != nil {
			if err := item.Pattern.Validate(); err != nil {
				return nil, err
			}
		}
		if item.EvidenceID == "" {
			if _, exists := seen[identity]; exists {
				return nil, ErrDuplicateCase
			}
			seen[identity] = struct{}{}
		}
	}
	return seen, nil
}

// Validate checks a metadata-only evidence pattern.
func (p EvidencePattern) Validate() error {
	values := []struct {
		name  string
		value string
	}{
		{"path_pattern", p.PathPattern},
		{"member", p.Member},
		{"symbol", p.Symbol},
		{"attribute", p.Attribute},
		{"xpath", p.XPath},
		{"value_pattern", p.ValuePattern},
	}
	seen := false
	for _, item := range values {
		if item.value == "" {
			continue
		}
		seen = true
		if err := validateSafeText("evidence pattern "+item.name, item.value, maxTextBytes); err != nil {
			return err
		}
		if item.name == "path_pattern" {
			if err := validatePortablePath(item.value); err != nil {
				return err
			}
		}
	}
	if !seen {
		return fmt.Errorf("%w: empty evidence pattern", ErrInvalidCases)
	}
	return nil
}

func validateExpectedGaps(items []ExpectedGap) (map[string]struct{}, error) {
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		if err := validateIdentifier("gap_id", item.GapID, maxCaseIDBytes); err != nil {
			return nil, err
		}
		if _, exists := seen[item.GapID]; exists {
			return nil, ErrDuplicateCase
		}
		seen[item.GapID] = struct{}{}
		if err := validateIdentifier("gap_code", item.Code, maxCaseIDBytes); err != nil {
			return nil, err
		}
		if err := validateSafeText("gap statement", item.Statement, maxTextBytes); err != nil {
			return nil, err
		}
		if !validAttributionStage(item.AttributionStage) {
			return nil, ErrInvalidAttributionStage
		}
	}
	return seen, nil
}

func validateClaims(items []AcceptableClaim, evidenceIDs, gapIDs map[string]struct{}) error {
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		if err := validateIdentifier("claim_id", item.ClaimID, maxCaseIDBytes); err != nil {
			return err
		}
		if _, exists := seen[item.ClaimID]; exists {
			return ErrDuplicateCase
		}
		seen[item.ClaimID] = struct{}{}
		if err := validateSafeText("claim statement", item.Statement, maxTextBytes); err != nil {
			return err
		}
		switch item.Origin {
		case ClaimOriginObserved, ClaimOriginGenerated, ClaimOriginCurated:
		default:
			return fmt.Errorf("%w: claim origin", ErrInvalidCases)
		}
		if item.Match != ClaimMatchSemantic {
			return fmt.Errorf("%w: claim match must be semantic", ErrInvalidCases)
		}
		if len(item.EvidenceIDs) == 0 && len(item.GapIDs) == 0 {
			return fmt.Errorf("%w: claim has no evidence or gap", ErrInvalidCases)
		}
		if err := validateReferences(item.EvidenceIDs, evidenceIDs, "claim evidence"); err != nil {
			return err
		}
		if err := validateReferences(item.GapIDs, gapIDs, "claim gap"); err != nil {
			return err
		}
	}
	return nil
}

func validateAttribution(items []FailureAttribution) error {
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		if !validAttributionStage(item.Stage) {
			return ErrInvalidAttributionStage
		}
		if err := validateIdentifier("attribution code", item.Code, maxCaseIDBytes); err != nil {
			return err
		}
		if err := validateSafeText("attribution reason", item.Reason, maxTextBytes); err != nil {
			return err
		}
		identity := string(item.Stage) + "\x00" + item.Code
		if _, exists := seen[identity]; exists {
			return ErrDuplicateCase
		}
		seen[identity] = struct{}{}
	}
	return nil
}

func validateReview(authors, reviewers []string, reviewedAt string) error {
	if len(authors) == 0 || len(authors) > maxListItems || len(reviewers) == 0 || len(reviewers) > maxListItems {
		return fmt.Errorf("%w: authors and reviewers", ErrInvalidCases)
	}
	if err := validateStringList(authors, "author", maxTextBytes); err != nil {
		return err
	}
	if err := validateStringList(reviewers, "reviewer", maxTextBytes); err != nil {
		return err
	}
	if _, err := time.Parse(time.RFC3339, reviewedAt); err != nil {
		return fmt.Errorf("%w: review timestamp", ErrInvalidCases)
	}
	return nil
}

func validateScopeItems(items []ScopeItem, label string) error {
	if len(items) > maxListItems {
		return fmt.Errorf("%w: %s count", ErrInvalidCases, label)
	}
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		if err := validateSafeText(label+" ref", item.Ref, maxTextBytes); err != nil {
			return err
		}
		if strings.Contains(item.Ref, "/") || strings.Contains(item.Ref, "\\") {
			if err := validatePortablePath(item.Ref); err != nil {
				return err
			}
		}
		if err := validateSafeText(label+" reason", item.Reason, maxTextBytes); err != nil {
			return err
		}
		if _, exists := seen[item.Ref]; exists {
			return ErrDuplicateCase
		}
		seen[item.Ref] = struct{}{}
	}
	return nil
}

func validateStringList(values []string, label string, maxBytes int) error {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if err := validateSafeText(label, value, maxBytes); err != nil {
			return err
		}
		if _, exists := seen[value]; exists {
			return ErrDuplicateCase
		}
		seen[value] = struct{}{}
	}
	return nil
}

func validateReferences(values []string, known map[string]struct{}, label string) error {
	if len(values) > maxListItems {
		return fmt.Errorf("%w: %s count", ErrCaseLimitExceeded, label)
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if err := validateIdentifier(label, value, maxCaseIDBytes); err != nil {
			return err
		}
		if _, duplicate := seen[value]; duplicate {
			return ErrDuplicateCase
		}
		if _, exists := known[value]; !exists {
			return fmt.Errorf("%w: %s reference", ErrInvalidCases, label)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func validateCaseLocator(locator contract.Locator) error {
	if err := locator.Validate(); err != nil {
		return fmt.Errorf("%w: locator", ErrInvalidCases)
	}
	if locator.Path != "" {
		if err := validatePortablePath(locator.Path); err != nil {
			return err
		}
	}
	for name, value := range map[string]string{
		"locator_uri":      locator.URI,
		"locator_source":   locator.SourceID,
		"locator_artifact": locator.ArtifactID,
		"locator_member":   locator.Member,
	} {
		if value == "" {
			continue
		}
		if err := validateSafeText(name, value, maxTextBytes); err != nil {
			return err
		}
	}
	return nil
}

func validatePortablePath(value string) error {
	if value == "" {
		return fmt.Errorf("%w: empty path", ErrUnsafeCase)
	}
	normalized := strings.ReplaceAll(value, "\\", "/")
	if strings.HasPrefix(normalized, "/") || strings.ContainsRune(normalized, 0) || strings.Contains(value, "\\") {
		return fmt.Errorf("%w: path", ErrUnsafeCase)
	}
	if len(normalized) >= 2 && normalized[1] == ':' && unicode.IsLetter(rune(normalized[0])) {
		return fmt.Errorf("%w: path", ErrUnsafeCase)
	}
	for _, segment := range strings.Split(normalized, "/") {
		if segment == ".." {
			return fmt.Errorf("%w: path", ErrUnsafeCase)
		}
	}
	if path.Clean(normalized) == "." {
		return fmt.Errorf("%w: path", ErrUnsafeCase)
	}
	if sensitivePath(normalized) {
		return ErrUnsafeCase
	}
	return nil
}

func validateIdentifier(name, value string, maxBytes int) error {
	if value == "" || len(value) > maxBytes || strings.TrimSpace(value) != value || !utf8.ValidString(value) {
		return fmt.Errorf("%w: %s", ErrInvalidCases, name)
	}
	for _, r := range value {
		if unicode.IsSpace(r) || unicode.IsControl(r) || strings.ContainsRune("/\\", r) {
			return fmt.Errorf("%w: %s", ErrInvalidCases, name)
		}
	}
	return nil
}

func validateSafeText(name, value string, maxBytes int) error {
	if value == "" || len(value) > maxBytes || !utf8.ValidString(value) {
		return fmt.Errorf("%w: %s", ErrInvalidCases, name)
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return ErrUnsafeCase
		}
	}
	if containsSensitiveLiteral(value) {
		return ErrUnsafeCase
	}
	if containsRawSourceMarker(value) {
		return ErrUnsafeCase
	}
	return nil
}

func containsSensitiveLiteral(value string) bool {
	lower := strings.ToLower(value)
	for _, marker := range []string{
		"-----begin ",
		"password=", "passwd=", "secret=", "token=", "api_key=", "api-key=",
		"client_secret=", "credential=", "authorization:", "bearer ",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func containsRawSourceMarker(value string) bool {
	if !strings.ContainsAny(value, "{};") {
		return false
	}
	lower := strings.ToLower(value)
	for _, marker := range []string{"class ", "package ", "import ", "func ", "public class ", "<?xml"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func sensitivePath(value string) bool {
	lower := strings.ToLower(value)
	for _, marker := range []string{
		".env", ".pem", ".key", ".p12", ".pfx", "secret", "password", "credential", "token",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func validAttributionStage(stage AttributionStage) bool {
	switch stage {
	case AttributionExtraction, AttributionRetrieval, AttributionGeneration, AttributionPolicy:
		return true
	default:
		return false
	}
}

func hasClaimGap(claims []AcceptableClaim) bool {
	for _, claim := range claims {
		if len(claim.GapIDs) > 0 {
			return true
		}
	}
	return false
}

func caseIdentity(item EvaluationCase) string {
	return item.CaseID + "\x00" + fmt.Sprint(item.CaseVersion)
}

func cloneCaseSet(input CaseSet) CaseSet {
	output := input
	output.Cases = make([]EvaluationCase, len(input.Cases))
	for index, item := range input.Cases {
		output.Cases[index] = cloneCase(item)
	}
	return output
}

func cloneCase(input EvaluationCase) EvaluationCase {
	output := input
	output.Scope.Dimensions = cloneCaseStrings(input.Scope.Dimensions)
	output.Scope.Artifacts = cloneCaseStrings(input.Scope.Artifacts)
	output.Inclusions = append([]ScopeItem(nil), input.Inclusions...)
	output.Exclusions = append([]ScopeItem(nil), input.Exclusions...)
	output.AcceptableClaims = append([]AcceptableClaim(nil), input.AcceptableClaims...)
	for index, claim := range output.AcceptableClaims {
		output.AcceptableClaims[index].EvidenceIDs = cloneCaseStrings(claim.EvidenceIDs)
		output.AcceptableClaims[index].GapIDs = cloneCaseStrings(claim.GapIDs)
	}
	output.ExpectedEvidence = append([]ExpectedEvidence(nil), input.ExpectedEvidence...)
	for index, item := range output.ExpectedEvidence {
		if item.Locator != nil {
			locator := *item.Locator
			output.ExpectedEvidence[index].Locator = &locator
		}
		if item.Pattern != nil {
			pattern := *item.Pattern
			output.ExpectedEvidence[index].Pattern = &pattern
		}
	}
	output.ExpectedGaps = append([]ExpectedGap(nil), input.ExpectedGaps...)
	output.Authors = cloneCaseStrings(input.Authors)
	output.Reviewers = cloneCaseStrings(input.Reviewers)
	output.FailureAttribution = append([]FailureAttribution(nil), input.FailureAttribution...)
	output.Variants = append([]EvaluationVariant(nil), input.Variants...)
	for index := range output.Variants {
		output.Variants[index].ToolIDs = cloneCaseStrings(input.Variants[index].ToolIDs)
		output.Variants[index].Capabilities = cloneCaseStrings(input.Variants[index].Capabilities)
		output.Variants[index].Limitations = cloneCaseStrings(input.Variants[index].Limitations)
	}
	output.Tools = append([]EvaluationTool(nil), input.Tools...)
	for index := range output.Tools {
		output.Tools[index].Capabilities = cloneCaseStrings(input.Tools[index].Capabilities)
		output.Tools[index].Limitations = cloneCaseStrings(input.Tools[index].Limitations)
	}
	output.Limitations = cloneCaseStrings(input.Limitations)
	output.ApplicableAnalyzers = append([]AnalyzerApplicability(nil), input.ApplicableAnalyzers...)
	for index := range output.ApplicableAnalyzers {
		output.ApplicableAnalyzers[index].Capabilities = cloneCaseStrings(input.ApplicableAnalyzers[index].Capabilities)
	}
	output.Configurations = append([]EvaluationConfiguration(nil), input.Configurations...)
	for index := range output.Configurations {
		output.Configurations[index].Settings = cloneCaseStringMap(input.Configurations[index].Settings)
	}
	output.Criteria.Items = append([]SuccessCriterion(nil), input.Criteria.Items...)
	for index := range output.Criteria.Items {
		output.Criteria.Items[index].EvidenceIDs = cloneCaseStrings(input.Criteria.Items[index].EvidenceIDs)
		output.Criteria.Items[index].GapIDs = cloneCaseStrings(input.Criteria.Items[index].GapIDs)
	}
	output.ReferenceAnswer.ClaimIDs = cloneCaseStrings(input.ReferenceAnswer.ClaimIDs)
	output.ReferenceAnswer.GapIDs = cloneCaseStrings(input.ReferenceAnswer.GapIDs)
	output.Policy.Permissions = cloneCaseStrings(input.Policy.Permissions)
	return output
}

func normalizeCaseCollections(item *EvaluationCase, current bool) {
	if item.Inclusions == nil {
		item.Inclusions = []ScopeItem{}
	}
	if item.Exclusions == nil {
		item.Exclusions = []ScopeItem{}
	}
	if item.Scope.Dimensions == nil {
		item.Scope.Dimensions = []string{}
	}
	if item.Scope.Artifacts == nil {
		item.Scope.Artifacts = []string{}
	}
	if item.ExpectedEvidence == nil {
		item.ExpectedEvidence = []ExpectedEvidence{}
	}
	if item.ExpectedGaps == nil {
		item.ExpectedGaps = []ExpectedGap{}
	}
	if item.FailureAttribution == nil {
		item.FailureAttribution = []FailureAttribution{}
	}
	if current && item.Variants != nil {
		for index := range item.Variants {
			if item.Variants[index].ToolIDs == nil {
				item.Variants[index].ToolIDs = []string{}
			}
			if item.Variants[index].Capabilities == nil {
				item.Variants[index].Capabilities = []string{}
			}
			if item.Variants[index].Limitations == nil {
				item.Variants[index].Limitations = []string{}
			}
			sort.Strings(item.Variants[index].Capabilities)
			sort.Strings(item.Variants[index].Limitations)
		}
	}
	if current && item.Tools != nil {
		for index := range item.Tools {
			if item.Tools[index].Capabilities == nil {
				item.Tools[index].Capabilities = []string{}
			}
			if item.Tools[index].Limitations == nil {
				item.Tools[index].Limitations = []string{}
			}
			sort.Strings(item.Tools[index].Capabilities)
			sort.Strings(item.Tools[index].Limitations)
		}
	}
	if current && item.ApplicableAnalyzers != nil {
		for index := range item.ApplicableAnalyzers {
			if item.ApplicableAnalyzers[index].Capabilities == nil {
				item.ApplicableAnalyzers[index].Capabilities = []string{}
			}
			sort.Strings(item.ApplicableAnalyzers[index].Capabilities)
		}
	}
	if current && item.Criteria.Items != nil {
		for index := range item.Criteria.Items {
			if item.Criteria.Items[index].EvidenceIDs == nil {
				item.Criteria.Items[index].EvidenceIDs = []string{}
			}
			if item.Criteria.Items[index].GapIDs == nil {
				item.Criteria.Items[index].GapIDs = []string{}
			}
		}
	}
	if current && item.Policy.Permissions == nil {
		item.Policy.Permissions = []string{}
	}
	for index := range item.AcceptableClaims {
		if item.AcceptableClaims[index].EvidenceIDs == nil {
			item.AcceptableClaims[index].EvidenceIDs = []string{}
		}
		if item.AcceptableClaims[index].GapIDs == nil {
			item.AcceptableClaims[index].GapIDs = []string{}
		}
	}
	if current {
		sort.Strings(item.Limitations)
		sort.Strings(item.Policy.Permissions)
	}
	sort.Strings(item.Scope.Dimensions)
	sort.Strings(item.Scope.Artifacts)
	sort.Strings(item.Authors)
	sort.Strings(item.Reviewers)
	sort.SliceStable(item.Inclusions, func(i, j int) bool { return item.Inclusions[i].Ref < item.Inclusions[j].Ref })
	sort.SliceStable(item.Exclusions, func(i, j int) bool { return item.Exclusions[i].Ref < item.Exclusions[j].Ref })
	sort.SliceStable(item.AcceptableClaims, func(i, j int) bool { return item.AcceptableClaims[i].ClaimID < item.AcceptableClaims[j].ClaimID })
	sort.SliceStable(item.ExpectedEvidence, func(i, j int) bool {
		return evidenceSortKey(item.ExpectedEvidence[i]) < evidenceSortKey(item.ExpectedEvidence[j])
	})
	sort.SliceStable(item.ExpectedGaps, func(i, j int) bool { return item.ExpectedGaps[i].GapID < item.ExpectedGaps[j].GapID })
	sort.SliceStable(item.FailureAttribution, func(i, j int) bool {
		if item.FailureAttribution[i].Stage != item.FailureAttribution[j].Stage {
			return item.FailureAttribution[i].Stage < item.FailureAttribution[j].Stage
		}
		return item.FailureAttribution[i].Code < item.FailureAttribution[j].Code
	})
	sort.SliceStable(item.Variants, func(i, j int) bool {
		if item.Variants[i].ID != item.Variants[j].ID {
			return item.Variants[i].ID < item.Variants[j].ID
		}
		return item.Variants[i].Kind < item.Variants[j].Kind
	})
	if current {
		sort.SliceStable(item.Tools, func(i, j int) bool {
			if item.Tools[i].ID != item.Tools[j].ID {
				return item.Tools[i].ID < item.Tools[j].ID
			}
			return item.Tools[i].Version < item.Tools[j].Version
		})
		sort.SliceStable(item.Configurations, func(i, j int) bool {
			if item.Configurations[i].ID != item.Configurations[j].ID {
				return item.Configurations[i].ID < item.Configurations[j].ID
			}
			return item.Configurations[i].Version < item.Configurations[j].Version
		})
		sort.SliceStable(item.ApplicableAnalyzers, func(i, j int) bool {
			return item.ApplicableAnalyzers[i].ID < item.ApplicableAnalyzers[j].ID
		})
		sort.SliceStable(item.Criteria.Items, func(i, j int) bool {
			return item.Criteria.Items[i].ID < item.Criteria.Items[j].ID
		})
	}
	for index := range item.Variants {
		sort.Strings(item.Variants[index].ToolIDs)
	}
	for index := range item.AcceptableClaims {
		sort.Strings(item.AcceptableClaims[index].EvidenceIDs)
		sort.Strings(item.AcceptableClaims[index].GapIDs)
	}
	if current {
		for index := range item.Criteria.Items {
			sort.Strings(item.Criteria.Items[index].EvidenceIDs)
			sort.Strings(item.Criteria.Items[index].GapIDs)
		}
		sort.Strings(item.ReferenceAnswer.ClaimIDs)
		sort.Strings(item.ReferenceAnswer.GapIDs)
	}
}

func cloneCaseStringMap(input map[string]string) map[string]string {
	if input == nil {
		return nil
	}
	output := make(map[string]string, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

func cloneCaseStrings(input []string) []string {
	if input == nil {
		return nil
	}
	output := make([]string, len(input))
	copy(output, input)
	return output
}

func evidenceSortKey(item ExpectedEvidence) string {
	var builder strings.Builder
	builder.WriteString(item.EvidenceID)
	builder.WriteByte(0)
	builder.WriteString(item.Kind)
	if item.Locator != nil {
		encoded, _ := json.Marshal(item.Locator)
		builder.Write(encoded)
	}
	if item.Pattern != nil {
		encoded, _ := json.Marshal(item.Pattern)
		builder.Write(encoded)
	}
	return builder.String()
}
