// Package state contains the reconstructible, versioned state used by the
// incremental analysis runner. State is an operational cache: it is safe to
// discard and is never read from or written to a source root.
package state

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"github.com/pedrogpaulino/manu/internal/contract"
)

const (
	// Version is the on-disk state schema. It is deliberately independent from
	// the result contract version so a state migration never changes facts.
	Version = "v1alpha1"
	// StateFileName is the canonical state file in an analysis output
	// directory.
	StateFileName = "state.json"
	// MaxStateBytes bounds untrusted state before decoding it.
	MaxStateBytes = 64 << 20
)

var (
	// ErrIncompatibleVersion indicates state from another schema or contract.
	ErrIncompatibleVersion = errors.New("state: incompatible version")
	// ErrCorrupt indicates malformed state. Callers should treat it as a cache
	// miss and rebuild rather than trusting any entries.
	ErrCorrupt = errors.New("state: corrupt")
	// ErrInvalid indicates an entry or snapshot that cannot be safely stored.
	ErrInvalid = errors.New("state: invalid")
)

// Key is the complete compatibility key for one analyzer result. All fields
// are explicit so a method or contract change cannot silently reuse state.
type Key struct {
	SourceID        string `json:"source_id"`
	ArtifactPath    string `json:"artifact_path"`
	ArtifactHash    string `json:"artifact_hash"`
	ContractVersion string `json:"contract_version"`
	AnalyzerID      string `json:"analyzer_id"`
	AnalyzerVersion string `json:"analyzer_version"`
	AnalyzerMethod  string `json:"analyzer_method"`
}

// NewKey constructs and normalizes a compatibility key.
func NewKey(sourceID, artifactPath, artifactHash, contractVersion, analyzerID, analyzerVersion, analyzerMethod string) Key {
	return Key{
		SourceID:        strings.TrimSpace(sourceID),
		ArtifactPath:    cleanPath(artifactPath),
		ArtifactHash:    strings.TrimSpace(artifactHash),
		ContractVersion: strings.TrimSpace(contractVersion),
		AnalyzerID:      strings.TrimSpace(analyzerID),
		AnalyzerVersion: strings.TrimSpace(analyzerVersion),
		AnalyzerMethod:  strings.TrimSpace(analyzerMethod),
	}
}

// Validate checks that every compatibility component is present.
func (k Key) Validate() error {
	if k.SourceID == "" || k.ArtifactPath == "" || k.ArtifactHash == "" ||
		k.ContractVersion == "" || k.AnalyzerID == "" ||
		k.AnalyzerVersion == "" || k.AnalyzerMethod == "" {
		return fmt.Errorf("%w: key fields are required", ErrInvalid)
	}
	if k.ArtifactPath == "." || k.ArtifactPath == ".." || strings.HasPrefix(k.ArtifactPath, "../") || filepath.IsAbs(filepath.FromSlash(k.ArtifactPath)) {
		return fmt.Errorf("%w: artifact path %q is not relative", ErrInvalid, k.ArtifactPath)
	}
	return nil
}

// Canonical returns the stable JSON representation used for diagnostics and
// tests. JSON field order is fixed by the struct declaration.
func (k Key) Canonical() string {
	encoded, _ := json.Marshal(k)
	return string(encoded)
}

// Digest returns a compact stable identifier for the complete key.
func (k Key) Digest() string {
	digest := sha256.Sum256([]byte(k.Canonical()))
	return hex.EncodeToString(digest[:])
}

// Entry stores the normalized analyzer output for one exact Key. Failures
// are intentionally not cached: a later run must be able to retry them.
type Entry struct {
	Key           Key                     `json:"key"`
	ArtifactID    string                  `json:"artifact_id"`
	Contributions []contract.Contribution `json:"contributions"`
	Coverage      []contract.Coverage     `json:"coverage"`
	Gaps          []contract.Gap          `json:"gaps"`
}

