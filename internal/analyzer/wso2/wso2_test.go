package wso2

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/pedrogpaulino/manu/internal/analysis"
	"github.com/pedrogpaulino/manu/internal/contract"
)

func TestParseXMLExtractsTypesReferencesAndDynamicGaps(t *testing.T) {
	input := analysis.ArtifactInput{
		SourceID: "source",
		Artifact: contract.Artifact{ID: "artifact", Path: "bundle/proxy.xml", Type: analysis.ArtifactTypeXML, Hash: strings.Repeat("a", 64)},
	}
	output, err := New().parseXML(context.Background(), input, "", []byte(`<proxy name="BookingProxy" targetEndpoint="bookingEndpoint"><property value="${ctx.key}"/><import location="conf/booking.xml"/><endpoint uri="https://user:pass@example.test/api?token=secret#fragment"/></proxy>`))
	if err != nil {
		t.Fatal(err)
	}
	if len(output.Contributions) < 4 {
		t.Fatalf("contributions = %d, want type, references, and include observations", len(output.Contributions))
	}
	encoded, err := json.Marshal(output)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "user:pass") || strings.Contains(string(encoded), "token=secret") {
		t.Fatalf("sensitive URI material leaked: %s", encoded)
	}
	foundDynamic := false
	for _, gap := range output.Gaps {
		if gap.Code == "dynamic_reference" {
			foundDynamic = true
		}
	}
	if !foundDynamic {
		t.Fatal("dynamic reference gap was not emitted")
	}
}

func TestSanitizeLiteralRedactsURLCredentialsAndQuery(t *testing.T) {
	got, redacted := sanitizeLiteral("https://user:pass@example.test/path?token=secret#fragment")
	if !redacted {
		t.Fatal("sanitizeLiteral() redacted = false, want true")
	}
	if strings.Contains(got, "pass") || strings.Contains(got, "token") || strings.Contains(got, "fragment") {
		t.Fatalf("sanitized URI = %q, contains sensitive material", got)
	}
}
