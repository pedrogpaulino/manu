package query

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/pedrogpaulino/manu/internal/fact"
)

func TestSelectContextUsesMarginalUtilityPerToken(t *testing.T) {
	t.Parallel()

	limits := contextTestLimits()
	limits.MaxItems = 1
	limits.MaxTokens = 8
	config := selectionTestConfiguration(1, 0, 0, 0)
	request := selectionTestRequest(limits, config,
		selectionTestCandidate(selectionTestEntityItem("entity-a", "artifact-a"), 0.9, 1, nil, "a", 3, 3, 3),
		selectionTestCandidate(selectionTestEntityItem("entity-b", "artifact-b"), 0.6, 2, nil, "b", 1, 1, 1),
	)

	result, err := SelectContext(context.Background(), request)
	if err != nil {
		t.Fatalf("SelectContext() error = %v", err)
	}
	if len(result.Items) != 1 || result.Items[0].ID != "entity-b" {
		t.Fatalf("selected items = %#v, want lower-gain/higher-ratio entity-b", result.Items)
	}
}

func TestContextMarginalUtilityChangesAfterAspectCoverage(t *testing.T) {
	t.Parallel()

	config := selectionTestConfiguration(0, 1, 0, 0)
	first := selectionTestCandidate(selectionTestEntityItem("entity-first", "artifact-first"), 0, 1, []string{"shared"}, "first", 1, 1, 1)
	second := selectionTestCandidate(selectionTestEntityItem("entity-second", "artifact-second"), 0, 2, []string{"shared", "new"}, "second", 1, 1, 1)

	initial, err := ContextMarginalUtility(second, nil, config)
	if err != nil {
		t.Fatalf("initial marginal utility error = %v", err)
	}
	afterCoverage, err := ContextMarginalUtility(second, []ContextSelectionCandidate{first}, config)
	if err != nil {
		t.Fatalf("covered marginal utility error = %v", err)
	}
	if initial != 2 || afterCoverage != 1 {
		t.Fatalf("marginal utility before/after coverage = %v/%v, want 2/1", initial, afterCoverage)
	}
}

func TestSelectContextRewardsKindAndArtifactDiversity(t *testing.T) {
	t.Parallel()

	limits := contextTestLimits()
	limits.MaxItems = 2
	config := selectionTestConfiguration(0, 0, 1, 1)
	request := selectionTestRequest(limits, config,
		selectionTestCandidate(selectionTestEntityItem("entity-a", "artifact-a"), 0, 1, nil, "a", 1, 1, 1),
		selectionTestCandidate(selectionTestEntityItem("entity-b", "artifact-b"), 0, 2, nil, "b", 1, 1, 1),
		selectionTestCandidate(selectionTestFactItem("fact-c", "artifact-c"), 0, 3, nil, "c", 1, 1, 1),
	)

	result, err := SelectContext(context.Background(), request)
	if err != nil {
		t.Fatalf("SelectContext() error = %v", err)
	}
	got := selectionItemIDs(result.Items)
	want := []string{"entity-a", request.Candidates[2].Item.ID}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("selected item IDs = %#v, want %#v", got, want)
	}
}

