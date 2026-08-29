package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/pedrogpaulino/manu/internal/analyzer"
	"github.com/pedrogpaulino/manu/internal/config"
	"github.com/pedrogpaulino/manu/internal/evaluation"
	"github.com/pedrogpaulino/manu/internal/fact"
)

func TestBuildEvaluationVariantMetadataIsDeterministicAndContentFree(t *testing.T) {
	cases := loadEvaluationMetadataCases(t)
	configuration := config.Default()
	configuration.Retrieval.TopK = 7
	configuration.Retrieval.MaxCandidates = 19
	configuration.Retrieval.MaxRelationHops = 1
	configuration.Retrieval.MaxRelationFanOut = 11
	configuration.Retrieval.MaxPackageUnits = 13
	configuration.Retrieval.MaxPackageBytes = 32 << 10
	configuration.Retrieval.MaxPackageTokens = 2048
	configuration.Retrieval.ExactWeight = 1.5
	configuration.Retrieval.TextWeight = 0.75
	configuration.Retrieval.VectorWeight = 0
	configuration.Retrieval.RelationWeight = 2

	started := time.Date(2026, 8, 28, 12, 0, 0, 0, time.FixedZone("test", -3*60*60))
	finished := started.Add(2 * time.Second)
	open := func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader([]byte("bounded executable fixture"))), nil
	}
	input := evaluationVariantMetadataInput{
		Cases:          cases,
		Configuration:  configuration,
		RunID:          "evaluation-run-8-8",
		StartedAt:      started,
		FinishedAt:     finished,
		OpenExecutable: open,
	}
	first, err := buildEvaluationVariantMetadata(input)
	if err != nil {
		t.Fatalf("buildEvaluationVariantMetadata() error = %v", err)
	}
	second, err := buildEvaluationVariantMetadata(input)
	if err != nil {
		t.Fatalf("repeated buildEvaluationVariantMetadata() error = %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("metadata is not deterministic:\nfirst=%#v\nsecond=%#v", first, second)
	}
	if err := first.Validate(); err != nil {
		t.Fatalf("metadata Validate() error = %v", err)
	}
	if !first.StartedAt.Equal(started.UTC()) || !first.FinishedAt.Equal(finished.UTC()) {
		t.Fatalf("timestamps = %v/%v, want UTC-normalized values", first.StartedAt, first.FinishedAt)
	}

	if first.Agent != (evaluation.EvaluationComponent{ID: evaluationMetadataAgentID, Version: evaluationMetadataAgentVersion}) {
		t.Fatalf("agent = %#v", first.Agent)
	}
	if first.Model != (evaluation.EvaluationComponent{ID: evaluationMetadataModelID, Version: evaluationMetadataModelVersion}) {
		t.Fatalf("model = %#v", first.Model)
	}
	wantExecutableDigest := sha256.Sum256([]byte("bounded executable fixture"))
	if first.ContextServer.ID != evaluationMetadataContextID || first.ContextServer.Version != serveRuntimeVersion || first.ContextServer.Digest != hex.EncodeToString(wantExecutableDigest[:]) {
		t.Fatalf("context server = %#v", first.ContextServer)
	}

	manifests, err := analyzer.FrontendManifests()
	if err != nil {
		t.Fatalf("analyzer.FrontendManifests() error = %v", err)
	}
	if len(first.Frontends) != len(manifests) {
		t.Fatalf("frontend count = %d, want %d", len(first.Frontends), len(manifests))
	}
	for index, manifest := range manifests {
		digest, digestErr := fact.FrontendManifestDigest(manifest)
		if digestErr != nil {
			t.Fatalf("frontend %q digest error = %v", manifest.ID, digestErr)
		}
		want := evaluation.EvaluationComponent{ID: manifest.ID, Version: manifest.Version, Digest: digest}
		if first.Frontends[index] != want {
			t.Fatalf("frontend %d = %#v, want %#v", index, first.Frontends[index], want)
		}
	}

	wantRulesDigest := evaluationDisabledRulesDigest()
	if len(first.Rules) != 1 || first.Rules[0] != (evaluation.EvaluationComponent{ID: evaluationMetadataRulesID, Version: evaluationMetadataRulesVersion, Digest: wantRulesDigest}) {
		t.Fatalf("rules = %#v", first.Rules)
	}
	if len(wantRulesDigest) != sha256.Size*2 {
		t.Fatalf("rules digest = %q, want SHA-256", wantRulesDigest)
	}

	wantSettings := map[string]string{
		"strategy":                "hybrid",
		"top_k":                   "7",
		"max_candidates":          "19",
		"max_relation_hops":       "1",
		"max_relation_fan_out":    "11",
		"max_package_units":       "13",
		"max_package_bytes":       "32768",
		"max_package_budget":      "2048",
		"max_package_budget_unit": "tokens",
		"exact_weight":            "1.5",
		"text_weight":             "0.75",
		"vector_weight":           "0",
		"relation_weight":         "2",
	}
	if first.Retrieval.ID != evaluationMetadataRetrievalID || first.Retrieval.Version != evaluationMetadataRetrievalVersion || !reflect.DeepEqual(first.Retrieval.Settings, wantSettings) {
		t.Fatalf("retrieval = %#v, want settings %#v", first.Retrieval, wantSettings)
	}

	wantTools := []evaluation.EvaluationComponent{
		{ID: "filesystem-search", Version: "v1"},
		{ID: "manu-context", Version: "v1"},
		{ID: "text-retrieval", Version: "v1"},
	}
	if !reflect.DeepEqual(first.Tools, wantTools) {
		t.Fatalf("tools = %#v, want %#v", first.Tools, wantTools)
	}
	for _, limitation := range []string{
		"generator_not_executed",
		"model_not_executed",
		"text_variant_unavailable",
		"impact_uses_question_fallback_without_typed_target",
		"tokens_and_cost_unavailable",
		"savings_only_defined_for_correct_supported_results",
	} {
		if !containsString(first.Limitations, limitation) {
			t.Fatalf("limitation %q absent: %#v", limitation, first.Limitations)
		}
	}

	encoded, err := json.Marshal(first)
	if err != nil {
		t.Fatalf("json.Marshal(metadata) error = %v", err)
	}
	for _, forbidden := range []string{
		"internal/analyzer/",
		"BookingResource",
		"password",
		"postgres",
		"/home/",
	} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("metadata serialized forbidden value %q: %s", forbidden, encoded)
		}
	}
}

