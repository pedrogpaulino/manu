package wso2

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
	"unicode"

	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/fact"
	"github.com/pedrogpaulino/manu/internal/normalization"
)

const (
	wso2TypeContribution          = "wso2.type"
	wso2IncludeContribution       = "wso2.include"
	wso2ReferenceContribution     = "wso2.reference"
	wso2EndpointContribution      = "wso2.endpoint"
	wso2MessageContribution       = "wso2.message"
	wso2ConfigurationContribution = "wso2.configuration"

	// MissingEvidenceCoverageMessage is intentionally fixed. It describes a
	// support limitation without copying source payloads into operational data.
	MissingEvidenceCoverageMessage = "WSO2 normalization requires source evidence"
	// IncompleteCoverageMessage is used when a contribution exists but its
	// literal is redacted, dynamic, or otherwise not safe to normalize.
	IncompleteCoverageMessage = "WSO2 normalization did not have a safe literal mapping"
)

var wso2RequiredPredicates = [...]fact.Predicate{
	fact.PredicateNamedElement,
	fact.PredicateMembership,
	fact.PredicateDependency,
	fact.PredicateReference,
	fact.PredicateEndpoint,
	fact.PredicateMessage,
	fact.PredicateConfiguration,
}

var wso2RequiredDimensions = [...]contract.Dimension{
	contract.DimensionLandscapeInventoryStructure,
	contract.DimensionEntitiesAndRelationships,
	contract.DimensionFlowsAndDependencies,
	contract.DimensionConfigurationVariations,
}

// NormalizerRegistrations returns the bounded WSO2 normalizers used by the
// shared registry. A registration is emitted only for a manifest that
// advertises every predicate and dimension used by these mappings.
func NormalizerRegistrations(manifest fact.FrontendManifest) ([]normalization.Registration, error) {
	if err := validateWSO2Manifest(manifest); err != nil {
		return nil, err
	}

	mappings := []wso2Mapping{
		{contributionType: wso2TypeContribution, dimension: contract.DimensionEntitiesAndRelationships, normalize: normalizeType},
		{contributionType: wso2IncludeContribution, dimension: contract.DimensionFlowsAndDependencies, normalize: normalizeInclude},
		{contributionType: wso2ReferenceContribution, dimension: contract.DimensionFlowsAndDependencies, normalize: normalizeReference},
		{contributionType: wso2EndpointContribution, dimension: contract.DimensionEntitiesAndRelationships, normalize: normalizeEndpointContribution},
		{contributionType: wso2MessageContribution, dimension: contract.DimensionFlowsAndDependencies, normalize: normalizeMessage},
		{contributionType: wso2ConfigurationContribution, dimension: contract.DimensionConfigurationVariations, normalize: normalizeConfigurationContribution},
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
				return normalizeWSO2Contribution(ctx, input, mapping)
			}),
		})
	}
	return registrations, nil
}

// WSO2NormalizerRegistrations is a descriptive alias for callers that name
// the frontend explicitly.
func WSO2NormalizerRegistrations(manifest fact.FrontendManifest) ([]normalization.Registration, error) {
	return NormalizerRegistrations(manifest)
}

type wso2Mapping struct {
	contributionType string
	dimension        contract.Dimension
	normalize        func(normalization.Input, wso2Mapping) (wso2NormalizationResult, error)
}

type wso2NormalizationResult struct {
	facts      []fact.CanonicalFact
	incomplete bool
}

func validateWSO2Manifest(manifest fact.FrontendManifest) error {
	if err := manifest.Validate(); err != nil {
		return fmt.Errorf("wso2 normalizer manifest: %w", err)
	}
	if manifest.ID != AnalyzerID || manifest.Version != AnalyzerVersion || manifest.Method != AnalyzerMethod {
		return errors.New("wso2 normalizer manifest: frontend identity does not match WSO2 analyzer")
	}
	for _, predicate := range wso2RequiredPredicates {
		if !declaresPredicate(manifest, predicate) {
			return fmt.Errorf("wso2 normalizer manifest: predicate %q is required", predicate)
		}
	}
	for _, dimension := range wso2RequiredDimensions {
		if !declaresDimension(manifest, dimension) {
			return fmt.Errorf("wso2 normalizer manifest: capability %q is required", dimension)
		}
	}
	return nil
}

