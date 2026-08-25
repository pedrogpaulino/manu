package query

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pedrogpaulino/manu/internal/aigateway"
	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/evidence"
	"github.com/pedrogpaulino/manu/internal/fact"
)

func TestProjectContextPackageProducesMatchingValidatedViews(t *testing.T) {
	packageContext := contextGatewayTestPackage(t, "present gateway content")
	projection, err := ProjectContextPackage(context.Background(), packageContext)
	if err != nil {
		t.Fatalf("ProjectContextPackage() error = %v", err)
	}
	if err := projection.Validate(); err != nil {
		t.Fatalf("projection.Validate() error = %v", err)
	}
	if err := projection.ValidateAgainst(packageContext); err != nil {
		t.Fatalf("projection.ValidateAgainst() error = %v", err)
	}
	if err := projection.ValidateAgainstGateway(); err != nil {
		t.Fatalf("projection.ValidateAgainstGateway() error = %v", err)
	}
	if err := projection.ValidationPackage.ValidateAgainstGateway(projection.GatewayPackage); err != nil {
		t.Fatalf("legacy ValidateAgainstGateway() error = %v", err)
	}
	if projection.ContextPackageID != packageContext.ID || projection.ContextPackageDigest != packageContext.Digest {
		t.Fatalf("context identity = %q/%q, want %q/%q", projection.ContextPackageID, projection.ContextPackageDigest, packageContext.ID, packageContext.Digest)
	}
	if projection.ItemCount != len(packageContext.Items) || projection.RelationCount != len(packageContext.Relations) || projection.EvidenceCount != len(projection.GatewayPackage.Evidence) || projection.GapCount != len(packageContext.Gaps) {
		t.Fatalf("projection counters = %#v", projection)
	}
	if !reflect.DeepEqual(projection.GapIDs, []string{"gap-gateway"}) || !reflect.DeepEqual(projection.GatewayPackage.Gaps, projection.GapIDs) {
		t.Fatalf("gap projection = %#v/%#v", projection.GapIDs, projection.GatewayPackage.Gaps)
	}

	projectedCount := len(packageContext.Items) + len(packageContext.Relations)
	if len(projection.ValidationPackage.Evidence) != projectedCount || len(projection.GatewayPackage.Evidence) != projectedCount {
		t.Fatalf("view evidence counts = %d/%d, want %d", len(projection.ValidationPackage.Evidence), len(projection.GatewayPackage.Evidence), projectedCount)
	}
	var characters, bytes int64
	for index, item := range packageContext.Items {
		validation := projection.ValidationPackage.Evidence[index]
		gateway := projection.GatewayPackage.Evidence[index]
		if validation.ID != gateway.ID || validation.ID != item.ID || validation.Locator == (contract.Locator{}) {
			t.Fatalf("identity/locator at %d = %#v/%#v", index, validation, gateway)
		}
		locatorBytes, err := json.Marshal(item.Locator)
		if err != nil {
			t.Fatalf("json.Marshal(locator %d) error = %v", index, err)
		}
		if gateway.Locator != string(locatorBytes) || validation.Locator != item.Locator {
			t.Fatalf("locator at %d = %q/%#v, want %q/%#v", index, gateway.Locator, validation.Locator, locatorBytes, item.Locator)
		}

		wantContent := ""
		switch item.Kind {
		case ContextItemEvidence:
			wantContent = item.Evidence.Content
		case ContextItemFact, ContextItemEntity:
			encoded, err := CanonicalContextItemJSON(item)
			if err != nil {
				t.Fatalf("CanonicalContextItemJSON(%d) error = %v", index, err)
			}
			wantContent = string(encoded)
		default:
			t.Fatalf("unexpected item kind %q", item.Kind)
		}
		if gateway.Content != wantContent || gateway.ContentDigest != evidence.ContentDigest(wantContent) || validation.ContentDigest != gateway.ContentDigest {
			t.Fatalf("content/digest at %d = %q/%q/%q, want %q/%q", index, gateway.Content, gateway.ContentDigest, validation.ContentDigest, wantContent, evidence.ContentDigest(wantContent))
		}
		characters += int64(len([]rune(wantContent)))
		bytes += int64(len([]byte(wantContent)))
	}
	for index, relation := range packageContext.Relations {
		projectedIndex := len(packageContext.Items) + index
		validation := projection.ValidationPackage.Evidence[projectedIndex]
		gateway := projection.GatewayPackage.Evidence[projectedIndex]
		if relation.ID != gateway.ID || validation.ID != relation.ID || validation.Locator == (contract.Locator{}) {
			t.Fatalf("relation identity/locator at %d = %#v/%#v", index, validation, gateway)
		}
		encoded, err := CanonicalContextRelationJSON(relation)
		if err != nil {
			t.Fatalf("CanonicalContextRelationJSON() error = %v", err)
		}
		if got := gateway.Content; got != string(encoded) {
			t.Fatalf("relation content = %q, want canonical JSON %q", got, encoded)
		}
		if gateway.ContentDigest != evidence.ContentDigest(gateway.Content) || validation.ContentDigest != gateway.ContentDigest {
			t.Fatalf("relation digest at %d = %q/%q", index, gateway.ContentDigest, validation.ContentDigest)
		}
		characters += int64(len([]rune(gateway.Content)))
		bytes += int64(len([]byte(gateway.Content)))
	}
	if projection.CharacterCount != characters || projection.ByteCount != bytes {
		t.Fatalf("projection counts = %d/%d, want %d/%d", projection.CharacterCount, projection.ByteCount, characters, bytes)
	}

	var present, redacted bool
	for _, item := range projection.GatewayPackage.Evidence {
		if item.Content == "present gateway content" {
			present = true
		}
		if item.Content == evidence.RedactedContent {
			redacted = true
		}
	}
	if !present || !redacted {
		t.Fatalf("gateway did not preserve present/redacted evidence: %#v", projection.GatewayPackage.Evidence)
	}
}

