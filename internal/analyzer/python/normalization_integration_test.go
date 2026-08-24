package python

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/pedrogpaulino/manu/internal/analysis"
	"github.com/pedrogpaulino/manu/internal/bundle"
	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/fact"
	"github.com/pedrogpaulino/manu/internal/normalization"
	"github.com/pedrogpaulino/manu/internal/source"
)

var pythonIntegrationDimensions = map[string]contract.Dimension{
	ArtifactContributionType:      contract.DimensionLandscapeInventoryStructure,
	SymbolContributionType:        contract.DimensionEntitiesAndRelationships,
	ImportContributionType:        contract.DimensionFlowsAndDependencies,
	RelationContributionType:      contract.DimensionFlowsAndDependencies,
	ConfigurationContributionType: contract.DimensionConfigurationVariations,
}

func TestPythonFrappeNormalizationEndToEndIsDeterministicAndConservative(t *testing.T) {
	manifest := pythonManifest()
	registry := pythonRegistry(t, manifest)
	scope := fact.Scope{
		OrganizationID: "organization-python-integration",
		SourceID:       "source-python-integration",
		SnapshotID:     "snapshot-python-frappe17",
	}

	fixtureNames := []string{"doctype.py", "hooks.py"}
	artifacts := make([]contract.Artifact, 0, len(fixtureNames))
	outputs := make([]analysis.Output, 0, len(fixtureNames))
	inputs := make([]normalization.Input, 0, 32)
	allContributions := make([]contract.Contribution, 0, 32)
	seenTypes := make(map[string]int, len(pythonIntegrationDimensions))

	for _, fixtureName := range fixtureNames {
		input, closeRoot := fixtureInput(t, fixtureName, true)
		fixtureArtifact, analyzed := analyzePythonFixture(t, input)
		closeRoot()
		fixtureArtifact.SourceID = scope.SourceID
		fixtureArtifact.ID = contract.ArtifactID(fixtureArtifact.SourceID, fixtureArtifact.Path, fixtureArtifact.Hash)
		artifacts = append(artifacts, fixtureArtifact)
		outputs = append(outputs, analyzed)

		if fixtureName == "doctype.py" && !hasGap(analyzed.Gaps, DynamicRelationGapCode) {
			t.Fatalf("doctype.py gaps = %#v, want dynamic relation gap", analyzed.Gaps)
		}
		if fixtureName == "hooks.py" && !hasGap(analyzed.Gaps, DynamicConfigurationGapCode) {
			t.Fatalf("hooks.py gaps = %#v, want dynamic configuration gap", analyzed.Gaps)
		}

		drafts := draftsByPythonContribution(analyzed.Evidence)
		for _, contribution := range analyzed.Contributions {
			allContributions = append(allContributions, contribution)
			draft, ok := drafts[contribution.ID]
			if !ok {
				t.Fatalf("contribution %q has no evidence draft", contribution.ID)
			}
			if draft.Locator != contribution.Locator {
				t.Fatalf("contribution %q locator = %#v, draft locator = %#v", contribution.ID, contribution.Locator, draft.Locator)
			}
			if _, supported := pythonIntegrationDimensions[contribution.Type]; !supported {
				continue
			}
			seenTypes[contribution.Type]++
			inputs = append(inputs, normalization.Input{
				Scope:        scope,
				Manifest:     clonePythonManifest(manifest),
				Contribution: contribution,
				Evidence: []fact.EvidenceRef{{
					ID: "evidence-" + contribution.ID,
					Locator: contract.Locator{
						SourceID:    scope.SourceID,
						ArtifactID:  contribution.ArtifactID,
						Path:        contribution.Locator.Path,
						Member:      contribution.Locator.Member,
						StartLine:   contribution.Locator.StartLine,
						StartColumn: contribution.Locator.StartColumn,
						EndLine:     contribution.Locator.EndLine,
						EndColumn:   contribution.Locator.EndColumn,
						ByteOffset:  contribution.Locator.ByteOffset,
						ByteLength:  contribution.Locator.ByteLength,
					},
				}},
			})
		}
	}

	for contributionType := range pythonIntegrationDimensions {
		if seenTypes[contributionType] == 0 {
			t.Fatalf("fixtures produced no mapped contribution of type %q", contributionType)
		}
	}
	if len(inputs) == 0 {
		t.Fatal("fixtures produced no supported Python contributions")
	}

	before := clonePythonNormalizationInputs(inputs)
	normalized, err := registry.NormalizeAll(context.Background(), inputs)
	if err != nil {
		t.Fatalf("NormalizeAll() error: %v", err)
	}
	if !reflect.DeepEqual(inputs, before) {
		t.Fatal("NormalizeAll() mutated normalization inputs")
	}
	if len(normalized.Coverage) != len(inputs) {
		t.Fatalf("coverage count = %d, want one per mapped contribution (%d)", len(normalized.Coverage), len(inputs))
	}
	assertPythonIntegrationFacts(t, normalized.Facts, inputs, scope, manifest)

	serializedFacts, err := json.Marshal(normalized.Facts)
	if err != nil {
		t.Fatalf("json.Marshal(facts) error: %v", err)
	}
	for _, forbidden := range []string{"DOCTYPE_NAME", "CONFIG_KEY", "safe_app.events", "Safe title"} {
		if strings.Contains(string(serializedFacts), forbidden) {
			t.Fatalf("normalized facts retained dynamic or configuration value %q: %s", forbidden, serializedFacts)
		}
	}
	serializedContributions, err := json.Marshal(allContributions)
	if err != nil {
		t.Fatalf("json.Marshal(contributions) error: %v", err)
	}
	for _, forbidden := range []string{"DOCTYPE_NAME", "CONFIG_KEY", "safe_app.events", "Safe title"} {
		if strings.Contains(string(serializedContributions), forbidden) {
			t.Fatalf("contributions retained forbidden source value %q: %s", forbidden, serializedContributions)
		}
	}

	reversedInputs := clonePythonNormalizationInputs(inputs)
	for left, right := 0, len(reversedInputs)-1; left < right; left, right = left+1, right-1 {
		reversedInputs[left], reversedInputs[right] = reversedInputs[right], reversedInputs[left]
	}
	reversed, err := registry.NormalizeAll(context.Background(), reversedInputs)
	if err != nil {
		t.Fatalf("NormalizeAll(reversed) error: %v", err)
	}
	if !reflect.DeepEqual(normalized, reversed) {
		t.Fatal("NormalizeAll() changed factual output when inputs were reversed")
	}

	firstDigest := pythonIntegrationFactualDigest(t, outputs, artifacts, allContributions, scope, manifest, normalized)
	reversedDigest := pythonIntegrationFactualDigest(t, outputs, artifacts, allContributions, scope, manifest, reversed)
	if firstDigest == "" || firstDigest != reversedDigest {
		t.Fatalf("FactualDigestV2 is not deterministic: first=%q reversed=%q", firstDigest, reversedDigest)
	}
	repeated, err := registry.NormalizeAll(context.Background(), inputs)
	if err != nil {
		t.Fatalf("NormalizeAll(repeated) error: %v", err)
	}
	repeatedDigest := pythonIntegrationFactualDigest(t, outputs, artifacts, allContributions, scope, manifest, repeated)
	if firstDigest != repeatedDigest {
		t.Fatalf("repeated FactualDigestV2 changed: first=%q repeated=%q", firstDigest, repeatedDigest)
	}
}

