package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/pedrogpaulino/manu/internal/analysis"
	"github.com/pedrogpaulino/manu/internal/api"
	"github.com/pedrogpaulino/manu/internal/bundle"
	domainquery "github.com/pedrogpaulino/manu/internal/query"
)

const (
	defaultRemoteServerURL   = "http://127.0.0.1:8080"
	defaultRemoteTimeout     = 30 * time.Second
	maxRemoteResponseBytes   = 8 << 20
	remoteRequestContentType = "application/json"
)

var (
	// ErrRemoteURL identifies a server URL that cannot be used by the local
	// unauthenticated client. Only explicit HTTP loopback addresses are valid.
	ErrRemoteURL = errors.New("cli: invalid local server URL")
	// ErrRemoteUnavailable identifies a transport or connection failure. The
	// underlying network diagnostic is intentionally not exposed to callers.
	ErrRemoteUnavailable = errors.New("cli: remote service unavailable")
	// ErrRemoteProtocol identifies a response that is not the versioned API
	// envelope expected by this client.
	ErrRemoteProtocol = errors.New("cli: invalid remote response")
	// ErrRemoteBundle identifies a local bundle that could not be streamed.
	ErrRemoteBundle = errors.New("cli: invalid bundle")

	remoteClientFactory = NewRemoteClient
)

// RemoteClientConfig controls the small HTTP client used by CLI commands.
// HTTPClient is injectable for deterministic tests; production defaults to a
// bounded net/http client.
type RemoteClientConfig struct {
	ServerURL  string
	Timeout    time.Duration
	HTTPClient *http.Client
}

// RemoteClient is a loopback-only client for the versioned HTTP API. It owns
// no credentials and never receives a source filesystem path from the server.
type RemoteClient struct {
	baseURL          *url.URL
	httpClient       *http.Client
	maxResponseBytes int64
}

// NewRemoteClient validates a local API URL and creates a bounded HTTP client.
// The endpoint is intentionally HTTP-only in this unauthenticated local cut.
func NewRemoteClient(configuration RemoteClientConfig) (*RemoteClient, error) {
	baseURL, err := validateRemoteServerURL(configuration.ServerURL)
	if err != nil {
		return nil, err
	}
	timeout := configuration.Timeout
	if timeout <= 0 {
		timeout = defaultRemoteTimeout
	}
	// Never follow redirects: the request body can contain source-derived code
	// and the local API has no redirecting route. Copy injected clients so a
	// caller cannot accidentally re-enable forwarding to another host.
	httpClient := &http.Client{
		Timeout:       timeout,
		CheckRedirect: rejectRemoteRedirect,
	}
	if configuration.HTTPClient != nil {
		copyClient := *configuration.HTTPClient
		if copyClient.Timeout <= 0 {
			copyClient.Timeout = timeout
		}
		copyClient.CheckRedirect = rejectRemoteRedirect
		httpClient = &copyClient
	}
	return &RemoteClient{
		baseURL:          baseURL,
		httpClient:       httpClient,
		maxResponseBytes: maxRemoteResponseBytes,
	}, nil
}

func rejectRemoteRedirect(_ *http.Request, _ []*http.Request) error {
	return http.ErrUseLastResponse
}

// ProblemError is a safe representation of an API problem response. It
// carries only status and controlled code; response bodies are never copied
// into an error string.
type ProblemError struct {
	Status int
	Code   string
}

func (e *ProblemError) Error() string {
	if e == nil {
		return "remote HTTP problem"
	}
	return fmt.Sprintf("remote HTTP problem (%d, %s)", e.Status, e.Code)
}

func validateRemoteServerURL(raw string) (*url.URL, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		value = defaultRemoteServerURL
	}
	parsed, err := url.Parse(value)
	if err != nil || !strings.EqualFold(parsed.Scheme, "http") || parsed.Host == "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, ErrRemoteURL
	}
	if parsed.Path != "" && parsed.Path != "/" || parsed.RawPath != "" {
		return nil, ErrRemoteURL
	}
	host := parsed.Hostname()
	portText := parsed.Port()
	if host == "" || portText == "" {
		return nil, ErrRemoteURL
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return nil, ErrRemoteURL
	}
	ip := net.ParseIP(host)
	if ip == nil {
		if !strings.EqualFold(host, "localhost") {
			return nil, ErrRemoteURL
		}
	} else if !ip.IsLoopback() {
		return nil, ErrRemoteURL
	}
	parsed.Path = ""
	parsed.RawPath = ""
	return parsed, nil
}

