package persistence

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/pedrogpaulino/manu/internal/bundle"
	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/evidence"
	"github.com/pedrogpaulino/manu/internal/fact"
	"github.com/pedrogpaulino/manu/internal/identity"
	"github.com/pedrogpaulino/manu/internal/ingestion"
)

// BundlePersistenceResult exposes the canonical IDs assigned to one accepted
// bundle. The maps are keyed by the external IDs carried by that bundle.
// Canonical IDs are deterministic, so a compatible retry returns the same
// result without duplicating rows.
type BundlePersistenceResult struct {
	FactualDigest  string
	OrganizationID string
	SourceID       string
	SnapshotID     string
	ArtifactIDs    map[string]string
	ObservationIDs map[string]string
	EvidenceIDs    map[string]string
	CoverageIDs    map[string]string
	GapIDs         map[string]string
	FailureIDs     map[string]string
	// FrontendManifestIDs and CanonicalFactIDs are populated for v1alpha2
	// bundles. They are additive to the legacy result and keyed by external
	// identities from the factual sequences.
	FrontendManifestIDs map[string]string
	CanonicalFactIDs    map[string]string
	// FactualIdentityIDs retains the external-ID key spelling used by the
	// original batch API. StableFactualIdentityIDs is the canonical comparison
	// key spelling used by localized updates. Both values point to
	// snapshot-scoped rows.
	FactualIdentityIDs       map[string]string
	StableFactualIdentityIDs map[string]string
}

type preparedBundle struct {
	input             bundle.Bundle
	organizationID    string
	sourceID          string
	snapshotID        string
	artifactIDs       map[string]string
	observationIDs    map[string]string
	evidenceIDs       map[string]string
	coverageIDs       map[string]string
	gapIDs            map[string]string
	failureIDs        map[string]string
	factualIdentities []preparedFactualIdentity
	factual           *PreparedFactualSnapshot
	capturedAt        time.Time
}

type preparedFactualIdentity struct {
	id         string
	sourceID   string
	snapshotID string
	key        string
	legacyKey  string
	digest     string
}

// PersistBundle validates and persists one complete bundle in a single
// transaction. It deliberately does not activate the snapshot; activation is
// owned by the later ingestion pipeline after all applicable projections are
// ready.
func (r *Repository) PersistBundle(ctx context.Context, input bundle.Bundle) (BundlePersistenceResult, error) {
	if err := validateContext(ctx); err != nil {
		return BundlePersistenceResult{}, err
	}
	prepared, err := prepareBundle(input)
	if err != nil {
		return BundlePersistenceResult{}, err
	}

	result := prepared.result()
	err = r.WithinTx(ctx, func(u *UnitOfWork) error {
		return persistPreparedBundle(ctx, u, prepared)
	})
	if err != nil {
		return BundlePersistenceResult{}, err
	}
	return result, nil
}

// PersistBundleIncremental compares two immutable bundles and persists only
// the new canonical snapshot. Canonical rows remain snapshot-scoped, while
// factual identity keys and the returned report let derived projection
// adapters copy compatible work forward. The previous snapshot is never
// updated or deleted by this operation.
func (r *Repository) PersistBundleIncremental(ctx context.Context, previous, current bundle.Bundle, options ...ingestion.IncrementalOptions) (BundlePersistenceResult, ingestion.IncrementalReport, error) {
	if err := validateContext(ctx); err != nil {
		return BundlePersistenceResult{}, ingestion.IncrementalReport{}, err
	}
	// Keep comparison IDs aligned with the normalized rows that are about to
	// be inserted. Coverage/gap/failure IDs may be omitted by a valid bundle
	// transport and are derived locally without mutating the caller.
	previous = normalizeBatchBundle(previous)
	current = normalizeBatchBundle(current)
	report, err := ingestion.CompareBundles(ctx, previous, current, options...)
	if err != nil {
		return BundlePersistenceResult{}, ingestion.IncrementalReport{}, err
	}
	prepared, err := prepareBundle(current)
	if err != nil {
		return BundlePersistenceResult{}, ingestion.IncrementalReport{}, err
	}
	result := prepared.result()
	if err := r.WithinTx(ctx, func(u *UnitOfWork) error {
		return persistPreparedBundle(ctx, u, prepared)
	}); err != nil {
		return BundlePersistenceResult{}, ingestion.IncrementalReport{}, err
	}
	return result, report, nil
}