func normalizeWSO2Contribution(ctx context.Context, input normalization.Input, mapping wso2Mapping) (normalization.Output, error) {
	if ctx == nil {
		return normalization.Output{}, errors.New("wso2 normalizer: context is required")
	}
	if err := ctx.Err(); err != nil {
		return normalization.Output{}, err
	}
	if len(input.Evidence) == 0 {
		return normalization.Output{Coverage: []contract.Coverage{wso2Coverage(input, mapping.dimension, contract.CoverageIncomplete, MissingEvidenceCoverageMessage)}}, nil
	}
	result, err := mapping.normalize(input, mapping)
	if err != nil {
		return normalization.Output{}, err
	}
	if err := ctx.Err(); err != nil {
		return normalization.Output{}, err
	}
	state := contract.CoverageProduced
	message := "WSO2 normalization produced canonical facts"
	if result.incomplete {
		state = contract.CoverageIncomplete
		message = IncompleteCoverageMessage
	}
	return normalization.Output{
		Facts: result.facts,
		Coverage: []contract.Coverage{
			wso2Coverage(input, mapping.dimension, state, message),
		},
	}, nil
}

type wso2Payload struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Target    string `json:"target"`
	Redacted  bool   `json:"redacted"`
	Path      string `json:"path"`
	Member    string `json:"member"`
	Context   string `json:"context"`
	URI       string `json:"uri"`
	MediaType string `json:"media_type"`
	Key       string `json:"key"`
}

func normalizeType(input normalization.Input, _ wso2Mapping) (wso2NormalizationResult, error) {
	payload, err := decodeWSO2Payload(input.Contribution.Value)
	if err != nil {
		return wso2NormalizationResult{}, fmt.Errorf("wso2 type contribution: %w", err)
	}
	if payload.Redacted || payloadContainsRedacted(payload) || payloadContainsDynamic(payload) {
		return wso2NormalizationResult{incomplete: true}, nil
	}
	if err := validateOptionalText(payload.Kind, "type kind"); err != nil {
		return wso2NormalizationResult{}, err
	}
	if err := validateOptionalText(payload.Name, "type name"); err != nil {
		return wso2NormalizationResult{}, err
	}
	if err := validateRequiredText(payload.Path, "type path"); err != nil {
		return wso2NormalizationResult{}, err
	}
	if strings.TrimSpace(payload.Kind) == "" && strings.TrimSpace(payload.Name) == "" {
		return wso2NormalizationResult{}, errors.New("wso2 type identity requires kind or name")
	}
	if err := validateOptionalText(payload.Member, "type member"); err != nil {
		return wso2NormalizationResult{}, err
	}
	if err := validateOptionalText(payload.Context, "type context"); err != nil {
		return wso2NormalizationResult{}, err
	}

	member := contributionMember(input, payload.Member)
	kind := firstNonEmpty(payload.Kind, payload.Name)
	name := firstNonEmpty(payload.Name, payload.Kind)
	element := fact.Participant{
		Kind: fact.ParticipantNamedElement,
		ID:   wso2Identity("element", input.Contribution.ArtifactID, member, kind, name, payload.Path, payload.Context),
	}
	qualifiers := wso2Qualifiers(
		stringQualifier("kind", payload.Kind),
		stringQualifier("path", payload.Path),
		stringQualifier("member", member),
		stringQualifier("context", payload.Context),
	)
	named, err := wso2Fact(input, fact.PredicateNamedElement, element, nil, &fact.TypedValue{Kind: fact.ValueString, String: name}, qualifiers)
	if err != nil {
		return wso2NormalizationResult{}, err
	}
	container := wso2Container(input, member)
	membership, err := wso2Fact(input, fact.PredicateMembership, container, &element, nil, qualifiers)
	if err != nil {
		return wso2NormalizationResult{}, err
	}
	return wso2NormalizationResult{facts: []fact.CanonicalFact{named, membership}}, nil
}

