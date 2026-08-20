package aigateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func openRouterEmbeddingProfile() EmbeddingProfile {
	profile := testEmbeddingProfile("openai/text-embedding-3-small")
	profile.Provider = ProviderOpenRouter
	profile.Dimension = 2
	return profile
}

func openRouterGenerationProfile() GenerationProfile {
	profile := testGenerationProfile("openai/gpt-4o-mini")
	profile.Provider = ProviderOpenRouter
	profile.Protocol = ProtocolChatCompletions
	return profile
}

func makeOpenRouterEmbeddingRequest() EmbeddingRequest {
	request := testEmbeddingRequest(openRouterEmbeddingProfile())
	request.Items = []EmbeddingItem{
		{ID: "unit-a", Content: "first authorized content"},
		{ID: "unit-b", Content: "second authorized content"},
	}
	return request
}

func makeOpenRouterGenerationRequest() GenerationRequest {
	return testGenerationRequest(openRouterGenerationProfile())
}

func openRouterAdapterConfig(baseURL string) OpenAICompatibleConfig {
	return OpenAICompatibleConfig{
		Dialect:           OpenRouterDialect,
		BaseURL:           baseURL,
		APIKey:            "openrouter-test-key",
		HTTPClient:        nil,
		MaxOutputTokens:   32,
		StructuredOutputs: true,
		HTTPReferer:       "https://manu.example",
		OpenRouterTitle:   "Manu tests",
	}
}

func openRouterEnvelopeJSON(t *testing.T, request GenerationRequest, text string) string {
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

func TestOpenRouterConfigRequiresExplicitSafeEndpoint(t *testing.T) {
	baseConfig := openRouterAdapterConfig("https://openrouter.ai/api/v1")
	tests := []struct {
		name string
		edit func(*OpenAICompatibleConfig)
		want error
	}{
		{name: "missing dialect", edit: func(config *OpenAICompatibleConfig) { config.Dialect = "" }, want: ErrCapability},
		{name: "missing base url", edit: func(config *OpenAICompatibleConfig) { config.BaseURL = "" }, want: ErrConfiguration},
		{name: "missing key", edit: func(config *OpenAICompatibleConfig) { config.APIKey = "" }, want: ErrConfiguration},
		{name: "remote http", edit: func(config *OpenAICompatibleConfig) { config.BaseURL = "http://openrouter.ai/api/v1" }, want: ErrConfiguration},
		{name: "arbitrary remote host", edit: func(config *OpenAICompatibleConfig) { config.BaseURL = "https://api.example.com/api/v1" }, want: ErrConfiguration},
		{name: "remote nonstandard port", edit: func(config *OpenAICompatibleConfig) { config.BaseURL = "https://openrouter.ai:8443/api/v1" }, want: ErrConfiguration},
		{name: "userinfo", edit: func(config *OpenAICompatibleConfig) { config.BaseURL = "https://user:password@openrouter.ai/api/v1" }, want: ErrConfiguration},
		{name: "query", edit: func(config *OpenAICompatibleConfig) { config.BaseURL = "https://openrouter.ai/api/v1?key=secret" }, want: ErrConfiguration},
		{name: "fragment", edit: func(config *OpenAICompatibleConfig) { config.BaseURL = "https://openrouter.ai/api/v1#secret" }, want: ErrConfiguration},
		{name: "private ipv4", edit: func(config *OpenAICompatibleConfig) { config.BaseURL = "https://10.0.0.1/api/v1" }, want: ErrConfiguration},
		{name: "link local ipv4", edit: func(config *OpenAICompatibleConfig) { config.BaseURL = "https://169.254.169.254/api/v1" }, want: ErrConfiguration},
		{name: "metadata host", edit: func(config *OpenAICompatibleConfig) { config.BaseURL = "https://metadata.google.internal/api/v1" }, want: ErrConfiguration},
		{name: "header control", edit: func(config *OpenAICompatibleConfig) { config.HTTPReferer = "https://manu.example\r\nX-Leak: yes" }, want: ErrConfiguration},
		{name: "title control", edit: func(config *OpenAICompatibleConfig) { config.OpenRouterTitle = "Manu\nInjected" }, want: ErrConfiguration},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := baseConfig
			test.edit(&config)
			_, err := NewOpenAICompatibleAdapter(config)
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}

	for _, baseURL := range []string{"http://127.0.0.1:12345", "https://127.0.0.1:12345", "http://[::1]:12345"} {
		config := baseConfig
		config.BaseURL = baseURL
		if _, err := newOpenAICompatibleAdapterForTest(config); err != nil {
			t.Fatalf("loopback base URL %q rejected: %v", baseURL, err)
		}
	}
}

func TestOpenRouterConfigAndAdapterDiagnosticsOmitKey(t *testing.T) {
	secret := "sk-openrouter-super-secret"
	config := openRouterAdapterConfig("https://openrouter.ai/api/v1")
	config.APIKey = secret
	if strings.Contains(fmt.Sprint(config), secret) {
		t.Fatalf("config diagnostic leaked API key: %s", config)
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), secret) {
		t.Fatalf("config JSON leaked API key: %s", encoded)
	}
	adapter, err := NewOpenAICompatibleAdapter(config)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(adapter.String(), secret) {
		t.Fatalf("adapter diagnostic leaked API key: %s", adapter)
	}
}

