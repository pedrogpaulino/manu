// Package python implements a conservative, lexical Python/Frappe analyzer.
//
// The analyzer deliberately stays inside the safe-static profile: it reads a
// bounded text view, removes comments and strings before looking for source
// constructs, and never imports, builds, executes, or resolves Python code.
package python

import (
	"context"
	"errors"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/pedrogpaulino/manu/internal/analysis"
	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/source"
)

const (
	AnalyzerID      = "python"
	AnalyzerVersion = "1"
	AnalyzerMethod  = "structural-python-v1"

	ArtifactContributionType       = "python.artifact"
	SymbolContributionType         = "python.symbol"
	ImportContributionType         = "python.import"
	RelationContributionType       = "python.relation"
	ConfigurationContributionType  = "python.configuration"
	DynamicRelationGapCode         = "python_dynamic_frappe_target"
	DynamicConfigurationGapCode    = "python_dynamic_configuration_key"
	SourceTruncatedGapCode         = "python_source_truncated"
	NotTextGapCode                 = "python_not_text"
	ReadFailedGapCode              = "python_read_failed"
	SemanticGapCode                = "python_semantics_not_supported"
	DynamicRelationGapMessage      = "a Frappe target was not a safe literal and was not normalized"
	DynamicConfigurationGapMessage = "a configuration key was not a safe literal and was not retained"
	SourceTruncatedGapMessage      = "bounded Python source was truncated before structural analysis completed"
	NotTextGapMessage              = "Python structural extraction requires text classified as textual"
	ReadFailedGapMessage           = "bounded Python source could not be read"
	SemanticGapMessage             = "import resolution, type inference, runtime execution, and Frappe semantics are not supported by safe-static lexical analysis"
	SymbolCoverageMessage          = "Python symbols were observed lexically; scope and type resolution remain incomplete"
	FlowCoverageMessage            = "Python imports and Frappe calls were observed lexically; resolution remains incomplete"
	ConfigurationCoverageMessage   = "Python/Frappe configuration keys were observed lexically; values and runtime semantics are not retained"
	InventoryCoverageMessage       = "Python artifact identity and path were observed"

	maxLiteralValueBytes = 256
)

var (
	importLineRE = regexp.MustCompile(`^\s*import\s+(.+?)\s*$`)
	fromImportRE = regexp.MustCompile(`^\s*from\s+((?:\.+[A-Za-z_][A-Za-z0-9_.]*)|(?:[A-Za-z_][A-Za-z0-9_.]*)|\.+)\s+import\s+(.+?)\s*$`)
	classRE      = regexp.MustCompile(`^\s*class\s+([A-Za-z_][A-Za-z0-9_]*)(.*)$`)
	defRE        = regexp.MustCompile(`^\s*(?:(async)\s+)?def\s+([A-Za-z_][A-Za-z0-9_]*)\b(.*)$`)
	callRE       = regexp.MustCompile(`\bfrappe(?:\.db)?\.(get_doc|get_all|get_value)\s*\(`)
	confCallRE   = regexp.MustCompile(`\bfrappe\.(?:conf\.get|get_conf\s*\(\s*\)\s*\.\s*get)\s*\(`)
	assignmentRE = regexp.MustCompile(`^\s*([A-Za-z_][A-Za-z0-9_]*)\s*=`)
	identifierRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	dottedNameRE = regexp.MustCompile(`^(?:\.*[A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z_][A-Za-z0-9_]*)*|\.+)$`)
)

// Analyzer extracts bounded structural observations from Python artifacts.
type Analyzer struct{}

// New returns a stateless Python analyzer.
func New() *Analyzer { return &Analyzer{} }

// Descriptor declares the deliberately limited safe-static Python method.
func (a *Analyzer) Descriptor() analysis.Descriptor {
	return analysis.Descriptor{
		ID:              AnalyzerID,
		Version:         AnalyzerVersion,
		Method:          AnalyzerMethod,
		ContractVersion: contract.Version,
		ArtifactTypes:   []string{analysis.ArtifactTypePython},
		Capabilities: []string{
			"artifact_inventory",
			"symbols",
			"imports_lexical",
			"frappe_literal_relations",
			"configuration_keys",
			"safe-static",
		},
	}
}

