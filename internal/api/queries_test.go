package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pedrogpaulino/manu/internal/config"
	"github.com/pedrogpaulino/manu/internal/persistence"
	domainquery "github.com/pedrogpaulino/manu/internal/query"
)

const (
	testQueryID     = "00000000-0000-4000-8000-000000000101"
	testSourceID    = "00000000-0000-4000-8000-000000000102"
	testSnapshotID  = "00000000-0000-4000-8000-000000000103"
	testEvidenceID  = "00000000-0000-4000-8000-000000000104"
	testPackageHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

type fakeQueryService struct {
	mu                   sync.Mutex
	organization         string
	input                QueryInput
	execution            QueryExecution
	err                  error
	getErr               error
	persisted            bool
	organizationOverride string
}

func (s *fakeQueryService) Create(_ context.Context, organization string, input QueryInput) (QueryExecution, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.organization = organization
	s.input = input
	s.persisted = true
	if s.err != nil {
		return QueryExecution{}, s.err
	}
	execution := s.execution
	execution.OrganizationID = organization
	if s.organizationOverride != "" {
		execution.OrganizationID = s.organizationOverride
	}
	execution.QuestionDigest = input.QuestionDigest
	return execution, nil
}

func (s *fakeQueryService) Get(_ context.Context, organization, _ string) (QueryExecution, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.organization = organization
	if s.getErr != nil {
		return QueryExecution{}, s.getErr
	}
	execution := s.execution
	execution.OrganizationID = organization
	if s.organizationOverride != "" {
		execution.OrganizationID = s.organizationOverride
	}
	return execution, nil
}

func completedQueryExecution() QueryExecution {
	createdAt := time.Now().UTC()
	startedAt := createdAt
	finishedAt := createdAt.Add(time.Millisecond)
	return QueryExecution{
		ID:             testQueryID,
		SourceID:       testSourceID,
		SnapshotID:     testSnapshotID,
		State:          QueryStateCompleted,
		QuestionDigest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		PackageDigest:  testPackageHash,
		Response:       json.RawMessage(`{"answer":"authorized"}`),
		CreatedAt:      createdAt,
		StartedAt:      &startedAt,
		FinishedAt:     &finishedAt,
	}
}

func TestQueryPostPersistsBeforeResponseAndUsesFixedOrganization(t *testing.T) {
	service := &fakeQueryService{execution: completedQueryExecution()}
	configuration := config.Default()
	handler, err := NewHandlerWithQuery(configuration, service, nil)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, QueriesPath, strings.NewReader(`{"question":"What is the deployment flow?","kind":"possible_flow","organization_id":"other"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Organization-ID", "other")
	request.Header.Set(RequestIDHeader, "query-request")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("organization override status = %d, want 400", response.Code)
	}

	request = httptest.NewRequest(http.MethodPost, QueriesPath, strings.NewReader(`{"question":"What is the deployment flow?","kind":"possible_flow"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Organization-ID", "other")
	request.Header.Set(RequestIDHeader, "query-request")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("query status = %d, want 200: %s", response.Code, response.Body.String())
	}
	var decoded QueryResponse
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Version != Version || decoded.ID != testQueryID || decoded.OrganizationID != configuration.Organization.ID || decoded.State != QueryStateCompleted || decoded.RequestID != "query-request" {
		t.Fatalf("query response = %#v", decoded)
	}
	if !service.persisted || service.organization != configuration.Organization.ID || service.input.QuestionDigest == "" {
		t.Fatalf("service did not receive persisted fixed-scope input: %#v", service)
	}
	if strings.Contains(response.Body.String(), "deployment flow") {
		t.Fatalf("raw question leaked into response: %s", response.Body.String())
	}
}

func TestQueryGetReinspectsAndRejectsCrossScopeOverride(t *testing.T) {
	service := &fakeQueryService{execution: completedQueryExecution()}
	handler, err := NewHandlerWithQuery(config.Default(), service, nil)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, QueriesPath+"/"+testQueryID, nil)
	request.Header.Set("Organization-ID", "other")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200: %s", response.Code, response.Body.String())
	}
	var decoded QueryResponse
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.OrganizationID != config.Default().Organization.ID || service.organization != config.Default().Organization.ID {
		t.Fatalf("GET organization was overridden: response=%#v service=%q", decoded, service.organization)
	}

	invalid := httptest.NewRecorder()
	handler.ServeHTTP(invalid, httptest.NewRequest(http.MethodGet, QueriesPath+"/not-a-uuid", nil))
	if invalid.Code != http.StatusBadRequest || invalid.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("invalid query path = %d/%q", invalid.Code, invalid.Header().Get("Content-Type"))
	}
}