type openRouterRoundTripFunc func(*http.Request) (*http.Response, error)

func (f openRouterRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestOpenRouterValidatesCapabilityBeforeNetwork(t *testing.T) {
	var calls atomic.Int32
	config := openRouterAdapterConfig("https://openrouter.ai/api/v1")
	config.HTTPClient = &http.Client{Transport: openRouterRoundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, errors.New("network should not be reached")
	})}
	adapter, err := NewOpenAICompatibleAdapter(config)
	if err != nil {
		t.Fatal(err)
	}
	invalidEmbedding := makeOpenRouterEmbeddingRequest()
	invalidEmbedding.Profile.Provider = ProviderOpenAI
	if _, err := adapter.Embed(context.Background(), invalidEmbedding); !errors.Is(err, ErrCapability) {
		t.Fatalf("provider capability error = %v", err)
	}
	invalidGeneration := makeOpenRouterGenerationRequest()
	invalidGeneration.Profile.Protocol = ProtocolResponses
	if _, err := adapter.Generate(context.Background(), invalidGeneration); !errors.Is(err, ErrCapability) {
		t.Fatalf("protocol capability error = %v", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("invalid requests reached network: %d calls", calls.Load())
	}

	config.StructuredOutputs = false
	adapter, err = NewOpenAICompatibleAdapter(config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Generate(context.Background(), makeOpenRouterGenerationRequest()); !errors.Is(err, ErrCapability) {
		t.Fatalf("structured-output capability error = %v", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("unsupported structured output reached network: %d calls", calls.Load())
	}
}

func TestOpenRouterEmbedShapeHeadersAndIndexedOrder(t *testing.T) {
	var body map[string]any
	server := newOpenAITestServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/embeddings" {
			t.Errorf("path = %s", request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer openrouter-test-key" || request.Header.Get("Content-Type") != "application/json" || request.Header.Get("Accept") != "application/json" {
			t.Errorf("headers = %#v", request.Header)
		}
		if request.Header.Get("HTTP-Referer") != "https://manu.example" || request.Header.Get("X-OpenRouter-Title") != "Manu tests" {
			t.Errorf("attribution headers = %#v", request.Header)
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		writeOpenAIJSON(t, writer, map[string]any{
			"data": []any{
				map[string]any{"embedding": []float64{3, 4}, "index": 1, "object": "embedding"},
				map[string]any{"embedding": []float64{1, 2}, "index": 0, "object": "embedding"},
			},
			"model": "openai/text-embedding-3-small:free",
			"usage": map[string]any{"prompt_tokens": 7, "total_tokens": 7},
		})
	}))
	defer server.Close()
	adapter, err := newOpenAICompatibleAdapterForTest(openRouterAdapterConfig(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	result, err := adapter.Embed(context.Background(), makeOpenRouterEmbeddingRequest())
	if err != nil {
		t.Fatal(err)
	}
	if result.Provider != ProviderOpenRouter || result.Model != "openai/text-embedding-3-small:free" || result.Metadata["dialect"] != string(OpenRouterDialect) {
		t.Fatalf("embedding result = %+v", result)
	}
	if len(result.Vectors) != 2 || result.Vectors[0][0] != 1 || result.Vectors[1][0] != 3 {
		t.Fatalf("indexed response order = %#v", result.Vectors)
	}
	if result.Usage.InputTokens != 7 || result.Usage.OutputTokens != 0 {
		t.Fatalf("embedding usage = %+v", result.Usage)
	}
	if body["model"] != "openai/text-embedding-3-small" || body["encoding_format"] != "float" {
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

func TestOpenRouterGenerateChatCompletionsStructuredShape(t *testing.T) {
	request := makeOpenRouterGenerationRequest()
	var body map[string]any
	server := newOpenAITestServer(t, http.HandlerFunc(func(writer http.ResponseWriter, incoming *http.Request) {
		if incoming.URL.Path != "/api/v1/chat/completions" {
			t.Errorf("path = %s", incoming.URL.Path)
		}
		if incoming.Header.Get("Authorization") != "Bearer openrouter-test-key" || incoming.Header.Get("Content-Type") != "application/json" {
			t.Errorf("headers = %#v", incoming.Header)
		}
		if err := json.NewDecoder(incoming.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		writeOpenAIJSON(t, writer, map[string]any{
			"model": "openai/gpt-4o-mini-2025-01-01",
			"choices": []any{map[string]any{
				"index":         0,
				"message":       map[string]any{"role": "assistant", "content": openRouterEnvelopeJSON(t, request, "authorized answer")},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{"prompt_tokens": 7, "completion_tokens": 3, "total_tokens": 10},
		})
	}))
	defer server.Close()
	adapter, err := newOpenAICompatibleAdapterForTest(openRouterAdapterConfig(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	result, err := adapter.Generate(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Provider != ProviderOpenRouter || result.Model != "openai/gpt-4o-mini-2025-01-01" || result.Output.Text != "authorized answer" {
		t.Fatalf("generation result = %+v", result)
	}
	if result.Usage.InputTokens != 7 || result.Usage.OutputTokens != 3 || result.Metadata["dialect"] != string(OpenRouterDialect) || result.Metadata["protocol"] != string(ProtocolChatCompletions) {
		t.Fatalf("generation accounting/metadata = %+v", result)
	}
	messages, ok := body["messages"].([]any)
	if !ok || len(messages) != 2 {
		t.Fatalf("messages = %#v", body["messages"])
	}
	systemMessage, ok := messages[0].(map[string]any)
	if !ok || systemMessage["role"] != "system" || systemMessage["content"] != untrustedEvidenceInstruction {
		t.Fatalf("system message = %#v", messages[0])
	}
	message, ok := messages[1].(map[string]any)
	if !ok || message["role"] != "user" {
		t.Fatalf("user message = %#v", messages[1])
	}
	content, ok := message["content"].(string)
	if !ok || !strings.Contains(content, request.Question) || !strings.Contains(content, request.Package.Evidence[0].Content) {
		t.Fatalf("authorized input = %#v", message["content"])
	}
	if body["model"] != request.Profile.Model || body["max_tokens"] != float64(32) {
		t.Fatalf("generation request fields = %#v", body)
	}
	responseFormat, ok := body["response_format"].(map[string]any)
	if !ok || responseFormat["type"] != "json_schema" {
		t.Fatalf("response format = %#v", body["response_format"])
	}
	schema, ok := responseFormat["json_schema"].(map[string]any)
	if !ok || schema["name"] != "manu_generation_envelope" || schema["strict"] != true {
		t.Fatalf("JSON schema format = %#v", responseFormat["json_schema"])
	}
	if _, ok := schema["schema"].(map[string]any); !ok {
		t.Fatalf("JSON schema body = %#v", schema["schema"])
	}
}

func TestOpenRouterStatusErrorsAreNormalizedWithoutBodyLeak(t *testing.T) {
	secret := "api_key=super-secret prompt output"
	tests := []struct {
		name   string
		status int
		want   error
	}{
		{name: "unauthorized", status: http.StatusUnauthorized, want: ErrAuthentication},
		{name: "forbidden", status: http.StatusForbidden, want: ErrAuthentication},
		{name: "credits", status: http.StatusPaymentRequired, want: ErrBudget},
		{name: "missing model", status: http.StatusNotFound, want: ErrCapability},
		{name: "rate limit", status: http.StatusTooManyRequests, want: ErrRateLimit},
		{name: "cloudflare timeout", status: 524, want: ErrTimeout},
		{name: "provider unavailable", status: 529, want: ErrUnavailable},
		{name: "server unavailable", status: http.StatusBadGateway, want: ErrUnavailable},
		{name: "bad request", status: http.StatusBadRequest, want: ErrInvalidResponse},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := newOpenAITestServer(t, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.WriteHeader(test.status)
				_, _ = io.WriteString(writer, `{"error":"`+secret+`"}`)
			}))
			adapter, err := newOpenAICompatibleAdapterForTest(openRouterAdapterConfig(server.URL))
			if err != nil {
				server.Close()
				t.Fatal(err)
			}
			_, err = adapter.Embed(context.Background(), makeOpenRouterEmbeddingRequest())
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

func TestOpenRouterGenerationRejectsAmbiguousOrUnsafeOutput(t *testing.T) {
	request := makeOpenRouterGenerationRequest()
	validText := openRouterEnvelopeJSON(t, request, "safe answer")
	tests := []struct {
		name string
		body any
		want error
	}{
		{name: "multiple choices", body: map[string]any{
			"model": "openai/gpt", "choices": []any{
				map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": validText}, "finish_reason": "stop"},
				map[string]any{"index": 1, "message": map[string]any{"role": "assistant", "content": validText}, "finish_reason": "stop"},
			}, "usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 2, "total_tokens": 3},
		}, want: ErrInvalidResponse},
		{name: "wrong choice index", body: map[string]any{
			"model": "openai/gpt", "choices": []any{map[string]any{"index": 1, "message": map[string]any{"role": "assistant", "content": validText}, "finish_reason": "stop"}},
			"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 2, "total_tokens": 3},
		}, want: ErrInvalidResponse},
		{name: "refusal", body: map[string]any{
			"model": "openai/gpt", "choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "refusal": "blocked output"}, "finish_reason": "stop"}},
			"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 0, "total_tokens": 1},
		}, want: ErrContentBlocked},
		{name: "wrong role", body: map[string]any{
			"model": "openai/gpt", "choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "user", "content": validText}, "finish_reason": "stop"}},
			"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 2, "total_tokens": 3},
		}, want: ErrInvalidResponse},
		{name: "truncated completion", body: map[string]any{
			"model": "openai/gpt", "choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": validText}, "finish_reason": "length"}},
			"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 2, "total_tokens": 3},
		}, want: ErrInvalidResponse},
		{name: "extra envelope JSON", body: map[string]any{
			"model": "openai/gpt", "choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": validText + ` {"extra":true}`}, "finish_reason": "stop"}},
			"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 2, "total_tokens": 3},
		}, want: ErrInvalidResponse},
		{name: "ambiguous content shape", body: map[string]any{
			"model": "openai/gpt", "choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": validText}}}, "finish_reason": "stop"}},
			"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 2, "total_tokens": 3},
		}, want: ErrInvalidResponse},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := newOpenAITestServer(t, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writeOpenAIJSON(t, writer, test.body)
			}))
			adapter, err := newOpenAICompatibleAdapterForTest(openRouterAdapterConfig(server.URL))
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

