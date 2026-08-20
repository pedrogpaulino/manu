package retrieval

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// MaxEmbeddingDimension is the largest profile accepted by the local
	// projection boundary. Provider-specific limits remain gateway concerns.
	MaxEmbeddingDimension       = 4096
	maxEmbeddingIdentifierBytes = 128
)

var (
	ErrInvalidEmbeddingProjection       = errors.New("retrieval: invalid embedding projection input")
	ErrEmbeddingProfileConflict         = errors.New("retrieval: embedding profile conflict")
	ErrEmbeddingProfileMix              = errors.New("retrieval: embedding profiles cannot be mixed")
	ErrEmbeddingScopeMismatch           = errors.New("retrieval: embedding projection scope mismatch")
	ErrEmbeddingProjectionNotConfigured = errors.New("retrieval: embedding projection store is not configured")
)

// EmbeddingProfile is the immutable, non-secret identity of one embedding
// projection. Provider credentials are deliberately absent; they stay in the
// gateway configuration and never become cache identity.
type EmbeddingProfile struct {
	ID                   string
	OrganizationID       string
	Provider             string
	Model                string
	Dimension            int
	Normalization        string
	ConfigurationVersion string
	// ConfigurationDigest is the SHA-256 digest of the canonical JSON bytes
	// produced by Normalize; callers must provide that digest, not a digest of
	// an alternate JSON spelling.
	ConfigurationDigest string
	Configuration       json.RawMessage
}

// Validate checks a profile without contacting a provider. Configuration is
// required to be a JSON object and secret-looking keys are rejected before the
// profile crosses the persistence boundary.
func (p EmbeddingProfile) Validate() error {
	_, err := p.Normalize()
	return err
}

// Normalize returns a canonical, defensive profile representation. JSON map
// encoding is deterministic, so the same non-secret configuration has the
// same stored bytes across rebuilds; the supplied digest must match those
// canonical bytes.
func (p EmbeddingProfile) Normalize() (EmbeddingProfile, error) {
	p.ID = strings.ToLower(strings.TrimSpace(p.ID))
	p.OrganizationID = strings.ToLower(strings.TrimSpace(p.OrganizationID))
	p.Provider = strings.TrimSpace(p.Provider)
	p.Model = strings.TrimSpace(p.Model)
	p.Normalization = strings.TrimSpace(p.Normalization)
	p.ConfigurationVersion = strings.TrimSpace(p.ConfigurationVersion)
	p.ConfigurationDigest = strings.ToLower(strings.TrimSpace(p.ConfigurationDigest))
	for name, value := range map[string]string{
		"profile_id": p.ID, "organization_id": p.OrganizationID,
	} {
		if err := validateEmbeddingUUID(name, value); err != nil {
			return EmbeddingProfile{}, err
		}
	}
	for name, value := range map[string]string{
		"provider": p.Provider, "model": p.Model,
		"normalization": p.Normalization, "configuration_version": p.ConfigurationVersion,
	} {
		if err := validateEmbeddingIdentifier(name, value); err != nil {
			return EmbeddingProfile{}, err
		}
	}
	if p.Dimension <= 0 || p.Dimension > MaxEmbeddingDimension {
		return EmbeddingProfile{}, fmt.Errorf("%w: embedding dimension is invalid", ErrInvalidEmbeddingProjection)
	}
	if !isEmbeddingSHA256(p.ConfigurationDigest) {
		return EmbeddingProfile{}, fmt.Errorf("%w: configuration digest is invalid", ErrInvalidEmbeddingProjection)
	}
	configuration, err := normalizeEmbeddingConfiguration(p.Configuration)
	if err != nil {
		return EmbeddingProfile{}, err
	}
	digest := sha256.Sum256(configuration)
	if p.ConfigurationDigest != hex.EncodeToString(digest[:]) {
		return EmbeddingProfile{}, fmt.Errorf("%w: configuration digest does not match canonical configuration", ErrInvalidEmbeddingProjection)
	}
	p.Configuration = configuration
	return p, nil
}

// EmbeddingScope is mandatory for every snapshot projection operation.
type EmbeddingScope struct {
	OrganizationID string
	SourceID       string
	SnapshotID     string
}

func (s EmbeddingScope) Validate() error {
	_, err := s.Normalize()
	return err
}

