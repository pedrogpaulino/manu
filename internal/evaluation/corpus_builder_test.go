package evaluation

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestFactualCorpusBuilderRunsThreeCanonicalFamilies(t *testing.T) {
	t.Parallel()

	cases := loadContextEfficiencyFixture(t)
	builder, err := NewFactualCorpusBuilder(FactualCorpusConfig{Root: repositoryRoot()})
	if err != nil {
		t.Fatalf("NewFactualCorpusBuilder() error = %v", err)
	}
	corpus, buildErr := builder.Build(context.Background(), cases)
	if buildErr != nil {
		t.Fatalf("Build() error = %v", buildErr)
	}
	if err := corpus.Validate(); err != nil {
		t.Fatalf("Build() corpus validation = %v", err)
	}
	if len(corpus.Snapshots) != 3 {
		t.Fatalf("snapshots = %d, want one per canonical family", len(corpus.Snapshots))
	}
	for _, snapshot := range corpus.Snapshots {
		if snapshot.Bundle.Manifest.Version != "v1alpha2" {
			t.Fatalf("bundle version for %s = %q, want v1alpha2", snapshot.CorpusID, snapshot.Bundle.Manifest.Version)
		}
		if len(snapshot.Bundle.Facts) == 0 {
			t.Fatalf("canonical facts for %s are empty", snapshot.CorpusID)
		}
		if snapshot.FactualDigest == "" || snapshot.FactualDigest == snapshot.SourceRevision {
			t.Fatalf("digest identities for %s were conflated: factual=%q source=%q", snapshot.CorpusID, snapshot.FactualDigest, snapshot.SourceRevision)
		}
		if snapshot.FactualDigest != snapshot.Bundle.Manifest.FactualDigest {
			t.Fatalf("factual digest for %s does not come from bundle manifest", snapshot.CorpusID)
		}
	}
}

func TestFactualCorpusBuilderIsDeterministic(t *testing.T) {
	t.Parallel()

	cases := loadContextEfficiencyFixture(t)
	builder, err := NewFactualCorpusBuilder(FactualCorpusConfig{Root: repositoryRoot()})
	if err != nil {
		t.Fatal(err)
	}
	first, firstErr := builder.Build(context.Background(), cases)
	second, secondErr := builder.Build(context.Background(), cases)
	if firstErr != nil || secondErr != nil {
		t.Fatalf("Build() errors = %v/%v", firstErr, secondErr)
	}
	if len(first.Snapshots) != len(second.Snapshots) {
		t.Fatalf("repeated builds returned %d/%d snapshots", len(first.Snapshots), len(second.Snapshots))
	}
	for index := range first.Snapshots {
		left, right := first.Snapshots[index], second.Snapshots[index]
		if left.CorpusID != right.CorpusID || left.SourceRevision != right.SourceRevision || left.FactualDigest != right.FactualDigest || !reflect.DeepEqual(left.Bundle.Artifacts, right.Bundle.Artifacts) || !reflect.DeepEqual(left.Bundle.Contributions, right.Bundle.Contributions) || !reflect.DeepEqual(left.Bundle.Evidence, right.Bundle.Evidence) || !reflect.DeepEqual(left.Bundle.Facts, right.Bundle.Facts) {
			t.Fatalf("repeated factual corpus builds changed stable output for %s", left.CorpusID)
		}
	}
}

func TestFactualCorpusBuilderReportsRevisionMismatchWithoutReturningPartialBundle(t *testing.T) {
	t.Parallel()

	cases := loadContextEfficiencyFixture(t)
	cases.Cases = cases.Cases[:1]
	cases.Cases[0].SourceRevision = strings.Repeat("0", 64)
	builder, err := NewFactualCorpusBuilder(FactualCorpusConfig{Root: repositoryRoot()})
	if err != nil {
		t.Fatal(err)
	}
	corpus, buildErr := builder.Build(context.Background(), cases)
	if !errors.Is(buildErr, ErrFactualCorpusUnavailable) || !errors.Is(buildErr, ErrSourceRevisionMismatch) {
		t.Fatalf("Build() error = %v, want unavailable factual corpus with source revision mismatch", buildErr)
	}
	var unavailable FactualCorpusUnavailableError
	if !errors.As(buildErr, &unavailable) || len(unavailable.Mismatches) != 1 {
		t.Fatalf("Build() error = %v, want one safe mismatch diagnostic", buildErr)
	}
	if !reflect.DeepEqual(corpus, FactualCorpus{}) {
		t.Fatalf("mismatch corpus = %#v", corpus)
	}
	if unavailable.Mismatches[0].Expected != strings.Repeat("0", 64) || unavailable.Mismatches[0].Observed == "" {
		t.Fatalf("mismatch details = %#v", unavailable.Mismatches[0])
	}
	if strings.Contains(buildErr.Error(), repositoryRoot()) {
		t.Fatalf("mismatch error leaked repository root: %v", buildErr)
	}
}

