package retrieval

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"testing"

	"github.com/pedrogpaulino/manu/internal/evidence"
)

type recordingEmbeddingStore struct {
	profile        EmbeddingProfile
	lookupKey      EmbeddingCacheKey
	lookupItem     EmbeddingItem
	lookupHit      bool
	rebuildArgs    []EmbeddingInput
	rebuildScope   EmbeddingScope
	rebuildProfile EmbeddingProfile
	ensureCalls    int
	lookupCalls    int
	rebuildCalls   int
	ensureErr      error
	lookupErr      error
	rebuildErr     error
}

func (s *recordingEmbeddingStore) EnsureProfile(_ context.Context, profile EmbeddingProfile) (EmbeddingProfile, error) {
	s.ensureCalls++
	if s.ensureErr != nil {
		return EmbeddingProfile{}, s.ensureErr
	}
	s.profile = profile
	return profile, nil
}

func (s *recordingEmbeddingStore) Lookup(_ context.Context, key EmbeddingCacheKey) (EmbeddingItem, bool, error) {
	s.lookupCalls++
	s.lookupKey = key
	return s.lookupItem, s.lookupHit, s.lookupErr
}

func (s *recordingEmbeddingStore) RebuildSnapshot(_ context.Context, profile EmbeddingProfile, scope EmbeddingScope, inputs []EmbeddingInput) (EmbeddingRebuildResult, error) {
	s.rebuildCalls++
	s.rebuildProfile = profile
	s.rebuildScope = scope
	s.rebuildArgs = append([]EmbeddingInput(nil), inputs...)
	return EmbeddingRebuildResult{OrganizationID: scope.OrganizationID, ProfileID: profile.ID, SourceID: scope.SourceID, SnapshotID: scope.SnapshotID}, s.rebuildErr
}

func embeddingTestUUID(number int) string {
	return fmt.Sprintf("00000000-0000-0000-0000-%012d", number)
}

func embeddingTestProfile() EmbeddingProfile {
	return EmbeddingProfile{
		ID:                   embeddingTestUUID(1),
		OrganizationID:       embeddingTestUUID(2),
		Provider:             "simulated",
		Model:                "embed-v1",
		Dimension:            3,
		Normalization:        "l2",
		ConfigurationVersion: "v1",
		ConfigurationDigest:  evidence.ContentDigest("{}"),
		Configuration:        jsonRaw(`{}`),
	}
}

func jsonRaw(value string) []byte { return []byte(value) }

func embeddingTestScope() EmbeddingScope {
	return EmbeddingScope{OrganizationID: embeddingTestUUID(2), SourceID: embeddingTestUUID(3), SnapshotID: embeddingTestUUID(4)}
}

func embeddingTestInput(number int, vector []float32) EmbeddingInput {
	return EmbeddingInput{
		ID:                  embeddingTestUUID(100 + number),
		EvidenceID:          embeddingTestUUID(200 + number),
		EvidenceContentHash: evidence.ContentDigest("evidence-" + string(rune('a'+number))),
		Vector:              vector,
	}
}

func TestEmbeddingProfileNormalizeCanonicalizesConfigurationAndDigest(t *testing.T) {
	canonical := `{"a":1,"nested":{"a":true,"z":[3,2]},"z":2}`
	profile := embeddingTestProfile()
	profile.Configuration = jsonRaw(`{"z":2,"nested":{"z":[3,2],"a":true},"a":1}`)
	profile.ConfigurationDigest = evidence.ContentDigest(canonical)
	normalized, err := profile.Normalize()
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	if string(normalized.Configuration) != canonical || normalized.ConfigurationDigest != evidence.ContentDigest(canonical) {
		t.Fatalf("normalized configuration/digest = %s/%s", normalized.Configuration, normalized.ConfigurationDigest)
	}

	profile.ConfigurationDigest = evidence.ContentDigest(`{"a":1,"z":3}`)
	if err := profile.Validate(); !errors.Is(err, ErrInvalidEmbeddingProjection) {
		t.Fatalf("Validate() error = %v, want digest mismatch", err)
	}
}

func TestEmbeddingProfileRejectsInvalidDimensionAndSecretConfiguration(t *testing.T) {
	tests := []struct {
		name   string
		modify func(*EmbeddingProfile)
	}{
		{name: "zero dimension", modify: func(profile *EmbeddingProfile) { profile.Dimension = 0 }},
		{name: "too large dimension", modify: func(profile *EmbeddingProfile) { profile.Dimension = MaxEmbeddingDimension + 1 }},
		{name: "secret key", modify: func(profile *EmbeddingProfile) {
			profile.Configuration = jsonRaw(`{"api_key":"not-stored"}`)
			profile.ConfigurationDigest = evidence.ContentDigest(`{"api_key":"not-stored"}`)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			profile := embeddingTestProfile()
			test.modify(&profile)
			if err := profile.Validate(); !errors.Is(err, ErrInvalidEmbeddingProjection) {
				t.Fatalf("Validate() error = %v, want invalid embedding profile", err)
			}
		})
	}
}

