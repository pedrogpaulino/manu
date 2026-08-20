package aigateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"
)

func testEmbeddingProfile(model string) EmbeddingProfile {
	return EmbeddingProfile{
		Provider:     ProviderSimulated,
		Model:        model,
		Version:      EmbeddingProfileVersion,
		Dimension:    8,
		Normalize:    true,
		MaxBatchSize: 4,
	}
}

func testGenerationProfile(model string) GenerationProfile {
	return GenerationProfile{
		Provider:       ProviderSimulated,
		Model:          model,
		Version:        GenerationProfileVersion,
		Protocol:       ProtocolResponses,
		MaxOutputBytes: 1024,
	}
}

func testEvidence(content string) AuthorizedEvidence {
	digest := sha256.Sum256([]byte(content))
	return AuthorizedEvidence{
		ID:            "evidence-1",
		Content:       content,
		ContentDigest: hex.EncodeToString(digest[:]),
		Locator:       "artifact-1",
	}
}

func testPackage() EvidencePackage {
	return EvidencePackage{
		ID:       "package-1",
		Digest:   strings.Repeat("a", sha256.Size*2),
		Evidence: []AuthorizedEvidence{testEvidence("database secret")},
	}
}

func testEmbeddingRequest(profile EmbeddingProfile) EmbeddingRequest {
	return EmbeddingRequest{
		ExecutionID: "execution-1",
		RequestID:   "request-1",
		Deadline:    time.Now().Add(time.Hour),
		Profile:     profile,
		Items: []EmbeddingItem{
			{ID: "unit-1", Content: "first authorized unit"},
			{ID: "unit-2", Content: "second authorized unit"},
		},
	}
}

func testGenerationRequest(profile GenerationProfile) GenerationRequest {
	return GenerationRequest{
		ExecutionID: "execution-1",
		RequestID:   "request-1",
		Deadline:    time.Now().Add(time.Hour),
		Profile:     profile,
		Question:    "What is supported?",
		Package:     testPackage(),
	}
}

