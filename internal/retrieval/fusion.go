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
)

const (
	DefaultFusionRRFK           = 60
	DefaultFusionMaxCandidates  = 20
	DefaultFusionRelationBudget = 100
	MaxFusionRRFK               = 10000
	MaxFusionCandidates         = 1000
	MaxFusionRelationBudget     = 10000
)

var (
	ErrInvalidFusion       = errors.New("retrieval: invalid fusion input")
	ErrFusionScopeMismatch = errors.New("retrieval: fusion scope mismatch")
	ErrFusionProfileMix    = errors.New("retrieval: fusion embedding profiles cannot be mixed")
	ErrFusionNotConfigured = errors.New("retrieval: fusion is not configured")
)

// FusionScope is the mandatory boundary for every retrieval signal. A
// candidate from another organization, source, or snapshot is never merged
// into this scope.
type FusionScope struct {
	OrganizationID string
	SourceID       string
	SnapshotID     string
}

func (s FusionScope) Normalize() (FusionScope, error) {
	s.OrganizationID = strings.ToLower(strings.TrimSpace(s.OrganizationID))
	s.SourceID = strings.ToLower(strings.TrimSpace(s.SourceID))
	s.SnapshotID = strings.ToLower(strings.TrimSpace(s.SnapshotID))
	for name, value := range map[string]string{
		"organization_id": s.OrganizationID,
		"source_id":       s.SourceID,
		"snapshot_id":     s.SnapshotID,
	} {
		if err := validateEmbeddingUUID(name, value); err != nil {
			return FusionScope{}, fmt.Errorf("%w: %v", ErrInvalidFusion, err)
		}
	}
	return s, nil
}

// ExactHit represents the exact technical-match ranking supplied by an
// upstream retrieval signal. Rank is one-based and is normalized again after
// deterministic tie-breaking.
type ExactHit struct {
	EvidenceID          string
	OrganizationID      string
	SourceID            string
	SnapshotID          string
	EvidenceContentHash string
	Rank                int
}

// RelationSeed identifies an already selected candidate and its entity anchor
// for the optional, one-hop relation expansion.
type RelationSeed struct {
	EvidenceID string
	EntityID   string
}

// FusionEvidenceReference is the organization-scoped identity used when a
// relation expansion resolves an entity to an Evidence Unit. A bare UUID is
// intentionally insufficient: relation-derived candidates must carry the
// same scope and factual content hash as candidates from other signals.
type FusionEvidenceReference struct {
	EvidenceID          string
	OrganizationID      string
	SourceID            string
	SnapshotID          string
	EvidenceContentHash string
}

// FusionConfiguration is the immutable configuration recorded with every
// fusion response. Scores use reciprocal rank fusion:
// weight / (RRFK + rank). RelationBudget and RelationFanOut are independent
// from the final candidate budget.
type FusionConfiguration struct {
	Version           string            `json:"version"`
	RRFK              int               `json:"rrf_k"`
	ExactWeight       float64           `json:"exact_weight"`
	TextualWeight     float64           `json:"textual_weight"`
	VectorWeight      float64           `json:"vector_weight"`
	RelationWeight    float64           `json:"relation_weight"`
	MaxCandidates     int               `json:"max_candidates"`
	RelationBudget    int               `json:"relation_budget"`
	RelationFanOut    int               `json:"relation_fan_out"`
	RelationMaxHops   int               `json:"relation_max_hops"`
	RelationDirection RelationDirection `json:"relation_direction"`
	Digest            string            `json:"-"`
}

