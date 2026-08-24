package query

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sort"
)

const (
	// ContextUtilityVersion identifies the version of the utility contract.
	ContextUtilityVersion = "v1alpha1"
	// ContextSelectionAlgorithm identifies the deterministic selection
	// algorithm. It does not include a token estimator; costs are supplied by
	// the caller until the token-estimation change is implemented.
	ContextSelectionAlgorithm = "greedy-v1"

	maxContextSelectionCandidates = maxContextItems
	maxContextSelectionAspects    = 1_024
	maxContextSelectionWeight     = 1 << 20
	maxContextSelectionScore      = 1 << 20
)

var (
	// ErrInvalidContextSelection is returned for malformed selection input.
	// It aliases the context-contract sentinel so callers can handle both
	// boundaries with one stable error vocabulary.
	ErrInvalidContextSelection = ErrInvalidContext
	// ErrInvalidContextUtilityConfiguration identifies a malformed utility
	// configuration and is kept as a descriptive alias for compatibility with
	// the context contract error vocabulary.
	ErrInvalidContextUtilityConfiguration = ErrInvalidContext
)

// ContextUtilityConfiguration is the versioned, reproducible utility
// function used by greedy context selection. Weights are intentionally
// explicit: changing one changes Digest and therefore the applied algorithm
// identity.
type ContextUtilityConfiguration struct {
	Version                 string  `json:"version"`
	Algorithm               string  `json:"algorithm"`
	RelevanceWeight         float64 `json:"relevance_weight"`
	AspectWeight            float64 `json:"aspect_weight"`
	KindDiversityWeight     float64 `json:"kind_diversity_weight"`
	ArtifactDiversityWeight float64 `json:"artifact_diversity_weight"`
	Digest                  string  `json:"digest,omitempty"`
}

// ContextUtilityConfig is a concise alias for ContextUtilityConfiguration.
type ContextUtilityConfig = ContextUtilityConfiguration

// ContextSelectionConfiguration is a descriptive alias for the applied
// utility configuration.
type ContextSelectionConfiguration = ContextUtilityConfiguration

// DefaultContextUtilityConfiguration returns the initial bounded utility
// weights. The returned value has a deterministic digest.
func DefaultContextUtilityConfiguration() ContextUtilityConfiguration {
	configuration := ContextUtilityConfiguration{
		Version:                 ContextUtilityVersion,
		Algorithm:               ContextSelectionAlgorithm,
		RelevanceWeight:         1,
		AspectWeight:            1,
		KindDiversityWeight:     1,
		ArtifactDiversityWeight: 1,
	}
	return configuration.withDigest()
}

// Normalize fills only the fixed version and algorithm identifiers. Utility
// weights are never inferred from budgets or candidates.
func (c ContextUtilityConfiguration) Normalize() (ContextUtilityConfiguration, error) {
	if c.Version == "" {
		c.Version = ContextUtilityVersion
	}
	if c.Algorithm == "" {
		c.Algorithm = ContextSelectionAlgorithm
	}
	if c.RelevanceWeight == 0 && c.AspectWeight == 0 && c.KindDiversityWeight == 0 && c.ArtifactDiversityWeight == 0 {
		c.RelevanceWeight = 1
		c.AspectWeight = 1
		c.KindDiversityWeight = 1
		c.ArtifactDiversityWeight = 1
	}
	if err := c.Validate(); err != nil {
		return ContextUtilityConfiguration{}, err
	}
	computed := c.withDigest()
	if c.Digest != "" && c.Digest != computed.Digest {
		return ContextUtilityConfiguration{}, fmt.Errorf("%w: digest does not match configuration", ErrInvalidContextUtilityConfiguration)
	}
	return computed, nil
}

