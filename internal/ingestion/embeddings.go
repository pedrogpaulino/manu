package ingestion

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/pedrogpaulino/manu/internal/aigateway"
	"github.com/pedrogpaulino/manu/internal/evidence"
	"github.com/pedrogpaulino/manu/internal/identity"
	"github.com/pedrogpaulino/manu/internal/retrieval"
)

// EmbeddingMode controls the optional vector stage. The zero value keeps the
// 4.2 behavior where embeddings are not applicable yet; disabled explicitly
// records a partial limitation and never calls an external provider.
type EmbeddingMode string

const (
	EmbeddingModeNotApplicable EmbeddingMode = ""
	EmbeddingModeEnabled       EmbeddingMode = "enabled"
	EmbeddingModeDisabled      EmbeddingMode = "disabled"
	EmbeddingModeForbidden     EmbeddingMode = "forbidden"
)

var (
	// ErrEmbeddingUnavailable is safe to return to orchestration code. Its
	// message never contains provider, SQL, or evidence content.
	ErrEmbeddingUnavailable = errors.New("ingestion: embedding unavailable")
	// ErrEmbeddingForbidden identifies an external-transfer policy block.
	ErrEmbeddingForbidden = errors.New("ingestion: embedding forbidden")
	// ErrEmbeddingIncomplete identifies a rebuild that could not cover every
	// eligible unit. The canonical evidence remains intact.
	ErrEmbeddingIncomplete = errors.New("ingestion: embedding incomplete")
)

// EmbeddingEvidence is the minimal canonical representation needed to build
// a vector. It contains no bundle or source path and is expected to come from
// already-persisted Evidence Units.
type EmbeddingEvidence struct {
	ID               string
	Content          string
	ContentHash      string
	ExternalTransfer evidence.Decision
	Transfer         evidence.Decision
}

// EmbeddingEvidenceSource reads canonical evidence for one immutable scope.
// It is deliberately separate from BundleLoader so a partial retry never
// needs the original bundle.
type EmbeddingEvidenceSource interface {
	ListEmbeddingEvidence(context.Context, string, string, string) ([]EmbeddingEvidence, error)
}

// EmbeddingOptions composes the provider-independent gateway port with the
// independently persisted embedding projection. Profile data is non-secret;
// credentials stay inside the injected aigateway.Embedder.
type EmbeddingOptions struct {
	Mode           EmbeddingMode
	Profile        retrieval.EmbeddingProfile
	GatewayProfile aigateway.EmbeddingProfile
	Embedder       aigateway.Embedder
	Projector      retrieval.EmbeddingStore
	// EmbeddingStore is an explicit alias for callers that prefer the port's
	// domain name. Projector remains useful when composing several projections.
	EmbeddingStore retrieval.EmbeddingStore
	EvidenceSource EmbeddingEvidenceSource
	Timeout        time.Duration
	Now            func() time.Time
}

// EmbeddingPipelineOptions is kept as a readable alias for callers composing
// a pipeline alongside other stage options.
type EmbeddingPipelineOptions = EmbeddingOptions

func (o EmbeddingOptions) validMode() bool {
	switch o.Mode {
	case EmbeddingModeNotApplicable, EmbeddingModeEnabled, EmbeddingModeDisabled, EmbeddingModeForbidden:
		return true
	default:
		return false
	}
}

func (o EmbeddingOptions) timeout() time.Duration {
	if o.Timeout > 0 {
		return o.Timeout
	}
	return 2 * time.Minute
}

func (o EmbeddingOptions) now() time.Time {
	if o.Now != nil {
		return o.Now().UTC()
	}
	return time.Now().UTC()
}

func (p *Pipeline) embeddingForScope(ctx context.Context, job Job, organizationID, sourceID, snapshotID string) error {
	if p == nil {
		return ErrPipelineNotConfigured
	}
	switch p.embedding.Mode {
	case EmbeddingModeNotApplicable:
		return nil
	case EmbeddingModeDisabled, EmbeddingModeForbidden:
		return ErrEmbeddingForbidden
	case EmbeddingModeEnabled:
		return p.rebuildEmbeddings(ctx, job, organizationID, sourceID, snapshotID)
	default:
		return ErrEmbeddingUnavailable
	}
}

