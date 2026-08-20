package config

import "testing"

func enabledGenerationConfig(provider Provider, protocol Protocol) GenerationConfig {
	return GenerationConfig{
		Enabled:         true,
		Provider:        provider,
		Model:           "generation-model",
		BaseURL:         "https://provider.example/v1",
		APIKey:          "test-key",
		Protocol:        protocol,
		Timeout:         30,
		MaxOutputTokens: 256,
		Budget: BudgetConfig{
			MaxRequests:     1,
			MaxInputTokens:  128,
			MaxOutputTokens: 256,
			MaxCostUSD:      1,
		},
	}
}

func TestGenerationConfigProtocolMatrixHasNoFallback(t *testing.T) {
	tests := []struct {
		name     string
		provider Provider
		protocol Protocol
		valid    bool
	}{
		{name: "openai responses", provider: ProviderOpenAI, protocol: ProtocolResponses, valid: true},
		{name: "openai chat rejected", provider: ProviderOpenAI, protocol: ProtocolChatCompletions},
		{name: "openrouter chat", provider: ProviderOpenRouter, protocol: ProtocolChatCompletions, valid: true},
		{name: "openrouter responses rejected", provider: ProviderOpenRouter, protocol: ProtocolResponses},
		{name: "compatible chat", provider: ProviderOpenAICompatible, protocol: ProtocolChatCompletions, valid: true},
		{name: "compatible responses rejected", provider: ProviderOpenAICompatible, protocol: ProtocolResponses},
		{name: "simulated responses", provider: ProviderSimulated, protocol: ProtocolResponses, valid: true},
		{name: "simulated chat", provider: ProviderSimulated, protocol: ProtocolChatCompletions, valid: true},
		{name: "simulated unknown", provider: ProviderSimulated, protocol: ProtocolUnknown},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			configuration := enabledGenerationConfig(test.provider, test.protocol)
			if test.provider == ProviderSimulated {
				configuration.APIKey = ""
				configuration.BaseURL = ""
			}
			err := configuration.Validate()
			if test.valid && err != nil {
				t.Fatalf("Validate() error = %v, want nil", err)
			}
			if !test.valid && err == nil {
				t.Fatal("Validate() error = nil, want explicit provider/protocol rejection")
			}
		})
	}
}

func TestDefaultConfigKeepsCapabilitiesDisabledAndValid(t *testing.T) {
	configuration := Default()
	if err := configuration.Validate(); err != nil {
		t.Fatalf("Default().Validate() error = %v", err)
	}
	if configuration.Embedding.Enabled || configuration.Generation.Enabled {
		t.Fatal("default configuration unexpectedly enables an AI capability")
	}
}