func analyzePythonFixture(t *testing.T, input analysis.ArtifactInput) (contract.Artifact, analysis.Output) {
	t.Helper()
	textResult, err := source.ReadTextInRoot(context.Background(), input.RootHandle, input.Artifact.Path, 1<<20)
	if err != nil {
		t.Fatalf("read fixture %q: %v", input.Artifact.Path, err)
	}
	hash := sha256.Sum256([]byte(textResult.Content))
	artifact := input.Artifact
	artifact.SourceID = "source-python-integration"
	artifact.Hash = hex.EncodeToString(hash[:])
	artifact.Size = int64(len([]byte(textResult.Content)))
	artifact.ID = contract.ArtifactID(artifact.SourceID, artifact.Path, artifact.Hash)
	input.SourceID = artifact.SourceID
	input.Artifact = artifact
	analyzed, err := New().Analyze(context.Background(), input)
	if err != nil {
		t.Fatalf("Analyze(%q) error: %v", input.Artifact.Path, err)
	}
	repeated, err := New().Analyze(context.Background(), input)
	if err != nil {
		t.Fatalf("repeated Analyze(%q) error: %v", input.Artifact.Path, err)
	}
	if !reflect.DeepEqual(analyzed, repeated) {
		t.Fatalf("repeated Analyze(%q) changed output", input.Artifact.Path)
	}
	return artifact, analyzed
}

