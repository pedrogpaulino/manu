package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pedrogpaulino/manu/internal/config"
	"github.com/pedrogpaulino/manu/internal/persistence"
)

func TestValidateListenAddress(t *testing.T) {
	tests := []struct {
		name    string
		address string
		wantErr error
	}{
		{name: "ipv4 loopback", address: "127.0.0.1:8080"},
		{name: "ipv6 loopback", address: "[::1]:8080"},
		{name: "canonical localhost", address: "localhost:8080"},
		{name: "wildcard ipv4", address: "0.0.0.0:8080", wantErr: ErrNonLoopbackListenAddress},
		{name: "wildcard ipv6", address: "[::]:8080", wantErr: ErrNonLoopbackListenAddress},
		{name: "remote ipv4", address: "192.0.2.1:8080", wantErr: ErrNonLoopbackListenAddress},
		{name: "ambiguous hostname", address: "api.internal:8080", wantErr: ErrNonLoopbackListenAddress},
		{name: "missing port", address: "127.0.0.1", wantErr: ErrInvalidListenAddress},
		{name: "zero port", address: "127.0.0.1:0", wantErr: ErrInvalidListenAddress},
		{name: "bad port", address: "127.0.0.1:not-a-port", wantErr: ErrInvalidListenAddress},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateListenAddress(tt.address)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("ValidateListenAddress(%q) = %v, want %v", tt.address, err, tt.wantErr)
			}
		})
	}
}

func TestNewServerAcceptsCanonicalLocalhost(t *testing.T) {
	serverConfig := config.Default()
	serverConfig.Server.ListenAddress = "localhost:8080"
	server, err := NewServer(serverConfig)
	if err != nil {
		t.Fatalf("NewServer(localhost) error = %v", err)
	}
	if got := server.ListenAddress(); got != serverConfig.Server.ListenAddress {
		t.Fatalf("ListenAddress() = %q, want %q", got, serverConfig.Server.ListenAddress)
	}
}

func TestHandlerReturnsSafeVersionedProblemAndRequestID(t *testing.T) {
	serverConfig := config.Default()
	handler, err := NewHandler(serverConfig)
	if err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/api/v1/not-ready", nil)
	request.Header.Set(RequestIDHeader, "client-request_1")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNotFound)
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/problem+json" {
		t.Fatalf("content type = %q, want application/problem+json", got)
	}
	if got := recorder.Header().Get(RequestIDHeader); got != "client-request_1" {
		t.Fatalf("response request id = %q, want client-request_1", got)
	}
	var problem Problem
	if err := json.Unmarshal(recorder.Body.Bytes(), &problem); err != nil {
		t.Fatal(err)
	}
	if problem.Version != Version || problem.Version == "v1alpha1" {
		t.Fatalf("problem version = %q, want API version %q", problem.Version, Version)
	}
	if problem.Code != "route_not_implemented" || problem.RequestID != "client-request_1" {
		t.Fatalf("problem = %#v", problem)
	}
}

func TestHandlerReplacesUnsafeRequestIDWithoutEchoingIt(t *testing.T) {
	serverConfig := config.Default()
	handler, err := NewHandler(serverConfig)
	if err != nil {
		t.Fatal(err)
	}
	unsafe := "request id with spaces and secret-value"
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/", nil)
	request.Header.Set(RequestIDHeader, unsafe)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	requestID := recorder.Header().Get(RequestIDHeader)
	if requestID == "" || requestID == unsafe || strings.Contains(recorder.Body.String(), unsafe) {
		t.Fatalf("unsafe request id was echoed: header=%q body=%q", requestID, recorder.Body.String())
	}
	var problem Problem
	if err := json.Unmarshal(recorder.Body.Bytes(), &problem); err != nil {
		t.Fatal(err)
	}
	if problem.RequestID != requestID {
		t.Fatalf("problem request id = %q, header = %q", problem.RequestID, requestID)
	}
}

func TestHealthLivenessDoesNotCallFailingReadinessChecker(t *testing.T) {
	serverConfig := config.Default()
	called := false
	checker := ReadinessFunc(func(context.Context) error {
		called = true
		return errors.New("dsn password=do-not-leak")
	})
	handler, err := NewHandlerWithReadiness(serverConfig, checker)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1"+HealthPath, nil)
	request.Header.Set(RequestIDHeader, "live-1")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK || called {
		t.Fatalf("liveness status/called = %d/%t, want 200/false", recorder.Code, called)
	}
	response := decodeHealth(t, recorder)
	if response.Version != Version || response.Status != "ok" || response.Code != "alive" || response.RequestID != "live-1" {
		t.Fatalf("liveness response = %#v", response)
	}
	if strings.Contains(recorder.Body.String(), "do-not-leak") {
		t.Fatalf("liveness leaked checker detail: %q", recorder.Body.String())
	}
}

