package java

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/fact"
	"github.com/pedrogpaulino/manu/internal/normalization"
)

const (
	javaArtifactContribution      = "java.artifact"
	javaPackageContribution       = "java.package"
	javaTypeContribution          = "java.type"
	javaMethodContribution        = "java.method"
	javaImportContribution        = "java.import"
	javaRelationContribution      = "java.relation"
	javaConfigurationContribution = "java.configuration"
	javaEndpointContribution      = "java.endpoint"

	// MissingEvidenceCoverageMessage is intentionally fixed. It is a factual
	// coverage explanation, not a copy of the contribution payload.
	MissingEvidenceCoverageMessage = "Java normalization requires source evidence"
)

var javaRequiredPredicates = [...]fact.Predicate{
	fact.PredicateArtifact,
	fact.PredicateSymbol,
	fact.PredicateNamedElement,
	fact.PredicateDefinition,
	fact.PredicateReference,
	fact.PredicateCall,
	fact.PredicateDependency,
	fact.PredicateConfiguration,
	fact.PredicateEndpoint,
	fact.PredicateMembership,
}

var javaRequiredDimensions = [...]contract.Dimension{
	contract.DimensionLandscapeInventoryStructure,
	contract.DimensionEntitiesAndRelationships,
	contract.DimensionFlowsAndDependencies,
	contract.DimensionConfigurationVariations,
}

// NormalizerRegistrations returns the bounded Java normalizers used by the
// shared normalization registry. A registration is only produced for a
// manifest that advertises the vocabulary and dimensions needed by all
// mappings.
func NormalizerRegistrations(manifest fact.FrontendManifest) ([]normalization.Registration, error) {
	if err := validateJavaManifest(manifest); err != nil {
		return nil, err
	}

	mappings := []javaMapping{
		{contributionType: javaArtifactContribution, dimension: contract.DimensionLandscapeInventoryStructure, normalize: normalizeArtifact},
		{contributionType: javaPackageContribution, dimension: contract.DimensionLandscapeInventoryStructure, normalize: normalizePackage},
		{contributionType: javaTypeContribution, dimension: contract.DimensionEntitiesAndRelationships, normalize: normalizeType},
		{contributionType: javaMethodContribution, dimension: contract.DimensionEntitiesAndRelationships, normalize: normalizeMethod},
		{contributionType: javaImportContribution, dimension: contract.DimensionFlowsAndDependencies, normalize: normalizeImport},
		{contributionType: javaRelationContribution, dimension: contract.DimensionFlowsAndDependencies, normalize: normalizeRelation},
		{contributionType: javaConfigurationContribution, dimension: contract.DimensionConfigurationVariations, normalize: normalizeConfiguration},
		{contributionType: javaEndpointContribution, dimension: contract.DimensionEntitiesAndRelationships, normalize: normalizeEndpoint},
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
				return normalizeJavaContribution(ctx, input, mapping)
			}),
		})
	}
	return registrations, nil
}

// JavaNormalizerRegistrations is a descriptive alias for callers that name
// the frontend explicitly.
func JavaNormalizerRegistrations(manifest fact.FrontendManifest) ([]normalization.Registration, error) {
	return NormalizerRegistrations(manifest)
}

type javaMapping struct {
	contributionType string
	dimension        contract.Dimension
	normalize        func(normalization.Input, javaMapping) ([]fact.CanonicalFact, error)
}

func validateJavaManifest(manifest fact.FrontendManifest) error {
	if err := manifest.Validate(); err != nil {
		return fmt.Errorf("java normalizer manifest: %w", err)
	}
	if manifest.ID != AnalyzerID || manifest.Version != AnalyzerVersion || manifest.Method != AnalyzerMethod {
		return fmt.Errorf("java normalizer manifest: frontend identity does not match Java analyzer")
	}
	for _, predicate := range javaRequiredPredicates {
		if !declaresPredicate(manifest, predicate) {
			return fmt.Errorf("java normalizer manifest: predicate %q is required", predicate)
		}
	}
	for _, dimension := range javaRequiredDimensions {
		if !declaresDimension(manifest, dimension) {
			return fmt.Errorf("java normalizer manifest: capability %q is required", dimension)
		}
	}
	return nil
}

