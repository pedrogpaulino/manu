package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pedrogpaulino/manu/internal/config"
)

const (
	// Version identifies the first local HTTP API contract. It is independent
	// from the legacy result and bundle contract versions.
	Version = "v1"

	// APIVersion is an explicit alias for callers that prefer the qualified
	// name when constructing or checking response envelopes.
	APIVersion = Version

	// RequestIDHeader is the request correlation header understood by the
	// local HTTP surface.
	RequestIDHeader = "X-Request-ID"

	requestIDMaxLength = 128
)

var (
	// ErrInvalidServerConfig identifies a server configuration that cannot be
	// used safely. It deliberately carries no configuration values.
	ErrInvalidServerConfig = errors.New("api: invalid server configuration")
	// ErrInvalidListenAddress identifies malformed listen addresses.
	ErrInvalidListenAddress = errors.New("api: invalid listen address")
	// ErrNonLoopbackListenAddress identifies an address that is not an
	// explicit loopback address. Only the canonical localhost hostname is
	// accepted; other hostnames are rejected fail-closed.
	ErrNonLoopbackListenAddress = errors.New("api: non-loopback listen address")
	// ErrListen identifies a listener startup failure without exposing the
	// underlying operating-system diagnostic.
	ErrListen = errors.New("api: listener startup failed")
	// ErrServe identifies an unexpected server-loop failure.
	ErrServe = errors.New("api: server loop failed")
	// ErrShutdown identifies a graceful shutdown that exceeded its bound.
	ErrShutdown = errors.New("api: graceful shutdown failed")
	// ErrServerRunning prevents two concurrent Serve calls from sharing one
	// net/http.Server and its listener lifecycle.
	ErrServerRunning = errors.New("api: server is already running")
)

// ErrNonLoopbackAddress is kept as a concise compatibility alias for callers
// that only need to distinguish the fail-closed address policy.
var ErrNonLoopbackAddress = ErrNonLoopbackListenAddress

// ListenFunc opens a listener for Server. The production implementation is
// net.Listen; tests can inject an in-memory or otherwise controlled listener.
type ListenFunc func(network, address string) (net.Listener, error)

// Problem is the safe, versioned error envelope returned before route
// contracts are implemented. Message is fixed server text and never contains
// parser, request, or configuration diagnostics.
type Problem struct {
	Version   string `json:"version"`
	Code      string `json:"code"`
	Message   string `json:"message"`
	Status    int    `json:"status"`
	RequestID string `json:"request_id"`
}

// Server owns one HTTP listener lifecycle. It does not open a database; the
// optional ingestion port is composed at the HTTP boundary while health and
// readiness remain local process/dependency checks.
type Server struct {
	configuration config.ServerConfig
	httpServer    *http.Server
	listen        ListenFunc
	readiness     ReadinessChecker

	lifecycleMu sync.Mutex
	started     bool
}

// NewServer constructs a local-only HTTP server using net.Listen.
func NewServer(configuration config.Config) (*Server, error) {
	return NewServerWithListenerAndReadiness(configuration, nil, nil)
}

// NewServerWithListener constructs a local-only HTTP server with an injected
// listener factory. A nil factory selects net.Listen.
func NewServerWithListener(configuration config.Config, listen ListenFunc) (*Server, error) {
	return NewServerWithListenerAndReadiness(configuration, listen, nil)
}

// NewServerWithReadiness constructs a local-only HTTP server with an
// injectable readiness checker. A nil checker keeps /readyz safely not ready.
func NewServerWithReadiness(configuration config.Config, readiness ReadinessChecker) (*Server, error) {
	return NewServerWithListenerAndReadiness(configuration, nil, readiness)
}

// NewServerWithListenerAndReadiness composes both test seams while retaining
// the same bounded HTTP lifecycle and loopback validation.
func NewServerWithListenerAndReadiness(configuration config.Config, listen ListenFunc, readiness ReadinessChecker) (*Server, error) {
	return NewServerWithListenerAndReadinessAndIngestion(configuration, listen, readiness, nil)
}

// NewServerWithIngestion composes the local server with the injected
// asynchronous ingestion application port. The constructor does not open a
// database; callers provide the already configured JobStore-backed service.
func NewServerWithIngestion(configuration config.Config, service IngestionService) (*Server, error) {
	return NewServerWithListenerAndReadinessAndIngestion(configuration, nil, nil, service)
}

