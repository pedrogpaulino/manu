package contract

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"sort"
	"strings"
)

// SourceID returns a deterministic identity for a source key. The key should
// be a logical source identifier, not an execution timestamp or a machine
// local path when a portable identity is available.
func SourceID(key string) string {
	return digestID("source", canonicalText(key))
}

// NewSourceID is an explicit constructor spelling for SourceID.
func NewSourceID(key string) string { return SourceID(key) }

// SourceIdentity returns the stable identity represented by a Source. An
// existing ID is preserved; otherwise the stable logical source fields are
// used. Revision and content hash belong to a Snapshot and deliberately do
// not change the identity of the logical source across revisions.
func SourceIdentity(source Source) string {
	if strings.TrimSpace(source.ID) != "" {
		return source.ID
	}
	return SourceID(strings.Join([]string{
		canonicalText(source.Type),
		canonicalText(source.Name),
		canonicalText(source.Root),
	}, "\x00"))
}

// ArtifactID returns a deterministic identity derived from its source, path,
// and content hash. Execution metadata is intentionally not part of the key.
func ArtifactID(sourceID, relativePath, hash string) string {
	return digestID("artifact", canonicalText(sourceID), canonicalPath(relativePath), canonicalText(hash))
}

// NewArtifactID is an explicit constructor spelling for ArtifactID.
func NewArtifactID(sourceID, relativePath, hash string) string {
	return ArtifactID(sourceID, relativePath, hash)
}

// ArtifactIdentity returns the stable identity represented by an Artifact.
func ArtifactIdentity(artifact Artifact) string {
	if strings.TrimSpace(artifact.ID) != "" {
		return artifact.ID
	}
	return ArtifactID(artifact.SourceID, artifact.Path, artifact.Hash)
}

// ContributionID returns a deterministic identity for one analyzer method on
// one artifact. Analyzer version is included so incompatible method changes
// cannot silently reuse a previous contribution.
func ContributionID(artifactID, analyzerID, analyzerVersion, method string) string {
	return digestID(
		"contribution",
		canonicalText(artifactID),
		canonicalText(analyzerID),
		canonicalText(analyzerVersion),
		canonicalText(method),
	)
}

// NewContributionID is an explicit constructor spelling for ContributionID.
func NewContributionID(artifactID, analyzerID, analyzerVersion, method string) string {
	return ContributionID(artifactID, analyzerID, analyzerVersion, method)
}

// ContributionIdentity returns the stable identity represented by a
// Contribution.
func ContributionIdentity(contribution Contribution) string {
	if strings.TrimSpace(contribution.ID) != "" {
		return contribution.ID
	}
	return ContributionID(
		contribution.ArtifactID,
		contribution.AnalyzerID,
		contribution.AnalyzerVersion,
		contribution.Method,
	)
}

// SnapshotID returns a deterministic identity for a source snapshot.
func SnapshotID(sourceID, revision, hash string) string {
	return digestID("snapshot", canonicalText(sourceID), canonicalText(revision), canonicalText(hash))
}

// NewSnapshotID is an explicit constructor spelling for SnapshotID.
func NewSnapshotID(sourceID, revision, hash string) string {
	return SnapshotID(sourceID, revision, hash)
}

// CoverageID returns a deterministic identity for a coverage entry.
func CoverageID(dimension, scope string, state CoverageState, analyzerID string) string {
	return digestID("coverage", canonicalText(dimension), canonicalText(scope), string(state), canonicalText(analyzerID))
}

// GapID returns a deterministic identity for a gap. The message is included
// because two gaps with the same code may describe different scopes.
func GapID(code, dimension, scope, message, analyzerID string) string {
	return digestID("gap", canonicalText(code), canonicalText(dimension), canonicalText(scope), canonicalText(message), canonicalText(analyzerID))
}

// FailureID returns a deterministic identity for a failure.
func FailureID(code, operation, artifactID, analyzerID, message string) string {
	return digestID("failure", canonicalText(code), canonicalText(operation), canonicalText(artifactID), canonicalText(analyzerID), canonicalText(message))
}