// Normalize applies bounded defaults and computes the canonical SHA-256
// registration digest. A supplied digest must match the normalized
// configuration; the digest never contains credentials or evidence content.
func (c FusionConfiguration) Normalize() (FusionConfiguration, error) {
	c.Version = strings.TrimSpace(c.Version)
	if c.Version == "" {
		c.Version = "rrf-v1"
	}
	if err := validateFusionIdentifier("version", c.Version); err != nil {
		return FusionConfiguration{}, err
	}
	if c.RRFK == 0 {
		c.RRFK = DefaultFusionRRFK
	}
	if c.RRFK < 1 || c.RRFK > MaxFusionRRFK {
		return FusionConfiguration{}, fmt.Errorf("%w: rrf k is invalid", ErrInvalidFusion)
	}
	if c.ExactWeight == 0 && c.TextualWeight == 0 && c.VectorWeight == 0 && c.RelationWeight == 0 {
		c.ExactWeight, c.TextualWeight, c.VectorWeight, c.RelationWeight = 1, 1, 1, 1
	}
	for name, value := range map[string]float64{
		"exact_weight":    c.ExactWeight,
		"textual_weight":  c.TextualWeight,
		"vector_weight":   c.VectorWeight,
		"relation_weight": c.RelationWeight,
	} {
		if !isFiniteFusionFloat(value) || value < 0 {
			return FusionConfiguration{}, fmt.Errorf("%w: %s is invalid", ErrInvalidFusion, name)
		}
	}
	if c.ExactWeight == 0 && c.TextualWeight == 0 && c.VectorWeight == 0 && c.RelationWeight == 0 {
		return FusionConfiguration{}, fmt.Errorf("%w: all fusion weights are zero", ErrInvalidFusion)
	}
	if c.MaxCandidates == 0 {
		c.MaxCandidates = DefaultFusionMaxCandidates
	}
	if c.MaxCandidates < 1 || c.MaxCandidates > MaxFusionCandidates {
		return FusionConfiguration{}, fmt.Errorf("%w: max candidates is invalid", ErrInvalidFusion)
	}
	if c.RelationBudget == 0 {
		c.RelationBudget = DefaultFusionRelationBudget
	}
	if c.RelationBudget < 1 || c.RelationBudget > MaxFusionRelationBudget {
		return FusionConfiguration{}, fmt.Errorf("%w: relation budget is invalid", ErrInvalidFusion)
	}
	if c.RelationFanOut == 0 {
		c.RelationFanOut = DefaultRelationFanOut
	}
	if c.RelationFanOut < 1 || c.RelationFanOut > MaxRelationFanOut {
		return FusionConfiguration{}, fmt.Errorf("%w: relation fan-out is invalid", ErrInvalidFusion)
	}
	if c.RelationMaxHops == 0 {
		c.RelationMaxHops = MaxRelationHops
	}
	if c.RelationMaxHops != MaxRelationHops {
		return FusionConfiguration{}, fmt.Errorf("%w: relation expansion supports one hop only", ErrInvalidFusion)
	}
	if c.RelationDirection == "" {
		c.RelationDirection = RelationDirectionBoth
	}
	switch c.RelationDirection {
	case RelationDirectionOutbound, RelationDirectionInbound, RelationDirectionBoth:
	default:
		return FusionConfiguration{}, fmt.Errorf("%w: relation direction is invalid", ErrInvalidFusion)
	}
	suppliedDigest := strings.ToLower(strings.TrimSpace(c.Digest))
	c.Digest = ""
	canonical, err := json.Marshal(c)
	if err != nil {
		return FusionConfiguration{}, fmt.Errorf("%w: configuration cannot be encoded", ErrInvalidFusion)
	}
	digest := sha256.Sum256(canonical)
	computed := hex.EncodeToString(digest[:])
	if suppliedDigest != "" && suppliedDigest != computed {
		return FusionConfiguration{}, fmt.Errorf("%w: configuration digest does not match", ErrInvalidFusion)
	}
	c.Digest = computed
	return c, nil
}

func (c FusionConfiguration) Validate() error {
	_, err := c.Normalize()
	return err
}

// FusionRequest groups independent retrieval signals. Vector may be empty
// when embeddings are unavailable or prohibited; this is a valid degraded
// request and does not disable textual or relational retrieval.
type FusionRequest struct {
	Scope              FusionScope
	Configuration      FusionConfiguration
	Exact              []ExactHit
	Textual            []TextHit
	Vector             []VectorHit
	RelationStore      RelationStore
	RelationSeeds      []RelationSeed
	EvidenceByEntityID map[string][]FusionEvidenceReference
}

// FusionInput is an operation-neutral alias for callers that use input
// terminology.
type FusionInput = FusionRequest

type FusionSignalKind string

const (
	FusionSignalExact    FusionSignalKind = "exact"
	FusionSignalTextual  FusionSignalKind = "textual"
	FusionSignalVector   FusionSignalKind = "vector"
	FusionSignalRelation FusionSignalKind = "relation"
)

// FusionSignal explains one contribution to a candidate score without
// copying source content into the fusion result.
type FusionSignal struct {
	Kind                FusionSignalKind
	Rank                int
	Weight              float64
	Contribution        float64
	ExactMatch          bool
	EvidenceContentHash string
	ProfileID           string
	Provider            string
	Model               string
}

type FusionProvenance struct {
	EvidenceID          string
	OrganizationID      string
	SourceID            string
	SnapshotID          string
	EvidenceContentHash string
	ProfileID           string
	Provider            string
	Model               string
}

type RelationSignal struct {
	Rank           int
	Weight         float64
	Contribution   float64
	SeedEvidenceID string
	Relation       RelationHit
}

// FusionCandidate is the deduplicated, explainable output consumed by later
// package composition. Relations retain their original one-hop provenance.
type FusionCandidate struct {
	EvidenceID          string
	OrganizationID      string
	SourceID            string
	SnapshotID          string
	Score               float64
	Rank                int
	Signals             []FusionSignal
	RelationSignals     []RelationSignal
	Provenance          FusionProvenance
	ConfigurationDigest string
}

type FusionTelemetry struct {
	ExactInputCount        int
	TextualInputCount      int
	VectorInputCount       int
	InitialCandidateCount  int
	RelationRequestCount   int
	RelationResultCount    int
	RelationCandidateCount int
	FinalCandidateCount    int
	VectorAvailable        bool
}

