package bundle_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pedrogpaulino/manu/internal/bundle"
	"github.com/pedrogpaulino/manu/internal/fact"
)

func TestV1Alpha2ImportedDataRequiresCanonicalScopeAndProvenance(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*bundle.Bundle)
		want   error
	}{
		{
			name: "fact outside organization scope",
			mutate: func(input *bundle.Bundle) {
				input.Facts[0].Scope.OrganizationID = "organization-other"
				reidentifyFact(t, &input.Facts[0])
			},
			want: bundle.ErrScopeMismatch,
		},
		{
			name: "fact outside source scope",
			mutate: func(input *bundle.Bundle) {
				input.Facts[0].Scope.SourceID = "source-other"
				input.Facts[0].Evidence[0].Locator.SourceID = "source-other"
				reidentifyFact(t, &input.Facts[0])
			},
			want: bundle.ErrScopeMismatch,
		},
		{
			name: "fact outside snapshot scope",
			mutate: func(input *bundle.Bundle) {
				input.Facts[0].Scope.SnapshotID = "snapshot-other"
				reidentifyFact(t, &input.Facts[0])
			},
			want: bundle.ErrScopeMismatch,
		},
		{
			name: "unknown producer",
			mutate: func(input *bundle.Bundle) {
				input.Facts[0].Producer.ID = "unlisted-frontend"
				reidentifyFact(t, &input.Facts[0])
			},
			want: bundle.ErrInvalidReference,
		},
		{
			name: "producer source type mismatch",
			mutate: func(input *bundle.Bundle) {
				input.FrontendManifests[1].SourceTypes = []string{"repository"}
			},
			want: bundle.ErrInvalidReference,
		},
		{
			name: "lineage input is absent",
			mutate: func(input *bundle.Bundle) {
				input.Facts[0].Lineage = &fact.Lineage{
					RuleID:       "membership",
					RuleVersion:  "1",
					InputFactIDs: []string{"fact-missing"},
				}
			},
			want: bundle.ErrInvalidReference,
		},
		{
			name: "evidence identity is absent",
			mutate: func(input *bundle.Bundle) {
				input.Facts[0].Evidence[0].ID = "evidence-missing"
			},
			want: bundle.ErrInvalidReference,
		},
		{
			name: "evidence locator traverses parent",
			mutate: func(input *bundle.Bundle) {
				input.Facts[0].Evidence[0].Locator.Path = "../outside.java"
			},
			want: bundle.ErrInvalid,
		},
		{
			name: "evidence locator differs from referenced unit",
			mutate: func(input *bundle.Bundle) {
				input.Facts[0].Evidence[0].Locator.Path = "src/Other.java"
			},
			want: bundle.ErrInvalidReference,
		},
		{
			name: "extension has no declared schema",
			mutate: func(input *bundle.Bundle) {
				input.FrontendManifests[1].Extensions = nil
			},
			want: bundle.ErrInvalidExtension,
		},
		{
			name: "extension schema digest differs",
			mutate: func(input *bundle.Bundle) {
				input.FrontendManifests[1].Extensions[0].Digest = strings.Repeat("d", 64)
			},
			want: bundle.ErrInvalidExtension,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := validV2Bundle()
			tt.mutate(&input)
			err := bundle.WriteBundle(context.Background(), t.TempDir(), input)
			if !errors.Is(err, tt.want) {
				t.Fatalf("Bundle.Validate() error = %v, want errors.Is(..., %v)", err, tt.want)
			}
		})
	}
}

func TestV1Alpha2ImportedDataRejectsInconsistentCanonicalFactIdentity(t *testing.T) {
	t.Parallel()

	input := validV2Bundle()
	input.Facts[0].ID = "fact-supplied"
	if err := bundle.WriteBundle(context.Background(), t.TempDir(), input); !errors.Is(err, fact.ErrInvalidIdentity) {
		t.Fatalf("Bundle.Validate(canonical fact identity) error = %v, want invalid bundle", err)
	}
}

