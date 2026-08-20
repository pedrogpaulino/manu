package docs

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"testing"

	_ "embed"

	"github.com/pedrogpaulino/manu/internal/api"
)

// openAPISpec is intentionally a small structural view of the document. It
// lets this test validate the implemented contract without adding a YAML or
// OpenAPI dependency to the runtime module.
type openAPISpec struct {
	OpenAPI    string                                `json:"openapi"`
	Info       map[string]any                        `json:"info"`
	Paths      map[string]map[string]json.RawMessage `json:"paths"`
	Components openAPIComponents                     `json:"components"`
}

type openAPIComponents struct {
	Headers    map[string]json.RawMessage `json:"headers"`
	Responses  map[string]json.RawMessage `json:"responses"`
	Schemas    map[string]json.RawMessage `json:"schemas"`
	Parameters map[string]json.RawMessage `json:"parameters"`
}

//go:embed openapi.json
var openAPIDocument []byte

func TestOpenAPIDocumentIsValidJSONAndMatchesHTTPSurface(t *testing.T) {
	var raw map[string]any
	decodeJSON(t, openAPIDocument, &raw)
	if _, ok := raw["security"]; ok {
		t.Fatal("document must not advertise authentication for the no-auth local mode")
	}

	var spec openAPISpec
	decodeJSON(t, openAPIDocument, &spec)
	if spec.OpenAPI != "3.1.0" {
		t.Fatalf("openapi version = %q, want 3.1.0", spec.OpenAPI)
	}
	if spec.Info["version"] != api.APIVersion {
		t.Fatalf("info.version = %v, want %q", spec.Info["version"], api.APIVersion)
	}

	wantPaths := map[string]map[string]bool{
		api.HealthPath:                         {"get": true},
		api.ReadinessPath:                      {"get": true},
		api.IngestionsPath:                     {"post": true},
		api.IngestionsPath + "/{ingestion_id}": {"get": true},
		api.QueriesPath:                        {"post": true},
		api.QueriesPath + "/{query_id}":        {"get": true},
		api.EvidencePath + "/{evidence_id}":    {"get": true},
	}
	if len(spec.Paths) != len(wantPaths) {
		t.Fatalf("documented path count = %d, want %d", len(spec.Paths), len(wantPaths))
	}
	for path, wantMethods := range wantPaths {
		item, ok := spec.Paths[path]
		if !ok {
			t.Fatalf("missing documented path %q", path)
		}
		for method := range item {
			if method != "parameters" && !wantMethods[method] {
				t.Errorf("path %s documents unsupported method %s", path, method)
			}
		}
		for method := range wantMethods {
			operation, ok := item[method]
			if !ok {
				t.Fatalf("path %s is missing method %s", path, method)
			}
			var value struct {
				Responses map[string]json.RawMessage `json:"responses"`
			}
			decodeJSON(t, operation, &value)
			if len(value.Responses) == 0 {
				t.Fatalf("%s %s has no responses", method, path)
			}
			for status, response := range value.Responses {
				if _, err := resolveResponse(spec.Components.Responses, response); err != nil {
					t.Fatalf("%s %s response %s: %v", method, path, status, err)
				}
			}
		}
	}

	assertStatusSet(t, spec, api.HealthPath, "get", "200")
	assertStatusSet(t, spec, api.ReadinessPath, "get", "200", "503")
	assertStatusSet(t, spec, api.IngestionsPath, "post", "202", "400", "405", "408", "409", "413", "415", "500", "503")
	assertStatusSet(t, spec, api.IngestionsPath+"/{ingestion_id}", "get", "200", "400", "404", "405", "408", "500", "503")
	assertStatusSet(t, spec, api.QueriesPath, "post", "200", "400", "405", "408", "409", "413", "415", "500", "503")
	assertStatusSet(t, spec, api.QueriesPath+"/{query_id}", "get", "200", "400", "404", "405", "408", "500", "503")
	assertStatusSet(t, spec, api.EvidencePath+"/{evidence_id}", "get", "200", "400", "404", "405", "408", "500", "503")
}