// NewServerWithIngestionAndQuery composes ingestion, query execution, and
// evidence inspection ports behind the same fixed local organization. A nil
// query or evidence port remains an explicit 503 at its route.
func NewServerWithIngestionAndQuery(
	configuration config.Config,
	ingestionService IngestionService,
	queryService QueryService,
	evidenceService EvidenceService,
) (*Server, error) {
	return NewServerWithListenerAndReadinessAndServices(
		configuration, nil, nil, ingestionService, queryService, evidenceService,
	)
}

// NewServerWithListenerAndReadinessAndIngestion composes listener,
// readiness, and ingestion seams while retaining the bounded HTTP lifecycle.
func NewServerWithListenerAndReadinessAndIngestion(
	configuration config.Config,
	listen ListenFunc,
	readiness ReadinessChecker,
	service IngestionService,
) (*Server, error) {
	return NewServerWithListenerAndReadinessAndServices(configuration, listen, readiness, service, nil, nil)
}

// NewServerWithListenerAndReadinessAndServices composes all currently
// available HTTP application ports while preserving the existing listener
// and readiness seams.
func NewServerWithListenerAndReadinessAndServices(
	configuration config.Config,
	listen ListenFunc,
	readiness ReadinessChecker,
	ingestionService IngestionService,
	queryService QueryService,
	evidenceService EvidenceService,
) (*Server, error) {
	if err := validateServerConfig(configuration.Server); err != nil {
		return nil, err
	}
	if ingestionService != nil || queryService != nil || evidenceService != nil {
		if err := configuration.Organization.Validate(); err != nil {
			return nil, ErrInvalidServerConfig
		}
	}
	if listen == nil {
		listen = net.Listen
	}

	serverConfig := configuration.Server
	server := &Server{
		configuration: serverConfig,
		listen:        listen,
		readiness:     readiness,
	}
	server.httpServer = &http.Server{
		Handler:           newHandlerWithReadinessAndServices(serverConfig, nil, readiness, configuration.Organization.ID, configuration.Limits, ingestionService, queryService, evidenceService),
		ReadTimeout:       serverConfig.ReadTimeout,
		ReadHeaderTimeout: serverConfig.ReadTimeout,
		WriteTimeout:      serverConfig.WriteTimeout,
		IdleTimeout:       serverConfig.IdleTimeout,
		MaxHeaderBytes:    serverConfig.MaxHeaderBytes,
	}
	return server, nil
}

// NewHandler constructs the default safe handler without starting a listener.
// This is useful to embed the local HTTP boundary in tests or a caller-owned
// net/http server while retaining the same limits and response envelope.
func NewHandler(configuration config.Config) (http.Handler, error) {
	return NewHandlerWithReadiness(configuration, nil)
}

// NewHandlerWithReadiness constructs the default handler with an optional
// local readiness dependency and without opening a listener.
func NewHandlerWithReadiness(configuration config.Config, readiness ReadinessChecker) (http.Handler, error) {
	if err := validateServerConfig(configuration.Server); err != nil {
		return nil, err
	}
	return newHandlerWithReadiness(configuration.Server, nil, readiness), nil
}

// NewHandlerWithIngestion constructs the versioned HTTP handler with an
// injected ingestion application port and the configured fixed organization.
// It performs no database or network I/O.
func NewHandlerWithIngestion(configuration config.Config, service IngestionService) (http.Handler, error) {
	return NewHandlerWithIngestionAndQuery(configuration, service, nil, nil)
}

// NewHandlerWithIngestionAndQuery constructs the complete currently exposed
// local handler with injected application ports.
func NewHandlerWithIngestionAndQuery(
	configuration config.Config,
	ingestionService IngestionService,
	queryService QueryService,
	evidenceService EvidenceService,
) (http.Handler, error) {
	if err := validateServerConfig(configuration.Server); err != nil {
		return nil, err
	}
	if ingestionService != nil || queryService != nil || evidenceService != nil {
		if err := configuration.Organization.Validate(); err != nil {
			return nil, ErrInvalidServerConfig
		}
	}
	return newHandlerWithReadinessAndServices(
		configuration.Server,
		nil,
		nil,
		configuration.Organization.ID,
		configuration.Limits,
		ingestionService,
		queryService,
		evidenceService,
	), nil
}

// NewHandlerWithQuery constructs a handler with query and evidence ports but
// without an ingestion port.
func NewHandlerWithQuery(configuration config.Config, queryService QueryService, evidenceService EvidenceService) (http.Handler, error) {
	return NewHandlerWithIngestionAndQuery(configuration, nil, queryService, evidenceService)
}

