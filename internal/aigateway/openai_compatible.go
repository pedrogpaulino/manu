package aigateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	openRouterDialect        OpenAICompatibleDialect = "openrouter"
	maxOpenRouterHeaderBytes                         = 2 << 10
)

// OpenAICompatibleDialect identifies an explicit compatible-provider
// dialect. This first implementation supports only OpenRouter.
type OpenAICompatibleDialect string

const (
	// OpenRouterDialect is the supported compatible-provider dialect.
	OpenRouterDialect OpenAICompatibleDialect = openRouterDialect
)

// OpenAICompatibleConfig configures an explicit compatible-provider transport.
// BaseURL is required; unlike the direct OpenAI adapter there is no implicit
// endpoint. APIKey is never serialized or included in diagnostics.
type OpenAICompatibleConfig struct {
	Dialect                OpenAICompatibleDialect `json:"dialect"`
	BaseURL                string                  `json:"base_url"`
	APIKey                 string                  `json:"-"`
	HTTPClient             *http.Client            `json:"-"`
	MaxBodyBytes           int64                   `json:"max_body_bytes"`
	MaxOutputTokens        int                     `json:"max_output_tokens"`
	StructuredOutputs      bool                    `json:"structured_outputs"`
	StructuredOutputName   string                  `json:"structured_output_name"`
	StructuredOutputSchema json.RawMessage         `json:"structured_output_schema,omitempty"`
	HTTPReferer            string                  `json:"http_referer,omitempty"`
	OpenRouterTitle        string                  `json:"openrouter_title,omitempty"`
}

// String returns only non-secret bounded configuration dimensions.
func (c OpenAICompatibleConfig) String() string {
	return fmt.Sprintf("openai-compatible config dialect=%q base_url=%q max_body_bytes=%d max_output_tokens=%d structured_outputs=%t", c.Dialect, redactedOpenRouterBaseURL(c.BaseURL), c.MaxBodyBytes, c.MaxOutputTokens, c.StructuredOutputs)
}

// OpenAICompatibleAdapter implements the explicitly selected compatible
// dialect without exposing provider DTOs to the gateway core.
type OpenAICompatibleAdapter struct {
	dialect                OpenAICompatibleDialect
	baseURL                *url.URL
	apiKey                 string
	httpClient             *http.Client
	maxBodyBytes           int64
	maxOutputTokens        int
	structuredOutputs      bool
	structuredOutputName   string
	structuredOutputSchema json.RawMessage
	httpReferer            string
	openRouterTitle        string
}

var (
	_ Embedder  = (*OpenAICompatibleAdapter)(nil)
	_ Generator = (*OpenAICompatibleAdapter)(nil)
)

// String returns only safe adapter dimensions and never the API key.
func (a *OpenAICompatibleAdapter) String() string {
	if a == nil {
		return "openai-compatible adapter <nil>"
	}
	baseURL := ""
	if a.baseURL != nil {
		baseURL = a.baseURL.String()
	}
	return fmt.Sprintf("openai-compatible adapter dialect=%q base_url=%q max_body_bytes=%d structured_outputs=%t", a.dialect, baseURL, a.maxBodyBytes, a.structuredOutputs)
}

// NewOpenAICompatibleAdapter validates the explicit dialect, endpoint,
// structured-output declaration and transport without network discovery.
func NewOpenAICompatibleAdapter(config OpenAICompatibleConfig) (*OpenAICompatibleAdapter, error) {
	return newOpenAICompatibleAdapter(config, false)
}

