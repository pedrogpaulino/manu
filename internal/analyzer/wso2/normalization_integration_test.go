package wso2

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
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

const wso2GoldenSchemaVersion = "v1alpha1"

type wso2GoldenManifestIdentity struct {
	ManifestVersion string `json:"manifest_version"`
	ID              string `json:"id"`
	Version         string `json:"version"`
	Method          string `json:"method"`
	DigestSHA256    string `json:"digest_sha256"`
}

type wso2GoldenLocator struct {
	URI         string `json:"uri"`
	SourceID    string `json:"source_id"`
	ArtifactID  string `json:"artifact_id"`
	Path        string `json:"path"`
	Member      string `json:"member"`
	StartLine   int    `json:"start_line"`
	StartColumn int    `json:"start_column"`
	EndLine     int    `json:"end_line"`
	EndColumn   int    `json:"end_column"`
	ByteOffset  int64  `json:"byte_offset"`
	ByteLength  int64  `json:"byte_length"`
}

type wso2GoldenEvidence struct {
	ID      string            `json:"id"`
	Locator wso2GoldenLocator `json:"locator"`
}

type wso2GoldenFact struct {
	FactID    string               `json:"fact_id"`
	Predicate fact.Predicate       `json:"predicate"`
	Evidence  []wso2GoldenEvidence `json:"evidence"`
}

type wso2GoldenCoverage struct {
	Dimension string                 `json:"dimension"`
	State     contract.CoverageState `json:"state"`
	Count     int                    `json:"count"`
}

type wso2GoldenSnapshot struct {
	SchemaVersion string                     `json:"schema_version"`
	Family        string                     `json:"family"`
	Manifest      wso2GoldenManifestIdentity `json:"manifest"`
	FactualDigest string                     `json:"factual_digest"`
	Facts         []wso2GoldenFact           `json:"facts"`
	Coverage      []wso2GoldenCoverage       `json:"coverage"`
}

type wso2IntegrationScenario struct {
	Analyzed   analysis.Output
	Artifact   contract.Artifact
	Scope      fact.Scope
	Manifest   fact.FrontendManifest
	Inputs     []normalization.Input
	Normalized normalization.Output
}

func TestWSO2NormalizationFactualGolden(t *testing.T) {
	scenario := wso2GoldenScenario(t)
	want := readWSO2Golden(t)
	got := wso2GoldenSnapshotFor(t, scenario)
	if !reflect.DeepEqual(got, want) {
		gotJSON, err := json.MarshalIndent(got, "", "  ")
		if err != nil {
			t.Fatalf("marshal generated WSO2 golden: %v", err)
		}
		wantJSON, err := json.MarshalIndent(want, "", "  ")
		if err != nil {
			t.Fatalf("marshal expected WSO2 golden: %v", err)
		}
		t.Fatalf("WSO2 factual golden differs\n--- got ---\n%s\n--- want ---\n%s", gotJSON, wantJSON)
	}
	assertWSO2Golden(t, got)
	assertWSO2GoldenDeterminism(t, scenario, got.FactualDigest)
}

func wso2GoldenScenario(t *testing.T) wso2IntegrationScenario {
	t.Helper()
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
	manifest := Manifest()
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
	normalized, err := registry.NormalizeAll(context.Background(), inputs)
	if err != nil {
		t.Fatalf("NormalizeAll() error: %v", err)
	}
	assertWSO2IntegrationOutput(t, normalized, inputs, scope, manifest, evidenceLocators)
	assertWSO2MemberIncludeCorrelation(t, normalized, inputs, contributionsByID, artifact.ID)
	return wso2IntegrationScenario{
		Analyzed:   analyzed,
		Artifact:   artifact,
		Scope:      scope,
		Manifest:   manifest,
		Inputs:     inputs,
		Normalized: normalized,
	}
}

func readWSO2Golden(t *testing.T) wso2GoldenSnapshot {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "facts.golden.json"))
	if err != nil {
		t.Fatalf("read WSO2 factual golden: %v", err)
	}
	for _, forbidden := range []string{
		"user:pass",
		"tenant=fixture",
		"#fragment",
		"secret-value",
		"[redacted]",
		"${ctx.",
	} {
		if bytes.Contains(data, []byte(forbidden)) {
			t.Fatalf("WSO2 factual golden retained forbidden material %q", forbidden)
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var golden wso2GoldenSnapshot
	if err := decoder.Decode(&golden); err != nil {
		t.Fatalf("decode WSO2 factual golden: %v", err)
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			t.Fatal("WSO2 factual golden contains trailing JSON")
		}
		t.Fatalf("decode trailing WSO2 factual golden: %v", err)
	}
	return golden
}

