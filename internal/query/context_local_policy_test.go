package query

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/pedrogpaulino/manu/internal/evidence"
)

func TestApplyLocalContextPolicyPreservesDeniedTransferForLocalPackage(t *testing.T) {
	fixture := contextPolicyTestFixture(t, "safe local policy content")
	items := []ContextItem{fixture.fact, fixture.entity, fixture.evidence}
	policy := &evidence.Policy{Installation: evidence.PolicyLayer{
		Persist: evidence.DecisionAllow, ExternalTransfer: evidence.DecisionDeny,
	}}
	request := contextPolicyTestRequest(t, fixture.scope, items, []ContextRelation{fixture.relation}, map[string]evidence.Decision{
		fixture.fact.ID:     evidence.DecisionAllow,
		fixture.entity.ID:   evidence.DecisionAllow,
		fixture.evidence.ID: evidence.DecisionAllow,
	}, policy, []string{fixture.fact.ID, fixture.entity.ID, fixture.evidence.ID})
	request.Mode = ContextPolicyModeLocal
	request.PolicyDigest = mustContextLocalPolicyDigest(t, policy, request.Authorizations)

	result, err := ApplyLocalContextPolicy(context.Background(), request)
	if err != nil {
		t.Fatalf("ApplyLocalContextPolicy() error = %v", err)
	}
	if err := result.ValidateAgainst(request); err != nil {
		t.Fatalf("local result.ValidateAgainst() error = %v", err)
	}
	if result.Mode != ContextPolicyModeLocal || len(result.Items) != len(items) || len(result.Relations) != 1 {
		t.Fatalf("local result = %#v", result)
	}
	var localEvidence ContextItem
	for _, item := range result.Items {
		if item.Kind == ContextItemEvidence {
			localEvidence = item
		}
	}
	if localEvidence.Evidence == nil || localEvidence.Evidence.Content != fixture.evidence.Evidence.Content || localEvidence.Evidence.ExternalTransfer != evidence.DecisionDeny {
		t.Fatalf("local evidence = %#v, want retained content with denied transfer", localEvidence)
	}
	if _, err := ProjectContextPackage(context.Background(), contextPackageFromPolicyResult(t, result, fixture.scope)); !errors.Is(err, ErrInvalidContextGatewayProjection) {
		t.Fatalf("gateway projection error = %v, want %v", err, ErrInvalidContextGatewayProjection)
	}
}

func TestApplyLocalContextPolicyHonorsPersistenceAndRedaction(t *testing.T) {
	fixture := contextPolicyTestFixture(t, "safe persistence policy content")

	t.Run("redact", func(t *testing.T) {
		policy := &evidence.Policy{Installation: evidence.PolicyLayer{
			Persist: evidence.DecisionRedact, ExternalTransfer: evidence.DecisionDeny,
		}}
		request := contextPolicyTestRequest(t, fixture.scope, []ContextItem{fixture.evidence}, nil, map[string]evidence.Decision{
			fixture.evidence.ID: evidence.DecisionAllow,
		}, policy, []string{fixture.evidence.ID})
		request.Mode = ContextPolicyModeLocal
		request.PolicyDigest = mustContextLocalPolicyDigest(t, policy, request.Authorizations)

		result, err := ApplyLocalContextPolicy(context.Background(), request)
		if err != nil {
			t.Fatalf("ApplyLocalContextPolicy() error = %v", err)
		}
		if len(result.Items) != 1 || result.Items[0].Evidence == nil || result.Items[0].Evidence.Content != evidence.RedactedContent || result.Items[0].Evidence.ContentState != evidence.ContentStateRedacted || result.Items[0].Evidence.ExternalTransfer != evidence.DecisionDeny {
			t.Fatalf("redacted local result = %#v", result)
		}
		if strings.Contains(string(mustJSON(t, result)), fixture.evidence.Evidence.Content) {
			t.Fatal("original content leaked into redacted local result")
		}
	})

	t.Run("deny", func(t *testing.T) {
		policy := &evidence.Policy{Installation: evidence.PolicyLayer{
			Persist: evidence.DecisionDeny, ExternalTransfer: evidence.DecisionDeny,
		}}
		request := contextPolicyTestRequest(t, fixture.scope, []ContextItem{fixture.evidence}, nil, map[string]evidence.Decision{
			fixture.evidence.ID: evidence.DecisionAllow,
		}, policy, []string{fixture.evidence.ID})
		request.Mode = ContextPolicyModeLocal
		request.PolicyDigest = mustContextLocalPolicyDigest(t, policy, request.Authorizations)

		result, err := ApplyLocalContextPolicy(context.Background(), request)
		if err != nil {
			t.Fatalf("ApplyLocalContextPolicy() error = %v", err)
		}
		if len(result.Items) != 0 || len(result.ContinuationIDs) != 0 || result.ItemAudit[0].Reason != ContextPolicyItemExcludedPersistence {
			t.Fatalf("denied local result = %#v", result)
		}
	})
}

