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
	"github.com/pedrogpaulino/manu/internal/evidence"
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
	artifactType := input.Artifact.Type
	if artifactType == "" {
		artifactType = analysis.ArtifactTypeJava
	}
	parsed.observations = append(parsed.observations, observation{
		Method:  "artifact:" + input.Artifact.Path,
		Type:    "java.artifact",
		Line:    1,
		EndLine: 1,
		Value: map[string]any{
			"path": input.Artifact.Path,
			"type": artifactType,
		},
	})
	sort.SliceStable(parsed.observations, func(i, j int) bool {
		if parsed.observations[i].Line != parsed.observations[j].Line {
			return parsed.observations[i].Line < parsed.observations[j].Line
		}
		return parsed.observations[i].Method < parsed.observations[j].Method
	})
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
			sanitizeObservationValue(observation.Value),
		)
		if contributionErr != nil {
			return analysis.Output{}, contributionErr
		}
		output.Contributions = append(output.Contributions, contribution)
		if input.Evidence.Enabled {
			snippet, snippetTruncated := lineSnippet(text.Content, observation.Line, observation.EndLine)
			draft := analysis.EvidenceDraft{
				ContributionID: contribution.ID,
				Locator:        contribution.Locator,
				Content:        snippet,
				Truncated:      text.Truncated || snippetTruncated,
			}
			if snippet == "" {
				draft.State = evidence.ContentStateOmitted
			}
			output.Evidence = append(output.Evidence, draft)
		}
	}
	for _, coverage := range parsed.coverage {
		line := 0
		if coverage.Locator != nil {
			line = coverage.Locator.StartLine
		}
		coverage.Locator = locatorPointerAt(input, line)
		output.Coverage = append(output.Coverage, coverage)
	}
	for _, gap := range parsed.gaps {
		line := 0
		if gap.Locator != nil {
			line = gap.Locator.StartLine
		}
		gap.Locator = locatorPointerAt(input, line)
		output.Gaps = append(output.Gaps, gap)
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

func sanitizeObservationValue(value map[string]any) map[string]any {
	if len(value) == 0 {
		return value
	}
	sanitized := make(map[string]any, len(value))
	for key, raw := range value {
		text, ok := raw.(string)
		if !ok {
			sanitized[key] = raw
			continue
		}
		clean := analysis.SanitizeEvidenceContent(text)
		if clean.Redacted {
			sanitized[key] = "[redacted]"
		} else {
			sanitized[key] = clean.Content
		}
	}
	return sanitized
}

// lineSnippet retains only the source line(s) that support one lexical
// observation. It never copies the complete Java artifact into a draft.
func lineSnippet(content string, startLine, endLine int) (string, bool) {
	if startLine <= 0 {
		return "", false
	}
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	start := startLine - 1
	if start >= len(lines) {
		return "", false
	}
	end := endLine
	if end <= startLine {
		end = startLine
	}
	if end > len(lines) {
		end = len(lines)
	}
	const maxLines = 3
	if end-start > maxLines {
		end = start + maxLines
		return strings.Join(lines[start:end], "\n"), true
	}
	return strings.Join(lines[start:end], "\n"), false
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
	gaps         []contract.Gap
}

const (
	javaEndpointCoverageMessage = "JAX-RS endpoint extraction requires a supported literal path and lexical association"
	javaEndpointGapCode         = "java_endpoint_semantics_not_supported"
	javaEndpointGapMessage      = "endpoint routing, parameter binding, and runtime resolution are not reconstructed by the lexical method"
)

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
	endpoints := parseEndpoints(content)
	parsed.observations = append(parsed.observations, endpoints.observations...)
	if endpoints.produced {
		addCoverage(&parsed.coverage, seenCoverage, contract.DimensionEntitiesAndRelationships, contract.CoverageProduced, "JAX-RS endpoint annotations observed", endpoints.firstLine)
	}
	if endpoints.unresolved {
		addCoverage(&parsed.coverage, seenCoverage, contract.DimensionEntitiesAndRelationships, contract.CoverageIncomplete, javaEndpointCoverageMessage, endpoints.firstLine)
		parsed.gaps = append(parsed.gaps, contract.Gap{
			Code:      javaEndpointGapCode,
			Dimension: string(contract.DimensionEntitiesAndRelationships),
			Scope:     "file",
			Message:   javaEndpointGapMessage,
			Locator:   &contract.Locator{StartLine: endpoints.firstLine, EndLine: endpoints.firstLine},
		})
	}
	sort.SliceStable(parsed.observations, func(i, j int) bool {
		if parsed.observations[i].Line != parsed.observations[j].Line {
			return parsed.observations[i].Line < parsed.observations[j].Line
		}
		return parsed.observations[i].Method < parsed.observations[j].Method
	})
	sort.SliceStable(parsed.gaps, func(i, j int) bool {
		if parsed.gaps[i].Code != parsed.gaps[j].Code {
			return parsed.gaps[i].Code < parsed.gaps[j].Code
		}
		return parsed.gaps[i].Dimension < parsed.gaps[j].Dimension
	})
	return parsed
}