// Analyze performs bounded lexical extraction. It does not execute Python,
// import a module, resolve a type, inspect a Frappe site, or access a network.
func (a *Analyzer) Analyze(ctx context.Context, input analysis.ArtifactInput) (analysis.Output, error) {
	if ctx == nil {
		return analysis.Output{}, errors.New("python: nil context")
	}
	if err := ctx.Err(); err != nil {
		return analysis.Output{}, err
	}

	path := strings.TrimSpace(input.Artifact.Path)
	artifactType := strings.TrimSpace(input.Artifact.Type)
	if artifactType == "" {
		artifactType = analysis.ArtifactTypePython
	}
	output := analysis.Output{
		Contributions: make([]contract.Contribution, 0, 16),
		Coverage:      make([]contract.Coverage, 0, 4),
		Gaps:          make([]contract.Gap, 0, 4),
	}

	artifactObservation := observation{
		method: "artifact:" + path,
		typ:    ArtifactContributionType,
		line:   1,
		end:    1,
		value: map[string]any{
			"path": path,
			"type": artifactType,
		},
	}
	artifact, err := makeContribution(input, a.Descriptor(), artifactObservation)
	if err != nil {
		return analysis.Output{}, err
	}
	output.Contributions = append(output.Contributions, artifact)
	artifactEvidenceIndex := -1
	if input.Evidence.Enabled {
		artifactEvidenceIndex = len(output.Evidence)
		output.Evidence = append(output.Evidence, analysis.EvidenceDraft{
			ContributionID: artifact.ID,
			Locator:        artifact.Locator,
		})
	}

	textResult, err := input.Text(ctx, true)
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return analysis.Output{}, contextErr
		}
		output.Coverage = append(output.Coverage, coverage(input, contract.DimensionLandscapeInventoryStructure, contract.CoverageFailed, ReadFailedGapMessage))
		output.Gaps = append(output.Gaps, gap(input, ReadFailedGapCode, contract.DimensionLandscapeInventoryStructure, ReadFailedGapMessage, 1))
		return output, nil
	}
	if textResult.Classification != source.ClassificationText {
		output.Coverage = append(output.Coverage,
			coverage(input, contract.DimensionLandscapeInventoryStructure, contract.CoverageProduced, InventoryCoverageMessage),
			coverage(input, contract.DimensionEntitiesAndRelationships, contract.CoverageNotApplicable, NotTextGapMessage),
			coverage(input, contract.DimensionFlowsAndDependencies, contract.CoverageNotApplicable, NotTextGapMessage),
			coverage(input, contract.DimensionConfigurationVariations, contract.CoverageNotApplicable, NotTextGapMessage),
		)
		output.Gaps = append(output.Gaps, gap(input, NotTextGapCode, contract.DimensionEntitiesAndRelationships, NotTextGapMessage, 1))
		return output, nil
	}

	content := strings.ReplaceAll(textResult.Content, "\r\n", "\n")
	content = strings.ReplaceAll(content, "\r", "\n")
	if artifactEvidenceIndex >= 0 {
		output.Evidence[artifactEvidenceIndex] = evidenceDraft(artifact, content, artifactObservation)
	}
	parsed := parseSource(content, path)

	for _, item := range parsed.observations {
		if err := ctx.Err(); err != nil {
			return analysis.Output{}, err
		}
		contribution, contributionErr := makeContribution(input, a.Descriptor(), item)
		if contributionErr != nil {
			return analysis.Output{}, contributionErr
		}
		output.Contributions = append(output.Contributions, contribution)
		if input.Evidence.Enabled {
			output.Evidence = append(output.Evidence, evidenceDraft(contribution, content, item))
		}
	}

	output.Coverage = append(output.Coverage,
		coverage(input, contract.DimensionLandscapeInventoryStructure, contract.CoverageProduced, InventoryCoverageMessage),
		coverage(input, contract.DimensionEntitiesAndRelationships, contract.CoverageIncomplete, SymbolCoverageMessage),
		coverage(input, contract.DimensionFlowsAndDependencies, contract.CoverageIncomplete, FlowCoverageMessage),
		coverage(input, contract.DimensionConfigurationVariations, contract.CoverageIncomplete, ConfigurationCoverageMessage),
	)
	output.Gaps = append(output.Gaps, gap(input, SemanticGapCode, contract.DimensionFlowsAndDependencies, SemanticGapMessage, 1))
	if parsed.dynamicRelation {
		output.Gaps = append(output.Gaps, gap(input, DynamicRelationGapCode, contract.DimensionFlowsAndDependencies, DynamicRelationGapMessage, parsed.dynamicRelationLine))
	}
	if parsed.dynamicConfiguration {
		output.Gaps = append(output.Gaps, gap(input, DynamicConfigurationGapCode, contract.DimensionConfigurationVariations, DynamicConfigurationGapMessage, parsed.dynamicConfigurationLine))
	}
	if textResult.Truncated {
		output.Gaps = append(output.Gaps, gap(input, SourceTruncatedGapCode, contract.DimensionEvidenceAndGaps, SourceTruncatedGapMessage, 1))
	}
	sort.SliceStable(output.Gaps, func(i, j int) bool {
		if output.Gaps[i].Code != output.Gaps[j].Code {
			return output.Gaps[i].Code < output.Gaps[j].Code
		}
		return output.Gaps[i].Dimension < output.Gaps[j].Dimension
	})
	return output, nil
}