func wso2GoldenSnapshotFor(t *testing.T, scenario wso2IntegrationScenario) wso2GoldenSnapshot {
	t.Helper()
	manifestDigest, err := fact.FrontendManifestDigest(scenario.Manifest)
	if err != nil {
		t.Fatalf("FrontendManifestDigest() error: %v", err)
	}
	facts := make([]wso2GoldenFact, 0, len(scenario.Normalized.Facts))
	for _, candidate := range scenario.Normalized.Facts {
		if err := candidate.Validate(); err != nil {
			t.Fatalf("normalized fact %q is invalid: %v", candidate.ID, err)
		}
		evidence := make([]wso2GoldenEvidence, 0, len(candidate.Evidence))
		for _, reference := range candidate.Evidence {
			evidence = append(evidence, wso2GoldenEvidence{
				ID: reference.ID,
				Locator: wso2GoldenLocator{
					URI:         reference.Locator.URI,
					SourceID:    reference.Locator.SourceID,
					ArtifactID:  reference.Locator.ArtifactID,
					Path:        reference.Locator.Path,
					Member:      reference.Locator.Member,
					StartLine:   reference.Locator.StartLine,
					StartColumn: reference.Locator.StartColumn,
					EndLine:     reference.Locator.EndLine,
					EndColumn:   reference.Locator.EndColumn,
					ByteOffset:  reference.Locator.ByteOffset,
					ByteLength:  reference.Locator.ByteLength,
				},
			})
		}
		sort.Slice(evidence, func(left, right int) bool { return evidence[left].ID < evidence[right].ID })
		facts = append(facts, wso2GoldenFact{
			FactID:    candidate.ID,
			Predicate: candidate.Predicate,
			Evidence:  evidence,
		})
	}
	sort.Slice(facts, func(left, right int) bool { return facts[left].FactID < facts[right].FactID })

	coverageCounts := make(map[string]int, len(scenario.Normalized.Coverage))
	coverageStates := make(map[string]contract.CoverageState, len(scenario.Normalized.Coverage))
	for _, coverage := range scenario.Normalized.Coverage {
		key := coverage.Dimension + "\x00" + string(coverage.State)
		coverageCounts[key]++
		coverageStates[key] = coverage.State
	}
	coverage := make([]wso2GoldenCoverage, 0, len(coverageCounts))
	for key, count := range coverageCounts {
		parts := strings.SplitN(key, "\x00", 2)
		coverage = append(coverage, wso2GoldenCoverage{Dimension: parts[0], State: coverageStates[key], Count: count})
	}
	sort.Slice(coverage, func(left, right int) bool {
		if coverage[left].Dimension != coverage[right].Dimension {
			return coverage[left].Dimension < coverage[right].Dimension
		}
		return coverage[left].State < coverage[right].State
	})

	return wso2GoldenSnapshot{
		SchemaVersion: wso2GoldenSchemaVersion,
		Family:        "wso2",
		Manifest: wso2GoldenManifestIdentity{
			ManifestVersion: scenario.Manifest.ManifestVersion,
			ID:              scenario.Manifest.ID,
			Version:         scenario.Manifest.Version,
			Method:          scenario.Manifest.Method,
			DigestSHA256:    manifestDigest,
		},
		FactualDigest: wso2IntegrationFactualDigest(t, scenario.Analyzed, scenario.Artifact, scenario.Scope, scenario.Manifest, scenario.Normalized),
		Facts:         facts,
		Coverage:      coverage,
	}
}

