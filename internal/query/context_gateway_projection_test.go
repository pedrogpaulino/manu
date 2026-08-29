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
		gatewayLocator, err := contextGatewayLocator(item.Locator)
		if err != nil {
			t.Fatalf("contextGatewayLocator(%d) error = %v", index, err)
		}
		if gateway.Locator != gatewayLocator || validation.Locator != item.Locator {
			t.Fatalf("locator at %d = %q/%#v, want %q/%#v", index, gateway.Locator, validation.Locator, gatewayLocator, item.Locator)
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

func TestProjectContextPackageRejectsUnsealedPackage(t *testing.T) {
	packageContext := contextGatewayTestPackage(t, "unsealed gateway content")
	projection, err := ProjectContextPackage(context.Background(), packageContext)
	if err != nil {
		t.Fatalf("ProjectContextPackage() error = %v", err)
	}

	unsealed := packageContext.Clone()
	unsealed.IdentityBinding = nil
	if _, err := ProjectContextPackage(context.Background(), unsealed); !errors.Is(err, ErrInvalidContextGatewayProjection) {
		t.Fatalf("ProjectContextPackage(unsealed) error = %v, want %v", err, ErrInvalidContextGatewayProjection)
	}
	if err := projection.ValidateAgainst(unsealed); !errors.Is(err, ErrInvalidContextGatewayProjection) {
		t.Fatalf("ValidateAgainst(unsealed) error = %v, want %v", err, ErrInvalidContextGatewayProjection)
	}
}

func TestProjectContextPackageForExternalTransferAppliesPolicyAtomically(t *testing.T) {
	packageContext := contextGatewayTestPackage(t, "external policy content")
	before := packageContext.Clone()

	tests := []struct {
		name             string
		policy           evidence.Policy
		wantError        bool
		wantEvidence     int
		wantRedactedOnly bool
		wantPresent      bool
	}{
		{
			name:         "deny",
			policy:       evidence.Policy{Installation: evidence.PolicyLayer{ExternalTransfer: evidence.DecisionDeny}},
			wantError:    true,
			wantEvidence: 0,
		},
		{
			name:             "redact",
			policy:           evidence.Policy{Installation: evidence.PolicyLayer{ExternalTransfer: evidence.DecisionRedact}},
			wantEvidence:     2,
			wantRedactedOnly: true,
		},
		{
			name:         "allow",
			policy:       evidence.Policy{Installation: evidence.PolicyLayer{ExternalTransfer: evidence.DecisionAllow}},
			wantEvidence: 5,
			wantPresent:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			projection, err := ProjectContextPackageForExternalTransfer(context.Background(), packageContext, tt.policy)
			if tt.wantError {
				if !errors.Is(err, ErrInvalidContextGatewayProjection) {
					t.Fatalf("ProjectContextPackageForExternalTransfer() error = %v, want %v", err, ErrInvalidContextGatewayProjection)
				}
				if !reflect.DeepEqual(projection, ContextGatewayProjection{}) {
					t.Fatalf("denied projection = %#v, want no projection", projection)
				}
				if !reflect.DeepEqual(packageContext, before) {
					t.Fatal("external projection mutated local package")
				}
				return
			}
			if err != nil {
				t.Fatalf("ProjectContextPackageForExternalTransfer() error = %v", err)
			}
			if err := projection.Validate(); err != nil {
				t.Fatalf("projection.Validate() error = %v", err)
			}
			if len(projection.GatewayPackage.Evidence) != tt.wantEvidence || len(projection.ValidationPackage.Evidence) != tt.wantEvidence {
				t.Fatalf("projected evidence count = %d/%d, want %d", len(projection.GatewayPackage.Evidence), len(projection.ValidationPackage.Evidence), tt.wantEvidence)
			}
			var redacted, present bool
			for _, item := range projection.GatewayPackage.Evidence {
				if item.Content == evidence.RedactedContent {
					redacted = true
				}
				if item.Content == "external policy content" {
					present = true
				}
			}
			if tt.wantRedactedOnly && (!redacted || present) {
				t.Fatalf("redacted projection leaked or omitted representation: %#v", projection.GatewayPackage.Evidence)
			}
			if tt.wantPresent && !present {
				t.Fatalf("allow projection omitted present content: %#v", projection.GatewayPackage.Evidence)
			}
			if !reflect.DeepEqual(packageContext, before) {
				t.Fatal("external projection mutated local package")
			}
		})
	}
}

