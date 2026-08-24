package derivation_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/derivation"
	"github.com/pedrogpaulino/manu/internal/fact"
)

func TestDeriveWithLimitsReportsIterationFactAndFanoutGaps(t *testing.T) {
	t.Parallel()

	scope := testScope()
	seed := observedFact(t, scope, "seed-limits")
	first := derivedFact(t, seed, "bounded", "1", "first", seed)
	second := derivedFact(t, seed, "bounded", "1", "second", seed)

	tests := []struct {
		name      string
		limits    derivation.DerivationLimits
		wantFacts int
		wantGap   string
		rule      []fact.CanonicalFact
	}{
		{
			name:      "iteration",
			limits:    derivation.DerivationLimits{MaxIterations: 1},
			wantFacts: 2,
			wantGap:   derivation.DerivationIterationLimitCode,
			rule:      []fact.CanonicalFact{first},
		},
		{
			name:      "fact total",
			limits:    derivation.DerivationLimits{MaxFacts: 1},
			wantFacts: 1,
			wantGap:   derivation.DerivationFactLimitCode,
			rule:      []fact.CanonicalFact{first},
		},
		{
			name:      "fanout",
			limits:    derivation.DerivationLimits{MaxFanout: 1},
			wantFacts: 1,
			wantGap:   derivation.DerivationFanoutLimitCode,
			rule:      []fact.CanonicalFact{first, second},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			executor := newLimitedExecutor(t, "bounded", test.rule...)
			result, err := executor.DeriveWithLimits(context.Background(), []fact.CanonicalFact{seed}, test.limits)
			if err != nil {
				t.Fatalf("DeriveWithLimits() error = %v", err)
			}
			if len(result.Facts) != test.wantFacts {
				t.Fatalf("facts = %d, want %d: %#v", len(result.Facts), test.wantFacts, result.Facts)
			}
			if len(result.Gaps) != 1 || result.Gaps[0].Code != test.wantGap {
				t.Fatalf("gaps = %#v, want one %q", result.Gaps, test.wantGap)
			}
			assertLimitedResultMetadata(t, result, scope, true)
		})
	}
}

func TestDeriveWithLimitsPublishesExactBoundaryWithoutFalseGap(t *testing.T) {
	t.Parallel()

	scope := testScope()
	seed := observedFact(t, scope, "seed-exact")
	candidate := derivedFact(t, seed, "exact", "1", "candidate", seed)
	executor := newLimitedExecutor(t, "exact", candidate)

	result, err := executor.DeriveWithLimits(context.Background(), []fact.CanonicalFact{seed}, derivation.DerivationLimits{
		MaxIterations: 2,
		MaxFacts:      2,
		MaxFanout:     1,
	})
	if err != nil {
		t.Fatalf("DeriveWithLimits() error = %v", err)
	}
	if len(result.Facts) != 2 || len(result.Gaps) != 0 {
		t.Fatalf("exact-boundary result = %#v, want two facts and no gaps", result)
	}
	assertLimitedResultMetadata(t, result, scope, false)

	for _, candidate := range result.Facts {
		if err := candidate.Validate(); err != nil {
			t.Fatalf("fact.Validate() error = %v", err)
		}
	}
}

func TestDeriveWithLimitsSortsMultipleLimitGapsByIdentity(t *testing.T) {
	t.Parallel()

	scope := testScope()
	seed := observedFact(t, scope, "seed-two-limits")
	first := derivedFact(t, seed, "two-limits", "1", "first-two-limits", seed)
	second := derivedFact(t, seed, "two-limits", "1", "second-two-limits", seed)
	executor := newLimitedExecutor(t, "two-limits", first, second)
	result, err := executor.DeriveWithLimits(context.Background(), []fact.CanonicalFact{seed}, derivation.DerivationLimits{
		MaxFacts:  1,
		MaxFanout: 1,
	})
	if err != nil {
		t.Fatalf("DeriveWithLimits() error = %v", err)
	}
	if len(result.Gaps) != 2 || result.Gaps[0].Code == result.Gaps[1].Code {
		t.Fatalf("gaps = %#v, want fact and fanout gaps", result.Gaps)
	}
	for index := 1; index < len(result.Gaps); index++ {
		if result.Gaps[index-1].ID >= result.Gaps[index].ID {
			t.Fatalf("gaps are not ordered by identity: %#v", result.Gaps)
		}
	}
	assertLimitedResultMetadata(t, result, scope, true)
}

