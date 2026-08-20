package cli

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/pedrogpaulino/manu/internal/aigateway"
	"github.com/pedrogpaulino/manu/internal/config"
)

// EvalConfigLoader is the configuration seam for live evaluation. The
// production loader reads the typed environment configuration; tests inject
// an in-memory value and never need process-wide credentials.
type EvalConfigLoader func() (config.Config, error)

// LiveGeneratorFactory builds the one explicitly configured generation
// adapter. It receives the typed configuration, not CLI provider/model flags,
// so there is one source of truth for endpoint, protocol and credentials.
type LiveGeneratorFactory func(config.Config) (aigateway.Generator, error)

const maxLiveGenerationProfileBytes = 256 << 10

func newProductionLiveGenerator(configuration config.Config) (aigateway.Generator, error) {
	return newLiveGeneratorWithHTTPClient(configuration, nil)
}

// newLiveGeneratorWithHTTPClient is the transport-injection seam used by
// adapter tests. The production path passes nil and therefore uses the
// adapter's normal guarded HTTP client only after the user explicitly opts in.
func newLiveGeneratorWithHTTPClient(configuration config.Config, httpClient *http.Client) (aigateway.Generator, error) {
	generation := configuration.Generation
	if !generation.Enabled || generation.Provider == config.ProviderSimulated {
		return nil, errors.New("evaluation: live generation requires an enabled external provider")
	}
	if generation.MaxOutputTokens <= 0 || generation.MaxOutputTokens > maxLiveGenerationProfileBytes/4 {
		return nil, errors.New("evaluation: live generation output limit is invalid")
	}
	switch generation.Provider {
	case config.ProviderOpenAI:
		return aigateway.NewOpenAIAdapter(aigateway.OpenAIConfig{
			BaseURL:         generation.BaseURL,
			APIKey:          generation.APIKey,
			HTTPClient:      httpClient,
			MaxOutputTokens: generation.MaxOutputTokens,
		})
	case config.ProviderOpenRouter:
		return aigateway.NewOpenAICompatibleAdapter(aigateway.OpenAICompatibleConfig{
			Dialect:           aigateway.OpenRouterDialect,
			BaseURL:           generation.BaseURL,
			APIKey:            generation.APIKey,
			HTTPClient:        httpClient,
			MaxOutputTokens:   generation.MaxOutputTokens,
			StructuredOutputs: true,
		})
	case config.ProviderOpenAICompatible:
		// The current compatible adapter has an explicit OpenRouter dialect.
		// Do not silently treat an arbitrary compatible endpoint as OpenRouter.
		return nil, errors.New("evaluation: generic openai-compatible live dialect is not implemented")
	default:
		return nil, errors.New("evaluation: configured live provider is unsupported")
	}
}

func liveGenerationProfile(configuration config.Config) (aigateway.GenerationProfile, error) {
	generation := configuration.Generation
	if !generation.Enabled || generation.Provider == config.ProviderSimulated {
		return aigateway.GenerationProfile{}, errors.New("evaluation: live generation requires an enabled external provider")
	}
	if generation.MaxOutputTokens <= 0 || generation.MaxOutputTokens > maxLiveGenerationProfileBytes/4 {
		return aigateway.GenerationProfile{}, errors.New("evaluation: live generation output limit is invalid")
	}
	profile := aigateway.GenerationProfile{
		Provider:       aigateway.Provider(generation.Provider),
		Model:          generation.Model,
		Version:        aigateway.GenerationProfileVersion,
		Protocol:       aigateway.Protocol(generation.Protocol),
		MaxOutputBytes: generation.MaxOutputTokens * 4,
	}
	if err := profile.Validate(); err != nil {
		return aigateway.GenerationProfile{}, fmt.Errorf("evaluation: live generation profile is invalid: %w", err)
	}
	return profile, nil
}