func TestSelectContextEnforcesEachBudgetIndependently(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		mutate     func(*ContextLimits)
		candidate  ContextSelectionCandidate
		candidates []ContextSelectionCandidate
		wantItems  int
		wantID     string
	}{
		{
			name: "tokens",
			mutate: func(limits *ContextLimits) {
				limits.MaxTokens = 1
			},
			candidate: selectionTestCandidate(selectionTestEntityItem("tokens", "artifact-tokens"), 1, 1, nil, "tokens", 2, 1, 1),
		},
		{
			name: "characters",
			mutate: func(limits *ContextLimits) {
				limits.MaxCharacters = 1
			},
			candidate: selectionTestCandidate(selectionTestEntityItem("characters", "artifact-characters"), 1, 1, nil, "characters", 1, 2, 1),
		},
		{
			name: "bytes",
			mutate: func(limits *ContextLimits) {
				limits.MaxBytes = 1
			},
			candidate: selectionTestCandidate(selectionTestEntityItem("bytes", "artifact-bytes"), 1, 1, nil, "bytes", 1, 1, 2),
		},
		{
			name: "items",
			mutate: func(limits *ContextLimits) {
				limits.MaxItems = 1
			},
			candidates: []ContextSelectionCandidate{
				selectionTestCandidate(selectionTestEntityItem("item-a", "artifact-item-a"), 1, 1, nil, "item-a", 1, 1, 1),
				selectionTestCandidate(selectionTestEntityItem("item-b", "artifact-item-b"), 0.5, 2, nil, "item-b", 1, 1, 1),
			},
			wantItems: 1,
			wantID:    "item-a",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			limits := contextTestLimits()
			tt.mutate(&limits)
			candidates := tt.candidates
			if candidates == nil {
				candidates = []ContextSelectionCandidate{tt.candidate}
			}
			result, err := SelectContext(context.Background(), selectionTestRequest(limits, selectionTestConfiguration(1, 0, 0, 0), candidates...))
			if err != nil {
				t.Fatalf("SelectContext() error = %v", err)
			}
			if tt.wantItems == 0 {
				if len(result.Items) != 0 {
					t.Fatalf("selected items = %#v, want no item", result.Items)
				}
				if audit := selectionTestAudit(t, result, tt.candidate.Item.ID); audit.Reason != ContextSelectionExcludedBudget {
					t.Fatalf("audit reason = %q, want budget", audit.Reason)
				}
				return
			}
			if len(result.Items) != tt.wantItems || result.Items[0].ID != tt.wantID {
				t.Fatalf("selected items = %#v, want %q", result.Items, tt.wantID)
			}
		})
	}
}

func TestSelectContextExcludesIndividualCandidateLargerThanBudget(t *testing.T) {
	t.Parallel()

	limits := contextTestLimits()
	limits.MaxTokens = 2
	limits.MaxCharacters = 2
	limits.MaxBytes = 2
	candidate := selectionTestCandidate(selectionTestEntityItem("too-large", "artifact-too-large"), 1, 1, nil, "too-large", 3, 1, 1)
	result, err := SelectContext(context.Background(), selectionTestRequest(limits, selectionTestConfiguration(1, 0, 0, 0), candidate))
	if err != nil {
		t.Fatalf("SelectContext() error = %v", err)
	}
	if len(result.Items) != 0 {
		t.Fatalf("selected items = %#v, want none", result.Items)
	}
	if audit := selectionTestAudit(t, result, candidate.Item.ID); audit.Reason != ContextSelectionExcludedBudget {
		t.Fatalf("audit reason = %q, want budget", audit.Reason)
	}
	if !result.BudgetExhausted {
		t.Fatal("BudgetExhausted = false, want true")
	}
}

