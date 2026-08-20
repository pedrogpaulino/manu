package aigateway

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"time"
)

// SimulatedEmbedderConfig describes a deterministic, network-free embedding
// provider. Failure is an injected normalized error for contract tests.
type SimulatedEmbedderConfig struct {
	Profile  EmbeddingProfile
	Latency  time.Duration
	Failure  ErrorKind
	Metadata Metadata
}

// SimulatedEmbedder implements Embedder with fixed-dimension vectors derived
// from the authorized item identity. It never emits item content.
type SimulatedEmbedder struct {
	profile  EmbeddingProfile
	latency  time.Duration
	failure  ErrorKind
	metadata Metadata
}

var _ Embedder = (*SimulatedEmbedder)(nil)

// NewSimulatedEmbedder constructs a deterministic embedding provider.
func NewSimulatedEmbedder(config SimulatedEmbedderConfig) (*SimulatedEmbedder, error) {
	if err := config.Profile.Validate(); err != nil {
		return nil, capabilityError(CapabilityEmbedding, err)
	}
	if config.Profile.Provider != ProviderSimulated {
		return nil, NewGatewayError(ErrorKindCapability, CapabilityEmbedding, nil)
	}
	if err := validateSimulationOptions(config.Latency, config.Failure, config.Metadata); err != nil {
		return nil, capabilityError(CapabilityEmbedding, err)
	}
	failure := config.Failure
	if failure == "" {
		failure = ErrorKindUnknown
	}
	return &SimulatedEmbedder{
		profile:  config.Profile,
		latency:  config.Latency,
		failure:  failure,
		metadata: cloneMetadata(config.Metadata),
	}, nil
}

// Embed returns one deterministic vector for each authorized item.
func (s *SimulatedEmbedder) Embed(ctx context.Context, request EmbeddingRequest) (EmbeddingResult, error) {
	if s == nil {
		return EmbeddingResult{}, NewGatewayError(ErrorKindConfiguration, CapabilityEmbedding, nil)
	}
	started := time.Now()
	if ctx == nil {
		return EmbeddingResult{}, NewGatewayError(ErrorKindConfiguration, CapabilityEmbedding, nil)
	}
	if err := request.Validate(); err != nil {
		return EmbeddingResult{}, capabilityError(CapabilityEmbedding, err)
	}
	if request.Profile != s.profile {
		return EmbeddingResult{}, NewGatewayError(ErrorKindCapability, CapabilityEmbedding, nil)
	}
	if err := contextError(ctx, request.Deadline, CapabilityEmbedding); err != nil {
		return EmbeddingResult{}, err
	}
	if err := waitSimulation(ctx, request.Deadline, CapabilityEmbedding, s.latency); err != nil {
		return EmbeddingResult{}, err
	}
	if s.failure != ErrorKindUnknown {
		return EmbeddingResult{}, NewGatewayError(s.failure, CapabilityEmbedding, nil)
	}

	vectors := make([][]float64, len(request.Items))
	inputTokens := 0
	for index, item := range request.Items {
		if err := contextError(ctx, request.Deadline, CapabilityEmbedding); err != nil {
			return EmbeddingResult{}, err
		}
		vectors[index] = simulatedVector(s.profile, item)
		inputTokens += tokenEstimate(item.Content)
	}
	if err := contextError(ctx, request.Deadline, CapabilityEmbedding); err != nil {
		return EmbeddingResult{}, err
	}
	result := EmbeddingResult{
		ExecutionID: request.ExecutionID,
		RequestID:   request.RequestID,
		Provider:    s.profile.Provider,
		Model:       s.profile.Model,
		Vectors:     vectors,
		Usage: Usage{
			InputItems:  len(request.Items),
			OutputItems: len(vectors),
			InputTokens: inputTokens,
		},
		Latency:     time.Since(started),
		Termination: TerminationCompleted,
		Metadata:    cloneMetadata(s.metadata),
	}
	if err := result.Validate(request); err != nil {
		return EmbeddingResult{}, NewGatewayError(ErrorKindInvalidResponse, CapabilityEmbedding, err)
	}
	return result, nil
}

// SimulatedGeneratorConfig describes a deterministic, network-free generator.
// Fixture is an envelope template; its package digest is bound to each
// request at execution time and its evidence IDs are checked against that
// request's package.
type SimulatedGeneratorConfig struct {
	Profile  GenerationProfile
	Fixture  GenerationEnvelope
	Latency  time.Duration
	Failure  ErrorKind
	Metadata Metadata
}

// SimulatedGenerator implements Generator without source, database, tool or
// network access. Only package IDs and digest are used to bind its output.
type SimulatedGenerator struct {
	profile  GenerationProfile
	fixture  GenerationEnvelope
	latency  time.Duration
	failure  ErrorKind
	metadata Metadata
}

