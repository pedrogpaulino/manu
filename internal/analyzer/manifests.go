// Package analyzer exposes the production frontend catalog used by callers
// that need the bounded Java, Python, and WSO2 manifests together.
package analyzer

import (
	"fmt"
	"sort"

	"github.com/pedrogpaulino/manu/internal/analyzer/java"
	"github.com/pedrogpaulino/manu/internal/analyzer/python"
	"github.com/pedrogpaulino/manu/internal/analyzer/wso2"
	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/fact"
)

// FrontendManifests returns detached, validated production manifests in their
// canonical catalog order. The catalog contains only the frontend versions
// exercised by the corresponding bounded analyzers; it is not a fallback and
// does not imply semantic completeness.
func FrontendManifests() ([]fact.FrontendManifest, error) {
	manifests := []fact.FrontendManifest{
		java.Manifest(),
		python.Manifest(),
		wso2.Manifest(),
	}

	seenIDs := make(map[string]struct{}, len(manifests))
	result := make([]fact.FrontendManifest, 0, len(manifests))
	for index, manifest := range manifests {
		if err := manifest.Validate(); err != nil {
			return nil, fmt.Errorf("production frontend manifest %d (%q): %w", index, manifest.ID, err)
		}
		if _, exists := seenIDs[manifest.ID]; exists {
			return nil, fmt.Errorf("%w: duplicate production frontend manifest id %q", fact.ErrInvalidManifest, manifest.ID)
		}
		seenIDs[manifest.ID] = struct{}{}
		result = append(result, cloneFrontendManifest(manifest))
	}

	sort.SliceStable(result, func(left, right int) bool {
		return frontendManifestKey(result[left]) < frontendManifestKey(result[right])
	})
	return result, nil
}

func frontendManifestKey(manifest fact.FrontendManifest) string {
	return manifest.ID + "\x00" + manifest.Version + "\x00" + manifest.Method
}

func cloneFrontendManifest(manifest fact.FrontendManifest) fact.FrontendManifest {
	clone := manifest
	clone.SourceTypes = append([]string(nil), manifest.SourceTypes...)
	clone.Families = append([]string(nil), manifest.Families...)
	clone.Versions = append([]string(nil), manifest.Versions...)
	clone.Capabilities = append([]contract.Dimension(nil), manifest.Capabilities...)
	clone.Limitations = append([]string(nil), manifest.Limitations...)
	clone.Predicates = append([]fact.Predicate(nil), manifest.Predicates...)
	clone.Extensions = append([]fact.ExtensionSchema(nil), manifest.Extensions...)
	return clone
}