func (c *RemoteClient) newRequest(ctx context.Context, method, endpoint string, body io.Reader) (*http.Request, error) {
	if c == nil || c.baseURL == nil || c.httpClient == nil {
		return nil, ErrRemoteProtocol
	}
	if ctx == nil {
		ctx = context.Background()
	}
	target := *c.baseURL
	target.Path = endpoint
	target.RawPath = ""
	request, err := http.NewRequestWithContext(ctx, method, target.String(), body)
	if err != nil {
		return nil, ErrRemoteURL
	}
	request.Header.Set("Accept", "application/json")
	return request, nil
}

func (c *RemoteClient) execute(request *http.Request) (int, []byte, error) {
	if c == nil || c.httpClient == nil {
		return 0, nil, ErrRemoteProtocol
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		if request != nil && request.Context().Err() != nil {
			return 0, nil, request.Context().Err()
		}
		return 0, nil, ErrRemoteUnavailable
	}
	defer response.Body.Close()
	limit := c.maxResponseBytes
	if limit <= 0 {
		limit = maxRemoteResponseBytes
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return 0, nil, ErrRemoteUnavailable
	}
	if int64(len(body)) > limit {
		return 0, nil, ErrRemoteProtocol
	}
	return response.StatusCode, body, nil
}

func (c *RemoteClient) requestJSON(ctx context.Context, method, endpoint string, payload []byte) (int, []byte, error) {
	request, err := c.newRequest(ctx, method, endpoint, bytes.NewReader(payload))
	if err != nil {
		return 0, nil, err
	}
	request.Header.Set("Content-Type", remoteRequestContentType)
	return c.execute(request)
}

func (c *RemoteClient) problem(status int, body []byte) error {
	problem := api.Problem{}
	if decodeRemoteJSON(body, &problem) == nil && problem.Code != "" {
		return &ProblemError{Status: status, Code: safeRemoteCode(problem.Code)}
	}
	return &ProblemError{Status: status, Code: fmt.Sprintf("http_%d", status)}
}

func (c *RemoteClient) requireStatus(status int, body []byte, expected int) error {
	if status == expected {
		return nil
	}
	if status >= 400 && status <= 599 {
		return c.problem(status, body)
	}
	return ErrRemoteProtocol
}

// UploadBundle streams a bundle directory into the request body. The pipe
// keeps the multipart sender and net/http backpressured; no complete bundle
// or MIME body is accumulated in memory.
func (c *RemoteClient) UploadBundle(ctx context.Context, directory string) (api.IngestionResponse, error) {
	var empty api.IngestionResponse
	if ctx == nil {
		ctx = context.Background()
	}
	sender, err := bundle.NewMultipartSender(directory, bundle.MultipartWriteOptions{})
	if err != nil {
		return empty, ErrRemoteBundle
	}
	reader, writer := io.Pipe()
	request, err := c.newRequest(ctx, http.MethodPost, api.IngestionsPath, reader)
	if err != nil {
		_ = reader.CloseWithError(err)
		return empty, err
	}
	request.Header.Set("Content-Type", sender.ContentType())
	sendDone := make(chan error, 1)
	go func() {
		_, sendErr := sender.Send(ctx, writer)
		if sendErr != nil {
			_ = writer.CloseWithError(sendErr)
		} else {
			_ = writer.Close()
		}
		sendDone <- sendErr
	}()
	status, body, requestErr := c.execute(request)
	// A server can reject a request before the sender has finished. Closing the
	// reader guarantees that the producer goroutine cannot remain blocked.
	_ = reader.CloseWithError(io.ErrClosedPipe)
	sendErr := <-sendDone
	if requestErr != nil {
		if ctx != nil && ctx.Err() != nil {
			return empty, ctx.Err()
		}
		if sendErr != nil && !errors.Is(sendErr, io.ErrClosedPipe) {
			return empty, ErrRemoteBundle
		}
		return empty, requestErr
	}
	if err := c.requireStatus(status, body, http.StatusAccepted); err != nil {
		return empty, err
	}
	if sendErr != nil && !errors.Is(sendErr, io.ErrClosedPipe) {
		return empty, ErrRemoteBundle
	}
	var result api.IngestionResponse
	if err := decodeRemoteJSON(body, &result); err != nil || !validIngestionEnvelope(result) {
		return empty, ErrRemoteProtocol
	}
	return result, nil
}

