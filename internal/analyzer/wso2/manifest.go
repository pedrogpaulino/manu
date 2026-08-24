package wso2

import (
	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/fact"
)

const (
	// ManifestSourceVersion is the bounded declarative corpus identifier used
	// by the WSO2 XML/CAR fixtures. It is not a WSO2 runtime version and must
	// not be inferred from a Composite Application filename.
	ManifestSourceVersion = "wso2-declarative-v1"

	manifestLimitationDeclarativeLexical        = "declarative-xml-and-car-lexical-only"
	manifestLimitationRuntimeUnknown            = "runtime-version-unknown"
	manifestLimitationCompositeVersionNoRuntime = "composite-application-version-not-runtime"
	manifestLimitationNoExecution               = "no-build-or-runtime-execution"
	manifestLimitationNoNetwork                 = "no-network-or-dependency-installation"
)

// Manifest returns the versioned production contract for the bounded WSO2
// frontend. Each call allocates fresh slices so callers can canonicalize or
// adapt the returned value without mutating another selection.
func Manifest() fact.FrontendManifest {
	return fact.FrontendManifest{
		ManifestVersion: fact.FrontendManifestVersion,
		ID:              AnalyzerID,
		Version:         AnalyzerVersion,
		Method:          AnalyzerMethod,
		// SourceTypes identifies the executable source kind consumed by the
		// bundle flow. XML and CAR are artifact types inside that source.
		SourceTypes: []string{"filesystem"},
		Families: []string{
			"wso2",
		},
		// This is the version identifier exercised by the bounded frontend and
		// fixtures, not the runtime version of a WSO2 installation.
		Versions: []string{
			ManifestSourceVersion,
		},
		Capabilities: []contract.Dimension{
			contract.DimensionLandscapeInventoryStructure,
			contract.DimensionEntitiesAndRelationships,
			contract.DimensionFlowsAndDependencies,
			contract.DimensionConfigurationVariations,
		},
		Limitations: []string{
			manifestLimitationDeclarativeLexical,
			manifestLimitationRuntimeUnknown,
			manifestLimitationCompositeVersionNoRuntime,
			manifestLimitationNoExecution,
			manifestLimitationNoNetwork,
		},
		Predicates: []fact.Predicate{
			fact.PredicateNamedElement,
			fact.PredicateMembership,
			fact.PredicateDependency,
			fact.PredicateReference,
			fact.PredicateEndpoint,
			fact.PredicateMessage,
			fact.PredicateConfiguration,
		},
		Execution: fact.ExecutionProfileSafeStatic,
	}
}
