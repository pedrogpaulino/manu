package evaluation

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
	"time"

	"github.com/pedrogpaulino/manu/internal/aigateway"
	"github.com/pedrogpaulino/manu/internal/bundle"
	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/evidence"
	"github.com/pedrogpaulino/manu/internal/identity"
	"github.com/pedrogpaulino/manu/internal/ingestion"
	"github.com/pedrogpaulino/manu/internal/query"
	"github.com/pedrogpaulino/manu/internal/retrieval"
)

type simulatedExtractor struct{}

func (simulatedExtractor) Extract(ctx context.Context, item EvaluationCase) (bundle.Bundle, map[string]string, error) {
	if ctx == nil {
		return bundle.Bundle{}, nil, errors.New("evaluation: extraction context is nil")
	}
	if err := ctx.Err(); err != nil {
		return bundle.Bundle{}, nil, err
	}
	const organizationID = defaultEvaluationOrganization
	sourceID := item.SourceID
	revision := item.SourceRevision
	snapshotHash := digestString(sourceID + "\x00" + revision)
	snapshotID := contract.SnapshotID(sourceID, revision, snapshotHash)

	type artifactState struct {
		artifact contract.Artifact
		index    int
	}
	artifactsByPath := make(map[string]artifactState)
	artifacts := make([]contract.Artifact, 0, len(item.ExpectedEvidence))
	contributions := make([]contract.Contribution, 0, len(item.ExpectedEvidence))
	units := make([]evidence.EvidenceUnit, 0, len(item.ExpectedEvidence))
	mapping := make(map[string]string, len(item.ExpectedEvidence))
	for index, expected := range item.ExpectedEvidence {
		path, member, startLine, endLine := expectedLocator(expected, item, index)
		state, exists := artifactsByPath[path]
		if !exists {
			hash := digestString(sourceID + "\x00" + revision + "\x00" + path)
			artifact := contract.Artifact{
				SourceID: sourceID,
				Path:     path,
				Type:     expected.Kind,
				Hash:     hash,
				Size:     int64(len(path)),
				Kind:     "simulated",
			}
			artifact.ID = contract.ArtifactID(artifact.SourceID, artifact.Path, artifact.Hash)
			state = artifactState{artifact: artifact, index: len(artifacts)}
			artifactsByPath[path] = state
			artifacts = append(artifacts, artifact)
		}
		analyzerVersion := "evaluation-v1"
		method := fmt.Sprintf("case-evidence-%03d", index+1)
		locator := contract.Locator{
			SourceID:   sourceID,
			ArtifactID: state.artifact.ID,
			Path:       path,
			Member:     member,
			StartLine:  startLine,
			EndLine:    endLine,
		}
		contribution := contract.Contribution{
			ArtifactID:      state.artifact.ID,
			AnalyzerID:      "evaluation-simulator",
			AnalyzerVersion: analyzerVersion,
			Method:          method,
			Type:            expected.Kind,
			Locator:         locator,
		}
		contribution.ID = contract.ContributionID(contribution.ArtifactID, contribution.AnalyzerID, contribution.AnalyzerVersion, contribution.Method)
		contributions = append(contributions, contribution)

		content := simulatedEvidenceContent(item, expected, path, member, index)
		unit := evidence.EvidenceUnit{
			Version:        evidence.Version,
			OrganizationID: organizationID,
			SourceID:       sourceID,
			SnapshotID:     snapshotID,
			ArtifactID:     state.artifact.ID,
			Contribution: evidence.ContributionRef{
				ID: contribution.ID, ArtifactID: contribution.ArtifactID,
				AnalyzerID: contribution.AnalyzerID, AnalyzerVersion: contribution.AnalyzerVersion,
				Method: contribution.Method,
			},
			Locator:           locator,
			ContentState:      evidence.ContentStatePresent,
			Content:           content,
			ContentHash:       evidence.ContentDigest(content),
			ContentBytes:      int64(len([]byte(content))),
			ContentCharacters: int64(len([]rune(content))),
			Persist:           evidence.DecisionAllow,
			ExternalTransfer:  evidence.DecisionAllow,
			Classification:    evidence.ClassificationSafeText,
		}
		unit.ID = evidence.EvidenceID(unit)
		units = append(units, unit)
		key := expected.EvidenceID
		if key == "" {
			key = fmt.Sprintf("expected-%03d", index+1)
		}
		mapping[key] = unit.ID
	}

	contractManifest := contract.Manifest{
		ContractVersion: contract.Version,
		ResultID:        "evaluation-result-" + digestString(item.CaseID)[:16],
		Source:          contract.Source{ID: sourceID, Name: item.SourceID, Type: "evaluation-fixture", Revision: revision},
		Snapshot:        contract.Snapshot{ID: snapshotID, SourceID: sourceID, Revision: revision, Hash: snapshotHash},
		Execution: contract.ExecutionMetadata{
			RunID:       "evaluation-extraction-" + digestString(item.CaseID)[:16],
			ToolVersion: "evaluation-simulator-v1", ConfigurationID: "evaluation-case-" + fmt.Sprint(item.CaseVersion),
		},
		ArtifactCount: len(artifacts), ContributionCount: len(contributions),
		Coverage: []contract.Coverage{}, Gaps: []contract.Gap{}, Failures: []contract.Failure{},
	}
	for _, expectedGap := range item.ExpectedGaps {
		contractManifest.Gaps = append(contractManifest.Gaps, contract.Gap{
			ID: expectedGap.GapID, Code: expectedGap.Code, Dimension: "evaluation",
			Scope: item.CaseID, Message: expectedGap.Statement,
		})
	}
	legacyResult := contract.Result{Manifest: contractManifest, Artifacts: artifacts, Contributions: contributions}
	digest, err := bundle.FactualDigest(legacyResult, units)
	if err != nil {
		return bundle.Bundle{}, nil, errors.New("evaluation: could not build factual digest")
	}
	fileDigest := digestString(item.CaseID + "\x00files")
	manifest := bundle.Manifest{
		Version:      bundle.Version,
		Organization: bundle.Organization{ID: organizationID, Name: "simulated evaluation"},
		Manifest:     contractManifest,
		Analysis: bundle.Analysis{
			ID:              "evaluation-analysis-" + digestString(item.CaseID)[:16],
			ConfigurationID: contractManifest.Execution.ConfigurationID,
			Revision:        item.SourceRevision,
		},
		FactualDigest: digest,
		Files: []bundle.File{
			{Name: bundle.ArtifactsFileName, Bytes: int64(len(artifacts)), Count: int64(len(artifacts)), Digest: fileDigest},
			{Name: bundle.ContributionsFileName, Bytes: int64(len(contributions)), Count: int64(len(contributions)), Digest: fileDigest},
			{Name: bundle.EvidenceFileName, Bytes: evidenceBytes(units), Count: int64(len(units)), Digest: fileDigest},
		},
		Counts:   bundle.Counts{ArtifactCount: int64(len(artifacts)), ContributionCount: int64(len(contributions)), EvidenceUnitCount: int64(len(units))},
		Limits:   bundle.Limits{MaxBundleBytes: 1 << 20, MaxManifestBytes: 1 << 16, MaxEvidenceBytes: 1 << 20, MaxArtifacts: 1_000, MaxContributions: 1_000, MaxEvidenceUnits: 1_000},
		Evidence: bundle.EvidenceMetadata{State: bundle.EvidenceStateAvailable},
	}
	return bundle.Bundle{Manifest: manifest, Artifacts: artifacts, Contributions: contributions, Evidence: units}, mapping, nil
}