func TestOpenRouterRedirectIsNotFollowed(t *testing.T) {
	var calls atomic.Int32
	server := newOpenAITestServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		if request.URL.Path == "/api/v1/embeddings" {
			http.Redirect(writer, request, "/redirect", http.StatusTemporaryRedirect)
			return
		}
		writeOpenAIJSON(t, writer, map[string]any{})
	}))
	defer server.Close()
	client := server.Client()
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return nil }
	config := openRouterAdapterConfig(server.URL)
	config.HTTPClient = client
	adapter, err := newOpenAICompatibleAdapterForTest(config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Embed(context.Background(), makeOpenRouterEmbeddingRequest()); !errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("redirect error = %v, want invalid response", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("redirect calls = %d, want one", calls.Load())
	}
}

func TestOpenRouterContextCancellationAndDeadlineReachTransport(t *testing.T) {
	started := make(chan struct{})
	server := newOpenAITestServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		select {
		case <-started:
		default:
			close(started)
		}
		<-request.Context().Done()
	}))
	adapter, err := newOpenAICompatibleAdapterForTest(openRouterAdapterConfig(server.URL))
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	request := makeOpenRouterEmbeddingRequest()
	ctx, cancel := context.WithCancel(context.Background())
	resultCh := make(chan error, 1)
	go func() {
		_, callErr := adapter.Embed(ctx, request)
		resultCh <- callErr
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		server.Close()
		t.Fatal("request did not reach test server")
	}
	cancel()
	if err := <-resultCh; !errors.Is(err, ErrCancelled) || !errors.Is(err, context.Canceled) {
		server.Close()
		t.Fatalf("cancel error = %v", err)
	}
	server.Close()

	deadlineServer := newOpenAITestServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		<-request.Context().Done()
	}))
	defer deadlineServer.Close()
	deadlineAdapter, err := newOpenAICompatibleAdapterForTest(openRouterAdapterConfig(deadlineServer.URL))
	if err != nil {
		t.Fatal(err)
	}
	deadlineRequest := makeOpenRouterEmbeddingRequest()
	deadlineRequest.Deadline = time.Now().Add(25 * time.Millisecond)
	if _, err := deadlineAdapter.Embed(context.Background(), deadlineRequest); !errors.Is(err, ErrTimeout) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline error = %v", err)
	}
}

