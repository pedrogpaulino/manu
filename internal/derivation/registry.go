package derivation

import (
	"sort"
	"sync"
)

type registeredRule struct {
	version RuleVersion
	rule    Rule
}

// Registry stores rules by their exact ID/version identity. Registration is
// sorted immediately, so execution is independent of registration order.
type Registry struct {
	mu    sync.RWMutex
	rules []registeredRule
}

// NewRegistry validates and copies all registrations before returning a
// usable registry. A duplicate or malformed registration leaves no registry
// available to the caller.
func NewRegistry(registrations ...Registration) (*Registry, error) {
	registry := &Registry{rules: make([]registeredRule, 0, len(registrations))}
	for _, registration := range registrations {
		if err := registry.Register(registration); err != nil {
			return nil, err
		}
	}
	return registry, nil
}

// Register adds one rule and keeps the registry in canonical order.
func (r *Registry) Register(registration Registration) error {
	if r == nil {
		return ErrInvalidRule
	}
	if err := registration.Validate(); err != nil {
		return ErrInvalidRule
	}
	entry := registeredRule{
		version: registration.VersionInfo(),
		rule:    registration.Rule,
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	for _, existing := range r.rules {
		if existing.version.Identity() == entry.version.Identity() {
			return ErrDuplicateRule
		}
	}
	r.rules = append(r.rules, entry)
	sort.SliceStable(r.rules, func(left, right int) bool {
		return r.rules[left].version.Identity() < r.rules[right].version.Identity()
	})
	return nil
}

// RegisterRule is an explicit alias for Register.
func (r *Registry) RegisterRule(registration Registration) error {
	return r.Register(registration)
}

// Registrations returns detached registration values in execution order.
func (r *Registry) Registrations() []Registration {
	if r == nil {
		return []Registration{}
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]Registration, len(r.rules))
	for index, entry := range r.rules {
		result[index] = Registration{
			RuleID:  entry.version.RuleID,
			Version: entry.version.Version,
			Rule:    entry.rule,
		}
	}
	return result
}

// Rules is an explicit alias for Registrations.
func (r *Registry) Rules() []Registration {
	return r.Registrations()
}

// RuleVersions returns the ordered identities without exposing rule
// implementations.
func (r *Registry) RuleVersions() []RuleVersion {
	if r == nil {
		return []RuleVersion{}
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]RuleVersion, len(r.rules))
	for index, entry := range r.rules {
		result[index] = entry.version
	}
	return result
}

func (r *Registry) snapshot() []registeredRule {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]registeredRule(nil), r.rules...)
}