// GetIngestion retrieves one organization-scoped ingestion status.
func (c *RemoteClient) GetIngestion(ctx context.Context, id string) (api.IngestionResponse, error) {
	var empty api.IngestionResponse
	status, body, err := c.requestJSON(ctx, http.MethodGet, api.IngestionsPath+"/"+url.PathEscape(id), nil)
	if err != nil {
		return empty, err
	}
	if err := c.requireStatus(status, body, http.StatusOK); err != nil {
		return empty, err
	}
	var result api.IngestionResponse
	if err := decodeRemoteJSON(body, &result); err != nil || !validIngestionEnvelope(result) {
		return empty, ErrRemoteProtocol
	}
	return result, nil
}

// Ask executes one query through the versioned API. A terminal failed query
// is a valid QueryResponse with HTTP 500; a problem+json 500 remains an error.
func (c *RemoteClient) Ask(ctx context.Context, request api.QueryRequest) (api.QueryResponse, error) {
	var empty api.QueryResponse
	payload, err := json.Marshal(request)
	if err != nil {
		return empty, ErrRemoteProtocol
	}
	status, body, err := c.requestJSON(ctx, http.MethodPost, api.QueriesPath, payload)
	if err != nil {
		return empty, err
	}
	if status != http.StatusOK && status != http.StatusInternalServerError {
		if status >= 400 && status <= 599 {
			return empty, c.problem(status, body)
		}
		return empty, ErrRemoteProtocol
	}
	var result api.QueryResponse
	if err := decodeRemoteJSON(body, &result); err != nil || !validQueryEnvelope(result) {
		if status >= 400 {
			return empty, c.problem(status, body)
		}
		return empty, ErrRemoteProtocol
	}
	if status == http.StatusInternalServerError && result.State != api.QueryStateFailed {
		return empty, ErrRemoteProtocol
	}
	return result, nil
}

// GetEvidence retrieves one persisted Evidence Unit. Content is returned only
// when the API has already authorized it for local inspection.
func (c *RemoteClient) GetEvidence(ctx context.Context, id string) (api.EvidenceResponse, error) {
	var empty api.EvidenceResponse
	status, body, err := c.requestJSON(ctx, http.MethodGet, api.EvidencePath+"/"+url.PathEscape(id), nil)
	if err != nil {
		return empty, err
	}
	if err := c.requireStatus(status, body, http.StatusOK); err != nil {
		return empty, err
	}
	var result api.EvidenceResponse
	if err := decodeRemoteJSON(body, &result); err != nil || !validEvidenceEnvelope(result) {
		return empty, ErrRemoteProtocol
	}
	return result, nil
}

type remoteCommandFlags struct {
	server  *string
	url     *string
	format  *string
	json    *bool
	timeout *time.Duration
}

func addRemoteFlags(flagSet *flag.FlagSet) remoteCommandFlags {
	return remoteCommandFlags{
		server:  flagSet.String("server", defaultRemoteServerURL, "local Manu API URL"),
		url:     flagSet.String("url", "", "alias for --server"),
		format:  flagSet.String("format", "human", "output format: human or json"),
		json:    flagSet.Bool("json", false, "emit the response as JSON"),
		timeout: flagSet.Duration("timeout", defaultRemoteTimeout, "request timeout"),
	}
}

