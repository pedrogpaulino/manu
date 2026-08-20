package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/pedrogpaulino/manu/internal/config"
	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/evidence"
	"github.com/pedrogpaulino/manu/internal/persistence"
	domainquery "github.com/pedrogpaulino/manu/internal/query"
)

const (
	// EvidencePath is the versioned evidence inspection collection.
	EvidencePath = "/api/v1/evidence"

	evidencePathPrefix = EvidencePath + "/"
)

var (
	// ErrEvidenceUnavailable identifies a handler composed without the
	// organization-scoped evidence reader.
	ErrEvidenceUnavailable = errors.New("api: evidence service unavailable")
	// ErrEvidenceNotFound identifies an evidence unit outside the fixed scope
	// or absent from persistence.
	ErrEvidenceNotFound = errors.New("api: evidence not found")
	// ErrInvalidEvidence identifies an invalid service result.
	ErrInvalidEvidence = errors.New("api: invalid evidence")
)

// EvidenceInspection and EvidenceService alias the neutral application
// boundary. The HTTP package only validates and serializes the result.
type EvidenceInspection = domainquery.EvidenceInspection
type EvidenceService = domainquery.EvidenceReader

type evidenceEndpoint struct {
	service      EvidenceService
	organization string
	limits       config.LimitsConfig
	slots        chan struct{}
}

func newEvidenceEndpoint(service EvidenceService, organization string, limits config.LimitsConfig, slots chan struct{}) *evidenceEndpoint {
	if slots == nil {
		concurrency := limits.MaxConcurrentQueries
		if concurrency < 1 {
			concurrency = 1
		}
		slots = make(chan struct{}, concurrency)
	}
	return &evidenceEndpoint{service: service, organization: organization, limits: limits, slots: slots}
}

