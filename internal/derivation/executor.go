package derivation

import (
	"container/heap"
	"context"
	"sort"

	"github.com/pedrogpaulino/manu/internal/fact"
)

// Deriver is the application-facing port for deterministic fact derivation.
type Deriver interface {
	Derive(context.Context, []fact.CanonicalFact) ([]fact.CanonicalFact, error)
}

// Executor evaluates registered rules until no new factual identity can be
// added. It publishes no result when execution fails; bounded success may
// explicitly return an incomplete result with coverage and gaps.
type Executor struct {
	registry *Registry
}

var _ Deriver = (*Executor)(nil)

// LimitedDeriver is the optional bounded execution surface. It is separate
// from Deriver so existing unlimited callers and test doubles remain source
// compatible.
type LimitedDeriver interface {
	DeriveWithLimits(context.Context, []fact.CanonicalFact, DerivationLimits) (DerivationResult, error)
}

var _ LimitedDeriver = (*Executor)(nil)

// NewExecutor creates an executor over a validated registry.
func NewExecutor(registry *Registry) (*Executor, error) {
	if registry == nil {
		return nil, ErrInvalidRule
	}
	return &Executor{registry: registry}, nil
}

// NewDeriver creates the concrete executor behind the Deriver port.
func NewDeriver(registry *Registry) (Deriver, error) {
	return NewExecutor(registry)
}

// Derive validates and detaches all inputs, then evaluates the registry to a
// least fixed point. Observed facts are returned unchanged semantically, and
// all results use the same canonical identity ordering.
func (e *Executor) Derive(ctx context.Context, inputs []fact.CanonicalFact) ([]fact.CanonicalFact, error) {
	result, err := e.DeriveWithLimits(ctx, inputs, DerivationLimits{})
	if err != nil {
		return nil, err
	}
	return cloneFacts(result.Facts), nil
}

// DeriveWithLimits evaluates registered rules until a least fixed point or a
// configured limit is reached. A configured limit is reported as a successful
// incomplete result; invalid input, rule failures, and cancellation return no
// partial result.
func (e *Executor) DeriveWithLimits(ctx context.Context, inputs []fact.CanonicalFact, limits DerivationLimits) (DerivationResult, error) {
	if ctx == nil {
		return DerivationResult{}, ErrInvalidInput
	}
	if err := ctx.Err(); err != nil {
		return DerivationResult{}, err
	}
	if err := limits.Validate(); err != nil {
		return DerivationResult{}, err
	}
	if e == nil || e.registry == nil {
		return DerivationResult{}, ErrInvalidRule
	}

	scope, known, pending, err := prepareInputs(inputs)
	if err != nil {
		return DerivationResult{}, err
	}
	if limits.MaxFacts > 0 && len(known) > limits.MaxFacts {
		return DerivationResult{}, ErrInputExceedsFactLimit
	}
	rules := e.registry.snapshot()
	limitCodes := make(map[string]struct{})
	iterations := 0
	for pending.Len() > 0 {
		if err := ctx.Err(); err != nil {
			return DerivationResult{}, err
		}
		if limits.MaxIterations > 0 && iterations >= limits.MaxIterations {
			limitCodes[DerivationIterationLimitCode] = struct{}{}
			break
		}
		_ = heap.Pop(pending).(fact.CanonicalFact)
		iterations++
		acceptedThisIteration := false
		for _, registered := range rules {
			if err := ctx.Err(); err != nil {
				return DerivationResult{}, err
			}
			view := newFactView(orderedFacts(known))
			candidates, ruleErr := invokeRule(ctx, registered.rule, view)
			if err := ctx.Err(); err != nil {
				return DerivationResult{}, err
			}
			if ruleErr != nil {
				return DerivationResult{}, ErrRuleFailed
			}
			accepted, skipped, batchErr := acceptCandidateBatch(
				ctx,
				candidates,
				scope,
				registered.version,
				known,
				limits,
			)
			if batchErr != nil {
				return DerivationResult{}, batchErr
			}
			for code := range skipped {
				limitCodes[code] = struct{}{}
			}
			for _, detached := range accepted {
				if err := ctx.Err(); err != nil {
					return DerivationResult{}, err
				}
				known[detached.ID] = detached
				heap.Push(pending, detached)
			}
			if len(accepted) > 0 {
				acceptedThisIteration = true
			}
		}
		if !acceptedThisIteration {
			break
		}
	}

	if err := ctx.Err(); err != nil {
		return DerivationResult{}, err
	}
	return makeDerivationResult(scope, known, limitCodes)
}

// Execute is an explicit alias for Derive.
func (e *Executor) Execute(ctx context.Context, inputs []fact.CanonicalFact) ([]fact.CanonicalFact, error) {
	return e.Derive(ctx, inputs)
}

// Run is an explicit alias for Derive.
func (e *Executor) Run(ctx context.Context, inputs []fact.CanonicalFact) ([]fact.CanonicalFact, error) {
	return e.Derive(ctx, inputs)
}