func (f remoteCommandFlags) open(ctx context.Context) (*RemoteClient, context.Context, context.CancelFunc, error) {
	server := strings.TrimSpace(*f.server)
	if strings.TrimSpace(*f.url) != "" {
		server = strings.TrimSpace(*f.url)
	}
	if *f.timeout <= 0 {
		return nil, nil, nil, ErrRemoteURL
	}
	client, err := remoteClientFactory(RemoteClientConfig{ServerURL: server, Timeout: *f.timeout})
	if err != nil {
		return nil, nil, nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	requestContext, cancel := context.WithTimeout(ctx, *f.timeout)
	return client, requestContext, cancel, nil
}

func (f remoteCommandFlags) outputFormat() (string, error) {
	return outputFormat(*f.format, *f.json)
}

func runIngest(runContext analysis.RunContext, args []string, stdout, stderr io.Writer) int {
	ctx, stop := contextWithSignals(runContext)
	defer stop()
	flagSet := newFlagSet("ingest", stderr)
	bundlePath := flagSet.String("bundle", "", "Analysis Bundle directory")
	remote := addRemoteFlags(flagSet)
	if err := flagSet.Parse(args); err != nil {
		return ExitUsage
	}
	if *bundlePath == "" && flagSet.NArg() == 1 {
		*bundlePath = flagSet.Arg(0)
	}
	if flagSet.NArg() > 1 || strings.TrimSpace(*bundlePath) == "" {
		fmt.Fprintln(stderr, "manu ingest: a bundle directory is required")
		return ExitUsage
	}
	if info, err := os.Stat(*bundlePath); err != nil || !info.IsDir() {
		fmt.Fprintln(stderr, "manu ingest: bundle must be an existing directory")
		return ExitUsage
	}
	format, err := remote.outputFormat()
	if err != nil {
		fmt.Fprintln(stderr, "manu ingest:", err)
		return ExitUsage
	}
	client, requestContext, cancel, err := remote.open(ctx)
	if err != nil {
		return writeRemoteCommandError(stderr, "manu ingest", err)
	}
	defer cancel()
	result, err := client.UploadBundle(requestContext, *bundlePath)
	if err != nil {
		return writeRemoteCommandError(stderr, "manu ingest", err)
	}
	if err := writeRemoteResult(stdout, format, func() error { return writeIngestionHuman(stdout, "ingestion accepted", result) }, result); err != nil {
		return ExitTechnical
	}
	return ingestionExitCode(string(result.State))
}

func runIngestionStatus(runContext analysis.RunContext, args []string, stdout, stderr io.Writer) int {
	ctx, stop := contextWithSignals(runContext)
	defer stop()
	flagSet := newFlagSet("ingestion", stderr)
	id := flagSet.String("id", "", "ingestion UUID")
	remote := addRemoteFlags(flagSet)
	if err := flagSet.Parse(args); err != nil {
		return ExitUsage
	}
	if *id == "" && flagSet.NArg() == 1 {
		*id = flagSet.Arg(0)
	}
	if flagSet.NArg() > 1 || !validRemoteID(*id) {
		fmt.Fprintln(stderr, "manu ingestion: a valid ingestion UUID is required")
		return ExitUsage
	}
	format, err := remote.outputFormat()
	if err != nil {
		fmt.Fprintln(stderr, "manu ingestion:", err)
		return ExitUsage
	}
	client, requestContext, cancel, err := remote.open(ctx)
	if err != nil {
		return writeRemoteCommandError(stderr, "manu ingestion", err)
	}
	defer cancel()
	result, err := client.GetIngestion(requestContext, *id)
	if err != nil {
		return writeRemoteCommandError(stderr, "manu ingestion", err)
	}
	if err := writeRemoteResult(stdout, format, func() error { return writeIngestionHuman(stdout, "ingestion status", result) }, result); err != nil {
		return ExitTechnical
	}
	return ingestionExitCode(string(result.State))
}

func runAsk(runContext analysis.RunContext, args []string, stdout, stderr io.Writer) int {
	ctx, stop := contextWithSignals(runContext)
	defer stop()
	flagSet := newFlagSet("ask", stderr)
	question := flagSet.String("question", "", "question to ask")
	kind := flagSet.String("kind", "", "knowledge kind: inventory, possible_flow, observed_execution, or business_intent")
	sourceID := flagSet.String("source-id", "", "optional source UUID")
	snapshotID := flagSet.String("snapshot-id", "", "optional snapshot UUID")
	remote := addRemoteFlags(flagSet)
	if err := flagSet.Parse(args); err != nil {
		return ExitUsage
	}
	if *question == "" && flagSet.NArg() == 1 {
		*question = flagSet.Arg(0)
	}
	if flagSet.NArg() > 1 || !validQuestion(*question) || !validRemoteQuestionKind(*kind) ||
		(*sourceID != "" && !validRemoteID(*sourceID)) ||
		(*snapshotID != "" && (!validRemoteID(*snapshotID) || *sourceID == "")) {
		fmt.Fprintln(stderr, "manu ask: question, kind, source and snapshot arguments are invalid")
		return ExitUsage
	}
	format, err := remote.outputFormat()
	if err != nil {
		fmt.Fprintln(stderr, "manu ask:", err)
		return ExitUsage
	}
	client, requestContext, cancel, err := remote.open(ctx)
	if err != nil {
		return writeRemoteCommandError(stderr, "manu ask", err)
	}
	defer cancel()
	result, err := client.Ask(requestContext, api.QueryRequest{
		Question:   *question,
		Kind:       domainquery.KnowledgeQuestionKind(*kind),
		SourceID:   *sourceID,
		SnapshotID: *snapshotID,
	})
	if err != nil {
		return writeRemoteCommandError(stderr, "manu ask", err)
	}
	if err := writeRemoteResult(stdout, format, func() error { return writeQueryHuman(stdout, result) }, result); err != nil {
		return ExitTechnical
	}
	switch result.State {
	case api.QueryStatePartial, api.QueryStateAbstained:
		return ExitPartial
	case api.QueryStateFailed:
		return ExitTechnical
	default:
		return ExitSuccess
	}
}

func runEvidence(runContext analysis.RunContext, args []string, stdout, stderr io.Writer) int {
	ctx, stop := contextWithSignals(runContext)
	defer stop()
	flagSet := newFlagSet("evidence", stderr)
	id := flagSet.String("id", "", "evidence UUID")
	remote := addRemoteFlags(flagSet)
	if err := flagSet.Parse(args); err != nil {
		return ExitUsage
	}
	if *id == "" && flagSet.NArg() == 1 {
		*id = flagSet.Arg(0)
	}
	if flagSet.NArg() > 1 || !validRemoteID(*id) {
		fmt.Fprintln(stderr, "manu evidence: a valid evidence UUID is required")
		return ExitUsage
	}
	format, err := remote.outputFormat()
	if err != nil {
		fmt.Fprintln(stderr, "manu evidence:", err)
		return ExitUsage
	}
	client, requestContext, cancel, err := remote.open(ctx)
	if err != nil {
		return writeRemoteCommandError(stderr, "manu evidence", err)
	}
	defer cancel()
	result, err := client.GetEvidence(requestContext, *id)
	if err != nil {
		return writeRemoteCommandError(stderr, "manu evidence", err)
	}
	if err := writeRemoteResult(stdout, format, func() error { return writeEvidenceHuman(stdout, result) }, result); err != nil {
		return ExitTechnical
	}
	return ExitSuccess
}

func writeRemoteResult(stdout io.Writer, format string, human func() error, value any) error {
	if format == "json" {
		return writeJSON(stdout, value)
	}
	return human()
}

func writeIngestionHuman(w io.Writer, heading string, response api.IngestionResponse) error {
	if _, err := fmt.Fprintln(w, heading); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "id: %s\n", response.ID); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "organization: %s\n", response.OrganizationID); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "state: %s\n", response.State); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "stage: %s\n", response.Stage); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "attempts: %d\n", response.AttemptCount); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "counts: artifacts=%d observations=%d evidence=%d failures=%d\n", response.Counts.ArtifactCount, response.Counts.ObservationCount, response.Counts.EvidenceCount, response.Counts.FailureCount); err != nil {
		return err
	}
	if response.DiagnosticCode != "" {
		_, err := fmt.Fprintf(w, "diagnostic: %s\n", response.DiagnosticCode)
		return err
	}
	return nil
}

