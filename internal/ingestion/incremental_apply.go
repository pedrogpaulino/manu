package ingestion

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/pedrogpaulino/manu/internal/aigateway"
	"github.com/pedrogpaulino/manu/internal/bundle"
	"github.com/pedrogpaulino/manu/internal/evidence"
	"github.com/pedrogpaulino/manu/internal/identity"
	"github.com/pedrogpaulino/manu/internal/retrieval"
)

var (
	// ErrIncrementalApply identifies a safe orchestration failure. The
	// operation name is retained for diagnostics, while provider, SQL, and
	// source-content details are deliberately discarded.
	ErrIncrementalApply = errors.New("ingestion: incremental update failed")
	// ErrIncrementalNotConfigured identifies an updater without the ports
	// required to preserve localized-update invariants.
	ErrIncrementalNotConfigured = errors.New("ingestion: incremental updater is not configured")
	ErrIncrementalPartial       = errors.New("ingestion: incremental update is partial")
)

// IncrementalCanonicalPersister creates the new canonical snapshot and
// returns the deterministic comparison report produced at that persistence
// boundary. Implementations must insert the new snapshot transactionally and
// must not mutate or delete the previous snapshot.
type IncrementalCanonicalPersister interface {
	PersistIncremental(context.Context, bundle.Bundle, bundle.Bundle, ...IncrementalOptions) (CanonicalPersistenceResult, IncrementalReport, error)
}

// IncrementalTextProjector is the consumer-side localized projection port.
// It receives only bounded current TextEntries plus the previous canonical ID
// for rows explicitly marked reusable.
type IncrementalTextProjector interface {
	RebuildSnapshotIncremental(context.Context, string, string, string, string, []retrieval.IncrementalTextEntry) error
}

// IncrementalUpdateResult is the safe outcome of applying one new snapshot.
// The report contains only hashes and identities; projection counts are
// intentionally derived from that report and not from raw source data.
type IncrementalUpdateResult struct {
	Canonical CanonicalPersistenceResult
	Report    IncrementalReport
	Textual   int
	Embedding retrieval.EmbeddingRebuildResult
	Partial   bool
	// PartialReason is a stable, content-free code suitable for a later job
	// diagnostic; it is not a provider or SQL message.
	PartialReason string
}

// IncrementalUpdater materializes a localized snapshot update. Canonical
// persistence happens first, then textual and applicable relational
// validation, and activation is last. A failed stage therefore cannot make
// the new snapshot active.
type IncrementalUpdater struct {
	canonical  IncrementalCanonicalPersister
	text       IncrementalTextProjector
	relational RelationalValidator
	activator  SnapshotActivator
	embedding  EmbeddingOptions
}

// NewIncrementalUpdater composes the ports required to apply a localized
// update. Relational validation is optional when the installed corpus has no
// applicable directed projection; the canonical and textual ports remain
// mandatory because they establish the new queryable snapshot.
func NewIncrementalUpdater(canonical IncrementalCanonicalPersister, text IncrementalTextProjector, relational RelationalValidator, activator SnapshotActivator, embeddingOptions ...EmbeddingOptions) (*IncrementalUpdater, error) {
	if canonical == nil || text == nil || activator == nil {
		return nil, ErrIncrementalNotConfigured
	}
	if len(embeddingOptions) > 1 || (len(embeddingOptions) == 1 && !embeddingOptions[0].validMode()) {
		return nil, ErrIncrementalNotConfigured
	}
	var embedding EmbeddingOptions
	if len(embeddingOptions) == 1 {
		embedding = embeddingOptions[0]
	}
	return &IncrementalUpdater{canonical: canonical, text: text, relational: relational, activator: activator, embedding: embedding}, nil
}

