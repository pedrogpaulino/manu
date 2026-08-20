package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/pedrogpaulino/manu/internal/bundle"
	"github.com/pedrogpaulino/manu/internal/config"
	"github.com/pedrogpaulino/manu/internal/evidence"
	"github.com/pedrogpaulino/manu/internal/ingestion"
)

const (
	// IngestionsPath is the collection endpoint for asynchronous bundle jobs.
	IngestionsPath = "/api/v1/ingestions"

	// ingestionPathPrefix is kept separate from IngestionsPath so an unrelated
	// route such as /api/v1/ingestions-other is never intercepted.
	ingestionPathPrefix = IngestionsPath + "/"
)

var (
	// ErrIngestionUnavailable identifies a handler composed without the job
	// application port. It is deliberately distinct from a route-not-found
	// response so the default handler cannot imply that ingestion is active.
	ErrIngestionUnavailable = errors.New("api: ingestion service unavailable")
	// ErrInvalidIngestionPath identifies a malformed collection or item path.
	ErrInvalidIngestionPath = errors.New("api: invalid ingestion path")
	// ErrUnsupportedIngestionMedia identifies a body that is not multipart.
	ErrUnsupportedIngestionMedia = errors.New("api: unsupported ingestion media")
)

// IngestionService is the application port consumed by the HTTP boundary.
// Implementations receive the fixed organization explicitly and must keep
// every read and write scoped to it.
type IngestionService interface {
	Create(context.Context, string, bundle.Bundle) (ingestion.Job, error)
	Get(context.Context, string, string) (ingestion.Job, error)
}

// MultipartIngestionService is an optional streaming extension of
// IngestionService. Implementations use it when the request body must be
// staged durably before the asynchronous job is acknowledged. The fallback
// remains available for local/test services that already receive a validated
// in-memory bundle.
type MultipartIngestionService interface {
	CreateMultipart(context.Context, string, io.Reader, string, bundle.MultipartReadOptions) (ingestion.Job, error)
}

// IngestionCounts is the stable public counter subset of an ingestion job.
// Lease and worker coordination fields intentionally do not cross this API.
type IngestionCounts struct {
	ArtifactCount    int64 `json:"artifact_count"`
	ObservationCount int64 `json:"observation_count"`
	EvidenceCount    int64 `json:"evidence_count"`
	FailureCount     int64 `json:"failure_count"`
}

// IngestionResponse is the versioned status envelope returned by POST and
// GET. DiagnosticCode is controlled vocabulary only; raw error messages and
// lease ownership never appear in this representation.
type IngestionResponse struct {
	Version        string             `json:"version"`
	ID             string             `json:"id"`
	OrganizationID string             `json:"organization_id"`
	State          ingestion.JobState `json:"state"`
	Stage          ingestion.JobStage `json:"stage"`
	AttemptCount   int                `json:"attempt_count"`
	Counts         IngestionCounts    `json:"counts"`
	DiagnosticCode string             `json:"diagnostic_code,omitempty"`
	RequestID      string             `json:"request_id"`
}

// IngestionEnvelope is a descriptive alias for callers that name all API
// responses as envelopes.
type IngestionEnvelope = IngestionResponse

type ingestionEndpoint struct {
	service      IngestionService
	organization string
	limits       config.LimitsConfig
	slots        chan struct{}
}

func newIngestionEndpoint(
	service IngestionService,
	organization string,
	limits config.LimitsConfig,
) *ingestionEndpoint {
	concurrency := limits.MaxConcurrentIngestions
	if concurrency < 1 {
		concurrency = 1
	}
	return &ingestionEndpoint{
		service:      service,
		organization: organization,
		limits:       limits,
		slots:        make(chan struct{}, concurrency),
	}
}

func (e *ingestionEndpoint) ServeHTTP(w http.ResponseWriter, r *http.Request, next http.Handler) {
	if r.URL.Path != IngestionsPath && !strings.HasPrefix(r.URL.Path, ingestionPathPrefix) {
		next.ServeHTTP(w, r)
		return
	}

	if r.URL.Path == IngestionsPath {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeProblem(w, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed", requestIDFromContext(r.Context()))
			return
		}
		e.create(w, r)
		return
	}

	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeProblem(w, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed", requestIDFromContext(r.Context()))
		return
	}
	id, ok := ingestionPathID(r.URL.Path)
	if !ok {
		writeProblem(w, http.StatusBadRequest, "invalid_ingestion_path", "ingestion path is invalid", requestIDFromContext(r.Context()))
		return
	}
	e.get(w, r, id)
}