func expectedLocator(expected ExpectedEvidence, item EvaluationCase, index int) (string, string, int, int) {
	if expected.Locator != nil {
		locator := *expected.Locator
		path := locator.Path
		if path == "" {
			path = fmt.Sprintf("evaluation/%s/%03d.metadata", item.CaseID, index+1)
		}
		member := locator.Member
		if member == "" {
			member = expected.Kind
		}
		start, end := locator.StartLine, locator.EndLine
		if start == 0 {
			start = 1
		}
		if end == 0 {
			end = start
		}
		return path, member, start, end
	}
	if expected.Pattern != nil {
		path := expected.Pattern.PathPattern
		if path == "" {
			path = fmt.Sprintf("evaluation/%s/%03d.metadata", item.CaseID, index+1)
		}
		member := expected.Pattern.Member
		if member == "" {
			member = expected.Pattern.Symbol
		}
		if member == "" {
			member = expected.Kind
		}
		return path, member, 1, 1
	}
	return fmt.Sprintf("evaluation/%s/%03d.metadata", item.CaseID, index+1), expected.Kind, 1, 1
}

func simulatedEvidenceContent(item EvaluationCase, expected ExpectedEvidence, path, member string, index int) string {
	return fmt.Sprintf("simulated evidence %s %s %s %s %d", item.CaseID, expected.Kind, path, member, index+1)
}

type simulationLoader struct {
	input bundle.Bundle
}

func (l *simulationLoader) Load(ctx context.Context, _ ingestion.Job) (bundle.Bundle, error) {
	if ctx == nil {
		return bundle.Bundle{}, errors.New("evaluation: loader context is nil")
	}
	if err := ctx.Err(); err != nil {
		return bundle.Bundle{}, err
	}
	return l.input, nil
}

type simulationCanonical struct {
	byDigest             map[string]ingestion.CanonicalPersistenceResult
	units                map[string]evidence.EvidenceUnit
	bundles              map[string]bundle.Bundle
	organizationExternal string
	sourceExternal       string
	snapshotExternal     string
	reused               int
	processed            int
}

func newSimulationCanonical() *simulationCanonical {
	return &simulationCanonical{byDigest: make(map[string]ingestion.CanonicalPersistenceResult), units: make(map[string]evidence.EvidenceUnit), bundles: make(map[string]bundle.Bundle)}
}

func (p *simulationCanonical) Persist(ctx context.Context, input bundle.Bundle) (ingestion.CanonicalPersistenceResult, error) {
	if ctx == nil {
		return ingestion.CanonicalPersistenceResult{}, errors.New("evaluation: canonical context is nil")
	}
	if err := ctx.Err(); err != nil {
		return ingestion.CanonicalPersistenceResult{}, err
	}
	digest := input.Manifest.FactualDigest
	if existing, ok := p.byDigest[digest]; ok {
		p.reused += len(existing.EvidenceIDs)
		return cloneCanonicalResult(existing), nil
	}
	externalOrganization := input.Manifest.Organization.ID
	p.organizationExternal = externalOrganization
	p.sourceExternal = input.Manifest.Source.ID
	p.snapshotExternal = input.Manifest.Snapshot.ID
	organizationID := identity.CanonicalUUID("organization", externalOrganization)
	sourceID := identity.CanonicalUUID("source", externalOrganization, input.Manifest.Source.ID)
	snapshotID := identity.CanonicalUUID("snapshot", externalOrganization, input.Manifest.Source.ID, input.Manifest.Snapshot.ID)
	result := ingestion.CanonicalPersistenceResult{
		OrganizationID: organizationID, SourceID: sourceID, SnapshotID: snapshotID,
		ArtifactIDs: make(map[string]string, len(input.Artifacts)), ObservationIDs: make(map[string]string, len(input.Contributions)), EvidenceIDs: make(map[string]string, len(input.Evidence)),
	}
	for _, artifact := range input.Artifacts {
		result.ArtifactIDs[artifact.ID] = identity.CanonicalUUID("artifact", externalOrganization, input.Manifest.Source.ID, input.Manifest.Snapshot.ID, artifact.ID)
	}
	for _, contribution := range input.Contributions {
		result.ObservationIDs[contribution.ID] = identity.CanonicalUUID("observation", externalOrganization, input.Manifest.Source.ID, input.Manifest.Snapshot.ID, contribution.ID)
	}
	for _, unit := range input.Evidence {
		canonicalID := identity.CanonicalUUID("evidence", externalOrganization, input.Manifest.Source.ID, input.Manifest.Snapshot.ID, unit.ID)
		result.EvidenceIDs[unit.ID] = canonicalID
		p.units[canonicalID] = unit
	}
	p.byDigest[digest] = cloneCanonicalResult(result)
	p.bundles[digest] = input
	p.processed += len(input.Evidence)
	return result, nil
}