func TestSelectContextRecordsExclusionReasons(t *testing.T) {
	t.Parallel()

	limits := contextTestLimits()
	limits.MaxTokens = 2
	valid := selectionTestCandidate(selectionTestEntityItem("included", "artifact-included"), 1, 1, nil, "shared", 1, 1, 1)
	redundant := selectionTestCandidate(selectionTestEntityItem("redundant", "artifact-redundant"), 0.9, 2, nil, "shared", 1, 1, 1)
	budget := selectionTestCandidate(selectionTestEntityItem("budget", "artifact-budget"), 0.8, 3, nil, "budget", 3, 1, 1)
	invalidItem := selectionTestEntityItem("invalid", "artifact-invalid")
	invalidItem.Entity = nil
	invalid := selectionTestCandidate(invalidItem, 0.7, 4, nil, "invalid", 1, 1, 1)
	scopeItem := selectionTestEntityItem("scope", "artifact-scope")
	otherScope := contextTestScope()
	otherScope.SnapshotID = contextTestUUID(99)
	scopeItem.Scope = otherScope
	scope := selectionTestCandidate(scopeItem, 0.6, 5, nil, "scope", 1, 1, 1)

	result, err := SelectContext(context.Background(), selectionTestRequest(limits, selectionTestConfiguration(1, 0, 0, 0), valid, redundant, budget, invalid, scope))
	if err != nil {
		t.Fatalf("SelectContext() error = %v", err)
	}
	if len(result.Items) != 1 || result.Items[0].ID != valid.Item.ID {
		t.Fatalf("selected items = %#v, want only included", result.Items)
	}
	wantReasons := map[string]ContextSelectionReason{
		valid.Item.ID:     ContextSelectionIncluded,
		redundant.Item.ID: ContextSelectionExcludedRedundancy,
		budget.Item.ID:    ContextSelectionExcludedBudget,
		invalid.Item.ID:   ContextSelectionExcludedInvalid,
		scope.Item.ID:     ContextSelectionExcludedScope,
	}
	for id, want := range wantReasons {
		if got := selectionTestAudit(t, result, id).Reason; got != want {
			t.Errorf("audit[%q].Reason = %q, want %q", id, got, want)
		}
	}
}

func TestSelectContextBudgetExhaustedReflectsFinalReasons(t *testing.T) {
	t.Parallel()

	limits := contextTestLimits()
	limits.MaxTokens = 2
	selected := selectionTestCandidate(selectionTestEntityItem("selected", "artifact-selected"), 1, 1, nil, "same", 1, 1, 1)
	tooLargeButRedundant := selectionTestCandidate(selectionTestEntityItem("redundant-large", "artifact-redundant-large"), 0.8, 2, nil, "same", 3, 1, 1)
	result, err := SelectContext(context.Background(), selectionTestRequest(limits, selectionTestConfiguration(1, 0, 0, 0), selected, tooLargeButRedundant))
	if err != nil {
		t.Fatalf("SelectContext() error = %v", err)
	}
	if audit := selectionTestAudit(t, result, tooLargeButRedundant.Item.ID); audit.Reason != ContextSelectionExcludedRedundancy {
		t.Fatalf("audit reason = %q, want redundancy", audit.Reason)
	}
	if result.BudgetExhausted {
		t.Fatal("BudgetExhausted = true, want false when final exclusion is redundancy")
	}
}

func TestSelectContextDoesNotSelectZeroMarginalUtility(t *testing.T) {
	t.Parallel()

	limits := contextTestLimits()
	limits.MaxItems = 2
	config := selectionTestConfiguration(1, 0, 0, 0)
	positive := selectionTestCandidate(selectionTestEntityItem("positive", "artifact-positive"), 1, 1, nil, "positive", 1, 1, 1)
	zero := selectionTestCandidate(selectionTestEntityItem("zero", "artifact-zero"), 0, 2, nil, "zero", 1, 1, 1)
	result, err := SelectContext(context.Background(), selectionTestRequest(limits, config, positive, zero))
	if err != nil {
		t.Fatalf("SelectContext() error = %v", err)
	}
	if len(result.Items) != 1 || result.Items[0].ID != positive.Item.ID {
		t.Fatalf("selected items = %#v, want only positive", result.Items)
	}
	if audit := selectionTestAudit(t, result, zero.Item.ID); audit.Included {
		t.Fatal("zero-marginal candidate was included")
	}
}

