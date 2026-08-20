package cli

import (
	"net/http"
	"testing"

	"github.com/pedrogpaulino/manu/internal/aigateway"
	"github.com/pedrogpaulino/manu/internal/config"
)

func TestLiveGeneratorFactorySelectsConfiguredExternalAdapter(t *testing.T) {
	tests := []struct {
		name     string
		provider config.Provider
		protocol config.Protocol
		baseURL  string
		wantType any
	}{
		{name: "openai responses", provider: config.ProviderOpenAI, protocol: config.ProtocolResponses, baseURL: "https://api.openai.com", wantType: (*aigateway.OpenAIAdapter)(nil)},
		{name: "openrouter chat completions", provider: config.ProviderOpenRouter, protocol: config.ProtocolChatCompletions, baseURL: "https://openrouter.ai/api/v1", wantType: (*aigateway.OpenAICompatibleAdapter)(nil)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			configuration := config.Default()
			configuration.Generation.Enabled = true
			configuration.Generation.Provider = test.provider
			configuration.Generation.Protocol = test.protocol
			configuration.Generation.Model = "gpt-test"
			configuration.Generation.BaseURL = test.baseURL
			configuration.Generation.APIKey = "test-key"
			configuration.Generation.MaxOutputTokens = 32
			generator, err := newLiveGeneratorWithHTTPClient(configuration, &http.Client{Transport: evalRoundTripFunc(func(*http.Request) (*http.Response, error) {
				t.Fatal("factory test unexpectedly made a network request")
				return nil, nil
			})})
			if err != nil {
				t.Fatalf("newLiveGeneratorWithHTTPClient() error = %v", err)
			}
			switch test.wantType.(type) {
			case *aigateway.OpenAIAdapter:
				if _, ok := generator.(*aigateway.OpenAIAdapter); !ok {
					t.Fatalf("generator type = %T, want OpenAIAdapter", generator)
				}
			case *aigateway.OpenAICompatibleAdapter:
				if _, ok := generator.(*aigateway.OpenAICompatibleAdapter); !ok {
					t.Fatalf("generator type = %T, want OpenAICompatibleAdapter", generator)
				}
			}
		})
	}
}

func TestLiveGeneratorFactoryRejectsSimulatedAndGenericFallback(t *testing.T) {
	for _, provider := range []config.Provider{config.ProviderSimulated, config.ProviderOpenAICompatible} {
		configuration := config.Default()
		configuration.Generation.Enabled = true
		configuration.Generation.Provider = provider
		configuration.Generation.Protocol = config.ProtocolChatCompletions
		configuration.Generation.Model = "gpt-test"
		configuration.Generation.BaseURL = "https://openrouter.ai/api/v1"
		configuration.Generation.APIKey = "test-key"
		if _, err := newLiveGeneratorWithHTTPClient(configuration, nil); err == nil {
			t.Fatalf("provider %q was accepted without a supported live adapter", provider)
		}
	}
}

type evalRoundTripFunc func(*http.Request) (*http.Response, error)

func (f evalRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}