// PersistIncremental is a concise alias for callers using the persistence
// adapter as the incremental canonical port.
func (r *Repository) PersistIncremental(ctx context.Context, previous, current bundle.Bundle, options ...ingestion.IncrementalOptions) (BundlePersistenceResult, ingestion.IncrementalReport, error) {
	return r.PersistBundleIncremental(ctx, previous, current, options...)
}

func prepareBundle(input bundle.Bundle) (preparedBundle, error) {
	input = normalizeBatchBundle(input)
	if err := input.Validate(); err != nil {
		return preparedBundle{}, fmt.Errorf("validate bundle: %w", err)
	}
	// Bundle.Validate checks this digest as well. Computing it explicitly here
	// keeps the batch boundary honest if validation is extended later and makes
	// the idempotency key independent of transport bytes.
	digest, err := input.FactualDigest()
	if err != nil {
		return preparedBundle{}, fmt.Errorf("compute bundle factual digest: %w", err)
	}
	if digest != input.Manifest.FactualDigest {
		return preparedBundle{}, fmt.Errorf("%w: bundle factual digest", bundle.ErrDigestMismatch)
	}
	for index, unit := range input.Evidence {
		if err := unit.ValidatePrepared(); err != nil {
			return preparedBundle{}, fmt.Errorf("evidence unit %d is not prepared: %w", index, err)
		}
	}

	artifactIDs := make(map[string]string, len(input.Artifacts))
	for _, artifact := range input.Artifacts {
		artifactIDs[artifact.ID] = batchCanonicalUUID(
			"artifact", input.Manifest.Organization.ID, input.Manifest.Source.ID,
			input.Manifest.Snapshot.ID, artifact.ID,
		)
	}
	observationIDs := make(map[string]string, len(input.Contributions))
	for _, contribution := range input.Contributions {
		observationIDs[contribution.ID] = batchCanonicalUUID(
			"observation", input.Manifest.Organization.ID, input.Manifest.Source.ID,
			input.Manifest.Snapshot.ID, contribution.ID,
		)
	}
	for _, failure := range input.Manifest.Failures {
		if failure.ArtifactID != "" {
			if _, exists := artifactIDs[failure.ArtifactID]; !exists {
				return preparedBundle{}, fmt.Errorf("%w: failure %q references artifact %q", bundle.ErrInvalidReference, failure.ID, failure.ArtifactID)
			}
		}
	}

	evidenceIDs := make(map[string]string, len(input.Evidence))
	for _, unit := range input.Evidence {
		evidenceIDs[unit.ID] = batchCanonicalUUID(
			"evidence", input.Manifest.Organization.ID, input.Manifest.Source.ID,
			input.Manifest.Snapshot.ID, unit.ID,
		)
	}
	coverageIDs := make(map[string]string, len(input.Manifest.Coverage))
	for _, coverage := range input.Manifest.Coverage {
		coverageIDs[coverage.ID] = batchCanonicalUUID(
			"coverage", input.Manifest.Organization.ID, input.Manifest.Source.ID,
			input.Manifest.Snapshot.ID, coverage.ID,
		)
	}
	gapIDs := make(map[string]string, len(input.Manifest.Gaps))
	for _, gap := range input.Manifest.Gaps {
		gapIDs[gap.ID] = batchCanonicalUUID(
			"gap", input.Manifest.Organization.ID, input.Manifest.Source.ID,
			input.Manifest.Snapshot.ID, gap.ID,
		)
	}
	failureIDs := make(map[string]string, len(input.Manifest.Failures))
	for _, failure := range input.Manifest.Failures {
		failureIDs[failure.ID] = batchCanonicalUUID(
			"failure", input.Manifest.Organization.ID, input.Manifest.Source.ID,
			input.Manifest.Snapshot.ID, failure.ID,
		)
	}

	identities := make([]preparedFactualIdentity, 0,
		len(input.Artifacts)+len(input.Contributions)+len(input.Evidence)+
			len(input.Manifest.Coverage)+len(input.Manifest.Gaps)+len(input.Manifest.Failures),
	)
	stableKeys, err := ingestion.IncrementalFactKeys(context.Background(), input)
	if err != nil {
		return preparedBundle{}, fmt.Errorf("compute stable factual identities: %w", err)
	}
	appendIdentity := func(kind, externalID string, fact any) error {
		legacyKey := kind + ":" + externalID
		key := legacyKey
		if values := stableKeys[ingestion.IncrementalFactKind(kind)]; values != nil {
			if stable, exists := values[externalID]; exists {
				key = stablePersistenceIdentityKey(stable)
			}
		}
		digest, err := batchFactDigest(kind, fact)
		if err != nil {
			return fmt.Errorf("compute factual identity %q: %w", key, err)
		}
		identities = append(identities, preparedFactualIdentity{
			id: batchCanonicalUUID("factual-identity", input.Manifest.Organization.ID,
				input.Manifest.Source.ID, input.Manifest.Snapshot.ID, key),
			sourceID: sourceCanonicalID(input), snapshotID: snapshotCanonicalID(input),
			key: key, legacyKey: legacyKey, digest: digest,
		})
		return nil
	}
	for _, artifact := range input.Artifacts {
		if err := appendIdentity("artifact", artifact.ID, artifact); err != nil {
			return preparedBundle{}, err
		}
	}
	for _, contribution := range input.Contributions {
		if err := appendIdentity("observation", contribution.ID, contribution); err != nil {
			return preparedBundle{}, err
		}
	}
	for _, coverage := range input.Manifest.Coverage {
		if err := appendIdentity("coverage", coverage.ID, coverage); err != nil {
			return preparedBundle{}, err
		}
	}
	for _, gap := range input.Manifest.Gaps {
		if err := appendIdentity("gap", gap.ID, gap); err != nil {
			return preparedBundle{}, err
		}
	}
	for _, failure := range input.Manifest.Failures {
		if err := appendIdentity("failure", failure.ID, failure); err != nil {
			return preparedBundle{}, err
		}
	}
	for _, unit := range input.Evidence {
		if err := appendIdentity("evidence", unit.ID, factualEvidenceForIdentity(unit)); err != nil {
			return preparedBundle{}, err
		}
	}

	var factual *PreparedFactualSnapshot
	if input.Manifest.Version == bundle.VersionV1Alpha2 {
		prepared, err := prepareFactualSnapshot(FactualSnapshotInput{
			OrganizationID: organizationCanonicalID(input),
			SourceID:       sourceCanonicalID(input),
			SnapshotID:     snapshotCanonicalID(input),
			Scope: fact.Scope{
				OrganizationID: input.Manifest.Organization.ID,
				SourceID:       input.Manifest.Source.ID,
				SnapshotID:     input.Manifest.Snapshot.ID,
			},
			FrontendManifests: input.FrontendManifests,
			Facts:             input.Facts,
		})
		if err != nil {
			return preparedBundle{}, err
		}
		factual = &prepared
	}

	return preparedBundle{
		input:             input,
		organizationID:    organizationCanonicalID(input),
		sourceID:          sourceCanonicalID(input),
		snapshotID:        snapshotCanonicalID(input),
		artifactIDs:       artifactIDs,
		observationIDs:    observationIDs,
		evidenceIDs:       evidenceIDs,
		coverageIDs:       coverageIDs,
		gapIDs:            gapIDs,
		failureIDs:        failureIDs,
		factualIdentities: identities,
		factual:           factual,
		capturedAt:        batchCapturedAt(input),
	}, nil
}

