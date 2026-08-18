package contract

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const (
	// ManifestFileName is the canonical manifest filename in a result
	// directory.
	ManifestFileName = "manifest.json"
	// ArtifactsFileName is the canonical artifact sequence filename.
	ArtifactsFileName = "artifacts.ndjson"
	// ContributionsFileName is the canonical contribution sequence filename.
	ContributionsFileName = "contributions.ndjson"
)

var (
	// ErrIncompatibleVersion indicates that a result belongs to another
	// contract version and requires an explicit migration.
	ErrIncompatibleVersion = errors.New("contract: incompatible version")
	// ErrInvalidResult indicates malformed or incomplete contract data.
	ErrInvalidResult = errors.New("contract: invalid result")
)

// VersionError identifies the version found in an incompatible result.
type VersionError struct {
	Expected string
	Actual   string
}

// Error implements error.
func (e *VersionError) Error() string {
	return fmt.Sprintf("contract: incompatible version %q (expected %q)", e.Actual, e.Expected)
}

// Is allows errors.Is to recognize ErrIncompatibleVersion.
func (e *VersionError) Is(target error) bool { return target == ErrIncompatibleVersion }

// WriteManifest atomically writes one validated manifest as indented JSON.
func WriteManifest(ctx context.Context, path string, manifest Manifest) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if manifest.Coverage == nil {
		manifest.Coverage = []Coverage{}
	}
	if manifest.Gaps == nil {
		manifest.Gaps = []Gap{}
	}
	if manifest.Failures == nil {
		manifest.Failures = []Failure{}
	}
	if err := manifest.validateVersionAndShape(); err != nil {
		return fmt.Errorf("writing manifest: %w", err)
	}
	return writeAtomic(ctx, path, func(w io.Writer) error {
		encoder := json.NewEncoder(w)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(manifest); err != nil {
			return fmt.Errorf("encoding manifest: %w", err)
		}
		return nil
	})
}

// ReadManifest reads and validates one manifest. Versions other than v1alpha1
// return an error that can be matched with errors.Is and ErrIncompatibleVersion.
func ReadManifest(ctx context.Context, path string) (Manifest, error) {
	var manifest Manifest
	if err := contextError(ctx); err != nil {
		return manifest, err
	}
	file, err := os.Open(path)
	if err != nil {
		return manifest, fmt.Errorf("opening manifest %q: %w", path, err)
	}
	defer file.Close()
	if err := decodeSingle(ctx, file, &manifest); err != nil {
		return manifest, fmt.Errorf("decoding manifest %q: %w", path, err)
	}
	if err := manifest.validateVersionAndShape(); err != nil {
		return manifest, fmt.Errorf("validating manifest %q: %w", path, err)
	}
	return manifest, nil
}

// WriteSequence writes values as newline-delimited JSON. The writer is not
// closed by this function, which makes it suitable for a streaming pipeline.
func WriteSequence[T any](ctx context.Context, w io.Writer, values []T) error {
	if w == nil {
		return fmt.Errorf("writing sequence: nil writer")
	}
	encoder := json.NewEncoder(w)
	for i, value := range values {
		if err := contextError(ctx); err != nil {
			return fmt.Errorf("writing sequence item %d: %w", i, err)
		}
		if err := encoder.Encode(value); err != nil {
			return fmt.Errorf("encoding sequence item %d: %w", i, err)
		}
	}
	return nil
}

// ReadSequence decodes newline-delimited JSON values and invokes visit for
// each value before reading the next one. It keeps only one decoded value in
// memory at a time.
func ReadSequence[T any](ctx context.Context, r io.Reader, visit func(T) error) error {
	if r == nil {
		return fmt.Errorf("reading sequence: nil reader")
	}
	if visit == nil {
		return fmt.Errorf("reading sequence: nil visitor")
	}
	decoder := json.NewDecoder(bufio.NewReader(r))
	index := 0
	for {
		if err := contextError(ctx); err != nil {
			return fmt.Errorf("reading sequence item %d: %w", index, err)
		}
		var value T
		err := decoder.Decode(&value)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("decoding sequence item %d: %w", index, err)
		}
		if err := visit(value); err != nil {
			return fmt.Errorf("visiting sequence item %d: %w", index, err)
		}
		index++
	}
}

// WriteJSONSequence atomically writes a generic newline-delimited JSON file.
func WriteJSONSequence[T any](ctx context.Context, path string, values []T) error {
	return writeAtomic(ctx, path, func(w io.Writer) error {
		return WriteSequence(ctx, w, values)
	})
}

// ReadJSONSequence reads a generic newline-delimited JSON file in streaming
// fashion.
func ReadJSONSequence[T any](ctx context.Context, path string, visit func(T) error) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("opening sequence %q: %w", path, err)
	}
	defer file.Close()
	return ReadSequence(ctx, file, visit)
}

// WriteArtifacts atomically writes the artifact sequence for a result.
func WriteArtifacts(ctx context.Context, path string, artifacts []Artifact) error {
	for i := range artifacts {
		if err := artifacts[i].validate(); err != nil {
			return fmt.Errorf("artifact %d: %w", i, err)
		}
	}
	copyOfArtifacts := append([]Artifact(nil), artifacts...)
	SortArtifacts(copyOfArtifacts)
	return WriteJSONSequence(ctx, path, copyOfArtifacts)
}

