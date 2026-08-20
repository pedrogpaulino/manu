// Package generic provides the deterministic fallback analyzer. It records
// bounded artifact metadata and text classification without copying complete
// source contents into the common result.
package generic

import (
	"context"
	"fmt"
	"strings"

	"github.com/pedrogpaulino/manu/internal/analysis"
	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/evidence"
	"github.com/pedrogpaulino/manu/internal/source"
)

const (
	AnalyzerID      = "generic"
	AnalyzerVersion = "1"
	AnalyzerMethod  = "inventory-text-v1"
)

// Analyzer is the fallback inventory and bounded-text analyzer.
type Analyzer struct{}

// New returns a stateless generic analyzer.
func New() *Analyzer { return &Analyzer{} }

// Descriptor describes the fallback's broad applicability.
func (a *Analyzer) Descriptor() analysis.Descriptor {
	return analysis.Descriptor{
		ID:              AnalyzerID,
		Version:         AnalyzerVersion,
		Method:          AnalyzerMethod,
		ContractVersion: contract.Version,
		ArtifactTypes:   []string{analysis.ArtifactTypeAny},
		Capabilities: []string{
			"inventory",
			"classification",
			"bounded_text_metadata",
		},
		Fallback: true,
	}
}

// Analyze emits one inventory contribution for every artifact. Text files
// receive an additional bounded metadata observation; binary files retain an
// explicit not-applicable coverage state instead of an unsafe text guess.
func (a *Analyzer) Analyze(ctx context.Context, input analysis.ArtifactInput) (analysis.Output, error) {
	if err := ctx.Err(); err != nil {
		return analysis.Output{}, err
	}
	if input.Artifact.ID == "" || input.Artifact.Path == "" || input.Artifact.Hash == "" {
		return analysis.Output{}, fmt.Errorf("generic: artifact identity is incomplete")
	}
	output := analysis.Output{
		Contributions: []contract.Contribution{},
		Coverage:      []contract.Coverage{},
		Gaps:          []contract.Gap{},
	}
	inventory, err := analysis.NewContribution(
		input,
		a.Descriptor(),
		"inventory:"+input.Artifact.Path,
		"artifact.inventory",
		contract.Locator{Path: input.Artifact.Path},
		map[string]any{
			"path":           input.Artifact.Path,
			"type":           input.Artifact.Type,
			"hash":           input.Artifact.Hash,
			"size":           input.Artifact.Size,
			"classification": input.Artifact.Kind,
		},
	)
	if err != nil {
		return analysis.Output{}, err
	}
	output.Contributions = append(output.Contributions, inventory)
	output.Coverage = append(output.Coverage,
		contract.Coverage{
			Dimension: string(contract.DimensionLandscapeInventoryStructure),
			Scope:     input.Artifact.Path,
			State:     contract.CoverageProduced,
			Message:   "artifact inventory and identity observed",
			Locator:   locatorPointer(input),
		},
	)

	if input.SourceArtifact.Classification == source.ClassificationBinary || input.Artifact.Type == analysis.ArtifactTypeBinary {
		if input.Evidence.Enabled {
			output.Evidence = append(output.Evidence, analysis.EvidenceDraft{
				ContributionID: inventory.ID,
				Locator:        *locatorPointer(input),
				State:          evidence.ContentStateOmitted,
				OriginalHash:   input.Artifact.Hash,
			})
		}
		output.Coverage = append(output.Coverage, contract.Coverage{
			Dimension: string(contract.DimensionDocumentation),
			Scope:     input.Artifact.Path,
			State:     contract.CoverageNotApplicable,
			Message:   "binary artifact has no textual extraction",
			Locator:   locatorPointer(input),
		})
		return output, nil
	}

	var textResult source.TextResult
	var textReadErr error
	if input.Evidence.Enabled {
		textResult, textReadErr = input.EvidenceText(ctx)
	}
	textObservation, err := analysis.NewContribution(
		input,
		a.Descriptor(),
		"text:"+input.Artifact.Path,
		"artifact.text",
		contract.Locator{Path: input.Artifact.Path},
		map[string]any{
			"classification":  string(input.SourceArtifact.Classification),
			"bytes_read":      0,
			"truncated":       false,
			"content_emitted": false,
		},
	)
	if err != nil {
		return analysis.Output{}, err
	}
	output.Contributions = append(output.Contributions, textObservation)
	if input.Evidence.Enabled {
		if textReadErr != nil || textResult.Classification != source.ClassificationText {
			output.Evidence = append(output.Evidence, analysis.EvidenceDraft{
				ContributionID: textObservation.ID,
				Locator:        *locatorPointer(input),
				State:          evidence.ContentStateOmitted,
				OriginalHash:   input.Artifact.Hash,
			})
		} else {
			snippet, startLine, endLine, snippetTruncated := firstTextBlock(textResult.Content)
			output.Evidence = append(output.Evidence, analysis.EvidenceDraft{
				ContributionID: textObservation.ID,
				Locator: contract.Locator{
					SourceID:   input.SourceID,
					ArtifactID: input.Artifact.ID,
					Path:       input.Artifact.Path,
					StartLine:  startLine,
					EndLine:    endLine,
				},
				Content:   snippet,
				Truncated: textResult.Truncated || snippetTruncated,
			})
		}
	}
	state := contract.CoverageProduced
	message := "bounded text metadata observed"
	if input.SourceArtifact.Classification != source.ClassificationText {
		state = contract.CoverageIncomplete
		message = "content classification is conservative and not textual"
	}
	output.Coverage = append(output.Coverage, contract.Coverage{
		Dimension: string(contract.DimensionDocumentation),
		Scope:     input.Artifact.Path,
		State:     state,
		Message:   message,
		Locator:   locatorPointer(input),
	})
	if input.SourceArtifact.Classification != source.ClassificationText {
		output.Gaps = append(output.Gaps, contract.Gap{
			Code:      "text_not_confirmed",
			Dimension: string(contract.DimensionDocumentation),
			Scope:     input.Artifact.Path,
			Message:   "text extraction was not attempted for conservatively classified content",
			Locator:   locatorPointer(input),
		})
	}
	return output, nil
}

// firstTextBlock keeps the first small logical block rather than retaining a
// complete text artifact. Empty leading lines are skipped and line metadata
// remains visible through the evidence locator.
func firstTextBlock(content string) (string, int, int, bool) {
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	start := 0
	for start < len(lines) && strings.TrimSpace(lines[start]) == "" {
		start++
	}
	if start == len(lines) {
		return "", 1, 1, content != ""
	}
	const maxLines = 8
	end := start + maxLines
	if end > len(lines) {
		end = len(lines)
	}
	truncated := end < len(lines)
	block := strings.Join(lines[start:end], "\n")
	return block, start + 1, end, truncated
}

func locatorPointer(input analysis.ArtifactInput) *contract.Locator {
	locator := contract.Locator{
		SourceID:   input.SourceID,
		ArtifactID: input.Artifact.ID,
		Path:       strings.TrimSpace(input.Artifact.Path),
	}
	return &locator
}

var _ analysis.Analyzer = (*Analyzer)(nil)