// Validate checks fixed versions, finite non-negative weights and the
// bounded digest representation. An all-zero input is normalized to the
// fixed default weights before this validation is normally reached.
func (c ContextUtilityConfiguration) Validate() error {
	if c.Version != ContextUtilityVersion || c.Algorithm != ContextSelectionAlgorithm {
		return fmt.Errorf("%w: unsupported utility version", ErrInvalidContextUtilityConfiguration)
	}
	weights := []float64{c.RelevanceWeight, c.AspectWeight, c.KindDiversityWeight, c.ArtifactDiversityWeight}
	for _, weight := range weights {
		if math.IsNaN(weight) || math.IsInf(weight, 0) || weight < 0 || weight > maxContextSelectionWeight {
			return fmt.Errorf("%w: utility weights must be finite and non-negative", ErrInvalidContextUtilityConfiguration)
		}
	}
	if c.Digest != "" && !isSHA256(c.Digest) {
		return fmt.Errorf("%w: invalid utility digest", ErrInvalidContextUtilityConfiguration)
	}
	return nil
}

// ConfigurationDigest returns the digest of the validated configuration
// without trusting or including its Digest field.
func (c ContextUtilityConfiguration) ConfigurationDigest() (string, error) {
	normalized, err := c.Normalize()
	if err != nil {
		return "", err
	}
	return normalized.Digest, nil
}

// DigestValue is a descriptive spelling for ConfigurationDigest.
func (c ContextUtilityConfiguration) DigestValue() (string, error) {
	return c.ConfigurationDigest()
}

type contextUtilityDigestInput struct {
	Version                 string  `json:"version"`
	Algorithm               string  `json:"algorithm"`
	RelevanceWeight         float64 `json:"relevance_weight"`
	AspectWeight            float64 `json:"aspect_weight"`
	KindDiversityWeight     float64 `json:"kind_diversity_weight"`
	ArtifactDiversityWeight float64 `json:"artifact_diversity_weight"`
}

func (c ContextUtilityConfiguration) withDigest() ContextUtilityConfiguration {
	c.Digest = ""
	encoded, _ := json.Marshal(contextUtilityDigestInput{
		Version:                 c.Version,
		Algorithm:               c.Algorithm,
		RelevanceWeight:         c.RelevanceWeight,
		AspectWeight:            c.AspectWeight,
		KindDiversityWeight:     c.KindDiversityWeight,
		ArtifactDiversityWeight: c.ArtifactDiversityWeight,
	})
	digest := sha256.Sum256(encoded)
	c.Digest = hex.EncodeToString(digest[:])
	return c
}

// ContextSelectionCandidate is one auditable candidate. Costs are supplied by
// the compositor and are not estimated here. TokenCost, CharacterCost and
// ByteCost are the canonical fields; the shorter aliases are accepted for
// callers that use the transport vocabulary.
type ContextSelectionCandidate struct {
	Item          ContextItem `json:"item"`
	Relevance     float64     `json:"relevance"`
	Rank          int         `json:"rank"`
	Aspects       []string    `json:"aspects,omitempty"`
	RedundancyKey string      `json:"redundancy_key"`
	TokenCost     int         `json:"token_cost"`
	CharacterCost int64       `json:"character_cost"`
	ByteCost      int64       `json:"byte_cost"`

	Tokens     int   `json:"tokens,omitempty"`
	Characters int64 `json:"characters,omitempty"`
	Bytes      int64 `json:"bytes,omitempty"`
}

// ContextCandidate is a concise alias for ContextSelectionCandidate.
type ContextCandidate = ContextSelectionCandidate

// Validate checks candidate metadata and its canonical item. Selection
// requests use validateMetadata separately so malformed but safely
// identifiable items can be recorded with ContextSelectionExcludedInvalid.
func (c ContextSelectionCandidate) Validate() error {
	if err := c.validateMetadata(); err != nil {
		return err
	}
	return c.Item.Validate()
}

