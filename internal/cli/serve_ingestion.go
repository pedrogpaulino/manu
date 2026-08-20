package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pedrogpaulino/manu/internal/api"
	"github.com/pedrogpaulino/manu/internal/bundle"
	"github.com/pedrogpaulino/manu/internal/config"
	"github.com/pedrogpaulino/manu/internal/evidence"
	"github.com/pedrogpaulino/manu/internal/identity"
	"github.com/pedrogpaulino/manu/internal/ingestion"
	"github.com/pedrogpaulino/manu/internal/persistence"
	"github.com/pedrogpaulino/manu/internal/retrieval"
)

const (
	serveIngestionReadyFile = ".ready"
)

var (
	errServeIngestionStaging = errors.New("serve: ingestion staging is unavailable")
)

// serveBundleStager owns the durable payload associated with one ingestion
// job. The database stores the immutable bundle identity; this local store
// keeps the canonical transport files available to a later/restarted worker.
// A configured organization is captured at composition time because the
// unauthenticated local server has one fixed organization scope.
type serveBundleStager struct {
	root         string
	organization string
	limits       config.LimitsConfig
}

func newServeBundleStager(root, organization string, limits config.LimitsConfig) (*serveBundleStager, error) {
	if strings.TrimSpace(organization) == "" || strings.ContainsAny(organization, "\x00\r\n/\\") {
		return nil, errServeIngestionStaging
	}
	if strings.TrimSpace(root) == "" || strings.ContainsAny(root, "\x00\r\n") {
		return nil, errServeIngestionStaging
	}
	absolute, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return nil, errServeIngestionStaging
	}
	if err := os.MkdirAll(absolute, 0o700); err != nil {
		return nil, errServeIngestionStaging
	}
	info, err := os.Lstat(absolute)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, errServeIngestionStaging
	}
	return &serveBundleStager{root: absolute, organization: organization, limits: limits}, nil
}

func (s *serveBundleStager) stage(ctx context.Context, source io.Reader, contentType string, options bundle.MultipartReadOptions) (bundle.StagedMultipart, error) {
	if s == nil || strings.TrimSpace(s.root) == "" || strings.TrimSpace(s.organization) == "" {
		return bundle.StagedMultipart{}, errServeIngestionStaging
	}
	if options.OrganizationID == "" {
		options.OrganizationID = s.organization
	}
	options.Limits = serveStagingLimits(s.limits, options.Limits)
	staged, err := bundle.StageMultipart(ctx, source, contentType, s.root, options)
	if err != nil {
		return bundle.StagedMultipart{}, err
	}
	if staged.Manifest.Organization.ID != s.organization {
		_ = staged.Remove()
		return bundle.StagedMultipart{}, bundle.ErrScopeMismatch
	}
	return staged, nil
}

func serveStagingLimits(configuration config.LimitsConfig, requested bundle.Limits) bundle.Limits {
	return bundle.Limits{
		MaxBundleBytes:   serveMinimumPositive(configuration.MaxBundleBytes, requested.MaxBundleBytes),
		MaxManifestBytes: serveMinimumPositive(configuration.MaxManifestBytes, requested.MaxManifestBytes),
		MaxEvidenceBytes: serveMinimumPositive(configuration.MaxEvidenceBytes, requested.MaxEvidenceBytes),
		MaxEvidenceUnits: serveMinimumPositive(int64(configuration.MaxEvidenceUnits), requested.MaxEvidenceUnits),
	}
}

func serveMinimumPositive(configured, requested int64) int64 {
	if configured <= 0 {
		return requested
	}
	if requested <= 0 || configured < requested {
		return configured
	}
	return requested
}

