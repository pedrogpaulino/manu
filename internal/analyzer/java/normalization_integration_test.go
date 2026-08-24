package java

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"testing"

	"github.com/pedrogpaulino/manu/internal/analysis"
	"github.com/pedrogpaulino/manu/internal/bundle"
	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/fact"
	"github.com/pedrogpaulino/manu/internal/normalization"
)

var javaIntegrationDimensions = map[string]contract.Dimension{
	javaArtifactContribution:      contract.DimensionLandscapeInventoryStructure,
	javaPackageContribution:       contract.DimensionLandscapeInventoryStructure,
	javaTypeContribution:          contract.DimensionEntitiesAndRelationships,
	javaMethodContribution:        contract.DimensionEntitiesAndRelationships,
	javaImportContribution:        contract.DimensionFlowsAndDependencies,
	javaRelationContribution:      contract.DimensionFlowsAndDependencies,
	javaConfigurationContribution: contract.DimensionConfigurationVariations,
	javaEndpointContribution:      contract.DimensionEntitiesAndRelationships,
}

func TestJavaQuarkusNormalizationEndToEnd(t *testing.T) {
	fixturePath := javaIntegrationFixturePath(t)
	root, err := os.OpenRoot(filepath.Dir(fixturePath))
	if err != nil {
		t.Fatalf("open fixture root: %v", err)
	}
	defer root.Close()

	fixtureName := filepath.Base(fixturePath)
	fixtureBytes := javaIntegrationReadFixture(t, root, fixtureName)
	hash := sha256.Sum256(fixtureBytes)
	artifact := contract.Artifact{
		SourceID: "source-quarkus3",
		Path:     fixtureName,
		Type:     analysis.ArtifactTypeJava,
		Hash:     hex.EncodeToString(hash[:]),
		Size:     int64(len(fixtureBytes)),
	}
	artifact.ID = contract.ArtifactID(artifact.SourceID, artifact.Path, artifact.Hash)

	analyzerInput := analysis.ArtifactInput{
		SourceID:   artifact.SourceID,
		RootHandle: root,
		Artifact:   artifact,
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

	assertJavaFixtureGaps(t, analyzed)
	manifest := javaIntegrationManifest()
	if err := manifest.Validate(); err != nil {
		t.Fatalf("integration manifest is invalid: %v", err)
	}
	assertJavaIntegrationManifest(t, manifest)

	registrations, err := NormalizerRegistrations(manifest)
	if err != nil {
		t.Fatalf("NormalizerRegistrations() error: %v", err)
	}
	if len(registrations) != len(javaIntegrationDimensions) {
		t.Fatalf("registration count = %d, want %d", len(registrations), len(javaIntegrationDimensions))
	}
	for _, registration := range registrations {
		if _, supported := javaIntegrationDimensions[registration.ContributionType]; !supported {
			t.Fatalf("unexpected registration for %q", registration.ContributionType)
		}
		if registration.ContributionType == "java.annotation" || registration.ContributionType == "java.exception" {
			t.Fatalf("unsupported observation %q received a universal registration", registration.ContributionType)
		}
	}
	registry, err := normalization.NewRegistry(registrations...)
	if err != nil {
		t.Fatalf("NewRegistry() error: %v", err)
	}

	scope := fact.Scope{
		OrganizationID: "organization-quarkus3",
		SourceID:       artifact.SourceID,
		SnapshotID:     "snapshot-quarkus3-booking",
	}
	inputs, allInputs, evidenceLocators, contributionsByID := javaIntegrationInputs(t, analyzed, scope, manifest)
	if len(inputs) == 0 {
		t.Fatal("Analyze() produced no supported Java contributions")
	}

	beforeInputs := cloneJavaIntegrationInputs(inputs)
	normalized, err := registry.NormalizeAll(context.Background(), inputs)
	if err != nil {
		t.Fatalf("NormalizeAll() error: %v", err)
	}
	if !reflect.DeepEqual(inputs, beforeInputs) {
		t.Fatal("NormalizeAll() mutated its normalization inputs")
	}
	assertJavaNormalizedOutput(t, normalized, inputs, scope, manifest, evidenceLocators, contributionsByID)

	reverseInputs := cloneJavaIntegrationInputs(inputs)
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

	firstDigest := javaIntegrationFactualDigest(t, analyzed, artifact, scope, manifest, normalized)
	reversedDigest := javaIntegrationFactualDigest(t, analyzed, artifact, scope, manifest, reversed)
	if firstDigest != reversedDigest {
		t.Fatalf("factual digest changed with input order: %q != %q", firstDigest, reversedDigest)
	}

	firstSummary := javaQuarkusGoldenSummary(t, manifest, normalized, firstDigest)
	golden := readJavaQuarkusGolden(t)
	if !reflect.DeepEqual(firstSummary, golden) {
		t.Fatalf("generated Java/Quarkus factual summary differs from golden\n--- got ---\n%#v\n--- want ---\n%#v", firstSummary, golden)
	}
	reversedSummary := javaQuarkusGoldenSummary(t, manifest, reversed, reversedDigest)
	if !reflect.DeepEqual(firstSummary, reversedSummary) {
		t.Fatal("Java/Quarkus factual summary changed when inputs were reversed")
	}
	repeatedDigest := javaIntegrationFactualDigest(t, analyzed, artifact, scope, manifest, repeated)
	if firstDigest != repeatedDigest {
		t.Fatalf("repeated factual digest changed: %q != %q", firstDigest, repeatedDigest)
	}
	repeatedSummary := javaQuarkusGoldenSummary(t, manifest, repeated, repeatedDigest)
	if !reflect.DeepEqual(firstSummary, repeatedSummary) {
		t.Fatal("repeated Java/Quarkus factual summary changed")
	}

	assertUnsupportedJavaObservationsHaveNoFacts(t, registry, allInputs, analyzed, contributionsByID)
}

func javaIntegrationFixturePath(t *testing.T) string {
	t.Helper()
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller() could not locate the Java package")
	}
	return filepath.Join(filepath.Dir(sourceFile), "testdata", "quarkus3", "BookingResource.java")
}