func (c ContextSelectionCandidate) validateMetadata() error {
	if !validContextID(c.Item.ID) {
		return fmt.Errorf("%w: candidate item id", ErrInvalidContextSelection)
	}
	if math.IsNaN(c.Relevance) || math.IsInf(c.Relevance, 0) || c.Relevance < 0 || c.Relevance > maxContextSelectionScore {
		return fmt.Errorf("%w: candidate relevance", ErrInvalidContextSelection)
	}
	if c.Rank < 0 || c.Rank > maxContextSelectionCandidates {
		return fmt.Errorf("%w: candidate rank", ErrInvalidContextSelection)
	}
	if len(c.Aspects) > maxContextSelectionAspects {
		return fmt.Errorf("%w: candidate aspects", ErrInvalidContextSelection)
	}
	seenAspects := make(map[string]struct{}, len(c.Aspects))
	for _, aspect := range c.Aspects {
		if !validContextString(aspect, maxContextIdentifierBytes) {
			return fmt.Errorf("%w: candidate aspect", ErrInvalidContextSelection)
		}
		if _, exists := seenAspects[aspect]; exists {
			return fmt.Errorf("%w: duplicate candidate aspect", ErrInvalidContextSelection)
		}
		seenAspects[aspect] = struct{}{}
	}
	if c.RedundancyKey != "" && !validContextID(c.RedundancyKey) {
		return fmt.Errorf("%w: candidate redundancy key", ErrInvalidContextSelection)
	}
	tokenCost, characterCost, byteCost, err := c.costs()
	if err != nil {
		return err
	}
	if tokenCost > maxContextTokens || characterCost > maxContextCharacters || byteCost > maxContextBytes {
		return fmt.Errorf("%w: candidate costs exceed bounds", ErrInvalidContextSelection)
	}
	return nil
}

func (c ContextSelectionCandidate) costs() (int, int64, int64, error) {
	tokenCost, err := resolveSelectionCost(c.TokenCost, c.Tokens, "token")
	if err != nil {
		return 0, 0, 0, err
	}
	characterCost, err := resolveSelectionInt64Cost(c.CharacterCost, c.Characters, "character")
	if err != nil {
		return 0, 0, 0, err
	}
	byteCost, err := resolveSelectionInt64Cost(c.ByteCost, c.Bytes, "byte")
	if err != nil {
		return 0, 0, 0, err
	}
	return tokenCost, characterCost, byteCost, nil
}

func (c ContextSelectionCandidate) redundancyKey() string {
	if c.RedundancyKey != "" {
		return c.RedundancyKey
	}
	return c.Item.ID
}

func resolveSelectionCost(canonical, alias int, name string) (int, error) {
	if canonical < 0 || alias < 0 {
		return 0, fmt.Errorf("%w: negative %s cost", ErrInvalidContextSelection, name)
	}
	if canonical != 0 && alias != 0 && canonical != alias {
		return 0, fmt.Errorf("%w: conflicting %s costs", ErrInvalidContextSelection, name)
	}
	if canonical != 0 {
		return canonical, nil
	}
	return alias, nil
}

func resolveSelectionInt64Cost(canonical, alias int64, name string) (int64, error) {
	if canonical < 0 || alias < 0 {
		return 0, fmt.Errorf("%w: negative %s cost", ErrInvalidContextSelection, name)
	}
	if canonical != 0 && alias != 0 && canonical != alias {
		return 0, fmt.Errorf("%w: conflicting %s costs", ErrInvalidContextSelection, name)
	}
	if canonical != 0 {
		return canonical, nil
	}
	return alias, nil
}

// ContextSelectionRequest is the bounded input to greedy context selection.
// Candidates are evaluated only inside Scope and Limits; no policy, token
// estimator, relation closure or package digest is applied here.
type ContextSelectionRequest struct {
	Scope         Scope                       `json:"scope"`
	Limits        ContextLimits               `json:"limits"`
	Candidates    []ContextSelectionCandidate `json:"candidates"`
	Configuration ContextUtilityConfiguration `json:"configuration"`
}