// Validate checks the entry without making assumptions about the analyzer's
// value schema. The runner performs the additional artifact-level checks.
func (e Entry) Validate() error {
	if err := e.Key.Validate(); err != nil {
		return fmt.Errorf("entry key: %w", err)
	}
	if strings.TrimSpace(e.ArtifactID) == "" {
		return fmt.Errorf("%w: entry artifact id is required", ErrInvalid)
	}
	if e.Contributions == nil {
		e.Contributions = []contract.Contribution{}
	}
	if e.Coverage == nil {
		e.Coverage = []contract.Coverage{}
	}
	if e.Gaps == nil {
		e.Gaps = []contract.Gap{}
	}
	for i, contribution := range e.Contributions {
		if err := contribution.Validate(); err != nil {
			return fmt.Errorf("%w: contribution %d: %v", ErrCorrupt, i, err)
		}
		if contribution.ArtifactID != e.ArtifactID || contribution.AnalyzerID != e.Key.AnalyzerID ||
			contribution.AnalyzerVersion != e.Key.AnalyzerVersion {
			return fmt.Errorf("%w: contribution %d does not match entry key", ErrCorrupt, i)
		}
		if contribution.Locator.SourceID != "" && contribution.Locator.SourceID != e.Key.SourceID {
			return fmt.Errorf("%w: contribution %d source does not match entry key", ErrCorrupt, i)
		}
		if contribution.Locator.Path != "" && contribution.Locator.Path != e.Key.ArtifactPath {
			return fmt.Errorf("%w: contribution %d path does not match entry key", ErrCorrupt, i)
		}
	}
	for i, coverage := range e.Coverage {
		if err := coverage.Validate(); err != nil {
			return fmt.Errorf("%w: coverage %d: %v", ErrCorrupt, i, err)
		}
		if coverage.AnalyzerID != "" && coverage.AnalyzerID != e.Key.AnalyzerID {
			return fmt.Errorf("%w: coverage %d does not match entry key", ErrCorrupt, i)
		}
		if coverage.Locator != nil && !locatorMatchesEntry(*coverage.Locator, e) {
			return fmt.Errorf("%w: coverage %d locator does not match entry key", ErrCorrupt, i)
		}
	}
	for i, gap := range e.Gaps {
		if err := gap.Validate(); err != nil {
			return fmt.Errorf("%w: gap %d: %v", ErrCorrupt, i, err)
		}
		if gap.AnalyzerID != "" && gap.AnalyzerID != e.Key.AnalyzerID {
			return fmt.Errorf("%w: gap %d does not match entry key", ErrCorrupt, i)
		}
		if gap.Locator != nil && !locatorMatchesEntry(*gap.Locator, e) {
			return fmt.Errorf("%w: gap %d locator does not match entry key", ErrCorrupt, i)
		}
	}
	return nil
}

// Dependency is a conservative, directly observed edge from one artifact to
// another. Paths are kept rather than guessed IDs so an edge can invalidate a
// dependent even when the target was removed in the new revision.
type Dependency struct {
	FromPath string `json:"from_path"`
	ToPath   string `json:"to_path"`
	Kind     string `json:"kind,omitempty"`
}

// Validate checks that an edge is local and has two distinct relative paths.
func (d Dependency) Validate() error {
	from, to := cleanPath(d.FromPath), cleanPath(d.ToPath)
	if from == "." || to == "." || from == to || strings.HasPrefix(from, "../") || strings.HasPrefix(to, "../") ||
		strings.HasPrefix(from, "/") || strings.HasPrefix(to, "/") {
		return fmt.Errorf("%w: invalid dependency %q -> %q", ErrInvalid, d.FromPath, d.ToPath)
	}
	return nil
}

// Snapshot is the complete reconstructible state for one logical source.
type Snapshot struct {
	Version         string              `json:"version"`
	ContractVersion string              `json:"contract_version"`
	SourceID        string              `json:"source_id"`
	Artifacts       []contract.Artifact `json:"artifacts"`
	Entries         []Entry             `json:"entries"`
	Dependencies    []Dependency        `json:"dependencies"`
}

