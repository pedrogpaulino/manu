package persistence

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/pedrogpaulino/manu/internal/contract"
)

func TestProjectContextSnapshotLocator(t *testing.T) {
	const (
		factualSourceID     = "source-external"
		canonicalSourceID   = "11111111-1111-1111-1111-111111111111"
		canonicalArtifactID = "22222222-2222-2222-2222-222222222222"
	)

	marshal := func(locator contract.Locator) []byte {
		raw, err := json.Marshal(locator)
		if err != nil {
			t.Fatalf("json.Marshal() error = %v", err)
		}
		return raw
	}

	withExternalIDs := contract.Locator{
		URI:         "file:///workspace/src/main.go",
		SourceID:    factualSourceID,
		ArtifactID:  "artifact-external",
		Path:        "src/main.go",
		Member:      "main",
		StartLine:   10,
		StartColumn: 2,
		EndLine:     14,
		EndColumn:   6,
		ByteOffset:  128,
		ByteLength:  64,
	}
	withoutIDs := contract.Locator{Path: "src/main.go", StartLine: 10, EndLine: 14}
	artifactID := canonicalArtifactID
	invalidArtifactID := "not-a-uuid"

	tests := []struct {
		name              string
		raw               []byte
		artifactID        *string
		factualSourceID   string
		canonicalSourceID string
		want              *contract.Locator
		wantErr           bool
	}{
		{
			name:              "nil raw and nil artifact",
			raw:               nil,
			factualSourceID:   factualSourceID,
			canonicalSourceID: canonicalSourceID,
		},
		{
			name:              "projects external source and artifact IDs",
			raw:               marshal(withExternalIDs),
			artifactID:        &artifactID,
			factualSourceID:   factualSourceID,
			canonicalSourceID: canonicalSourceID,
			want: &contract.Locator{
				URI:         withExternalIDs.URI,
				SourceID:    canonicalSourceID,
				ArtifactID:  canonicalArtifactID,
				Path:        withExternalIDs.Path,
				Member:      withExternalIDs.Member,
				StartLine:   withExternalIDs.StartLine,
				StartColumn: withExternalIDs.StartColumn,
				EndLine:     withExternalIDs.EndLine,
				EndColumn:   withExternalIDs.EndColumn,
				ByteOffset:  withExternalIDs.ByteOffset,
				ByteLength:  withExternalIDs.ByteLength,
			},
		},
		{
			name:              "preserves empty source and artifact IDs",
			raw:               marshal(withoutIDs),
			factualSourceID:   factualSourceID,
			canonicalSourceID: canonicalSourceID,
			want:              &withoutIDs,
		},
		{
			name:              "invalid raw",
			raw:               []byte("{"),
			factualSourceID:   factualSourceID,
			canonicalSourceID: canonicalSourceID,
			wantErr:           true,
		},
		{
			name: "divergent external source ID",
			raw: marshal(contract.Locator{
				SourceID: factualSourceID + "-other", ArtifactID: withExternalIDs.ArtifactID,
				Path: withExternalIDs.Path, StartLine: withExternalIDs.StartLine, EndLine: withExternalIDs.EndLine,
			}),
			artifactID:        &artifactID,
			factualSourceID:   factualSourceID,
			canonicalSourceID: canonicalSourceID,
			wantErr:           true,
		},
		{
			name:              "external artifact ID without join",
			raw:               marshal(withExternalIDs),
			factualSourceID:   factualSourceID,
			canonicalSourceID: canonicalSourceID,
			wantErr:           true,
		},
		{
			name:              "invalid joined artifact UUID",
			raw:               marshal(withExternalIDs),
			artifactID:        &invalidArtifactID,
			factualSourceID:   factualSourceID,
			canonicalSourceID: canonicalSourceID,
			wantErr:           true,
		},
		{
			name:              "joined artifact with empty locator artifact ID",
			raw:               marshal(withoutIDs),
			artifactID:        &artifactID,
			factualSourceID:   factualSourceID,
			canonicalSourceID: canonicalSourceID,
			wantErr:           true,
		},
		{
			name:              "nil raw with joined artifact",
			raw:               nil,
			artifactID:        &artifactID,
			factualSourceID:   factualSourceID,
			canonicalSourceID: canonicalSourceID,
			wantErr:           true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := projectContextSnapshotLocator(tt.raw, tt.artifactID, tt.factualSourceID, tt.canonicalSourceID)
			if tt.wantErr {
				if err == nil {
					t.Fatal("projectContextSnapshotLocator() error = nil, want ErrInconsistent")
				}
				if !errors.Is(err, ErrInconsistent) {
					t.Fatalf("projectContextSnapshotLocator() error = %v, want ErrInconsistent", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("projectContextSnapshotLocator() error = %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("projectContextSnapshotLocator() = %#v, want %#v", got, tt.want)
			}
		})
	}
}
