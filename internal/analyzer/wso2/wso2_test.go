package wso2

import (
	"archive/zip"
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/pedrogpaulino/manu/internal/analysis"
	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/source"
)

// The fixtures are deliberately small and versioned with the analyzer. They
// are embedded only by tests; the runtime never reads this directory.
//
//go:embed testdata/*.xml
var fixtureFiles embed.FS

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

func TestParseXMLExtractsSustainedWSO2Contributions(t *testing.T) {
	data := readFixture(t, "testdata/api-v1.xml")
	input := xmlInput("api.xml")
	input.Evidence.Enabled = true
	output, err := New().parseXML(context.Background(), input, "", data)
	if err != nil {
		t.Fatal(err)
	}

	var typePayloads, endpointPayloads, messagePayloads, configurationPayloads []map[string]any
	for _, contribution := range output.Contributions {
		var payload map[string]any
		if err := json.Unmarshal(contribution.Value, &payload); err != nil {
			t.Fatalf("decode %s payload: %v", contribution.Type, err)
		}
		switch contribution.Type {
		case "wso2.type":
			typePayloads = append(typePayloads, payload)
		case endpointContributionType:
			endpointPayloads = append(endpointPayloads, payload)
		case messageContributionType:
			messagePayloads = append(messagePayloads, payload)
		case configurationContributionType:
			configurationPayloads = append(configurationPayloads, payload)
		}
		if contribution.Locator.Member != "" {
			t.Fatalf("standalone contribution unexpectedly has member: %#v", contribution.Locator)
		}
	}
	if len(typePayloads) == 0 || len(endpointPayloads) == 0 || len(messagePayloads) < 1 || len(configurationPayloads) == 0 {
		t.Fatalf("contribution counts = type:%d endpoint:%d message:%d configuration:%d", len(typePayloads), len(endpointPayloads), len(messagePayloads), len(configurationPayloads))
	}
	assertPayloadValue(t, typePayloads, "kind", "api")
	apiType := findPayload(typePayloads, "kind", "api")
	if apiType["name"] != "OrdersAPI" || apiType["name_source"] != "declared_name" || apiType["path"] != "api" {
		t.Fatalf("api type payload = %#v", apiType)
	}
	address := findPayload(endpointPayloads, "kind", "address")
	if address["uri"] != "https://api.example.test/orders" || address["xml_path"] != "api/endpoint/address" {
		t.Fatalf("address endpoint payload = %#v", address)
	}
	if strings.Contains(string(mustJSON(t, address)), "pass") || strings.Contains(string(mustJSON(t, address)), "tenant=fixture") {
		t.Fatalf("sanitized endpoint payload retained URI secret material: %#v", address)
	}
	payloadFactory := findPayload(messagePayloads, "kind", "payloadFactory")
	if payloadFactory["media_type"] != "json" || strings.Contains(string(mustJSON(t, payloadFactory)), "$1") {
		t.Fatalf("payloadFactory payload = %#v", payloadFactory)
	}
	configuration := findPayload(configurationPayloads, "key", "orders.timeout")
	if configuration["kind"] != "property" || configuration["path"] != "api/property" {
		t.Fatalf("configuration payload = %#v", configuration)
	}
	if strings.Contains(string(mustJSON(t, configuration)), "30") {
		t.Fatalf("configuration payload retained value: %#v", configuration)
	}
	if !hasContributionTarget(output.Contributions, "wso2.include", "synapse/shared-v1.xml") {
		t.Fatal("literal include target was not preserved")
	}
	if !hasContributionTarget(output.Contributions, "wso2.reference", "orders-sequence") {
		t.Fatal("literal reference target was not preserved")
	}
	for _, draft := range output.Evidence {
		if draft.ContributionID == "" || draft.Locator.ByteOffset < 0 || strings.Contains(draft.Content, "<") || strings.Contains(draft.Content, "pass") {
			t.Fatalf("unsafe or incomplete evidence draft = %#v", draft)
		}
	}
	serialized := string(mustJSON(t, output))
	for _, secret := range []string{"secret-value", "https://user:pass", "tenant=fixture", "{&quot;id&quot;"} {
		if strings.Contains(serialized, secret) {
			t.Fatalf("serialized output retained source-only material %q: %s", secret, serialized)
		}
	}
}

