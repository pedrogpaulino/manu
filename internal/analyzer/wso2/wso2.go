// Package wso2 extracts declarative WSO2/Synapse observations from XML files
// and CAR members. CARs are inspected and read in place; no member is
// extracted to disk and dynamic expressions remain explicit gaps.
package wso2

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strings"

	"github.com/pedrogpaulino/manu/internal/analysis"
	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/evidence"
	"github.com/pedrogpaulino/manu/internal/source"
)

const (
	AnalyzerID      = "wso2"
	AnalyzerVersion = "1"
	AnalyzerMethod  = "declarative-wso2-v1"
)

var typeElements = map[string]bool{
	"api":              true,
	"endpoint":         true,
	"inboundendpoint":  true,
	"localentry":       true,
	"proxy":            true,
	"proxyservice":     true,
	"sequence":         true,
	"template":         true,
	"task":             true,
	"property":         true,
	"data":             true,
	"dataservice":      true,
	"eventsource":      true,
	"message-store":    true,
	"messageprocessor": true,
}

var endpointElements = map[string]bool{
	"api":             true,
	"proxy":           true,
	"proxyservice":    true,
	"endpoint":        true,
	"inboundendpoint": true,
	"address":         true,
	"http":            true,
}

var messageElements = map[string]bool{
	"payloadfactory": true,
	"message":        true,
	"publishevent":   true,
}

var configurationElements = map[string]bool{
	"property":  true,
	"parameter": true,
	"param":     true,
}

const (
	endpointContributionType       = "wso2.endpoint"
	messageContributionType        = "wso2.message"
	configurationContributionType  = "wso2.configuration"
	dynamicEndpointGapCode         = "dynamic_endpoint"
	redactedEndpointGapCode        = "redacted_endpoint"
	dynamicMessageGapCode          = "dynamic_message"
	redactedMessageGapCode         = "redacted_message"
	dynamicConfigurationGapCode    = "dynamic_configuration"
	redactedConfigurationGapCode   = "redacted_configuration"
	missingConfigurationKeyGapCode = "configuration_key_missing"
)

// Analyzer handles standalone XML and CAR package artifacts.
type Analyzer struct{}

// New returns a stateless WSO2 analyzer.
func New() *Analyzer { return &Analyzer{} }

// Descriptor describes the supported declarative method.
func (a *Analyzer) Descriptor() analysis.Descriptor {
	return analysis.Descriptor{
		ID:              AnalyzerID,
		Version:         AnalyzerVersion,
		Method:          AnalyzerMethod,
		ContractVersion: contract.Version,
		ArtifactTypes:   []string{analysis.ArtifactTypeCAR, analysis.ArtifactTypeXML},
		Capabilities: []string{
			"car_members",
			"xml_streaming_tokens",
			"types",
			"literal_references",
			"imports_includes",
			"dynamic_gap_reporting",
		},
	}
}