func TestProjectContextPackageGatewayContractReachesGeneratorWithoutSourceAccess(t *testing.T) {
	packageContext := contextGatewayTestPackage(t, "generator contract content")
	projection, err := ProjectContextPackage(context.Background(), packageContext)
	if err != nil {
		t.Fatalf("ProjectContextPackage() error = %v", err)
	}
	request := aigateway.GenerationRequest{
		ExecutionID: "context-gateway-execution",
		RequestID:   "context-gateway-request",
		Deadline:    time.Now().Add(time.Minute),
		Profile: aigateway.GenerationProfile{
			Provider:       aigateway.ProviderSimulated,
			Model:          "context-gateway-generator",
			Version:        aigateway.GenerationProfileVersion,
			Protocol:       aigateway.ProtocolResponses,
			MaxOutputBytes: 1024,
		},
		Question: "where should this behavior be inspected?",
		Package:  projection.GatewayPackage,
	}
	if err := request.Validate(); err != nil {
		t.Fatalf("GenerationRequest.Validate() error = %v", err)
	}
	stub := &contextGatewayGeneratorStub{}
	if _, err := stub.Generate(context.Background(), request); err != nil {
		t.Fatalf("generator stub error = %v", err)
	}
	if !reflect.DeepEqual(stub.Request.Package, projection.GatewayPackage) {
		t.Fatalf("generator received package %#v, want %#v", stub.Request.Package, projection.GatewayPackage)
	}
	if stub.Calls != 1 {
		t.Fatalf("generator calls = %d, want 1", stub.Calls)
	}
	encoded, err := json.Marshal(stub.Request)
	if err != nil {
		t.Fatalf("json.Marshal(GenerationRequest) error = %v", err)
	}
	if strings.Contains(string(encoded), "source/store") || strings.Contains(string(encoded), "postgres") {
		t.Fatalf("gateway request contains source/store detail: %s", encoded)
	}
}

