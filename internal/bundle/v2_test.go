package bundle_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/pedrogpaulino/manu/internal/bundle"
	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/fact"
)

func TestV1Alpha2BundleRoundTrip(t *testing.T) {
	t.Parallel()

	want := validV2Bundle()
	directory := t.TempDir()
	if err := bundle.WriteBundle(context.Background(), directory, want); err != nil {
		t.Fatalf("WriteBundle() error = %v", err)
	}
	for _, name := range []string{
		bundle.ManifestFileName,
		bundle.ArtifactsFileName,
		bundle.ContributionsFileName,
		bundle.EvidenceFileName,
		bundle.FrontendManifestsFileName,
		bundle.CanonicalFactsFileName,
		bundle.ExtensionsFileName,
	} {
		if _, err := os.Stat(filepath.Join(directory, name)); err != nil {
			t.Fatalf("stat %s: %v", name, err)
		}
	}

	got, err := bundle.ReadBundle(context.Background(), directory)
	if err != nil {
		t.Fatalf("ReadBundle() error = %v", err)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("read bundle validation error = %v", err)
	}
	if got.Manifest.Version != bundle.VersionV1Alpha2 {
		t.Fatalf("bundle version = %q, want %q", got.Manifest.Version, bundle.VersionV1Alpha2)
	}
	if got.Manifest.ContractVersion != contract.Version {
		t.Fatalf("contract version = %q, want %q", got.Manifest.ContractVersion, contract.Version)
	}
	if !reflect.DeepEqual(got.FrontendManifests, want.FrontendManifests) {
		t.Fatalf("frontend manifests differ:\n got %#v\nwant %#v", got.FrontendManifests, want.FrontendManifests)
	}
	if !reflect.DeepEqual(got.Facts, want.Facts) {
		t.Fatalf("facts differ:\n got %#v\nwant %#v", got.Facts, want.Facts)
	}
	expectedExtensions := make([]json.RawMessage, 0, len(want.Extensions))
	for _, extension := range want.Extensions {
		compacted := canonicalJSONForTest(extension)
		expectedExtensions = append(expectedExtensions, compacted)
	}
	if !reflect.DeepEqual(got.Extensions, expectedExtensions) {
		t.Fatalf("extensions differ:\n got %#v\nwant %#v", got.Extensions, expectedExtensions)
	}
}

func TestV1Alpha2FactualDigestSortsNewSequences(t *testing.T) {
	t.Parallel()

	input := validV2Bundle()
	left, err := input.FactualDigest()
	if err != nil {
		t.Fatalf("FactualDigest(left) error = %v", err)
	}
	reordered := input
	reordered.FrontendManifests = append([]fact.FrontendManifest(nil), input.FrontendManifests...)
	for leftIndex, rightIndex := 0, len(reordered.FrontendManifests)-1; leftIndex < rightIndex; leftIndex, rightIndex = leftIndex+1, rightIndex-1 {
		reordered.FrontendManifests[leftIndex], reordered.FrontendManifests[rightIndex] = reordered.FrontendManifests[rightIndex], reordered.FrontendManifests[leftIndex]
	}
	reordered.Facts = append([]fact.CanonicalFact(nil), input.Facts...)
	for leftIndex, rightIndex := 0, len(reordered.Facts)-1; leftIndex < rightIndex; leftIndex, rightIndex = leftIndex+1, rightIndex-1 {
		reordered.Facts[leftIndex], reordered.Facts[rightIndex] = reordered.Facts[rightIndex], reordered.Facts[leftIndex]
	}
	reordered.Extensions = append([]json.RawMessage(nil), input.Extensions...)
	for leftIndex, rightIndex := 0, len(reordered.Extensions)-1; leftIndex < rightIndex; leftIndex, rightIndex = leftIndex+1, rightIndex-1 {
		reordered.Extensions[leftIndex], reordered.Extensions[rightIndex] = reordered.Extensions[rightIndex], reordered.Extensions[leftIndex]
	}
	right, err := reordered.FactualDigest()
	if err != nil {
		t.Fatalf("FactualDigest(right) error = %v", err)
	}
	if left != right {
		t.Fatalf("v1alpha2 digest depends on sequence order: left=%q right=%q", left, right)
	}
}

func TestV1Alpha2FactualDigestChangesForEachNewSequence(t *testing.T) {
	t.Parallel()

	input := validV2Bundle()
	want, err := input.FactualDigest()
	if err != nil {
		t.Fatalf("FactualDigest() error = %v", err)
	}

	manifestChanged := input
	manifestChanged.FrontendManifests = append([]fact.FrontendManifest(nil), input.FrontendManifests...)
	manifestChanged.FrontendManifests[0].Limitations = []string{"limited-depth"}
	assertV2DigestChanged(t, want, manifestChanged)

	factChanged := input
	factChanged.Facts = append([]fact.CanonicalFact(nil), input.Facts...)
	factChanged.Facts[0].Producer.Method = "other-method"
	factID, err := fact.FactID(factChanged.Facts[0])
	if err != nil {
		t.Fatalf("FactID(changed) error = %v", err)
	}
	factChanged.Facts[0].ID = factID
	assertV2DigestChanged(t, want, factChanged)

	extensionChanged := input
	extensionChanged.Extensions = append([]json.RawMessage(nil), input.Extensions...)
	extensionChanged.Extensions[0] = json.RawMessage(`{"kind":"changed"}`)
	assertV2DigestChanged(t, want, extensionChanged)
}