// Apply compares and materializes current as a new immutable snapshot based
// on previous. Removed facts are intentionally absent from the projection
// input; compatible rows carry their previous canonical identity so the
// persistence adapter can copy them forward without mixing organization or
// source profiles.
func (u *IncrementalUpdater) Apply(ctx context.Context, previous, current bundle.Bundle, options ...IncrementalOptions) (IncrementalUpdateResult, error) {
	if err := incrementalContext(ctx); err != nil {
		return IncrementalUpdateResult{}, err
	}
	if u == nil || u.canonical == nil || u.text == nil || u.activator == nil {
		return IncrementalUpdateResult{}, ErrIncrementalNotConfigured
	}
	comparisonOptions, err := u.prepareEmbeddingComparisonOptions(current, options)
	if err != nil {
		return IncrementalUpdateResult{}, err
	}
	canonical, report, err := u.canonical.PersistIncremental(ctx, previous, current, comparisonOptions...)
	if err != nil {
		return IncrementalUpdateResult{}, incrementalApplyError(ctx, "persist canonical snapshot", err)
	}
	if err := incrementalContext(ctx); err != nil {
		return IncrementalUpdateResult{}, err
	}
	if err := report.Validate(); err != nil {
		return IncrementalUpdateResult{}, err
	}
	if err := validateIncrementalReportScope(previous, current, report); err != nil {
		return IncrementalUpdateResult{}, err
	}
	if err := validateCanonicalResult(current, canonical); err != nil {
		return IncrementalUpdateResult{}, err
	}
	entries, err := incrementalTextEntries(ctx, previous, current, canonical, report)
	if err != nil {
		return IncrementalUpdateResult{}, err
	}
	if err := incrementalContext(ctx); err != nil {
		return IncrementalUpdateResult{}, err
	}
	previousSnapshotID := identity.CanonicalUUID(
		"snapshot", previous.Manifest.Organization.ID, previous.Manifest.Source.ID,
		previous.Manifest.Snapshot.ID,
	)
	if err := u.text.RebuildSnapshotIncremental(ctx, canonical.OrganizationID, canonical.SourceID, previousSnapshotID, canonical.SnapshotID, entries); err != nil {
		return IncrementalUpdateResult{}, incrementalApplyError(ctx, "rebuild textual snapshot", err)
	}
	if err := incrementalContext(ctx); err != nil {
		return IncrementalUpdateResult{}, err
	}
	if u.relational != nil {
		if err := u.relational.ValidateSnapshot(ctx, canonical.OrganizationID, canonical.SourceID, canonical.SnapshotID); err != nil {
			return IncrementalUpdateResult{}, incrementalApplyError(ctx, "validate relational snapshot", err)
		}
		if err := incrementalContext(ctx); err != nil {
			return IncrementalUpdateResult{}, err
		}
	}
	embeddingResult := retrieval.EmbeddingRebuildResult{}
	partialReason := ""
	if u.embedding.Mode != EmbeddingModeNotApplicable {
		embeddingResult, err = u.rebuildIncrementalEmbeddings(ctx, previous, current, canonical, report)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || (ctx != nil && ctx.Err() != nil) {
				return IncrementalUpdateResult{}, err
			}
			partialReason = incrementalEmbeddingPartialReason(err)
		}
	}
	if partialReason != "" {
		if err := incrementalContext(ctx); err != nil {
			return IncrementalUpdateResult{}, err
		}
		if err := u.activator.ActivateSnapshot(ctx, canonical.OrganizationID, canonical.SourceID, canonical.SnapshotID); err != nil {
			return IncrementalUpdateResult{}, incrementalApplyError(ctx, "activate partial snapshot", err)
		}
		return IncrementalUpdateResult{
			Canonical: canonical, Report: report, Textual: len(entries), Embedding: embeddingResult,
			Partial: true, PartialReason: partialReason,
		}, ErrIncrementalPartial
	}
	if err := u.activator.ActivateSnapshot(ctx, canonical.OrganizationID, canonical.SourceID, canonical.SnapshotID); err != nil {
		return IncrementalUpdateResult{}, incrementalApplyError(ctx, "activate snapshot", err)
	}
	return IncrementalUpdateResult{Canonical: canonical, Report: report, Textual: len(entries), Embedding: embeddingResult}, nil
}

// Update is a readable alias for Apply used by callers that model snapshots
// as an update operation.
func (u *IncrementalUpdater) Update(ctx context.Context, previous, current bundle.Bundle, options ...IncrementalOptions) (IncrementalUpdateResult, error) {
	return u.Apply(ctx, previous, current, options...)
}