func (s EmbeddingScope) Normalize() (EmbeddingScope, error) {
	s.OrganizationID = strings.ToLower(strings.TrimSpace(s.OrganizationID))
	s.SourceID = strings.ToLower(strings.TrimSpace(s.SourceID))
	s.SnapshotID = strings.ToLower(strings.TrimSpace(s.SnapshotID))
	for name, value := range map[string]string{
		"organization_id": s.OrganizationID, "source_id": s.SourceID, "snapshot_id": s.SnapshotID,
	} {
		if err := validateEmbeddingUUID(name, value); err != nil {
			return EmbeddingScope{}, err
		}
	}
	return s, nil
}

// EmbeddingCacheKey identifies a reusable vector by organization, immutable
// profile, and the canonical Evidence Unit content hash.
type EmbeddingCacheKey struct {
	OrganizationID      string
	ProfileID           string
	EvidenceContentHash string
}

func (k EmbeddingCacheKey) Normalize() (EmbeddingCacheKey, error) {
	k.OrganizationID = strings.ToLower(strings.TrimSpace(k.OrganizationID))
	k.ProfileID = strings.ToLower(strings.TrimSpace(k.ProfileID))
	k.EvidenceContentHash = strings.ToLower(strings.TrimSpace(k.EvidenceContentHash))
	if err := validateEmbeddingUUID("organization_id", k.OrganizationID); err != nil {
		return EmbeddingCacheKey{}, err
	}
	if err := validateEmbeddingUUID("profile_id", k.ProfileID); err != nil {
		return EmbeddingCacheKey{}, err
	}
	if !isEmbeddingSHA256(k.EvidenceContentHash) {
		return EmbeddingCacheKey{}, fmt.Errorf("%w: evidence content hash is invalid", ErrInvalidEmbeddingProjection)
	}
	return k, nil
}

// EmbeddingInput is one canonical evidence identity entering a rebuild. A
// nil/empty Vector requests cache reuse; a supplied Vector is used only on a
// cache miss and is validated even when a compatible cache hit exists.
type EmbeddingInput struct {
	ID                  string
	EvidenceID          string
	EvidenceContentHash string
	Vector              []float32
}

// IncrementalEmbeddingInput carries one current-snapshot embedding request.
// A reusable input has no provider vector and references the previous
// canonical Evidence Unit; affected inputs receive a vector from the gateway
// before persistence. StableKey is comparison metadata, not a relational ID.
type IncrementalEmbeddingInput struct {
	StableKey          string
	PreviousEvidenceID string
	Input              EmbeddingInput
	Reuse              bool
}

func (i EmbeddingInput) Normalize(profile EmbeddingProfile, scope EmbeddingScope) (EmbeddingInput, error) {
	normalizedProfile, err := profile.Normalize()
	if err != nil {
		return EmbeddingInput{}, err
	}
	normalizedScope, err := scope.Normalize()
	if err != nil {
		return EmbeddingInput{}, err
	}
	if normalizedProfile.OrganizationID != normalizedScope.OrganizationID {
		return EmbeddingInput{}, fmt.Errorf("%w: profile organization differs from scope", ErrEmbeddingScopeMismatch)
	}
	i.ID = strings.ToLower(strings.TrimSpace(i.ID))
	i.EvidenceID = strings.ToLower(strings.TrimSpace(i.EvidenceID))
	i.EvidenceContentHash = strings.ToLower(strings.TrimSpace(i.EvidenceContentHash))
	if err := validateEmbeddingUUID("embedding_id", i.ID); err != nil {
		return EmbeddingInput{}, err
	}
	if err := validateEmbeddingUUID("evidence_id", i.EvidenceID); err != nil {
		return EmbeddingInput{}, err
	}
	if !isEmbeddingSHA256(i.EvidenceContentHash) {
		return EmbeddingInput{}, fmt.Errorf("%w: evidence content hash is invalid", ErrInvalidEmbeddingProjection)
	}
	if len(i.Vector) != 0 {
		if err := validateEmbeddingVector(i.Vector, normalizedProfile.Dimension); err != nil {
			return EmbeddingInput{}, err
		}
		i.Vector = append([]float32(nil), i.Vector...)
	}
	return i, nil
}

// EmbeddingItem is the persisted derived row. It never replaces the
// canonical Evidence Unit and is always tied to one immutable profile.
type EmbeddingItem struct {
	ID                  string
	OrganizationID      string
	ProfileID           string
	ProfileDimension    int
	SourceID            string
	SnapshotID          string
	EvidenceID          string
	EvidenceContentHash string
	Vector              []float32
	State               string
}