func TestDeriveWithLimitsInputAboveFactLimitIsRejectedWithoutExecution(t *testing.T) {
	t.Parallel()

	scope := testScope()
	first := observedFact(t, scope, "seed-over-one")
	second := observedFact(t, scope, "seed-over-two")
	called := false
	rule := derivation.RuleFunc(func(context.Context, derivation.FactView) ([]fact.CanonicalFact, error) {
		called = true
		return nil, nil
	})
	registry, err := derivation.NewRegistry(derivation.Registration{RuleID: "input-limit", Version: "1", Rule: rule})
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	executor, err := derivation.NewExecutor(registry)
	if err != nil {
		t.Fatalf("NewExecutor() error = %v", err)
	}

	result, err := executor.DeriveWithLimits(context.Background(), []fact.CanonicalFact{first, second}, derivation.DerivationLimits{MaxFacts: 1})
	if !errors.Is(err, derivation.ErrInputExceedsFactLimit) || !errors.Is(err, derivation.ErrInvalidInput) {
		t.Fatalf("error = %v, want input/fact limit errors", err)
	}
	if !reflect.DeepEqual(result, derivation.DerivationResult{}) {
		t.Fatalf("rejected input returned result %#v", result)
	}
	if called {
		t.Fatal("rule executed for an input already above MaxFacts")
	}
}

func TestDeriveWithLimitsValidatesAndDeduplicatesWholeBatchBeforeLimits(t *testing.T) {
	t.Parallel()

	scope := testScope()
	seed := observedFact(t, scope, "seed-batch")
	first := derivedFact(t, seed, "batch", "1", "first-batch", seed)
	second := derivedFact(t, first, "batch", "1", "second-batch", first)
	duplicate := derivedFact(t, seed, "batch", "1", "duplicate-batch", seed)

	// Duplicate identities are counted once when every candidate is supported
	// by the facts that existed before the invocation.
	executor := newLimitedExecutor(t, "batch", first, duplicate, first)
	result, err := executor.DeriveWithLimits(context.Background(), []fact.CanonicalFact{seed}, derivation.DerivationLimits{MaxFanout: 2})
	if err != nil {
		t.Fatalf("DeriveWithLimits() error = %v", err)
	}
	if len(result.Facts) != 3 || len(result.Gaps) != 0 {
		t.Fatalf("batch result = %#v, want seed plus two derived facts and no gaps", result)
	}

	// A candidate cannot use another candidate from the same invocation as
	// support. The entire batch is rejected atomically.
	invalidExecutor := newLimitedExecutor(t, "batch", first, second)
	invalidResult, err := invalidExecutor.DeriveWithLimits(context.Background(), []fact.CanonicalFact{seed}, derivation.DerivationLimits{MaxFanout: 10})
	if !errors.Is(err, derivation.ErrInvalidOutput) || !reflect.DeepEqual(invalidResult, derivation.DerivationResult{}) {
		t.Fatalf("intra-batch support result/error = %#v/%v, want atomic invalid output", invalidResult, err)
	}

	unknown := observedFact(t, scope, "unknown-batch")
	unknownCandidate := derivedFact(t, seed, "batch-unknown", "1", "unknown-batch-candidate", unknown)
	unknownExecutor := newLimitedExecutor(t, "batch-unknown", unknownCandidate)
	unknownResult, err := unknownExecutor.DeriveWithLimits(context.Background(), []fact.CanonicalFact{seed}, derivation.DerivationLimits{MaxFanout: 10})
	if !errors.Is(err, derivation.ErrInvalidOutput) || !reflect.DeepEqual(unknownResult, derivation.DerivationResult{}) {
		t.Fatalf("unknown support result/error = %#v/%v, want atomic invalid output", unknownResult, err)
	}
}

