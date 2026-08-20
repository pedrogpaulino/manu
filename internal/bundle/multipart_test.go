package bundle_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/textproto"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pedrogpaulino/manu/internal/bundle"
	"github.com/pedrogpaulino/manu/internal/contract"
)

func TestMultipartRoundTripAvailableAndLimited(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		available      bool
		expectParts    int
		expectEvidence bundle.EvidenceState
	}{
		{name: "available", available: true, expectParts: 4, expectEvidence: bundle.EvidenceStateAvailable},
		{name: "limited", available: false, expectParts: 3, expectEvidence: bundle.EvidenceStateLimited},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			directory := t.TempDir()
			input := validBundle()
			if !tt.available {
				input.Evidence = nil
				input.Manifest.Evidence = bundle.EvidenceMetadata{State: bundle.EvidenceStateLimited}
			}
			if err := bundle.WriteBundle(context.Background(), directory, input); err != nil {
				t.Fatalf("WriteBundle() error = %v", err)
			}

			sender, err := bundle.NewMultipartSender(directory, bundle.MultipartWriteOptions{Boundary: "manu-test-boundary"})
			if err != nil {
				t.Fatalf("NewMultipartSender() error = %v", err)
			}
			if sender.ContentType() != `multipart/form-data; boundary=manu-test-boundary` {
				t.Fatalf("ContentType() = %q", sender.ContentType())
			}
			var body bytes.Buffer
			written, err := sender.Send(context.Background(), &body)
			if err != nil {
				t.Fatalf("Send() error = %v", err)
			}
			if written.FactualDigest == "" || written.FactualDigest != written.Digest {
				t.Fatalf("sender metadata digest = %#v", written)
			}
			if got := countMultipartParts(t, body.Bytes(), sender.ContentType()); got != tt.expectParts {
				t.Fatalf("multipart part count = %d, want %d", got, tt.expectParts)
			}

			got, received, err := bundle.ReadMultipart(
				context.Background(),
				&chunkReader{reader: bytes.NewReader(body.Bytes()), chunk: 3},
				sender.ContentType(),
				bundle.MultipartReadOptions{},
			)
			if err != nil {
				t.Fatalf("ReadMultipart() error = %v", err)
			}
			if got.Manifest.Evidence.State != tt.expectEvidence {
				t.Fatalf("evidence state = %q, want %q", got.Manifest.Evidence.State, tt.expectEvidence)
			}
			if received.FactualDigest != written.FactualDigest || received.Bytes != written.Bytes || received.Counts != written.Counts {
				t.Fatalf("metadata mismatch:\n sent=%#v\nreceived=%#v", written, received)
			}
			if err := got.Validate(); err != nil {
				t.Fatalf("received validation error = %v", err)
			}
			if !reflect.DeepEqual(got.Artifacts, input.Artifacts) || !reflect.DeepEqual(got.Contributions, input.Contributions) || !reflect.DeepEqual(got.Evidence, input.Evidence) {
				t.Fatal("round-trip sequences differ")
			}
		})
	}
}

func TestStageMultipartRetainsBoundedPayloadForLaterRead(t *testing.T) {
	t.Parallel()
	_, sender, body := multipartFixture(t, true)
	root := t.TempDir()
	staged, err := bundle.StageMultipart(
		context.Background(), bytes.NewReader(body.Bytes()), sender.ContentType(), root,
		bundle.MultipartReadOptions{OrganizationID: "organization-1"},
	)
	if err != nil {
		t.Fatalf("StageMultipart() error = %v", err)
	}
	if staged.Directory == "" || staged.Directory == root {
		t.Fatalf("staged directory = %q, want private child", staged.Directory)
	}
	for _, name := range []string{bundle.ManifestFileName, bundle.ArtifactsFileName, bundle.ContributionsFileName, bundle.EvidenceFileName} {
		if _, err := os.Stat(filepath.Join(staged.Directory, name)); err != nil {
			t.Fatalf("staged %s: %v", name, err)
		}
	}
	if _, err := bundle.ReadBundle(context.Background(), staged.Directory, bundle.Options{OrganizationID: "organization-1"}); err != nil {
		t.Fatalf("ReadBundle(staged) error = %v", err)
	}
	if err := staged.Remove(); err != nil {
		t.Fatalf("StagedMultipart.Remove() error = %v", err)
	}
	if _, err := os.Stat(staged.Directory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staged directory after Remove() error = %v, want not exist", err)
	}
}