func (i EmbeddingItem) Normalize(profile EmbeddingProfile, scope EmbeddingScope) (EmbeddingItem, error) {
	normalizedProfile, err := profile.Normalize()
	if err != nil {
		return EmbeddingItem{}, err
	}
	normalizedScope, err := scope.Normalize()
	if err != nil {
		return EmbeddingItem{}, err
	}
	if normalizedProfile.OrganizationID != normalizedScope.OrganizationID {
		return EmbeddingItem{}, fmt.Errorf("%w: profile organization differs from scope", ErrEmbeddingScopeMismatch)
	}
	i.ID = strings.ToLower(strings.TrimSpace(i.ID))
	i.OrganizationID = strings.ToLower(strings.TrimSpace(i.OrganizationID))
	i.ProfileID = strings.ToLower(strings.TrimSpace(i.ProfileID))
	i.SourceID = strings.ToLower(strings.TrimSpace(i.SourceID))
	i.SnapshotID = strings.ToLower(strings.TrimSpace(i.SnapshotID))
	i.EvidenceID = strings.ToLower(strings.TrimSpace(i.EvidenceID))
	i.EvidenceContentHash = strings.ToLower(strings.TrimSpace(i.EvidenceContentHash))
	for name, value := range map[string]string{
		"embedding_id": i.ID, "organization_id": i.OrganizationID,
		"profile_id": i.ProfileID, "source_id": i.SourceID,
		"snapshot_id": i.SnapshotID, "evidence_id": i.EvidenceID,
	} {
		if err := validateEmbeddingUUID(name, value); err != nil {
			return EmbeddingItem{}, err
		}
	}
	if i.OrganizationID != normalizedScope.OrganizationID || i.SourceID != normalizedScope.SourceID || i.SnapshotID != normalizedScope.SnapshotID {
		return EmbeddingItem{}, fmt.Errorf("%w: item is outside requested scope", ErrEmbeddingScopeMismatch)
	}
	if i.ProfileID != normalizedProfile.ID || i.ProfileDimension != normalizedProfile.Dimension {
		return EmbeddingItem{}, fmt.Errorf("%w: item profile or dimension differs", ErrEmbeddingProfileMix)
	}
	if !isEmbeddingSHA256(i.EvidenceContentHash) {
		return EmbeddingItem{}, fmt.Errorf("%w: evidence content hash is invalid", ErrInvalidEmbeddingProjection)
	}
	if i.State == "" {
		i.State = "ready"
	}
	if i.State != "ready" && i.State != "stale" {
		return EmbeddingItem{}, fmt.Errorf("%w: embedding item state is invalid", ErrInvalidEmbeddingProjection)
	}
	if err := validateEmbeddingVector(i.Vector, normalizedProfile.Dimension); err != nil {
		return EmbeddingItem{}, err
	}
	i.Vector = append([]float32(nil), i.Vector...)
	return i, nil
}

// EmbeddingMissing identifies a requested evidence unit that had neither a
// compatible cache hit nor a supplied vector. Missing work is explicit and
// does not silently look like a completed rebuild.
type EmbeddingMissing struct {
	EvidenceID          string
	EvidenceContentHash string
}

// EmbeddingRebuildResult describes a committed projection rebuild.
type EmbeddingRebuildResult struct {
	OrganizationID string
	ProfileID      string
	SourceID       string
	SnapshotID     string
	Requested      int
	CacheHits      int
	Inserted       int
	Missing        []EmbeddingMissing
	Items          []EmbeddingItem
}

func (r EmbeddingRebuildResult) Complete() bool { return len(r.Missing) == 0 }

// EmbeddingStore is the persistence port for profile metadata and the
// rebuildable cache. It deliberately has no vector similarity method; exact
// pgvector search is a later task.
type EmbeddingStore interface {
	EnsureProfile(context.Context, EmbeddingProfile) (EmbeddingProfile, error)
	Lookup(context.Context, EmbeddingCacheKey) (EmbeddingItem, bool, error)
	RebuildSnapshot(context.Context, EmbeddingProfile, EmbeddingScope, []EmbeddingInput) (EmbeddingRebuildResult, error)
}

// IncrementalEmbeddingStore materializes a new profile/snapshot projection
// with copy-forward for compatible rows. Implementations must keep the old
// snapshot and profile immutable and must not mix profiles.
type IncrementalEmbeddingStore interface {
	RebuildSnapshotIncremental(context.Context, EmbeddingProfile, EmbeddingScope, string, []IncrementalEmbeddingInput) (EmbeddingRebuildResult, error)
}

