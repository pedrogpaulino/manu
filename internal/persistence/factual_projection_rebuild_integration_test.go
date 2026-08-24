//go:build integration

package persistence_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/pedrogpaulino/manu/internal/identity"
	"github.com/pedrogpaulino/manu/internal/persistence"
)

func TestRebuildFactualProjectionIntegrationReplacesAndRepeats(t *testing.T) {
	database := openPostgresIntegrationDatabase(t)
	repository := persistence.NewRepository(database.pool)
	first := integrationFixture(t, "factual-projection-rebuild-first", "snapshot-1", "class FactualProjectionRebuildFirst {}")
	second := integrationFixture(t, "factual-projection-rebuild-second", "snapshot-1", "class FactualProjectionRebuildSecond {}")
	firstInput := factualRepositoryInput(t, first, "evidence-present")
	secondInput := factualRepositoryInput(t, second, "evidence-present")
	for _, fixture := range []integrationFixtureData{first, second} {
		if _, err := repository.PersistBundle(context.Background(), fixture.input); err != nil {
			t.Fatalf("seed legacy bundle %s: %v", fixture.organizationID, err)
		}
	}
	if err := repository.PersistFactualSnapshot(context.Background(), firstInput); err != nil {
		t.Fatalf("persist first factual snapshot: %v", err)
	}
	if err := repository.PersistFactualSnapshot(context.Background(), secondInput); err != nil {
		t.Fatalf("persist second factual snapshot: %v", err)
	}

	firstExpected, err := persistence.PrepareFactualProjection(firstInput)
	if err != nil {
		t.Fatalf("prepare first projection: %v", err)
	}
	secondExpected, err := persistence.PrepareFactualProjection(secondInput)
	if err != nil {
		t.Fatalf("prepare second projection: %v", err)
	}
	insertStaleFactualProjectionRows(t, database, first)
	secondFactsBefore := scopedProjectionTableCount(t, database, "canonical_facts", secondInput)
	secondEvidenceBefore := scopedProjectionTableCount(t, database, "canonical_fact_evidence", secondInput)
	secondEvidenceUnitsBefore := scopedProjectionTableCount(t, database, "evidence_units", secondInput)

	result, err := repository.RebuildFactualProjection(context.Background(), firstInput.OrganizationID, firstInput.SourceID, firstInput.SnapshotID)
	if err != nil {
		t.Fatalf("first factual projection rebuild: %v", err)
	}
	if result.EntityCount != len(firstExpected.Entities) || result.RelationshipCount != len(firstExpected.Relationships) {
		t.Fatalf("first rebuild result = %#v, want entities=%d relationships=%d", result, len(firstExpected.Entities), len(firstExpected.Relationships))
	}
	assertFactualProjectionRows(t, database, firstInput, firstExpected)
	if stale := scopedProjectionExternalCount(t, database, "entities", firstInput, "stale-entity"); stale != 0 {
		t.Fatalf("stale entity rows after rebuild = %d, want 0", stale)
	}
	if got := scopedProjectionTableCount(t, database, "canonical_facts", firstInput); got != 2 {
		t.Fatalf("first canonical fact count after rebuild = %d, want 2", got)
	}
	if got := scopedProjectionTableCount(t, database, "canonical_fact_evidence", firstInput); got != 2 {
		t.Fatalf("first evidence-link count after rebuild = %d, want 2", got)
	}
	if got := scopedProjectionTableCount(t, database, "canonical_facts", secondInput); got != secondFactsBefore || scopedProjectionTableCount(t, database, "canonical_fact_evidence", secondInput) != secondEvidenceBefore || scopedProjectionTableCount(t, database, "evidence_units", secondInput) != secondEvidenceUnitsBefore {
		t.Fatalf("second factual scope changed during first rebuild")
	}
	assertFactualProjectionRows(t, database, secondInput, persistence.PreparedFactualProjection{})
	if got := scopedProjectionTableCount(t, database, "entities", secondInput); got != 0 {
		t.Fatalf("second projection count before its rebuild = %d, want 0", got)
	}

	if _, err := repository.RebuildFactualProjection(context.Background(), secondInput.OrganizationID, secondInput.SourceID, secondInput.SnapshotID); err != nil {
		t.Fatalf("second factual projection rebuild: %v", err)
	}
	assertFactualProjectionRows(t, database, secondInput, secondExpected)

	if _, err := database.pool.Exec(context.Background(), `DELETE FROM relationships WHERE organization_id = $1::uuid AND source_id = $2::uuid AND snapshot_id = $3::uuid`, firstInput.OrganizationID, firstInput.SourceID, firstInput.SnapshotID); err != nil {
		t.Fatalf("delete first relationships for rebuild: %v", err)
	}
	if _, err := database.pool.Exec(context.Background(), `DELETE FROM entities WHERE organization_id = $1::uuid AND source_id = $2::uuid AND snapshot_id = $3::uuid`, firstInput.OrganizationID, firstInput.SourceID, firstInput.SnapshotID); err != nil {
		t.Fatalf("delete first entities for rebuild: %v", err)
	}
	if got := scopedProjectionTableCount(t, database, "entities", firstInput); got != 0 {
		t.Fatalf("first entities after explicit delete = %d, want 0", got)
	}
	if _, err := repository.RebuildFactualProjection(context.Background(), firstInput.OrganizationID, firstInput.SourceID, firstInput.SnapshotID); err != nil {
		t.Fatalf("rebuild after explicit projection delete: %v", err)
	}
	assertFactualProjectionRows(t, database, firstInput, firstExpected)
	if _, err := repository.RebuildFactualProjection(context.Background(), firstInput.OrganizationID, firstInput.SourceID, firstInput.SnapshotID); err != nil {
		t.Fatalf("idempotent repeated rebuild: %v", err)
	}
	assertFactualProjectionRows(t, database, firstInput, firstExpected)
}