// Analyze inspects XML in a bounded stream. For CARs, only textual members
// are selected and each member is read through the confined source root.
func (a *Analyzer) Analyze(ctx context.Context, input analysis.ArtifactInput) (analysis.Output, error) {
	if err := ctx.Err(); err != nil {
		return analysis.Output{}, err
	}
	output := analysis.Output{
		Contributions: make([]contract.Contribution, 0),
		Coverage:      make([]contract.Coverage, 0),
		Gaps:          make([]contract.Gap, 0),
	}
	switch input.Artifact.Type {
	case analysis.ArtifactTypeCAR:
		archive, err := input.Archive(ctx)
		if err != nil {
			return output, fmt.Errorf("inspect CAR: %w", err)
		}
		members := append([]string{}, archiveTextMembers(archive)...)
		sort.Strings(members)
		if len(members) == 0 {
			output.Coverage = append(output.Coverage, coverage(input, "package", contract.CoverageNotSupported, "CAR has no bounded textual XML member", ""))
			output.Gaps = append(output.Gaps, gap(input, "car_no_xml_member", "entities_and_relationships", "CAR member inventory did not contain a supported XML document", ""))
			ensureSemanticGap(input, &output)
			return output, nil
		}
		for _, member := range members {
			if err := ctx.Err(); err != nil {
				return output, err
			}
			data, truncated, err := input.ArchiveMember(ctx, member)
			if err != nil {
				output.Coverage = append(output.Coverage, coverage(input, "member:"+member, contract.CoverageFailed, "CAR member could not be read within configured limits", member))
				output.Gaps = append(output.Gaps, gap(input, "car_member_read_failed", "evidence_provenance_uncertainty_gaps", "CAR member was not read", member))
				continue
			}
			memberOutput, parseErr := a.parseXML(ctx, input, member, data)
			output = appendOutput(output, memberOutput)
			if truncated {
				output.Coverage = append(output.Coverage, coverage(input, "member:"+member, contract.CoverageIncomplete, "CAR member XML input was bounded and truncated", member))
				output.Gaps = append(output.Gaps, gap(input, "car_member_truncated", "evidence_provenance_uncertainty_gaps", "XML member exceeded the configured extraction limit", member))
			}
			if parseErr != nil {
				output.Coverage = append(output.Coverage, coverage(input, "member:"+member, contract.CoverageFailed, "XML tokenization stopped at malformed input", member))
				output.Gaps = append(output.Gaps, gap(input, "xml_parse_failed", "entities_and_relationships", "XML tokenization was incomplete", member))
			}
		}
		ensureSemanticGap(input, &output)
		return output, nil
	case analysis.ArtifactTypeXML:
		text, err := input.Text(ctx, true)
		if err != nil {
			return output, fmt.Errorf("read XML: %w", err)
		}
		if text.Classification != "text" {
			output.Coverage = append(output.Coverage, coverage(input, "document", contract.CoverageNotApplicable, "XML artifact was not classified as text", ""))
			output.Gaps = append(output.Gaps, gap(input, "xml_not_text", "entities_and_relationships", "XML tokenization requires textual input", ""))
			ensureSemanticGap(input, &output)
			return output, nil
		}
		memberOutput, parseErr := a.parseXML(ctx, input, "", []byte(text.Content))
		output = appendOutput(output, memberOutput)
		if text.Truncated {
			output.Coverage = append(output.Coverage, coverage(input, "document", contract.CoverageIncomplete, "XML input was bounded and truncated", ""))
			output.Gaps = append(output.Gaps, gap(input, "xml_truncated", "evidence_provenance_uncertainty_gaps", "XML document exceeded the configured extraction limit", ""))
		}
		ensureSemanticGap(input, &output)
		return output, parseErr
	default:
		return output, fmt.Errorf("unsupported WSO2 artifact type %q", input.Artifact.Type)
	}
}