func (s *serveBundleStager) publish(staged bundle.StagedMultipart) error {
	if s == nil || strings.TrimSpace(staged.Directory) == "" {
		return errServeIngestionStaging
	}
	key := serveBundleKey(s.organization, staged.Manifest)
	finalPath := filepath.Join(s.root, key)
	if err := ensureServePathComponent(key); err != nil {
		return errServeIngestionStaging
	}
	if ready, err := serveReadyDirectory(finalPath); err != nil {
		return err
	} else if ready {
		if err := verifyServeReadyDigest(finalPath, staged.Manifest.FactualDigest); err != nil {
			return err
		}
		return staged.Remove()
	}
	for _, name := range []string{bundle.ManifestFileName, bundle.ArtifactsFileName, bundle.ContributionsFileName, bundle.EvidenceFileName} {
		from := filepath.Join(staged.Directory, name)
		if _, err := os.Lstat(from); errors.Is(err, os.ErrNotExist) && name == bundle.EvidenceFileName {
			continue
		} else if err != nil {
			return errServeIngestionStaging
		}
		if err := requireServeRegularFile(from); err != nil {
			return errServeIngestionStaging
		}
	}
	for _, name := range []string{bundle.ManifestFileName, bundle.ArtifactsFileName, bundle.ContributionsFileName, bundle.EvidenceFileName} {
		path := filepath.Join(staged.Directory, name)
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			continue
		}
		file, err := os.Open(path)
		if err != nil {
			return errServeIngestionStaging
		}
		syncErr := file.Sync()
		closeErr := file.Close()
		if syncErr != nil || closeErr != nil {
			return errServeIngestionStaging
		}
	}
	marker, err := os.OpenFile(filepath.Join(staged.Directory, serveIngestionReadyFile), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return errServeIngestionStaging
	}
	if _, err := io.WriteString(marker, staged.Manifest.FactualDigest+"\n"); err != nil {
		_ = marker.Close()
		return errServeIngestionStaging
	}
	if err := marker.Sync(); err != nil {
		_ = marker.Close()
		return errServeIngestionStaging
	}
	if err := marker.Close(); err != nil {
		return errServeIngestionStaging
	}
	directory, err := os.Open(staged.Directory)
	if err != nil {
		return errServeIngestionStaging
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if syncErr != nil || closeErr != nil {
		return errServeIngestionStaging
	}
	if info, statErr := os.Lstat(finalPath); statErr == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return errServeIngestionStaging
		}
		ready, readyErr := serveReadyDirectory(finalPath)
		if readyErr != nil {
			return readyErr
		}
		if ready {
			if err := verifyServeReadyDigest(finalPath, staged.Manifest.FactualDigest); err != nil {
				return err
			}
			return staged.Remove()
		}
		if err := os.RemoveAll(finalPath); err != nil {
			return errServeIngestionStaging
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return errServeIngestionStaging
	}
	if err := os.Rename(staged.Directory, finalPath); err != nil {
		if errors.Is(err, os.ErrExist) {
			ready, readyErr := serveReadyDirectory(finalPath)
			if readyErr == nil && ready {
				if verifyErr := verifyServeReadyDigest(finalPath, staged.Manifest.FactualDigest); verifyErr != nil {
					return verifyErr
				}
				return staged.Remove()
			}
		}
		return errServeIngestionStaging
	}
	root, err := os.Open(s.root)
	if err != nil {
		return errServeIngestionStaging
	}
	syncErr = root.Sync()
	closeErr = root.Close()
	if syncErr != nil || closeErr != nil {
		return errServeIngestionStaging
	}
	return nil
}