func TestProjectContextPackageIsDeterministicAndInputIndependent(t *testing.T) {
	packageContext := contextGatewayTestPackage(t, "repeatable gateway content")
	before := packageContext.Clone()
	first, err := ProjectContextPackage(context.Background(), packageContext)
	if err != nil {
		t.Fatalf("first ProjectContextPackage() error = %v", err)
	}
	second, err := ProjectContextPackage(context.Background(), packageContext)
	if err != nil {
		t.Fatalf("second ProjectContextPackage() error = %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("projection is not deterministic:\nfirst=%#v\nsecond=%#v", first, second)
	}
	if !reflect.DeepEqual(packageContext, before) {
		t.Fatal("ProjectContextPackage mutated the input package")
	}

	changedDigest := packageContext.Clone()
	changedDigest.Digest = strings.Repeat("b", 64)
	changedProjection, err := ProjectContextPackage(context.Background(), changedDigest)
	if err != nil {
		t.Fatalf("changed digest projection error = %v", err)
	}
	if changedProjection.GatewayPackage.Digest == first.GatewayPackage.Digest {
		t.Fatal("changing context package digest did not change projected digest")
	}

	changedContent, err := ProjectContextPackage(context.Background(), contextGatewayTestPackage(t, "different gateway content"))
	if err != nil {
		t.Fatalf("changed content projection error = %v", err)
	}
	if changedContent.GatewayPackage.Digest == first.GatewayPackage.Digest {
		t.Fatal("changing evidence content did not change projected digest")
	}

	changedRelation := packageContext.Clone()
	changedRelation.Relations[0].Predicate = fact.PredicateCall
	changedRelationProjection, err := ProjectContextPackage(context.Background(), changedRelation)
	if err != nil {
		t.Fatalf("changed relation projection error = %v", err)
	}
	if changedRelationProjection.GatewayPackage.Digest == first.GatewayPackage.Digest {
		t.Fatal("changing relation did not change projected digest")
	}
}

func TestProjectContextPackageFailsClosedWithoutEchoingUnsafeContent(t *testing.T) {
	tests := []struct {
		name      string
		forbidden string
		mutate    func(*ContextPackage)
	}{
		{
			name:      "evidence transfer denied",
			forbidden: "gateway-denied-secret",
			mutate: func(value *ContextPackage) {
				value.Items[2].Evidence.ExternalTransfer = evidence.DecisionDeny
			},
		},
		{
			name:      "evidence content omitted",
			forbidden: "present gateway content",
			mutate: func(value *ContextPackage) {
				unit := value.Items[2].Evidence
				unit.ContentState = evidence.ContentStateOmitted
				unit.Content = ""
				unit.ContentBytes = 0
				unit.ContentCharacters = 0
			},
		},
		{
			name:      "evidence secret",
			forbidden: "password = gateway-secret",
			mutate: func(value *ContextPackage) {
				contextGatewayReplaceEvidenceContent(value, 2, "password = gateway-secret")
			},
		},
		{
			name:      "fact secret",
			forbidden: "token = fact-gateway-secret",
			mutate: func(value *ContextPackage) {
				value.Relations = nil
				item := &value.Items[0]
				oldID := item.ID
				item.Fact.Value.String = "token = fact-gateway-secret"
				item.Fact.ID = ""
				id, _ := fact.FactID(*item.Fact)
				item.Fact.ID = id
				item.ID = id
				for index := range value.Audit {
					if value.Audit[index].ItemID == oldID {
						value.Audit[index].ItemID = id
					}
				}
			},
		},
		{
			name:      "entity prompt injection",
			forbidden: "jailbreak",
			mutate: func(value *ContextPackage) {
				value.Items[1].Provenance.Producer = &fact.Producer{ID: "frontend-gateway", Version: "v1", Method: "jailbreak"}
			},
		},
		{
			name:      "relation prompt injection",
			forbidden: "jailbreak",
			mutate: func(value *ContextPackage) {
				value.Relations[0].Provenance.Producer = &fact.Producer{ID: "frontend-gateway", Version: "v1", Method: "jailbreak"}
			},
		},
		{
			name:      "locator too large",
			forbidden: "locator-large-secret",
			mutate: func(value *ContextPackage) {
				value.Items[2].Locator.Path = strings.Repeat("locator-large-secret", int(maxContextLocatorBytes))
			},
		},
		{
			name:      "relation has no support locator",
			forbidden: "missing relation locator",
			mutate: func(value *ContextPackage) {
				value.Relations[0].SupportIDs = nil
			},
		},
		{
			name:      "empty gap",
			forbidden: "empty-gap-secret",
			mutate: func(value *ContextPackage) {
				value.Gaps[0].ID = ""
			},
		},
		{
			name:      "duplicate gap",
			forbidden: "duplicate-gap-secret",
			mutate: func(value *ContextPackage) {
				value.Gaps = append(value.Gaps, value.Gaps[0])
			},
		},
		{
			name:      "item collision",
			forbidden: "collision-secret",
			mutate: func(value *ContextPackage) {
				collision := cloneContextItem(value.Items[0])
				value.Items = append(value.Items, collision)
			},
		},
		{
			name:      "item limit",
			forbidden: "limit-secret",
			mutate: func(value *ContextPackage) {
				value.Limits.MaxItems = 1
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			packageContext := contextGatewayTestPackage(t, "present gateway content")
			tt.mutate(&packageContext)
			if tt.name == "fact secret" {
				if err := packageContext.Validate(); err != nil {
					t.Fatalf("fact-secret fixture became structurally invalid before projection: %v", err)
				}
			}
			projection, err := ProjectContextPackage(context.Background(), packageContext)
			if err == nil {
				t.Fatalf("ProjectContextPackage() accepted unsafe input: %#v", projection)
			}
			if !errors.Is(err, ErrInvalidContextGatewayProjection) && !errors.Is(err, context.Canceled) {
				t.Fatalf("ProjectContextPackage() error = %v, want controlled projection error", err)
			}
			if strings.Contains(err.Error(), tt.forbidden) {
				t.Fatalf("error echoed forbidden content %q: %v", tt.forbidden, err)
			}
			encoded, marshalErr := json.Marshal(map[string]string{"error": err.Error()})
			if marshalErr != nil {
				t.Fatalf("json.Marshal(error) error = %v", marshalErr)
			}
			if strings.Contains(string(encoded), tt.forbidden) {
				t.Fatalf("serialized error echoed forbidden content %q: %s", tt.forbidden, encoded)
			}
			resultEncoded, marshalResultErr := json.Marshal(projection)
			if marshalResultErr != nil {
				t.Fatalf("json.Marshal(result) error = %v", marshalResultErr)
			}
			if strings.Contains(string(resultEncoded), tt.forbidden) {
				t.Fatalf("serialized result echoed forbidden content %q: %s", tt.forbidden, resultEncoded)
			}
		})
	}
}

func TestProjectContextPackageRelationLocatorUsesProjectedSupportNotNestedProvenance(t *testing.T) {
	packageContext := contextGatewayTestPackage(t, "relation locator support")
	otherLocator := contract.Locator{SourceID: packageContext.Scope.SourceID, ArtifactID: "artifact-nested", Path: "nested/hidden.go", StartLine: 2, EndLine: 2}
	packageContext.Relations[0].Provenance.Evidence = []fact.EvidenceRef{{
		ID: "provenance-only", Locator: otherLocator,
	}}
	projection, err := ProjectContextPackage(context.Background(), packageContext)
	if err != nil {
		t.Fatalf("ProjectContextPackage() error = %v", err)
	}
	wantLocatorBytes, err := json.Marshal(packageContext.Items[2].Locator)
	if err != nil {
		t.Fatalf("json.Marshal(support locator) error = %v", err)
	}
	relationEvidence := projection.GatewayPackage.Evidence[len(packageContext.Items)]
	if relationEvidence.Locator != string(wantLocatorBytes) {
		t.Fatalf("relation locator = %q, want authorized support locator %q", relationEvidence.Locator, wantLocatorBytes)
	}
	otherEncodedBytes, err := json.Marshal(otherLocator)
	if err != nil {
		t.Fatalf("json.Marshal(other locator) error = %v", err)
	}
	if relationEvidence.Locator == string(otherEncodedBytes) {
		t.Fatalf("relation used nested provenance locator %q", otherEncodedBytes)
	}
}

func TestContextGatewayProjectionValidateAgainstRejectsAdulterationAndHonorsContext(t *testing.T) {
	packageContext := contextGatewayTestPackage(t, "validation gateway content")
	projection, err := ProjectContextPackage(context.Background(), packageContext)
	if err != nil {
		t.Fatalf("ProjectContextPackage() error = %v", err)
	}
	if err := projection.Validate(); err != nil {
		t.Fatalf("projection.Validate() error = %v", err)
	}

	for _, tt := range []struct {
		name   string
		mutate func(*ContextGatewayProjection)
	}{
		{name: "context package id", mutate: func(value *ContextGatewayProjection) { value.ContextPackageID = "other-package" }},
		{name: "context package digest", mutate: func(value *ContextGatewayProjection) { value.ContextPackageDigest = strings.Repeat("b", 64) }},
		{name: "validation digest", mutate: func(value *ContextGatewayProjection) {
			value.ValidationPackage.Evidence[0].ContentDigest = strings.Repeat("0", 64)
		}},
		{name: "gateway content", mutate: func(value *ContextGatewayProjection) { value.GatewayPackage.Evidence[0].Content += " altered" }},
		{name: "gap ids", mutate: func(value *ContextGatewayProjection) { value.GapIDs = []string{"other-gap"} }},
		{name: "byte count", mutate: func(value *ContextGatewayProjection) { value.ByteCount++ }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			mutated := contextGatewayCloneProjection(projection)
			tt.mutate(&mutated)
			if err := mutated.ValidateAgainst(packageContext); err == nil {
				t.Fatalf("ValidateAgainst() accepted %s adulteration", tt.name)
			}
		})
	}

	t.Run("Validate rejects coordinated content tampering", func(t *testing.T) {
		mutated := contextGatewayCloneProjection(projection)
		gateway := &mutated.GatewayPackage.Evidence[0]
		oldContent := gateway.Content
		gateway.Content = oldContent + " altered"
		gateway.ContentDigest = evidence.ContentDigest(gateway.Content)
		mutated.ValidationPackage.Evidence[0].ContentDigest = gateway.ContentDigest
		mutated.CharacterCount += int64(len([]rune(gateway.Content)) - len([]rune(oldContent)))
		mutated.ByteCount += int64(len([]byte(gateway.Content)) - len([]byte(oldContent)))
		if err := mutated.Validate(); err == nil {
			t.Fatal("Validate() accepted content tampering after both view digests and counters were recomputed")
		}
	})

	t.Run("Validate rejects coordinated scope tampering", func(t *testing.T) {
		mutated := contextGatewayCloneProjection(projection)
		tamperedScope := Scope{
			OrganizationID: "00000000-0000-0000-0000-000000000010",
			SourceID:       "00000000-0000-0000-0000-000000000011",
			SnapshotID:     "00000000-0000-0000-0000-000000000012",
		}
		mutated.ValidationPackage.OrganizationID = tamperedScope.OrganizationID
		mutated.ValidationPackage.SourceID = tamperedScope.SourceID
		mutated.ValidationPackage.SnapshotID = tamperedScope.SnapshotID
		for index := range mutated.ValidationPackage.Evidence {
			mutated.ValidationPackage.Evidence[index].OrganizationID = tamperedScope.OrganizationID
			mutated.ValidationPackage.Evidence[index].SourceID = tamperedScope.SourceID
			mutated.ValidationPackage.Evidence[index].SnapshotID = tamperedScope.SnapshotID
		}
		if err := mutated.Validate(); err == nil {
			t.Fatal("Validate() accepted scope tampering while retaining the gateway package and stored digest")
		}
	})

	if _, err := ProjectContextPackage(nil, packageContext); !errors.Is(err, ErrInvalidContextGatewayProjection) {
		t.Fatalf("nil context error = %v, want projection error", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ProjectContextPackage(canceled, packageContext); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled context error = %v, want context canceled", err)
	}

	secretPackage := packageContext.Clone()
	secret := "password = gateway-invalid-secret"
	secretPackage.ID = secret
	_, err = ProjectContextPackage(context.Background(), secretPackage)
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("invalid secret package error = %v, secret echoed=%v", err, err != nil && strings.Contains(err.Error(), secret))
	}
}

