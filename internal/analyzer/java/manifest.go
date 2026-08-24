package java

import (
	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/fact"
)

// Manifest returns the production manifest for the bounded Java/Quarkus
// frontend. Each call allocates fresh declaration slices so callers can
// safely retain and inspect the manifest without sharing mutable state.
//
// The supported family and version labels are limited to the representatives
// exercised by the Quarkus fixture and the first-vertical-slice corpus. The
// lexical method does not claim semantic completeness for either ecosystem.
func Manifest() fact.FrontendManifest {
	return fact.FrontendManifest{
		ManifestVersion: fact.FrontendManifestVersion,
		ID:              AnalyzerID,
		Version:         AnalyzerVersion,
		Method:          AnalyzerMethod,
		SourceTypes:     []string{"filesystem"},
		Families:        []string{"java", "quarkus"},
		Versions:        []string{"java-21", "quarkus-3.26.4"},
		Capabilities: []contract.Dimension{
			contract.DimensionLandscapeInventoryStructure,
			contract.DimensionEntitiesAndRelationships,
			contract.DimensionFlowsAndDependencies,
			contract.DimensionConfigurationVariations,
		},
		Limitations: []string{
			"lexical-only",
			"no-build-resolution",
			"no-runtime-semantics",
		},
		Predicates: []fact.Predicate{
			fact.PredicateArtifact,
			fact.PredicateSymbol,
			fact.PredicateNamedElement,
			fact.PredicateDefinition,
			fact.PredicateReference,
			fact.PredicateCall,
			fact.PredicateDependency,
			fact.PredicateConfiguration,
			fact.PredicateEndpoint,
			fact.PredicateMembership,
		},
		Execution: fact.ExecutionProfileSafeStatic,
	}
}