func normalizeInclude(input normalization.Input, _ wso2Mapping) (wso2NormalizationResult, error) {
	payload, err := decodeWSO2Payload(input.Contribution.Value)
	if err != nil {
		return wso2NormalizationResult{}, fmt.Errorf("wso2 include contribution: %w", err)
	}
	if payload.Redacted || payloadContainsRedacted(payload) || payloadContainsDynamic(payload) || dynamicExpression(payload.Target) {
		return wso2NormalizationResult{incomplete: true}, nil
	}
	target, safe := safeLiteral(payload.Target)
	if !safe {
		return wso2NormalizationResult{incomplete: true}, nil
	}
	if err := validateOptionalText(payload.Kind, "include kind"); err != nil {
		return wso2NormalizationResult{}, err
	}
	if err := validateOptionalText(payload.Member, "include member"); err != nil {
		return wso2NormalizationResult{}, err
	}
	if err := validateOptionalText(payload.Path, "include path"); err != nil {
		return wso2NormalizationResult{}, err
	}
	member := contributionMember(input, payload.Member)
	subject := wso2Container(input, member)
	targetParticipant := fact.Participant{Kind: fact.ParticipantNamedElement, ID: wso2Identity("literal", target)}
	if sourceMember := strings.TrimSpace(input.Contribution.Locator.Member); sourceMember != "" {
		if canonicalTarget, ok := canonicalMemberTarget(target); ok {
			targetParticipant.ID = wso2Identity("member", input.Contribution.ArtifactID, canonicalTarget)
		}
	}
	qualifiers := wso2Qualifiers(
		stringQualifier("kind", payload.Kind),
		stringQualifier("target", target),
		stringQualifier("path", payload.Path),
	)
	candidate, err := wso2Fact(input, fact.PredicateDependency, subject, &targetParticipant, nil, qualifiers)
	if err != nil {
		return wso2NormalizationResult{}, err
	}
	return wso2NormalizationResult{facts: []fact.CanonicalFact{candidate}}, nil
}

func normalizeReference(input normalization.Input, _ wso2Mapping) (wso2NormalizationResult, error) {
	payload, err := decodeWSO2Payload(input.Contribution.Value)
	if err != nil {
		return wso2NormalizationResult{}, fmt.Errorf("wso2 reference contribution: %w", err)
	}
	if payload.Redacted || payloadContainsRedacted(payload) || payloadContainsDynamic(payload) || dynamicExpression(payload.Target) {
		return wso2NormalizationResult{incomplete: true}, nil
	}
	target, safe := safeLiteral(payload.Target)
	if !safe {
		return wso2NormalizationResult{incomplete: true}, nil
	}
	for field, value := range map[string]string{
		"reference kind":    payload.Kind,
		"reference name":    payload.Name,
		"reference path":    payload.Path,
		"reference member":  payload.Member,
		"reference context": payload.Context,
	} {
		if err := validateOptionalText(value, field); err != nil {
			return wso2NormalizationResult{}, err
		}
	}
	member := contributionMember(input, payload.Member)
	subject := wso2ReferenceSubject(input, member, payload)
	targetParticipant := fact.Participant{Kind: fact.ParticipantNamedElement, ID: wso2Identity("literal", target)}
	qualifiers := wso2Qualifiers(
		stringQualifier("kind", payload.Kind),
		stringQualifier("target", target),
		stringQualifier("name", payload.Name),
		stringQualifier("path", payload.Path),
		stringQualifier("context", payload.Context),
	)
	reference, err := wso2Fact(input, fact.PredicateReference, subject, &targetParticipant, nil, qualifiers)
	if err != nil {
		return wso2NormalizationResult{}, err
	}
	facts := []fact.CanonicalFact{reference}
	if isStructuralReferenceKind(payload.Kind) {
		dependency, dependencyErr := wso2Fact(input, fact.PredicateDependency, subject, &targetParticipant, nil, qualifiers)
		if dependencyErr != nil {
			return wso2NormalizationResult{}, dependencyErr
		}
		facts = append(facts, dependency)
	}
	return wso2NormalizationResult{facts: facts}, nil
}

