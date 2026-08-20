package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/pedrogpaulino/manu/internal/api"
)

func TestValidateRemoteServerURL(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		wantErr bool
	}{
		{name: "default", value: ""},
		{name: "ipv4 loopback", value: "http://127.0.0.1:8080"},
		{name: "localhost", value: "http://localhost:8080/"},
		{name: "ipv6 loopback", value: "http://[::1]:8080"},
		{name: "remote address", value: "http://192.0.2.10:8080", wantErr: true},
		{name: "https is not local HTTP contract", value: "https://127.0.0.1:8080", wantErr: true},
		{name: "missing port", value: "http://127.0.0.1", wantErr: true},
		{name: "path is not a base URL", value: "http://127.0.0.1:8080/api/v1", wantErr: true},
		{name: "credentials", value: "http://user:pass@127.0.0.1:8080", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := validateRemoteServerURL(test.value)
			if (err != nil) != test.wantErr {
				t.Fatalf("validateRemoteServerURL(%q) error = %v, wantErr %t", test.value, err, test.wantErr)
			}
		})
	}
}

func TestRunAskAndEvidenceSupportHumanAndJSONOutput(t *testing.T) {
	const (
		queryID    = "66666666-6666-4666-8666-666666666666"
		evidenceID = "44444444-4444-4444-8444-444444444444"
	)
	var queryBody []byte
	client := testRemoteClient(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == api.QueriesPath:
			queryBody, _ = io.ReadAll(r.Body)
			return remoteJSONResponse(http.StatusOK, api.QueryResponse{
				Version:        api.APIVersion,
				ID:             queryID,
				OrganizationID: "local",
				State:          api.QueryStatePartial,
				QuestionDigest: strings.Repeat("a", 64),
				Result:         json.RawMessage(`{"answer":"supported result"}`),
				RequestID:      "request-query",
			}), nil
		case r.Method == http.MethodGet && r.URL.Path == api.EvidencePath+"/"+evidenceID:
			return remoteJSONResponse(http.StatusOK, api.EvidenceResponse{
				Version:          api.APIVersion,
				ID:               evidenceID,
				OrganizationID:   "local",
				SourceID:         "22222222-2222-4222-8222-222222222222",
				SnapshotID:       "33333333-3333-4333-8333-333333333333",
				ArtifactID:       "55555555-5555-4555-8555-555555555555",
				ContentState:     "present",
				Classification:   "safe_text",
				Persist:          "allow",
				ExternalTransfer: "deny",
				Content:          "authorized local evidence",
				ContentHash:      strings.Repeat("b", 64),
				RequestID:        "request-evidence",
			}), nil
		default:
			return remoteJSONResponse(http.StatusNotFound, api.Problem{
				Version: api.APIVersion,
				Code:    "not_found",
				Status:  http.StatusNotFound,
			}), nil
		}
	}))
	withRemoteClientFactory(t, client)

	var askJSON, askErr bytes.Buffer
	code := RunContext(context.Background(), nil, []string{
		"ask", "--server", defaultRemoteServerURL, "--kind", "observed_execution", "--json", "what is observed?",
	}, &askJSON, &askErr)
	if code != ExitPartial {
		t.Fatalf("ask JSON code = %d, want %d; stdout=%q stderr=%q", code, ExitPartial, askJSON.String(), askErr.String())
	}
	var query api.QueryResponse
	if err := json.Unmarshal(askJSON.Bytes(), &query); err != nil {
		t.Fatalf("ask JSON = %q: %v", askJSON.String(), err)
	}
	if query.State != api.QueryStatePartial ||
		!bytes.Contains(queryBody, []byte(`"question":"what is observed?"`)) ||
		!bytes.Contains(queryBody, []byte(`"kind":"observed_execution"`)) {
		t.Fatalf("ask request/response = %q / %#v", queryBody, query)
	}
	if bytes.Contains(queryBody, []byte("organization_id")) || bytes.Contains(askJSON.Bytes(), []byte("what is observed?")) {
		t.Fatalf("ask leaked request-only content: request=%q response=%q", queryBody, askJSON.String())
	}

	var evidenceHuman, evidenceErr bytes.Buffer
	code = RunContext(context.Background(), nil, []string{
		"evidence", "--server", defaultRemoteServerURL, evidenceID,
	}, &evidenceHuman, &evidenceErr)
	if code != ExitSuccess {
		t.Fatalf("evidence human code = %d, want %d; stdout=%q stderr=%q", code, ExitSuccess, evidenceHuman.String(), evidenceErr.String())
	}
	if !strings.Contains(evidenceHuman.String(), "authorized local evidence") || !strings.Contains(evidenceHuman.String(), "content_state: present") {
		t.Fatalf("evidence human output = %q", evidenceHuman.String())
	}
}