// EmbeddingProjection validates boundaries around an embedding store. It is
// stateless; a rebuild means replacing one scoped derived projection.
type EmbeddingProjection struct {
	store EmbeddingStore
}

func NewEmbeddingProjection(store EmbeddingStore) *EmbeddingProjection {
	return &EmbeddingProjection{store: store}
}

func (p *EmbeddingProjection) EnsureProfile(ctx context.Context, profile EmbeddingProfile) (EmbeddingProfile, error) {
	if err := validateEmbeddingContext(ctx); err != nil {
		return EmbeddingProfile{}, err
	}
	if p == nil || p.store == nil {
		return EmbeddingProfile{}, ErrEmbeddingProjectionNotConfigured
	}
	normalized, err := profile.Normalize()
	if err != nil {
		return EmbeddingProfile{}, err
	}
	return p.store.EnsureProfile(ctx, normalized)
}

func (p *EmbeddingProjection) Lookup(ctx context.Context, key EmbeddingCacheKey) (EmbeddingItem, bool, error) {
	if err := validateEmbeddingContext(ctx); err != nil {
		return EmbeddingItem{}, false, err
	}
	if p == nil || p.store == nil {
		return EmbeddingItem{}, false, ErrEmbeddingProjectionNotConfigured
	}
	normalized, err := key.Normalize()
	if err != nil {
		return EmbeddingItem{}, false, err
	}
	return p.store.Lookup(ctx, normalized)
}

func (p *EmbeddingProjection) RebuildSnapshot(ctx context.Context, profile EmbeddingProfile, scope EmbeddingScope, inputs []EmbeddingInput) (EmbeddingRebuildResult, error) {
	if err := validateEmbeddingContext(ctx); err != nil {
		return EmbeddingRebuildResult{}, err
	}
	if p == nil || p.store == nil {
		return EmbeddingRebuildResult{}, ErrEmbeddingProjectionNotConfigured
	}
	normalizedProfile, normalizedScope, prepared, err := prepareEmbeddingRebuild(profile, scope, inputs)
	if err != nil {
		return EmbeddingRebuildResult{}, err
	}
	return p.store.RebuildSnapshot(ctx, normalizedProfile, normalizedScope, prepared)
}

// RebuildSnapshotIncremental delegates only to an adapter that supports
// explicit copy-forward. Falling back to a full rebuild would hide removals
// and overstate reuse, so unsupported stores fail closed.
func (p *EmbeddingProjection) RebuildSnapshotIncremental(ctx context.Context, profile EmbeddingProfile, scope EmbeddingScope, previousSnapshotID string, inputs []IncrementalEmbeddingInput) (EmbeddingRebuildResult, error) {
	if err := validateEmbeddingContext(ctx); err != nil {
		return EmbeddingRebuildResult{}, err
	}
	if p == nil || p.store == nil {
		return EmbeddingRebuildResult{}, ErrEmbeddingProjectionNotConfigured
	}
	normalizedProfile, normalizedScope, err := normalizeEmbeddingProfileScope(profile, scope)
	if err != nil {
		return EmbeddingRebuildResult{}, err
	}
	if err := validateEmbeddingUUID("previous_snapshot_id", previousSnapshotID); err != nil {
		return EmbeddingRebuildResult{}, err
	}
	if previousSnapshotID == normalizedScope.SnapshotID {
		return EmbeddingRebuildResult{}, fmt.Errorf("%w: incremental snapshots must differ", ErrInvalidEmbeddingProjection)
	}
	prepared := make([]IncrementalEmbeddingInput, 0, len(inputs))
	seenKeys := make(map[string]struct{}, len(inputs))
	seenEvidence := make(map[string]struct{}, len(inputs))
	for _, candidate := range inputs {
		if strings.TrimSpace(candidate.StableKey) == "" {
			return EmbeddingRebuildResult{}, fmt.Errorf("%w: incremental stable key is required", ErrInvalidEmbeddingProjection)
		}
		if _, exists := seenKeys[candidate.StableKey]; exists {
			return EmbeddingRebuildResult{}, fmt.Errorf("%w: duplicate incremental stable key", ErrInvalidEmbeddingProjection)
		}
		normalized, err := candidate.Input.Normalize(normalizedProfile, normalizedScope)
		if err != nil {
			return EmbeddingRebuildResult{}, err
		}
		if _, exists := seenEvidence[normalized.EvidenceID]; exists {
			return EmbeddingRebuildResult{}, fmt.Errorf("%w: duplicate incremental evidence identity", ErrInvalidEmbeddingProjection)
		}
		if candidate.Reuse {
			if err := validateEmbeddingUUID("previous_evidence_id", candidate.PreviousEvidenceID); err != nil {
				return EmbeddingRebuildResult{}, err
			}
		}
		candidate.Input = normalized
		prepared = append(prepared, candidate)
		seenKeys[candidate.StableKey] = struct{}{}
		seenEvidence[normalized.EvidenceID] = struct{}{}
	}
	sort.Slice(prepared, func(i, j int) bool { return prepared[i].Input.EvidenceID < prepared[j].Input.EvidenceID })
	store, ok := p.store.(IncrementalEmbeddingStore)
	if !ok {
		return EmbeddingRebuildResult{}, ErrEmbeddingProjectionNotConfigured
	}
	return store.RebuildSnapshotIncremental(ctx, normalizedProfile, normalizedScope, previousSnapshotID, prepared)
}