func TestOpenRouterBodyLimitsApplyBeforeAndAfterTransport(t *testing.T) {
	var calls atomic.Int32
	server := newOpenAITestServer(t, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{}`+strings.Repeat(" ", 2_000))
	}))
	defer server.Close()
	config := openRouterAdapterConfig(server.URL)
	config.MaxBodyBytes = 512
	config.StructuredOutputSchema = json.RawMessage(`{"type":"object"}`)
	adapter, err := newOpenAICompatibleAdapterForTest(config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Embed(context.Background(), makeOpenRouterEmbeddingRequest()); !errors.Is(err, ErrBudget) {
		t.Fatalf("response body limit error = %v, want budget", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("response limit calls = %d, want one", calls.Load())
	}

	largeRequest := makeOpenRouterEmbeddingRequest()
	largeRequest.Items[0].Content = strings.Repeat("x", 2_000)
	if _, err := adapter.Embed(context.Background(), largeRequest); !errors.Is(err, ErrBudget) {
		t.Fatalf("request body limit error = %v, want budget", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("request limit reached server with calls = %d", calls.Load())
	}
}

func TestOpenRouterNormalizationRejectsMalformedEmbeddingIndexesAndDimensions(t *testing.T) {
	request := makeOpenRouterEmbeddingRequest()
	validUsage := &openAIEmbeddingUsage{PromptTokens: 1, TotalTokens: 1}
	tests := []struct {
		name string
		data []openAIEmbeddingData
	}{
		{name: "duplicate index", data: []openAIEmbeddingData{
			{Embedding: []float64{1, 2}, Index: 0, Object: "embedding"},
			{Embedding: []float64{3, 4}, Index: 0, Object: "embedding"},
		}},
		{name: "out of range index", data: []openAIEmbeddingData{
			{Embedding: []float64{1, 2}, Index: 0, Object: "embedding"},
			{Embedding: []float64{3, 4}, Index: 2, Object: "embedding"},
		}},
		{name: "wrong dimension", data: []openAIEmbeddingData{
			{Embedding: []float64{1}, Index: 0, Object: "embedding"},
			{Embedding: []float64{3, 4}, Index: 1, Object: "embedding"},
		}},
		{name: "wrong object", data: []openAIEmbeddingData{
			{Embedding: []float64{1, 2}, Index: 0, Object: "vector"},
			{Embedding: []float64{3, 4}, Index: 1, Object: "embedding"},
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := normalizeOpenRouterEmbedding(request, openAIEmbeddingResponse{
				Data:  test.data,
				Model: "openai/text-embedding-3-small",
				Usage: validUsage,
			}, time.Millisecond)
			if !errors.Is(err, ErrInvalidResponse) {
				t.Fatalf("error = %v, want invalid response", err)
			}
		})
	}
}

func TestOpenRouterNormalizationRejectsMalformedGeneration(t *testing.T) {
	request := makeOpenRouterGenerationRequest()
	validText := openRouterEnvelopeJSON(t, request, "safe answer")
	validUsage := &openRouterChatUsage{PromptTokens: 1, CompletionTokens: 2, TotalTokens: 3}
	validChoice := openRouterChoice{
		Index: 0,
		Message: openRouterMessage{
			Role:    "assistant",
			Content: validText,
		},
		FinishReason: "stop",
	}
	tests := []struct {
		name    string
		choices []openRouterChoice
		usage   *openRouterChatUsage
		want    error
	}{
		{name: "multiple choices", choices: []openRouterChoice{validChoice, validChoice}, usage: validUsage, want: ErrInvalidResponse},
		{name: "choice index", choices: []openRouterChoice{{Index: 1, Message: validChoice.Message, FinishReason: "stop"}}, usage: validUsage, want: ErrInvalidResponse},
		{name: "refusal", choices: []openRouterChoice{{Index: 0, Message: openRouterMessage{Role: "assistant", Refusal: "blocked"}, FinishReason: "stop"}}, usage: validUsage, want: ErrContentBlocked},
		{name: "incomplete finish", choices: []openRouterChoice{{Index: 0, Message: validChoice.Message, FinishReason: "length"}}, usage: validUsage, want: ErrInvalidResponse},
		{name: "extra JSON", choices: []openRouterChoice{{Index: 0, Message: openRouterMessage{Role: "assistant", Content: validText + ` {"extra":true}`}, FinishReason: "stop"}}, usage: validUsage, want: ErrInvalidResponse},
		{name: "usage mismatch", choices: []openRouterChoice{validChoice}, usage: &openRouterChatUsage{PromptTokens: 1, CompletionTokens: 2, TotalTokens: 4}, want: ErrInvalidResponse},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := normalizeOpenRouterGeneration(request, openRouterChatResponse{
				Model:   "openai/gpt-4o-mini",
				Choices: test.choices,
				Usage:   test.usage,
			}, time.Millisecond)
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}