// stablePersistenceIdentityKey keeps the comparison key's exact bytes while
// satisfying the canonical text boundary, which rejects NUL separators used
// internally by the deterministic key format.
func stablePersistenceIdentityKey(key string) string {
	return "v1:" + hex.EncodeToString([]byte(key))
}

func normalizeBatchBundle(input bundle.Bundle) bundle.Bundle {
	// Bundle validation intentionally accepts omitted IDs for coverage, gap,
	// and failure entries because the contract normalizer can derive them.
	// Copy these slices before deriving IDs so persistence never mutates the
	// caller's bundle while still accepting the same valid contract input.
	input.Manifest.Coverage = append([]contract.Coverage(nil), input.Manifest.Coverage...)
	input.Manifest.Gaps = append([]contract.Gap(nil), input.Manifest.Gaps...)
	input.Manifest.Failures = append([]contract.Failure(nil), input.Manifest.Failures...)
	for index := range input.Manifest.Coverage {
		if input.Manifest.Coverage[index].ID == "" {
			value := input.Manifest.Coverage[index]
			input.Manifest.Coverage[index].ID = contract.CoverageID(value.Dimension, value.Scope, value.State, value.AnalyzerID)
		}
	}
	for index := range input.Manifest.Gaps {
		if input.Manifest.Gaps[index].ID == "" {
			value := input.Manifest.Gaps[index]
			input.Manifest.Gaps[index].ID = contract.GapID(value.Code, value.Dimension, value.Scope, value.Message, value.AnalyzerID)
		}
	}
	for index := range input.Manifest.Failures {
		if input.Manifest.Failures[index].ID == "" {
			value := input.Manifest.Failures[index]
			input.Manifest.Failures[index].ID = contract.FailureID(value.Code, value.Operation, value.ArtifactID, value.AnalyzerID, value.Message)
		}
	}
	return input
}

