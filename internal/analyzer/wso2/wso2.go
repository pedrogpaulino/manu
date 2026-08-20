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
				contribution, contributionErr := analysis.NewContribution(
					input,
					a.Descriptor(),
					methodFor(member, "type", pathName, decoder.InputOffset()),
					"wso2.type",
					locator(input, member, decoder.InputOffset()),
					map[string]any{"name": value.Name.Local, "path": pathName, "member": member},
				)
				if contributionErr != nil {
					return output, contributionErr
				}
				output.Contributions = append(output.Contributions, contribution)
				if input.Evidence.Enabled {
					output.Evidence = append(output.Evidence, analysis.EvidenceDraft{
						ContributionID: contribution.ID,
						Locator:        contribution.Locator,
						Content:        "element " + value.Name.Local,
						OriginalHash:   input.Artifact.Hash,
					})
				}
				addCoverage(&output, seenCoverage, coverage(input, "member:"+member, contract.CoverageProduced, "declarative WSO2 type observed", member))
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
			if literal == "" || !isImportElement(strings.ToLower(stack[len(stack)-1])) {
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
	return strings.Contains(value, "${") || strings.Contains(value, "{{") || strings.Contains(value, "$ctx") || strings.Contains(value, "get-property") || strings.Contains(value, "xpath(") || strings.Contains(value, "synapse")
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
	return contract.Coverage{
		Dimension: string(contract.DimensionEntitiesAndRelationships),
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