// Empty returns a valid empty snapshot.
func Empty(sourceID string) Snapshot {
	return Snapshot{
		Version:         Version,
		ContractVersion: contract.Version,
		SourceID:        strings.TrimSpace(sourceID),
		Artifacts:       []contract.Artifact{},
		Entries:         []Entry{},
		Dependencies:    []Dependency{},
	}
}

// Validate checks the on-disk schema and all entries. A single malformed
// entry invalidates the whole snapshot so no partial cache can be trusted.
func (s Snapshot) Validate() error {
	if s.Version != Version {
		return &VersionError{Expected: Version, Actual: s.Version}
	}
	if s.ContractVersion != contract.Version {
		return &ContractVersionError{Expected: contract.Version, Actual: s.ContractVersion}
	}
	if strings.TrimSpace(s.SourceID) == "" {
		return fmt.Errorf("%w: source id is required", ErrInvalid)
	}
	seenArtifacts := make(map[string]struct{}, len(s.Artifacts))
	artifactsByID := make(map[string]contract.Artifact, len(s.Artifacts))
	for i, artifact := range s.Artifacts {
		if err := artifact.Validate(); err != nil {
			return fmt.Errorf("%w: artifact %d: %v", ErrCorrupt, i, err)
		}
		if artifact.SourceID != s.SourceID {
			return fmt.Errorf("%w: artifact %q belongs to another source", ErrCorrupt, artifact.ID)
		}
		if _, exists := seenArtifacts[artifact.Path]; exists {
			return fmt.Errorf("%w: duplicate artifact path %q", ErrCorrupt, artifact.Path)
		}
		if _, exists := artifactsByID[artifact.ID]; exists {
			return fmt.Errorf("%w: duplicate artifact id %q", ErrCorrupt, artifact.ID)
		}
		seenArtifacts[artifact.Path] = struct{}{}
		artifactsByID[artifact.ID] = artifact
	}
	seenEntries := make(map[string]struct{}, len(s.Entries))
	for i, entry := range s.Entries {
		if err := entry.Validate(); err != nil {
			return fmt.Errorf("%w: entry %d: %v", ErrCorrupt, i, err)
		}
		if entry.Key.SourceID != s.SourceID {
			return fmt.Errorf("%w: entry %d belongs to another source", ErrCorrupt, i)
		}
		if entry.Key.ContractVersion != s.ContractVersion {
			return fmt.Errorf("%w: entry %d uses another contract version", ErrCorrupt, i)
		}
		artifact, exists := artifactsByID[entry.ArtifactID]
		if !exists {
			return fmt.Errorf("%w: entry %d references unknown artifact", ErrCorrupt, i)
		}
		if entry.Key.ArtifactPath != artifact.Path || entry.Key.ArtifactHash != artifact.Hash {
			return fmt.Errorf("%w: entry %d artifact path or hash does not match", ErrCorrupt, i)
		}
		if _, exists := seenEntries[entry.Key.Digest()]; exists {
			return fmt.Errorf("%w: duplicate entry key", ErrCorrupt)
		}
		seenEntries[entry.Key.Digest()] = struct{}{}
	}
	for i, dependency := range s.Dependencies {
		if err := dependency.Validate(); err != nil {
			return fmt.Errorf("%w: dependency %d: %v", ErrCorrupt, i, err)
		}
		if _, exists := seenArtifacts[cleanPath(dependency.FromPath)]; !exists {
			return fmt.Errorf("%w: dependency %d source path is unknown", ErrCorrupt, i)
		}
		if _, exists := seenArtifacts[cleanPath(dependency.ToPath)]; !exists {
			return fmt.Errorf("%w: dependency %d target path is unknown", ErrCorrupt, i)
		}
	}
	return nil
}

// VersionError identifies a state schema mismatch.
type VersionError struct {
	Expected string
	Actual   string
}