func TestRebuildFactualProjectionIntegrationRejectsCorruptCanonicalDataWithoutDeletingProjection(t *testing.T) {
	database := openPostgresIntegrationDatabase(t)
	fixture := integrationFixture(t, "factual-projection-rebuild-corrupt", "snapshot-1", "class FactualProjectionRebuildCorrupt {}")
	repository := persistence.NewRepository(database.pool)
	if _, err := repository.PersistBundle(context.Background(), fixture.input); err != nil {
		t.Fatalf("seed legacy bundle: %v", err)
	}
	input := factualRepositoryInput(t, fixture, "evidence-present")
	if err := repository.PersistFactualSnapshot(context.Background(), input); err != nil {
		t.Fatalf("persist factual snapshot: %v", err)
	}
	want, err := persistence.PrepareFactualProjection(input)
	if err != nil {
		t.Fatalf("prepare expected projection: %v", err)
	}
	if _, err := repository.RebuildFactualProjection(context.Background(), input.OrganizationID, input.SourceID, input.SnapshotID); err != nil {
		t.Fatalf("initial factual projection rebuild: %v", err)
	}
	assertFactualProjectionRows(t, database, input, want)
	beforeEntities := scopedProjectionTableCount(t, database, "entities", input)
	beforeRelationships := scopedProjectionTableCount(t, database, "relationships", input)
	beforeFacts := scopedProjectionTableCount(t, database, "canonical_facts", input)

	if _, err := database.pool.Exec(context.Background(), `
UPDATE canonical_facts
SET predicate = 'corrupt-predicate'
WHERE organization_id = $1::uuid AND source_id = $2::uuid AND snapshot_id = $3::uuid AND fact_kind = 'observed'`, input.OrganizationID, input.SourceID, input.SnapshotID); err != nil {
		t.Fatalf("corrupt canonical predicate: %v", err)
	}
	result, err := repository.RebuildFactualProjection(context.Background(), input.OrganizationID, input.SourceID, input.SnapshotID)
	if !errors.Is(err, persistence.ErrInconsistent) || result != (persistence.FactualProjectionRebuildResult{}) {
		t.Fatalf("corrupt rebuild result/error = %#v/%v, want zero/inconsistent", result, err)
	}
	if scopedProjectionTableCount(t, database, "entities", input) != beforeEntities || scopedProjectionTableCount(t, database, "relationships", input) != beforeRelationships || scopedProjectionTableCount(t, database, "canonical_facts", input) != beforeFacts {
		t.Fatal("corrupt read changed projection or canonical facts")
	}
	if _, err := database.pool.Exec(context.Background(), `
UPDATE canonical_facts
SET predicate = 'definition'
WHERE organization_id = $1::uuid AND source_id = $2::uuid AND snapshot_id = $3::uuid AND fact_kind = 'observed'`, input.OrganizationID, input.SourceID, input.SnapshotID); err != nil {
		t.Fatalf("restore canonical predicate: %v", err)
	}
}