func TestQueryRejectsCrossOrganizationExecution(t *testing.T) {
	service := &fakeQueryService{
		execution:            completedQueryExecution(),
		organizationOverride: "00000000-0000-4000-8000-000000000199",
	}
	handler, err := NewHandlerWithQuery(config.Default(), service, nil)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, QueriesPath, strings.NewReader(`{"question":"scope?","kind":"inventory"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || strings.Contains(response.Body.String(), "authorized") {
		t.Fatalf("cross-organization query response = %d/%s", response.Code, response.Body.String())
	}
}

func TestQueryGetReinspectsNonTerminalStateWithoutResult(t *testing.T) {
	execution := completedQueryExecution()
	execution.State = QueryStateRunning
	execution.Response = nil
	execution.PackageDigest = ""
	execution.FinishedAt = nil
	service := &fakeQueryService{execution: execution}
	handler, err := NewHandlerWithQuery(config.Default(), service, nil)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, QueriesPath+"/"+testQueryID, nil))
	if response.Code != http.StatusOK {
		t.Fatalf("running GET status = %d, want 200: %s", response.Code, response.Body.String())
	}
	var decoded QueryResponse
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.State != QueryStateRunning || decoded.Result != nil {
		t.Fatalf("running GET response = %#v", decoded)
	}

	execution.Response = json.RawMessage(`{"answer":"must-not-appear"}`)
	service.execution = execution
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, QueriesPath+"/"+testQueryID, nil))
	if response.Code != http.StatusBadRequest || strings.Contains(response.Body.String(), "must-not-appear") {
		t.Fatalf("running result response = %d/%s", response.Code, response.Body.String())
	}
}

func TestQueryStatesAndServiceErrorsAreExplicit(t *testing.T) {
	tests := []struct {
		name       string
		state      QueryState
		wantStatus int
	}{
		{name: "pending", state: QueryStatePending, wantStatus: http.StatusServiceUnavailable},
		{name: "running", state: QueryStateRunning, wantStatus: http.StatusServiceUnavailable},
		{name: "partial", state: QueryStatePartial, wantStatus: http.StatusOK},
		{name: "abstained", state: QueryStateAbstained, wantStatus: http.StatusOK},
		{name: "failed", state: QueryStateFailed, wantStatus: http.StatusInternalServerError},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			execution := completedQueryExecution()
			execution.State = test.state
			if test.state == QueryStatePending {
				execution.Response = nil
				execution.PackageDigest = ""
				execution.StartedAt = nil
				execution.FinishedAt = nil
			}
			if test.state == QueryStateRunning {
				execution.Response = nil
				execution.PackageDigest = ""
				execution.FinishedAt = nil
			}
			if test.state == QueryStateAbstained {
				execution.Response = nil
				execution.PackageDigest = ""
				execution.DiagnosticCode = "pipeline_not_configured"
			}
			if test.state == QueryStateFailed {
				execution.Response = nil
				execution.PackageDigest = ""
				execution.DiagnosticCode = "processing_failed"
			}
			service := &fakeQueryService{execution: execution}
			handler, err := NewHandlerWithQuery(config.Default(), service, nil)
			if err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(http.MethodPost, QueriesPath, strings.NewReader(`{"question":"status?","kind":"inventory"}`))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d: %s", response.Code, test.wantStatus, response.Body.String())
			}
		})
	}

	service := &fakeQueryService{err: persistence.ErrDatabase}
	handler, err := NewHandlerWithQuery(config.Default(), service, nil)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, QueriesPath, strings.NewReader(`{"question":"status?","kind":"inventory"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || strings.Contains(response.Body.String(), "database") {
		t.Fatalf("database error response = %d/%s", response.Code, response.Body.String())
	}

	service.getErr = persistence.ErrNotFound
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, QueriesPath+"/"+testQueryID, nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("not found response = %d, want 404", response.Code)
	}
}

func TestValidateQueryExecutionRejectsInconsistentLifecycle(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*QueryExecution)
	}{
		{name: "missing created timestamp", mutate: func(execution *QueryExecution) {
			execution.CreatedAt = time.Time{}
		}},
		{name: "completed without finished timestamp", mutate: func(execution *QueryExecution) {
			execution.FinishedAt = nil
		}},
		{name: "pending with terminal timestamp", mutate: func(execution *QueryExecution) {
			execution.State = QueryStatePending
			execution.Response = nil
			execution.PackageDigest = ""
			execution.StartedAt = nil
		}},
		{name: "running without start", mutate: func(execution *QueryExecution) {
			execution.State = QueryStateRunning
			execution.Response = nil
			execution.PackageDigest = ""
			execution.StartedAt = nil
			execution.FinishedAt = nil
		}},
		{name: "abstained without diagnostic", mutate: func(execution *QueryExecution) {
			execution.State = QueryStateAbstained
			execution.Response = nil
			execution.PackageDigest = ""
			execution.DiagnosticCode = ""
		}},
		{name: "failed with result", mutate: func(execution *QueryExecution) {
			execution.State = QueryStateFailed
			execution.DiagnosticCode = "failed"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			execution := completedQueryExecution()
			test.mutate(&execution)
			if err := validateQueryExecution(execution, execution.QuestionDigest); !errors.Is(err, ErrInvalidQuery) {
				t.Fatalf("validateQueryExecution() error = %v, want invalid query", err)
			}
		})
	}
}