type observation struct {
	method string
	typ    string
	line   int
	end    int
	value  map[string]any
}

type parsedFile struct {
	observations             []observation
	dynamicRelation          bool
	dynamicRelationLine      int
	dynamicConfiguration     bool
	dynamicConfigurationLine int
}

func makeContribution(input analysis.ArtifactInput, descriptor analysis.Descriptor, item observation) (contract.Contribution, error) {
	start := item.line
	end := item.end
	if start <= 0 {
		start = 1
	}
	if end < start {
		end = start
	}
	return analysis.NewContribution(
		input,
		descriptor,
		item.method,
		item.typ,
		contract.Locator{
			SourceID:   input.SourceID,
			ArtifactID: input.Artifact.ID,
			Path:       input.Artifact.Path,
			StartLine:  start,
			EndLine:    end,
		},
		item.value,
	)
}

func evidenceDraft(contribution contract.Contribution, content string, item observation) analysis.EvidenceDraft {
	if structural := structuralEvidence(item); structural != "" {
		return analysis.EvidenceDraft{
			ContributionID: contribution.ID,
			Locator:        contribution.Locator,
			Content:        structural,
		}
	}
	snippet, truncated := lineSnippet(content, item.line, item.end)
	if len([]byte(snippet)) > 512 {
		var cut int
		for index := range snippet {
			if index > 512 {
				break
			}
			cut = index
		}
		if cut == 0 {
			cut = 512
		}
		snippet = snippet[:cut]
		truncated = true
	}
	sanitized := analysis.SanitizeEvidenceContent(snippet)
	if sanitized.Redacted {
		snippet = sanitized.Content
	}
	draft := analysis.EvidenceDraft{
		ContributionID: contribution.ID,
		Locator:        contribution.Locator,
		Content:        snippet,
		Truncated:      truncated,
	}
	return draft
}

func structuralEvidence(item observation) string {
	switch item.typ {
	case ArtifactContributionType:
		return "artifact path=" + valueText(item.value["path"]) + " type=" + valueText(item.value["type"])
	case SymbolContributionType:
		return "symbol kind=" + valueText(item.value["kind"]) + " name=" + valueText(item.value["name"]) + " qualified_name=" + valueText(item.value["qualified_name"]) + " signature=" + valueText(item.value["signature"])
	case ImportContributionType:
		return "import module=" + valueText(item.value["module"]) + " name=" + valueText(item.value["name"]) + " alias=" + valueText(item.value["alias"])
	case RelationContributionType:
		return "relation callee=" + valueText(item.value["callee"]) + " target=" + valueText(item.value["target"]) + " source_symbol=" + valueText(item.value["source_symbol"])
	case ConfigurationContributionType:
		return "configuration key=" + valueText(item.value["key"]) + " kind=" + valueText(item.value["kind"]) + " path=" + valueText(item.value["path"])
	default:
		return ""
	}
}

func valueText(value any) string {
	text, _ := value.(string)
	return text
}

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