func writeQueryHuman(w io.Writer, response api.QueryResponse) error {
	if _, err := fmt.Fprintf(w, "query %s\n", response.State); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "id: %s\n", response.ID); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "organization: %s\n", response.OrganizationID); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "question_digest: %s\n", response.QuestionDigest); err != nil {
		return err
	}
	if response.PackageDigest != "" {
		if _, err := fmt.Fprintf(w, "package_digest: %s\n", response.PackageDigest); err != nil {
			return err
		}
	}
	if response.DiagnosticCode != "" {
		if _, err := fmt.Fprintf(w, "diagnostic: %s\n", response.DiagnosticCode); err != nil {
			return err
		}
	}
	if len(response.Result) > 0 {
		if _, err := fmt.Fprintln(w, "result:"); err != nil {
			return err
		}
		if err := writeIndentedJSON(w, response.Result); err != nil {
			return err
		}
	}
	return nil
}

func writeEvidenceHuman(w io.Writer, response api.EvidenceResponse) error {
	if _, err := fmt.Fprintln(w, "evidence"); err != nil {
		return err
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{name: "id", value: response.ID},
		{name: "organization", value: response.OrganizationID},
		{name: "source", value: response.SourceID},
		{name: "snapshot", value: response.SnapshotID},
		{name: "artifact", value: response.ArtifactID},
		{name: "content_state", value: string(response.ContentState)},
		{name: "classification", value: string(response.Classification)},
		{name: "persist", value: string(response.Persist)},
		{name: "external_transfer", value: string(response.ExternalTransfer)},
		{name: "content_hash", value: response.ContentHash},
	} {
		if _, err := fmt.Fprintf(w, "%s: %s\n", field.name, field.value); err != nil {
			return err
		}
	}
	if response.Content != "" {
		if _, err := fmt.Fprintln(w, "content:"); err != nil {
			return err
		}
		if _, err := fmt.Fprintln(w, response.Content); err != nil {
			return err
		}
	}
	return nil
}

