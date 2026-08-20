package aigateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func openAIEmbeddingProfile() EmbeddingProfile {
	profile := testEmbeddingProfile("text-embedding-3-small")
	profile.Provider = ProviderOpenAI
	profile.Dimension = 2
	return profile
}

func openAIGenerationProfile() GenerationProfile {
	profile := testGenerationProfile("gpt-effective")
	profile.Provider = ProviderOpenAI
	profile.Protocol = ProtocolResponses
	return profile
}

func makeOpenAIEmbeddingRequest() EmbeddingRequest {
	request := testEmbeddingRequest(openAIEmbeddingProfile())
	request.Items = []EmbeddingItem{
		{ID: "unit-a", Content: "first authorized content"},
		{ID: "unit-b", Content: "second authorized content"},
	}
	return request
}

func makeOpenAIGenerationRequest() GenerationRequest {
	return testGenerationRequest(openAIGenerationProfile())
}

func openAIAdapterConfig(server *httptest.Server) OpenAIConfig {
	return OpenAIConfig{
		BaseURL:         server.URL,
		APIKey:          "test-key",
		HTTPClient:      server.Client(),
		MaxOutputTokens: 32,
	}
}

func newOpenAITestServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("loopback HTTP test unavailable: %v", err)
	}
	server := &httptest.Server{Listener: listener, Config: &http.Server{Handler: handler}}
	server.Start()
	return server
}

func writeOpenAIJSON(t *testing.T, writer http.ResponseWriter, value any) {
	t.Helper()
	writer.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(writer).Encode(value); err != nil {
		t.Error(err)
	}
}

func openAIEnvelopeJSON(t *testing.T, request GenerationRequest, text string) string {
	t.Helper()
	encoded, err := json.Marshal(GenerationEnvelope{
		Version:       GenerationEnvelopeVersion,
		Text:          text,
		PackageDigest: request.Package.Digest,
		EvidenceIDs:   []string{"evidence-1"},
		Gaps:          []string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func openAIResponsesJSON(t *testing.T, request GenerationRequest, status, outputText string) map[string]any {
	t.Helper()
	return map[string]any{
		"status": status,
		"model":  "gpt-4o-2025-effective",
		"usage": map[string]any{
			"input_tokens":  7,
			"output_tokens": 3,
			"total_tokens":  10,
		},
		"output": []any{
			map[string]any{
				"type": "message",
				"content": []any{
					map[string]any{"type": "output_text", "text": outputText},
				},
			},
		},
	}
}

func TestOpenAIEmbedShapeHeadersAndIndexedOrder(t *testing.T) {
	var body map[string]any
	var auth, contentType, path string
	done := make(chan struct{})
	server := newOpenAITestServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		defer close(done)
		path = request.URL.Path
		auth = request.Header.Get("Authorization")
		contentType = request.Header.Get("Content-Type")
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		writeOpenAIJSON(t, writer, map[string]any{
			"data": []any{
				map[string]any{"embedding": []float64{3, 4}, "index": 1, "object": "embedding"},
				map[string]any{"embedding": []float64{1, 2}, "index": 0, "object": "embedding"},
			},
			"model": "text-embedding-3-small-2025",
			"usage": map[string]any{"prompt_tokens": 7, "total_tokens": 7},
		})
	}))
	defer server.Close()
	adapter, err := newOpenAIAdapterForTest(openAIAdapterConfig(server))
	if err != nil {
		t.Fatal(err)
	}
	result, err := adapter.Embed(context.Background(), makeOpenAIEmbeddingRequest())
	if err != nil {
		t.Fatal(err)
	}
	<-done
	if path != "/v1/embeddings" || auth != "Bearer test-key" || contentType != "application/json" {
		t.Fatalf("request transport path=%q auth=%q content_type=%q", path, auth, contentType)
	}
	if result.Model != "text-embedding-3-small-2025" || result.Provider != ProviderOpenAI || result.Latency <= 0 {
		t.Fatalf("unexpected embedding result: %+v", result)
	}
	if len(result.Vectors) != 2 || result.Vectors[0][0] != 1 || result.Vectors[1][0] != 3 {
		t.Fatalf("indexed response order = %#v", result.Vectors)
	}
	if result.Usage.InputTokens != 7 || result.Usage.OutputTokens != 0 {
		t.Fatalf("embedding usage = %+v", result.Usage)
	}
	if body["model"] != openAIEmbeddingProfile().Model || body["encoding_format"] != "float" {
		t.Fatalf("embedding request fields = %#v", body)
	}
	if dimensions, ok := body["dimensions"].(float64); !ok || dimensions != 2 {
		t.Fatalf("dimensions = %#v, want 2", body["dimensions"])
	}
	input, ok := body["input"].([]any)
	if !ok || len(input) != 2 || input[0] != "first authorized content" || input[1] != "second authorized content" {
		t.Fatalf("embedding input = %#v", body["input"])
	}
}