func TestValidateQueryExecutionAcceptsStructuredAbstention(t *testing.T) {
	input := domainquery.AbstentionInput{
		Package: domainquery.EvidencePackage{
			ID:             "package-1",
			Digest:         strings.Repeat("a", 64),
			OrganizationID: testSourceID,
			SourceID:       testSnapshotID,
			SnapshotID:     testEvidenceID,
		},
		QueryID:      "query-1",
		QueryDigest:  strings.Repeat("b", 64),
		QuestionKind: domainquery.KnowledgeQuestionInventory,
		Support: domainquery.SupportAssessment{
			Kind:  domainquery.KnowledgeQuestionInventory,
			Level: domainquery.EvidenceSupportSufficient,
		},
	}
	input.Package.Evidence = nil
	result, err := domainquery.EvaluateAbstention(input)
	if err != nil {
		t.Fatalf("EvaluateAbstention() error = %v", err)
	}
	if !result.Decision.Abstain {
		t.Fatal("expected deterministic abstention")
	}
	encoded, err := json.Marshal(result.Response)
	if err != nil {
		t.Fatalf("marshal abstention response: %v", err)
	}
	createdAt := time.Now().UTC()
	startedAt := createdAt
	finishedAt := createdAt.Add(time.Millisecond)
	execution := QueryExecution{
		ID:             testQueryID,
		State:          QueryStateAbstained,
		QuestionDigest: input.QueryDigest,
		PackageDigest:  input.Package.Digest,
		Response:       encoded,
		CreatedAt:      createdAt,
		StartedAt:      &startedAt,
		FinishedAt:     &finishedAt,
	}
	if err := validateQueryExecution(execution, input.QueryDigest); err != nil {
		t.Fatalf("structured abstention was rejected: %v", err)
	}
}