func TestMultipartRejectsLegacyContract(t *testing.T) {
	t.Parallel()

	input := validBundle()
	directory := t.TempDir()
	legacy := contract.Result{
		Manifest:      input.Manifest.Manifest,
		Artifacts:     input.Artifacts,
		Contributions: input.Contributions,
	}
	if err := contract.WriteResult(context.Background(), directory, legacy); err != nil {
		t.Fatalf("WriteResult() error = %v", err)
	}
	options := bundle.MultipartWriteOptions{Boundary: "legacy-test-boundary"}
	sender, err := bundle.NewMultipartSender(directory, options)
	if err != nil {
		t.Fatalf("NewMultipartSender() error = %v", err)
	}
	var body bytes.Buffer
	if _, err := sender.Send(context.Background(), &body); !errors.Is(err, bundle.ErrUnsupportedVersion) {
		t.Fatalf("Send() error = %v, want unsupported version", err)
	}
}

func TestMultipartRejectsDigestCountAndLimitTampering(t *testing.T) {
	t.Parallel()

	directory, sender, body := multipartFixture(t, true)
	_ = directory
	parts := parseMultipartParts(t, body.Bytes(), sender.ContentType())

	t.Run("digest", func(t *testing.T) {
		mutated := cloneMultipartParts(parts)
		for i := range mutated {
			if mutated[i].name == bundle.MultipartArtifactsPartName {
				mutated[i].payload[0] ^= 1
				break
			}
		}
		bodyBytes, contentType := encodeMultipartParts(t, sender.ContentType(), mutated)
		_, _, err := bundle.ReadMultipart(context.Background(), bytes.NewReader(bodyBytes), contentType, bundle.MultipartReadOptions{})
		if !errors.Is(err, bundle.ErrDigestMismatch) {
			t.Fatalf("ReadMultipart() error = %v, want digest mismatch", err)
		}
	})

	t.Run("count", func(t *testing.T) {
		mutated := cloneMultipartParts(parts)
		var manifest map[string]any
		if err := json.Unmarshal(mutated[0].payload, &manifest); err != nil {
			t.Fatalf("decode manifest: %v", err)
		}
		counts := manifest["counts"].(map[string]any)
		counts["artifact_count"] = float64(2)
		manifest["artifact_count"] = float64(2)
		files := manifest["files"].([]any)
		files[0].(map[string]any)["count"] = float64(2)
		mutated[0].payload, _ = json.Marshal(manifest)
		bodyBytes, contentType := encodeMultipartParts(t, sender.ContentType(), mutated)
		_, _, err := bundle.ReadMultipart(context.Background(), bytes.NewReader(bodyBytes), contentType, bundle.MultipartReadOptions{})
		if !errors.Is(err, bundle.ErrCountMismatch) {
			t.Fatalf("ReadMultipart() error = %v, want count mismatch", err)
		}
	})

	t.Run("limit", func(t *testing.T) {
		_, _, err := bundle.ReadMultipart(context.Background(), bytes.NewReader(body.Bytes()), sender.ContentType(), bundle.MultipartReadOptions{
			Limits: bundle.Limits{MaxManifestBytes: 1},
		})
		if !errors.Is(err, bundle.ErrLimitExceeded) {
			t.Fatalf("ReadMultipart() error = %v, want limit exceeded", err)
		}
	})
}

