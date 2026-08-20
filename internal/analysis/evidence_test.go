package analysis_test

import (
	"strings"
	"testing"

	"github.com/pedrogpaulino/manu/internal/analysis"
)

func TestSanitizeEvidenceContentRejectsSensitiveMaterial(t *testing.T) {
	tests := []struct {
		name    string
		content string
		wantRed bool
		wantOut string
	}{
		{name: "password", content: "password=super-secret", wantRed: true, wantOut: "[redacted]"},
		{name: "authorization", content: "Authorization: Bearer abc.def.ghi", wantRed: true, wantOut: "[redacted]"},
		{name: "private key", content: "-----BEGIN PRIVATE KEY-----\nsecret\n-----END PRIVATE KEY-----", wantRed: true, wantOut: "[redacted]"},
		{name: "safe text", content: "class Inventory {}", wantRed: false, wantOut: "class Inventory {}"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := analysis.SanitizeEvidenceContent(test.content)
			if got.Redacted != test.wantRed || got.Content != test.wantOut {
				t.Fatalf("SanitizeEvidenceContent() = %#v, want redacted=%v content=%q", got, test.wantRed, test.wantOut)
			}
			if test.wantRed && strings.Contains(got.Content, "secret") {
				t.Fatalf("redacted output retained secret material: %q", got.Content)
			}
		})
	}
}
func TestEvidenceLimitsZeroUsesSafeDefaults(t *testing.T) {
	limits := analysis.EvidenceLimits{}
	if err := limits.Validate(); err != nil {
		t.Fatal(err)
	}
	if analysis.DefaultEvidenceMaxUnitsPerArtifact <= 0 || analysis.DefaultEvidenceMaxBytesPerUnit <= 0 || analysis.DefaultEvidenceMaxCharactersPerUnit <= 0 {
		t.Fatal("evidence defaults must be positive")
	}
}