func TestOpenAIEmbedSingleInputUsesStringAndRejectsInvalidIndexes(t *testing.T) {
	var input any
	var calls atomic.Int32
	done := make(chan struct{})
	server := newOpenAITestServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		defer close(done)
		calls.Add(1)
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		input = body["input"]
		writeOpenAIJSON(t, writer, map[string]any{
			"data": []any{
				map[string]any{"embedding": []float64{1, 2}, "index": 4, "object": "embedding"},
			},
			"model": "embedding-model",
			"usage": map[string]any{"prompt_tokens": 1, "total_tokens": 1},
		})
	}))
	defer server.Close()
	adapter, err := newOpenAIAdapterForTest(openAIAdapterConfig(server))
	if err != nil {
		t.Fatal(err)
	}
	request := makeOpenAIEmbeddingRequest()
	request.Items = request.Items[:1]
	if _, err := adapter.Embed(context.Background(), request); !errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("invalid index error = %v, want invalid response", err)
	}
	<-done
	if input != "first authorized content" {
		t.Fatalf("single input = %#v, want string", input)
	}
	if calls.Load() != 1 {
		t.Fatalf("server calls = %d, want one", calls.Load())
	}
}

func TestOpenAIRejectsDimensionIncompatibleEmbeddingModelBeforeNetwork(t *testing.T) {
	var calls atomic.Int32
	server := newOpenAITestServer(t, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
	}))
	defer server.Close()
	adapter, err := newOpenAIAdapterForTest(openAIAdapterConfig(server))
	if err != nil {
		t.Fatal(err)
	}
	request := makeOpenAIEmbeddingRequest()
	request.Profile.Model = "text-embedding-ada-002"
	if _, err := adapter.Embed(context.Background(), request); !errors.Is(err, ErrCapability) {
		t.Fatalf("unsupported dimension model error = %v", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("unsupported model reached network: %d calls", calls.Load())
	}
}