func TestHealthReadinessReadyAndDefaultNotReady(t *testing.T) {
	tests := []struct {
		name       string
		checker    ReadinessChecker
		wantStatus int
		wantCode   string
		wantState  string
	}{
		{name: "ready", checker: ReadinessFunc(func(context.Context) error { return nil }), wantStatus: http.StatusOK, wantCode: "ready", wantState: "ready"},
		{name: "not configured", checker: nil, wantStatus: http.StatusServiceUnavailable, wantCode: "readiness_not_configured", wantState: "not_ready"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			serverConfig := config.Default()
			handler, err := NewHandlerWithReadiness(serverConfig, tt.checker)
			if err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1"+ReadinessPath, nil)
			request.Header.Set(RequestIDHeader, "ready-1")
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			if recorder.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body=%q", recorder.Code, tt.wantStatus, recorder.Body.String())
			}
			response := decodeHealth(t, recorder)
			if response.Version != Version || response.Code != tt.wantCode || response.Status != tt.wantState || response.RequestID != "ready-1" {
				t.Fatalf("readiness response = %#v", response)
			}
			if got := recorder.Header().Get("Content-Type"); got != "application/json" {
				t.Fatalf("content type = %q, want application/json", got)
			}
		})
	}
}

func TestHealthReadinessSafeDiagnostics(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantCode   string
		wantStatus int
	}{
		{name: "missing schema", err: ErrReadinessSchemaMissing, wantCode: "schema_missing", wantStatus: http.StatusServiceUnavailable},
		{name: "incompatible schema", err: ErrReadinessSchemaIncompatible, wantCode: "schema_incompatible", wantStatus: http.StatusServiceUnavailable},
		{name: "dependency", err: ErrReadinessDependency, wantCode: "dependency_unavailable", wantStatus: http.StatusServiceUnavailable},
		{name: "canceled", err: context.Canceled, wantCode: "readiness_canceled", wantStatus: http.StatusServiceUnavailable},
		{name: "deadline", err: context.DeadlineExceeded, wantCode: "readiness_timeout", wantStatus: http.StatusServiceUnavailable},
		{name: "unknown safe fallback", err: errors.New("postgres password=secret-value"), wantCode: "readiness_unavailable", wantStatus: http.StatusServiceUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checker := ReadinessFunc(func(context.Context) error { return tt.err })
			handler, err := NewHandlerWithReadiness(config.Default(), checker)
			if err != nil {
				t.Fatal(err)
			}
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "http://127.0.0.1"+ReadinessPath, nil))
			if recorder.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", recorder.Code, tt.wantStatus)
			}
			response := decodeHealth(t, recorder)
			if response.Code != tt.wantCode || response.Status != "not_ready" || response.Version != Version {
				t.Fatalf("response = %#v", response)
			}
			if strings.Contains(recorder.Body.String(), "secret-value") || strings.Contains(recorder.Body.String(), "postgres") {
				t.Fatalf("raw readiness error leaked: %q", recorder.Body.String())
			}
		})
	}
}

func TestMigrationReadinessMapsSchemaAndContextStates(t *testing.T) {
	tests := []struct {
		name      string
		status    persistence.Status
		err       error
		wantError error
	}{
		{name: "ready", status: persistence.Status{Ready: true}},
		{name: "incomplete", status: persistence.Status{Ready: false}, wantError: ErrReadinessSchemaIncomplete},
		{name: "missing", err: persistence.ErrMigrationSchemaMissing, wantError: ErrReadinessSchemaMissing},
		{name: "ahead", err: persistence.ErrSchemaAhead, wantError: ErrReadinessSchemaIncompatible},
		{name: "checksum", err: persistence.ErrMigrationChecksumMismatch, wantError: ErrReadinessSchemaIncompatible},
		{name: "database", err: persistence.ErrMigrationDatabase, wantError: ErrReadinessDependency},
		{name: "canceled", err: context.Canceled, wantError: context.Canceled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reader := fakeMigrationStatusReader{status: tt.status, err: tt.err}
			checker := NewMigrationReadiness(reader)
			err := checker.Check(context.Background())
			if !errors.Is(err, tt.wantError) {
				t.Fatalf("Check() error = %v, want %v", err, tt.wantError)
			}
		})
	}
}

