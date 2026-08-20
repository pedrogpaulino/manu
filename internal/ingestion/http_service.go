package ingestion

import (
	"context"
	"strings"

	"github.com/pedrogpaulino/manu/internal/bundle"
	"github.com/pedrogpaulino/manu/internal/identity"
)

// HTTPService is the small application boundary used by the HTTP ingestion
// endpoints. It validates the already streamed Analysis Bundle and delegates
// durable identity creation and inspection to the injected JobStore. It does
// not start processing or open a database connection.
type HTTPService struct {
	store JobStore
}

// NewHTTPService composes the HTTP application boundary with a durable job
// store. A nil store is retained as unavailable and is reported safely when a
// request arrives.
func NewHTTPService(store JobStore) *HTTPService {
	return &HTTPService{store: store}
}

// Create records one pending job for the factual bundle identity in the
// configured organization. JobStore.Create supplies the atomic idempotency
// boundary for repeated factual submissions.
func (s *HTTPService) Create(ctx context.Context, organizationID string, input bundle.Bundle) (Job, error) {
	if err := validateHTTPServiceContext(ctx); err != nil {
		return Job{}, err
	}
	if s == nil || s.store == nil {
		return Job{}, ErrJobStore
	}
	if err := validateHTTPServiceIdentifier(organizationID); err != nil {
		return Job{}, err
	}
	if input.Manifest.Organization.ID != organizationID {
		return Job{}, ErrInvalidJob
	}
	if err := input.Validate(); err != nil {
		return Job{}, ErrInvalidJob
	}

	job, err := NewJob(NewJobInput{
		OrganizationID:          identity.CanonicalUUID("organization", organizationID),
		OrganizationExternalID:  organizationID,
		OrganizationName:        input.Manifest.Organization.Name,
		SourceExternalID:        input.Manifest.Source.ID,
		SnapshotExternalID:      input.Manifest.Snapshot.ID,
		FactualDigest:           input.Manifest.FactualDigest,
		AnalysisConfigurationID: input.Manifest.Analysis.ConfigurationID,
	})
	if err != nil {
		return Job{}, err
	}
	return s.store.Create(ctx, job)
}

// Get returns one job only when it belongs to the configured organization.
func (s *HTTPService) Get(ctx context.Context, organizationID, jobID string) (Job, error) {
	if err := validateHTTPServiceContext(ctx); err != nil {
		return Job{}, err
	}
	if s == nil || s.store == nil {
		return Job{}, ErrJobStore
	}
	if err := validateHTTPServiceIdentifier(organizationID); err != nil {
		return Job{}, err
	}
	if err := validateHTTPServiceIdentifier(jobID); err != nil {
		return Job{}, err
	}
	if !isHTTPServiceUUID(jobID) {
		return Job{}, ErrInvalidJob
	}
	return s.store.Get(ctx, identity.CanonicalUUID("organization", organizationID), jobID)
}

func validateHTTPServiceContext(ctx context.Context) error {
	if ctx == nil {
		return ErrInvalidJob
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func validateHTTPServiceIdentifier(value string) error {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || len(trimmed) > maxJobIdentifierLength || trimmed != value {
		return ErrInvalidJob
	}
	if strings.ContainsAny(value, "/\\\x00\r\n") {
		return ErrInvalidJob
	}
	return nil
}

func isHTTPServiceUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for index, character := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			if character != '-' {
				return false
			}
			continue
		}
		if (character < '0' || character > '9') &&
			(character < 'a' || character > 'f') &&
			(character < 'A' || character > 'F') {
			return false
		}
	}
	return true
}

var _ interface {
	Create(context.Context, string, bundle.Bundle) (Job, error)
	Get(context.Context, string, string) (Job, error)
} = (*HTTPService)(nil)