type FusionResponse struct {
	Candidates         []FusionCandidate
	Configuration      FusionConfiguration
	Degraded           bool
	DegradationReasons []string
	Telemetry          FusionTelemetry
}

// FusedResult is an operation-neutral alias for the response.
type FusedResult = FusionResponse

// Fuse merges exact, textual, vector, and optional one-hop relational signals
// deterministically. It is stateless and does not call a model or inspect the
// source filesystem.
func Fuse(ctx context.Context, request FusionRequest) (FusionResponse, error) {
	if ctx == nil {
		return FusionResponse{}, fmt.Errorf("%w: context is nil", ErrInvalidFusion)
	}
	if err := ctx.Err(); err != nil {
		return FusionResponse{}, err
	}
	scope, err := request.Scope.Normalize()
	if err != nil {
		return FusionResponse{}, err
	}
	configuration, err := request.Configuration.Normalize()
	if err != nil {
		return FusionResponse{}, err
	}
	if err := validateFusionRequestLimits(request); err != nil {
		return FusionResponse{}, err
	}
	telemetry := FusionTelemetry{
		ExactInputCount:   len(request.Exact),
		TextualInputCount: len(request.Textual),
		VectorInputCount:  len(request.Vector),
		VectorAvailable:   len(request.Vector) > 0,
	}
	degradation := make(map[string]struct{})
	if len(request.Vector) == 0 && configuration.VectorWeight > 0 {
		degradation["vector_unavailable"] = struct{}{}
	}

	accumulators := make(map[string]*fusionAccumulator)
	if err := addExactSignals(scope, configuration, request.Exact, accumulators); err != nil {
		return FusionResponse{}, err
	}
	if err := addTextualSignals(scope, configuration, request.Textual, accumulators); err != nil {
		return FusionResponse{}, err
	}
	if err := addVectorSignals(scope, configuration, request.Vector, accumulators); err != nil {
		return FusionResponse{}, err
	}
	telemetry.InitialCandidateCount = len(accumulators)
	if err := validateRelationSeeds(request.RelationSeeds); err != nil {
		return FusionResponse{}, err
	}
	normalizedEvidenceByEntity, err := normalizeFusionEvidenceReferences(scope, request.EvidenceByEntityID)
	if err != nil {
		return FusionResponse{}, err
	}
	request.EvidenceByEntityID = normalizedEvidenceByEntity
	if len(request.RelationSeeds) > 0 && configuration.RelationWeight > 0 {
		if request.RelationStore == nil {
			degradation["relation_unavailable"] = struct{}{}
		} else {
			if err := expandFusionRelations(ctx, scope, configuration, request, accumulators, &telemetry); err != nil {
				return FusionResponse{}, err
			}
		}
	}
	if len(accumulators) == 0 {
		degradation["no_retrieval_candidates"] = struct{}{}
	}
	candidates := finalizeFusionCandidates(accumulators, configuration)
	telemetry.FinalCandidateCount = len(candidates)
	reasons := make([]string, 0, len(degradation))
	for reason := range degradation {
		reasons = append(reasons, reason)
	}
	sort.Strings(reasons)
	for index := range candidates {
		candidates[index].ConfigurationDigest = configuration.Digest
	}
	return FusionResponse{
		Candidates:         candidates,
		Configuration:      configuration,
		Degraded:           len(reasons) > 0,
		DegradationReasons: reasons,
		Telemetry:          telemetry,
	}, nil
}

func validateFusionRequestLimits(request FusionRequest) error {
	for name, count := range map[string]int{
		"exact":   len(request.Exact),
		"textual": len(request.Textual),
		"vector":  len(request.Vector),
	} {
		if count > MaxFusionCandidates {
			return fmt.Errorf("%w: %s signal count exceeds limit", ErrInvalidFusion, name)
		}
	}
	if len(request.RelationSeeds) > MaxFusionRelationBudget {
		return fmt.Errorf("%w: relation seed count exceeds limit", ErrInvalidFusion)
	}
	if len(request.EvidenceByEntityID) > MaxFusionRelationBudget {
		return fmt.Errorf("%w: relation entity count exceeds limit", ErrInvalidFusion)
	}
	totalReferences := 0
	for _, references := range request.EvidenceByEntityID {
		if len(references) > MaxFusionRelationBudget || totalReferences > MaxFusionRelationBudget-len(references) {
			return fmt.Errorf("%w: relation evidence reference count exceeds limit", ErrInvalidFusion)
		}
		totalReferences += len(references)
	}
	return nil
}

// NewFusion returns a stateless fusion service for dependency-injection
// oriented callers.
type Fusion struct{}

func NewFusion() *Fusion { return &Fusion{} }

