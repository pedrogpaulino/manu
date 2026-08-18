// Package java implements a conservative lexical Java analyzer. It records
// declarations and directly visible relations with source locators, without
// claiming type resolution, control-flow, or runtime execution semantics.
package java

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/pedrogpaulino/manu/internal/analysis"
	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/source"
)

const (
	AnalyzerID      = "java"
	AnalyzerVersion = "1"
	AnalyzerMethod  = "lexical-java-v1"
)

var (
	packageRE        = regexp.MustCompile(`^\s*package\s+([A-Za-z_$][\w$]*(?:\.[A-Za-z_$][\w$]*)*)\s*;`)
	importRE         = regexp.MustCompile(`^\s*import\s+(static\s+)?([A-Za-z_$][\w$]*(?:\.[A-Za-z_$][\w$*]*)*)\s*;`)
	typeRE           = regexp.MustCompile(`\b(class|interface|enum|record)\s+([A-Za-z_$][\w$]*)\b([^\{;]*)`)
	extendsRE        = regexp.MustCompile(`\bextends\s+([A-Za-z_$][\w$]*(?:\s*<[^>]+>)?(?:\.[A-Za-z_$][\w$]*)*)`)
	implementsRE     = regexp.MustCompile(`\bimplements\s+([^\{]+)`)
	methodRE         = regexp.MustCompile(`(?:^|\s)([A-Za-z_$][\w$]*(?:\s*<[^;{}()]+>)?(?:\[\])?(?:\s+|\s*\*\s*)?)([A-Za-z_$][\w$]*)\s*\(([^;{}()]*)\)\s*(throws\s+([^\{;]+))?\s*(?:\{|;)`)
	throwRE          = regexp.MustCompile(`\bthrow\s+new\s+([A-Za-z_$][\w$]*(?:\.[A-Za-z_$][\w$]*)*)`)
	throwsRE         = regexp.MustCompile(`\bthrows\s+([^\{;]+)`)
	annotationRE     = regexp.MustCompile(`(?:^|\s)@([A-Za-z_$][\w$]*(?:\.[A-Za-z_$][\w$]*)*)`)
	configPropertyRE = regexp.MustCompile(`@(?:[A-Za-z_$][\w$]*\.)*ConfigProperty\s*\([^)]*\bname\s*=\s*["']([^"']+)["']`)
	propertyAccessRE = regexp.MustCompile(`\b(?:System\.(?:getProperty|getenv)|ConfigProvider\.getConfig)\s*\(\s*["']([^"']+)["']`)
	newTypeRE        = regexp.MustCompile(`\bnew\s+([A-Za-z_$][\w$]*(?:\.[A-Za-z_$][\w$]*)*)\s*\(`)
	callRE           = regexp.MustCompile(`\b([A-Za-z_$][\w$]*)\.([A-Za-z_$][\w$]*)\s*\(`)
	identifierRE     = regexp.MustCompile(`^[A-Za-z_$][\w$]*(?:\.[A-Za-z_$][\w$]*)*$`)
)

// Analyzer extracts stable lexical observations from .java artifacts.
type Analyzer struct{}

// New returns a stateless Java analyzer.
func New() *Analyzer { return &Analyzer{} }

// Descriptor describes the intentionally limited Java method.
func (a *Analyzer) Descriptor() analysis.Descriptor {
	return analysis.Descriptor{
		ID:              AnalyzerID,
		Version:         AnalyzerVersion,
		Method:          AnalyzerMethod,
		ContractVersion: contract.Version,
		ArtifactTypes:   []string{analysis.ArtifactTypeJava},
		Capabilities: []string{
			"package",
			"imports",
			"types",
			"methods",
			"annotations",
			"configuration_literals",
			"exceptions",
			"direct_relations",
		},
	}
}