func javaIntegrationReadFixture(t *testing.T, root *os.Root, name string) []byte {
	t.Helper()
	file, err := root.Open(name)
	if err != nil {
		t.Fatalf("open fixture %q from root: %v", name, err)
	}
	defer file.Close()
	content, err := io.ReadAll(file)
	if err != nil {
		t.Fatalf("read fixture %q: %v", name, err)
	}
	return content
}

func javaIntegrationManifest() fact.FrontendManifest {
	return Manifest()
}

func assertJavaIntegrationManifest(t *testing.T, manifest fact.FrontendManifest) {
	t.Helper()
	for _, value := range []string{"java", "quarkus"} {
		if !containsJavaString(manifest.Families, value) {
			t.Fatalf("manifest families = %#v, want representative %q", manifest.Families, value)
		}
	}
	for _, value := range []string{"java-21", "quarkus-3.26.4"} {
		if !containsJavaString(manifest.Versions, value) {
			t.Fatalf("manifest versions = %#v, want representative %q", manifest.Versions, value)
		}
	}
}

func containsJavaString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func javaIntegrationInputs(
	t *testing.T,
	analyzed analysis.Output,
	scope fact.Scope,
	manifest fact.FrontendManifest,
) ([]normalization.Input, map[string]normalization.Input, map[string]contract.Locator, map[string]contract.Contribution) {
	t.Helper()

	draftsByContribution := make(map[string][]analysis.EvidenceDraft)
	for _, draft := range analyzed.Evidence {
		if draft.ContributionID == "" {
			t.Fatal("evidence draft has no contribution identity")
		}
		draftsByContribution[draft.ContributionID] = append(draftsByContribution[draft.ContributionID], draft)
	}

	contributionsByID := make(map[string]contract.Contribution, len(analyzed.Contributions))
	allInputs := make(map[string]normalization.Input, len(analyzed.Contributions))
	evidenceLocators := make(map[string]contract.Locator, len(analyzed.Contributions))
	inputs := make([]normalization.Input, 0, len(analyzed.Contributions))
	seenTypes := make(map[string]int, len(javaIntegrationDimensions))
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
			Manifest:     cloneJavaIntegrationManifest(manifest),
			Contribution: contribution,
			Evidence: []fact.EvidenceRef{{
				ID:      "evidence-" + contribution.ID,
				Locator: drafts[0].Locator,
			}},
		}
		if err := input.Evidence[0].Validate(scope); err != nil {
			t.Fatalf("evidence for contribution %q is invalid: %v", contribution.ID, err)
		}
		allInputs[contribution.ID] = input
		evidenceLocators[input.Evidence[0].ID] = contribution.Locator
		if _, supported := javaIntegrationDimensions[contribution.Type]; supported {
			inputs = append(inputs, input)
			seenTypes[contribution.Type]++
		}
	}
	for contributionID, drafts := range draftsByContribution {
		if _, exists := contributionsByID[contributionID]; !exists {
			t.Fatalf("evidence draft %q has no matching contribution", contributionID)
		}
		if len(drafts) != 1 {
			t.Fatalf("evidence draft association for %q is ambiguous", contributionID)
		}
	}
	for contributionType := range javaIntegrationDimensions {
		if seenTypes[contributionType] == 0 {
			t.Fatalf("fixture produced no mapped contribution of type %q", contributionType)
		}
	}
	return inputs, allInputs, evidenceLocators, contributionsByID
}