func TestEmbeddingInputRejectsWrongDimensionAndNonFiniteValues(t *testing.T) {
	profile := embeddingTestProfile()
	scope := embeddingTestScope()
	tests := []struct {
		name   string
		vector []float32
	}{
		{name: "short", vector: []float32{1, 2}},
		{name: "nan", vector: []float32{1, float32(math.NaN()), 3}},
		{name: "positive infinity", vector: []float32{1, float32(math.Inf(1)), 3}},
		{name: "negative infinity", vector: []float32{1, float32(math.Inf(-1)), 3}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := embeddingTestInput(1, test.vector).Normalize(profile, scope); !errors.Is(err, ErrInvalidEmbeddingProjection) {
				t.Fatalf("Normalize() error = %v, want invalid vector", err)
			}
		})
	}
}

func TestEmbeddingProjectionRebuildNormalizesAndSortsBeforeStore(t *testing.T) {
	store := &recordingEmbeddingStore{}
	projection := NewEmbeddingProjection(store)
	profile := embeddingTestProfile()
	scope := embeddingTestScope()
	inputs := []EmbeddingInput{embeddingTestInput(2, []float32{2, 3, 4}), embeddingTestInput(1, nil)}
	result, err := projection.RebuildSnapshot(context.Background(), profile, scope, inputs)
	if err != nil {
		t.Fatalf("RebuildSnapshot() error = %v", err)
	}
	if store.rebuildCalls != 1 || result.ProfileID != profile.ID {
		t.Fatalf("store/result calls = %d/%+v", store.rebuildCalls, result)
	}
	if got := store.rebuildArgs[0].EvidenceID; got >= store.rebuildArgs[1].EvidenceID {
		t.Fatalf("inputs were not sorted by evidence ID: %q >= %q", got, store.rebuildArgs[1].EvidenceID)
	}
	if store.rebuildArgs[1].Vector[0] != 2 {
		t.Fatalf("vector was not preserved: %#v", store.rebuildArgs[1].Vector)
	}
}

func TestEmbeddingProjectionRejectsInvalidScopeBeforeStore(t *testing.T) {
	store := &recordingEmbeddingStore{}
	projection := NewEmbeddingProjection(store)
	profile := embeddingTestProfile()
	scope := embeddingTestScope()
	scope.OrganizationID = embeddingTestUUID(99)
	if _, err := projection.RebuildSnapshot(context.Background(), profile, scope, nil); !errors.Is(err, ErrEmbeddingScopeMismatch) {
		t.Fatalf("RebuildSnapshot() error = %v, want scope mismatch", err)
	}
	if store.rebuildCalls != 0 {
		t.Fatal("store was called after scope validation failure")
	}
}

func TestOrderEmbeddingItemsRefusesMixedProfilesAndIsDeterministic(t *testing.T) {
	profile := embeddingTestProfile()
	scope := embeddingTestScope()
	first := embeddingTestInput(1, []float32{1, 2, 3})
	second := embeddingTestInput(2, []float32{2, 3, 4})
	items := []EmbeddingItem{
		{ID: second.ID, OrganizationID: scope.OrganizationID, ProfileID: profile.ID, ProfileDimension: profile.Dimension, SourceID: scope.SourceID, SnapshotID: scope.SnapshotID, EvidenceID: second.EvidenceID, EvidenceContentHash: second.EvidenceContentHash, Vector: second.Vector, State: "ready"},
		{ID: first.ID, OrganizationID: scope.OrganizationID, ProfileID: profile.ID, ProfileDimension: profile.Dimension, SourceID: scope.SourceID, SnapshotID: scope.SnapshotID, EvidenceID: first.EvidenceID, EvidenceContentHash: first.EvidenceContentHash, Vector: first.Vector, State: "ready"},
	}
	ordered, err := OrderEmbeddingItems(profile, scope, items)
	if err != nil {
		t.Fatalf("OrderEmbeddingItems() error = %v", err)
	}
	if ordered[0].EvidenceID != first.EvidenceID || ordered[1].EvidenceID != second.EvidenceID {
		t.Fatalf("order = %q, %q", ordered[0].EvidenceID, ordered[1].EvidenceID)
	}
	ordered[0].Vector[0] = 99
	if items[1].Vector[0] == 99 {
		t.Fatal("ordering returned an aliased vector")
	}

	otherProfile := profile
	otherProfile.ID = embeddingTestUUID(9)
	mixed := items
	mixed[0].ProfileID = otherProfile.ID
	if _, err := OrderEmbeddingItems(profile, scope, mixed); !errors.Is(err, ErrEmbeddingProfileMix) {
		t.Fatalf("mixed profile error = %v, want ErrEmbeddingProfileMix", err)
	}
}

func TestEmbeddingProjectionLookupAndCancellationBoundaries(t *testing.T) {
	store := &recordingEmbeddingStore{lookupHit: true, lookupItem: EmbeddingItem{ID: embeddingTestUUID(101)}}
	projection := NewEmbeddingProjection(store)
	key := EmbeddingCacheKey{OrganizationID: embeddingTestUUID(2), ProfileID: embeddingTestUUID(1), EvidenceContentHash: evidence.ContentDigest("x")}
	if _, hit, err := projection.Lookup(context.Background(), key); err != nil || !hit || store.lookupCalls != 1 {
		t.Fatalf("Lookup() = hit %v, error %v, calls %d", hit, err, store.lookupCalls)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := projection.Lookup(canceled, key); !errors.Is(err, context.Canceled) {
		t.Fatalf("Lookup(canceled) error = %v, want context.Canceled", err)
	}
	if !reflect.DeepEqual(store.lookupKey, key) {
		t.Fatalf("lookup key = %#v, want %#v", store.lookupKey, key)
	}
}
