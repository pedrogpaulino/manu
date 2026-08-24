package derivation

import (
	"fmt"
	"sort"

	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/fact"
)

const (
	// DerivationAnalyzerID identifies coverage and gaps emitted by the
	// derivation executor rather than by one of the source frontends.
	DerivationAnalyzerID = "derivation"

	// DerivationIterationLimitCode identifies a bounded work-queue run that
	// stopped before the least fixed point.
	DerivationIterationLimitCode = "derivation_iteration_limit"
	// DerivationFactLimitCode identifies a candidate batch that would exceed
	// the total factual result limit.
	DerivationFactLimitCode = "derivation_fact_limit"
	// DerivationFanoutLimitCode identifies a candidate batch that exceeds the
	// per-rule-invocation fanout limit.
	DerivationFanoutLimitCode = "derivation_fanout_limit"
)

var (
	// ErrInvalidLimits identifies a negative derivation limit.
	ErrInvalidLimits = fmt.Errorf("%w: invalid derivation limits", ErrInvalidInput)
	// ErrInputExceedsFactLimit identifies an input set that is already larger
	// than the configured total fact bound. The input is never truncated.
	ErrInputExceedsFactLimit = fmt.Errorf("%w: input exceeds max facts", ErrInvalidInput)
)

// DerivationLimits bounds one fixed-point run. Zero means unlimited for each
// field; negative values are invalid. MaxIterations counts facts removed from
// the ordered work queue, including initially observed facts.
type DerivationLimits struct {
	MaxIterations int
	MaxFacts      int
	MaxFanout     int
}

// Validate checks that every configured bound is zero or positive.
func (l DerivationLimits) Validate() error {
	if l.MaxIterations < 0 || l.MaxFacts < 0 || l.MaxFanout < 0 {
		return ErrInvalidLimits
	}
	return nil
}

// DerivationResult is the detached result of a bounded derivation run.
// Coverage is always present for a successful run. Gaps are present only when
// a configured bound stops the run before the least fixed point.
type DerivationResult struct {
	Facts    []fact.CanonicalFact
	Coverage []contract.Coverage
	Gaps     []contract.Gap
}

// Clone returns a detached deterministic copy of the result.
func (r DerivationResult) Clone() DerivationResult {
	clone := DerivationResult{
		Facts:    cloneFacts(r.Facts),
		Coverage: cloneCoverage(r.Coverage),
		Gaps:     cloneGaps(r.Gaps),
	}
	return clone
}

// Validate checks facts and the metadata emitted by a bounded run. It also
// checks deterministic IDs and ordering so callers can safely persist or
// compare the result without another normalization pass.
func (r DerivationResult) Validate() error {
	seenFacts := make(map[string]struct{}, len(r.Facts))
	for index, candidate := range r.Facts {
		if err := candidate.Validate(); err != nil {
			return ErrInvalidOutput
		}
		if _, exists := seenFacts[candidate.ID]; exists {
			return ErrInvalidOutput
		}
		seenFacts[candidate.ID] = struct{}{}
		if index > 0 && r.Facts[index-1].ID >= candidate.ID {
			return ErrInvalidOutput
		}
	}
	for _, candidate := range r.Facts {
		if candidate.Lineage == nil {
			continue
		}
		for _, inputID := range candidate.Lineage.InputFactIDs {
			if _, exists := seenFacts[inputID]; !exists {
				return ErrInvalidOutput
			}
		}
	}

	seenCoverage := make(map[string]struct{}, len(r.Coverage))
	for index, coverage := range r.Coverage {
		if err := coverage.Validate(); err != nil {
			return ErrInvalidOutput
		}
		if coverage.ID != contract.CoverageID(coverage.Dimension, coverage.Scope, coverage.State, coverage.AnalyzerID) {
			return ErrInvalidOutput
		}
		if _, exists := seenCoverage[coverage.ID]; exists {
			return ErrInvalidOutput
		}
		seenCoverage[coverage.ID] = struct{}{}
		if index > 0 && r.Coverage[index-1].ID >= coverage.ID {
			return ErrInvalidOutput
		}
	}

	seenGaps := make(map[string]struct{}, len(r.Gaps))
	for index, gap := range r.Gaps {
		if err := gap.Validate(); err != nil {
			return ErrInvalidOutput
		}
		if gap.ID != contract.GapID(gap.Code, gap.Dimension, gap.Scope, gap.Message, gap.AnalyzerID) {
			return ErrInvalidOutput
		}
		if _, exists := seenGaps[gap.ID]; exists {
			return ErrInvalidOutput
		}
		seenGaps[gap.ID] = struct{}{}
		if index > 0 && r.Gaps[index-1].ID >= gap.ID {
			return ErrInvalidOutput
		}
	}
	return nil
}