// Normalize fills deterministic IDs and sorts all result collections by their
// stable keys. It does not set execution metadata or timestamps.
func (r *Result) Normalize() error {
	if r == nil {
		return fmt.Errorf("normalizing result: nil result")
	}
	if r.Manifest.ContractVersion == "" {
		r.Manifest.ContractVersion = Version
	}
	if r.Artifacts == nil {
		r.Artifacts = []Artifact{}
	}
	if r.Contributions == nil {
		r.Contributions = []Contribution{}
	}
	if r.Manifest.Coverage == nil {
		r.Manifest.Coverage = []Coverage{}
	}
	if r.Manifest.Gaps == nil {
		r.Manifest.Gaps = []Gap{}
	}
	if r.Manifest.Failures == nil {
		r.Manifest.Failures = []Failure{}
	}
	if r.Manifest.Source.ID == "" {
		r.Manifest.Source.ID = SourceIdentity(r.Manifest.Source)
	}
	if r.Manifest.Snapshot.SourceID == "" {
		r.Manifest.Snapshot.SourceID = r.Manifest.Source.ID
	}
	if r.Manifest.Snapshot.ID == "" {
		r.Manifest.Snapshot.ID = SnapshotID(
			r.Manifest.Source.ID,
			r.Manifest.Snapshot.Revision,
			r.Manifest.Snapshot.Hash,
		)
	}
	if r.Manifest.ResultID == "" {
		r.Manifest.ResultID = digestID("result", r.Manifest.Source.ID, r.Manifest.Snapshot.ID)
	}
	for i := range r.Artifacts {
		if r.Artifacts[i].SourceID == "" {
			r.Artifacts[i].SourceID = r.Manifest.Source.ID
		}
		if r.Artifacts[i].ID == "" {
			r.Artifacts[i].ID = ArtifactIdentity(r.Artifacts[i])
		}
		if r.Artifacts[i].Path != "" {
			r.Artifacts[i].Path = canonicalPath(r.Artifacts[i].Path)
		}
	}
	for i := range r.Contributions {
		if r.Contributions[i].ID == "" {
			r.Contributions[i].ID = ContributionIdentity(r.Contributions[i])
		}
	}
	for i := range r.Manifest.Coverage {
		if r.Manifest.Coverage[i].ID == "" {
			coverage := r.Manifest.Coverage[i]
			r.Manifest.Coverage[i].ID = CoverageID(
				coverage.Dimension,
				coverage.Scope,
				coverage.State,
				coverage.AnalyzerID,
			)
		}
	}
	for i := range r.Manifest.Gaps {
		if r.Manifest.Gaps[i].ID == "" {
			r.Manifest.Gaps[i].ID = GapID(
				r.Manifest.Gaps[i].Code,
				r.Manifest.Gaps[i].Dimension,
				r.Manifest.Gaps[i].Scope,
				r.Manifest.Gaps[i].Message,
				r.Manifest.Gaps[i].AnalyzerID,
			)
		}
	}
	for i := range r.Manifest.Failures {
		if r.Manifest.Failures[i].ID == "" {
			failure := r.Manifest.Failures[i]
			r.Manifest.Failures[i].ID = FailureID(
				failure.Code,
				failure.Operation,
				failure.ArtifactID,
				failure.AnalyzerID,
				failure.Message,
			)
		}
	}
	r.Manifest.ArtifactCount = len(r.Artifacts)
	r.Manifest.ContributionCount = len(r.Contributions)
	SortArtifacts(r.Artifacts)
	SortContributions(r.Contributions)
	SortCoverage(r.Manifest.Coverage)
	SortGaps(r.Manifest.Gaps)
	SortFailures(r.Manifest.Failures)
	return r.Validate()
}

