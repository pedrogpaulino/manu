package wso2

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
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

var wso2IntegrationDimensions = map[string]contract.Dimension{
	wso2TypeContribution:          contract.DimensionEntitiesAndRelationships,
	wso2IncludeContribution:       contract.DimensionFlowsAndDependencies,
	wso2ReferenceContribution:     contract.DimensionFlowsAndDependencies,
	wso2EndpointContribution:      contract.DimensionEntitiesAndRelationships,
	wso2MessageContribution:       contract.DimensionFlowsAndDependencies,
	wso2ConfigurationContribution: contract.DimensionConfigurationVariations,
}

func TestWSO2NormalizationEndToEndCARMemberCorrelation(t *testing.T) {
	archiveBytes := makeCAR(t, map[string][]byte{
		"synapse/api-v1.xml":    readFixture(t, "testdata/api-v1.xml"),
		"synapse/shared-v1.xml": readFixture(t, "testdata/shared-v1.xml"),
	})
	rootPath := t.TempDir()
	if err := os.WriteFile(filepath.Join(rootPath, "fixture.car"), archiveBytes, 0o600); err != nil {
		t.Fatalf("write CAR fixture: %v", err)
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatalf("open fixture root: %v", err)
	}
	defer root.Close()

	hash := sha256.Sum256(archiveBytes)
	artifact := contract.Artifact{
		SourceID: "wso2-integration-source",
		Path:     "fixture.car",
		Type:     analysis.ArtifactTypeCAR,
		Hash:     hex.EncodeToString(hash[:]),
		Size:     int64(len(archiveBytes)),
	}
	artifact.ID = contract.ArtifactID(artifact.SourceID, artifact.Path, artifact.Hash)
	analyzerInput := analysis.ArtifactInput{
		SourceID:   artifact.SourceID,
		RootHandle: root,
		Artifact:   artifact,
		Limits: source.Limits{
			MaxArchiveMembers:         16,
			MaxArchiveBytes:           1 << 20,
			MaxArchiveMemberBytes:     1 << 20,
			MaxArchiveCompressedBytes: 1 << 20,
			MaxExpansionRatio:         100,
			MaxExtractionBytes:        1 << 20,
		},
		Evidence: analysis.EvidenceInput{
			Enabled: true,
			Limits:  analysis.DefaultEvidenceLimits(),
		},
	}

	analyzed, err := New().Analyze(context.Background(), analyzerInput)
	if err != nil {
		t.Fatalf("Analyze() error: %v", err)
	}
	repeatedAnalysis, err := New().Analyze(context.Background(), analyzerInput)
	if err != nil {
		t.Fatalf("repeated Analyze() error: %v", err)
	}
	if !reflect.DeepEqual(analyzed, repeatedAnalysis) {
		t.Fatal("repeated Analyze() changed contributions, coverage, gaps, or evidence drafts")
	}

	if !hasGap(analyzed.Gaps, dynamicConfigurationGapCode) && !hasGap(analyzed.Gaps, "dynamic_reference") {
		t.Fatalf("CAR fixture gaps = %#v, want a dynamic metadata gap", analyzed.Gaps)
	}
	manifest := wso2TestManifest()
	registrations, err := NormalizerRegistrations(manifest)
	if err != nil {
		t.Fatalf("NormalizerRegistrations() error: %v", err)
	}
	registry, err := normalization.NewRegistry(registrations...)
	if err != nil {
		t.Fatalf("NewRegistry() error: %v", err)
	}

	scope := fact.Scope{
		OrganizationID: "organization-wso2-integration",
		SourceID:       artifact.SourceID,
		SnapshotID:     "snapshot-wso2-integration",
	}
	inputs, contributionsByID, evidenceLocators := wso2IntegrationInputs(t, analyzed, scope, manifest)
	if len(inputs) == 0 {
		t.Fatal("Analyze() produced no mapped WSO2 contributions")
	}

	beforeInputs := cloneWSO2IntegrationInputs(inputs)
	normalized, err := registry.NormalizeAll(context.Background(), inputs)
	if err != nil {
		t.Fatalf("NormalizeAll() error: %v", err)
	}
	if !reflect.DeepEqual(inputs, beforeInputs) {
		t.Fatal("NormalizeAll() mutated its normalization inputs")
	}
	assertWSO2IntegrationOutput(t, normalized, inputs, scope, manifest, evidenceLocators)
	assertWSO2MemberIncludeCorrelation(t, normalized, inputs, contributionsByID, artifact.ID)

	serializedFacts, err := json.Marshal(normalized.Facts)
	if err != nil {
		t.Fatalf("marshal normalized facts: %v", err)
	}
	for _, forbidden := range []string{
		"user:pass",
		"tenant=fixture",
		"#fragment",
		"secret-value",
		"[redacted]",
		"${ctx.",
	} {
		if strings.Contains(string(serializedFacts), forbidden) {
			t.Fatalf("normalized facts retained forbidden material %q: %s", forbidden, serializedFacts)
		}
	}

	reverseInputs := cloneWSO2IntegrationInputs(inputs)
	for left, right := 0, len(reverseInputs)-1; left < right; left, right = left+1, right-1 {
		reverseInputs[left], reverseInputs[right] = reverseInputs[right], reverseInputs[left]
	}
	reversed, err := registry.NormalizeAll(context.Background(), reverseInputs)
	if err != nil {
		t.Fatalf("NormalizeAll(reversed) error: %v", err)
	}
	if !reflect.DeepEqual(normalized, reversed) {
		t.Fatal("NormalizeAll() changed its factual output when inputs were reversed")
	}
	repeated, err := registry.NormalizeAll(context.Background(), inputs)
	if err != nil {
		t.Fatalf("NormalizeAll(repeated) error: %v", err)
	}
	if !reflect.DeepEqual(normalized, repeated) {
		t.Fatal("repeated NormalizeAll() changed its factual output")
	}

	firstDigest := wso2IntegrationFactualDigest(t, analyzed, artifact, scope, manifest, normalized)
	reversedDigest := wso2IntegrationFactualDigest(t, analyzed, artifact, scope, manifest, reversed)
	repeatedDigest := wso2IntegrationFactualDigest(t, analyzed, artifact, scope, manifest, repeated)
	if firstDigest == "" || firstDigest != reversedDigest || firstDigest != repeatedDigest {
		t.Fatalf("FactualDigestV2 is not deterministic: first=%q reversed=%q repeated=%q", firstDigest, reversedDigest, repeatedDigest)
	}
}

