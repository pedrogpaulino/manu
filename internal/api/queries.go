package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/pedrogpaulino/manu/internal/config"
	"github.com/pedrogpaulino/manu/internal/persistence"
	domainquery "github.com/pedrogpaulino/manu/internal/query"
)

const (
	// QueriesPath is the versioned query execution collection.
	QueriesPath = "/api/v1/queries"

	queryPathPrefix = QueriesPath + "/"
)

var (
	// ErrQueryUnavailable identifies a handler composed without the query
	// application port. Returning it as 503 makes the missing pipeline
	// explicit instead of pretending that a response was generated.
	ErrQueryUnavailable = errors.New("api: query service unavailable")
	// ErrInvalidQuery identifies a malformed query request or execution.
	ErrInvalidQuery = errors.New("api: invalid query")
	// ErrQueryNotFound identifies an organization-scoped query that is absent.
	ErrQueryNotFound = errors.New("api: query not found")
	// ErrQueryConflict identifies an immutable query identity conflict.
	ErrQueryConflict = errors.New("api: query conflict")
	// ErrQueryBusy identifies exhaustion of the local query concurrency bound.
	ErrQueryBusy = errors.New("api: query concurrency limit reached")
	// ErrUnsupportedQueryMedia identifies a body that is not JSON.
	ErrUnsupportedQueryMedia = errors.New("api: unsupported query media")
	// ErrQueryNotTerminal identifies a query service result that did not
	// complete the synchronous POST contract.
	ErrQueryNotTerminal = errors.New("api: query did not complete")
)

// QueryState aliases the neutral query application state so the HTTP package
// does not own or duplicate persistence lifecycle semantics.
type QueryState = domainquery.ExecutionState

const (
	QueryStatePending   = domainquery.ExecutionStatePending
	QueryStateRunning   = domainquery.ExecutionStateRunning
	QueryStateCompleted = domainquery.ExecutionStateCompleted
	QueryStatePartial   = domainquery.ExecutionStatePartial
	QueryStateFailed    = domainquery.ExecutionStateFailed
	QueryStateAbstained = domainquery.ExecutionStateAbstained
)

// QueryRequest is the JSON body accepted by POST /api/v1/queries. The
// organization is deliberately absent: the handler always uses the one
// organization selected by configuration.
type QueryRequest struct {
	Question   string                            `json:"question"`
	Kind       domainquery.KnowledgeQuestionKind `json:"kind"`
	SourceID   string                            `json:"source_id,omitempty"`
	SnapshotID string                            `json:"snapshot_id,omitempty"`
}

// QueryInput and QueryExecution are aliases to neutral application types.
// The HTTP layer validates transport data but does not define the persisted
// execution model.
type QueryInput = domainquery.ExecutionInput
type QueryExecution = domainquery.Execution
type QueryService = domainquery.ExecutionService

type queryEndpoint struct {
	service      QueryService
	organization string
	limits       config.LimitsConfig
	slots        chan struct{}
}

func newQueryEndpoint(service QueryService, organization string, limits config.LimitsConfig) *queryEndpoint {
	concurrency := limits.MaxConcurrentQueries
	if concurrency < 1 {
		concurrency = 1
	}
	return &queryEndpoint{
		service:      service,
		organization: organization,
		limits:       limits,
		slots:        make(chan struct{}, concurrency),
	}
}