func TestSelectContextUsesRedundancyKey(t *testing.T) {
	t.Parallel()

	limits := contextTestLimits()
	limits.MaxItems = 2
	config := selectionTestConfiguration(1, 0, 0, 0)
	first := selectionTestCandidate(selectionTestEntityItem("first", "artifact-first"), 1, 1, nil, "same", 1, 1, 1)
	duplicate := selectionTestCandidate(selectionTestEntityItem("duplicate", "artifact-duplicate"), 0.9, 2, nil, "same", 1, 1, 1)
	other := selectionTestCandidate(selectionTestEntityItem("other", "artifact-other"), 0.8, 3, nil, "other", 1, 1, 1)
	result, err := SelectContext(context.Background(), selectionTestRequest(limits, config, first, duplicate, other))
	if err != nil {
		t.Fatalf("SelectContext() error = %v", err)
	}
	if got := selectionItemIDs(result.Items); !reflect.DeepEqual(got, []string{"first", "other"}) {
		t.Fatalf("selected item IDs = %#v, want first/other", got)
	}
	if audit := selectionTestAudit(t, result, duplicate.Item.ID); audit.Reason != ContextSelectionExcludedRedundancy {
		t.Fatalf("duplicate audit reason = %q, want redundancy", audit.Reason)
	}
}

func TestSelectContextFallsBackToItemIDForEmptyRedundancyKey(t *testing.T) {
	t.Parallel()

	limits := contextTestLimits()
	limits.MaxItems = 2
	first := selectionTestCandidate(selectionTestEntityItem("fallback-first", "artifact-fallback-first"), 1, 1, nil, "", 1, 1, 1)
	second := selectionTestCandidate(selectionTestEntityItem("fallback-second", "artifact-fallback-second"), 0.9, 2, nil, "", 1, 1, 1)
	result, err := SelectContext(context.Background(), selectionTestRequest(limits, selectionTestConfiguration(1, 0, 0, 0), first, second))
	if err != nil {
		t.Fatalf("SelectContext() error = %v", err)
	}
	if got := selectionItemIDs(result.Items); !reflect.DeepEqual(got, []string{"fallback-first", "fallback-second"}) {
		t.Fatalf("selected item IDs = %#v, want both item-ID redundancy groups", got)
	}
}

func TestSelectContextUsesCanonicalRankAndIDTieBreakers(t *testing.T) {
	t.Parallel()

	config := selectionTestConfiguration(1, 0, 0, 0)
	limits := contextTestLimits()
	limits.MaxItems = 1

	rankResult, err := SelectContext(context.Background(), selectionTestRequest(limits, config,
		selectionTestCandidate(selectionTestEntityItem("entity-z", "artifact-z"), 1, 1, nil, "rank-z", 1, 1, 1),
		selectionTestCandidate(selectionTestEntityItem("entity-a", "artifact-a"), 1, 2, nil, "rank-a", 1, 1, 1),
	))
	if err != nil {
		t.Fatalf("rank tie SelectContext() error = %v", err)
	}
	if len(rankResult.Items) != 1 || rankResult.Items[0].ID != "entity-z" {
		t.Fatalf("rank tie selected items = %#v, want entity-z", rankResult.Items)
	}

	idResult, err := SelectContext(context.Background(), selectionTestRequest(limits, config,
		selectionTestCandidate(selectionTestEntityItem("entity-z", "artifact-z"), 1, 1, nil, "id-z", 1, 1, 1),
		selectionTestCandidate(selectionTestEntityItem("entity-a", "artifact-a"), 1, 1, nil, "id-a", 1, 1, 1),
	))
	if err != nil {
		t.Fatalf("ID tie SelectContext() error = %v", err)
	}
	if len(idResult.Items) != 1 || idResult.Items[0].ID != "entity-a" {
		t.Fatalf("ID tie selected items = %#v, want entity-a", idResult.Items)
	}
}