func coverage(input analysis.ArtifactInput, dimension contract.Dimension, state contract.CoverageState, message string) contract.Coverage {
	entry := contract.Coverage{
		Dimension:  string(dimension),
		Scope:      input.Artifact.Path,
		State:      state,
		AnalyzerID: AnalyzerID,
		Message:    message,
		Locator:    locatorPointer(input, 1),
	}
	entry.ID = contract.CoverageID(entry.Dimension, entry.Scope, entry.State, entry.AnalyzerID)
	return entry
}

func gap(input analysis.ArtifactInput, code string, dimension contract.Dimension, message string, line int) contract.Gap {
	entry := contract.Gap{
		Code:       code,
		Dimension:  string(dimension),
		Scope:      input.Artifact.Path,
		Message:    message,
		AnalyzerID: AnalyzerID,
		Locator:    locatorPointer(input, line),
	}
	entry.ID = contract.GapID(entry.Code, entry.Dimension, entry.Scope, entry.Message, entry.AnalyzerID)
	return entry
}

func locatorPointer(input analysis.ArtifactInput, line int) *contract.Locator {
	if line <= 0 {
		line = 1
	}
	return &contract.Locator{
		SourceID:   input.SourceID,
		ArtifactID: input.Artifact.ID,
		Path:       input.Artifact.Path,
		StartLine:  line,
		EndLine:    line,
	}
}

func parseSource(content, path string) parsedFile {
	masked, literals := maskPythonSource(content)
	maskedLines := strings.Split(masked, "\n")
	starts := lineStarts(content)
	parsed := parsedFile{observations: make([]observation, 0, 16)}

	symbols, scopeByLine := parseSymbols(maskedLines)
	parsed.observations = append(parsed.observations, symbols...)
	parsed.observations = append(parsed.observations, parseImports(maskedLines)...)

	for _, match := range callRE.FindAllStringIndex(masked, -1) {
		if !rootFrappeCall(masked, match[0]) {
			continue
		}
		open := strings.LastIndex(masked[match[0]:match[1]], "(")
		if open < 0 {
			continue
		}
		open += match[0] + 1
		callee := strings.TrimSpace(masked[match[0] : open-1])
		if callee == "" {
			continue
		}
		line := lineForOffset(starts, match[0])
		column := match[0] - starts[line-1] + 1
		target, ok := literalCallTarget(masked, literals, open)
		if !ok || strings.TrimSpace(target) == "" {
			if !parsed.dynamicRelation {
				parsed.dynamicRelation = true
				parsed.dynamicRelationLine = line
			}
			continue
		}
		value := map[string]any{
			"kind":   "frappe_call",
			"callee": callee,
			"target": target,
		}
		if sourceSymbol := scopeByLine[line]; sourceSymbol != "" {
			value["source_symbol"] = sourceSymbol
		}
		parsed.observations = append(parsed.observations, observation{
			method: "relation:" + strconv.Itoa(line) + ":" + strconv.Itoa(column) + ":" + callee + ":" + target,
			typ:    RelationContributionType,
			line:   line,
			end:    line,
			value:  value,
		})
	}

	for _, match := range confCallRE.FindAllStringIndex(masked, -1) {
		if !rootFrappeCall(masked, match[0]) {
			continue
		}
		open := strings.LastIndex(masked[match[0]:match[1]], "(")
		if open < 0 {
			continue
		}
		open += match[0] + 1
		callText := strings.TrimSpace(masked[match[0] : open-1])
		kind := "frappe.conf.get"
		if strings.Contains(callText, "get_conf") {
			kind = "frappe.get_conf().get"
		}
		line := lineForOffset(starts, match[0])
		column := match[0] - starts[line-1] + 1
		key, ok := literalCallTarget(masked, literals, open)
		if !ok || strings.TrimSpace(key) == "" {
			if !parsed.dynamicConfiguration {
				parsed.dynamicConfiguration = true
				parsed.dynamicConfigurationLine = line
			}
			continue
		}
		parsed.observations = append(parsed.observations, observation{
			method: "configuration:" + strconv.Itoa(line) + ":" + strconv.Itoa(column) + ":" + kind + ":" + key,
			typ:    ConfigurationContributionType,
			line:   line,
			end:    line,
			value: map[string]any{
				"key":  key,
				"kind": kind,
				"path": path,
			},
		})
	}

	if strings.EqualFold(lastPathPart(path), "hooks.py") {
		parsed.observations = append(parsed.observations, parseHookConfigurations(masked, literals, starts, path)...)
	}

	sort.SliceStable(parsed.observations, func(i, j int) bool {
		left, right := parsed.observations[i], parsed.observations[j]
		if left.line != right.line {
			return left.line < right.line
		}
		if left.typ != right.typ {
			return left.typ < right.typ
		}
		return left.method < right.method
	})
	return parsed
}

