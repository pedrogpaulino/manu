package query

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/pedrogpaulino/manu/internal/contract"
)

var (
	// ErrInvalidContextPackageIdentity identifies an incoherent package
	// identity input or binding. It intentionally carries no package payload.
	ErrInvalidContextPackageIdentity = errors.New("query: invalid context package identity")
)

// ContextPackageIdentityBinding contains policy material that contributes to
// the final package identity but is not part of ContextPackage itself.
// PolicyContinuationIDs preserve the policy result's semantic order.
type ContextPackageIdentityBinding struct {
	PolicyDigest          string
	PolicyContinuationIDs []string
	PolicyFiltered        bool
}

// contextPackageIdentityMaterial is deliberately a typed struct. Field order
// is part of the identity contract and must remain stable across callers.
// Package ID and digest are excluded because they are assigned by the
// finalizer itself.
type contextPackageIdentityMaterial struct {
	Version               string
	Revision              string
	Scope                 Scope
	Intent                Intent
	Limits                ContextLimits
	Items                 []ContextItem
	Relations             []ContextRelation
	Coverage              []contract.Coverage
	Gaps                  []contract.Gap
	Degradations          []ContextDegradation
	Audit                 []ContextSelectionAudit
	TokenEstimate         int
	CharactersUsed        int64
	BytesUsed             int64
	Truncated             bool
	Continuation          *ContextContinuation
	PolicyDigest          string
	PolicyContinuationIDs []string
	PolicyFiltered        bool
}

// FinalizeContextPackage validates package material, binds policy output and
// assigns deterministic Digest and ID. The input package and binding remain
// unchanged; collection order is preserved as semantic input order.
func FinalizeContextPackage(ctx context.Context, input ContextPackage, binding ContextPackageIdentityBinding) (ContextPackage, error) {
	if ctx == nil {
		return ContextPackage{}, ErrInvalidContextPackageIdentity
	}
	if err := ctx.Err(); err != nil {
		return ContextPackage{}, err
	}
	if input.ID != "" || input.Digest != "" {
		return ContextPackage{}, ErrInvalidContextPackageIdentity
	}

	if err := validateContextPackageIdentityBinding(input, binding); err != nil {
		return ContextPackage{}, err
	}
	if err := validateContextPackageIdentityMaterial(input); err != nil {
		return ContextPackage{}, err
	}
	if err := ctx.Err(); err != nil {
		return ContextPackage{}, err
	}

	material := contextPackageIdentityMaterial{
		Version:               input.Version,
		Revision:              input.Revision,
		Scope:                 input.Scope,
		Intent:                input.Intent,
		Limits:                input.Limits,
		Items:                 input.Items,
		Relations:             input.Relations,
		Coverage:              input.Coverage,
		Gaps:                  input.Gaps,
		Degradations:          input.Degradations,
		Audit:                 input.Audit,
		TokenEstimate:         input.TokenEstimate,
		CharactersUsed:        input.CharactersUsed,
		BytesUsed:             input.BytesUsed,
		Truncated:             input.Truncated,
		Continuation:          input.Continuation,
		PolicyDigest:          binding.PolicyDigest,
		PolicyContinuationIDs: cloneContextPackageIdentityIDs(binding.PolicyContinuationIDs),
		PolicyFiltered:        binding.PolicyFiltered,
	}
	encoded, err := json.Marshal(material)
	if err != nil {
		return ContextPackage{}, fmt.Errorf("%w: material encoding", ErrInvalidContextPackageIdentity)
	}
	if err := ctx.Err(); err != nil {
		return ContextPackage{}, err
	}
	digest := sha256.Sum256(encoded)
	final := input.Clone()
	final.Digest = hex.EncodeToString(digest[:])
	final.ID = "context-" + final.Digest
	if err := final.Validate(); err != nil {
		return ContextPackage{}, fmt.Errorf("%w: finalized package", ErrInvalidContextPackageIdentity)
	}
	return final, nil
}

func cloneContextPackageIdentityIDs(values []string) []string {
	if values == nil {
		return nil
	}
	clone := make([]string, len(values))
	copy(clone, values)
	return clone
}

func validateContextPackageIdentityBinding(input ContextPackage, binding ContextPackageIdentityBinding) error {
	if !isSHA256(binding.PolicyDigest) {
		return ErrInvalidContextPackageIdentity
	}
	itemIDs := make(map[string]struct{}, len(input.Items))
	for _, item := range input.Items {
		if !validContextID(item.ID) {
			return ErrInvalidContextPackageIdentity
		}
		if _, exists := itemIDs[item.ID]; exists {
			return ErrInvalidContextPackageIdentity
		}
		itemIDs[item.ID] = struct{}{}
	}
	seen := make(map[string]struct{}, len(binding.PolicyContinuationIDs))
	for _, id := range binding.PolicyContinuationIDs {
		if !validContextID(id) {
			return ErrInvalidContextPackageIdentity
		}
		if _, exists := seen[id]; exists {
			return ErrInvalidContextPackageIdentity
		}
		if _, exists := itemIDs[id]; !exists {
			return ErrInvalidContextPackageIdentity
		}
		seen[id] = struct{}{}
	}
	return nil
}

// ContextPackage.Validate requires a final identity. Probe with bounded,
// content-free placeholders so every other package invariant is checked
// before the final digest is assigned.
func validateContextPackageIdentityMaterial(input ContextPackage) error {
	probe := input
	probe.ID = "context-" + zeroContextDigest
	probe.Digest = zeroContextDigest
	if err := probe.Validate(); err != nil {
		return fmt.Errorf("%w: package material", ErrInvalidContextPackageIdentity)
	}
	return nil
}

const zeroContextDigest = "0000000000000000000000000000000000000000000000000000000000000000"