func TestOpenAPIUsesImplementedMediaTypesLimitsAndStates(t *testing.T) {
	var spec openAPISpec
	decodeJSON(t, openAPIDocument, &spec)

	postIngestion := operation(t, spec, api.IngestionsPath, "post")
	requestBody := objectField(t, postIngestion, "requestBody")
	content := objectField(t, requestBody, "content")
	if _, ok := content["multipart/form-data"]; !ok {
		t.Fatal("ingestion request must use multipart/form-data")
	}

	postQuery := operation(t, spec, api.QueriesPath, "post")
	queryBody := objectField(t, postQuery, "requestBody")
	queryContent := objectField(t, queryBody, "content")
	if _, ok := queryContent["application/json"]; !ok {
		t.Fatal("query request must use application/json")
	}

	querySchema := schema(t, spec, "QueryRequest")
	question := property(t, querySchema, "question")
	if got := numberField(t, question, "maxLength"); got != 16384 {
		t.Fatalf("query question maxLength = %v, want 16384", got)
	}
	if _, ok := propertyMap(t, querySchema)["organization_id"]; ok {
		t.Fatal("query request must not accept organization_id")
	}
	if kind := property(t, querySchema, "kind"); stringField(t, kind, "type") != "string" {
		t.Fatalf("query kind type = %q, want string", stringField(t, kind, "type"))
	}
	queryRequired := stringSet(t, querySchema, "required")
	if !queryRequired["kind"] {
		t.Fatal("query request kind must be required")
	}
	claimSchema := schema(t, spec, "Claim")
	claimRequired := stringSet(t, claimSchema, "required")
	if !claimRequired["id"] {
		t.Fatal("query claims must expose a stable id")
	}
	claimID := property(t, claimSchema, "id")
	if ref := stringField(t, claimID, "$ref"); ref != "#/components/schemas/UUID" {
		t.Fatalf("claim id ref = %q, want UUID", ref)
	}

	bundleSchema := schema(t, spec, "AnalysisBundleMultipart")
	bundleProperties := propertyMap(t, bundleSchema)
	for part, mediaType := range map[string]string{
		"manifest.json":        "application/json",
		"artifacts.ndjson":     "application/x-ndjson",
		"contributions.ndjson": "application/x-ndjson",
		"evidence.ndjson":      "application/x-ndjson",
	} {
		partSchema, ok := bundleProperties[part]
		if !ok {
			t.Fatalf("missing multipart part %q", part)
		}
		partObject, ok := partSchema.(map[string]any)
		if !ok {
			t.Fatalf("part %s has invalid schema shape", part)
		}
		if got := stringField(t, partObject, "contentMediaType"); got != mediaType {
			t.Fatalf("part %s contentMediaType = %q, want %q", part, got, mediaType)
		}
	}

	queryStates := enumValues(t, schema(t, spec, "QueryResponse"), "state")
	for _, state := range []string{"pending", "running", "completed", "partial", "failed", "abstained"} {
		if !queryStates[state] {
			t.Errorf("query state %q is not documented", state)
		}
	}
	ingestionStates := enumValues(t, schema(t, spec, "IngestionResponse"), "state")
	if ingestionStates["abstained"] {
		t.Error("ingestion state must not document query-only abstained")
	}
	for _, state := range []string{"pending", "running", "completed", "partial", "failed"} {
		if !ingestionStates[state] {
			t.Errorf("ingestion state %q is not documented", state)
		}
	}

	var runtime map[string]any
	decodeJSON(t, openAPIDocument, &runtime)
	defaults := runtime["x-manu-runtime"].(map[string]any)["default_limits"].(map[string]any)
	for name, want := range map[string]float64{
		"server_max_body_bytes":     67108864,
		"bundle_max_bytes":          67108864,
		"manifest_max_bytes":        1048576,
		"evidence_units_max":        10000,
		"evidence_stream_max_bytes": 262144,
		"evidence_text_max_bytes":   65536,
		"query_max_bytes":           16384,
	} {
		if got, ok := defaults[name].(float64); !ok || got != want {
			t.Errorf("default limit %s = %v, want %v", name, defaults[name], want)
		}
	}
}

func TestOpenAPIExamplesAreSanitizedAndErrorsAreProblemJSON(t *testing.T) {
	lower := bytes.ToLower(openAPIDocument)
	for _, forbidden := range []string{"sk-", "api_key", "authorization", "\"password\"", "bearer "} {
		if bytes.Contains(lower, []byte(forbidden)) {
			t.Fatalf("OpenAPI document contains forbidden secret-like marker %q", forbidden)
		}
	}

	var spec openAPISpec
	decodeJSON(t, openAPIDocument, &spec)
	if _, ok := spec.Components.Headers["RequestID"]; !ok {
		t.Fatal("request ID header component is missing")
	}
	if len(spec.Components.Responses) == 0 {
		t.Fatal("problem response components are missing")
	}
	for name, raw := range spec.Components.Responses {
		response, err := resolveResponse(spec.Components.Responses, raw)
		if err != nil {
			t.Fatalf("response component %s: %v", name, err)
		}
		if _, ok := response["content"]; !ok {
			t.Errorf("response component %s has no content", name)
		}
		content, ok := response["content"].(map[string]any)
		if !ok {
			t.Errorf("response component %s content has invalid shape", name)
			continue
		}
		if _, ok := content["application/problem+json"]; !ok {
			t.Errorf("response component %s must be problem+json", name)
		}
		headers, ok := response["headers"].(map[string]any)
		if !ok {
			t.Errorf("response component %s has no headers", name)
			continue
		}
		if _, ok := headers[api.RequestIDHeader]; !ok {
			t.Errorf("response component %s does not expose %s", name, api.RequestIDHeader)
		}
	}
}

