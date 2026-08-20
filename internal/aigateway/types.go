package aigateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode"
)

const (
	// EmbeddingProfileVersion is the version of the internal embedding profile
	// contract. It is independent from any provider API version.
	EmbeddingProfileVersion = "v1alpha1"
	// GenerationProfileVersion is the version of the internal generation
	// profile contract.
	GenerationProfileVersion = "v1alpha1"
	// GenerationEnvelopeVersion is the version of the neutral generated output
	// envelope. Claims and persistence schemas belong to later tasks.
	GenerationEnvelopeVersion = "v1alpha1"

	maxIdentifierBytes        = 128
	maxQuestionBytes          = 64 << 10
	maxEvidenceContentBytes   = 256 << 10
	maxEmbeddingBatchSize     = 1_024
	maxEmbeddingBatchBytes    = 4 << 20
	maxMetadataEntries        = 32
	maxMetadataKeyBytes       = 128
	maxMetadataValueBytes     = 512
	maxGenerationTextBytes    = 256 << 10
	maxGenerationEvidenceID   = 10_000
	maxGenerationPackageBytes = 4 << 20
	maxGenerationGapCount     = 1_000
	maxEmbeddingDimension     = 4_096
	maxSimulationLatency      = 10 * time.Minute
)

// Provider identifies a configured provider without importing provider DTOs.
type Provider string

const (
	ProviderUnknown          Provider = ""
	ProviderSimulated        Provider = "simulated"
	ProviderOpenAI           Provider = "openai"
	ProviderOpenAICompatible Provider = "openai-compatible"
	ProviderOpenRouter       Provider = "openrouter"
)

// Capability identifies the independent gateway operation represented by a
// request or normalized error.
type Capability string

const (
	CapabilityUnknown    Capability = ""
	CapabilityEmbedding  Capability = "embedding"
	CapabilityGeneration Capability = "generation"
)

// Protocol identifies the explicitly selected generation protocol. Adapters
// own protocol details; the core only records the configured choice.
type Protocol string

const (
	ProtocolUnknown         Protocol = ""
	ProtocolResponses       Protocol = "responses"
	ProtocolChatCompletions Protocol = "chat_completions"
)

// ErrorKind is the normalized taxonomy shared by both gateway capabilities.
type ErrorKind string

const (
	ErrorKindUnknown         ErrorKind = "unknown"
	ErrorKindAuthentication  ErrorKind = "authentication"
	ErrorKindConfiguration   ErrorKind = "configuration"
	ErrorKindCapability      ErrorKind = "capability"
	ErrorKindBudget          ErrorKind = "budget"
	ErrorKindRateLimit       ErrorKind = "rate_limit"
	ErrorKindUnavailable     ErrorKind = "unavailable"
	ErrorKindTimeout         ErrorKind = "timeout"
	ErrorKindCancelled       ErrorKind = "cancelled"
	ErrorKindContentBlocked  ErrorKind = "content_blocked"
	ErrorKindInvalidResponse ErrorKind = "invalid_response"
)

// GatewayError is a normalized error. Its Error string contains only the
// stable category; provider messages, credentials, prompts, and evidence are
// deliberately not included. Cause remains available to internal callers via
// errors.Is/errors.As without becoming part of the diagnostic string.
type GatewayError struct {
	Kind       ErrorKind
	Capability Capability
	Cause      error
}

func (e *GatewayError) Error() string {
	if e == nil {
		return "ai gateway: unknown"
	}
	if e.Kind == "" {
		return "ai gateway: unknown"
	}
	return "ai gateway: " + string(e.Kind)
}