type parsedEndpoints struct {
	observations []observation
	produced     bool
	unresolved   bool
	firstLine    int
}

type endpointAnnotation struct {
	name       string
	path       string
	pathValid  bool
	httpMethod string
	start      int
	end        int
	startLine  int
	endLine    int
}

type endpointClass struct {
	name      string
	start     int
	bodyOpen  int
	bodyClose int
	line      int
	pathIndex int
	pathValid bool
	path      string
}

type endpointMethod struct {
	start       int
	line        int
	classIndex  int
	httpIndexes []int
	pathIndex   int
	pathValid   bool
	path        string
}

var endpointAnnotationRE = regexp.MustCompile(`@([A-Za-z_$][\w$]*(?:\.[A-Za-z_$][\w$]*)*)`)
var endpointMethodRE = regexp.MustCompile(`\b([A-Za-z_$][\w$]*)\s*\([^;{}()]*\)\s*(?:throws\s+[^\{;]+)?(?:\{|;)`)

func parseEndpoints(content string) parsedEndpoints {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	masked := maskJavaSource(content)
	lineStarts := sourceLineStarts(masked)
	lines := strings.Split(masked, "\n")
	braces := matchingBraces(masked)
	annotations := scanEndpointAnnotations(content, masked, lineStarts)
	classes := scanEndpointClasses(masked, lines, lineStarts, braces, annotations)
	methods := scanEndpointMethods(masked, lines, lineStarts, classes, annotations)

	result := parsedEndpoints{
		observations: make([]observation, 0),
		firstLine:    firstEndpointLine(annotations),
	}
	used := make(map[int]bool)

	for methodIndex := range methods {
		method := &methods[methodIndex]
		classPathIndex := -1
		classPath := ""
		if method.classIndex >= 0 && method.classIndex < len(classes) && classes[method.classIndex].pathValid {
			classPathIndex = classes[method.classIndex].pathIndex
			classPath = classes[method.classIndex].path
		}
		paths := make([]string, 0, 2)
		if classPath != "" {
			paths = append(paths, classPath)
		}
		if method.pathValid {
			paths = append(paths, method.path)
		}
		if len(paths) == 0 || (len(method.httpIndexes) == 0 && !method.pathValid) {
			continue
		}
		path := joinEndpointPaths(paths...)
		if path == "" {
			continue
		}
		startLine := method.line
		if classPathIndex >= 0 {
			startLine = minInt(startLine, annotations[classPathIndex].startLine)
		}
		if method.pathIndex >= 0 {
			startLine = minInt(startLine, annotations[method.pathIndex].startLine)
		}
		if len(method.httpIndexes) == 0 {
			result.observations = append(result.observations, endpointObservation(path, "", startLine, method.line))
			if classPathIndex >= 0 {
				used[classPathIndex] = true
			}
			if method.pathIndex >= 0 {
				used[method.pathIndex] = true
			}
			result.produced = true
			continue
		}
		for _, httpIndex := range method.httpIndexes {
			if httpIndex < 0 || httpIndex >= len(annotations) {
				continue
			}
			used[httpIndex] = true
			result.observations = append(result.observations, endpointObservation(path, annotations[httpIndex].httpMethod, startLine, method.line))
		}
		if classPathIndex >= 0 {
			used[classPathIndex] = true
		}
		if method.pathIndex >= 0 {
			used[method.pathIndex] = true
		}
		if len(method.httpIndexes) > 0 {
			result.produced = true
		}
	}

	for classIndex := range classes {
		class := &classes[classIndex]
		if !class.pathValid || class.pathIndex < 0 {
			continue
		}
		if used[class.pathIndex] {
			continue
		}
		classHasEndpoint := false
		for _, method := range methods {
			if method.classIndex == classIndex && (method.pathValid || len(method.httpIndexes) > 0) {
				classHasEndpoint = true
				break
			}
		}
		if classHasEndpoint {
			continue
		}
		used[class.pathIndex] = true
		result.observations = append(result.observations, endpointObservation(class.path, "", annotations[class.pathIndex].startLine, class.line))
		result.produced = true
	}

	for index, annotation := range annotations {
		if !strings.EqualFold(lastQualifiedName(annotation.name), "Path") && annotation.httpMethod == "" {
			continue
		}
		if !used[index] {
			result.unresolved = true
		}
	}
	if result.firstLine == 0 && result.unresolved {
		result.firstLine = 1
	}
	return result
}

