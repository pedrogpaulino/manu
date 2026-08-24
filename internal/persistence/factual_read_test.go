package persistence

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/pedrogpaulino/manu/internal/fact"
	"github.com/pedrogpaulino/manu/internal/identity"
)

func TestReadFactualSnapshotValidatesBeforeBegin(t *testing.T) {
	repository, starter := newFactualSQLRepository()
	if _, err := repository.ReadFactualSnapshot(context.Background(), "not-a-uuid", testUUID(2), testUUID(3)); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("ReadFactualSnapshot() error = %v, want invalid input", err)
	}
	if starter.beginCalls != 0 {
		t.Fatalf("Begin calls = %d, want 0", starter.beginCalls)
	}
}

func TestReadFactualSnapshotReturnsNotFoundAndRedactsScopeErrors(t *testing.T) {
	organizationExternal := "read-org"
	sourceExternal := "read-source"
	snapshotExternal := "read-snapshot"
	organizationID := identity.CanonicalUUID("organization", organizationExternal)
	sourceID := identity.CanonicalUUID("source", organizationExternal, sourceExternal)
	snapshotID := identity.CanonicalUUID("snapshot", organizationExternal, sourceExternal, snapshotExternal)

	t.Run("not found", func(t *testing.T) {
		repository, starter := newFactualSQLRepository()
		starter.tx.queryRowList = []pgx.Row{fakeRow{err: pgx.ErrNoRows}}
		got, err := repository.ReadFactualSnapshot(context.Background(), organizationID, sourceID, snapshotID)
		if !errors.Is(err, ErrNotFound) || !isZeroFactualSnapshotInput(got) {
			t.Fatalf("result/error = %#v/%v, want empty/not found", got, err)
		}
		if starter.tx.commitCalls != 0 || starter.tx.rollbackCalls != 1 {
			t.Fatalf("commit/rollback = %d/%d, want 0/1", starter.tx.commitCalls, starter.tx.rollbackCalls)
		}
	})

	t.Run("redacted database error", func(t *testing.T) {
		repository, starter := newFactualSQLRepository()
		starter.tx.queryRowList = []pgx.Row{fakeRow{err: errors.New("secret factual payload")}}
		_, err := repository.ReadFactualSnapshot(context.Background(), organizationID, sourceID, snapshotID)
		if !errors.Is(err, ErrDatabase) || strings.Contains(err.Error(), "secret factual payload") {
			t.Fatalf("redacted error = %v", err)
		}
		if starter.tx.commitCalls != 0 || starter.tx.rollbackCalls != 1 {
			t.Fatalf("commit/rollback = %d/%d, want 0/1", starter.tx.commitCalls, starter.tx.rollbackCalls)
		}
	})
}

func TestReadFactualSnapshotRejectsManifestJSONInconsistency(t *testing.T) {
	organizationExternal := "read-manifest-org"
	sourceExternal := "read-manifest-source"
	snapshotExternal := "read-manifest-snapshot"
	organizationID := identity.CanonicalUUID("organization", organizationExternal)
	sourceID := identity.CanonicalUUID("source", organizationExternal, sourceExternal)
	snapshotID := identity.CanonicalUUID("snapshot", organizationExternal, sourceExternal, snapshotExternal)
	frontendID := identity.CanonicalUUID("frontend-manifest", organizationExternal, sourceExternal, snapshotExternal, "frontend", "1", "method")
	repository, starter := newFactualSQLRepository()
	starter.tx.queryRowList = []pgx.Row{fakeRow{values: []any{organizationExternal, sourceExternal, snapshotExternal, testSnapshot().CapturedAt}}}
	starter.tx.queryRows[readFactualManifestsSQL] = []pgx.Rows{&repositoryFakeRows{values: [][]any{{
		frontendID, organizationID, sourceID, snapshotID, "frontend", "v1alpha1", "frontend", "1", "method", "safe-static", []byte(`{}`), strings.Repeat("a", 64),
	}}}}

	_, err := repository.ReadFactualSnapshot(context.Background(), organizationID, sourceID, snapshotID)
	if !errors.Is(err, ErrInconsistent) {
		t.Fatalf("manifest corruption error = %v, want inconsistent", err)
	}
	if starter.tx.commitCalls != 0 || starter.tx.rollbackCalls != 1 {
		t.Fatalf("commit/rollback = %d/%d, want 0/1", starter.tx.commitCalls, starter.tx.rollbackCalls)
	}
}

func TestReadFactualSnapshotAcceptsEmptyFactualScope(t *testing.T) {
	organizationExternal := "read-empty-org"
	sourceExternal := "read-empty-source"
	snapshotExternal := "read-empty-snapshot"
	organizationID := identity.CanonicalUUID("organization", organizationExternal)
	sourceID := identity.CanonicalUUID("source", organizationExternal, sourceExternal)
	snapshotID := identity.CanonicalUUID("snapshot", organizationExternal, sourceExternal, snapshotExternal)
	repository, starter := newFactualSQLRepository()
	starter.tx.queryRowList = []pgx.Row{fakeRow{values: []any{organizationExternal, sourceExternal, snapshotExternal, testSnapshot().CapturedAt}}}

	got, err := repository.ReadFactualSnapshot(context.Background(), organizationID, sourceID, snapshotID)
	if err != nil {
		t.Fatalf("ReadFactualSnapshot() error = %v", err)
	}
	if got.OrganizationID != organizationID || got.SourceID != sourceID || got.SnapshotID != snapshotID ||
		got.Scope.OrganizationID != organizationExternal || len(got.FrontendManifests) != 0 || len(got.RuleVersions) != 0 || len(got.Facts) != 0 {
		t.Fatalf("empty factual snapshot = %#v", got)
	}
	if starter.tx.commitCalls != 1 || starter.tx.rollbackCalls != 0 {
		t.Fatalf("commit/rollback = %d/%d, want 1/0", starter.tx.commitCalls, starter.tx.rollbackCalls)
	}
}

func isZeroFactualSnapshotInput(input FactualSnapshotInput) bool {
	return input.OrganizationID == "" && input.SourceID == "" && input.SnapshotID == "" && input.Scope == (fact.Scope{}) &&
		len(input.FrontendManifests) == 0 && len(input.RuleVersions) == 0 && len(input.Facts) == 0
}