func insertStaleFactualProjectionRows(t *testing.T, database *postgresIntegrationDatabase, fixture integrationFixtureData) {
	t.Helper()
	entityA := persistence.Entity{
		ID:       identity.CanonicalUUID("stale-factual-projection-entity", fixture.job.OrganizationID, fixture.job.SourceID, fixture.job.SnapshotID, "a"),
		SourceID: fixture.job.SourceID, SnapshotID: fixture.job.SnapshotID, ExternalID: "stale-entity-a", Type: "stale", Name: "stale-a", Attributes: json.RawMessage(`{}`),
	}
	entityB := persistence.Entity{
		ID:       identity.CanonicalUUID("stale-factual-projection-entity", fixture.job.OrganizationID, fixture.job.SourceID, fixture.job.SnapshotID, "b"),
		SourceID: fixture.job.SourceID, SnapshotID: fixture.job.SnapshotID, ExternalID: "stale-entity-b", Type: "stale", Name: "stale-b", Attributes: json.RawMessage(`{}`),
	}
	repository := persistence.NewRepository(database.pool)
	for _, entity := range []persistence.Entity{entityA, entityB} {
		if err := repository.InsertEntity(context.Background(), fixture.job.OrganizationID, entity); err != nil {
			t.Fatalf("insert stale entity %s: %v", entity.ExternalID, err)
		}
	}
	relationship := persistence.Relationship{
		ID:       identity.CanonicalUUID("stale-factual-projection-relationship", fixture.job.OrganizationID, fixture.job.SourceID, fixture.job.SnapshotID),
		SourceID: fixture.job.SourceID, SnapshotID: fixture.job.SnapshotID, ExternalID: "stale-relationship", FromEntityID: entityA.ID, ToEntityID: entityB.ID, Type: "stale", Attributes: json.RawMessage(`{}`),
	}
	if err := repository.InsertRelationship(context.Background(), fixture.job.OrganizationID, relationship); err != nil {
		t.Fatalf("insert stale relationship: %v", err)
	}
}