func decodeJSON(t *testing.T, data []byte, destination any) {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(destination); err != nil {
		t.Fatalf("decode OpenAPI document: %v", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		t.Fatal("OpenAPI document contains trailing JSON")
	}
}

func operation(t *testing.T, spec openAPISpec, path, method string) map[string]any {
	t.Helper()
	item, ok := spec.Paths[path]
	if !ok {
		t.Fatalf("missing path %s", path)
	}
	raw, ok := item[method]
	if !ok {
		t.Fatalf("missing method %s %s", method, path)
	}
	var value map[string]any
	decodeJSON(t, raw, &value)
	return value
}

func assertStatusSet(t *testing.T, spec openAPISpec, path, method string, want ...string) {
	t.Helper()
	value := operation(t, spec, path, method)
	responses, ok := value["responses"].(map[string]any)
	if !ok {
		t.Fatalf("%s %s responses has invalid shape", method, path)
	}
	wantSet := make(map[string]bool, len(want))
	for _, status := range want {
		wantSet[status] = true
	}
	if len(responses) != len(wantSet) {
		t.Fatalf("%s %s statuses = %v, want %v", method, path, mapKeys(responses), want)
	}
	for status := range responses {
		if !wantSet[status] {
			t.Errorf("%s %s has undocumented status %s", method, path, status)
		}
	}
}

func resolveResponse(components map[string]json.RawMessage, raw json.RawMessage) (map[string]any, error) {
	for depth := 0; depth < 8; depth++ {
		var value map[string]any
		if err := json.Unmarshal(raw, &value); err != nil {
			return nil, err
		}
		ref, ok := value["$ref"].(string)
		if !ok {
			return value, nil
		}
		const prefix = "#/components/responses/"
		if !strings.HasPrefix(ref, prefix) {
			return nil, fmt.Errorf("unsupported response reference %q", ref)
		}
		next, ok := components[strings.TrimPrefix(ref, prefix)]
		if !ok {
			return nil, fmt.Errorf("unresolved response reference %q", ref)
		}
		raw = next
	}
	return nil, fmt.Errorf("response reference chain is too deep")
}

func schema(t *testing.T, spec openAPISpec, name string) map[string]any {
	t.Helper()
	raw, ok := spec.Components.Schemas[name]
	if !ok {
		t.Fatalf("missing schema %s", name)
	}
	var value map[string]any
	decodeJSON(t, raw, &value)
	return value
}

func objectField(t *testing.T, object map[string]any, field string) map[string]any {
	t.Helper()
	value, ok := object[field].(map[string]any)
	if !ok {
		t.Fatalf("field %s has invalid object shape", field)
	}
	return value
}

func property(t *testing.T, schema map[string]any, name string) map[string]any {
	t.Helper()
	value, ok := propertyMap(t, schema)[name]
	if !ok {
		t.Fatalf("schema is missing property %s", name)
	}
	object, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("property %s has invalid object shape", name)
	}
	return object
}

func propertyMap(t *testing.T, schema map[string]any) map[string]any {
	t.Helper()
	return objectField(t, schema, "properties")
}

func numberField(t *testing.T, object map[string]any, field string) float64 {
	t.Helper()
	value, ok := object[field].(float64)
	if !ok {
		t.Fatalf("field %s is not a number", field)
	}
	return value
}

func stringField(t *testing.T, object map[string]any, field string) string {
	t.Helper()
	value, ok := object[field].(string)
	if !ok {
		t.Fatalf("field %s is not a string", field)
	}
	return value
}

func stringSet(t *testing.T, object map[string]any, field string) map[string]bool {
	t.Helper()
	values, ok := object[field].([]any)
	if !ok {
		t.Fatalf("field %s is not an array", field)
	}
	result := make(map[string]bool, len(values))
	for _, value := range values {
		item, ok := value.(string)
		if !ok {
			t.Fatalf("field %s contains a non-string value", field)
		}
		result[item] = true
	}
	return result
}

func enumValues(t *testing.T, schema map[string]any, propertyName string) map[string]bool {
	t.Helper()
	values := property(t, schema, propertyName)["enum"].([]any)
	result := make(map[string]bool, len(values))
	for _, value := range values {
		result[value.(string)] = true
	}
	return result
}

func mapKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
