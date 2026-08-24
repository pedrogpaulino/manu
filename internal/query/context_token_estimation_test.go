package query

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestEstimateContextTokensUsesUTF8BytesAndCeiling(t *testing.T) {
	t.Parallel()

	configuration := DefaultContextTokenEstimatorConfiguration()
	tests := []struct {
		name       string
		content    string
		characters int64
		bytes      int64
		tokens     int
	}{
		{name: "empty", content: "", characters: 0, bytes: 0, tokens: 0},
		{name: "ascii", content: "hello", characters: 5, bytes: 5, tokens: 2},
		{name: "multibyte", content: "ação", characters: 4, bytes: 6, tokens: 2},
		{name: "four-byte-rune", content: "🙂", characters: 1, bytes: 4, tokens: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			estimate, err := EstimateContextTokens(context.Background(), tt.content, configuration, ContextTokenEstimationLimits{})
			if err != nil {
				t.Fatalf("EstimateContextTokens() error = %v", err)
			}
			if estimate.TokenEstimate != tt.tokens {
				t.Fatalf("tokens = %d, want %d", estimate.TokenEstimate, tt.tokens)
			}
			if estimate.Characters != tt.characters {
				t.Fatalf("characters = %d, want %d", estimate.Characters, tt.characters)
			}
			if estimate.Bytes != tt.bytes {
				t.Fatalf("bytes = %d, want %d", estimate.Bytes, tt.bytes)
			}
			if err := estimate.Validate(); err != nil {
				t.Fatalf("estimate.Validate() error = %v", err)
			}
		})
	}
}

