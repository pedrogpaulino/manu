package persistence

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/pedrogpaulino/manu/internal/fact"
	"github.com/pedrogpaulino/manu/internal/identity"
)

func TestRebuildFactualProjectionValidatesBeforeBegin(t *testing.T) {
	repository, starter := newFactualSQLRepository()
	if got, err := repository.RebuildFactualProjection(context.Background(), "not-a-uuid", testUUID(2), testUUID(3)); !errors.Is(err, ErrInvalidInput) || !reflect.DeepEqual(got, FactualProjectionRebuildResult{}) {
		t.Fatalf("result/error = %#v/%v, want zero/invalid input", got, err)
	}
	if starter.beginCalls != 0 {
		t.Fatalf("Begin calls = %d, want 0", starter.beginCalls)
	}
}

func TestRebuildFactualProjectionReadsDeletesAndInsertsInOneTransaction(t *testing.T) {
	repository, starter := newFactualSQLRepository()
	input := factualProjectionInput(t)
	capturedAt := time.Date(2026, time.August, 23, 19, 20, 21, 0, time.UTC)
	configureFactualProjectionRead(t, starter, input, capturedAt)

	wantProjection, err := PrepareFactualProjection(input)
	if err != nil {
		t.Fatalf("prepare expected projection: %v", err)
	}
	got, err := repository.RebuildFactualProjection(context.Background(), input.OrganizationID, input.SourceID, input.SnapshotID)
	if err != nil {
		t.Fatalf("RebuildFactualProjection() error = %v", err)
	}
	if got != (FactualProjectionRebuildResult{EntityCount: len(wantProjection.Entities), RelationshipCount: len(wantProjection.Relationships)}) {
		t.Fatalf("rebuild result = %#v, want entities=%d relationships=%d", got, len(wantProjection.Entities), len(wantProjection.Relationships))
	}
	if starter.tx.commitCalls != 1 || starter.tx.rollbackCalls != 0 {
		t.Fatalf("commit/rollback = %d/%d, want 1/0", starter.tx.commitCalls, starter.tx.rollbackCalls)
	}
	if len(starter.tx.queries) == 0 {
		t.Fatal("rebuild did not read factual rows")
	}
	if len(starter.tx.execs) != 2+len(wantProjection.Entities)+len(wantProjection.Relationships) {
		t.Fatalf("exec count = %d, want %d", len(starter.tx.execs), 2+len(wantProjection.Entities)+len(wantProjection.Relationships))
	}
	if !strings.Contains(starter.tx.execs[0], "DELETE FROM relationships") || !strings.Contains(starter.tx.execs[1], "DELETE FROM entities") {
		t.Fatalf("delete order = %q then %q", starter.tx.execs[0], starter.tx.execs[1])
	}
	if !reflect.DeepEqual(starter.tx.execArgs[0], []any{input.OrganizationID, input.SourceID, input.SnapshotID}) ||
		!reflect.DeepEqual(starter.tx.execArgs[1], []any{input.OrganizationID, input.SourceID, input.SnapshotID}) {
		t.Fatalf("delete arguments are not exactly scoped: %#v/%#v", starter.tx.execArgs[0], starter.tx.execArgs[1])
	}
	for index := 2; index < 2+len(wantProjection.Entities); index++ {
		if !strings.Contains(starter.tx.execs[index], "INSERT INTO entities") {
			t.Fatalf("exec %d = %q, want entity insert", index, starter.tx.execs[index])
		}
	}
	for index := 2 + len(wantProjection.Entities); index < len(starter.tx.execs); index++ {
		if !strings.Contains(starter.tx.execs[index], "INSERT INTO relationships") {
			t.Fatalf("exec %d = %q, want relationship insert", index, starter.tx.execs[index])
		}
	}
}

func TestRebuildFactualProjectionRollsBackAfterDeleteAndRedactsDatabaseError(t *testing.T) {
	repository, starter := newFactualSQLRepository()
	input := factualProjectionInput(t)
	configureFactualProjectionRead(t, starter, input, time.Date(2026, time.August, 23, 19, 20, 21, 0, time.UTC))
	starter.tx.execErrorAt = 3

	got, err := repository.RebuildFactualProjection(context.Background(), input.OrganizationID, input.SourceID, input.SnapshotID)
	if !errors.Is(err, ErrDatabase) || !reflect.DeepEqual(got, FactualProjectionRebuildResult{}) {
		t.Fatalf("result/error = %#v/%v, want zero/database", got, err)
	}
	if starter.tx.commitCalls != 0 || starter.tx.rollbackCalls != 1 {
		t.Fatalf("commit/rollback = %d/%d, want 0/1", starter.tx.commitCalls, starter.tx.rollbackCalls)
	}
	if !strings.Contains(starter.tx.execs[0], "DELETE FROM relationships") || !strings.Contains(starter.tx.execs[1], "DELETE FROM entities") {
		t.Fatalf("delete order before failure = %#v", starter.tx.execs)
	}
	if strings.Contains(err.Error(), "secret factual database detail") {
		t.Fatalf("database detail leaked: %v", err)
	}
}

