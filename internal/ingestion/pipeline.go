package ingestion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/pedrogpaulino/manu/internal/bundle"
	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/evidence"
	"github.com/pedrogpaulino/manu/internal/fact"
	"github.com/pedrogpaulino/manu/internal/identity"
	"github.com/pedrogpaulino/manu/internal/retrieval"
)

var (
	// ErrPipelineNotConfigured identifies a pipeline with a missing required
	// port. Optional projection ports are handled explicitly by the pipeline.
	ErrPipelineNotConfigured = errors.New("ingestion: pipeline is not configured")
	// ErrPipelineMismatch identifies a job and bundle that do not describe the
	// same organization-scoped factual identity.
	ErrPipelineMismatch = errors.New("ingestion: job and bundle identity mismatch")
	// ErrPipelineStep identifies a failed pipeline operation. The underlying
	// error is deliberately not retained because it may contain source data,
	// SQL diagnostics, or provider content.
	ErrPipelineStep = errors.New("ingestion: pipeline step failed")
)

// BundleLoader supplies the complete bundle for a claimed job. Loading is a
// separate port because the first cut deliberately does not prescribe durable
// bundle storage or make the job handler read a filesystem path.
type BundleLoader interface {
	Load(context.Context, Job) (bundle.Bundle, error)
}

// CanonicalPersistenceResult contains only the canonical IDs assigned by a
// successful persistence operation. External bundle IDs remain in the bundle
// and are never used as relational identifiers in a projection.
type CanonicalPersistenceResult struct {
	OrganizationID string
	SourceID       string
	SnapshotID     string
	ArtifactIDs    map[string]string
	ObservationIDs map[string]string
	EvidenceIDs    map[string]string
}

// CanonicalPersister is the small consumer-side port for the source of truth.
// A concrete persistence adapter can translate its own result DTO into this
// boundary without importing persistence into the ingestion package.
type CanonicalPersister interface {
	Persist(context.Context, bundle.Bundle) (CanonicalPersistenceResult, error)
}

// TextProjector rebuilds the non-vector textual projection for one snapshot.
// retrieval.TextProjection and persistence adapters can satisfy this port
// directly through the same method signature.
type TextProjector interface {
	RebuildSnapshot(context.Context, string, string, string, []retrieval.TextEntry) error
}

// RelationalValidator validates the applicable directed relational projection
// for a snapshot. The port is optional for bundles without applicable
// relations; this cut does not invent relational facts from arbitrary JSON.
type RelationalValidator interface {
	ValidateSnapshot(context.Context, string, string, string) error
}

// SnapshotActivator changes the active snapshot only after all applicable
// canonical and non-vector projection invariants have passed.
type SnapshotActivator interface {
	ActivateSnapshot(context.Context, string, string, string) error
}

// Pipeline coordinates the durable stages for one claimed ingestion job.
// Embeddings are an optional, later projection and never replace canonical
// knowledge or the textual/relational projections.
type Pipeline struct {
	jobs       JobStore
	loader     BundleLoader
	canonical  CanonicalPersister
	text       TextProjector
	relational RelationalValidator
	activator  SnapshotActivator
	embedding  EmbeddingOptions
}

// NewPipeline composes the required ports for the first ingestion slice. The
// relational validator may be nil when the installed corpus has no applicable
// directed relations; all other ports are required to preserve activation
// safety.
func NewPipeline(
	jobs JobStore,
	loader BundleLoader,
	canonical CanonicalPersister,
	text TextProjector,
	relational RelationalValidator,
	activator SnapshotActivator,
	embeddingOptions ...EmbeddingOptions,
) (*Pipeline, error) {
	if jobs == nil || loader == nil || canonical == nil || text == nil || activator == nil {
		return nil, ErrPipelineNotConfigured
	}
	if len(embeddingOptions) > 1 || (len(embeddingOptions) == 1 && !embeddingOptions[0].validMode()) {
		return nil, ErrPipelineNotConfigured
	}
	var embedding EmbeddingOptions
	if len(embeddingOptions) == 1 {
		embedding = embeddingOptions[0]
	}
	return &Pipeline{
		jobs: jobs, loader: loader, canonical: canonical, text: text,
		relational: relational, activator: activator, embedding: embedding,
	}, nil
}

