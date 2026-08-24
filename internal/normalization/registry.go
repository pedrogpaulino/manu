package normalization

import (
	"context"
	"reflect"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/pedrogpaulino/manu/internal/bundle"
	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/fact"
)

// Registry is an immutable dispatch table. Its map is constructed once by
// NewRegistry and is only read by Normalize, so concurrent reads are safe.
type Registry struct {
	normalizers map[string]Normalizer
}

// NewRegistry validates and copies registrations into an immutable registry.
func NewRegistry(registrations ...Registration) (*Registry, error) {
	normalizers := make(map[string]Normalizer, len(registrations))
	for _, registration := range registrations {
		if err := validateRegistration(registration); err != nil {
			return nil, err
		}
		key := registrationKey(
			registration.FrontendID,
			registration.FrontendVersion,
			registration.FrontendMethod,
			registration.ContributionType,
		)
		if _, exists := normalizers[key]; exists {
			return nil, ErrDuplicateRegistration
		}
		normalizers[key] = registration.Normalizer
	}
	return &Registry{normalizers: normalizers}, nil
}

// Normalize validates one contribution, dispatches an exact registration,
// and returns detached deterministic output. An absent registration produces
// an explicit incomplete fallback instead of silently discarding extensions.
func (r *Registry) Normalize(ctx context.Context, input Input) (Output, error) {
	if err := validateContext(ctx); err != nil {
		return Output{}, err
	}
	if r == nil {
		return Output{}, ErrInvalidInput
	}
	if err := validateInput(input); err != nil {
		return Output{}, err
	}
	prepared := cloneInput(input)
	key := registrationKey(
		prepared.Manifest.ID,
		prepared.Manifest.Version,
		prepared.Manifest.Method,
		prepared.Contribution.Type,
	)
	normalizer, exists := r.normalizers[key]
	if !exists {
		return fallbackOutput(prepared), nil
	}

	output, err := invokeNormalizer(ctx, normalizer, prepared)
	if ctx.Err() != nil {
		return Output{}, ErrInvalidInput
	}
	if err != nil {
		return Output{}, ErrNormalizationFailed
	}
	return prepareOutput(prepared, output)
}

func validateContext(ctx context.Context) error {
	if ctx == nil || ctx.Err() != nil {
		return ErrInvalidInput
	}
	return nil
}

func validateRegistration(registration Registration) error {
	if !validText(registration.FrontendID) ||
		!validText(registration.FrontendVersion) ||
		!validText(registration.FrontendMethod) ||
		!validText(registration.ContributionType) ||
		isNilNormalizer(registration.Normalizer) {
		return ErrInvalidRegistration
	}
	return nil
}

func registrationKey(frontendID, frontendVersion, frontendMethod, contributionType string) string {
	return strings.Join([]string{frontendID, frontendVersion, frontendMethod, contributionType}, "\x00")
}