func TestSelectContextIsEquivalentUnderCandidatePermutation(t *testing.T) {
	t.Parallel()

	limits := contextTestLimits()
	limits.MaxItems = 3
	config := selectionTestConfiguration(1, 0, 0, 0)
	candidates := []ContextSelectionCandidate{
		selectionTestCandidate(selectionTestEntityItem("entity-a", "artifact-a"), 0.7, 3, []string{"a"}, "a", 1, 1, 1),
		selectionTestCandidate(selectionTestEntityItem("entity-b", "artifact-b"), 0.9, 2, []string{"b"}, "b", 1, 1, 1),
		selectionTestCandidate(selectionTestFactItem("fact-c", "artifact-c"), 0.8, 1, []string{"c"}, "c", 1, 1, 1),
	}
	forward, err := SelectContext(context.Background(), selectionTestRequest(limits, config, candidates...))
	if err != nil {
		t.Fatalf("forward SelectContext() error = %v", err)
	}
	reverseCandidates := []ContextSelectionCandidate{candidates[2], candidates[0], candidates[1]}
	reverse, err := SelectContext(context.Background(), selectionTestRequest(limits, config, reverseCandidates...))
	if err != nil {
		t.Fatalf("reverse SelectContext() error = %v", err)
	}
	if !reflect.DeepEqual(forward, reverse) {
		t.Fatalf("selection changed under candidate permutation:\nforward=%#v\nreverse=%#v", forward, reverse)
	}
}

func TestSelectContextAcceptsZeroCosts(t *testing.T) {
	t.Parallel()

	limits := contextTestLimits()
	limits.MaxItems = 1
	limits.MaxTokens = 1
	limits.MaxCharacters = 1
	limits.MaxBytes = 1
	candidate := selectionTestCandidate(selectionTestEntityItem("zero-cost", "artifact-zero-cost"), 1, 1, nil, "zero-cost", 0, 0, 0)
	result, err := SelectContext(context.Background(), selectionTestRequest(limits, selectionTestConfiguration(1, 0, 0, 0), candidate))
	if err != nil {
		t.Fatalf("SelectContext() error = %v", err)
	}
	if len(result.Items) != 1 || result.Items[0].ID != candidate.Item.ID {
		t.Fatalf("selected items = %#v, want zero-cost candidate", result.Items)
	}
	if result.TokenEstimate != 0 || result.CharactersUsed != 0 || result.BytesUsed != 0 {
		t.Fatalf("selected counts = tokens:%d characters:%d bytes:%d, want zero", result.TokenEstimate, result.CharactersUsed, result.BytesUsed)
	}
}

func TestSelectContextUsesSuppliedUTF8CharacterAndByteCosts(t *testing.T) {
	t.Parallel()

	limits := contextTestLimits()
	limits.MaxItems = 2
	limits.MaxCharacters = 2
	limits.MaxBytes = 4
	first := selectionTestCandidate(selectionTestEntityItem("utf8-fit", "artifact-utf8-fit"), 1, 1, nil, "utf8-fit", 1, 2, 4)
	second := selectionTestCandidate(selectionTestEntityItem("utf8-byte-over", "artifact-utf8-byte-over"), 0.9, 2, nil, "utf8-byte-over", 1, 2, 5)
	result, err := SelectContext(context.Background(), selectionTestRequest(limits, selectionTestConfiguration(1, 0, 0, 0), first, second))
	if err != nil {
		t.Fatalf("SelectContext() error = %v", err)
	}
	if len(result.Items) != 1 || result.Items[0].ID != first.Item.ID {
		t.Fatalf("selected items = %#v, want only UTF-8 fit candidate", result.Items)
	}
	if result.CharactersUsed != 2 || result.BytesUsed != 4 {
		t.Fatalf("selected character/byte counts = %d/%d, want 2/4", result.CharactersUsed, result.BytesUsed)
	}
	if audit := selectionTestAudit(t, result, second.Item.ID); audit.Reason != ContextSelectionExcludedBudget {
		t.Fatalf("UTF-8 byte-over audit reason = %q, want budget", audit.Reason)
	}
}