// ContextSelectionInput is a descriptive alias for ContextSelectionRequest.
type ContextSelectionInput = ContextSelectionRequest

// Validate checks request-level bounds and safe audit identity. Candidate
// item payload errors remain selectable as explicit invalid audits when their
// IDs are safe; duplicate IDs and malformed metadata reject the request.
func (r ContextSelectionRequest) Validate() error {
	if err := r.Scope.Validate(); err != nil {
		return fmt.Errorf("%w: selection scope", ErrInvalidContextSelection)
	}
	if err := r.Limits.Validate(); err != nil {
		return err
	}
	if len(r.Candidates) > maxContextSelectionCandidates {
		return fmt.Errorf("%w: too many selection candidates", ErrInvalidContextSelection)
	}
	if _, err := r.Configuration.Normalize(); err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(r.Candidates))
	for _, candidate := range r.Candidates {
		if err := candidate.validateMetadata(); err != nil {
			return err
		}
		if _, exists := seen[candidate.Item.ID]; exists {
			return fmt.Errorf("%w: duplicate candidate item id", ErrInvalidContextSelection)
		}
		seen[candidate.Item.ID] = struct{}{}
	}
	return nil
}

// ContextSelectionResult contains selected canonical items, content-free
// decisions for every candidate and the exact applied utility configuration.
// Counts are based solely on the explicit candidate costs.
type ContextSelectionResult struct {
	Items           []ContextItem               `json:"items"`
	Audit           []ContextSelectionAudit     `json:"audit"`
	ItemCount       int                         `json:"item_count"`
	TokenEstimate   int                         `json:"token_estimate"`
	CharactersUsed  int64                       `json:"characters_used"`
	BytesUsed       int64                       `json:"bytes_used"`
	Configuration   ContextUtilityConfiguration `json:"configuration"`
	BudgetExhausted bool                        `json:"budget_exhausted"`
}

// ContextSelectionOutput is a descriptive alias for ContextSelectionResult.
type ContextSelectionOutput = ContextSelectionResult

// Validate checks result bounds and the identity/audit relationship. The
// request scope and limits are intentionally checked by ValidateAgainst.
func (r ContextSelectionResult) Validate() error {
	if len(r.Items) > maxContextItems || len(r.Audit) > maxContextAudits {
		return fmt.Errorf("%w: selection result bounds", ErrInvalidContextSelection)
	}
	if r.ItemCount != len(r.Items) || r.TokenEstimate < 0 || r.CharactersUsed < 0 || r.BytesUsed < 0 {
		return fmt.Errorf("%w: selection result counts", ErrInvalidContextSelection)
	}
	if _, err := r.Configuration.Normalize(); err != nil {
		return err
	}
	itemIDs := make(map[string]struct{}, len(r.Items))
	for _, item := range r.Items {
		if err := item.Validate(); err != nil {
			return err
		}
		if _, exists := itemIDs[item.ID]; exists {
			return fmt.Errorf("%w: duplicate selected item id", ErrInvalidContextSelection)
		}
		itemIDs[item.ID] = struct{}{}
	}
	seenAudit := make(map[string]struct{}, len(r.Audit))
	for _, audit := range r.Audit {
		if err := audit.Validate(); err != nil {
			return err
		}
		if _, exists := seenAudit[audit.ItemID]; exists {
			return fmt.Errorf("%w: duplicate selection audit", ErrInvalidContextSelection)
		}
		seenAudit[audit.ItemID] = struct{}{}
		if audit.Included {
			if _, exists := itemIDs[audit.ItemID]; !exists {
				return fmt.Errorf("%w: audit item is not selected", ErrInvalidContextSelection)
			}
		} else if _, exists := itemIDs[audit.ItemID]; exists {
			return fmt.Errorf("%w: excluded audit item is selected", ErrInvalidContextSelection)
		}
	}
	for id := range itemIDs {
		if _, exists := seenAudit[id]; !exists {
			return fmt.Errorf("%w: selected item has no audit", ErrInvalidContextSelection)
		}
	}
	return nil
}