func TestLocalContextPolicyDigestIsDistinctAndExternalPathRemainsSeparate(t *testing.T) {
	fixture := contextPolicyTestFixture(t, "local digest content")
	policy := &evidence.Policy{Installation: evidence.PolicyLayer{
		Persist: evidence.DecisionAllow, ExternalTransfer: evidence.DecisionDeny,
	}}
	authorizations := []ContextItemAuthorization{{ItemID: fixture.evidence.ID, Decision: evidence.DecisionAllow}}
	externalDigest, err := ContextPolicyDigest(policy, authorizations)
	if err != nil {
		t.Fatalf("ContextPolicyDigest() error = %v", err)
	}
	localDigest, err := ContextLocalPolicyDigest(policy, authorizations)
	if err != nil {
		t.Fatalf("ContextLocalPolicyDigest() error = %v", err)
	}
	if externalDigest == localDigest {
		t.Fatalf("local and external policy digests collide: %q", localDigest)
	}
	changedExternalPolicy := *policy
	changedExternalPolicy.Installation.ExternalTransfer = evidence.DecisionAllow
	changedLocalDigest, err := ContextLocalPolicyDigest(&changedExternalPolicy, authorizations)
	if err != nil {
		t.Fatalf("ContextLocalPolicyDigest() with changed transfer decision error = %v", err)
	}
	if changedLocalDigest != localDigest {
		t.Fatalf("local digest changed with only ExternalTransfer changed: %q != %q", changedLocalDigest, localDigest)
	}
	changedExternalDigest, err := ContextPolicyDigest(&changedExternalPolicy, authorizations)
	if err != nil {
		t.Fatalf("ContextPolicyDigest() with changed transfer decision error = %v", err)
	}
	if changedExternalDigest == externalDigest {
		t.Fatal("external digest did not change with ExternalTransfer")
	}

	request := contextPolicyTestRequest(t, fixture.scope, []ContextItem{fixture.evidence}, nil, map[string]evidence.Decision{
		fixture.evidence.ID: evidence.DecisionAllow,
	}, policy, nil)
	localRequest := request
	localRequest.Mode = ContextPolicyModeLocal
	localRequest.PolicyDigest = localDigest
	local, err := ApplyLocalContextPolicy(context.Background(), localRequest)
	if err != nil {
		t.Fatalf("ApplyLocalContextPolicy() error = %v", err)
	}
	external, err := ApplyContextPolicy(context.Background(), request)
	if err != nil {
		t.Fatalf("ApplyContextPolicy() error = %v", err)
	}
	if len(local.Items) != 1 || len(external.Items) != 0 {
		t.Fatalf("local/external item counts = %d/%d", len(local.Items), len(external.Items))
	}
	if reflect.DeepEqual(local, external) {
		t.Fatal("local and external policy results unexpectedly equal")
	}
}

func TestApplyLocalContextPolicyFailsClosedOnCancellationAndInvalidInput(t *testing.T) {
	fixture := contextPolicyTestFixture(t, "local cancellation content")
	request := contextPolicyTestRequest(t, fixture.scope, []ContextItem{fixture.evidence}, nil, map[string]evidence.Decision{
		fixture.evidence.ID: evidence.DecisionAllow,
	}, nil, nil)
	request.Mode = ContextPolicyModeLocal
	request.PolicyDigest = mustContextLocalPolicyDigest(t, nil, request.Authorizations)

	if _, err := ApplyLocalContextPolicy(nil, request); !errors.Is(err, ErrInvalidContextPolicy) {
		t.Fatalf("nil context error = %v", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ApplyLocalContextPolicy(canceled, request); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled context error = %v", err)
	}

	secret := "password = local-policy-secret"
	malformed := request
	malformed.Items = []ContextItem{cloneContextItem(fixture.evidence)}
	malformed.Items[0].ID = secret
	if _, err := ApplyLocalContextPolicy(context.Background(), malformed); err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("invalid input error = %v, secret echoed=%v", err, err != nil && strings.Contains(err.Error(), secret))
	}
}

func mustContextLocalPolicyDigest(t *testing.T, policy *evidence.Policy, authorizations []ContextItemAuthorization) string {
	t.Helper()
	digest, err := ContextLocalPolicyDigest(policy, authorizations)
	if err != nil {
		t.Fatalf("ContextLocalPolicyDigest() error = %v", err)
	}
	return digest
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	return encoded
}

func contextPackageFromPolicyResult(t *testing.T, result ContextPolicyResult, scope Scope) ContextPackage {
	t.Helper()
	if len(result.Items) == 0 {
		t.Fatal("policy result has no items")
	}
	packageContext := ContextPackage{
		Version:  ContextVersion,
		Revision: "local-policy-test-revision",
		Scope:    scope,
		Intent:   Intent{Version: ContextVersion, Kind: IntentKindQuestion, Question: "local policy"},
		Limits:   ContextLimits{MaxTokens: 4_096, MaxItems: 8, MaxCharacters: 64 << 10, MaxBytes: 64 << 10},
		Items:    result.Items,
	}
	for _, item := range packageContext.Items {
		packageContext.Audit = append(packageContext.Audit, ContextSelectionAudit{
			ItemID: item.ID, Included: true, Reason: ContextSelectionIncluded,
		})
	}
	finalized, err := FinalizeContextPackage(context.Background(), packageContext, ContextPackageIdentityBinding{
		PolicyDigest:          result.PolicyDigest,
		PolicyContinuationIDs: result.ContinuationIDs,
		PolicyFiltered:        result.PolicyFiltered,
	})
	if err != nil {
		t.Fatalf("FinalizeContextPackage() error = %v", err)
	}
	return finalized
}