func (p preparedBundle) result() BundlePersistenceResult {
	result := BundlePersistenceResult{
		FactualDigest:            p.input.Manifest.FactualDigest,
		OrganizationID:           p.organizationID,
		SourceID:                 p.sourceID,
		SnapshotID:               p.snapshotID,
		ArtifactIDs:              p.artifactIDs,
		ObservationIDs:           p.observationIDs,
		EvidenceIDs:              p.evidenceIDs,
		CoverageIDs:              p.coverageIDs,
		GapIDs:                   p.gapIDs,
		FailureIDs:               p.failureIDs,
		FactualIdentityIDs:       factualIdentityIDs(p.factualIdentities),
		StableFactualIdentityIDs: stableFactualIdentityIDs(p.factualIdentities),
	}
	if p.factual != nil {
		result.FrontendManifestIDs = make(map[string]string, len(p.factual.FrontendManifests))
		for _, manifest := range p.factual.FrontendManifests {
			result.FrontendManifestIDs[manifest.ExternalID] = manifest.ID
		}
		result.CanonicalFactIDs = make(map[string]string, len(p.factual.Facts))
		for _, canonicalFact := range p.factual.Facts {
			result.CanonicalFactIDs[canonicalFact.ExternalID] = canonicalFact.ID
		}
	}
	return result
}

func factualIdentityIDs(identities []preparedFactualIdentity) map[string]string {
	result := make(map[string]string, len(identities))
	for _, identity := range identities {
		key := identity.legacyKey
		if key == "" {
			key = identity.key
		}
		result[key] = identity.id
	}
	return result
}

func stableFactualIdentityIDs(identities []preparedFactualIdentity) map[string]string {
	result := make(map[string]string, len(identities))
	for _, identity := range identities {
		result[identity.key] = identity.id
	}
	return result
}