// ValidateAgainst checks result accounting against the request's scope and
// simultaneous limits.
func (r ContextSelectionResult) ValidateAgainst(request ContextSelectionRequest) error {
	if err := request.Validate(); err != nil {
		return err
	}
	if err := r.Validate(); err != nil {
		return err
	}
	if len(r.Items) > request.Limits.MaxItems || r.TokenEstimate > request.Limits.MaxTokens ||
		r.CharactersUsed > request.Limits.MaxCharacters || r.BytesUsed > request.Limits.MaxBytes {
		return ErrInvalidContextBudget
	}
	if !sameUtilityConfiguration(r.Configuration, mustNormalizeUtilityConfiguration(request.Configuration)) {
		return fmt.Errorf("%w: applied configuration differs", ErrInvalidContextSelection)
	}
	expectedIDs := make(map[string]struct{}, len(request.Candidates))
	candidatesByID := make(map[string]ContextSelectionCandidate, len(request.Candidates))
	for _, candidate := range request.Candidates {
		expectedIDs[candidate.Item.ID] = struct{}{}
		candidatesByID[candidate.Item.ID] = candidate
	}
	seenAudits := make(map[string]struct{}, len(r.Audit))
	includedCount := 0
	var includedTokens int64
	var includedCharacters, includedBytes int64
	budgetAudit := false
	for _, audit := range r.Audit {
		if _, expected := expectedIDs[audit.ItemID]; !expected {
			return fmt.Errorf("%w: audit candidate is not in request", ErrInvalidContextSelection)
		}
		if _, duplicate := seenAudits[audit.ItemID]; duplicate {
			return fmt.Errorf("%w: duplicate request audit", ErrInvalidContextSelection)
		}
		seenAudits[audit.ItemID] = struct{}{}
		if !audit.Included {
			budgetAudit = budgetAudit || audit.Reason == ContextSelectionExcludedBudget
			continue
		}
		candidate := candidatesByID[audit.ItemID]
		tokens, characters, bytes, err := candidate.costs()
		if err != nil {
			return err
		}
		if audit.TokenEstimate != tokens || audit.Characters != characters || audit.Bytes != bytes {
			return fmt.Errorf("%w: included audit costs differ", ErrInvalidContextSelection)
		}
		includedCount++
		includedTokens += int64(audit.TokenEstimate)
		includedCharacters += audit.Characters
		includedBytes += audit.Bytes
	}
	if len(seenAudits) != len(expectedIDs) {
		return fmt.Errorf("%w: request candidate has no audit", ErrInvalidContextSelection)
	}
	if includedCount != r.ItemCount || includedTokens != int64(r.TokenEstimate) ||
		includedCharacters != r.CharactersUsed || includedBytes != r.BytesUsed {
		return fmt.Errorf("%w: included audit counts differ", ErrInvalidContextSelection)
	}
	if budgetAudit != r.BudgetExhausted {
		return fmt.Errorf("%w: budget exhaustion flag differs", ErrInvalidContextSelection)
	}
	for _, item := range r.Items {
		if !sameScope(item.Scope, request.Scope) {
			return ErrInvalidContextScope
		}
		if _, expected := expectedIDs[item.ID]; !expected {
			return fmt.Errorf("%w: selected item is not a request candidate", ErrInvalidContextSelection)
		}
	}
	return nil
}

func mustNormalizeUtilityConfiguration(configuration ContextUtilityConfiguration) ContextUtilityConfiguration {
	normalized, _ := configuration.Normalize()
	return normalized
}

func sameUtilityConfiguration(left, right ContextUtilityConfiguration) bool {
	return left.Version == right.Version && left.Algorithm == right.Algorithm &&
		left.RelevanceWeight == right.RelevanceWeight && left.AspectWeight == right.AspectWeight &&
		left.KindDiversityWeight == right.KindDiversityWeight &&
		left.ArtifactDiversityWeight == right.ArtifactDiversityWeight && left.Digest == right.Digest
}