func wso2IntegrationInputs(
	t *testing.T,
	analyzed analysis.Output,
	scope fact.Scope,
	manifest fact.FrontendManifest,
) ([]normalization.Input, map[string]contract.Contribution, map[string]contract.Locator) {
	t.Helper()
	draftsByContribution := make(map[string][]analysis.EvidenceDraft)
	for _, draft := range analyzed.Evidence {
		if draft.ContributionID == "" {
			t.Fatal("evidence draft has no contribution identity")
		}
		draftsByContribution[draft.ContributionID] = append(draftsByContribution[draft.ContributionID], draft)
	}
	contributionsByID := make(map[string]contract.Contribution, len(analyzed.Contributions))
	evidenceLocators := make(map[string]contract.Locator, len(analyzed.Contributions))
	inputs := make([]normalization.Input, 0, len(analyzed.Contributions))
	seenTypes := make(map[string]int, len(wso2IntegrationDimensions))
	for _, contribution := range analyzed.Contributions {
		if _, duplicate := contributionsByID[contribution.ID]; duplicate {
			t.Fatalf("Analyze() emitted duplicate contribution ID %q", contribution.ID)
		}
		contributionsByID[contribution.ID] = contribution
		drafts := draftsByContribution[contribution.ID]
		if len(drafts) != 1 {
			t.Fatalf("contribution %q has %d evidence drafts, want exactly one", contribution.ID, len(drafts))
		}
		if drafts[0].Locator != contribution.Locator {
			t.Fatalf("contribution %q locator = %#v, draft locator = %#v", contribution.ID, contribution.Locator, drafts[0].Locator)
		}
		input := normalization.Input{
			Scope:        scope,
			Manifest:     cloneWSO2IntegrationManifest(manifest),
			Contribution: contribution,
			Evidence: []fact.EvidenceRef{{
				ID:      "evidence-" + contribution.ID,
				Locator: drafts[0].Locator,
			}},
		}
		if err := input.Evidence[0].Validate(scope); err != nil {
			t.Fatalf("evidence for contribution %q is invalid: %v", contribution.ID, err)
		}
		if _, mapped := wso2IntegrationDimensions[contribution.Type]; !mapped {
			t.Fatalf("unexpected unmapped contribution type %q", contribution.Type)
		}
		inputs = append(inputs, input)
		seenTypes[contribution.Type]++
		evidenceLocators[input.Evidence[0].ID] = contribution.Locator
	}
	for contributionID, drafts := range draftsByContribution {
		if _, exists := contributionsByID[contributionID]; !exists {
			t.Fatalf("evidence draft %q has no matching contribution", contributionID)
		}
		if len(drafts) != 1 {
			t.Fatalf("evidence draft association for %q is ambiguous", contributionID)
		}
	}
	for contributionType := range wso2IntegrationDimensions {
		if seenTypes[contributionType] == 0 {
			t.Fatalf("fixture produced no mapped contribution of type %q", contributionType)
		}
	}
	return inputs, contributionsByID, evidenceLocators
}

