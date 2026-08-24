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
// added. It publishes no result until the complete run succeeds.
type Executor struct {
	registry *Registry
}

var _ Deriver = (*Executor)(nil)

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
	if ctx == nil {
		return nil, ErrInvalidInput
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if e == nil || e.registry == nil {
		return nil, ErrInvalidRule
	}

	scope, known, pending, err := prepareInputs(inputs)
	if err != nil {
		return nil, err
	}
	rules := e.registry.snapshot()
	for pending.Len() > 0 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		_ = heap.Pop(pending).(fact.CanonicalFact)
		for _, registered := range rules {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			view := newFactView(orderedFacts(known))
			candidates, ruleErr := invokeRule(ctx, registered.rule, view)
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if ruleErr != nil {
				return nil, ErrRuleFailed
			}
			if len(candidates) == 0 {
				continue
			}
			candidates = append([]fact.CanonicalFact(nil), candidates...)
			sort.SliceStable(candidates, func(left, right int) bool {
				return candidates[left].ID < candidates[right].ID
			})
			for _, candidate := range candidates {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				if err := validateCandidate(candidate, scope, registered.version, known); err != nil {
					return nil, err
				}
				if _, exists := known[candidate.ID]; exists {
					continue
				}
				detached := cloneFact(candidate)
				known[detached.ID] = detached
				heap.Push(pending, detached)
			}
		}
	}

	result := orderedFacts(known)
	return cloneFacts(result), nil
}

// Execute is an explicit alias for Derive.
func (e *Executor) Execute(ctx context.Context, inputs []fact.CanonicalFact) ([]fact.CanonicalFact, error) {
	return e.Derive(ctx, inputs)
}

// Run is an explicit alias for Derive.
func (e *Executor) Run(ctx context.Context, inputs []fact.CanonicalFact) ([]fact.CanonicalFact, error) {
	return e.Derive(ctx, inputs)
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

func validateCandidate(
	candidate fact.CanonicalFact,
	scope fact.Scope,
	version RuleVersion,
	known map[string]fact.CanonicalFact,
) error {
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
	for _, inputID := range lineage.InputFactIDs {
		if _, exists := known[inputID]; !exists {
			return ErrInvalidOutput
		}
	}
	return nil
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
