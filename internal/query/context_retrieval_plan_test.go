package query

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/pedrogpaulino/manu/internal/retrieval"
)

func TestPlanContextRetrievalMapsEveryContextIntent(t *testing.T) {
	tests := []struct {
		name         string
		intent       Intent
		question     string
		questionKind KnowledgeQuestionKind
		rawID        string
	}{
		{
			name:         "question",
			intent:       Intent{Version: ContextVersion, Kind: IntentKindQuestion, Question: "where does the service call?"},
			question:     "where does the service call?",
			questionKind: KnowledgeQuestionInventory,
			rawID:        "where does the service call?",
		},
		{
			name:         "entity",
			intent:       Intent{Version: ContextVersion, Kind: IntentKindEntity, Target: IntentTarget{Kind: IntentTargetEntity, ID: "entity-plan"}},
			question:     "entity-plan",
			questionKind: KnowledgeQuestionInventory,
			rawID:        "entity-plan",
		},
		{
			name:         "symbol",
			intent:       Intent{Version: ContextVersion, Kind: IntentKindSymbol, Target: IntentTarget{Kind: IntentTargetSymbol, ID: "symbol-plan"}},
			question:     "symbol-plan",
			questionKind: KnowledgeQuestionInventory,
			rawID:        "symbol-plan",
		},
		{
			name:         "evidence inspection",
			intent:       Intent{Version: ContextVersion, Kind: IntentKindEvidenceInspection, Target: IntentTarget{Kind: IntentTargetEvidence, ID: "evidence-plan"}},
			question:     "evidence-plan",
			questionKind: KnowledgeQuestionInventory,
			rawID:        "evidence-plan",
		},
		{
			name:         "possible impact",
			intent:       Intent{Version: ContextVersion, Kind: IntentKindPossibleImpact, Target: IntentTarget{Kind: IntentTargetEntity, ID: "entity-impact-plan"}},
			question:     "entity-impact-plan",
			questionKind: KnowledgeQuestionPossibleFlow,
			rawID:        "entity-impact-plan",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scope := contextTestScope()
			request := ContextRequest{
				Version: ContextVersion,
				Scope:   scope,
				Intent:  tt.intent,
				Limits:  contextTestLimits(),
			}
			before := request
			got, err := PlanContextRetrieval(context.Background(), request, 17)
			if err != nil {
				t.Fatalf("PlanContextRetrieval() error = %v", err)
			}
			if err := got.Validate(); err != nil {
				t.Fatalf("ContextRetrievalPlan.Validate() error = %v", err)
			}
			if got.Input.Question != tt.question || got.Input.QuestionKind != tt.questionKind || got.Input.Scope != scope || got.Input.Limit != 17 {
				t.Fatalf("retrieval input = %+v", got.Input)
			}
			if strings.Contains(got.Input.ExecutionID, tt.rawID) || strings.Contains(got.Input.RequestID, tt.rawID) {
				t.Fatalf("raw intent identity leaked into plan ids: %+v", got.Input)
			}
			encoded, encodeErr := json.Marshal(tt.intent)
			if encodeErr != nil {
				t.Fatalf("intent encoding error = %v", encodeErr)
			}
			digest := sha256.Sum256(encoded)
			if got.IntentDigest != hex.EncodeToString(digest[:]) {
				t.Fatalf("intent digest = %q, want typed JSON digest", got.IntentDigest)
			}
			again, err := PlanContextRetrieval(context.Background(), request, 17)
			if err != nil || !reflect.DeepEqual(got, again) {
				t.Fatalf("planning is not deterministic: got %+v, again %+v, err %v", got, again, err)
			}
			if !reflect.DeepEqual(request, before) {
				t.Fatalf("request was mutated")
			}
		})
	}
}

func TestPlanContextRetrievalRejectsInvalidInputsSafely(t *testing.T) {
	validRequest := ContextRequest{
		Version: ContextVersion,
		Scope:   contextTestScope(),
		Intent:  Intent{Version: ContextVersion, Kind: IntentKindQuestion, Question: "inventory"},
		Limits:  contextTestLimits(),
	}
	tests := []struct {
		name    string
		ctx     context.Context
		request ContextRequest
		limit   int
		want    error
	}{
		{name: "nil context", request: validRequest, limit: 1, want: ErrInvalidContextRetrievalPlan},
		{name: "canceled context", ctx: canceledContextForPlanTest(), request: validRequest, limit: 1, want: context.Canceled},
		{name: "invalid request", request: func() ContextRequest {
			request := validRequest
			request.Version = "v0"
			return request
		}(), limit: 1, want: ErrInvalidContextRetrievalPlan},
		{name: "zero limit", request: validRequest, limit: 0, want: ErrInvalidContextRetrievalPlan},
		{name: "excessive limit", request: validRequest, limit: retrieval.MaxTextSearchLimit + 1, want: ErrInvalidContextRetrievalPlan},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := tt.ctx
			if ctx == nil && tt.name != "nil context" {
				ctx = context.Background()
			}
			_, err := PlanContextRetrieval(ctx, tt.request, tt.limit)
			if !errors.Is(err, tt.want) {
				t.Fatalf("error = %v, want errors.Is(_, %v)", err, tt.want)
			}
			if strings.Contains(err.Error(), validRequest.Intent.Question) && tt.name != "canceled context" {
				t.Fatalf("error echoed raw intent: %v", err)
			}
		})
	}

	plan, err := PlanContextRetrieval(context.Background(), validRequest, 1)
	if err != nil {
		t.Fatalf("valid plan error = %v", err)
	}
	plan.Input.ExecutionID = "inventory"
	if !errors.Is(plan.Validate(), ErrInvalidContextRetrievalPlan) {
		t.Fatalf("tampered execution id was accepted")
	}
	plan, err = PlanContextRetrieval(context.Background(), validRequest, 1)
	if err != nil {
		t.Fatalf("valid plan error = %v", err)
	}
	plan.IntentDigest = "invalid"
	if !errors.Is(plan.Validate(), ErrInvalidContextRetrievalPlan) {
		t.Fatalf("tampered intent digest was accepted")
	}
}

func canceledContextForPlanTest() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}