// OrderEmbeddingItems applies the only ordering guarantee needed before the
// exact vector-search task: all rows must belong to one profile and scope,
// then are sorted by stable canonical IDs. It refuses mixed profiles rather
// than silently combining incompatible vector spaces.
func OrderEmbeddingItems(profile EmbeddingProfile, scope EmbeddingScope, items []EmbeddingItem) ([]EmbeddingItem, error) {
	normalizedProfile, normalizedScope, err := normalizeEmbeddingProfileScope(profile, scope)
	if err != nil {
		return nil, err
	}
	ordered := make([]EmbeddingItem, 0, len(items))
	for _, item := range items {
		normalized, err := item.Normalize(normalizedProfile, normalizedScope)
		if err != nil {
			if errors.Is(err, ErrEmbeddingProfileMix) {
				return nil, err
			}
			return nil, err
		}
		if normalized.State != "ready" {
			return nil, fmt.Errorf("%w: stale embedding cannot be ordered", ErrInvalidEmbeddingProjection)
		}
		ordered = append(ordered, normalized)
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].EvidenceID != ordered[j].EvidenceID {
			return ordered[i].EvidenceID < ordered[j].EvidenceID
		}
		return ordered[i].ID < ordered[j].ID
	})
	return ordered, nil
}

func prepareEmbeddingRebuild(profile EmbeddingProfile, scope EmbeddingScope, inputs []EmbeddingInput) (EmbeddingProfile, EmbeddingScope, []EmbeddingInput, error) {
	normalizedProfile, normalizedScope, err := normalizeEmbeddingProfileScope(profile, scope)
	if err != nil {
		return EmbeddingProfile{}, EmbeddingScope{}, nil, err
	}
	prepared := make([]EmbeddingInput, 0, len(inputs))
	seenIDs := make(map[string]struct{}, len(inputs))
	seenEvidence := make(map[string]struct{}, len(inputs))
	for _, input := range inputs {
		normalized, err := input.Normalize(normalizedProfile, normalizedScope)
		if err != nil {
			return EmbeddingProfile{}, EmbeddingScope{}, nil, err
		}
		if _, exists := seenIDs[normalized.ID]; exists {
			return EmbeddingProfile{}, EmbeddingScope{}, nil, fmt.Errorf("%w: duplicate embedding identity", ErrInvalidEmbeddingProjection)
		}
		if _, exists := seenEvidence[normalized.EvidenceID]; exists {
			return EmbeddingProfile{}, EmbeddingScope{}, nil, fmt.Errorf("%w: duplicate evidence identity", ErrInvalidEmbeddingProjection)
		}
		seenIDs[normalized.ID] = struct{}{}
		seenEvidence[normalized.EvidenceID] = struct{}{}
		prepared = append(prepared, normalized)
	}
	sort.Slice(prepared, func(i, j int) bool {
		if prepared[i].EvidenceID != prepared[j].EvidenceID {
			return prepared[i].EvidenceID < prepared[j].EvidenceID
		}
		return prepared[i].ID < prepared[j].ID
	})
	return normalizedProfile, normalizedScope, prepared, nil
}

func normalizeEmbeddingProfileScope(profile EmbeddingProfile, scope EmbeddingScope) (EmbeddingProfile, EmbeddingScope, error) {
	normalizedProfile, err := profile.Normalize()
	if err != nil {
		return EmbeddingProfile{}, EmbeddingScope{}, err
	}
	normalizedScope, err := scope.Normalize()
	if err != nil {
		return EmbeddingProfile{}, EmbeddingScope{}, err
	}
	if normalizedProfile.OrganizationID != normalizedScope.OrganizationID {
		return EmbeddingProfile{}, EmbeddingScope{}, fmt.Errorf("%w: profile organization differs from scope", ErrEmbeddingScopeMismatch)
	}
	return normalizedProfile, normalizedScope, nil
}