func persistPreparedBundle(ctx context.Context, u *UnitOfWork, p preparedBundle) error {
	input := p.input
	organizationName := input.Manifest.Organization.Name
	if strings.TrimSpace(organizationName) == "" {
		// The bundle permits an omitted display name. The canonical table does
		// not; retaining the explicit external identity is deterministic and
		// does not invent descriptive tenant data.
		organizationName = input.Manifest.Organization.ID
	}
	if err := u.EnsureOrganization(ctx, p.organizationID, Organization{
		ID: p.organizationID, ExternalID: input.Manifest.Organization.ID, Name: organizationName,
	}); err != nil {
		return err
	}
	if err := u.InsertSource(ctx, p.organizationID, Source{
		ID: p.sourceID, ExternalID: input.Manifest.Source.ID, Name: input.Manifest.Source.Name,
		Type: input.Manifest.Source.Type, Root: input.Manifest.Source.Root,
	}); err != nil {
		return err
	}

	sourceHash := input.Manifest.Snapshot.Hash
	if sourceHash == "" {
		sourceHash = input.Manifest.Source.Hash
	}
	revision := input.Manifest.Snapshot.Revision
	if revision == "" {
		revision = input.Manifest.Source.Revision
	}
	if err := u.InsertSnapshot(ctx, p.organizationID, Snapshot{
		ID: p.snapshotID, SourceID: p.sourceID, ExternalID: input.Manifest.Snapshot.ID,
		Revision: revision, Hash: sourceHash,
		AnalysisConfigurationID: input.Manifest.Analysis.ConfigurationID,
		FactualDigest:           input.Manifest.FactualDigest, CapturedAt: p.capturedAt,
	}); err != nil {
		return err
	}

	for _, artifact := range input.Artifacts {
		if err := u.InsertArtifact(ctx, p.organizationID, Artifact{
			ID: p.artifactIDs[artifact.ID], SourceID: p.sourceID, SnapshotID: p.snapshotID,
			ExternalID: artifact.ID, Path: artifact.Path, Type: artifact.Type,
			ContentHash: artifact.Hash, ContentSize: artifact.Size,
		}); err != nil {
			return err
		}
	}
	for _, contribution := range input.Contributions {
		observedAt := contribution.ObservedAt
		if observedAt.IsZero() {
			observedAt = p.capturedAt
		}
		if err := u.InsertObservation(ctx, p.organizationID, Observation{
			ID: p.observationIDs[contribution.ID], SourceID: p.sourceID, SnapshotID: p.snapshotID,
			ArtifactID: p.artifactIDs[contribution.ArtifactID], ExternalID: contribution.ID,
			AnalyzerID: contribution.AnalyzerID, AnalyzerVersion: contribution.AnalyzerVersion,
			Method: contribution.Method, Type: contribution.Type, Locator: contribution.Locator,
			Value: contribution.Value, ObservedAt: observedAt,
		}); err != nil {
			return err
		}
	}
	for _, coverage := range input.Manifest.Coverage {
		if err := u.InsertCoverage(ctx, p.organizationID, Coverage{
			ID: p.coverageIDs[coverage.ID], SourceID: p.sourceID, SnapshotID: p.snapshotID, Value: coverage,
		}); err != nil {
			return err
		}
	}
	for _, gap := range input.Manifest.Gaps {
		if err := u.InsertGap(ctx, p.organizationID, Gap{
			ID: p.gapIDs[gap.ID], SourceID: p.sourceID, SnapshotID: p.snapshotID, Value: gap,
		}); err != nil {
			return err
		}
	}
	for _, failure := range input.Manifest.Failures {
		artifactID := ""
		if failure.ArtifactID != "" {
			artifactID = p.artifactIDs[failure.ArtifactID]
		}
		if err := u.InsertFailure(ctx, p.organizationID, Failure{
			ID: p.failureIDs[failure.ID], SourceID: p.sourceID, SnapshotID: p.snapshotID,
			ArtifactID: artifactID, ArtifactExternalID: failure.ArtifactID, Value: failure,
		}); err != nil {
			return err
		}
	}
	for _, unit := range input.Evidence {
		if err := u.InsertEvidence(ctx, p.organizationID, Evidence{
			ID: p.evidenceIDs[unit.ID], OrganizationID: p.organizationID,
			SourceID: p.sourceID, SnapshotID: p.snapshotID,
			ArtifactID:             p.artifactIDs[unit.ArtifactID],
			ObservationID:          p.observationIDs[unit.Contribution.ID],
			OrganizationExternalID: input.Manifest.Organization.ID,
			SourceExternalID:       input.Manifest.Source.ID,
			SnapshotExternalID:     input.Manifest.Snapshot.ID,
			ArtifactExternalID:     unit.ArtifactID,
			ObservationExternalID:  unit.Contribution.ID,
			Unit:                   unit,
		}); err != nil {
			return err
		}
	}
	for _, identity := range p.factualIdentities {
		if err := u.InsertFactualIdentity(ctx, p.organizationID, FactualIdentity{
			ID: identity.id, SourceID: identity.sourceID, SnapshotID: identity.snapshotID,
			IdentityKey: identity.key, FactualDigest: identity.digest,
			State: "historical", ObservedAt: p.capturedAt,
		}); err != nil {
			return err
		}
	}
	if p.factual != nil {
		if err := u.persistPreparedFactualSnapshot(ctx, *p.factual, p.capturedAt); err != nil {
			return err
		}
	}
	return nil
}