func TestSelectContextRejectsInvalidUtilityConfiguration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		config ContextUtilityConfiguration
	}{
		{
			name: "version",
			config: func() ContextUtilityConfiguration {
				config := selectionTestConfiguration(1, 0, 0, 0)
				config.Version = "v0"
				return config
			}(),
		},
		{
			name: "algorithm",
			config: func() ContextUtilityConfiguration {
				config := selectionTestConfiguration(1, 0, 0, 0)
				config.Algorithm = "other"
				return config
			}(),
		},
		{
			name: "malformed digest",
			config: func() ContextUtilityConfiguration {
				config := selectionTestConfiguration(1, 0, 0, 0)
				config.Digest = "not-a-sha256"
				return config
			}(),
		},
		{
			name: "mismatched digest",
			config: func() ContextUtilityConfiguration {
				config := selectionTestConfiguration(1, 0, 0, 0)
				config.Digest = strings.Repeat("0", 64)
				return config
			}(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidate := selectionTestCandidate(selectionTestEntityItem("config-item", "artifact-config"), 1, 1, nil, "config-item", 1, 1, 1)
			_, err := SelectContext(context.Background(), selectionTestRequest(contextTestLimits(), tt.config, candidate))
			if !errors.Is(err, ErrInvalidContextSelection) {
				t.Fatalf("SelectContext() error = %v, want invalid selection configuration", err)
			}
		})
	}
}

func TestSelectContextDoesNotMutateInput(t *testing.T) {
	t.Parallel()

	request := selectionTestRequest(contextTestLimits(), selectionTestConfiguration(1, 1, 1, 1),
		selectionTestCandidate(selectionTestEntityItem("input-item", "artifact-input"), 1, 1, []string{"aspect"}, "input", 1, 2, 3),
	)
	before := cloneSelectionTestRequest(request)
	result, err := SelectContext(context.Background(), request)
	if err != nil {
		t.Fatalf("SelectContext() error = %v", err)
	}
	if !reflect.DeepEqual(request, before) {
		t.Fatalf("selection mutated request:\nbefore=%#v\nafter=%#v", before, request)
	}
	if len(result.Items) == 1 && result.Items[0].Entity != nil {
		result.Items[0].Entity.ID = "mutated-output"
		if request.Candidates[0].Item.Entity.ID == "mutated-output" {
			t.Fatal("selected item shares mutable entity with request")
		}
	}
}

func TestSelectContextHonorsCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	candidate := selectionTestCandidate(selectionTestEntityItem("cancelled", "artifact-cancelled"), 1, 1, nil, "cancelled", 1, 1, 1)
	_, err := SelectContext(ctx, selectionTestRequest(contextTestLimits(), selectionTestConfiguration(1, 0, 0, 0), candidate))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("SelectContext() error = %v, want context.Canceled", err)
	}
}

func TestContextSelectionResultValidateAgainstAcceptsNormalResult(t *testing.T) {
	t.Parallel()

	limits := contextTestLimits()
	request := selectionTestRequest(limits, selectionTestConfiguration(1, 0, 0, 0),
		selectionTestCandidate(selectionTestEntityItem("validated", "artifact-validated"), 1, 1, nil, "validated", 2, 3, 4),
	)
	result, err := SelectContext(context.Background(), request)
	if err != nil {
		t.Fatalf("SelectContext() error = %v", err)
	}
	if err := result.ValidateAgainst(request); err != nil {
		t.Fatalf("normal result ValidateAgainst() error = %v", err)
	}
}

