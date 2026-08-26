//go:build integration

package persistence_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/pedrogpaulino/manu/internal/identity"
	"github.com/pedrogpaulino/manu/internal/persistence"
	domainquery "github.com/pedrogpaulino/manu/internal/query"
)

func TestReadContextSnapshotRoundTripsCanonicalFactualState(t *testing.T) {
	database := openPostgresIntegrationDatabase(t)
	fixture := integrationFixture(t, "context-snapshot", "snapshot-1", "class ContextSnapshot {}")
	repository := persistence.NewRepository(database.pool)
	if _, err := repository.PersistBundle(context.Background(), fixture.input); err != nil {
		t.Fatalf("seed canonical bundle: %v", err)
	}
	input := factualRepositoryInput(t, fixture, "evidence-present")
	if err := repository.PersistFactualSnapshot(context.Background(), input); err != nil {
		t.Fatalf("persist factual snapshot: %v", err)
	}
	wantFactual, err := repository.ReadFactualSnapshot(context.Background(), input.OrganizationID, input.SourceID, input.SnapshotID)
	if err != nil {
		t.Fatalf("read expected factual snapshot: %v", err)
	}
	scope := domainquery.Scope{
		OrganizationID: input.OrganizationID,
		SourceID:       input.SourceID,
		SnapshotID:     input.SnapshotID,
	}

	got, err := repository.ReadContextSnapshot(context.Background(), scope)
	if err != nil {
		t.Fatalf("ReadContextSnapshot() error = %v", err)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("ReadContextSnapshot() result validation error = %v", err)
	}
	if got.Scope != scope {
		t.Fatalf("context snapshot scope = %#v, want %#v", got.Scope, scope)
	}
	if got.Revision != "revision-snapshot-1" {
		t.Fatalf("context snapshot revision = %q, want %q", got.Revision, "revision-snapshot-1")
	}
	if !reflect.DeepEqual(got.Facts, wantFactual.Facts) {
		t.Fatalf("context snapshot facts differ from factual read:\n got %#v\nwant %#v", got.Facts, wantFactual.Facts)
	}
	if len(got.Coverage) != 0 || len(got.Gaps) != 0 {
		t.Fatalf("context snapshot coverage/gaps = %d/%d, want 0/0", len(got.Coverage), len(got.Gaps))
	}

	got.Facts[0].Subject.ID = "mutated-context-snapshot-subject"
	if len(got.Facts[0].Qualifiers) > 0 {
		got.Facts[0].Qualifiers[0].Value.String = "mutated-context-snapshot-qualifier"
	}
	again, err := repository.ReadContextSnapshot(context.Background(), scope)
	if err != nil {
		t.Fatalf("ReadContextSnapshot() after mutation error = %v", err)
	}
	if !reflect.DeepEqual(again.Facts, wantFactual.Facts) {
		t.Fatalf("context snapshot read was affected by caller mutation:\n got %#v\nwant %#v", again.Facts, wantFactual.Facts)
	}
}

func TestReadContextSnapshotReturnsNotFoundWithoutScopeLeak(t *testing.T) {
	database := openPostgresIntegrationDatabase(t)
	fixture := integrationFixture(t, "context-snapshot-missing", "snapshot-1", "class ContextSnapshotMissing {}")
	repository := persistence.NewRepository(database.pool)
	if _, err := repository.PersistBundle(context.Background(), fixture.input); err != nil {
		t.Fatalf("seed canonical bundle: %v", err)
	}
	missingSnapshotID := identity.CanonicalUUID("snapshot", fixture.organizationID, fixture.sourceID, "missing-snapshot")
	scope := domainquery.Scope{
		OrganizationID: fixture.job.OrganizationID,
		SourceID:       fixture.job.SourceID,
		SnapshotID:     missingSnapshotID,
	}

	got, err := repository.ReadContextSnapshot(context.Background(), scope)
	if !errors.Is(err, persistence.ErrNotFound) {
		t.Fatalf("ReadContextSnapshot() error = %v, want ErrNotFound", err)
	}
	if !reflect.DeepEqual(got, domainquery.ContextSnapshot{}) {
		t.Fatalf("missing context snapshot = %#v, want zero value", got)
	}
	for _, secret := range []string{missingSnapshotID, fixture.organizationID, fixture.sourceID} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("not-found error leaked scope value %q: %v", secret, err)
		}
	}
}