// Analyze performs bounded lexical extraction. It never executes Java or
// resolves symbols outside the current source artifact.
func (a *Analyzer) Analyze(ctx context.Context, input analysis.ArtifactInput) (analysis.Output, error) {
	if err := ctx.Err(); err != nil {
		return analysis.Output{}, err
	}
	text, err := input.Text(ctx, true)
	if err != nil {
		return analysis.Output{
			Coverage: []contract.Coverage{{
				Dimension: string(contract.DimensionEntitiesAndRelationships),
				Scope:     input.Artifact.Path,
				State:     contract.CoverageFailed,
				Message:   "bounded Java source could not be read",
				Locator:   locatorPointer(input),
			}},
			Gaps: []contract.Gap{{
				Code:      "java_read_failed",
				Dimension: string(contract.DimensionEntitiesAndRelationships),
				Scope:     input.Artifact.Path,
				Message:   "lexical extraction was not attempted",
				Locator:   locatorPointer(input),
			}},
		}, nil
	}
	if text.Classification != source.ClassificationText {
		return analysis.Output{
			Coverage: []contract.Coverage{{
				Dimension: string(contract.DimensionEntitiesAndRelationships),
				Scope:     input.Artifact.Path,
				State:     contract.CoverageNotApplicable,
				Message:   "Java artifact was not classified as text",
				Locator:   locatorPointer(input),
			}},
			Gaps: []contract.Gap{{
				Code:      "java_not_text",
				Dimension: string(contract.DimensionEntitiesAndRelationships),
				Scope:     input.Artifact.Path,
				Message:   "Java lexical extraction requires text",
				Locator:   locatorPointer(input),
			}},
		}, nil
	}

	parsed := parse(strings.ReplaceAll(text.Content, "\r\n", "\n"))
	output := analysis.Output{
		Contributions: make([]contract.Contribution, 0, len(parsed.observations)),
		Coverage:      make([]contract.Coverage, 0, 8),
		Gaps:          make([]contract.Gap, 0, 1),
	}
	for _, observation := range parsed.observations {
		contribution, contributionErr := analysis.NewContribution(
			input,
			a.Descriptor(),
			observation.Method,
			observation.Type,
			contract.Locator{
				Path:      input.Artifact.Path,
				StartLine: observation.Line,
				EndLine:   observation.EndLine,
			},
			observation.Value,
		)
		if contributionErr != nil {
			return analysis.Output{}, contributionErr
		}
		output.Contributions = append(output.Contributions, contribution)
	}
	for _, coverage := range parsed.coverage {
		line := 0
		if coverage.Locator != nil {
			line = coverage.Locator.StartLine
		}
		coverage.Locator = locatorPointerAt(input, line)
		output.Coverage = append(output.Coverage, coverage)
	}
	if len(output.Coverage) == 0 {
		output.Coverage = append(output.Coverage, contract.Coverage{
			Dimension: string(contract.DimensionEntitiesAndRelationships),
			Scope:     input.Artifact.Path,
			State:     contract.CoverageIncomplete,
			Message:   "no supported Java lexical declaration was found",
			Locator:   locatorPointer(input),
		})
	}
	output.Gaps = append(output.Gaps, contract.Gap{
		Code:      "java_semantics_not_supported",
		Dimension: string(contract.DimensionFlowsAndDependencies),
		Scope:     input.Artifact.Path,
		Message:   "type resolution, control flow, build configuration, and runtime execution are not reconstructed by the lexical method",
		Locator:   locatorPointer(input),
	})
	return output, nil
}

type observation struct {
	Method  string
	Type    string
	Line    int
	EndLine int
	Value   map[string]any
}

type parsedFile struct {
	observations []observation
	coverage     []contract.Coverage
}