func assertWSO2Golden(t *testing.T, golden wso2GoldenSnapshot) {
	t.Helper()
	if golden.SchemaVersion != wso2GoldenSchemaVersion || golden.Family != "wso2" {
		t.Fatalf("golden identity = %#v, want schema %q and family wso2", golden, wso2GoldenSchemaVersion)
	}
	if golden.Manifest.ID == "" || golden.Manifest.Version == "" || golden.Manifest.Method == "" || golden.Manifest.DigestSHA256 == "" {
		t.Fatalf("golden manifest identity is incomplete: %#v", golden.Manifest)
	}
	for index, candidate := range golden.Facts {
		if candidate.FactID == "" || candidate.Predicate == fact.PredicateUnknown || len(candidate.Evidence) == 0 {
			t.Fatalf("golden fact %d is incomplete: %#v", index, candidate)
		}
		if index > 0 && golden.Facts[index-1].FactID >= candidate.FactID {
			t.Fatalf("golden facts are not strictly ordered by fact_id at %d", index)
		}
		if err := candidate.Predicate.Validate(); err != nil {
			t.Fatalf("golden fact %q predicate is invalid: %v", candidate.FactID, err)
		}
		for evidenceIndex, evidence := range candidate.Evidence {
			if evidence.ID == "" || (evidenceIndex > 0 && candidate.Evidence[evidenceIndex-1].ID >= evidence.ID) {
				t.Fatalf("golden fact %q evidence is not strictly ordered: %#v", candidate.FactID, candidate.Evidence)
			}
			locator := evidence.Locator
			if locator.Member == "" {
				t.Fatalf("golden fact %q evidence %q has empty CAR member", candidate.FactID, evidence.ID)
			}
			if locator.StartLine < 0 || locator.StartColumn < 0 || locator.EndLine < 0 || locator.EndColumn < 0 || locator.ByteOffset < 0 || locator.ByteLength < 0 {
				t.Fatalf("golden fact %q evidence %q has negative locator position: %#v", candidate.FactID, evidence.ID, locator)
			}
			if locator.EndLine > 0 && locator.StartLine > locator.EndLine {
				t.Fatalf("golden fact %q evidence %q has invalid line range: %#v", candidate.FactID, evidence.ID, locator)
			}
		}
	}
	for index, coverage := range golden.Coverage {
		if coverage.Dimension == "" || coverage.State == contract.CoverageUnknown || coverage.Count <= 0 {
			t.Fatalf("golden coverage %d is incomplete: %#v", index, coverage)
		}
		if index > 0 && (golden.Coverage[index-1].Dimension > coverage.Dimension || (golden.Coverage[index-1].Dimension == coverage.Dimension && golden.Coverage[index-1].State >= coverage.State)) {
			t.Fatalf("golden coverage is not strictly ordered at %d", index)
		}
	}
}

func assertWSO2GoldenDeterminism(t *testing.T, scenario wso2IntegrationScenario, wantDigest string) {
	t.Helper()
	registrations, err := NormalizerRegistrations(scenario.Manifest)
	if err != nil {
		t.Fatalf("NormalizerRegistrations() for determinism check: %v", err)
	}
	registry, err := normalization.NewRegistry(registrations...)
	if err != nil {
		t.Fatalf("NewRegistry() for determinism check: %v", err)
	}
	reversedInputs := cloneWSO2IntegrationInputs(scenario.Inputs)
	for left, right := 0, len(reversedInputs)-1; left < right; left, right = left+1, right-1 {
		reversedInputs[left], reversedInputs[right] = reversedInputs[right], reversedInputs[left]
	}
	reversed, err := registry.NormalizeAll(context.Background(), reversedInputs)
	if err != nil {
		t.Fatalf("NormalizeAll(reversed) for golden: %v", err)
	}
	repeated, err := registry.NormalizeAll(context.Background(), scenario.Inputs)
	if err != nil {
		t.Fatalf("NormalizeAll(repeated) for golden: %v", err)
	}
	if !reflect.DeepEqual(scenario.Normalized, reversed) || !reflect.DeepEqual(scenario.Normalized, repeated) {
		t.Fatal("WSO2 factual output changed under input inversion or repetition")
	}
	reversedDigest := wso2IntegrationFactualDigest(t, scenario.Analyzed, scenario.Artifact, scenario.Scope, scenario.Manifest, reversed)
	repeatedDigest := wso2IntegrationFactualDigest(t, scenario.Analyzed, scenario.Artifact, scenario.Scope, scenario.Manifest, repeated)
	if wantDigest == "" || wantDigest != reversedDigest || wantDigest != repeatedDigest {
		t.Fatalf("WSO2 factual digest changed under input inversion or repetition: want=%q reversed=%q repeated=%q", wantDigest, reversedDigest, repeatedDigest)
	}
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
