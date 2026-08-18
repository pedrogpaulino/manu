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
		output.Coverage = append(output.Coverage, contract.Coverage{
			Dimension: string(contract.DimensionDocumentation),
			Scope:     input.Artifact.Path,
			State:     contract.CoverageNotApplicable,
			Message:   "binary artifact has no textual extraction",
			Locator:   locatorPointer(input),
		})
		return output, nil
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

func locatorPointer(input analysis.ArtifactInput) *contract.Locator {
	locator := contract.Locator{
		SourceID:   input.SourceID,
		ArtifactID: input.Artifact.ID,
		Path:       strings.TrimSpace(input.Artifact.Path),
	}
	return &locator
}

var _ analysis.Analyzer = (*Analyzer)(nil)
