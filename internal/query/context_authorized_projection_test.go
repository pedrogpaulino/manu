package query

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/pedrogpaulino/manu/internal/evidence"
)

func TestAuthorizeContextCandidateProjectionAllowsInOrderAndReestimatesCosts(t *testing.T) {
	scope, projection := authorizedContextProjectionFixture(t)
	before := cloneAuthorizedContextProjectionInput(projection)

	got, err := AuthorizeContextCandidateProjection(
		context.Background(), scope, projection, nil,
		ContextTokenEstimatorConfiguration{}, ContextTokenEstimationLimits{},
	)
	if err != nil {
		t.Fatalf("AuthorizeContextCandidateProjection() error = %v", err)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("authorized projection Validate() error = %v", err)
	}
	if len(got.Candidates) != len(projection.Candidates) || len(got.Relations) != len(projection.Relations) {
		t.Fatalf("authorized projection shape = %d/%d, want %d/%d", len(got.Candidates), len(got.Relations), len(projection.Candidates), len(projection.Relations))
	}
	for index, candidate := range got.Candidates {
		if candidate.Item.ID != projection.Candidates[index].Item.ID {
			t.Fatalf("candidate order = %q at %d, want %q", candidate.Item.ID, index, projection.Candidates[index].Item.ID)
		}
		if candidate.RedundancyKey != candidate.Item.ID {
			t.Fatalf("candidate redundancy key = %q, want output id %q", candidate.RedundancyKey, candidate.Item.ID)
		}
		copyCandidate := cloneContextSelectionCandidate(candidate)
		estimate, estimateErr := EstimateContextSelectionCandidateCosts(
			context.Background(), &copyCandidate,
			ContextTokenEstimatorConfiguration{}, ContextTokenEstimationLimits{},
		)
		if estimateErr != nil {
			t.Fatalf("candidate cost estimate = %v", estimateErr)
		}
		if candidate.TokenCost != estimate.TokenEstimate || candidate.CharacterCost != estimate.Characters || candidate.ByteCost != estimate.Bytes {
			t.Fatalf("candidate %q costs = %d/%d/%d, want %d/%d/%d", candidate.Item.ID, candidate.TokenCost, candidate.CharacterCost, candidate.ByteCost, estimate.TokenEstimate, estimate.Characters, estimate.Bytes)
		}
	}
	for index, relation := range got.Relations {
		if relation.Relation.ID != projection.Relations[index].Relation.ID || relation.Score != projection.Relations[index].Score || relation.Rank != projection.Relations[index].Rank {
			t.Fatalf("relation %d = %#v, want score/rank from input %#v", index, relation, projection.Relations[index])
		}
		copyRelation := cloneContextRelationCandidate(relation)
		estimate, estimateErr := EstimateContextRelationCandidateCosts(
			context.Background(), &copyRelation,
			ContextTokenEstimatorConfiguration{}, ContextTokenEstimationLimits{},
		)
		if estimateErr != nil {
			t.Fatalf("relation cost estimate = %v", estimateErr)
		}
		if relation.TokenCost != estimate.TokenEstimate || relation.CharacterCost != estimate.Characters || relation.ByteCost != estimate.Bytes {
			t.Fatalf("relation %q costs = %d/%d/%d, want %d/%d/%d", relation.Relation.ID, relation.TokenCost, relation.CharacterCost, relation.ByteCost, estimate.TokenEstimate, estimate.Characters, estimate.Bytes)
		}
	}
	if !reflect.DeepEqual(projection, before) {
		t.Fatal("authorization mutated candidate projection input")
	}

	got.Candidates[0].Item.Locator.Path = "mutated-output.go"
	if projection.Candidates[0].Item.Locator.Path == "mutated-output.go" {
		t.Fatal("authorized output aliases candidate input")
	}
}

