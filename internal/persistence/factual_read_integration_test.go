//go:build integration

package persistence_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/pedrogpaulino/manu/internal/persistence"
)

func TestReadFactualSnapshotRoundTripsPersistedFactsAndLineage(t *testing.T) {
	database := openPostgresIntegrationDatabase(t)
	fixture := integrationFixture(t, "factual-read", "snapshot-1", "class FactualRead {}")
	repository := persistence.NewRepository(database.pool)
	if _, err := repository.PersistBundle(context.Background(), fixture.input); err != nil {
		t.Fatalf("seed legacy bundle: %v", err)
	}
	want := factualRepositoryInput(t, fixture, "evidence-present")
	if err := repository.PersistFactualSnapshot(context.Background(), want); err != nil {
		t.Fatalf("persist factual fixture: %v", err)
	}
	wantPrepared, err := persistence.PrepareFactualSnapshot(want)
	if err != nil {
		t.Fatalf("prepare expected factual fixture: %v", err)
	}

	got, err := repository.ReadFactualSnapshot(context.Background(), want.OrganizationID, want.SourceID, want.SnapshotID)
	if err != nil {
		t.Fatalf("ReadFactualSnapshot() error = %v", err)
	}
	gotPrepared, err := persistence.PrepareFactualSnapshot(got)
	if err != nil {
		t.Fatalf("prepare read factual fixture: %v", err)
	}
	if !reflect.DeepEqual(gotPrepared, wantPrepared) {
		t.Fatalf("read factual snapshot differs from persisted preparation:\n got %#v\nwant %#v", gotPrepared, wantPrepared)
	}
	derived := 0
	for _, candidate := range got.Facts {
		if candidate.Lineage != nil {
			derived++
		}
	}
	if len(got.RuleVersions) != 1 || len(got.Facts) != 2 || derived != 1 {
		t.Fatalf("read factual lineage/rules = rules %d facts %d derived %d", len(got.RuleVersions), len(got.Facts), derived)
	}
}

func TestReadFactualSnapshotRejectsMissingOrCorruptRowsSafely(t *testing.T) {
	database := openPostgresIntegrationDatabase(t)
	fixture := integrationFixture(t, "factual-read-corrupt", "snapshot-1", "class FactualReadCorrupt {}")
	repository := persistence.NewRepository(database.pool)
	if _, err := repository.PersistBundle(context.Background(), fixture.input); err != nil {
		t.Fatalf("seed legacy bundle: %v", err)
	}
	want := factualRepositoryInput(t, fixture, "evidence-present")
	if err := repository.PersistFactualSnapshot(context.Background(), want); err != nil {
		t.Fatalf("persist factual fixture: %v", err)
	}

	missing, err := repository.ReadFactualSnapshot(context.Background(), want.OrganizationID, want.SourceID, "00000000-0000-0000-0000-000000000099")
	if !errors.Is(err, persistence.ErrNotFound) || !reflect.DeepEqual(missing, persistence.FactualSnapshotInput{}) {
		t.Fatalf("missing snapshot result/error = %#v/%v, want empty/not found", missing, err)
	}

	_, err = database.pool.Exec(context.Background(), `
UPDATE canonical_fact_qualifiers
SET ordinal = 9
WHERE organization_id = $1::uuid AND source_id = $2::uuid AND snapshot_id = $3::uuid`,
		want.OrganizationID, want.SourceID, want.SnapshotID)
	if err != nil {
		t.Fatalf("corrupt qualifier row: %v", err)
	}
	if _, err := repository.ReadFactualSnapshot(context.Background(), want.OrganizationID, want.SourceID, want.SnapshotID); !errors.Is(err, persistence.ErrInconsistent) {
		t.Fatalf("corrupt factual read error = %v, want inconsistent", err)
	} else if len(err.Error()) > 160 {
		t.Fatalf("corrupt factual read error is too detailed: %q", err)
	}
}