func TestEvaluationExecutableDigestUsesBoundedReaderAndSafeErrors(t *testing.T) {
	const marker = "metadata-executable"
	digest, err := evaluationExecutableDigest(func() (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader(marker)), nil
	})
	if err != nil {
		t.Fatalf("evaluationExecutableDigest() error = %v", err)
	}
	want := sha256.Sum256([]byte(marker))
	if digest != hex.EncodeToString(want[:]) {
		t.Fatalf("digest = %q, want %q", digest, hex.EncodeToString(want[:]))
	}

	openErr := errors.New("/sensitive/private/executable")
	_, err = evaluationExecutableDigest(func() (io.ReadCloser, error) { return nil, openErr })
	if !errors.Is(err, ErrEvaluationVariantMetadata) || strings.Contains(err.Error(), openErr.Error()) {
		t.Fatalf("open error = %v, want safe ErrEvaluationVariantMetadata", err)
	}
	_, err = evaluationExecutableDigest(func() (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader("")), nil
	})
	if !errors.Is(err, ErrEvaluationVariantMetadata) {
		t.Fatalf("empty executable error = %v, want ErrEvaluationVariantMetadata", err)
	}
}

func TestBuildEvaluationVariantMetadataRejectsInvalidInputsWithoutLeakingValues(t *testing.T) {
	cases := loadEvaluationMetadataCases(t)
	configuration := config.Default()
	input := evaluationVariantMetadataInput{
		Cases:         cases,
		Configuration: configuration,
		RunID:         "metadata-run",
		OpenExecutable: func() (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader("executable")), nil
		},
	}

	tests := []struct {
		name   string
		mutate func(*evaluationVariantMetadataInput)
	}{
		{name: "invalid run id", mutate: func(value *evaluationVariantMetadataInput) { value.RunID = "bad run id" }},
		{name: "invalid retrieval", mutate: func(value *evaluationVariantMetadataInput) { value.Configuration.Retrieval.TopK = 0 }},
		{name: "invalid cases", mutate: func(value *evaluationVariantMetadataInput) {
			value.Cases.Cases[0].CompetenceQuestion = "secret=password=do-not-return"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := input
			candidate.Cases = cases
			test.mutate(&candidate)
			_, err := buildEvaluationVariantMetadata(candidate)
			if !errors.Is(err, ErrEvaluationVariantMetadata) {
				t.Fatalf("error = %v, want ErrEvaluationVariantMetadata", err)
			}
			if strings.Contains(err.Error(), "bad run id") || strings.Contains(err.Error(), "password") || strings.Contains(err.Error(), "do-not-return") {
				t.Fatalf("error leaked input value: %v", err)
			}
		})
	}
}

func loadEvaluationMetadataCases(t *testing.T) evaluation.CaseSet {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("repository root: %v", err)
	}
	cases, err := evaluation.LoadCases(filepath.Join(root, "testdata", "evaluation", "context-efficiency.v1alpha2.json"))
	if err != nil {
		t.Fatalf("load evaluation cases: %v", err)
	}
	return cases
}

func containsString(values []string, want string) bool {
	index := sort.SearchStrings(values, want)
	return index < len(values) && values[index] == want
}
