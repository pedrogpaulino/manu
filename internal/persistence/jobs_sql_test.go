package persistence

import (
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/pedrogpaulino/manu/internal/ingestion"
)

func TestJobSQLKeepsClaimScopedAndStageDurable(t *testing.T) {
	queries := map[string]string{
		"claim":           claimJobSQL,
		"create":          insertJobSQL,
		"heartbeat":       heartbeatJobSQL,
		"advance":         advanceStageSQL,
		"complete":        completeJobSQL,
		"partial":         partialJobSQL,
		"resume partial":  resumePartialJobSQL,
		"fail":            failJobSQL,
		"cancel":          cancelJobSQL,
		"create stage":    insertJobStageSQL,
		"start stage":     startJobStageSQL,
		"refresh stage":   refreshJobStageSQL,
		"heartbeat stage": heartbeatJobStageSQL,
		"finish stage":    completeJobStageSQL,
	}
	wantParameters := map[string]int{
		"claim":           4,
		"create":          8,
		"heartbeat":       5,
		"advance":         9,
		"complete":        9,
		"partial":         12,
		"resume partial":  4,
		"fail":            7,
		"cancel":          3,
		"create stage":    6,
		"start stage":     6,
		"refresh stage":   7,
		"heartbeat stage": 5,
		"finish stage":    9,
	}
	for name, query := range queries {
		assertSQLParameterShape(t, name, query, wantParameters[name])
	}
	assertSQLParameterOrder(t, "advance", advanceStageSQL, []string{
		"stage = $4", "artifact_count = $5", "observation_count = $6", "evidence_count = $7", "failure_count = $8",
	})
	assertSQLParameterOrder(t, "complete", completeJobSQL, []string{
		"artifact_count = $4", "observation_count = $5", "evidence_count = $6", "failure_count = $7", "finished_at = $8",
	})
	assertSQLParameterOrder(t, "partial", partialJobSQL, []string{
		"stage = $4", "artifact_count = $5", "observation_count = $6", "evidence_count = $7", "failure_count = $8",
		"diagnostic_code = $9", "diagnostic_message = $10", "finished_at = $11",
	})
	if !strings.Contains(resumePartialJobSQL, "state = 'partial'") ||
		!strings.Contains(resumePartialJobSQL, "stage = 'embedding_projection'") ||
		!strings.Contains(resumePartialJobSQL, "cancel_requested = false") {
		t.Fatal("partial resume SQL must be explicitly scoped to resumable embedding jobs")
	}
	for name, query := range map[string]string{
		"claim":     claimJobSQL,
		"create":    insertJobSQL,
		"heartbeat": heartbeatJobSQL,
		"advance":   advanceStageSQL,
		"complete":  completeJobSQL,
		"partial":   partialJobSQL,
		"fail":      failJobSQL,
		"cancel":    cancelJobSQL,
	} {
		if !strings.Contains(query, "ingestion_jobs") {
			t.Errorf("%s query does not target ingestion_jobs", name)
		}
	}
	for name, query := range map[string]string{
		"create stage":    insertJobStageSQL,
		"start stage":     startJobStageSQL,
		"heartbeat stage": heartbeatJobStageSQL,
		"finish stage":    completeJobStageSQL,
	} {
		if !strings.Contains(query, "ingestion_job_stages") {
			t.Errorf("%s query does not target ingestion_job_stages", name)
		}
	}
	if !strings.Contains(claimJobSQL, "organization_id = $1") || !strings.Contains(claimJobSQL, "FOR UPDATE SKIP LOCKED") {
		t.Fatal("claim SQL must scope organization and lock candidates")
	}
	if !strings.Contains(completeJobSQL, "stage = 'activation'") || !strings.Contains(startJobStageSQL, "state = 'running'") {
		t.Fatal("completion SQL must require activation and stage start must be running")
	}
	if !strings.Contains(insertJobSQL, "ON CONFLICT") || !strings.Contains(insertJobStageSQL, "ON CONFLICT") {
		t.Fatal("create SQL must be retry-safe")
	}
}

func assertSQLParameterOrder(t *testing.T, name, query string, fragments []string) {
	t.Helper()
	position := 0
	for _, fragment := range fragments {
		index := strings.Index(query[position:], fragment)
		if index < 0 {
			t.Errorf("%s omits ordered fragment %q", name, fragment)
			return
		}
		position += index + len(fragment)
	}
}

func assertSQLParameterShape(t *testing.T, name, query string, wantMax int) {
	t.Helper()
	seen := make(map[int]bool)
	for _, match := range regexp.MustCompile(`\$(\d+)`).FindAllStringSubmatch(query, -1) {
		parameter, err := strconv.Atoi(match[1])
		if err != nil {
			t.Fatalf("%s parameter %q: %v", name, match[0], err)
		}
		seen[parameter] = true
	}
	for parameter := 1; parameter <= wantMax; parameter++ {
		if !seen[parameter] {
			t.Errorf("%s omits SQL parameter $%d", name, parameter)
		}
	}
	for parameter := range seen {
		if parameter > wantMax {
			t.Errorf("%s has unexpected SQL parameter $%d (want max $%d)", name, parameter, wantMax)
		}
	}
}

func TestJobStageIDIsDeterministicAndStageSpecific(t *testing.T) {
	organizationID := "00000000-0000-4000-8000-000000000001"
	jobID := "00000000-0000-4000-8000-000000000004"
	first := jobStageID(organizationID, jobID, ingestion.JobStageValidation)
	if first != jobStageID(organizationID, jobID, ingestion.JobStageValidation) {
		t.Fatal("stage ID is not deterministic")
	}
	if first == jobStageID(organizationID, jobID, ingestion.JobStageActivation) {
		t.Fatal("different stages share a stage ID")
	}
	if len(first) != 36 || first[14] != '5' || first[19] != '8' && first[19] != '9' && first[19] != 'a' && first[19] != 'b' && first[19] != 'A' && first[19] != 'B' {
		t.Fatalf("stage ID is not UUID-shaped: %q", first)
	}
}