func parse(content string) parsedFile {
	lines := strings.Split(content, "\n")
	parsed := parsedFile{
		observations: make([]observation, 0),
		coverage:     make([]contract.Coverage, 0),
	}
	blockComment := false
	seenCoverage := make(map[string]bool)
	for index, rawLine := range lines {
		lineNumber := index + 1
		line := stripNoise(rawLine, &blockComment)
		literalLine := stripCommentsPreserveLiterals(rawLine)
		line = strings.TrimSpace(line)
		literalLine = strings.TrimSpace(literalLine)
		if line == "" {
			continue
		}
		if match := packageRE.FindStringSubmatch(line); match != nil {
			parsed.observations = append(parsed.observations, observation{
				Method: "package:" + match[1], Type: "java.package", Line: lineNumber, EndLine: lineNumber,
				Value: map[string]any{"name": match[1]},
			})
			addCoverage(&parsed.coverage, seenCoverage, contract.DimensionLandscapeInventoryStructure, contract.CoverageProduced, "package declaration observed", lineNumber)
		}
		if match := importRE.FindStringSubmatch(line); match != nil {
			name := match[2]
			if match[1] != "" {
				name = "static " + name
			}
			parsed.observations = append(parsed.observations, observation{
				Method: "import:" + name, Type: "java.import", Line: lineNumber, EndLine: lineNumber,
				Value: map[string]any{"name": name, "static": match[1] != ""},
			})
			addCoverage(&parsed.coverage, seenCoverage, contract.DimensionEntitiesAndRelationships, contract.CoverageProduced, "import declarations observed", lineNumber)
		}
		for _, match := range typeRE.FindAllStringSubmatch(line, -1) {
			name := match[2]
			kind := match[1]
			parsed.observations = append(parsed.observations, observation{
				Method: "type:" + name, Type: "java.type", Line: lineNumber, EndLine: lineNumber,
				Value: map[string]any{"kind": kind, "name": name},
			})
			addCoverage(&parsed.coverage, seenCoverage, contract.DimensionEntitiesAndRelationships, contract.CoverageProduced, "type declarations observed", lineNumber)
			relationTail := match[3]
			if extends := extendsRE.FindStringSubmatch(relationTail); extends != nil {
				target := strings.TrimSpace(extends[1])
				parsed.observations = append(parsed.observations, observation{
					Method: "relation:" + name + ":extends:" + target, Type: "java.relation", Line: lineNumber, EndLine: lineNumber,
					Value: map[string]any{"from": name, "kind": "extends", "to": target},
				})
				addCoverage(&parsed.coverage, seenCoverage, contract.DimensionEntitiesAndRelationships, contract.CoverageProduced, "direct type relation observed", lineNumber)
			}
			if implements := implementsRE.FindStringSubmatch(relationTail); implements != nil {
				for _, target := range splitTypes(implements[1]) {
					parsed.observations = append(parsed.observations, observation{
						Method: "relation:" + name + ":implements:" + target, Type: "java.relation", Line: lineNumber, EndLine: lineNumber,
						Value: map[string]any{"from": name, "kind": "implements", "to": target},
					})
				}
				addCoverage(&parsed.coverage, seenCoverage, contract.DimensionEntitiesAndRelationships, contract.CoverageProduced, "direct type relation observed", lineNumber)
			}
		}
		for _, match := range methodRE.FindAllStringSubmatch(line, -1) {
			name := strings.TrimSpace(match[2])
			if isControlName(name) {
				continue
			}
			value := map[string]any{"name": name, "parameters": strings.TrimSpace(match[3])}
			if match[5] != "" {
				value["throws"] = splitTypes(match[5])
			}
			parsed.observations = append(parsed.observations, observation{
				Method: "method:" + name + ":" + fmt.Sprint(lineNumber), Type: "java.method", Line: lineNumber, EndLine: lineNumber,
				Value: value,
			})
			addCoverage(&parsed.coverage, seenCoverage, contract.DimensionFlowsAndDependencies, contract.CoverageIncomplete, "method declarations observed lexically", lineNumber)
		}
		for _, match := range annotationRE.FindAllStringSubmatch(line, -1) {
			name := match[1]
			if name == "interface" {
				continue
			}
			parsed.observations = append(parsed.observations, observation{
				Method: "annotation:" + name + ":" + fmt.Sprint(lineNumber), Type: "java.annotation", Line: lineNumber, EndLine: lineNumber,
				Value: map[string]any{"name": name},
			})
			addCoverage(&parsed.coverage, seenCoverage, contract.DimensionConfigurationVariations, contract.CoverageProduced, "annotation observed", lineNumber)
		}
		for _, match := range configPropertyRE.FindAllStringSubmatch(literalLine, -1) {
			if !strings.Contains(line, "ConfigProperty") {
				continue
			}
			parsed.observations = append(parsed.observations, observation{
				Method: "config:" + match[1] + ":" + fmt.Sprint(lineNumber), Type: "java.configuration", Line: lineNumber, EndLine: lineNumber,
				Value: map[string]any{"key": match[1], "kind": "ConfigProperty"},
			})
			addCoverage(&parsed.coverage, seenCoverage, contract.DimensionConfigurationVariations, contract.CoverageProduced, "configuration key literal observed", lineNumber)
		}
		for _, match := range propertyAccessRE.FindAllStringSubmatch(literalLine, -1) {
			if !strings.Contains(line, "System.getProperty") &&
				!strings.Contains(line, "System.getenv") &&
				!strings.Contains(line, "ConfigProvider.getConfig") {
				continue
			}
			parsed.observations = append(parsed.observations, observation{
				Method: "config:" + match[1] + ":" + fmt.Sprint(lineNumber), Type: "java.configuration", Line: lineNumber, EndLine: lineNumber,
				Value: map[string]any{"key": match[1], "kind": "property-access"},
			})
			addCoverage(&parsed.coverage, seenCoverage, contract.DimensionConfigurationVariations, contract.CoverageProduced, "configuration key literal observed", lineNumber)
		}
		for _, match := range throwRE.FindAllStringSubmatch(line, -1) {
			parsed.observations = append(parsed.observations, observation{
				Method: "throws:new:" + match[1] + ":" + fmt.Sprint(lineNumber), Type: "java.exception", Line: lineNumber, EndLine: lineNumber,
				Value: map[string]any{"kind": "throw", "type": match[1]},
			})
			addCoverage(&parsed.coverage, seenCoverage, contract.DimensionErrorsAndPossibleFlows, contract.CoverageProduced, "explicit exception construction observed", lineNumber)
		}
		for _, match := range throwsRE.FindAllStringSubmatch(line, -1) {
			for _, name := range splitTypes(match[1]) {
				parsed.observations = append(parsed.observations, observation{
					Method: "throws:declared:" + name + ":" + fmt.Sprint(lineNumber), Type: "java.exception", Line: lineNumber, EndLine: lineNumber,
					Value: map[string]any{"kind": "throws", "type": name},
				})
			}
			addCoverage(&parsed.coverage, seenCoverage, contract.DimensionErrorsAndPossibleFlows, contract.CoverageProduced, "declared exception observed", lineNumber)
		}
		for _, match := range newTypeRE.FindAllStringSubmatch(line, -1) {
			name := match[1]
			parsed.observations = append(parsed.observations, observation{
				Method: "relation:new:" + name + ":" + fmt.Sprint(lineNumber), Type: "java.relation", Line: lineNumber, EndLine: lineNumber,
				Value: map[string]any{"kind": "constructs", "to": name},
			})
			addCoverage(&parsed.coverage, seenCoverage, contract.DimensionEntitiesAndRelationships, contract.CoverageProduced, "direct constructor relation observed", lineNumber)
		}
		for _, match := range callRE.FindAllStringSubmatch(line, -1) {
			if isControlName(match[2]) || !identifierRE.MatchString(match[1]) {
				continue
			}
			parsed.observations = append(parsed.observations, observation{
				Method: "relation:call:" + match[1] + "." + match[2] + ":" + fmt.Sprint(lineNumber), Type: "java.relation", Line: lineNumber, EndLine: lineNumber,
				Value: map[string]any{"kind": "call", "from": match[1], "to": match[2]},
			})
		}
	}
	sort.SliceStable(parsed.observations, func(i, j int) bool {
		if parsed.observations[i].Line != parsed.observations[j].Line {
			return parsed.observations[i].Line < parsed.observations[j].Line
		}
		return parsed.observations[i].Method < parsed.observations[j].Method
	})
	return parsed
}