func derivationScope(scope fact.Scope) string {
	return scope.SnapshotID
}

func derivationCoverage(scope fact.Scope, incomplete bool) contract.Coverage {
	state := contract.CoverageProduced
	message := "derivation reached the least fixed point"
	if incomplete {
		state = contract.CoverageIncomplete
		message = "derivation stopped at a configured limit"
	}
	coverage := contract.Coverage{
		Dimension:  string(contract.DimensionEntitiesAndRelationships),
		Scope:      derivationScope(scope),
		State:      state,
		AnalyzerID: DerivationAnalyzerID,
		Message:    message,
	}
	coverage.ID = contract.CoverageID(coverage.Dimension, coverage.Scope, coverage.State, coverage.AnalyzerID)
	return coverage
}

func derivationGap(scope fact.Scope, code string) contract.Gap {
	messages := map[string]string{
		DerivationIterationLimitCode: "derivation iteration limit reached",
		DerivationFactLimitCode:      "derivation fact limit reached",
		DerivationFanoutLimitCode:    "derivation fanout limit reached",
	}
	message := messages[code]
	gap := contract.Gap{
		Code:       code,
		Dimension:  string(contract.DimensionEntitiesAndRelationships),
		Scope:      derivationScope(scope),
		Message:    message,
		AnalyzerID: DerivationAnalyzerID,
	}
	gap.ID = contract.GapID(gap.Code, gap.Dimension, gap.Scope, gap.Message, gap.AnalyzerID)
	return gap
}

func makeDerivationResult(scope fact.Scope, known map[string]fact.CanonicalFact, limitCodes map[string]struct{}) (DerivationResult, error) {
	codes := make([]string, 0, len(limitCodes))
	for code := range limitCodes {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	gaps := make([]contract.Gap, 0, len(codes))
	for _, code := range codes {
		gaps = append(gaps, derivationGap(scope, code))
	}
	sort.SliceStable(gaps, func(left, right int) bool {
		return gaps[left].ID < gaps[right].ID
	})
	result := DerivationResult{
		Facts:    orderedFacts(known),
		Coverage: []contract.Coverage{derivationCoverage(scope, len(gaps) > 0)},
		Gaps:     gaps,
	}
	if err := result.Validate(); err != nil {
		return DerivationResult{}, ErrInvalidOutput
	}
	return result.Clone(), nil
}

func cloneCoverage(values []contract.Coverage) []contract.Coverage {
	if values == nil {
		return nil
	}
	cloned := make([]contract.Coverage, len(values))
	for index, value := range values {
		cloned[index] = value
		if value.Locator != nil {
			locator := *value.Locator
			cloned[index].Locator = &locator
		}
	}
	return cloned
}

func cloneGaps(values []contract.Gap) []contract.Gap {
	if values == nil {
		return nil
	}
	cloned := make([]contract.Gap, len(values))
	for index, value := range values {
		cloned[index] = value
		if value.Locator != nil {
			locator := *value.Locator
			cloned[index].Locator = &locator
		}
	}
	return cloned
}