func endpointObservation(path, httpMethod string, startLine, endLine int) observation {
	method := "endpoint:" + path
	if httpMethod != "" {
		method += ":" + httpMethod
	}
	method += ":" + fmt.Sprint(startLine) + ":" + fmt.Sprint(endLine)
	value := map[string]any{"path": path}
	if httpMethod != "" {
		value["http_method"] = httpMethod
	}
	return observation{
		Method:  method,
		Type:    "java.endpoint",
		Line:    startLine,
		EndLine: endLine,
		Value:   value,
	}
}

func scanEndpointAnnotations(content, masked string, lineStarts []int) []endpointAnnotation {
	matches := endpointAnnotationRE.FindAllStringSubmatchIndex(masked, -1)
	annotations := make([]endpointAnnotation, 0, len(matches))
	for _, match := range matches {
		name := masked[match[2]:match[3]]
		annotation := endpointAnnotation{
			name:      name,
			start:     match[0],
			end:       match[1],
			startLine: sourceLineAt(lineStarts, match[0]),
			endLine:   sourceLineAt(lineStarts, match[1]-1),
		}
		cursor := match[1]
		for cursor < len(masked) && isJavaSpace(masked[cursor]) {
			cursor++
		}
		if cursor < len(masked) && masked[cursor] == '(' {
			if close := closingParenthesis(masked, cursor); close >= 0 {
				annotation.end = close + 1
				annotation.endLine = sourceLineAt(lineStarts, close)
				if strings.EqualFold(lastQualifiedName(name), "Path") {
					literal, valid := endpointAnnotationLiteral(content[cursor+1 : close])
					annotation.path = joinEndpointPaths(literal)
					annotation.pathValid = valid
				}
			}
		} else if strings.EqualFold(lastQualifiedName(name), "Path") {
			annotation.pathValid = false
		}
		if httpMethod := endpointHTTPMethod(name); httpMethod != "" {
			annotation.httpMethod = httpMethod
		}
		annotations = append(annotations, annotation)
	}
	return annotations
}

func scanEndpointClasses(masked string, lines []string, lineStarts []int, braces map[int]int, annotations []endpointAnnotation) []endpointClass {
	classes := make([]endpointClass, 0)
	for lineIndex, line := range lines {
		matches := typeRE.FindAllStringSubmatchIndex(line, -1)
		for _, match := range matches {
			name := line[match[4]:match[5]]
			start := lineStarts[lineIndex] + match[0]
			bodyOpen := findDeclarationBrace(masked, start, match[1]+lineStarts[lineIndex])
			bodyClose := len(masked)
			if close, ok := braces[bodyOpen]; ok {
				bodyClose = close
			}
			class := endpointClass{
				name:      name,
				start:     start,
				bodyOpen:  bodyOpen,
				bodyClose: bodyClose,
				line:      lineIndex + 1,
				pathIndex: -1,
			}
			if pathIndex, path, valid := attachedPathAnnotation(annotations, start, lineIndex+1, masked); pathIndex >= 0 {
				class.pathIndex = pathIndex
				class.path = path
				class.pathValid = valid
			}
			classes = append(classes, class)
		}
	}
	return classes
}

func scanEndpointMethods(masked string, lines []string, lineStarts []int, classes []endpointClass, annotations []endpointAnnotation) []endpointMethod {
	methods := make([]endpointMethod, 0)
	for lineIndex, line := range lines {
		matches := endpointMethodRE.FindAllStringSubmatchIndex(line, -1)
		for _, match := range matches {
			name := line[match[2]:match[3]]
			if isControlName(name) {
				continue
			}
			start := lineStarts[lineIndex] + match[0]
			classIndex := containingEndpointClass(classes, start)
			method := endpointMethod{
				start:       start,
				line:        lineIndex + 1,
				classIndex:  classIndex,
				httpIndexes: make([]int, 0),
				pathIndex:   -1,
			}
			for index, annotation := range annotations {
				if annotation.end > start || annotation.start < endpointClassBodyStart(classes, classIndex) {
					continue
				}
				if !endpointAnnotationNear(annotation, lineIndex+1, lines) {
					continue
				}
				if annotation.httpMethod != "" {
					method.httpIndexes = append(method.httpIndexes, index)
				}
				if annotation.pathValid && strings.EqualFold(lastQualifiedName(annotation.name), "Path") {
					if method.pathIndex < 0 || annotations[method.pathIndex].end < annotation.end {
						method.pathIndex = index
						method.path = annotation.path
						method.pathValid = true
					}
				}
			}
			sort.Slice(method.httpIndexes, func(i, j int) bool {
				return annotations[method.httpIndexes[i]].start < annotations[method.httpIndexes[j]].start
			})
			methods = append(methods, method)
		}
	}
	return methods
}