type contextGatewayGeneratorStub struct {
	Request aigateway.GenerationRequest
	Calls   int
}

var _ aigateway.Generator = (*contextGatewayGeneratorStub)(nil)

func (g *contextGatewayGeneratorStub) Generate(_ context.Context, request aigateway.GenerationRequest) (aigateway.GenerationResult, error) {
	if err := request.Validate(); err != nil {
		return aigateway.GenerationResult{}, err
	}
	g.Request = request
	g.Calls++
	return aigateway.GenerationResult{}, nil
}

func contextGatewayTestPackage(t *testing.T, content string) ContextPackage {
	t.Helper()
	fixture := contextPolicyTestFixture(t, content)
	redactedFixture := contextPolicyTestFixture(t, content+" redacted")
	redactedUnit, err := evidence.PrepareForExternalTransfer(*redactedFixture.evidence.Evidence, evidence.Policy{
		Installation: evidence.PolicyLayer{Persist: evidence.DecisionAllow, ExternalTransfer: evidence.DecisionRedact},
	})
	if err != nil {
		t.Fatalf("PrepareForExternalTransfer() error = %v", err)
	}
	redactedItem := redactedFixture.evidence
	redactedItem.ID = redactedUnit.ID
	redactedItem.Evidence = &redactedUnit
	redactedItem.Provenance.Evidence[0].ID = redactedUnit.ID

	items := []ContextItem{fixture.fact, fixture.entity, fixture.evidence, redactedItem}
	// The gateway contract bounds its opaque locator string to 128 bytes. Keep
	// the context locator compact while the underlying evidence retains its
	// complete source-scoped locator.
	compactLocator := contract.Locator{URI: "context://gateway"}
	for index := range items {
		items[index].Locator = compactLocator
	}
	audit := make([]ContextSelectionAudit, 0, len(items))
	for index, item := range items {
		audit = append(audit, ContextSelectionAudit{ItemID: item.ID, Included: true, Reason: ContextSelectionIncluded, Rank: index + 1, Score: 1})
	}
	packageContext := ContextPackage{
		Version:   ContextVersion,
		ID:        "context-gateway-package",
		Digest:    strings.Repeat("a", 64),
		Revision:  "revision-gateway",
		Scope:     fixture.scope,
		Intent:    contextTestQuestionIntent(),
		Limits:    contextTestLimits(),
		Items:     items,
		Relations: []ContextRelation{fixture.relation},
		Gaps: []contract.Gap{{
			ID: "gap-gateway", Code: "runtime-unobserved", Message: "runtime evidence is unavailable",
		}},
		Audit:         audit,
		TokenEstimate: 32,
		Truncated:     false,
	}
	if err := packageContext.Validate(); err != nil {
		t.Fatalf("gateway package fixture invalid: %v", err)
	}
	return packageContext
}