// NewPipelineWithEmbeddings is an explicit constructor for callers that want
// to enable the optional vector stage while retaining the original six-port
// constructor for local/non-vector composition.
func NewPipelineWithEmbeddings(
	jobs JobStore,
	loader BundleLoader,
	canonical CanonicalPersister,
	text TextProjector,
	relational RelationalValidator,
	activator SnapshotActivator,
	embedding EmbeddingOptions,
) (*Pipeline, error) {
	return NewPipeline(jobs, loader, canonical, text, relational, activator, embedding)
}

// Handler returns the JobHandler expected by Executor.
func (p *Pipeline) Handler() JobHandler {
	if p == nil {
		return nil
	}
	return p.Handle
}

// ApplyIncremental exposes the localized snapshot path on the same composed
// pipeline. It is intentionally separate from Handle: a caller must provide
// the previous immutable bundle explicitly, and adapters must opt into the
// copy-forward ports rather than silently falling back to a full rebuild.
func (p *Pipeline) ApplyIncremental(ctx context.Context, previous, current bundle.Bundle, options ...IncrementalOptions) (IncrementalUpdateResult, error) {
	if p == nil || p.canonical == nil || p.text == nil || p.activator == nil {
		return IncrementalUpdateResult{}, ErrIncrementalNotConfigured
	}
	canonical, ok := p.canonical.(IncrementalCanonicalPersister)
	if !ok {
		return IncrementalUpdateResult{}, ErrIncrementalNotConfigured
	}
	text, ok := p.text.(IncrementalTextProjector)
	if !ok {
		return IncrementalUpdateResult{}, ErrIncrementalNotConfigured
	}
	updater, err := NewIncrementalUpdater(canonical, text, p.relational, p.activator, p.embedding)
	if err != nil {
		return IncrementalUpdateResult{}, err
	}
	return updater.Apply(ctx, previous, current, options...)
}

// Handle executes the stages for one claimed job. Every durable transition is
// made only after its operation succeeds. A vector limitation is recorded
// after activating the already-valid local snapshot; earlier stage failures
// and cancellation still prevent activation.
func (p *Pipeline) Handle(ctx context.Context, job Job) error {
	if err := pipelineContext(ctx); err != nil {
		return err
	}
	if p == nil || p.jobs == nil || p.loader == nil || p.canonical == nil || p.text == nil || p.activator == nil {
		return ErrPipelineNotConfigured
	}
	if err := job.Validate(); err != nil {
		return ErrPipelineMismatch
	}

	input, err := p.loader.Load(ctx, job)
	if err != nil {
		return pipelineStepError("load bundle", err)
	}
	if err := pipelineContext(ctx); err != nil {
		return err
	}
	if err := input.Validate(); err != nil {
		return ErrPipelineMismatch
	}
	if err := validateJobBundleIdentity(job, input); err != nil {
		return err
	}

	canonical, err := p.canonical.Persist(ctx, input)
	if err != nil {
		return pipelineStepError("persist canonical knowledge", err)
	}
	if err := pipelineContext(ctx); err != nil {
		return err
	}
	if err := validateCanonicalResult(input, canonical); err != nil {
		return err
	}

	counts := bundleJobCounts(input)
	if err := p.advanceIfNeeded(ctx, job, JobStageCanonicalPersistence, counts); err != nil {
		return err
	}

	entries, err := textEntries(input, canonical)
	if err != nil {
		return err
	}
	if err := pipelineContext(ctx); err != nil {
		return err
	}
	if stageBefore(job.Stage, JobStageTextualProjection) {
		if err := p.text.RebuildSnapshot(ctx, canonical.OrganizationID, canonical.SourceID, canonical.SnapshotID, entries); err != nil {
			return pipelineStepError("rebuild textual projection", err)
		}
		if err := pipelineContext(ctx); err != nil {
			return err
		}
		if err := p.advance(ctx, job, JobStageTextualProjection, counts); err != nil {
			return err
		}
	}

	if stageBefore(job.Stage, JobStageRelationalProjection) {
		if p.relational != nil {
			if err := p.relational.ValidateSnapshot(ctx, canonical.OrganizationID, canonical.SourceID, canonical.SnapshotID); err != nil {
				return pipelineStepError("validate relational projection", err)
			}
		}
		if err := pipelineContext(ctx); err != nil {
			return err
		}
		if err := p.advance(ctx, job, JobStageRelationalProjection, counts); err != nil {
			return err
		}
	}

	if stageBefore(job.Stage, JobStageEmbeddingProjection) {
		if err := pipelineContext(ctx); err != nil {
			return err
		}
		if err := p.advance(ctx, job, JobStageEmbeddingProjection, counts); err != nil {
			return err
		}
	}
	if stageBefore(job.Stage, JobStageActivation) {
		if err := p.embeddingForScope(ctx, job, canonical.OrganizationID, canonical.SourceID, canonical.SnapshotID); err != nil {
			if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return err
			}
			// Canonical, textual, and relational knowledge is useful even when
			// the optional vector projection is unavailable. Activate that local
			// snapshot before recording the vector limitation; the job remains
			// partial at the embedding boundary and is never completed here.
			if activationErr := p.activator.ActivateSnapshot(ctx, canonical.OrganizationID, canonical.SourceID, canonical.SnapshotID); activationErr != nil {
				return pipelineStepError("activate partial snapshot", activationErr)
			}
			if partialErr := p.markEmbeddingPartial(ctx, job, counts, err); partialErr != nil {
				return partialErr
			}
			return ErrJobPartial
		}
	}

	if stageBefore(job.Stage, JobStageActivation) {
		if err := pipelineContext(ctx); err != nil {
			return err
		}
		if err := p.activator.ActivateSnapshot(ctx, canonical.OrganizationID, canonical.SourceID, canonical.SnapshotID); err != nil {
			return pipelineStepError("activate snapshot", err)
		}
		if err := pipelineContext(ctx); err != nil {
			return err
		}
		if err := p.advance(ctx, job, JobStageActivation, counts); err != nil {
			return err
		}
	}
	return nil
}