func (e *evidenceEndpoint) ServeHTTP(w http.ResponseWriter, r *http.Request, next http.Handler) {
	if r.URL.Path != EvidencePath && !strings.HasPrefix(r.URL.Path, evidencePathPrefix) {
		next.ServeHTTP(w, r)
		return
	}
	if r.URL.Path == EvidencePath {
		w.Header().Set("Allow", http.MethodGet)
		writeProblem(w, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed", requestIDFromContext(r.Context()))
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeProblem(w, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed", requestIDFromContext(r.Context()))
		return
	}
	id, ok := apiPathID(r.URL.Path, evidencePathPrefix)
	if !ok {
		writeProblem(w, http.StatusBadRequest, "invalid_evidence_path", "evidence path is invalid", requestIDFromContext(r.Context()))
		return
	}
	e.get(w, r, id)
}

func (e *evidenceEndpoint) get(w http.ResponseWriter, r *http.Request, id string) {
	requestID := requestIDFromContext(r.Context())
	if e.service == nil {
		writeProblem(w, http.StatusServiceUnavailable, "evidence_unavailable", "evidence service is unavailable", requestID)
		return
	}
	if err := acquireQuerySlot(r.Context(), e.slots); err != nil {
		writeQueryError(w, r, err)
		return
	}
	defer releaseQuerySlot(e.slots)
	inspection, err := e.service.GetEvidence(r.Context(), e.organization, id)
	if err != nil {
		status, code, message := classifyEvidenceError(err)
		writeProblem(w, status, code, message, requestID)
		return
	}
	response, err := newEvidenceResponse(e.organization, inspection, e.limits.MaxEvidenceTextBytes)
	if err != nil {
		status, code, message := classifyEvidenceError(err)
		writeProblem(w, status, code, message, requestID)
		return
	}
	response.RequestID = requestID
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(response)
}

// EvidenceResponse is the versioned public inspection envelope. It exposes
// only persisted, policy-authorized content and bounded provenance identity.
type EvidenceResponse struct {
	Version           string                  `json:"version"`
	ID                string                  `json:"id"`
	OrganizationID    string                  `json:"organization_id"`
	SourceID          string                  `json:"source_id"`
	SnapshotID        string                  `json:"snapshot_id"`
	ArtifactID        string                  `json:"artifact_id"`
	ObservationID     string                  `json:"observation_id,omitempty"`
	Locator           contract.Locator        `json:"locator"`
	ContentState      evidence.ContentState   `json:"content_state"`
	Classification    evidence.Classification `json:"classification"`
	Persist           evidence.Decision       `json:"persist"`
	ExternalTransfer  evidence.Decision       `json:"external_transfer"`
	Content           string                  `json:"content,omitempty"`
	ContentHash       string                  `json:"content_hash"`
	ContentBytes      int64                   `json:"content_bytes"`
	ContentCharacters int64                   `json:"content_characters"`
	Truncated         bool                    `json:"truncated"`
	RequestID         string                  `json:"request_id"`
}

func newEvidenceResponse(organization string, inspection EvidenceInspection, maxContentBytes int64) (EvidenceResponse, error) {
	if !validIngestionUUID(inspection.ID) || !validIngestionUUID(inspection.SourceID) ||
		!validIngestionUUID(inspection.SnapshotID) || !validIngestionUUID(inspection.ArtifactID) {
		return EvidenceResponse{}, ErrInvalidEvidence
	}
	if organization == "" || inspection.OrganizationID != organization {
		return EvidenceResponse{}, ErrInvalidEvidence
	}
	if inspection.ObservationID != "" && !validIngestionUUID(inspection.ObservationID) {
		return EvidenceResponse{}, ErrInvalidEvidence
	}
	if err := inspection.ContentState.Validate(); err != nil {
		return EvidenceResponse{}, ErrInvalidEvidence
	}
	if err := inspection.Classification.Validate(); err != nil {
		return EvidenceResponse{}, ErrInvalidEvidence
	}
	if err := inspection.Persist.Validate(); err != nil {
		return EvidenceResponse{}, ErrInvalidEvidence
	}
	if err := inspection.ExternalTransfer.Validate(); err != nil {
		return EvidenceResponse{}, ErrInvalidEvidence
	}
	if err := inspection.Locator.Validate(); err != nil {
		return EvidenceResponse{}, ErrInvalidEvidence
	}
	if !isSHA256Digest(inspection.ContentHash) || inspection.ContentBytes < 0 || inspection.ContentCharacters < 0 {
		return EvidenceResponse{}, ErrInvalidEvidence
	}
	if maxContentBytes > 0 && int64(len(inspection.Content)) > maxContentBytes {
		return EvidenceResponse{}, ErrInvalidEvidence
	}
	if !utf8.ValidString(inspection.Content) {
		return EvidenceResponse{}, ErrInvalidEvidence
	}
	if inspection.Content != "" && (int64(len(inspection.Content)) != inspection.ContentBytes ||
		int64(utf8.RuneCountInString(inspection.Content)) != inspection.ContentCharacters) {
		return EvidenceResponse{}, ErrInvalidEvidence
	}
	if int64(len(inspection.Content)) != inspection.ContentBytes && inspection.ContentState == evidence.ContentStatePresent {
		return EvidenceResponse{}, ErrInvalidEvidence
	}
	if inspection.ContentState == evidence.ContentStatePresent &&
		evidence.ContentDigest(inspection.Content) != inspection.ContentHash {
		return EvidenceResponse{}, ErrInvalidEvidence
	}
	if inspection.ContentState == evidence.ContentStatePresent && inspection.Content == "" {
		return EvidenceResponse{}, ErrInvalidEvidence
	}
	unsafeClassification := inspection.Classification == evidence.ClassificationBinary ||
		inspection.Classification == evidence.ClassificationInvalid ||
		inspection.Classification == evidence.ClassificationProhibited
	if inspection.ContentState == evidence.ContentStatePresent &&
		inspection.Classification != evidence.ClassificationUnknown &&
		inspection.Classification != evidence.ClassificationSafeText {
		return EvidenceResponse{}, ErrInvalidEvidence
	}
	if inspection.ContentState == evidence.ContentStatePresent && inspection.Persist == evidence.DecisionRedact {
		return EvidenceResponse{}, ErrInvalidEvidence
	}
	if inspection.ExternalTransfer == evidence.DecisionAllow &&
		inspection.Classification != evidence.ClassificationSafeText {
		return EvidenceResponse{}, ErrInvalidEvidence
	}
	if inspection.ContentState == evidence.ContentStateOmitted &&
		(inspection.Content != "" || inspection.ContentBytes != 0 || inspection.ContentCharacters != 0) {
		return EvidenceResponse{}, ErrInvalidEvidence
	}
	if inspection.Persist == evidence.DecisionDeny && inspection.ContentState != evidence.ContentStateOmitted {
		return EvidenceResponse{}, ErrInvalidEvidence
	}
	if inspection.Persist == evidence.DecisionDeny &&
		(inspection.Content != "" || inspection.ContentBytes != 0 || inspection.ContentCharacters != 0) {
		return EvidenceResponse{}, ErrInvalidEvidence
	}
	if unsafeClassification && (inspection.ContentState == evidence.ContentStatePresent || inspection.Content != "") {
		return EvidenceResponse{}, ErrInvalidEvidence
	}
	return EvidenceResponse{
		Version:           Version,
		ID:                inspection.ID,
		OrganizationID:    organization,
		SourceID:          inspection.SourceID,
		SnapshotID:        inspection.SnapshotID,
		ArtifactID:        inspection.ArtifactID,
		ObservationID:     inspection.ObservationID,
		Locator:           inspection.Locator,
		ContentState:      inspection.ContentState,
		Classification:    inspection.Classification,
		Persist:           inspection.Persist,
		ExternalTransfer:  inspection.ExternalTransfer,
		Content:           inspection.Content,
		ContentHash:       inspection.ContentHash,
		ContentBytes:      inspection.ContentBytes,
		ContentCharacters: inspection.ContentCharacters,
		Truncated:         inspection.Truncated,
	}, nil
}

func classifyEvidenceError(err error) (int, string, string) {
	switch {
	case err == nil:
		return http.StatusInternalServerError, "evidence_failed", "evidence inspection failed"
	case errors.Is(err, context.Canceled):
		return http.StatusRequestTimeout, "request_canceled", "request canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return http.StatusRequestTimeout, "request_timeout", "request timed out"
	case errors.Is(err, ErrQueryBusy):
		return http.StatusServiceUnavailable, "query_busy", "query service is busy"
	case errors.Is(err, ErrEvidenceNotFound), errors.Is(err, persistence.ErrNotFound):
		return http.StatusNotFound, "evidence_not_found", "evidence was not found"
	case errors.Is(err, ErrEvidenceUnavailable), errors.Is(err, persistence.ErrDatabase):
		return http.StatusServiceUnavailable, "evidence_unavailable", "evidence service is unavailable"
	case errors.Is(err, ErrInvalidEvidence), errors.Is(err, persistence.ErrInvalidInput):
		return http.StatusInternalServerError, "evidence_invalid", "stored evidence is invalid"
	default:
		return http.StatusInternalServerError, "evidence_failed", "evidence inspection failed"
	}
}