func rootFrappeCall(masked string, start int) bool {
	if start <= 0 {
		return true
	}
	previous := masked[start-1]
	return previous != '.' && !isIdentifierByte(previous)
}

func parseImports(lines []string) []observation {
	observations := make([]observation, 0, 8)
	for index, line := range lines {
		lineNumber := index + 1
		if match := importLineRE.FindStringSubmatch(line); match != nil {
			for partIndex, part := range splitCommaList(match[1]) {
				module, alias, ok := parseImportPart(part)
				if !ok {
					continue
				}
				value := map[string]any{"module": module}
				if alias != "" {
					value["alias"] = alias
				}
				observations = append(observations, observation{
					method: "import:" + strconv.Itoa(lineNumber) + ":" + strconv.Itoa(partIndex) + ":" + module + ":" + alias,
					typ:    ImportContributionType,
					line:   lineNumber,
					end:    lineNumber,
					value:  value,
				})
			}
		}
		if match := fromImportRE.FindStringSubmatch(line); match != nil {
			module := strings.TrimSpace(match[1])
			if !dottedNameRE.MatchString(module) {
				continue
			}
			for partIndex, part := range splitCommaList(match[2]) {
				name, alias, ok := parseImportName(part)
				if !ok {
					continue
				}
				value := map[string]any{"module": module, "name": name}
				if alias != "" {
					value["alias"] = alias
				}
				observations = append(observations, observation{
					method: "from-import:" + strconv.Itoa(lineNumber) + ":" + strconv.Itoa(partIndex) + ":" + module + ":" + name + ":" + alias,
					typ:    ImportContributionType,
					line:   lineNumber,
					end:    lineNumber,
					value:  value,
				})
			}
		}
	}
	return observations
}

func parseImportPart(part string) (string, string, bool) {
	fields := strings.Fields(strings.TrimSpace(part))
	if len(fields) == 0 || len(fields) > 3 {
		return "", "", false
	}
	module := fields[0]
	if !dottedNameRE.MatchString(module) {
		return "", "", false
	}
	if len(fields) == 1 {
		return module, "", true
	}
	if len(fields) != 3 || fields[1] != "as" || !identifierRE.MatchString(fields[2]) {
		return "", "", false
	}
	return module, fields[2], true
}

func parseImportName(part string) (string, string, bool) {
	fields := strings.Fields(strings.TrimSpace(part))
	if len(fields) == 0 || len(fields) > 3 || !identifierRE.MatchString(fields[0]) {
		return "", "", false
	}
	if len(fields) == 1 {
		return fields[0], "", true
	}
	if len(fields) != 3 || fields[1] != "as" || !identifierRE.MatchString(fields[2]) {
		return "", "", false
	}
	return fields[0], fields[2], true
}

func splitCommaList(value string) []string {
	parts := make([]string, 0, 4)
	start := 0
	depth := 0
	for index, r := range value {
		switch r {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			if depth > 0 {
				depth--
			}
		case ',':
			if depth == 0 {
				parts = append(parts, value[start:index])
				start = index + 1
			}
		}
	}
	parts = append(parts, value[start:])
	return parts
}

type scopeEntry struct {
	indent    int
	qualified string
}