func TestAuthorizeContextCandidateProjectionSupportsEmptyAndFiltersDependencies(t *testing.T) {
	scope, projection := authorizedContextProjectionFixture(t)

	empty, err := AuthorizeContextCandidateProjection(
		context.Background(), scope, ContextCandidateProjection{}, nil,
		ContextTokenEstimatorConfiguration{}, ContextTokenEstimationLimits{},
	)
	if err != nil {
		t.Fatalf("empty authorization error = %v", err)
	}
	if err := empty.Validate(); err != nil {
		t.Fatalf("empty authorization Validate() error = %v", err)
	}
	if !sameScope(empty.Policy.Scope, scope) || len(empty.Candidates) != 0 || len(empty.Relations) != 0 {
		t.Fatalf("empty authorization = %#v", empty)
	}

	deny := &evidence.Policy{Installation: evidence.PolicyLayer{ExternalTransfer: evidence.DecisionDeny}}
	filtered, err := AuthorizeContextCandidateProjection(
		context.Background(), scope, projection, deny,
		ContextTokenEstimatorConfiguration{}, ContextTokenEstimationLimits{},
	)
	if err != nil {
		t.Fatalf("deny authorization error = %v", err)
	}
	if err := filtered.Validate(); err != nil {
		t.Fatalf("deny authorization Validate() error = %v", err)
	}
	if len(filtered.Candidates) != 0 || len(filtered.Relations) != 0 || !filtered.Policy.PolicyFiltered {
		t.Fatalf("deny authorization retained dependent material: %#v", filtered)
	}
	if len(filtered.Policy.RelationAudit) != len(projection.Relations) || filtered.Policy.RelationAudit[0].Included {
		t.Fatalf("dependent relation audit = %#v", filtered.Policy.RelationAudit)
	}
}

func TestAuthorizeContextCandidateProjectionRedactsAndRemapsEvidence(t *testing.T) {
	scope, projection := authorizedContextProjectionFixture(t)
	policy := &evidence.Policy{Installation: evidence.PolicyLayer{ExternalTransfer: evidence.DecisionRedact}}
	beforePolicy := cloneAuthorizedContextPolicyForTest(policy)
	beforeProjection := cloneAuthorizedContextProjectionInput(projection)

	got, err := AuthorizeContextCandidateProjection(
		context.Background(), scope, projection, policy,
		ContextTokenEstimatorConfiguration{}, ContextTokenEstimationLimits{},
	)
	if err != nil {
		t.Fatalf("redaction authorization error = %v", err)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("redaction authorization Validate() error = %v", err)
	}
	if len(got.Candidates) != 1 || got.Candidates[0].Item.Kind != ContextItemEvidence {
		t.Fatalf("redaction candidates = %#v", got.Candidates)
	}
	redacted := got.Candidates[0]
	if redacted.RedundancyKey != redacted.Item.ID ||
		redacted.Item.Evidence == nil || redacted.Item.Evidence.ContentState != evidence.ContentStateRedacted {
		t.Fatalf("redacted evidence remap = %#v", redacted)
	}
	var evidenceAudit *ContextPolicyItemAudit
	for index := range got.Policy.ItemAudit {
		if got.Policy.ItemAudit[index].ItemID == projection.Candidates[2].Item.ID {
			evidenceAudit = &got.Policy.ItemAudit[index]
			break
		}
	}
	if evidenceAudit == nil || !evidenceAudit.Included || !evidenceAudit.Redacted || evidenceAudit.OutputID != redacted.Item.ID {
		t.Fatalf("redaction audit = %#v", evidenceAudit)
	}
	if !reflect.DeepEqual(policy, beforePolicy) || !reflect.DeepEqual(projection, beforeProjection) {
		t.Fatal("redaction authorization mutated policy or input")
	}
}