func TestProfilesAndRequestsValidateLimits(t *testing.T) {
	tests := []struct {
		name    string
		request EmbeddingRequest
		want    error
	}{
		{
			name: "empty batch",
			request: func() EmbeddingRequest {
				r := testEmbeddingRequest(testEmbeddingProfile("embed-v1"))
				r.Items = nil
				return r
			}(),
			want: ErrBudget,
		},
		{
			name: "batch limit",
			request: func() EmbeddingRequest {
				r := testEmbeddingRequest(testEmbeddingProfile("embed-v1"))
				r.Items = append(r.Items, EmbeddingItem{ID: "unit-3", Content: "third"}, EmbeddingItem{ID: "unit-4", Content: "fourth"}, EmbeddingItem{ID: "unit-5", Content: "fifth"})
				return r
			}(),
			want: ErrBudget,
		},
		{
			name: "duplicate item",
			request: func() EmbeddingRequest {
				r := testEmbeddingRequest(testEmbeddingProfile("embed-v1"))
				r.Items[1].ID = r.Items[0].ID
				return r
			}(),
			want: ErrConfiguration,
		},
		{
			name: "content hash mismatch",
			request: func() EmbeddingRequest {
				r := testEmbeddingRequest(testEmbeddingProfile("embed-v1"))
				r.Items[0].ContentHash = strings.Repeat("b", sha256.Size*2)
				return r
			}(),
			want: ErrConfiguration,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.request.Validate(); !errors.Is(err, test.want) {
				t.Fatalf("Validate() error = %v, want %v", err, test.want)
			}
		})
	}

	profile := testEmbeddingProfile("embed-v1")
	profile.Version = "v2"
	if err := profile.Validate(); !errors.Is(err, ErrConfiguration) {
		t.Fatalf("profile version error = %v, want configuration", err)
	}
	if err := (GenerationProfile{Provider: ProviderSimulated, Model: "generate-v1", Version: GenerationProfileVersion, MaxOutputBytes: 10}).Validate(); !errors.Is(err, ErrConfiguration) {
		t.Fatalf("generation protocol error = %v, want configuration", err)
	}

	secret := "prompt secret should never be echoed"
	invalid := testEmbeddingRequest(testEmbeddingProfile("embed-v1"))
	invalid.RequestID = secret + "\n"
	err := invalid.Validate()
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("validation error leaked content: %q", err)
	}

	encoded, err := json.Marshal(invalid)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"deadline"`) {
		t.Fatalf("request JSON omitted explicit deadline: %s", encoded)
	}
	valid := testEmbeddingRequest(testEmbeddingProfile("embed-v1"))
	decoded := EmbeddingRequest{}
	if err := json.Unmarshal(mustJSON(t, valid), &decoded); err != nil {
		t.Fatal(err)
	}
	if err := decoded.Validate(); err != nil {
		t.Fatalf("deadline JSON round trip invalid: %v", err)
	}
	valid.Deadline = time.Time{}
	if err := valid.Validate(); !errors.Is(err, ErrConfiguration) {
		t.Fatalf("zero deadline error = %v, want configuration", err)
	}
	valid.Deadline = time.Date(10_000, time.January, 1, 0, 0, 0, 0, time.UTC)
	if err := valid.Validate(); !errors.Is(err, ErrConfiguration) {
		t.Fatalf("unserializable deadline error = %v, want configuration", err)
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func TestSimulatedEmbedderDeterministicBatchAndShape(t *testing.T) {
	profile := testEmbeddingProfile("embed-v1")
	config := SimulatedEmbedderConfig{
		Profile:  profile,
		Metadata: Metadata{"fixture": "deterministic"},
	}
	first, err := NewSimulatedEmbedder(config)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewSimulatedEmbedder(config)
	if err != nil {
		t.Fatal(err)
	}
	request := testEmbeddingRequest(profile)
	gotFirst, err := first.Embed(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	gotSecond, err := second.Embed(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotFirst.Vectors, gotSecond.Vectors) {
		t.Fatal("simulated vectors are not deterministic")
	}
	if gotFirst.Provider != ProviderSimulated || gotFirst.Model != profile.Model {
		t.Fatalf("result identity = %s/%s", gotFirst.Provider, gotFirst.Model)
	}
	if gotFirst.Usage.InputItems != len(request.Items) || gotFirst.Usage.OutputItems != len(request.Items) || gotFirst.Usage.InputTokens <= 0 {
		t.Fatalf("unexpected usage: %+v", gotFirst.Usage)
	}
	if gotFirst.Latency < 0 || gotFirst.Termination != TerminationCompleted {
		t.Fatalf("unexpected completion: %s/%s", gotFirst.Latency, gotFirst.Termination)
	}
	for _, vector := range gotFirst.Vectors {
		if len(vector) != profile.Dimension {
			t.Fatalf("vector dimension = %d, want %d", len(vector), profile.Dimension)
		}
		norm := 0.0
		for _, value := range vector {
			if math.IsNaN(value) || math.IsInf(value, 0) {
				t.Fatal("simulated vector contains a non-finite value")
			}
			norm += value * value
		}
		if math.Abs(math.Sqrt(norm)-1) > 1e-12 {
			t.Fatalf("normalized vector norm = %v", math.Sqrt(norm))
		}
	}

	encoded, err := json.Marshal(gotFirst)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "authorized unit") {
		t.Fatalf("embedding result contains source content: %s", encoded)
	}
	request.Items[0].Content = "changed content"
	request.Items[0].ContentHash = ""
	changed, err := first.Embed(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if reflect.DeepEqual(gotFirst.Vectors[0], changed.Vectors[0]) {
		t.Fatal("content changes did not change the simulated vector")
	}
}

func TestSimulatedGeneratorBindsFixtureToAuthorizedPackage(t *testing.T) {
	profile := testGenerationProfile("generate-v1")
	config := SimulatedGeneratorConfig{
		Profile: profile,
		Fixture: GenerationEnvelope{
			Text:        "fixture answer",
			EvidenceIDs: []string{"evidence-1"},
			Gaps:        []string{"coverage-gap"},
		},
		Metadata: Metadata{"fixture": "deterministic"},
	}
	generator, err := NewSimulatedGenerator(config)
	if err != nil {
		t.Fatal(err)
	}
	request := testGenerationRequest(profile)
	first, err := generator.Generate(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := generator.Generate(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first.Output, second.Output) {
		t.Fatalf("fixture output is not deterministic: %#v != %#v", first.Output, second.Output)
	}
	if first.Provider != ProviderSimulated || first.Model != profile.Model || first.Output.PackageDigest != request.Package.Digest {
		t.Fatalf("unexpected generation identity: %+v", first)
	}
	if first.Output.Text != "fixture answer" || !reflect.DeepEqual(first.Output.EvidenceIDs, []string{"evidence-1"}) {
		t.Fatalf("unexpected fixture output: %+v", first.Output)
	}
	if first.Usage.InputItems != 1 || first.Usage.OutputItems != 1 || first.Usage.InputTokens == 0 || first.Usage.OutputTokens == 0 {
		t.Fatalf("unexpected generation usage: %+v", first.Usage)
	}
	if first.Termination != TerminationCompleted || first.Latency < 0 {
		t.Fatalf("unexpected generation completion: %s/%s", first.Latency, first.Termination)
	}
	encoded, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "database secret") {
		t.Fatalf("generation result contains source content: %s", encoded)
	}

	empty := request
	empty.Package = EvidencePackage{ID: "empty-package", Digest: strings.Repeat("c", sha256.Size*2)}
	abstained, err := generator.Generate(context.Background(), empty)
	if err != nil {
		t.Fatal(err)
	}
	if abstained.Termination != TerminationAbstained || abstained.Usage.OutputItems != 0 || abstained.Output.Text != "" {
		t.Fatalf("unexpected abstention: %+v", abstained)
	}
}

func TestSimulatedCapabilitiesRemainIndependent(t *testing.T) {
	embeddingProfile := testEmbeddingProfile("embedding-model")
	generationProfile := testGenerationProfile("generation-model")
	embedder, err := NewSimulatedEmbedder(SimulatedEmbedderConfig{Profile: embeddingProfile})
	if err != nil {
		t.Fatal(err)
	}
	generator, err := NewSimulatedGenerator(SimulatedGeneratorConfig{Profile: generationProfile, Fixture: GenerationEnvelope{Text: "answer", EvidenceIDs: []string{"evidence-1"}}})
	if err != nil {
		t.Fatal(err)
	}
	var embeddingPort Embedder = embedder
	var generationPort Generator = generator
	embeddingResult, err := embeddingPort.Embed(context.Background(), testEmbeddingRequest(embeddingProfile))
	if err != nil {
		t.Fatal(err)
	}
	generationResult, err := generationPort.Generate(context.Background(), testGenerationRequest(generationProfile))
	if err != nil {
		t.Fatal(err)
	}
	if embeddingResult.Model != embeddingProfile.Model || generationResult.Model != generationProfile.Model {
		t.Fatalf("models crossed capability boundary: %q/%q", embeddingResult.Model, generationResult.Model)
	}
	if embeddingResult.Provider != embeddingProfile.Provider || generationResult.Provider != generationProfile.Provider {
		t.Fatalf("providers crossed capability boundary: %q/%q", embeddingResult.Provider, generationResult.Provider)
	}

	wrongProvider := embeddingProfile
	wrongProvider.Provider = ProviderOpenAI
	if _, err := NewSimulatedEmbedder(SimulatedEmbedderConfig{Profile: wrongProvider}); !errors.Is(err, ErrCapability) {
		t.Fatalf("wrong simulator provider error = %v, want capability", err)
	}
}

func TestSimulatedCancellationAndDeadlineNormalizeErrors(t *testing.T) {
	profile := testEmbeddingProfile("embed-v1")
	embedder, err := NewSimulatedEmbedder(SimulatedEmbedderConfig{Profile: profile, Latency: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	request := testEmbeddingRequest(profile)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = embedder.Embed(cancelled, request)
	if !errors.Is(err, ErrCancelled) || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error = %v, want cancelled and context.Canceled", err)
	}
	var gatewayErr *GatewayError
	if !errors.As(err, &gatewayErr) || gatewayErr.Capability != CapabilityEmbedding {
		t.Fatalf("cancel error capability = %#v", err)
	}

	deadline, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	_, err = embedder.Embed(deadline, request)
	if !errors.Is(err, ErrTimeout) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline error = %v, want timeout and context deadline", err)
	}

	generatorProfile := testGenerationProfile("generate-v1")
	generator, err := NewSimulatedGenerator(SimulatedGeneratorConfig{Profile: generatorProfile, Latency: 50 * time.Millisecond, Fixture: GenerationEnvelope{Text: "answer", EvidenceIDs: []string{"evidence-1"}}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = generator.Generate(cancelled, testGenerationRequest(generatorProfile))
	if !errors.Is(err, ErrCancelled) {
		t.Fatalf("generation cancel error = %v, want cancelled", err)
	}
}

func TestSimulatedRequestDeadlinesUseMostRestrictiveLimit(t *testing.T) {
	profile := testEmbeddingProfile("embed-v1")
	embedder, err := NewSimulatedEmbedder(SimulatedEmbedderConfig{Profile: profile, Latency: 200 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	request := testEmbeddingRequest(profile)
	request.Deadline = time.Now().Add(20 * time.Millisecond)
	_, err = embedder.Embed(context.Background(), request)
	if !errors.Is(err, ErrTimeout) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("request deadline error = %v, want timeout and deadline exceeded", err)
	}

	request = testEmbeddingRequest(profile)
	request.Deadline = time.Now().Add(time.Second)
	contextDeadline, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err = embedder.Embed(contextDeadline, request)
	if !errors.Is(err, ErrTimeout) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("context deadline error = %v, want timeout and deadline exceeded", err)
	}

	generationProfile := testGenerationProfile("generate-v1")
	generator, err := NewSimulatedGenerator(SimulatedGeneratorConfig{
		Profile: generationProfile,
		Latency: 200 * time.Millisecond,
		Fixture: GenerationEnvelope{Text: "answer", EvidenceIDs: []string{"evidence-1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	generationRequest := testGenerationRequest(generationProfile)
	generationRequest.Deadline = time.Now().Add(20 * time.Millisecond)
	_, err = generator.Generate(context.Background(), generationRequest)
	if !errors.Is(err, ErrTimeout) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("generation request deadline error = %v, want timeout and deadline exceeded", err)
	}

	expired := testGenerationRequest(generationProfile)
	expired.Deadline = time.Now().Add(-time.Second)
	_, err = generator.Generate(context.Background(), expired)
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("expired generation deadline error = %v, want timeout", err)
	}
}

func TestSimulatedErrorsAreNormalizedAndSafe(t *testing.T) {
	profile := testEmbeddingProfile("embed-v1")
	request := testEmbeddingRequest(profile)
	secret := "api-key=super-secret prompt body"
	kinds := []ErrorKind{
		ErrorKindAuthentication,
		ErrorKindConfiguration,
		ErrorKindCapability,
		ErrorKindBudget,
		ErrorKindRateLimit,
		ErrorKindUnavailable,
		ErrorKindTimeout,
		ErrorKindCancelled,
		ErrorKindContentBlocked,
		ErrorKindInvalidResponse,
	}
	for _, kind := range kinds {
		t.Run(string(kind), func(t *testing.T) {
			embedder, err := NewSimulatedEmbedder(SimulatedEmbedderConfig{Profile: profile, Failure: kind})
			if err != nil {
				t.Fatal(err)
			}
			got, err := embedder.Embed(context.Background(), request)
			if !errors.Is(err, &GatewayError{Kind: kind}) {
				t.Fatalf("error = %v, want kind %s", err, kind)
			}
			if len(got.Vectors) != 0 || got.ExecutionID != "" || got.RequestID != "" {
				t.Fatalf("failure returned a result: %+v", got)
			}
			if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "secret") {
				t.Fatalf("error leaked secret: %q", err)
			}
		})
	}

	unknown := errors.New(secret)
	err := NormalizeError(CapabilityEmbedding, unknown)
	if !errors.Is(err, ErrInvalidResponse) || strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "api-key") {
		t.Fatalf("normalized unknown error = %v", err)
	}
	wrapped := NewGatewayError(ErrorKindUnavailable, CapabilityGeneration, unknown)
	if !errors.Is(wrapped, ErrUnavailable) || strings.Contains(wrapped.Error(), secret) {
		t.Fatalf("wrapped error = %v", wrapped)
	}
}

func TestResultValidationRejectsInvalidProviderOutput(t *testing.T) {
	request := testEmbeddingRequest(testEmbeddingProfile("embed-v1"))
	invalid := EmbeddingResult{
		ExecutionID: request.ExecutionID,
		RequestID:   request.RequestID,
		Provider:    request.Profile.Provider,
		Model:       request.Profile.Model,
		Vectors:     [][]float64{{math.NaN()}},
		Usage:       Usage{InputItems: len(request.Items), OutputItems: len(request.Items)},
		Termination: TerminationCompleted,
	}
	if err := invalid.Validate(request); !errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("invalid embedding result error = %v", err)
	}

	generationProfile := testGenerationProfile("generate-v1")
	generationRequest := testGenerationRequest(generationProfile)
	invalidGeneration := GenerationResult{
		ExecutionID: generationRequest.ExecutionID,
		RequestID:   generationRequest.RequestID,
		Provider:    generationRequest.Profile.Provider,
		Model:       generationRequest.Profile.Model,
		Output: GenerationEnvelope{
			Version:       GenerationEnvelopeVersion,
			PackageDigest: generationRequest.Package.Digest,
			EvidenceIDs:   []string{"not-in-package"},
		},
		Usage:       Usage{InputItems: 1, OutputItems: 1},
		Termination: TerminationCompleted,
	}
	if err := invalidGeneration.Validate(generationRequest); !errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("invalid generation result error = %v", err)
	}
}

func TestSimulatorOptionsAreBounded(t *testing.T) {
	profile := testEmbeddingProfile("embed-v1")
	metadata := make(Metadata, maxMetadataEntries+1)
	for index := 0; index < maxMetadataEntries+1; index++ {
		metadata["key"+string(rune('a'+index))] = "value"
	}
	if _, err := NewSimulatedEmbedder(SimulatedEmbedderConfig{Profile: profile, Metadata: metadata}); !errors.Is(err, ErrBudget) {
		t.Fatalf("metadata limit error = %v, want budget", err)
	}
	if _, err := NewSimulatedEmbedder(SimulatedEmbedderConfig{Profile: profile, Metadata: Metadata{"api-key": "secret"}}); !errors.Is(err, ErrConfiguration) {
		t.Fatalf("metadata secret-key error = %v, want configuration", err)
	}
	if _, err := NewSimulatedEmbedder(SimulatedEmbedderConfig{Profile: profile, Metadata: Metadata{"provider_note": "Authorization: Bearer secret"}}); !errors.Is(err, ErrConfiguration) || strings.Contains(err.Error(), "secret") {
		t.Fatalf("metadata secret-value error = %v, want safe configuration error", err)
	}
	if _, err := NewSimulatedEmbedder(SimulatedEmbedderConfig{Profile: profile, Latency: -time.Millisecond}); !errors.Is(err, ErrConfiguration) {
		t.Fatalf("negative latency error = %v, want configuration", err)
	}
	if _, err := NewSimulatedEmbedder(SimulatedEmbedderConfig{Profile: profile, Failure: ErrorKind("provider-secret")}); !errors.Is(err, ErrConfiguration) {
		t.Fatalf("failure kind error = %v, want configuration", err)
	}
}