func (u *IncrementalUpdater) prepareEmbeddingComparisonOptions(current bundle.Bundle, options []IncrementalOptions) ([]IncrementalOptions, error) {
	if len(options) > 1 {
		return nil, ErrIncrementalInvalid
	}
	if u.embedding.Mode != EmbeddingModeEnabled {
		return options, nil
	}
	profile, err := u.embedding.Profile.Normalize()
	if err != nil {
		return nil, ErrIncrementalProfileMismatch
	}
	expectedOrganization := identity.CanonicalUUID("organization", current.Manifest.Organization.ID)
	if profile.OrganizationID != expectedOrganization {
		return nil, ErrIncrementalProfileMismatch
	}
	if len(options) == 0 {
		return []IncrementalOptions{{EmbeddingProfile: &profile}}, nil
	}
	option := options[0]
	if option.EmbeddingProfile != nil {
		normalized, normalizeErr := option.EmbeddingProfile.Normalize()
		if normalizeErr != nil || !sameIncrementalProfile(normalized, profile) {
			return nil, ErrIncrementalProfileMismatch
		}
		// Keep the single-profile shorthand exclusive; the comparison
		// normalizer treats side-specific fields as a separate form.
		option.PreviousEmbeddingProfile = nil
		option.CurrentEmbeddingProfile = nil
		return []IncrementalOptions{option}, nil
	}
	if option.CurrentEmbeddingProfile != nil {
		normalized, normalizeErr := option.CurrentEmbeddingProfile.Normalize()
		if normalizeErr != nil || !sameIncrementalProfile(normalized, profile) {
			return nil, ErrIncrementalProfileMismatch
		}
	} else {
		option.CurrentEmbeddingProfile = &profile
	}
	if option.EmbeddingProfile == nil && option.PreviousEmbeddingProfile == nil {
		option.EmbeddingProfile = &profile
		option.CurrentEmbeddingProfile = nil
	}
	return []IncrementalOptions{option}, nil
}