// ResumePartial explicitly claims and completes only the embedding stage of a
// partial job. It reads canonical evidence through EmbeddingEvidenceSource,
// never calls BundleLoader/CanonicalPersister/TextProjector, and serializes
// concurrent retries through JobStore.ResumePartial.
func (p *Pipeline) ResumePartial(ctx context.Context, organizationID, jobID, owner string, lease time.Duration) (Job, error) {
	if err := pipelineContext(ctx); err != nil {
		return Job{}, err
	}
	if p == nil || p.jobs == nil || p.activator == nil || p.embedding.Mode != EmbeddingModeEnabled {
		return Job{}, ErrPipelineNotConfigured
	}
	job, err := p.jobs.ResumePartial(ctx, organizationID, jobID, owner, lease)
	if err != nil {
		return Job{}, err
	}
	if err := p.embeddingForScope(ctx, job, job.OrganizationID, job.SourceID, job.SnapshotID); err != nil {
		if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return job, err
		}
		if partialErr := p.markEmbeddingPartial(ctx, job, job.JobCounts, err); partialErr != nil {
			return Job{}, partialErr
		}
		partial, getErr := p.jobs.Get(ctx, job.OrganizationID, job.ID)
		if getErr != nil {
			return Job{}, pipelineStepError("read partial embedding projection", getErr)
		}
		return partial, ErrJobPartial
	}
	if err := pipelineContext(ctx); err != nil {
		return job, err
	}
	if err := p.activator.ActivateSnapshot(ctx, job.OrganizationID, job.SourceID, job.SnapshotID); err != nil {
		return p.failResumedJob(ctx, job, err)
	}
	if err := p.advance(ctx, job, JobStageActivation, job.JobCounts); err != nil {
		return p.failResumedJob(ctx, job, err)
	}
	completed, err := p.jobs.Complete(ctx, job.OrganizationID, job.ID, job.LeaseOwner, job.JobCounts)
	if err != nil {
		return p.failResumedJob(ctx, job, err)
	}
	return completed, nil
}