func (e *queryEndpoint) ServeHTTP(w http.ResponseWriter, r *http.Request, next http.Handler) {
	if r.URL.Path != QueriesPath && !strings.HasPrefix(r.URL.Path, queryPathPrefix) {
		next.ServeHTTP(w, r)
		return
	}

	if r.URL.Path == QueriesPath {
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
	id, ok := apiPathID(r.URL.Path, queryPathPrefix)
	if !ok {
		writeProblem(w, http.StatusBadRequest, "invalid_query_path", "query path is invalid", requestIDFromContext(r.Context()))
		return
	}
	e.get(w, r, id)
}

func (e *queryEndpoint) create(w http.ResponseWriter, r *http.Request) {
	requestID := requestIDFromContext(r.Context())
	if e.service == nil {
		writeProblem(w, http.StatusServiceUnavailable, "query_unavailable", "query service is unavailable", requestID)
		return
	}
	if err := acquireQuerySlot(r.Context(), e.slots); err != nil {
		writeQueryError(w, r, err)
		return
	}
	defer releaseQuerySlot(e.slots)

	input, err := readQueryInput(r, e.limits)
	if err != nil {
		writeQueryError(w, r, err)
		return
	}
	execution, err := e.service.Create(r.Context(), e.organization, input)
	if err != nil {
		writeQueryError(w, r, err)
		return
	}
	if err := validateQueryExecution(execution, input.QuestionDigest); err != nil {
		writeQueryError(w, r, err)
		return
	}
	if execution.OrganizationID != e.organization {
		writeQueryError(w, r, ErrInvalidQuery)
		return
	}
	if !execution.State.Terminal() {
		writeQueryError(w, r, ErrQueryNotTerminal)
		return
	}
	writeQueryResponse(w, queryCreateStatus(execution.State), e.organization, execution, requestID)
}

func (e *queryEndpoint) get(w http.ResponseWriter, r *http.Request, id string) {
	requestID := requestIDFromContext(r.Context())
	if e.service == nil {
		writeProblem(w, http.StatusServiceUnavailable, "query_unavailable", "query service is unavailable", requestID)
		return
	}
	if err := acquireQuerySlot(r.Context(), e.slots); err != nil {
		writeQueryError(w, r, err)
		return
	}
	defer releaseQuerySlot(e.slots)

	execution, err := e.service.Get(r.Context(), e.organization, id)
	if err != nil {
		writeQueryError(w, r, err)
		return
	}
	if err := validateQueryExecution(execution, ""); err != nil {
		writeQueryError(w, r, err)
		return
	}
	if execution.OrganizationID != e.organization {
		writeQueryError(w, r, ErrInvalidQuery)
		return
	}
	writeQueryResponse(w, http.StatusOK, e.organization, execution, requestID)
}

func readQueryInput(r *http.Request, limits config.LimitsConfig) (QueryInput, error) {
	if r == nil || r.Body == nil {
		return QueryInput{}, ErrInvalidQuery
	}
	contentType := strings.TrimSpace(r.Header.Get("Content-Type"))
	mediaType, _, mediaErr := mime.ParseMediaType(contentType)
	if mediaErr != nil || !strings.EqualFold(mediaType, "application/json") {
		return QueryInput{}, ErrUnsupportedQueryMedia
	}
	maxBytes := limits.MaxQueryBytes
	if maxBytes <= 0 {
		return QueryInput{}, ErrInvalidQuery
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBytes+1))
	if err != nil {
		return QueryInput{}, err
	}
	if int64(len(body)) > maxBytes {
		return QueryInput{}, ErrQueryLimit
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var request QueryRequest
	if err := decoder.Decode(&request); err != nil {
		return QueryInput{}, ErrInvalidQuery
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return QueryInput{}, ErrInvalidQuery
	}
	if err := validateQueryRequest(request, maxBytes); err != nil {
		return QueryInput{}, err
	}
	digest := sha256.Sum256([]byte(request.Question))
	return QueryInput{
		Question:       request.Question,
		QuestionKind:   request.Kind,
		SourceID:       request.SourceID,
		SnapshotID:     request.SnapshotID,
		QuestionDigest: hex.EncodeToString(digest[:]),
	}, nil
}

func validateQueryRequest(request QueryRequest, maxBytes int64) error {
	if strings.TrimSpace(request.Question) == "" || int64(len(request.Question)) > maxBytes {
		return ErrInvalidQuery
	}
	switch request.Kind {
	case domainquery.KnowledgeQuestionInventory,
		domainquery.KnowledgeQuestionPossibleFlow,
		domainquery.KnowledgeQuestionObservedExecution,
		domainquery.KnowledgeQuestionBusinessIntent:
	default:
		return ErrInvalidQuery
	}
	if request.SourceID != "" && !validIngestionUUID(request.SourceID) {
		return ErrInvalidQuery
	}
	if request.SnapshotID != "" {
		if request.SourceID == "" || !validIngestionUUID(request.SnapshotID) {
			return ErrInvalidQuery
		}
	}
	if strings.ContainsAny(request.Question, "\x00\r\n") {
		return ErrInvalidQuery
	}
	return nil
}

func validateQueryExecution(execution QueryExecution, expectedQuestionDigest string) error {
	if !validIngestionUUID(execution.ID) || !execution.State.Valid() || !isSHA256Digest(execution.QuestionDigest) {
		return ErrInvalidQuery
	}
	if execution.CreatedAt.IsZero() {
		return ErrInvalidQuery
	}
	if expectedQuestionDigest != "" && execution.QuestionDigest != expectedQuestionDigest {
		return ErrInvalidQuery
	}
	if execution.SourceID != "" && !validIngestionUUID(execution.SourceID) {
		return ErrInvalidQuery
	}
	if execution.SnapshotID != "" && (!validIngestionUUID(execution.SnapshotID) || execution.SourceID == "") {
		return ErrInvalidQuery
	}
	if execution.PackageDigest != "" && !isSHA256Digest(execution.PackageDigest) {
		return ErrInvalidQuery
	}
	responsePresent := len(execution.Response) > 0
	packagePresent := execution.PackageDigest != ""
	if responsePresent {
		if !json.Valid(execution.Response) {
			return ErrInvalidQuery
		}
		if !execution.State.Terminal() {
			return ErrInvalidQuery
		}
	}
	if responsePresent != packagePresent {
		return ErrInvalidQuery
	}
	if execution.DiagnosticCode != "" && safeDiagnosticCode(execution.DiagnosticCode) == "" {
		return ErrInvalidQuery
	}
	if execution.StartedAt != nil && execution.StartedAt.Before(execution.CreatedAt) {
		return ErrInvalidQuery
	}
	if execution.FinishedAt != nil && execution.FinishedAt.Before(execution.CreatedAt) {
		return ErrInvalidQuery
	}
	if execution.StartedAt != nil && execution.FinishedAt != nil && execution.FinishedAt.Before(*execution.StartedAt) {
		return ErrInvalidQuery
	}
	hasDiagnostic := execution.DiagnosticCode != ""
	switch execution.State {
	case QueryStatePending:
		if execution.StartedAt != nil || execution.FinishedAt != nil || responsePresent || packagePresent || hasDiagnostic {
			return ErrInvalidQuery
		}
	case QueryStateRunning:
		if execution.StartedAt == nil || execution.FinishedAt != nil || responsePresent || packagePresent || hasDiagnostic {
			return ErrInvalidQuery
		}
	case QueryStateCompleted:
		if execution.FinishedAt == nil || !responsePresent || hasDiagnostic {
			return ErrInvalidQuery
		}
	case QueryStatePartial:
		if execution.FinishedAt == nil || (!responsePresent && !hasDiagnostic) {
			return ErrInvalidQuery
		}
	case QueryStateFailed:
		if execution.FinishedAt == nil || !hasDiagnostic || responsePresent || packagePresent {
			return ErrInvalidQuery
		}
	case QueryStateAbstained:
		if execution.FinishedAt == nil || responsePresent != packagePresent {
			return ErrInvalidQuery
		}
		if !responsePresent {
			// The concrete 5.3 adapter has no response/package columns yet;
			// its explicit diagnostic abstention must not be confused with a
			// fabricated final answer.
			if execution.DiagnosticCode != "pipeline_not_configured" {
				return ErrInvalidQuery
			}
			break
		}
		var response domainquery.Response
		if err := json.Unmarshal(execution.Response, &response); err != nil ||
			response.Generation.Termination != domainquery.TerminationAbstained || response.Validate() != nil {
			return ErrInvalidQuery
		}
	}
	return nil
}

// QueryResponse is the public status envelope. It contains digests and a
// bounded generated result when the complete pipeline supplied one, never a
// raw question or provider diagnostic.
type QueryResponse struct {
	Version        string          `json:"version"`
	ID             string          `json:"id"`
	OrganizationID string          `json:"organization_id"`
	SourceID       string          `json:"source_id,omitempty"`
	SnapshotID     string          `json:"snapshot_id,omitempty"`
	State          QueryState      `json:"state"`
	QuestionDigest string          `json:"question_digest"`
	PackageDigest  string          `json:"package_digest,omitempty"`
	Result         json.RawMessage `json:"result,omitempty"`
	DiagnosticCode string          `json:"diagnostic_code,omitempty"`
	CreatedAt      time.Time       `json:"created_at,omitempty"`
	StartedAt      *time.Time      `json:"started_at,omitempty"`
	FinishedAt     *time.Time      `json:"finished_at,omitempty"`
	RequestID      string          `json:"request_id"`
}

func writeQueryResponse(w http.ResponseWriter, status int, organization string, execution QueryExecution, requestID string) {
	response := QueryResponse{
		Version:        Version,
		ID:             execution.ID,
		OrganizationID: organization,
		SourceID:       execution.SourceID,
		SnapshotID:     execution.SnapshotID,
		State:          execution.State,
		QuestionDigest: execution.QuestionDigest,
		PackageDigest:  execution.PackageDigest,
		Result:         append(json.RawMessage(nil), execution.Response...),
		DiagnosticCode: safeDiagnosticCode(execution.DiagnosticCode),
		CreatedAt:      execution.CreatedAt,
		StartedAt:      execution.StartedAt,
		FinishedAt:     execution.FinishedAt,
		RequestID:      requestID,
	}
	w.Header().Set("Content-Type", "application/json")
	if status == http.StatusAccepted {
		w.Header().Set("Location", QueriesPath+"/"+execution.ID)
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(response)
}

func queryCreateStatus(state QueryState) int {
	switch state {
	case QueryStateFailed:
		return http.StatusInternalServerError
	default:
		return http.StatusOK
	}
}

func acquireQuerySlot(ctx context.Context, slots chan struct{}) error {
	if slots == nil {
		return nil
	}
	select {
	case slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	default:
		return ErrQueryBusy
	}
}

func releaseQuerySlot(slots chan struct{}) {
	if slots != nil {
		<-slots
	}
}

var ErrQueryLimit = errors.New("api: query exceeds configured limits")

func writeQueryError(w http.ResponseWriter, r *http.Request, err error) {
	status, code, message := classifyQueryError(err)
	writeProblem(w, status, code, message, requestIDFromContext(r.Context()))
}

func classifyQueryError(err error) (int, string, string) {
	var maxBytesError *http.MaxBytesError
	if errors.As(err, &maxBytesError) {
		return http.StatusRequestEntityTooLarge, "query_limit_exceeded", "query exceeds configured limits"
	}
	switch {
	case err == nil:
		return http.StatusInternalServerError, "query_failed", "query failed"
	case errors.Is(err, context.Canceled):
		return http.StatusRequestTimeout, "request_canceled", "request canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return http.StatusRequestTimeout, "request_timeout", "request timed out"
	case errors.Is(err, ErrQueryBusy):
		return http.StatusServiceUnavailable, "query_busy", "query service is busy"
	case errors.Is(err, ErrUnsupportedQueryMedia):
		return http.StatusUnsupportedMediaType, "unsupported_media_type", "content type is not supported"
	case errors.Is(err, ErrQueryLimit):
		return http.StatusRequestEntityTooLarge, "query_limit_exceeded", "query exceeds configured limits"
	case errors.Is(err, ErrQueryNotTerminal):
		return http.StatusServiceUnavailable, "query_not_complete", "query did not complete"
	case errors.Is(err, ErrInvalidQuery), errors.Is(err, persistence.ErrInvalidInput):
		return http.StatusBadRequest, "invalid_query", "query request is invalid"
	case errors.Is(err, ErrQueryNotFound), errors.Is(err, persistence.ErrNotFound):
		return http.StatusNotFound, "query_not_found", "query was not found"
	case errors.Is(err, ErrQueryConflict), errors.Is(err, persistence.ErrConflict):
		return http.StatusConflict, "query_conflict", "query conflicts with existing execution"
	case errors.Is(err, ErrQueryUnavailable), errors.Is(err, persistence.ErrDatabase):
		return http.StatusServiceUnavailable, "query_unavailable", "query service is unavailable"
	default:
		return http.StatusInternalServerError, "query_failed", "query failed"
	}
}

func apiPathID(path, prefix string) (string, bool) {
	if !strings.HasPrefix(path, prefix) {
		return "", false
	}
	id := strings.TrimPrefix(path, prefix)
	if id == "" || strings.ContainsAny(id, "/\\\x00\r\n") || id == "." || id == ".." {
		return "", false
	}
	if strings.TrimSpace(id) != id || len(id) > 256 {
		return "", false
	}
	return id, validIngestionUUID(id)
}

func isSHA256Digest(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}