func assertFactualProjectionRows(t *testing.T, database *postgresIntegrationDatabase, input persistence.FactualSnapshotInput, expected persistence.PreparedFactualProjection) {
	t.Helper()
	actualEntities := make(map[string]persistence.Entity)
	rows, err := database.pool.Query(context.Background(), `
SELECT id::text, source_id::text, snapshot_id::text, external_id, entity_type, name, attributes
FROM entities
WHERE organization_id = $1::uuid AND source_id = $2::uuid AND snapshot_id = $3::uuid
ORDER BY external_id`, input.OrganizationID, input.SourceID, input.SnapshotID)
	if err != nil {
		t.Fatalf("query factual entities: %v", err)
	}
	for rows.Next() {
		var entity persistence.Entity
		if err := rows.Scan(&entity.ID, &entity.SourceID, &entity.SnapshotID, &entity.ExternalID, &entity.Type, &entity.Name, &entity.Attributes); err != nil {
			rows.Close()
			t.Fatalf("scan factual entity: %v", err)
		}
		actualEntities[entity.ExternalID] = entity
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatalf("iterate factual entities: %v", err)
	}
	rows.Close()
	if len(actualEntities) != len(expected.Entities) {
		t.Fatalf("entity rows = %d, want %d", len(actualEntities), len(expected.Entities))
	}
	for _, want := range expected.Entities {
		got, ok := actualEntities[want.ExternalID]
		if !ok || got.ID != want.ID || got.SourceID != want.SourceID || got.SnapshotID != want.SnapshotID || got.Type != want.Type || got.Name != want.Name || !semanticJSONEqual(got.Attributes, want.Attributes) {
			t.Fatalf("entity %q = %#v, want %#v", want.ExternalID, got, want)
		}
	}

	actualRelationships := make(map[string]persistence.Relationship)
	rows, err = database.pool.Query(context.Background(), `
SELECT id::text, source_id::text, snapshot_id::text, external_id, from_entity_id::text, to_entity_id::text, relationship_type, attributes
FROM relationships
WHERE organization_id = $1::uuid AND source_id = $2::uuid AND snapshot_id = $3::uuid
ORDER BY external_id`, input.OrganizationID, input.SourceID, input.SnapshotID)
	if err != nil {
		t.Fatalf("query factual relationships: %v", err)
	}
	for rows.Next() {
		var relationship persistence.Relationship
		if err := rows.Scan(&relationship.ID, &relationship.SourceID, &relationship.SnapshotID, &relationship.ExternalID, &relationship.FromEntityID, &relationship.ToEntityID, &relationship.Type, &relationship.Attributes); err != nil {
			rows.Close()
			t.Fatalf("scan factual relationship: %v", err)
		}
		actualRelationships[relationship.ExternalID] = relationship
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatalf("iterate factual relationships: %v", err)
	}
	rows.Close()
	if len(actualRelationships) != len(expected.Relationships) {
		t.Fatalf("relationship rows = %d, want %d", len(actualRelationships), len(expected.Relationships))
	}
	for _, want := range expected.Relationships {
		got, ok := actualRelationships[want.ExternalID]
		if !ok || got.ID != want.ID || got.SourceID != want.SourceID || got.SnapshotID != want.SnapshotID || got.FromEntityID != want.FromEntityID || got.ToEntityID != want.ToEntityID || got.Type != want.Type || !semanticJSONEqual(got.Attributes, want.Attributes) {
			t.Fatalf("relationship %q = %#v, want %#v", want.ExternalID, got, want)
		}
	}
}

func scopedProjectionTableCount(t *testing.T, database *postgresIntegrationDatabase, table string, input persistence.FactualSnapshotInput) int {
	t.Helper()
	queries := map[string]string{
		"canonical_facts":         "SELECT COUNT(*) FROM canonical_facts",
		"canonical_fact_evidence": "SELECT COUNT(*) FROM canonical_fact_evidence",
		"evidence_units":          "SELECT COUNT(*) FROM evidence_units",
		"entities":                "SELECT COUNT(*) FROM entities",
		"relationships":           "SELECT COUNT(*) FROM relationships",
	}
	query, ok := queries[table]
	if !ok {
		t.Fatalf("unsupported scoped table %q", table)
	}
	var count int
	query += " WHERE organization_id = $1::uuid AND source_id = $2::uuid AND snapshot_id = $3::uuid"
	if err := database.pool.QueryRow(context.Background(), query, input.OrganizationID, input.SourceID, input.SnapshotID).Scan(&count); err != nil {
		t.Fatalf("count scoped %s: %v", table, err)
	}
	return count
}

func scopedProjectionExternalCount(t *testing.T, database *postgresIntegrationDatabase, table string, input persistence.FactualSnapshotInput, externalID string) int {
	t.Helper()
	if table != "entities" && table != "relationships" {
		t.Fatalf("unsupported external projection table %q", table)
	}
	query := fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE organization_id = $1::uuid AND source_id = $2::uuid AND snapshot_id = $3::uuid AND external_id = $4", table)
	var count int
	if err := database.pool.QueryRow(context.Background(), query, input.OrganizationID, input.SourceID, input.SnapshotID, externalID).Scan(&count); err != nil {
		t.Fatalf("count external %s: %v", table, err)
	}
	return count
}

func semanticJSONEqual(left, right []byte) bool {
	var leftValue, rightValue any
	if json.Unmarshal(left, &leftValue) != nil || json.Unmarshal(right, &rightValue) != nil {
		return false
	}
	leftCanonical, leftErr := json.Marshal(leftValue)
	rightCanonical, rightErr := json.Marshal(rightValue)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftCanonical, rightCanonical)
}