func (e *ingestionEndpoint) create(w http.ResponseWriter, r *http.Request) {
	requestID := requestIDFromContext(r.Context())
	if e.service == nil {
		writeProblem(w, http.StatusServiceUnavailable, "ingestion_unavailable", "ingestion service is unavailable", requestID)
		return
	}
	if err := acquireIngestionSlot(r.Context(), e.slots); err != nil {
		writeIngestionError(w, r, err)
		return
	}
	defer releaseIngestionSlot(e.slots)

	var job ingestion.Job
	var err error
	if streaming, ok := e.service.(MultipartIngestionService); ok {
		job, err = streaming.CreateMultipart(
			r.Context(), e.organization, r.Body, r.Header.Get("Content-Type"),
			multipartReadOptions(e.organization, e.limits),
		)
	} else {
		var input bundle.Bundle
		input, err = readIngestionBundle(r.Context(), r, e.organization, e.limits)
		if err == nil {
			job, err = e.service.Create(r.Context(), e.organization, input)
		}
	}
	if err != nil {
		writeIngestionError(w, r, err)
		return
	}
	writeIngestionResponse(w, http.StatusAccepted, e.organization, job, requestID)
}

func (e *ingestionEndpoint) get(w http.ResponseWriter, r *http.Request, id string) {
	requestID := requestIDFromContext(r.Context())
	if e.service == nil {
		writeProblem(w, http.StatusServiceUnavailable, "ingestion_unavailable", "ingestion service is unavailable", requestID)
		return
	}
	job, err := e.service.Get(r.Context(), e.organization, id)
	if err != nil {
		writeIngestionError(w, r, err)
		return
	}
	writeIngestionResponse(w, http.StatusOK, e.organization, job, requestID)
}

func readIngestionBundle(
	ctx context.Context,
	request *http.Request,
	organization string,
	limits config.LimitsConfig,
) (bundle.Bundle, error) {
	var empty bundle.Bundle
	if request == nil || request.Body == nil {
		return empty, bundle.ErrMultipartInvalid
	}
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	contentType := strings.TrimSpace(request.Header.Get("Content-Type"))
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil || mediaType != "multipart/form-data" || strings.TrimSpace(params["boundary"]) == "" {
		return empty, ErrUnsupportedIngestionMedia
	}

	input, _, err := bundle.ReadMultipart(ctx, request.Body, contentType, multipartReadOptions(organization, limits))
	if err != nil {
		return empty, err
	}
	for _, unit := range input.Evidence {
		if err := unit.ValidateWithLimits(evidence.UnitLimits{
			MaxBytes:      limits.MaxEvidenceTextBytes,
			MaxCharacters: limits.MaxEvidenceTextBytes,
		}); err != nil {
			return empty, err
		}
	}
	return input, nil
}

func multipartReadOptions(organization string, limits config.LimitsConfig) bundle.MultipartReadOptions {
	return bundle.MultipartReadOptions{
		Limits: bundle.Limits{
			MaxBundleBytes:   limits.MaxBundleBytes,
			MaxManifestBytes: limits.MaxManifestBytes,
			MaxEvidenceBytes: limits.MaxEvidenceBytes,
			MaxEvidenceUnits: int64(limits.MaxEvidenceUnits),
		},
		OrganizationID: organization,
	}
}

func ingestionPathID(path string) (string, bool) {
	if !strings.HasPrefix(path, ingestionPathPrefix) {
		return "", false
	}
	id := strings.TrimPrefix(path, ingestionPathPrefix)
	if id == "" || strings.ContainsAny(id, "/\\\x00\r\n") || id == "." || id == ".." {
		return "", false
	}
	if strings.TrimSpace(id) != id || len(id) > 256 {
		return "", false
	}
	return id, validIngestionUUID(id)
}

