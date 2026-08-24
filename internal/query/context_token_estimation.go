package query

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"unicode/utf8"
)

const (
	// ContextTokenEstimatorVersion identifies the versioned estimator contract.
	ContextTokenEstimatorVersion = "v1alpha1"
	// ContextTokenEstimatorAlgorithm identifies the deterministic UTF-8 byte
	// approximation used by this implementation.
	ContextTokenEstimatorAlgorithm = "utf8-bytes-ceil-v1"
	// DefaultContextTokenBytesPerToken is the default ceil(bytes/4) ratio.
	DefaultContextTokenBytesPerToken = 4

	maxContextTokenBytesPerToken = 1 << 20
)

var (
	// ErrInvalidContextTokenEstimation identifies malformed estimation input,
	// configuration or audit data.
	ErrInvalidContextTokenEstimation = errors.New("query: invalid context token estimation")
	// ErrInvalidContextTokenEstimatorConfiguration identifies an unsupported or
	// incoherent estimator configuration.
	ErrInvalidContextTokenEstimatorConfiguration = ErrInvalidContextTokenEstimation
	// ErrContextTokenEstimationLimit identifies content or accounting beyond a
	// configured or representation limit.
	ErrContextTokenEstimationLimit = errors.New("query: context token estimation limit exceeded")
	// ErrContextTokenEstimationOverflow identifies unrepresentable accounting.
	ErrContextTokenEstimationOverflow = errors.New("query: context token estimation overflow")
	// ErrContextTokenEstimationContent identifies invalid UTF-8 content.
	ErrContextTokenEstimationContent = errors.New("query: invalid context token estimation content")
)

// ContextTokenEstimatorConfiguration is the versioned, reproducible
// configuration applied to one estimate. Digest excludes its own field.
type ContextTokenEstimatorConfiguration struct {
	Version       string `json:"version"`
	Algorithm     string `json:"algorithm"`
	BytesPerToken int    `json:"bytes_per_token"`
	Digest        string `json:"digest,omitempty"`
}

// DefaultContextTokenEstimatorConfiguration returns the fixed initial
// configuration with its digest populated.
func DefaultContextTokenEstimatorConfiguration() ContextTokenEstimatorConfiguration {
	return ContextTokenEstimatorConfiguration{
		Version:       ContextTokenEstimatorVersion,
		Algorithm:     ContextTokenEstimatorAlgorithm,
		BytesPerToken: DefaultContextTokenBytesPerToken,
	}.withDigest()
}

// Normalize applies only omitted fixed defaults and computes the applied
// digest. An explicit digest must match the configuration.
func (c ContextTokenEstimatorConfiguration) Normalize() (ContextTokenEstimatorConfiguration, error) {
	if c.Version == "" {
		c.Version = ContextTokenEstimatorVersion
	}
	if c.Algorithm == "" {
		c.Algorithm = ContextTokenEstimatorAlgorithm
	}
	if c.BytesPerToken == 0 {
		c.BytesPerToken = DefaultContextTokenBytesPerToken
	}
	if err := c.validateWithoutDigest(); err != nil {
		return ContextTokenEstimatorConfiguration{}, err
	}
	computed := c.withDigest()
	if c.Digest != "" && c.Digest != computed.Digest {
		return ContextTokenEstimatorConfiguration{}, fmt.Errorf("%w: digest does not match configuration", ErrInvalidContextTokenEstimatorConfiguration)
	}
	return computed, nil
}

// Validate checks configuration shape. An omitted digest is valid before
// application; Normalize supplies it when the configuration is applied.
func (c ContextTokenEstimatorConfiguration) Validate() error {
	if err := c.validateWithoutDigest(); err != nil {
		return err
	}
	if c.Digest != "" {
		if !isSHA256(c.Digest) {
			return fmt.Errorf("%w: invalid configuration digest", ErrInvalidContextTokenEstimatorConfiguration)
		}
		if c.Digest != c.withDigest().Digest {
			return fmt.Errorf("%w: digest does not match configuration", ErrInvalidContextTokenEstimatorConfiguration)
		}
	}
	return nil
}

// ConfigurationDigest returns the digest of the normalized configuration.
func (c ContextTokenEstimatorConfiguration) ConfigurationDigest() (string, error) {
	normalized, err := c.Normalize()
	if err != nil {
		return "", err
	}
	return normalized.Digest, nil
}

