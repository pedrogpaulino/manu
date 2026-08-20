package docs

import (
	"bytes"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"io"
	"strconv"
	"strings"
	"testing"
)

// evaluationBaseline is kept as an embedded, versioned artifact so the
// structural safety checks do not depend on a developer's working directory.
// The test deliberately validates metadata and invariants, not source content.
//
//go:embed evaluation/first-vertical-slice-9-5.json
var evaluationBaseline []byte

func TestEvaluationBaselineIsVersionedSafeAndReproducible(t *testing.T) {
	document := decodeEvaluationBaseline(t)
	if document["version"] != "v1alpha1" {
		t.Fatalf("baseline version = %v, want v1alpha1", document["version"])
	}
	if document["baseline_id"] != "first-vertical-slice-9-5-20260820" {
		t.Fatalf("unexpected baseline identity: %v", document["baseline_id"])
	}

	sourceRun := baselineObject(t, document, "source_run")
	validateDigest(t, "source_run.runner_reproducibility_digest", baselineString(t, sourceRun, "runner_reproducibility_digest"))
	if sourceRun["mode"] != "simulated" {
		t.Fatalf("source run mode = %v, want simulated", sourceRun["mode"])
	}

	metrics := baselineObject(t, document, "metrics")
	summary := baselineObject(t, metrics, "summary")
	assertBaselineNumber(t, summary, "cases", 4)
	assertBaselineNumber(t, summary, "expected_abstentions", 1)
	assertBaselineNumber(t, summary, "correct_abstentions", 1)
	assertBaselineNumber(t, summary, "evidence_recall_at_k_mean", 0)
	assertBaselineNumber(t, summary, "valid_claims", 4)
	assertBaselineNumber(t, summary, "valid_citations", 0)

	cases, ok := metrics["cases"].([]any)
	if !ok || len(cases) != 4 {
		t.Fatalf("baseline cases = %v, want four case records", metrics["cases"])
	}
	for _, rawCase := range cases {
		caseRecord, ok := rawCase.(map[string]any)
		if !ok {
			t.Fatalf("case record has unexpected type %T", rawCase)
		}
		if caseRecord["policy_status"] != "limited" || caseRecord["policy_code"] != "transfer_not_authorized" {
			t.Fatalf("case policy must preserve the transfer limitation: %v", caseRecord)
		}
	}

	transfer := baselineObject(t, document, "content_transfer")
	if transfer["policy"] != "deny" || transfer["secret_present"] != false || transfer["raw_content_recorded"] != false {
		t.Fatalf("baseline transfer policy is unsafe: %v", transfer)
	}
	assertEmptyBaselineList(t, transfer, "transferred_evidence")
	assertEmptyBaselineList(t, transfer, "transferred_content_hashes")
	assertBaselineNumber(t, transfer, "external_provider_calls", 0)

	profiles := baselineObject(t, document, "profiles")
	assertEmptyBaselineList(t, profiles, "external")
	costs := baselineObject(t, document, "costs")
	externalCost := baselineObject(t, costs, "external_provider")
	if externalCost["status"] != "not_applicable" || externalCost["calls"] != float64(0) {
		t.Fatalf("external cost must be explicitly not applicable: %v", externalCost)
	}

	timings := baselineObject(t, metrics, "timing_observations_ms")
	if timings["integrity"] != "excluded_from_reproducibility_digest" {
		t.Fatalf("timing observations must not affect reproducibility: %v", timings["integrity"])
	}
	integrity := baselineObject(t, document, "integrity")
	if integrity["raw_source_content_included"] != false || integrity["credentials_included"] != false {
		t.Fatalf("baseline integrity must exclude source content and credentials: %v", integrity)
	}
	volatile, ok := integrity["volatile_fields_excluded"].([]any)
	if !ok || len(volatile) == 0 {
		t.Fatalf("baseline must list volatile fields excluded from integrity: %v", integrity)
	}
	validateDigest(t, "integrity.runner_reproducibility_digest", baselineString(t, integrity, "runner_reproducibility_digest"))
	comparability := baselineObject(t, document, "comparability")
	if comparability["status"] != "single_baseline" || comparability["model_comparison"] != "not_performed" {
		t.Fatalf("baseline must not claim an invalid model comparison: %v", comparability)
	}

	configuration := baselineObject(t, document, "configuration")
	corpora, ok := configuration["corpora"].([]any)
	if !ok || len(corpora) != 3 {
		t.Fatalf("baseline corpora = %v, want three source records", configuration["corpora"])
	}
	for _, rawCorpus := range corpora {
		corpus, ok := rawCorpus.(map[string]any)
		if !ok || baselineString(t, corpus, "source_id") == "" || baselineString(t, corpus, "source_revision") == "" {
			t.Fatalf("corpus identity is incomplete: %v", rawCorpus)
		}
		if corpus["source_id"] == "wso2-car-sample" {
			hashes := baselineObject(t, corpus, "artifact_hashes")
			if len(hashes) != 6 {
				t.Fatalf("WSO2 selected artifact hash count = %d, want 6", len(hashes))
			}
			for name, rawHash := range hashes {
				hash, ok := rawHash.(string)
				if !ok {
					t.Fatalf("WSO2 hash for %s has type %T", name, rawHash)
				}
				validateDigest(t, "WSO2 artifact "+name, hash)
			}
		}
	}

	assertBaselineStringsAreSafe(t, document, "baseline")
}

