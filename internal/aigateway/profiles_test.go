package aigateway

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func profileEmbeddingForIdentityTests() EmbeddingProfile {
	return EmbeddingProfile{
		Provider:     ProviderSimulated,
		Model:        "embedding-v1",
		Version:      EmbeddingProfileVersion,
		Dimension:    8,
		Normalize:    true,
		MaxBatchSize: 8,
	}
}

func profileGenerationForIdentityTests() GenerationProfile {
	return GenerationProfile{
		Provider:       ProviderSimulated,
		Model:          "generator-v1",
		Version:        GenerationProfileVersion,
		Protocol:       ProtocolResponses,
		MaxOutputBytes: 1024,
	}
}

func TestEmbeddingIdentityExcludesOperationalBatching(t *testing.T) {
	profile := profileEmbeddingForIdentityTests()
	identity := profile.Identity()
	identityJSON, err := json.Marshal(identity)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(identityJSON), "max_batch_size") {
		t.Fatalf("embedding identity includes max batch size: %s", identityJSON)
	}

	changed := profile
	changed.MaxBatchSize++
	if changed.CacheKey() != profile.CacheKey() {
		t.Fatal("changing only max batch size changed embedding cache identity")
	}
	if changed.RequiresRebuild(profile) {
		t.Fatal("changing only max batch size requires an embedding rebuild")
	}
}

func TestEmbeddingIdentityRequiresRebuildForSemanticChanges(t *testing.T) {
	base := profileEmbeddingForIdentityTests()
	tests := []struct {
		name string
		edit func(*EmbeddingProfile)
	}{
		{name: "provider", edit: func(profile *EmbeddingProfile) { profile.Provider = ProviderOpenAI }},
		{name: "model", edit: func(profile *EmbeddingProfile) { profile.Model = "embedding-v2" }},
		{name: "version", edit: func(profile *EmbeddingProfile) { profile.Version = "v2" }},
		{name: "dimension", edit: func(profile *EmbeddingProfile) { profile.Dimension = 16 }},
		{name: "normalization", edit: func(profile *EmbeddingProfile) { profile.Normalize = !profile.Normalize }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			changed := base
			test.edit(&changed)
			if changed.CacheKey() == base.CacheKey() {
				t.Fatal("semantic embedding profile change did not change cache key")
			}
			if !changed.RequiresRebuild(base) {
				t.Fatal("semantic embedding profile change did not require rebuild")
			}
			if err := (CapabilityProfiles{Embedding: changed, Generation: profileGenerationForIdentityTests()}).CheckEmbeddingCompatibility(CapabilityProfiles{Embedding: base, Generation: profileGenerationForIdentityTests()}); !errors.Is(err, ErrEmbeddingRebuildRequired) {
				t.Fatalf("compatibility error = %v, want rebuild required", err)
			}
		})
	}
}

func TestGenerationChangesDoNotInvalidateEmbeddingIdentity(t *testing.T) {
	embedding := profileEmbeddingForIdentityTests()
	previous := CapabilityProfiles{Embedding: embedding, Generation: profileGenerationForIdentityTests()}
	changes := []func(*GenerationProfile){
		func(profile *GenerationProfile) {
			profile.Provider = ProviderOpenRouter
			profile.Protocol = ProtocolChatCompletions
		},
		func(profile *GenerationProfile) { profile.Model = "generator-v2" },
		func(profile *GenerationProfile) { profile.Version = "v2" },
		func(profile *GenerationProfile) { profile.MaxOutputBytes++ },
	}
	for index, change := range changes {
		current := previous
		change(&current.Generation)
		if current.EmbeddingCacheKey() != previous.EmbeddingCacheKey() {
			t.Fatalf("change %d altered embedding cache key", index)
		}
		if current.RequiresEmbeddingRebuild(previous) {
			t.Fatalf("change %d required embedding rebuild", index)
		}
		if err := current.CheckEmbeddingCompatibility(previous); err != nil {
			t.Fatalf("change %d compatibility error = %v", index, err)
		}
	}
}

func TestGenerationProfileProtocolMatrixHasNoFallback(t *testing.T) {
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
			profile := profileGenerationForIdentityTests()
			profile.Provider = test.provider
			profile.Protocol = test.protocol
			err := profile.Validate()
			if test.valid && err != nil {
				t.Fatalf("Validate() error = %v, want nil", err)
			}
			if !test.valid && err == nil {
				t.Fatal("Validate() error = nil, want an explicit capability/configuration error")
			}
		})
	}
}

func TestCapabilityProfilesContainNoTransportOrCredentialIdentity(t *testing.T) {
	profile := profileEmbeddingForIdentityTests()
	identityJSON, err := json.Marshal(profile.Identity())
	if err != nil {
		t.Fatal(err)
	}
	identity := string(identityJSON)
	for _, forbidden := range []string{"api_key", "base_url", "transport", "super-secret"} {
		if strings.Contains(identity, forbidden) {
			t.Fatalf("embedding identity contains %q: %s", forbidden, identity)
		}
	}
	if strings.Contains(profile.CacheKey(), "super-secret") {
		t.Fatal("embedding cache key contains credential text")
	}
}