// Handler returns the server's configured handler. It is safe to use it with
// httptest without starting Serve; the handler itself has no external I/O.
func (s *Server) Handler() http.Handler {
	if s == nil || s.httpServer == nil {
		return http.NotFoundHandler()
	}
	return s.httpServer.Handler
}

// ListenAddress returns the validated local listen address selected for the
// server.
func (s *Server) ListenAddress() string {
	if s == nil {
		return ""
	}
	return s.configuration.ListenAddress
}

// Serve opens the configured listener and blocks until the server exits or
// ctx is canceled. Cancellation triggers a bounded graceful shutdown and is
// returned to the caller as context.Canceled or context.DeadlineExceeded.
func (s *Server) Serve(ctx context.Context) error {
	if s == nil || s.httpServer == nil {
		return ErrInvalidServerConfig
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	s.lifecycleMu.Lock()
	if s.started {
		s.lifecycleMu.Unlock()
		return ErrServerRunning
	}
	s.started = true
	s.lifecycleMu.Unlock()

	listener, err := s.listen("tcp", s.configuration.ListenAddress)
	if err != nil {
		return ErrListen
	}

	// BaseContext lets net/http propagate the command context to every
	// accepted request. Serve is single-use, so assigning it before starting
	// the server does not race with another lifecycle.
	s.httpServer.BaseContext = func(net.Listener) context.Context { return ctx }
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- s.httpServer.Serve(listener)
	}()

	select {
	case err := <-serveDone:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return ErrServe
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), s.configuration.ShutdownTimeout)
		shutdownErr := s.httpServer.Shutdown(shutdownCtx)
		cancel()
		if shutdownErr != nil {
			// Shutdown leaves active connections in place after its deadline.
			// Close is the bounded, best-effort final release for this owned
			// listener and prevents a goroutine from surviving cancellation.
			_ = s.httpServer.Close()
			waitForServe(serveDone)
			return ErrShutdown
		}
		if err, ok := waitForServe(serveDone); !ok {
			_ = s.httpServer.Close()
			if _, ok = waitForServe(serveDone); !ok {
				return ErrShutdown
			}
		} else if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return ErrServe
		}
		return ctx.Err()
	}
}

// Shutdown requests a bounded graceful shutdown for callers that own the
// lifecycle directly. It does not expose net/http diagnostics.
func (s *Server) Shutdown(ctx context.Context) error {
	if s == nil || s.httpServer == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := s.httpServer.Shutdown(ctx); err != nil {
		return ErrShutdown
	}
	return nil
}

// ValidateListenAddress accepts an explicit loopback host (or the canonical
// localhost hostname) and a non-zero TCP port. Wildcard addresses, zones, and
// all remote or ambiguous hostnames are rejected because this mode has no
// authentication.
func ValidateListenAddress(address string) error {
	host, port, err := net.SplitHostPort(strings.TrimSpace(address))
	if err != nil || host == "" {
		return ErrInvalidListenAddress
	}
	parsedPort, err := strconv.Atoi(port)
	if err != nil || parsedPort < 1 || parsedPort > 65535 {
		return ErrInvalidListenAddress
	}
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return ErrNonLoopbackListenAddress
	}
	return nil
}

func validateServerConfig(serverConfig config.ServerConfig) error {
	if err := serverConfig.Validate(); err != nil {
		return ErrInvalidServerConfig
	}
	if err := ValidateListenAddress(serverConfig.ListenAddress); err != nil {
		return err
	}
	return nil
}

func newHandler(serverConfig config.ServerConfig, next http.Handler) http.Handler {
	return newHandlerWithReadiness(serverConfig, next, nil)
}

func newHandlerWithReadiness(serverConfig config.ServerConfig, next http.Handler, readiness ReadinessChecker) http.Handler {
	return newHandlerWithReadinessAndIngestion(serverConfig, next, readiness, "", config.LimitsConfig{}, nil)
}

func newHandlerWithReadinessAndIngestion(
	serverConfig config.ServerConfig,
	next http.Handler,
	readiness ReadinessChecker,
	organization string,
	limits config.LimitsConfig,
	service IngestionService,
) http.Handler {
	return newHandlerWithReadinessAndServices(serverConfig, next, readiness, organization, limits, service, nil, nil)
}