func normalizeEndpointContribution(input normalization.Input, _ wso2Mapping) (wso2NormalizationResult, error) {
	payload, err := decodeWSO2Payload(input.Contribution.Value)
	if err != nil {
		return wso2NormalizationResult{}, fmt.Errorf("wso2 endpoint contribution: %w", err)
	}
	if payload.Redacted || payloadContainsRedacted(payload) || payloadContainsDynamic(payload) || dynamicExpression(payload.URI) {
		return wso2NormalizationResult{incomplete: true}, nil
	}
	for field, value := range map[string]string{
		"endpoint kind":       payload.Kind,
		"endpoint name":       payload.Name,
		"endpoint path":       payload.Path,
		"endpoint member":     payload.Member,
		"endpoint context":    payload.Context,
		"endpoint media type": payload.MediaType,
	} {
		if err := validateOptionalText(value, field); err != nil {
			return wso2NormalizationResult{}, err
		}
	}
	uri := strings.TrimSpace(payload.URI)
	if uri != "" {
		var redacted bool
		uri, redacted = sanitizeLiteral(uri)
		if redacted || uri == "" || uri == "[redacted]" {
			return wso2NormalizationResult{incomplete: true}, nil
		}
		if dynamicExpression(uri) {
			return wso2NormalizationResult{incomplete: true}, nil
		}
	}
	if strings.TrimSpace(payload.Kind) == "" && strings.TrimSpace(payload.Name) == "" && strings.TrimSpace(payload.Path) == "" && strings.TrimSpace(payload.Context) == "" && uri == "" {
		return wso2NormalizationResult{}, errors.New("wso2 endpoint identity requires a declarative field")
	}
	member := contributionMember(input, payload.Member)
	endpoint := fact.Participant{
		Kind: fact.ParticipantNamedElement,
		ID:   wso2Identity("endpoint", input.Contribution.ArtifactID, member, payload.Kind, payload.Name, payload.Path, payload.Context, uri),
	}
	value := firstNonEmpty(uri, payload.Path, payload.Name, payload.Kind)
	qualifiers := wso2Qualifiers(
		stringQualifier("kind", payload.Kind),
		stringQualifier("name", payload.Name),
		stringQualifier("path", payload.Path),
		stringQualifier("context", payload.Context),
		stringQualifier("media_type", payload.MediaType),
	)
	candidate, err := wso2Fact(input, fact.PredicateEndpoint, endpoint, nil, &fact.TypedValue{Kind: fact.ValueString, String: value}, qualifiers)
	if err != nil {
		return wso2NormalizationResult{}, err
	}
	return wso2NormalizationResult{facts: []fact.CanonicalFact{candidate}}, nil
}

func normalizeMessage(input normalization.Input, _ wso2Mapping) (wso2NormalizationResult, error) {
	payload, err := decodeWSO2Payload(input.Contribution.Value)
	if err != nil {
		return wso2NormalizationResult{}, fmt.Errorf("wso2 message contribution: %w", err)
	}
	if payload.Redacted || payloadContainsRedacted(payload) || payloadContainsDynamic(payload) || dynamicExpression(payload.URI) || dynamicExpression(payload.Target) {
		return wso2NormalizationResult{incomplete: true}, nil
	}
	for field, value := range map[string]string{
		"message kind":       payload.Kind,
		"message name":       payload.Name,
		"message target":     payload.Target,
		"message path":       payload.Path,
		"message member":     payload.Member,
		"message context":    payload.Context,
		"message uri":        payload.URI,
		"message media type": payload.MediaType,
	} {
		if err := validateOptionalText(value, field); err != nil {
			return wso2NormalizationResult{}, err
		}
	}
	uri := strings.TrimSpace(payload.URI)
	if uri != "" {
		var redacted bool
		uri, redacted = sanitizeLiteral(uri)
		if redacted || uri == "" || uri == "[redacted]" {
			return wso2NormalizationResult{incomplete: true}, nil
		}
	}
	target := strings.TrimSpace(payload.Target)
	if target != "" {
		var safe bool
		target, safe = safeLiteral(target)
		if !safe {
			return wso2NormalizationResult{incomplete: true}, nil
		}
	}
	if strings.TrimSpace(payload.Kind) == "" && strings.TrimSpace(payload.Name) == "" && strings.TrimSpace(payload.Path) == "" && strings.TrimSpace(payload.Context) == "" && uri == "" && strings.TrimSpace(payload.MediaType) == "" {
		return wso2NormalizationResult{}, errors.New("wso2 message identity requires declarative metadata")
	}
	member := contributionMember(input, payload.Member)
	message := fact.Participant{
		Kind: fact.ParticipantNamedElement,
		ID:   wso2Identity("message", input.Contribution.ArtifactID, member, payload.Kind, payload.Name, payload.Path, payload.Context, uri),
	}
	value := firstNonEmpty(payload.Name, payload.Kind, payload.Path, payload.Context, uri, target)
	qualifiers := wso2Qualifiers(
		stringQualifier("kind", payload.Kind),
		stringQualifier("name", payload.Name),
		stringQualifier("path", payload.Path),
		stringQualifier("context", payload.Context),
		stringQualifier("uri", uri),
		stringQualifier("media_type", payload.MediaType),
		stringQualifier("target", target),
	)
	candidate, err := wso2Fact(input, fact.PredicateMessage, message, nil, &fact.TypedValue{Kind: fact.ValueString, String: value}, qualifiers)
	if err != nil {
		return wso2NormalizationResult{}, err
	}
	return wso2NormalizationResult{facts: []fact.CanonicalFact{candidate}}, nil
}

