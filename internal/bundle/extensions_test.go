package bundle_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/pedrogpaulino/manu/internal/bundle"
	"github.com/pedrogpaulino/manu/internal/fact"
)

func TestExtensionRecordValidate(t *testing.T) {
	t.Parallel()

	input := validV2Bundle()
	var record bundle.ExtensionRecord
	if err := json.Unmarshal(input.Extensions[0], &record); err != nil {
		t.Fatalf("decode extension record: %v", err)
	}

	tests := []struct {
		name      string
		manifests func() []fact.FrontendManifest
		mutate    func(*bundle.ExtensionRecord)
		want      error
		contains  string
	}{
		{
			name:      "declared schema",
			manifests: func() []fact.FrontendManifest { return input.FrontendManifests },
		},
		{
			name:      "nil manifests",
			manifests: func() []fact.FrontendManifest { return nil },
			want:      bundle.ErrInvalidExtension,
		},
		{
			name:      "empty manifests",
			manifests: func() []fact.FrontendManifest { return []fact.FrontendManifest{} },
			want:      bundle.ErrInvalidExtension,
		},
		{
			name:      "schema is not declared",
			manifests: func() []fact.FrontendManifest { return input.FrontendManifests },
			mutate: func(candidate *bundle.ExtensionRecord) {
				candidate.SchemaID = "unknown-schema"
			},
			want: bundle.ErrInvalidExtension,
		},
		{
			name:      "schema bytes corrupted",
			manifests: func() []fact.FrontendManifest { return input.FrontendManifests },
			mutate: func(candidate *bundle.ExtensionRecord) {
				candidate.Schema = json.RawMessage(`{"type":"array"}`)
			},
			want: bundle.ErrInvalidExtension,
		},
		{
			name:      "schema digest corrupted",
			manifests: func() []fact.FrontendManifest { return input.FrontendManifests },
			mutate: func(candidate *bundle.ExtensionRecord) {
				candidate.SchemaDigest = strings.Repeat("d", 64)
			},
			want: bundle.ErrInvalidExtension,
		},
		{
			name:      "payload corrupted",
			manifests: func() []fact.FrontendManifest { return input.FrontendManifests },
			mutate: func(candidate *bundle.ExtensionRecord) {
				candidate.Payload = json.RawMessage(`{"secret":"bundle-extension-secret",`)
			},
			want:     bundle.ErrInvalidExtension,
			contains: "bundle-extension-secret",
		},
		{
			name: "conflicting declarations",
			manifests: func() []fact.FrontendManifest {
				manifests := append([]fact.FrontendManifest(nil), input.FrontendManifests...)
				conflict := manifests[0]
				conflict.Extensions = []fact.ExtensionSchema{{
					ID: record.SchemaID, Version: record.SchemaVersion, Digest: strings.Repeat("0", 64),
				}}
				return append(manifests, conflict)
			},
			want: bundle.ErrInvalidExtension,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidate := record
			if tt.mutate != nil {
				tt.mutate(&candidate)
			}
			err := candidate.Validate(tt.manifests())
			if tt.want == nil {
				if err != nil {
					t.Fatalf("ExtensionRecord.Validate() error = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tt.want) {
				t.Fatalf("ExtensionRecord.Validate() error = %v, want errors.Is(..., %v)", err, tt.want)
			}
			if tt.contains != "" && strings.Contains(err.Error(), tt.contains) {
				t.Fatalf("ExtensionRecord.Validate() echoed secret payload: %v", err)
			}
		})
	}
}

func TestExtensionRecordValidateDoesNotMutateInputs(t *testing.T) {
	t.Parallel()

	input := validV2Bundle()
	manifests := append([]fact.FrontendManifest(nil), input.FrontendManifests...)
	var record bundle.ExtensionRecord
	if err := json.Unmarshal(input.Extensions[0], &record); err != nil {
		t.Fatalf("decode extension record: %v", err)
	}
	wantManifests := append([]fact.FrontendManifest(nil), manifests...)
	wantRecord := record
	if err := record.Validate(manifests); err != nil {
		t.Fatalf("ExtensionRecord.Validate() error = %v, want nil", err)
	}
	if !reflect.DeepEqual(manifests, wantManifests) {
		t.Fatalf("manifests mutated: got %#v, want %#v", manifests, wantManifests)
	}
	if !reflect.DeepEqual(record, wantRecord) {
		t.Fatalf("extension record mutated: got %#v, want %#v", record, wantRecord)
	}
}
