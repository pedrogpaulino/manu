package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pedrogpaulino/manu/internal/config"
	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/evidence"
	"github.com/pedrogpaulino/manu/internal/persistence"
)

type fakeEvidenceService struct {
	organization string
	inspection   EvidenceInspection
	err          error
}

func (s *fakeEvidenceService) GetEvidence(_ context.Context, organization, _ string) (EvidenceInspection, error) {
	s.organization = organization
	if s.err != nil {
		return EvidenceInspection{}, s.err
	}
	return s.inspection, nil
}

func validEvidenceInspection() EvidenceInspection {
	return EvidenceInspection{
		ID:                testEvidenceID,
		OrganizationID:    config.Default().Organization.ID,
		SourceID:          testSourceID,
		SnapshotID:        testSnapshotID,
		ArtifactID:        "00000000-0000-4000-8000-000000000105",
		Locator:           contract.Locator{Path: "src/service.go", StartLine: 12, EndLine: 15},
		ContentState:      evidence.ContentStatePresent,
		Classification:    evidence.ClassificationSafeText,
		Persist:           evidence.DecisionAllow,
		ExternalTransfer:  evidence.DecisionDeny,
		Content:           "func Handle() error { return nil }",
		ContentHash:       "94e0a6ccfe3849c160d8d51067a98507a6db684f8b6daa7cbee8a2ce75bdaa21",
		ContentBytes:      int64(len("func Handle() error { return nil }")),
		ContentCharacters: int64(len("func Handle() error { return nil }")),
	}
}

func TestEvidenceGetReturnsAuthorizedPersistedIdentityAndContent(t *testing.T) {
	service := &fakeEvidenceService{inspection: validEvidenceInspection()}
	configuration := config.Default()
	handler, err := NewHandlerWithQuery(configuration, nil, service)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, EvidencePath+"/"+testEvidenceID, nil)
	request.Header.Set("Organization-ID", "other")
	request.Header.Set(RequestIDHeader, "evidence-request")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("GET evidence status = %d, want 200: %s", response.Code, response.Body.String())
	}
	var decoded EvidenceResponse
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Version != Version || decoded.ID != testEvidenceID || decoded.OrganizationID != configuration.Organization.ID || decoded.Content == "" || decoded.RequestID != "evidence-request" {
		t.Fatalf("evidence response = %#v", decoded)
	}
	if service.organization != configuration.Organization.ID || decoded.ExternalTransfer != evidence.DecisionDeny {
		t.Fatalf("fixed scope/policy not preserved: response=%#v service=%q", decoded, service.organization)
	}
}

func TestEvidenceRejectsCrossOrganizationInspectionWithoutContent(t *testing.T) {
	inspection := validEvidenceInspection()
	inspection.OrganizationID = "00000000-0000-4000-8000-000000000199"
	service := &fakeEvidenceService{inspection: inspection}
	handler, err := NewHandlerWithQuery(config.Default(), nil, service)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, EvidencePath+"/"+testEvidenceID, nil))
	if response.Code != http.StatusInternalServerError || strings.Contains(response.Body.String(), inspection.Content) {
		t.Fatalf("cross-organization evidence response = %d/%s", response.Code, response.Body.String())
	}
}