func (c ContextTokenEstimatorConfiguration) validateWithoutDigest() error {
	if c.Version != ContextTokenEstimatorVersion || c.Algorithm != ContextTokenEstimatorAlgorithm {
		return fmt.Errorf("%w: unsupported estimator version or algorithm", ErrInvalidContextTokenEstimatorConfiguration)
	}
	if c.BytesPerToken < 1 || c.BytesPerToken > maxContextTokenBytesPerToken {
		return fmt.Errorf("%w: bytes per token out of bounds", ErrInvalidContextTokenEstimatorConfiguration)
	}
	return nil
}

type contextTokenEstimatorDigestInput struct {
	Version       string `json:"version"`
	Algorithm     string `json:"algorithm"`
	BytesPerToken int    `json:"bytes_per_token"`
}

func (c ContextTokenEstimatorConfiguration) withDigest() ContextTokenEstimatorConfiguration {
	c.Digest = ""
	payload, _ := json.Marshal(contextTokenEstimatorDigestInput{
		Version:       c.Version,
		Algorithm:     c.Algorithm,
		BytesPerToken: c.BytesPerToken,
	})
	digest := sha256.Sum256(payload)
	c.Digest = hex.EncodeToString(digest[:])
	return c
}

// ContextTokenEstimationLimits optionally tightens the representation-wide
// limits. Zero means no additional bound; negative values are invalid.
type ContextTokenEstimationLimits struct {
	MaxTokens     int   `json:"max_tokens,omitempty"`
	MaxCharacters int64 `json:"max_characters,omitempty"`
	MaxBytes      int64 `json:"max_bytes,omitempty"`
}

func (l ContextTokenEstimationLimits) Validate() error {
	if l.MaxTokens < 0 || l.MaxCharacters < 0 || l.MaxBytes < 0 {
		return fmt.Errorf("%w: negative estimate limit", ErrContextTokenEstimationLimit)
	}
	if l.MaxTokens > maxContextTokens || l.MaxCharacters > maxContextCharacters || l.MaxBytes > maxContextBytes {
		return fmt.Errorf("%w: estimate limit exceeds context bounds", ErrContextTokenEstimationLimit)
	}
	return nil
}

// ContextTokenEstimate is the deterministic estimate of one UTF-8
// representation. ContentSHA256 hashes the exact representation counted.
type ContextTokenEstimate struct {
	TokenEstimate int                                `json:"token_estimate"`
	Characters    int64                              `json:"characters"`
	Bytes         int64                              `json:"bytes"`
	ContentSHA256 string                             `json:"content_sha256"`
	Configuration ContextTokenEstimatorConfiguration `json:"configuration"`
}

// Validate checks accounting, content digest shape and the applied
// configuration digest. It cannot verify the digest against content without
// the original representation; use ValidateAgainst for that check.
func (e ContextTokenEstimate) Validate() error {
	configuration, err := e.Configuration.Normalize()
	if err != nil {
		return err
	}
	if e.TokenEstimate < 0 || e.TokenEstimate > maxContextTokens ||
		e.Characters < 0 || e.Characters > maxContextCharacters ||
		e.Bytes < 0 || e.Bytes > maxContextBytes {
		return ErrContextTokenEstimationLimit
	}
	if !isSHA256(e.ContentSHA256) {
		return fmt.Errorf("%w: content digest", ErrInvalidContextTokenEstimation)
	}
	if e.Configuration.Digest != configuration.Digest {
		return fmt.Errorf("%w: configuration digest", ErrInvalidContextTokenEstimation)
	}
	return nil
}

// ContextProviderTokenCount is optional, provider-reported usage. The
// pointer on ContextTokenAudit distinguishes absent usage from a present
// report whose counters are all zero.
type ContextProviderTokenCount struct {
	Provider     string `json:"provider"`
	Model        string `json:"model"`
	InputTokens  int    `json:"input_tokens"`
	OutputTokens int    `json:"output_tokens"`
	TotalTokens  int    `json:"total_tokens"`
}

