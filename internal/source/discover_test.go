package source

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"
	"time"
)

func TestDiscoverReadOnlyAndBoundedSelection(t *testing.T) {
	root := t.TempDir()
	files := map[string][]byte{
		"src/main.go":              []byte("package main\nfunc main() {}\n"),
		"src/readme.txt":           []byte("documentação segura\n"),
		"bin/data.bin":             {0x00, 0x01, 0x02, 0xff},
		"config/.env":              []byte("TOKEN=must-not-be-read\n"),
		"config/client-secret.yml": []byte("password: must-not-be-read\n"),
		"ignored/skip.txt":         []byte("excluded\n"),
	}
	paths := make(map[string]string, len(files))
	for relativePath, data := range files {
		filePath := filepath.Join(root, filepath.FromSlash(relativePath))
		if err := os.MkdirAll(filepath.Dir(filePath), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filePath, data, 0o640); err != nil {
			t.Fatal(err)
		}
		paths[relativePath] = filePath
	}
	linkTarget := filepath.Join(root, "src", "main.go")
	linkPath := filepath.Join(root, "src", "link.go")
	if err := os.Symlink(linkTarget, linkPath); err != nil {
		t.Skipf("symbolic links are unavailable: %v", err)
	}

	type fileSnapshot struct {
		mode os.FileMode
		data []byte
	}
	snapshot := make(map[string]fileSnapshot, len(paths))
	for relativePath, filePath := range paths {
		info, err := os.Stat(filePath)
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(filePath)
		if err != nil {
			t.Fatal(err)
		}
		snapshot[relativePath] = fileSnapshot{mode: info.Mode(), data: data}
	}

	result, err := Discover(context.Background(), Config{
		Root:     root,
		Includes: []string{"src/**", "bin/**", "config/**", "ignored/**"},
		Excludes: []string{"ignored/**"},
		Limits: Limits{
			MaxConcurrency: 3,
			MaxProbeBytes:  64,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Root != root {
		t.Fatalf("result root = %q, want %q", result.Root, root)
	}
	if result.Concurrency != 3 {
		t.Fatalf("result concurrency = %d, want 3", result.Concurrency)
	}
	gotPaths := make([]string, 0, len(result.Artifacts))
	for _, artifact := range result.Artifacts {
		gotPaths = append(gotPaths, artifact.RelativePath)
		if artifact.SHA256 == "" {
			t.Fatalf("artifact %q has no SHA-256", artifact.RelativePath)
		}
		if artifact.RelativePath == "config/.env" || artifact.RelativePath == "config/client-secret.yml" {
			t.Fatalf("sensitive artifact was analyzed: %q", artifact.RelativePath)
		}
		if artifact.RelativePath == "src/link.go" {
			t.Fatalf("symbolic link was analyzed")
		}
	}
	sort.Strings(gotPaths)
	wantPaths := []string{"bin/data.bin", "src/main.go", "src/readme.txt"}
	if len(gotPaths) != len(wantPaths) {
		t.Fatalf("artifact paths = %v, want %v", gotPaths, wantPaths)
	}
	for index := range wantPaths {
		if gotPaths[index] != wantPaths[index] {
			t.Fatalf("artifact paths = %v, want %v", gotPaths, wantPaths)
		}
	}
	if len(result.Exclusions) < 3 {
		t.Fatalf("exclusions = %v, want sensitive and configured exclusions", result.Exclusions)
	}
	if !hasFailureCode(result.Failures, failureCodeSymlink) {
		t.Fatalf("failures = %v, want symbolic-link failure", result.Failures)
	}

	for relativePath, expected := range snapshot {
		filePath := paths[relativePath]
		info, err := os.Stat(filePath)
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(filePath)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode() != expected.mode || string(data) != string(expected.data) {
			t.Fatalf("source file %q changed during discovery", relativePath)
		}
	}
}

func TestDiscoverRejectsSymlinkRootAndTraversalPatterns(t *testing.T) {
	root := t.TempDir()
	link := filepath.Join(t.TempDir(), "root-link")
	if err := os.Symlink(root, link); err != nil {
		t.Skipf("symbolic links are unavailable: %v", err)
	}
	if _, err := Discover(context.Background(), Config{Root: link}); !errors.Is(err, ErrSymlink) {
		t.Fatalf("Discover(symlink root) error = %v, want ErrSymlink", err)
	}
	if _, err := Discover(context.Background(), Config{
		Root:     root,
		Includes: []string{"../outside/**"},
	}); !errors.Is(err, ErrPathTraversal) {
		t.Fatalf("Discover(traversal include) error = %v, want ErrPathTraversal", err)
	}
}

func TestDiscoverLimitsPreserveCompletedArtifacts(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	result, err := Discover(context.Background(), Config{
		Root: root,
		Limits: Limits{
			MaxFiles:       1,
			MaxConcurrency: 1,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Limited {
		t.Fatalf("result limited = false, want true; result = %+v", result)
	}
	if len(result.Artifacts) != 1 {
		t.Fatalf("artifacts = %d, want one completed artifact", len(result.Artifacts))
	}
	if !hasFailureCode(result.Failures, failureCodeLimitFiles) {
		t.Fatalf("failures = %v, want max-files failure", result.Failures)
	}

	result, err = Discover(context.Background(), Config{
		Root: root,
		Limits: Limits{
			MaxBytes:       int64(len("a.txt")),
			MaxConcurrency: 1,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Limited || !hasFailureCode(result.Failures, failureCodeLimitBytes) {
		t.Fatalf("byte limit result = %+v, want limit failure", result)
	}
}

func TestDiscoverMaxBytesCapsActualIO(t *testing.T) {
	root := t.TempDir()
	content := []byte("0123456789abcdef")
	if err := os.WriteFile(filepath.Join(root, "payload.txt"), content, 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := Discover(context.Background(), Config{
		Root: root,
		Limits: Limits{
			MaxBytes:      int64(len(content)),
			MaxProbeBytes: 8,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Limited {
		t.Fatalf("result unexpectedly limited at exact byte budget: %+v", result)
	}
	if result.BytesRead != int64(len(content)) || result.BytesRead > int64(len(content)) {
		t.Fatalf("bytes read = %d, want exactly %d and never above MaxBytes", result.BytesRead, len(content))
	}

	result, err = Discover(context.Background(), Config{
		Root: root,
		Limits: Limits{
			MaxBytes:      int64(len(content) - 1),
			MaxProbeBytes: 8,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Limited || result.BytesRead > int64(len(content)-1) {
		t.Fatalf("tight byte budget result = %+v, want limited without exceeding budget", result)
	}
}

func TestDiscoverCountsPartialReadOnCancellation(t *testing.T) {
	rootPath := t.TempDir()
	data := bytes.Repeat([]byte("x"), 64*1024)
	filePath := filepath.Join(rootPath, "payload.bin")
	if err := os.WriteFile(filePath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	state := &discoveryState{result: DiscoveryResult{}}
	processCandidate(
		&cancelAfterFirstReadContext{Context: context.Background()},
		root,
		discoveryCandidate{
			path:         filePath,
			relativePath: "payload.bin",
			size:         int64(len(data)),
		},
		Limits{MaxProbeBytes: 64},
		state,
		func() {},
	)

	if state.result.BytesRead == 0 {
		t.Fatalf("partial cancellation recorded no bytes: %+v", state.result)
	}
	if state.result.BytesRead > int64(len(data)) {
		t.Fatalf("partial cancellation over-counted bytes: %+v", state.result)
	}
	if len(state.result.Artifacts) != 0 {
		t.Fatalf("cancelled inspection produced an artifact: %+v", state.result.Artifacts)
	}
	if !hasFailureCode(state.result.Failures, failureCodeCancelled) {
		t.Fatalf("failures = %v, want cancellation", state.result.Failures)
	}
}

type cancelAfterFirstReadContext struct {
	context.Context
	checks int
}

func (c *cancelAfterFirstReadContext) Err() error {
	c.checks++
	if c.checks > 1 {
		return context.Canceled
	}
	return nil
}

func TestRootOpenConfinementAndSymlinkRevalidation(t *testing.T) {
	rootPath := t.TempDir()
	outsidePath := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outsidePath, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	linkPath := filepath.Join(rootPath, "link.txt")
	if err := os.Symlink(outsidePath, linkPath); err != nil {
		t.Skipf("symbolic links are unavailable: %v", err)
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if _, err := root.Open("link.txt"); err == nil {
		t.Fatal("os.Root.Open unexpectedly followed a link outside the root")
	}
	if _, err := openRegularRoot(root, "link.txt"); !errors.Is(err, ErrSymlink) {
		t.Fatalf("openRegularRoot(link) error = %v, want ErrSymlink", err)
	}
	if _, err := HashFileInRoot(context.Background(), root, "link.txt", 64); !errors.Is(err, ErrSymlink) {
		t.Fatalf("HashFileInRoot(link) error = %v, want ErrSymlink", err)
	}
	if _, _, _, err := inspectRootFile(context.Background(), root, "link.txt", 64, 64); !errors.Is(err, ErrSymlink) {
		t.Fatalf("inspectRootFile(link) error = %v, want ErrSymlink", err)
	}
}

func TestDiscoverCancellationAndDeadline(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "file.txt"), []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	before := runtime.NumGoroutine()
	result, err := Discover(ctx, Config{Root: root})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Discover(cancelled) error = %v, want context.Canceled", err)
	}
	if !result.Cancelled {
		t.Fatalf("result cancelled = false, want true; result = %+v", result)
	}
	waitForGoroutines(t, before)

	deadlineResult, err := Discover(context.Background(), Config{
		Root: root,
		Limits: Limits{
			MaxDuration: 1 * time.Nanosecond,
		},
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Discover(deadline) error = %v, want context deadline exceeded", err)
	}
	if !deadlineResult.Cancelled {
		t.Fatalf("deadline result cancelled = false, want true")
	}
}

func TestHashFileStreamsAndLimits(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "payload.txt")
	data := []byte("streamed payload\n")
	if err := os.WriteFile(filePath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	wantDigest := sha256.Sum256(data)
	got, err := HashFile(context.Background(), filePath, int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if got.SHA256 != hex.EncodeToString(wantDigest[:]) || got.BytesRead != int64(len(data)) {
		t.Fatalf("hash result = %+v, want digest and %d bytes", got, len(data))
	}
	if _, err := HashFile(context.Background(), filePath, int64(len(data)-1)); !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("HashFile(limit) error = %v, want ErrLimitExceeded", err)
	}
}

func hasFailureCode(failures []Failure, code string) bool {
	for _, failure := range failures {
		if failure.Code == code {
			return true
		}
	}
	return false
}

func waitForGoroutines(t *testing.T, before int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= before+1 {
			return
		}
		runtime.Gosched()
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("goroutines did not settle: before=%d after=%d", before, runtime.NumGoroutine())
}