// ReadArtifacts reads artifacts in streaming order and validates each item.
func ReadArtifacts(ctx context.Context, path string, visit func(Artifact) error) error {
	if visit == nil {
		return fmt.Errorf("reading artifacts: nil visitor")
	}
	return ReadJSONSequence(ctx, path, func(artifact Artifact) error {
		if err := artifact.validate(); err != nil {
			return err
		}
		return visit(artifact)
	})
}

// WriteContributions atomically writes the contribution sequence for a result.
func WriteContributions(ctx context.Context, path string, contributions []Contribution) error {
	for i := range contributions {
		if err := contributions[i].validate(); err != nil {
			return fmt.Errorf("contribution %d: %w", i, err)
		}
	}
	copyOfContributions := append([]Contribution(nil), contributions...)
	SortContributions(copyOfContributions)
	return WriteJSONSequence(ctx, path, copyOfContributions)
}

// ReadContributions reads contributions in streaming order and validates each
// item.
func ReadContributions(ctx context.Context, path string, visit func(Contribution) error) error {
	if visit == nil {
		return fmt.Errorf("reading contributions: nil visitor")
	}
	return ReadJSONSequence(ctx, path, func(contribution Contribution) error {
		if err := contribution.validate(); err != nil {
			return err
		}
		return visit(contribution)
	})
}

// WriteResult writes a complete result to a directory. Each file is written
// through a same-directory temporary file and renamed atomically.
func WriteResult(ctx context.Context, directory string, result Result) error {
	if err := result.Normalize(); err != nil {
		return fmt.Errorf("normalizing result: %w", err)
	}
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return fmt.Errorf("creating result directory %q: %w", directory, err)
	}
	if err := WriteManifest(ctx, filepath.Join(directory, ManifestFileName), result.Manifest); err != nil {
		return err
	}
	if err := WriteArtifacts(ctx, filepath.Join(directory, ArtifactsFileName), result.Artifacts); err != nil {
		return err
	}
	if err := WriteContributions(ctx, filepath.Join(directory, ContributionsFileName), result.Contributions); err != nil {
		return err
	}
	return nil
}

// ReadResult reads and validates a complete result directory while decoding
// artifact and contribution sequences incrementally.
func ReadResult(ctx context.Context, directory string) (Result, error) {
	var result Result
	manifest, err := ReadManifest(ctx, filepath.Join(directory, ManifestFileName))
	if err != nil {
		return result, err
	}
	result.Manifest = manifest
	result.Artifacts = make([]Artifact, 0, manifest.ArtifactCount)
	if err := ReadArtifacts(ctx, filepath.Join(directory, ArtifactsFileName), func(artifact Artifact) error {
		result.Artifacts = append(result.Artifacts, artifact)
		return nil
	}); err != nil {
		return Result{}, err
	}
	result.Contributions = make([]Contribution, 0, manifest.ContributionCount)
	if err := ReadContributions(ctx, filepath.Join(directory, ContributionsFileName), func(contribution Contribution) error {
		result.Contributions = append(result.Contributions, contribution)
		return nil
	}); err != nil {
		return Result{}, err
	}
	if err := result.Validate(); err != nil {
		return Result{}, fmt.Errorf("validating result: %w", err)
	}
	return result, nil
}

// WriteAnalysis is an alias for WriteResult.
func WriteAnalysis(ctx context.Context, directory string, result Result) error {
	return WriteResult(ctx, directory, result)
}

// ReadAnalysis is an alias for ReadResult.
func ReadAnalysis(ctx context.Context, directory string) (Result, error) {
	return ReadResult(ctx, directory)
}

func (m Manifest) validateVersionAndShape() error {
	if err := validateVersion(m.ContractVersion); err != nil {
		return err
	}
	if err := m.validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidResult, err)
	}
	return nil
}

func validateVersion(actual string) error {
	if actual != Version {
		return &VersionError{Expected: Version, Actual: actual}
	}
	return nil
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

func decodeSingle(ctx context.Context, r io.Reader, value any) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	decoder := json.NewDecoder(bufio.NewReader(r))
	if err := decoder.Decode(value); err != nil {
		return err
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("multiple JSON values")
		}
		return err
	}
	return nil
}

func writeAtomic(ctx context.Context, path string, write func(io.Writer) error) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if write == nil {
		return fmt.Errorf("writing %q: nil writer function", path)
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return fmt.Errorf("creating output directory %q: %w", directory, err)
	}
	base := filepath.Base(path)
	temporary, err := os.CreateTemp(directory, "."+base+".tmp-")
	if err != nil {
		return fmt.Errorf("creating temporary output for %q: %w", path, err)
	}
	temporaryName := temporary.Name()
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = os.Remove(temporaryName)
		}
	}()

	if err := write(temporary); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("writing temporary output for %q: %w", path, err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("syncing temporary output for %q: %w", path, err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("closing temporary output for %q: %w", path, err)
	}
	if err := os.Rename(temporaryName, path); err != nil {
		return fmt.Errorf("renaming temporary output for %q: %w", path, err)
	}
	removeTemporary = false
	return nil
}