func (s *serveBundleStager) Load(ctx context.Context, job ingestion.Job) (bundle.Bundle, error) {
	var empty bundle.Bundle
	if s == nil || strings.TrimSpace(s.root) == "" {
		return empty, errServeIngestionStaging
	}
	if err := job.Validate(); err != nil {
		return empty, ingestion.ErrInvalidJob
	}
	if job.OrganizationID != identity.CanonicalUUID("organization", s.organization) {
		return empty, bundle.ErrScopeMismatch
	}
	manifest := bundle.Manifest{Organization: bundle.Organization{ID: s.organization}}
	// The key function only needs the identity fields. Build a minimal
	// manifest value with the same fields instead of reconstructing source
	// metadata that is intentionally not duplicated in the job table.
	manifest.Source.ID = job.SourceExternalID
	manifest.Snapshot.ID = job.SnapshotExternalID
	manifest.FactualDigest = job.FactualDigest
	manifest.Analysis.ConfigurationID = job.AnalysisConfigurationID
	key := serveBundleKey(s.organization, manifest)
	path := filepath.Join(s.root, key)
	ready, err := serveReadyDirectory(path)
	if err != nil {
		return empty, err
	}
	if !ready {
		return empty, errServeIngestionStaging
	}
	if err := verifyServeReadyDigest(path, job.FactualDigest); err != nil {
		return empty, errServeIngestionStaging
	}
	input, err := bundle.ReadBundle(ctx, path, bundle.Options{
		Limits: bundle.Limits{
			MaxBundleBytes:   s.limits.MaxBundleBytes,
			MaxManifestBytes: s.limits.MaxManifestBytes,
			MaxEvidenceBytes: s.limits.MaxEvidenceBytes,
			MaxEvidenceUnits: int64(s.limits.MaxEvidenceUnits),
		},
		OrganizationID: s.organization,
	})
	if err != nil {
		return empty, err
	}
	for _, unit := range input.Evidence {
		if err := unit.ValidateWithLimits(evidence.UnitLimits{
			MaxBytes:      s.limits.MaxEvidenceTextBytes,
			MaxCharacters: s.limits.MaxEvidenceTextBytes,
		}); err != nil {
			return empty, bundle.ErrLimitExceeded
		}
	}
	return input, nil
}

func serveBundleKey(organization string, manifest bundle.Manifest) string {
	digest := sha256.Sum256([]byte(strings.Join([]string{
		organization, manifest.Source.ID, manifest.Snapshot.ID,
		manifest.FactualDigest, manifest.Analysis.ConfigurationID,
	}, "\x00")))
	return hex.EncodeToString(digest[:])
}

func ensureServePathComponent(value string) error {
	if len(value) != sha256.Size*2 || strings.Trim(value, "0123456789abcdef") != "" {
		return errServeIngestionStaging
	}
	return nil
}

func serveReadyDirectory(path string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return false, errServeIngestionStaging
	}
	marker, err := os.Lstat(filepath.Join(path, serveIngestionReadyFile))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil || marker.Mode()&os.ModeSymlink != 0 || !marker.Mode().IsRegular() {
		return false, errServeIngestionStaging
	}
	return true, nil
}

func verifyServeReadyDigest(path, digest string) error {
	file, err := os.Open(filepath.Join(path, serveIngestionReadyFile))
	if err != nil {
		return errServeIngestionStaging
	}
	marker, readErr := io.ReadAll(io.LimitReader(file, 129))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil || len(marker) > 128 || strings.TrimSpace(string(marker)) != digest {
		return errServeIngestionStaging
	}
	return nil
}

func requireServeRegularFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return errServeIngestionStaging
	}
	return nil
}

// serveIngestionService adds durable multipart creation to the existing job
// status boundary. The executor is composed separately so resource ownership
// stays in the serve runtime.
type serveIngestionService struct {
	jobs         ingestion.JobStore
	stager       *serveBundleStager
	organization string
}

func newServeIngestionService(jobs ingestion.JobStore, stager *serveBundleStager, organization string) *serveIngestionService {
	return &serveIngestionService{jobs: jobs, stager: stager, organization: organization}
}

func (s *serveIngestionService) Create(ctx context.Context, organizationID string, input bundle.Bundle) (ingestion.Job, error) {
	// The runtime service cannot accept an already materialized Bundle without
	// losing the durable staging invariant. HTTP uses CreateMultipart; callers
	// that reach this compatibility method fail closed instead of creating a
	// job whose payload cannot be recovered after restart.
	return ingestion.Job{}, ingestion.ErrInvalidJob
}

func (s *serveIngestionService) Get(ctx context.Context, organizationID, jobID string) (ingestion.Job, error) {
	if s == nil {
		return ingestion.Job{}, ingestion.ErrInvalidJob
	}
	return ingestion.NewHTTPService(s.jobs).Get(ctx, organizationID, jobID)
}

