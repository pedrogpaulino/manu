package persistence

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pedrogpaulino/manu/internal/evidence"
	"github.com/pedrogpaulino/manu/internal/identity"
	"github.com/pedrogpaulino/manu/internal/query"
)

type queryExecutionStarter struct {
	tx *queryExecutionTransaction
}

func (s *queryExecutionStarter) Begin(context.Context) (transaction, error) {
	return s.tx, nil
}

type queryExecutionTransaction struct {
	row           pgx.Row
	execs         []fakeExec
	queryRows     []fakeExec
	commitCalls   int
	rollbackCalls int
	committed     bool
}

func (t *queryExecutionTransaction) Exec(_ context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	t.execs = append(t.execs, fakeExec{query: query, args: append([]any(nil), args...)})
	return pgconn.NewCommandTag("INSERT 1"), nil
}

func (t *queryExecutionTransaction) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return &repositoryFakeRows{}, nil
}

func (t *queryExecutionTransaction) QueryRow(_ context.Context, query string, args ...any) pgx.Row {
	t.queryRows = append(t.queryRows, fakeExec{query: query, args: append([]any(nil), args...)})
	if t.row == nil {
		return queryExecutionRow{err: pgx.ErrNoRows}
	}
	return t.row
}

func (t *queryExecutionTransaction) Commit(context.Context) error {
	t.commitCalls++
	t.committed = true
	return nil
}

func (t *queryExecutionTransaction) Rollback(context.Context) error {
	t.rollbackCalls++
	return nil
}

type queryExecutionRow struct {
	values []any
	err    error
}

func (r queryExecutionRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(r.values) != len(dest) {
		return fmt.Errorf("query row values = %d, destinations = %d", len(r.values), len(dest))
	}
	for i := range r.values {
		if err := assignQueryExecutionValue(r.values[i], dest[i]); err != nil {
			return fmt.Errorf("query row value %d: %w", i, err)
		}
	}
	return nil
}

func assignQueryExecutionValue(value any, destination any) error {
	switch target := destination.(type) {
	case *string:
		converted, ok := value.(string)
		if value == nil || !ok {
			return fmt.Errorf("want string, got %T", value)
		}
		*target = converted
	case **string:
		if value == nil {
			*target = nil
			return nil
		}
		converted, ok := value.(string)
		if !ok {
			return fmt.Errorf("want nullable string, got %T", value)
		}
		copy := converted
		*target = &copy
	case *time.Time:
		converted, ok := value.(time.Time)
		if !ok {
			return fmt.Errorf("want time, got %T", value)
		}
		*target = converted
	case **time.Time:
		if value == nil {
			*target = nil
			return nil
		}
		converted, ok := value.(time.Time)
		if !ok {
			return fmt.Errorf("want nullable time, got %T", value)
		}
		copy := converted
		*target = &copy
	case *[]byte:
		if value == nil {
			*target = nil
			return nil
		}
		converted, ok := value.([]byte)
		if !ok {
			return fmt.Errorf("want bytes, got %T", value)
		}
		*target = append((*target)[:0], converted...)
	case *int64:
		converted, ok := value.(int64)
		if !ok {
			return fmt.Errorf("want int64, got %T", value)
		}
		*target = converted
	case *bool:
		converted, ok := value.(bool)
		if !ok {
			return fmt.Errorf("want bool, got %T", value)
		}
		*target = converted
	default:
		return fmt.Errorf("unsupported query row destination %T", destination)
	}
	return nil
}