func addCoverage(coverage *[]contract.Coverage, seen map[string]bool, dimension contract.Dimension, state contract.CoverageState, message string, line int) {
	key := string(dimension) + "\x00" + string(state)
	if seen[key] {
		return
	}
	seen[key] = true
	*coverage = append(*coverage, contract.Coverage{
		Dimension: string(dimension),
		Scope:     "file",
		State:     state,
		Message:   message,
		Locator:   &contract.Locator{StartLine: line},
	})
}

func splitTypes(value string) []string {
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		part = strings.TrimSuffix(part, "{")
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if index := strings.IndexAny(part, " <"); index >= 0 {
			part = part[:index]
		}
		if identifierRE.MatchString(part) {
			result = append(result, part)
		}
	}
	return result
}

func isControlName(name string) bool {
	switch name {
	case "if", "for", "while", "switch", "catch", "try", "finally", "synchronized", "return", "new":
		return true
	default:
		return false
	}
}

func stripNoise(line string, blockComment *bool) string {
	var builder strings.Builder
	for index := 0; index < len(line); {
		if *blockComment {
			end := strings.Index(line[index:], "*/")
			if end < 0 {
				return builder.String()
			}
			index += end + 2
			*blockComment = false
			continue
		}
		if strings.HasPrefix(line[index:], "//") {
			break
		}
		if strings.HasPrefix(line[index:], "/*") {
			*blockComment = true
			index += 2
			continue
		}
		if line[index] == '"' || line[index] == '\'' {
			quote := line[index]
			builder.WriteByte(' ')
			index++
			for index < len(line) {
				if line[index] == '\\' {
					index += 2
					continue
				}
				if line[index] == quote {
					index++
					break
				}
				builder.WriteByte(' ')
				index++
			}
			continue
		}
		builder.WriteByte(line[index])
		index++
	}
	return builder.String()
}