func normalizeJavaContribution(ctx context.Context, input normalization.Input, mapping javaMapping) (normalization.Output, error) {
	if ctx == nil {
		return normalization.Output{}, errors.New("java normalizer: context is required")
	}
	if err := ctx.Err(); err != nil {
		return normalization.Output{}, err
	}
	if len(input.Evidence) == 0 {
		return normalization.Output{Coverage: []contract.Coverage{incompleteJavaCoverage(input, mapping.dimension)}}, nil
	}
	facts, err := mapping.normalize(input, mapping)
	if err != nil {
		return normalization.Output{}, err
	}
	if err := ctx.Err(); err != nil {
		return normalization.Output{}, err
	}
	return normalization.Output{
		Facts: facts,
		Coverage: []contract.Coverage{javaCoverage(
			input,
			mapping.dimension,
			contract.CoverageProduced,
			"Java normalization produced canonical facts",
		)},
	}, nil
}

func incompleteJavaCoverage(input normalization.Input, dimension contract.Dimension) contract.Coverage {
	return javaCoverage(input, dimension, contract.CoverageIncomplete, MissingEvidenceCoverageMessage)
}

func javaCoverage(input normalization.Input, dimension contract.Dimension, state contract.CoverageState, message string) contract.Coverage {
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

type javaArtifactPayload struct {
	Path           string `json:"path"`
	Type           string `json:"type"`
	Hash           string `json:"hash"`
	Size           *int64 `json:"size"`
	Classification string `json:"classification"`
}

type javaTypePayload struct {
	Kind          string `json:"kind"`
	Name          string `json:"name"`
	QualifiedName string `json:"qualified_name"`
}

type javaMethodPayload struct {
	Kind       string          `json:"kind"`
	Name       string          `json:"name"`
	Signature  string          `json:"signature"`
	Parameters json.RawMessage `json:"parameters"`
	ReturnType string          `json:"return_type"`
}

type javaImportPayload struct {
	Name   string `json:"name"`
	Static *bool  `json:"static"`
}

type javaPackagePayload struct {
	Name string `json:"name"`
}

type javaRelationPayload struct {
	Kind string `json:"kind"`
	From string `json:"from"`
	To   string `json:"to"`
}

type javaConfigurationPayload struct {
	Key  string `json:"key"`
	Kind string `json:"kind"`
}

type javaEndpointPayload struct {
	Path       string `json:"path"`
	HTTPMethod string `json:"http_method"`
}

func normalizeArtifact(input normalization.Input, _ javaMapping) ([]fact.CanonicalFact, error) {
	var payload javaArtifactPayload
	if err := decodeJavaPayload(input.Contribution.Value, &payload); err != nil {
		return nil, fmt.Errorf("java artifact contribution: %w", err)
	}
	if err := validLexicalText(payload.Path, "artifact path"); err != nil {
		return nil, err
	}
	for field, value := range map[string]string{
		"artifact type":           payload.Type,
		"artifact hash":           payload.Hash,
		"artifact classification": payload.Classification,
	} {
		if value != "" {
			if err := validLexicalText(value, field); err != nil {
				return nil, err
			}
		}
	}
	qualifiers := make([]fact.Qualifier, 0, 4)
	if payload.Type != "" {
		qualifiers = append(qualifiers, javaStringQualifier("type", payload.Type))
	}
	if payload.Hash != "" {
		qualifiers = append(qualifiers, javaStringQualifier("hash", payload.Hash))
	}
	if payload.Classification != "" {
		qualifiers = append(qualifiers, javaStringQualifier("classification", payload.Classification))
	}
	if payload.Size != nil {
		if *payload.Size < 0 {
			return nil, errors.New("java artifact contribution: size must not be negative")
		}
		qualifiers = append(qualifiers, fact.Qualifier{Name: "size", Value: fact.TypedValue{Kind: fact.ValueInteger, Integer: *payload.Size}})
	}
	candidate, err := javaFact(input, fact.PredicateArtifact, fact.Participant{Kind: fact.ParticipantArtifact, ID: input.Contribution.ArtifactID}, nil, &fact.TypedValue{Kind: fact.ValueString, String: payload.Path}, qualifiers)
	return oneJavaFact(candidate, err)
}

func normalizePackage(input normalization.Input, _ javaMapping) ([]fact.CanonicalFact, error) {
	var payload javaPackagePayload
	if err := decodeJavaPayload(input.Contribution.Value, &payload); err != nil {
		return nil, fmt.Errorf("java package contribution: %w", err)
	}
	if err := validLexicalText(payload.Name, "package name"); err != nil {
		return nil, err
	}
	packageID := javaLexicalID("package", input.Contribution.ArtifactID, payload.Name, "")
	packageParticipant := fact.Participant{Kind: fact.ParticipantNamedElement, ID: packageID}
	artifactParticipant := fact.Participant{Kind: fact.ParticipantArtifact, ID: input.Contribution.ArtifactID}
	nameQualifier := []fact.Qualifier{javaStringQualifier("kind", "package")}
	namedFact, err := javaFact(input, fact.PredicateNamedElement, packageParticipant, nil, &fact.TypedValue{Kind: fact.ValueString, String: payload.Name}, nameQualifier)
	if err != nil {
		return nil, err
	}
	// Membership is directed from the artifact to its declared package: the
	// artifact is the lexical member associated with the package container.
	membershipFact, err := javaFact(input, fact.PredicateMembership, artifactParticipant, &packageParticipant, nil, nameQualifier)
	if err != nil {
		return nil, err
	}
	return []fact.CanonicalFact{namedFact, membershipFact}, nil
}

func normalizeType(input normalization.Input, _ javaMapping) ([]fact.CanonicalFact, error) {
	var payload javaTypePayload
	if err := decodeJavaPayload(input.Contribution.Value, &payload); err != nil {
		return nil, fmt.Errorf("java type contribution: %w", err)
	}
	if err := validLexicalText(payload.Kind, "type kind"); err != nil {
		return nil, err
	}
	if err := validLexicalText(payload.Name, "type name"); err != nil {
		return nil, err
	}
	qualifiedName := payload.QualifiedName
	if qualifiedName == "" {
		qualifiedName = payload.Name
	}
	if err := validLexicalText(qualifiedName, "qualified type name"); err != nil {
		return nil, err
	}
	symbolID := javaLexicalID("type", input.Contribution.ArtifactID, qualifiedName, payload.Kind)
	symbol := fact.Participant{Kind: fact.ParticipantSymbol, ID: symbolID}
	artifact := fact.Participant{Kind: fact.ParticipantArtifact, ID: input.Contribution.ArtifactID}
	nameQualifiers := []fact.Qualifier{
		javaStringQualifier("name", payload.Name),
		javaStringQualifier("kind", payload.Kind),
	}
	if payload.QualifiedName != "" && payload.QualifiedName != payload.Name {
		nameQualifiers = append(nameQualifiers, javaStringQualifier("qualified_name", payload.QualifiedName))
	}
	symbolFact, err := javaFact(input, fact.PredicateSymbol, symbol, nil, &fact.TypedValue{Kind: fact.ValueString, String: payload.Name}, nameQualifiers)
	if err != nil {
		return nil, err
	}
	definitionFact, err := javaFact(input, fact.PredicateDefinition, symbol, &artifact, nil, nameQualifiers)
	if err != nil {
		return nil, err
	}
	return []fact.CanonicalFact{symbolFact, definitionFact}, nil
}

func normalizeMethod(input normalization.Input, _ javaMapping) ([]fact.CanonicalFact, error) {
	var payload javaMethodPayload
	if err := decodeJavaPayload(input.Contribution.Value, &payload); err != nil {
		return nil, fmt.Errorf("java method contribution: %w", err)
	}
	if err := validLexicalText(payload.Name, "method name"); err != nil {
		return nil, err
	}
	parameters, err := decodeJavaParameters(payload.Parameters)
	if err != nil {
		return nil, err
	}
	if parameters != "" {
		if err := validLexicalText(parameters, "method parameters"); err != nil {
			return nil, err
		}
	}
	if payload.Signature != "" {
		if err := validLexicalText(payload.Signature, "method signature"); err != nil {
			return nil, err
		}
	}
	if payload.ReturnType != "" {
		if err := validLexicalText(payload.ReturnType, "method return type"); err != nil {
			return nil, err
		}
	}
	signature := payload.Signature
	if signature == "" {
		signature = payload.Name + "(" + parameters + ")"
	}
	symbolID := javaLexicalID("method", input.Contribution.ArtifactID, payload.Name, signature)
	symbol := fact.Participant{Kind: fact.ParticipantSymbol, ID: symbolID}
	artifact := fact.Participant{Kind: fact.ParticipantArtifact, ID: input.Contribution.ArtifactID}
	qualifiers := []fact.Qualifier{
		javaStringQualifier("name", payload.Name),
		javaStringQualifier("parameters", parameters),
	}
	if payload.ReturnType != "" {
		qualifiers = append(qualifiers, javaStringQualifier("return_type", payload.ReturnType))
	}
	symbolFact, err := javaFact(input, fact.PredicateSymbol, symbol, nil, &fact.TypedValue{Kind: fact.ValueString, String: signature}, qualifiers)
	if err != nil {
		return nil, err
	}
	definitionFact, err := javaFact(input, fact.PredicateDefinition, symbol, &artifact, nil, qualifiers)
	if err != nil {
		return nil, err
	}
	return []fact.CanonicalFact{symbolFact, definitionFact}, nil
}

func normalizeImport(input normalization.Input, _ javaMapping) ([]fact.CanonicalFact, error) {
	var payload javaImportPayload
	if err := decodeJavaPayload(input.Contribution.Value, &payload); err != nil {
		return nil, fmt.Errorf("java import contribution: %w", err)
	}
	target, staticImport, err := normalizeImportTarget(payload.Name, payload.Static)
	if err != nil {
		return nil, err
	}
	targetID := javaLexicalID("import", input.Contribution.ArtifactID, target, fmt.Sprint(staticImport))
	from := fact.Participant{Kind: fact.ParticipantArtifact, ID: input.Contribution.ArtifactID}
	to := fact.Participant{Kind: fact.ParticipantSymbol, ID: targetID}
	qualifiers := []fact.Qualifier{javaStringQualifier("target", target)}
	if staticImport {
		qualifiers = append(qualifiers, fact.Qualifier{Name: "static", Value: fact.TypedValue{Kind: fact.ValueBoolean, Boolean: true}})
	}
	referenceFact, err := javaFact(input, fact.PredicateReference, from, &to, nil, qualifiers)
	if err != nil {
		return nil, err
	}
	dependencyFact, err := javaFact(input, fact.PredicateDependency, from, &to, nil, qualifiers)
	if err != nil {
		return nil, err
	}
	return []fact.CanonicalFact{referenceFact, dependencyFact}, nil
}

func normalizeRelation(input normalization.Input, _ javaMapping) ([]fact.CanonicalFact, error) {
	var payload javaRelationPayload
	if err := decodeJavaPayload(input.Contribution.Value, &payload); err != nil {
		return nil, fmt.Errorf("java relation contribution: %w", err)
	}
	if err := validLexicalText(payload.Kind, "relation kind"); err != nil {
		return nil, err
	}
	if err := validLexicalText(payload.To, "relation target"); err != nil {
		return nil, err
	}
	kind := strings.ToLower(strings.TrimSpace(payload.Kind))
	var predicate fact.Predicate
	switch kind {
	case "call":
		predicate = fact.PredicateCall
		if err := validLexicalText(payload.From, "call source"); err != nil {
			return nil, err
		}
	case "extends", "implements", "constructs":
		predicate = fact.PredicateDependency
	case "":
		return nil, errors.New("java relation kind is required")
	default:
		return nil, errors.New("java relation kind is unsupported")
	}

	var subject fact.Participant
	if payload.From == "" {
		subject = fact.Participant{Kind: fact.ParticipantArtifact, ID: input.Contribution.ArtifactID}
	} else {
		if err := validLexicalText(payload.From, "relation source"); err != nil {
			return nil, err
		}
		subject = fact.Participant{Kind: fact.ParticipantSymbol, ID: javaLexicalID("relation-from", input.Contribution.ArtifactID, payload.From, kind)}
	}
	target := fact.Participant{Kind: fact.ParticipantSymbol, ID: javaLexicalID("relation-to", input.Contribution.ArtifactID, payload.To, kind)}
	qualifiers := []fact.Qualifier{
		javaStringQualifier("kind", kind),
		javaStringQualifier("target", payload.To),
	}
	if payload.From != "" {
		qualifiers = append(qualifiers, javaStringQualifier("source", payload.From))
	}
	candidate, err := javaFact(input, predicate, subject, &target, nil, qualifiers)
	if err != nil {
		return nil, err
	}
	return oneJavaFact(candidate, nil)
}

func normalizeConfiguration(input normalization.Input, _ javaMapping) ([]fact.CanonicalFact, error) {
	var payload javaConfigurationPayload
	if err := decodeJavaPayload(input.Contribution.Value, &payload); err != nil {
		return nil, fmt.Errorf("java configuration contribution: %w", err)
	}
	if err := validLexicalText(payload.Key, "configuration key"); err != nil {
		return nil, err
	}
	qualifiers := make([]fact.Qualifier, 0, 1)
	if payload.Kind != "" {
		if err := validLexicalText(payload.Kind, "configuration kind"); err != nil {
			return nil, err
		}
		qualifiers = append(qualifiers, javaStringQualifier("kind", payload.Kind))
	}
	candidate, err := javaFact(
		input,
		fact.PredicateConfiguration,
		fact.Participant{Kind: fact.ParticipantArtifact, ID: input.Contribution.ArtifactID},
		nil,
		&fact.TypedValue{Kind: fact.ValueString, String: payload.Key},
		qualifiers,
	)
	return oneJavaFact(candidate, err)
}

func normalizeEndpoint(input normalization.Input, _ javaMapping) ([]fact.CanonicalFact, error) {
	var payload javaEndpointPayload
	if err := decodeJavaPayload(input.Contribution.Value, &payload); err != nil {
		return nil, fmt.Errorf("java endpoint contribution: %w", err)
	}
	if err := validLexicalText(payload.Path, "endpoint path"); err != nil {
		return nil, err
	}
	httpMethod, err := normalizeHTTPMethod(payload.HTTPMethod)
	if err != nil {
		return nil, err
	}
	endpointID := javaLexicalID("endpoint", input.Contribution.ArtifactID, payload.Path, httpMethod)
	qualifiers := make([]fact.Qualifier, 0, 1)
	if httpMethod != "" {
		qualifiers = append(qualifiers, javaStringQualifier("http_method", httpMethod))
	}
	candidate, err := javaFact(
		input,
		fact.PredicateEndpoint,
		fact.Participant{Kind: fact.ParticipantNamedElement, ID: endpointID},
		nil,
		&fact.TypedValue{Kind: fact.ValueString, String: payload.Path},
		qualifiers,
	)
	return oneJavaFact(candidate, err)
}

func normalizeHTTPMethod(value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if value != "" && trimmed == "" {
		return "", errors.New("java endpoint HTTP method is unsupported")
	}
	if trimmed == "" {
		return "", nil
	}
	method := strings.ToUpper(trimmed)
	switch method {
	case "GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS", "HEAD":
		return method, nil
	default:
		return "", errors.New("java endpoint HTTP method is unsupported")
	}
}

func oneJavaFact(candidate fact.CanonicalFact, err error) ([]fact.CanonicalFact, error) {
	if err != nil {
		return nil, err
	}
	return []fact.CanonicalFact{candidate}, nil
}

func javaFact(input normalization.Input, predicate fact.Predicate, subject fact.Participant, object *fact.Participant, value *fact.TypedValue, qualifiers []fact.Qualifier) (fact.CanonicalFact, error) {
	candidate := fact.CanonicalFact{
		Version:    fact.Version,
		Scope:      input.Scope,
		Predicate:  predicate,
		Subject:    subject,
		Object:     cloneJavaParticipant(object),
		Value:      cloneJavaValue(value),
		Qualifiers: append([]fact.Qualifier(nil), qualifiers...),
		Producer:   fact.Producer{ID: input.Manifest.ID, Version: input.Manifest.Version, Method: input.Manifest.Method},
		Evidence:   append([]fact.EvidenceRef(nil), input.Evidence...),
	}
	identifier, err := fact.FactID(candidate)
	if err != nil {
		return fact.CanonicalFact{}, fmt.Errorf("java normalizer fact identity: %w", err)
	}
	candidate.ID = identifier
	if err := candidate.Validate(); err != nil {
		return fact.CanonicalFact{}, fmt.Errorf("java normalizer fact validation: %w", err)
	}
	return candidate, nil
}

func cloneJavaParticipant(participant *fact.Participant) *fact.Participant {
	if participant == nil {
		return nil
	}
	clone := *participant
	return &clone
}

func cloneJavaValue(value *fact.TypedValue) *fact.TypedValue {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

func decodeJavaPayload(value json.RawMessage, destination any) error {
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

func decodeJavaParameters(raw json.RawMessage) (string, error) {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return canonicalJavaParameters(text), nil
	}
	var values []string
	if err := json.Unmarshal(raw, &values); err != nil {
		return "", fmt.Errorf("java method parameters are malformed")
	}
	for _, value := range values {
		if err := validLexicalText(value, "method parameter"); err != nil {
			return "", err
		}
	}
	return canonicalJavaParameters(strings.Join(values, ",")), nil
}

func canonicalJavaParameters(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func normalizeImportTarget(name string, static *bool) (string, bool, error) {
	target := strings.TrimSpace(name)
	staticImport := static != nil && *static
	if strings.HasPrefix(target, "static ") {
		staticImport = true
		target = strings.TrimSpace(strings.TrimPrefix(target, "static "))
	}
	if err := validLexicalText(target, "import target"); err != nil {
		return "", false, err
	}
	for _, character := range target {
		if unicode.IsSpace(character) {
			return "", false, errors.New("import target must not contain whitespace")
		}
	}
	return target, staticImport, nil
}

func validLexicalText(value, field string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("java %s is required", field)
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return fmt.Errorf("java %s contains a control character", field)
		}
	}
	return nil
}

func javaStringQualifier(name, value string) fact.Qualifier {
	return fact.Qualifier{Name: name, Value: fact.TypedValue{Kind: fact.ValueString, String: value}}
}

func javaLexicalID(kind, artifactID, primary, secondary string) string {
	digest := sha256.Sum256([]byte("manu:java:lexical:v1\x00" + kind + "\x00" + artifactID + "\x00" + primary + "\x00" + secondary))
	return "java-" + kind + "-" + hex.EncodeToString(digest[:])
}

func declaresPredicate(manifest fact.FrontendManifest, predicate fact.Predicate) bool {
	for _, declared := range manifest.Predicates {
		if declared == predicate {
			return true
		}
	}
	return false
}

func declaresDimension(manifest fact.FrontendManifest, dimension contract.Dimension) bool {
	for _, declared := range manifest.Capabilities {
		if declared == dimension {
			return true
		}
	}
	return false
}