func TestDeriveWithLimitsIsPermutationAndRepetitionDeterministic(t *testing.T) {
	t.Parallel()

	scope := testScope()
	seed := observedFact(t, scope, "seed-order")
	first := derivedFact(t, seed, "ordered", "1", "first-order", seed)
	second := derivedFact(t, seed, "ordered", "1", "second-order", seed)
	batch := []fact.CanonicalFact{first, second, first}
	newOrderedExecutor := func(reverse bool) *derivation.Executor {
		rule := derivation.RuleFunc(func(context.Context, derivation.FactView) ([]fact.CanonicalFact, error) {
			candidates := append([]fact.CanonicalFact(nil), batch...)
			if reverse {
				for left, right := 0, len(candidates)-1; left < right; left, right = left+1, right-1 {
					candidates[left], candidates[right] = candidates[right], candidates[left]
				}
			}
			return candidates, nil
		})
		registry, err := derivation.NewRegistry(derivation.Registration{RuleID: "ordered", Version: "1", Rule: rule})
		if err != nil {
			t.Fatalf("NewRegistry() error = %v", err)
		}
		executor, err := derivation.NewExecutor(registry)
		if err != nil {
			t.Fatalf("NewExecutor() error = %v", err)
		}
		return executor
	}
	forwardExecutor := newOrderedExecutor(false)
	reverseExecutor := newOrderedExecutor(true)
	limits := derivation.DerivationLimits{}
	firstResult, err := forwardExecutor.DeriveWithLimits(context.Background(), []fact.CanonicalFact{seed}, limits)
	if err != nil {
		t.Fatalf("first DeriveWithLimits() error = %v", err)
	}
	secondResult, err := reverseExecutor.DeriveWithLimits(context.Background(), []fact.CanonicalFact{seed}, limits)
	if err != nil {
		t.Fatalf("reverse DeriveWithLimits() error = %v", err)
	}
	if !reflect.DeepEqual(firstResult, secondResult) {
		t.Fatalf("permuted bounded derivation differs:\nforward=%#v\nreverse=%#v", firstResult, secondResult)
	}
	unlimited, err := forwardExecutor.Derive(context.Background(), []fact.CanonicalFact{seed})
	if err != nil {
		t.Fatalf("unlimited Derive() error = %v", err)
	}
	if !reflect.DeepEqual(unlimited, firstResult.Facts) {
		t.Fatalf("unlimited and zero-limit facts differ:\nunlimited=%#v\nbounded=%#v", unlimited, firstResult.Facts)
	}
	repeated, err := forwardExecutor.DeriveWithLimits(context.Background(), []fact.CanonicalFact{seed}, limits)
	if err != nil {
		t.Fatalf("repeated DeriveWithLimits() error = %v", err)
	}
	if !reflect.DeepEqual(firstResult, repeated) {
		t.Fatalf("repeated bounded derivation differs:\nfirst=%#v\nrepeated=%#v", firstResult, repeated)
	}
	for index := 1; index < len(firstResult.Facts); index++ {
		if firstResult.Facts[index-1].ID >= firstResult.Facts[index].ID {
			t.Fatalf("facts are not ordered: %#v", firstResult.Facts)
		}
	}
}

func TestDeriveWithLimitsStopsAtFixedPointWithoutIterationGap(t *testing.T) {
	t.Parallel()

	scope := testScope()
	first := observedFact(t, scope, "seed-no-output-first")
	second := observedFact(t, scope, "seed-no-output-second")
	executor := newLimitedExecutor(t, "no-output")
	result, err := executor.DeriveWithLimits(context.Background(), []fact.CanonicalFact{first, second}, derivation.DerivationLimits{MaxIterations: 1})
	if err != nil {
		t.Fatalf("DeriveWithLimits() error = %v", err)
	}
	if len(result.Facts) != 2 || len(result.Gaps) != 0 {
		t.Fatalf("no-output result = %#v, want two facts and no gaps", result)
	}
	assertLimitedResultMetadata(t, result, scope, false)
}

func TestDerivationResultCloneIsDefensiveAndValidatesLineageSupport(t *testing.T) {
	t.Parallel()

	scope := testScope()
	seed := observedFact(t, scope, "seed-clone")
	first := derivedFact(t, seed, "clone", "1", "first-clone", seed)
	second := derivedFact(t, seed, "clone", "1", "second-clone", seed)
	executor := newLimitedExecutor(t, "clone", first, second)
	result, err := executor.DeriveWithLimits(context.Background(), []fact.CanonicalFact{seed}, derivation.DerivationLimits{})
	if err != nil {
		t.Fatalf("DeriveWithLimits() error = %v", err)
	}
	clone := result.Clone()
	derivedIndex := -1
	for index, candidate := range clone.Facts {
		if candidate.Lineage != nil {
			derivedIndex = index
			break
		}
	}
	if derivedIndex < 0 {
		t.Fatalf("clone facts = %#v, want a derived fact", clone.Facts)
	}
	clone.Facts[derivedIndex].Evidence[0].ID = "mutated-clone-evidence"
	clone.Facts[derivedIndex].Lineage.InputFactIDs[0] = "missing-clone-support"
	if result.Facts[derivedIndex].Evidence[0].ID == "mutated-clone-evidence" || result.Facts[derivedIndex].Lineage.InputFactIDs[0] == "missing-clone-support" {
		t.Fatal("DerivationResult.Clone() leaked mutable fact state")
	}
	if err := clone.Validate(); !errors.Is(err, derivation.ErrInvalidOutput) {
		t.Fatalf("clone with missing lineage support Validate() error = %v, want ErrInvalidOutput", err)
	}

	limited, err := executor.DeriveWithLimits(context.Background(), []fact.CanonicalFact{seed}, derivation.DerivationLimits{MaxFanout: 1})
	if err != nil {
		t.Fatalf("limited DeriveWithLimits() error = %v", err)
	}
	limitedClone := limited.Clone()
	if len(limited.Gaps) != 1 || len(limitedClone.Gaps) != 1 {
		t.Fatalf("limited gaps = %#v/%#v, want one gap in each result", limited.Gaps, limitedClone.Gaps)
	}
	limitedClone.Gaps[0].Message = "mutated-clone-gap"
	if limited.Gaps[0].Message == "mutated-clone-gap" {
		t.Fatal("DerivationResult.Clone() leaked mutable gap state")
	}
}