func assertWSO2IntegrationOutput(
	t *testing.T,
	output normalization.Output,
	inputs []normalization.Input,
	scope fact.Scope,
	manifest fact.FrontendManifest,
	evidenceLocators map[string]contract.Locator,
) {
	t.Helper()
	if len(output.Coverage) != len(inputs) {
		t.Fatalf("normalized coverage count = %d, want one per mapped contribution (%d)", len(output.Coverage), len(inputs))
	}
	seenCoverage := make(map[string]struct{}, len(output.Coverage))
	contributionTypes := make(map[string]string, len(inputs))
	for _, input := range inputs {
		contributionTypes[input.Contribution.ID] = input.Contribution.Type
	}
	for _, coverage := range output.Coverage {
		contributionType, exists := contributionTypes[coverage.Scope]
		if !exists {
			t.Fatalf("coverage scope %q does not identify an input contribution", coverage.Scope)
		}
		if expected := string(wso2IntegrationDimensions[contributionType]); coverage.Dimension != expected {
			t.Fatalf("coverage for %q = %#v, want dimension %q", coverage.Scope, coverage, expected)
		}
		if coverage.AnalyzerID != manifest.ID || coverage.Scope == "" || coverage.ID != contract.CoverageID(coverage.Dimension, coverage.Scope, coverage.State, coverage.AnalyzerID) {
			t.Fatalf("coverage = %#v, want deterministic WSO2 coverage", coverage)
		}
		if err := coverage.Validate(); err != nil {
			t.Fatalf("coverage.Validate() error: %v", err)
		}
		if _, duplicate := seenCoverage[coverage.Scope]; duplicate {
			t.Fatalf("duplicate coverage for contribution %q", coverage.Scope)
		}
		seenCoverage[coverage.Scope] = struct{}{}
	}

	wantProducer := fact.Producer{ID: manifest.ID, Version: manifest.Version, Method: manifest.Method}
	seenPredicates := make(map[fact.Predicate]struct{})
	if len(output.Facts) == 0 {
		t.Fatal("normalization produced no facts for the CAR fixture")
	}
	for _, candidate := range output.Facts {
		if err := candidate.Validate(); err != nil {
			t.Fatalf("fact.Validate() error = %v, fact = %#v", err, candidate)
		}
		if candidate.Scope != scope || candidate.Producer != wantProducer {
			t.Fatalf("fact provenance = %#v, want scope %#v and producer %#v", candidate, scope, wantProducer)
		}
		if len(candidate.Evidence) == 0 {
			t.Fatalf("fact %q has no evidence reference", candidate.ID)
		}
		for _, evidence := range candidate.Evidence {
			locator, exists := evidenceLocators[evidence.ID]
			if !exists {
				t.Fatalf("fact %q references unknown evidence %q", candidate.ID, evidence.ID)
			}
			if evidence.Locator != locator || evidence.Locator.Member == "" {
				t.Fatalf("fact %q evidence locator = %#v, want exact CAR member locator %#v", candidate.ID, evidence.Locator, locator)
			}
		}
		seenPredicates[candidate.Predicate] = struct{}{}
	}
	for _, predicate := range []fact.Predicate{
		fact.PredicateNamedElement,
		fact.PredicateMembership,
		fact.PredicateEndpoint,
		fact.PredicateMessage,
		fact.PredicateConfiguration,
		fact.PredicateReference,
		fact.PredicateDependency,
	} {
		if _, exists := seenPredicates[predicate]; !exists {
			t.Fatalf("normalized CAR facts do not contain predicate %q", predicate)
		}
	}
}