func TestContextSelectionResultValidateAgainstRejectsInconsistentAccounting(t *testing.T) {
	t.Parallel()

	limits := contextTestLimits()
	request := selectionTestRequest(limits, selectionTestConfiguration(1, 0, 0, 0),
		selectionTestCandidate(selectionTestEntityItem("accounted", "artifact-accounted"), 1, 1, nil, "accounted", 2, 3, 4),
	)
	result, err := SelectContext(context.Background(), request)
	if err != nil {
		t.Fatalf("SelectContext() error = %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*ContextSelectionResult)
	}{
		{
			name: "token estimate",
			mutate: func(result *ContextSelectionResult) {
				result.TokenEstimate++
			},
		},
		{
			name: "characters used",
			mutate: func(result *ContextSelectionResult) {
				result.CharactersUsed++
			},
		},
		{
			name: "bytes used",
			mutate: func(result *ContextSelectionResult) {
				result.BytesUsed++
			},
		},
		{
			name: "item count",
			mutate: func(result *ContextSelectionResult) {
				result.ItemCount++
			},
		},
		{
			name: "budget exhausted without budget exclusion",
			mutate: func(result *ContextSelectionResult) {
				result.BudgetExhausted = true
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mutated := result
			tt.mutate(&mutated)
			if err := mutated.ValidateAgainst(request); err == nil {
				t.Fatalf("ValidateAgainst() accepted inconsistent %s", tt.name)
			}
		})
	}
}

func selectionTestConfiguration(relevance, aspects, kind, artifact float64) ContextUtilityConfiguration {
	return ContextUtilityConfiguration{
		Version:                 ContextUtilityVersion,
		Algorithm:               ContextSelectionAlgorithm,
		RelevanceWeight:         relevance,
		AspectWeight:            aspects,
		KindDiversityWeight:     kind,
		ArtifactDiversityWeight: artifact,
	}
}

func selectionTestCandidate(item ContextItem, relevance float64, rank int, aspects []string, redundancyKey string, tokens int, characters, bytes int64) ContextSelectionCandidate {
	return ContextSelectionCandidate{
		Item:          item,
		Relevance:     relevance,
		Rank:          rank,
		Aspects:       append([]string(nil), aspects...),
		RedundancyKey: redundancyKey,
		TokenCost:     tokens,
		CharacterCost: characters,
		ByteCost:      bytes,
	}
}

func selectionTestRequest(limits ContextLimits, config ContextUtilityConfiguration, candidates ...ContextSelectionCandidate) ContextSelectionRequest {
	return ContextSelectionRequest{
		Scope:         contextTestScope(),
		Limits:        limits,
		Candidates:    candidates,
		Configuration: config,
	}
}

func selectionTestEntityItem(id, artifact string) ContextItem {
	item := cloneContextItem(contextTestPackage().Items[1])
	item.ID = id
	entity := *item.Entity
	entity.ID = id
	item.Entity = &entity
	item.Locator.ArtifactID = artifact
	item.Locator.Path = artifact + ".go"
	return item
}

func selectionTestFactItem(id, artifact string) ContextItem {
	item := cloneContextItem(contextTestPackage().Items[0])
	item.Fact.Subject.ID = "symbol-" + id
	item.Fact.ID = ""
	factID, err := fact.FactID(*item.Fact)
	if err != nil {
		panic(err)
	}
	item.Fact.ID = factID
	item.ID = factID
	item.Locator.ArtifactID = artifact
	item.Locator.Path = artifact + ".go"
	return item
}

func selectionItemIDs(items []ContextItem) []string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	return ids
}

func selectionTestAudit(t *testing.T, result ContextSelectionResult, itemID string) ContextSelectionAudit {
	t.Helper()
	for _, audit := range result.Audit {
		if audit.ItemID == itemID {
			return audit
		}
	}
	t.Fatalf("audit for item %q not found in %#v", itemID, result.Audit)
	return ContextSelectionAudit{}
}

func cloneSelectionTestRequest(request ContextSelectionRequest) ContextSelectionRequest {
	clone := request
	clone.Candidates = make([]ContextSelectionCandidate, len(request.Candidates))
	for index, candidate := range request.Candidates {
		clone.Candidates[index] = candidate
		clone.Candidates[index].Aspects = append([]string(nil), candidate.Aspects...)
		clone.Candidates[index].Item = cloneContextItem(candidate.Item)
	}
	return clone
}