func (f *Fusion) Fuse(ctx context.Context, request FusionRequest) (FusionResponse, error) {
	if f == nil {
		return FusionResponse{}, ErrFusionNotConfigured
	}
	return Fuse(ctx, request)
}

type fusionAccumulator struct {
	candidate    FusionCandidate
	signalKinds  map[FusionSignalKind]struct{}
	relationKeys map[string]struct{}
	contentHash  string
}

func addExactSignals(scope FusionScope, configuration FusionConfiguration, hits []ExactHit, accumulators map[string]*fusionAccumulator) error {
	prepared := make([]ExactHit, 0, len(hits))
	seen := make(map[string]struct{}, len(hits))
	for _, hit := range hits {
		normalized, err := normalizeExactHit(scope, hit)
		if err != nil {
			return err
		}
		if _, exists := seen[normalized.EvidenceID]; exists {
			return fmt.Errorf("%w: duplicate exact evidence", ErrInvalidFusion)
		}
		seen[normalized.EvidenceID] = struct{}{}
		prepared = append(prepared, normalized)
	}
	sort.SliceStable(prepared, func(i, j int) bool {
		if prepared[i].Rank != prepared[j].Rank {
			return prepared[i].Rank < prepared[j].Rank
		}
		return prepared[i].EvidenceID < prepared[j].EvidenceID
	})
	for index, hit := range prepared {
		rank := index + 1
		contribution := reciprocalRankContribution(configuration.ExactWeight, configuration.RRFK, rank)
		if configuration.ExactWeight == 0 {
			continue
		}
		accumulator, err := ensureFusionAccumulator(scope, hit.EvidenceID, hit.EvidenceContentHash, accumulators)
		if err != nil {
			return err
		}
		if _, exists := accumulator.signalKinds[FusionSignalExact]; exists {
			return fmt.Errorf("%w: duplicate exact signal", ErrInvalidFusion)
		}
		accumulator.signalKinds[FusionSignalExact] = struct{}{}
		accumulator.candidate.Score += contribution
		accumulator.candidate.Signals = append(accumulator.candidate.Signals, FusionSignal{
			Kind: FusionSignalExact, Rank: rank, Weight: configuration.ExactWeight,
			Contribution: contribution, EvidenceContentHash: hit.EvidenceContentHash,
		})
	}
	return nil
}

func addTextualSignals(scope FusionScope, configuration FusionConfiguration, hits []TextHit, accumulators map[string]*fusionAccumulator) error {
	prepared := make([]TextHit, 0, len(hits))
	seen := make(map[string]struct{}, len(hits))
	for _, hit := range hits {
		normalized, err := normalizeFusionTextHit(scope, hit)
		if err != nil {
			return err
		}
		if _, exists := seen[normalized.EvidenceID]; exists {
			return fmt.Errorf("%w: duplicate textual evidence", ErrInvalidFusion)
		}
		seen[normalized.EvidenceID] = struct{}{}
		prepared = append(prepared, normalized)
	}
	sort.SliceStable(prepared, func(i, j int) bool {
		if prepared[i].Rank != prepared[j].Rank {
			return prepared[i].Rank > prepared[j].Rank
		}
		if prepared[i].ExactMatch != prepared[j].ExactMatch {
			return prepared[i].ExactMatch
		}
		return prepared[i].EvidenceID < prepared[j].EvidenceID
	})
	for index, hit := range prepared {
		rank := index + 1
		if configuration.TextualWeight == 0 {
			continue
		}
		contribution := reciprocalRankContribution(configuration.TextualWeight, configuration.RRFK, rank)
		accumulator, err := ensureFusionAccumulator(scope, hit.EvidenceID, hit.ContentHash, accumulators)
		if err != nil {
			return err
		}
		if _, exists := accumulator.signalKinds[FusionSignalTextual]; exists {
			return fmt.Errorf("%w: duplicate textual signal", ErrInvalidFusion)
		}
		accumulator.signalKinds[FusionSignalTextual] = struct{}{}
		accumulator.candidate.Score += contribution
		accumulator.candidate.Signals = append(accumulator.candidate.Signals, FusionSignal{
			Kind: FusionSignalTextual, Rank: rank, Weight: configuration.TextualWeight,
			Contribution: contribution, ExactMatch: hit.ExactMatch,
			EvidenceContentHash: hit.ContentHash,
		})
	}
	return nil
}