func TestOpenAIGenerateStructuredResponsesShape(t *testing.T) {
	request := makeOpenAIGenerationRequest()
	var body map[string]any
	done := make(chan struct{})
	server := newOpenAITestServer(t, http.HandlerFunc(func(writer http.ResponseWriter, incoming *http.Request) {
		defer close(done)
		if incoming.URL.Path != "/v1/responses" {
			t.Errorf("path = %s", incoming.URL.Path)
		}
		if incoming.Header.Get("Authorization") != "Bearer test-key" || incoming.Header.Get("Content-Type") != "application/json" {
			t.Errorf("headers = %#v", incoming.Header)
		}
		if err := json.NewDecoder(incoming.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		writeOpenAIJSON(t, writer, openAIResponsesJSON(t, request, "completed", openAIEnvelopeJSON(t, request, "authorized answer")))
	}))
	defer server.Close()
	config := openAIAdapterConfig(server)
	adapter, err := newOpenAIAdapterForTest(config)
	if err != nil {
		t.Fatal(err)
	}
	result, err := adapter.Generate(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	<-done
	if result.Provider != ProviderOpenAI || result.Model != "gpt-4o-2025-effective" || result.Output.Text != "authorized answer" || result.Usage.InputTokens != 7 || result.Usage.OutputTokens != 3 {
		t.Fatalf("generation result = %+v", result)
	}
	if body["model"] != request.Profile.Model || body["instructions"] != untrustedEvidenceInstruction || body["store"] != false || body["max_output_tokens"] != float64(32) {
		t.Fatalf("generation request fields = %#v", body)
	}
	input, ok := body["input"].(string)
	if !ok || !strings.Contains(input, request.Question) || !strings.Contains(input, request.Package.Evidence[0].Content) {
		t.Fatalf("authorized input = %#v", body["input"])
	}
	textConfig, ok := body["text"].(map[string]any)
	if !ok {
		t.Fatalf("text config = %#v", body["text"])
	}
	format, ok := textConfig["format"].(map[string]any)
	if !ok || format["type"] != "json_schema" || format["name"] != "manu_generation_envelope" || format["strict"] != true {
		t.Fatalf("structured output format = %#v", textConfig["format"])
	}
	if _, ok := format["schema"].(map[string]any); !ok {
		t.Fatalf("structured output schema = %#v", format["schema"])
	}
}

func TestOpenAIResponsesRejectIncompleteRefusalAmbiguousAndInvalidOutput(t *testing.T) {
	request := makeOpenAIGenerationRequest()
	validText := openAIEnvelopeJSON(t, request, "safe answer")
	tests := []struct {
		name string
		body any
		want error
	}{
		{name: "incomplete", body: openAIResponsesJSON(t, request, "incomplete", validText), want: ErrInvalidResponse},
		{name: "refusal", body: map[string]any{
			"status": "completed", "model": "gpt-refusal", "usage": map[string]any{"input_tokens": 1, "output_tokens": 0, "total_tokens": 1},
			"output": []any{map[string]any{"type": "message", "content": []any{map[string]any{"type": "refusal", "refusal": "blocked output"}}}},
		}, want: ErrContentBlocked},
		{name: "multiple texts", body: map[string]any{
			"status": "completed", "model": "gpt-multiple", "usage": map[string]any{"input_tokens": 1, "output_tokens": 2, "total_tokens": 3},
			"output": []any{map[string]any{"type": "message", "content": []any{
				map[string]any{"type": "output_text", "text": validText},
				map[string]any{"type": "output_text", "text": validText},
			}}},
		}, want: ErrInvalidResponse},
		{name: "no text", body: map[string]any{
			"status": "completed", "model": "gpt-no-text", "usage": map[string]any{"input_tokens": 1, "output_tokens": 0, "total_tokens": 1},
			"output": []any{map[string]any{"type": "reasoning", "content": []any{}}},
		}, want: ErrInvalidResponse},
		{name: "unknown envelope field", body: openAIResponsesJSON(t, request, "completed", `{"version":"v1alpha1","text":"answer","package_digest":"`+request.Package.Digest+`","evidence_ids":["evidence-1"],"gaps":[],"extra":"reject"}`), want: ErrInvalidResponse},
		{name: "invalid envelope json", body: openAIResponsesJSON(t, request, "completed", `not-json`), want: ErrInvalidResponse},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := newOpenAITestServer(t, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writeOpenAIJSON(t, writer, test.body)
			}))
			adapter, err := newOpenAIAdapterForTest(openAIAdapterConfig(server))
			if err != nil {
				server.Close()
				t.Fatal(err)
			}
			_, err = adapter.Generate(context.Background(), request)
			server.Close()
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestOpenAIEmbeddingRejectsMalformedData(t *testing.T) {
	request := makeOpenAIEmbeddingRequest()
	tests := []struct {
		name string
		data []any
	}{
		{name: "duplicate index", data: []any{
			map[string]any{"embedding": []float64{1, 2}, "index": 0, "object": "embedding"},
			map[string]any{"embedding": []float64{3, 4}, "index": 0, "object": "embedding"},
		}},
		{name: "wrong dimension", data: []any{
			map[string]any{"embedding": []float64{1}, "index": 0, "object": "embedding"},
			map[string]any{"embedding": []float64{3, 4}, "index": 1, "object": "embedding"},
		}},
		{name: "wrong object", data: []any{
			map[string]any{"embedding": []float64{1, 2}, "index": 0, "object": "vector"},
			map[string]any{"embedding": []float64{3, 4}, "index": 1, "object": "embedding"},
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := newOpenAITestServer(t, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writeOpenAIJSON(t, writer, map[string]any{
					"data":  test.data,
					"model": "embedding-model",
					"usage": map[string]any{"prompt_tokens": 1, "total_tokens": 1},
				})
			}))
			adapter, err := newOpenAIAdapterForTest(openAIAdapterConfig(server))
			if err != nil {
				server.Close()
				t.Fatal(err)
			}
			_, err = adapter.Embed(context.Background(), request)
			server.Close()
			if !errors.Is(err, ErrInvalidResponse) {
				t.Fatalf("error = %v, want invalid response", err)
			}
		})
	}
}

