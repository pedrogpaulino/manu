package persistence

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/pedrogpaulino/manu/internal/identity"
	"github.com/pedrogpaulino/manu/internal/query"
)

func TestPipelineQueryRepositoryResolveActiveScopeIsOrganizationScoped(t *testing.T) {
	const (
		organizationExternal = "local"
		sourceID             = "00000000-0000-0000-0000-000000000201"
		snapshotID           = "00000000-0000-0000-0000-000000000202"
	)
	organizationID := identity.CanonicalUUID("organization", organizationExternal)
	tx := &queryExecutionTransaction{row: queryExecutionRow{values: []any{sourceID, snapshotID}}}
	repository := newPipelineQueryRepositoryWithStarter(&queryExecutionStarter{tx: tx})

	scope, err := repository.ResolveActiveScope(context.Background(), organizationExternal, "")
	if err != nil {
		t.Fatalf("ResolveActiveScope() error = %v", err)
	}
	if scope != (query.Scope{OrganizationID: organizationID, SourceID: sourceID, SnapshotID: snapshotID}) {
		t.Fatalf("ResolveActiveScope() = %#v", scope)
	}
	if !tx.committed || tx.rollbackCalls != 0 {
		t.Fatalf("transaction state: committed=%v rollbacks=%d", tx.committed, tx.rollbackCalls)
	}
	if len(tx.queryRows) != 1 || !strings.Contains(tx.queryRows[0].query, "organization_id = $1::uuid") || strings.Contains(tx.queryRows[0].query, organizationExternal) {
		t.Fatalf("active scope query was not scoped/parameterized: %#v", tx.queryRows)
	}
	if len(tx.queryRows[0].args) != 1 || tx.queryRows[0].args[0] != organizationID {
		t.Fatalf("active scope query args = %#v", tx.queryRows[0].args)
	}
}

func TestPipelineQueryRepositoryResolveActiveScopeRequiresActiveSnapshot(t *testing.T) {
	tx := &queryExecutionTransaction{row: queryExecutionRow{err: pgx.ErrNoRows}}
	repository := newPipelineQueryRepositoryWithStarter(&queryExecutionStarter{tx: tx})

	_, err := repository.ResolveActiveScope(context.Background(), "local", "")
	if !errors.Is(err, query.ErrQueryScopeRequired) {
		t.Fatalf("ResolveActiveScope() error = %v, want missing active scope", err)
	}
	if tx.committed || tx.rollbackCalls != 1 {
		t.Fatalf("missing scope transaction state: committed=%v rollbacks=%d", tx.committed, tx.rollbackCalls)
	}
}

func TestPipelineQueryRepositoryFinishPersistsTerminalAuditBeforeReturn(t *testing.T) {
	const (
		organizationExternal = "local"
		question             = "what is in this snapshot?"
		queryID              = "00000000-0000-4000-8000-000000000301"
	)
	organizationID := identity.CanonicalUUID("organization", organizationExternal)
	scope := query.Scope{
		OrganizationID: organizationID,
		SourceID:       testUUID(302),
		SnapshotID:     testUUID(303),
	}
	questionDigest := digestBytes([]byte(question))
	composition, err := query.ComposeEvidencePackage(context.Background(), query.PackageRequest{Scope: scope})
	if err != nil {
		t.Fatalf("ComposeEvidencePackage() error = %v", err)
	}
	abstention, err := query.EvaluateAbstention(query.AbstentionInput{
		Package:      composition.ValidationPackage,
		QueryID:      queryID,
		QueryDigest:  questionDigest,
		QuestionKind: query.KnowledgeQuestionInventory,
		Support:      query.SupportAssessment{Kind: query.KnowledgeQuestionInventory, Level: query.EvidenceSupportNone},
	})
	if err != nil {
		t.Fatalf("EvaluateAbstention() error = %v", err)
	}
	createdAt := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	startedAt := createdAt.Add(time.Second)
	finishedAt := startedAt.Add(time.Second)
	tx := &queryExecutionTransaction{row: queryExecutionRow{values: []any{
		queryID, organizationID, scope.SourceID, scope.SnapshotID, question, questionDigest,
		string(query.ExecutionStateRunning), nil, createdAt, startedAt, nil,
	}}}
	repository := newPipelineQueryRepositoryWithStarter(&queryExecutionStarter{tx: tx})

	got, err := repository.Finish(context.Background(), organizationExternal, query.QueryOutcome{
		ExecutionID:    queryID,
		Input:          query.ExecutionInput{Question: question, QuestionKind: query.KnowledgeQuestionInventory, SourceID: scope.SourceID, SnapshotID: scope.SnapshotID, QuestionDigest: questionDigest},
		State:          query.ExecutionStateAbstained,
		QuestionDigest: questionDigest,
		PackageDigest:  composition.ValidationPackage.Digest,
		Response:       abstention.Response,
		HasResponse:    true,
		StartedAt:      startedAt,
		FinishedAt:     finishedAt,
		Composition:    composition,
	})
	if err != nil {
		t.Fatalf("Finish() error = %v", err)
	}
	if got.ID != queryID || got.State != query.ExecutionStateAbstained || len(got.Response) == 0 {
		t.Fatalf("Finish() = %#v", got)
	}
	var persistedResponse query.Response
	if err := json.Unmarshal(got.Response, &persistedResponse); err != nil {
		t.Fatalf("Finish() response JSON error = %v", err)
	}
	if len(persistedResponse.Claims) != 1 || persistedResponse.Claims[0].ID == "" {
		t.Fatalf("Finish() response claim ID = %#v", persistedResponse.Claims)
	}
	if !tx.committed || tx.commitCalls != 1 || tx.rollbackCalls != 0 {
		t.Fatalf("Finish() transaction state: committed=%v commits=%d rollbacks=%d", tx.committed, tx.commitCalls, tx.rollbackCalls)
	}
	var sawPackage, sawClaim, sawResult, sawTerminalUpdate bool
	for _, exec := range tx.execs {
		switch {
		case strings.Contains(exec.query, "INSERT INTO evidence_packages"):
			sawPackage = true
		case strings.Contains(exec.query, "INSERT INTO generated_claims"):
			sawClaim = true
		case strings.Contains(exec.query, "INSERT INTO query_results"):
			sawResult = true
		case strings.Contains(exec.query, "UPDATE queries"):
			sawTerminalUpdate = true
		}
	}
	if !sawPackage || !sawClaim || !sawResult || !sawTerminalUpdate {
		t.Fatalf("Finish() did not persist complete terminal audit: package=%v claim=%v result=%v update=%v", sawPackage, sawClaim, sawResult, sawTerminalUpdate)
	}
	if len(tx.queryRows) != 1 || strings.Contains(tx.queryRows[0].query, question) {
		t.Fatalf("Finish() query lock was not parameterized: %#v", tx.queryRows)
	}
}