func addVectorSignals(scope FusionScope, configuration FusionConfiguration, hits []VectorHit, accumulators map[string]*fusionAccumulator) error {
	if len(hits) == 0 {
		return nil
	}
	profile, err := hits[0].Profile.Normalize()
	if err != nil {
		return fmt.Errorf("%w: vector result profile is invalid", ErrFusionProfileMix)
	}
	if profile.ID == "" {
		return fmt.Errorf("%w: vector result has no embedding profile", ErrFusionProfileMix)
	}
	query := VectorSearchQuery{
		OrganizationID: scope.OrganizationID,
		SourceID:       scope.SourceID,
		SnapshotID:     scope.SnapshotID,
		Profile:        profile,
		Vector:         make([]float32, profile.Dimension),
		Limit:          MaxVectorSearchLimit,
	}
	normalizedQuery, err := query.Normalize()
	if err != nil {
		return fmt.Errorf("%w: vector query profile: %v", ErrFusionProfileMix, err)
	}
	prepared := make([]VectorHit, 0, len(hits))
	seen := make(map[string]struct{}, len(hits))
	for _, hit := range hits {
		hitProfile, profileErr := hit.Profile.Normalize()
		if profileErr != nil || !sameFusionEmbeddingProfile(hitProfile, normalizedQuery.Profile) || hit.ProfileDimension != normalizedQuery.Profile.Dimension {
			return fmt.Errorf("%w: vector results contain multiple profiles", ErrFusionProfileMix)
		}
		normalized, err := hit.Normalize(normalizedQuery)
		if err != nil {
			return err
		}
		if _, exists := seen[normalized.EvidenceID]; exists {
			return fmt.Errorf("%w: duplicate vector evidence", ErrInvalidFusion)
		}
		seen[normalized.EvidenceID] = struct{}{}
		prepared = append(prepared, normalized)
	}
	allRanks := true
	seenRanks := make(map[int]struct{}, len(prepared))
	for _, hit := range prepared {
		if hit.Rank < 1 {
			allRanks = false
			break
		}
		if _, exists := seenRanks[hit.Rank]; exists {
			allRanks = false
			break
		}
		seenRanks[hit.Rank] = struct{}{}
	}
	sort.SliceStable(prepared, func(i, j int) bool {
		if allRanks && prepared[i].Rank != prepared[j].Rank {
			return prepared[i].Rank < prepared[j].Rank
		}
		if prepared[i].Distance != prepared[j].Distance {
			return prepared[i].Distance < prepared[j].Distance
		}
		return prepared[i].EvidenceID < prepared[j].EvidenceID
	})
	for index, hit := range prepared {
		rank := index + 1
		if configuration.VectorWeight == 0 {
			continue
		}
		contribution := reciprocalRankContribution(configuration.VectorWeight, configuration.RRFK, rank)
		accumulator, err := ensureFusionAccumulator(scope, hit.EvidenceID, hit.EvidenceContentHash, accumulators)
		if err != nil {
			return err
		}
		if _, exists := accumulator.signalKinds[FusionSignalVector]; exists {
			return fmt.Errorf("%w: duplicate vector signal", ErrInvalidFusion)
		}
		accumulator.signalKinds[FusionSignalVector] = struct{}{}
		accumulator.candidate.Score += contribution
		accumulator.candidate.Signals = append(accumulator.candidate.Signals, FusionSignal{
			Kind: FusionSignalVector, Rank: rank, Weight: configuration.VectorWeight,
			Contribution: contribution, EvidenceContentHash: hit.EvidenceContentHash,
			ProfileID: hit.Profile.ID, Provider: hit.Provider, Model: hit.Model,
		})
		accumulator.candidate.Provenance.ProfileID = hit.Profile.ID
		accumulator.candidate.Provenance.Provider = hit.Provider
		accumulator.candidate.Provenance.Model = hit.Model
	}
	return nil
}