func newOpenAICompatibleAdapter(config OpenAICompatibleConfig, allowLoopback bool) (*OpenAICompatibleAdapter, error) {
	if config.Dialect != OpenRouterDialect {
		return nil, NewGatewayError(ErrorKindCapability, CapabilityUnknown, nil)
	}
	baseURL, err := parseOpenRouterBaseURL(config.BaseURL, allowLoopback)
	if err != nil {
		return nil, capabilityError(CapabilityUnknown, err)
	}
	if strings.TrimSpace(config.APIKey) == "" || len(config.APIKey) > maxOpenAIKeyBytes || containsControl(config.APIKey) {
		return nil, NewGatewayError(ErrorKindConfiguration, CapabilityUnknown, nil)
	}
	maxBodyBytes := config.MaxBodyBytes
	if maxBodyBytes == 0 {
		maxBodyBytes = defaultOpenAIBodyBytes
	}
	if maxBodyBytes <= 0 || maxBodyBytes > maxOpenAIBodyBytes {
		return nil, NewGatewayError(ErrorKindConfiguration, CapabilityUnknown, nil)
	}
	if config.MaxOutputTokens < 0 || config.MaxOutputTokens > maxOpenAIOutputTokens {
		return nil, NewGatewayError(ErrorKindConfiguration, CapabilityGeneration, nil)
	}
	if err := validateOpenRouterHeader(config.HTTPReferer); err != nil {
		return nil, capabilityError(CapabilityUnknown, err)
	}
	if err := validateOpenRouterHeader(config.OpenRouterTitle); err != nil {
		return nil, capabilityError(CapabilityUnknown, err)
	}
	name := config.StructuredOutputName
	if name == "" {
		name = "manu_generation_envelope"
	}
	if err := validateOpenAISchemaName(name); err != nil {
		return nil, capabilityError(CapabilityGeneration, err)
	}
	schema := config.StructuredOutputSchema
	if len(schema) == 0 {
		schema = defaultOpenAIStructuredOutputSchema
	}
	if err := validateOpenAISchema(schema, maxBodyBytes); err != nil {
		return nil, capabilityError(CapabilityGeneration, err)
	}
	return &OpenAICompatibleAdapter{
		dialect:                config.Dialect,
		baseURL:                baseURL,
		apiKey:                 config.APIKey,
		httpClient:             cloneOpenAIHTTPClient(config.HTTPClient),
		maxBodyBytes:           maxBodyBytes,
		maxOutputTokens:        config.MaxOutputTokens,
		structuredOutputs:      config.StructuredOutputs,
		structuredOutputName:   name,
		structuredOutputSchema: append(json.RawMessage(nil), schema...),
		httpReferer:            config.HTTPReferer,
		openRouterTitle:        config.OpenRouterTitle,
	}, nil
}

// Embed sends the OpenRouter embedding dialect and restores provider order by
// index. Dimensions are sent only when the internal profile configures one.
func (a *OpenAICompatibleAdapter) Embed(ctx context.Context, request EmbeddingRequest) (EmbeddingResult, error) {
	if a == nil || a.baseURL == nil || a.httpClient == nil {
		return EmbeddingResult{}, NewGatewayError(ErrorKindConfiguration, CapabilityEmbedding, nil)
	}
	if ctx == nil {
		return EmbeddingResult{}, NewGatewayError(ErrorKindConfiguration, CapabilityEmbedding, nil)
	}
	if err := request.Validate(); err != nil {
		return EmbeddingResult{}, capabilityError(CapabilityEmbedding, err)
	}
	if request.Profile.Provider != ProviderOpenRouter {
		return EmbeddingResult{}, NewGatewayError(ErrorKindCapability, CapabilityEmbedding, nil)
	}
	effective, cancel, err := effectiveOrchestrationContext(ctx, request.Deadline, CapabilityEmbedding)
	if err != nil {
		return EmbeddingResult{}, err
	}
	defer cancel()
	input := make([]string, len(request.Items))
	for index, item := range request.Items {
		input[index] = item.Content
	}
	var inputValue any = input
	if len(input) == 1 {
		inputValue = input[0]
	}
	payload := openRouterEmbeddingRequest{
		Input:          inputValue,
		Model:          request.Profile.Model,
		EncodingFormat: "float",
	}
	if request.Profile.Dimension > 0 {
		payload.Dimensions = &request.Profile.Dimension
	}
	var response openAIEmbeddingResponse
	started := time.Now()
	if err := a.doOpenRouterJSON(effective, CapabilityEmbedding, "/embeddings", payload, &response); err != nil {
		return EmbeddingResult{}, err
	}
	if err := contextError(effective, request.Deadline, CapabilityEmbedding); err != nil {
		return EmbeddingResult{}, err
	}
	return normalizeOpenRouterEmbedding(request, response, time.Since(started))
}