type preparedContextSelectionCandidate struct {
	candidate  ContextSelectionCandidate
	tokens     int
	characters int64
	bytes      int64
	lastGain   float64
	lastRatio  float64
	decision   ContextSelectionReason
	included   bool
}

// ContextMarginalUtility returns the versioned marginal utility of candidate
// relative to selected. It combines relevance, newly covered aspects,
// ContextItemKind diversity and canonical-locator artifact diversity.
func ContextMarginalUtility(candidate ContextSelectionCandidate, selected []ContextSelectionCandidate, configuration ContextUtilityConfiguration) (float64, error) {
	if err := candidate.Validate(); err != nil {
		return 0, err
	}
	normalized, err := configuration.Normalize()
	if err != nil {
		return 0, err
	}
	selectedAspects := make(map[string]struct{})
	selectedKinds := make(map[ContextItemKind]struct{})
	selectedArtifacts := make(map[string]struct{})
	for _, current := range selected {
		if err := current.Validate(); err != nil {
			return 0, err
		}
		for _, aspect := range current.Aspects {
			selectedAspects[aspect] = struct{}{}
		}
		selectedKinds[current.Item.Kind] = struct{}{}
		selectedArtifacts[contextItemArtifactKey(current.Item)] = struct{}{}
	}

	newAspects := 0
	for _, aspect := range candidate.Aspects {
		if _, exists := selectedAspects[aspect]; !exists {
			newAspects++
		}
	}
	kindDiversity := 0.0
	if _, exists := selectedKinds[candidate.Item.Kind]; !exists {
		kindDiversity = 1
	}
	artifactDiversity := 0.0
	if _, exists := selectedArtifacts[contextItemArtifactKey(candidate.Item)]; !exists {
		artifactDiversity = 1
	}
	utility := normalized.RelevanceWeight*candidate.Relevance +
		normalized.AspectWeight*float64(newAspects) +
		normalized.KindDiversityWeight*kindDiversity +
		normalized.ArtifactDiversityWeight*artifactDiversity
	if math.IsNaN(utility) || math.IsInf(utility, 0) || utility < 0 {
		return 0, fmt.Errorf("%w: non-finite marginal utility", ErrInvalidContextSelection)
	}
	return utility, nil
}

// MarginalContextUtility is a descriptive alias for ContextMarginalUtility.
func MarginalContextUtility(candidate ContextSelectionCandidate, selected []ContextSelectionCandidate, configuration ContextUtilityConfiguration) (float64, error) {
	return ContextMarginalUtility(candidate, selected, configuration)
}

// ContextSelectionMarginalUtility is a descriptive alias for
// ContextMarginalUtility.
func ContextSelectionMarginalUtility(candidate ContextSelectionCandidate, selected []ContextSelectionCandidate, configuration ContextUtilityConfiguration) (float64, error) {
	return ContextMarginalUtility(candidate, selected, configuration)
}

// MarginalUtility is a concise alias for ContextMarginalUtility.
func MarginalUtility(candidate ContextSelectionCandidate, selected []ContextSelectionCandidate, configuration ContextUtilityConfiguration) (float64, error) {
	return ContextMarginalUtility(candidate, selected, configuration)
}