func (p *Pipeline) markEmbeddingPartial(ctx context.Context, job Job, counts JobCounts, cause error) error {
	if err := pipelineContext(ctx); err != nil {
		return err
	}
	diagnostic := embeddingDiagnostic(cause)
	if _, err := p.jobs.Partial(ctx, job.OrganizationID, job.ID, job.LeaseOwner, JobStageEmbeddingProjection, diagnostic, counts); err != nil {
		return pipelineStepError("mark partial embedding projection", err)
	}
	return nil
}

func (p *Pipeline) failResumedJob(ctx context.Context, job Job, cause error) (Job, error) {
	diagnostic := Diagnostic{Code: DiagnosticCodeProcessing, Message: "job processing failed"}
	failed, err := p.jobs.Fail(ctx, job.OrganizationID, job.ID, job.LeaseOwner, diagnostic)
	if err != nil {
		return Job{}, pipelineStepError("fail resumed ingestion", err)
	}
	_ = cause
	return failed, ErrJobProcessing
}

func (p *Pipeline) advanceIfNeeded(ctx context.Context, job Job, stage JobStage, counts JobCounts) error {
	if !stageBefore(job.Stage, stage) {
		return nil
	}
	return p.advance(ctx, job, stage, counts)
}

func (p *Pipeline) advance(ctx context.Context, job Job, stage JobStage, counts JobCounts) error {
	if err := pipelineContext(ctx); err != nil {
		return err
	}
	if _, err := p.jobs.AdvanceStage(ctx, job.OrganizationID, job.ID, job.LeaseOwner, stage, counts); err != nil {
		return pipelineStepError("advance ingestion stage", err)
	}
	return nil
}

func stageBefore(current, target JobStage) bool {
	return stageIndex(target) > stageIndex(current)
}

func bundleJobCounts(input bundle.Bundle) JobCounts {
	return JobCounts{
		ArtifactCount:    int64(len(input.Artifacts)),
		ObservationCount: int64(len(input.Contributions)),
		EvidenceCount:    int64(len(input.Evidence)),
		FailureCount:     int64(len(input.Manifest.Failures)),
	}
}

func validateJobBundleIdentity(job Job, input bundle.Bundle) error {
	manifest := input.Manifest
	canonicalOrganizationID := identity.CanonicalUUID("organization", manifest.Organization.ID)
	canonicalSourceID := identity.CanonicalUUID("source", manifest.Organization.ID, manifest.Source.ID)
	canonicalSnapshotID := identity.CanonicalUUID("snapshot", manifest.Organization.ID, manifest.Source.ID, manifest.Snapshot.ID)
	if job.OrganizationID != canonicalOrganizationID {
		return ErrPipelineMismatch
	}
	if job.OrganizationExternalID != "" && job.OrganizationExternalID != manifest.Organization.ID {
		return ErrPipelineMismatch
	}
	if job.SourceExternalID != manifest.Source.ID || job.SnapshotExternalID != manifest.Snapshot.ID ||
		job.FactualDigest != manifest.FactualDigest || job.AnalysisConfigurationID != manifest.Analysis.ConfigurationID {
		return ErrPipelineMismatch
	}
	if job.SourceID != "" && job.SourceID != canonicalSourceID {
		return ErrPipelineMismatch
	}
	if job.SnapshotID != "" && job.SnapshotID != canonicalSnapshotID {
		return ErrPipelineMismatch
	}
	return nil
}

func validateCanonicalResult(input bundle.Bundle, result CanonicalPersistenceResult) error {
	manifest := input.Manifest
	expected := CanonicalPersistenceResult{
		OrganizationID: identity.CanonicalUUID("organization", manifest.Organization.ID),
		SourceID:       identity.CanonicalUUID("source", manifest.Organization.ID, manifest.Source.ID),
		SnapshotID:     identity.CanonicalUUID("snapshot", manifest.Organization.ID, manifest.Source.ID, manifest.Snapshot.ID),
	}
	if result.OrganizationID != expected.OrganizationID || result.SourceID != expected.SourceID || result.SnapshotID != expected.SnapshotID {
		return ErrPipelineMismatch
	}
	for _, artifact := range input.Artifacts {
		if !canonicalMapContains(result.ArtifactIDs, artifact.ID) {
			return ErrPipelineMismatch
		}
	}
	for _, observation := range input.Contributions {
		if !canonicalMapContains(result.ObservationIDs, observation.ID) {
			return ErrPipelineMismatch
		}
	}
	for _, unit := range input.Evidence {
		if !canonicalMapContains(result.EvidenceIDs, unit.ID) {
			return ErrPipelineMismatch
		}
	}
	return nil
}

