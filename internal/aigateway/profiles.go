package aigateway

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
)

var (
	// ErrEmbeddingRebuildRequired prevents a caller from reusing a projection
	// after changing the immutable embedding profile.
	ErrEmbeddingRebuildRequired = errors.New("embedding profile changed; rebuild required")
)

// EmbeddingProfileIdentity is the non-secret, comparable identity of the
// vector space and its bounded profile configuration. Generation settings are
// deliberately absent: changing a generator must not invalidate embeddings.
type EmbeddingProfileIdentity struct {
	Provider  Provider `json:"provider"`
	Model     string   `json:"model"`
	Version   string   `json:"version"`
	Dimension int      `json:"dimension"`
	Normalize bool     `json:"normalize"`
}

// Identity returns a copy of the fields that define embedding cache
// compatibility. Credentials and transport-only settings are not part of an
// embedding profile and therefore cannot cause a cache identity leak.
func (p EmbeddingProfile) Identity() EmbeddingProfileIdentity {
	return EmbeddingProfileIdentity{
		Provider:  p.Provider,
		Model:     p.Model,
		Version:   p.Version,
		Dimension: p.Dimension,
		Normalize: p.Normalize,
	}
}

// CacheKey returns a stable digest for one embedding profile. Callers should
// validate the profile before persisting or using the key; the method itself
// remains total so incompatible profiles can be compared and rejected before
// a rebuild is attempted.
func (p EmbeddingProfile) CacheKey() string {
	encoded, _ := json.Marshal(p.Identity())
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

// RequiresRebuild reports whether the embedding cache can be reused for a
// new profile. Every identity change is conservative and requires a complete
// rebuild of the affected projection.
func (p EmbeddingProfile) RequiresRebuild(previous EmbeddingProfile) bool {
	return p.CacheKey() != previous.CacheKey()
}

// CapabilityProfiles groups the independent gateway profiles without making
// generation part of embedding cache identity.
type CapabilityProfiles struct {
	Embedding  EmbeddingProfile  `json:"embedding"`
	Generation GenerationProfile `json:"generation"`
}

// Validate validates both capability profiles and their provider/protocol
// combinations without contacting a provider.
func (p CapabilityProfiles) Validate() error {
	if err := p.Embedding.Validate(); err != nil {
		return fmt.Errorf("embedding profile: %w", err)
	}
	if err := p.Generation.Validate(); err != nil {
		return fmt.Errorf("generation profile: %w", err)
	}
	return nil
}

// EmbeddingCacheKey returns only the embedding profile key. Generation model,
// protocol and version changes therefore leave the key unchanged.
func (p CapabilityProfiles) EmbeddingCacheKey() string {
	return p.Embedding.CacheKey()
}

// RequiresEmbeddingRebuild reports whether the new capability selection needs
// a new vector projection. Generation changes are intentionally ignored.
func (p CapabilityProfiles) RequiresEmbeddingRebuild(previous CapabilityProfiles) bool {
	return p.Embedding.RequiresRebuild(previous.Embedding)
}

// CheckEmbeddingCompatibility rejects cache reuse when the embedding profile
// changed. A caller must rebuild before publishing vectors for the new key.
func (p CapabilityProfiles) CheckEmbeddingCompatibility(previous CapabilityProfiles) error {
	if p.RequiresEmbeddingRebuild(previous) {
		return ErrEmbeddingRebuildRequired
	}
	return nil
}

func validateGenerationProtocol(provider Provider, protocol Protocol) error {
	switch provider {
	case ProviderOpenAI:
		if protocol != ProtocolResponses {
			return fmt.Errorf("openai requires responses protocol: %w", ErrCapability)
		}
	case ProviderOpenRouter:
		if protocol != ProtocolChatCompletions {
			return fmt.Errorf("openrouter requires chat completions protocol: %w", ErrCapability)
		}
	case ProviderOpenAICompatible:
		// The compatible adapter implemented in this slice uses the chat
		// completions wire contract. A caller must select it explicitly; no
		// protocol fallback is allowed.
		if protocol != ProtocolChatCompletions {
			return fmt.Errorf("openai-compatible requires chat completions protocol: %w", ErrCapability)
		}
	case ProviderSimulated:
		// The deterministic simulator supports both internal protocol
		// contracts, but the caller must still choose one explicitly. The
		// caller's enum validation below rejects the zero/unknown value.
		if protocol != ProtocolResponses && protocol != ProtocolChatCompletions {
			return fmt.Errorf("simulated protocol is unsupported: %w", ErrCapability)
		}
	default:
		return fmt.Errorf("provider has no supported generation protocol: %w", ErrCapability)
	}
	return nil
}