func TestEvidenceNeverReturnsDeniedOrOmittedContent(t *testing.T) {
	for _, state := range []evidence.ContentState{evidence.ContentStateOmitted} {
		inspection := validEvidenceInspection()
		inspection.ContentState = state
		inspection.Content = "secret=must-not-leak"
		inspection.ContentBytes = 0
		inspection.ContentCharacters = 0
		inspection.ContentHash = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
		service := &fakeEvidenceService{inspection: inspection}
		handler, err := NewHandlerWithQuery(config.Default(), nil, service)
		if err != nil {
			t.Fatal(err)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, EvidencePath+"/"+testEvidenceID, nil))
		if response.Code != http.StatusInternalServerError {
			t.Fatalf("inconsistent omitted evidence status = %d, want 500: %s", response.Code, response.Body.String())
		}
		if strings.Contains(response.Body.String(), "must-not-leak") {
			t.Fatalf("omitted evidence leaked content: %s", response.Body.String())
		}
	}

	inspection := validEvidenceInspection()
	inspection.Persist = evidence.DecisionDeny
	inspection.ContentState = evidence.ContentStateOmitted
	inspection.Content = "secret=must-not-leak"
	inspection.ContentBytes = 0
	inspection.ContentCharacters = 0
	service := &fakeEvidenceService{inspection: inspection}
	handler, err := NewHandlerWithQuery(config.Default(), nil, service)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, EvidencePath+"/"+testEvidenceID, nil))
	if response.Code != http.StatusInternalServerError || strings.Contains(response.Body.String(), "must-not-leak") {
		t.Fatalf("denied evidence response = %d/%s", response.Code, response.Body.String())
	}

	validOmitted := validEvidenceInspection()
	validOmitted.ContentState = evidence.ContentStateOmitted
	validOmitted.Content = ""
	validOmitted.ContentBytes = 0
	validOmitted.ContentCharacters = 0
	validOmitted.ContentHash = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	service = &fakeEvidenceService{inspection: validOmitted}
	handler, err = NewHandlerWithQuery(config.Default(), nil, service)
	if err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, EvidencePath+"/"+testEvidenceID, nil))
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "must-not-leak") {
		t.Fatalf("valid omitted evidence response = %d/%s", response.Code, response.Body.String())
	}

	validDenied := validOmitted
	validDenied.Persist = evidence.DecisionDeny
	service = &fakeEvidenceService{inspection: validDenied}
	handler, err = NewHandlerWithQuery(config.Default(), nil, service)
	if err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, EvidencePath+"/"+testEvidenceID, nil))
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "must-not-leak") {
		t.Fatalf("valid denied evidence response = %d/%s", response.Code, response.Body.String())
	}

	for _, classification := range []evidence.Classification{
		evidence.ClassificationBinary,
		evidence.ClassificationInvalid,
		evidence.ClassificationProhibited,
	} {
		inspection = validEvidenceInspection()
		inspection.Classification = classification
		inspection.ContentState = evidence.ContentStateOmitted
		inspection.Content = "secret=must-not-leak"
		inspection.ContentBytes = int64(len(inspection.Content))
		inspection.ContentCharacters = int64(len(inspection.Content))
		service = &fakeEvidenceService{inspection: inspection}
		handler, err = NewHandlerWithQuery(config.Default(), nil, service)
		if err != nil {
			t.Fatal(err)
		}
		response = httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, EvidencePath+"/"+testEvidenceID, nil))
		if response.Code != http.StatusInternalServerError || strings.Contains(response.Body.String(), "must-not-leak") {
			t.Fatalf("unsafe %s evidence response = %d/%s", classification, response.Code, response.Body.String())
		}
	}
}

func TestEvidenceMethodPathAndServiceErrors(t *testing.T) {
	service := &fakeEvidenceService{err: persistence.ErrNotFound}
	handler, err := NewHandlerWithQuery(config.Default(), nil, service)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, EvidencePath+"/"+testEvidenceID, nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("not found status = %d, want 404", response.Code)
	}
	wrongMethod := httptest.NewRecorder()
	handler.ServeHTTP(wrongMethod, httptest.NewRequest(http.MethodPost, EvidencePath+"/"+testEvidenceID, nil))
	if wrongMethod.Code != http.StatusMethodNotAllowed || wrongMethod.Header().Get("Allow") != http.MethodGet {
		t.Fatalf("wrong method = %d/%q", wrongMethod.Code, wrongMethod.Header().Get("Allow"))
	}
	invalidPath := httptest.NewRecorder()
	handler.ServeHTTP(invalidPath, httptest.NewRequest(http.MethodGet, EvidencePath+"/../secret", nil))
	if invalidPath.Code != http.StatusBadRequest {
		t.Fatalf("invalid evidence path status = %d, want 400", invalidPath.Code)
	}

	service.err = errors.New("database password=secret")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, EvidencePath+"/"+testEvidenceID, nil))
	if response.Code != http.StatusInternalServerError || strings.Contains(response.Body.String(), "password") || strings.Contains(response.Body.String(), "secret") {
		t.Fatalf("unsafe evidence error response = %d/%s", response.Code, response.Body.String())
	}
}

func TestEvidenceUnavailableDefaultIsExplicit(t *testing.T) {
	handler, err := NewHandlerWithQuery(config.Default(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, EvidencePath+"/"+testEvidenceID, nil))
	if response.Code != http.StatusServiceUnavailable || response.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("unavailable evidence = %d/%q", response.Code, response.Header().Get("Content-Type"))
	}
}