func canonicalMapContains(values map[string]string, externalID string) bool {
	value, ok := values[externalID]
	return ok && isUUID(value)
}

func textEntries(input bundle.Bundle, result CanonicalPersistenceResult) ([]retrieval.TextEntry, error) {
	entries := make([]retrieval.TextEntry, 0, len(input.Evidence))
	contributions := make(map[string]contract.Contribution, len(input.Contributions))
	for _, contribution := range input.Contributions {
		contributions[contribution.ID] = contribution
	}
	factual := indexFactualTextProjection(input)
	for _, unit := range input.Evidence {
		if !persistibleEvidence(unit) {
			continue
		}
		evidenceID, ok := result.EvidenceIDs[unit.ID]
		if !ok || !isUUID(evidenceID) {
			return nil, ErrPipelineMismatch
		}
		metadata := textProjectionMetadata{ProjectionKind: "generic"}
		if contribution, exists := contributions[unit.Contribution.ID]; exists {
			metadata = contributionProjectionMetadata(contribution)
		}
		facts := factual.factsFor(unit)
		metadata = mergeFactProjectionMetadata(metadata, facts)
		exactTerms := append([]string{unit.Contribution.AnalyzerID, unit.Contribution.Method}, metadata.ExactTerms...)
		for _, candidate := range facts {
			exactTerms = append(exactTerms, canonicalFactExactTerms(candidate)...)
		}
		entries = append(entries, retrieval.TextEntry{
			EvidenceID:          evidenceID,
			OrganizationID:      result.OrganizationID,
			SourceID:            result.SourceID,
			SnapshotID:          result.SnapshotID,
			ProjectionKind:      metadata.ProjectionKind,
			ContentState:        unit.ContentState,
			Content:             unit.Content,
			ContentHash:         unit.ContentHash,
			Truncated:           unit.Truncated,
			Classification:      unit.Classification,
			SymbolName:          metadata.SymbolName,
			SymbolQualifiedName: metadata.SymbolQualifiedName,
			ConfigurationKey:    metadata.ConfigurationKey,
			ExceptionType:       metadata.ExceptionType,
			ExactTerms:          exactTerms,
			Persist:             unit.Persist,
		})
	}
	sort.SliceStable(entries, func(left, right int) bool { return entries[left].EvidenceID < entries[right].EvidenceID })
	return entries, nil
}

// factualTextProjectionIndex keeps canonical facts separate from the
// relational evidence UUID assigned by persistence. The bundle contract binds
// a fact to an Evidence Unit by its explicit external EvidenceRef identity;
// locators remain provenance only and are never used to infer identity.
type factualTextProjectionIndex struct {
	byEvidenceID map[string][]fact.CanonicalFact
}

func indexFactualTextProjection(input bundle.Bundle) factualTextProjectionIndex {
	index := factualTextProjectionIndex{
		byEvidenceID: make(map[string][]fact.CanonicalFact),
	}
	for _, candidate := range input.Facts {
		if !factBelongsToBundleScope(candidate, input) {
			continue
		}
		for _, reference := range candidate.Evidence {
			if reference.ID != "" {
				index.byEvidenceID[reference.ID] = append(index.byEvidenceID[reference.ID], candidate)
			}
		}
	}
	return index
}

func (i factualTextProjectionIndex) factsFor(unit evidence.EvidenceUnit) []fact.CanonicalFact {
	candidates := append([]fact.CanonicalFact(nil), i.byEvidenceID[unit.ID]...)
	if len(candidates) < 2 {
		return candidates
	}
	// Keep one copy so repeated references cannot make terms depend on input
	// order. Retrieval normalization performs the final term compaction.
	seen := make(map[string]struct{}, len(candidates))
	unique := candidates[:0]
	for _, candidate := range candidates {
		if _, exists := seen[candidate.ID]; exists {
			continue
		}
		seen[candidate.ID] = struct{}{}
		unique = append(unique, candidate)
	}
	return unique
}