func (a *Analyzer) parseXML(ctx context.Context, input analysis.ArtifactInput, member string, data []byte) (analysis.Output, error) {
	output := analysis.Output{
		Contributions: make([]contract.Contribution, 0),
		Coverage:      make([]contract.Coverage, 0),
		Gaps:          make([]contract.Gap, 0),
	}
	decoder := xml.NewDecoder(bytes.NewReader(data))
	stack := make([]string, 0, 8)
	seenCoverage := make(map[string]bool)
	descriptor := a.Descriptor()
	for {
		if err := ctx.Err(); err != nil {
			return output, err
		}
		token, err := decoder.Token()
		if err == io.EOF {
			return output, nil
		}
		if err != nil {
			return output, fmt.Errorf("decode XML member %q: %w", member, err)
		}
		switch value := token.(type) {
		case xml.StartElement:
			stack = append(stack, value.Name.Local)
			pathName := strings.Join(stack, "/")
			local := strings.ToLower(value.Name.Local)
			if typeElements[local] {
				if err := appendTypeObservation(&output, input, descriptor, value, pathName, member, decoder.InputOffset()); err != nil {
					return output, err
				}
				addCoverage(&output, seenCoverage, coverage(input, "member:"+member, contract.CoverageProduced, "declarative WSO2 type observed", member))
			}
			if endpointElements[local] {
				if err := appendEndpointObservation(&output, seenCoverage, input, descriptor, value, pathName, member, decoder.InputOffset()); err != nil {
					return output, err
				}
			}
			if messageElements[local] {
				if err := appendMessageObservation(&output, seenCoverage, input, descriptor, value, pathName, member, decoder.InputOffset()); err != nil {
					return output, err
				}
			}
			if configurationElements[local] {
				if err := appendConfigurationObservation(&output, seenCoverage, input, descriptor, value, pathName, member, decoder.InputOffset()); err != nil {
					return output, err
				}
			}
			for _, attribute := range value.Attr {
				attributeName := strings.ToLower(attribute.Name.Local)
				if isImportAttribute(attributeName) {
					if dynamicExpression(attribute.Value) {
						output.Gaps = append(output.Gaps, gapAt(input, "dynamic_import", "configuration_variations", "import/include target is dynamic and was not resolved", member, decoder.InputOffset()))
						continue
					}
					target, redacted := sanitizeLiteral(attribute.Value)
					contribution, contributionErr := analysis.NewContribution(
						input,
						a.Descriptor(),
						methodFor(member, "include", pathName+"/@"+attribute.Name.Local, decoder.InputOffset()),
						"wso2.include",
						locator(input, member, decoder.InputOffset()),
						map[string]any{"kind": attributeName, "target": target, "redacted": redacted, "path": pathName, "member": member},
					)
					if contributionErr != nil {
						return output, contributionErr
					}
					output.Contributions = append(output.Contributions, contribution)
					if input.Evidence.Enabled {
						draft := analysis.EvidenceDraft{
							ContributionID: contribution.ID,
							Locator:        contribution.Locator,
							OriginalHash:   evidence.ContentDigest(attribute.Value),
						}
						if redacted {
							draft.State = evidence.ContentStateRedacted
							draft.RedactionReason = "sensitive-content"
							draft.Content = "attribute " + attribute.Name.Local + " (redacted)"
						} else {
							draft.Content = "attribute " + attribute.Name.Local + ": " + target
						}
						output.Evidence = append(output.Evidence, draft)
					}
					addCoverage(&output, seenCoverage, coverage(input, "member:"+member, contract.CoverageProduced, "literal import/include target observed", member))
				}
				if isReferenceAttribute(attributeName) {
					if dynamicExpression(attribute.Value) {
						output.Gaps = append(output.Gaps, gapAt(input, "dynamic_reference", "flows_and_dependencies", "reference expression is dynamic and was not resolved", member, decoder.InputOffset()))
						continue
					}
					target, redacted := sanitizeLiteral(attribute.Value)
					contribution, contributionErr := analysis.NewContribution(
						input,
						a.Descriptor(),
						methodFor(member, "reference", pathName+"/@"+attribute.Name.Local, decoder.InputOffset()),
						"wso2.reference",
						locator(input, member, decoder.InputOffset()),
						map[string]any{"kind": attributeName, "target": target, "redacted": redacted, "path": pathName, "member": member},
					)
					if contributionErr != nil {
						return output, contributionErr
					}
					output.Contributions = append(output.Contributions, contribution)
					if input.Evidence.Enabled {
						draft := analysis.EvidenceDraft{
							ContributionID: contribution.ID,
							Locator:        contribution.Locator,
							OriginalHash:   evidence.ContentDigest(attribute.Value),
						}
						if redacted {
							draft.State = evidence.ContentStateRedacted
							draft.RedactionReason = "sensitive-content"
							draft.Content = "attribute " + attribute.Name.Local + " (redacted)"
						} else {
							draft.Content = "attribute " + attribute.Name.Local + ": " + target
						}
						output.Evidence = append(output.Evidence, draft)
					}
					addCoverage(&output, seenCoverage, coverage(input, "member:"+member, contract.CoverageProduced, "literal WSO2 reference observed", member))
				}
			}
		case xml.CharData:
			if len(stack) == 0 {
				continue
			}
			literal := strings.TrimSpace(string(value))
			if literal == "" {
				continue
			}
			local := strings.ToLower(stack[len(stack)-1])
			if (local == "address" || local == "http") && !isImportElement(local) {
				if err := appendEndpointTextObservation(&output, seenCoverage, input, descriptor, stack, member, decoder.InputOffset(), literal); err != nil {
					return output, err
				}
			}
			if !isImportElement(local) {
				continue
			}
			if dynamicExpression(literal) {
				output.Gaps = append(output.Gaps, gapAt(input, "dynamic_include", "configuration_variations", "import/include text is dynamic and was not resolved", member, decoder.InputOffset()))
				continue
			}
			target, redacted := sanitizeLiteral(literal)
			contribution, contributionErr := analysis.NewContribution(
				input,
				a.Descriptor(),
				methodFor(member, "include-text", strings.Join(stack, "/"), decoder.InputOffset()),
				"wso2.include",
				locator(input, member, decoder.InputOffset()),
				map[string]any{"kind": "text", "target": target, "redacted": redacted, "path": strings.Join(stack, "/"), "member": member},
			)
			if contributionErr != nil {
				return output, contributionErr
			}
			output.Contributions = append(output.Contributions, contribution)
			if input.Evidence.Enabled {
				draft := analysis.EvidenceDraft{
					ContributionID: contribution.ID,
					Locator:        contribution.Locator,
					OriginalHash:   evidence.ContentDigest(literal),
				}
				if redacted {
					draft.State = evidence.ContentStateRedacted
					draft.RedactionReason = "sensitive-content"
					draft.Content = "include text (redacted)"
				} else {
					draft.Content = "include text: " + target
				}
				output.Evidence = append(output.Evidence, draft)
			}
			addCoverage(&output, seenCoverage, coverage(input, "member:"+member, contract.CoverageProduced, "literal import/include text observed", member))
		case xml.EndElement:
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		}
	}
}