func cloneCanonicalResult(input ingestion.CanonicalPersistenceResult) ingestion.CanonicalPersistenceResult {
	output := input
	output.ArtifactIDs = cloneStringMap(input.ArtifactIDs)
	output.ObservationIDs = cloneStringMap(input.ObservationIDs)
	output.EvidenceIDs = cloneStringMap(input.EvidenceIDs)
	return output
}

func cloneStringMap(input map[string]string) map[string]string {
	if input == nil {
		return nil
	}
	output := make(map[string]string, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

func (p *simulationCanonical) ListEmbeddingEvidence(ctx context.Context, organizationID, sourceID, snapshotID string) ([]ingestion.EmbeddingEvidence, error) {
	if ctx == nil {
		return nil, errors.New("evaluation: embedding source context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(p.units))
	for id, unit := range p.units {
		canonicalScope := identity.CanonicalUUID("organization", p.scopeExternalOrganization(unit))
		if canonicalScope != organizationID {
			continue
		}
		canonicalSource := identity.CanonicalUUID("source", p.scopeExternalOrganization(unit), p.sourceExternal)
		// Snapshot identity in the embedding source is derived from the
		// external unit scope and is checked by the pipeline before this port.
		if canonicalSource != sourceID || unit.SnapshotID == "" || p.snapshotExternal == "" {
			continue
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	result := make([]ingestion.EmbeddingEvidence, 0, len(ids))
	for _, id := range ids {
		unit := p.units[id]
		result = append(result, ingestion.EmbeddingEvidence{ID: id, Content: unit.Content, ContentHash: unit.ContentHash, ExternalTransfer: unit.ExternalTransfer, Transfer: unit.ExternalTransfer})
	}
	_ = snapshotID
	return result, nil
}

func (p *simulationCanonical) scopeExternalOrganization(unit evidence.EvidenceUnit) string {
	// All default simulated bundles use one organization. A custom extractor
	// can still use the same deterministic identity contract by setting this
	// boundary explicitly in its bundle.
	if p.organizationExternal != "" {
		return p.organizationExternal
	}
	if unit.OrganizationID == "" {
		return defaultEvaluationOrganization
	}
	return defaultEvaluationOrganization
}

type simulationProjection struct {
	entries       map[string]retrieval.TextEntry
	lastScope     retrievalScope
	rebuilds      int
	activations   int
	relationships int
}

type retrievalScope struct{ organizationID, sourceID, snapshotID string }

func newSimulationProjection() *simulationProjection {
	return &simulationProjection{entries: make(map[string]retrieval.TextEntry)}
}

func (p *simulationProjection) RebuildSnapshot(ctx context.Context, organizationID, sourceID, snapshotID string, entries []retrieval.TextEntry) error {
	if ctx == nil {
		return errors.New("evaluation: text projection context is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	prepared := make(map[string]retrieval.TextEntry, len(entries))
	for _, entry := range entries {
		if err := entry.Validate(); err != nil {
			return err
		}
		if entry.OrganizationID != organizationID || entry.SourceID != sourceID || entry.SnapshotID != snapshotID {
			return retrieval.ErrInvalidTextProjection
		}
		normalized, err := entry.Normalize()
		if err != nil {
			return err
		}
		prepared[normalized.EvidenceID] = normalized
	}
	if sameRetrievalScope(p.lastScope, retrievalScope{organizationID, sourceID, snapshotID}) {
		for id := range prepared {
			if _, ok := p.entries[id]; ok {
				// The caller records canonical reuse; this projection only
				// replaces the immutable snapshot view.
			}
		}
	}
	p.entries = prepared
	p.lastScope = retrievalScope{organizationID, sourceID, snapshotID}
	p.rebuilds++
	return nil
}

func (p *simulationProjection) Search(ctx context.Context, options retrieval.SearchOptions) ([]retrieval.TextHit, error) {
	if ctx == nil {
		return nil, errors.New("evaluation: text search context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	normalized, err := options.Normalize()
	if err != nil {
		return nil, err
	}
	terms := retrieval.ExactTermsForQuery(normalized.Query)
	termSet := make(map[string]struct{}, len(terms))
	for _, term := range terms {
		termSet[term] = struct{}{}
	}
	type scored struct {
		entry retrieval.TextEntry
		score float64
		exact bool
	}
	ordered := make([]scored, 0, len(p.entries))
	for _, entry := range p.entries {
		if entry.OrganizationID != normalized.OrganizationID ||
			(normalized.SourceID != "" && entry.SourceID != normalized.SourceID) ||
			(normalized.SnapshotID != "" && entry.SnapshotID != normalized.SnapshotID) {
			continue
		}
		hits := 0
		exact := false
		for _, term := range entry.ExactTerms {
			if _, ok := termSet[term]; ok {
				hits++
				exact = true
			}
		}
		contentTerms := retrieval.ExactTermsForQuery(entry.Content)
		for _, term := range contentTerms {
			if _, ok := termSet[term]; ok {
				hits++
			}
		}
		if hits == 0 {
			// Technical case questions are not expected to repeat every
			// locator. A bounded fallback keeps the local simulation useful.
			hits = 1
		}
		ordered = append(ordered, scored{entry: entry, score: float64(hits), exact: exact})
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].score != ordered[j].score {
			return ordered[i].score > ordered[j].score
		}
		if ordered[i].exact != ordered[j].exact {
			return ordered[i].exact
		}
		return ordered[i].entry.EvidenceID < ordered[j].entry.EvidenceID
	})
	if len(ordered) > normalized.Limit {
		ordered = ordered[:normalized.Limit]
	}
	result := make([]retrieval.TextHit, 0, len(ordered))
	for index, item := range ordered {
		entry := item.entry
		result = append(result, retrieval.TextHit{
			EvidenceID: entry.EvidenceID, OrganizationID: entry.OrganizationID, SourceID: entry.SourceID, SnapshotID: entry.SnapshotID,
			ProjectionKind: entry.ProjectionKind, ContentState: entry.ContentState, Content: entry.Content, ContentHash: entry.ContentHash,
			Truncated: entry.Truncated, Classification: entry.Classification, SymbolName: entry.SymbolName, SymbolQualifiedName: entry.SymbolQualifiedName,
			ConfigurationKey: entry.ConfigurationKey, ExceptionType: entry.ExceptionType, ExactTerms: append([]string(nil), entry.ExactTerms...), Rank: item.score,
			ExactMatch: item.exact,
		})
		_ = index
	}
	return result, nil
}

func (p *simulationProjection) ValidateSnapshot(ctx context.Context, _, _, _ string) error {
	if ctx == nil {
		return errors.New("evaluation: relational validation context is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	p.relationships++
	return nil
}

func (p *simulationProjection) ActivateSnapshot(ctx context.Context, _, _, _ string) error {
	if ctx == nil {
		return errors.New("evaluation: activation context is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	p.activations++
	return nil
}

func sameRetrievalScope(left, right retrievalScope) bool {
	return left.organizationID == right.organizationID && left.sourceID == right.sourceID && left.snapshotID == right.snapshotID
}

type simulationEmbeddings struct {
	profile   retrieval.EmbeddingProfile
	items     map[string]retrieval.EmbeddingItem
	byHash    map[string]string
	projected int
	reused    int
}

func newSimulationEmbeddings() *simulationEmbeddings {
	return &simulationEmbeddings{items: make(map[string]retrieval.EmbeddingItem), byHash: make(map[string]string)}
}

func (p *simulationEmbeddings) EnsureProfile(ctx context.Context, profile retrieval.EmbeddingProfile) (retrieval.EmbeddingProfile, error) {
	if ctx == nil {
		return retrieval.EmbeddingProfile{}, errors.New("evaluation: embedding profile context is nil")
	}
	if err := ctx.Err(); err != nil {
		return retrieval.EmbeddingProfile{}, err
	}
	normalized, err := profile.Normalize()
	if err != nil {
		return retrieval.EmbeddingProfile{}, err
	}
	if p.profile.ID != "" && !sameEmbeddingProfile(p.profile, normalized) {
		return retrieval.EmbeddingProfile{}, retrieval.ErrEmbeddingProfileMix
	}
	p.profile = normalized
	return normalized, nil
}

func sameEmbeddingProfile(left, right retrieval.EmbeddingProfile) bool {
	return left.ID == right.ID && left.OrganizationID == right.OrganizationID && left.Provider == right.Provider && left.Model == right.Model && left.Dimension == right.Dimension && left.Normalization == right.Normalization && left.ConfigurationVersion == right.ConfigurationVersion && left.ConfigurationDigest == right.ConfigurationDigest && bytes.Equal(left.Configuration, right.Configuration)
}

func (p *simulationEmbeddings) Lookup(ctx context.Context, key retrieval.EmbeddingCacheKey) (retrieval.EmbeddingItem, bool, error) {
	if ctx == nil {
		return retrieval.EmbeddingItem{}, false, errors.New("evaluation: embedding lookup context is nil")
	}
	if err := ctx.Err(); err != nil {
		return retrieval.EmbeddingItem{}, false, err
	}
	normalized, err := key.Normalize()
	if err != nil {
		return retrieval.EmbeddingItem{}, false, err
	}
	id, ok := p.byHash[normalized.EvidenceContentHash]
	if !ok {
		return retrieval.EmbeddingItem{}, false, nil
	}
	item, ok := p.items[id]
	return item, ok, nil
}

func (p *simulationEmbeddings) RebuildSnapshot(ctx context.Context, profile retrieval.EmbeddingProfile, scope retrieval.EmbeddingScope, inputs []retrieval.EmbeddingInput) (retrieval.EmbeddingRebuildResult, error) {
	if ctx == nil {
		return retrieval.EmbeddingRebuildResult{}, errors.New("evaluation: embedding rebuild context is nil")
	}
	if err := ctx.Err(); err != nil {
		return retrieval.EmbeddingRebuildResult{}, err
	}
	normalizedProfile, err := profile.Normalize()
	if err != nil {
		return retrieval.EmbeddingRebuildResult{}, err
	}
	normalizedScope, err := scope.Normalize()
	if err != nil {
		return retrieval.EmbeddingRebuildResult{}, err
	}
	if err := p.ensureScopeProfile(normalizedProfile, normalizedScope); err != nil {
		return retrieval.EmbeddingRebuildResult{}, err
	}
	result := retrieval.EmbeddingRebuildResult{OrganizationID: normalizedScope.OrganizationID, ProfileID: normalizedProfile.ID, SourceID: normalizedScope.SourceID, SnapshotID: normalizedScope.SnapshotID, Requested: len(inputs)}
	for _, input := range inputs {
		normalizedInput, err := input.Normalize(normalizedProfile, normalizedScope)
		if err != nil {
			return retrieval.EmbeddingRebuildResult{}, err
		}
		cacheKey := retrieval.EmbeddingCacheKey{OrganizationID: normalizedScope.OrganizationID, ProfileID: normalizedProfile.ID, EvidenceContentHash: normalizedInput.EvidenceContentHash}
		cached, hit, err := p.Lookup(ctx, cacheKey)
		if err != nil {
			return retrieval.EmbeddingRebuildResult{}, err
		}
		if hit {
			cached.ID = normalizedInput.ID
			cached.EvidenceID = normalizedInput.EvidenceID
			cached.SourceID = normalizedScope.SourceID
			cached.SnapshotID = normalizedScope.SnapshotID
			p.items[cached.ID] = cached
			p.byHash[normalizedInput.EvidenceContentHash] = cached.ID
			result.CacheHits++
			p.reused++
			result.Items = append(result.Items, cached)
			continue
		}
		if len(normalizedInput.Vector) == 0 {
			result.Missing = append(result.Missing, retrieval.EmbeddingMissing{EvidenceID: normalizedInput.EvidenceID, EvidenceContentHash: normalizedInput.EvidenceContentHash})
			continue
		}
		item := retrieval.EmbeddingItem{ID: normalizedInput.ID, OrganizationID: normalizedScope.OrganizationID, ProfileID: normalizedProfile.ID, ProfileDimension: normalizedProfile.Dimension, SourceID: normalizedScope.SourceID, SnapshotID: normalizedScope.SnapshotID, EvidenceID: normalizedInput.EvidenceID, EvidenceContentHash: normalizedInput.EvidenceContentHash, Vector: append([]float32(nil), normalizedInput.Vector...), State: "ready"}
		if _, err := item.Normalize(normalizedProfile, normalizedScope); err != nil {
			return retrieval.EmbeddingRebuildResult{}, err
		}
		p.items[item.ID] = item
		p.byHash[item.EvidenceContentHash] = item.ID
		p.projected++
		result.Inserted++
		result.Items = append(result.Items, item)
	}
	return result, nil
}

func (p *simulationEmbeddings) ensureScopeProfile(profile retrieval.EmbeddingProfile, scope retrieval.EmbeddingScope) error {
	if profile.OrganizationID != scope.OrganizationID {
		return retrieval.ErrEmbeddingScopeMismatch
	}
	return nil
}

func (p *simulationEmbeddings) Search(ctx context.Context, input retrieval.VectorSearchQuery) (retrieval.VectorSearchResponse, error) {
	if ctx == nil {
		return retrieval.VectorSearchResponse{}, errors.New("evaluation: vector search context is nil")
	}
	if err := ctx.Err(); err != nil {
		return retrieval.VectorSearchResponse{}, err
	}
	queryInput, err := input.Normalize()
	if err != nil {
		return retrieval.VectorSearchResponse{}, err
	}
	hits := make([]retrieval.VectorHit, 0, len(p.items))
	for _, item := range p.items {
		if item.OrganizationID != queryInput.OrganizationID || item.SourceID != queryInput.SourceID || item.SnapshotID != queryInput.SnapshotID || item.ProfileID != queryInput.Profile.ID {
			continue
		}
		distance := cosineDistance(queryInput.Vector, item.Vector)
		hits = append(hits, retrieval.VectorHit{EvidenceID: item.EvidenceID, OrganizationID: item.OrganizationID, SourceID: item.SourceID, SnapshotID: item.SnapshotID, ProfileID: item.ProfileID, Profile: queryInput.Profile, Provider: queryInput.Profile.Provider, Model: queryInput.Profile.Model, ProfileDimension: queryInput.Profile.Dimension, EvidenceContentHash: item.EvidenceContentHash, Distance: distance})
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Distance != hits[j].Distance {
			return hits[i].Distance < hits[j].Distance
		}
		return hits[i].EvidenceID < hits[j].EvidenceID
	})
	if len(hits) > queryInput.Limit {
		hits = hits[:queryInput.Limit]
	}
	return retrieval.VectorSearchResponse{Hits: hits, Telemetry: retrieval.VectorSearchTelemetry{ResultCount: len(hits), RequestedLimit: queryInput.Limit, ProfileID: queryInput.Profile.ID}}, nil
}

func cosineDistance(left, right []float32) float64 {
	if len(left) != len(right) || len(left) == 0 {
		return 1
	}
	var dot, leftNorm, rightNorm float64
	for index := range left {
		a, b := float64(left[index]), float64(right[index])
		dot += a * b
		leftNorm += a * a
		rightNorm += b * b
	}
	if leftNorm == 0 || rightNorm == 0 {
		return 1
	}
	value := 1 - dot/(math.Sqrt(leftNorm)*math.Sqrt(rightNorm))
	if value < 0 {
		return 0
	}
	if value > 2 {
		return 2
	}
	return value
}

type simulationWorkspace struct {
	input                bundle.Bundle
	expectedCanonical    map[string]string
	canonical            *simulationCanonical
	projection           *simulationProjection
	embeddings           *simulationEmbeddings
	embedder             *aigateway.SimulatedEmbedder
	vectorAvailable      bool
	profile              retrieval.EmbeddingProfile
	gatewayProfile       aigateway.EmbeddingProfile
	reusedEvidence       int
	embeddingReused      int
	embeddingReprocessed int
	lastCanonical        ingestion.CanonicalPersistenceResult
	lastJobNumber        int
}

func (w *simulationWorkspace) canonicalIDForExternalEvidence(externalID string) string {
	return identity.CanonicalUUID("evidence", w.input.Manifest.Organization.ID, w.input.Manifest.Source.ID, w.input.Manifest.Snapshot.ID, externalID)
}

func (w *simulationWorkspace) packageUnitForCanonical(canonicalID string) (evidence.EvidenceUnit, bool) {
	for _, original := range w.canonical.units {
		if w.canonicalIDForExternalEvidence(original.ID) != canonicalID {
			continue
		}
		organizationID := identity.CanonicalUUID("organization", w.input.Manifest.Organization.ID)
		sourceID := identity.CanonicalUUID("source", w.input.Manifest.Organization.ID, w.input.Manifest.Source.ID)
		snapshotID := identity.CanonicalUUID("snapshot", w.input.Manifest.Organization.ID, w.input.Manifest.Source.ID, w.input.Manifest.Snapshot.ID)
		artifactID := identity.CanonicalUUID("artifact", w.input.Manifest.Organization.ID, w.input.Manifest.Source.ID, w.input.Manifest.Snapshot.ID, original.ArtifactID)
		unit := original
		unit.OrganizationID, unit.SourceID, unit.SnapshotID, unit.ArtifactID = organizationID, sourceID, snapshotID, artifactID
		unit.Contribution.ArtifactID = artifactID
		// The package scope already carries organization/source/snapshot. Keep
		// the portable path/member locator compact enough for the compositor's
		// bounded citation representation.
		unit.Locator.SourceID, unit.Locator.ArtifactID = "", ""
		unit.ID = evidence.EvidenceID(unit)
		return unit, true
	}
	return evidence.EvidenceUnit{}, false
}

func (w *simulationWorkspace) expectedCanonicalEvidence(canonicalID string) (string, bool) {
	for expectedID, value := range w.expectedCanonical {
		if value == canonicalID {
			return expectedID, true
		}
	}
	return "", false
}

func newSimulationWorkspace(input bundle.Bundle, expectedCanonical map[string]string, now time.Time) *simulationWorkspace {
	organizationID := identity.CanonicalUUID("organization", input.Manifest.Organization.ID)
	configuration := json.RawMessage(`{"purpose":"evaluation-simulation","version":"v1"}`)
	digest := sha256.Sum256(configuration)
	profile := retrieval.EmbeddingProfile{
		ID: identity.CanonicalUUID("embedding-profile", organizationID, "evaluation-simulation"), OrganizationID: organizationID,
		Provider: "simulated", Model: "evaluation-embedding", Dimension: 8, Normalization: "none", ConfigurationVersion: "v1", ConfigurationDigest: hex.EncodeToString(digest[:]), Configuration: configuration,
	}
	gatewayProfile := aigateway.EmbeddingProfile{Provider: aigateway.ProviderSimulated, Model: "evaluation-embedding", Version: aigateway.EmbeddingProfileVersion, Dimension: 8, Normalize: false, MaxBatchSize: 128}
	embedder, err := aigateway.NewSimulatedEmbedder(aigateway.SimulatedEmbedderConfig{Profile: gatewayProfile})
	if err != nil {
		// The values above are constants validated by tests. Preserve a nil
		// embedder on impossible configuration failure; the stage will report
		// the controlled ingestion error instead of panicking.
		embedder = nil
	}
	_ = now
	canonicalExpected := make(map[string]string, len(expectedCanonical))
	for expectedID, externalID := range expectedCanonical {
		canonicalExpected[expectedID] = identity.CanonicalUUID("evidence", input.Manifest.Organization.ID, input.Manifest.Source.ID, input.Manifest.Snapshot.ID, externalID)
	}
	return &simulationWorkspace{input: input, expectedCanonical: canonicalExpected, canonical: newSimulationCanonical(), projection: newSimulationProjection(), embeddings: newSimulationEmbeddings(), embedder: embedder, profile: profile, gatewayProfile: gatewayProfile}
}

func (w *simulationWorkspace) ingest(ctx context.Context, input bundle.Bundle, now time.Time, pass string) error {
	job, err := ingestion.NewJob(ingestion.NewJobInput{
		ID:             identity.CanonicalUUID("evaluation-job", input.Manifest.Organization.ID, input.Manifest.Source.ID, input.Manifest.Snapshot.ID, pass),
		OrganizationID: identity.CanonicalUUID("organization", input.Manifest.Organization.ID), OrganizationExternalID: input.Manifest.Organization.ID,
		SourceID: identity.CanonicalUUID("source", input.Manifest.Organization.ID, input.Manifest.Source.ID), SnapshotID: identity.CanonicalUUID("snapshot", input.Manifest.Organization.ID, input.Manifest.Source.ID, input.Manifest.Snapshot.ID),
		SourceExternalID: input.Manifest.Source.ID, SnapshotExternalID: input.Manifest.Snapshot.ID, FactualDigest: input.Manifest.FactualDigest, AnalysisConfigurationID: input.Manifest.Analysis.ConfigurationID, CreatedAt: now,
	})
	if err != nil {
		return err
	}
	store := ingestion.NewMemoryStore(ingestion.MemoryStoreOptions{Now: func() time.Time { return now }})
	if _, err := store.Create(ctx, job); err != nil {
		return err
	}
	loader := &simulationLoader{input: input}
	// A real local corpus is extracted with external transfer denied. Keep its
	// canonical and textual projections usable locally, but do not configure an
	// embedding stage that would turn that policy decision into ErrJobPartial.
	// The simulated fixture intentionally marks every unit transferable so its
	// deterministic vector path remains covered.
	w.vectorAvailable = embeddingApplicable(input)
	var pipeline *ingestion.Pipeline
	if w.vectorAvailable {
		options, optionsErr := w.embeddingOptions()
		if optionsErr != nil {
			return optionsErr
		}
		pipeline, err = ingestion.NewPipelineWithEmbeddings(store, loader, w.canonical, w.projection, w.projection, w.projection, options)
	} else {
		pipeline, err = ingestion.NewPipeline(store, loader, w.canonical, w.projection, w.projection, w.projection)
	}
	if err != nil {
		return err
	}
	executor, err := ingestion.NewExecutor(store, pipeline.Handler(), ingestion.ExecutorOptions{OrganizationID: job.OrganizationID, Workers: 1, LeaseDuration: time.Minute, HeartbeatInterval: time.Second, PollInterval: time.Millisecond, Owner: "evaluation-runner"})
	if err != nil {
		return err
	}
	claimed, err := executor.RunOnce(ctx)
	if err != nil {
		return err
	}
	if !claimed {
		return errors.New("evaluation: ingestion job was not claimed")
	}
	w.reusedEvidence = w.canonical.reused
	w.embeddingReused = w.embeddings.reused
	w.embeddingReprocessed = w.embeddings.projected
	return nil
}

// embeddingApplicable is deliberately conservative for a local evaluation:
// vector projection is enabled only when every present unit is explicitly
// authorized for transfer. If any unit is denied, textual/canonical evidence
// is still evaluated while vector retrieval is reported as unavailable.
func embeddingApplicable(input bundle.Bundle) bool {
	if len(input.Evidence) == 0 {
		return false
	}
	for _, unit := range input.Evidence {
		if unit.ContentState != evidence.ContentStatePresent ||
			unit.ExternalTransfer != evidence.DecisionAllow ||
			strings.TrimSpace(unit.Content) == "" {
			return false
		}
	}
	return true
}

func (w *simulationWorkspace) embeddingOptions() (ingestion.EmbeddingOptions, error) {
	if w.embedder == nil {
		return ingestion.EmbeddingOptions{}, errors.New("evaluation: simulated embedder is unavailable")
	}
	return ingestion.EmbeddingOptions{Mode: ingestion.EmbeddingModeEnabled, Profile: w.profile, GatewayProfile: w.gatewayProfile, Embedder: w.embedder, Projector: w.embeddings, EvidenceSource: w.canonical, Timeout: time.Minute}, nil
}

func (w *simulationWorkspace) retrieveAndCompose(ctx context.Context, item EvaluationCase, topK int) (retrieval.FusionResponse, query.Composition, query.Scope, error) {
	organizationID := identity.CanonicalUUID("organization", w.input.Manifest.Organization.ID)
	sourceID := identity.CanonicalUUID("source", w.input.Manifest.Organization.ID, w.input.Manifest.Source.ID)
	snapshotID := identity.CanonicalUUID("snapshot", w.input.Manifest.Organization.ID, w.input.Manifest.Source.ID, w.input.Manifest.Snapshot.ID)
	textProjection := retrieval.NewTextProjection(w.projection)
	textHits, err := textProjection.Search(ctx, retrieval.SearchOptions{OrganizationID: organizationID, SourceID: sourceID, SnapshotID: snapshotID, Query: item.CompetenceQuestion, Limit: topK})
	if err != nil {
		return retrieval.FusionResponse{}, query.Composition{}, query.Scope{}, err
	}
	exact := make([]retrieval.ExactHit, 0, len(textHits))
	for index, hit := range textHits {
		exact = append(exact, retrieval.ExactHit{EvidenceID: hit.EvidenceID, OrganizationID: hit.OrganizationID, SourceID: hit.SourceID, SnapshotID: hit.SnapshotID, EvidenceContentHash: hit.ContentHash, Rank: index + 1})
	}
	var vectorHits []retrieval.VectorHit
	if w.vectorAvailable {
		vectorHits, err = w.vectorSearch(ctx, organizationID, sourceID, snapshotID, item.CompetenceQuestion, topK)
		if err != nil {
			return retrieval.FusionResponse{}, query.Composition{}, query.Scope{}, err
		}
	}
	fused, err := retrieval.Fuse(ctx, retrieval.FusionRequest{Scope: retrieval.FusionScope{OrganizationID: organizationID, SourceID: sourceID, SnapshotID: snapshotID}, Configuration: retrieval.FusionConfiguration{MaxCandidates: topK}, Exact: exact, Textual: textHits, Vector: vectorHits})
	if err != nil {
		return retrieval.FusionResponse{}, query.Composition{}, query.Scope{}, err
	}
	candidates := make([]query.PackageCandidate, 0, len(fused.Candidates))
	for _, candidate := range fused.Candidates {
		// Fusion uses canonical UUIDs at the retrieval boundary. The evidence
		// package keeps its deterministic external identity, so translate only
		// at the package boundary and retain the same provenance hash.
		packagedUnit, ok := w.packageUnitForCanonical(candidate.EvidenceID)
		if !ok {
			continue
		}
		candidate.EvidenceID = packagedUnit.ID
		candidate.Provenance.EvidenceID = packagedUnit.ID
		candidates = append(candidates, query.PackageCandidate{Fusion: candidate, Unit: packagedUnit, Kind: query.CandidateKindText})
	}
	scope := query.Scope{OrganizationID: organizationID, SourceID: sourceID, SnapshotID: snapshotID}
	gaps := make([]query.MaterialGap, 0, len(item.ExpectedGaps))
	for _, gap := range item.ExpectedGaps {
		gaps = append(gaps, query.MaterialGap{ID: gap.GapID, Code: gap.Code, Dimension: "evaluation"})
	}
	composition, err := query.ComposeEvidencePackage(ctx, query.PackageRequest{Scope: scope, Candidates: candidates, Gaps: gaps, Limits: query.DefaultPackageLimits()})
	if err != nil {
		return retrieval.FusionResponse{}, query.Composition{}, query.Scope{}, err
	}
	return fused, composition, scope, nil
}

func (w *simulationWorkspace) policyExcluded(item EvaluationCase, fused retrieval.FusionResponse, composition query.Composition) int {
	retrieved := make(map[string]struct{}, len(fused.Candidates))
	for _, candidate := range fused.Candidates {
		retrieved[candidate.EvidenceID] = struct{}{}
	}
	inPackage := make(map[string]struct{}, len(composition.ValidationPackage.Evidence))
	for _, reference := range composition.ValidationPackage.Evidence {
		inPackage[reference.ID] = struct{}{}
	}
	excluded := 0
	for index, expected := range item.ExpectedEvidence {
		canonicalID := w.expectedCanonical[caseEvidenceKey(expected, index)]
		if canonicalID == "" {
			continue
		}
		if _, ok := retrieved[canonicalID]; !ok {
			continue
		}
		unit, ok := w.packageUnitForCanonical(canonicalID)
		if ok {
			if _, included := inPackage[unit.ID]; !included {
				excluded++
			}
		}
	}
	return excluded
}

// localOnlyEvidenceCount reports evidence that exists in the local bundle but
// is not authorized for external transfer. It is intentionally independent
// from the top-k result: a policy block must remain visible even when the
// textual ranker did not select the expected locator.
func (w *simulationWorkspace) localOnlyEvidenceCount() int {
	if w == nil {
		return 0
	}
	count := 0
	for _, unit := range w.input.Evidence {
		if unit.ExternalTransfer != evidence.DecisionAllow {
			count++
		}
	}
	return count
}

func (w *simulationWorkspace) vectorSearch(ctx context.Context, organizationID, sourceID, snapshotID, question string, limit int) ([]retrieval.VectorHit, error) {
	questionHash := evidence.ContentDigest(question)
	request := aigateway.EmbeddingRequest{ExecutionID: "evaluation-query", RequestID: "evaluation-query-" + questionHash[:16], Deadline: time.Now().Add(time.Minute), Profile: w.gatewayProfile, Items: []aigateway.EmbeddingItem{{ID: "evaluation-query-item", Content: question, ContentHash: questionHash}}}
	if err := request.Validate(); err != nil {
		return nil, err
	}
	result, err := w.embedder.Embed(ctx, request)
	if err != nil {
		return nil, err
	}
	if err := result.Validate(request); err != nil {
		return nil, err
	}
	vector := make([]float32, len(result.Vectors[0]))
	for index, value := range result.Vectors[0] {
		vector[index] = float32(value)
	}
	search := retrieval.NewVectorProjection(w.embeddings)
	response, err := search.Search(ctx, retrieval.VectorSearchQuery{OrganizationID: organizationID, SourceID: sourceID, SnapshotID: snapshotID, Profile: w.profile, Vector: vector, Limit: limit})
	if err != nil {
		return nil, err
	}
	return response.Hits, nil
}

func (w *simulationWorkspace) generate(ctx context.Context, item EvaluationCase, composition query.Composition, scope query.Scope, fused retrieval.FusionResponse, localOnlyEvidenceCount int) (GenerationMetric, query.Response, error) {
	questionKind := questionKind(item.Kind)
	input := query.AbstentionInput{Package: composition.ValidationPackage, QueryID: queryID(item), QueryDigest: queryDigest(item.CompetenceQuestion), QuestionKind: questionKind, LocalOnlyEvidenceCount: localOnlyEvidenceCount, Support: query.SupportAssessment{Kind: questionKind, Level: query.EvidenceSupportSufficient}}
	if item.Kind == CaseKindAbstention {
		input.Support = query.SupportAssessment{Kind: query.KnowledgeQuestionPossibleFlow, Level: query.EvidenceSupportSufficient}
	}
	expectedAbstention := item.Kind == CaseKindAbstention
	abstention, err := query.EvaluateAbstention(input)
	if err != nil {
		return GenerationMetric{}, query.Response{}, err
	}
	if abstention.Decision.Abstain {
		return generationMetricFromResponse(abstention.Response, true, expectedAbstention, 0), abstention.Response, nil
	}
	evidenceIDs := make([]string, 0, len(composition.GatewayPackage.Evidence))
	for _, item := range composition.GatewayPackage.Evidence {
		evidenceIDs = append(evidenceIDs, item.ID)
	}
	sort.Strings(evidenceIDs)
	profile := aigateway.GenerationProfile{Provider: aigateway.ProviderSimulated, Model: "evaluation-generator", Version: aigateway.GenerationProfileVersion, Protocol: aigateway.ProtocolChatCompletions, MaxOutputBytes: 4 << 10}
	generator, err := aigateway.NewSimulatedGenerator(aigateway.SimulatedGeneratorConfig{Profile: profile, Fixture: aigateway.GenerationEnvelope{Version: aigateway.GenerationEnvelopeVersion, Text: "resposta simulada para avaliação", EvidenceIDs: evidenceIDs}})
	if err != nil {
		return GenerationMetric{}, query.Response{}, err
	}
	request := aigateway.GenerationRequest{ExecutionID: "evaluation-generation", RequestID: "evaluation-generation-" + queryDigest(item.CompetenceQuestion)[:16], Deadline: time.Now().Add(time.Minute), Profile: profile, Question: item.CompetenceQuestion, Package: composition.GatewayPackage}
	generated, err := generator.Generate(ctx, request)
	if err != nil {
		return GenerationMetric{}, query.Response{}, err
	}
	response := responseFromGeneration(item, composition, scope, generated, fused)
	metric := generationMetricFromResponse(response, false, false, generated.Latency)
	return metric, response, nil
}

func questionKind(kind CaseKind) query.KnowledgeQuestionKind {
	switch kind {
	case CaseKindInventory:
		return query.KnowledgeQuestionInventory
	case CaseKindPossibleFlow:
		return query.KnowledgeQuestionPossibleFlow
	case CaseKindAbstention:
		return query.KnowledgeQuestionObservedExecution
	default:
		return query.KnowledgeQuestionPossibleFlow
	}
}

func responseFromGeneration(item EvaluationCase, composition query.Composition, scope query.Scope, generated aigateway.GenerationResult, _ retrieval.FusionResponse) query.Response {
	citations := make([]query.Citation, 0, len(generated.Output.EvidenceIDs))
	byID := make(map[string]query.EvidenceReference, len(composition.ValidationPackage.Evidence))
	for _, reference := range composition.ValidationPackage.Evidence {
		byID[reference.ID] = reference
	}
	for index, id := range generated.Output.EvidenceIDs {
		reference, ok := byID[id]
		if !ok {
			continue
		}
		citations = append(citations, query.Citation{Ordinal: index + 1, OrganizationID: scope.OrganizationID, SourceID: scope.SourceID, SnapshotID: scope.SnapshotID, EvidenceID: id, Locator: reference.Locator, Role: query.CitationRoleSupports})
	}
	claimSupport := query.SupportSupported
	if generated.Termination == aigateway.TerminationAbstained {
		claimSupport = query.SupportAbstained
	}
	claim := query.Claim{Ordinal: 1, Kind: query.ClaimKindGenerated, Support: claimSupport, Text: generated.Output.Text}
	for index := range citations {
		claim.CitationOrdinals = append(claim.CitationOrdinals, citations[index].Ordinal)
	}
	gaps := make([]query.Gap, 0, len(composition.Gaps))
	for index, gap := range composition.Gaps {
		gaps = append(gaps, query.Gap{Ordinal: index + 1, ID: gap.ID, Code: gap.Code, Message: gap.Code})
	}
	provider := query.Provider(generated.Provider)
	protocol := query.ProtocolChatCompletions
	if generated.Provider == aigateway.ProviderOpenAI {
		protocol = query.ProtocolResponses
	}
	termination := query.TerminationCompleted
	if generated.Termination == aigateway.TerminationAbstained {
		termination = query.TerminationAbstained
	} else if generated.Termination == aigateway.TerminationPartial {
		termination = query.TerminationPartial
	}
	startedAt := time.Unix(0, 0).UTC()
	return query.Response{Version: query.Version, KnowledgeState: query.KnowledgeStateGeneratedReviewable, Text: generated.Output.Text, Claims: []query.Claim{claim}, Citations: citations, Gaps: gaps, Generation: query.GenerationMetadata{Provider: provider, Model: generated.Model, Profile: generated.Model, Protocol: protocol, Usage: query.Usage{InputItems: generated.Usage.InputItems, OutputItems: generated.Usage.OutputItems, InputTokens: generated.Usage.InputTokens, OutputTokens: generated.Usage.OutputTokens}, Termination: termination, PackageID: composition.ValidationPackage.ID, PackageDigest: composition.ValidationPackage.Digest, QueryID: queryID(item), QueryDigest: queryDigest(item.CompetenceQuestion), StartedAt: startedAt, FinishedAt: startedAt.Add(generated.Latency), Latency: generated.Latency}}
}

func generationMetricFromResponse(response query.Response, abstained, expected bool, duration time.Duration) GenerationMetric {
	validClaims := 0
	unsupported := 0
	for _, claim := range response.Claims {
		if claim.Support == query.SupportSupported || claim.Support == query.SupportAbstained {
			validClaims++
		} else {
			unsupported++
		}
	}
	return GenerationMetric{Status: StageCompleted, Duration: duration, Termination: string(response.Generation.Termination), Provider: string(response.Generation.Provider), Model: response.Generation.Model, Profile: response.Generation.Profile, InputTokens: response.Generation.Usage.InputTokens, OutputTokens: response.Generation.Usage.OutputTokens, LocalOnly: true, ValidClaims: validClaims, TotalClaims: len(response.Claims), ValidCitations: len(response.Citations), TotalCitations: len(response.Citations), Abstained: abstained, ExpectedAbstention: expected, AbstentionCorrect: abstained == expected, UnsupportedClaims: unsupported}
}