func draftsByPythonContribution(drafts []analysis.EvidenceDraft) map[string]analysis.EvidenceDraft {
	result := make(map[string]analysis.EvidenceDraft, len(drafts))
	for _, draft := range drafts {
		if draft.ContributionID == "" {
			continue
		}
		result[draft.ContributionID] = draft
	}
	return result
}

func assertPythonIntegrationFacts(t *testing.T, facts []fact.CanonicalFact, inputs []normalization.Input, scope fact.Scope, manifest fact.FrontendManifest) {
	t.Helper()
	if len(facts) == 0 {
		t.Fatal("normalization produced no facts")
	}
	inputByArtifact := make(map[string]normalization.Input, len(inputs))
	for _, input := range inputs {
		inputByArtifact[input.Contribution.ArtifactID] = input
	}
	seenPredicates := make(map[fact.Predicate]struct{})
	wantProducer := fact.Producer{ID: manifest.ID, Version: manifest.Version, Method: manifest.Method}
	for _, candidate := range facts {
		if err := candidate.Validate(); err != nil {
			t.Fatalf("fact.Validate() error: %v", err)
		}
		if candidate.Scope != scope || candidate.Producer != wantProducer {
			t.Fatalf("fact provenance = %#v, want scope %#v and producer %#v", candidate, scope, wantProducer)
		}
		if len(candidate.Evidence) == 0 {
			t.Fatalf("fact %q has no evidence", candidate.ID)
		}
		for _, evidence := range candidate.Evidence {
			input, ok := inputByArtifact[evidence.Locator.ArtifactID]
			if !ok {
				// A locator is sufficient to identify the artifact, but the
				// evidence ID also has the contribution identity in this fixture.
				continue
			}
			if evidence.Locator.SourceID != scope.SourceID || evidence.Locator.ArtifactID != input.Contribution.ArtifactID {
				t.Fatalf("fact %q evidence locator = %#v, outside scope/input", candidate.ID, evidence.Locator)
			}
		}
		seenPredicates[candidate.Predicate] = struct{}{}
	}
	for _, predicate := range []fact.Predicate{
		fact.PredicateArtifact,
		fact.PredicateSymbol,
		fact.PredicateDefinition,
		fact.PredicateReference,
		fact.PredicateDependency,
		fact.PredicateConfiguration,
	} {
		if _, ok := seenPredicates[predicate]; !ok {
			t.Fatalf("normalized facts do not contain predicate %q", predicate)
		}
	}
}

func pythonIntegrationFactualDigest(
	t *testing.T,
	outputs []analysis.Output,
	artifacts []contract.Artifact,
	contributions []contract.Contribution,
	scope fact.Scope,
	manifest fact.FrontendManifest,
	normalized normalization.Output,
) string {
	t.Helper()
	gaps := make([]contract.Gap, 0, 8)
	for _, output := range outputs {
		gaps = append(gaps, output.Gaps...)
	}
	result := contract.Result{
		Manifest: contract.Manifest{
			ContractVersion:   contract.Version,
			ResultID:          "result-python-frappe17",
			Source:            contract.Source{ID: scope.SourceID, Name: "frappe17", Type: "repository"},
			Snapshot:          contract.Snapshot{ID: scope.SnapshotID, SourceID: scope.SourceID},
			Execution:         contract.ExecutionMetadata{RunID: "run-python-frappe17"},
			ArtifactCount:     len(artifacts),
			ContributionCount: len(contributions),
			Coverage:          append([]contract.Coverage(nil), normalized.Coverage...),
			Gaps:              gaps,
		},
		Artifacts:     append([]contract.Artifact(nil), artifacts...),
		Contributions: append([]contract.Contribution(nil), contributions...),
	}
	digest, err := bundle.FactualDigestV2(result, nil, []fact.FrontendManifest{manifest}, normalized.Facts, nil)
	if err != nil {
		t.Fatalf("FactualDigestV2() error: %v", err)
	}
	return digest
}

func clonePythonManifest(manifest fact.FrontendManifest) fact.FrontendManifest {
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

func clonePythonNormalizationInputs(inputs []normalization.Input) []normalization.Input {
	clone := make([]normalization.Input, len(inputs))
	for index, input := range inputs {
		clone[index] = input
		clone[index].Manifest = clonePythonManifest(input.Manifest)
		clone[index].Contribution.Value = append([]byte(nil), input.Contribution.Value...)
		clone[index].Evidence = append([]fact.EvidenceRef(nil), input.Evidence...)
	}
	return clone
}