type literalAttribute struct {
	value    string
	name     string
	found    bool
	dynamic  bool
	redacted bool
}

func appendTypeObservation(
	output *analysis.Output,
	input analysis.ArtifactInput,
	descriptor analysis.Descriptor,
	element xml.StartElement,
	pathName, member string,
	offset int64,
) error {
	identity := declaredIdentity(element)
	name := element.Name.Local
	nameSource := "local_name"
	if identity.value != "" {
		name = identity.value
		nameSource = "declared_" + identity.name
	}
	payload := map[string]any{
		"kind":        element.Name.Local,
		"name":        name,
		"name_source": nameSource,
		"path":        pathName,
		"member":      member,
	}
	return appendObservation(
		output,
		input,
		descriptor,
		methodFor(member, "type", pathName, offset),
		"wso2.type",
		locator(input, member, offset),
		payload,
		"element "+element.Name.Local,
		input.Artifact.Hash,
	)
}

func appendEndpointObservation(
	output *analysis.Output,
	seenCoverage map[string]bool,
	input analysis.ArtifactInput,
	descriptor analysis.Descriptor,
	element xml.StartElement,
	pathName, member string,
	offset int64,
) error {
	identity := declaredIdentity(element)
	pathValue := findLiteralAttribute(element, "path")
	contextValue := findLiteralAttribute(element, "context")
	uriValue := findLiteralAttribute(element, "uri", "url", "address", "serviceurl", "location")

	redacted := redactedPlaceholder(identity) || redactedPlaceholder(pathValue) || redactedPlaceholder(contextValue) || redactedPlaceholder(uriValue)
	dynamic := identity.dynamic || pathValue.dynamic || contextValue.dynamic || uriValue.dynamic
	if redactedPlaceholder(pathValue) {
		pathValue.value = ""
	}
	if redactedPlaceholder(contextValue) {
		contextValue.value = ""
	}
	if redactedPlaceholder(uriValue) {
		uriValue.value = ""
	}
	if redactedPlaceholder(identity) {
		identity.value = ""
	}

	sustained := identity.value != "" || pathValue.value != "" || contextValue.value != "" || uriValue.value != ""
	if dynamic {
		addGap(output, gapAt(input, dynamicEndpointGapCode, string(contract.DimensionEntitiesAndRelationships), "endpoint identity, path, or URI is dynamic and was not resolved", member, offset))
	}
	if redacted {
		addGap(output, gapAt(input, redactedEndpointGapCode, string(contract.DimensionEntitiesAndRelationships), "endpoint identity, path, or URI was redacted and was not used as an endpoint fact", member, offset))
	}
	if !sustained {
		addCoverage(output, seenCoverage, coverageForDimension(input, "member:"+member, contract.DimensionEntitiesAndRelationships, contract.CoverageIncomplete, "endpoint extraction requires a safe literal identity, path, or URI", member))
		return nil
	}

	payload := map[string]any{
		"kind":     element.Name.Local,
		"member":   member,
		"xml_path": pathName,
	}
	if identity.value != "" {
		payload["name"] = identity.value
	}
	if pathValue.value != "" {
		payload["path"] = pathValue.value
	}
	if contextValue.value != "" {
		payload["context"] = contextValue.value
	}
	if uriValue.value != "" {
		payload["uri"] = uriValue.value
	}
	if redacted {
		payload["redacted"] = true
	}
	if err := appendObservation(
		output,
		input,
		descriptor,
		methodFor(member, "endpoint", pathName, offset),
		endpointContributionType,
		locator(input, member, offset),
		payload,
		"endpoint "+element.Name.Local,
		input.Artifact.Hash,
	); err != nil {
		return err
	}
	state := contract.CoverageProduced
	message := "literal WSO2 endpoint identity, path, or URI observed"
	if dynamic || redacted {
		state = contract.CoverageIncomplete
		message = "WSO2 endpoint metadata was partially dynamic or redacted"
	}
	addCoverage(output, seenCoverage, coverageForDimension(input, "member:"+member, contract.DimensionEntitiesAndRelationships, state, message, member))
	return nil
}