// Generate uses OpenRouter's chat-completions dialect only. It does not fall
// back to Responses or discover model capabilities remotely.
func (a *OpenAICompatibleAdapter) Generate(ctx context.Context, request GenerationRequest) (GenerationResult, error) {
	if a == nil || a.baseURL == nil || a.httpClient == nil {
		return GenerationResult{}, NewGatewayError(ErrorKindConfiguration, CapabilityGeneration, nil)
	}
	if ctx == nil {
		return GenerationResult{}, NewGatewayError(ErrorKindConfiguration, CapabilityGeneration, nil)
	}
	if err := request.Validate(); err != nil {
		return GenerationResult{}, capabilityError(CapabilityGeneration, err)
	}
	if request.Profile.Provider != ProviderOpenRouter || request.Profile.Protocol != ProtocolChatCompletions {
		return GenerationResult{}, NewGatewayError(ErrorKindCapability, CapabilityGeneration, nil)
	}
	if !a.structuredOutputs {
		return GenerationResult{}, NewGatewayError(ErrorKindCapability, CapabilityGeneration, nil)
	}
	effective, cancel, err := effectiveOrchestrationContext(ctx, request.Deadline, CapabilityGeneration)
	if err != nil {
		return GenerationResult{}, err
	}
	defer cancel()
	input, err := openAIResponseInput(request)
	if err != nil {
		return GenerationResult{}, capabilityError(CapabilityGeneration, err)
	}
	payload := openRouterChatRequest{
		Model: request.Profile.Model,
		Messages: []openRouterMessage{
			{Role: "system", Content: untrustedEvidenceInstruction},
			{Role: "user", Content: input},
		},
		ResponseFormat: openRouterResponseFormat{
			Type: "json_schema",
			JSONSchema: openRouterJSONSchema{
				Name:   a.structuredOutputName,
				Strict: true,
				Schema: json.RawMessage(append([]byte(nil), a.structuredOutputSchema...)),
			},
		},
	}
	if a.maxOutputTokens > 0 {
		payload.MaxTokens = &a.maxOutputTokens
	}
	var response openRouterChatResponse
	started := time.Now()
	if err := a.doOpenRouterJSON(effective, CapabilityGeneration, "/chat/completions", payload, &response); err != nil {
		return GenerationResult{}, err
	}
	if err := contextError(effective, request.Deadline, CapabilityGeneration); err != nil {
		return GenerationResult{}, err
	}
	return normalizeOpenRouterGeneration(request, response, time.Since(started))
}

type openRouterEmbeddingRequest struct {
	Input          any    `json:"input"`
	Model          string `json:"model"`
	Dimensions     *int   `json:"dimensions,omitempty"`
	EncodingFormat string `json:"encoding_format,omitempty"`
}

type openRouterChatRequest struct {
	Model          string                   `json:"model"`
	Messages       []openRouterMessage      `json:"messages"`
	ResponseFormat openRouterResponseFormat `json:"response_format"`
	MaxTokens      *int                     `json:"max_tokens,omitempty"`
}

type openRouterMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	Refusal string `json:"refusal,omitempty"`
}

type openRouterResponseFormat struct {
	Type       string               `json:"type"`
	JSONSchema openRouterJSONSchema `json:"json_schema"`
}

type openRouterJSONSchema struct {
	Name   string          `json:"name"`
	Strict bool            `json:"strict"`
	Schema json.RawMessage `json:"schema"`
}

type openRouterChatResponse struct {
	Model   string               `json:"model"`
	Choices []openRouterChoice   `json:"choices"`
	Usage   *openRouterChatUsage `json:"usage"`
}

type openRouterChoice struct {
	Index        int               `json:"index"`
	Message      openRouterMessage `json:"message"`
	FinishReason string            `json:"finish_reason"`
}

type openRouterChatUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