// acceptCandidateBatch validates and deterministically deduplicates a whole
// rule result before any candidate is counted or published. Every support
// identity must already be present in the known set before the rule runs.
func acceptCandidateBatch(
	ctx context.Context,
	candidates []fact.CanonicalFact,
	scope fact.Scope,
	version RuleVersion,
	known map[string]fact.CanonicalFact,
	limits DerivationLimits,
) ([]fact.CanonicalFact, map[string]struct{}, error) {
	if len(candidates) == 0 {
		return nil, nil, nil
	}

	validated := make([]fact.CanonicalFact, len(candidates))
	available := make(map[string]struct{}, len(known))
	for id := range known {
		available[id] = struct{}{}
	}
	for index, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		if err := validateCandidateShape(candidate, scope, version); err != nil {
			return nil, nil, err
		}
		validated[index] = cloneFact(candidate)
	}
	for _, candidate := range validated {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		for _, inputID := range candidate.Lineage.InputFactIDs {
			if _, exists := available[inputID]; !exists {
				return nil, nil, ErrInvalidOutput
			}
		}
	}

	// Fact identity excludes evidence and lineage, so the same ID may occur
	// with different support metadata. Canonical bytes provide a stable
	// tie-breaker and avoid making batch order observable.
	type candidateWithKey struct {
		fact fact.CanonicalFact
		key  string
	}
	ordered := make([]candidateWithKey, 0, len(validated))
	for _, candidate := range validated {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		encoded, err := fact.CanonicalBytes(candidate)
		if err != nil {
			return nil, nil, ErrInvalidOutput
		}
		ordered = append(ordered, candidateWithKey{
			fact: candidate,
			key:  candidate.ID + "\x00" + string(encoded),
		})
	}
	sort.SliceStable(ordered, func(left, right int) bool {
		return ordered[left].key < ordered[right].key
	})

	unique := make([]fact.CanonicalFact, 0, len(ordered))
	seen := make(map[string]struct{}, len(ordered))
	for _, item := range ordered {
		if _, exists := seen[item.fact.ID]; exists {
			continue
		}
		seen[item.fact.ID] = struct{}{}
		unique = append(unique, item.fact)
	}
	sort.SliceStable(unique, func(left, right int) bool {
		return unique[left].ID < unique[right].ID
	})

	newFacts := make([]fact.CanonicalFact, 0, len(unique))
	for _, candidate := range unique {
		if _, exists := known[candidate.ID]; !exists {
			newFacts = append(newFacts, candidate)
		}
	}
	if len(newFacts) == 0 {
		return nil, nil, nil
	}
	limitsReached := make(map[string]struct{})
	if limits.MaxFacts > 0 && len(known)+len(newFacts) > limits.MaxFacts {
		limitsReached[DerivationFactLimitCode] = struct{}{}
	}
	if limits.MaxFanout > 0 && len(newFacts) > limits.MaxFanout {
		limitsReached[DerivationFanoutLimitCode] = struct{}{}
	}
	if len(limitsReached) > 0 {
		return nil, limitsReached, nil
	}
	return newFacts, nil, nil
}

func validateCandidateShape(candidate fact.CanonicalFact, scope fact.Scope, version RuleVersion) error {
	if err := candidate.Validate(); err != nil {
		return ErrInvalidOutput
	}
	if candidate.Scope != scope || candidate.Lineage == nil {
		return ErrInvalidOutput
	}
	lineage := candidate.Lineage
	if lineage.RuleID != version.RuleID || lineage.RuleVersion != version.Version {
		return ErrInvalidOutput
	}
	if err := lineage.Validate(candidate.ID); err != nil {
		return ErrInvalidOutput
	}
	return nil
}

func prepareInputs(inputs []fact.CanonicalFact) (fact.Scope, map[string]fact.CanonicalFact, *factQueue, error) {
	known := make(map[string]fact.CanonicalFact, len(inputs))
	pending := &factQueue{}
	var scope fact.Scope
	for index, candidate := range inputs {
		if err := candidate.Validate(); err != nil {
			return fact.Scope{}, nil, nil, ErrInvalidInput
		}
		if index == 0 {
			scope = candidate.Scope
		} else if candidate.Scope != scope {
			return fact.Scope{}, nil, nil, ErrInvalidInput
		}
		if _, exists := known[candidate.ID]; exists {
			return fact.Scope{}, nil, nil, ErrDuplicateFact
		}
		detached := cloneFact(candidate)
		known[detached.ID] = detached
		heap.Push(pending, detached)
	}
	return scope, known, pending, nil
}

func invokeRule(ctx context.Context, rule Rule, view FactView) (output []fact.CanonicalFact, err error) {
	defer func() {
		if recover() != nil {
			output = nil
			err = ErrRuleFailed
		}
	}()
	return rule.Apply(ctx, view)
}

func orderedFacts(known map[string]fact.CanonicalFact) []fact.CanonicalFact {
	ids := make([]string, 0, len(known))
	for id := range known {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	result := make([]fact.CanonicalFact, 0, len(ids))
	for _, id := range ids {
		result = append(result, cloneFact(known[id]))
	}
	return result
}

type factQueue []fact.CanonicalFact

func (q factQueue) Len() int {
	return len(q)
}

func (q factQueue) Less(left, right int) bool {
	return q[left].ID < q[right].ID
}

func (q factQueue) Swap(left, right int) {
	q[left], q[right] = q[right], q[left]
}

func (q *factQueue) Push(value any) {
	*q = append(*q, value.(fact.CanonicalFact))
}

func (q *factQueue) Pop() any {
	old := *q
	last := len(old) - 1
	value := old[last]
	*q = old[:last]
	return value
}