// Unwrap keeps context cancellation and provider-specific internal causes
// inspectable without exposing them through Error.
func (e *GatewayError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// Is lets callers match taxonomy sentinels while retaining capability data.
func (e *GatewayError) Is(target error) bool {
	other, ok := target.(*GatewayError)
	return ok && e != nil && other != nil && e.Kind == other.Kind
}

var (
	ErrAuthentication  = &GatewayError{Kind: ErrorKindAuthentication}
	ErrConfiguration   = &GatewayError{Kind: ErrorKindConfiguration}
	ErrCapability      = &GatewayError{Kind: ErrorKindCapability}
	ErrBudget          = &GatewayError{Kind: ErrorKindBudget}
	ErrRateLimit       = &GatewayError{Kind: ErrorKindRateLimit}
	ErrUnavailable     = &GatewayError{Kind: ErrorKindUnavailable}
	ErrTimeout         = &GatewayError{Kind: ErrorKindTimeout}
	ErrCancelled       = &GatewayError{Kind: ErrorKindCancelled}
	ErrContentBlocked  = &GatewayError{Kind: ErrorKindContentBlocked}
	ErrInvalidResponse = &GatewayError{Kind: ErrorKindInvalidResponse}
)

// NewGatewayError creates an error in the normalized taxonomy. The cause is
// retained for programmatic inspection but never rendered by Error.
func NewGatewayError(kind ErrorKind, capability Capability, cause error) error {
	if !validErrorKind(kind) {
		kind = ErrorKindInvalidResponse
	}
	return &GatewayError{Kind: kind, Capability: capability, Cause: cause}
}

// NormalizeError maps context cancellation and unknown provider failures to
// the gateway taxonomy. Existing GatewayErrors are preserved.
func NormalizeError(capability Capability, err error) error {
	if err == nil {
		return nil
	}
	var gatewayErr *GatewayError
	if errors.As(err, &gatewayErr) {
		if validErrorKind(gatewayErr.Kind) {
			return err
		}
		return NewGatewayError(ErrorKindInvalidResponse, capability, err)
	}
	if errors.Is(err, context.Canceled) {
		return NewGatewayError(ErrorKindCancelled, capability, err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return NewGatewayError(ErrorKindTimeout, capability, err)
	}
	return NewGatewayError(ErrorKindInvalidResponse, capability, err)
}

// EmbeddingProfile selects one immutable embedding configuration. It is
// independent from GenerationProfile, so changing generation never changes
// an embedding identity.
type EmbeddingProfile struct {
	Provider     Provider `json:"provider"`
	Model        string   `json:"model"`
	Version      string   `json:"version"`
	Dimension    int      `json:"dimension"`
	Normalize    bool     `json:"normalize"`
	MaxBatchSize int      `json:"max_batch_size"`
}

// Validate checks the profile without contacting a provider.
func (p EmbeddingProfile) Validate() error {
	if err := validateProvider(p.Provider); err != nil {
		return err
	}
	if err := validateIdentifier(p.Model, maxIdentifierBytes); err != nil {
		return fmt.Errorf("embedding profile model: %w", err)
	}
	if err := validateIdentifier(p.Version, maxIdentifierBytes); err != nil {
		return fmt.Errorf("embedding profile version: %w", err)
	}
	if p.Version != EmbeddingProfileVersion {
		return fmt.Errorf("embedding profile version: %w", ErrConfiguration)
	}
	if p.Dimension <= 0 || p.Dimension > maxEmbeddingDimension || p.MaxBatchSize <= 0 || p.MaxBatchSize > maxEmbeddingBatchSize {
		return fmt.Errorf("embedding profile limits: %w", ErrConfiguration)
	}
	return nil
}

// GenerationProfile selects one immutable generation configuration. Protocol
// is explicit so compatible providers cannot silently fall back.
type GenerationProfile struct {
	Provider       Provider `json:"provider"`
	Model          string   `json:"model"`
	Version        string   `json:"version"`
	Protocol       Protocol `json:"protocol"`
	MaxOutputBytes int      `json:"max_output_bytes"`
}

// Validate checks the profile without contacting a provider.
func (p GenerationProfile) Validate() error {
	if err := validateProvider(p.Provider); err != nil {
		return err
	}
	if err := validateIdentifier(p.Model, maxIdentifierBytes); err != nil {
		return fmt.Errorf("generation profile model: %w", err)
	}
	if err := validateIdentifier(p.Version, maxIdentifierBytes); err != nil {
		return fmt.Errorf("generation profile version: %w", err)
	}
	if p.Version != GenerationProfileVersion {
		return fmt.Errorf("generation profile version: %w", ErrConfiguration)
	}
	if p.Protocol != ProtocolResponses && p.Protocol != ProtocolChatCompletions {
		return fmt.Errorf("generation profile protocol: %w", ErrConfiguration)
	}
	if err := validateGenerationProtocol(p.Provider, p.Protocol); err != nil {
		return fmt.Errorf("generation profile protocol: %w", err)
	}
	if p.MaxOutputBytes <= 0 || p.MaxOutputBytes > maxGenerationTextBytes {
		return fmt.Errorf("generation profile limits: %w", ErrConfiguration)
	}
	return nil
}

// EmbeddingItem is authorized input for one vector. Content is never included
// in results or errors; ContentHash is the stable identity used by the
// simulator and future cache implementations.
type EmbeddingItem struct {
	ID          string `json:"id"`
	Content     string `json:"content"`
	ContentHash string `json:"content_hash,omitempty"`
}

func (i EmbeddingItem) validate() error {
	if err := validateIdentifier(i.ID, maxIdentifierBytes); err != nil {
		return fmt.Errorf("embedding item id: %w", err)
	}
	if len(i.Content) > maxEvidenceContentBytes {
		return fmt.Errorf("embedding item content: %w", ErrBudget)
	}
	if i.ContentHash != "" && !isSHA256(i.ContentHash) {
		return fmt.Errorf("embedding item hash: %w", ErrConfiguration)
	}
	if i.ContentHash != "" {
		digest := sha256.Sum256([]byte(i.Content))
		if hex.EncodeToString(digest[:]) != i.ContentHash {
			return fmt.Errorf("embedding item hash: %w", ErrConfiguration)
		}
	}
	return nil
}

// EmbeddingRequest is the provider-independent batch request.
type EmbeddingRequest struct {
	ExecutionID string           `json:"execution_id"`
	RequestID   string           `json:"request_id"`
	Deadline    time.Time        `json:"deadline"`
	Profile     EmbeddingProfile `json:"profile"`
	Items       []EmbeddingItem  `json:"items"`
}

// Validate checks request identity, explicit deadline, profile and batch
// bounds. Deadline expiry is an execution-time timeout, not a malformed
// request; the zero value and timestamps that cannot be serialized are
// configuration errors.
func (r EmbeddingRequest) Validate() error {
	if err := validateRequestIdentity(r.ExecutionID, r.RequestID); err != nil {
		return err
	}
	if err := validateDeadline(r.Deadline); err != nil {
		return err
	}
	if err := r.Profile.Validate(); err != nil {
		return err
	}
	if len(r.Items) == 0 || len(r.Items) > r.Profile.MaxBatchSize {
		return fmt.Errorf("embedding batch: %w", ErrBudget)
	}
	seen := make(map[string]struct{}, len(r.Items))
	totalBytes := 0
	for index, item := range r.Items {
		if err := item.validate(); err != nil {
			return fmt.Errorf("embedding item %d: %w", index, err)
		}
		if totalBytes > maxEmbeddingBatchBytes-len(item.Content) {
			return fmt.Errorf("embedding batch content: %w", ErrBudget)
		}
		totalBytes += len(item.Content)
		if _, exists := seen[item.ID]; exists {
			return fmt.Errorf("embedding batch: %w", ErrConfiguration)
		}
		seen[item.ID] = struct{}{}
	}
	return nil
}

// AuthorizedEvidence is the minimal evidence representation allowed into a
// generation package. The content has already passed the independent
// transfer policy; generators must not gain any source or database access.
type AuthorizedEvidence struct {
	ID            string `json:"id"`
	Content       string `json:"content,omitempty"`
	ContentDigest string `json:"content_digest"`
	Locator       string `json:"locator,omitempty"`
}

func (e AuthorizedEvidence) validate() error {
	if err := validateIdentifier(e.ID, maxIdentifierBytes); err != nil {
		return fmt.Errorf("package evidence id: %w", err)
	}
	if len(e.Content) > maxEvidenceContentBytes {
		return fmt.Errorf("package evidence content: %w", ErrBudget)
	}
	if !isSHA256(e.ContentDigest) {
		return fmt.Errorf("package evidence digest: %w", ErrConfiguration)
	}
	if e.Content != "" {
		digest := sha256.Sum256([]byte(e.Content))
		if hex.EncodeToString(digest[:]) != e.ContentDigest {
			return fmt.Errorf("package evidence digest: %w", ErrConfiguration)
		}
	}
	if len(e.Locator) > maxIdentifierBytes {
		return fmt.Errorf("package evidence locator: %w", ErrConfiguration)
	}
	return nil
}

// EvidencePackage is the only organizational input accepted by Generator.
// Digest and IDs make the package auditable without granting source or DB
// access to a provider.
type EvidencePackage struct {
	ID       string               `json:"id"`
	Digest   string               `json:"digest"`
	Evidence []AuthorizedEvidence `json:"evidence"`
	Gaps     []string             `json:"gaps,omitempty"`
}

// Validate checks package bounds and unique evidence identities.
func (p EvidencePackage) Validate() error {
	if err := validateIdentifier(p.ID, maxIdentifierBytes); err != nil {
		return fmt.Errorf("evidence package id: %w", err)
	}
	if !isSHA256(p.Digest) {
		return fmt.Errorf("evidence package digest: %w", ErrConfiguration)
	}
	if len(p.Evidence) > maxGenerationEvidenceID {
		return fmt.Errorf("evidence package items: %w", ErrBudget)
	}
	seen := make(map[string]struct{}, len(p.Evidence))
	totalBytes := 0
	for index, item := range p.Evidence {
		if err := item.validate(); err != nil {
			return fmt.Errorf("evidence package item %d: %w", index, err)
		}
		if totalBytes > maxGenerationPackageBytes-len(item.Content) {
			return fmt.Errorf("evidence package content: %w", ErrBudget)
		}
		totalBytes += len(item.Content)
		if _, exists := seen[item.ID]; exists {
			return fmt.Errorf("evidence package: %w", ErrConfiguration)
		}
		seen[item.ID] = struct{}{}
	}
	if len(p.Gaps) > maxGenerationGapCount {
		return fmt.Errorf("evidence package gaps: %w", ErrBudget)
	}
	for _, gap := range p.Gaps {
		if err := validateIdentifier(gap, maxIdentifierBytes); err != nil {
			return fmt.Errorf("evidence package gap: %w", err)
		}
	}
	return nil
}

// GenerationRequest is a question plus a bounded, already-authorized package.
type GenerationRequest struct {
	ExecutionID string            `json:"execution_id"`
	RequestID   string            `json:"request_id"`
	Deadline    time.Time         `json:"deadline"`
	Profile     GenerationProfile `json:"profile"`
	Question    string            `json:"question"`
	Package     EvidencePackage   `json:"package"`
}

// Validate checks request, explicit deadline, question and package bounds
// without echoing their contents.
func (r GenerationRequest) Validate() error {
	if err := validateRequestIdentity(r.ExecutionID, r.RequestID); err != nil {
		return err
	}
	if err := validateDeadline(r.Deadline); err != nil {
		return err
	}
	if err := r.Profile.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(r.Question) == "" {
		return fmt.Errorf("generation question: %w", ErrConfiguration)
	}
	if len(r.Question) > maxQuestionBytes {
		return fmt.Errorf("generation question: %w", ErrBudget)
	}
	if err := r.Package.Validate(); err != nil {
		return err
	}
	return nil
}

// Usage records normalized token and item counters. Zero is valid when a
// provider does not expose token accounting; negative values are invalid.
type Usage struct {
	InputItems   int `json:"input_items"`
	OutputItems  int `json:"output_items"`
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

func (u Usage) validate() error {
	if u.InputItems < 0 || u.OutputItems < 0 || u.InputTokens < 0 || u.OutputTokens < 0 {
		return fmt.Errorf("usage: %w", ErrInvalidResponse)
	}
	return nil
}

// Termination records how a provider operation ended.
type Termination string

const (
	TerminationUnknown   Termination = ""
	TerminationCompleted Termination = "completed"
	TerminationPartial   Termination = "partial"
	TerminationAbstained Termination = "abstained"
)

func (t Termination) validate() error {
	switch t {
	case TerminationCompleted, TerminationPartial, TerminationAbstained:
		return nil
	default:
		return fmt.Errorf("termination: %w", ErrInvalidResponse)
	}
}

// Metadata is a bounded map for non-secret audit fields only. Credentials,
// prompts, raw content and obvious credential-shaped values are rejected. It
// is copied on input and output so callers cannot mutate an in-flight result.
type Metadata map[string]string

func validateMetadata(metadata Metadata) error {
	if len(metadata) > maxMetadataEntries {
		return fmt.Errorf("metadata: %w", ErrBudget)
	}
	for key, value := range metadata {
		if err := validateIdentifier(key, maxMetadataKeyBytes); err != nil {
			return fmt.Errorf("metadata key: %w", err)
		}
		if forbiddenMetadataKey(key) {
			return fmt.Errorf("metadata key: %w", ErrConfiguration)
		}
		if len(value) > maxMetadataValueBytes || containsControl(value) || containsCredentialPattern(value) {
			return fmt.Errorf("metadata value: %w", ErrConfiguration)
		}
	}
	return nil
}

func forbiddenMetadataKey(key string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(key, "-", "_"), ".", "_"))
	switch normalized {
	case "api_key", "apikey", "authorization", "access_token", "refresh_token",
		"client_secret", "clientsecret", "token", "password", "passwd", "secret",
		"credential", "prompt", "question":
		return true
	default:
		return false
	}
}

func containsCredentialPattern(value string) bool {
	lower := strings.ToLower(value)
	for _, pattern := range []string{
		"api_key=", "api-key=", "apikey=", "api_key:", "api-key:", "apikey:",
		"authorization=", "authorization:", "bearer ", "access_token=", "access-token=",
		"refresh_token=", "refresh-token=", "client_secret=", "client-secret=",
		"password=", "password:", "passwd=", "passwd:", "secret=", "secret:",
		"token=", "token:", "-----begin private key-----", "-----begin rsa private key-----",
		"-----begin openssh private key-----",
	} {
		if strings.Contains(lower, pattern) {
			return true
		}
	}
	return false
}

func cloneMetadata(metadata Metadata) Metadata {
	if len(metadata) == 0 {
		return nil
	}
	clone := make(Metadata, len(metadata))
	for key, value := range metadata {
		clone[key] = value
	}
	return clone
}

// EmbeddingResult is the normalized result of one embedding batch.
type EmbeddingResult struct {
	ExecutionID string        `json:"execution_id"`
	RequestID   string        `json:"request_id"`
	Provider    Provider      `json:"provider"`
	Model       string        `json:"model"`
	Vectors     [][]float64   `json:"vectors"`
	Usage       Usage         `json:"usage"`
	Latency     time.Duration `json:"latency"`
	Termination Termination   `json:"termination"`
	Metadata    Metadata      `json:"metadata,omitempty"`
}

// Validate checks result shape against its request.
func (r EmbeddingResult) Validate(request EmbeddingRequest) error {
	if r.ExecutionID != request.ExecutionID || r.RequestID != request.RequestID || r.Provider != request.Profile.Provider {
		return fmt.Errorf("embedding result identity: %w", ErrInvalidResponse)
	}
	if err := validateIdentifier(r.Model, maxIdentifierBytes); err != nil {
		return fmt.Errorf("embedding result model: %w", ErrInvalidResponse)
	}
	if len(r.Vectors) != len(request.Items) {
		return fmt.Errorf("embedding result items: %w", ErrInvalidResponse)
	}
	for _, vector := range r.Vectors {
		if len(vector) != request.Profile.Dimension {
			return fmt.Errorf("embedding result dimension: %w", ErrInvalidResponse)
		}
		for _, value := range vector {
			if math.IsNaN(value) || math.IsInf(value, 0) {
				return fmt.Errorf("embedding result values: %w", ErrInvalidResponse)
			}
		}
	}
	if err := r.Usage.validate(); err != nil {
		return err
	}
	if r.Usage.InputItems != len(request.Items) || r.Usage.OutputItems != len(r.Vectors) {
		return fmt.Errorf("embedding result usage: %w", ErrInvalidResponse)
	}
	if r.Latency < 0 || r.Termination != TerminationCompleted {
		return fmt.Errorf("embedding result completion: %w", ErrInvalidResponse)
	}
	if err := validateMetadata(r.Metadata); err != nil {
		return err
	}
	return nil
}

// GenerationEnvelope is the neutral structured result for the core. It does
// not define persisted claims; later query work may extend it independently.
type GenerationEnvelope struct {
	Version       string   `json:"version"`
	Text          string   `json:"text"`
	PackageDigest string   `json:"package_digest"`
	EvidenceIDs   []string `json:"evidence_ids,omitempty"`
	Gaps          []string `json:"gaps,omitempty"`
}

func (e GenerationEnvelope) validate(request GenerationRequest) error {
	if e.Version != GenerationEnvelopeVersion || len(e.Text) > request.Profile.MaxOutputBytes || !isSHA256(e.PackageDigest) || e.PackageDigest != request.Package.Digest {
		return fmt.Errorf("generation envelope: %w", ErrInvalidResponse)
	}
	if len(e.EvidenceIDs) > maxGenerationEvidenceID || len(e.Gaps) > maxGenerationGapCount {
		return fmt.Errorf("generation envelope bounds: %w", ErrInvalidResponse)
	}
	known := make(map[string]struct{}, len(request.Package.Evidence))
	for _, item := range request.Package.Evidence {
		known[item.ID] = struct{}{}
	}
	seen := make(map[string]struct{}, len(e.EvidenceIDs))
	for _, id := range e.EvidenceIDs {
		if err := validateIdentifier(id, maxIdentifierBytes); err != nil {
			return fmt.Errorf("generation envelope evidence: %w", ErrInvalidResponse)
		}
		if _, exists := known[id]; !exists {
			return fmt.Errorf("generation envelope evidence: %w", ErrInvalidResponse)
		}
		if _, duplicate := seen[id]; duplicate {
			return fmt.Errorf("generation envelope evidence: %w", ErrInvalidResponse)
		}
		seen[id] = struct{}{}
	}
	for _, gap := range e.Gaps {
		if err := validateIdentifier(gap, maxIdentifierBytes); err != nil {
			return fmt.Errorf("generation envelope gap: %w", ErrInvalidResponse)
		}
	}
	return nil
}

// GenerationResult is the normalized result of one generation request.
type GenerationResult struct {
	ExecutionID string             `json:"execution_id"`
	RequestID   string             `json:"request_id"`
	Provider    Provider           `json:"provider"`
	Model       string             `json:"model"`
	Output      GenerationEnvelope `json:"output"`
	Usage       Usage              `json:"usage"`
	Latency     time.Duration      `json:"latency"`
	Termination Termination        `json:"termination"`
	Metadata    Metadata           `json:"metadata,omitempty"`
}

// Validate checks result identity, envelope support and usage.
func (r GenerationResult) Validate(request GenerationRequest) error {
	if r.ExecutionID != request.ExecutionID || r.RequestID != request.RequestID || r.Provider != request.Profile.Provider {
		return fmt.Errorf("generation result identity: %w", ErrInvalidResponse)
	}
	if err := validateIdentifier(r.Model, maxIdentifierBytes); err != nil {
		return fmt.Errorf("generation result model: %w", ErrInvalidResponse)
	}
	if err := r.Output.validate(request); err != nil {
		return err
	}
	if err := r.Usage.validate(); err != nil {
		return err
	}
	if r.Usage.InputItems != 1 {
		return fmt.Errorf("generation result usage: %w", ErrInvalidResponse)
	}
	if r.Termination == TerminationAbstained {
		if r.Usage.OutputItems != 0 {
			return fmt.Errorf("generation result usage: %w", ErrInvalidResponse)
		}
	} else if r.Usage.OutputItems != 1 {
		return fmt.Errorf("generation result usage: %w", ErrInvalidResponse)
	}
	if r.Latency < 0 || r.Termination.validate() != nil {
		return fmt.Errorf("generation result completion: %w", ErrInvalidResponse)
	}
	if err := validateMetadata(r.Metadata); err != nil {
		return err
	}
	return nil
}

// Embedder is the independent embedding capability port.
type Embedder interface {
	Embed(ctx context.Context, request EmbeddingRequest) (EmbeddingResult, error)
}

// Generator is the independent generation capability port.
type Generator interface {
	Generate(ctx context.Context, request GenerationRequest) (GenerationResult, error)
}

func validateProvider(provider Provider) error {
	switch provider {
	case ProviderSimulated, ProviderOpenAI, ProviderOpenAICompatible, ProviderOpenRouter:
		return nil
	default:
		return fmt.Errorf("provider: %w", ErrConfiguration)
	}
}

func validateRequestIdentity(executionID, requestID string) error {
	if err := validateIdentifier(executionID, maxIdentifierBytes); err != nil {
		return fmt.Errorf("execution id: %w", err)
	}
	if err := validateIdentifier(requestID, maxIdentifierBytes); err != nil {
		return fmt.Errorf("request id: %w", err)
	}
	return nil
}

func validateDeadline(deadline time.Time) error {
	if deadline.IsZero() {
		return fmt.Errorf("deadline: %w", ErrConfiguration)
	}
	if _, err := deadline.MarshalJSON(); err != nil {
		return fmt.Errorf("deadline: %w", ErrConfiguration)
	}
	return nil
}

func validateIdentifier(value string, maxBytes int) error {
	if strings.TrimSpace(value) == "" || len(value) > maxBytes || containsControl(value) || strings.IndexFunc(value, unicode.IsSpace) >= 0 {
		return fmt.Errorf("identifier: %w", ErrConfiguration)
	}
	return nil
}

func containsControl(value string) bool {
	for _, r := range value {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

func isSHA256(value string) bool {
	if len(value) != sha256.Size*2 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validErrorKind(kind ErrorKind) bool {
	switch kind {
	case ErrorKindAuthentication, ErrorKindConfiguration, ErrorKindCapability,
		ErrorKindBudget, ErrorKindRateLimit, ErrorKindUnavailable,
		ErrorKindTimeout, ErrorKindCancelled, ErrorKindContentBlocked,
		ErrorKindInvalidResponse:
		return true
	default:
		return false
	}
}

func waitSimulation(ctx context.Context, deadline time.Time, capability Capability, delay time.Duration) error {
	if ctx == nil {
		return NewGatewayError(ErrorKindConfiguration, capability, nil)
	}
	if err := contextError(ctx, deadline, capability); err != nil {
		return err
	}
	if delay <= 0 {
		return nil
	}
	operationTimer := time.NewTimer(delay)
	defer operationTimer.Stop()
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return NewGatewayError(ErrorKindTimeout, capability, context.DeadlineExceeded)
	}
	var deadlineTimer *time.Timer
	var deadlineChannel <-chan time.Time
	deadlineTimer = time.NewTimer(remaining)
	defer deadlineTimer.Stop()
	deadlineChannel = deadlineTimer.C
	select {
	case <-ctx.Done():
		return NormalizeError(capability, ctx.Err())
	case <-deadlineChannel:
		return NewGatewayError(ErrorKindTimeout, capability, context.DeadlineExceeded)
	case <-operationTimer.C:
		return contextError(ctx, deadline, capability)
	}
}
