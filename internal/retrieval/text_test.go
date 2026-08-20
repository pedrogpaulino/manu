package retrieval

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/pedrogpaulino/manu/internal/evidence"
)

type recordingTextStore struct {
	rebuildEntries []TextEntry
	rebuildScope   [3]string
	searchOptions  SearchOptions
	searchHits     []TextHit
	rebuildErr     error
	searchErr      error
}

func (s *recordingTextStore) RebuildSnapshot(_ context.Context, organizationID, sourceID, snapshotID string, entries []TextEntry) error {
	s.rebuildScope = [3]string{organizationID, sourceID, snapshotID}
	s.rebuildEntries = append([]TextEntry(nil), entries...)
	return s.rebuildErr
}

func (s *recordingTextStore) Search(_ context.Context, options SearchOptions) ([]TextHit, error) {
	s.searchOptions = options
	return s.searchHits, s.searchErr
}

func textTestUUID(number int) string {
	return fmt.Sprintf("00000000-0000-0000-0000-%012d", number)
}

func textTestEntry(id int) TextEntry {
	content := fmt.Sprintf("OrderService.create %d", id)
	return TextEntry{
		EvidenceID:          textTestUUID(id),
		OrganizationID:      textTestUUID(100),
		SourceID:            textTestUUID(101),
		SnapshotID:          textTestUUID(102),
		ProjectionKind:      "symbol",
		ContentState:        evidence.ContentStatePresent,
		Content:             content,
		ContentHash:         evidence.ContentDigest(content),
		Classification:      evidence.ClassificationSafeText,
		Persist:             evidence.DecisionAllow,
		SymbolName:          "create",
		SymbolQualifiedName: "OrderService.create",
	}
}

func TestTextEntryNormalizeSortsAndCompactsAllExactTerms(t *testing.T) {
	entry := textTestEntry(1)
	entry.ExactTerms = []string{"Beta", "alpha", "BETA", "gamma", "alpha"}
	normalized, err := entry.Normalize()
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	want := []string{"alpha", "beta", "create", "gamma", "orderservice.create"}
	if !reflect.DeepEqual(normalized.ExactTerms, want) {
		t.Fatalf("normalized exact terms = %#v, want %#v", normalized.ExactTerms, want)
	}
	if normalized.SymbolQualifiedName != "orderservice.create" {
		t.Fatalf("normalized qualified symbol = %q", normalized.SymbolQualifiedName)
	}
}

func TestTextEntrySupportsTechnicalProjectionKinds(t *testing.T) {
	tests := []struct {
		name      string
		kind      string
		symbol    string
		qualified string
		config    string
		exception string
		wantTerm  string
	}{
		{name: "symbol", kind: "symbol", symbol: "create", qualified: "OrderService.create", wantTerm: "orderservice.create"},
		{name: "configuration", kind: "configuration", config: "MANU_RETRIEVAL_LIMIT", wantTerm: "manu_retrieval_limit"},
		{name: "exception", kind: "exception", exception: "IllegalStateException", wantTerm: "illegalstateexception"},
		{name: "generic", kind: "generic", wantTerm: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			entry := textTestEntry(10)
			entry.ProjectionKind = test.kind
			entry.SymbolName = test.symbol
			entry.SymbolQualifiedName = test.qualified
			entry.ConfigurationKey = test.config
			entry.ExceptionType = test.exception
			normalized, err := entry.Normalize()
			if err != nil {
				t.Fatalf("Normalize() error = %v", err)
			}
			if normalized.ProjectionKind != test.kind {
				t.Fatalf("projection kind = %q, want %q", normalized.ProjectionKind, test.kind)
			}
			if test.wantTerm != "" && !containsTextTerm(normalized.ExactTerms, test.wantTerm) {
				t.Fatalf("exact terms = %#v, want %q", normalized.ExactTerms, test.wantTerm)
			}
		})
	}
}

func containsTextTerm(terms []string, want string) bool {
	for _, term := range terms {
		if term == want {
			return true
		}
	}
	return false
}

func TestExactTermsForQuerySortsAndCompactsNonAdjacentDuplicates(t *testing.T) {
	got := ExactTermsForQuery("OrderService.create alpha OrderService.create")
	want := []string{"alpha", "create", "orderservice", "orderservice.create"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ExactTermsForQuery() = %#v, want %#v", got, want)
	}
}