func appendMessageObservation(
	output *analysis.Output,
	seenCoverage map[string]bool,
	input analysis.ArtifactInput,
	descriptor analysis.Descriptor,
	element xml.StartElement,
	pathName, member string,
	offset int64,
) error {
	identity := findLiteralAttribute(element, "name", "id", "key", "streamname", "stream", "eventstream", "topic")
	media := findLiteralAttribute(element, "media-type", "mediatype", "media_type")
	contentType := findLiteralAttribute(element, "content-type", "contenttype", "content_type", "type")
	dynamic := identity.dynamic || media.dynamic || contentType.dynamic

	if dynamic {
		addGap(output, gapAt(input, dynamicMessageGapCode, string(contract.DimensionFlowsAndDependencies), "message construction metadata is dynamic and was not resolved", member, offset))
	}
	if redactedPlaceholder(identity) || redactedPlaceholder(media) || redactedPlaceholder(contentType) {
		addGap(output, gapAt(input, redactedMessageGapCode, string(contract.DimensionFlowsAndDependencies), "message construction metadata was redacted and was not retained", member, offset))
	}

	payload := map[string]any{
		"kind":     element.Name.Local,
		"member":   member,
		"path":     pathName,
		"xml_path": pathName,
	}
	if identity.value != "" {
		payload["name"] = identity.value
	}
	if media.value != "" && media.value != "[redacted]" {
		payload["media_type"] = media.value
	}
	if contentType.value != "" && contentType.value != "[redacted]" {
		payload["content_type"] = contentType.value
	}
	if redactedPlaceholder(identity) || redactedPlaceholder(media) || redactedPlaceholder(contentType) {
		payload["redacted"] = true
	}
	if err := appendObservation(
		output,
		input,
		descriptor,
		methodFor(member, "message", pathName, offset),
		messageContributionType,
		locator(input, member, offset),
		payload,
		"message "+element.Name.Local,
		input.Artifact.Hash,
	); err != nil {
		return err
	}
	state := contract.CoverageProduced
	message := "declarative WSO2 message construction observed"
	if dynamic || redactedPlaceholder(identity) || redactedPlaceholder(media) || redactedPlaceholder(contentType) {
		state = contract.CoverageIncomplete
		message = "WSO2 message metadata was partially dynamic or redacted"
	}
	addCoverage(output, seenCoverage, coverageForDimension(input, "member:"+member, contract.DimensionFlowsAndDependencies, state, message, member))
	return nil
}

