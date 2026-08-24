package persistence

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"github.com/pedrogpaulino/manu/internal/fact"
)

var (
	// ErrInvalidDerivation identifies a rebuild request or result that cannot
	// cross the persistence boundary. It deliberately contains no factual
	// identity or rule payload.
	ErrInvalidDerivation = fmt.Errorf("%w: invalid factual derivation", ErrInvalidInput)
	// ErrInvalidDerivationOutput identifies a deriver result that does not
	// preserve the observed facts or does not satisfy the canonical contract.
	ErrInvalidDerivationOutput = fmt.Errorf("%w: invalid derivation output", ErrInvalidDerivation)
)

// FactualDeriver is the smallest application port needed by a derivation
// rebuild. Implementations receive a detached set of observed facts and must
// return the complete detached factual result for the same snapshot.
type FactualDeriver interface {
	Derive(context.Context, []fact.CanonicalFact) ([]fact.CanonicalFact, error)
}

// RebuildFactualDerivations evaluates new rule versions against the observed
// facts of one persisted snapshot and appends the resulting derivations.
//
// Reading and derivation happen before the write transaction is opened. The
// write path is the existing atomic PersistFactualSnapshot boundary, which is
// insert-only: previous rule versions and their facts remain available for
// comparison with the new result. A retry therefore reuses the same
// immutable identities without deleting or reclassifying prior rows.
func (r *Repository) RebuildFactualDerivations(
	ctx context.Context,
	organizationID string,
	sourceID string,
	snapshotID string,
	deriver FactualDeriver,
	ruleVersions []RuleVersion,
) error {
	if err := validateContext(ctx); err != nil {
		return err
	}
	if r == nil || r.starter == nil || isNilFactualDeriver(deriver) {
		return ErrInvalidDerivation
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{name: "organization_id", value: organizationID},
		{name: "source_id", value: sourceID},
		{name: "snapshot_id", value: snapshotID},
	} {
		if err := validateUUID(field.name, field.value); err != nil {
			return ErrInvalidDerivation
		}
	}

	stored, err := r.ReadFactualSnapshot(ctx, organizationID, sourceID, snapshotID)
	if err != nil {
		return err
	}
	if err := validateContext(ctx); err != nil {
		return err
	}

	newRules := cloneRuleVersions(ruleVersions)
	if err := validateRebuildRules(stored.Scope, newRules); err != nil {
		return err
	}
	observed := observedFacts(stored.Facts)
	derivationInput := cloneCanonicalFacts(observed)
	derived, deriveErr := invokeFactualDeriver(ctx, deriver, derivationInput)
	if deriveErr != nil {
		if err := validateContext(ctx); err != nil {
			return err
		}
		if errors.Is(deriveErr, context.Canceled) {
			return context.Canceled
		}
		if errors.Is(deriveErr, context.DeadlineExceeded) {
			return context.DeadlineExceeded
		}
		return ErrInvalidDerivation
	}
	if err := validateContext(ctx); err != nil {
		return err
	}

	facts, err := validateRebuildOutput(stored.Scope, observed, derived)
	if err != nil {
		return err
	}
	input := FactualSnapshotInput{
		OrganizationID:    stored.OrganizationID,
		SourceID:          stored.SourceID,
		SnapshotID:        stored.SnapshotID,
		Scope:             stored.Scope,
		FrontendManifests: cloneFrontendManifests(stored.FrontendManifests),
		RuleVersions:      newRules,
		Facts:             facts,
	}
	prepared, err := PrepareFactualSnapshot(input)
	if err != nil {
		return errors.Join(ErrInvalidDerivationOutput, ErrInvalidFactualSnapshot)
	}
	if err := validateContext(ctx); err != nil {
		return err
	}
	return r.PersistFactualSnapshot(ctx, factualInputFromPrepared(prepared))
}

func validateRebuildRules(scope fact.Scope, rules []RuleVersion) error {
	if err := scope.Validate(); err != nil {
		return ErrInvalidDerivation
	}
	if _, _, err := prepareFactualRules(FactualSnapshotInput{
		Scope:        scope,
		RuleVersions: rules,
	}); err != nil {
		return ErrInvalidDerivation
	}
	return nil
}

func observedFacts(facts []fact.CanonicalFact) []fact.CanonicalFact {
	observed := make([]fact.CanonicalFact, 0, len(facts))
	for _, candidate := range facts {
		if candidate.Lineage == nil {
			observed = append(observed, cloneCanonicalFact(candidate))
		}
	}
	return observed
}

func validateRebuildOutput(
	scope fact.Scope,
	observed []fact.CanonicalFact,
	derived []fact.CanonicalFact,
) ([]fact.CanonicalFact, error) {
	observedByID := make(map[string]fact.CanonicalFact, len(observed))
	for _, candidate := range observed {
		observedByID[candidate.ID] = candidate
	}
	byID := make(map[string]fact.CanonicalFact, len(derived))
	for _, candidate := range derived {
		if err := candidate.Validate(); err != nil || candidate.Scope != scope {
			return nil, ErrInvalidDerivationOutput
		}
		if expected, isObserved := observedByID[candidate.ID]; isObserved {
			if !reflect.DeepEqual(candidate, expected) {
				return nil, ErrInvalidDerivationOutput
			}
		} else if candidate.Lineage == nil {
			return nil, ErrInvalidDerivationOutput
		}
		if _, exists := byID[candidate.ID]; exists {
			return nil, ErrInvalidDerivationOutput
		}
		byID[candidate.ID] = cloneCanonicalFact(candidate)
	}
	for _, expected := range observed {
		actual, exists := byID[expected.ID]
		if !exists || !reflect.DeepEqual(actual, expected) || actual.Lineage != nil {
			return nil, ErrInvalidDerivationOutput
		}
	}
	result := make([]fact.CanonicalFact, 0, len(byID))
	for _, candidate := range byID {
		result = append(result, candidate)
	}
	return result, nil
}

func cloneRuleVersions(rules []RuleVersion) []RuleVersion {
	cloned := make([]RuleVersion, len(rules))
	for index, rule := range rules {
		cloned[index] = rule
		cloned[index].Configuration = append([]byte(nil), rule.Configuration...)
	}
	return cloned
}

func cloneFrontendManifests(manifests []fact.FrontendManifest) []fact.FrontendManifest {
	cloned := make([]fact.FrontendManifest, len(manifests))
	for index, manifest := range manifests {
		canonical, err := fact.CanonicalFrontendManifest(manifest)
		if err == nil {
			cloned[index] = canonical
			continue
		}
		cloned[index] = manifest
	}
	return cloned
}

func cloneCanonicalFacts(facts []fact.CanonicalFact) []fact.CanonicalFact {
	cloned := make([]fact.CanonicalFact, len(facts))
	for index, candidate := range facts {
		cloned[index] = cloneCanonicalFact(candidate)
	}
	return cloned
}

func invokeFactualDeriver(
	ctx context.Context,
	deriver FactualDeriver,
	facts []fact.CanonicalFact,
) (result []fact.CanonicalFact, err error) {
	defer func() {
		if recover() != nil {
			result = nil
			err = ErrInvalidDerivation
		}
	}()
	return deriver.Derive(ctx, facts)
}

func isNilFactualDeriver(deriver FactualDeriver) bool {
	if deriver == nil {
		return true
	}
	value := reflect.ValueOf(deriver)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}