func contextGatewayReplaceEvidenceContent(packageContext *ContextPackage, index int, content string) {
	item := &packageContext.Items[index]
	oldID := item.ID
	unit := item.Evidence
	unit.ContentState = evidence.ContentStatePresent
	unit.Content = content
	unit.ContentHash = evidence.ContentDigest(content)
	unit.ContentBytes = int64(len([]byte(content)))
	unit.ContentCharacters = int64(len([]rune(content)))
	unit.Classification = evidence.ClassificationSafeText
	unit.Findings = nil
	unit.ID = evidence.EvidenceID(*unit)
	item.ID = unit.ID
	item.Provenance.Evidence[0].ID = unit.ID
	for itemIndex := range packageContext.Items {
		for supportIndex, supportID := range packageContext.Items[itemIndex].SupportIDs {
			if supportID == oldID {
				packageContext.Items[itemIndex].SupportIDs[supportIndex] = unit.ID
			}
		}
	}
	for relationIndex := range packageContext.Relations {
		relation := &packageContext.Relations[relationIndex]
		for index, id := range relation.SupportIDs {
			if id == oldID {
				relation.SupportIDs[index] = unit.ID
			}
		}
	}
	for auditIndex := range packageContext.Audit {
		if packageContext.Audit[auditIndex].ItemID == oldID {
			packageContext.Audit[auditIndex].ItemID = unit.ID
		}
	}
}

func contextGatewayCloneProjection(value ContextGatewayProjection) ContextGatewayProjection {
	clone := value
	clone.ValidationPackage.Evidence = append([]EvidenceReference(nil), value.ValidationPackage.Evidence...)
	clone.GatewayPackage.Evidence = append([]aigateway.AuthorizedEvidence(nil), value.GatewayPackage.Evidence...)
	clone.GatewayPackage.Gaps = append([]string(nil), value.GatewayPackage.Gaps...)
	clone.GapIDs = append([]string(nil), value.GapIDs...)
	return clone
}