func (a *OpenAICompatibleAdapter) doOpenRouterJSON(ctx context.Context, capability Capability, path string, payload any, output any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return NewGatewayError(ErrorKindConfiguration, capability, nil)
	}
	if int64(len(body)) > a.maxBodyBytes {
		return NewGatewayError(ErrorKindBudget, capability, nil)
	}
	endpoint := openRouterEndpoint(a.baseURL, path)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return NewGatewayError(ErrorKindConfiguration, capability, nil)
	}
	req.Header.Set("Authorization", "Bearer "+a.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if a.httpReferer != "" {
		req.Header.Set("HTTP-Referer", a.httpReferer)
	}
	if a.openRouterTitle != "" {
		req.Header.Set("X-OpenRouter-Title", a.openRouterTitle)
	}
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return normalizeOpenAITransportError(capability, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return normalizeOpenRouterStatus(capability, resp.StatusCode)
	}
	if err := decodeOpenAILimitedJSON(resp.Body, a.maxBodyBytes, output); err != nil {
		if errors.Is(err, errOpenAIBodyLimit) {
			return NewGatewayError(ErrorKindBudget, capability, nil)
		}
		return NewGatewayError(ErrorKindInvalidResponse, capability, nil)
	}
	return nil
}

func normalizeOpenRouterEmbedding(request EmbeddingRequest, response openAIEmbeddingResponse, latency time.Duration) (EmbeddingResult, error) {
	if len(response.Data) != len(request.Items) || !safeOpenAIModel(response.Model) || response.Usage == nil {
		return EmbeddingResult{}, NewGatewayError(ErrorKindInvalidResponse, CapabilityEmbedding, nil)
	}
	if response.Usage.PromptTokens < 0 || response.Usage.TotalTokens < response.Usage.PromptTokens {
		return EmbeddingResult{}, NewGatewayError(ErrorKindInvalidResponse, CapabilityEmbedding, nil)
	}
	vectors := make([][]float64, len(response.Data))
	seen := make([]bool, len(response.Data))
	for _, item := range response.Data {
		if item.Index < 0 || item.Index >= len(response.Data) || seen[item.Index] || item.Object != "embedding" || len(item.Embedding) != request.Profile.Dimension {
			return EmbeddingResult{}, NewGatewayError(ErrorKindInvalidResponse, CapabilityEmbedding, nil)
		}
		for _, value := range item.Embedding {
			if math.IsNaN(value) || math.IsInf(value, 0) {
				return EmbeddingResult{}, NewGatewayError(ErrorKindInvalidResponse, CapabilityEmbedding, nil)
			}
		}
		seen[item.Index] = true
		vectors[item.Index] = append([]float64(nil), item.Embedding...)
	}
	for _, present := range seen {
		if !present {
			return EmbeddingResult{}, NewGatewayError(ErrorKindInvalidResponse, CapabilityEmbedding, nil)
		}
	}
	result := EmbeddingResult{
		ExecutionID: request.ExecutionID,
		RequestID:   request.RequestID,
		Provider:    ProviderOpenRouter,
		Model:       response.Model,
		Vectors:     vectors,
		Usage: Usage{
			InputItems:   len(vectors),
			OutputItems:  len(vectors),
			InputTokens:  response.Usage.PromptTokens,
			OutputTokens: response.Usage.TotalTokens - response.Usage.PromptTokens,
		},
		Latency:     latency,
		Termination: TerminationCompleted,
		Metadata:    Metadata{"dialect": string(OpenRouterDialect)},
	}
	if err := result.Validate(request); err != nil {
		return EmbeddingResult{}, NewGatewayError(ErrorKindInvalidResponse, CapabilityEmbedding, nil)
	}
	return result, nil
}