func TestEstimateContextTokensIsDeterministicAndDigestable(t *testing.T) {
	t.Parallel()

	content := "deterministic ação"
	configuration := DefaultContextTokenEstimatorConfiguration()
	first, err := EstimateContextTokens(context.Background(), content, configuration, ContextTokenEstimationLimits{})
	if err != nil {
		t.Fatalf("first EstimateContextTokens() error = %v", err)
	}
	second, err := EstimateContextTokens(context.Background(), content, configuration, ContextTokenEstimationLimits{})
	if err != nil {
		t.Fatalf("second EstimateContextTokens() error = %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("repeated estimate changed:\nfirst=%#v\nsecond=%#v", first, second)
	}

	digest := sha256.Sum256([]byte(content))
	if first.ContentSHA256 != hex.EncodeToString(digest[:]) {
		t.Fatalf("content digest = %q, want SHA-256 of content", first.ContentSHA256)
	}
	configurationDigest, err := configuration.ConfigurationDigest()
	if err != nil {
		t.Fatalf("ConfigurationDigest() error = %v", err)
	}
	if first.Configuration.Digest != configurationDigest {
		t.Fatalf("estimate configuration digest = %q, want %q", first.Configuration.Digest, configurationDigest)
	}

	changed, err := EstimateContextTokens(context.Background(), content+"!", configuration, ContextTokenEstimationLimits{})
	if err != nil {
		t.Fatalf("changed EstimateContextTokens() error = %v", err)
	}
	if changed.ContentSHA256 == first.ContentSHA256 {
		t.Fatal("different content reused the original content digest")
	}
}

func TestContextTokenEstimatorRejectsInvalidOrIncompatibleConfiguration(t *testing.T) {
	t.Parallel()

	defaultConfiguration := DefaultContextTokenEstimatorConfiguration()
	otherConfiguration := ContextTokenEstimatorConfiguration{
		Version:       ContextTokenEstimatorVersion,
		Algorithm:     ContextTokenEstimatorAlgorithm,
		BytesPerToken: 2,
	}
	normalizedOther, err := otherConfiguration.Normalize()
	if err != nil {
		t.Fatalf("other configuration Normalize() error = %v", err)
	}
	if err := normalizedOther.Validate(); err != nil {
		t.Fatalf("other configuration Validate() error = %v", err)
	}
	if defaultConfiguration.Digest == normalizedOther.Digest {
		t.Fatal("incompatible valid estimators share a digest")
	}

	defaultEstimate, err := EstimateContextTokens(context.Background(), "12345", defaultConfiguration, ContextTokenEstimationLimits{})
	if err != nil {
		t.Fatalf("default EstimateContextTokens() error = %v", err)
	}
	otherEstimate, err := EstimateContextTokens(context.Background(), "12345", normalizedOther, ContextTokenEstimationLimits{})
	if err != nil {
		t.Fatalf("other EstimateContextTokens() error = %v", err)
	}
	if defaultEstimate.TokenEstimate == otherEstimate.TokenEstimate {
		t.Fatalf("incompatible estimators produced the same token count: %d", defaultEstimate.TokenEstimate)
	}

	badDigest := defaultConfiguration
	badDigest.Digest = strings.Repeat("0", 64)
	if _, err := badDigest.Normalize(); !errors.Is(err, ErrInvalidContextTokenEstimatorConfiguration) {
		t.Fatalf("bad configuration digest error = %v, want incompatible digest", err)
	}
	badVersion := defaultConfiguration
	badVersion.Version = "v2"
	if _, err := EstimateContextTokens(context.Background(), "content", badVersion, ContextTokenEstimationLimits{}); !errors.Is(err, ErrInvalidContextTokenEstimatorConfiguration) {
		t.Fatalf("bad configuration version error = %v, want incompatible configuration", err)
	}
}

func TestEstimateContextTokensHonorsLimitsAndLargeContentBounds(t *testing.T) {
	t.Parallel()

	configuration := DefaultContextTokenEstimatorConfiguration()
	tests := []struct {
		name    string
		content string
		limits  ContextTokenEstimationLimits
	}{
		{name: "tokens", content: "hello", limits: ContextTokenEstimationLimits{MaxTokens: 1}},
		{name: "characters", content: "🙂🙂", limits: ContextTokenEstimationLimits{MaxCharacters: 1}},
		{name: "bytes", content: "hello", limits: ContextTokenEstimationLimits{MaxBytes: 4}},
		{name: "negative", content: "hello", limits: ContextTokenEstimationLimits{MaxBytes: -1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := EstimateContextTokens(context.Background(), tt.content, configuration, tt.limits)
			if !errors.Is(err, ErrContextTokenEstimationLimit) {
				t.Fatalf("EstimateContextTokens() error = %v, want limit error", err)
			}
		})
	}

	large := strings.Repeat("x", int(maxContextBytes))
	largeConfiguration := ContextTokenEstimatorConfiguration{
		Version:       ContextTokenEstimatorVersion,
		Algorithm:     ContextTokenEstimatorAlgorithm,
		BytesPerToken: DefaultContextTokenBytesPerToken * 4,
	}
	estimate, err := EstimateContextTokens(context.Background(), large, largeConfiguration, ContextTokenEstimationLimits{})
	if err != nil {
		t.Fatalf("maximum-sized EstimateContextTokens() error = %v", err)
	}
	if estimate.Bytes != maxContextBytes || estimate.Characters != maxContextBytes || estimate.TokenEstimate != maxContextTokens {
		t.Fatalf("maximum-sized estimate = bytes:%d chars:%d tokens:%d, want %d/%d/%d", estimate.Bytes, estimate.Characters, estimate.TokenEstimate, maxContextBytes, maxContextBytes, maxContextTokens)
	}

	_, err = EstimateContextTokens(context.Background(), large+"x", largeConfiguration, ContextTokenEstimationLimits{})
	if !errors.Is(err, ErrContextTokenEstimationLimit) {
		t.Fatalf("above-maximum EstimateContextTokens() error = %v, want limit error", err)
	}
}

func TestEstimateContextTokensRejectsInvalidContentAndContext(t *testing.T) {
	t.Parallel()

	configuration := DefaultContextTokenEstimatorConfiguration()
	invalidUTF8 := string([]byte{0xff, 0xfe, 0xfd})
	if _, err := EstimateContextTokens(context.Background(), invalidUTF8, configuration, ContextTokenEstimationLimits{}); !errors.Is(err, ErrContextTokenEstimationContent) {
		t.Fatalf("invalid UTF-8 error = %v, want content error", err)
	}
	if _, err := EstimateContextTokens(nil, "content", configuration, ContextTokenEstimationLimits{}); !errors.Is(err, ErrInvalidContextTokenEstimation) {
		t.Fatalf("nil context error = %v, want invalid estimation", err)
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := EstimateContextTokens(canceled, "content", configuration, ContextTokenEstimationLimits{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled context error = %v, want context.Canceled", err)
	}
}

func TestContextTokenAuditSeparatesProviderUsageFromEstimate(t *testing.T) {
	t.Parallel()

	configuration := DefaultContextTokenEstimatorConfiguration()
	content := "hello"
	expected, err := EstimateContextTokens(context.Background(), content, configuration, ContextTokenEstimationLimits{})
	if err != nil {
		t.Fatalf("EstimateContextTokens() error = %v", err)
	}

	withoutProvider, err := NewContextTokenAudit(context.Background(), content, configuration, ContextTokenEstimationLimits{}, nil)
	if err != nil {
		t.Fatalf("audit without provider error = %v", err)
	}
	if withoutProvider.ProviderUsage != nil {
		t.Fatal("absent provider usage was represented as a zero-valued report")
	}
	if !reflect.DeepEqual(withoutProvider.Estimate, expected) {
		t.Fatalf("audit estimate = %#v, want %#v", withoutProvider.Estimate, expected)
	}

	zeroProvider := ContextProviderTokenCount{Provider: "provider", Model: "model"}
	withZeroProvider, err := NewContextTokenAudit(context.Background(), content, configuration, ContextTokenEstimationLimits{}, &zeroProvider)
	if err != nil {
		t.Fatalf("zero provider audit error = %v", err)
	}
	if withZeroProvider.ProviderUsage == nil || *withZeroProvider.ProviderUsage != zeroProvider {
		t.Fatalf("zero provider usage = %#v, want present zero report", withZeroProvider.ProviderUsage)
	}
	if !reflect.DeepEqual(withZeroProvider.Estimate, expected) {
		t.Fatalf("zero provider replaced estimate = %#v, want %#v", withZeroProvider.Estimate, expected)
	}

	provider := ContextProviderTokenCount{Provider: "provider", Model: "model", InputTokens: 3, OutputTokens: 2, TotalTokens: 5}
	withProvider, err := NewContextTokenAudit(context.Background(), content, configuration, ContextTokenEstimationLimits{}, &provider)
	if err != nil {
		t.Fatalf("provider audit error = %v", err)
	}
	if withProvider.ProviderUsage == nil || *withProvider.ProviderUsage != provider {
		t.Fatalf("provider usage = %#v, want %#v", withProvider.ProviderUsage, provider)
	}
	if !reflect.DeepEqual(withProvider.Estimate, expected) {
		t.Fatalf("provider replaced estimate = %#v, want %#v", withProvider.Estimate, expected)
	}

	invalidProvider := provider
	invalidProvider.TotalTokens++
	if _, err := NewContextTokenAudit(context.Background(), content, configuration, ContextTokenEstimationLimits{}, &invalidProvider); !errors.Is(err, ErrInvalidContextTokenEstimation) {
		t.Fatalf("incoherent provider total error = %v, want invalid audit", err)
	}
}

func TestContextTokenAuditValidateAndValidateAgainstRejectAdulteration(t *testing.T) {
	t.Parallel()

	content := "validate ação"
	configuration := DefaultContextTokenEstimatorConfiguration()
	provider := ContextProviderTokenCount{Provider: "provider", Model: "model", InputTokens: 4, OutputTokens: 1, TotalTokens: 5}
	audit, err := NewContextTokenAudit(context.Background(), content, configuration, ContextTokenEstimationLimits{}, &provider)
	if err != nil {
		t.Fatalf("NewContextTokenAudit() error = %v", err)
	}
	if err := audit.Validate(); err != nil {
		t.Fatalf("audit.Validate() error = %v", err)
	}
	if err := audit.ValidateAgainst(content, configuration, ContextTokenEstimationLimits{}); err != nil {
		t.Fatalf("audit.ValidateAgainst() error = %v", err)
	}
	if err := audit.Estimate.ValidateAgainst(content, configuration, ContextTokenEstimationLimits{}); err != nil {
		t.Fatalf("estimate.ValidateAgainst() error = %v", err)
	}

	tests := []struct {
		name          string
		validateAlone bool
		mutate        func(*ContextTokenAudit)
	}{
		{name: "token count", mutate: func(value *ContextTokenAudit) { value.Estimate.TokenEstimate++ }},
		{name: "character count", mutate: func(value *ContextTokenAudit) { value.Estimate.Characters++ }},
		{name: "byte count", mutate: func(value *ContextTokenAudit) { value.Estimate.Bytes++ }},
		{name: "content digest", mutate: func(value *ContextTokenAudit) { value.Estimate.ContentSHA256 = strings.Repeat("0", 64) }},
		{name: "configuration digest", validateAlone: true, mutate: func(value *ContextTokenAudit) { value.Estimate.Configuration.Digest = strings.Repeat("0", 64) }},
		{name: "provider total", validateAlone: true, mutate: func(value *ContextTokenAudit) { value.ProviderUsage.TotalTokens++ }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mutated := audit
			if audit.ProviderUsage != nil {
				providerCopy := *audit.ProviderUsage
				mutated.ProviderUsage = &providerCopy
			}
			tt.mutate(&mutated)
			if tt.validateAlone {
				if err := mutated.Validate(); err == nil {
					t.Fatalf("Validate() accepted adulterated %s", tt.name)
				}
			}
			if err := mutated.ValidateAgainst(content, configuration, ContextTokenEstimationLimits{}); err == nil {
				t.Fatalf("ValidateAgainst() accepted adulterated %s", tt.name)
			}
		})
	}

	otherConfiguration := configuration
	otherConfiguration.BytesPerToken = 2
	if err := audit.ValidateAgainst(content, otherConfiguration, ContextTokenEstimationLimits{}); err == nil {
		t.Fatal("ValidateAgainst() accepted a valid but incompatible estimator")
	}
	if err := audit.ValidateAgainst(content+"!", configuration, ContextTokenEstimationLimits{}); err == nil {
		t.Fatal("ValidateAgainst() accepted different content")
	}

	mutatedEstimate := audit.Estimate
	mutatedEstimate.ContentSHA256 = strings.Repeat("1", 64)
	if err := mutatedEstimate.ValidateAgainst(content, configuration, ContextTokenEstimationLimits{}); err == nil {
		t.Fatal("estimate.ValidateAgainst() accepted an adulterated content digest")
	}
}

func TestContextTokenCanonicalJSONCostsApplyToSelectionAndRelationCandidates(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	configuration := DefaultContextTokenEstimatorConfiguration()
	packageFixture := contextTestPackage()
	item := cloneContextItem(packageFixture.Items[1])
	relation := cloneContextRelation(packageFixture.Relations[0])

	itemJSON, err := CanonicalContextItemJSON(item)
	if err != nil {
		t.Fatalf("CanonicalContextItemJSON() error = %v", err)
	}
	itemEstimate, err := EstimateContextItemTokens(ctx, item, configuration, ContextTokenEstimationLimits{})
	if err != nil {
		t.Fatalf("EstimateContextItemTokens() error = %v", err)
	}
	itemFromJSON, err := EstimateContextTokens(ctx, string(itemJSON), configuration, ContextTokenEstimationLimits{})
	if err != nil {
		t.Fatalf("EstimateContextTokens(item JSON) error = %v", err)
	}
	if !reflect.DeepEqual(itemEstimate, itemFromJSON) {
		t.Fatalf("item estimate differs from canonical JSON estimate:\nitem=%#v\njson=%#v", itemEstimate, itemFromJSON)
	}

	itemCandidate := ContextSelectionCandidate{Item: item}
	itemCandidateEstimate, err := EstimateContextSelectionCandidateCosts(ctx, &itemCandidate, configuration, ContextTokenEstimationLimits{})
	if err != nil {
		t.Fatalf("EstimateContextSelectionCandidateCosts() error = %v", err)
	}
	if !reflect.DeepEqual(itemCandidateEstimate, itemEstimate) {
		t.Fatalf("candidate estimate = %#v, want %#v", itemCandidateEstimate, itemEstimate)
	}
	if itemCandidate.TokenCost != itemEstimate.TokenEstimate || itemCandidate.CharacterCost != itemEstimate.Characters || itemCandidate.ByteCost != itemEstimate.Bytes {
		t.Fatalf("selection candidate costs = %d/%d/%d, want %d/%d/%d", itemCandidate.TokenCost, itemCandidate.CharacterCost, itemCandidate.ByteCost, itemEstimate.TokenEstimate, itemEstimate.Characters, itemEstimate.Bytes)
	}

	relationJSON, err := CanonicalContextRelationJSON(relation)
	if err != nil {
		t.Fatalf("CanonicalContextRelationJSON() error = %v", err)
	}
	relationEstimate, err := EstimateContextRelationTokens(ctx, relation, configuration, ContextTokenEstimationLimits{})
	if err != nil {
		t.Fatalf("EstimateContextRelationTokens() error = %v", err)
	}
	relationFromJSON, err := EstimateContextTokens(ctx, string(relationJSON), configuration, ContextTokenEstimationLimits{})
	if err != nil {
		t.Fatalf("EstimateContextTokens(relation JSON) error = %v", err)
	}
	if !reflect.DeepEqual(relationEstimate, relationFromJSON) {
		t.Fatalf("relation estimate differs from canonical JSON estimate:\nrelation=%#v\njson=%#v", relationEstimate, relationFromJSON)
	}

	relationCandidate := ContextRelationCandidate{Relation: relation}
	relationCandidateEstimate, err := EstimateContextRelationCandidateCosts(ctx, &relationCandidate, configuration, ContextTokenEstimationLimits{})
	if err != nil {
		t.Fatalf("EstimateContextRelationCandidateCosts() error = %v", err)
	}
	if !reflect.DeepEqual(relationCandidateEstimate, relationEstimate) {
		t.Fatalf("relation candidate estimate = %#v, want %#v", relationCandidateEstimate, relationEstimate)
	}
	if relationCandidate.TokenCost != relationEstimate.TokenEstimate || relationCandidate.CharacterCost != relationEstimate.Characters || relationCandidate.ByteCost != relationEstimate.Bytes {
		t.Fatalf("relation candidate costs = %d/%d/%d, want %d/%d/%d", relationCandidate.TokenCost, relationCandidate.CharacterCost, relationCandidate.ByteCost, relationEstimate.TokenEstimate, relationEstimate.Characters, relationEstimate.Bytes)
	}
}

func TestContextTokenCandidateCostHelpersLeaveInvalidCandidatesUnchanged(t *testing.T) {
	t.Parallel()

	configuration := DefaultContextTokenEstimatorConfiguration()
	itemCandidate := ContextSelectionCandidate{Item: cloneContextItem(contextTestPackage().Items[1])}
	itemBefore := itemCandidate
	if _, err := EstimateContextSelectionCandidateCosts(context.Background(), &itemCandidate, configuration, ContextTokenEstimationLimits{MaxBytes: 1}); !errors.Is(err, ErrContextTokenEstimationLimit) {
		t.Fatalf("selection candidate limit error = %v, want limit", err)
	}
	if !reflect.DeepEqual(itemCandidate, itemBefore) {
		t.Fatalf("selection candidate changed after failed estimation:\nbefore=%#v\nafter=%#v", itemBefore, itemCandidate)
	}

	relationCandidate := ContextRelationCandidate{Relation: cloneContextRelation(contextTestPackage().Relations[0])}
	relationBefore := relationCandidate
	if _, err := EstimateContextRelationCandidateCosts(context.Background(), &relationCandidate, configuration, ContextTokenEstimationLimits{MaxBytes: 1}); !errors.Is(err, ErrContextTokenEstimationLimit) {
		t.Fatalf("relation candidate limit error = %v, want limit", err)
	}
	if !reflect.DeepEqual(relationCandidate, relationBefore) {
		t.Fatalf("relation candidate changed after failed estimation:\nbefore=%#v\nafter=%#v", relationBefore, relationCandidate)
	}
}

func TestContextTokenCanonicalJSONRejectsInvalidValues(t *testing.T) {
	t.Parallel()

	if _, err := CanonicalContextItemJSON(ContextItem{}); !errors.Is(err, ErrInvalidContextTokenEstimation) {
		t.Fatalf("invalid item canonical JSON error = %v, want invalid estimation", err)
	}
	if _, err := CanonicalContextRelationJSON(ContextRelation{}); !errors.Is(err, ErrInvalidContextTokenEstimation) {
		t.Fatalf("invalid relation canonical JSON error = %v, want invalid estimation", err)
	}
}