func appendEndpointTextObservation(
	output *analysis.Output,
	seenCoverage map[string]bool,
	input analysis.ArtifactInput,
	descriptor analysis.Descriptor,
	stack []string,
	member string,
	offset int64,
	literal string,
) error {
	if dynamicExpression(literal) {
		addGap(output, gapAt(input, dynamicEndpointGapCode, string(contract.DimensionEntitiesAndRelationships), "endpoint URI is dynamic and was not resolved", member, offset))
		addCoverage(output, seenCoverage, coverageForDimension(input, "member:"+member, contract.DimensionEntitiesAndRelationships, contract.CoverageIncomplete, "endpoint extraction requires a safe literal URI", member))
		return nil
	}
	parsed, err := url.Parse(literal)
	if err != nil || (parsed.Scheme == "" && !strings.HasPrefix(literal, "/")) {
		return nil
	}
	uri, sanitized := sanitizeLiteral(literal)
	if uri == "" || uri == "[redacted]" {
		addGap(output, gapAt(input, redactedEndpointGapCode, string(contract.DimensionEntitiesAndRelationships), "endpoint URI was redacted and was not used as an endpoint fact", member, offset))
		addCoverage(output, seenCoverage, coverageForDimension(input, "member:"+member, contract.DimensionEntitiesAndRelationships, contract.CoverageIncomplete, "endpoint extraction requires a safe literal URI", member))
		return nil
	}
	pathName := strings.Join(stack, "/")
	payload := map[string]any{
		"kind":     stack[len(stack)-1],
		"member":   member,
		"uri":      uri,
		"xml_path": pathName,
	}
	if err := appendObservation(
		output,
		input,
		descriptor,
		methodFor(member, "endpoint", pathName, offset),
		endpointContributionType,
		locator(input, member, offset),
		payload,
		"endpoint "+stack[len(stack)-1],
		input.Artifact.Hash,
	); err != nil {
		return err
	}
	state := contract.CoverageProduced
	message := "literal WSO2 endpoint URI observed"
	if sanitized {
		// The retained URI is already sanitized. The contribution remains
		// usable because no redaction placeholder is published.
		message = "sanitized literal WSO2 endpoint URI observed"
	}
	addCoverage(output, seenCoverage, coverageForDimension(input, "member:"+member, contract.DimensionEntitiesAndRelationships, state, message, member))
	return nil
}

func appendConfigurationObservation(
	output *analysis.Output,
	seenCoverage map[string]bool,
	input analysis.ArtifactInput,
	descriptor analysis.Descriptor,
	element xml.StartElement,
	pathName, member string,
	offset int64,
) error {
	key := findLiteralAttribute(element, "name", "key")
	value := findLiteralAttribute(element, "value", "expression", "default", "value-ref", "valueref")
	if !key.found || (key.value == "" && !key.dynamic && !redactedPlaceholder(key)) {
		addGap(output, gapAt(input, missingConfigurationKeyGapCode, string(contract.DimensionConfigurationVariations), "configuration element has no literal name or key", member, offset))
		addCoverage(output, seenCoverage, coverageForDimension(input, "member:"+member, contract.DimensionConfigurationVariations, contract.CoverageIncomplete, "configuration extraction requires a literal name or key", member))
		return nil
	}
	if key.dynamic || value.dynamic {
		addGap(output, gapAt(input, dynamicConfigurationGapCode, string(contract.DimensionConfigurationVariations), "configuration key or value is dynamic and was not resolved", member, offset))
	}
	if redactedPlaceholder(key) || redactedPlaceholder(value) {
		addGap(output, gapAt(input, redactedConfigurationGapCode, string(contract.DimensionConfigurationVariations), "configuration key or value was redacted and was not retained", member, offset))
	}
	if key.value == "" || redactedPlaceholder(key) {
		addCoverage(output, seenCoverage, coverageForDimension(input, "member:"+member, contract.DimensionConfigurationVariations, contract.CoverageIncomplete, "configuration key was not safe to retain", member))
		return nil
	}

	payload := map[string]any{
		"kind":   element.Name.Local,
		"key":    key.value,
		"member": member,
		"path":   pathName,
	}
	if err := appendObservation(
		output,
		input,
		descriptor,
		methodFor(member, "configuration", pathName, offset),
		configurationContributionType,
		locator(input, member, offset),
		payload,
		"configuration "+element.Name.Local,
		input.Artifact.Hash,
	); err != nil {
		return err
	}
	state := contract.CoverageProduced
	message := "literal WSO2 configuration key observed"
	if key.dynamic || redactedPlaceholder(key) || value.dynamic || redactedPlaceholder(value) {
		state = contract.CoverageIncomplete
		message = "WSO2 configuration key observed without resolving dynamic or sensitive value"
	}
	addCoverage(output, seenCoverage, coverageForDimension(input, "member:"+member, contract.DimensionConfigurationVariations, state, message, member))
	return nil
}