func organizationCanonicalID(input bundle.Bundle) string {
	return batchCanonicalUUID("organization", input.Manifest.Organization.ID)
}

func sourceCanonicalID(input bundle.Bundle) string {
	return batchCanonicalUUID("source", input.Manifest.Organization.ID, input.Manifest.Source.ID)
}

func snapshotCanonicalID(input bundle.Bundle) string {
	return batchCanonicalUUID("snapshot", input.Manifest.Organization.ID, input.Manifest.Source.ID, input.Manifest.Snapshot.ID)
}

// factualEvidenceIdentity deliberately contains no retained content. The
// content hash and representation metadata identify the authorized fact while
// avoiding another source-content copy in the identity digest input.
type factualEvidenceIdentity struct {
	Version           string                   `json:"version"`
	ID                string                   `json:"id"`
	OrganizationID    string                   `json:"organization_id"`
	SourceID          string                   `json:"source_id"`
	SnapshotID        string                   `json:"snapshot_id"`
	ArtifactID        string                   `json:"artifact_id"`
	Contribution      evidence.ContributionRef `json:"contribution"`
	Locator           contract.Locator         `json:"locator"`
	ContentState      evidence.ContentState    `json:"content_state"`
	ContentHash       string                   `json:"content_hash"`
	Truncated         bool                     `json:"truncated"`
	RedactionReason   string                   `json:"redaction_reason,omitempty"`
	ContentBytes      int64                    `json:"content_bytes"`
	ContentCharacters int64                    `json:"content_characters"`
	Persist           evidence.Decision        `json:"persist"`
	ExternalTransfer  evidence.Decision        `json:"external_transfer"`
	Classification    evidence.Classification  `json:"classification,omitempty"`
	Findings          []string                 `json:"findings,omitempty"`
}

func factualEvidenceForIdentity(unit evidence.EvidenceUnit) factualEvidenceIdentity {
	findings := append([]string(nil), unit.Findings...)
	sort.Strings(findings)
	findings = deduplicateBatchFindings(findings)
	return factualEvidenceIdentity{
		Version: unit.Version, ID: unit.ID, OrganizationID: unit.OrganizationID,
		SourceID: unit.SourceID, SnapshotID: unit.SnapshotID, ArtifactID: unit.ArtifactID,
		Contribution: unit.Contribution, Locator: unit.Locator, ContentState: unit.ContentState,
		ContentHash: unit.ContentHash, Truncated: unit.Truncated, RedactionReason: unit.RedactionReason,
		ContentBytes: unit.ContentBytes, ContentCharacters: unit.ContentCharacters,
		Persist: unit.Persist, ExternalTransfer: unit.ExternalTransfer,
		Classification: unit.Classification, Findings: findings,
	}
}

func deduplicateBatchFindings(findings []string) []string {
	if len(findings) < 2 {
		return findings
	}
	result := findings[:1]
	for _, finding := range findings[1:] {
		if finding != result[len(result)-1] {
			result = append(result, finding)
		}
	}
	return result
}

func batchFactDigest(kind string, value any) (string, error) {
	payload, err := json.Marshal(struct {
		Kind  string `json:"kind"`
		Value any    `json:"value"`
	}{Kind: kind, Value: value})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:]), nil
}

func batchCapturedAt(input bundle.Bundle) time.Time {
	if !input.Manifest.Snapshot.CapturedAt.IsZero() {
		return input.Manifest.Snapshot.CapturedAt
	}
	if !input.Manifest.Execution.StartedAt.IsZero() {
		return input.Manifest.Execution.StartedAt
	}
	// The legacy contract permits an absent capture time. The canonical schema
	// requires one; epoch is a deterministic sentinel rather than wall-clock
	// state, so retries cannot diverge.
	return time.Unix(0, 0).UTC()
}

func batchCanonicalUUID(kind string, values ...string) string {
	return identity.CanonicalUUID(kind, values...)
}
