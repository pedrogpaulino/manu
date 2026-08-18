package analysis

import (
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/pedrogpaulino/manu/internal/contract"
)

// Registry stores analyzers by their stable descriptor identity. A registry
// has no package-global state, which keeps selection deterministic and makes
// tests independent from one another.
type Registry struct {
	analyzers []Analyzer
}

// AnalyzerDescriptor is an explicit alias for Descriptor.
type AnalyzerDescriptor = Descriptor

// AnalyzerRegistry is an explicit alias for Registry.
type AnalyzerRegistry = Registry

// NewRegistry creates a registry and registers the supplied analyzers in a
// deterministic order. Registration failures are returned before any run is
// started.
func NewRegistry(analyzers ...Analyzer) (*Registry, error) {
	registry := &Registry{analyzers: make([]Analyzer, 0, len(analyzers))}
	for _, analyzer := range analyzers {
		if err := registry.Register(analyzer); err != nil {
			return nil, err
		}
	}
	return registry, nil
}

// NewAnalyzerRegistry is an explicit constructor alias.
func NewAnalyzerRegistry(analyzers ...Analyzer) (*Registry, error) {
	return NewRegistry(analyzers...)
}

// Register adds one analyzer. An incompatible contract version is rejected
// without disturbing already registered analyzers.
func (r *Registry) Register(analyzer Analyzer) error {
	if r == nil || isNilAnalyzer(analyzer) {
		return fmt.Errorf("%w: nil analyzer", ErrInvalidAnalyzer)
	}
	descriptor := analyzer.Descriptor()
	if err := validateDescriptor(descriptor); err != nil {
		return err
	}
	for _, registered := range r.analyzers {
		other := registered.Descriptor()
		if descriptorKey(descriptor) == descriptorKey(other) {
			return fmt.Errorf("%w: %s", ErrDuplicateAnalyzer, descriptorKey(descriptor))
		}
	}
	r.analyzers = append(r.analyzers, analyzer)
	sort.SliceStable(r.analyzers, func(i, j int) bool {
		return descriptorKey(r.analyzers[i].Descriptor()) < descriptorKey(r.analyzers[j].Descriptor())
	})
	return nil
}

// RegisterAnalyzer is an explicit alias for Register.
func (r *Registry) RegisterAnalyzer(analyzer Analyzer) error {
	return r.Register(analyzer)
}

// Analyzers returns a defensive copy of all registered analyzers.
func (r *Registry) Analyzers() []Analyzer {
	if r == nil {
		return []Analyzer{}
	}
	return append([]Analyzer{}, r.analyzers...)
}

// Select returns all analyzers applicable to one artifact. Fallback analyzers
// are not replaced by specialized matches; they remain in the result and
// are sorted by descriptor identity.
func (r *Registry) Select(input ArtifactInput) []Analyzer {
	if r == nil {
		return []Analyzer{}
	}
	selected := make([]Analyzer, 0, len(r.analyzers))
	for _, analyzer := range r.analyzers {
		if matches(analyzer.Descriptor(), input) {
			selected = append(selected, analyzer)
		}
	}
	sort.SliceStable(selected, func(i, j int) bool {
		return descriptorKey(selected[i].Descriptor()) < descriptorKey(selected[j].Descriptor())
	})
	return selected
}

// SelectAnalyzers is an explicit alias for callers that prefer the longer
// method name.
func (r *Registry) SelectAnalyzers(input ArtifactInput) []Analyzer {
	return r.Select(input)
}

func validateDescriptor(descriptor Descriptor) error {
	if strings.TrimSpace(descriptor.ID) == "" || strings.TrimSpace(descriptor.Version) == "" || strings.TrimSpace(descriptor.Method) == "" {
		return fmt.Errorf("%w: id, version, and method are required", ErrInvalidAnalyzer)
	}
	if descriptor.ContractVersion != contract.Version {
		return fmt.Errorf("%w: %s uses contract %q", ErrInvalidAnalyzer, descriptor.ID, descriptor.ContractVersion)
	}
	return nil
}

func descriptorKey(descriptor Descriptor) string {
	return strings.Join([]string{
		descriptor.ID,
		descriptor.Version,
		descriptor.Method,
	}, "\x00")
}

func isNilAnalyzer(analyzer Analyzer) bool {
	if analyzer == nil {
		return true
	}
	value := reflect.ValueOf(analyzer)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func matches(descriptor Descriptor, input ArtifactInput) bool {
	if len(descriptor.SourceTypes) > 0 && !matchesType(descriptor.SourceTypes, input.SourceType) {
		return false
	}
	typ := artifactType(input.Artifact, input.SourceArtifact)
	if len(descriptor.ArtifactTypes) == 0 {
		return true
	}
	return matchesType(descriptor.ArtifactTypes, typ)
}

func matchesType(values []string, wanted string) bool {
	wanted = strings.ToLower(strings.TrimSpace(wanted))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == ArtifactTypeAny || value == wanted {
			return true
		}
	}
	return false
}
