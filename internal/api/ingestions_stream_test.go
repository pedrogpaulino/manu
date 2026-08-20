package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pedrogpaulino/manu/internal/bundle"
	"github.com/pedrogpaulino/manu/internal/config"
	"github.com/pedrogpaulino/manu/internal/ingestion"
)

func TestIngestionEndpointDelegatesMultipartBodyToDurableStreamingPort(t *testing.T) {
	service := &streamingIngestionService{}
	handler, err := NewHandlerWithIngestion(config.Default(), service)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, IngestionsPath, strings.NewReader("streamed-payload"))
	request.Header.Set("Content-Type", `multipart/form-data; boundary=stream-test`)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("POST status = %d, want %d: %s", response.Code, http.StatusAccepted, response.Body.String())
	}
	if service.bytes != len("streamed-payload") || service.options.OrganizationID != config.DefaultOrganizationID {
		t.Fatalf("streaming service received bytes/options = %d/%#v", service.bytes, service.options)
	}
}

type streamingIngestionService struct {
	bytes   int
	options bundle.MultipartReadOptions
}

func (s *streamingIngestionService) Create(context.Context, string, bundle.Bundle) (ingestion.Job, error) {
	return ingestion.Job{}, ingestion.ErrInvalidJob
}

func (s *streamingIngestionService) Get(context.Context, string, string) (ingestion.Job, error) {
	return ingestion.Job{}, ingestion.ErrJobNotFound
}

func (s *streamingIngestionService) CreateMultipart(_ context.Context, _ string, reader io.Reader, _ string, options bundle.MultipartReadOptions) (ingestion.Job, error) {
	payload, err := io.ReadAll(reader)
	if err != nil {
		return ingestion.Job{}, err
	}
	s.bytes = len(payload)
	s.options = options
	return ingestion.NewJob(ingestion.NewJobInput{
		OrganizationID:          "6f3f0b2d-9d62-5ef5-8e1f-cb8cb17a7b1f",
		OrganizationExternalID:  config.DefaultOrganizationID,
		SourceExternalID:        "source",
		SnapshotExternalID:      "snapshot",
		FactualDigest:           strings.Repeat("a", 64),
		AnalysisConfigurationID: "configuration",
	})
}

var _ IngestionService = (*streamingIngestionService)(nil)
var _ MultipartIngestionService = (*streamingIngestionService)(nil)