func TestOpenAIHTTPStatusesAreNormalizedWithoutBodyLeak(t *testing.T) {
	secret := "api_key=super-secret prompt output"
	tests := []struct {
		status int
		want   error
	}{
		{status: http.StatusUnauthorized, want: ErrAuthentication},
		{status: http.StatusForbidden, want: ErrAuthentication},
		{status: http.StatusTooManyRequests, want: ErrRateLimit},
		{status: http.StatusRequestTimeout, want: ErrTimeout},
		{status: http.StatusBadGateway, want: ErrUnavailable},
		{status: http.StatusBadRequest, want: ErrInvalidResponse},
	}
	for _, test := range tests {
		t.Run(http.StatusText(test.status), func(t *testing.T) {
			server := newOpenAITestServer(t, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.WriteHeader(test.status)
				_, _ = io.WriteString(writer, `{"error":"`+secret+`"}`)
			}))
			adapter, err := newOpenAIAdapterForTest(openAIAdapterConfig(server))
			if err != nil {
				server.Close()
				t.Fatal(err)
			}
			_, err = adapter.Embed(context.Background(), makeOpenAIEmbeddingRequest())
			server.Close()
			if !errors.Is(err, test.want) {
				t.Fatalf("status %d error = %v, want %v", test.status, err, test.want)
			}
			if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "super-secret") {
				t.Fatalf("status error leaked body: %q", err)
			}
		})
	}
}

func TestOpenAIDoesNotFollowRedirectsWithInjectedClient(t *testing.T) {
	var calls atomic.Int32
	server := newOpenAITestServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		if request.URL.Path == "/v1/embeddings" {
			http.Redirect(writer, request, "/redirect", http.StatusTemporaryRedirect)
			return
		}
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	client := server.Client()
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return nil }
	config := openAIAdapterConfig(server)
	config.HTTPClient = client
	adapter, err := newOpenAIAdapterForTest(config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Embed(context.Background(), makeOpenAIEmbeddingRequest()); !errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("redirect error = %v, want invalid response", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("redirect calls = %d, want one", calls.Load())
	}
}

func TestOpenAIContextCancellationAndDeadlineReachTransport(t *testing.T) {
	started := make(chan struct{})
	var startedOnce sync.Once
	server := newOpenAITestServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		startedOnce.Do(func() { close(started) })
		<-request.Context().Done()
	}))
	defer server.Close()
	adapter, err := newOpenAIAdapterForTest(openAIAdapterConfig(server))
	if err != nil {
		t.Fatal(err)
	}
	request := makeOpenAIEmbeddingRequest()
	ctx, cancel := context.WithCancel(context.Background())
	resultCh := make(chan error, 1)
	go func() {
		_, callErr := adapter.Embed(ctx, request)
		resultCh <- callErr
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("request did not reach test server")
	}
	cancel()
	if err := <-resultCh; !errors.Is(err, ErrCancelled) || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error = %v", err)
	}

	deadlineRequest := makeOpenAIEmbeddingRequest()
	deadlineRequest.Deadline = time.Now().Add(25 * time.Millisecond)
	if _, err := adapter.Embed(context.Background(), deadlineRequest); !errors.Is(err, ErrTimeout) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline error = %v", err)
	}
}