func (u ContextProviderTokenCount) Validate() error {
	if !validContextString(u.Provider, maxContextIdentifierBytes) || !validContextString(u.Model, maxContextIdentifierBytes) ||
		u.InputTokens < 0 || u.OutputTokens < 0 || u.TotalTokens < 0 ||
		u.InputTokens > maxContextTokens || u.OutputTokens > maxContextTokens || u.TotalTokens > maxContextTokens {
		return ErrInvalidContextTokenEstimation
	}
	if u.InputTokens > math.MaxInt-u.OutputTokens || u.TotalTokens != u.InputTokens+u.OutputTokens {
		return fmt.Errorf("%w: provider token totals are incoherent", ErrInvalidContextTokenEstimation)
	}
	return nil
}

// ContextTokenAudit separates the deterministic estimate from optional real
// provider usage. ProviderUsage never changes Estimate.
type ContextTokenAudit struct {
	Estimate      ContextTokenEstimate       `json:"estimate"`
	ProviderUsage *ContextProviderTokenCount `json:"provider_usage,omitempty"`
}

func (a ContextTokenAudit) Validate() error {
	if err := a.Estimate.Validate(); err != nil {
		return err
	}
	if a.ProviderUsage != nil {
		if err := a.ProviderUsage.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// EstimateContextTokens computes ceil(UTF-8 bytes / BytesPerToken), rune
// characters, byte length and SHA-256 content identity. It applies the
// representation-wide context bounds even when limits are zero.
func EstimateContextTokens(ctx context.Context, content string, configuration ContextTokenEstimatorConfiguration, limits ContextTokenEstimationLimits) (ContextTokenEstimate, error) {
	if ctx == nil {
		return ContextTokenEstimate{}, ErrInvalidContextTokenEstimation
	}
	if err := ctx.Err(); err != nil {
		return ContextTokenEstimate{}, err
	}
	applied, err := configuration.Normalize()
	if err != nil {
		return ContextTokenEstimate{}, err
	}
	if err := limits.Validate(); err != nil {
		return ContextTokenEstimate{}, err
	}
	if !utf8.ValidString(content) {
		return ContextTokenEstimate{}, ErrContextTokenEstimationContent
	}

	characters, err := countContextTokenRunes(ctx, content)
	if err != nil {
		return ContextTokenEstimate{}, err
	}
	bytes := int64(len(content))
	if bytes < 0 {
		return ContextTokenEstimate{}, ErrContextTokenEstimationOverflow
	}
	if bytes > maxContextBytes || characters > maxContextCharacters ||
		(limits.MaxBytes > 0 && bytes > limits.MaxBytes) ||
		(limits.MaxCharacters > 0 && characters > limits.MaxCharacters) {
		return ContextTokenEstimate{}, ErrContextTokenEstimationLimit
	}

	bytesPerToken := int64(applied.BytesPerToken)
	tokens64 := bytes / bytesPerToken
	if bytes%bytesPerToken != 0 {
		if tokens64 == math.MaxInt64 {
			return ContextTokenEstimate{}, ErrContextTokenEstimationOverflow
		}
		tokens64++
	}
	maxInt := int64(^uint(0) >> 1)
	if tokens64 > maxInt || tokens64 > int64(maxContextTokens) ||
		(limits.MaxTokens > 0 && tokens64 > int64(limits.MaxTokens)) {
		return ContextTokenEstimate{}, ErrContextTokenEstimationLimit
	}
	if err := ctx.Err(); err != nil {
		return ContextTokenEstimate{}, err
	}

	digest := sha256.Sum256([]byte(content))
	return ContextTokenEstimate{
		TokenEstimate: int(tokens64),
		Characters:    characters,
		Bytes:         bytes,
		ContentSHA256: hex.EncodeToString(digest[:]),
		Configuration: applied,
	}, nil
}

// EstimateContextTokensDefault uses the default estimator configuration.
func EstimateContextTokensDefault(ctx context.Context, content string, limits ContextTokenEstimationLimits) (ContextTokenEstimate, error) {
	return EstimateContextTokens(ctx, content, DefaultContextTokenEstimatorConfiguration(), limits)
}

func countContextTokenRunes(ctx context.Context, content string) (int64, error) {
	var characters int64
	for offset := 0; offset < len(content); {
		if offset&4095 == 0 {
			if err := ctx.Err(); err != nil {
				return 0, err
			}
		}
		runeValue, width := utf8.DecodeRuneInString(content[offset:])
		if runeValue == utf8.RuneError && width == 1 {
			return 0, ErrContextTokenEstimationContent
		}
		if characters == math.MaxInt64 {
			return 0, ErrContextTokenEstimationOverflow
		}
		characters++
		offset += width
	}
	return characters, nil
}

// CanonicalContextItemJSON returns deterministic JSON for one valid item.
func CanonicalContextItemJSON(item ContextItem) ([]byte, error) {
	if err := item.Validate(); err != nil {
		return nil, fmt.Errorf("%w: context item: %v", ErrInvalidContextTokenEstimation, err)
	}
	return json.Marshal(canonicalContextItem(item))
}

// CanonicalContextRelationJSON returns deterministic JSON for one valid
// relation. Path order is preserved; support collections are sorted.
func CanonicalContextRelationJSON(relation ContextRelation) ([]byte, error) {
	if err := relation.Validate(); err != nil {
		return nil, fmt.Errorf("%w: context relation: %v", ErrInvalidContextTokenEstimation, err)
	}
	return json.Marshal(canonicalContextRelation(relation))
}

// ContextItemContentSHA256 returns the hash of canonical item JSON.
func ContextItemContentSHA256(item ContextItem) (string, error) {
	encoded, err := CanonicalContextItemJSON(item)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

// ContextRelationContentSHA256 returns the hash of canonical relation JSON.
func ContextRelationContentSHA256(relation ContextRelation) (string, error) {
	encoded, err := CanonicalContextRelationJSON(relation)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func canonicalContextItem(item ContextItem) ContextItem {
	clone := cloneContextItem(item)
	sort.Strings(clone.SupportIDs)
	clone.Provenance = canonicalContextProvenance(clone.Provenance)
	if clone.Fact != nil {
		sort.SliceStable(clone.Fact.Qualifiers, func(i, j int) bool {
			left, _ := json.Marshal(clone.Fact.Qualifiers[i])
			right, _ := json.Marshal(clone.Fact.Qualifiers[j])
			return string(left) < string(right)
		})
		sort.SliceStable(clone.Fact.Evidence, func(i, j int) bool {
			return clone.Fact.Evidence[i].ID < clone.Fact.Evidence[j].ID
		})
	}
	if clone.Evidence != nil {
		sort.Strings(clone.Evidence.Findings)
	}
	return clone
}

func canonicalContextRelation(relation ContextRelation) ContextRelation {
	clone := cloneContextRelation(relation)
	sort.Strings(clone.SupportIDs)
	clone.Provenance = canonicalContextProvenance(clone.Provenance)
	return clone
}

func canonicalContextProvenance(provenance ContextProvenance) ContextProvenance {
	if provenance.Lineage != nil {
		sort.Strings(provenance.Lineage.InputFactIDs)
	}
	sort.SliceStable(provenance.Evidence, func(i, j int) bool {
		return provenance.Evidence[i].ID < provenance.Evidence[j].ID
	})
	return provenance
}

// EstimateContextItemTokens estimates canonical item JSON.
func EstimateContextItemTokens(ctx context.Context, item ContextItem, configuration ContextTokenEstimatorConfiguration, limits ContextTokenEstimationLimits) (ContextTokenEstimate, error) {
	if ctx == nil {
		return ContextTokenEstimate{}, ErrInvalidContextTokenEstimation
	}
	if err := ctx.Err(); err != nil {
		return ContextTokenEstimate{}, err
	}
	encoded, err := CanonicalContextItemJSON(item)
	if err != nil {
		return ContextTokenEstimate{}, err
	}
	return EstimateContextTokens(ctx, string(encoded), configuration, limits)
}

// EstimateContextRelationTokens estimates canonical relation JSON.
func EstimateContextRelationTokens(ctx context.Context, relation ContextRelation, configuration ContextTokenEstimatorConfiguration, limits ContextTokenEstimationLimits) (ContextTokenEstimate, error) {
	if ctx == nil {
		return ContextTokenEstimate{}, ErrInvalidContextTokenEstimation
	}
	if err := ctx.Err(); err != nil {
		return ContextTokenEstimate{}, err
	}
	encoded, err := CanonicalContextRelationJSON(relation)
	if err != nil {
		return ContextTokenEstimate{}, err
	}
	return EstimateContextTokens(ctx, string(encoded), configuration, limits)
}

// EstimateContextSelectionCandidateCosts computes and fills all candidate
// cost spellings from the canonical item representation.
func EstimateContextSelectionCandidateCosts(ctx context.Context, candidate *ContextSelectionCandidate, configuration ContextTokenEstimatorConfiguration, limits ContextTokenEstimationLimits) (ContextTokenEstimate, error) {
	if candidate == nil {
		return ContextTokenEstimate{}, ErrInvalidContextTokenEstimation
	}
	estimate, err := EstimateContextItemTokens(ctx, candidate.Item, configuration, limits)
	if err != nil {
		return ContextTokenEstimate{}, err
	}
	candidate.TokenCost = estimate.TokenEstimate
	candidate.CharacterCost = estimate.Characters
	candidate.ByteCost = estimate.Bytes
	candidate.Tokens = estimate.TokenEstimate
	candidate.Characters = estimate.Characters
	candidate.Bytes = estimate.Bytes
	return estimate, nil
}

// EstimateContextRelationCandidateCosts computes and fills all relation
// candidate cost spellings from canonical relation representation.
func EstimateContextRelationCandidateCosts(ctx context.Context, candidate *ContextRelationCandidate, configuration ContextTokenEstimatorConfiguration, limits ContextTokenEstimationLimits) (ContextTokenEstimate, error) {
	if candidate == nil {
		return ContextTokenEstimate{}, ErrInvalidContextTokenEstimation
	}
	estimate, err := EstimateContextRelationTokens(ctx, candidate.Relation, configuration, limits)
	if err != nil {
		return ContextTokenEstimate{}, err
	}
	candidate.TokenCost = estimate.TokenEstimate
	candidate.CharacterCost = estimate.Characters
	candidate.ByteCost = estimate.Bytes
	candidate.Tokens = estimate.TokenEstimate
	candidate.Characters = estimate.Characters
	candidate.Bytes = estimate.Bytes
	return estimate, nil
}

// NewContextTokenAudit estimates content and attaches optional provider
// usage without changing the deterministic estimate.
func NewContextTokenAudit(ctx context.Context, content string, configuration ContextTokenEstimatorConfiguration, limits ContextTokenEstimationLimits, providerUsage *ContextProviderTokenCount) (ContextTokenAudit, error) {
	estimate, err := EstimateContextTokens(ctx, content, configuration, limits)
	if err != nil {
		return ContextTokenAudit{}, err
	}
	var providerCopy *ContextProviderTokenCount
	if providerUsage != nil {
		copyOfProvider := *providerUsage
		providerCopy = &copyOfProvider
	}
	audit := ContextTokenAudit{Estimate: estimate, ProviderUsage: providerCopy}
	if err := audit.Validate(); err != nil {
		return ContextTokenAudit{}, err
	}
	return audit, nil
}

// ValidateAgainst recomputes the estimate from content and rejects content,
// configuration or digest mismatches. Provider usage remains independent.
func (a ContextTokenAudit) ValidateAgainst(content string, expectedConfiguration ContextTokenEstimatorConfiguration, limits ContextTokenEstimationLimits) error {
	if err := a.Validate(); err != nil {
		return err
	}
	expected, err := EstimateContextTokens(context.Background(), content, expectedConfiguration, limits)
	if err != nil {
		return err
	}
	if a.Estimate != expected {
		return fmt.Errorf("%w: estimate differs from content or configuration", ErrInvalidContextTokenEstimation)
	}
	return nil
}

// ValidateAgainst recomputes this estimate from content and expected
// configuration.
func (e ContextTokenEstimate) ValidateAgainst(content string, expectedConfiguration ContextTokenEstimatorConfiguration, limits ContextTokenEstimationLimits) error {
	if err := e.Validate(); err != nil {
		return err
	}
	expected, err := EstimateContextTokens(context.Background(), content, expectedConfiguration, limits)
	if err != nil {
		return err
	}
	if e != expected {
		return fmt.Errorf("%w: estimate differs from content or configuration", ErrInvalidContextTokenEstimation)
	}
	return nil
}