func TestRebuildFactualProjectionEmptySnapshotDeletesOnlyProjection(t *testing.T) {
	repository, starter := newFactualSQLRepository()
	input := factualProjectionInput(t)
	input.FrontendManifests = nil
	input.Facts = nil
	configureFactualProjectionRead(t, starter, input, time.Date(2026, time.August, 23, 19, 20, 21, 0, time.UTC))

	got, err := repository.RebuildFactualProjection(context.Background(), input.OrganizationID, input.SourceID, input.SnapshotID)
	if err != nil {
		t.Fatalf("empty rebuild error = %v", err)
	}
	if got != (FactualProjectionRebuildResult{}) || len(starter.tx.execs) != 2 {
		t.Fatalf("empty rebuild result/execs = %#v/%d, want zero/two deletes", got, len(starter.tx.execs))
	}
	if starter.tx.commitCalls != 1 || starter.tx.rollbackCalls != 0 {
		t.Fatalf("commit/rollback = %d/%d, want 1/0", starter.tx.commitCalls, starter.tx.rollbackCalls)
	}
}

func configureFactualProjectionRead(t *testing.T, starter *factualSQLStarter, input FactualSnapshotInput, capturedAt time.Time) {
	t.Helper()
	prepared, err := PrepareFactualSnapshot(input)
	if err != nil {
		t.Fatalf("prepare factual read fixture: %v", err)
	}
	organizationID := input.OrganizationID
	sourceID := input.SourceID
	snapshotID := input.SnapshotID
	starter.tx.queryRowList = []pgx.Row{fakeRow{values: []any{input.Scope.OrganizationID, input.Scope.SourceID, input.Scope.SnapshotID, capturedAt}}}
	starter.tx.queryRows = make(map[string][]pgx.Rows)
	starter.tx.queryRows[readFactualManifestsSQL] = []pgx.Rows{&repositoryFakeRows{values: factualProjectionManifestRows(prepared, organizationID, sourceID, snapshotID)}}
	starter.tx.queryRows[readFactualFactsSQL] = []pgx.Rows{&repositoryFakeRows{values: factualProjectionFactRows(prepared, organizationID, sourceID, snapshotID, capturedAt)}}
	starter.tx.queryRows[readFactualEvidenceSQL] = []pgx.Rows{&repositoryFakeRows{values: factualProjectionEvidenceRows(prepared, input.Scope)}}
}

func factualProjectionManifestRows(prepared PreparedFactualSnapshot, organizationID, sourceID, snapshotID string) [][]any {
	rows := make([][]any, 0, len(prepared.FrontendManifests))
	for _, manifest := range prepared.FrontendManifests {
		rows = append(rows, []any{
			manifest.ID, organizationID, sourceID, snapshotID,
			manifest.ExternalID, manifest.Manifest.ManifestVersion, manifest.Manifest.ID,
			manifest.Manifest.Version, manifest.Manifest.Method, string(manifest.Manifest.Execution),
			[]byte(manifest.CanonicalJSON), manifest.Digest,
		})
	}
	return rows
}

func factualProjectionFactRows(prepared PreparedFactualSnapshot, organizationID, sourceID, snapshotID string, capturedAt time.Time) [][]any {
	rows := make([][]any, 0, len(prepared.Facts))
	for _, item := range prepared.Facts {
		var objectKind, objectID, typedValue, frontendManifestID, ruleVersionID any
		if item.Fact.Object != nil {
			objectKind, objectID = string(item.Fact.Object.Kind), item.Fact.Object.ID
		}
		if item.Fact.Value != nil {
			encoded, err := factualFactValueJSON(item.Fact)
			if err != nil {
				panic(err)
			}
			typedValue = encoded
		}
		if item.FrontendManifestID != "" {
			frontendManifestID = item.FrontendManifestID
		}
		if item.RuleVersionID != "" {
			ruleVersionID = item.RuleVersionID
		}
		rows = append(rows, []any{
			item.ID, organizationID, sourceID, snapshotID, item.IdentityKey, item.Fact.Version,
			item.Kind, string(item.Fact.Predicate), string(item.Fact.Subject.Kind), item.Fact.Subject.ID,
			objectKind, objectID, typedValue, frontendManifestID, item.Fact.Producer.ID,
			item.Fact.Producer.Version, item.Fact.Producer.Method, ruleVersionID, capturedAt,
		})
	}
	return rows
}

func factualProjectionEvidenceRows(prepared PreparedFactualSnapshot, scope fact.Scope) [][]any {
	rows := make([][]any, 0)
	for _, item := range prepared.Facts {
		for ordinal, evidence := range item.Evidence {
			var reference fact.EvidenceRef
			for _, candidate := range item.Fact.Evidence {
				if candidate.ID == evidence.ExternalID {
					reference = candidate
					break
				}
			}
			locator, _ := json.Marshal(reference.Locator)
			artifactID := identity.CanonicalUUID("artifact", scope.OrganizationID, scope.SourceID, scope.SnapshotID, reference.Locator.ArtifactID)
			rows = append(rows, []any{
				evidence.ID, identity.CanonicalUUID("organization", scope.OrganizationID),
				identity.CanonicalUUID("source", scope.OrganizationID, scope.SourceID),
				identity.CanonicalUUID("snapshot", scope.OrganizationID, scope.SourceID, scope.SnapshotID),
				evidence.FactID, evidence.EvidenceUnitID, int64(ordinal),
				identity.CanonicalUUID("organization", scope.OrganizationID),
				identity.CanonicalUUID("source", scope.OrganizationID, scope.SourceID),
				identity.CanonicalUUID("snapshot", scope.OrganizationID, scope.SourceID, scope.SnapshotID),
				evidence.ExternalID, artifactID, reference.Locator.ArtifactID, locator,
			})
		}
	}
	sort.Slice(rows, func(left, right int) bool {
		leftFactID, rightFactID := rows[left][4].(string), rows[right][4].(string)
		if leftFactID != rightFactID {
			return leftFactID < rightFactID
		}
		return rows[left][6].(int64) < rows[right][6].(int64)
	})
	return rows
}