func TestProjectContextPackageRejectsFinalizedTransferTampering(t *testing.T) {
	packageContext := contextGatewayTestPackage(t, "finalized transfer content")
	packageContext.Relations = nil
	packageContext.Items = packageContext.Items[:3]
	packageContext.Audit = packageContext.Audit[:3]
	denied, err := evidence.PrepareForExternalTransfer(*packageContext.Items[2].Evidence, evidence.Policy{
		Installation: evidence.PolicyLayer{ExternalTransfer: evidence.DecisionDeny},
	})
	if err != nil {
		t.Fatalf("PrepareForExternalTransfer() error = %v", err)
	}
	oldID := packageContext.Items[2].ID
	packageContext.Items[2].ID = denied.ID
	packageContext.Items[2].Evidence = &denied
	packageContext.Items[2].Provenance.Evidence[0].ID = denied.ID
	for index := range packageContext.Items {
		for supportIndex, supportID := range packageContext.Items[index].SupportIDs {
			if supportID == oldID {
				packageContext.Items[index].SupportIDs[supportIndex] = denied.ID
			}
		}
	}
	for index := range packageContext.Audit {
		if packageContext.Audit[index].ItemID == oldID {
			packageContext.Audit[index].ItemID = denied.ID
		}
	}
	packageContext.ID = ""
	packageContext.Digest = ""
	packageContext.IdentityBinding = nil
	finalized, err := FinalizeContextPackage(context.Background(), packageContext, ContextPackageIdentityBinding{
		PolicyDigest:          strings.Repeat("a", 64),
		PolicyContinuationIDs: []string{packageContext.Items[0].ID, packageContext.Items[1].ID},
	})
	if err != nil {
		t.Fatalf("FinalizeContextPackage() error = %v", err)
	}
	for _, tt := range []struct {
		name   string
		mutate func(*ContextPackage)
	}{
		{
			name: "deny to allow",
			mutate: func(value *ContextPackage) {
				value.Items[2].Evidence.ExternalTransfer = evidence.DecisionAllow
			},
		},
		{
			name: "content",
			mutate: func(value *ContextPackage) {
				value.Items[2].Evidence.Content = "tampered transfer content"
			},
		},
		{
			name: "package id",
			mutate: func(value *ContextPackage) {
				value.ID = "context-tampered"
			},
		},
		{
			name: "package digest",
			mutate: func(value *ContextPackage) {
				value.Digest = strings.Repeat("b", 64)
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			mutated := finalized.Clone()
			tt.mutate(&mutated)
			if err := mutated.Validate(); err == nil {
				t.Fatalf("ContextPackage.Validate() accepted %s tampering", tt.name)
			}
			if _, err := ProjectContextPackage(context.Background(), mutated); !errors.Is(err, ErrInvalidContextGatewayProjection) {
				t.Fatalf("legacy projection error = %v, want %v", err, ErrInvalidContextGatewayProjection)
			}
			if _, err := ProjectContextPackageForExternalTransfer(context.Background(), mutated, evidence.Policy{Installation: evidence.PolicyLayer{ExternalTransfer: evidence.DecisionAllow}}); !errors.Is(err, ErrInvalidContextGatewayProjection) {
				t.Fatalf("external projection error = %v, want %v", err, ErrInvalidContextGatewayProjection)
			}
		})
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
	if _, err := ProjectContextPackage(context.Background(), changedDigest); !errors.Is(err, ErrInvalidContextGatewayProjection) {
		t.Fatalf("changed digest projection error = %v, want invalid projection", err)
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
	changedRelation = contextGatewayRefinalize(t, changedRelation)
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
	packageContext = contextGatewayRefinalize(t, packageContext)
	projection, err := ProjectContextPackage(context.Background(), packageContext)
	if err != nil {
		t.Fatalf("ProjectContextPackage() error = %v", err)
	}
	wantLocator, err := contextGatewayLocator(packageContext.Items[2].Locator)
	if err != nil {
		t.Fatalf("contextGatewayLocator(support locator) error = %v", err)
	}
	relationEvidence := projection.GatewayPackage.Evidence[len(packageContext.Items)]
	if relationEvidence.Locator != wantLocator {
		t.Fatalf("relation locator = %q, want authorized support locator %q", relationEvidence.Locator, wantLocator)
	}
	otherLocatorEncoded, err := contextGatewayLocator(otherLocator)
	if err != nil {
		t.Fatalf("contextGatewayLocator(other locator) error = %v", err)
	}
	if relationEvidence.Locator == otherLocatorEncoded {
		t.Fatalf("relation used nested provenance locator %q", otherLocatorEncoded)
	}
}

func TestContextGatewayLocatorCompactsRealSourceLocators(t *testing.T) {
	scope := contextTestScope()
	javaLocator := contract.Locator{
		SourceID:    scope.SourceID,
		ArtifactID:  "11111111-1111-4111-8111-111111111111",
		Path:        "src/main/java/com/example/BookingResource.java",
		StartLine:   42,
		StartColumn: 7,
		EndLine:     42,
		EndColumn:   21,
	}
	wso2Locator := contract.Locator{
		SourceID:   scope.SourceID,
		ArtifactID: "22222222-2222-4222-8222-222222222222",
		Path:       "apis/booking/api-v1.xml",
		Member:     "resource:GET:/v1/bookings",
		ByteOffset: 4096,
		ByteLength: 128,
	}
	longPath := "src/" + strings.Repeat("deep/", 700) + "handler.py"
	longPathLocator := contract.Locator{SourceID: scope.SourceID, Path: longPath}
	tests := []struct {
		name           string
		locator        contract.Locator
		wantFragments  []string
		changedLocator func(contract.Locator) contract.Locator
	}{
		{
			name:    "java artifact and line position",
			locator: javaLocator,
			wantFragments: []string{
				`"a":"11111111-1111-4111-8111-111111111111"`,
				`"l":42`,
				`"c":7`,
				`"el":42`,
				`"ec":21`,
			},
			changedLocator: func(value contract.Locator) contract.Locator {
				value.StartColumn++
				return value
			},
		},
		{
			name:    "wso2 artifact member and byte position",
			locator: wso2Locator,
			wantFragments: []string{
				`"a":"22222222-2222-4222-8222-222222222222"`,
				`"m":"resource:GET:/v1/bookings"`,
				`"o":4096`,
				`"n":128`,
			},
			changedLocator: func(value contract.Locator) contract.Locator {
				value.ByteOffset++
				return value
			},
		},
		{
			name:    "long path uses prefix and digest",
			locator: longPathLocator,
			wantFragments: []string{
				`"s":"` + scope.SourceID + `"`,
				`"p":"src/deep/deep/`,
			},
			changedLocator: func(value contract.Locator) contract.Locator {
				value.Path += "-changed"
				return value
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			full, err := json.Marshal(tt.locator)
			if err != nil {
				t.Fatalf("json.Marshal(full locator) error = %v", err)
			}
			first, err := contextGatewayLocator(tt.locator)
			if err != nil {
				t.Fatalf("contextGatewayLocator() error = %v", err)
			}
			second, err := contextGatewayLocator(tt.locator)
			if err != nil {
				t.Fatalf("second contextGatewayLocator() error = %v", err)
			}
			if first != second {
				t.Fatalf("locator projection is not deterministic: %q != %q", first, second)
			}
			if len([]byte(first)) > contextGatewayMaxLocatorBytes {
				t.Fatalf("compact locator bytes = %d, want <= %d: %q", len([]byte(first)), contextGatewayMaxLocatorBytes, first)
			}
			if first == string(full) {
				t.Fatalf("gateway locator retained full locator: %q", first)
			}
			for _, fragment := range tt.wantFragments {
				if !strings.Contains(first, fragment) {
					t.Fatalf("compact locator %q lacks %q", first, fragment)
				}
			}

			changed, err := contextGatewayLocator(tt.changedLocator(tt.locator))
			if err != nil {
				t.Fatalf("changed contextGatewayLocator() error = %v", err)
			}
			if changed == first {
				t.Fatalf("distinct source positions collapsed to same gateway locator: %q", first)
			}
		})
	}
}

func TestContextGatewayLocatorKeepsLineAndByteIdentity(t *testing.T) {
	scope := contextTestScope()
	base := contract.Locator{
		SourceID:    scope.SourceID,
		ArtifactID:  "55555555-5555-4555-8555-555555555555",
		Path:        "src/main.py",
		StartLine:   42,
		StartColumn: 7,
		EndLine:     42,
		EndColumn:   21,
		ByteOffset:  4096,
		ByteLength:  128,
	}
	changed := base
	changed.ByteOffset++
	first, err := contextGatewayLocator(base)
	if err != nil {
		t.Fatalf("contextGatewayLocator(base) error = %v", err)
	}
	second, err := contextGatewayLocator(changed)
	if err != nil {
		t.Fatalf("contextGatewayLocator(changed) error = %v", err)
	}
	if first == second {
		t.Fatalf("locators with same line but different bytes collided: %q", first)
	}
	for _, fragment := range []string{`"l":42`, `"c":7`, `"o":4096`, `"n":128`} {
		if !strings.Contains(first, fragment) {
			t.Fatalf("combined locator %q lacks %q", first, fragment)
		}
	}
	if len([]byte(first)) > contextGatewayMaxLocatorBytes || len([]byte(second)) > contextGatewayMaxLocatorBytes {
		t.Fatalf("combined locators exceed gateway limit: %d/%d", len([]byte(first)), len([]byte(second)))
	}
}

func TestContextGatewayLocatorDigestFallbackBoundsLongLocatorWithoutArtifact(t *testing.T) {
	scope := contextTestScope()
	base := contract.Locator{
		SourceID:    scope.SourceID,
		Path:        "src/" + strings.Repeat("path-segment/", 100),
		Member:      "member-" + strings.Repeat("nested.", 100),
		URI:         "manu://" + strings.Repeat("uri-segment/", 100),
		StartLine:   42,
		StartColumn: 7,
		EndLine:     42,
		EndColumn:   21,
		ByteOffset:  4096,
		ByteLength:  128,
	}
	if len([]byte(base.Path))+len([]byte(base.Member))+len([]byte(base.URI)) > int(maxContextLocatorBytes) {
		t.Fatalf("test locator unexpectedly exceeds context bound")
	}
	changed := base
	changed.Member += "changed"
	first, err := contextGatewayLocator(base)
	if err != nil {
		t.Fatalf("contextGatewayLocator(base) error = %v", err)
	}
	second, err := contextGatewayLocator(base)
	if err != nil {
		t.Fatalf("second contextGatewayLocator(base) error = %v", err)
	}
	changedProjection, err := contextGatewayLocator(changed)
	if err != nil {
		t.Fatalf("contextGatewayLocator(changed) error = %v", err)
	}
	if first != second {
		t.Fatalf("digest fallback is not deterministic: %q != %q", first, second)
	}
	if first == changedProjection {
		t.Fatalf("long path/member/URI locators collided: %q", first)
	}
	if len([]byte(first)) > contextGatewayMaxLocatorBytes || len([]byte(changedProjection)) > contextGatewayMaxLocatorBytes {
		t.Fatalf("digest fallback exceeds gateway limit: %d/%d", len([]byte(first)), len([]byte(changedProjection)))
	}
	if !strings.Contains(first, `"s":"`+scope.SourceID+`"`) || !strings.Contains(first, `"d":"`) {
		t.Fatalf("digest fallback lost source identity or digest: %q", first)
	}
	for _, value := range []string{base.Path, base.Member, base.URI} {
		if strings.Contains(first, value) {
			t.Fatalf("digest fallback leaked long locator component %q", value)
		}
	}
}

func TestProjectContextPackageKeepsFullValidationLocatorsAndBoundsGatewayLocators(t *testing.T) {
	scope := contextTestScope()
	tests := []struct {
		name    string
		locator contract.Locator
	}{
		{
			name: "java uuid path line",
			locator: contract.Locator{
				SourceID:    scope.SourceID,
				ArtifactID:  "33333333-3333-4333-8333-333333333333",
				Path:        "src/main/java/com/example/BookingResource.java",
				StartLine:   42,
				StartColumn: 7,
				EndLine:     42,
				EndColumn:   21,
			},
		},
		{
			name: "wso2 byte member",
			locator: contract.Locator{
				SourceID:   scope.SourceID,
				ArtifactID: "44444444-4444-4444-8444-444444444444",
				Path:       "apis/booking/api-v1.xml",
				Member:     "resource:GET:/v1/bookings",
				ByteOffset: 4096,
				ByteLength: 128,
			},
		},
		{
			name:    "long path",
			locator: contract.Locator{SourceID: scope.SourceID, Path: "src/" + strings.Repeat("deep/", 700) + "handler.py"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			packageContext := contextGatewayTestPackage(t, "full locator projection")
			for index := range packageContext.Items {
				packageContext.Items[index].Locator = tt.locator
			}
			packageContext = contextGatewayRefinalize(t, packageContext)
			if err := packageContext.Validate(); err != nil {
				t.Fatalf("ContextPackage.Validate() error = %v", err)
			}
			projection, err := ProjectContextPackage(context.Background(), packageContext)
			if err != nil {
				t.Fatalf("ProjectContextPackage() error = %v", err)
			}
			if err := projection.ValidateAgainst(packageContext); err != nil {
				t.Fatalf("projection.ValidateAgainst() error = %v", err)
			}
			wantGatewayLocator, err := contextGatewayLocator(tt.locator)
			if err != nil {
				t.Fatalf("contextGatewayLocator() error = %v", err)
			}
			fullLocator, err := json.Marshal(tt.locator)
			if err != nil {
				t.Fatalf("json.Marshal(locator) error = %v", err)
			}
			for index, reference := range projection.ValidationPackage.Evidence {
				if reference.Locator != tt.locator {
					t.Fatalf("validation locator %d = %#v, want full %#v", index, reference.Locator, tt.locator)
				}
				gateway := projection.GatewayPackage.Evidence[index]
				if gateway.Locator != wantGatewayLocator || len([]byte(gateway.Locator)) > contextGatewayMaxLocatorBytes {
					t.Fatalf("gateway locator %d = %q, want %q and <= %d bytes", index, gateway.Locator, wantGatewayLocator, contextGatewayMaxLocatorBytes)
				}
				if gateway.Locator == string(fullLocator) {
					t.Fatalf("gateway locator %d retained full locator", index)
				}
			}
		})
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
	return contextGatewayRefinalize(t, packageContext)
}

func contextGatewayRefinalize(t *testing.T, packageContext ContextPackage) ContextPackage {
	t.Helper()
	var binding ContextPackageIdentityBinding
	if packageContext.IdentityBinding != nil {
		binding = *packageContext.IdentityBinding
		binding.PolicyContinuationIDs = append([]string(nil), packageContext.IdentityBinding.PolicyContinuationIDs...)
	} else {
		binding.PolicyDigest = strings.Repeat("a", 64)
		for _, item := range packageContext.Items {
			binding.PolicyContinuationIDs = append(binding.PolicyContinuationIDs, item.ID)
		}
	}
	packageContext.ID = ""
	packageContext.Digest = ""
	packageContext.IdentityBinding = nil
	finalized, err := FinalizeContextPackage(context.Background(), packageContext, binding)
	if err != nil {
		t.Fatalf("FinalizeContextPackage() error: %v", err)
	}
	return finalized
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