func TestAnalyzeCARCorrelatesMembersAndEvidence(t *testing.T) {
	root := t.TempDir()
	archivePath := filepath.Join(root, "fixture.car")
	archiveData := makeCAR(t, map[string][]byte{
		"synapse/api-v1.xml":    readFixture(t, "testdata/api-v1.xml"),
		"synapse/shared-v1.xml": readFixture(t, "testdata/shared-v1.xml"),
	})
	if err := os.WriteFile(archivePath, archiveData, 0o600); err != nil {
		t.Fatal(err)
	}
	rootHandle, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer rootHandle.Close()
	input := analysis.ArtifactInput{
		SourceID: "wso2-source",
		Artifact: contract.Artifact{
			ID:       "fixture-car",
			SourceID: "wso2-source",
			Path:     "fixture.car",
			Type:     analysis.ArtifactTypeCAR,
			Hash:     strings.Repeat("b", 64),
		},
		RootHandle: rootHandle,
		Limits: source.Limits{
			MaxArchiveMembers:         16,
			MaxArchiveBytes:           1 << 20,
			MaxArchiveMemberBytes:     1 << 20,
			MaxArchiveCompressedBytes: 1 << 20,
			MaxExpansionRatio:         100,
			MaxExtractionBytes:        1 << 20,
		},
		Evidence: analysis.EvidenceInput{Enabled: true},
	}
	first, err := New().Analyze(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := New().Analyze(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("repeated CAR analysis was not deterministic:\nfirst=%s\nsecond=%s", mustJSON(t, first), mustJSON(t, second))
	}
	memberSeen := map[string]bool{}
	artifactID := ""
	for _, contribution := range first.Contributions {
		if contribution.AnalyzerID != AnalyzerID {
			continue
		}
		if artifactID == "" {
			artifactID = contribution.ArtifactID
		}
		if contribution.ArtifactID != artifactID || contribution.Locator.Member == "" || contribution.Locator.ByteOffset < 0 {
			t.Fatalf("CAR contribution is not member-correlatable: %#v", contribution)
		}
		memberSeen[contribution.Locator.Member] = true
	}
	if !memberSeen["synapse/api-v1.xml"] || !memberSeen["synapse/shared-v1.xml"] {
		t.Fatalf("CAR members observed = %#v", memberSeen)
	}
	if !hasContributionTarget(first.Contributions, "wso2.include", "synapse/shared-v1.xml") || !hasContributionTarget(first.Contributions, "wso2.include", "synapse/api-v1.xml") {
		t.Fatal("cross-member literal include targets were not preserved")
	}
	for _, draft := range first.Evidence {
		if draft.Locator.Member == "" || draft.Locator.ByteOffset < 0 || strings.Contains(draft.Content, "<") {
			t.Fatalf("CAR evidence draft = %#v", draft)
		}
	}
}

func TestDynamicWSO2MetadataProducesGapsAndIncompleteCoverage(t *testing.T) {
	input := xmlInput("dynamic.xml")
	output, err := New().parseXML(context.Background(), input, "", []byte(`<api context="${ctx.path}"><payloadFactory media-type="${ctx.media}"/><property name="${ctx.key}" value="${ctx.value}"/></api>`))
	if err != nil {
		t.Fatal(err)
	}
	serialized := string(mustJSON(t, output))
	for _, expression := range []string{"${ctx.path}", "${ctx.media}", "${ctx.key}", "${ctx.value}"} {
		if strings.Contains(serialized, expression) {
			t.Fatalf("dynamic expression leaked into output: %s", serialized)
		}
	}
	if !hasGap(output.Gaps, dynamicEndpointGapCode) || !hasGap(output.Gaps, dynamicMessageGapCode) || !hasGap(output.Gaps, dynamicConfigurationGapCode) {
		t.Fatalf("dynamic gaps = %#v", output.Gaps)
	}
	if !hasIncompleteCoverage(output.Coverage, string(contract.DimensionEntitiesAndRelationships)) ||
		!hasIncompleteCoverage(output.Coverage, string(contract.DimensionFlowsAndDependencies)) ||
		!hasIncompleteCoverage(output.Coverage, string(contract.DimensionConfigurationVariations)) {
		t.Fatalf("dynamic coverage = %#v", output.Coverage)
	}
}

func TestWSO2RedactedEndpointIsNotInvented(t *testing.T) {
	input := xmlInput("redacted.xml")
	output, err := New().parseXML(context.Background(), input, "", []byte(`<endpoint uri="secret-value"/>`))
	if err != nil {
		t.Fatal(err)
	}
	for _, contribution := range output.Contributions {
		if contribution.Type == endpointContributionType {
			t.Fatalf("redacted endpoint was emitted: %s", contribution.Value)
		}
	}
	if !hasGap(output.Gaps, redactedEndpointGapCode) {
		t.Fatalf("gaps = %#v, want redacted endpoint gap", output.Gaps)
	}
}

func xmlInput(path string) analysis.ArtifactInput {
	return analysis.ArtifactInput{
		SourceID: "wso2-test-source",
		Artifact: contract.Artifact{
			ID:       "wso2-test-artifact",
			SourceID: "wso2-test-source",
			Path:     path,
			Type:     analysis.ArtifactTypeXML,
			Hash:     strings.Repeat("a", 64),
		},
	}
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := fixtureFiles.ReadFile(name)
	if err != nil {
		t.Fatalf("read fixture %q: %v", name, err)
	}
	return data
}

func makeCAR(t *testing.T, members map[string][]byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	names := make([]string, 0, len(members))
	for name := range members {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write(members[name]); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func findPayload(payloads []map[string]any, key, value string) map[string]any {
	for _, payload := range payloads {
		if payload[key] == value {
			return payload
		}
	}
	return nil
}

func assertPayloadValue(t *testing.T, payloads []map[string]any, key, value string) {
	t.Helper()
	if findPayload(payloads, key, value) == nil {
		t.Fatalf("payloads do not contain %s=%q: %#v", key, value, payloads)
	}
}

func hasContributionTarget(contributions []contract.Contribution, typ, target string) bool {
	for _, contribution := range contributions {
		if contribution.Type != typ {
			continue
		}
		var payload map[string]any
		if json.Unmarshal(contribution.Value, &payload) == nil && payload["target"] == target {
			return true
		}
	}
	return false
}

func hasGap(gaps []contract.Gap, code string) bool {
	for _, gap := range gaps {
		if gap.Code == code {
			return true
		}
	}
	return false
}

func hasIncompleteCoverage(coverage []contract.Coverage, dimension string) bool {
	for _, entry := range coverage {
		if entry.Dimension == dimension && entry.State == contract.CoverageIncomplete {
			return true
		}
	}
	return false
}