func TestRunAskRequiresKnowledgeKind(t *testing.T) {
	var requests int
	client := testRemoteClient(t, roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		requests++
		return remoteJSONResponse(http.StatusOK, api.QueryResponse{
			Version:        api.APIVersion,
			ID:             "66666666-6666-4666-8666-666666666666",
			OrganizationID: "local",
			State:          api.QueryStateCompleted,
			QuestionDigest: strings.Repeat("a", 64),
		}), nil
	}))
	withRemoteClientFactory(t, client)

	var stdout, stderr bytes.Buffer
	code := RunContext(context.Background(), nil, []string{
		"ask", "--server", defaultRemoteServerURL, "--question", "question",
	}, &stdout, &stderr)
	if code != ExitUsage || requests != 0 || stdout.Len() != 0 {
		t.Fatalf("missing kind = code %d requests %d stdout=%q stderr=%q", code, requests, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "kind") {
		t.Fatalf("missing kind diagnostic = %q", stderr.String())
	}
}

func TestRunIngestStreamsBundleAndRunIngestionStatus(t *testing.T) {
	const ingestionID = "77777777-7777-4777-8777-777777777777"
	var (
		method       string
		path         string
		contentType  string
		requestBytes int
	)
	client := testRemoteClient(t, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		method = request.Method
		path = request.URL.Path
		contentType = request.Header.Get("Content-Type")
		if request.Body != nil {
			body, err := io.ReadAll(request.Body)
			if err != nil {
				return nil, err
			}
			requestBytes = len(body)
		}
		switch {
		case request.Method == http.MethodPost && request.URL.Path == api.IngestionsPath:
			return remoteJSONResponse(http.StatusAccepted, api.IngestionResponse{
				Version:        api.APIVersion,
				ID:             ingestionID,
				OrganizationID: "local",
				State:          "pending",
				Stage:          "validation",
				RequestID:      "request-ingestion",
			}), nil
		case request.Method == http.MethodGet && request.URL.Path == api.IngestionsPath+"/"+ingestionID:
			return remoteJSONResponse(http.StatusOK, api.IngestionResponse{
				Version:        api.APIVersion,
				ID:             ingestionID,
				OrganizationID: "local",
				State:          "completed",
				Stage:          "activation",
				RequestID:      "request-status",
			}), nil
		default:
			return remoteJSONResponse(http.StatusNotFound, api.Problem{Code: "not_found", Status: http.StatusNotFound}), nil
		}
	}))
	withRemoteClientFactory(t, client)
	bundleDirectory := testRemoteBundle(t)

	var ingestJSON, ingestErr bytes.Buffer
	code := RunContext(context.Background(), nil, []string{
		"ingest", "--server", defaultRemoteServerURL, "--bundle", bundleDirectory, "--json",
	}, &ingestJSON, &ingestErr)
	if code != ExitSuccess {
		t.Fatalf("ingest code = %d; stdout=%q stderr=%q", code, ingestJSON.String(), ingestErr.String())
	}
	var ingestion api.IngestionResponse
	if err := json.Unmarshal(ingestJSON.Bytes(), &ingestion); err != nil {
		t.Fatalf("ingest JSON = %q: %v", ingestJSON.String(), err)
	}
	if ingestion.ID != ingestionID || method != http.MethodPost || path != api.IngestionsPath ||
		!strings.HasPrefix(contentType, "multipart/form-data;") || requestBytes == 0 {
		t.Fatalf("ingest request/response = method %q path %q content-type %q bytes %d response %#v", method, path, contentType, requestBytes, ingestion)
	}

	var statusJSON, statusErr bytes.Buffer
	code = RunContext(context.Background(), nil, []string{
		"ingestion", "--server", defaultRemoteServerURL, "--json", ingestionID,
	}, &statusJSON, &statusErr)
	if code != ExitSuccess {
		t.Fatalf("ingestion status code = %d; stdout=%q stderr=%q", code, statusJSON.String(), statusErr.String())
	}
	if err := json.Unmarshal(statusJSON.Bytes(), &ingestion); err != nil {
		t.Fatalf("ingestion JSON = %q: %v", statusJSON.String(), err)
	}
	if ingestion.State != "completed" || method != http.MethodGet || path != api.IngestionsPath+"/"+ingestionID {
		t.Fatalf("ingestion request/response = method %q path %q response %#v", method, path, ingestion)
	}
}