func (e *VersionError) Error() string {
	return fmt.Sprintf("state: incompatible version %q (expected %q)", e.Actual, e.Expected)
}

func (e *VersionError) Is(target error) bool { return target == ErrIncompatibleVersion }

// ContractVersionError identifies a result contract mismatch in state.
type ContractVersionError struct {
	Expected string
	Actual   string
}

func (e *ContractVersionError) Error() string {
	return fmt.Sprintf("state: incompatible contract %q (expected %q)", e.Actual, e.Expected)
}

func (e *ContractVersionError) Is(target error) bool { return target == ErrIncompatibleVersion }

// Store is a synchronized in-memory snapshot backed by an atomic JSON file.
// It has no fallback path: Path always remains under the caller-configured
// destination.
type Store struct {
	mu       sync.RWMutex
	path     string
	snapshot Snapshot
	loadErr  error
}

// Open opens a store directory (or an explicit state.json path). Missing
// state is a valid empty cache. Corrupt/incompatible state is retained as a
// load error while exposing an empty snapshot for safe misses.
func Open(destination string) (*Store, error) {
	statePath, err := resolvePath(destination)
	if err != nil {
		return nil, err
	}
	snapshot, loadErr := Load(context.Background(), statePath)
	if loadErr != nil {
		snapshot = Empty("")
	}
	return &Store{path: statePath, snapshot: snapshot, loadErr: loadErr}, nil
}

// NewStore is an explicit constructor alias for Open.
func NewStore(destination string) (*Store, error) { return Open(destination) }

// Load reads a state file. Missing files return an empty snapshot without an
// error; malformed or incompatible files return an empty snapshot and a
// classified error.
func Load(ctx context.Context, filePath string) (Snapshot, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return Snapshot{}, err
		}
	}
	file, err := os.Open(filePath)
	if errors.Is(err, os.ErrNotExist) {
		return Empty(""), nil
	}
	if err != nil {
		return Snapshot{}, fmt.Errorf("opening state %q: %w", filePath, err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return Snapshot{}, fmt.Errorf("stating state %q: %w", filePath, err)
	}
	if info.Size() > MaxStateBytes {
		return Snapshot{}, fmt.Errorf("%w: state exceeds %d bytes", ErrCorrupt, MaxStateBytes)
	}
	decoder := json.NewDecoder(bufio.NewReader(io.LimitReader(file, MaxStateBytes+1)))
	var snapshot Snapshot
	if err := decoder.Decode(&snapshot); err != nil {
		return Snapshot{}, fmt.Errorf("%w: decoding state: %v", ErrCorrupt, err)
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); err != io.EOF {
		return Snapshot{}, fmt.Errorf("%w: multiple JSON values", ErrCorrupt)
	}
	if err := snapshot.Validate(); err != nil {
		if errors.Is(err, ErrIncompatibleVersion) {
			return Snapshot{}, err
		}
		return Snapshot{}, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	return snapshot, nil
}

// Read is an alias for Load.
func Read(ctx context.Context, filePath string) (Snapshot, error) { return Load(ctx, filePath) }

// Path returns the configured state file path.
func (s *Store) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

// LoadError reports corruption/version issues observed when opening the
// store. Callers should use it for a limitation message, not fail the run.
func (s *Store) LoadError() error {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.loadErr
}

// Snapshot returns a defensive copy of the current state.
func (s *Store) Snapshot() Snapshot {
	if s == nil {
		return Snapshot{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneSnapshot(s.snapshot)
}

// Lookup returns one exact key. No fuzzy or path-only lookup is performed.
func (s *Store) Lookup(key Key) (Entry, bool) {
	if s == nil {
		return Entry{}, false
	}
	digest := key.Digest()
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, entry := range s.snapshot.Entries {
		if entry.Key.Digest() == digest && entry.Key.Canonical() == key.Canonical() {
			return cloneEntry(entry), true
		}
	}
	return Entry{}, false
}

// Replace atomically replaces the in-memory and on-disk snapshot.
func (s *Store) Replace(ctx context.Context, snapshot Snapshot) error {
	if s == nil {
		return fmt.Errorf("%w: nil store", ErrInvalid)
	}
	if err := snapshot.Validate(); err != nil {
		return err
	}
	if err := writeAtomic(ctx, s.path, snapshot); err != nil {
		return err
	}
	s.mu.Lock()
	s.snapshot = cloneSnapshot(snapshot)
	s.loadErr = nil
	s.mu.Unlock()
	return nil
}

// Write atomically writes a snapshot to a destination without opening a
// mutable Store.
func Write(ctx context.Context, destination string, snapshot Snapshot) error {
	statePath, err := resolvePath(destination)
	if err != nil {
		return err
	}
	if err := snapshot.Validate(); err != nil {
		return err
	}
	return writeAtomic(ctx, statePath, snapshot)
}

func resolvePath(destination string) (string, error) {
	if strings.TrimSpace(destination) == "" {
		return "", fmt.Errorf("%w: empty destination", ErrInvalid)
	}
	destination = filepath.Clean(destination)
	if filepath.Base(destination) == StateFileName || filepath.Ext(destination) == ".json" {
		return destination, nil
	}
	return filepath.Join(destination, StateFileName), nil
}

func writeAtomic(ctx context.Context, filePath string, snapshot Snapshot) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	directory := filepath.Dir(filePath)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return fmt.Errorf("creating state directory %q: %w", directory, err)
	}
	temporary, err := os.CreateTemp(directory, "."+filepath.Base(filePath)+".tmp-")
	if err != nil {
		return fmt.Errorf("creating temporary state: %w", err)
	}
	temporaryName := temporary.Name()
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = os.Remove(temporaryName)
		}
	}()
	encoder := json.NewEncoder(temporary)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(snapshot); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("encoding state: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("syncing state: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("closing state: %w", err)
	}
	if err := os.Rename(temporaryName, filePath); err != nil {
		return fmt.Errorf("renaming state: %w", err)
	}
	removeTemporary = false
	return nil
}