func parseSymbols(lines []string) ([]observation, map[int]string) {
	observations := make([]observation, 0, 8)
	scopeByLine := make(map[int]string, len(lines))
	stack := make([]scopeEntry, 0, 8)
	for index, line := range lines {
		lineNumber := index + 1
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		indent := indentation(line)
		for len(stack) > 0 && indent <= stack[len(stack)-1].indent {
			stack = stack[:len(stack)-1]
		}
		if len(stack) > 0 {
			scopeByLine[lineNumber] = stack[len(stack)-1].qualified
		}

		if match := classRE.FindStringSubmatch(line); match != nil {
			name := match[1]
			qualified := qualify(stack, name)
			header, headerEnd := declarationHeader(lines, index)
			observations = append(observations, observation{
				method: "symbol:" + qualified + ":" + strconv.Itoa(lineNumber),
				typ:    SymbolContributionType,
				line:   lineNumber,
				end:    headerEnd,
				value: map[string]any{
					"kind":           "class",
					"name":           name,
					"qualified_name": qualified,
					"signature":      headerSignature(header, name, true),
				},
			})
			stack = append(stack, scopeEntry{indent: indent, qualified: qualified})
			scopeByLine[lineNumber] = qualified
			continue
		}
		if match := defRE.FindStringSubmatch(line); match != nil {
			name := match[2]
			qualified := qualify(stack, name)
			header, headerEnd := declarationHeader(lines, index)
			kind := "function"
			if match[1] == "async" {
				kind = "async_function"
			}
			observations = append(observations, observation{
				method: "symbol:" + qualified + ":" + strconv.Itoa(lineNumber),
				typ:    SymbolContributionType,
				line:   lineNumber,
				end:    headerEnd,
				value: map[string]any{
					"kind":           kind,
					"name":           name,
					"qualified_name": qualified,
					"signature":      headerSignature(header, name, false),
				},
			})
			stack = append(stack, scopeEntry{indent: indent, qualified: qualified})
			scopeByLine[lineNumber] = qualified
		}
	}
	return observations, scopeByLine
}

func qualify(stack []scopeEntry, name string) string {
	if len(stack) == 0 {
		return name
	}
	return stack[len(stack)-1].qualified + "." + name
}

func declarationHeader(lines []string, start int) (string, int) {
	var builder strings.Builder
	depth := 0
	end := start
	for index := start; index < len(lines) && index < start+8; index++ {
		line := lines[index]
		for position := 0; position < len(line); position++ {
			switch line[position] {
			case '(', '[', '{':
				depth++
			case ')', ']', '}':
				if depth > 0 {
					depth--
				}
			case ':':
				if depth == 0 {
					if builder.Len() > 0 {
						builder.WriteByte(' ')
					}
					builder.WriteString(line[:position])
					return strings.TrimSpace(builder.String()), index + 1
				}
			}
		}
		if builder.Len() > 0 {
			builder.WriteByte(' ')
		}
		builder.WriteString(line)
		end = index + 1
	}
	return strings.TrimSpace(builder.String()), end
}

func headerSignature(header, name string, class bool) string {
	_ = name
	return declarationSignature(header, class)
}

func declarationSignature(rest string, class bool) string {
	open := strings.Index(rest, "(")
	if open < 0 {
		return ""
	}
	depth := 0
	for index := open; index < len(rest); index++ {
		switch rest[index] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				signature := strings.TrimSpace(rest[open : index+1])
				return compactSignature(signature)
			}
		}
	}
	if class {
		return ""
	}
	return compactSignature(rest[:])
}

func compactSignature(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	value = strings.ReplaceAll(value, "( ", "(")
	value = strings.ReplaceAll(value, " )", ")")
	value = strings.ReplaceAll(value, " ,", ",")
	value = strings.ReplaceAll(value, ", ", ",")
	if len(value) <= 256 {
		return value
	}
	limit := 256
	for limit > 0 && !utf8.ValidString(value[:limit]) {
		limit--
	}
	return value[:limit]
}

func indentation(line string) int {
	columns := 0
	for _, r := range line {
		switch r {
		case ' ':
			columns++
		case '\t':
			columns += 8 - columns%8
		default:
			return columns
		}
	}
	return columns
}

type literal struct {
	start  int
	end    int
	line   int
	value  string
	prefix string
}

