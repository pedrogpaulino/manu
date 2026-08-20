package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pedrogpaulino/manu/internal/config"
	"github.com/pedrogpaulino/manu/internal/ingestion"
)

func TestUnauthenticatedServerRejectsHostnameConfusionAndRemoteAliases(t *testing.T) {
	tests := []string{
		"localhost.evil.example:8080",
		"localhost.:8080",
		"127.0.0.1.evil.example:8080",
		"[::1%25lo]:8080",
		"[::ffff:192.0.2.1]:8080",
	}
	for _, address := range tests {
		t.Run(address, func(t *testing.T) {
			if err := ValidateListenAddress(address); err != ErrNonLoopbackListenAddress {
				t.Fatalf("ValidateListenAddress(%q) = %v, want ErrNonLoopbackListenAddress", address, err)
			}
		})
	}
}

func TestIngestionCannotSelectOrganizationThroughBundleManifest(t *testing.T) {
	configuration := config.Default()
	store := ingestion.NewMemoryStore()
	service := ingestion.NewHTTPService(store)
	handler, err := NewHandlerWithIngestion(configuration, service)
	if err != nil {
		t.Fatalf("NewHandlerWithIngestion() error = %v", err)
	}
	body, contentType := httpBundleBody(t, "other-organization")
	request := httptest.NewRequest(http.MethodPost, IngestionsPath, strings.NewReader(string(body)))
	request.Header.Set("Content-Type", contentType)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("cross-organization bundle status = %d, want 400: %s", response.Code, response.Body.String())
	}
	if len(store.Snapshot()) != 0 {
		t.Fatalf("cross-organization bundle was persisted: %#v", store.Snapshot())
	}
	if strings.Contains(response.Body.String(), "other-organization") {
		t.Fatalf("organization identity leaked in error response: %s", response.Body.String())
	}
}
