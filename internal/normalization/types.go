// Package normalization dispatches one contribution to the exact frontend
// normalizer that declared support for it. It keeps unsupported mappings
// explicit without discarding extension data.
package normalization

import (
	"context"
	"errors"
	"fmt"

	"github.com/pedrogpaulino/manu/internal/bundle"
	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/fact"
)

var (
	// ErrInvalidInput identifies a value that cannot cross the normalization
	// boundary safely.
	ErrInvalidInput = errors.New("normalization: invalid input")
	// ErrInvalidRegistration identifies a malformed registry entry.
	ErrInvalidRegistration = errors.New("normalization: invalid registration")
	// ErrDuplicateRegistration identifies a registration key already present in
	// a registry.
	ErrDuplicateRegistration = fmt.Errorf("%w: duplicate registration", ErrInvalidRegistration)
	// ErrInvalidOutput identifies output that does not satisfy the canonical
	// fact, extension, or coverage contract.
	ErrInvalidOutput = errors.New("normalization: invalid normalizer output")
	// ErrNormalizationFailed identifies a normalizer failure without exposing
	// its implementation error or any source payload.
	ErrNormalizationFailed = errors.New("normalization: normalizer failed")

	// ErrInvalidNormalizerOutput is a descriptive compatibility alias.
	ErrInvalidNormalizerOutput = ErrInvalidOutput
	// ErrNormalizerFailure is a descriptive compatibility alias.
	ErrNormalizerFailure = ErrNormalizationFailed
)

// Input is the complete, scoped input presented to one normalizer.
type Input struct {
	Scope        fact.Scope               `json:"scope"`
	Manifest     fact.FrontendManifest    `json:"manifest"`
	Contribution contract.Contribution    `json:"contribution"`
	Evidence     []fact.EvidenceRef       `json:"evidence,omitempty"`
	Extensions   []bundle.ExtensionRecord `json:"extensions,omitempty"`
}

// Output contains only additive canonical facts, extensions, and coverage
// produced for one input contribution.
type Output struct {
	Facts      []fact.CanonicalFact     `json:"facts,omitempty"`
	Extensions []bundle.ExtensionRecord `json:"extensions,omitempty"`
	Coverage   []contract.Coverage      `json:"coverage,omitempty"`
}

// Normalizer converts one validated contribution into canonical output.
type Normalizer interface {
	Normalize(context.Context, Input) (Output, error)
}

// NormalizerFunc adapts a function to Normalizer.
type NormalizerFunc func(context.Context, Input) (Output, error)

// Normalize implements Normalizer.
func (f NormalizerFunc) Normalize(ctx context.Context, input Input) (Output, error) {
	if f == nil {
		return Output{}, ErrNormalizationFailed
	}
	return f(ctx, input)
}

// Registration binds one frontend identity and contribution type to a
// normalizer. The zero value is not a valid registration.
type Registration struct {
	FrontendID       string     `json:"frontend_id"`
	FrontendVersion  string     `json:"frontend_version"`
	FrontendMethod   string     `json:"frontend_method"`
	ContributionType string     `json:"contribution_type"`
	Normalizer       Normalizer `json:"-"`
}
