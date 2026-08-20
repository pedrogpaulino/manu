package query

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/pedrogpaulino/manu/internal/contract"
)

const (
	// Version is the version of the internal response representation.
	Version = "v1alpha1"

	defaultMaxTextBytes          int64 = 64 << 10
	defaultMaxClaimBytes         int64 = 16 << 10
	defaultMaxGapBytes           int64 = 4 << 10
	defaultMaxLocatorBytes       int64 = 4 << 10
	defaultMaxTotalBytes         int64 = 256 << 10
	defaultMaxIdentifierBytes    int64 = 256
	defaultMaxClaims                   = 1_000
	defaultMaxCitations                = 2_000
	defaultMaxGaps                     = 1_000
	defaultMaxReferencesPerClaim       = 100
	maxLocatorLine                     = 1_000_000
	maxLocatorColumn                   = 1_000_000
	maxLocatorOffset             int64 = 1 << 40
	maxUsageCounter                    = 1 << 30
	maxGenerationLatency               = 24 * time.Hour
)

var (
	// ErrInvalidResponse identifies a response that cannot be accepted.
	ErrInvalidResponse = errors.New("query: invalid response")
	// ErrUnsupportedVersion identifies a response version this package does
	// not understand.
	ErrUnsupportedVersion = errors.New("query: unsupported response version")
	// ErrInvalidReference identifies a malformed or incoherent citation,
	// claim, gap, or scope reference.
	ErrInvalidReference = errors.New("query: invalid response reference")
	// ErrInvalidDigest identifies a digest that is not a lowercase SHA-256.
	ErrInvalidDigest = errors.New("query: invalid response digest")
	// ErrLimitExceeded identifies a response larger than configured bounds.
	ErrLimitExceeded = errors.New("query: response limit exceeded")
	// ErrUnsafeLocator identifies a locator that is not bounded to source data.
	ErrUnsafeLocator = errors.New("query: unsafe response locator")

	credentialPattern = regexp.MustCompile(`(?i)(?:^|[^[:alnum:]_])(?:[[:alnum:]]+[_-])*(?:password|passwd|secret|token|api[-_]?key|client[-_]?secret)(?:[_-][[:alnum:]]+)*\s*[:=]\s*(?:"[^"\r\n]*"|'[^'\r\n]*'|[^\s,;]+)`)
)

// ClaimKind qualifies the epistemic form of one response assertion.
type ClaimKind string

const (
	ClaimKindObserved  ClaimKind = "observed"
	ClaimKindGenerated ClaimKind = "generated"
	ClaimKindGap       ClaimKind = "gap"
)

// SupportState declares whether the response package supports one claim.
type SupportState string

const (
	SupportSupported   SupportState = "supported"
	SupportUnsupported SupportState = "unsupported"
	SupportAbstained   SupportState = "abstained"
)

// CitationRole qualifies how an Evidence Unit relates to a claim.
type CitationRole string

const (
	CitationRoleSupports CitationRole = "supports"
	CitationRoleContests CitationRole = "contests"
	CitationRoleContext  CitationRole = "context"
)

// KnowledgeState makes the response's review status explicit. There is no
// curated state in this schema.
type KnowledgeState string

const (
	KnowledgeStateGeneratedReviewable KnowledgeState = "generated_reviewable"
)

// Provider is the neutral provider vocabulary recorded in response metadata.
// It intentionally contains no provider request or response DTO.
type Provider string

const (
	// ProviderNone identifies a deterministic local outcome for which no
	// provider was called. It is distinct from ProviderSimulated, which
	// represents a simulator invocation.
	ProviderNone             Provider = "none"
	ProviderSimulated        Provider = "simulated"
	ProviderOpenAI           Provider = "openai"
	ProviderOpenAICompatible Provider = "openai-compatible"
	ProviderOpenRouter       Provider = "openrouter"
)

// Protocol is the neutral generation protocol vocabulary.
type Protocol string

const (
	// ProtocolNone identifies a deterministic local outcome without a model
	// protocol invocation.
	ProtocolNone            Protocol = "none"
	ProtocolResponses       Protocol = "responses"
	ProtocolChatCompletions Protocol = "chat_completions"
)

// Termination records how generation ended.
type Termination string

const (
	TerminationCompleted Termination = "completed"
	TerminationPartial   Termination = "partial"
	TerminationAbstained Termination = "abstained"
)