// SortArtifacts orders artifacts by deterministic identity and then by their
// factual fields for the unlikely case of malformed duplicate IDs.
func SortArtifacts(artifacts []Artifact) {
	sort.SliceStable(artifacts, func(i, j int) bool {
		left, right := artifacts[i], artifacts[j]
		if left.SourceID != right.SourceID {
			return left.SourceID < right.SourceID
		}
		if left.Path != right.Path {
			return left.Path < right.Path
		}
		if left.Hash != right.Hash {
			return left.Hash < right.Hash
		}
		return left.ID < right.ID
	})
}

// SortContributions orders contributions by deterministic identity and
// provenance fields.
func SortContributions(contributions []Contribution) {
	sort.SliceStable(contributions, func(i, j int) bool {
		left, right := contributions[i], contributions[j]
		if left.ArtifactID != right.ArtifactID {
			return left.ArtifactID < right.ArtifactID
		}
		if left.AnalyzerID != right.AnalyzerID {
			return left.AnalyzerID < right.AnalyzerID
		}
		if left.AnalyzerVersion != right.AnalyzerVersion {
			return left.AnalyzerVersion < right.AnalyzerVersion
		}
		if left.Method != right.Method {
			return left.Method < right.Method
		}
		return left.ID < right.ID
	})
}

// SortCoverage orders coverage entries deterministically.
func SortCoverage(coverage []Coverage) {
	sort.SliceStable(coverage, func(i, j int) bool {
		left, right := coverage[i], coverage[j]
		if left.Dimension != right.Dimension {
			return left.Dimension < right.Dimension
		}
		if left.Scope != right.Scope {
			return left.Scope < right.Scope
		}
		if left.AnalyzerID != right.AnalyzerID {
			return left.AnalyzerID < right.AnalyzerID
		}
		if left.State != right.State {
			return left.State < right.State
		}
		return left.ID < right.ID
	})
}

// SortGaps orders gaps deterministically.
func SortGaps(gaps []Gap) {
	sort.SliceStable(gaps, func(i, j int) bool {
		if gaps[i].Dimension != gaps[j].Dimension {
			return gaps[i].Dimension < gaps[j].Dimension
		}
		if gaps[i].Scope != gaps[j].Scope {
			return gaps[i].Scope < gaps[j].Scope
		}
		if gaps[i].Code != gaps[j].Code {
			return gaps[i].Code < gaps[j].Code
		}
		if gaps[i].Message != gaps[j].Message {
			return gaps[i].Message < gaps[j].Message
		}
		return gaps[i].ID < gaps[j].ID
	})
}

// SortFailures orders failures deterministically.
func SortFailures(failures []Failure) {
	sort.SliceStable(failures, func(i, j int) bool {
		if failures[i].Operation != failures[j].Operation {
			return failures[i].Operation < failures[j].Operation
		}
		if failures[i].ArtifactID != failures[j].ArtifactID {
			return failures[i].ArtifactID < failures[j].ArtifactID
		}
		if failures[i].AnalyzerID != failures[j].AnalyzerID {
			return failures[i].AnalyzerID < failures[j].AnalyzerID
		}
		if failures[i].Code != failures[j].Code {
			return failures[i].Code < failures[j].Code
		}
		if failures[i].Message != failures[j].Message {
			return failures[i].Message < failures[j].Message
		}
		return failures[i].ID < failures[j].ID
	})
}

func digestID(prefix string, parts ...string) string {
	h := sha256.New()
	for _, part := range parts {
		// Length framing avoids collisions such as ["ab", "c"] and
		// ["a", "bc"].
		fmt.Fprintf(h, "%d:", len(part))
		h.Write([]byte(part))
	}
	return prefix + "-" + hex.EncodeToString(h.Sum(nil))
}

func canonicalText(value string) string {
	return strings.TrimSpace(value)
}

func canonicalPath(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	if value == "" {
		return ""
	}
	clean := path.Clean(value)
	if clean == "." {
		return ""
	}
	return strings.TrimPrefix(clean, "./")
}