func appendObservation(
	output *analysis.Output,
	input analysis.ArtifactInput,
	descriptor analysis.Descriptor,
	method, typ string,
	loc contract.Locator,
	payload map[string]any,
	evidenceText, originalHash string,
) error {
	contribution, err := analysis.NewContribution(input, descriptor, method, typ, loc, payload)
	if err != nil {
		return err
	}
	output.Contributions = append(output.Contributions, contribution)
	if input.Evidence.Enabled {
		if originalHash == "" {
			originalHash = input.Artifact.Hash
		}
		output.Evidence = append(output.Evidence, analysis.EvidenceDraft{
			ContributionID: contribution.ID,
			Locator:        contribution.Locator,
			Content:        evidenceText,
			OriginalHash:   originalHash,
		})
	}
	return nil
}

func declaredIdentity(element xml.StartElement) literalAttribute {
	return findLiteralAttribute(element, "name", "id", "key")
}

func findLiteralAttribute(element xml.StartElement, names ...string) literalAttribute {
	var observed literalAttribute
	for _, wanted := range names {
		for _, attribute := range element.Attr {
			if strings.ToLower(attribute.Name.Local) != wanted {
				continue
			}
			value := strings.TrimSpace(attribute.Value)
			result := literalAttribute{name: wanted, found: true}
			if value == "" {
				observed.found = true
				continue
			}
			if dynamicExpression(value) {
				observed.found = true
				observed.dynamic = true
				continue
			}
			sanitized, redacted := sanitizeLiteral(value)
			if sanitized == "[redacted]" {
				observed.found = true
				observed.redacted = true
				continue
			}
			result.value = sanitized
			result.redacted = redacted
			result.dynamic = observed.dynamic
			result.redacted = result.redacted || observed.redacted
			return result
		}
	}
	return observed
}

func redactedPlaceholder(value literalAttribute) bool {
	return value.value == "[redacted]" || (value.redacted && value.value == "")
}

func addGap(output *analysis.Output, value contract.Gap) {
	for _, existing := range output.Gaps {
		if existing.Code == value.Code && sameLocator(existing.Locator, value.Locator) {
			return
		}
	}
	output.Gaps = append(output.Gaps, value)
}

func sameLocator(left, right *contract.Locator) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
}

func appendOutput(left, right analysis.Output) analysis.Output {
	left.Contributions = append(left.Contributions, right.Contributions...)
	left.Coverage = append(left.Coverage, right.Coverage...)
	left.Gaps = append(left.Gaps, right.Gaps...)
	left.Evidence = append(left.Evidence, right.Evidence...)
	return left
}

func archiveTextMembers(archive source.ArchiveResult) []string {
	members := make([]string, 0)
	for _, member := range archive.Members {
		if member.Directory {
			continue
		}
		lower := strings.ToLower(member.Name)
		if strings.HasSuffix(lower, ".xml") || strings.HasSuffix(lower, ".wsdl") || strings.HasSuffix(lower, ".xsd") {
			members = append(members, member.Name)
		}
	}
	return members
}

