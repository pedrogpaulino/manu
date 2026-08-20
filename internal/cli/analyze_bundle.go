package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/pedrogpaulino/manu/internal/analysis"
	"github.com/pedrogpaulino/manu/internal/bundle"
	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/evidence"
)

const (
	analyzeOutputLegacy = "legacy"
	analyzeOutputBundle = "bundle"

	// The first public bundle-producing profile is intentionally explicit. A
	// future profile can change this identifier without making bundles from
	// different analysis configurations look idempotent.
	defaultAnalyzeConfigurationID = "manu-analyze-v1"
)

func analyzeOutputMode(value string) (string, error) {
	switch mode := strings.ToLower(strings.TrimSpace(value)); mode {
	case "", analyzeOutputLegacy:
		return analyzeOutputLegacy, nil
	case analyzeOutputBundle:
		return analyzeOutputBundle, nil
	default:
		return "", fmt.Errorf("invalid output mode %q", value)
	}
}

// writeAnalysisBundle adapts the evidence-aware analysis result to the
// versioned bundle codec. The source root is an Agent-local authority and is
// deliberately removed from the portable bundle envelope; artifacts and
// locators remain relative and retain their stable identities.
func writeAnalysisBundle(ctx context.Context, directory string, input analysis.AnalysisResult, organizationID string) error {
	output, err := analysisBundle(input, organizationID)
	if err != nil {
		return err
	}
	return bundle.WriteBundle(ctx, directory, output)
}

func analysisBundle(input analysis.AnalysisResult, organizationID string) (bundle.Bundle, error) {
	if err := input.Validate(); err != nil {
		return bundle.Bundle{}, fmt.Errorf("validating analysis result for bundle: %w", err)
	}
	organizationID = strings.TrimSpace(organizationID)
	if organizationID == "" {
		return bundle.Bundle{}, fmt.Errorf("bundle organization id is required")
	}
	for _, unit := range input.Evidence {
		if unit.OrganizationID != organizationID {
			return bundle.Bundle{}, fmt.Errorf("bundle evidence organization does not match requested organization")
		}
	}

	result := input.Result
	result.Manifest.Source.Root = ""
	configurationID := strings.TrimSpace(result.Manifest.Execution.ConfigurationID)
	if configurationID == "" {
		configurationID = defaultAnalyzeConfigurationID
		result.Manifest.Execution.ConfigurationID = configurationID
	}
	snapshot := result.Manifest.Snapshot
	manifest := bundle.Manifest{
		Version:      bundle.Version,
		Organization: bundle.Organization{ID: organizationID},
		Manifest:     result.Manifest,
		Analysis: bundle.Analysis{
			ID:              result.Manifest.ResultID,
			ConfigurationID: configurationID,
			Revision:        snapshot.Revision,
			Hash:            snapshot.Hash,
		},
		Limits: bundle.Limits{
			MaxBundleBytes:   bundle.DefaultMaxBundleBytes,
			MaxManifestBytes: bundle.DefaultMaxManifestBytes,
			MaxEvidenceBytes: bundle.DefaultMaxEvidenceBytes,
			MaxArtifacts:     bundle.DefaultMaxArtifacts,
			MaxContributions: bundle.DefaultMaxContributions,
			MaxEvidenceUnits: bundle.DefaultMaxEvidenceUnits,
		},
		Evidence: bundle.EvidenceMetadata{State: bundle.EvidenceStateAvailable},
	}
	return bundle.Bundle{
		Manifest:      manifest,
		Artifacts:     append([]contract.Artifact(nil), result.Artifacts...),
		Contributions: append([]contract.Contribution(nil), result.Contributions...),
		Evidence:      append([]evidence.EvidenceUnit(nil), input.Evidence...),
	}, nil
}