func TestAuthorizeContextCandidateProjectionIsDeterministicAndRejectsInvalidOrTamperedValues(t *testing.T) {
	scope, projection := authorizedContextProjectionFixture(t)
	policy := &evidence.Policy{Installation: evidence.PolicyLayer{ExternalTransfer: evidence.DecisionAllow}}
	first, err := AuthorizeContextCandidateProjection(
		context.Background(), scope, projection, policy,
		ContextTokenEstimatorConfiguration{}, ContextTokenEstimationLimits{},
	)
	if err != nil {
		t.Fatalf("first authorization error = %v", err)
	}
	second, err := AuthorizeContextCandidateProjection(
		context.Background(), scope, projection, policy,
		ContextTokenEstimatorConfiguration{}, ContextTokenEstimationLimits{},
	)
	if err != nil {
		t.Fatalf("second authorization error = %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("authorization is not deterministic")
	}

	tampered := first
	tampered.Candidates = append([]ContextSelectionCandidate(nil), first.Candidates...)
	tampered.Candidates[0] = cloneContextSelectionCandidate(first.Candidates[0])
	tampered.Candidates[0].Item.Locator.Path += ".tampered"
	if !errors.Is(tampered.Validate(), ErrInvalidContextAuthorizedProjection) {
		t.Fatalf("tampered candidate Validate() = %v", tampered.Validate())
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := AuthorizeContextCandidateProjection(
		canceled, scope, projection, policy,
		ContextTokenEstimatorConfiguration{}, ContextTokenEstimationLimits{},
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled authorization error = %v", err)
	}
	if _, err := AuthorizeContextCandidateProjection(
		nil, scope, projection, policy,
		ContextTokenEstimatorConfiguration{}, ContextTokenEstimationLimits{},
	); !errors.Is(err, ErrInvalidContextAuthorizedProjection) {
		t.Fatalf("nil context error = %v", err)
	}
	if _, err := AuthorizeContextCandidateProjection(
		context.Background(), Scope{}, projection, policy,
		ContextTokenEstimatorConfiguration{}, ContextTokenEstimationLimits{},
	); !errors.Is(err, ErrInvalidContextAuthorizedProjection) {
		t.Fatalf("invalid scope error = %v", err)
	}
	invalidPolicy := &evidence.Policy{Classifications: map[evidence.Classification]evidence.PolicyLayer{
		evidence.Classification("invalid-classification"): {},
	}}
	if _, err := AuthorizeContextCandidateProjection(
		context.Background(), scope, projection, invalidPolicy,
		ContextTokenEstimatorConfiguration{}, ContextTokenEstimationLimits{},
	); !errors.Is(err, ErrInvalidContextAuthorizedProjection) {
		t.Fatalf("invalid policy error = %v", err)
	}
}

func authorizedContextProjectionFixture(t *testing.T) (Scope, ContextCandidateProjection) {
	t.Helper()
	fixture := contextPolicyTestFixture(t, "authorized context projection")
	projection := ContextCandidateProjection{
		Candidates: []ContextSelectionCandidate{
			{Item: fixture.entity, Relevance: 0.30, Rank: 3, Aspects: []string{"entity"}, RedundancyKey: "metadata-entity"},
			{Item: fixture.fact, Relevance: 0.90, Rank: 1, Aspects: []string{"fact"}, RedundancyKey: "metadata-fact"},
			{Item: fixture.evidence, Relevance: 0.80, Rank: 2, Aspects: []string{"evidence"}, RedundancyKey: "metadata-evidence"},
		},
		Relations: []ContextRelationCandidate{{
			Relation: fixture.relation,
			Score:    0.75,
			Rank:     2,
		}},
	}
	if err := projection.Validate(); err != nil {
		t.Fatalf("authorization fixture invalid: %v", err)
	}
	return fixture.scope, projection
}

func cloneAuthorizedContextProjectionInput(input ContextCandidateProjection) ContextCandidateProjection {
	clone := input
	clone.Candidates = make([]ContextSelectionCandidate, len(input.Candidates))
	for index := range input.Candidates {
		clone.Candidates[index] = cloneContextSelectionCandidate(input.Candidates[index])
	}
	clone.Relations = make([]ContextRelationCandidate, len(input.Relations))
	for index := range input.Relations {
		clone.Relations[index] = cloneContextRelationCandidate(input.Relations[index])
	}
	return clone
}

func cloneAuthorizedContextPolicyForTest(policy *evidence.Policy) *evidence.Policy {
	if policy == nil {
		return nil
	}
	clone := *policy
	if policy.Classifications != nil {
		clone.Classifications = make(map[evidence.Classification]evidence.PolicyLayer, len(policy.Classifications))
		for classification, layer := range policy.Classifications {
			clone.Classifications[classification] = layer
		}
	}
	return &clone
}

func TestAuthorizedContextProjectionErrorsDoNotEchoInput(t *testing.T) {
	_, projection := authorizedContextProjectionFixture(t)
	secret := "Bearer authorized-projection-secret"
	projection.Candidates[0].Item.ID = secret
	if _, err := AuthorizeContextCandidateProjection(
		context.Background(), contextTestScope(), projection, nil,
		ContextTokenEstimatorConfiguration{}, ContextTokenEstimationLimits{},
	); err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("invalid input error = %v, secret echoed", err)
	}
}