func TestV1Alpha2WriteRejectsInvalidImportedDataBeforePublishing(t *testing.T) {
	t.Parallel()

	input := validV2Bundle()
	input.Manifest.Limits.MaxCanonicalFacts = 1
	directory := t.TempDir()
	if err := bundle.WriteBundle(context.Background(), directory, input); !errors.Is(err, bundle.ErrLimitExceeded) {
		t.Fatalf("WriteBundle() error = %v, want limit exceeded", err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("rejected bundle published partial files: %v", entries)
	}
}

func TestV1Alpha2WriteRejectsBeforeCreatingDestination(t *testing.T) {
	t.Parallel()

	input := validV2Bundle()
	input.Manifest.Limits.MaxCanonicalFacts = 1
	parent := t.TempDir()
	directory := filepath.Join(parent, "not-created", "bundle")
	if err := bundle.WriteBundle(context.Background(), directory, input); !errors.Is(err, bundle.ErrLimitExceeded) {
		t.Fatalf("WriteBundle() error = %v, want limit exceeded", err)
	}
	if _, err := os.Stat(directory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rejected WriteBundle() destination stat error = %v, want not exist", err)
	}
}

func TestV1Alpha2ReadRejectsInvalidImportedDataWithoutReturningPartialBundle(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	if err := bundle.WriteBundle(context.Background(), directory, validV2Bundle()); err != nil {
		t.Fatalf("WriteBundle() error = %v", err)
	}
	factsPath := filepath.Join(directory, bundle.CanonicalFactsFileName)
	facts, err := os.ReadFile(factsPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if err := os.WriteFile(factsPath, append(facts, []byte("{}\n")...), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	got, err := bundle.ReadBundle(context.Background(), directory)
	if err == nil {
		t.Fatal("ReadBundle() error = nil, want controlled rejection")
	}
	if !errors.Is(err, bundle.ErrCountMismatch) && !errors.Is(err, bundle.ErrDigestMismatch) && !errors.Is(err, bundle.ErrLimitExceeded) {
		t.Fatalf("ReadBundle() error = %v, want controlled sequence mismatch", err)
	}
	if got.Manifest.Version != "" || got.Artifacts != nil || got.Facts != nil {
		t.Fatalf("ReadBundle() returned partial bundle: %#v", got)
	}
}

func TestV1Alpha2ExtensionErrorsDoNotEchoPayload(t *testing.T) {
	t.Parallel()

	const secret = "bundle-secret-payload"
	input := validV2Bundle()
	input.Extensions[0] = []byte(`{"schema_id":"annotation-one","payload":"` + secret + `"}`)
	err := bundle.WriteBundle(context.Background(), t.TempDir(), input)
	if !errors.Is(err, bundle.ErrInvalidExtension) {
		t.Fatalf("Bundle.Validate() error = %v, want invalid extension", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("Bundle.Validate() echoed extension payload: %v", err)
	}
}

func TestV1Alpha2ExtensionEnvelopeMatchesDeclaredSchema(t *testing.T) {
	t.Parallel()

	input := validV2Bundle()
	payload := json.RawMessage(`{"kind":"annotation","value":"one"}`)
	schema := input.FrontendManifests[1].Extensions[0]
	envelope, err := json.Marshal(bundle.ExtensionRecord{
		SchemaID:      schema.ID,
		SchemaVersion: schema.Version,
		SchemaDigest:  schema.Digest,
		Schema:        canonicalJSONForTest(json.RawMessage(`{"type":"object","properties":{"kind":{"type":"string"},"value":{"type":"string"}}}`)),
		Payload:       payload,
	})
	if err != nil {
		t.Fatalf("Marshal(ExtensionRecord) error = %v", err)
	}
	input.Extensions[0] = envelope
	if err := bundle.WriteBundle(context.Background(), t.TempDir(), input); err != nil {
		t.Fatalf("WriteBundle() error = %v", err)
	}
}

func TestV1Alpha2DifferentPayloadsShareVerifiedSchema(t *testing.T) {
	t.Parallel()

	input := validV2Bundle()
	var first, second bundle.ExtensionRecord
	if err := json.Unmarshal(input.Extensions[0], &first); err != nil {
		t.Fatalf("decode first extension: %v", err)
	}
	if err := json.Unmarshal(input.Extensions[1], &second); err != nil {
		t.Fatalf("decode second extension: %v", err)
	}
	if first.SchemaID != second.SchemaID || first.SchemaVersion != second.SchemaVersion || first.SchemaDigest != second.SchemaDigest {
		t.Fatal("fixture payloads do not share one schema identity")
	}
	if string(first.Payload) == string(second.Payload) {
		t.Fatal("fixture payloads unexpectedly equal")
	}
	if err := bundle.WriteBundle(context.Background(), t.TempDir(), input); err != nil {
		t.Fatalf("WriteBundle() error = %v, want both payloads accepted", err)
	}
}

func TestV1Alpha2ExtensionRejectsSchemaMetadataOrBytesMismatch(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*bundle.ExtensionRecord)
	}{
		{
			name: "version",
			mutate: func(record *bundle.ExtensionRecord) {
				record.SchemaVersion = "2"
			},
		},
		{
			name: "digest",
			mutate: func(record *bundle.ExtensionRecord) {
				record.SchemaDigest = strings.Repeat("d", 64)
			},
		},
		{
			name: "schema bytes",
			mutate: func(record *bundle.ExtensionRecord) {
				record.Schema = json.RawMessage(`{"type":"array"}`)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := validV2Bundle()
			var record bundle.ExtensionRecord
			if err := json.Unmarshal(input.Extensions[0], &record); err != nil {
				t.Fatalf("decode extension: %v", err)
			}
			tt.mutate(&record)
			encoded, err := json.Marshal(record)
			if err != nil {
				t.Fatalf("encode extension: %v", err)
			}
			input.Extensions[0] = encoded
			if err := bundle.WriteBundle(context.Background(), t.TempDir(), input); !errors.Is(err, bundle.ErrInvalidExtension) {
				t.Fatalf("WriteBundle() error = %v, want invalid extension", err)
			}
		})
	}
}

func TestV1Alpha2RawExtensionPayloadIsNotAccepted(t *testing.T) {
	t.Parallel()

	input := validV2Bundle()
	input.Extensions[0] = json.RawMessage(`{"kind":"annotation","value":"one"}`)
	if err := bundle.WriteBundle(context.Background(), t.TempDir(), input); !errors.Is(err, bundle.ErrInvalidExtension) {
		t.Fatalf("WriteBundle() error = %v, want invalid extension", err)
	}
}

func TestReadImportedBundleRequiresCallerScopeAndFrontendAuthorization(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	if err := bundle.WriteBundle(context.Background(), directory, validV2Bundle()); err != nil {
		t.Fatalf("WriteBundle() error = %v", err)
	}
	validExpectation := bundle.ImportExpectation{
		OrganizationID:   validV2Bundle().Manifest.Organization.ID,
		SourceID:         validV2Bundle().Manifest.Source.ID,
		SnapshotID:       validV2Bundle().Manifest.Snapshot.ID,
		AllowedFrontends: validV2Bundle().FrontendManifests,
	}
	if _, err := bundle.ReadImportedBundle(context.Background(), directory, validExpectation); err != nil {
		t.Fatalf("ReadImportedBundle(valid expectation) error = %v", err)
	}

	wrongScope := validExpectation
	wrongScope.OrganizationID = "organization-other"
	if _, err := bundle.ReadImportedBundle(context.Background(), directory, wrongScope); !errors.Is(err, bundle.ErrScopeMismatch) {
		t.Fatalf("ReadImportedBundle(wrong scope) error = %v, want scope mismatch", err)
	}

	unauthorizedFrontend := validExpectation
	unauthorizedFrontend.AllowedFrontends = []fact.FrontendManifest{validV2Bundle().FrontendManifests[1]}
	if _, err := bundle.ReadImportedBundle(context.Background(), directory, unauthorizedFrontend); !errors.Is(err, bundle.ErrInvalidReference) {
		t.Fatalf("ReadImportedBundle(unauthorized frontend) error = %v, want invalid reference", err)
	}
}

func TestValidateImportedBundleEnforcesExpectedLimits(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	if err := bundle.WriteBundle(context.Background(), directory, validV2Bundle()); err != nil {
		t.Fatalf("WriteBundle() error = %v", err)
	}
	input, err := bundle.ReadBundle(context.Background(), directory)
	if err != nil {
		t.Fatalf("ReadBundle() error = %v", err)
	}
	expectation := importExpectationFor(input)
	expectation.Limits.MaxCanonicalFacts = 1
	if err := bundle.ValidateImportedBundle(input, expectation); !errors.Is(err, bundle.ErrLimitExceeded) {
		t.Fatalf("ValidateImportedBundle(limit) error = %v, want limit exceeded", err)
	}
}

func TestWriteImportedBundleRejectsExpectedLimitBeforeCreatingDestination(t *testing.T) {
	t.Parallel()

	input := validV2Bundle()
	expectation := importExpectationFor(input)
	expectation.Limits.MaxCanonicalFacts = 1
	parent := t.TempDir()
	directory := filepath.Join(parent, "not-created", "bundle")
	if err := bundle.WriteImportedBundle(context.Background(), directory, input, expectation); !errors.Is(err, bundle.ErrLimitExceeded) {
		t.Fatalf("WriteImportedBundle(limit) error = %v, want limit exceeded", err)
	}
	if _, err := os.Stat(directory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rejected WriteImportedBundle() destination stat error = %v, want not exist", err)
	}
}

func TestReadImportedBundleRejectsAdulteratedManifestWithAllowedIdentity(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	if err := bundle.WriteBundle(context.Background(), directory, validV2Bundle()); err != nil {
		t.Fatalf("WriteBundle() error = %v", err)
	}
	input, err := bundle.ReadBundle(context.Background(), directory)
	if err != nil {
		t.Fatalf("ReadBundle() error = %v", err)
	}
	trusted := validV2Bundle().FrontendManifests
	expectation := bundle.ImportExpectation{
		OrganizationID:   input.Manifest.Organization.ID,
		SourceID:         input.Manifest.Source.ID,
		SnapshotID:       input.Manifest.Snapshot.ID,
		AllowedFrontends: trusted,
	}
	input.FrontendManifests[1].Limitations = []string{"adulterated-capability"}
	digest, err := input.FactualDigest()
	if err != nil {
		t.Fatalf("FactualDigest() error = %v", err)
	}
	input.Manifest.FactualDigest = digest
	if err := bundle.ValidateImportedBundle(input, expectation); !errors.Is(err, bundle.ErrInvalidReference) {
		t.Fatalf("ValidateImportedBundle(adulterated frontend) error = %v, want invalid reference", err)
	}
}

func TestValidateImportedBundleRejectsAdulteratedSchemaWithAllowedIdentity(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	if err := bundle.WriteBundle(context.Background(), directory, validV2Bundle()); err != nil {
		t.Fatalf("WriteBundle() error = %v", err)
	}
	input, err := bundle.ReadBundle(context.Background(), directory)
	if err != nil {
		t.Fatalf("ReadBundle() error = %v", err)
	}
	trusted := validV2Bundle().FrontendManifests
	expectation := bundle.ImportExpectation{
		OrganizationID:   input.Manifest.Organization.ID,
		SourceID:         input.Manifest.Source.ID,
		SnapshotID:       input.Manifest.Snapshot.ID,
		AllowedFrontends: trusted,
	}
	const schema = `{"type":"array"}`
	input.FrontendManifests[1].Extensions[0].Digest = fact.ExtensionDigest([]byte(schema))
	for index, raw := range input.Extensions {
		var record bundle.ExtensionRecord
		if err := json.Unmarshal(raw, &record); err != nil {
			t.Fatalf("decode extension %d: %v", index, err)
		}
		record.Schema = json.RawMessage(schema)
		record.SchemaDigest = input.FrontendManifests[1].Extensions[0].Digest
		encoded, err := json.Marshal(record)
		if err != nil {
			t.Fatalf("encode extension %d: %v", index, err)
		}
		input.Extensions[index] = encoded
	}
	digest, err := input.FactualDigest()
	if err != nil {
		t.Fatalf("FactualDigest() error = %v", err)
	}
	input.Manifest.FactualDigest = digest
	if err := bundle.ValidateImportedBundle(input, expectation); !errors.Is(err, bundle.ErrInvalidReference) {
		t.Fatalf("ValidateImportedBundle(adulterated schema) error = %v, want invalid reference", err)
	}
}

func reidentifyFact(t *testing.T, canonicalFact *fact.CanonicalFact) {
	t.Helper()
	id, err := fact.FactID(*canonicalFact)
	if err != nil {
		t.Fatalf("FactID() error = %v", err)
	}
	canonicalFact.ID = id
}

func importExpectationFor(input bundle.Bundle) bundle.ImportExpectation {
	return bundle.ImportExpectation{
		OrganizationID:   input.Manifest.Organization.ID,
		SourceID:         input.Manifest.Source.ID,
		SnapshotID:       input.Manifest.Snapshot.ID,
		AllowedFrontends: validV2Bundle().FrontendManifests,
	}
}