func cloneJavaIntegrationManifest(manifest fact.FrontendManifest) fact.FrontendManifest {
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

func cloneJavaIntegrationInputs(inputs []normalization.Input) []normalization.Input {
	clone := make([]normalization.Input, len(inputs))
	for index, input := range inputs {
		clone[index] = input
		clone[index].Manifest = cloneJavaIntegrationManifest(input.Manifest)
		clone[index].Contribution.Value = append([]byte(nil), input.Contribution.Value...)
		clone[index].Evidence = append([]fact.EvidenceRef(nil), input.Evidence...)
		clone[index].Extensions = append([]bundle.ExtensionRecord(nil), input.Extensions...)
	}
	return clone
}

func assertJavaNormalizedOutput(
	t *testing.T,
	output normalization.Output,
	inputs []normalization.Input,
	scope fact.Scope,
	manifest fact.FrontendManifest,
	evidenceLocators map[string]contract.Locator,
	contributionsByID map[string]contract.Contribution,
) {
	t.Helper()
	if len(output.Coverage) != len(inputs) {
		t.Fatalf("normalized coverage count = %d, want one per mapped contribution (%d)", len(output.Coverage), len(inputs))
	}
	seenCoverage := make(map[string]struct{}, len(output.Coverage))
	for _, coverage := range output.Coverage {
		contribution, exists := contributionsByID[coverage.Scope]
		if !exists {
			t.Fatalf("coverage scope %q does not identify an input contribution", coverage.Scope)
		}
		expectedDimension, mapped := javaIntegrationDimensions[contribution.Type]
		if !mapped || coverage.Dimension != string(expectedDimension) {
			t.Fatalf("coverage for %q = %#v, want mapped dimension %q", contribution.ID, coverage, expectedDimension)
		}
		if coverage.State != contract.CoverageProduced || coverage.AnalyzerID != manifest.ID || coverage.Scope != contribution.ID {
			t.Fatalf("coverage = %#v, want produced coverage scoped to %q and analyzer %q", coverage, contribution.ID, manifest.ID)
		}
		if coverage.ID != contract.CoverageID(coverage.Dimension, coverage.Scope, coverage.State, coverage.AnalyzerID) {
			t.Fatalf("coverage %q is not deterministic", coverage.ID)
		}
		if err := coverage.Validate(); err != nil {
			t.Fatalf("coverage.Validate() error = %v", err)
		}
		if _, duplicate := seenCoverage[coverage.Scope]; duplicate {
			t.Fatalf("duplicate coverage for contribution %q", coverage.Scope)
		}
		seenCoverage[coverage.Scope] = struct{}{}
	}

	wantProducer := fact.Producer{ID: manifest.ID, Version: manifest.Version, Method: manifest.Method}
	seenPredicates := make(map[fact.Predicate]struct{})
	if len(output.Facts) == 0 {
		t.Fatal("normalization produced no facts for the representative fixture")
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
			expectedLocator, exists := evidenceLocators[evidence.ID]
			if !exists {
				t.Fatalf("fact %q references unknown evidence %q", candidate.ID, evidence.ID)
			}
			if evidence.Locator != expectedLocator {
				t.Fatalf("fact %q evidence locator = %#v, want contribution locator %#v", candidate.ID, evidence.Locator, expectedLocator)
			}
		}
		seenPredicates[candidate.Predicate] = struct{}{}
	}
	for _, predicate := range []fact.Predicate{
		fact.PredicateArtifact,
		fact.PredicateSymbol,
		fact.PredicateDefinition,
		fact.PredicateReference,
		fact.PredicateCall,
		fact.PredicateDependency,
		fact.PredicateConfiguration,
		fact.PredicateEndpoint,
	} {
		if _, exists := seenPredicates[predicate]; !exists {
			t.Fatalf("normalized fixture facts do not contain predicate %q", predicate)
		}
	}
}

