package java

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pedrogpaulino/manu/internal/analysis"
	"github.com/pedrogpaulino/manu/internal/contract"
)

func TestParseExtractsConfigurationOnlyOutsideLiterals(t *testing.T) {
	parsed := parse(`package example;
String fake = "@ConfigProperty(name = 'fake.key') System.getenv('fake.env')";
// @ConfigProperty(name = "comment.key")
@ConfigProperty(name = "real.key")
void configure() { System.getenv("REAL_ENV"); }`)

	keys := make(map[string]bool)
	for _, observation := range parsed.observations {
		if observation.Type != "java.configuration" {
			continue
		}
		key, ok := observation.Value["key"].(string)
		if ok {
			keys[key] = true
		}
	}
	if !keys["real.key"] || !keys["REAL_ENV"] {
		t.Fatalf("configuration keys = %#v, want real.key and REAL_ENV", keys)
	}
	if keys["fake.key"] || keys["fake.env"] || keys["comment.key"] {
		t.Fatalf("configuration literal/comment false positives = %#v", keys)
	}
}

func TestParseExtractsDirectJavaRelations(t *testing.T) {
	parsed := parse(`class BookingService extends BaseService implements Auditable, Traceable {
    Booking create() { return repository.save(new Booking()); }
}`)
	methods := make(map[string]bool)
	for _, observation := range parsed.observations {
		methods[observation.Method] = true
	}
	for _, want := range []string{
		"relation:BookingService:extends:BaseService",
		"relation:BookingService:implements:Auditable",
		"relation:BookingService:implements:Traceable",
	} {
		if !methods[want] {
			t.Fatalf("missing relation %q in %#v", want, methods)
		}
	}
}

func TestAnalyzeQuarkus3FixtureEmitsArtifactEndpointsAndExistingObservations(t *testing.T) {
	fixturePath := filepath.Join("testdata", "quarkus3", "BookingResource.java")
	root, err := os.OpenRoot(filepath.Dir(fixturePath))
	if err != nil {
		t.Fatalf("open fixture root: %v", err)
	}
	defer root.Close()

	input := analysis.ArtifactInput{
		SourceID:   "source-quarkus3",
		RootHandle: root,
		Artifact: contract.Artifact{
			ID:       "artifact-quarkus3-booking",
			SourceID: "source-quarkus3",
			Path:     "BookingResource.java",
			Type:     analysis.ArtifactTypeJava,
		},
	}
	output, err := New().Analyze(context.Background(), input)
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}

	byType := make(map[string][]contract.Contribution)
	for _, contribution := range output.Contributions {
		byType[contribution.Type] = append(byType[contribution.Type], contribution)
		if strings.Contains(string(contribution.Value), "return Response") || strings.Contains(string(contribution.Value), "package com.example") {
			t.Fatalf("contribution %s copied source content: %s", contribution.Type, contribution.Value)
		}
	}
	artifactContributions := byType["java.artifact"]
	if len(artifactContributions) != 1 {
		t.Fatalf("artifact contributions = %d, want one", len(artifactContributions))
	}
	var artifactValue map[string]any
	if err := json.Unmarshal(artifactContributions[0].Value, &artifactValue); err != nil {
		t.Fatalf("artifact value is not JSON: %v", err)
	}
	if artifactValue["path"] != "BookingResource.java" || artifactValue["type"] != analysis.ArtifactTypeJava {
		t.Fatalf("artifact value = %#v, want path/type metadata", artifactValue)
	}
	if artifactContributions[0].Locator.StartLine != 1 || artifactContributions[0].Locator.EndLine != 1 {
		t.Fatalf("artifact locator = %#v, want line 1", artifactContributions[0].Locator)
	}

	endpoints := byType["java.endpoint"]
	if len(endpoints) != 3 {
		t.Fatalf("endpoint contributions = %d, want three", len(endpoints))
	}
	wantEndpoints := map[string]struct {
		method string
		start  int
		end    int
	}{
		`{"http_method":"DELETE","path":"/api/bookings"}`:      {method: "DELETE", start: 11, end: 29},
		`{"http_method":"GET","path":"/api/bookings/list"}`:    {method: "GET", start: 11, end: 18},
		`{"http_method":"POST","path":"/api/bookings/create"}`: {method: "POST", start: 11, end: 24},
	}
	for _, contribution := range endpoints {
		var value map[string]string
		if err := json.Unmarshal(contribution.Value, &value); err != nil {
			t.Fatalf("endpoint value is not JSON: %v", err)
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("encode endpoint value: %v", err)
		}
		want, ok := wantEndpoints[string(encoded)]
		if !ok {
			t.Fatalf("unexpected endpoint value = %s", encoded)
		}
		if value["http_method"] != want.method || contribution.Locator.StartLine != want.start || contribution.Locator.EndLine != want.end {
			t.Fatalf("endpoint %s locator = %#v, want %d-%d", encoded, contribution.Locator, want.start, want.end)
		}
	}
	for _, typ := range []string{"java.import", "java.configuration", "java.relation"} {
		if len(byType[typ]) == 0 {
			t.Fatalf("missing preserved %s observations", typ)
		}
	}
	for _, gap := range output.Gaps {
		if gap.Code == javaEndpointGapCode {
			t.Fatalf("complete fixture unexpectedly reported endpoint gap: %#v", gap)
		}
	}

	repeated, err := New().Analyze(context.Background(), input)
	if err != nil {
		t.Fatalf("repeated Analyze() error = %v", err)
	}
	if !reflect.DeepEqual(output, repeated) {
		t.Fatalf("repeated analysis changed output")
	}
}

func TestParseEndpointsRejectsFalsePositivesAndReportsConservativeCoverage(t *testing.T) {
	parsed := parse(`package example;
import jakarta.ws.rs.Path;
class Resource {
    String fake = "@Path(\"/string\") @GET";
    // @Path("/comment")
    /* @POST @Path("/block") */

    @Path(BASE + "/unsafe")
    @GET
    Response unsafe() { return null; }

    @jakarta.ws.rs.Path(value = "/safe//item")
    @GET
    Response safe() { return null; }

    @jakarta.ws.rs.Path("/qualified")
    @HEAD
    Response qualified() { return null; }

    @Path
    @POST
    Response missing() { return null; }
}`)

	values := make(map[string]string)
	for _, observation := range parsed.observations {
		if observation.Type != "java.endpoint" {
			continue
		}
		path, _ := observation.Value["path"].(string)
		method, _ := observation.Value["http_method"].(string)
		values[path] = method
		if strings.Contains(path, "string") || strings.Contains(path, "comment") || strings.Contains(path, "unsafe") {
			t.Fatalf("false positive endpoint = %#v", observation)
		}
	}
	if values["/safe/item"] != "GET" || values["/qualified"] != "HEAD" {
		t.Fatalf("endpoint values = %#v, want normalized safe and qualified paths", values)
	}
	if len(values) != 2 {
		t.Fatalf("endpoint values = %#v, want only two supported endpoints", values)
	}
	if len(parsed.gaps) != 1 || parsed.gaps[0].Code != javaEndpointGapCode || parsed.gaps[0].Message != javaEndpointGapMessage {
		t.Fatalf("endpoint gaps = %#v, want one fixed conservative gap", parsed.gaps)
	}
	foundIncomplete := false
	for _, coverage := range parsed.coverage {
		if coverage.State == contract.CoverageIncomplete && coverage.Message == javaEndpointCoverageMessage {
			foundIncomplete = true
		}
	}
	if !foundIncomplete {
		t.Fatalf("coverage = %#v, want endpoint incomplete coverage", parsed.coverage)
	}
}