// SelectContext greedily selects candidates by marginal utility per supplied
// token cost. It reevaluates every remaining candidate after each inclusion,
// enforces all four limits simultaneously and emits deterministic audits.
func SelectContext(ctx context.Context, request ContextSelectionRequest) (ContextSelectionResult, error) {
	if ctx == nil {
		return ContextSelectionResult{}, ErrInvalidContextSelection
	}
	if err := ctx.Err(); err != nil {
		return ContextSelectionResult{}, err
	}
	if err := request.Validate(); err != nil {
		return ContextSelectionResult{}, err
	}
	configuration, err := request.Configuration.Normalize()
	if err != nil {
		return ContextSelectionResult{}, err
	}
	prepared := make([]*preparedContextSelectionCandidate, 0, len(request.Candidates))
	audits := make([]ContextSelectionAudit, 0, len(request.Candidates))
	for _, candidate := range request.Candidates {
		if err := ctx.Err(); err != nil {
			return ContextSelectionResult{}, err
		}
		metadataErr := candidate.validateMetadata()
		if metadataErr != nil {
			return ContextSelectionResult{}, metadataErr
		}
		audit := ContextSelectionAudit{
			ItemID:   candidate.Item.ID,
			Included: false,
			Reason:   ContextSelectionExcludedInvalid,
			Rank:     candidate.Rank,
			Score:    candidate.Relevance,
		}
		tokens, characters, bytes, _ := candidate.costs()
		audit.TokenEstimate = tokens
		audit.Characters = characters
		audit.Bytes = bytes
		if candidate.Item.Scope.Validate() != nil || !sameScope(candidate.Item.Scope, request.Scope) {
			audit.Reason = ContextSelectionExcludedScope
			audits = append(audits, audit)
			continue
		}
		if err := candidate.Item.Validate(); err != nil {
			audit.Reason = ContextSelectionExcludedInvalid
			audits = append(audits, audit)
			continue
		}
		prepared = append(prepared, &preparedContextSelectionCandidate{
			candidate:  candidate,
			tokens:     tokens,
			characters: characters,
			bytes:      bytes,
			decision:   ContextSelectionExcludedBudget,
		})
	}

	selected := make([]*preparedContextSelectionCandidate, 0, minInt(request.Limits.MaxItems, len(prepared)))
	selectedForUtility := make([]ContextSelectionCandidate, 0, len(selected))
	selectedRedundancy := make(map[string]struct{}, len(prepared))
	var tokenCount int
	var characterCount, byteCount int64
	budgetExhausted := false

	for len(selected) < request.Limits.MaxItems {
		if err := ctx.Err(); err != nil {
			return ContextSelectionResult{}, err
		}
		var best *preparedContextSelectionCandidate
		bestGain, bestRatio := -1.0, -1.0
		for _, candidate := range prepared {
			if candidate.included {
				continue
			}
			if _, redundant := selectedRedundancy[candidate.candidate.redundancyKey()]; redundant {
				candidate.decision = ContextSelectionExcludedRedundancy
				continue
			}
			if !fitsSelectionBudget(candidate, tokenCount, characterCount, byteCount, len(selected), request.Limits) {
				candidate.decision = ContextSelectionExcludedBudget
				continue
			}
			gain, utilityErr := ContextMarginalUtility(candidate.candidate, selectedForUtility, configuration)
			if utilityErr != nil {
				return ContextSelectionResult{}, utilityErr
			}
			ratio := gain / float64(maxInt(candidate.tokens, 1))
			candidate.lastGain = gain
			candidate.lastRatio = ratio
			if gain <= 0 {
				candidate.decision = ContextSelectionExcludedRedundancy
				continue
			}
			if best == nil || betterSelectionChoice(candidate, gain, ratio, best, bestGain, bestRatio) {
				best = candidate
				bestGain = gain
				bestRatio = ratio
			}
		}
		if best == nil {
			break
		}
		best.included = true
		best.decision = ContextSelectionIncluded
		selected = append(selected, best)
		selectedForUtility = append(selectedForUtility, best.candidate)
		selectedRedundancy[best.candidate.redundancyKey()] = struct{}{}
		tokenCount += best.tokens
		characterCount += best.characters
		byteCount += best.bytes
	}
	items := make([]ContextItem, 0, len(selected))
	for _, candidate := range selected {
		items = append(items, cloneContextItem(candidate.candidate.Item))
	}
	for _, candidate := range prepared {
		if candidate.included {
			audits = append(audits, ContextSelectionAudit{
				ItemID:        candidate.candidate.Item.ID,
				Included:      true,
				Reason:        ContextSelectionIncluded,
				Rank:          candidate.candidate.Rank,
				Score:         candidate.lastGain,
				TokenEstimate: candidate.tokens,
				Characters:    candidate.characters,
				Bytes:         candidate.bytes,
			})
			continue
		}
		reason := candidate.decision
		if reason == ContextSelectionIncluded || reason == "" {
			reason = ContextSelectionExcludedBudget
		}
		if _, redundant := selectedRedundancy[candidate.candidate.redundancyKey()]; redundant {
			reason = ContextSelectionExcludedRedundancy
		}
		audits = append(audits, ContextSelectionAudit{
			ItemID:        candidate.candidate.Item.ID,
			Included:      false,
			Reason:        reason,
			Rank:          candidate.candidate.Rank,
			Score:         candidate.lastGain,
			TokenEstimate: candidate.tokens,
			Characters:    candidate.characters,
			Bytes:         candidate.bytes,
		})
	}
	sort.Slice(audits, func(i, j int) bool {
		return audits[i].ItemID < audits[j].ItemID
	})
	budgetExhausted = false
	for _, audit := range audits {
		if !audit.Included && audit.Reason == ContextSelectionExcludedBudget {
			budgetExhausted = true
			break
		}
	}
	result := ContextSelectionResult{
		Items:           items,
		Audit:           audits,
		ItemCount:       len(items),
		TokenEstimate:   tokenCount,
		CharactersUsed:  characterCount,
		BytesUsed:       byteCount,
		Configuration:   configuration,
		BudgetExhausted: budgetExhausted,
	}
	if err := result.ValidateAgainst(request); err != nil {
		return ContextSelectionResult{}, err
	}
	return result, nil
}