func TestV1Alpha2BundleRejectsMissingSequence(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	if err := bundle.WriteBundle(context.Background(), directory, validV2Bundle()); err != nil {
		t.Fatalf("WriteBundle() error = %v", err)
	}
	if err := os.Remove(filepath.Join(directory, bundle.CanonicalFactsFileName)); err != nil {
		t.Fatalf("remove facts: %v", err)
	}
	if _, err := bundle.ReadBundle(context.Background(), directory); !errors.Is(err, bundle.ErrInvalidFile) {
		t.Fatalf("ReadBundle() error = %v, want invalid file", err)
	}
}

func TestV1Alpha2BundleRejectsSequenceDigestMismatch(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	if err := bundle.WriteBundle(context.Background(), directory, validV2Bundle()); err != nil {
		t.Fatalf("WriteBundle() error = %v", err)
	}
	path := filepath.Join(directory, bundle.ExtensionsFileName)
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read extensions: %v", err)
	}
	content = append(content, []byte(`{"kind":"extra"}`+"\n")...)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("rewrite extensions: %v", err)
	}
	if _, err := bundle.ReadBundle(context.Background(), directory); !errors.Is(err, bundle.ErrSizeMismatch) && !errors.Is(err, bundle.ErrDigestMismatch) && !errors.Is(err, bundle.ErrCountMismatch) && !errors.Is(err, bundle.ErrLimitExceeded) {
		t.Fatalf("ReadBundle() error = %v, want controlled sequence mismatch", err)
	}
}

func TestV1Alpha1RejectsV1Alpha2DataWithoutPublishing(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*bundle.Bundle)
	}{
		{
			name: "frontend manifests",
			mutate: func(input *bundle.Bundle) {
				input.FrontendManifests = []fact.FrontendManifest{{}}
			},
		},
		{
			name: "facts",
			mutate: func(input *bundle.Bundle) {
				input.Facts = []fact.CanonicalFact{{}}
			},
		},
		{
			name: "extensions",
			mutate: func(input *bundle.Bundle) {
				input.Extensions = []json.RawMessage{json.RawMessage(`{}`)}
			},
		},
		{
			name: "v2 count",
			mutate: func(input *bundle.Bundle) {
				input.Manifest.Counts.CanonicalFactCount = 1
			},
		},
		{
			name: "v2 limit",
			mutate: func(input *bundle.Bundle) {
				input.Manifest.Limits.MaxCanonicalFacts = 1
			},
		},
		{
			name: "v2 descriptor",
			mutate: func(input *bundle.Bundle) {
				input.Manifest.Files = append(input.Manifest.Files, bundle.File{
					Name:   bundle.CanonicalFactsFileName,
					Digest: strings.Repeat("d", 64),
				})
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := validBundle()
			tt.mutate(&input)
			if err := input.Validate(); !errors.Is(err, bundle.ErrUnsupportedVersion) {
				t.Fatalf("Bundle.Validate() error = %v, want unsupported version", err)
			}
			directory := t.TempDir()
			if err := bundle.WriteBundle(context.Background(), directory, input); !errors.Is(err, bundle.ErrUnsupportedVersion) {
				t.Fatalf("WriteBundle() error = %v, want unsupported version", err)
			}
			entries, err := os.ReadDir(directory)
			if err != nil {
				t.Fatalf("read output directory: %v", err)
			}
			if len(entries) != 0 {
				t.Fatalf("rejected v1alpha1 write published files: %v", entries)
			}
		})
	}
}

func TestV1Alpha2CanonicalizesEquivalentExtensionJSON(t *testing.T) {
	t.Parallel()

	left := validV2Bundle()
	left.Extensions = []json.RawMessage{
		json.RawMessage(`{"z":90071992547409931234567890,"a":{"y":2,"x":1}}`),
	}
	setV2ExtensionSchemas(&left)
	right := left
	right.Extensions = []json.RawMessage{
		json.RawMessage(` { "a": { "x": 1, "y": 2 }, "z": 90071992547409931234567890 } `),
	}
	setV2ExtensionSchemas(&right)
	leftDigest, err := left.FactualDigest()
	if err != nil {
		t.Fatalf("FactualDigest(left) error = %v", err)
	}
	rightDigest, err := right.FactualDigest()
	if err != nil {
		t.Fatalf("FactualDigest(right) error = %v", err)
	}
	if leftDigest != rightDigest {
		t.Fatalf("semantically equivalent extensions changed digest: left=%q right=%q", leftDigest, rightDigest)
	}

	leftDirectory := t.TempDir()
	rightDirectory := t.TempDir()
	if err := bundle.WriteBundle(context.Background(), leftDirectory, left); err != nil {
		t.Fatalf("WriteBundle(left) error = %v", err)
	}
	if err := bundle.WriteBundle(context.Background(), rightDirectory, right); err != nil {
		t.Fatalf("WriteBundle(right) error = %v", err)
	}
	leftBytes, err := os.ReadFile(filepath.Join(leftDirectory, bundle.ExtensionsFileName))
	if err != nil {
		t.Fatalf("read left extensions: %v", err)
	}
	rightBytes, err := os.ReadFile(filepath.Join(rightDirectory, bundle.ExtensionsFileName))
	if err != nil {
		t.Fatalf("read right extensions: %v", err)
	}
	if !reflect.DeepEqual(leftBytes, rightBytes) {
		t.Fatalf("equivalent extensions have different canonical bytes:\nleft %s\nright %s", leftBytes, rightBytes)
	}

	changed := right
	changed.Extensions = []json.RawMessage{
		json.RawMessage(`{"a":{"x":1,"y":2},"z":90071992547409931234567891}`),
	}
	changedDigest, err := changed.FactualDigest()
	if err != nil {
		t.Fatalf("FactualDigest(changed) error = %v", err)
	}
	if changedDigest == leftDigest {
		t.Fatal("real extension value change did not change digest")
	}
}

