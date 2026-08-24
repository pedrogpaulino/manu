package persistence

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/pedrogpaulino/manu/internal/bundle"
	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/fact"
)

func TestPersistBundleV2WritesFactualRowsAfterLegacyEvidenceInOneTransaction(t *testing.T) {
	input := batchV2Fixture(t, "snapshot-1")
	factualInput := batchFactualInput(input)
	prepared, err := PrepareFactualSnapshot(factualInput)
	if err != nil {
		t.Fatalf("prepare factual fixture: %v", err)
	}
	repository, starter := newFactualSQLRepository()
	starter.tx.queryRows = factualSupportRows(prepared)

	result, err := repository.PersistBundle(context.Background(), input)
	if err != nil {
		t.Fatalf("PersistBundle() error = %v", err)
	}
	if starter.beginCalls != 1 || starter.tx.commitCalls != 1 || starter.tx.rollbackCalls != 0 {
		t.Fatalf("transaction calls = begin %d commit %d rollback %d, want 1/1/0", starter.beginCalls, starter.tx.commitCalls, starter.tx.rollbackCalls)
	}
	if result.FrontendManifestIDs["batch-frontend"] == "" || result.CanonicalFactIDs[input.Facts[0].ID] == "" {
		t.Fatalf("v1alpha2 IDs missing from result: %#v", result)
	}
	legacyEvidenceIndex, factualManifestIndex := -1, -1
	for index, query := range starter.tx.execs {
		if strings.Contains(query, "INSERT INTO evidence_units") && legacyEvidenceIndex < 0 {
			legacyEvidenceIndex = index
		}
		if strings.Contains(query, "INSERT INTO frontend_manifests") && factualManifestIndex < 0 {
			factualManifestIndex = index
		}
	}
	if legacyEvidenceIndex < 0 || factualManifestIndex < 0 || legacyEvidenceIndex >= factualManifestIndex {
		t.Fatalf("legacy evidence/factual order = %d/%d, want evidence first", legacyEvidenceIndex, factualManifestIndex)
	}
}

func TestPersistBundleV2RejectsBeforeBeginAndRollsBackLateFactualFailure(t *testing.T) {
	invalid := batchV2Fixture(t, "snapshot-invalid")
	invalid.Facts[0].Producer.ID = "other-frontend"
	invalid.Facts[0].ID = mustBatchFactID(t, invalid.Facts[0])
	setBatchDigest(t, &invalid)
	invalidRepository, invalidStarter := newFactualSQLRepository()
	if _, err := invalidRepository.PersistBundle(context.Background(), invalid); !errors.Is(err, bundle.ErrInvalidReference) {
		t.Fatalf("invalid v1alpha2 bundle error = %v, want invalid reference", err)
	}
	if invalidStarter.beginCalls != 0 || len(invalidStarter.tx.execs) != 0 {
		t.Fatalf("invalid v1alpha2 bundle opened/wrote transaction: begin=%d writes=%d", invalidStarter.beginCalls, len(invalidStarter.tx.execs))
	}

	input := batchV2Fixture(t, "snapshot-late-failure")
	prepared, err := PrepareFactualSnapshot(batchFactualInput(input))
	if err != nil {
		t.Fatalf("prepare factual fixture: %v", err)
	}
	repository, starter := newFactualSQLRepository()
	starter.tx.queryRows = factualSupportRows(prepared)
	// The v1alpha1 portion of batchFixture has fifteen writes. The first
	// factual write must therefore be the sixteenth statement.
	starter.tx.execErrorAt = 16
	if _, err := repository.PersistBundle(context.Background(), input); err == nil {
		t.Fatal("PersistBundle() error = nil, want factual write failure")
	}
	if starter.beginCalls != 1 || starter.tx.commitCalls != 0 || starter.tx.rollbackCalls != 1 {
		t.Fatalf("late factual transaction calls = begin %d commit %d rollback %d, want 1/0/1", starter.beginCalls, starter.tx.commitCalls, starter.tx.rollbackCalls)
	}
	if len(starter.tx.execs) != 16 {
		t.Fatalf("writes before late factual failure = %d, want 16", len(starter.tx.execs))
	}
}

func TestPersistBundleV2RejectsDerivedFactWithoutInventingRuleVersion(t *testing.T) {
	input := batchV2Fixture(t, "snapshot-derived")
	observed := input.Facts[0]
	derived := fact.CanonicalFact{
		Version:   fact.Version,
		Scope:     observed.Scope,
		Predicate: fact.PredicateDependency,
		Subject:   observed.Subject,
		Producer:  observed.Producer,
		Evidence:  append([]fact.EvidenceRef(nil), observed.Evidence...),
		Lineage: &fact.Lineage{
			RuleID:       "dependency-rule",
			RuleVersion:  "1",
			InputFactIDs: []string{observed.ID},
		},
	}
	derived.ID = mustBatchFactID(t, derived)
	input.Facts = []fact.CanonicalFact{observed, derived}
	input.Manifest.Counts.CanonicalFactCount = 2
	for index := range input.Manifest.Files {
		if input.Manifest.Files[index].Name == bundle.CanonicalFactsFileName {
			input.Manifest.Files[index].Count = 2
		}
	}
	setBatchDigest(t, &input)
	if err := input.Validate(); err != nil {
		t.Fatalf("bundle.Validate() error = %v, want structurally valid bundle", err)
	}

	repository, starter := newFactualSQLRepository()
	if _, err := repository.PersistBundle(context.Background(), input); !errors.Is(err, ErrInvalidFactualSnapshot) && !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("derived bundle error = %v, want invalid factual snapshot/input", err)
	}
	if starter.beginCalls != 0 || len(starter.tx.execs) != 0 {
		t.Fatalf("derived bundle opened/wrote transaction: begin=%d writes=%d", starter.beginCalls, len(starter.tx.execs))
	}
}