// maskPythonSource replaces comments and string contents with spaces while
// preserving byte offsets and newlines. It is intentionally not a Python
// interpreter; malformed or dynamic literals are simply unavailable to the
// structural recognizers.
func maskPythonSource(content string) (string, []literal) {
	masked := []byte(content)
	literals := make([]literal, 0, 16)
	line := 1
	for index := 0; index < len(content); {
		char := content[index]
		if char == '\n' {
			line++
			index++
			continue
		}
		if char == '#' {
			for index < len(content) && content[index] != '\n' {
				masked[index] = ' '
				index++
			}
			continue
		}
		if char != '\'' && char != '"' {
			index++
			continue
		}

		prefixStart := index
		for prefixStart > 0 && isStringPrefix(content[prefixStart-1]) && index-prefixStart < 3 {
			prefixStart--
		}
		prefix := content[prefixStart:index]
		if prefixStart > 0 && isIdentifierByte(content[prefixStart-1]) {
			prefixStart = index
			prefix = ""
		}
		for cursor := prefixStart; cursor < index; cursor++ {
			masked[cursor] = ' '
		}
		quoteLength := 1
		if index+2 < len(content) && content[index+1] == char && content[index+2] == char {
			quoteLength = 3
		}
		start := index
		for cursor := index; cursor < minInt(len(content), index+quoteLength); cursor++ {
			masked[cursor] = ' '
		}
		cursor := index + quoteLength
		closed := false
		for cursor < len(content) {
			if content[cursor] == '\n' {
				if quoteLength == 1 {
					break
				}
				line++
				masked[cursor] = '\n'
				cursor++
				continue
			}
			if content[cursor] == '\\' && !strings.ContainsAny(strings.ToLower(prefix), "r") {
				masked[cursor] = ' '
				if cursor+1 < len(content) {
					if content[cursor+1] == '\n' {
						line++
						masked[cursor+1] = '\n'
					} else {
						masked[cursor+1] = ' '
					}
					cursor += 2
				} else {
					cursor++
				}
				continue
			}
			if cursor+quoteLength <= len(content) && content[cursor:cursor+quoteLength] == strings.Repeat(string(char), quoteLength) {
				for end := cursor; end < cursor+quoteLength; end++ {
					masked[end] = ' '
				}
				cursor += quoteLength
				closed = true
				break
			}
			masked[cursor] = ' '
			cursor++
		}
		if !closed {
			cursor = len(content)
		}
		value, valid := decodePythonLiteral(content[index+quoteLength:minInt(cursor-quoteLength, len(content))], prefix)
		if valid {
			literals = append(literals, literal{start: start, end: cursor, line: line, value: value, prefix: prefix})
		}
		index = cursor
	}
	return string(masked), literals
}

func decodePythonLiteral(value, prefix string) (string, bool) {
	if strings.ContainsAny(strings.ToLower(prefix), "fb") {
		return "", false
	}
	if strings.ContainsAny(strings.ToLower(prefix), "r") {
		return value, safeLiteralValue(value)
	}
	var builder strings.Builder
	for index := 0; index < len(value); index++ {
		if value[index] != '\\' {
			builder.WriteByte(value[index])
			continue
		}
		if index+1 >= len(value) {
			return "", false
		}
		index++
		switch value[index] {
		case '\\', '\'', '"':
			builder.WriteByte(value[index])
		case 'n':
			builder.WriteByte('\n')
		case 'r':
			builder.WriteByte('\r')
		case 't':
			builder.WriteByte('\t')
		default:
			return "", false
		}
	}
	result := builder.String()
	return result, safeLiteralValue(result)
}

func literalCallTarget(masked string, literals []literal, open int) (string, bool) {
	index := open
	for index < len(masked) {
		// String contents are masked as spaces, so inspect the literal index
		// before consuming whitespace from the masked view.
		if item, ok := literalAt(literals, index); ok {
			return item.value, safeLiteralValue(item.value)
		}
		if !isPythonWhitespace(masked[index]) {
			break
		}
		index++
	}
	if target, ok := keywordLiteralTarget(masked, literals, index); ok {
		return target, true
	}
	if index >= len(masked) || masked[index] != '{' {
		return "", false
	}
	end := matchingDelimiter(masked, index, '{', '}')
	if end <= index {
		return "", false
	}
	for _, key := range literals {
		if key.start <= index || key.start >= end || key.value != "doctype" {
			continue
		}
		colon := key.end
		for colon < end && isPythonWhitespace(masked[colon]) {
			colon++
		}
		if colon >= end || masked[colon] != ':' {
			continue
		}
		colon++
		for colon < end && isPythonWhitespace(masked[colon]) {
			colon++
		}
		if value, ok := literalAt(literals, colon); ok && value.start == colon {
			return value.value, safeLiteralValue(value.value)
		}
	}
	return "", false
}