func assertV2DigestChanged(t *testing.T, want string, input bundle.Bundle) {
	t.Helper()
	got, err := input.FactualDigest()
	if err != nil {
		t.Fatalf("FactualDigest(changed) error = %v", err)
	}
	if got == want {
		t.Fatal("v1alpha2 digest did not change after sequence mutation")
	}
}

func validV2Bundle() bundle.Bundle {
	input := validBundle()
	input.Manifest.Version = bundle.VersionV1Alpha2

	frontendManifest := fact.FrontendManifest{
		ManifestVersion: fact.FrontendManifestVersion,
		ID:              "java-frontend",
		Version:         "1",
		Method:          "symbols",
		SourceTypes:     []string{"filesystem"},
		Families:        []string{"java"},
		Versions:        []string{"17"},
		Capabilities:    []contract.Dimension{contract.DimensionEntitiesAndRelationships},
		Predicates:      []fact.Predicate{fact.PredicateDefinition, fact.PredicateSymbol},
		Execution:       fact.ExecutionProfileSafeStatic,
	}
	frontendFallback := frontendManifest
	frontendFallback.ID = "generic-frontend"
	frontendFallback.Version = "2"
	frontendFallback.Fallback = true
	frontendFallback.Limitations = []string{"generic-only"}
	input.FrontendManifests = []fact.FrontendManifest{frontendFallback, frontendManifest}

	facts := make([]fact.CanonicalFact, 2)
	for index, subjectID := range []string{"symbol-a", "symbol-b"} {
		facts[index] = fact.CanonicalFact{
			Version:   fact.Version,
			Scope:     fact.Scope{OrganizationID: input.Manifest.Organization.ID, SourceID: input.Manifest.Source.ID, SnapshotID: input.Manifest.Snapshot.ID},
			Predicate: fact.PredicateDefinition,
			Subject:   fact.Participant{Kind: fact.ParticipantSymbol, ID: subjectID},
			Producer:  fact.Producer{ID: "java-frontend", Version: "1", Method: "symbols"},
			Evidence:  []fact.EvidenceRef{{ID: input.Evidence[0].ID, Locator: input.Evidence[0].Locator}},
		}
		id, err := fact.FactID(facts[index])
		if err != nil {
			panic(err)
		}
		facts[index].ID = id
	}
	sort.Slice(facts, func(left, right int) bool { return facts[left].ID < facts[right].ID })
	input.Facts = facts
	input.Extensions = []json.RawMessage{
		json.RawMessage(`{"kind":"annotation","value":"one"}`),
		json.RawMessage(` { "kind": "annotation", "value": "two" } `),
	}
	setV2ExtensionSchemas(&input)
	return input
}

func setV2ExtensionSchemas(input *bundle.Bundle) {
	if len(input.FrontendManifests) < 2 {
		return
	}
	schema := canonicalJSONForTest(json.RawMessage(`{"type":"object","properties":{"kind":{"type":"string"},"value":{"type":"string"}}}`))
	schemaID := "annotation"
	schemaVersion := "1"
	input.FrontendManifests[1].Extensions = []fact.ExtensionSchema{{
		ID:      schemaID,
		Version: schemaVersion,
		Digest:  fact.ExtensionDigest(schema),
	}}
	envelopes := make([]json.RawMessage, 0, len(input.Extensions))
	for _, extension := range input.Extensions {
		canonicalPayload := canonicalJSONForTest(extension)
		envelope, err := json.Marshal(bundle.ExtensionRecord{
			SchemaID:      schemaID,
			SchemaVersion: schemaVersion,
			SchemaDigest:  input.FrontendManifests[1].Extensions[0].Digest,
			Schema:        schema,
			Payload:       canonicalPayload,
		})
		if err != nil {
			panic(err)
		}
		envelopes = append(envelopes, envelope)
	}
	input.Extensions = envelopes
}

func canonicalJSONForTest(raw []byte) []byte {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		panic(err)
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return canonical
}
