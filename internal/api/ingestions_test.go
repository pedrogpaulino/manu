package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pedrogpaulino/manu/internal/bundle"
	"github.com/pedrogpaulino/manu/internal/config"
	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/evidence"
	"github.com/pedrogpaulino/manu/internal/identity"
	"github.com/pedrogpaulino/manu/internal/ingestion"
)

func TestIngestionPostIsAcceptedAndFactuallyIdempotent(t *testing.T) {
	configuration := config.Default()
	store := ingestion.NewMemoryStore()
	service := ingestion.NewHTTPService(store)
	handler, err := NewHandlerWithIngestion(configuration, service)
	if err != nil {
		t.Fatalf("NewHandlerWithIngestion() error = %v", err)
	}
	body, contentType := httpBundleBody(t, configuration.Organization.ID)

	first := postIngestion(t, handler, body, contentType, "request-one")
	if first.Code != http.StatusAccepted {
		t.Fatalf("first POST status = %d, want %d: %s", first.Code, http.StatusAccepted, first.Body.String())
	}
	firstResponse := decodeIngestionResponse(t, first)
	if firstResponse.Version != Version || firstResponse.State != ingestion.JobStatePending || firstResponse.Stage != ingestion.JobStageValidation {
		t.Fatalf("first response = %#v", firstResponse)
	}
	if firstResponse.OrganizationID != configuration.Organization.ID || firstResponse.Counts.EvidenceCount != 0 {
		t.Fatalf("first response scope/counts = %#v", firstResponse)
	}

	second := postIngestion(t, handler, body, contentType, "request-two")
	if second.Code != http.StatusAccepted {
		t.Fatalf("second POST status = %d, want %d: %s", second.Code, http.StatusAccepted, second.Body.String())
	}
	secondResponse := decodeIngestionResponse(t, second)
	if secondResponse.ID != firstResponse.ID {
		t.Fatalf("factual retry created a different job: %q != %q", secondResponse.ID, firstResponse.ID)
	}
	stored := store.Snapshot()
	if len(stored) != 1 || stored[0].OrganizationID != identity.CanonicalUUID("organization", configuration.Organization.ID) {
		t.Fatalf("stored organization identity = %#v", stored)
	}

	request := httptest.NewRequest(http.MethodGet, IngestionsPath+"/"+firstResponse.ID, nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want %d: %s", response.Code, http.StatusOK, response.Body.String())
	}
	got := decodeIngestionResponse(t, response)
	if got.ID != firstResponse.ID || got.OrganizationID != configuration.Organization.ID {
		t.Fatalf("GET response = %#v", got)
	}
}

func TestIngestionGetNotFoundAndFixedOrganizationScope(t *testing.T) {
	configuration := config.Default()
	service := ingestion.NewHTTPService(ingestion.NewMemoryStore())
	handler, err := NewHandlerWithIngestion(configuration, service)
	if err != nil {
		t.Fatalf("NewHandlerWithIngestion() error = %v", err)
	}
	request := httptest.NewRequest(http.MethodGet, IngestionsPath+"/00000000-0000-4000-8000-000000000001", nil)
	request.Header.Set("Organization-ID", "other-organization")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("GET unknown status = %d, want %d", response.Code, http.StatusNotFound)
	}
	if response.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("GET unknown content type = %q", response.Header().Get("Content-Type"))
	}
	invalid := httptest.NewRecorder()
	invalidRequest := httptest.NewRequest(http.MethodGet, IngestionsPath+"/not-an-id", nil)
	handler.ServeHTTP(invalid, invalidRequest)
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("GET invalid id status = %d, want %d", invalid.Code, http.StatusBadRequest)
	}
}