var _ Generator = (*SimulatedGenerator)(nil)

// NewSimulatedGenerator constructs a deterministic generation provider.
func NewSimulatedGenerator(config SimulatedGeneratorConfig) (*SimulatedGenerator, error) {
	if err := config.Profile.Validate(); err != nil {
		return nil, capabilityError(CapabilityGeneration, err)
	}
	if config.Profile.Provider != ProviderSimulated {
		return nil, NewGatewayError(ErrorKindCapability, CapabilityGeneration, nil)
	}
	if err := validateSimulationOptions(config.Latency, config.Failure, config.Metadata); err != nil {
		return nil, capabilityError(CapabilityGeneration, err)
	}
	failure := config.Failure
	if failure == "" {
		failure = ErrorKindUnknown
	}
	fixture := cloneEnvelope(config.Fixture)
	if fixture.Version == "" {
		fixture.Version = GenerationEnvelopeVersion
	}
	if err := validateFixture(fixture, config.Profile.MaxOutputBytes); err != nil {
		return nil, capabilityError(CapabilityGeneration, err)
	}
	return &SimulatedGenerator{
		profile:  config.Profile,
		fixture:  fixture,
		latency:  config.Latency,
		failure:  failure,
		metadata: cloneMetadata(config.Metadata),
	}, nil
}

// Generate returns the configured neutral envelope bound to the authorized
// package. An empty package is handled as deterministic abstinence and does
// not invoke the configured fixture or simulated failure.
func (s *SimulatedGenerator) Generate(ctx context.Context, request GenerationRequest) (GenerationResult, error) {
	if s == nil {
		return GenerationResult{}, NewGatewayError(ErrorKindConfiguration, CapabilityGeneration, nil)
	}
	started := time.Now()
	if ctx == nil {
		return GenerationResult{}, NewGatewayError(ErrorKindConfiguration, CapabilityGeneration, nil)
	}
	if err := request.Validate(); err != nil {
		return GenerationResult{}, capabilityError(CapabilityGeneration, err)
	}
	if request.Profile != s.profile {
		return GenerationResult{}, NewGatewayError(ErrorKindCapability, CapabilityGeneration, nil)
	}
	if err := contextError(ctx, request.Deadline, CapabilityGeneration); err != nil {
		return GenerationResult{}, err
	}
	if len(request.Package.Evidence) == 0 {
		output := GenerationEnvelope{
			Version:       GenerationEnvelopeVersion,
			PackageDigest: request.Package.Digest,
			Gaps:          append([]string(nil), request.Package.Gaps...),
		}
		if len(output.Gaps) == 0 {
			output.Gaps = []string{"no_transferable_evidence"}
		}
		if err := contextError(ctx, request.Deadline, CapabilityGeneration); err != nil {
			return GenerationResult{}, err
		}
		result := GenerationResult{
			ExecutionID: request.ExecutionID,
			RequestID:   request.RequestID,
			Provider:    s.profile.Provider,
			Model:       s.profile.Model,
			Output:      output,
			Usage: Usage{
				InputItems:  1,
				InputTokens: tokenEstimate(request.Question),
			},
			Latency:     time.Since(started),
			Termination: TerminationAbstained,
			Metadata:    cloneMetadata(s.metadata),
		}
		if err := result.Validate(request); err != nil {
			return GenerationResult{}, NewGatewayError(ErrorKindInvalidResponse, CapabilityGeneration, err)
		}
		return result, nil
	}
	if err := waitSimulation(ctx, request.Deadline, CapabilityGeneration, s.latency); err != nil {
		return GenerationResult{}, err
	}
	if s.failure != ErrorKindUnknown {
		return GenerationResult{}, NewGatewayError(s.failure, CapabilityGeneration, nil)
	}
	if err := contextError(ctx, request.Deadline, CapabilityGeneration); err != nil {
		return GenerationResult{}, err
	}

	output := cloneEnvelope(s.fixture)
	output.PackageDigest = request.Package.Digest
	if s.fixture.PackageDigest != "" && s.fixture.PackageDigest != request.Package.Digest {
		return GenerationResult{}, NewGatewayError(ErrorKindInvalidResponse, CapabilityGeneration, nil)
	}
	result := GenerationResult{
		ExecutionID: request.ExecutionID,
		RequestID:   request.RequestID,
		Provider:    s.profile.Provider,
		Model:       s.profile.Model,
		Output:      output,
		Usage: Usage{
			InputItems:   1,
			OutputItems:  1,
			InputTokens:  tokenEstimate(request.Question),
			OutputTokens: tokenEstimate(output.Text),
		},
		Latency:     time.Since(started),
		Termination: TerminationCompleted,
		Metadata:    cloneMetadata(s.metadata),
	}
	if err := result.Validate(request); err != nil {
		return GenerationResult{}, NewGatewayError(ErrorKindInvalidResponse, CapabilityGeneration, err)
	}
	return result, nil
}