func newHandlerWithReadinessAndServices(
	serverConfig config.ServerConfig,
	next http.Handler,
	readiness ReadinessChecker,
	organization string,
	limits config.LimitsConfig,
	ingestionService IngestionService,
	queryService QueryService,
	evidenceService EvidenceService,
) http.Handler {
	if next == nil {
		next = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			writeProblem(w, http.StatusNotFound, "route_not_implemented", "route is not implemented", requestIDFromContext(r.Context()))
		})
	}
	ingestionEndpoint := newIngestionEndpoint(ingestionService, organization, limits)
	queryEndpoint := newQueryEndpoint(queryService, organization, limits)
	evidenceEndpoint := newEvidenceEndpoint(evidenceService, organization, limits, queryEndpoint.slots)
	downstream := next
	ingestionRoute := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ingestionEndpoint.ServeHTTP(w, r, downstream)
	})
	queryDownstream := http.Handler(ingestionRoute)
	queryRoute := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queryEndpoint.ServeHTTP(w, r, queryDownstream)
	})
	evidenceDownstream := http.Handler(queryRoute)
	next = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		evidenceEndpoint.ServeHTTP(w, r, evidenceDownstream)
	})
	if readiness == nil {
		readiness = ReadinessFunc(nil)
	}
	next = readinessRoutes(next, readiness)

	semaphore := make(chan struct{}, serverConfig.MaxConcurrentRequests)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := requestID(r.Header.Get(RequestIDHeader))
		w.Header().Set(RequestIDHeader, requestID)

		requestContext := r.Context()
		if timeout := requestTimeout(serverConfig); timeout > 0 {
			var cancel context.CancelFunc
			requestContext, cancel = context.WithTimeout(requestContext, timeout)
			defer cancel()
		}
		r = r.WithContext(withRequestID(requestContext, requestID))
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, serverConfig.MaxBodyBytes)
			defer r.Body.Close()
		}
		if r.ContentLength > serverConfig.MaxBodyBytes {
			writeProblem(w, http.StatusRequestEntityTooLarge, "body_too_large", "request body is too large", requestID)
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == HealthPath {
			writeLiveness(w, requestID)
			return
		}
		if err := r.Context().Err(); err != nil {
			writeProblem(w, http.StatusRequestTimeout, "request_canceled", "request canceled", requestID)
			return
		}

		select {
		case semaphore <- struct{}{}:
			defer func() { <-semaphore }()
		case <-r.Context().Done():
			writeProblem(w, http.StatusRequestTimeout, "request_canceled", "request canceled", requestID)
			return
		default:
			writeProblem(w, http.StatusServiceUnavailable, "concurrency_limited", "server is busy", requestID)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func readinessRoutes(next http.Handler, readiness ReadinessChecker) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == ReadinessPath {
			writeReadiness(w, r.Context(), readiness, requestIDFromContext(r.Context()))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func requestTimeout(serverConfig config.ServerConfig) time.Duration {
	timeout := serverConfig.ReadTimeout
	if timeout <= 0 || (serverConfig.WriteTimeout > 0 && serverConfig.WriteTimeout < timeout) {
		timeout = serverConfig.WriteTimeout
	}
	return timeout
}

type requestIDKey struct{}

func withRequestID(ctx context.Context, requestID string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, requestID)
}

func requestIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	requestID, _ := ctx.Value(requestIDKey{}).(string)
	return requestID
}

// RequestID returns the validated correlation identifier installed by the
// server middleware, or an empty string when the context did not come from
// this HTTP boundary.
func RequestID(ctx context.Context) string { return requestIDFromContext(ctx) }

var requestSequence atomic.Uint64

func requestID(candidate string) string {
	if validRequestID(candidate) {
		return candidate
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err == nil {
		return hex.EncodeToString(random[:])
	}
	// crypto/rand failure is exceptionally rare; a process-local fallback
	// still provides correlation without copying untrusted input.
	return "local-" + strconv.FormatUint(requestSequence.Add(1), 10)
}

func validRequestID(value string) bool {
	if value == "" || len(value) > requestIDMaxLength {
		return false
	}
	for _, character := range []byte(value) {
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') ||
			character == '-' || character == '_' || character == '.' {
			continue
		}
		return false
	}
	return true
}

func writeProblem(w http.ResponseWriter, status int, code, message, requestID string) {
	problem := Problem{
		Version:   Version,
		Code:      code,
		Message:   message,
		Status:    status,
		RequestID: requestID,
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(problem)
}

func waitForServe(serveDone <-chan error) (error, bool) {
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case err := <-serveDone:
		return err, true
	case <-timer.C:
		return nil, false
	}
}
