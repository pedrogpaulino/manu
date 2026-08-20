package retrieval

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
)

func TestRelationQueryNormalize(t *testing.T) {
	validScope := relationTestScope()
	tests := []struct {
		name      string
		query     RelationQuery
		want      RelationQuery
		wantError error
	}{
		{
			name: "defaults direction and fanout",
			query: RelationQuery{
				OrganizationID: validScope.OrganizationID,
				SourceID:       validScope.SourceID,
				SnapshotID:     validScope.SnapshotID,
				AnchorEntityID: relationTestUUID(10),
				MaxHops:        1,
			},
			want: RelationQuery{
				OrganizationID: validScope.OrganizationID,
				SourceID:       validScope.SourceID,
				SnapshotID:     validScope.SnapshotID,
				AnchorEntityID: relationTestUUID(10),
				Direction:      RelationDirectionBoth,
				MaxHops:        1,
				FanOut:         DefaultRelationFanOut,
			},
		},
		{
			name: "zero hops disables expansion",
			query: RelationQuery{
				OrganizationID: validScope.OrganizationID,
				SourceID:       validScope.SourceID,
				SnapshotID:     validScope.SnapshotID,
				AnchorEntityID: relationTestUUID(10),
			},
			want: RelationQuery{
				OrganizationID: validScope.OrganizationID,
				SourceID:       validScope.SourceID,
				SnapshotID:     validScope.SnapshotID,
				AnchorEntityID: relationTestUUID(10),
				Direction:      RelationDirectionBoth,
				FanOut:         DefaultRelationFanOut,
			},
		},
		{
			name: "invalid direction",
			query: RelationQuery{
				OrganizationID: validScope.OrganizationID,
				SourceID:       validScope.SourceID,
				SnapshotID:     validScope.SnapshotID,
				AnchorEntityID: relationTestUUID(10),
				Direction:      "transitive",
			},
			wantError: ErrInvalidRelationProjection,
		},
		{
			name: "negative hops",
			query: RelationQuery{
				OrganizationID: validScope.OrganizationID,
				SourceID:       validScope.SourceID,
				SnapshotID:     validScope.SnapshotID,
				AnchorEntityID: relationTestUUID(10),
				MaxHops:        -1,
			},
			wantError: ErrInvalidRelationProjection,
		},
		{
			name: "multi-hop rejected",
			query: RelationQuery{
				OrganizationID: validScope.OrganizationID,
				SourceID:       validScope.SourceID,
				SnapshotID:     validScope.SnapshotID,
				AnchorEntityID: relationTestUUID(10),
				MaxHops:        2,
			},
			wantError: ErrRelationTraversalLimit,
		},
		{
			name: "fanout must be positive",
			query: RelationQuery{
				OrganizationID: validScope.OrganizationID,
				SourceID:       validScope.SourceID,
				SnapshotID:     validScope.SnapshotID,
				AnchorEntityID: relationTestUUID(10),
				FanOut:         -1,
			},
			wantError: ErrInvalidRelationProjection,
		},
		{
			name: "fanout hard cap",
			query: RelationQuery{
				OrganizationID: validScope.OrganizationID,
				SourceID:       validScope.SourceID,
				SnapshotID:     validScope.SnapshotID,
				AnchorEntityID: relationTestUUID(10),
				FanOut:         MaxRelationFanOut + 1,
			},
			wantError: ErrInvalidRelationProjection,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := test.query.Normalize()
			if test.wantError != nil {
				if !errors.Is(err, test.wantError) {
					t.Fatalf("Normalize() error = %v, want %v", err, test.wantError)
				}
				return
			}
			if err != nil {
				t.Fatalf("Normalize() error = %v", err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("Normalize() = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestExpandRelationsDirectedOneHopAndDeterministic(t *testing.T) {
	scope := relationTestScope()
	anchor := relationTestUUID(10)
	relations := []RelationRecord{
		relationTestRecord(14, 14, relationTestUUID(14), anchor, "depends"),
		relationTestRecord(12, 12, relationTestUUID(13), anchor, "receives"),
		relationTestRecord(11, 11, anchor, relationTestUUID(12), "calls"),
		relationTestRecord(13, 13, anchor, relationTestUUID(11), "uses"),
		// This is two hops from the anchor and must not be returned.
		relationTestRecord(15, 15, relationTestUUID(12), relationTestUUID(17), "calls"),
	}
	query := RelationQuery{
		OrganizationID: scope.OrganizationID,
		SourceID:       scope.SourceID,
		SnapshotID:     scope.SnapshotID,
		AnchorEntityID: anchor,
		Direction:      RelationDirectionBoth,
		MaxHops:        1,
		FanOut:         3,
	}

	got, err := ExpandRelations(context.Background(), query, relations)
	if err != nil {
		t.Fatalf("ExpandRelations() error = %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("ExpandRelations() returned %d hits, want fan-out 3", len(got))
	}
	if got[0].RelationID != relationTestUUID(13) || got[1].RelationID != relationTestUUID(11) || got[2].RelationID != relationTestUUID(12) {
		t.Fatalf("ExpandRelations() order = [%s %s %s], want [13 11 12]", got[0].RelationID, got[1].RelationID, got[2].RelationID)
	}
	for _, hit := range got {
		if hit.Hops != 1 {
			t.Errorf("hit %s Hops = %d, want 1", hit.RelationID, hit.Hops)
		}
		wantProvenance := RelationProvenance{
			OrganizationID:     scope.OrganizationID,
			SourceID:           scope.SourceID,
			SnapshotID:         scope.SnapshotID,
			RelationID:         hit.RelationID,
			RelationExternalID: hit.RelationExternalID,
			FromEntityID:       hit.FromEntityID,
			ToEntityID:         hit.ToEntityID,
		}
		if hit.Provenance != wantProvenance {
			t.Errorf("hit %s provenance = %#v, want %#v", hit.RelationID, hit.Provenance, wantProvenance)
		}
	}

	reversed := append([]RelationRecord(nil), relations...)
	for left, right := 0, len(reversed)-1; left < right; left, right = left+1, right-1 {
		reversed[left], reversed[right] = reversed[right], reversed[left]
	}
	reordered, err := ExpandRelations(context.Background(), query, reversed)
	if err != nil {
		t.Fatalf("ExpandRelations(reordered) error = %v", err)
	}
	if !reflect.DeepEqual(got, reordered) {
		t.Fatalf("reordering input changed result:\n got %#v\nwant %#v", reordered, got)
	}

	outbound := query
	outbound.Direction = RelationDirectionOutbound
	outbound.FanOut = 100
	outboundHits, err := ExpandRelations(context.Background(), outbound, relations)
	if err != nil {
		t.Fatalf("outbound ExpandRelations() error = %v", err)
	}
	if len(outboundHits) != 2 || outboundHits[0].FromEntityID != anchor || outboundHits[1].FromEntityID != anchor {
		t.Fatalf("outbound hits = %#v, want only anchor-originated edges", outboundHits)
	}

	inbound := query
	inbound.Direction = RelationDirectionInbound
	inbound.FanOut = 100
	inboundHits, err := ExpandRelations(context.Background(), inbound, relations)
	if err != nil {
		t.Fatalf("inbound ExpandRelations() error = %v", err)
	}
	if len(inboundHits) != 2 || inboundHits[0].ToEntityID != anchor || inboundHits[1].ToEntityID != anchor {
		t.Fatalf("inbound hits = %#v, want only anchor-targeted edges", inboundHits)
	}
}

func TestExpandRelationsRejectsMixedScopeAndInvalidRows(t *testing.T) {
	scope := relationTestScope()
	base := relationTestRecord(20, 20, relationTestUUID(10), relationTestUUID(11), "calls")
	tests := []struct {
		name      string
		mutate    func(RelationRecord) RelationRecord
		wantError error
	}{
		{
			name: "organization mismatch",
			mutate: func(record RelationRecord) RelationRecord {
				record.OrganizationID = relationTestUUID(99)
				return record
			},
			wantError: ErrRelationScopeMismatch,
		},
		{
			name: "source mismatch",
			mutate: func(record RelationRecord) RelationRecord {
				record.SourceID = relationTestUUID(98)
				return record
			},
			wantError: ErrRelationScopeMismatch,
		},
		{
			name: "snapshot mismatch",
			mutate: func(record RelationRecord) RelationRecord {
				record.SnapshotID = relationTestUUID(97)
				return record
			},
			wantError: ErrRelationScopeMismatch,
		},
		{
			name: "invalid attributes",
			mutate: func(record RelationRecord) RelationRecord {
				record.Attributes = []byte(`[]`)
				return record
			},
			wantError: ErrInvalidRelationProjection,
		},
	}
	query := RelationQuery{
		OrganizationID: scope.OrganizationID,
		SourceID:       scope.SourceID,
		SnapshotID:     scope.SnapshotID,
		AnchorEntityID: relationTestUUID(10),
		MaxHops:        1,
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ExpandRelations(context.Background(), query, []RelationRecord{test.mutate(base)})
			if !errors.Is(err, test.wantError) {
				t.Fatalf("ExpandRelations() error = %v, want %v", err, test.wantError)
			}
		})
	}

	duplicate := base
	duplicate.FromEntityID = relationTestUUID(12)
	if _, err := ExpandRelations(context.Background(), query, []RelationRecord{base, duplicate}); !errors.Is(err, ErrInvalidRelationProjection) {
		t.Fatalf("duplicate relation IDs error = %v, want invalid projection", err)
	}
	duplicate = relationTestRecord(21, 21, base.FromEntityID, base.ToEntityID, base.Type)
	if _, err := ExpandRelations(context.Background(), query, []RelationRecord{base, duplicate}); !errors.Is(err, ErrInvalidRelationProjection) {
		t.Fatalf("duplicate directed relation error = %v, want invalid projection", err)
	}
}

func TestRelationProjectionValidatesStoreResultsAndScope(t *testing.T) {
	scope := relationTestScope()
	query := RelationQuery{
		OrganizationID: scope.OrganizationID,
		SourceID:       scope.SourceID,
		SnapshotID:     scope.SnapshotID,
		AnchorEntityID: relationTestUUID(10),
		Direction:      RelationDirectionOutbound,
		MaxHops:        1,
		FanOut:         1,
	}
	store := &relationTestStore{hits: []RelationHit{
		relationTestHit(31, 31, relationTestUUID(10), relationTestUUID(12), "calls", scope),
		relationTestHit(30, 30, relationTestUUID(10), relationTestUUID(11), "uses", scope),
	}}
	projection := NewRelationProjection(store)
	got, err := projection.Expand(context.Background(), query)
	if err != nil {
		t.Fatalf("RelationProjection.Expand() error = %v", err)
	}
	if len(got) != 1 || got[0].RelationID != relationTestUUID(30) {
		t.Fatalf("RelationProjection.Expand() = %#v, want the first deterministic edge", got)
	}
	if store.gotQuery.Direction != RelationDirectionOutbound || store.gotQuery.FanOut != 1 {
		t.Fatalf("store query = %#v, want normalized direction/fanout", store.gotQuery)
	}

	badStore := &relationTestStore{hits: []RelationHit{
		relationTestHit(32, 32, relationTestUUID(10), relationTestUUID(12), "calls", RelationScope{
			OrganizationID: relationTestUUID(90), SourceID: scope.SourceID, SnapshotID: scope.SnapshotID,
		}),
	}}
	_, err = NewRelationProjection(badStore).Expand(context.Background(), query)
	if !errors.Is(err, ErrRelationScopeMismatch) {
		t.Fatalf("mixed-organization store result error = %v, want scope mismatch", err)
	}
}

func TestRelationProjectionZeroHopsDoesNotCallStore(t *testing.T) {
	store := &relationTestStore{}
	projection := NewRelationProjection(store)
	query := RelationQuery{
		OrganizationID: relationTestUUID(1),
		SourceID:       relationTestUUID(2),
		SnapshotID:     relationTestUUID(3),
		AnchorEntityID: relationTestUUID(10),
		MaxHops:        0,
	}
	got, err := projection.Expand(context.Background(), query)
	if err != nil {
		t.Fatalf("zero-hop Expand() error = %v", err)
	}
	if len(got) != 0 || store.called {
		t.Fatalf("zero-hop result = %#v, store called = %t; want empty and no store call", got, store.called)
	}
}

func TestRelationProjectionRejectsNilContextAndMissingStore(t *testing.T) {
	query := RelationQuery{
		OrganizationID: relationTestUUID(1),
		SourceID:       relationTestUUID(2),
		SnapshotID:     relationTestUUID(3),
		AnchorEntityID: relationTestUUID(10),
		MaxHops:        1,
	}
	if _, err := NewRelationProjection(nil).Expand(context.Background(), query); !errors.Is(err, ErrRelationProjectionNotConfigured) {
		t.Fatalf("missing store error = %v, want not configured", err)
	}
	if _, err := NewRelationProjection(&relationTestStore{}).Expand(nil, query); !errors.Is(err, ErrInvalidRelationProjection) {
		t.Fatalf("nil context error = %v, want invalid projection", err)
	}
}

type relationTestStore struct {
	hits     []RelationHit
	gotQuery RelationQuery
	called   bool
}

func (s *relationTestStore) Expand(_ context.Context, query RelationQuery) ([]RelationHit, error) {
	s.called = true
	s.gotQuery = query
	return append([]RelationHit(nil), s.hits...), nil
}

func relationTestScope() RelationScope {
	return RelationScope{
		OrganizationID: relationTestUUID(1),
		SourceID:       relationTestUUID(2),
		SnapshotID:     relationTestUUID(3),
	}
}

func relationTestUUID(value byte) string {
	return fmt.Sprintf("00000000-0000-0000-0000-0000000000%02x", value)
}

func relationTestRecord(id, externalID int, from, to, relationType string) RelationRecord {
	scope := relationTestScope()
	return RelationRecord{
		ID:             relationTestUUID(byte(id)),
		OrganizationID: scope.OrganizationID,
		SourceID:       scope.SourceID,
		SnapshotID:     scope.SnapshotID,
		ExternalID:     fmt.Sprintf("relation-%d", externalID),
		FromEntityID:   from,
		ToEntityID:     to,
		Type:           relationType,
		Attributes:     []byte(`{"kind":"test"}`),
	}
}

func relationTestHit(id, externalID int, from, to, relationType string, scope RelationScope) RelationHit {
	record := relationTestRecord(id, externalID, from, to, relationType)
	record.OrganizationID = scope.OrganizationID
	record.SourceID = scope.SourceID
	record.SnapshotID = scope.SnapshotID
	return relationHitFromRecord(record)
}