func (p *Pipeline) rebuildEmbeddings(ctx context.Context, job Job, organizationID, sourceID, snapshotID string) error {
	options := p.embedding
	projector := options.Projector
	if projector == nil {
		projector = options.EmbeddingStore
	}
	if options.EvidenceSource == nil || projector == nil || options.Embedder == nil {
		return ErrEmbeddingUnavailable
	}
	profile, gatewayProfile, scope, err := normalizeEmbeddingOptions(options, organizationID, sourceID, snapshotID)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrEmbeddingUnavailable, err)
	}
	if err := pipelineContext(ctx); err != nil {
		return err
	}
	workCtx, cancel := context.WithTimeout(ctx, options.timeout())
	defer cancel()
	units, err := options.EvidenceSource.ListEmbeddingEvidence(workCtx, organizationID, sourceID, snapshotID)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrEmbeddingUnavailable
	}
	if err := workCtx.Err(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrEmbeddingUnavailable
	}
	if _, err := projector.EnsureProfile(workCtx, profile); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrEmbeddingUnavailable
	}

	inputs := make([]retrieval.EmbeddingInput, 0, len(units))
	requests := make([]aigateway.EmbeddingItem, 0, len(units))
	requestInput := make(map[string]int, len(units))
	seen := make(map[string]struct{}, len(units))
	for _, unit := range units {
		if err := pipelineContext(workCtx); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return ErrEmbeddingUnavailable
		}
		if !isUUID(unit.ID) {
			return ErrEmbeddingUnavailable
		}
		if _, exists := seen[unit.ID]; exists {
			return ErrEmbeddingUnavailable
		}
		seen[unit.ID] = struct{}{}
		transfer := unit.ExternalTransfer
		if transfer == "" {
			transfer = unit.Transfer
		}
		if transfer != evidence.DecisionAllow {
			// Keep local knowledge available while reporting the policy gap
			// after all other eligible evidence has been rebuilt. Redacted
			// placeholders are intentionally not embedded: they have no
			// semantic content and must not share the original content hash.
			continue
		}
		content := unit.Content
		contentHash := unit.ContentHash
		if content == evidence.RedactedContent {
			continue
		}
		if !isLowerSHA256(contentHash) || strings.TrimSpace(content) == "" || evidence.ContentDigest(content) != contentHash {
			return ErrEmbeddingUnavailable
		}
		embeddingID := identity.CanonicalUUID("embedding", organizationID, profile.ID, sourceID, snapshotID, unit.ID)
		input := retrieval.EmbeddingInput{ID: embeddingID, EvidenceID: unit.ID, EvidenceContentHash: unit.ContentHash}
		cacheKey := retrieval.EmbeddingCacheKey{
			OrganizationID:      organizationID,
			ProfileID:           profile.ID,
			EvidenceContentHash: unit.ContentHash,
		}
		_, hit, lookupErr := projector.Lookup(workCtx, cacheKey)
		if lookupErr != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return ErrEmbeddingUnavailable
		}
		inputs = append(inputs, input)
		if hit {
			continue
		}
		requestInput[unit.ID] = len(inputs) - 1
		requests = append(requests, aigateway.EmbeddingItem{ID: unit.ID, Content: content, ContentHash: contentHash})
	}

	if len(requests) > 0 {
		request := aigateway.EmbeddingRequest{
			ExecutionID: job.ID,
			RequestID:   fmt.Sprintf("%s-embedding-%d", job.ID, job.AttemptCount),
			Deadline:    options.now().Add(options.timeout()),
			Profile:     gatewayProfile,
			Items:       requests,
		}
		if err := request.Validate(); err != nil {
			return ErrEmbeddingUnavailable
		}
		result, embedErr := options.Embedder.Embed(workCtx, request)
		if embedErr != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return ErrEmbeddingUnavailable
		}
		if err := result.Validate(request); err != nil {
			return ErrEmbeddingUnavailable
		}
		for index, item := range requests {
			inputIndex, ok := requestInput[item.ID]
			if !ok || inputIndex < 0 || inputIndex >= len(inputs) {
				return ErrEmbeddingUnavailable
			}
			vector := make([]float32, len(result.Vectors[index]))
			for vectorIndex, value := range result.Vectors[index] {
				vector[vectorIndex] = float32(value)
			}
			inputs[inputIndex].Vector = vector
		}
	}

	rebuilt, err := projector.RebuildSnapshot(workCtx, profile, scope, inputs)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrEmbeddingUnavailable
	}
	if rebuilt.OrganizationID != scope.OrganizationID || rebuilt.ProfileID != profile.ID ||
		rebuilt.SourceID != scope.SourceID || rebuilt.SnapshotID != scope.SnapshotID ||
		rebuilt.Requested != len(inputs) {
		return ErrEmbeddingUnavailable
	}
	if !rebuilt.Complete() {
		return ErrEmbeddingIncomplete
	}
	if len(inputs) != len(units) {
		return ErrEmbeddingForbidden
	}
	return nil
}