func TestMigrationReadinessHonorsCanceledContextBeforeInspecting(t *testing.T) {
	called := false
	reader := migrationStatusReaderFunc(func(context.Context) (persistence.Status, error) {
		called = true
		return persistence.Status{Ready: true}, nil
	})
	checker := NewMigrationReadiness(reader)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := checker.Check(ctx)
	if !errors.Is(err, context.Canceled) || called {
		t.Fatalf("Check() error/called = %v/%t, want context.Canceled/false", err, called)
	}
}

func decodeHealth(t *testing.T, recorder *httptest.ResponseRecorder) HealthResponse {
	t.Helper()
	var response HealthResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode health response: %v; body=%q", err, recorder.Body.String())
	}
	return response
}

type fakeMigrationStatusReader struct {
	status persistence.Status
	err    error
}

func (f fakeMigrationStatusReader) Status(context.Context) (persistence.Status, error) {
	return f.status, f.err
}

type migrationStatusReaderFunc func(context.Context) (persistence.Status, error)

func (f migrationStatusReaderFunc) Status(ctx context.Context) (persistence.Status, error) {
	return f(ctx)
}

func TestHandlerAppliesBodyLimit(t *testing.T) {
	serverConfig := config.Default().Server
	serverConfig.MaxBodyBytes = 4
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err == nil {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(body)
			return
		}
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			writeProblem(w, http.StatusRequestEntityTooLarge, "body_too_large", "request body is too large", requestIDFromContext(r.Context()))
			return
		}
		writeProblem(w, http.StatusBadRequest, "invalid_body", "request body is invalid", requestIDFromContext(r.Context()))
	})
	handler := newHandler(serverConfig, next)
	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/", strings.NewReader("12345"))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusRequestEntityTooLarge)
	}
	if strings.Contains(recorder.Body.String(), "12345") {
		t.Fatalf("body content echoed in error: %q", recorder.Body.String())
	}
}

func TestHandlerLimitsConcurrentRequests(t *testing.T) {
	serverConfig := config.Default().Server
	serverConfig.MaxConcurrentRequests = 1
	serverConfig.ReadTimeout = time.Second
	serverConfig.WriteTimeout = time.Second
	started := make(chan struct{})
	release := make(chan struct{})
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		w.WriteHeader(http.StatusNoContent)
	})
	handler := newHandler(serverConfig, next)

	firstDone := make(chan struct{})
	go func() {
		request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/first", nil)
		handler.ServeHTTP(httptest.NewRecorder(), request)
		close(firstDone)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first request did not start")
	}

	secondRecorder := httptest.NewRecorder()
	secondRequest := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/second", nil)
	handler.ServeHTTP(secondRecorder, secondRequest)
	if secondRecorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("second status = %d, want %d", secondRecorder.Code, http.StatusServiceUnavailable)
	}
	close(release)
	select {
	case <-firstDone:
	case <-time.After(time.Second):
		t.Fatal("first request did not finish")
	}
}

func TestServeCancellationClosesInjectedListener(t *testing.T) {
	serverConfig := config.Default()
	fake := newBlockingListener()
	server, err := NewServerWithListener(serverConfig, func(string, string) (net.Listener, error) {
		return fake, nil
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()
	select {
	case <-fake.accepted:
	case <-time.After(time.Second):
		t.Fatal("server did not call Accept")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Serve cancellation error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not stop after cancellation")
	}
	select {
	case <-fake.closed:
	default:
		t.Fatal("injected listener was not closed")
	}
}

func TestServeHidesListenerError(t *testing.T) {
	serverConfig := config.Default()
	server, err := NewServerWithListener(serverConfig, func(string, string) (net.Listener, error) {
		return nil, errors.New("driver secret at 127.0.0.1:8080")
	})
	if err != nil {
		t.Fatal(err)
	}
	err = server.Serve(context.Background())
	if !errors.Is(err, ErrListen) || strings.Contains(err.Error(), "driver secret") {
		t.Fatalf("Serve error = %v, want safe ErrListen", err)
	}
}

type blockingListener struct {
	accepted chan struct{}
	closed   chan struct{}
	once     sync.Once
}

func newBlockingListener() *blockingListener {
	return &blockingListener{
		accepted: make(chan struct{}),
		closed:   make(chan struct{}),
	}
}

func (l *blockingListener) Accept() (net.Conn, error) {
	select {
	case <-l.accepted:
	default:
		close(l.accepted)
	}
	<-l.closed
	return nil, errors.New("listener closed")
}

func (l *blockingListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}

func (l *blockingListener) Addr() net.Addr { return fakeAddr("127.0.0.1:8080") }

type fakeAddr string

func (a fakeAddr) Network() string { return "tcp" }
func (a fakeAddr) String() string  { return string(a) }
