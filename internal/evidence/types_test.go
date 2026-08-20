package evidence_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/evidence"
)

func TestEvidenceUnitValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		mutate  func(*evidence.EvidenceUnit)
		wantErr error
	}{
		{
			name:    "valid present content",
			mutate:  func(*evidence.EvidenceUnit) {},
			wantErr: nil,
		},
		{
			name: "unsupported version",
			mutate: func(unit *evidence.EvidenceUnit) {
				unit.Version = "v0"
			},
			wantErr: evidence.ErrUnsupportedVersion,
		},
		{
			name: "missing identity",
			mutate: func(unit *evidence.EvidenceUnit) {
				unit.ID = ""
			},
			wantErr: evidence.ErrInvalid,
		},
		{
			name: "identity does not match deterministic identity",
			mutate: func(unit *evidence.EvidenceUnit) {
				unit.ID = "evidence-other"
			},
			wantErr: evidence.ErrInvalid,
		},
		{
			name: "identity with whitespace",
			mutate: func(unit *evidence.EvidenceUnit) {
				unit.ArtifactID = "artifact with spaces"
			},
			wantErr: evidence.ErrInvalid,
		},
		{
			name: "missing contribution reference",
			mutate: func(unit *evidence.EvidenceUnit) {
				unit.Contribution.ID = ""
			},
			wantErr: evidence.ErrInvalid,
		},
		{
			name: "contribution points to another artifact",
			mutate: func(unit *evidence.EvidenceUnit) {
				unit.Contribution.ArtifactID = "artifact-other"
			},
			wantErr: evidence.ErrInvalid,
		},
		{
			name: "locator points to another source",
			mutate: func(unit *evidence.EvidenceUnit) {
				unit.Locator.SourceID = "source-other"
			},
			wantErr: evidence.ErrInvalid,
		},
		{
			name: "locator is empty",
			mutate: func(unit *evidence.EvidenceUnit) {
				unit.Locator = contract.Locator{}
			},
			wantErr: evidence.ErrUnsafeLocator,
		},
		{
			name: "locator path traverses parent",
			mutate: func(unit *evidence.EvidenceUnit) {
				unit.Locator.Path = "src/../secrets.txt"
			},
			wantErr: evidence.ErrUnsafeLocator,
		},
		{
			name: "locator path is absolute",
			mutate: func(unit *evidence.EvidenceUnit) {
				unit.Locator.Path = "/etc/passwd"
			},
			wantErr: evidence.ErrUnsafeLocator,
		},
		{
			name: "locator path is drive relative",
			mutate: func(unit *evidence.EvidenceUnit) {
				unit.Locator.Path = "C:secret.txt"
			},
			wantErr: evidence.ErrUnsafeLocator,
		},
		{
			name: "locator path contains control",
			mutate: func(unit *evidence.EvidenceUnit) {
				unit.Locator.Path = "src/secret\x00.txt"
			},
			wantErr: evidence.ErrUnsafeLocator,
		},
		{
			name: "unsupported content state",
			mutate: func(unit *evidence.EvidenceUnit) {
				unit.ContentState = evidence.ContentState("unknown")
			},
			wantErr: evidence.ErrInvalidContent,
		},
		{
			name: "present content has wrong hash",
			mutate: func(unit *evidence.EvidenceUnit) {
				unit.ContentHash = evidence.ContentDigest("other content")
			},
			wantErr: evidence.ErrInvalidDigest,
		},
		{
			name: "present content has wrong byte count",
			mutate: func(unit *evidence.EvidenceUnit) {
				unit.ContentBytes++
			},
			wantErr: evidence.ErrInvalidContent,
		},
		{
			name: "present content has redaction reason",
			mutate: func(unit *evidence.EvidenceUnit) {
				unit.RedactionReason = "policy"
			},
			wantErr: evidence.ErrInvalidContent,
		},
		{
			name: "redacted content requires reason",
			mutate: func(unit *evidence.EvidenceUnit) {
				unit.ContentState = evidence.ContentStateRedacted
			},
			wantErr: evidence.ErrInvalidContent,
		},
		{
			name: "omitted content cannot retain text",
			mutate: func(unit *evidence.EvidenceUnit) {
				unit.ContentState = evidence.ContentStateOmitted
			},
			wantErr: evidence.ErrInvalidContent,
		},
		{
			name: "invalid persistence decision",
			mutate: func(unit *evidence.EvidenceUnit) {
				unit.Persist = evidence.Decision("maybe")
			},
			wantErr: evidence.ErrInvalidDecision,
		},
		{
			name: "invalid transfer decision",
			mutate: func(unit *evidence.EvidenceUnit) {
				unit.ExternalTransfer = evidence.Decision("maybe")
			},
			wantErr: evidence.ErrInvalidDecision,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			unit := validUnit()
			tt.mutate(&unit)
			err := unit.Validate()
			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("EvidenceUnit.Validate() error = %v", err)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("EvidenceUnit.Validate() error = %v, want errors.Is(..., %v)", err, tt.wantErr)
			}
		})
	}
}