func decodeEvaluationBaseline(t *testing.T) map[string]any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(evaluationBaseline))
	var document map[string]any
	if err := decoder.Decode(&document); err != nil {
		t.Fatalf("decode evaluation baseline: %v", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		t.Fatalf("evaluation baseline contains trailing JSON: %v", err)
	}
	return document
}

func baselineObject(t *testing.T, parent map[string]any, key string) map[string]any {
	t.Helper()
	value, ok := parent[key].(map[string]any)
	if !ok {
		t.Fatalf("baseline field %s is not an object: %T", key, parent[key])
	}
	return value
}

func baselineString(t *testing.T, parent map[string]any, key string) string {
	t.Helper()
	value, ok := parent[key].(string)
	if !ok {
		t.Fatalf("baseline field %s is not a string: %T", key, parent[key])
	}
	return value
}

func assertBaselineNumber(t *testing.T, parent map[string]any, key string, want float64) {
	t.Helper()
	value, ok := parent[key].(float64)
	if !ok || value != want {
		t.Fatalf("baseline field %s = %v, want %v", key, parent[key], want)
	}
}

func assertEmptyBaselineList(t *testing.T, parent map[string]any, key string) {
	t.Helper()
	value, ok := parent[key].([]any)
	if !ok || len(value) != 0 {
		t.Fatalf("baseline field %s must be an empty list: %v", key, parent[key])
	}
}

func validateDigest(t *testing.T, name, value string) {
	t.Helper()
	if len(value) != 64 {
		t.Fatalf("%s has digest length %d, want 64", name, len(value))
	}
	if _, err := hex.DecodeString(value); err != nil {
		t.Fatalf("%s is not a hexadecimal digest: %v", name, err)
	}
}

func assertBaselineStringsAreSafe(t *testing.T, value any, path string) {
	t.Helper()
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			assertBaselineStringsAreSafe(t, child, path+"."+key)
		}
	case []any:
		for index, child := range typed {
			assertBaselineStringsAreSafe(t, child, path+"["+strconv.Itoa(index)+"]")
		}
	case string:
		lower := strings.ToLower(typed)
		for _, forbidden := range []string{
			"/home/", "/root/", "file://", "sk-", "begin private key", "bearer ",
			"openai_api_key", "openrouter_api_key",
		} {
			if strings.Contains(lower, forbidden) {
				t.Fatalf("baseline field %s contains forbidden material %q", path, forbidden)
			}
		}
	}
}