func TestIngestionStreamsMultipartAndRejectsMalformedPayloadSafely(t *testing.T) {
	configuration := config.Default()
	service := ingestion.NewHTTPService(ingestion.NewMemoryStore())
	handler, err := NewHandlerWithIngestion(configuration, service)
	if err != nil {
		t.Fatalf("NewHandlerWithIngestion() error = %v", err)
	}
	body, contentType := httpBundleBody(t, configuration.Organization.ID)
	reader := &chunkReadCloser{reader: bytes.NewReader(body), chunk: 5}
	request := httptest.NewRequest(http.MethodPost, IngestionsPath, reader)
	request.Header.Set("Content-Type", contentType)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("streaming POST status = %d, want %d: %s", response.Code, http.StatusAccepted, response.Body.String())
	}
	if reader.reads < 2 {
		t.Fatalf("multipart body was not consumed incrementally: reads = %d", reader.reads)
	}

	const secret = "source-secret-must-not-echo"
	malformed := httptest.NewRequest(http.MethodPost, IngestionsPath, strings.NewReader(secret))
	malformed.Header.Set("Content-Type", `multipart/form-data; boundary=broken-boundary`)
	malformed.Header.Set(RequestIDHeader, "safe-request")
	malformedResponse := httptest.NewRecorder()
	handler.ServeHTTP(malformedResponse, malformed)
	if malformedResponse.Code != http.StatusBadRequest {
		t.Fatalf("malformed status = %d, want %d", malformedResponse.Code, http.StatusBadRequest)
	}
	if strings.Contains(malformedResponse.Body.String(), secret) {
		t.Fatalf("problem response echoed payload: %s", malformedResponse.Body.String())
	}
	if malformedResponse.Header().Get(RequestIDHeader) != "safe-request" {
		t.Fatalf("request id header = %q", malformedResponse.Header().Get(RequestIDHeader))
	}
	if malformedResponse.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("problem content type = %q", malformedResponse.Header().Get("Content-Type"))
	}
}

func TestIngestionRejectsWrongMethodAndBodyLimit(t *testing.T) {
	configuration := config.Default()
	configuration.Server.MaxBodyBytes = 32
	service := ingestion.NewHTTPService(ingestion.NewMemoryStore())
	handler, err := NewHandlerWithIngestion(configuration, service)
	if err != nil {
		t.Fatalf("NewHandlerWithIngestion() error = %v", err)
	}

	get := httptest.NewRequest(http.MethodGet, IngestionsPath, nil)
	getResponse := httptest.NewRecorder()
	handler.ServeHTTP(getResponse, get)
	if getResponse.Code != http.StatusMethodNotAllowed || getResponse.Header().Get("Allow") != http.MethodPost {
		t.Fatalf("collection GET = %d allow %q", getResponse.Code, getResponse.Header().Get("Allow"))
	}

	tooLarge := httptest.NewRequest(http.MethodPost, IngestionsPath, strings.NewReader(strings.Repeat("x", 64)))
	tooLarge.Header.Set("Content-Type", "multipart/form-data; boundary=ignored")
	tooLargeResponse := httptest.NewRecorder()
	handler.ServeHTTP(tooLargeResponse, tooLarge)
	if tooLargeResponse.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("too-large status = %d, want %d", tooLargeResponse.Code, http.StatusRequestEntityTooLarge)
	}
}