func normalizeConfigurationContribution(input normalization.Input, _ wso2Mapping) (wso2NormalizationResult, error) {
	payload, err := decodeWSO2Payload(input.Contribution.Value)
	if err != nil {
		return wso2NormalizationResult{}, fmt.Errorf("wso2 configuration contribution: %w", err)
	}
	if payload.Redacted || payloadContainsRedacted(payload) || payloadContainsDynamic(payload) || dynamicExpression(payload.Key) {
		return wso2NormalizationResult{incomplete: true}, nil
	}
	if err := validateRequiredText(payload.Key, "configuration key"); err != nil {
		return wso2NormalizationResult{}, err
	}
	if payload.Key == "[redacted]" {
		return wso2NormalizationResult{incomplete: true}, nil
	}
	if err := validateOptionalText(payload.Kind, "configuration kind"); err != nil {
		return wso2NormalizationResult{}, err
	}
	if err := validateOptionalText(payload.Member, "configuration member"); err != nil {
		return wso2NormalizationResult{}, err
	}
	member := contributionMember(input, payload.Member)
	qualifiers := wso2Qualifiers(stringQualifier("kind", payload.Kind))
	candidate, err := wso2Fact(
		input,
		fact.PredicateConfiguration,
		wso2Container(input, member),
		nil,
		&fact.TypedValue{Kind: fact.ValueString, String: strings.TrimSpace(payload.Key)},
		qualifiers,
	)
	if err != nil {
		return wso2NormalizationResult{}, err
	}
	return wso2NormalizationResult{facts: []fact.CanonicalFact{candidate}}, nil
}

func decodeWSO2Payload(value json.RawMessage) (wso2Payload, error) {
	trimmed := bytes.TrimSpace(value)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return wso2Payload{}, errors.New("payload object is required")
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &object); err != nil {
		return wso2Payload{}, errors.New("payload is not a JSON object")
	}
	if object == nil {
		return wso2Payload{}, errors.New("payload object is required")
	}
	var payload wso2Payload
	if err := json.Unmarshal(trimmed, &payload); err != nil {
		return wso2Payload{}, errors.New("payload fields are malformed")
	}
	return payload, nil
}

func payloadContainsRedacted(payload wso2Payload) bool {
	for _, value := range []string{
		payload.Kind,
		payload.Name,
		payload.Target,
		payload.Path,
		payload.Member,
		payload.Context,
		payload.URI,
		payload.MediaType,
		payload.Key,
	} {
		if strings.Contains(strings.ToLower(value), "[redacted]") {
			return true
		}
	}
	return false
}

func payloadContainsDynamic(payload wso2Payload) bool {
	for _, value := range []string{
		payload.Kind,
		payload.Name,
		payload.Target,
		payload.Path,
		payload.Member,
		payload.Context,
		payload.URI,
		payload.MediaType,
		payload.Key,
	} {
		if dynamicExpression(value) {
			return true
		}
	}
	return false
}