func keywordLiteralTarget(masked string, literals []literal, start int) (string, bool) {
	end := matchingDelimiter(masked, start-1, '(', ')')
	if end < start {
		end = len(masked)
	}
	for _, item := range literals {
		if item.start < start || item.start >= end {
			continue
		}
		left := item.start - 1
		for left >= start && isPythonWhitespace(masked[left]) {
			left--
		}
		if left < start || masked[left] != '=' {
			continue
		}
		left--
		for left >= start && isPythonWhitespace(masked[left]) {
			left--
		}
		nameEnd := left + 1
		for left >= start && isIdentifierByte(masked[left]) {
			left--
		}
		if strings.TrimSpace(masked[left+1:nameEnd]) == "doctype" {
			return item.value, safeLiteralValue(item.value)
		}
	}
	return "", false
}

func safeLiteralValue(value string) bool {
	if value == "" || len([]byte(value)) > maxLiteralValueBytes || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if character == '\n' || character == '\r' || character == '\x00' || character < 0x20 {
			return false
		}
	}
	return true
}

func matchingDelimiter(value string, start int, opening, closing byte) int {
	depth := 0
	for index := start; index < len(value); index++ {
		switch value[index] {
		case opening:
			depth++
		case closing:
			depth--
			if depth == 0 {
				return index
			}
		}
	}
	return -1
}

func literalAt(literals []literal, offset int) (literal, bool) {
	for _, item := range literals {
		if item.start == offset {
			return item, true
		}
	}
	return literal{}, false
}

func parseHookConfigurations(masked string, literals []literal, starts []int, path string) []observation {
	observations := make([]observation, 0, 8)
	lines := strings.Split(masked, "\n")
	for index, line := range lines {
		lineNumber := index + 1
		if match := assignmentRE.FindStringSubmatch(line); match != nil {
			key := match[1]
			observations = append(observations, observation{
				method: "configuration:" + strconv.Itoa(lineNumber) + ":hooks-assignment:" + key,
				typ:    ConfigurationContributionType,
				line:   lineNumber,
				end:    lineNumber,
				value: map[string]any{
					"key":  key,
					"kind": "hooks_assignment",
					"path": path,
				},
			})
		}
		lineStart := starts[index]
		lineEnd := lineStart + len(line)
		for _, item := range literals {
			if item.start < lineStart || item.start >= lineEnd || item.value == "" {
				continue
			}
			left := item.start - 1
			for left >= 0 && isPythonWhitespace(masked[left]) {
				left--
			}
			right := item.end
			for right < len(masked) && isPythonWhitespace(masked[right]) {
				right++
			}
			if left < 0 || right >= len(masked) || masked[left] != '{' && masked[left] != ',' || masked[right] != ':' {
				continue
			}
			observations = append(observations, observation{
				method: "configuration:" + strconv.Itoa(lineNumber) + ":hooks-dict-key:" + strconv.Itoa(item.start) + ":" + item.value,
				typ:    ConfigurationContributionType,
				line:   lineNumber,
				end:    lineNumber,
				value: map[string]any{
					"key":  item.value,
					"kind": "hooks_dict_key",
					"path": path,
				},
			})
		}
	}
	return observations
}

func lineStarts(content string) []int {
	starts := []int{0}
	for index, char := range content {
		if char == '\n' {
			starts = append(starts, index+1)
		}
	}
	return starts
}

func lineForOffset(starts []int, offset int) int {
	if len(starts) == 0 {
		return 1
	}
	index := sort.Search(len(starts), func(index int) bool { return starts[index] > offset })
	if index == 0 {
		return 1
	}
	return index
}

func lastPathPart(path string) string {
	path = strings.TrimRight(strings.ReplaceAll(path, "\\", "/"), "/")
	if slash := strings.LastIndexByte(path, '/'); slash >= 0 {
		return path[slash+1:]
	}
	return path
}

func isStringPrefix(value byte) bool {
	return strings.ContainsRune("rRuUbBfF", rune(value))
}

func isIdentifierByte(value byte) bool {
	return value == '_' || value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9'
}

func isPythonWhitespace(value byte) bool {
	return value == ' ' || value == '\t' || value == '\n' || value == '\r' || value == '\f'
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}

var _ analysis.Analyzer = (*Analyzer)(nil)