func TestMultipartRejectsPartOrderDuplicateMissingAndTraversal(t *testing.T) {
	t.Parallel()

	_, sender, body := multipartFixture(t, true)
	parts := parseMultipartParts(t, body.Bytes(), sender.ContentType())

	tests := []struct {
		name   string
		mutate func([]multipartPartFixture) []multipartPartFixture
		want   error
	}{
		{
			name: "duplicate",
			mutate: func(value []multipartPartFixture) []multipartPartFixture {
				return append(value, value[1])
			},
			want: bundle.ErrMultipartPart,
		},
		{
			name: "unexpected",
			mutate: func(value []multipartPartFixture) []multipartPartFixture {
				value[1].name = "unexpected.ndjson"
				value[1].filename = "unexpected.ndjson"
				return value
			},
			want: bundle.ErrMultipartPart,
		},
		{
			name: "traversal",
			mutate: func(value []multipartPartFixture) []multipartPartFixture {
				value[1].filename = "../artifacts.ndjson"
				return value
			},
			want: bundle.ErrMultipartTraversal,
		},
		{
			name: "missing",
			mutate: func(value []multipartPartFixture) []multipartPartFixture {
				return value[:len(value)-1]
			},
			want: bundle.ErrMultipartPart,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mutated := tt.mutate(cloneMultipartParts(parts))
			bodyBytes, contentType := encodeMultipartParts(t, sender.ContentType(), mutated)
			_, _, err := bundle.ReadMultipart(context.Background(), bytes.NewReader(bodyBytes), contentType, bundle.MultipartReadOptions{})
			if !errors.Is(err, tt.want) {
				t.Fatalf("ReadMultipart() error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestMultipartErrorsDoNotEchoPayload(t *testing.T) {
	t.Parallel()

	_, sender, body := multipartFixture(t, true)
	parts := parseMultipartParts(t, body.Bytes(), sender.ContentType())
	const secret = "prompt-injection=do-not-echo"
	for index := range parts {
		if parts[index].name == bundle.MultipartArtifactsPartName {
			parts[index].payload = []byte(secret)
			break
		}
	}
	bodyBytes, contentType := encodeMultipartParts(t, sender.ContentType(), parts)
	_, _, err := bundle.ReadMultipart(context.Background(), bytes.NewReader(bodyBytes), contentType, bundle.MultipartReadOptions{})
	if err == nil {
		t.Fatal("ReadMultipart() accepted tampered payload")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("multipart error echoed payload: %v", err)
	}
}

func TestMultipartCancellationAndFragmentedReader(t *testing.T) {
	t.Parallel()

	directory, sender, body := multipartFixture(t, true)
	_ = directory
	var chunkedBody bytes.Buffer
	if _, err := sender.Send(context.Background(), &chunkingWriter{destination: &chunkedBody, chunk: 5}); err != nil {
		t.Fatalf("chunked Send() error = %v", err)
	}
	if !bytes.Equal(chunkedBody.Bytes(), body.Bytes()) {
		t.Fatal("chunked sender changed the multipart body")
	}
	reader := &chunkReader{reader: bytes.NewReader(body.Bytes()), chunk: 2}
	got, _, err := bundle.ReadMultipart(context.Background(), reader, sender.ContentType(), bundle.MultipartReadOptions{})
	if err != nil {
		t.Fatalf("fragmented ReadMultipart() error = %v", err)
	}
	if got.Manifest.FactualDigest == "" || reader.maxRead > 2 {
		t.Fatalf("reader consumed oversized chunks or lost digest: max=%d", reader.maxRead)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var destination bytes.Buffer
	if _, err := sender.Send(ctx, &destination); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Send() error = %v, want context canceled", err)
	}
	if destination.Len() != 0 {
		t.Fatalf("canceled sender wrote %d bytes", destination.Len())
	}

	ctx, cancel = context.WithCancel(context.Background())
	var cancelingDestination cancelWriter
	cancelingDestination.cancel = cancel
	if _, err := sender.Send(ctx, &cancelingDestination); !errors.Is(err, context.Canceled) {
		t.Fatalf("sender cancellation during copy error = %v, want context canceled", err)
	}

	ctx, cancel = context.WithCancel(context.Background())
	canceling := &cancelReader{reader: bytes.NewReader(body.Bytes()), cancel: cancel, afterReads: 2, chunk: 2}
	if _, _, err := bundle.ReadMultipart(ctx, canceling, sender.ContentType(), bundle.MultipartReadOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled ReadMultipart() error = %v, want context canceled", err)
	}
}

func TestMultipartSenderRejectsSymlinkAndPreservesSource(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	if err := bundle.WriteBundle(context.Background(), directory, validBundle()); err != nil {
		t.Fatalf("WriteBundle() error = %v", err)
	}
	names := []string{bundle.ManifestFileName, bundle.ArtifactsFileName, bundle.ContributionsFileName, bundle.EvidenceFileName}
	type sourceSnapshot struct {
		bytes []byte
		mode  os.FileMode
		mtime time.Time
	}
	before := make(map[string]sourceSnapshot, len(names))
	for _, name := range names {
		path := filepath.Join(directory, name)
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", name, err)
		}
		before[name] = sourceSnapshot{bytes: content, mode: info.Mode(), mtime: info.ModTime()}
	}
	sender, err := bundle.NewMultipartSender(directory, bundle.MultipartWriteOptions{Boundary: "source-test-boundary"})
	if err != nil {
		t.Fatalf("NewMultipartSender() error = %v", err)
	}
	var body bytes.Buffer
	if _, err := sender.Send(context.Background(), &body); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	for _, name := range names {
		path := filepath.Join(directory, name)
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read after send %s: %v", name, err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat after send %s: %v", name, err)
		}
		want := before[name]
		if !bytes.Equal(content, want.bytes) || info.Mode() != want.mode || !info.ModTime().Equal(want.mtime) {
			t.Fatalf("source %s changed", name)
		}
	}

	symlinkDirectory := t.TempDir()
	if err := bundle.WriteBundle(context.Background(), symlinkDirectory, validBundle()); err != nil {
		t.Fatalf("WriteBundle(symlink) error = %v", err)
	}
	artifactsPath := filepath.Join(symlinkDirectory, bundle.ArtifactsFileName)
	backupPath := filepath.Join(symlinkDirectory, "artifacts-source")
	if err := os.Rename(artifactsPath, backupPath); err != nil {
		t.Fatalf("rename artifacts: %v", err)
	}
	if err := os.Symlink(backupPath, artifactsPath); err != nil {
		t.Fatalf("symlink artifacts: %v", err)
	}
	symlinkSender, err := bundle.NewMultipartSender(symlinkDirectory, bundle.MultipartWriteOptions{Boundary: "symlink-test-boundary"})
	if err != nil {
		t.Fatalf("NewMultipartSender(symlink) error = %v", err)
	}
	if _, err := symlinkSender.Send(context.Background(), io.Discard); !errors.Is(err, bundle.ErrInvalidFile) {
		t.Fatalf("Send(symlink) error = %v, want invalid file", err)
	}
}

func multipartFixture(t *testing.T, available bool) (string, *bundle.MultipartSender, *bytes.Buffer) {
	t.Helper()
	directory := t.TempDir()
	input := validBundle()
	if !available {
		input.Evidence = nil
		input.Manifest.Evidence = bundle.EvidenceMetadata{State: bundle.EvidenceStateLimited}
	}
	if err := bundle.WriteBundle(context.Background(), directory, input); err != nil {
		t.Fatalf("WriteBundle() error = %v", err)
	}
	sender, err := bundle.NewMultipartSender(directory, bundle.MultipartWriteOptions{Boundary: "fixture-test-boundary"})
	if err != nil {
		t.Fatalf("NewMultipartSender() error = %v", err)
	}
	body := new(bytes.Buffer)
	if _, err := sender.Send(context.Background(), body); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	return directory, sender, body
}

type multipartPartFixture struct {
	name      string
	filename  string
	mediaType string
	payload   []byte
}

func parseMultipartParts(t *testing.T, body []byte, contentType string) []multipartPartFixture {
	t.Helper()
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil || mediaType != "multipart/form-data" {
		t.Fatalf("parse content type: %v", err)
	}
	reader := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	var result []multipartPartFixture
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			return result
		}
		if err != nil {
			t.Fatalf("read part: %v", err)
		}
		var payloadBuffer bytes.Buffer
		if _, err := io.Copy(&payloadBuffer, part); err != nil {
			t.Fatalf("read part payload: %v", err)
		}
		_, typeParams, _ := mime.ParseMediaType(part.Header.Get("Content-Type"))
		_ = typeParams
		_, dispositionParams, _ := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
		result = append(result, multipartPartFixture{
			name:      dispositionParams["name"],
			filename:  dispositionParams["filename"],
			mediaType: part.Header.Get("Content-Type"),
			payload:   payloadBuffer.Bytes(),
		})
	}
}