func TestDeriveWithLimitsRejectsNegativeLimitsAndPreservesAtomicErrors(t *testing.T) {
	t.Parallel()

	scope := testScope()
	seed := observedFact(t, scope, "seed-negative")
	limits := []derivation.DerivationLimits{
		{MaxIterations: -1},
		{MaxFacts: -1},
		{MaxFanout: -1},
	}
	executor := newLimitedExecutor(t, "negative")
	for _, candidate := range limits {
		result, err := executor.DeriveWithLimits(context.Background(), []fact.CanonicalFact{seed}, candidate)
		if !errors.Is(err, derivation.ErrInvalidLimits) || !errors.Is(err, derivation.ErrInvalidInput) || !reflect.DeepEqual(result, derivation.DerivationResult{}) {
			t.Fatalf("limits %#v result/error = %#v/%v, want invalid atomic result", candidate, result, err)
		}
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := executor.DeriveWithLimits(canceled, []fact.CanonicalFact{seed}, derivation.DerivationLimits{})
	if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(result, derivation.DerivationResult{}) {
		t.Fatalf("canceled result/error = %#v/%v, want context.Canceled and empty result", result, err)
	}

	secret := "secret-limited-rule"
	ruleRegistry, err := derivation.NewRegistry(derivation.Registration{
		RuleID:  "failure-limited",
		Version: "1",
		Rule: derivation.RuleFunc(func(context.Context, derivation.FactView) ([]fact.CanonicalFact, error) {
			return nil, errors.New(secret)
		}),
	})
	if err != nil {
		t.Fatalf("failure NewRegistry() error = %v", err)
	}
	failingExecutor, err := derivation.NewExecutor(ruleRegistry)
	if err != nil {
		t.Fatalf("failure NewExecutor() error = %v", err)
	}
	result, err = failingExecutor.DeriveWithLimits(context.Background(), []fact.CanonicalFact{seed}, derivation.DerivationLimits{})
	if !errors.Is(err, derivation.ErrRuleFailed) || strings.Contains(err.Error(), secret) || !reflect.DeepEqual(result, derivation.DerivationResult{}) {
		t.Fatalf("failing result/error = %#v/%v, want sanitized atomic failure", result, err)
	}
}

func newLimitedExecutor(t *testing.T, ruleID string, candidates ...fact.CanonicalFact) *derivation.Executor {
	t.Helper()
	registry, err := derivation.NewRegistry(derivation.Registration{
		RuleID:  ruleID,
		Version: "1",
		Rule: derivation.RuleFunc(func(context.Context, derivation.FactView) ([]fact.CanonicalFact, error) {
			return append([]fact.CanonicalFact(nil), candidates...), nil
		}),
	})
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	executor, err := derivation.NewExecutor(registry)
	if err != nil {
		t.Fatalf("NewExecutor() error = %v", err)
	}
	return executor
}

func assertLimitedResultMetadata(t *testing.T, result derivation.DerivationResult, scope fact.Scope, incomplete bool) {
	t.Helper()
	if err := result.Validate(); err != nil {
		t.Fatalf("DerivationResult.Validate() error = %v", err)
	}
	if len(result.Coverage) != 1 {
		t.Fatalf("coverage = %#v, want one entry", result.Coverage)
	}
	coverage := result.Coverage[0]
	wantState := contract.CoverageProduced
	if incomplete {
		wantState = contract.CoverageIncomplete
	}
	if coverage.Dimension != string(contract.DimensionEntitiesAndRelationships) || coverage.Scope != scope.SnapshotID || coverage.State != wantState || coverage.AnalyzerID != derivation.DerivationAnalyzerID {
		t.Fatalf("coverage = %#v, want snapshot-scoped %q coverage", coverage, wantState)
	}
	if coverage.ID != contract.CoverageID(coverage.Dimension, coverage.Scope, coverage.State, coverage.AnalyzerID) {
		t.Fatalf("coverage ID = %q is not deterministic", coverage.ID)
	}
}