func TestIngestionCancellationAndUnavailableDefault(t *testing.T) {
	configuration := config.Default()
	configuration.Postgres.DSN = "postgres://user:password@example.invalid/manu"
	service := ingestion.NewHTTPService(ingestion.NewMemoryStore())
	handler, err := NewHandlerWithIngestion(configuration, service)
	if err != nil {
		t.Fatalf("NewHandlerWithIngestion() error = %v", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	request := httptest.NewRequest(http.MethodPost, IngestionsPath, strings.NewReader("body")).WithContext(canceled)
	request.Header.Set("Content-Type", "multipart/form-data; boundary=ignored")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusRequestTimeout {
		t.Fatalf("canceled status = %d, want %d", response.Code, http.StatusRequestTimeout)
	}

	defaultHandler, err := NewHandlerWithIngestion(configuration, nil)
	if err != nil {
		t.Fatalf("NewHandlerWithIngestion(nil) error = %v", err)
	}
	defaultResponse := httptest.NewRecorder()
	defaultRequest := httptest.NewRequest(http.MethodGet, IngestionsPath+"/00000000-0000-4000-8000-000000000001", nil)
	defaultHandler.ServeHTTP(defaultResponse, defaultRequest)
	if defaultResponse.Code != http.StatusServiceUnavailable {
		t.Fatalf("default GET status = %d, want %d", defaultResponse.Code, http.StatusServiceUnavailable)
	}
	if strings.Contains(defaultResponse.Body.String(), configuration.Postgres.DSN) {
		t.Fatalf("default problem exposed a configured DSN")
	}
}

func TestIngestionConcurrencyIsBounded(t *testing.T) {
	configuration := config.Default()
	configuration.Limits.MaxConcurrentIngestions = 1
	blocking := &blockingIngestionService{
		delegate: ingestion.NewHTTPService(ingestion.NewMemoryStore()),
		started:  make(chan struct{}),
		release:  make(chan struct{}),
	}
	handler, err := NewHandlerWithIngestion(configuration, blocking)
	if err != nil {
		t.Fatalf("NewHandlerWithIngestion() error = %v", err)
	}
	body, contentType := httpBundleBody(t, configuration.Organization.ID)
	firstDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		firstDone <- postIngestion(t, handler, body, contentType, "first")
	}()
	<-blocking.started
	second := postIngestion(t, handler, body, contentType, "second")
	if second.Code != http.StatusServiceUnavailable {
		t.Fatalf("second concurrent POST status = %d, want %d", second.Code, http.StatusServiceUnavailable)
	}
	close(blocking.release)
	first := <-firstDone
	if first.Code != http.StatusAccepted {
		t.Fatalf("first concurrent POST status = %d, want %d", first.Code, http.StatusAccepted)
	}
}

func postIngestion(
	t *testing.T,
	handler http.Handler,
	body []byte,
	contentType string,
	requestID string,
) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, IngestionsPath, bytes.NewReader(body))
	request.Header.Set("Content-Type", contentType)
	request.Header.Set(RequestIDHeader, requestID)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func decodeIngestionResponse(t *testing.T, response *httptest.ResponseRecorder) IngestionResponse {
	t.Helper()
	var decoded IngestionResponse
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return decoded
}

func httpBundleBody(t *testing.T, organizationID string) ([]byte, string) {
	t.Helper()
	directory := t.TempDir()
	input := httpTestBundle(organizationID)
	if err := bundle.WriteBundle(context.Background(), directory, input); err != nil {
		t.Fatalf("WriteBundle() error = %v", err)
	}
	sender, err := bundle.NewMultipartSender(directory, bundle.MultipartWriteOptions{Boundary: "http-test-boundary"})
	if err != nil {
		t.Fatalf("NewMultipartSender() error = %v", err)
	}
	var body bytes.Buffer
	if _, err := sender.Send(context.Background(), &body); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	return body.Bytes(), sender.ContentType()
}