func isImportAttribute(name string) bool {
	switch name {
	case "href", "schemalocation", "location", "file", "include", "import":
		return true
	default:
		return false
	}
}

func isReferenceAttribute(name string) bool {
	switch name {
	case "ref", "key", "uri", "url", "value", "endpoint", "sequence", "target", "targetendpoint", "insequence", "outsequence", "onerror", "faultsequence", "template":
		return true
	default:
		return false
	}
}

func isImportElement(name string) bool {
	switch name {
	case "import", "include", "imports", "resources", "resource":
		return true
	default:
		return false
	}
}

func dynamicExpression(value string) bool {
	value = strings.TrimSpace(value)
	return strings.Contains(value, "${") || strings.Contains(value, "{{") || strings.Contains(value, "$ctx") || strings.Contains(value, "get-property") || strings.Contains(value, "xpath(") || strings.Contains(value, "synapse(") || strings.Contains(value, "synapse:")
}

func sanitizeLiteral(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", false
	}
	lower := strings.ToLower(value)
	for _, marker := range []string{"password", "passwd", "secret", "token", "authorization", "bearer", "credential", "api-key", "api_key", "clientsecret", "private key", "pem"} {
		if strings.Contains(lower, marker) {
			return "[redacted]", true
		}
	}
	parsed, err := url.Parse(value)
	redacted := false
	if err == nil && (parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "") {
		parsed.User = nil
		parsed.RawQuery = ""
		parsed.Fragment = ""
		value = parsed.String()
		redacted = true
	}
	if len(value) > 512 {
		return "[redacted]", true
	}
	return value, redacted
}

func ensureSemanticGap(input analysis.ArtifactInput, output *analysis.Output) {
	for _, existing := range output.Gaps {
		if existing.Code == "wso2_semantics_not_supported" {
			return
		}
	}
	output.Gaps = append(output.Gaps, gap(
		input,
		"wso2_semantics_not_supported",
		"flows_and_dependencies",
		"dynamic mediation, runtime expressions, deployment state, and execution semantics are not reconstructed",
		"",
	))
}

func methodFor(member, kind, pathName string, offset int64) string {
	return strings.Join([]string{kind, member, pathName, fmt.Sprint(offset)}, ":")
}

func locator(input analysis.ArtifactInput, member string, offset int64) contract.Locator {
	return contract.Locator{
		SourceID:   input.SourceID,
		ArtifactID: input.Artifact.ID,
		Path:       input.Artifact.Path,
		Member:     member,
		ByteOffset: offset,
	}
}

func coverage(input analysis.ArtifactInput, scope string, state contract.CoverageState, message, member string) contract.Coverage {
	return coverageForDimension(input, scope, contract.DimensionEntitiesAndRelationships, state, message, member)
}

func coverageForDimension(input analysis.ArtifactInput, scope string, dimension contract.Dimension, state contract.CoverageState, message, member string) contract.Coverage {
	return contract.Coverage{
		Dimension: string(dimension),
		Scope:     scope,
		State:     state,
		Message:   message,
		Locator:   &contract.Locator{SourceID: input.SourceID, ArtifactID: input.Artifact.ID, Path: input.Artifact.Path, Member: member},
	}
}

func gap(input analysis.ArtifactInput, code, dimension, message, member string) contract.Gap {
	return contract.Gap{
		Code:      code,
		Dimension: dimension,
		Scope:     "member:" + member,
		Message:   message,
		Locator:   &contract.Locator{SourceID: input.SourceID, ArtifactID: input.Artifact.ID, Path: input.Artifact.Path, Member: member},
	}
}

func gapAt(input analysis.ArtifactInput, code, dimension, message, member string, offset int64) contract.Gap {
	value := gap(input, code, dimension, message, member)
	value.Locator.ByteOffset = offset
	return value
}

func addCoverage(output *analysis.Output, seen map[string]bool, value contract.Coverage) {
	key := value.Dimension + "\x00" + value.Scope + "\x00" + string(value.State)
	if seen[key] {
		return
	}
	seen[key] = true
	output.Coverage = append(output.Coverage, value)
}

var _ analysis.Analyzer = (*Analyzer)(nil)