func TestOpenAIBodyLimitsApplyBeforeAndAfterTransport(t *testing.T) {
	var calls atomic.Int32
	server := newOpenAITestServer(t, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{}`+strings.Repeat(" ", 2_000))
	}))
	defer server.Close()
	config := openAIAdapterConfig(server)
	config.MaxBodyBytes = 512
	config.StructuredOutputSchema = json.RawMessage(`{"type":"object"}`)
	adapter, err := newOpenAIAdapterForTest(config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Embed(context.Background(), makeOpenAIEmbeddingRequest()); !errors.Is(err, ErrBudget) {
		t.Fatalf("response body limit error = %v, want budget", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("response limit calls = %d, want one", calls.Load())
	}

	largeRequest := makeOpenAIEmbeddingRequest()
	largeRequest.Items[0].Content = strings.Repeat("x", 2_000)
	if _, err := adapter.Embed(context.Background(), largeRequest); !errors.Is(err, ErrBudget) {
		t.Fatalf("request body limit error = %v, want budget", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("request limit reached server with calls = %d", calls.Load())
	}
}

func TestOpenAIValidatesCapabilityAndConfigurationBeforeNetwork(t *testing.T) {
	var calls atomic.Int32
	server := newOpenAITestServer(t, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
	}))
	defer server.Close()
	config := openAIAdapterConfig(server)
	adapter, err := newOpenAIAdapterForTest(config)
	if err != nil {
		t.Fatal(err)
	}
	invalidEmbedding := makeOpenAIEmbeddingRequest()
	invalidEmbedding.Profile.Provider = ProviderSimulated
	if _, err := adapter.Embed(context.Background(), invalidEmbedding); !errors.Is(err, ErrCapability) {
		t.Fatalf("provider capability error = %v", err)
	}
	invalidGeneration := makeOpenAIGenerationRequest()
	invalidGeneration.Profile.Protocol = ProtocolChatCompletions
	if _, err := adapter.Generate(context.Background(), invalidGeneration); !errors.Is(err, ErrCapability) {
		t.Fatalf("protocol capability error = %v", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("invalid capability reached network: %d calls", calls.Load())
	}

	if _, err := newOpenAIAdapterForTest(OpenAIConfig{BaseURL: server.URL}); !errors.Is(err, ErrConfiguration) {
		t.Fatalf("missing key error = %v", err)
	}
	if _, err := NewOpenAIAdapter(OpenAIConfig{BaseURL: "http://example.com", APIKey: "key"}); !errors.Is(err, ErrConfiguration) {
		t.Fatalf("non-loopback HTTP URL error = %v", err)
	}
	if _, err := newOpenAIAdapterForTest(OpenAIConfig{BaseURL: server.URL, APIKey: "key", StructuredOutputName: "bad name"}); !errors.Is(err, ErrConfiguration) {
		t.Fatalf("schema name error = %v", err)
	}
	if _, err := newOpenAIAdapterForTest(OpenAIConfig{BaseURL: server.URL, APIKey: "key", StructuredOutputSchema: json.RawMessage(`[]`)}); !errors.Is(err, ErrCapability) {
		t.Fatalf("schema type error = %v", err)
	}
}

func TestOpenAIConfigAndAdapterDiagnosticsDoNotExposeKey(t *testing.T) {
	secret := "super-secret-api-key"
	config := OpenAIConfig{BaseURL: "https://api.openai.com", APIKey: secret, StructuredOutputName: "schema"}
	if strings.Contains(fmt.Sprint(config), secret) {
		t.Fatalf("config diagnostic leaked API key: %s", config)
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), secret) || strings.Contains(string(encoded), "api_key") {
		t.Fatalf("config JSON leaked API key: %s", encoded)
	}
	adapter, err := NewOpenAIAdapter(config)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(fmt.Sprint(adapter), secret) || strings.Contains(adapter.String(), secret) {
		t.Fatalf("adapter diagnostic leaked API key: %s", adapter)
	}
}