func factBelongsToBundleScope(candidate fact.CanonicalFact, input bundle.Bundle) bool {
	return candidate.Scope.OrganizationID == input.Manifest.Organization.ID &&
		candidate.Scope.SourceID == input.Manifest.Source.ID &&
		candidate.Scope.SnapshotID == input.Manifest.Snapshot.ID
}

func mergeFactProjectionMetadata(metadata textProjectionMetadata, facts []fact.CanonicalFact) textProjectionMetadata {
	if len(facts) == 0 {
		return metadata
	}
	// Canonical bundle writing already orders facts by ID, but textEntries also
	// serves direct callers. Sorting the local copy makes technical metadata
	// independent of fact input order without changing the canonical bundle.
	ordered := append([]fact.CanonicalFact(nil), facts...)
	sort.SliceStable(ordered, func(left, right int) bool { return ordered[left].ID < ordered[right].ID })
	factKind := metadata.ProjectionKind
	if factKind == "generic" {
		for _, candidate := range ordered {
			switch candidate.Predicate {
			case fact.PredicateConfiguration:
				// Configuration is the most specific technical projection when
				// a unit carries more than one canonical assertion.
				factKind = "configuration"
			case fact.PredicateSymbol, fact.PredicateDefinition, fact.PredicateNamedElement:
				if factKind == "generic" {
					factKind = "symbol"
				}
			}
		}
	}
	if metadata.ProjectionKind == "generic" {
		metadata.ProjectionKind = factKind
	}
	for _, candidate := range ordered {
		switch candidate.Predicate {
		case fact.PredicateConfiguration:
			if metadata.ProjectionKind == "configuration" && metadata.ConfigurationKey == "" && candidate.Value != nil && candidate.Value.Kind == fact.ValueString {
				metadata.ConfigurationKey = candidate.Value.String
			}
		case fact.PredicateSymbol, fact.PredicateDefinition, fact.PredicateNamedElement:
			if metadata.ProjectionKind != "symbol" {
				continue
			}
			if metadata.SymbolName == "" && candidate.Value != nil && candidate.Value.Kind == fact.ValueString {
				metadata.SymbolName = candidate.Value.String
			}
			if metadata.SymbolQualifiedName == "" {
				metadata.SymbolQualifiedName = factQualifierString(candidate, "qualified_name", "qualifiedName", "qualified")
			}
		}
	}
	for _, value := range []string{
		metadata.SymbolName, metadata.SymbolQualifiedName,
		metadata.ConfigurationKey, metadata.ExceptionType,
	} {
		if value != "" && !containsFactTerm(metadata.ExactTerms, value) {
			metadata.ExactTerms = append(metadata.ExactTerms, value)
		}
	}
	return metadata
}

func factQualifierString(candidate fact.CanonicalFact, names ...string) string {
	for _, qualifier := range candidate.Qualifiers {
		for _, name := range names {
			if qualifier.Name == name && qualifier.Value.Kind == fact.ValueString {
				return qualifier.Value.String
			}
		}
	}
	return ""
}

func containsFactTerm(terms []string, want string) bool {
	for _, term := range terms {
		if term == want {
			return true
		}
	}
	return false
}

func canonicalFactExactTerms(candidate fact.CanonicalFact) []string {
	terms := []string{string(candidate.Predicate), candidate.Subject.ID}
	if candidate.Object != nil {
		terms = append(terms, candidate.Object.ID)
	}
	if candidate.Value != nil {
		terms = append(terms, canonicalTypedValueTerms(*candidate.Value)...)
	}
	for _, qualifier := range candidate.Qualifiers {
		terms = append(terms, qualifier.Name)
		terms = append(terms, canonicalTypedValueTerms(qualifier.Value)...)
	}
	return validFactTerms(terms)
}

