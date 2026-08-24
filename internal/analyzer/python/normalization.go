package python

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/fact"
	"github.com/pedrogpaulino/manu/internal/normalization"
)

const (
	// MissingEvidenceCoverageMessage is deliberately fixed. It explains why
	// a contribution was not promoted without retaining source payload data.
	MissingEvidenceCoverageMessage = "Python normalization requires source evidence"
	// IncompleteCoverageMessage describes a contribution whose structural
	// observation did not have a safe universal mapping.
	IncompleteCoverageMessage = "Python normalization did not have a safe literal mapping"
	// ProducedCoverageMessage is stable operational metadata and never contains
	// a contribution payload.
	ProducedCoverageMessage = "Python normalization produced canonical facts"
)

var pythonRequiredPredicates = [...]fact.Predicate{
	fact.PredicateArtifact,
	fact.PredicateSymbol,
	fact.PredicateDefinition,
	fact.PredicateReference,
	fact.PredicateDependency,
	fact.PredicateConfiguration,
}

var pythonRequiredDimensions = [...]contract.Dimension{
	contract.DimensionLandscapeInventoryStructure,
	contract.DimensionEntitiesAndRelationships,
	contract.DimensionFlowsAndDependencies,
	contract.DimensionConfigurationVariations,
}

var pythonQualifiedNameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z_][A-Za-z0-9_]*)*$`)

// NormalizerRegistrations returns one explicit mapping for every contribution
// type emitted by the safe-static Python analyzer. The manifest method is kept
// as the producer method so observation methods (which identify one source
// occurrence) never become canonical fact provenance.
func NormalizerRegistrations(manifest fact.FrontendManifest) ([]normalization.Registration, error) {
	if err := validatePythonManifest(manifest); err != nil {
		return nil, err
	}

	mappings := []pythonMapping{
		{contributionType: ArtifactContributionType, dimension: contract.DimensionLandscapeInventoryStructure, normalize: normalizePythonArtifact},
		{contributionType: SymbolContributionType, dimension: contract.DimensionEntitiesAndRelationships, normalize: normalizePythonSymbol},
		{contributionType: ImportContributionType, dimension: contract.DimensionFlowsAndDependencies, normalize: normalizePythonImport},
		{contributionType: RelationContributionType, dimension: contract.DimensionFlowsAndDependencies, normalize: normalizePythonRelation},
		{contributionType: ConfigurationContributionType, dimension: contract.DimensionConfigurationVariations, normalize: normalizePythonConfiguration},
	}
	registrations := make([]normalization.Registration, 0, len(mappings))
	for _, mapping := range mappings {
		mapping := mapping
		registrations = append(registrations, normalization.Registration{
			FrontendID:       manifest.ID,
			FrontendVersion:  manifest.Version,
			FrontendMethod:   manifest.Method,
			ContributionType: mapping.contributionType,
			Normalizer: normalization.NormalizerFunc(func(ctx context.Context, input normalization.Input) (normalization.Output, error) {
				return normalizePythonContribution(ctx, input, mapping)
			}),
		})
	}
	return registrations, nil
}

// PythonNormalizerRegistrations is a descriptive alias for callers that name
// the frontend explicitly.
func PythonNormalizerRegistrations(manifest fact.FrontendManifest) ([]normalization.Registration, error) {
	return NormalizerRegistrations(manifest)
}

type pythonMapping struct {
	contributionType string
	dimension        contract.Dimension
	normalize        func(normalization.Input, pythonMapping) (pythonNormalizationResult, error)
}

type pythonNormalizationResult struct {
	facts      []fact.CanonicalFact
	incomplete bool
}

func validatePythonManifest(manifest fact.FrontendManifest) error {
	if err := manifest.Validate(); err != nil {
		return fmt.Errorf("python normalizer manifest: %w", err)
	}
	if manifest.ID != AnalyzerID || manifest.Version != AnalyzerVersion || manifest.Method != AnalyzerMethod {
		return errors.New("python normalizer manifest: frontend identity does not match Python analyzer")
	}
	if manifest.Execution != fact.ExecutionProfileSafeStatic {
		return errors.New("python normalizer manifest: safe-static execution profile is required")
	}
	for _, predicate := range pythonRequiredPredicates {
		if !pythonManifestDeclaresPredicate(manifest, predicate) {
			return fmt.Errorf("python normalizer manifest: predicate %q is required", predicate)
		}
	}
	for _, dimension := range pythonRequiredDimensions {
		if !pythonManifestDeclaresDimension(manifest, dimension) {
			return fmt.Errorf("python normalizer manifest: capability %q is required", dimension)
		}
	}
	return nil
}

func normalizePythonContribution(ctx context.Context, input normalization.Input, mapping pythonMapping) (normalization.Output, error) {
	if ctx == nil {
		return normalization.Output{}, errors.New("python normalizer: context is required")
	}
	if err := ctx.Err(); err != nil {
		return normalization.Output{}, err
	}
	if len(input.Evidence) == 0 {
		return normalization.Output{
			Coverage: []contract.Coverage{pythonCoverage(input, mapping.dimension, contract.CoverageIncomplete, MissingEvidenceCoverageMessage)},
		}, nil
	}

	result, err := mapping.normalize(input, mapping)
	if err != nil {
		return normalization.Output{}, err
	}
	if err := ctx.Err(); err != nil {
		return normalization.Output{}, err
	}
	state := contract.CoverageProduced
	message := ProducedCoverageMessage
	if result.incomplete {
		state = contract.CoverageIncomplete
		message = IncompleteCoverageMessage
	}
	return normalization.Output{
		Facts: result.facts,
		Coverage: []contract.Coverage{
			pythonCoverage(input, mapping.dimension, state, message),
		},
	}, nil
}

func pythonCoverage(input normalization.Input, dimension contract.Dimension, state contract.CoverageState, message string) contract.Coverage {
	coverage := contract.Coverage{
		Dimension:  string(dimension),
		Scope:      input.Contribution.ID,
		State:      state,
		AnalyzerID: input.Manifest.ID,
		Message:    message,
	}
	coverage.ID = contract.CoverageID(coverage.Dimension, coverage.Scope, coverage.State, coverage.AnalyzerID)
	return coverage
}

type pythonArtifactPayload struct {
	Path string `json:"path"`
	Type string `json:"type"`
}

type pythonSymbolPayload struct {
	Kind          string `json:"kind"`
	Name          string `json:"name"`
	QualifiedName string `json:"qualified_name"`
	Signature     string `json:"signature"`
}

type pythonImportPayload struct {
	Module string `json:"module"`
	Name   string `json:"name"`
	Alias  string `json:"alias"`
}

type pythonRelationPayload struct {
	Kind         string `json:"kind"`
	Callee       string `json:"callee"`
	Target       string `json:"target"`
	SourceSymbol string `json:"source_symbol"`
}

type pythonConfigurationPayload struct {
	Key  string `json:"key"`
	Kind string `json:"kind"`
	Path string `json:"path"`
}

func normalizePythonArtifact(input normalization.Input, _ pythonMapping) (pythonNormalizationResult, error) {
	var payload pythonArtifactPayload
	if err := decodePythonPayload(input.Contribution.Value, &payload); err != nil {
		return pythonNormalizationResult{}, fmt.Errorf("python artifact contribution: %w", err)
	}
	if err := validPythonText(payload.Path, "artifact path"); err != nil {
		return pythonNormalizationResult{}, err
	}
	if err := validOptionalPythonText(payload.Type, "artifact type"); err != nil {
		return pythonNormalizationResult{}, err
	}
	if payload.Type != "" && payload.Type != "python" {
		return pythonNormalizationResult{}, errors.New("python artifact type is unsupported")
	}
	qualifiers := make([]fact.Qualifier, 0, 1)
	if payload.Type != "" {
		qualifiers = append(qualifiers, pythonStringQualifier("type", payload.Type))
	}
	candidate, err := pythonFact(
		input,
		fact.PredicateArtifact,
		fact.Participant{Kind: fact.ParticipantArtifact, ID: input.Contribution.ArtifactID},
		nil,
		&fact.TypedValue{Kind: fact.ValueString, String: payload.Path},
		qualifiers,
	)
	if err != nil {
		return pythonNormalizationResult{}, err
	}
	return pythonNormalizationResult{facts: []fact.CanonicalFact{candidate}}, nil
}

func normalizePythonSymbol(input normalization.Input, _ pythonMapping) (pythonNormalizationResult, error) {
	var payload pythonSymbolPayload
	if err := decodePythonPayload(input.Contribution.Value, &payload); err != nil {
		return pythonNormalizationResult{}, fmt.Errorf("python symbol contribution: %w", err)
	}
	for field, value := range map[string]string{
		"symbol kind":           payload.Kind,
		"symbol name":           payload.Name,
		"qualified symbol name": payload.QualifiedName,
	} {
		if err := validPythonText(value, field); err != nil {
			return pythonNormalizationResult{}, err
		}
	}
	if payload.Kind != "class" && payload.Kind != "function" && payload.Kind != "async_function" {
		return pythonNormalizationResult{}, errors.New("python symbol kind is unsupported")
	}
	if !identifierRE.MatchString(payload.Name) {
		return pythonNormalizationResult{}, errors.New("python symbol name is not a lexical identifier")
	}
	if !pythonQualifiedNameRE.MatchString(payload.QualifiedName) {
		return pythonNormalizationResult{}, errors.New("python qualified symbol name is not lexical")
	}
	if err := validOptionalPythonText(payload.Signature, "symbol signature"); err != nil {
		return pythonNormalizationResult{}, err
	}

	symbol := fact.Participant{
		Kind: fact.ParticipantSymbol,
		ID:   pythonLexicalID("symbol", input.Contribution.ArtifactID, payload.QualifiedName, ""),
	}
	artifact := fact.Participant{Kind: fact.ParticipantArtifact, ID: input.Contribution.ArtifactID}
	qualifiers := []fact.Qualifier{
		pythonStringQualifier("kind", payload.Kind),
		pythonStringQualifier("name", payload.Name),
		pythonStringQualifier("qualified_name", payload.QualifiedName),
	}
	if payload.Signature != "" {
		qualifiers = append(qualifiers, pythonStringQualifier("signature", payload.Signature))
	}

	symbolFact, err := pythonFact(
		input,
		fact.PredicateSymbol,
		symbol,
		nil,
		&fact.TypedValue{Kind: fact.ValueString, String: payload.Name},
		qualifiers,
	)
	if err != nil {
		return pythonNormalizationResult{}, err
	}
	definitionFact, err := pythonFact(input, fact.PredicateDefinition, symbol, &artifact, nil, qualifiers)
	if err != nil {
		return pythonNormalizationResult{}, err
	}
	return pythonNormalizationResult{facts: []fact.CanonicalFact{symbolFact, definitionFact}}, nil
}

func normalizePythonImport(input normalization.Input, _ pythonMapping) (pythonNormalizationResult, error) {
	var payload pythonImportPayload
	if err := decodePythonPayload(input.Contribution.Value, &payload); err != nil {
		return pythonNormalizationResult{}, fmt.Errorf("python import contribution: %w", err)
	}
	if err := validPythonText(payload.Module, "import module"); err != nil {
		return pythonNormalizationResult{}, err
	}
	if !dottedNameRE.MatchString(payload.Module) {
		return pythonNormalizationResult{}, errors.New("python import module is not lexical")
	}
	for field, value := range map[string]string{
		"import name":  payload.Name,
		"import alias": payload.Alias,
	} {
		if err := validOptionalPythonText(value, field); err != nil {
			return pythonNormalizationResult{}, err
		}
	}
	if payload.Name != "" && !identifierRE.MatchString(payload.Name) {
		return pythonNormalizationResult{}, errors.New("python import name is not a lexical identifier")
	}
	if payload.Alias != "" && !identifierRE.MatchString(payload.Alias) {
		return pythonNormalizationResult{}, errors.New("python import alias is not a lexical identifier")
	}
	target := fact.Participant{
		Kind: fact.ParticipantSymbol,
		ID:   pythonLexicalID("import", input.Contribution.ArtifactID, payload.Module, payload.Name+"\x00"+payload.Alias),
	}
	subject := fact.Participant{Kind: fact.ParticipantArtifact, ID: input.Contribution.ArtifactID}
	qualifiers := []fact.Qualifier{pythonStringQualifier("module", payload.Module)}
	if payload.Name != "" {
		qualifiers = append(qualifiers, pythonStringQualifier("name", payload.Name))
	}
	if payload.Alias != "" {
		qualifiers = append(qualifiers, pythonStringQualifier("alias", payload.Alias))
	}
	reference, err := pythonFact(input, fact.PredicateReference, subject, &target, nil, qualifiers)
	if err != nil {
		return pythonNormalizationResult{}, err
	}
	dependency, err := pythonFact(input, fact.PredicateDependency, subject, &target, nil, qualifiers)
	if err != nil {
		return pythonNormalizationResult{}, err
	}
	return pythonNormalizationResult{facts: []fact.CanonicalFact{reference, dependency}}, nil
}

func normalizePythonRelation(input normalization.Input, _ pythonMapping) (pythonNormalizationResult, error) {
	var payload pythonRelationPayload
	if err := decodePythonPayload(input.Contribution.Value, &payload); err != nil {
		return pythonNormalizationResult{}, fmt.Errorf("python relation contribution: %w", err)
	}
	for field, value := range map[string]string{
		"relation kind":   payload.Kind,
		"relation callee": payload.Callee,
		"relation target": payload.Target,
	} {
		if err := validPythonText(value, field); err != nil {
			return pythonNormalizationResult{}, err
		}
	}
	if payload.Kind != "frappe_call" {
		return pythonNormalizationResult{}, errors.New("python relation kind is unsupported")
	}
	if payload.Callee != "frappe.get_doc" && payload.Callee != "frappe.get_all" && payload.Callee != "frappe.db.get_value" {
		return pythonNormalizationResult{}, errors.New("python relation callee is unsupported")
	}
	if err := validOptionalPythonText(payload.SourceSymbol, "relation source symbol"); err != nil {
		return pythonNormalizationResult{}, err
	}
	if payload.SourceSymbol != "" && !pythonQualifiedNameRE.MatchString(payload.SourceSymbol) {
		return pythonNormalizationResult{}, errors.New("python relation source symbol is not lexical")
	}

	subject := fact.Participant{Kind: fact.ParticipantArtifact, ID: input.Contribution.ArtifactID}
	if payload.SourceSymbol != "" {
		subject = fact.Participant{
			Kind: fact.ParticipantSymbol,
			ID:   pythonLexicalID("symbol", input.Contribution.ArtifactID, payload.SourceSymbol, ""),
		}
	}
	target := fact.Participant{
		Kind: fact.ParticipantNamedElement,
		ID:   pythonLexicalID("frappe-target", input.Contribution.ArtifactID, payload.Callee, payload.Target),
	}
	qualifiers := []fact.Qualifier{
		pythonStringQualifier("kind", payload.Kind),
		pythonStringQualifier("callee", payload.Callee),
		pythonStringQualifier("target", payload.Target),
	}
	if payload.SourceSymbol != "" {
		qualifiers = append(qualifiers, pythonStringQualifier("source_symbol", payload.SourceSymbol))
	}
	// A literal Frappe call proves a lexical reference to a named DocType. It
	// does not prove runtime behavior, call resolution, or a dependency edge.
	reference, err := pythonFact(input, fact.PredicateReference, subject, &target, nil, qualifiers)
	if err != nil {
		return pythonNormalizationResult{}, err
	}
	return pythonNormalizationResult{facts: []fact.CanonicalFact{reference}}, nil
}

func normalizePythonConfiguration(input normalization.Input, _ pythonMapping) (pythonNormalizationResult, error) {
	var payload pythonConfigurationPayload
	if err := decodePythonPayload(input.Contribution.Value, &payload); err != nil {
		return pythonNormalizationResult{}, fmt.Errorf("python configuration contribution: %w", err)
	}
	for field, value := range map[string]string{
		"configuration key":  payload.Key,
		"configuration kind": payload.Kind,
		"configuration path": payload.Path,
	} {
		if err := validPythonText(value, field); err != nil {
			return pythonNormalizationResult{}, err
		}
	}
	if payload.Kind != "frappe.conf.get" && payload.Kind != "frappe.get_conf().get" && payload.Kind != "hooks_assignment" && payload.Kind != "hooks_dict_key" {
		return pythonNormalizationResult{}, errors.New("python configuration kind is unsupported")
	}
	qualifiers := []fact.Qualifier{
		pythonStringQualifier("kind", payload.Kind),
		pythonStringQualifier("path", payload.Path),
	}
	candidate, err := pythonFact(
		input,
		fact.PredicateConfiguration,
		fact.Participant{Kind: fact.ParticipantArtifact, ID: input.Contribution.ArtifactID},
		nil,
		&fact.TypedValue{Kind: fact.ValueString, String: payload.Key},
		qualifiers,
	)
	if err != nil {
		return pythonNormalizationResult{}, err
	}
	return pythonNormalizationResult{facts: []fact.CanonicalFact{candidate}}, nil
}

func pythonFact(
	input normalization.Input,
	predicate fact.Predicate,
	subject fact.Participant,
	object *fact.Participant,
	value *fact.TypedValue,
	qualifiers []fact.Qualifier,
) (fact.CanonicalFact, error) {
	candidate := fact.CanonicalFact{
		Version:    fact.Version,
		Scope:      input.Scope,
		Predicate:  predicate,
		Subject:    subject,
		Object:     clonePythonParticipant(object),
		Value:      clonePythonValue(value),
		Qualifiers: append([]fact.Qualifier(nil), qualifiers...),
		Producer:   fact.Producer{ID: input.Manifest.ID, Version: input.Manifest.Version, Method: input.Manifest.Method},
		Evidence:   append([]fact.EvidenceRef(nil), input.Evidence...),
	}
	identifier, err := fact.FactID(candidate)
	if err != nil {
		return fact.CanonicalFact{}, fmt.Errorf("python normalizer fact identity: %w", err)
	}
	candidate.ID = identifier
	if err := candidate.Validate(); err != nil {
		return fact.CanonicalFact{}, fmt.Errorf("python normalizer fact validation: %w", err)
	}
	return candidate, nil
}

func clonePythonParticipant(participant *fact.Participant) *fact.Participant {
	if participant == nil {
		return nil
	}
	clone := *participant
	return &clone
}

func clonePythonValue(value *fact.TypedValue) *fact.TypedValue {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

func decodePythonPayload(value json.RawMessage, destination any) error {
	trimmed := bytes.TrimSpace(value)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return errors.New("payload object is required")
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &object); err != nil {
		return fmt.Errorf("payload is not a JSON object: %w", err)
	}
	if object == nil {
		return errors.New("payload object is required")
	}
	if err := json.Unmarshal(trimmed, destination); err != nil {
		return fmt.Errorf("payload fields are malformed: %w", err)
	}
	return nil
}

func validPythonText(value, field string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("python %s is required", field)
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("python %s is not valid utf-8", field)
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return fmt.Errorf("python %s contains a control character", field)
		}
	}
	return nil
}

func validOptionalPythonText(value, field string) error {
	if value == "" {
		return nil
	}
	return validPythonText(value, field)
}

func pythonStringQualifier(name, value string) fact.Qualifier {
	return fact.Qualifier{Name: name, Value: fact.TypedValue{Kind: fact.ValueString, String: value}}
}

func pythonLexicalID(kind, artifactID, primary, secondary string) string {
	digest := sha256.Sum256([]byte("manu:python:lexical:v1\x00" + kind + "\x00" + artifactID + "\x00" + primary + "\x00" + secondary))
	return "python-" + kind + "-" + hex.EncodeToString(digest[:])
}

func pythonManifestDeclaresPredicate(manifest fact.FrontendManifest, predicate fact.Predicate) bool {
	for _, declared := range manifest.Predicates {
		if declared == predicate {
			return true
		}
	}
	return false
}

func pythonManifestDeclaresDimension(manifest fact.FrontendManifest, dimension contract.Dimension) bool {
	for _, declared := range manifest.Capabilities {
		if declared == dimension {
			return true
		}
	}
	return false
}