func httpTestBundle(organizationID string) bundle.Bundle {
	const (
		sourceID        = "source-1"
		revision        = "revision-1"
		configurationID = "configuration-1"
	)
	source := contract.Source{ID: sourceID, Name: "fixture", Type: "filesystem", Revision: revision}
	snapshot := contract.Snapshot{
		ID:       contract.SnapshotID(sourceID, revision, strings.Repeat("b", 64)),
		SourceID: sourceID,
		Revision: revision,
		Hash:     strings.Repeat("b", 64),
	}
	artifact := contract.Artifact{
		SourceID: sourceID,
		Path:     "src/A.java",
		Type:     "java",
		Hash:     strings.Repeat("a", 64),
		Size:     12,
	}
	artifact.ID = contract.ArtifactID(artifact.SourceID, artifact.Path, artifact.Hash)
	contribution := contract.Contribution{
		ArtifactID:      artifact.ID,
		AnalyzerID:      "java",
		AnalyzerVersion: "1",
		Method:          "symbols",
		Type:            "symbol",
		Locator: contract.Locator{
			SourceID: sourceID, ArtifactID: artifact.ID, Path: artifact.Path,
			StartLine: 1, EndLine: 1,
		},
	}
	contribution.ID = contract.ContributionID(
		contribution.ArtifactID,
		contribution.AnalyzerID,
		contribution.AnalyzerVersion,
		contribution.Method,
	)
	legacy := contract.Manifest{
		ContractVersion: contract.Version,
		ResultID:        "result-1",
		Source:          source,
		Snapshot:        snapshot,
		Execution: contract.ExecutionMetadata{
			RunID: "run-1", ConfigurationID: configurationID,
		},
		ArtifactCount: 1, ContributionCount: 1,
		Coverage: []contract.Coverage{}, Gaps: []contract.Gap{}, Failures: []contract.Failure{},
	}
	unit := evidence.EvidenceUnit{
		Version: evidence.Version, OrganizationID: organizationID, SourceID: sourceID,
		SnapshotID: snapshot.ID, ArtifactID: artifact.ID,
		Contribution: evidence.ContributionRef{
			ID: contribution.ID, ArtifactID: artifact.ID,
			AnalyzerID: contribution.AnalyzerID, AnalyzerVersion: contribution.AnalyzerVersion,
			Method: contribution.Method,
		},
		Locator: contribution.Locator, ContentState: evidence.ContentStatePresent,
		Content: "class A {}", ContentHash: evidence.ContentDigest("class A {}"),
		ContentBytes: 10, ContentCharacters: 10,
		Persist: evidence.DecisionAllow, ExternalTransfer: evidence.DecisionAllow,
	}
	unit.ID = evidence.EvidenceID(unit)
	return bundle.Bundle{
		Manifest: bundle.Manifest{
			Version:      bundle.Version,
			Organization: bundle.Organization{ID: organizationID, Name: "Fixture organization"},
			Manifest:     legacy,
			Analysis:     bundle.Analysis{ID: "analysis-1", ConfigurationID: configurationID, Revision: "analysis-revision-1"},
			Evidence:     bundle.EvidenceMetadata{State: bundle.EvidenceStateAvailable},
			Limits: bundle.Limits{
				MaxBundleBytes: 1 << 20, MaxManifestBytes: 1 << 16,
				MaxEvidenceBytes: 1 << 16, MaxArtifacts: 10,
				MaxContributions: 10, MaxEvidenceUnits: 10,
			},
		},
		Artifacts:     []contract.Artifact{artifact},
		Contributions: []contract.Contribution{contribution},
		Evidence:      []evidence.EvidenceUnit{unit},
	}
}

type chunkReadCloser struct {
	reader io.Reader
	chunk  int
	reads  int
}

func (r *chunkReadCloser) Read(destination []byte) (int, error) {
	r.reads++
	if len(destination) > r.chunk {
		destination = destination[:r.chunk]
	}
	return r.reader.Read(destination)
}

func (r *chunkReadCloser) Close() error { return nil }

type blockingIngestionService struct {
	delegate *ingestion.HTTPService
	started  chan struct{}
	release  chan struct{}
}

func (s *blockingIngestionService) Create(
	ctx context.Context,
	organizationID string,
	input bundle.Bundle,
) (ingestion.Job, error) {
	select {
	case <-s.started:
	default:
		close(s.started)
	}
	select {
	case <-s.release:
		return s.delegate.Create(ctx, organizationID, input)
	case <-ctx.Done():
		return ingestion.Job{}, ctx.Err()
	}
}

func (s *blockingIngestionService) Get(
	ctx context.Context,
	organizationID string,
	jobID string,
) (ingestion.Job, error) {
	return s.delegate.Get(ctx, organizationID, jobID)
}