func expandFusionRelations(ctx context.Context, scope FusionScope, configuration FusionConfiguration, request FusionRequest, accumulators map[string]*fusionAccumulator, telemetry *FusionTelemetry) error {
	if configuration.RelationWeight == 0 || configuration.RelationMaxHops == 0 {
		return nil
	}
	projection := NewRelationProjection(request.RelationStore)
	seeds := make([]RelationSeed, 0, len(request.RelationSeeds))
	for _, seed := range request.RelationSeeds {
		if _, exists := accumulators[strings.ToLower(strings.TrimSpace(seed.EvidenceID))]; !exists {
			continue
		}
		seeds = append(seeds, RelationSeed{
			EvidenceID: strings.ToLower(strings.TrimSpace(seed.EvidenceID)),
			EntityID:   strings.ToLower(strings.TrimSpace(seed.EntityID)),
		})
	}
	sort.SliceStable(seeds, func(i, j int) bool {
		left, right := accumulators[seeds[i].EvidenceID], accumulators[seeds[j].EvidenceID]
		if left.candidate.Score != right.candidate.Score {
			return left.candidate.Score > right.candidate.Score
		}
		if seeds[i].EvidenceID != seeds[j].EvidenceID {
			return seeds[i].EvidenceID < seeds[j].EvidenceID
		}
		return seeds[i].EntityID < seeds[j].EntityID
	})
	remainingWork := configuration.RelationBudget
	relationRank := 0
	for _, seed := range seeds {
		if remainingWork <= 0 {
			break
		}
		limit := configuration.RelationFanOut
		if limit > remainingWork {
			limit = remainingWork
		}
		telemetry.RelationRequestCount++
		hits, err := projection.Expand(ctx, RelationQuery{
			OrganizationID: scope.OrganizationID,
			SourceID:       scope.SourceID,
			SnapshotID:     scope.SnapshotID,
			AnchorEntityID: seed.EntityID,
			Direction:      configuration.RelationDirection,
			MaxHops:        configuration.RelationMaxHops,
			FanOut:         limit,
		})
		if err != nil {
			return err
		}
		if len(hits) > remainingWork {
			hits = hits[:remainingWork]
		}
		remainingWork -= len(hits)
		telemetry.RelationResultCount += len(hits)
		for _, hit := range hits {
			targetEntityID := relationTargetEntity(seed.EntityID, hit)
			if targetEntityID == "" {
				continue
			}
			evidenceReferences := evidenceReferencesForEntity(request.EvidenceByEntityID, targetEntityID, accumulators)
			for _, reference := range evidenceReferences {
				if remainingWork <= 0 {
					break
				}
				remainingWork--
				evidenceID := reference.EvidenceID
				relationRank++
				accumulator, err := ensureFusionAccumulator(scope, evidenceID, reference.EvidenceContentHash, accumulators)
				if err != nil {
					return err
				}
				key := seed.EvidenceID + "\x00" + hit.RelationID + "\x00" + evidenceID
				if _, exists := accumulator.relationKeys[key]; exists {
					continue
				}
				accumulator.relationKeys[key] = struct{}{}
				contribution := reciprocalRankContribution(configuration.RelationWeight, configuration.RRFK, relationRank)
				accumulator.candidate.Score += contribution
				accumulator.candidate.Signals = append(accumulator.candidate.Signals, FusionSignal{
					Kind: FusionSignalRelation, Rank: relationRank,
					Weight: configuration.RelationWeight, Contribution: contribution,
				})
				accumulator.candidate.RelationSignals = append(accumulator.candidate.RelationSignals, RelationSignal{
					Rank: relationRank, Weight: configuration.RelationWeight,
					Contribution: contribution, SeedEvidenceID: seed.EvidenceID,
					Relation: hit,
				})
				telemetry.RelationCandidateCount++
			}
		}
	}
	return nil
}

func finalizeFusionCandidates(accumulators map[string]*fusionAccumulator, configuration FusionConfiguration) []FusionCandidate {
	result := make([]FusionCandidate, 0, len(accumulators))
	for _, accumulator := range accumulators {
		candidate := accumulator.candidate
		sort.SliceStable(candidate.Signals, func(i, j int) bool {
			if candidate.Signals[i].Kind != candidate.Signals[j].Kind {
				return candidate.Signals[i].Kind < candidate.Signals[j].Kind
			}
			return candidate.Signals[i].Rank < candidate.Signals[j].Rank
		})
		sort.SliceStable(candidate.RelationSignals, func(i, j int) bool {
			if candidate.RelationSignals[i].Rank != candidate.RelationSignals[j].Rank {
				return candidate.RelationSignals[i].Rank < candidate.RelationSignals[j].Rank
			}
			return candidate.RelationSignals[i].Relation.RelationID < candidate.RelationSignals[j].Relation.RelationID
		})
		if !isFiniteFusionFloat(candidate.Score) {
			continue
		}
		result = append(result, candidate)
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].Score != result[j].Score {
			return result[i].Score > result[j].Score
		}
		return result[i].EvidenceID < result[j].EvidenceID
	})
	if len(result) > configuration.MaxCandidates {
		result = result[:configuration.MaxCandidates]
	}
	for index := range result {
		result[index].Rank = index + 1
	}
	return result
}

func ensureFusionAccumulator(scope FusionScope, evidenceID, contentHash string, accumulators map[string]*fusionAccumulator) (*fusionAccumulator, error) {
	evidenceID = strings.ToLower(strings.TrimSpace(evidenceID))
	if err := validateEmbeddingUUID("evidence_id", evidenceID); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidFusion, err)
	}
	contentHash = strings.ToLower(strings.TrimSpace(contentHash))
	if contentHash != "" && !isEmbeddingSHA256(contentHash) {
		return nil, fmt.Errorf("%w: evidence content hash is invalid", ErrInvalidFusion)
	}
	if accumulator, exists := accumulators[evidenceID]; exists {
		if contentHash != "" && accumulator.contentHash != "" && accumulator.contentHash != contentHash {
			return nil, fmt.Errorf("%w: evidence provenance hash differs", ErrFusionScopeMismatch)
		}
		if accumulator.contentHash == "" {
			accumulator.contentHash = contentHash
			accumulator.candidate.Provenance.EvidenceContentHash = contentHash
		}
		return accumulator, nil
	}
	accumulator := &fusionAccumulator{
		candidate: FusionCandidate{
			EvidenceID: evidenceID, OrganizationID: scope.OrganizationID,
			SourceID: scope.SourceID, SnapshotID: scope.SnapshotID,
			Provenance: FusionProvenance{
				EvidenceID: evidenceID, OrganizationID: scope.OrganizationID,
				SourceID: scope.SourceID, SnapshotID: scope.SnapshotID,
				EvidenceContentHash: contentHash,
			},
		},
		signalKinds:  make(map[FusionSignalKind]struct{}),
		relationKeys: make(map[string]struct{}),
		contentHash:  contentHash,
	}
	accumulators[evidenceID] = accumulator
	return accumulator, nil
}