func TestEvidenceUnitValidateRedactedAndOmitted(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		state evidence.ContentState
		setup func(*evidence.EvidenceUnit)
	}{
		{
			name:  "redacted text",
			state: evidence.ContentStateRedacted,
			setup: func(unit *evidence.EvidenceUnit) {
				unit.Content = "[redacted]"
				unit.ContentBytes = int64(len(unit.Content))
				unit.ContentCharacters = int64(utf8.RuneCountInString(unit.Content))
				unit.RedactionReason = "external transfer policy"
			},
		},
		{
			name:  "omitted source text",
			state: evidence.ContentStateOmitted,
			setup: func(unit *evidence.EvidenceUnit) {
				unit.Content = ""
				unit.ContentBytes = 0
				unit.ContentCharacters = 0
				unit.RedactionReason = "persistence policy"
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			unit := validUnit()
			unit.ContentState = tt.state
			tt.setup(&unit)
			if err := unit.Validate(); err != nil {
				t.Fatalf("EvidenceUnit.Validate() error = %v", err)
			}
		})
	}
}

func TestDecisionValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   evidence.Decision
		wantErr bool
	}{
		{name: "allow", input: evidence.DecisionAllow},
		{name: "redact", input: evidence.DecisionRedact},
		{name: "deny", input: evidence.DecisionDeny},
		{name: "unset", input: evidence.DecisionUnknown, wantErr: true},
		{name: "future value", input: evidence.Decision("review"), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.input.Validate()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Decision.Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestContentStateValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   evidence.ContentState
		wantErr bool
	}{
		{name: "present", input: evidence.ContentStatePresent},
		{name: "redacted", input: evidence.ContentStateRedacted},
		{name: "omitted", input: evidence.ContentStateOmitted},
		{name: "unset", input: evidence.ContentStateUnknown, wantErr: true},
		{name: "future value", input: evidence.ContentState("masked"), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.input.Validate()
			if (err != nil) != tt.wantErr {
				t.Fatalf("ContentState.Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestEvidenceUnitJSONRoundTrip(t *testing.T) {
	t.Parallel()

	want := validUnit()
	encoded, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	var got evidence.EvidenceUnit
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("JSON round trip changed unit:\n got %#v\nwant %#v", got, want)
	}
	if !strings.Contains(string(encoded), `"external_transfer":"allow"`) {
		t.Fatalf("JSON omitted independent transfer decision: %s", encoded)
	}
}

func TestEvidenceUnitTruncationIsSerializedAndIdentified(t *testing.T) {
	t.Parallel()

	unit := validUnit()
	unit.Truncated = true
	unit.ID = evidence.EvidenceID(unit)
	if err := unit.Validate(); err != nil {
		t.Fatalf("truncated EvidenceUnit.Validate() error = %v", err)
	}
	encoded, err := json.Marshal(unit)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if !strings.Contains(string(encoded), `"truncated":true`) {
		t.Fatalf("JSON omitted truncation marker: %s", encoded)
	}
	var decoded evidence.EvidenceUnit
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if !reflect.DeepEqual(decoded, unit) {
		t.Fatalf("JSON round trip changed truncation: got %#v want %#v", decoded, unit)
	}
	withoutTruncation := unit
	withoutTruncation.Truncated = false
	withoutTruncation.ID = evidence.EvidenceID(withoutTruncation)
	if withoutTruncation.ID == unit.ID {
		t.Fatal("truncation flag did not change deterministic identity")
	}
}

func TestDigestAndEvidenceIDAreDeterministic(t *testing.T) {
	t.Parallel()

	unit := validUnit()
	if got, want := evidence.ContentDigest("hello"), "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"; got != want {
		t.Fatalf("ContentDigest() = %q, want %q", got, want)
	}
	if got, want := evidence.Digest([]byte("hello")), evidence.ContentDigest("hello"); got != want {
		t.Fatalf("Digest() = %q, ContentDigest() = %q", got, want)
	}

	first := evidence.EvidenceID(unit)
	second := evidence.EvidenceID(unit)
	if first == "" || first != second {
		t.Fatalf("EvidenceID() is not deterministic: first=%q second=%q", first, second)
	}
	changedPolicy := unit
	changedPolicy.ExternalTransfer = evidence.DecisionDeny
	if got := evidence.EvidenceID(changedPolicy); got != first {
		t.Fatalf("policy changed factual identity: got %q, want %q", got, first)
	}
	changedContent := unit
	changedContent.Content = "other"
	changedContent.ContentHash = evidence.ContentDigest(changedContent.Content)
	changedContent.ContentBytes = int64(len(changedContent.Content))
	changedContent.ContentCharacters = int64(utf8.RuneCountInString(changedContent.Content))
	if got := evidence.EvidenceID(changedContent); got == first {
		t.Fatal("content change did not change evidence identity")
	}
}

func TestEvidenceUnitValidateWithLimits(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		limits  evidence.UnitLimits
		wantErr error
	}{
		{name: "unbounded", limits: evidence.UnitLimits{}},
		{name: "exact bytes and characters", limits: evidence.UnitLimits{MaxBytes: 12, MaxCharacters: 12}},
		{name: "byte limit exceeded", limits: evidence.UnitLimits{MaxBytes: 11}, wantErr: evidence.ErrLimitExceeded},
		{name: "character limit exceeded", limits: evidence.UnitLimits{MaxCharacters: 11}, wantErr: evidence.ErrLimitExceeded},
		{name: "negative byte limit", limits: evidence.UnitLimits{MaxBytes: -1}, wantErr: evidence.ErrLimitExceeded},
		{name: "negative character limit", limits: evidence.UnitLimits{MaxCharacters: -1}, wantErr: evidence.ErrLimitExceeded},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			unit := validUnit()
			err := unit.ValidateWithLimits(tt.limits)
			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("EvidenceUnit.ValidateWithLimits() error = %v", err)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("EvidenceUnit.ValidateWithLimits() error = %v, want errors.Is(..., %v)", err, tt.wantErr)
			}
		})
	}
}

func validUnit() evidence.EvidenceUnit {
	const content = "class Foo {}"
	unit := evidence.EvidenceUnit{
		Version:        evidence.Version,
		OrganizationID: "organization-1",
		SourceID:       "source-1",
		SnapshotID:     "snapshot-1",
		ArtifactID:     "artifact-1",
		Contribution: evidence.ContributionRef{
			ID:              "contribution-1",
			ArtifactID:      "artifact-1",
			AnalyzerID:      "java",
			AnalyzerVersion: "1",
			Method:          "symbols",
		},
		Locator: contract.Locator{
			SourceID:   "source-1",
			ArtifactID: "artifact-1",
			Path:       "src/Foo.java",
			StartLine:  1,
			EndLine:    1,
		},
		ContentState:      evidence.ContentStatePresent,
		Content:           content,
		ContentHash:       evidence.ContentDigest(content),
		ContentBytes:      int64(len(content)),
		ContentCharacters: int64(utf8.RuneCountInString(content)),
		Persist:           evidence.DecisionAllow,
		ExternalTransfer:  evidence.DecisionAllow,
	}
	unit.ID = evidence.EvidenceID(unit)
	return unit
}