func (s *serveIngestionService) CreateMultipart(ctx context.Context, organizationID string, source io.Reader, contentType string, options bundle.MultipartReadOptions) (ingestion.Job, error) {
	if s == nil || s.jobs == nil || s.stager == nil || organizationID != s.organization {
		return ingestion.Job{}, ingestion.ErrInvalidJob
	}
	if options.OrganizationID != "" && options.OrganizationID != organizationID {
		return ingestion.Job{}, bundle.ErrScopeMismatch
	}
	options.OrganizationID = organizationID
	staged, err := s.stager.stage(ctx, source, contentType, options)
	if err != nil {
		return ingestion.Job{}, err
	}
	if err := s.stager.publish(staged); err != nil {
		_ = staged.Remove()
		return ingestion.Job{}, err
	}
	job, err := ingestion.NewJob(ingestion.NewJobInput{
		OrganizationID:          identity.CanonicalUUID("organization", organizationID),
		OrganizationExternalID:  organizationID,
		OrganizationName:        staged.Manifest.Organization.Name,
		SourceExternalID:        staged.Manifest.Source.ID,
		SnapshotExternalID:      staged.Manifest.Snapshot.ID,
		FactualDigest:           staged.Manifest.FactualDigest,
		AnalysisConfigurationID: staged.Manifest.Analysis.ConfigurationID,
	})
	if err != nil {
		return ingestion.Job{}, err
	}
	return s.jobs.Create(ctx, job)
}

func composeServeIngestion(configuration config.Config, pool *pgxpool.Pool) (api.IngestionService, *ingestion.Executor, error) {
	if pool == nil {
		return nil, nil, ErrServeRuntimeNotConfigured
	}
	stager, err := newServeBundleStager(configuration.Ingestion.StagingDirectory, configuration.Organization.ID, configuration.Limits)
	if err != nil {
		return nil, nil, ErrServeRuntimeConfiguration
	}
	repository := persistence.NewRepository(pool)
	jobs := persistence.NewJobStore(pool)
	text := retrieval.NewTextProjection(persistence.NewTextProjectionStore(pool))
	embedding := ingestion.EmbeddingOptions{Mode: ingestion.EmbeddingModeNotApplicable}
	if configuration.Embedding.Enabled {
		embedder, gatewayProfile, vectorProfile, composeErr := composeEmbedding(configuration)
		if composeErr != nil {
			return nil, nil, composeErr
		}
		embedding = ingestion.EmbeddingOptions{
			Mode:           ingestion.EmbeddingModeEnabled,
			Profile:        vectorProfile,
			GatewayProfile: gatewayProfile,
			Embedder:       embedder,
			Projector:      retrieval.NewEmbeddingProjection(persistence.NewEmbeddingProjectionStore(pool)),
			EvidenceSource: persistence.NewIngestionEmbeddingEvidenceSource(repository),
			Timeout:        configuration.Embedding.Timeout,
		}
		if configuration.Policy.ExternalTransfer != config.DecisionAllow {
			embedding.Mode = ingestion.EmbeddingModeForbidden
		}
	}
	pipeline, err := ingestion.NewPipelineWithEmbeddings(
		jobs,
		stager,
		persistence.NewIngestionCanonicalPersister(repository),
		text,
		nil,
		repository,
		embedding,
	)
	if err != nil {
		return nil, nil, ErrServeRuntimeConfiguration
	}
	options := ingestion.DefaultExecutorOptions()
	options.OrganizationID = identity.CanonicalUUID("organization", configuration.Organization.ID)
	options.Workers = configuration.Limits.MaxConcurrentIngestions
	executor, err := ingestion.NewExecutor(jobs, pipeline.Handler(), options)
	if err != nil {
		return nil, nil, ErrServeRuntimeConfiguration
	}
	return newServeIngestionService(jobs, stager, configuration.Organization.ID), executor, nil
}

var _ api.IngestionService = (*serveIngestionService)(nil)
var _ api.MultipartIngestionService = (*serveIngestionService)(nil)
var _ ingestion.BundleLoader = (*serveBundleStager)(nil)