// SelectContextItems is a descriptive alias for SelectContext.
func SelectContextItems(ctx context.Context, request ContextSelectionRequest) (ContextSelectionResult, error) {
	return SelectContext(ctx, request)
}

// GreedySelectContext is a descriptive alias for SelectContext.
func GreedySelectContext(ctx context.Context, request ContextSelectionRequest) (ContextSelectionResult, error) {
	return SelectContext(ctx, request)
}

func betterSelectionChoice(candidate *preparedContextSelectionCandidate, gain, ratio float64, best *preparedContextSelectionCandidate, bestGain, bestRatio float64) bool {
	if ratio != bestRatio {
		return ratio > bestRatio
	}
	if gain != bestGain {
		return gain > bestGain
	}
	if candidate.candidate.Rank != best.candidate.Rank {
		return candidate.candidate.Rank < best.candidate.Rank
	}
	return candidate.candidate.Item.ID < best.candidate.Item.ID
}

func fitsSelectionBudget(candidate *preparedContextSelectionCandidate, tokens int, characters, bytes int64, items int, limits ContextLimits) bool {
	if items >= limits.MaxItems || candidate.tokens > limits.MaxTokens-tokens {
		return false
	}
	if candidate.characters > limits.MaxCharacters-characters || candidate.bytes > limits.MaxBytes-bytes {
		return false
	}
	return true
}

func contextItemArtifactKey(item ContextItem) string {
	locator := item.Locator
	if isZeroLocator(locator) {
		locator = item.locatorForValidation()
	}
	switch {
	case locator.ArtifactID != "":
		return "artifact:" + locator.ArtifactID
	case locator.Path != "":
		return "path:" + locator.Path
	case locator.URI != "":
		return "uri:" + locator.URI
	case locator.Member != "":
		return "member:" + locator.Member
	case locator.SourceID != "":
		return "source:" + locator.SourceID
	default:
		return "item:" + item.ID
	}
}

// ContextArtifactKey exposes the canonical artifact identity used by the
// utility function without exposing locator normalization internals.
func ContextArtifactKey(item ContextItem) string {
	return contextItemArtifactKey(item)
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}