func wso2Coverage(input normalization.Input, dimension contract.Dimension, state contract.CoverageState, message string) contract.Coverage {
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

func wso2Fact(input normalization.Input, predicate fact.Predicate, subject fact.Participant, object *fact.Participant, value *fact.TypedValue, qualifiers []fact.Qualifier) (fact.CanonicalFact, error) {
	candidate := fact.CanonicalFact{
		Version:    fact.Version,
		Scope:      input.Scope,
		Predicate:  predicate,
		Subject:    subject,
		Object:     cloneParticipant(object),
		Value:      cloneValue(value),
		Qualifiers: append([]fact.Qualifier(nil), qualifiers...),
		Producer:   fact.Producer{ID: input.Manifest.ID, Version: input.Manifest.Version, Method: input.Manifest.Method},
		Evidence:   append([]fact.EvidenceRef(nil), input.Evidence...),
	}
	identifier, err := fact.FactID(candidate)
	if err != nil {
		return fact.CanonicalFact{}, fmt.Errorf("wso2 normalizer fact identity: %w", err)
	}
	candidate.ID = identifier
	if err := candidate.Validate(); err != nil {
		return fact.CanonicalFact{}, fmt.Errorf("wso2 normalizer fact validation: %w", err)
	}
	return candidate, nil
}

func wso2Container(input normalization.Input, member string) fact.Participant {
	if member == "" {
		return fact.Participant{Kind: fact.ParticipantArtifact, ID: input.Contribution.ArtifactID}
	}
	return fact.Participant{
		Kind: fact.ParticipantNamedElement,
		ID:   wso2Identity("member", input.Contribution.ArtifactID, member),
	}
}

func wso2ReferenceSubject(input normalization.Input, member string, payload wso2Payload) fact.Participant {
	if strings.TrimSpace(payload.Name) == "" && strings.TrimSpace(payload.Path) == "" {
		return wso2Container(input, member)
	}
	return fact.Participant{
		Kind: fact.ParticipantNamedElement,
		ID:   wso2Identity("element", input.Contribution.ArtifactID, member, payload.Kind, payload.Name, payload.Path, payload.Context),
	}
}

func contributionMember(input normalization.Input, payloadMember string) string {
	member := strings.TrimSpace(input.Contribution.Locator.Member)
	if member == "" {
		member = strings.TrimSpace(payloadMember)
	}
	return member
}

// canonicalMemberTarget returns the stable member identity path used for
// cross-member CAR correlation. It deliberately rejects absolute paths and
// every explicit traversal segment; an uncorrelatable target remains a
// literal participant instead of being resolved speculatively.
func canonicalMemberTarget(value string) (string, bool) {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	firstSegment := value
	if separator := strings.IndexByte(firstSegment, '/'); separator >= 0 {
		firstSegment = firstSegment[:separator]
	}
	if value == "" || strings.HasPrefix(value, "/") || strings.Contains(value, "://") || strings.Contains(firstSegment, ":") {
		return "", false
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == ".." {
			return "", false
		}
	}
	cleaned := path.Clean(value)
	if cleaned == "." || cleaned == "" || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", false
	}
	return cleaned, true
}

func wso2Identity(kind string, parts ...string) string {
	digest := sha256.New()
	_, _ = digest.Write([]byte("manu:wso2:normalization:v1\x00"))
	_, _ = digest.Write([]byte(kind))
	for _, part := range parts {
		_, _ = digest.Write([]byte{'\x00'})
		_, _ = digest.Write([]byte(part))
	}
	return "wso2-" + kind + "-" + hex.EncodeToString(digest.Sum(nil))
}

func wso2Qualifiers(values ...fact.Qualifier) []fact.Qualifier {
	result := make([]fact.Qualifier, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value.Name == "" || value.Value.Kind != fact.ValueString || value.Value.String == "" {
			continue
		}
		if _, exists := seen[value.Name]; exists {
			continue
		}
		seen[value.Name] = struct{}{}
		result = append(result, value)
	}
	for left := 0; left < len(result); left++ {
		for right := left + 1; right < len(result); right++ {
			if result[right].Name < result[left].Name {
				result[left], result[right] = result[right], result[left]
			}
		}
	}
	return result
}

func stringQualifier(name, value string) fact.Qualifier {
	return fact.Qualifier{Name: name, Value: fact.TypedValue{Kind: fact.ValueString, String: strings.TrimSpace(value)}}
}

func safeLiteral(value string) (string, bool) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || trimmed == "[redacted]" || dynamicExpression(trimmed) {
		return "", false
	}
	safe, redacted := sanitizeLiteral(trimmed)
	if redacted || safe == "" || safe == "[redacted]" {
		return "", false
	}
	for _, character := range safe {
		if unicode.IsControl(character) {
			return "", false
		}
	}
	return safe, true
}

func validateRequiredText(value, field string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("wso2 %s is required", field)
	}
	return validateOptionalText(value, field)
}

func validateOptionalText(value, field string) error {
	for _, character := range value {
		if unicode.IsControl(character) {
			return fmt.Errorf("wso2 %s contains a control character", field)
		}
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func isStructuralReferenceKind(kind string) bool {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "endpoint", "targetendpoint", "sequence", "insequence", "outsequence", "onerror", "faultsequence", "template", "import", "include", "extends", "implements", "depends", "dependency":
		return true
	default:
		return false
	}
}

func cloneParticipant(value *fact.Participant) *fact.Participant {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

func cloneValue(value *fact.TypedValue) *fact.TypedValue {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
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