func TestQueryExecutionRepositoryCreateCommitsBeforeReturn(t *testing.T) {
	const (
		organizationExternal = "local"
		question             = "which flow is observed?"
		queryID              = "00000000-0000-0000-0000-000000000101"
	)
	createdAt := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	digest := digestBytes([]byte(question))
	organizationID := identity.CanonicalUUID("organization", organizationExternal)
	tx := &queryExecutionTransaction{row: queryExecutionRow{values: []any{
		queryID, organizationID, nil, nil, question, digest, string(query.ExecutionStateAbstained),
		"pipeline_not_configured", createdAt, createdAt, createdAt,
	}}}
	service := newQueryExecutionRepositoryWithStarter(&queryExecutionStarter{tx: tx})
	service.now = func() time.Time { return createdAt }
	service.newID = func() (string, error) { return queryID, nil }

	got, err := service.Create(context.Background(), organizationExternal, query.ExecutionInput{
		Question: question, QuestionDigest: digest,
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if !tx.committed || tx.commitCalls != 1 || tx.rollbackCalls != 0 {
		t.Fatalf("Create() transaction state = committed %v, commits %d, rollbacks %d", tx.committed, tx.commitCalls, tx.rollbackCalls)
	}
	if got.ID != queryID || got.OrganizationID != organizationExternal || got.State != query.ExecutionStateAbstained {
		t.Fatalf("Create() = %#v", got)
	}
	if got.QuestionDigest != digest || got.DiagnosticCode != "pipeline_not_configured" || !got.FinishedAt.Equal(createdAt) {
		t.Fatalf("Create() persisted status = %#v", got)
	}
	if len(tx.execs) != 1 || !strings.Contains(tx.execs[0].query, "ON CONFLICT (external_id) DO NOTHING") {
		t.Fatalf("organization insert was not parameterized: %#v", tx.execs)
	}
	if len(tx.queryRows) != 1 || !strings.Contains(tx.queryRows[0].query, "$5") || strings.Contains(tx.queryRows[0].query, question) {
		t.Fatalf("query insert was not parameterized: %#v", tx.queryRows)
	}
	if tx.queryRows[0].args[0] != queryID || tx.queryRows[0].args[1] != organizationID || tx.queryRows[0].args[4] != question {
		t.Fatalf("query insert arguments = %#v", tx.queryRows[0].args)
	}
}

func TestQueryExecutionRepositoryGetIsOrganizationScoped(t *testing.T) {
	const question = "where is the error flow?"
	queryID := testUUID(101)
	createdAt := time.Date(2026, time.January, 3, 3, 4, 5, 0, time.UTC)
	digest := digestBytes([]byte(question))
	organizationID := identity.CanonicalUUID("organization", "local")

	t.Run("same organization commits read", func(t *testing.T) {
		tx := &queryExecutionTransaction{row: queryExecutionRow{values: []any{
			queryID, organizationID, nil, nil, question, digest, string(query.ExecutionStateAbstained),
			"pipeline_not_configured", createdAt, createdAt, createdAt,
		}}}
		service := newQueryExecutionRepositoryWithStarter(&queryExecutionStarter{tx: tx})
		got, err := service.Get(context.Background(), "local", queryID)
		if err != nil {
			t.Fatalf("Get() error = %v", err)
		}
		if got.ID != queryID || got.OrganizationID != "local" || !tx.committed {
			t.Fatalf("Get() = %#v, committed = %v", got, tx.committed)
		}
		if len(tx.queryRows) != 1 || tx.queryRows[0].args[0] != organizationID || tx.queryRows[0].args[1] != queryID {
			t.Fatalf("Get() scope arguments = %#v", tx.queryRows)
		}
	})

	t.Run("adapter rejects cross scope row", func(t *testing.T) {
		otherOrganizationID := identity.CanonicalUUID("organization", "other")
		tx := &queryExecutionTransaction{row: queryExecutionRow{values: []any{
			queryID, otherOrganizationID, nil, nil, question, digest, string(query.ExecutionStateCompleted),
			nil, createdAt, createdAt, createdAt,
		}}}
		service := newQueryExecutionRepositoryWithStarter(&queryExecutionStarter{tx: tx})
		got, err := service.Get(context.Background(), "local", queryID)
		if !errors.Is(err, ErrInconsistent) {
			t.Fatalf("Get() error = %v, want inconsistent scope", err)
		}
		if got.ID != "" || tx.committed || tx.rollbackCalls != 1 {
			t.Fatalf("cross-scope result = %#v, committed = %v, rollbacks = %d", got, tx.committed, tx.rollbackCalls)
		}
	})
}

func TestQueryExecutionRepositoryRejectsInvalidLifecycle(t *testing.T) {
	const question = "lifecycle?"
	queryID := testUUID(111)
	createdAt := time.Date(2026, time.January, 4, 3, 4, 5, 0, time.UTC)
	digest := digestBytes([]byte(question))
	organizationID := identity.CanonicalUUID("organization", "local")
	base := []any{queryID, organizationID, nil, nil, question, digest, "pending", nil, createdAt, nil, nil}
	tests := []struct {
		name   string
		mutate func([]any)
	}{
		{name: "pending with finished timestamp", mutate: func(values []any) {
			values[10] = createdAt
		}},
		{name: "running without started timestamp", mutate: func(values []any) {
			values[6] = "running"
		}},
		{name: "completed without durable result", mutate: func(values []any) {
			values[6] = "completed"
			values[10] = createdAt
		}},
		{name: "failed without diagnostic", mutate: func(values []any) {
			values[6] = "failed"
			values[10] = createdAt
		}},
		{name: "abstained without finished timestamp", mutate: func(values []any) {
			values[6] = "abstained"
			values[7] = "pipeline_not_configured"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			values := append([]any(nil), base...)
			test.mutate(values)
			tx := &queryExecutionTransaction{row: queryExecutionRow{values: values}}
			service := newQueryExecutionRepositoryWithStarter(&queryExecutionStarter{tx: tx})
			got, err := service.Get(context.Background(), "local", queryID)
			if !errors.Is(err, ErrInconsistent) {
				t.Fatalf("Get() error = %v, want inconsistent lifecycle", err)
			}
			if got.ID != "" || tx.committed || tx.rollbackCalls != 1 {
				t.Fatalf("invalid lifecycle result = %#v, committed = %v, rollbacks = %d", got, tx.committed, tx.rollbackCalls)
			}
		})
	}
}

func TestEvidenceInspectionRepositoryRejectsProhibitedContent(t *testing.T) {
	const organizationExternal = "local"
	organizationID := identity.CanonicalUUID("organization", organizationExternal)
	sourceID, snapshotID, artifactID := testUUID(201), testUUID(202), testUUID(203)
	evidenceID := testUUID(204)
	content := "a safe evidence excerpt"
	locator, _ := json.Marshal(map[string]any{
		"path": "src/main.go", "source_id": sourceID, "artifact_id": artifactID,
	})
	contentHash := evidence.ContentDigest(content)
	baseValues := []any{
		evidenceID, organizationID, sourceID, snapshotID, artifactID, nil,
		locator, "present", content, contentHash, int64(len(content)), int64(len([]rune(content))), false,
		string(evidence.ClassificationSafeText), []byte("[]"), string(evidence.DecisionAllow), string(evidence.DecisionAllow), nil,
	}

	t.Run("present content is returned after committed read", func(t *testing.T) {
		tx := &queryExecutionTransaction{row: queryExecutionRow{values: baseValues}}
		reader := newEvidenceInspectionRepositoryWithStarter(&queryExecutionStarter{tx: tx})
		got, err := reader.GetEvidence(context.Background(), organizationExternal, evidenceID)
		if err != nil {
			t.Fatalf("GetEvidence() error = %v", err)
		}
		if got.Content != content || got.ContentHash != contentHash || got.OrganizationID != organizationExternal || !tx.committed {
			t.Fatalf("GetEvidence() = %#v, committed = %v", got, tx.committed)
		}
	})

	t.Run("denied row carrying content is rejected before exposure", func(t *testing.T) {
		values := append([]any(nil), baseValues...)
		values[7] = "omitted"
		values[9] = evidence.ContentDigest("prohibited source")
		values[10] = int64(0)
		values[11] = int64(0)
		values[13] = string(evidence.ClassificationProhibited)
		values[14] = []byte(`["prohibited"]`)
		values[15] = string(evidence.DecisionDeny)
		values[16] = string(evidence.DecisionDeny)
		tx := &queryExecutionTransaction{row: queryExecutionRow{values: values}}
		reader := newEvidenceInspectionRepositoryWithStarter(&queryExecutionStarter{tx: tx})
		got, err := reader.GetEvidence(context.Background(), organizationExternal, evidenceID)
		if !errors.Is(err, ErrInconsistent) {
			t.Fatalf("GetEvidence() error = %v, want inconsistent row", err)
		}
		if got.Content != "" || tx.committed || tx.rollbackCalls != 1 {
			t.Fatalf("denied result = %#v, committed = %v, rollbacks = %d", got, tx.committed, tx.rollbackCalls)
		}
	})

	t.Run("cross organization row is rejected before content exposure", func(t *testing.T) {
		values := append([]any(nil), baseValues...)
		values[1] = identity.CanonicalUUID("organization", "other")
		tx := &queryExecutionTransaction{row: queryExecutionRow{values: values}}
		reader := newEvidenceInspectionRepositoryWithStarter(&queryExecutionStarter{tx: tx})
		got, err := reader.GetEvidence(context.Background(), organizationExternal, evidenceID)
		if !errors.Is(err, ErrInconsistent) {
			t.Fatalf("GetEvidence() error = %v, want inconsistent scope", err)
		}
		if got.Content != "" || tx.committed || tx.rollbackCalls != 1 {
			t.Fatalf("cross-scope result = %#v, committed = %v, rollbacks = %d", got, tx.committed, tx.rollbackCalls)
		}
	})
}