func normalizeEmbeddingOptions(options EmbeddingOptions, organizationID, sourceID, snapshotID string) (retrieval.EmbeddingProfile, aigateway.EmbeddingProfile, retrieval.EmbeddingScope, error) {
	profile, err := options.Profile.Normalize()
	if err != nil {
		return retrieval.EmbeddingProfile{}, aigateway.EmbeddingProfile{}, retrieval.EmbeddingScope{}, err
	}
	scope := retrieval.EmbeddingScope{OrganizationID: organizationID, SourceID: sourceID, SnapshotID: snapshotID}
	normalizedScope, err := scope.Normalize()
	if err != nil {
		return retrieval.EmbeddingProfile{}, aigateway.EmbeddingProfile{}, retrieval.EmbeddingScope{}, err
	}
	if profile.OrganizationID != normalizedScope.OrganizationID {
		return retrieval.EmbeddingProfile{}, aigateway.EmbeddingProfile{}, retrieval.EmbeddingScope{}, retrieval.ErrEmbeddingScopeMismatch
	}
	gatewayProfile := options.GatewayProfile
	if gatewayProfile.Provider == aigateway.ProviderUnknown {
		gatewayProfile.Provider = aigateway.Provider(profile.Provider)
	}
	if gatewayProfile.Model == "" {
		gatewayProfile.Model = profile.Model
	}
	if gatewayProfile.Version == "" {
		gatewayProfile.Version = aigateway.EmbeddingProfileVersion
	}
	if gatewayProfile.Dimension == 0 {
		gatewayProfile.Dimension = profile.Dimension
	}
	if gatewayProfile.MaxBatchSize == 0 {
		gatewayProfile.MaxBatchSize = 128
	}
	if err := gatewayProfile.Validate(); err != nil {
		return retrieval.EmbeddingProfile{}, aigateway.EmbeddingProfile{}, retrieval.EmbeddingScope{}, err
	}
	if gatewayProfile.Model != profile.Model || gatewayProfile.Dimension != profile.Dimension ||
		string(gatewayProfile.Provider) != profile.Provider {
		return retrieval.EmbeddingProfile{}, aigateway.EmbeddingProfile{}, retrieval.EmbeddingScope{}, retrieval.ErrEmbeddingProfileConflict
	}
	return profile, gatewayProfile, normalizedScope, nil
}

func embeddingDiagnostic(err error) Diagnostic {
	code := DiagnosticCodeEmbeddingUnavailable
	message := "embedding projection unavailable"
	if errors.Is(err, ErrEmbeddingForbidden) {
		code = DiagnosticCodeEmbeddingForbidden
		message = "embedding transfer is forbidden by policy"
	} else if errors.Is(err, ErrEmbeddingIncomplete) {
		code = DiagnosticCodeEmbeddingIncomplete
		message = "embedding projection is incomplete"
	}
	diagnostic, diagnosticErr := NewSafeDiagnostic(code, message)
	if diagnosticErr != nil {
		return Diagnostic{Code: DiagnosticCodeEmbeddingUnavailable, Message: "embedding projection unavailable"}
	}
	return diagnostic
}
