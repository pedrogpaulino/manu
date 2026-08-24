package python

import (
	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/fact"
)

// Manifest returns the versioned safe-static contract of the Python/Frappe
// frontend. The declared families and versions are limited to the Python 3
// and Frappe 17 fixtures currently exercised by this repository; recognition
// does not imply semantic completeness for either ecosystem.
func Manifest() fact.FrontendManifest {
	return fact.FrontendManifest{
		ManifestVersion: fact.FrontendManifestVersion,
		ID:              AnalyzerID,
		Version:         AnalyzerVersion,
		Method:          AnalyzerMethod,
		SourceTypes:     []string{"filesystem"},
		Families:        []string{"python", "frappe"},
		Versions:        []string{"python-3", "frappe-17"},
		Capabilities: []contract.Dimension{
			contract.DimensionLandscapeInventoryStructure,
			contract.DimensionEntitiesAndRelationships,
			contract.DimensionFlowsAndDependencies,
			contract.DimensionConfigurationVariations,
		},
		Limitations: []string{
			"lexical-only",
			"no-import-resolution",
			"no-build-or-dependency-installation",
			"no-runtime-execution",
			"no-site-access",
			"no-orm-semantics",
		},
		Predicates: []fact.Predicate{
			fact.PredicateArtifact,
			fact.PredicateSymbol,
			fact.PredicateDefinition,
			fact.PredicateReference,
			fact.PredicateDependency,
			fact.PredicateConfiguration,
		},
		Execution: fact.ExecutionProfileSafeStatic,
	}
}