func attachedPathAnnotation(annotations []endpointAnnotation, declarationStart, declarationLine int, masked string) (int, string, bool) {
	for index := len(annotations) - 1; index >= 0; index-- {
		annotation := annotations[index]
		if !strings.EqualFold(lastQualifiedName(annotation.name), "Path") || annotation.end > declarationStart {
			continue
		}
		if !endpointAnnotationNear(annotation, declarationLine, strings.Split(masked, "\n")) {
			continue
		}
		return index, annotation.path, annotation.pathValid
	}
	return -1, "", false
}

func endpointAnnotationNear(annotation endpointAnnotation, declarationLine int, lines []string) bool {
	if declarationLine < annotation.endLine || declarationLine-annotation.endLine > 8 {
		return false
	}
	for line := annotation.endLine + 1; line < declarationLine; line++ {
		lineIndex := line - 1
		if lineIndex < 0 || lineIndex >= len(lines) {
			return false
		}
		trimmed := strings.TrimSpace(lines[lineIndex])
		if trimmed == "" || strings.HasPrefix(trimmed, "@") || endpointModifierLine(trimmed) {
			continue
		}
		return false
	}
	return true
}

func endpointModifierLine(line string) bool {
	for _, field := range strings.Fields(line) {
		switch field {
		case "public", "protected", "private", "static", "final", "abstract", "synchronized", "native", "strictfp", "default":
		default:
			return false
		}
	}
	return line != ""
}

func endpointClassBodyStart(classes []endpointClass, index int) int {
	if index < 0 || index >= len(classes) {
		return 0
	}
	return classes[index].bodyOpen
}

func containingEndpointClass(classes []endpointClass, offset int) int {
	best := -1
	for index, class := range classes {
		if offset <= class.bodyOpen || offset >= class.bodyClose {
			continue
		}
		if best < 0 || classes[best].bodyOpen < class.bodyOpen {
			best = index
		}
	}
	return best
}

func findDeclarationBrace(masked string, start, after int) int {
	if after < start {
		after = start
	}
	limit := after + 4096
	if limit > len(masked) {
		limit = len(masked)
	}
	for index := after; index < limit; index++ {
		switch masked[index] {
		case '{':
			return index
		case ';':
			return -1
		}
	}
	return -1
}

func matchingBraces(masked string) map[int]int {
	opening := make([]int, 0)
	matching := make(map[int]int)
	for index := 0; index < len(masked); index++ {
		switch masked[index] {
		case '{':
			opening = append(opening, index)
		case '}':
			if len(opening) == 0 {
				continue
			}
			open := opening[len(opening)-1]
			opening = opening[:len(opening)-1]
			matching[open] = index
		}
	}
	return matching
}

func maskJavaSource(content string) string {
	masked := []byte(content)
	blockComment := false
	for index := 0; index < len(content); {
		if blockComment {
			if index+1 < len(content) && content[index] == '*' && content[index+1] == '/' {
				masked[index], masked[index+1] = ' ', ' '
				index += 2
				blockComment = false
				continue
			}
			if content[index] != '\n' {
				masked[index] = ' '
			}
			index++
			continue
		}
		if index+1 < len(content) && content[index] == '/' && content[index+1] == '/' {
			masked[index], masked[index+1] = ' ', ' '
			index += 2
			for index < len(content) && content[index] != '\n' {
				masked[index] = ' '
				index++
			}
			continue
		}
		if index+1 < len(content) && content[index] == '/' && content[index+1] == '*' {
			masked[index], masked[index+1] = ' ', ' '
			index += 2
			blockComment = true
			continue
		}
		if content[index] == '"' || content[index] == '\'' {
			quote := content[index]
			if quote == '"' && index+2 < len(content) && content[index:index+3] == "\"\"\"" {
				masked[index], masked[index+1], masked[index+2] = ' ', ' ', ' '
				index += 3
				for index < len(content) {
					if index+2 < len(content) && content[index:index+3] == "\"\"\"" {
						masked[index], masked[index+1], masked[index+2] = ' ', ' ', ' '
						index += 3
						break
					}
					if content[index] != '\n' {
						masked[index] = ' '
					}
					index++
				}
				continue
			}
			masked[index] = ' '
			index++
			for index < len(content) {
				if content[index] == '\n' {
					break
				}
				if content[index] == '\\' {
					masked[index] = ' '
					index++
					if index < len(content) && content[index] != '\n' {
						masked[index] = ' '
						index++
					}
					continue
				}
				masked[index] = ' '
				if content[index] == quote {
					index++
					break
				}
				index++
			}
			continue
		}
		index++
	}
	return string(masked)
}