func validIngestionUUID(value string) bool {
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

func acquireIngestionSlot(ctx context.Context, slots chan struct{}) error {
	if slots == nil {
		return nil
	}
	select {
	case slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	default:
		return ErrIngestionBusy
	}
}

func releaseIngestionSlot(slots chan struct{}) {
	if slots != nil {
		<-slots
	}
}

var ErrIngestionBusy = errors.New("api: ingestion concurrency limit reached")

func writeIngestionResponse(
	w http.ResponseWriter,
	status int,
	organization string,
	job ingestion.Job,
	requestID string,
) {
	response := IngestionResponse{
		Version:        Version,
		ID:             job.ID,
		OrganizationID: organization,
		State:          job.State,
		Stage:          job.Stage,
		AttemptCount:   job.AttemptCount,
		Counts: IngestionCounts{
			ArtifactCount:    job.ArtifactCount,
			ObservationCount: job.ObservationCount,
			EvidenceCount:    job.EvidenceCount,
			FailureCount:     job.FailureCount,
		},
		DiagnosticCode: safeDiagnosticCode(job.DiagnosticCode),
		RequestID:      requestID,
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(response)
}

func safeDiagnosticCode(value string) string {
	if value == "" || len(value) > 64 {
		return ""
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') ||
			(character >= '0' && character <= '9') || character == '_' || character == '-' || character == '.' {
			continue
		}
		return ""
	}
	return value
}

func writeIngestionError(w http.ResponseWriter, r *http.Request, err error) {
	status, code, message := classifyIngestionError(err)
	writeProblem(w, status, code, message, requestIDFromContext(r.Context()))
}

func classifyIngestionError(err error) (int, string, string) {
	var maxBytesError *http.MaxBytesError
	if errors.As(err, &maxBytesError) {
		return http.StatusRequestEntityTooLarge, "ingestion_limit_exceeded", "ingestion exceeds configured limits"
	}
	switch {
	case err == nil:
		return http.StatusInternalServerError, "ingestion_failed", "ingestion failed"
	case errors.Is(err, context.Canceled):
		return http.StatusRequestTimeout, "request_canceled", "request canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return http.StatusRequestTimeout, "request_timeout", "request timed out"
	case errors.Is(err, ErrIngestionBusy):
		return http.StatusServiceUnavailable, "ingestion_busy", "ingestion service is busy"
	case errors.Is(err, ErrUnsupportedIngestionMedia):
		return http.StatusUnsupportedMediaType, "unsupported_media_type", "content type is not supported"
	case errors.Is(err, ingestion.ErrJobNotFound):
		return http.StatusNotFound, "ingestion_not_found", "ingestion was not found"
	case errors.Is(err, ingestion.ErrJobConflict):
		return http.StatusConflict, "ingestion_conflict", "ingestion conflicts with existing work"
	case errors.Is(err, ingestion.ErrJobStore), errors.Is(err, ErrIngestionUnavailable):
		return http.StatusServiceUnavailable, "ingestion_unavailable", "ingestion service is unavailable"
	case errors.Is(err, bundle.ErrLimitExceeded), errors.Is(err, evidence.ErrLimitExceeded):
		return http.StatusRequestEntityTooLarge, "ingestion_limit_exceeded", "ingestion exceeds configured limits"
	case errors.Is(err, bundle.ErrMultipartInvalid), errors.Is(err, bundle.ErrMultipartPart),
		errors.Is(err, bundle.ErrMultipartTraversal), errors.Is(err, bundle.ErrUnsupportedVersion),
		errors.Is(err, bundle.ErrInvalid), errors.Is(err, bundle.ErrDigestMismatch),
		errors.Is(err, bundle.ErrCountMismatch), errors.Is(err, bundle.ErrInvalidReference),
		errors.Is(err, bundle.ErrScopeMismatch),
		errors.Is(err, evidence.ErrInvalid), errors.Is(err, evidence.ErrUnsupportedVersion),
		errors.Is(err, evidence.ErrInvalidContent), errors.Is(err, evidence.ErrInvalidDigest),
		errors.Is(err, evidence.ErrUnsafeLocator):
		return http.StatusBadRequest, "invalid_ingestion", "ingestion payload is invalid"
	case errors.Is(err, ingestion.ErrInvalidJob), errors.Is(err, ErrInvalidIngestionPath):
		return http.StatusBadRequest, "invalid_ingestion", "ingestion request is invalid"
	case errors.Is(err, io.ErrUnexpectedEOF):
		return http.StatusBadRequest, "invalid_ingestion", "ingestion payload is invalid"
	default:
		return http.StatusInternalServerError, "ingestion_failed", "ingestion failed"
	}
}