func (u *IncrementalUpdater) rebuildIncrementalEmbeddings(ctx context.Context, previous, current bundle.Bundle, canonical CanonicalPersistenceResult, report IncrementalReport) (retrieval.EmbeddingRebuildResult, error) {
	if u.embedding.Mode == EmbeddingModeDisabled || u.embedding.Mode == EmbeddingModeForbidden {
		return retrieval.EmbeddingRebuildResult{}, ErrEmbeddingForbidden
	}
	if u.embedding.Mode != EmbeddingModeEnabled {
		return retrieval.EmbeddingRebuildResult{}, ErrEmbeddingUnavailable
	}
	projector := u.embedding.Projector
	if projector == nil {
		projector = u.embedding.EmbeddingStore
	}
	if projector == nil || u.embedding.Embedder == nil {
		return retrieval.EmbeddingRebuildResult{}, ErrEmbeddingUnavailable
	}
	profile, gatewayProfile, scope, err := normalizeEmbeddingOptions(u.embedding, canonical.OrganizationID, canonical.SourceID, canonical.SnapshotID)
	if err != nil {
		return retrieval.EmbeddingRebuildResult{}, ErrEmbeddingUnavailable
	}
	if err := pipelineContext(ctx); err != nil {
		return retrieval.EmbeddingRebuildResult{}, err
	}
	if _, err := projector.EnsureProfile(ctx, profile); err != nil {
		return retrieval.EmbeddingRebuildResult{}, ErrEmbeddingUnavailable
	}
	factKeys, err := IncrementalFactKeys(ctx, current)
	if err != nil {
		return retrieval.EmbeddingRebuildResult{}, err
	}
	reusable := make(map[string]FactReference)
	for _, impact := range report.Impacts {
		if impact.Projection != IncrementalProjectionEmbedding || !report.EmbeddingProfileCompatible {
			continue
		}
		for _, key := range impact.Reusable {
			for _, reference := range report.Reused {
				if reference.Kind == IncrementalFactEvidence && reference.Key == key {
					reusable[key] = reference
					break
				}
			}
		}
	}
	inputs := make([]retrieval.IncrementalEmbeddingInput, 0, len(current.Evidence))
	requests := make([]aigateway.EmbeddingItem, 0, len(current.Evidence))
	requestIndex := make(map[string]int, len(current.Evidence))
	for _, unit := range current.Evidence {
		if err := pipelineContext(ctx); err != nil {
			return retrieval.EmbeddingRebuildResult{}, err
		}
		transfer := unit.ExternalTransfer
		if transfer != evidence.DecisionAllow || !persistibleEvidence(unit) || unit.Content == evidence.RedactedContent {
			// The local snapshot is still valid, but a non-transferable unit
			// makes the vector projection partial. In particular, never embed
			// a fixed redaction placeholder under an original content hash.
			continue
		}
		if evidence.ContentDigest(unit.Content) != unit.ContentHash {
			return retrieval.EmbeddingRebuildResult{}, ErrEmbeddingUnavailable
		}
		currentEvidenceID := canonical.EvidenceIDs[unit.ID]
		if !isUUID(currentEvidenceID) {
			return retrieval.EmbeddingRebuildResult{}, ErrPipelineMismatch
		}
		stableKey := factKeys[IncrementalFactEvidence][unit.ID]
		if stableKey == "" {
			return retrieval.EmbeddingRebuildResult{}, ErrIncrementalInvalid
		}
		input := retrieval.IncrementalEmbeddingInput{
			StableKey: stableKey,
			Input: retrieval.EmbeddingInput{
				ID:         identity.CanonicalUUID("embedding", canonical.OrganizationID, profile.ID, canonical.SourceID, canonical.SnapshotID, currentEvidenceID),
				EvidenceID: currentEvidenceID, EvidenceContentHash: unit.ContentHash,
			},
		}
		if reference, ok := reusable[stableKey]; ok {
			input.Reuse = true
			input.PreviousEvidenceID = identity.CanonicalUUID("evidence", previous.Manifest.Organization.ID, previous.Manifest.Source.ID, previous.Manifest.Snapshot.ID, reference.PreviousID)
		} else {
			requestIndex[unit.ID] = len(inputs)
			requests = append(requests, aigateway.EmbeddingItem{ID: unit.ID, Content: unit.Content, ContentHash: unit.ContentHash})
		}
		inputs = append(inputs, input)
	}
	if len(requests) > 0 {
		request := aigateway.EmbeddingRequest{
			ExecutionID: "incremental-" + canonical.SnapshotID,
			RequestID:   "incremental-embedding-" + canonical.SnapshotID,
			Deadline:    u.embedding.now().Add(u.embedding.timeout()), Profile: gatewayProfile, Items: requests,
		}
		if err := request.Validate(); err != nil {
			return retrieval.EmbeddingRebuildResult{}, ErrEmbeddingUnavailable
		}
		response, embedErr := u.embedding.Embedder.Embed(ctx, request)
		if embedErr != nil {
			if ctx.Err() != nil {
				return retrieval.EmbeddingRebuildResult{}, ctx.Err()
			}
			return retrieval.EmbeddingRebuildResult{}, ErrEmbeddingUnavailable
		}
		if err := response.Validate(request); err != nil {
			return retrieval.EmbeddingRebuildResult{}, ErrEmbeddingUnavailable
		}
		for index, item := range requests {
			inputIndex, ok := requestIndex[item.ID]
			if !ok || inputIndex < 0 || inputIndex >= len(inputs) {
				return retrieval.EmbeddingRebuildResult{}, ErrEmbeddingUnavailable
			}
			vector := make([]float32, len(response.Vectors[index]))
			for vectorIndex, value := range response.Vectors[index] {
				vector[vectorIndex] = float32(value)
			}
			inputs[inputIndex].Input.Vector = vector
		}
	}
	incrementalStore := retrieval.NewEmbeddingProjection(projector)
	previousSnapshotID := identity.CanonicalUUID("snapshot", previous.Manifest.Organization.ID, previous.Manifest.Source.ID, previous.Manifest.Snapshot.ID)
	var result retrieval.EmbeddingRebuildResult
	if _, supportsIncremental := projector.(retrieval.IncrementalEmbeddingStore); supportsIncremental {
		result, err = incrementalStore.RebuildSnapshotIncremental(ctx, profile, scope, previousSnapshotID, inputs)
	} else {
		// Older in-memory/test stores may expose only the original cache port.
		// They still receive exactly the current eligible set and nil vectors
		// for reusable items; the PostgreSQL adapter above provides the stronger
		// previous-row copy-forward contract.
		baseInputs := make([]retrieval.EmbeddingInput, 0, len(inputs))
		for _, input := range inputs {
			baseInputs = append(baseInputs, input.Input)
		}
		result, err = projector.RebuildSnapshot(ctx, profile, scope, baseInputs)
	}
	if err != nil {
		if ctx.Err() != nil {
			return retrieval.EmbeddingRebuildResult{}, ctx.Err()
		}
		return retrieval.EmbeddingRebuildResult{}, ErrEmbeddingUnavailable
	}
	if result.OrganizationID != scope.OrganizationID || result.ProfileID != profile.ID || result.SourceID != scope.SourceID || result.SnapshotID != scope.SnapshotID || result.Requested != len(inputs) {
		return retrieval.EmbeddingRebuildResult{}, ErrEmbeddingUnavailable
	}
	if !result.Complete() {
		return result, ErrEmbeddingIncomplete
	}
	if len(inputs) != len(current.Evidence) {
		return result, ErrEmbeddingForbidden
	}
	return result, nil
}

