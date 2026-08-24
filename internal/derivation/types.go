package derivation

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/pedrogpaulino/manu/internal/fact"
)

var (
	// ErrInvalidRule identifies a nil or malformed rule registration.
	ErrInvalidRule = errors.New("derivation: invalid rule")
	// ErrDuplicateRule identifies a rule ID/version already registered.
	ErrDuplicateRule = fmt.Errorf("%w: duplicate rule", ErrInvalidRule)
	// ErrInvalidInput identifies facts that cannot enter one derivation run.
	ErrInvalidInput = errors.New("derivation: invalid input")
	// ErrDuplicateFact identifies repeated factual identities in one input set.
	ErrDuplicateFact = fmt.Errorf("%w: duplicate fact", ErrInvalidInput)
	// ErrInvalidOutput identifies a candidate that violates the derivation
	// contract. The error intentionally carries no factual payload.
	ErrInvalidOutput = errors.New("derivation: invalid output")
	// ErrRuleFailed identifies a rule failure without exposing its error or
	// output payload to a caller.
	ErrRuleFailed = errors.New("derivation: rule failed")

	// Compatibility aliases use the vocabulary of the other registries in the
	// repository while keeping one sentinel for each boundary.
	ErrInvalidRegistration   = ErrInvalidRule
	ErrDuplicateRegistration = ErrDuplicateRule
	ErrInvalidRuleOutput     = ErrInvalidOutput
	ErrRuleExecution         = ErrRuleFailed
)

// RuleVersion identifies one immutable rule implementation by its external
// ID and version. The pair is the exact identity used for registration and
// lineage validation; changing either component creates another rule.
type RuleVersion struct {
	RuleID  string `json:"rule_id"`
	Version string `json:"version"`
}

// Validate checks the stable textual identity of a rule.
func (v RuleVersion) Validate() error {
	if !validIdentifier(v.RuleID) || !validIdentifier(v.Version) {
		return ErrInvalidRule
	}
	return nil
}

// Identity returns the internal deterministic key for a valid rule version.
// Callers normally do not need this key; it is exposed to make registry
// inspection and deterministic ordering straightforward.
func (v RuleVersion) Identity() string {
	return v.RuleID + "\x00" + v.Version
}

// Rule is the public derivation port. The executor supplies a read-only view
// of the complete factual set currently known to the fixed-point evaluation.
// A rule may return zero or more candidates, but it must not mutate the view
// or the facts returned from it.
type Rule interface {
	Apply(context.Context, FactView) ([]fact.CanonicalFact, error)
}

// RuleFunc adapts a function to Rule.
type RuleFunc func(context.Context, FactView) ([]fact.CanonicalFact, error)

var _ Rule = RuleFunc(nil)

// Apply implements Rule.
func (f RuleFunc) Apply(ctx context.Context, view FactView) ([]fact.CanonicalFact, error) {
	if f == nil {
		return nil, ErrRuleFailed
	}
	return f(ctx, view)
}

// Registration binds a monotonic rule implementation to its immutable
// external ID and version. The zero value is not a valid registration.
type Registration struct {
	RuleID  string `json:"rule_id"`
	Version string `json:"version"`
	Rule    Rule   `json:"-"`
}

// RuleRegistration is a domain-oriented alias for Registration.
type RuleRegistration = Registration

// NewRegistration creates a registration. Validation is performed when the
// registration enters a Registry, allowing callers to assemble a list before
// choosing the construction boundary.
func NewRegistration(version RuleVersion, rule Rule) Registration {
	return Registration{
		RuleID:  version.RuleID,
		Version: version.Version,
		Rule:    rule,
	}
}

// NewRuleRegistration is an explicit constructor alias.
func NewRuleRegistration(version RuleVersion, rule Rule) Registration {
	return NewRegistration(version, rule)
}

// Version returns the registration's rule identity.
func (r Registration) VersionInfo() RuleVersion {
	return RuleVersion{RuleID: r.RuleID, Version: r.Version}
}

// Validate checks the registration before it is copied into a Registry.
func (r Registration) Validate() error {
	if err := r.VersionInfo().Validate(); err != nil {
		return err
	}
	if isNilRule(r.Rule) {
		return ErrInvalidRule
	}
	return nil
}

// FactView is a read-only, deterministic view of canonical facts. It has no
// exported slice or map, and each accessor returns detached facts, so a rule
// cannot mutate executor state through the view.
type FactView struct {
	facts []fact.CanonicalFact
	index map[string]int
}

// FactSet is a shorter compatibility spelling for FactView.
type FactSet = FactView

// Len reports the number of facts in the view.
func (v FactView) Len() int {
	return len(v.facts)
}

// At returns a detached fact at index. It reports false for an invalid index.
func (v FactView) At(index int) (fact.CanonicalFact, bool) {
	if index < 0 || index >= len(v.facts) {
		return fact.CanonicalFact{}, false
	}
	return cloneFact(v.facts[index]), true
}

// Get returns a detached fact by its canonical identity.
func (v FactView) Get(id string) (fact.CanonicalFact, bool) {
	index, ok := v.index[id]
	if !ok {
		return fact.CanonicalFact{}, false
	}
	return cloneFact(v.facts[index]), true
}

// Lookup is an explicit alias for Get.
func (v FactView) Lookup(id string) (fact.CanonicalFact, bool) {
	return v.Get(id)
}

// Facts returns a detached copy in canonical identity order.
func (v FactView) Facts() []fact.CanonicalFact {
	return cloneFacts(v.facts)
}

// All is an explicit alias for Facts.
func (v FactView) All() []fact.CanonicalFact {
	return v.Facts()
}

// IDs returns the ordered identities in the view.
func (v FactView) IDs() []string {
	ids := make([]string, len(v.facts))
	for index, candidate := range v.facts {
		ids[index] = candidate.ID
	}
	return ids
}

// Scope returns the common scope of the view. An empty view has no scope.
func (v FactView) Scope() (fact.Scope, bool) {
	if len(v.facts) == 0 {
		return fact.Scope{}, false
	}
	return v.facts[0].Scope, true
}

func newFactView(facts []fact.CanonicalFact) FactView {
	cloned := cloneFacts(facts)
	index := make(map[string]int, len(cloned))
	for position, candidate := range cloned {
		index[candidate.ID] = position
	}
	return FactView{facts: cloned, index: index}
}

func validIdentifier(value string) bool {
	if strings.TrimSpace(value) == "" || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if unicode.IsSpace(character) || unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func isNilRule(rule Rule) bool {
	if rule == nil {
		return true
	}
	value := reflect.ValueOf(rule)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}