func cloneSnapshot(snapshot Snapshot) Snapshot {
	clone := snapshot
	clone.Artifacts = append([]contract.Artifact(nil), snapshot.Artifacts...)
	clone.Entries = make([]Entry, len(snapshot.Entries))
	for i, entry := range snapshot.Entries {
		clone.Entries[i] = cloneEntry(entry)
	}
	clone.Dependencies = append([]Dependency(nil), snapshot.Dependencies...)
	return clone
}

func cloneEntry(entry Entry) Entry {
	clone := entry
	clone.Contributions = make([]contract.Contribution, len(entry.Contributions))
	for i, contribution := range entry.Contributions {
		clone.Contributions[i] = contribution
		clone.Contributions[i].Value = append([]byte(nil), contribution.Value...)
	}
	clone.Coverage = make([]contract.Coverage, len(entry.Coverage))
	for i, coverage := range entry.Coverage {
		clone.Coverage[i] = coverage
		if coverage.Locator != nil {
			locator := *coverage.Locator
			clone.Coverage[i].Locator = &locator
		}
	}
	clone.Gaps = make([]contract.Gap, len(entry.Gaps))
	for i, gap := range entry.Gaps {
		clone.Gaps[i] = gap
		if gap.Locator != nil {
			locator := *gap.Locator
			clone.Gaps[i].Locator = &locator
		}
	}
	return clone
}

func cleanPath(value string) string {
	value = strings.TrimSpace(filepath.ToSlash(value))
	if value == "" {
		return ""
	}
	return path.Clean(value)
}

func locatorMatchesEntry(locator contract.Locator, entry Entry) bool {
	if locator.SourceID != "" && locator.SourceID != entry.Key.SourceID {
		return false
	}
	if locator.ArtifactID != "" && locator.ArtifactID != entry.ArtifactID {
		return false
	}
	return locator.Path == "" || locator.Path == entry.Key.ArtifactPath
}