func writeIndentedJSON(w io.Writer, raw json.RawMessage) error {
	var buffer bytes.Buffer
	if err := json.Indent(&buffer, raw, "", "  "); err != nil {
		return ErrRemoteProtocol
	}
	_, err := fmt.Fprintln(w, buffer.String())
	return err
}

func writeRemoteCommandError(w io.Writer, command string, err error) int {
	switch {
	case errors.Is(err, ErrRemoteURL):
		fmt.Fprintf(w, "%s: server URL must be explicit HTTP loopback\n", command)
		return ExitUsage
	case errors.Is(err, ErrRemoteBundle):
		fmt.Fprintf(w, "%s: bundle is invalid or cannot be streamed\n", command)
		return ExitUsage
	case errors.Is(err, context.Canceled):
		fmt.Fprintf(w, "%s: operation canceled\n", command)
		return ExitTechnical
	case errors.Is(err, context.DeadlineExceeded):
		fmt.Fprintf(w, "%s: operation timed out\n", command)
		return ExitTechnical
	case errors.Is(err, ErrRemoteUnavailable):
		fmt.Fprintf(w, "%s: API is unavailable\n", command)
		return ExitTechnical
	case errors.Is(err, ErrRemoteProtocol):
		fmt.Fprintf(w, "%s: API returned an invalid response\n", command)
		return ExitTechnical
	default:
		var problem *ProblemError
		if errors.As(err, &problem) {
			fmt.Fprintf(w, "%s: API problem status=%d code=%s\n", command, problem.Status, problem.Code)
			return ExitTechnical
		}
		fmt.Fprintf(w, "%s: request failed\n", command)
		return ExitTechnical
	}
}

func decodeRemoteJSON(raw []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(destination); err != nil {
		return ErrRemoteProtocol
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return ErrRemoteProtocol
	}
	return nil
}

func validIngestionEnvelope(response api.IngestionResponse) bool {
	if response.Version != api.APIVersion || !validRemoteID(response.ID) || response.OrganizationID == "" ||
		!response.Stage.Valid() || response.AttemptCount < 0 {
		return false
	}
	if response.Counts.ArtifactCount < 0 || response.Counts.ObservationCount < 0 ||
		response.Counts.EvidenceCount < 0 || response.Counts.FailureCount < 0 {
		return false
	}
	switch string(response.State) {
	case "pending", "running", "completed", "partial", "failed":
		return true
	default:
		return false
	}
}

func validQueryEnvelope(response api.QueryResponse) bool {
	return response.Version == api.APIVersion && validRemoteID(response.ID) &&
		response.OrganizationID != "" && response.State.Terminal() && response.QuestionDigest != ""
}

func validEvidenceEnvelope(response api.EvidenceResponse) bool {
	return response.Version == api.APIVersion && validRemoteID(response.ID) && response.OrganizationID != ""
}

func validQuestion(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && !strings.ContainsAny(value, "\x00\r\n") && len(value) <= 16<<10
}

func validRemoteQuestionKind(value string) bool {
	switch domainquery.KnowledgeQuestionKind(value) {
	case domainquery.KnowledgeQuestionInventory,
		domainquery.KnowledgeQuestionPossibleFlow,
		domainquery.KnowledgeQuestionObservedExecution,
		domainquery.KnowledgeQuestionBusinessIntent:
		return true
	default:
		return false
	}
}

func validRemoteID(value string) bool {
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

func safeRemoteCode(value string) string {
	if value == "" || len(value) > 64 {
		return "http_error"
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') ||
			(character >= '0' && character <= '9') || character == '_' || character == '-' || character == '.' {
			continue
		}
		return "http_error"
	}
	return value
}

func ingestionExitCode(state string) int {
	switch state {
	case "partial":
		return ExitPartial
	case "failed":
		return ExitTechnical
	default:
		return ExitSuccess
	}
}
