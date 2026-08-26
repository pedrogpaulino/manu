package query

import (
	"context"
	"errors"
	"fmt"

	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/fact"
)

var (
	// ErrInvalidContextSnapshot identifies a factual snapshot that cannot be
	// consumed as context input.
	ErrInvalidContextSnapshot = errors.New("query: invalid context snapshot")
)

// ContextSnapshot is the immutable factual view used to assemble a context
// package for one source snapshot.
type ContextSnapshot struct {
	Scope    Scope                `json:"scope"`
	Revision string               `json:"revision"`
	Facts    []fact.CanonicalFact `json:"facts"`
	Coverage []contract.Coverage  `json:"coverage,omitempty"`
	Gaps     []contract.Gap       `json:"gaps,omitempty"`
}

// ContextSnapshotReader supplies a validated factual snapshot for one scope.
type ContextSnapshotReader interface {
	ReadContextSnapshot(context.Context, Scope) (ContextSnapshot, error)
}

// Validate checks the scope, revision, bounded collections, facts, coverage,
// and gaps without correlating a fact scope to the enclosing query scope.
func (s ContextSnapshot) Validate() error {
	if err := s.Scope.Validate(); err != nil {
		return fmt.Errorf("%w: scope", ErrInvalidContextSnapshot)
	}
	if !validContextString(s.Revision, maxContextRevisionBytes) {
		return fmt.Errorf("%w: revision", ErrInvalidContextSnapshot)
	}
	if len(s.Facts) > maxContextItems {
		return fmt.Errorf("%w: facts", ErrInvalidContextSnapshot)
	}
	if len(s.Coverage) > maxContextItems {
		return fmt.Errorf("%w: coverage", ErrInvalidContextSnapshot)
	}
	if len(s.Gaps) > maxContextItems {
		return fmt.Errorf("%w: gaps", ErrInvalidContextSnapshot)
	}

	seenFactIDs := make(map[string]struct{}, len(s.Facts))
	for _, current := range s.Facts {
		if err := current.Validate(); err != nil {
			return fmt.Errorf("%w: facts", ErrInvalidContextSnapshot)
		}
		if _, exists := seenFactIDs[current.ID]; exists {
			return fmt.Errorf("%w: facts", ErrInvalidContextSnapshot)
		}
		seenFactIDs[current.ID] = struct{}{}
	}
	if err := validateContextCoverageAndGaps(s.Coverage, s.Gaps, s.Scope); err != nil {
		return fmt.Errorf("%w: coverage_or_gaps", ErrInvalidContextSnapshot)
	}
	return nil
}

// Clone returns a detached snapshot. Callers may mutate the result without
// changing the original snapshot or any nested factual values.
func (s ContextSnapshot) Clone() ContextSnapshot {
	clone := s
	if s.Facts != nil {
		clone.Facts = make([]fact.CanonicalFact, len(s.Facts))
		for index := range s.Facts {
			clone.Facts[index] = *cloneContextFact(&s.Facts[index])
		}
	}
	clone.Coverage = cloneContractCoverage(s.Coverage)
	clone.Gaps = cloneContractGaps(s.Gaps)
	return clone
}