func canonicalTypedValueTerms(value fact.TypedValue) []string {
	scalar := canonicalTypedValueScalar(value)
	return []string{string(value.Kind) + ":" + scalar, scalar}
}

func canonicalTypedValueScalar(value fact.TypedValue) string {
	switch value.Kind {
	case fact.ValueString:
		return value.String
	case fact.ValueInteger:
		return strconv.FormatInt(value.Integer, 10)
	case fact.ValueNumber:
		return strconv.FormatFloat(value.Number, 'g', -1, 64)
	case fact.ValueBoolean:
		return strconv.FormatBool(value.Boolean)
	case fact.ValueNull:
		return "null"
	default:
		return ""
	}
}

func validFactTerms(terms []string) []string {
	valid := make([]string, 0, len(terms))
	for _, term := range terms {
		if term == "" || !utf8.ValidString(term) || strings.ContainsRune(term, '\x00') {
			continue
		}
		valid = append(valid, term)
	}
	return valid
}

type textProjectionMetadata struct {
	ProjectionKind      string
	SymbolName          string
	SymbolQualifiedName string
	ConfigurationKey    string
	ExceptionType       string
	ExactTerms          []string
}

// contributionProjectionMetadata reads only fields already emitted by the
// known analyzers. Unsupported shapes remain generic; this helper never turns
// arbitrary analyzer JSON into a new fact.
func contributionProjectionMetadata(contribution contract.Contribution) textProjectionMetadata {
	metadata := textProjectionMetadata{ProjectionKind: "generic"}
	var fields map[string]json.RawMessage
	if len(contribution.Value) == 0 || json.Unmarshal(contribution.Value, &fields) != nil {
		return metadata
	}
	readString := func(names ...string) string {
		for _, name := range names {
			value, ok := fields[name]
			if !ok {
				continue
			}
			var decoded string
			if json.Unmarshal(value, &decoded) == nil && strings.TrimSpace(decoded) != "" {
				return strings.TrimSpace(decoded)
			}
		}
		return ""
	}
	name := readString("name")
	typ := strings.ToLower(strings.TrimSpace(contribution.Type))
	switch {
	case strings.HasSuffix(typ, ".configuration"):
		metadata.ProjectionKind = "configuration"
		metadata.ConfigurationKey = readString("key")
	case strings.HasSuffix(typ, ".exception"):
		metadata.ProjectionKind = "exception"
		metadata.ExceptionType = readString("type")
	case typ == "wso2.include", typ == "wso2.reference":
		metadata.ProjectionKind = "configuration"
		metadata.ConfigurationKey = readString("target")
	case typ == "wso2.type" || strings.HasSuffix(typ, ".type") || strings.HasSuffix(typ, ".method") ||
		strings.HasSuffix(typ, ".package") || strings.HasSuffix(typ, ".import") || strings.HasSuffix(typ, ".annotation"):
		metadata.ProjectionKind = "symbol"
		metadata.SymbolName = name
		metadata.SymbolQualifiedName = readString("qualified_name", "qualifiedName", "qualified")
	default:
		return metadata
	}
	for _, value := range []string{
		metadata.SymbolName, metadata.SymbolQualifiedName,
		metadata.ConfigurationKey, metadata.ExceptionType,
	} {
		if value != "" {
			metadata.ExactTerms = append(metadata.ExactTerms, value)
		}
	}
	return metadata
}

func persistibleEvidence(unit evidence.EvidenceUnit) bool {
	switch unit.ContentState {
	case evidence.ContentStatePresent:
		return unit.Persist == evidence.DecisionAllow && strings.TrimSpace(unit.Content) != ""
	case evidence.ContentStateRedacted:
		return (unit.Persist == evidence.DecisionAllow || unit.Persist == evidence.DecisionRedact) && unit.Content == evidence.RedactedContent
	default:
		return false
	}
}

func pipelineContext(ctx context.Context) error {
	if ctx == nil {
		return ErrPipelineNotConfigured
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func pipelineStepError(operation string, err error) error {
	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("%s: %w", operation, context.Canceled)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%s: %w", operation, context.DeadlineExceeded)
	}
	return fmt.Errorf("%w: %s", ErrPipelineStep, operation)
}