func normalizeExactHit(scope FusionScope, hit ExactHit) (ExactHit, error) {
	hit.EvidenceID = strings.ToLower(strings.TrimSpace(hit.EvidenceID))
	hit.OrganizationID = strings.ToLower(strings.TrimSpace(hit.OrganizationID))
	hit.SourceID = strings.ToLower(strings.TrimSpace(hit.SourceID))
	hit.SnapshotID = strings.ToLower(strings.TrimSpace(hit.SnapshotID))
	if err := validateEmbeddingUUID("evidence_id", hit.EvidenceID); err != nil {
		return ExactHit{}, fmt.Errorf("%w: %v", ErrInvalidFusion, err)
	}
	if hit.OrganizationID != scope.OrganizationID || hit.SourceID != scope.SourceID || hit.SnapshotID != scope.SnapshotID {
		return ExactHit{}, fmt.Errorf("%w: exact result is outside scope", ErrFusionScopeMismatch)
	}
	if hit.Rank < 1 {
		return ExactHit{}, fmt.Errorf("%w: exact rank is invalid", ErrInvalidFusion)
	}
	if hit.EvidenceContentHash != "" && !isEmbeddingSHA256(strings.ToLower(hit.EvidenceContentHash)) {
		return ExactHit{}, fmt.Errorf("%w: exact evidence hash is invalid", ErrInvalidFusion)
	}
	hit.EvidenceContentHash = strings.ToLower(strings.TrimSpace(hit.EvidenceContentHash))
	return hit, nil
}

func normalizeFusionTextHit(scope FusionScope, hit TextHit) (TextHit, error) {
	hit.EvidenceID = strings.ToLower(strings.TrimSpace(hit.EvidenceID))
	hit.OrganizationID = strings.ToLower(strings.TrimSpace(hit.OrganizationID))
	hit.SourceID = strings.ToLower(strings.TrimSpace(hit.SourceID))
	hit.SnapshotID = strings.ToLower(strings.TrimSpace(hit.SnapshotID))
	hit.ContentHash = strings.ToLower(strings.TrimSpace(hit.ContentHash))
	if err := validateEmbeddingUUID("evidence_id", hit.EvidenceID); err != nil {
		return TextHit{}, fmt.Errorf("%w: %v", ErrInvalidFusion, err)
	}
	if hit.OrganizationID != scope.OrganizationID || hit.SourceID != scope.SourceID || hit.SnapshotID != scope.SnapshotID {
		return TextHit{}, fmt.Errorf("%w: textual result is outside scope", ErrFusionScopeMismatch)
	}
	if !isFiniteFusionFloat(hit.Rank) || hit.Rank < 0 {
		return TextHit{}, fmt.Errorf("%w: textual rank is invalid", ErrInvalidFusion)
	}
	if hit.ContentHash != "" && !isEmbeddingSHA256(hit.ContentHash) {
		return TextHit{}, fmt.Errorf("%w: textual evidence hash is invalid", ErrInvalidFusion)
	}
	return hit, nil
}

func validateRelationSeeds(seeds []RelationSeed) error {
	seen := make(map[string]struct{}, len(seeds))
	for _, seed := range seeds {
		seed.EvidenceID = strings.ToLower(strings.TrimSpace(seed.EvidenceID))
		seed.EntityID = strings.ToLower(strings.TrimSpace(seed.EntityID))
		if err := validateEmbeddingUUID("relation_seed_evidence_id", seed.EvidenceID); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidFusion, err)
		}
		if err := validateEmbeddingUUID("relation_seed_entity_id", seed.EntityID); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidFusion, err)
		}
		if _, exists := seen[seed.EvidenceID+"\x00"+seed.EntityID]; exists {
			return fmt.Errorf("%w: duplicate relation seed", ErrInvalidFusion)
		}
		seen[seed.EvidenceID+"\x00"+seed.EntityID] = struct{}{}
	}
	return nil
}