func TestSourceRevisionDigestIsDeterministicAndCanonical(t *testing.T) {
	t.Parallel()

	left := []SourceRevisionArtifact{
		{Path: "src/z.go", SHA256: strings.Repeat("b", 64), Size: 20},
		{Path: "src/a.go", SHA256: strings.Repeat("a", 64), Size: 10},
	}
	right := []SourceRevisionArtifact{left[1], left[0]}
	first, err := SourceRevisionDigest(left)
	if err != nil {
		t.Fatal(err)
	}
	second, err := SourceRevisionDigest(right)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("reordered artifacts changed digest: %q != %q", first, second)
	}
	if left[0].Path != "src/z.go" {
		t.Fatal("SourceRevisionDigest mutated its input")
	}
}

func TestSourceRevisionDigestRejectsInvalidDigestAndPath(t *testing.T) {
	t.Parallel()

	if _, err := SourceRevisionDigest(nil); !errors.Is(err, ErrInvalidSourceRevision) {
		t.Fatalf("empty artifact set error = %v, want invalid source revision", err)
	}
	valid := SourceRevisionArtifact{Path: "src/main.go", SHA256: strings.Repeat("a", 64), Size: 1}
	tests := map[string]SourceRevisionArtifact{
		"empty path":       {Path: "", SHA256: valid.SHA256, Size: valid.Size},
		"absolute path":    {Path: "/src/main.go", SHA256: valid.SHA256, Size: valid.Size},
		"traversal":        {Path: "../main.go", SHA256: valid.SHA256, Size: valid.Size},
		"backslash":        {Path: "src\\main.go", SHA256: valid.SHA256, Size: valid.Size},
		"unclean path":     {Path: "src//main.go", SHA256: valid.SHA256, Size: valid.Size},
		"uppercase digest": {Path: valid.Path, SHA256: strings.Repeat("A", 64), Size: valid.Size},
		"bad digest":       {Path: valid.Path, SHA256: "not-a-digest", Size: valid.Size},
		"negative size":    {Path: valid.Path, SHA256: valid.SHA256, Size: -1},
	}
	for name, artifact := range tests {
		name, artifact := name, artifact
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := SourceRevisionDigest([]SourceRevisionArtifact{artifact}); !errors.Is(err, ErrInvalidSourceRevision) {
				t.Fatalf("SourceRevisionDigest() error = %v, want invalid source revision", err)
			}
		})
	}
	if _, err := SourceRevisionDigest([]SourceRevisionArtifact{valid, valid}); !errors.Is(err, ErrInvalidSourceRevision) {
		t.Fatalf("duplicate artifact path error = %v, want invalid source revision", err)
	}
}

func TestFactualCorpusValidateRejectsSourceDigestMismatch(t *testing.T) {
	t.Parallel()

	cases := loadContextEfficiencyFixture(t)
	builder, err := NewFactualCorpusBuilder(FactualCorpusConfig{Root: repositoryRoot()})
	if err != nil {
		t.Fatal(err)
	}
	corpus, err := builder.Build(context.Background(), cases)
	if err != nil {
		t.Fatal(err)
	}
	corpus.Snapshots[0].SourceRevision = strings.Repeat("0", 64)
	if err := corpus.Validate(); !errors.Is(err, ErrInvalidFactualCorpus) {
		t.Fatalf("Validate() error = %v, want invalid factual corpus", err)
	}
}