func endpointAnnotationLiteral(argument string) (string, bool) {
	var literals []string
	var code strings.Builder
	for index := 0; index < len(argument); {
		if index+1 < len(argument) && argument[index] == '/' && argument[index+1] == '/' {
			index += 2
			for index < len(argument) && argument[index] != '\n' {
				index++
			}
			continue
		}
		if index+1 < len(argument) && argument[index] == '/' && argument[index+1] == '*' {
			index += 2
			for index+1 < len(argument) && !(argument[index] == '*' && argument[index+1] == '/') {
				index++
			}
			if index+1 < len(argument) {
				index += 2
			}
			continue
		}
		if argument[index] == '"' {
			if index+2 < len(argument) && argument[index:index+3] == "\"\"\"" {
				return "", false
			}
			index++
			var literal strings.Builder
			closed := false
			for index < len(argument) {
				if argument[index] == '\n' || argument[index] == '\r' {
					return "", false
				}
				if argument[index] == '\\' && index+1 < len(argument) {
					literal.WriteByte(argument[index])
					literal.WriteByte(argument[index+1])
					index += 2
					continue
				}
				if argument[index] == '"' {
					index++
					closed = true
					break
				}
				literal.WriteByte(argument[index])
				index++
			}
			if !closed {
				return "", false
			}
			literals = append(literals, literal.String())
			continue
		}
		code.WriteByte(argument[index])
		index++
	}
	if len(literals) != 1 || strings.ContainsAny(literals[0], "\r\n") {
		return "", false
	}
	trimmed := strings.TrimSpace(code.String())
	if trimmed != "" {
		if !strings.HasPrefix(trimmed, "value") {
			return "", false
		}
		remainder := strings.TrimSpace(strings.TrimPrefix(trimmed, "value"))
		if !strings.HasPrefix(remainder, "=") || strings.TrimSpace(strings.TrimPrefix(remainder, "=")) != "" {
			return "", false
		}
	}
	if literals[0] == "" {
		return "/", true
	}
	return joinEndpointPaths(literals[0]), true
}

func joinEndpointPaths(paths ...string) string {
	joined := ""
	for _, path := range paths {
		if path == "" {
			continue
		}
		if joined == "" {
			joined = path
			continue
		}
		joined = strings.TrimRight(joined, "/") + "/" + strings.TrimLeft(path, "/")
	}
	if joined == "" {
		return ""
	}
	if !strings.HasPrefix(joined, "/") {
		joined = "/" + joined
	}
	for strings.Contains(joined, "//") {
		joined = strings.ReplaceAll(joined, "//", "/")
	}
	return joined
}

func endpointHTTPMethod(name string) string {
	switch strings.ToUpper(lastQualifiedName(name)) {
	case "GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS", "HEAD":
		return strings.ToUpper(lastQualifiedName(name))
	default:
		return ""
	}
}

func lastQualifiedName(name string) string {
	if index := strings.LastIndexByte(name, '.'); index >= 0 {
		return name[index+1:]
	}
	return name
}

func sourceLineStarts(content string) []int {
	starts := []int{0}
	for index := 0; index < len(content); index++ {
		if content[index] == '\n' && index+1 < len(content) {
			starts = append(starts, index+1)
		}
	}
	return starts
}

func sourceLineAt(starts []int, offset int) int {
	if offset < 0 {
		return 1
	}
	index := sort.Search(len(starts), func(index int) bool { return starts[index] > offset })
	if index == 0 {
		return 1
	}
	return index
}

func closingParenthesis(masked string, open int) int {
	depth := 0
	for index := open; index < len(masked); index++ {
		switch masked[index] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return index
			}
		}
	}
	return -1
}

func isJavaSpace(value byte) bool {
	switch value {
	case ' ', '\t', '\n', '\r', '\f':
		return true
	default:
		return false
	}
}

func firstEndpointLine(annotations []endpointAnnotation) int {
	first := 0
	for _, annotation := range annotations {
		if !strings.EqualFold(lastQualifiedName(annotation.name), "Path") && annotation.httpMethod == "" {
			continue
		}
		if first == 0 || annotation.startLine < first {
			first = annotation.startLine
		}
	}
	return first
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
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