func normalizeFusionEvidenceReferences(scope FusionScope, mapping map[string][]FusionEvidenceReference) (map[string][]FusionEvidenceReference, error) {
	normalized := make(map[string][]FusionEvidenceReference, len(mapping))
	seenEntities := make(map[string]struct{}, len(mapping))
	for entityID, references := range mapping {
		entityID = strings.ToLower(strings.TrimSpace(entityID))
		if err := validateEmbeddingUUID("relation_entity_id", entityID); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidFusion, err)
		}
		if _, exists := seenEntities[entityID]; exists {
			return nil, fmt.Errorf("%w: duplicate normalized relation entity", ErrInvalidFusion)
		}
		seenEntities[entityID] = struct{}{}
		if len(references) > MaxFusionRelationBudget {
			return nil, fmt.Errorf("%w: relation evidence references exceed bounded input", ErrInvalidFusion)
		}
		prepared := make([]FusionEvidenceReference, 0, len(references))
		seen := make(map[string]string, len(references))
		for _, reference := range references {
			reference.EvidenceID = strings.ToLower(strings.TrimSpace(reference.EvidenceID))
			reference.OrganizationID = strings.ToLower(strings.TrimSpace(reference.OrganizationID))
			reference.SourceID = strings.ToLower(strings.TrimSpace(reference.SourceID))
			reference.SnapshotID = strings.ToLower(strings.TrimSpace(reference.SnapshotID))
			reference.EvidenceContentHash = strings.ToLower(strings.TrimSpace(reference.EvidenceContentHash))
			if err := validateEmbeddingUUID("relation_evidence_id", reference.EvidenceID); err != nil {
				return nil, fmt.Errorf("%w: %v", ErrInvalidFusion, err)
			}
			if reference.OrganizationID != scope.OrganizationID || reference.SourceID != scope.SourceID || reference.SnapshotID != scope.SnapshotID {
				return nil, fmt.Errorf("%w: relation evidence reference is outside scope", ErrFusionScopeMismatch)
			}
			if !isEmbeddingSHA256(reference.EvidenceContentHash) {
				return nil, fmt.Errorf("%w: relation evidence reference hash is invalid", ErrInvalidFusion)
			}
			if previous, exists := seen[reference.EvidenceID]; exists {
				if previous != reference.EvidenceContentHash {
					return nil, fmt.Errorf("%w: relation evidence reference hash differs", ErrFusionScopeMismatch)
				}
				continue
			}
			seen[reference.EvidenceID] = reference.EvidenceContentHash
			prepared = append(prepared, reference)
		}
		sort.Slice(prepared, func(i, j int) bool {
			if prepared[i].EvidenceID != prepared[j].EvidenceID {
				return prepared[i].EvidenceID < prepared[j].EvidenceID
			}
			return prepared[i].EvidenceContentHash < prepared[j].EvidenceContentHash
		})
		normalized[entityID] = prepared
	}
	return normalized, nil
}

func evidenceReferencesForEntity(mapping map[string][]FusionEvidenceReference, entityID string, accumulators map[string]*fusionAccumulator) []FusionEvidenceReference {
	entityID = strings.ToLower(strings.TrimSpace(entityID))
	references := append([]FusionEvidenceReference(nil), mapping[entityID]...)
	if len(references) == 0 {
		if _, exists := accumulators[entityID]; exists {
			// An existing candidate already carries the scope; the relation
			// signal can attach to it without inventing a new evidence row.
			references = []FusionEvidenceReference{{EvidenceID: entityID, EvidenceContentHash: accumulators[entityID].contentHash}}
		}
	}
	prepared := make([]FusionEvidenceReference, 0, len(references))
	seen := make(map[string]struct{}, len(references))
	for _, reference := range references {
		reference.EvidenceID = strings.ToLower(strings.TrimSpace(reference.EvidenceID))
		if _, exists := seen[reference.EvidenceID]; exists {
			continue
		}
		if _, exists := accumulators[reference.EvidenceID]; exists || isEmbeddingSHA256(reference.EvidenceContentHash) {
			seen[reference.EvidenceID] = struct{}{}
			prepared = append(prepared, reference)
			continue
		}
	}
	return prepared
}

func sameFusionEmbeddingProfile(left, right EmbeddingProfile) bool {
	return left.ID == right.ID && left.OrganizationID == right.OrganizationID &&
		left.Provider == right.Provider && left.Model == right.Model &&
		left.Dimension == right.Dimension && left.Normalization == right.Normalization &&
		left.ConfigurationVersion == right.ConfigurationVersion &&
		left.ConfigurationDigest == right.ConfigurationDigest &&
		bytes.Equal(left.Configuration, right.Configuration)
}

func relationTargetEntity(anchor string, hit RelationHit) string {
	anchor = strings.ToLower(strings.TrimSpace(anchor))
	if hit.FromEntityID == anchor {
		return hit.ToEntityID
	}
	if hit.ToEntityID == anchor {
		return hit.FromEntityID
	}
	return ""
}

func reciprocalRankContribution(weight float64, rrfK, rank int) float64 {
	return weight / float64(rrfK+rank)
}

func validateFusionIdentifier(name, value string) error {
	if value == "" || len(value) > 64 {
		return fmt.Errorf("%w: %s is invalid", ErrInvalidFusion, name)
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("%w: %s is invalid", ErrInvalidFusion, name)
		}
	}
	return nil
}

func isFiniteFusionFloat(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}