func javaIntegrationFactualDigest(
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
			ResultID:          "result-quarkus3-booking",
			Source:            contract.Source{ID: scope.SourceID, Name: "quarkus3", Type: "repository"},
			Snapshot:          contract.Snapshot{ID: scope.SnapshotID, SourceID: scope.SourceID},
			Execution:         contract.ExecutionMetadata{RunID: "run-quarkus3-booking"},
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

func assertUnsupportedJavaObservationsHaveNoFacts(
	t *testing.T,
	registry *normalization.Registry,
	allInputs map[string]normalization.Input,
	analyzed analysis.Output,
	contributionsByID map[string]contract.Contribution,
) {
	t.Helper()
	unsupportedSeen := make(map[string]bool)
	for _, contribution := range analyzed.Contributions {
		if contribution.Type != "java.annotation" && contribution.Type != "java.exception" {
			continue
		}
		unsupportedSeen[contribution.Type] = true
		input, exists := allInputs[contribution.ID]
		if !exists {
			t.Fatalf("unsupported contribution %q has no normalization input", contribution.ID)
		}
		output, err := registry.Normalize(context.Background(), input)
		if err != nil {
			t.Fatalf("Normalize(%q) error: %v", contribution.Type, err)
		}
		if len(output.Facts) != 0 {
			t.Fatalf("unsupported contribution %q produced universal facts: %#v", contribution.Type, output.Facts)
		}
	}
	if !unsupportedSeen["java.annotation"] {
		t.Fatal("representative fixture did not retain its annotation observation")
	}
	for _, candidate := range analyzed.Contributions {
		if candidate.Type != "java.annotation" && candidate.Type != "java.exception" {
			continue
		}
		if _, exists := contributionsByID[candidate.ID]; !exists {
			t.Fatalf("unsupported contribution %q was not retained by Analyze()", candidate.ID)
		}
	}
}

func assertJavaFixtureGaps(t *testing.T, output analysis.Output) {
	t.Helper()
	foundGeneralGap := false
	for _, gap := range output.Gaps {
		switch gap.Code {
		case "java_semantics_not_supported":
			foundGeneralGap = true
		case javaEndpointGapCode:
			t.Fatalf("fixture reported endpoint gap despite supported endpoint observations: %#v", gap)
		}
	}
	if !foundGeneralGap {
		t.Fatalf("fixture gaps = %#v, want java_semantics_not_supported", output.Gaps)
	}
}

const javaQuarkusGoldenSchemaVersion = "java-quarkus-factual-v1"

type javaQuarkusGolden struct {
	SchemaVersion string                      `json:"schema_version"`
	Family        string                      `json:"family"`
	Manifest      javaQuarkusGoldenManifest   `json:"manifest"`
	FactualDigest string                      `json:"factual_digest"`
	Facts         []javaQuarkusGoldenFact     `json:"facts"`
	Coverage      []javaQuarkusGoldenCoverage `json:"coverage"`
}

type javaQuarkusGoldenManifest struct {
	ManifestVersion string `json:"manifest_version"`
	ID              string `json:"id"`
	Version         string `json:"version"`
	Method          string `json:"method"`
}

type javaQuarkusGoldenFact struct {
	FactID    string                      `json:"fact_id"`
	Predicate fact.Predicate              `json:"predicate"`
	Evidence  []javaQuarkusGoldenEvidence `json:"evidence"`
}

type javaQuarkusGoldenEvidence struct {
	ID      string           `json:"id"`
	Locator contract.Locator `json:"locator"`
}

type javaQuarkusGoldenCoverage struct {
	Dimension string `json:"dimension"`
	State     string `json:"state"`
	Count     int    `json:"count"`
}

func readJavaQuarkusGolden(t *testing.T) javaQuarkusGolden {
	t.Helper()
	goldenPath := filepath.Join(filepath.Dir(javaIntegrationFixturePath(t)), "facts.golden.json")
	file, err := os.Open(goldenPath)
	if err != nil {
		t.Fatalf("open Java/Quarkus golden %q: %v", goldenPath, err)
	}
	defer file.Close()

	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var golden javaQuarkusGolden
	if err := decoder.Decode(&golden); err != nil {
		t.Fatalf("decode Java/Quarkus golden %q: %v", goldenPath, err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			t.Fatalf("Java/Quarkus golden %q contains trailing JSON", goldenPath)
		}
		t.Fatalf("decode trailing Java/Quarkus golden data %q: %v", goldenPath, err)
	}
	validateJavaQuarkusGolden(t, golden)
	return golden
}

func validateJavaQuarkusGolden(t *testing.T, golden javaQuarkusGolden) {
	t.Helper()
	if golden.SchemaVersion != javaQuarkusGoldenSchemaVersion {
		t.Fatalf("golden schema_version = %q, want %q", golden.SchemaVersion, javaQuarkusGoldenSchemaVersion)
	}
	if golden.Family != "java/quarkus" {
		t.Fatalf("golden family = %q, want java/quarkus", golden.Family)
	}
	if golden.Manifest.ManifestVersion != fact.FrontendManifestVersion {
		t.Fatalf("golden manifest_version = %q, want %q", golden.Manifest.ManifestVersion, fact.FrontendManifestVersion)
	}
	if golden.Manifest.ID == "" || golden.Manifest.Version == "" || golden.Manifest.Method == "" {
		t.Fatalf("golden manifest identity is incomplete: %#v", golden.Manifest)
	}
	if len(golden.FactualDigest) != sha256.Size*2 {
		t.Fatalf("golden factual_digest length = %d, want %d", len(golden.FactualDigest), sha256.Size*2)
	}
	if _, err := hex.DecodeString(golden.FactualDigest); err != nil {
		t.Fatalf("golden factual_digest is not hexadecimal: %v", err)
	}
	if len(golden.Facts) == 0 {
		t.Fatal("golden facts is empty")
	}
	seenFacts := make(map[string]struct{}, len(golden.Facts))
	for index, candidate := range golden.Facts {
		if candidate.FactID == "" {
			t.Fatalf("golden fact %d has empty fact_id", index)
		}
		if _, duplicate := seenFacts[candidate.FactID]; duplicate {
			t.Fatalf("golden facts repeat fact_id %q", candidate.FactID)
		}
		seenFacts[candidate.FactID] = struct{}{}
		if err := candidate.Predicate.Validate(); err != nil {
			t.Fatalf("golden fact %q predicate is invalid: %v", candidate.FactID, err)
		}
		if len(candidate.Evidence) == 0 {
			t.Fatalf("golden fact %q has no evidence", candidate.FactID)
		}
		seenEvidence := make(map[string]struct{}, len(candidate.Evidence))
		for evidenceIndex, evidence := range candidate.Evidence {
			if evidence.ID == "" {
				t.Fatalf("golden fact %q evidence %d has empty id", candidate.FactID, evidenceIndex)
			}
			if _, duplicate := seenEvidence[evidence.ID]; duplicate {
				t.Fatalf("golden fact %q repeats evidence id %q", candidate.FactID, evidence.ID)
			}
			seenEvidence[evidence.ID] = struct{}{}
			if err := evidence.Locator.Validate(); err != nil {
				t.Fatalf("golden fact %q evidence %q locator is invalid: %v", candidate.FactID, evidence.ID, err)
			}
			assertJavaQuarkusGoldenLocator(t, candidate.FactID, evidence.Locator)
			if evidenceIndex > 0 && candidate.Evidence[evidenceIndex-1].ID >= evidence.ID {
				t.Fatalf("golden evidence for fact %q is not ordered by id", candidate.FactID)
			}
		}
		if index > 0 && golden.Facts[index-1].FactID >= candidate.FactID {
			t.Fatalf("golden facts are not ordered by fact_id")
		}
	}
	if len(golden.Coverage) == 0 {
		t.Fatal("golden coverage is empty")
	}
	for index, coverage := range golden.Coverage {
		if coverage.Dimension == "" || coverage.State == "" || coverage.Count <= 0 {
			t.Fatalf("golden coverage %d is incomplete: %#v", index, coverage)
		}
		if index > 0 {
			previous := golden.Coverage[index-1]
			if previous.Dimension > coverage.Dimension || (previous.Dimension == coverage.Dimension && previous.State >= coverage.State) {
				t.Fatalf("golden coverage is not ordered by dimension and state")
			}
		}
	}
}

func assertJavaQuarkusGoldenLocator(t *testing.T, factID string, locator contract.Locator) {
	t.Helper()
	if locator.SourceID == "" || locator.ArtifactID == "" {
		t.Fatalf("golden fact %q locator is incomplete: %#v", factID, locator)
	}
	if locator.Path != "BookingResource.java" {
		t.Fatalf("golden fact %q locator path = %q, want BookingResource.java", factID, locator.Path)
	}
	if locator.StartLine <= 0 || locator.EndLine < locator.StartLine || locator.EndLine > 35 {
		t.Fatalf("golden fact %q locator lines = %d-%d, want valid BookingResource.java lines", factID, locator.StartLine, locator.EndLine)
	}
}

func javaQuarkusGoldenSummary(t *testing.T, manifest fact.FrontendManifest, output normalization.Output, digest string) javaQuarkusGolden {
	t.Helper()
	facts := make([]javaQuarkusGoldenFact, 0, len(output.Facts))
	for _, candidate := range output.Facts {
		if err := candidate.Validate(); err != nil {
			t.Fatalf("generated fact %q is invalid: %v", candidate.ID, err)
		}
		goldenFact := javaQuarkusGoldenFact{
			FactID:    candidate.ID,
			Predicate: candidate.Predicate,
			Evidence:  make([]javaQuarkusGoldenEvidence, 0, len(candidate.Evidence)),
		}
		for _, evidence := range candidate.Evidence {
			if err := evidence.Validate(candidate.Scope); err != nil {
				t.Fatalf("generated fact %q evidence %q is invalid: %v", candidate.ID, evidence.ID, err)
			}
			goldenFact.Evidence = append(goldenFact.Evidence, javaQuarkusGoldenEvidence{ID: evidence.ID, Locator: evidence.Locator})
		}
		sort.Slice(goldenFact.Evidence, func(left, right int) bool {
			return goldenFact.Evidence[left].ID < goldenFact.Evidence[right].ID
		})
		facts = append(facts, goldenFact)
	}
	sort.Slice(facts, func(left, right int) bool { return facts[left].FactID < facts[right].FactID })

	type coverageKey struct {
		dimension string
		state     string
	}
	coverageCounts := make(map[coverageKey]int, len(output.Coverage))
	for _, coverage := range output.Coverage {
		if err := coverage.Validate(); err != nil {
			t.Fatalf("generated coverage is invalid: %v", err)
		}
		key := coverageKey{dimension: coverage.Dimension, state: string(coverage.State)}
		coverageCounts[key]++
	}
	coverage := make([]javaQuarkusGoldenCoverage, 0, len(coverageCounts))
	for key, count := range coverageCounts {
		coverage = append(coverage, javaQuarkusGoldenCoverage{Dimension: key.dimension, State: key.state, Count: count})
	}
	sort.Slice(coverage, func(left, right int) bool {
		if coverage[left].Dimension != coverage[right].Dimension {
			return coverage[left].Dimension < coverage[right].Dimension
		}
		return coverage[left].State < coverage[right].State
	})

	return javaQuarkusGolden{
		SchemaVersion: javaQuarkusGoldenSchemaVersion,
		Family:        "java/quarkus",
		Manifest: javaQuarkusGoldenManifest{
			ManifestVersion: manifest.ManifestVersion,
			ID:              manifest.ID,
			Version:         manifest.Version,
			Method:          manifest.Method,
		},
		FactualDigest: digest,
		Facts:         facts,
		Coverage:      coverage,
	}
}