func validateEmbeddingVector(vector []float32, dimension int) error {
	if len(vector) != dimension {
		return fmt.Errorf("%w: vector dimension is invalid", ErrInvalidEmbeddingProjection)
	}
	for _, value := range vector {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return fmt.Errorf("%w: vector contains non-finite value", ErrInvalidEmbeddingProjection)
		}
	}
	return nil
}

func isEmbeddingSHA256(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validateEmbeddingContext(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("%w: context is nil", ErrInvalidEmbeddingProjection)
	}
	return ctx.Err()
}

func validateEmbeddingUUID(field, value string) error {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return fmt.Errorf("%w: %s must be a uuid", ErrInvalidEmbeddingProjection, field)
	}
	for index, character := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			continue
		}
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f') || (character >= 'A' && character <= 'F')) {
			return fmt.Errorf("%w: %s must be a uuid", ErrInvalidEmbeddingProjection, field)
		}
	}
	return nil
}

func validateEmbeddingIdentifier(field, value string) error {
	if value == "" || len(value) > maxEmbeddingIdentifierBytes || !utf8.ValidString(value) {
		return fmt.Errorf("%w: %s is invalid", ErrInvalidEmbeddingProjection, field)
	}
	for _, character := range value {
		if unicode.IsControl(character) || character == '\x00' {
			return fmt.Errorf("%w: %s is invalid", ErrInvalidEmbeddingProjection, field)
		}
	}
	return nil
}

func normalizeEmbeddingConfiguration(configuration json.RawMessage) (json.RawMessage, error) {
	trimmed := bytes.TrimSpace(configuration)
	if len(trimmed) == 0 {
		trimmed = []byte("{}")
	}
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, fmt.Errorf("%w: configuration must be a JSON object", ErrInvalidEmbeddingProjection)
	}
	canonical, err := canonicalizeEmbeddingJSON(trimmed, true)
	if err != nil {
		return nil, err
	}
	return canonical, nil
}

func canonicalizeEmbeddingJSON(value json.RawMessage, root bool) (json.RawMessage, error) {
	trimmed := bytes.TrimSpace(value)
	if !json.Valid(trimmed) {
		return nil, fmt.Errorf("%w: configuration is not valid JSON", ErrInvalidEmbeddingProjection)
	}
	switch trimmed[0] {
	case '{':
		var object map[string]json.RawMessage
		if err := json.Unmarshal(trimmed, &object); err != nil || object == nil {
			return nil, fmt.Errorf("%w: configuration must be a JSON object", ErrInvalidEmbeddingProjection)
		}
		for key, nested := range object {
			if embeddingSecretKey(key) {
				return nil, fmt.Errorf("%w: configuration contains a prohibited secret", ErrInvalidEmbeddingProjection)
			}
			canonical, err := canonicalizeEmbeddingJSON(nested, false)
			if err != nil {
				return nil, err
			}
			object[key] = canonical
		}
		canonical, err := json.Marshal(object)
		if err != nil {
			return nil, fmt.Errorf("%w: configuration cannot be normalized", ErrInvalidEmbeddingProjection)
		}
		return canonical, nil
	case '[':
		var values []json.RawMessage
		if err := json.Unmarshal(trimmed, &values); err != nil {
			return nil, fmt.Errorf("%w: configuration array is invalid", ErrInvalidEmbeddingProjection)
		}
		for index, nested := range values {
			canonical, err := canonicalizeEmbeddingJSON(nested, false)
			if err != nil {
				return nil, err
			}
			values[index] = canonical
		}
		canonical, err := json.Marshal(values)
		if err != nil {
			return nil, fmt.Errorf("%w: configuration cannot be normalized", ErrInvalidEmbeddingProjection)
		}
		return canonical, nil
	default:
		if root {
			return nil, fmt.Errorf("%w: configuration must be a JSON object", ErrInvalidEmbeddingProjection)
		}
		compact := append(json.RawMessage(nil), trimmed...)
		return compact, nil
	}
}

func embeddingSecretKey(key string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(key), "-", "_"))
	for _, secret := range []string{
		"api_key", "apikey", "authorization", "credential", "password",
		"private_key", "refresh_token", "secret", "access_token",
	} {
		if normalized == secret || strings.HasSuffix(normalized, "_"+secret) {
			return true
		}
	}
	return false
}