// stripCommentsPreserveLiterals is used only for configuration key
// extraction. It keeps quoted values so keys remain visible while comments
// are ignored. The analyzer emits only the key metadata, never the literal
// value itself.
func stripCommentsPreserveLiterals(line string) string {
	var builder strings.Builder
	inQuote := byte(0)
	for index := 0; index < len(line); {
		if inQuote != 0 {
			builder.WriteByte(line[index])
			if line[index] == '\\' && index+1 < len(line) {
				index++
				builder.WriteByte(line[index])
				index++
				continue
			}
			if line[index] == inQuote {
				inQuote = 0
			}
			index++
			continue
		}
		if line[index] == '"' || line[index] == '\'' {
			inQuote = line[index]
			builder.WriteByte(line[index])
			index++
			continue
		}
		if strings.HasPrefix(line[index:], "//") || strings.HasPrefix(line[index:], "/*") {
			break
		}
		builder.WriteByte(line[index])
		index++
	}
	return builder.String()
}

func locatorPointer(input analysis.ArtifactInput) *contract.Locator {
	return locatorPointerAt(input, 0)
}

func locatorPointerAt(input analysis.ArtifactInput, line int) *contract.Locator {
	locator := contract.Locator{
		SourceID:   input.SourceID,
		ArtifactID: input.Artifact.ID,
		Path:       input.Artifact.Path,
	}
	if line > 0 {
		locator.StartLine = line
		locator.EndLine = line
	}
	return &locator
}

var _ analysis.Analyzer = (*Analyzer)(nil)