func encodeMultipartParts(t *testing.T, contentType string, parts []multipartPartFixture) ([]byte, string) {
	t.Helper()
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil || mediaType != "multipart/form-data" {
		t.Fatalf("parse content type: %v", err)
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.SetBoundary(params["boundary"]); err != nil {
		t.Fatalf("set boundary: %v", err)
	}
	for _, fixture := range parts {
		header := make(textproto.MIMEHeader)
		header.Set("Content-Disposition", `form-data; name="`+fixture.name+`"; filename="`+fixture.filename+`"`)
		header.Set("Content-Type", fixture.mediaType)
		part, err := writer.CreatePart(header)
		if err != nil {
			t.Fatalf("create part: %v", err)
		}
		if _, err := part.Write(fixture.payload); err != nil {
			t.Fatalf("write part: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart: %v", err)
	}
	return body.Bytes(), contentType
}

func cloneMultipartParts(parts []multipartPartFixture) []multipartPartFixture {
	result := make([]multipartPartFixture, len(parts))
	for i := range parts {
		result[i] = parts[i]
		result[i].payload = append([]byte(nil), parts[i].payload...)
	}
	return result
}

func countMultipartParts(t *testing.T, body []byte, contentType string) int {
	return len(parseMultipartParts(t, body, contentType))
}

type chunkReader struct {
	reader  io.Reader
	chunk   int
	maxRead int
}

func (r *chunkReader) Read(p []byte) (int, error) {
	if len(p) > r.chunk {
		p = p[:r.chunk]
	}
	n, err := r.reader.Read(p)
	if n > r.maxRead {
		r.maxRead = n
	}
	return n, err
}

type cancelReader struct {
	reader     io.Reader
	cancel     context.CancelFunc
	afterReads int
	reads      int
	chunk      int
}

type cancelWriter struct {
	cancel context.CancelFunc
}

func (w *cancelWriter) Write(value []byte) (int, error) {
	w.cancel()
	return len(value), nil
}

type chunkingWriter struct {
	destination io.Writer
	chunk       int
}

func (w *chunkingWriter) Write(value []byte) (int, error) {
	total := len(value)
	for len(value) > 0 {
		size := w.chunk
		if size > len(value) {
			size = len(value)
		}
		written, err := w.destination.Write(value[:size])
		if err != nil {
			return 0, err
		}
		if written != size {
			return 0, io.ErrShortWrite
		}
		value = value[size:]
	}
	return total, nil
}

func (r *cancelReader) Read(p []byte) (int, error) {
	if r.chunk > 0 && len(p) > r.chunk {
		p = p[:r.chunk]
	}
	r.reads++
	if r.reads > r.afterReads {
		r.cancel()
	}
	return r.reader.Read(p)
}