func validText(value string) bool {
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

func isNilNormalizer(normalizer Normalizer) bool {
	if normalizer == nil {
		return true
	}
	value := reflect.ValueOf(normalizer)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func invokeNormalizer(ctx context.Context, normalizer Normalizer, input Input) (output Output, err error) {
	defer func() {
		if recover() != nil {
			output = Output{}
			err = ErrNormalizationFailed
		}
	}()
	return normalizer.Normalize(ctx, input)
}

func validateInput(input Input) error {
	if err := input.Scope.Validate(); err != nil {
		return ErrInvalidInput
	}
	if err := input.Manifest.Validate(); err != nil {
		return ErrInvalidInput
	}
	if err := input.Contribution.Validate(); err != nil {
		return ErrInvalidInput
	}
	if input.Contribution.AnalyzerID != input.Manifest.ID ||
		input.Contribution.AnalyzerVersion != input.Manifest.Version ||
		input.Contribution.Method != input.Manifest.Method {
		return ErrInvalidInput
	}
	if input.Contribution.Locator.SourceID != "" && input.Contribution.Locator.SourceID != input.Scope.SourceID {
		return ErrInvalidInput
	}
	if input.Contribution.Locator.ArtifactID != "" && input.Contribution.Locator.ArtifactID != input.Contribution.ArtifactID {
		return ErrInvalidInput
	}

	seenEvidence := make(map[string]struct{}, len(input.Evidence))
	for _, evidence := range input.Evidence {
		if err := evidence.Validate(input.Scope); err != nil {
			return ErrInvalidInput
		}
		if _, exists := seenEvidence[evidence.ID]; exists {
			return ErrInvalidInput
		}
		seenEvidence[evidence.ID] = struct{}{}
	}
	for _, extension := range input.Extensions {
		if err := extension.Validate([]fact.FrontendManifest{input.Manifest}); err != nil {
			return ErrInvalidInput
		}
	}
	return nil
}

func prepareOutput(input Input, raw Output) (Output, error) {
	result := Output{
		Facts:      cloneFacts(raw.Facts),
		Extensions: cloneExtensions(input.Extensions),
		Coverage:   cloneCoverage(raw.Coverage),
	}

	evidenceByID := make(map[string]fact.EvidenceRef, len(input.Evidence))
	for _, evidence := range input.Evidence {
		evidenceByID[evidence.ID] = evidence
	}
	seenFacts := make(map[string]struct{}, len(result.Facts))
	for index := range result.Facts {
		candidate := &result.Facts[index]
		if err := candidate.Validate(); err != nil {
			return Output{}, ErrInvalidOutput
		}
		if candidate.Scope != input.Scope || candidate.Producer != producerFor(input.Manifest) || !manifestDeclaresPredicate(input.Manifest, candidate.Predicate) {
			return Output{}, ErrInvalidOutput
		}
		if _, exists := seenFacts[candidate.ID]; exists {
			return Output{}, ErrInvalidOutput
		}
		seenFacts[candidate.ID] = struct{}{}
		for _, evidence := range candidate.Evidence {
			declared, exists := evidenceByID[evidence.ID]
			if !exists || declared.Locator != evidence.Locator {
				return Output{}, ErrInvalidOutput
			}
		}
	}
	sort.SliceStable(result.Facts, func(left, right int) bool {
		return result.Facts[left].ID < result.Facts[right].ID
	})

	newExtensions := cloneExtensions(raw.Extensions)
	for _, extension := range newExtensions {
		if err := extension.Validate([]fact.FrontendManifest{input.Manifest}); err != nil {
			return Output{}, ErrInvalidOutput
		}
	}
	sort.SliceStable(newExtensions, func(left, right int) bool {
		return extensionKey(newExtensions[left]) < extensionKey(newExtensions[right])
	})
	result.Extensions = append(result.Extensions, newExtensions...)

	seenCoverage := make(map[string]struct{}, len(result.Coverage))
	for index := range result.Coverage {
		coverage := &result.Coverage[index]
		if !validCoverage(*coverage, input) {
			return Output{}, ErrInvalidOutput
		}
		if _, exists := seenCoverage[coverage.ID]; exists {
			return Output{}, ErrInvalidOutput
		}
		seenCoverage[coverage.ID] = struct{}{}
	}
	sort.SliceStable(result.Coverage, func(left, right int) bool {
		return result.Coverage[left].ID < result.Coverage[right].ID
	})
	return result, nil
}

func fallbackOutput(input Input) Output {
	result := Output{
		Extensions: cloneExtensions(input.Extensions),
		Coverage:   make([]contract.Coverage, 0, len(input.Manifest.Capabilities)),
	}
	for _, capability := range input.Manifest.Capabilities {
		coverage := contract.Coverage{
			Dimension:  string(capability),
			Scope:      input.Contribution.ID,
			State:      contract.CoverageIncomplete,
			AnalyzerID: input.Manifest.ID,
			Message:    "no universal normalization mapping",
		}
		coverage.ID = contract.CoverageID(coverage.Dimension, coverage.Scope, coverage.State, coverage.AnalyzerID)
		result.Coverage = append(result.Coverage, coverage)
	}
	sort.SliceStable(result.Coverage, func(left, right int) bool {
		return result.Coverage[left].ID < result.Coverage[right].ID
	})
	return result
}

func validCoverage(coverage contract.Coverage, input Input) bool {
	if coverage.Validate() != nil || coverage.Scope != input.Contribution.ID || coverage.AnalyzerID != input.Manifest.ID || !manifestDeclaresDimension(input.Manifest, coverage.Dimension) {
		return false
	}
	expectedID := contract.CoverageID(coverage.Dimension, coverage.Scope, coverage.State, coverage.AnalyzerID)
	return coverage.ID == expectedID
}

func producerFor(manifest fact.FrontendManifest) fact.Producer {
	return fact.Producer{ID: manifest.ID, Version: manifest.Version, Method: manifest.Method}
}

func manifestDeclaresPredicate(manifest fact.FrontendManifest, predicate fact.Predicate) bool {
	for _, declared := range manifest.Predicates {
		if declared == predicate {
			return true
		}
	}
	return false
}

func manifestDeclaresDimension(manifest fact.FrontendManifest, dimension string) bool {
	for _, declared := range manifest.Capabilities {
		if string(declared) == dimension {
			return true
		}
	}
	return false
}

func extensionKey(extension bundle.ExtensionRecord) string {
	return strings.Join([]string{
		extension.SchemaID,
		extension.SchemaVersion,
		extension.SchemaDigest,
		string(extension.Schema),
		string(extension.Payload),
	}, "\x00")
}

func cloneInput(input Input) Input {
	return Input{
		Scope:        input.Scope,
		Manifest:     cloneManifest(input.Manifest),
		Contribution: cloneContribution(input.Contribution),
		Evidence:     cloneEvidence(input.Evidence),
		Extensions:   cloneExtensions(input.Extensions),
	}
}

func cloneManifest(manifest fact.FrontendManifest) fact.FrontendManifest {
	manifest.SourceTypes = cloneStrings(manifest.SourceTypes)
	manifest.Families = cloneStrings(manifest.Families)
	manifest.Versions = cloneStrings(manifest.Versions)
	manifest.Capabilities = appendEnumDimensions(nil, manifest.Capabilities)
	manifest.Limitations = cloneStrings(manifest.Limitations)
	manifest.Predicates = appendPredicates(nil, manifest.Predicates)
	manifest.Extensions = appendExtensionSchemas(nil, manifest.Extensions)
	return manifest
}

func cloneContribution(contribution contract.Contribution) contract.Contribution {
	if contribution.Value != nil {
		contribution.Value = append([]byte(nil), contribution.Value...)
	}
	return contribution
}

func cloneEvidence(evidence []fact.EvidenceRef) []fact.EvidenceRef {
	if evidence == nil {
		return nil
	}
	return append([]fact.EvidenceRef(nil), evidence...)
}

func cloneExtensions(extensions []bundle.ExtensionRecord) []bundle.ExtensionRecord {
	if extensions == nil {
		return nil
	}
	cloned := make([]bundle.ExtensionRecord, len(extensions))
	for index, extension := range extensions {
		cloned[index] = extension
		cloned[index].Schema = append([]byte(nil), extension.Schema...)
		cloned[index].Payload = append([]byte(nil), extension.Payload...)
	}
	return cloned
}

func cloneFacts(facts []fact.CanonicalFact) []fact.CanonicalFact {
	if facts == nil {
		return nil
	}
	cloned := make([]fact.CanonicalFact, len(facts))
	for index, candidate := range facts {
		cloned[index] = cloneFact(candidate)
	}
	return cloned
}

func cloneFact(candidate fact.CanonicalFact) fact.CanonicalFact {
	clone := candidate
	if candidate.Object != nil {
		object := *candidate.Object
		clone.Object = &object
	}
	if candidate.Value != nil {
		value := *candidate.Value
		clone.Value = &value
	}
	clone.Qualifiers = append([]fact.Qualifier(nil), candidate.Qualifiers...)
	clone.Evidence = append([]fact.EvidenceRef(nil), candidate.Evidence...)
	if candidate.Lineage != nil {
		lineage := *candidate.Lineage
		lineage.InputFactIDs = append([]string(nil), candidate.Lineage.InputFactIDs...)
		clone.Lineage = &lineage
	}
	return clone
}

func cloneCoverage(coverage []contract.Coverage) []contract.Coverage {
	if coverage == nil {
		return nil
	}
	cloned := make([]contract.Coverage, len(coverage))
	for index, value := range coverage {
		cloned[index] = value
		if value.Locator != nil {
			locator := *value.Locator
			cloned[index].Locator = &locator
		}
	}
	return cloned
}

func cloneStrings(values []string) []string {
	if values == nil {
		return nil
	}
	return append([]string(nil), values...)
}

func appendEnumDimensions(destination []contract.Dimension, values []contract.Dimension) []contract.Dimension {
	if values == nil {
		return nil
	}
	return append(destination, values...)
}

func appendPredicates(destination []fact.Predicate, values []fact.Predicate) []fact.Predicate {
	if values == nil {
		return nil
	}
	return append(destination, values...)
}

func appendExtensionSchemas(destination []fact.ExtensionSchema, values []fact.ExtensionSchema) []fact.ExtensionSchema {
	if values == nil {
		return nil
	}
	return append(destination, values...)
}
