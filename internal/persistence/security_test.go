package persistence

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/pedrogpaulino/manu/internal/retrieval"
)

func TestProjectionAndEvidenceQueriesAreParameterizedAndScoped(t *testing.T) {
	queries := []struct {
		name  string
		query string
	}{
		{name: "embedding profile", query: selectEmbeddingProfileSQL},
		{name: "embedding cache", query: lookupEmbeddingCacheSQL},
		{name: "embedding rebuild cache", query: selectEmbeddingCacheSQL},
		{name: "embedding search", query: searchExactEmbeddingSQL},
		{name: "text search", query: searchTextProjectionSQL},
		{name: "relations", query: expandRelationsSQL},
		{name: "query evidence", query: selectQueryEvidenceUnitSQL},
		{name: "embedding evidence", query: listEmbeddingEvidenceSQL},
	}
	for _, test := range queries {
		t.Run(test.name, func(t *testing.T) {
			if !strings.Contains(test.query, "$1") {
				t.Fatal("query does not use positional parameters")
			}
			if strings.Contains(test.query, "%s") || strings.Contains(test.query, "' OR 1=1") {
				t.Fatalf("query contains an interpolation pattern: %s", test.query)
			}
			if !strings.Contains(test.query, "organization_id") {
				t.Fatal("query omits organization scope")
			}
		})
	}
}

func TestWrapPersistenceErrorRedactsNonPostgresDetailsAndKeepsCategory(t *testing.T) {
	const secret = "password=driver-secret"

	unknown := errors.New("driver detail: " + secret)
	redacted := wrapPersistenceError(context.Background(), "read evidence", unknown)
	if !errors.Is(redacted, ErrDatabase) || strings.Contains(redacted.Error(), secret) || strings.Contains(redacted.Error(), "driver detail") {
		t.Fatalf("unknown persistence error = %v, want redacted ErrDatabase", redacted)
	}
	if unwrapped := errors.Unwrap(redacted); unwrapped != ErrDatabase {
		t.Fatalf("unknown persistence unwrap = %v, want ErrDatabase", unwrapped)
	}

	classified := fmt.Errorf("%w: %s", ErrInvalidInput, secret)
	redacted = wrapPersistenceError(context.Background(), "validate evidence", classified)
	if !errors.Is(redacted, ErrInvalidInput) || errors.Is(redacted, ErrDatabase) || strings.Contains(redacted.Error(), secret) {
		t.Fatalf("classified persistence error = %v, want redacted ErrInvalidInput", redacted)
	}
}

func TestEmbeddingRebuildRejectsOrganizationProfileMixingBeforeTransaction(t *testing.T) {
	profile := embeddingPersistenceProfile()
	scope := embeddingPersistenceScope()
	profile.OrganizationID = embeddingPersistenceUUID(999)
	db := &embeddingFakeDatabase{tx: &embeddingFakeTransaction{}}

	_, err := newEmbeddingProjectionStore(db).RebuildSnapshot(
		context.Background(), profile, scope, []retrieval.EmbeddingInput{embeddingPersistenceInput(1, []float32{1, 2, 3})},
	)
	if !errors.Is(err, retrieval.ErrEmbeddingScopeMismatch) {
		t.Fatalf("RebuildSnapshot() error = %v, want ErrEmbeddingScopeMismatch", err)
	}
	if db.beginCalls != 0 {
		t.Fatalf("scope mismatch opened a transaction: %d", db.beginCalls)
	}
}