func incrementalEmbeddingPartialReason(err error) string {
	switch {
	case errors.Is(err, ErrEmbeddingForbidden):
		return "embedding_forbidden"
	case errors.Is(err, ErrEmbeddingIncomplete):
		return "embedding_incomplete"
	default:
		return "embedding_unavailable"
	}
}

func validateIncrementalReportScope(previous, current bundle.Bundle, report IncrementalReport) error {
	if report.OrganizationID != previous.Manifest.Organization.ID || report.OrganizationID != current.Manifest.Organization.ID ||
		report.SourceID != previous.Manifest.Source.ID || report.SourceID != current.Manifest.Source.ID ||
		report.PreviousSnapshotID != previous.Manifest.Snapshot.ID || report.CurrentSnapshotID != current.Manifest.Snapshot.ID {
		return ErrIncrementalScopeMismatch
	}
	return nil
}

func incrementalTextEntries(ctx context.Context, previous, current bundle.Bundle, result CanonicalPersistenceResult, report IncrementalReport) ([]retrieval.IncrementalTextEntry, error) {
	entries, err := textEntries(current, result)
	if err != nil {
		return nil, err
	}
	currentFacts, err := bundleFacts(ctx, current)
	if err != nil {
		return nil, err
	}
	currentEvidenceKeys := make(map[string]string, len(currentFacts[string(IncrementalFactEvidence)]))
	for _, fact := range currentFacts[string(IncrementalFactEvidence)] {
		currentEvidenceKeys[fact.id] = fact.key
	}
	reusable := make(map[string]FactReference)
	for _, impact := range report.Impacts {
		if impact.Projection != IncrementalProjectionTextual {
			continue
		}
		for _, key := range impact.Reusable {
			for _, ref := range report.Reused {
				if ref.Kind == IncrementalFactEvidence && ref.Key == key {
					reusable[key] = ref
					break
				}
			}
		}
	}
	prepared := make([]retrieval.IncrementalTextEntry, 0, len(entries))
	for _, entry := range entries {
		if err := incrementalContext(ctx); err != nil {
			return nil, err
		}
		key := ""
		for externalID, canonicalID := range result.EvidenceIDs {
			if canonicalID == entry.EvidenceID {
				key = currentEvidenceKeys[externalID]
				break
			}
		}
		if key == "" {
			return nil, ErrIncrementalInvalid
		}
		candidate := retrieval.IncrementalTextEntry{StableKey: key, Entry: entry}
		if ref, ok := reusable[key]; ok {
			candidate.Reuse = true
			candidate.PreviousEvidenceID = identity.CanonicalUUID(
				"evidence", previous.Manifest.Organization.ID, previous.Manifest.Source.ID,
				previous.Manifest.Snapshot.ID, ref.PreviousID,
			)
		}
		prepared = append(prepared, candidate)
	}
	sort.Slice(prepared, func(i, j int) bool {
		if prepared[i].StableKey != prepared[j].StableKey {
			return prepared[i].StableKey < prepared[j].StableKey
		}
		return prepared[i].Entry.EvidenceID < prepared[j].Entry.EvidenceID
	})
	return prepared, nil
}

func incrementalApplyError(ctx context.Context, operation string, err error) error {
	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("%s: %w", operation, context.Canceled)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%s: %w", operation, context.DeadlineExceeded)
	}
	if ctx != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("%s: %w", operation, ctxErr)
		}
	}
	for _, safe := range []error{ErrIncrementalInvalid, ErrIncrementalScopeMismatch, ErrIncrementalSnapshotImmutable, ErrIncrementalAmbiguousIdentity, ErrIncrementalProfileMismatch} {
		if errors.Is(err, safe) {
			return err
		}
	}
	return fmt.Errorf("%w: %s", ErrIncrementalApply, operation)
}
