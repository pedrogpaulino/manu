package persistence

import (
	"context"
	"strings"
	"testing"

	"github.com/pedrogpaulino/manu/internal/evidence"
)

func TestIngestionEmbeddingEvidenceSourceReadsCanonicalIDsAndPolicy(t *testing.T) {
	organizationID := testUUID(1)
	sourceID := testUUID(2)
	snapshotID := testUUID(3)
	firstID := testUUID(10)
	secondID := testUUID(11)
	firstHash := strings.Repeat("a", 64)
	secondHash := strings.Repeat("b", 64)
	tx := &fakeTransaction{queryRows: []*repositoryFakeRows{{values: [][]any{
		{firstID, "class A {}", firstHash, string(evidence.ContentStatePresent), string(evidence.DecisionAllow)},
		{secondID, nil, secondHash, string(evidence.ContentStateOmitted), string(evidence.DecisionDeny)},
	}}}}
	repository, starter := newFakeRepository(tx)
	source := NewIngestionEmbeddingEvidenceSource(repository)
	units, err := source.ListEmbeddingEvidence(context.Background(), organizationID, sourceID, snapshotID)
	if err != nil {
		t.Fatalf("ListEmbeddingEvidence() error = %v", err)
	}
	if len(units) != 2 || units[0].ID != firstID || units[0].ExternalTransfer != evidence.DecisionAllow || units[0].Content != "class A {}" {
		t.Fatalf("canonical transferable unit = %#v", units)
	}
	if units[1].ID != secondID || units[1].ExternalTransfer != evidence.DecisionDeny || units[1].Content != "" {
		t.Fatalf("canonical omitted unit = %#v", units[1])
	}
	if starter.beginCalls != 1 || tx.commitCalls != 1 || tx.rollbackCalls != 0 {
		t.Fatalf("transaction lifecycle = begin %d/commit %d/rollback %d", starter.beginCalls, tx.commitCalls, tx.rollbackCalls)
	}
	if len(tx.queries) != 1 || !strings.Contains(tx.queries[0], "FROM evidence_units") || !strings.Contains(tx.queries[0], "external_transfer_decision") {
		t.Fatalf("evidence query = %#v", tx.queries)
	}
}