func TestPersistBundleIncrementalV2UsesOneTransaction(t *testing.T) {
	previous := batchV2Fixture(t, "snapshot-previous")
	current := batchV2Fixture(t, "snapshot-current")
	prepared, err := PrepareFactualSnapshot(batchFactualInput(current))
	if err != nil {
		t.Fatalf("prepare current factual fixture: %v", err)
	}
	repository, starter := newFactualSQLRepository()
	starter.tx.queryRows = factualSupportRows(prepared)

	if _, _, err := repository.PersistBundleIncremental(context.Background(), previous, current); err != nil {
		t.Fatalf("PersistBundleIncremental() error = %v", err)
	}
	if starter.beginCalls != 1 || starter.tx.commitCalls != 1 || starter.tx.rollbackCalls != 0 {
		t.Fatalf("incremental transaction calls = begin %d commit %d rollback %d, want 1/1/0", starter.beginCalls, starter.tx.commitCalls, starter.tx.rollbackCalls)
	}
}

func batchV2Fixture(t *testing.T, snapshotID string) bundle.Bundle {
	t.Helper()
	input := batchFixture(snapshotID, "configuration-1")
	input.Manifest.Version = bundle.VersionV1Alpha2
	frontend := fact.FrontendManifest{
		ManifestVersion: fact.FrontendManifestVersion,
		ID:              "batch-frontend",
		Version:         "1",
		Method:          "symbols",
		SourceTypes:     []string{"filesystem"},
		Families:        []string{"go"},
		Versions:        []string{"1"},
		Capabilities:    []contract.Dimension{contract.DimensionEntitiesAndRelationships},
		Predicates:      []fact.Predicate{fact.PredicateDefinition},
		Execution:       fact.ExecutionProfileSafeStatic,
	}
	candidate := fact.CanonicalFact{
		Version:   fact.Version,
		Scope:     fact.Scope{OrganizationID: input.Manifest.Organization.ID, SourceID: input.Manifest.Source.ID, SnapshotID: snapshotID},
		Predicate: fact.PredicateDefinition,
		Subject:   fact.Participant{Kind: fact.ParticipantSymbol, ID: "main"},
		Producer:  fact.Producer{ID: frontend.ID, Version: frontend.Version, Method: frontend.Method},
		Evidence:  []fact.EvidenceRef{{ID: input.Evidence[0].ID, Locator: input.Evidence[0].Locator}},
	}
	candidate.ID = mustBatchFactID(t, candidate)
	input.FrontendManifests = []fact.FrontendManifest{frontend}
	input.Facts = []fact.CanonicalFact{candidate}
	input.Extensions = []json.RawMessage{}
	input.Manifest.Counts.FrontendManifestCount = 1
	input.Manifest.Counts.CanonicalFactCount = 1
	input.Manifest.Counts.ExtensionCount = 0
	input.Manifest.Files = append(input.Manifest.Files,
		bundle.File{Name: bundle.FrontendManifestsFileName, Bytes: 1, Count: 1, Digest: strings.Repeat("d", 64)},
		bundle.File{Name: bundle.CanonicalFactsFileName, Bytes: 1, Count: 1, Digest: strings.Repeat("d", 64)},
		bundle.File{Name: bundle.ExtensionsFileName, Bytes: 0, Count: 0, Digest: strings.Repeat("d", 64)},
	)
	input.Manifest.Limits.MaxFrontendManifests = 10
	input.Manifest.Limits.MaxCanonicalFacts = 10
	input.Manifest.Limits.MaxExtensions = 10
	setBatchDigest(t, &input)
	if err := input.Validate(); err != nil {
		t.Fatalf("validate v1alpha2 batch fixture: %v", err)
	}
	return input
}

func batchFactualInput(input bundle.Bundle) FactualSnapshotInput {
	return FactualSnapshotInput{
		OrganizationID: batchCanonicalUUID("organization", input.Manifest.Organization.ID),
		SourceID:       batchCanonicalUUID("source", input.Manifest.Organization.ID, input.Manifest.Source.ID),
		SnapshotID:     batchCanonicalUUID("snapshot", input.Manifest.Organization.ID, input.Manifest.Source.ID, input.Manifest.Snapshot.ID),
		Scope: fact.Scope{
			OrganizationID: input.Manifest.Organization.ID,
			SourceID:       input.Manifest.Source.ID,
			SnapshotID:     input.Manifest.Snapshot.ID,
		},
		FrontendManifests: input.FrontendManifests,
		Facts:             input.Facts,
	}
}

func mustBatchFactID(t *testing.T, candidate fact.CanonicalFact) string {
	t.Helper()
	id, err := fact.FactID(candidate)
	if err != nil {
		t.Fatalf("derive batch fact ID: %v", err)
	}
	return id
}