func normalizeOpenRouterGeneration(request GenerationRequest, response openRouterChatResponse, latency time.Duration) (GenerationResult, error) {
	if len(response.Choices) != 1 || response.Usage == nil || !safeOpenAIModel(response.Model) {
		return GenerationResult{}, NewGatewayError(ErrorKindInvalidResponse, CapabilityGeneration, nil)
	}
	choice := response.Choices[0]
	if choice.Index != 0 || choice.Message.Role != "assistant" || choice.Message.Content == "" || choice.Message.Refusal != "" || choice.FinishReason != "stop" {
		if choice.Message.Refusal != "" {
			return GenerationResult{}, NewGatewayError(ErrorKindContentBlocked, CapabilityGeneration, nil)
		}
		return GenerationResult{}, NewGatewayError(ErrorKindInvalidResponse, CapabilityGeneration, nil)
	}
	usage := response.Usage
	if usage.PromptTokens < 0 || usage.CompletionTokens < 0 || usage.TotalTokens < 0 || usage.TotalTokens != usage.PromptTokens+usage.CompletionTokens {
		return GenerationResult{}, NewGatewayError(ErrorKindInvalidResponse, CapabilityGeneration, nil)
	}
	var output GenerationEnvelope
	if err := decodeOpenAIEnvelope(choice.Message.Content, &output); err != nil {
		return GenerationResult{}, NewGatewayError(ErrorKindInvalidResponse, CapabilityGeneration, nil)
	}
	result := GenerationResult{
		ExecutionID: request.ExecutionID,
		RequestID:   request.RequestID,
		Provider:    ProviderOpenRouter,
		Model:       response.Model,
		Output:      output,
		Usage: Usage{
			InputItems:   1,
			OutputItems:  1,
			InputTokens:  usage.PromptTokens,
			OutputTokens: usage.CompletionTokens,
		},
		Latency:     latency,
		Termination: TerminationCompleted,
		Metadata:    Metadata{"dialect": string(OpenRouterDialect), "protocol": string(ProtocolChatCompletions)},
	}
	if err := result.Validate(request); err != nil {
		return GenerationResult{}, NewGatewayError(ErrorKindInvalidResponse, CapabilityGeneration, nil)
	}
	return result, nil
}

func parseOpenRouterBaseURL(raw string, allowLoopback bool) (*url.URL, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, fmt.Errorf("openrouter base url: %w", ErrConfiguration)
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, fmt.Errorf("openrouter base url: %w", ErrConfiguration)
	}
	host := parsed.Hostname()
	if isLoopbackHost(host) {
		if !allowLoopback {
			return nil, fmt.Errorf("openrouter base url: %w", ErrConfiguration)
		}
		// Loopback is allowed only by the unexported test constructor.
	} else {
		// The OpenRouter key must never be sent to an arbitrary compatible
		// endpoint. A future dialect gets its own explicit host policy.
		if parsed.Scheme != "https" || !strings.EqualFold(host, "openrouter.ai") || blockedOpenRouterHost(host) || (parsed.Port() != "" && parsed.Port() != "443") {
			return nil, fmt.Errorf("openrouter base url: %w", ErrConfiguration)
		}
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	if parsed.Path == "" {
		parsed.Path = "/api/v1"
	}
	return parsed, nil
}

func redactedOpenRouterBaseURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "<invalid>"
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

func blockedOpenRouterHost(host string) bool {
	lower := strings.ToLower(strings.TrimSuffix(host, "."))
	if lower == "localhost" || strings.HasSuffix(lower, ".localhost") || strings.HasSuffix(lower, ".local") || strings.HasSuffix(lower, ".internal") || lower == "metadata" || strings.HasSuffix(lower, ".metadata.google.internal") {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast()
}

func openRouterEndpoint(baseURL *url.URL, path string) *url.URL {
	endpoint := *baseURL
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + path
	return &endpoint
}

func validateOpenRouterHeader(value string) error {
	if len(value) > maxOpenRouterHeaderBytes || containsControl(value) {
		return fmt.Errorf("openrouter header: %w", ErrConfiguration)
	}
	return nil
}

func normalizeOpenRouterStatus(capability Capability, status int) error {
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return NewGatewayError(ErrorKindAuthentication, capability, nil)
	case status == http.StatusPaymentRequired:
		return NewGatewayError(ErrorKindBudget, capability, nil)
	case status == http.StatusNotFound:
		return NewGatewayError(ErrorKindCapability, capability, nil)
	case status == http.StatusTooManyRequests:
		return NewGatewayError(ErrorKindRateLimit, capability, nil)
	case status == http.StatusRequestTimeout || status == 524:
		return NewGatewayError(ErrorKindTimeout, capability, nil)
	case status >= 500:
		return NewGatewayError(ErrorKindUnavailable, capability, nil)
	default:
		return NewGatewayError(ErrorKindInvalidResponse, capability, nil)
	}
}