func TestRunAskMapsProblemAndUnavailableResponsesSafely(t *testing.T) {
	unavailableClient := testRemoteClient(t, roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return nil, errors.New("network diagnostic must not escape")
	}))
	withRemoteClientFactory(t, unavailableClient)

	// The injected transport returns an error without exposing its diagnostic
	// through the command output.
	var problemOut, problemErr bytes.Buffer
	code := RunContext(context.Background(), nil, []string{
		"ask", "--server", defaultRemoteServerURL, "--kind", "inventory", "--question", "question",
	}, &problemOut, &problemErr)
	if code != ExitTechnical || !strings.Contains(problemErr.String(), "API is unavailable") || problemOut.Len() != 0 {
		t.Fatalf("unavailable response = code %d stdout=%q stderr=%q", code, problemOut.String(), problemErr.String())
	}
	if strings.Contains(problemErr.String(), "network diagnostic") {
		t.Fatalf("transport diagnostic leaked: %q", problemErr.String())
	}

	problemClient := testRemoteClient(t, roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return remoteJSONResponse(http.StatusBadRequest, api.Problem{
			Version:   api.APIVersion,
			Code:      "invalid_query",
			Message:   "query request is invalid",
			Status:    http.StatusBadRequest,
			RequestID: "problem-request",
		}), nil
	}))
	withRemoteClientFactory(t, problemClient)
	problemOut.Reset()
	problemErr.Reset()
	code = RunContext(context.Background(), nil, []string{
		"ask", "--server", defaultRemoteServerURL, "--kind", "inventory", "--question", "question",
	}, &problemOut, &problemErr)
	if code != ExitTechnical || !strings.Contains(problemErr.String(), "status=400 code=invalid_query") || problemOut.Len() != 0 {
		t.Fatalf("problem response = code %d stdout=%q stderr=%q", code, problemOut.String(), problemErr.String())
	}
	if strings.Contains(problemErr.String(), "query request is invalid") {
		t.Fatalf("raw problem message leaked: %q", problemErr.String())
	}
}

func TestRemoteClientDoesNotFollowRedirectWithBundleBody(t *testing.T) {
	var localRequests, externalRequests int
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Host == "127.0.0.1:8080" {
			localRequests++
		} else {
			externalRequests++
		}
		return &http.Response{
			StatusCode: http.StatusTemporaryRedirect,
			Status:     fmt.Sprintf("%d Temporary Redirect", http.StatusTemporaryRedirect),
			Header: http.Header{
				"Content-Type": []string{"application/json"},
				"Location":     []string{"http://198.51.100.10:8080" + api.IngestionsPath},
			},
			Body:    io.NopCloser(strings.NewReader("{}")),
			Request: request,
		}, nil
	})

	bundleDirectory := testRemoteBundle(t)
	client, err := NewRemoteClient(RemoteClientConfig{
		ServerURL:  defaultRemoteServerURL,
		Timeout:    5 * time.Second,
		HTTPClient: &http.Client{Transport: transport},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.UploadBundle(context.Background(), bundleDirectory)
	if !errors.Is(err, ErrRemoteProtocol) {
		t.Fatalf("UploadBundle() error = %v, want protocol error for 307", err)
	}
	if localRequests != 1 || externalRequests != 0 {
		t.Fatalf("redirect requests = local %d external %d, want 1/0", localRequests, externalRequests)
	}
}

func testRemoteBundle(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locating bundle fixture")
	}
	return filepath.Join(filepath.Dir(filename), "..", "bundle", "testdata", "golden")
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func remoteJSONResponse(status int, value any) *http.Response {
	body, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	body = append(body, '\n')
	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d", status),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader(body)),
	}
}

func testRemoteClient(t *testing.T, transport http.RoundTripper) *RemoteClient {
	t.Helper()
	client, err := NewRemoteClient(RemoteClientConfig{
		ServerURL:  defaultRemoteServerURL,
		Timeout:    5 * time.Second,
		HTTPClient: &http.Client{Transport: transport},
	})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func withRemoteClientFactory(t *testing.T, client *RemoteClient) {
	t.Helper()
	previous := remoteClientFactory
	remoteClientFactory = func(RemoteClientConfig) (*RemoteClient, error) {
		return client, nil
	}
	t.Cleanup(func() {
		remoteClientFactory = previous
	})
}