// Usage records normalized generation counters. Zero is valid when a
// provider does not expose token accounting.
type Usage struct {
	InputItems   int `json:"input_items"`
	OutputItems  int `json:"output_items"`
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// GenerationMetadata records only safe, provider-neutral audit metadata.
// Credentials, questions, prompts, raw evidence, and arbitrary provider
// metadata are intentionally not representable here. Latency is the exact
// wall-clock interval FinishedAt-StartedAt in this version of the schema.
type GenerationMetadata struct {
	Provider      Provider      `json:"provider"`
	Model         string        `json:"model"`
	Profile       string        `json:"profile"`
	Protocol      Protocol      `json:"protocol"`
	Usage         Usage         `json:"usage"`
	Termination   Termination   `json:"termination"`
	PackageID     string        `json:"package_id"`
	PackageDigest string        `json:"package_digest"`
	QueryID       string        `json:"query_id"`
	QueryDigest   string        `json:"query_digest"`
	StartedAt     time.Time     `json:"started_at"`
	FinishedAt    time.Time     `json:"finished_at"`
	Latency       time.Duration `json:"latency"`
}

// ResponseMetadata is a descriptive alias for callers that use the shorter
// response terminology.
type ResponseMetadata = GenerationMetadata

// Claim is one qualified assertion. CitationOrdinals and GapOrdinals refer
// only to ordinals in the same response; package-level semantic validation is
// deliberately deferred to task 8.2.
type Claim struct {
	// ID is an optional stable identifier for generated claims. Legacy callers
	// may omit it, but the integrated query pipeline always emits one so a
	// persisted claim can be correlated without relying on its ordinal.
	ID               string       `json:"id,omitempty"`
	Ordinal          int          `json:"ordinal"`
	Kind             ClaimKind    `json:"kind"`
	Support          SupportState `json:"support"`
	Text             string       `json:"text"`
	CitationOrdinals []int        `json:"citation_ordinals,omitempty"`
	GapOrdinals      []int        `json:"gap_ordinals,omitempty"`
}

// Assertion is a descriptive alias for Claim.
type Assertion = Claim

// Citation points to one Evidence Unit without embedding source content.
// Locator is the canonical bounded contract locator, further constrained by
// this package's response limits.
type Citation struct {
	Ordinal        int              `json:"ordinal"`
	OrganizationID string           `json:"organization_id"`
	SourceID       string           `json:"source_id"`
	SnapshotID     string           `json:"snapshot_id"`
	EvidenceID     string           `json:"evidence_id"`
	Locator        contract.Locator `json:"locator"`
	Role           CitationRole     `json:"role"`
}

// EvidenceCitation is a descriptive alias for Citation.
type EvidenceCitation = Citation

// Gap is a material absence or limitation that remains visible to reviewers.
type Gap struct {
	Ordinal int    `json:"ordinal"`
	ID      string `json:"id"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ResponseGap is a descriptive alias for Gap.
type ResponseGap = Gap

// Response is the versioned, generated-and-reviewable result of a query.
// Claims are never represented as curated knowledge.
type Response struct {
	Version        string             `json:"version"`
	KnowledgeState KnowledgeState     `json:"knowledge_state"`
	Text           string             `json:"text"`
	Claims         []Claim            `json:"claims"`
	Citations      []Citation         `json:"citations"`
	Gaps           []Gap              `json:"gaps"`
	Generation     GenerationMetadata `json:"generation"`
}

// ResponseLimits bounds one response. Zero fields select the safe defaults;
// negative fields are invalid. Limits are intentionally local to this schema
// and do not imply validation against an Evidence Package.
type ResponseLimits struct {
	MaxTextBytes          int64
	MaxClaimBytes         int64
	MaxGapBytes           int64
	MaxLocatorBytes       int64
	MaxTotalBytes         int64
	MaxIdentifierBytes    int64
	MaxClaims             int
	MaxCitations          int
	MaxGaps               int
	MaxReferencesPerClaim int
}

// Limits is a concise compatibility alias.
type Limits = ResponseLimits

// DefaultResponseLimits returns the bounded defaults used by Validate.
func DefaultResponseLimits() ResponseLimits {
	return ResponseLimits{
		MaxTextBytes:          defaultMaxTextBytes,
		MaxClaimBytes:         defaultMaxClaimBytes,
		MaxGapBytes:           defaultMaxGapBytes,
		MaxLocatorBytes:       defaultMaxLocatorBytes,
		MaxTotalBytes:         defaultMaxTotalBytes,
		MaxIdentifierBytes:    defaultMaxIdentifierBytes,
		MaxClaims:             defaultMaxClaims,
		MaxCitations:          defaultMaxCitations,
		MaxGaps:               defaultMaxGaps,
		MaxReferencesPerClaim: defaultMaxReferencesPerClaim,
	}
}

// DefaultLimits is a descriptive alias for DefaultResponseLimits.
func DefaultLimits() ResponseLimits { return DefaultResponseLimits() }

func (l ResponseLimits) normalized() (ResponseLimits, error) {
	defaults := DefaultResponseLimits()
	for name, value := range map[string]int64{
		"max_text_bytes":       valueOrDefault(l.MaxTextBytes, defaults.MaxTextBytes),
		"max_claim_bytes":      valueOrDefault(l.MaxClaimBytes, defaults.MaxClaimBytes),
		"max_gap_bytes":        valueOrDefault(l.MaxGapBytes, defaults.MaxGapBytes),
		"max_locator_bytes":    valueOrDefault(l.MaxLocatorBytes, defaults.MaxLocatorBytes),
		"max_total_bytes":      valueOrDefault(l.MaxTotalBytes, defaults.MaxTotalBytes),
		"max_identifier_bytes": valueOrDefault(l.MaxIdentifierBytes, defaults.MaxIdentifierBytes),
	} {
		if value < 0 {
			return ResponseLimits{}, fmt.Errorf("%w: %s", ErrLimitExceeded, name)
		}
	}
	for name, value := range map[string]int{
		"max_claims":               positiveOrDefault(l.MaxClaims, defaults.MaxClaims),
		"max_citations":            positiveOrDefault(l.MaxCitations, defaults.MaxCitations),
		"max_gaps":                 positiveOrDefault(l.MaxGaps, defaults.MaxGaps),
		"max_references_per_claim": positiveOrDefault(l.MaxReferencesPerClaim, defaults.MaxReferencesPerClaim),
	} {
		if value < 0 {
			return ResponseLimits{}, fmt.Errorf("%w: %s", ErrLimitExceeded, name)
		}
	}
	l.MaxTextBytes = valueOrDefault(l.MaxTextBytes, defaults.MaxTextBytes)
	l.MaxClaimBytes = valueOrDefault(l.MaxClaimBytes, defaults.MaxClaimBytes)
	l.MaxGapBytes = valueOrDefault(l.MaxGapBytes, defaults.MaxGapBytes)
	l.MaxLocatorBytes = valueOrDefault(l.MaxLocatorBytes, defaults.MaxLocatorBytes)
	l.MaxTotalBytes = valueOrDefault(l.MaxTotalBytes, defaults.MaxTotalBytes)
	l.MaxIdentifierBytes = valueOrDefault(l.MaxIdentifierBytes, defaults.MaxIdentifierBytes)
	l.MaxClaims = positiveOrDefault(l.MaxClaims, defaults.MaxClaims)
	l.MaxCitations = positiveOrDefault(l.MaxCitations, defaults.MaxCitations)
	l.MaxGaps = positiveOrDefault(l.MaxGaps, defaults.MaxGaps)
	l.MaxReferencesPerClaim = positiveOrDefault(l.MaxReferencesPerClaim, defaults.MaxReferencesPerClaim)
	return l, nil
}

func valueOrDefault(value, fallback int64) int64 {
	if value == 0 {
		return fallback
	}
	return value
}

func positiveOrDefault(value, fallback int) int {
	if value == 0 {
		return fallback
	}
	return value
}

// Validate checks the response with safe default limits.
func (r Response) Validate() error { return r.ValidateWithLimits(ResponseLimits{}) }

// ValidateWithLimits checks schema, ordinals, references, scope, metadata,
// and bounded text without consulting the evidence package or a database.
func (r Response) ValidateWithLimits(limits ResponseLimits) error {
	limits, err := limits.normalized()
	if err != nil {
		return err
	}
	if r.Version != Version {
		return ErrUnsupportedVersion
	}
	if r.KnowledgeState != KnowledgeStateGeneratedReviewable {
		return invalidResponse("knowledge state")
	}
	if err := validateGenerationMetadata(r.Generation, limits); err != nil {
		return err
	}
	if err := validateResponseText(r.Text, limits.MaxTextBytes, true); err != nil {
		if (r.Generation.Termination == TerminationAbstained || r.Generation.Termination == TerminationPartial) && strings.TrimSpace(r.Text) == "" {
			// A limited response may carry its explanation in a material Gap.
		} else {
			return err
		}
	}
	if len(r.Claims) > limits.MaxClaims || len(r.Citations) > limits.MaxCitations || len(r.Gaps) > limits.MaxGaps {
		return fmt.Errorf("%w: response item count", ErrLimitExceeded)
	}

	gapByOrdinal, err := validateGaps(r.Gaps, limits)
	if err != nil {
		return err
	}
	citationByOrdinal, err := validateCitations(r.Citations, limits)
	if err != nil {
		return err
	}
	if err := validateClaims(r.Claims, citationByOrdinal, gapByOrdinal, limits); err != nil {
		return err
	}
	if err := validateCitationUsage(r.Claims, citationByOrdinal); err != nil {
		return err
	}
	if err := validateGenerationConsistency(r); err != nil {
		return err
	}
	if responsePayloadBytes(r) > limits.MaxTotalBytes {
		return fmt.Errorf("%w: total response bytes", ErrLimitExceeded)
	}
	return nil
}

func invalidResponse(field string) error {
	return fmt.Errorf("%w: %s", ErrInvalidResponse, field)
}

func validateGenerationMetadata(metadata GenerationMetadata, limits ResponseLimits) error {
	switch metadata.Provider {
	case ProviderNone, ProviderSimulated, ProviderOpenAI, ProviderOpenAICompatible, ProviderOpenRouter:
	default:
		return invalidResponse("generation provider")
	}
	if err := validateSafeToken("generation model", metadata.Model, limits.MaxIdentifierBytes); err != nil {
		return err
	}
	if err := validateSafeToken("generation profile", metadata.Profile, limits.MaxIdentifierBytes); err != nil {
		return err
	}
	switch metadata.Protocol {
	case ProtocolNone, ProtocolResponses, ProtocolChatCompletions:
	default:
		return invalidResponse("generation protocol")
	}
	if metadata.Provider == ProviderNone {
		if metadata.Protocol != ProtocolNone || metadata.Termination != TerminationAbstained || metadata.Usage != (Usage{}) {
			return invalidResponse("none provider metadata")
		}
	} else if metadata.Protocol == ProtocolNone {
		return invalidResponse("generation provider protocol")
	}
	if metadata.Provider == ProviderOpenAI && metadata.Protocol != ProtocolResponses {
		return invalidResponse("generation provider protocol")
	}
	if (metadata.Provider == ProviderOpenAICompatible || metadata.Provider == ProviderOpenRouter) && metadata.Protocol != ProtocolChatCompletions {
		return invalidResponse("generation provider protocol")
	}
	switch metadata.Termination {
	case TerminationCompleted, TerminationPartial, TerminationAbstained:
	default:
		return invalidResponse("generation termination")
	}
	if err := validateUsage(metadata.Usage); err != nil {
		return err
	}
	if metadata.Provider != ProviderNone && metadata.Usage.InputItems != 1 {
		return invalidResponse("generation input usage")
	}
	switch metadata.Termination {
	case TerminationCompleted:
		if metadata.Usage.OutputItems != 1 {
			return invalidResponse("completed output usage")
		}
	case TerminationAbstained:
		if metadata.Usage.OutputItems != 0 {
			return invalidResponse("abstained output usage")
		}
	case TerminationPartial:
		if metadata.Usage.OutputItems > 1 {
			return invalidResponse("partial output usage")
		}
	}
	if err := validateOpaqueID("package id", metadata.PackageID, limits.MaxIdentifierBytes); err != nil {
		return err
	}
	if err := validateOpaqueID("query id", metadata.QueryID, limits.MaxIdentifierBytes); err != nil {
		return err
	}
	if !isSHA256(metadata.PackageDigest) || !isSHA256(metadata.QueryDigest) {
		return fmt.Errorf("%w: generation digest", ErrInvalidDigest)
	}
	startedAt := metadata.StartedAt.Round(0)
	finishedAt := metadata.FinishedAt.Round(0)
	if startedAt.IsZero() || finishedAt.IsZero() || finishedAt.Before(startedAt) {
		return invalidResponse("generation timestamps")
	}
	if metadata.Latency < 0 || metadata.Latency > maxGenerationLatency || finishedAt.Sub(startedAt) != metadata.Latency {
		return invalidResponse("generation latency")
	}
	return nil
}

func validateUsage(usage Usage) error {
	for _, value := range []int{usage.InputItems, usage.OutputItems, usage.InputTokens, usage.OutputTokens} {
		if value < 0 || value > maxUsageCounter {
			return invalidResponse("generation usage")
		}
	}
	return nil
}

func validateResponseText(text string, maxBytes int64, required bool) error {
	if !utf8.ValidString(text) || strings.IndexByte(text, 0) >= 0 {
		return invalidResponse("response text")
	}
	if int64(len([]byte(text))) > maxBytes {
		return fmt.Errorf("%w: response text", ErrLimitExceeded)
	}
	if required && strings.TrimSpace(text) == "" {
		return invalidResponse("response text is required")
	}
	if containsCredentialPattern(text) {
		return invalidResponse("response text contains restricted material")
	}
	return nil
}

func validateClaims(claims []Claim, citations map[int]Citation, gaps map[int]Gap, limits ResponseLimits) error {
	seenIDs := make(map[string]struct{}, len(claims))
	for index, claim := range claims {
		if claim.Ordinal != index+1 {
			return invalidResponse("claim ordinals")
		}
		if claim.ID != "" {
			if err := validateOpaqueID("claim id", claim.ID, limits.MaxIdentifierBytes); err != nil {
				return err
			}
			if _, exists := seenIDs[claim.ID]; exists {
				return invalidResponse("duplicate claim id")
			}
			seenIDs[claim.ID] = struct{}{}
		}
		switch claim.Kind {
		case ClaimKindObserved, ClaimKindGenerated, ClaimKindGap:
		default:
			return invalidResponse("claim kind")
		}
		switch claim.Support {
		case SupportSupported, SupportUnsupported, SupportAbstained:
		default:
			return invalidResponse("claim support")
		}
		if err := validateResponseText(claim.Text, limits.MaxClaimBytes, true); err != nil {
			return err
		}
		if err := validateOrdinalReferences(claim.CitationOrdinals, len(citations), limits.MaxReferencesPerClaim, "claim citations"); err != nil {
			return err
		}
		if err := validateOrdinalReferences(claim.GapOrdinals, len(gaps), limits.MaxReferencesPerClaim, "claim gaps"); err != nil {
			return err
		}
		if claim.Support == SupportSupported && !hasSupportingCitation(claim.CitationOrdinals, citations) {
			return invalidResponse("supported claim citation")
		}
		if claim.Support != SupportSupported && hasSupportingCitation(claim.CitationOrdinals, citations) {
			return invalidResponse("unsupported claim citation")
		}
		if claim.Kind == ClaimKindGap && claim.Support != SupportAbstained {
			return invalidResponse("gap claim support")
		}
		if claim.Support == SupportAbstained && len(claim.GapOrdinals) == 0 {
			return invalidResponse("abstained claim gap")
		}
	}
	return nil
}

func validateOrdinalReferences(ordinals []int, maxOrdinal, maxReferences int, field string) error {
	if len(ordinals) > maxReferences {
		return fmt.Errorf("%w: %s", ErrLimitExceeded, field)
	}
	previous := 0
	for _, ordinal := range ordinals {
		if ordinal <= previous || ordinal < 1 || ordinal > maxOrdinal {
			return invalidResponse(field + " ordinals")
		}
		previous = ordinal
	}
	return nil
}

func validateGaps(gaps []Gap, limits ResponseLimits) (map[int]Gap, error) {
	byOrdinal := make(map[int]Gap, len(gaps))
	seenIDs := make(map[string]struct{}, len(gaps))
	for index, gap := range gaps {
		if gap.Ordinal != index+1 {
			return nil, invalidResponse("gap ordinals")
		}
		if err := validateOpaqueID("gap id", gap.ID, limits.MaxIdentifierBytes); err != nil {
			return nil, err
		}
		if _, exists := seenIDs[gap.ID]; exists {
			return nil, invalidResponse("duplicate gap id")
		}
		seenIDs[gap.ID] = struct{}{}
		if err := validateSafeToken("gap code", gap.Code, limits.MaxIdentifierBytes); err != nil {
			return nil, err
		}
		if err := validateResponseText(gap.Message, limits.MaxGapBytes, true); err != nil {
			return nil, err
		}
		byOrdinal[gap.Ordinal] = gap
	}
	return byOrdinal, nil
}

func validateCitations(citations []Citation, limits ResponseLimits) (map[int]Citation, error) {
	byOrdinal := make(map[int]Citation, len(citations))
	keys := make(map[string]struct{}, len(citations))
	var scope *citationScope
	for index, citation := range citations {
		if citation.Ordinal != index+1 {
			return nil, invalidResponse("citation ordinals")
		}
		for name, value := range map[string]string{
			"citation organization id": citation.OrganizationID,
			"citation source id":       citation.SourceID,
			"citation snapshot id":     citation.SnapshotID,
		} {
			if err := validateUUID(name, value); err != nil {
				return nil, err
			}
		}
		if err := validateOpaqueID("citation evidence id", citation.EvidenceID, limits.MaxIdentifierBytes); err != nil {
			return nil, err
		}
		switch citation.Role {
		case CitationRoleSupports, CitationRoleContests, CitationRoleContext:
		default:
			return nil, invalidResponse("citation role")
		}
		if err := validateCitationLocator(citation.Locator, citation.SourceID, limits.MaxLocatorBytes); err != nil {
			return nil, err
		}
		key := strings.ToLower(citation.OrganizationID) + "\x00" + strings.ToLower(citation.SourceID) + "\x00" + strings.ToLower(citation.SnapshotID) + "\x00" + citation.EvidenceID
		if _, exists := keys[key]; exists {
			return nil, invalidResponse("duplicate citation")
		}
		keys[key] = struct{}{}
		current := citationScope{OrganizationID: strings.ToLower(citation.OrganizationID), SourceID: strings.ToLower(citation.SourceID), SnapshotID: strings.ToLower(citation.SnapshotID)}
		if scope == nil {
			scope = &current
		} else if *scope != current {
			return nil, fmt.Errorf("%w: mixed citation scope", ErrInvalidReference)
		}
		byOrdinal[citation.Ordinal] = citation
	}
	return byOrdinal, nil
}

type citationScope struct {
	OrganizationID string
	SourceID       string
	SnapshotID     string
}

func validateCitationUsage(claims []Claim, citations map[int]Citation) error {
	used := make(map[int]struct{}, len(citations))
	for _, claim := range claims {
		for _, ordinal := range claim.CitationOrdinals {
			_, exists := citations[ordinal]
			if !exists {
				return fmt.Errorf("%w: citation ordinal", ErrInvalidReference)
			}
			used[ordinal] = struct{}{}
		}
	}
	if len(used) != len(citations) {
		return fmt.Errorf("%w: orphan citation", ErrInvalidReference)
	}
	return nil
}

func hasSupportingCitation(ordinals []int, citations map[int]Citation) bool {
	for _, ordinal := range ordinals {
		if citation, exists := citations[ordinal]; exists && citation.Role == CitationRoleSupports {
			return true
		}
	}
	return false
}

func validateGenerationConsistency(response Response) error {
	if response.Generation.Termination == TerminationAbstained {
		if len(response.Gaps) == 0 {
			return invalidResponse("abstention gap")
		}
		for _, claim := range response.Claims {
			if claim.Support == SupportSupported {
				return invalidResponse("abstained supported claim")
			}
		}
	}
	if response.Generation.Termination == TerminationPartial && len(response.Gaps) == 0 {
		return invalidResponse("partial response gap")
	}
	if response.Generation.Termination == TerminationCompleted && strings.TrimSpace(response.Text) == "" {
		return invalidResponse("completed response text")
	}
	if len(response.Claims) == 0 && len(response.Gaps) == 0 {
		return invalidResponse("response claims or gaps")
	}
	return nil
}

func responsePayloadBytes(response Response) int64 {
	var total int64
	add := func(value string) { total += int64(len([]byte(value))) }
	add(response.Text)
	for _, claim := range response.Claims {
		add(claim.Text)
	}
	for _, citation := range response.Citations {
		add(citation.OrganizationID)
		add(citation.SourceID)
		add(citation.SnapshotID)
		add(citation.EvidenceID)
		add(citation.Locator.URI)
		add(citation.Locator.SourceID)
		add(citation.Locator.ArtifactID)
		add(citation.Locator.Path)
		add(citation.Locator.Member)
	}
	for _, gap := range response.Gaps {
		add(gap.ID)
		add(gap.Code)
		add(gap.Message)
	}
	add(response.Generation.Model)
	add(response.Generation.Profile)
	add(response.Generation.PackageID)
	add(response.Generation.PackageDigest)
	add(response.Generation.QueryID)
	add(response.Generation.QueryDigest)
	return total
}

func validateCitationLocator(locator contract.Locator, sourceID string, maxBytes int64) error {
	if err := locator.Validate(); err != nil {
		return fmt.Errorf("%w: locator", ErrUnsafeLocator)
	}
	if locator.SourceID != "" && !strings.EqualFold(locator.SourceID, sourceID) {
		return fmt.Errorf("%w: locator source scope", ErrInvalidReference)
	}
	if locator.ArtifactID != "" {
		if err := validateOpaqueID("locator artifact id", locator.ArtifactID, maxBytes); err != nil {
			return err
		}
	}
	if locator.StartLine > maxLocatorLine || locator.EndLine > maxLocatorLine || locator.StartColumn > maxLocatorColumn || locator.EndColumn > maxLocatorColumn || locator.ByteOffset > maxLocatorOffset || locator.ByteLength > maxLocatorOffset {
		return fmt.Errorf("%w: locator position", ErrLimitExceeded)
	}
	parts := []string{locator.URI, locator.SourceID, locator.ArtifactID, locator.Path, locator.Member}
	var total int64
	for _, value := range parts {
		if !utf8.ValidString(value) || containsControl(value) || containsCredentialPattern(value) {
			return fmt.Errorf("%w: locator text", ErrUnsafeLocator)
		}
		total += int64(len([]byte(value)))
	}
	if total > maxBytes {
		return fmt.Errorf("%w: locator size", ErrLimitExceeded)
	}
	normalizedPath := strings.ReplaceAll(locator.Path, "\\", "/")
	if path.IsAbs(normalizedPath) || hasWindowsDrivePrefix(normalizedPath) {
		return fmt.Errorf("%w: absolute locator path", ErrUnsafeLocator)
	}
	for _, segment := range strings.Split(normalizedPath, "/") {
		if segment == ".." {
			return fmt.Errorf("%w: locator traversal", ErrUnsafeLocator)
		}
	}
	return nil
}

func hasWindowsDrivePrefix(value string) bool {
	if len(value) < 2 || value[1] != ':' {
		return false
	}
	return (value[0] >= 'a' && value[0] <= 'z') || (value[0] >= 'A' && value[0] <= 'Z')
}

func validateOpaqueID(field, value string, maxBytes int64) error {
	if err := validateSafeToken(field, value, maxBytes); err != nil {
		return err
	}
	return nil
}

func validateSafeToken(field, value string, maxBytes int64) error {
	value = strings.TrimSpace(value)
	if value == "" || int64(len([]byte(value))) > maxBytes || !utf8.ValidString(value) || containsControl(value) || containsCredentialPattern(value) {
		return invalidResponse(field)
	}
	for _, character := range value {
		if unicode.IsSpace(character) {
			return invalidResponse(field)
		}
	}
	return nil
}

func validateUUID(field, value string) error {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return fmt.Errorf("%w: %s", ErrInvalidReference, field)
	}
	compact := strings.ReplaceAll(value, "-", "")
	if len(compact) != 32 {
		return fmt.Errorf("%w: %s", ErrInvalidReference, field)
	}
	if _, err := hex.DecodeString(compact); err != nil {
		return fmt.Errorf("%w: %s", ErrInvalidReference, field)
	}
	return nil
}

func isSHA256(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func containsControl(value string) bool {
	for _, character := range value {
		if character == '\n' || character == '\r' || character == '\t' {
			continue
		}
		if unicode.IsControl(character) {
			return true
		}
	}
	return false
}

func containsCredentialPattern(value string) bool {
	return credentialPattern.MatchString(value)
}

// UnmarshalJSON rejects unknown fields so secret-shaped additions cannot be
// silently accepted and later forgotten by a round trip.
func (r *Response) UnmarshalJSON(data []byte) error {
	if r == nil {
		return invalidResponse("nil response")
	}
	type responseAlias Response
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var decoded responseAlias
	if err := decoder.Decode(&decoded); err != nil {
		return invalidResponse("json response")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return invalidResponse("json response has trailing data")
	}
	*r = Response(decoded)
	return nil
}