func capabilityError(capability Capability, err error) error {
	if err == nil {
		return nil
	}
	var gatewayErr *GatewayError
	if errors.As(err, &gatewayErr) && validErrorKind(gatewayErr.Kind) {
		return NewGatewayError(gatewayErr.Kind, capability, err)
	}
	return NewGatewayError(ErrorKindInvalidResponse, capability, err)
}

func contextError(ctx context.Context, deadline time.Time, capability Capability) error {
	if err := ctx.Err(); err != nil {
		return NormalizeError(capability, err)
	}
	if deadline.IsZero() {
		return NewGatewayError(ErrorKindConfiguration, capability, nil)
	}
	if !time.Now().Before(deadline) {
		return NewGatewayError(ErrorKindTimeout, capability, context.DeadlineExceeded)
	}
	return nil
}

func validateSimulationOptions(latency time.Duration, failure ErrorKind, metadata Metadata) error {
	if latency < 0 || latency > maxSimulationLatency {
		return fmt.Errorf("simulated latency: %w", ErrConfiguration)
	}
	if failure != "" && failure != ErrorKindUnknown && !validErrorKind(failure) {
		return fmt.Errorf("simulated failure: %w", ErrConfiguration)
	}
	if err := validateMetadata(metadata); err != nil {
		return err
	}
	return nil
}

func validateFixture(fixture GenerationEnvelope, maxOutputBytes int) error {
	if fixture.Version != GenerationEnvelopeVersion {
		return fmt.Errorf("generation fixture version: %w", ErrConfiguration)
	}
	if len(fixture.Text) > maxOutputBytes {
		return fmt.Errorf("generation fixture text: %w", ErrBudget)
	}
	if fixture.PackageDigest != "" && !isSHA256(fixture.PackageDigest) {
		return fmt.Errorf("generation fixture digest: %w", ErrConfiguration)
	}
	if len(fixture.EvidenceIDs) > maxGenerationEvidenceID || len(fixture.Gaps) > maxGenerationGapCount {
		return fmt.Errorf("generation fixture bounds: %w", ErrBudget)
	}
	seen := make(map[string]struct{}, len(fixture.EvidenceIDs))
	for _, id := range fixture.EvidenceIDs {
		if err := validateIdentifier(id, maxIdentifierBytes); err != nil {
			return fmt.Errorf("generation fixture evidence: %w", err)
		}
		if _, exists := seen[id]; exists {
			return fmt.Errorf("generation fixture evidence: %w", ErrConfiguration)
		}
		seen[id] = struct{}{}
	}
	for _, gap := range fixture.Gaps {
		if err := validateIdentifier(gap, maxIdentifierBytes); err != nil {
			return fmt.Errorf("generation fixture gap: %w", err)
		}
	}
	return nil
}

func cloneEnvelope(envelope GenerationEnvelope) GenerationEnvelope {
	envelope.EvidenceIDs = append([]string(nil), envelope.EvidenceIDs...)
	envelope.Gaps = append([]string(nil), envelope.Gaps...)
	return envelope
}

func simulatedVector(profile EmbeddingProfile, item EmbeddingItem) []float64 {
	contentHash := item.ContentHash
	if contentHash == "" {
		digest := sha256.Sum256([]byte(item.Content))
		contentHash = fmt.Sprintf("%x", digest[:])
	}
	seed := make([]byte, 0, len(profile.Version)+len(profile.Model)+len(contentHash)+32)
	seed = append(seed, profile.Version...)
	seed = append(seed, 0)
	seed = append(seed, profile.Provider...)
	seed = append(seed, 0)
	seed = append(seed, profile.Model...)
	seed = append(seed, 0)
	seed = append(seed, contentHash...)

	vector := make([]float64, profile.Dimension)
	for index := range vector {
		hash := sha256.New()
		_, _ = hash.Write(seed)
		var encoded [8]byte
		binary.BigEndian.PutUint64(encoded[:], uint64(index))
		_, _ = hash.Write(encoded[:])
		digest := hash.Sum(nil)
		raw := binary.BigEndian.Uint64(digest[:8])
		vector[index] = (float64(raw)/float64(^uint64(0)))*2 - 1
	}
	if profile.Normalize {
		norm := 0.0
		for _, value := range vector {
			norm += value * value
		}
		norm = math.Sqrt(norm)
		if norm > 0 {
			for index, value := range vector {
				vector[index] = value / norm
			}
		}
	}
	return vector
}

func tokenEstimate(value string) int {
	if len(value) == 0 {
		return 0
	}
	return (len([]byte(value)) + 3) / 4
}