func TestQueryRejectsMalformedBodyMediaAndLimits(t *testing.T) {
	configuration := config.Default()
	configuration.Limits.MaxQueryBytes = 32
	service := &fakeQueryService{execution: completedQueryExecution()}
	handler, err := NewHandlerWithQuery(configuration, service, nil)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		body   string
		header string
		want   int
	}{
		{name: "media", body: `{"question":"ok","kind":"inventory"}`, header: "text/plain", want: http.StatusUnsupportedMediaType},
		{name: "malformed", body: `{"question":`, header: "application/json", want: http.StatusBadRequest},
		{name: "too large", body: `{"question":"this question is too large","kind":"inventory"}`, header: "application/json", want: http.StatusRequestEntityTooLarge},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, QueriesPath, strings.NewReader(test.body))
			request.Header.Set("Content-Type", test.header)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.want {
				t.Fatalf("status = %d, want %d: %s", response.Code, test.want, response.Body.String())
			}
			if response.Header().Get("Content-Type") != "application/problem+json" {
				t.Fatalf("content type = %q", response.Header().Get("Content-Type"))
			}
		})
	}
}

func TestQueryWrongMethodsAndUnavailableService(t *testing.T) {
	handler, err := NewHandlerWithQuery(config.Default(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	getCollection := httptest.NewRecorder()
	handler.ServeHTTP(getCollection, httptest.NewRequest(http.MethodGet, QueriesPath, nil))
	if getCollection.Code != http.StatusMethodNotAllowed || getCollection.Header().Get("Allow") != http.MethodPost {
		t.Fatalf("collection method response = %d/%q", getCollection.Code, getCollection.Header().Get("Allow"))
	}
	postItem := httptest.NewRecorder()
	handler.ServeHTTP(postItem, httptest.NewRequest(http.MethodPost, QueriesPath+"/"+testQueryID, nil))
	if postItem.Code != http.StatusMethodNotAllowed || postItem.Header().Get("Allow") != http.MethodGet {
		t.Fatalf("item method response = %d/%q", postItem.Code, postItem.Header().Get("Allow"))
	}
	postUnavailable := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, QueriesPath, strings.NewReader(`{"question":"unavailable","kind":"inventory"}`))
	request.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(postUnavailable, request)
	if postUnavailable.Code != http.StatusServiceUnavailable {
		t.Fatalf("unavailable POST status = %d, want 503", postUnavailable.Code)
	}
}

func TestQueryCancellationDoesNotCallService(t *testing.T) {
	service := &fakeQueryService{execution: completedQueryExecution()}
	handler, err := NewHandlerWithQuery(config.Default(), service, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	request := httptest.NewRequest(http.MethodPost, QueriesPath, strings.NewReader(`{"question":"cancel","kind":"inventory"}`)).WithContext(ctx)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusRequestTimeout || service.persisted {
		t.Fatalf("canceled query response/service = %d/%t", response.Code, service.persisted)
	}
}

func TestQueryConcurrencyIsBounded(t *testing.T) {
	configuration := config.Default()
	configuration.Limits.MaxConcurrentQueries = 1
	service := &blockingQueryService{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	handler, err := NewHandlerWithQuery(configuration, service, nil)
	if err != nil {
		t.Fatal(err)
	}
	firstDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		request := httptest.NewRequest(http.MethodPost, QueriesPath, strings.NewReader(`{"question":"first","kind":"inventory"}`))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		firstDone <- response
	}()
	<-service.started
	second := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, QueriesPath, strings.NewReader(`{"question":"second","kind":"inventory"}`))
	request.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(second, request)
	if second.Code != http.StatusServiceUnavailable {
		t.Fatalf("second query status = %d, want 503", second.Code)
	}
	close(service.release)
	if first := <-firstDone; first.Code != http.StatusOK {
		t.Fatalf("first query status = %d, want 200", first.Code)
	}
}

type blockingQueryService struct {
	started chan struct{}
	release chan struct{}
}

func (s *blockingQueryService) Create(_ context.Context, organization string, input QueryInput) (QueryExecution, error) {
	close(s.started)
	<-s.release
	execution := completedQueryExecution()
	execution.OrganizationID = organization
	execution.QuestionDigest = input.QuestionDigest
	return execution, nil
}

func (s *blockingQueryService) Get(_ context.Context, organization, _ string) (QueryExecution, error) {
	execution := completedQueryExecution()
	execution.OrganizationID = organization
	return execution, nil
}
