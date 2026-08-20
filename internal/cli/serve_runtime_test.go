package cli

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/pedrogpaulino/manu/internal/aigateway"
	"github.com/pedrogpaulino/manu/internal/api"
	"github.com/pedrogpaulino/manu/internal/config"
)

func TestExecuteServeWithBuilderClosesOwnedResources(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	configuration := config.Default()
	closed := false
	server, err := api.NewServer(configuration)
	if err != nil {
		t.Fatal(err)
	}
	err = executeServeWithBuilder(ctx, configuration, func(context.Context, config.Config) (serveRuntime, error) {
		return serveRuntime{server: server, close: func() { closed = true }}, nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("executeServeWithBuilder() error = %v, want context.Canceled", err)
	}
	if !closed {
		t.Fatal("serve runtime close function was not called")
	}
}

func TestServeCompositionFailsClosedWithoutPool(t *testing.T) {
	_, _, err := composeServeServices(config.Default(), nil)
	if !errors.Is(err, ErrServeRuntimeNotConfigured) {
		t.Fatalf("composeServeServices(nil) error = %v, want ErrServeRuntimeNotConfigured", err)
	}
}

func TestServeCompositionRejectsUnsupportedCompatibleProviderWithoutNetwork(t *testing.T) {
	configuration := config.Default()
	configuration.Generation.Enabled = true
	configuration.Generation.Provider = config.ProviderOpenAICompatible
	configuration.Generation.Protocol = config.ProtocolChatCompletions
	configuration.Generation.Model = "model"
	configuration.Generation.APIKey = "secret-key"
	configuration.Generation.BaseURL = "https://example.invalid/api/v1"
	configuration.Generation.Budget.MaxRequests = 1
	configuration.Generation.Budget.MaxInputTokens = 100
	configuration.Generation.Budget.MaxOutputTokens = 100
	configuration.Generation.Budget.MaxCostUSD = 1
	_, _, err := composeGenerator(configuration)
	if !errors.Is(err, ErrServeRuntimeConfiguration) {
		t.Fatalf("composeGenerator(compatible) error = %v, want fail-closed configuration", err)
	}
	if strings.Contains(err.Error(), "secret-key") {
		t.Fatal("provider secret leaked in runtime composition error")
	}
}

func TestRuntimeEmbeddingProfileIsValidAndSecretFree(t *testing.T) {
	configuration := config.Default()
	configuration.Embedding.Enabled = true
	configuration.Embedding.Provider = config.ProviderSimulated
	configuration.Embedding.Model = "embedding-model"
	configuration.Embedding.Dimension = 8
	configuration.Embedding.MaxBatchSize = 4
	profile := aigateway.EmbeddingProfile{
		Provider: aigateway.ProviderSimulated, Model: configuration.Embedding.Model,
		Version: aigateway.EmbeddingProfileVersion, Dimension: 8, MaxBatchSize: 4,
	}
	got, err := runtimeEmbeddingProfile(configuration.Organization.ID, profile)
	if err != nil {
		t.Fatalf("runtimeEmbeddingProfile() error = %v", err)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("runtime profile validation error = %v", err)
	}
	if strings.Contains(string(got.Configuration), "secret") || strings.Contains(got.Provider, "api") {
		t.Fatalf("runtime profile contains unexpected secret-like data: %#v", got)
	}
}