func TestTextProjectionRebuildFiltersAndOrdersAuthorizedEntries(t *testing.T) {
	store := &recordingTextStore{}
	projection := NewTextProjection(store)
	entryOne := textTestEntry(1)
	entryThree := textTestEntry(3)
	omitted := textTestEntry(2)
	omitted.ContentState = evidence.ContentStateOmitted
	omitted.Content = ""
	omitted.ContentHash = evidence.ContentDigest("")

	err := projection.RebuildSnapshot(context.Background(), textTestUUID(100), textTestUUID(101), textTestUUID(102), []TextEntry{
		entryThree, omitted, entryOne,
	})
	if err != nil {
		t.Fatalf("RebuildSnapshot() error = %v", err)
	}
	if got, want := len(store.rebuildEntries), 2; got != want {
		t.Fatalf("persisted entry count = %d, want %d", got, want)
	}
	if store.rebuildEntries[0].EvidenceID != textTestUUID(1) || store.rebuildEntries[1].EvidenceID != textTestUUID(3) {
		t.Fatalf("persisted IDs = %q, %q; want deterministic order", store.rebuildEntries[0].EvidenceID, store.rebuildEntries[1].EvidenceID)
	}
	if !store.rebuildEntries[0].Persistible() {
		t.Fatal("persisted entry is not authorized")
	}
}

func TestTextProjectionRejectsScopeAndDuplicateEntries(t *testing.T) {
	tests := []struct {
		name    string
		entries []TextEntry
		want    error
	}{
		{
			name: "scope mismatch",
			entries: []TextEntry{func() TextEntry {
				entry := textTestEntry(1)
				entry.SourceID = textTestUUID(999)
				return entry
			}()},
			want: ErrInvalidTextProjection,
		},
		{
			name:    "duplicate",
			entries: []TextEntry{textTestEntry(1), textTestEntry(1)},
			want:    ErrInvalidTextProjection,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &recordingTextStore{}
			projection := NewTextProjection(store)
			err := projection.RebuildSnapshot(context.Background(), textTestUUID(100), textTestUUID(101), textTestUUID(102), test.entries)
			if !errors.Is(err, test.want) {
				t.Fatalf("RebuildSnapshot() error = %v, want %v", err, test.want)
			}
			if store.rebuildEntries != nil {
				t.Fatal("store received entries after validation failure")
			}
		})
	}
}

func TestTextProjectionSearchNormalizesRequiredScopeAndLimit(t *testing.T) {
	store := &recordingTextStore{searchHits: []TextHit{{EvidenceID: textTestUUID(1)}}}
	projection := NewTextProjection(store)
	options := SearchOptions{
		OrganizationID: textTestUUID(100),
		SourceID:       textTestUUID(101),
		SnapshotID:     textTestUUID(102),
		Query:          "  OrderService.create  ",
	}
	hits, err := projection.Search(context.Background(), options)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(hits) != 1 || hits[0].EvidenceID != textTestUUID(1) {
		t.Fatalf("Search() hits = %#v", hits)
	}
	if store.searchOptions.Query != "OrderService.create" || store.searchOptions.Limit != DefaultTextSearchLimit {
		t.Fatalf("normalized options = %#v", store.searchOptions)
	}

	for _, invalid := range []SearchOptions{
		{Query: "term"},
		{OrganizationID: textTestUUID(100), Query: "term", Limit: MaxTextSearchLimit + 1},
	} {
		if _, err := projection.Search(context.Background(), invalid); !errors.Is(err, ErrInvalidTextProjection) {
			t.Errorf("Search(%#v) error = %v, want ErrInvalidTextProjection", invalid, err)
		}
	}
}

func TestTextProjectionContextAndPersistibilityBoundaries(t *testing.T) {
	store := &recordingTextStore{}
	projection := NewTextProjection(store)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := projection.RebuildSnapshot(canceled, textTestUUID(100), textTestUUID(101), textTestUUID(102), nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("RebuildSnapshot(canceled) error = %v, want context.Canceled", err)
	}

	redacted := textTestEntry(4)
	redacted.ContentState = evidence.ContentStateRedacted
	redacted.Content = evidence.RedactedContent
	redacted.ContentHash = strings.Repeat("b", 64)
	redacted.Persist = evidence.DecisionRedact
	if !redacted.Persistible() {
		t.Fatal("redacted authorized entry should be persistible")
	}
	omitted := textTestEntry(5)
	omitted.ContentState = evidence.ContentStateOmitted
	omitted.Content = ""
	omitted.ContentHash = evidence.ContentDigest("")
	if omitted.Persistible() {
		t.Fatal("omitted entry should not be persistible")
	}
}