func assertWSO2MemberIncludeCorrelation(
	t *testing.T,
	output normalization.Output,
	inputs []normalization.Input,
	contributionsByID map[string]contract.Contribution,
	artifactID string,
) {
	t.Helper()
	memberContainers := map[string]fact.Participant{}
	for _, member := range []string{"synapse/api-v1.xml", "synapse/shared-v1.xml"} {
		memberContainers[member] = fact.Participant{
			Kind: fact.ParticipantNamedElement,
			ID:   wso2Identity("member", artifactID, member),
		}
	}
	seenMembership := make(map[string]bool)
	for _, candidate := range output.Facts {
		if candidate.Predicate != fact.PredicateMembership || candidate.Subject.Kind != fact.ParticipantNamedElement {
			continue
		}
		for member, container := range memberContainers {
			if candidate.Subject == container {
				seenMembership[member] = true
			}
		}
	}
	for member := range memberContainers {
		if !seenMembership[member] {
			t.Fatalf("no Membership fact was produced for CAR member %q", member)
		}
	}

	includeTargets := make(map[string]bool)
	for _, candidate := range output.Facts {
		if candidate.Predicate != fact.PredicateDependency || candidate.Object == nil {
			continue
		}
		target := wso2IntegrationQualifier(candidate, "target")
		if target == "synapse/shared-v1.xml" || target == "synapse/api-v1.xml" {
			includeTargets[target] = true
			want := memberContainers[target]
			if candidate.Object.Kind != want.Kind || candidate.Object.ID != want.ID {
				t.Fatalf("include target %q = %#v, want member participant %#v", target, candidate.Object, want)
			}
		}
	}
	for target := range memberContainers {
		if !includeTargets[target] {
			t.Fatalf("no dependency include fact targeted CAR member %q", target)
		}
	}

	// Keep the input/contribution association exercised explicitly: the
	// correlation is only valid for include observations originating in a CAR
	// member, never for a standalone XML contribution with a payload member.
	for _, input := range inputs {
		if input.Contribution.Type != wso2IncludeContribution {
			continue
		}
		if input.Contribution.Locator.Member == "" {
			t.Fatalf("include contribution %q lost its CAR member locator", input.Contribution.ID)
		}
		if _, exists := contributionsByID[input.Contribution.ID]; !exists {
			t.Fatalf("include contribution %q missing from contribution index", input.Contribution.ID)
		}
	}
}

func wso2IntegrationQualifier(candidate fact.CanonicalFact, name string) string {
	for _, qualifier := range candidate.Qualifiers {
		if qualifier.Name == name && qualifier.Value.Kind == fact.ValueString {
			return qualifier.Value.String
		}
	}
	return ""
}

func cloneWSO2IntegrationManifest(manifest fact.FrontendManifest) fact.FrontendManifest {
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

func cloneWSO2IntegrationInputs(inputs []normalization.Input) []normalization.Input {
	clone := make([]normalization.Input, len(inputs))
	for index, input := range inputs {
		clone[index] = input
		clone[index].Manifest = cloneWSO2IntegrationManifest(input.Manifest)
		clone[index].Contribution.Value = append([]byte(nil), input.Contribution.Value...)
		clone[index].Evidence = append([]fact.EvidenceRef(nil), input.Evidence...)
		clone[index].Extensions = append([]bundle.ExtensionRecord(nil), input.Extensions...)
	}
	return clone
}

func wso2IntegrationFactualDigest(
	t *testing.T,
	analyzed analysis.Output,
	artifact contract.Artifact,
	scope fact.Scope,
	manifest fact.FrontendManifest,
	normalized normalization.Output,
) string {
	t.Helper()
	result := contract.Result{
		Manifest: contract.Manifest{
			ContractVersion:   contract.Version,
			ResultID:          "result-wso2-integration",
			Source:            contract.Source{ID: scope.SourceID, Name: "wso2", Type: "repository"},
			Snapshot:          contract.Snapshot{ID: scope.SnapshotID, SourceID: scope.SourceID},
			Execution:         contract.ExecutionMetadata{RunID: "run-wso2-integration"},
			ArtifactCount:     1,
			ContributionCount: len(analyzed.Contributions),
			Coverage:          append([]contract.Coverage(nil), normalized.Coverage...),
			Gaps:              append([]contract.Gap(nil), analyzed.Gaps...),
		},
		Artifacts:     []contract.Artifact{artifact},
		Contributions: append([]contract.Contribution(nil), analyzed.Contributions...),
	}
	digest, err := bundle.FactualDigestV2(result, nil, []fact.FrontendManifest{manifest}, normalized.Facts, nil)
	if err != nil {
		t.Fatalf("FactualDigestV2() error: %v", err)
	}
	return digest
}
