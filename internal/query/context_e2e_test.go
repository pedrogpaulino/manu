package query

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
	"unicode"

	"github.com/pedrogpaulino/manu/internal/analysis"
	javaanalyzer "github.com/pedrogpaulino/manu/internal/analyzer/java"
	pythonanalyzer "github.com/pedrogpaulino/manu/internal/analyzer/python"
	wso2analyzer "github.com/pedrogpaulino/manu/internal/analyzer/wso2"
	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/evidence"
	"github.com/pedrogpaulino/manu/internal/fact"
	"github.com/pedrogpaulino/manu/internal/identity"
	"github.com/pedrogpaulino/manu/internal/normalization"
	"github.com/pedrogpaulino/manu/internal/retrieval"
	"github.com/pedrogpaulino/manu/internal/source"
)

type contextE2EFamily struct {
	name                 string
	scope                Scope
	manifest             fact.FrontendManifest
	facts                []fact.CanonicalFact
	coverage             []contract.Coverage
	gaps                 []contract.Gap
	evidence             map[string]evidence.EvidenceUnit
	locators             map[string]contract.Locator
	policy               evidence.Policy
	producer             fact.Producer
	dependencyFact       fact.CanonicalFact
	dependencyEvidenceID string
	dependencyFactItem   ContextItem
	subjectItem          ContextItem
	objectItem           ContextItem
	evidenceItem         ContextItem
	relation             ContextRelation
	retrieval            *contextE2ERetrievalState
	subjectID            string
	objectID             string
	symbolID             string
	impactTargetID       string
	evidenceID           string
}

type contextE2EFile struct {
	sourcePath string
	path       string
}

type contextE2EAnalyzer struct {
	analyzer       analysis.Analyzer
	manifest       func() fact.FrontendManifest
	registrations  func(fact.FrontendManifest) ([]normalization.Registration, error)
	files          []contextE2EFile
	policy         evidence.Policy
	organizationID string
	sourceID       string
}

type contextE2EIntent struct {
	name       string
	kind       IntentKind
	targetKind IntentTargetKind
	question   string
}

type contextE2ERetrievalState struct {
	retriever           *HybridRetriever
	text                *contextE2ETextSearcher
	relationInputs      *contextE2ERelationInputProvider
	relations           *contextE2ERelationStore
	subjectEntityID     string
	objectEntityID      string
	evidenceIDsByUnitID map[string]string
	itemIDsByEvidenceID map[string][]string
	results             []QueryRetrievalResult
	inputs              []QueryRetrievalInput
}

type contextE2ETextDocument struct {
	hit   retrieval.TextHit
	terms map[string]struct{}
}

type contextE2ETextSearcher struct {
	scope   Scope
	docs    []contextE2ETextDocument
	queries []retrieval.SearchOptions
}

type contextE2ERelationInputProvider struct {
	scope             Scope
	seedEvidenceID    string
	anchorEntityID    string
	targetEntityID    string
	targetEvidenceID  string
	targetContentHash string
	calls             []QueryRetrievalInput
}

type contextE2ERelationStore struct {
	scope   Scope
	hit     retrieval.RelationHit
	queries []retrieval.RelationQuery
}

func (s *contextE2ETextSearcher) Search(ctx context.Context, options retrieval.SearchOptions) ([]retrieval.TextHit, error) {
	if ctx == nil {
		return nil, context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if options.OrganizationID != s.scope.OrganizationID || options.SourceID != s.scope.SourceID || options.SnapshotID != s.scope.SnapshotID {
		return nil, ErrQueryScopeRequired
	}
	s.queries = append(s.queries, options)
	terms := make(map[string]struct{})
	for _, term := range strings.Fields(strings.ToLower(options.Query)) {
		terms[term] = struct{}{}
	}
	type match struct {
		document contextE2ETextDocument
		score    int
	}
	matches := make([]match, 0, len(s.docs))
	for _, document := range s.docs {
		score := 0
		for term := range terms {
			if _, exists := document.terms[term]; exists {
				score++
			}
		}
		if score > 0 {
			matches = append(matches, match{document: document, score: score})
		}
	}
	sort.SliceStable(matches, func(left, right int) bool {
		if matches[left].score != matches[right].score {
			return matches[left].score > matches[right].score
		}
		return matches[left].document.hit.EvidenceID < matches[right].document.hit.EvidenceID
	})
	if options.Limit < len(matches) {
		matches = matches[:options.Limit]
	}
	hits := make([]retrieval.TextHit, 0, len(matches))
	for index, current := range matches {
		hit := current.document.hit
		hit.Rank = float64(index + 1)
		hit.ExactMatch = true
		hits = append(hits, hit)
	}
	return hits, nil
}

func (p *contextE2ERelationInputProvider) RelationInputs(ctx context.Context, input QueryRetrievalInput, textHits []retrieval.TextHit, _ []retrieval.VectorHit) ([]retrieval.RelationSeed, map[string][]retrieval.FusionEvidenceReference, error) {
	if ctx == nil {
		return nil, nil, context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	p.calls = append(p.calls, input)
	if input.Scope != p.scope {
		return nil, nil, ErrQueryScopeRequired
	}
	if input.QuestionKind != KnowledgeQuestionPossibleFlow {
		return nil, nil, nil
	}
	for _, hit := range textHits {
		if hit.EvidenceID != p.seedEvidenceID {
			continue
		}
		return []retrieval.RelationSeed{{EvidenceID: p.seedEvidenceID, EntityID: p.anchorEntityID}}, map[string][]retrieval.FusionEvidenceReference{
			p.targetEntityID: {{
				EvidenceID:          p.targetEvidenceID,
				OrganizationID:      p.scope.OrganizationID,
				SourceID:            p.scope.SourceID,
				SnapshotID:          p.scope.SnapshotID,
				EvidenceContentHash: p.targetContentHash,
			}},
		}, nil
	}
	return nil, nil, nil
}

func (s *contextE2ERelationStore) Expand(ctx context.Context, query retrieval.RelationQuery) ([]retrieval.RelationHit, error) {
	if ctx == nil {
		return nil, context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.queries = append(s.queries, query)
	if query.OrganizationID != s.scope.OrganizationID || query.SourceID != s.scope.SourceID || query.SnapshotID != s.scope.SnapshotID {
		return nil, ErrQueryScopeRequired
	}
	anchorMatches := query.AnchorEntityID == s.hit.FromEntityID || query.AnchorEntityID == s.hit.ToEntityID
	if !anchorMatches || query.MaxHops != retrieval.MaxRelationHops {
		return nil, nil
	}
	switch query.Direction {
	case retrieval.RelationDirectionOutbound:
		if query.AnchorEntityID != s.hit.FromEntityID {
			return nil, nil
		}
	case retrieval.RelationDirectionInbound:
		if query.AnchorEntityID != s.hit.ToEntityID {
			return nil, nil
		}
	}
	return []retrieval.RelationHit{s.hit}, nil
}

type contextE2EUnitResolver struct {
	scope Scope
	units map[string]evidence.EvidenceUnit
}

func (r contextE2EUnitResolver) Resolve(ctx context.Context, scope Scope, evidenceID string) (evidence.EvidenceUnit, error) {
	if ctx == nil {
		return evidence.EvidenceUnit{}, context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return evidence.EvidenceUnit{}, err
	}
	if scope != r.scope {
		return evidence.EvidenceUnit{}, ErrQueryScopeRequired
	}
	unit, ok := r.units[evidenceID]
	if !ok {
		return evidence.EvidenceUnit{}, ErrEvidenceUnitNotFound
	}
	return unit, nil
}

func TestContextE2EThreeFamiliesAndFourIntentions(t *testing.T) {
	families := contextE2EFamilies(t)
	for _, family := range families {
		family := family
		t.Run(family.name, func(t *testing.T) {
			assertContextE2EFamily(t, family)
			for _, intent := range contextE2EIntents(family) {
				intent := intent
				t.Run(intent.name, func(t *testing.T) {
					request := contextE2ERequest(family, intent, contextE2ELimits())
					if err := request.Validate(); err != nil {
						t.Fatalf("ContextRequest.Validate() error: %v", err)
					}

					candidates := contextE2ECandidatesForRequest(t, family, request)
					selectionRequest := ContextSelectionRequest{
						Scope:         family.scope,
						Limits:        request.Limits,
						Candidates:    candidates,
						Configuration: DefaultContextUtilityConfiguration(),
					}
					selection, err := SelectContext(context.Background(), selectionRequest)
					if err != nil {
						t.Fatalf("SelectContext() error: %v", err)
					}
					if err := selection.ValidateAgainst(selectionRequest); err != nil {
						t.Fatalf("selection.ValidateAgainst() error: %v", err)
					}
					assertContextE2ESelection(t, family, intent, selection, candidates)

					closureRequest := ContextSupportClosureRequest{
						Request:   selectionRequest,
						Base:      selection,
						Relations: contextE2ERelationCandidates(t, family, intent),
					}
					closure, err := CloseContextSupport(context.Background(), closureRequest)
					if err != nil {
						t.Fatalf("CloseContextSupport() error: %v", err)
					}
					if err := closure.ValidateAgainst(closureRequest); err != nil {
						t.Fatalf("closure.ValidateAgainst() error: %v", err)
					}
					wantRelations := 0
					if intent.kind == IntentKindPossibleImpact {
						wantRelations = 1
					}
					if len(closure.Relations) != wantRelations {
						t.Fatalf("closed relations = %d, want %d", len(closure.Relations), wantRelations)
					}

					policy := contextE2EPolicy(t, family, closure.Selection.Items, closure.Relations)
					if err := policy.ValidateAgainst(contextE2EPolicyRequest(t, family, closure.Selection.Items, closure.Relations)); err != nil {
						t.Fatalf("policy.ValidateAgainst() error: %v", err)
					}
					packageContext := contextE2EPackage(t, family, request, closure, policy)
					if err := packageContext.Validate(); err != nil {
						t.Fatalf("ContextPackage.Validate() error: %v", err)
					}
					projection, err := ProjectContextPackage(context.Background(), packageContext)
					if err != nil {
						t.Fatalf("ProjectContextPackage() error: %v", err)
					}
					if err := projection.ValidateAgainst(packageContext); err != nil {
						t.Fatalf("projection.ValidateAgainst() error: %v", err)
					}

					assertContextE2EPackage(t, family, intent, packageContext, projection)
				})
			}
		})
	}
}

func TestContextE2ENegativeRetrievalBoundaries(t *testing.T) {
	for _, family := range contextE2EFamilies(t) {
		family := family
		t.Run(family.name, func(t *testing.T) {
			normal := contextE2ERequest(family, contextE2EIntents(family)[1], contextE2ELimits())
			matched := contextE2ECandidatesForRequest(t, family, normal)
			if len(matched) == 0 {
				t.Fatal("matched symbol target produced no candidates")
			}

			wrongTarget := normal
			wrongTarget.Intent.Target.ID = "symbol-not-in-corpus"
			wrong := contextE2ECandidatesForRequest(t, family, wrongTarget)
			contextE2EAssertLastRetrievalEmpty(t, family, "wrong symbol target")
			if sameStringSet(contextE2ECandidateIDs(matched), contextE2ECandidateIDs(wrong)) {
				t.Fatalf("different target recovered same candidate set: %#v", contextE2ECandidateIDs(wrong))
			}
			if len(wrong) != 0 {
				t.Fatalf("different target recovered candidates: %#v", contextE2ECandidateIDs(wrong))
			}

			irrelevant := contextE2ERequest(family, contextE2EQuestionIntent(family), contextE2ELimits())
			irrelevant.Intent.Question = "unrelated question about a galaxy outside this corpus"
			if candidates := contextE2ECandidatesForRequest(t, family, irrelevant); len(candidates) != 0 {
				t.Fatalf("question without corpus terms recovered candidates: %#v", contextE2ECandidateIDs(candidates))
			}
			contextE2EAssertLastRetrievalEmpty(t, family, "irrelevant question")
		})
	}
}

func contextE2EAssertLastRetrievalEmpty(t *testing.T, family contextE2EFamily, label string) {
	t.Helper()
	if family.retrieval == nil || len(family.retrieval.results) == 0 {
		t.Fatalf("%s %s did not execute retrieval", family.name, label)
	}
	result := family.retrieval.results[len(family.retrieval.results)-1]
	if len(result.Fusion.Candidates) != 0 || len(result.Candidates) != 0 {
		t.Fatalf("%s %s raw retrieval was not empty: fusion=%#v package=%#v", family.name, label, result.Fusion.Candidates, result.Candidates)
	}
}

func TestContextE2EProtectedWSO2PolicyDoesNotCrossBoundary(t *testing.T) {
	var family contextE2EFamily
	found := false
	for _, candidate := range contextE2EFamilies(t) {
		if candidate.manifest.ID == "wso2" {
			family = candidate
			found = true
			break
		}
	}
	if !found {
		t.Fatal("WSO2 family not found")
	}
	var protected evidence.EvidenceUnit
	for _, unit := range family.evidence {
		if unit.ContentState != evidence.ContentStatePresent || unit.Classification != evidence.ClassificationSafeText || unit.ExternalTransfer != evidence.DecisionAllow {
			protected = unit
			break
		}
	}
	if protected.ID == "" {
		t.Fatal("WSO2 fixture produced no protected evidence unit")
	}
	item := contextE2EContextItem(
		ContextItemEvidence,
		protected.ID,
		nil,
		nil,
		&protected,
		family.scope,
		family.producer,
		[]fact.EvidenceRef{{ID: protected.ID, Locator: protected.Locator}},
		protected.Locator,
		nil,
	)
	if err := item.Validate(); err != nil {
		t.Fatalf("protected WSO2 item invalid: %v", err)
	}
	request := contextE2EPolicyRequest(t, family, []ContextItem{item}, nil)
	result, err := ApplyContextPolicy(context.Background(), request)
	if err != nil {
		t.Fatalf("ApplyContextPolicy(protected WSO2) error: %v", err)
	}
	if err := result.ValidateAgainst(request); err != nil {
		t.Fatalf("protected WSO2 policy validation error: %v", err)
	}
	if !result.PolicyFiltered || len(result.Items) != 0 || len(result.Relations) != 0 || len(result.ContinuationIDs) != 0 {
		t.Fatalf("protected WSO2 crossed policy boundary: %#v", result)
	}
	if len(result.ItemAudit) != 1 || result.ItemAudit[0].Included || result.ItemAudit[0].Reason == ContextPolicyItemIncluded || result.ItemAudit[0].OutputID != "" || result.ItemAudit[0].Redacted {
		t.Fatalf("protected WSO2 audit = %#v", result.ItemAudit)
	}
	if len(result.Degradations) != 1 || result.Degradations[0].Code != ContextDegradationPolicyFiltered {
		t.Fatalf("protected WSO2 degradations = %#v", result.Degradations)
	}

	intent := Intent{Version: ContextVersion, Kind: IntentKindEvidenceInspection, Target: IntentTarget{Kind: IntentTargetEvidence, ID: protected.ID}}
	packageContext := ContextPackage{
		Version:      ContextVersion,
		Revision:     "revision-wso2-protected",
		Scope:        family.scope,
		Intent:       intent,
		Limits:       contextE2ELimits(),
		Coverage:     append([]contract.Coverage(nil), family.coverage...),
		Gaps:         append([]contract.Gap(nil), family.gaps...),
		Degradations: append([]ContextDegradation(nil), result.Degradations...),
	}
	var finalizeBinding = ContextPackageIdentityBinding{
		PolicyDigest:          result.PolicyDigest,
		PolicyContinuationIDs: result.ContinuationIDs,
		PolicyFiltered:        result.PolicyFiltered,
	}
	var finalizeErr error
	packageContext, finalizeErr = FinalizeContextPackage(context.Background(), packageContext, finalizeBinding)
	if finalizeErr != nil {
		t.Fatalf("FinalizeContextPackage(protected WSO2) error: %v", finalizeErr)
	}
	projection, err := ProjectContextPackage(context.Background(), packageContext)
	if err != nil {
		t.Fatalf("ProjectContextPackage(protected WSO2) error: %v", err)
	}
	if err := projection.ValidateAgainst(packageContext); err != nil {
		t.Fatalf("protected WSO2 projection validation error: %v", err)
	}

	codec, err := NewContextContinuationCodec([]byte("context-e2e-protected-wso2-key-32-bytes"))
	if err != nil {
		t.Fatalf("NewContextContinuationCodec(protected WSO2) error: %v", err)
	}
	binding := ContextContinuationBinding{
		Scope:            family.scope,
		SnapshotRevision: packageContext.Revision,
		IntentDigest:     contextE2EIntentDigest(t, intent),
		PolicyDigest:     result.PolicyDigest,
		AlgorithmVersion: ContextSelectionAlgorithm,
		Ordering:         "item-id-ascending",
	}
	page, err := codec.PageIDs(context.Background(), binding, result.ContinuationIDs, 1, nil)
	if err != nil {
		t.Fatalf("protected WSO2 cursor page error: %v", err)
	}
	if len(page.IDs) != 0 || page.Continuation != nil {
		t.Fatalf("protected WSO2 cursor exposed IDs: %#v", page)
	}

	for _, value := range []any{result, packageContext, projection, page, result.ItemAudit} {
		encoded, marshalErr := json.Marshal(value)
		if marshalErr != nil {
			t.Fatalf("protected WSO2 boundary JSON: %v", marshalErr)
		}
		serialized := string(encoded)
		for _, forbidden := range []string{"user:pass", "secret-value", "tenant=fixture"} {
			if strings.Contains(serialized, forbidden) {
				t.Fatalf("protected WSO2 boundary leaked %q: %s", forbidden, serialized)
			}
		}
		if protected.Content != "" && protected.Content != evidence.RedactedContent && strings.Contains(serialized, protected.Content) {
			t.Fatalf("protected WSO2 boundary leaked evidence content: %s", serialized)
		}
	}
}

func TestContextE2EEnforcesAllBudgetsAndStableContinuation(t *testing.T) {
	families := contextE2EFamilies(t)
	budgetCases := []struct {
		name   string
		mutate func(*ContextLimits)
	}{
		{name: "tokens", mutate: func(limits *ContextLimits) { limits.MaxTokens = 1 }},
		{name: "characters", mutate: func(limits *ContextLimits) { limits.MaxCharacters = 1 }},
		{name: "bytes", mutate: func(limits *ContextLimits) { limits.MaxBytes = 1 }},
		{name: "items", mutate: func(limits *ContextLimits) { limits.MaxItems = 1 }},
	}

	for _, family := range families {
		family := family
		t.Run(family.name, func(t *testing.T) {
			for _, budgetCase := range budgetCases {
				budgetCase := budgetCase
				t.Run(budgetCase.name, func(t *testing.T) {
					limits := contextE2ELimits()
					budgetCase.mutate(&limits)
					contextRequest := contextE2ERequest(family, contextE2EQuestionIntent(family), limits)
					request := ContextSelectionRequest{
						Scope:         family.scope,
						Limits:        limits,
						Candidates:    contextE2ECandidatesForRequest(t, family, contextRequest),
						Configuration: DefaultContextUtilityConfiguration(),
					}
					result, err := SelectContext(context.Background(), request)
					if err != nil {
						t.Fatalf("SelectContext() error: %v", err)
					}
					if err := result.ValidateAgainst(request); err != nil {
						t.Fatalf("result.ValidateAgainst() error: %v", err)
					}
					if result.TokenEstimate > limits.MaxTokens || result.CharactersUsed > limits.MaxCharacters || result.BytesUsed > limits.MaxBytes || result.ItemCount > limits.MaxItems {
						t.Fatalf("selection exceeded limits: items=%d tokens=%d chars=%d bytes=%d limits=%#v", result.ItemCount, result.TokenEstimate, result.CharactersUsed, result.BytesUsed, limits)
					}
					if !contextE2EHasBudgetAudit(result) {
						t.Fatalf("selection audit has no budget exclusion: %#v", result.Audit)
					}
				})
			}

			intent := contextE2EQuestionIntent(family)
			limits := contextE2ELimits()
			contextRequest := contextE2ERequest(family, intent, limits)
			selectionRequest := ContextSelectionRequest{
				Scope:         family.scope,
				Limits:        limits,
				Candidates:    contextE2ECandidatesForRequest(t, family, contextRequest),
				Configuration: DefaultContextUtilityConfiguration(),
			}
			selection, err := SelectContext(context.Background(), selectionRequest)
			if err != nil {
				t.Fatalf("SelectContext(continuation) error: %v", err)
			}
			if err := selection.ValidateAgainst(selectionRequest); err != nil {
				t.Fatalf("continuation selection validation error: %v", err)
			}
			if len(selection.Items) < 2 {
				t.Fatalf("continuation needs at least two eligible items, got %d", len(selection.Items))
			}
			policyRequest := contextE2EPolicyRequest(t, family, selection.Items, nil)
			policy, err := ApplyContextPolicy(context.Background(), policyRequest)
			if err != nil {
				t.Fatalf("ApplyContextPolicy(continuation) error: %v", err)
			}
			if err := policy.ValidateAgainst(policyRequest); err != nil {
				t.Fatalf("continuation policy validation error: %v", err)
			}
			sequence := append([]string(nil), policy.ContinuationIDs...)
			sort.Strings(sequence)
			if len(sequence) != len(selection.Items) || !sameStringSet(sequence, contextE2EItemIDs(policy.Items)) {
				t.Fatalf("eligible sequence = %#v, policy IDs = %#v", sequence, policy.ContinuationIDs)
			}
			requestIntent := contextE2ERequest(family, intent, limits).Intent
			policyValue := family.policy
			policyDigest, err := ContextPolicyDigest(&policyValue, contextE2EAuthorizations(policy.Items))
			if err != nil {
				t.Fatalf("ContextPolicyDigest() error: %v", err)
			}
			if policy.PolicyDigest != policyDigest {
				t.Fatalf("continuation policy digest = %q, recomputed = %q", policy.PolicyDigest, policyDigest)
			}
			binding := ContextContinuationBinding{
				Scope:            family.scope,
				SnapshotRevision: "revision-" + family.name,
				IntentDigest:     contextE2EIntentDigest(t, requestIntent),
				PolicyDigest:     policyDigest,
				AlgorithmVersion: ContextSelectionAlgorithm,
				Ordering:         "item-id-ascending",
			}
			codec, err := NewContextContinuationCodec([]byte("context-e2e-continuation-key-32-bytes"))
			if err != nil {
				t.Fatalf("NewContextContinuationCodec() error: %v", err)
			}
			firstPage, err := codec.PageIDs(context.Background(), binding, sequence, 1, nil)
			if err != nil {
				t.Fatalf("PageIDs(first) error: %v", err)
			}
			if len(firstPage.IDs) != 1 || firstPage.Continuation == nil {
				t.Fatalf("first continuation page = %#v, want one ID and continuation", firstPage)
			}
			secondPage, err := codec.PageIDs(context.Background(), binding, sequence, 1, firstPage.Continuation)
			if err != nil {
				t.Fatalf("PageIDs(second) error: %v", err)
			}
			if len(secondPage.IDs) != 1 || secondPage.IDs[0] == firstPage.IDs[0] {
				t.Fatalf("continuation repeated ID: first=%#v second=%#v", firstPage.IDs, secondPage.IDs)
			}
			if !containsString(sequence, firstPage.IDs[0]) || !containsString(sequence, secondPage.IDs[0]) {
				t.Fatalf("continuation returned unauthorized ID: first=%#v second=%#v eligible=%#v", firstPage.IDs, secondPage.IDs, sequence)
			}
			if firstPage.IDs[0] == secondPage.IDs[0] {
				t.Fatal("continuation pages overlap")
			}
			if _, err := codec.Resume(context.Background(), *firstPage.Continuation, binding, sequence); err != nil {
				t.Fatalf("Resume() error: %v", err)
			}
			incompatible := binding
			incompatible.SnapshotRevision = "revision-other"
			if _, err := codec.Resume(context.Background(), *firstPage.Continuation, incompatible, sequence); err == nil {
				t.Fatal("Resume() accepted continuation for another snapshot")
			}
			incompatible = binding
			incompatible.PolicyDigest = contextE2EDigest("policy-other-" + family.name)
			if _, err := codec.Resume(context.Background(), *firstPage.Continuation, incompatible, sequence); err == nil {
				t.Fatal("Resume() accepted continuation with another policy digest")
			}
			expanded := append(append([]string(nil), sequence...), "unauthorized-"+family.name)
			sort.Strings(expanded)
			if _, err := codec.Resume(context.Background(), *firstPage.Continuation, binding, expanded); err == nil {
				t.Fatal("Resume() accepted sequence with unauthorized expansion")
			}
		})
	}
}

func TestContextE2ETruncatedPackageCarriesContinuation(t *testing.T) {
	for _, family := range contextE2EFamilies(t) {
		family := family
		t.Run(family.name, func(t *testing.T) {
			intent := contextE2EQuestionIntent(family)
			fullLimits := contextE2ELimits()
			fullRequest := contextE2ERequest(family, intent, fullLimits)
			fullSelectionRequest := ContextSelectionRequest{
				Scope:         family.scope,
				Limits:        fullLimits,
				Candidates:    contextE2ECandidatesForRequest(t, family, fullRequest),
				Configuration: DefaultContextUtilityConfiguration(),
			}
			fullSelection, err := SelectContext(context.Background(), fullSelectionRequest)
			if err != nil {
				t.Fatalf("SelectContext(full) error: %v", err)
			}
			if err := fullSelection.ValidateAgainst(fullSelectionRequest); err != nil {
				t.Fatalf("full selection validation error: %v", err)
			}
			if len(fullSelection.Items) < 2 {
				t.Fatalf("full selection has %d items, want at least two eligible items", len(fullSelection.Items))
			}
			fullPolicyRequest := contextE2EPolicyRequest(t, family, fullSelection.Items, nil)
			fullPolicy, err := ApplyContextPolicy(context.Background(), fullPolicyRequest)
			if err != nil {
				t.Fatalf("ApplyContextPolicy(full) error: %v", err)
			}
			if err := fullPolicy.ValidateAgainst(fullPolicyRequest); err != nil {
				t.Fatalf("full policy validation error: %v", err)
			}
			pageLimits := fullLimits
			pageLimits.MaxItems = 1
			pageSelectionRequest := ContextSelectionRequest{
				Scope:         family.scope,
				Limits:        pageLimits,
				Candidates:    contextE2EPageCandidates(t, family, fullRequest),
				Configuration: DefaultContextUtilityConfiguration(),
			}
			pageSelection, err := SelectContext(context.Background(), pageSelectionRequest)
			if err != nil {
				t.Fatalf("SelectContext(page) error: %v", err)
			}
			if err := pageSelection.ValidateAgainst(pageSelectionRequest); err != nil {
				t.Fatalf("page selection validation error: %v", err)
			}
			if len(pageSelection.Items) != 1 || pageSelection.Items[0].ID != family.evidenceID {
				t.Fatalf("page selection = %#v, want standalone evidence item %q", pageSelection.Items, family.evidenceID)
			}
			sequence := contextE2ESequenceStarting(fullPolicy.ContinuationIDs, pageSelection.Items[0].ID)
			if len(sequence) != len(fullSelection.Items) || !sameStringSet(sequence, contextE2EItemIDs(fullPolicy.Items)) {
				t.Fatalf("full eligible sequence = %#v, policy IDs = %#v", sequence, fullPolicy.ContinuationIDs)
			}
			closure, err := CloseContextSupport(context.Background(), ContextSupportClosureRequest{
				Request:   pageSelectionRequest,
				Base:      pageSelection,
				Relations: nil,
			})
			if err != nil {
				t.Fatalf("CloseContextSupport(page) error: %v", err)
			}
			if err := closure.ValidateAgainst(ContextSupportClosureRequest{Request: pageSelectionRequest, Base: pageSelection, Relations: nil}); err != nil {
				t.Fatalf("page closure validation error: %v", err)
			}
			pagePolicyRequest := contextE2EPolicyRequest(t, family, closure.Selection.Items, nil)
			pagePolicy, err := ApplyContextPolicy(context.Background(), pagePolicyRequest)
			if err != nil {
				t.Fatalf("ApplyContextPolicy(page) error: %v", err)
			}
			if err := pagePolicy.ValidateAgainst(pagePolicyRequest); err != nil {
				t.Fatalf("page policy validation error: %v", err)
			}

			policyValue := family.policy
			policyDigest, err := ContextPolicyDigest(&policyValue, contextE2EAuthorizations(fullPolicy.Items))
			if err != nil {
				t.Fatalf("ContextPolicyDigest() error: %v", err)
			}
			if fullPolicy.PolicyDigest != policyDigest {
				t.Fatalf("full continuation policy digest = %q, recomputed = %q", fullPolicy.PolicyDigest, policyDigest)
			}
			binding := ContextContinuationBinding{
				Scope:            family.scope,
				SnapshotRevision: "revision-" + family.name,
				IntentDigest:     contextE2EIntentDigest(t, fullRequest.Intent),
				PolicyDigest:     policyDigest,
				AlgorithmVersion: ContextSelectionAlgorithm,
				Ordering:         "selection-order-v1",
			}
			codec, err := NewContextContinuationCodec([]byte("context-e2e-continuation-key-32-bytes"))
			if err != nil {
				t.Fatalf("NewContextContinuationCodec() error: %v", err)
			}
			page, err := codec.PageIDs(context.Background(), binding, sequence, 1, nil)
			if err != nil {
				t.Fatalf("PageIDs() error: %v", err)
			}
			if len(page.IDs) != 1 || page.Continuation == nil {
				t.Fatalf("first page = %#v, want one ID and continuation", page)
			}
			if page.IDs[0] != pagePolicy.Items[0].ID || !containsString(sequence, page.IDs[0]) {
				t.Fatalf("page IDs = %#v, page policy items = %#v, eligible = %#v", page.IDs, contextE2EItemIDs(pagePolicy.Items), sequence)
			}
			secondPage, err := codec.PageIDs(context.Background(), binding, sequence, 1, page.Continuation)
			if err != nil {
				t.Fatalf("PageIDs(second) error: %v", err)
			}
			if len(secondPage.IDs) != 1 || secondPage.IDs[0] == page.IDs[0] || !containsString(sequence, secondPage.IDs[0]) {
				t.Fatalf("second page = %#v, first=%#v, eligible=%#v", secondPage, page, sequence)
			}

			packageContext := ContextPackage{
				Version:        ContextVersion,
				Revision:       binding.SnapshotRevision,
				Scope:          family.scope,
				Intent:         fullRequest.Intent,
				Limits:         pageLimits,
				Items:          pagePolicy.Items,
				Relations:      pagePolicy.Relations,
				Coverage:       append([]contract.Coverage(nil), family.coverage...),
				Gaps:           append([]contract.Gap(nil), family.gaps...),
				Degradations:   []ContextDegradation{{Code: ContextDegradationBudgetExhausted}},
				Audit:          closure.Selection.Audit,
				TokenEstimate:  closure.TokenEstimate,
				CharactersUsed: closure.CharactersUsed,
				BytesUsed:      closure.BytesUsed,
				Truncated:      true,
				Continuation:   page.Continuation,
			}
			packageContext, err = FinalizeContextPackage(context.Background(), packageContext, ContextPackageIdentityBinding{
				PolicyDigest:          pagePolicy.PolicyDigest,
				PolicyContinuationIDs: pagePolicy.ContinuationIDs,
				PolicyFiltered:        pagePolicy.PolicyFiltered,
			})
			if err != nil {
				t.Fatalf("FinalizeContextPackage(truncated) error: %v", err)
			}
			projection, err := ProjectContextPackage(context.Background(), packageContext)
			if err != nil {
				t.Fatalf("ProjectContextPackage() error: %v", err)
			}
			if err := projection.ValidateAgainst(packageContext); err != nil {
				t.Fatalf("truncated projection.ValidateAgainst() error: %v", err)
			}
			if !packageContext.Truncated || packageContext.Continuation == nil {
				t.Fatalf("package lost truncation state: %#v", packageContext)
			}
			if _, err := codec.Resume(context.Background(), *packageContext.Continuation, binding, sequence); err != nil {
				t.Fatalf("package continuation cannot resume: %v", err)
			}
			if len(packageContext.Items) > pageLimits.MaxItems || packageContext.TokenEstimate > pageLimits.MaxTokens || packageContext.CharactersUsed > pageLimits.MaxCharacters || packageContext.BytesUsed > pageLimits.MaxBytes {
				t.Fatalf("truncated package exceeded limits: %#v", packageContext)
			}
		})
	}
}

func contextE2EFamilies(t *testing.T) []contextE2EFamily {
	t.Helper()
	root, err := repoRootForContextE2E()
	if err != nil {
		t.Fatalf("repository root: %v", err)
	}
	configs := []contextE2EAnalyzer{
		{
			analyzer:       javaanalyzer.New(),
			manifest:       javaanalyzer.Manifest,
			registrations:  javaanalyzer.NormalizerRegistrations,
			files:          []contextE2EFile{{sourcePath: filepath.Join(root, "internal/analyzer/java/testdata/quarkus3/BookingResource.java"), path: "BookingResource.java"}},
			policy:         contextE2ETransferPolicy(),
			organizationID: contextE2EUUID(101),
			sourceID:       contextE2EUUID(102),
		},
		{
			analyzer:       wso2analyzer.New(),
			manifest:       wso2analyzer.Manifest,
			registrations:  wso2analyzer.NormalizerRegistrations,
			files:          []contextE2EFile{{sourcePath: filepath.Join(root, "internal/analyzer/wso2/testdata/api-v1.xml"), path: "api-v1.xml"}},
			policy:         contextE2ETransferPolicy(),
			organizationID: contextE2EUUID(201),
			sourceID:       contextE2EUUID(202),
		},
		{
			analyzer:      pythonanalyzer.New(),
			manifest:      pythonanalyzer.Manifest,
			registrations: pythonanalyzer.NormalizerRegistrations,
			files: []contextE2EFile{
				{sourcePath: filepath.Join(root, "internal/analyzer/python/testdata/frappe17/doctype.py"), path: "doctype.py"},
				{sourcePath: filepath.Join(root, "internal/analyzer/python/testdata/frappe17/hooks.py"), path: "hooks.py"},
			},
			policy:         contextE2ETransferPolicy(),
			organizationID: contextE2EUUID(301),
			sourceID:       contextE2EUUID(302),
		},
	}

	families := make([]contextE2EFamily, 0, len(configs))
	for _, config := range configs {
		families = append(families, buildContextE2EFamily(t, config))
	}
	return families
}

func contextE2ESourceLimits() source.Limits {
	return source.Limits{
		MaxConcurrency:            1,
		MaxExtractionBytes:        1 << 20,
		MaxArchiveMembers:         32,
		MaxArchiveBytes:           1 << 20,
		MaxArchiveMemberBytes:     1 << 20,
		MaxArchiveCompressedBytes: 1 << 20,
		MaxExpansionRatio:         100,
	}
}

func contextE2ETransferPolicy() evidence.Policy {
	return evidence.Policy{
		Installation: evidence.PolicyLayer{
			Persist:          evidence.DecisionAllow,
			ExternalTransfer: evidence.DecisionAllow,
		},
		Classifications: map[evidence.Classification]evidence.PolicyLayer{
			evidence.ClassificationSensitive: {
				Persist:          evidence.DecisionRedact,
				ExternalTransfer: evidence.DecisionDeny,
			},
			evidence.ClassificationPromptInjection: {
				Persist:          evidence.DecisionRedact,
				ExternalTransfer: evidence.DecisionDeny,
			},
			evidence.ClassificationBinary: {
				Persist:          evidence.DecisionDeny,
				ExternalTransfer: evidence.DecisionDeny,
			},
			evidence.ClassificationInvalid: {
				Persist:          evidence.DecisionDeny,
				ExternalTransfer: evidence.DecisionDeny,
			},
			evidence.ClassificationProhibited: {
				Persist:          evidence.DecisionDeny,
				ExternalTransfer: evidence.DecisionDeny,
			},
		},
	}
}

type contextE2EQueryBoundary struct {
	scope                  Scope
	contributions          []contract.Contribution
	coverage               []contract.Coverage
	gaps                   []contract.Gap
	evidenceByID           map[string]evidence.EvidenceUnit
	locatorsByID           map[string]contract.Locator
	evidenceByContribution map[string][]evidence.EvidenceUnit
}

// contextE2EProjectQueryBoundary mirrors the canonical query boundary used by
// ingestion and persistence. The analyzers keep their external IDs; query
// scope, artifact IDs, locators, and evidence IDs use the deterministic UUID
// projection that the database adapter returns.
func contextE2EProjectQueryBoundary(
	t *testing.T,
	organizationExternal string,
	manifest contract.Manifest,
	artifacts []contract.Artifact,
	contributions []contract.Contribution,
	coverage []contract.Coverage,
	gaps []contract.Gap,
	units []evidence.EvidenceUnit,
) contextE2EQueryBoundary {
	t.Helper()
	canonicalScope := Scope{
		OrganizationID: identity.CanonicalUUID("organization", organizationExternal),
		SourceID:       identity.CanonicalUUID("source", organizationExternal, manifest.Source.ID),
		SnapshotID:     identity.CanonicalUUID("snapshot", organizationExternal, manifest.Source.ID, manifest.Snapshot.ID),
	}
	if err := canonicalScope.Validate(); err != nil {
		t.Fatalf("canonical query scope %#v: %v", canonicalScope, err)
	}

	artifactIDs := make(map[string]string, len(artifacts))
	artifactsByExternalID := make(map[string]contract.Artifact, len(artifacts))
	for _, artifact := range artifacts {
		if artifact.SourceID != manifest.Source.ID {
			t.Fatalf("artifact %q source = %q, want external source %q", artifact.ID, artifact.SourceID, manifest.Source.ID)
		}
		canonicalID := identity.CanonicalUUID("artifact", organizationExternal, manifest.Source.ID, manifest.Snapshot.ID, artifact.ID)
		if err := validateUUID("canonical artifact id", canonicalID); err != nil {
			t.Fatalf("artifact %q canonical id %q: %v", artifact.ID, canonicalID, err)
		}
		if _, exists := artifactIDs[artifact.ID]; exists {
			t.Fatalf("duplicate external artifact %q", artifact.ID)
		}
		artifactIDs[artifact.ID] = canonicalID
		artifactsByExternalID[artifact.ID] = artifact
	}

	externalContributions := make(map[string]contract.Contribution, len(contributions))
	projectedContributions := make([]contract.Contribution, len(contributions))
	for index, original := range contributions {
		canonicalArtifactID, ok := artifactIDs[original.ArtifactID]
		if !ok {
			t.Fatalf("contribution %q references unknown external artifact %q", original.ID, original.ArtifactID)
		}
		if _, exists := externalContributions[original.ID]; exists {
			t.Fatalf("duplicate external contribution %q", original.ID)
		}
		externalContributions[original.ID] = original

		projected := original
		projected.ArtifactID = canonicalArtifactID
		projected.Locator.SourceID = canonicalScope.SourceID
		projected.Locator.ArtifactID = canonicalArtifactID
		if projected.ID != original.ID || projected.Method != original.Method {
			t.Fatalf("contribution %q identity/method changed at query boundary", original.ID)
		}
		if err := projected.Validate(); err != nil {
			t.Fatalf("projected contribution %q invalid: %v", original.ID, err)
		}
		projectedContributions[index] = projected
	}

	projectedEvidence := make(map[string]evidence.EvidenceUnit, len(units))
	locatorsByID := make(map[string]contract.Locator, len(units))
	evidenceByContribution := make(map[string][]evidence.EvidenceUnit, len(units))
	for _, original := range units {
		if err := original.ValidatePrepared(); err != nil {
			t.Fatalf("external evidence %q ValidatePrepared(): %v", original.ID, err)
		}
		canonicalArtifactID, ok := artifactIDs[original.ArtifactID]
		if !ok {
			t.Fatalf("evidence %q references unknown external artifact %q", original.ID, original.ArtifactID)
		}
		artifact := artifactsByExternalID[original.ArtifactID]
		if original.Locator.ArtifactID != artifact.ID || original.Locator.Path != artifact.Path {
			t.Fatalf("evidence %q locator %#v is not bound to AnalysisResult artifact %#v", original.ID, original.Locator, artifact)
		}
		contribution, ok := externalContributions[original.Contribution.ID]
		if !ok {
			t.Fatalf("evidence %q references unknown external contribution %q", original.ID, original.Contribution.ID)
		}
		if original.Contribution.Method != contribution.Method || original.Contribution.AnalyzerID != contribution.AnalyzerID || original.Contribution.AnalyzerVersion != contribution.AnalyzerVersion {
			t.Fatalf("evidence %q contribution provenance differs from AnalysisResult", original.ID)
		}

		projected := original
		projected.OrganizationID = canonicalScope.OrganizationID
		projected.SourceID = canonicalScope.SourceID
		projected.SnapshotID = canonicalScope.SnapshotID
		projected.ArtifactID = canonicalArtifactID
		projected.Contribution.ArtifactID = canonicalArtifactID
		projected.Locator.SourceID = canonicalScope.SourceID
		projected.Locator.ArtifactID = canonicalArtifactID
		// QueryEvidenceUnitRepository preserves the external observation identity
		// and method while deriving the evidence identity from canonical fields.
		projected.ID = evidence.EvidenceID(projected)
		if projected.Contribution.ID != original.Contribution.ID || projected.Contribution.Method != original.Contribution.Method {
			t.Fatalf("evidence %q contribution ID/method changed at query boundary", original.ID)
		}
		if err := projected.ValidatePrepared(); err != nil {
			t.Fatalf("projected evidence %q ValidatePrepared(): %v", original.ID, err)
		}
		if _, exists := projectedEvidence[projected.ID]; exists {
			t.Fatalf("duplicate canonical evidence %q", projected.ID)
		}
		projectedEvidence[projected.ID] = projected
		locatorsByID[projected.ID] = projected.Locator
		evidenceByContribution[projected.Contribution.ID] = append(evidenceByContribution[projected.Contribution.ID], projected)
	}

	projectedCoverage := contextE2EProjectQueryCoverage(t, coverage, canonicalScope, artifactIDs)
	projectedGaps := contextE2EProjectQueryGaps(t, gaps, canonicalScope, artifactIDs)
	return contextE2EQueryBoundary{
		scope:                  canonicalScope,
		contributions:          projectedContributions,
		coverage:               projectedCoverage,
		gaps:                   projectedGaps,
		evidenceByID:           projectedEvidence,
		locatorsByID:           locatorsByID,
		evidenceByContribution: evidenceByContribution,
	}
}

func contextE2EProjectQueryCoverage(t *testing.T, values []contract.Coverage, scope Scope, artifactIDs map[string]string) []contract.Coverage {
	t.Helper()
	projected := append([]contract.Coverage(nil), values...)
	for index, original := range values {
		if original.Locator == nil {
			continue
		}
		locator := contextE2EProjectQueryLocator(t, *original.Locator, scope, artifactIDs)
		projected[index].Locator = &locator
		if projected[index].ID != original.ID || projected[index].Dimension != original.Dimension || projected[index].Scope != original.Scope || projected[index].State != original.State || projected[index].AnalyzerID != original.AnalyzerID || projected[index].Message != original.Message {
			t.Fatalf("coverage %q content changed at query boundary", original.ID)
		}
	}
	return projected
}

func contextE2EProjectQueryGaps(t *testing.T, values []contract.Gap, scope Scope, artifactIDs map[string]string) []contract.Gap {
	t.Helper()
	projected := append([]contract.Gap(nil), values...)
	for index, original := range values {
		if original.Locator == nil {
			continue
		}
		locator := contextE2EProjectQueryLocator(t, *original.Locator, scope, artifactIDs)
		projected[index].Locator = &locator
		if projected[index].ID != original.ID || projected[index].Code != original.Code || projected[index].Dimension != original.Dimension || projected[index].Scope != original.Scope || projected[index].AnalyzerID != original.AnalyzerID || projected[index].Message != original.Message {
			t.Fatalf("gap %q content changed at query boundary", original.ID)
		}
	}
	return projected
}

func contextE2EProjectQueryLocator(t *testing.T, original contract.Locator, scope Scope, artifactIDs map[string]string) contract.Locator {
	t.Helper()
	projected := original
	projected.SourceID = scope.SourceID
	if original.ArtifactID != "" {
		canonicalArtifactID, ok := artifactIDs[original.ArtifactID]
		if !ok {
			t.Fatalf("locator references unknown external artifact %q", original.ArtifactID)
		}
		projected.ArtifactID = canonicalArtifactID
	}
	if err := projected.Validate(); err != nil {
		t.Fatalf("projected locator %#v invalid: %v", projected, err)
	}
	if err := validateCitationLocator(projected, scope.SourceID, maxContextLocatorBytes); err != nil {
		t.Fatalf("projected locator %#v exceeds query boundary: %v", projected, err)
	}
	return projected
}

func contextE2EContributionEvidenceRefs(t *testing.T, contribution contract.Contribution, candidates []evidence.EvidenceUnit) []fact.EvidenceRef {
	t.Helper()
	ordered := append([]evidence.EvidenceUnit(nil), candidates...)
	sort.SliceStable(ordered, func(left, right int) bool { return ordered[left].ID < ordered[right].ID })
	refs := make([]fact.EvidenceRef, 0, len(ordered))
	for _, unit := range ordered {
		if unit.Contribution.ID != contribution.ID || unit.Contribution.Method != contribution.Method || unit.Contribution.ArtifactID != contribution.ArtifactID {
			t.Fatalf("contribution %q evidence %q provenance differs across AnalysisResult/evidence", contribution.ID, unit.ID)
		}
		refs = append(refs, fact.EvidenceRef{ID: unit.ID, Locator: unit.Locator})
	}
	if len(refs) == 0 {
		t.Fatalf("contribution %q has no compatible evidence units; expected at least one AnalysisResult reference", contribution.ID)
	}
	return refs
}

func contextE2EEvidenceForFact(t *testing.T, candidate fact.CanonicalFact, units map[string]evidence.EvidenceUnit) evidence.EvidenceUnit {
	t.Helper()
	if len(candidate.Evidence) == 0 {
		t.Fatalf("fact %q has no cited evidence target", candidate.ID)
	}
	for _, reference := range candidate.Evidence {
		unit, ok := units[reference.ID]
		if !ok {
			t.Fatalf("fact %q cites unknown evidence target %q", candidate.ID, reference.ID)
		}
		if unit.Locator != reference.Locator {
			t.Fatalf("fact %q cited evidence %q locator differs: fact=%#v evidence=%#v", candidate.ID, reference.ID, reference.Locator, unit.Locator)
		}
		return unit
	}
	t.Fatalf("fact %q cited no resolvable evidence target", candidate.ID)
	return evidence.EvidenceUnit{}
}

func contextE2ENewRetrievalState(
	t *testing.T,
	organizationExternal, sourceExternal, snapshotExternal string,
	scope Scope,
	facts []fact.CanonicalFact,
	units map[string]evidence.EvidenceUnit,
	dependencyFact fact.CanonicalFact,
	dependencyEvidence evidence.EvidenceUnit,
	items ...ContextItem,
) *contextE2ERetrievalState {
	t.Helper()
	evidenceIDsByUnitID := make(map[string]string, len(units))
	resolverUnits := make(map[string]evidence.EvidenceUnit, len(units))
	for unitID, unit := range units {
		canonicalID := identity.CanonicalUUID("evidence", organizationExternal, sourceExternal, snapshotExternal, unitID)
		if err := validateUUID("retrieval evidence id", canonicalID); err != nil {
			t.Fatalf("unit %q retrieval identity %q: %v", unitID, canonicalID, err)
		}
		evidenceIDsByUnitID[unitID] = canonicalID
		resolverUnits[canonicalID] = unit
	}

	factsByEvidenceID := make(map[string][]fact.CanonicalFact)
	for _, candidate := range facts {
		for _, reference := range candidate.Evidence {
			factsByEvidenceID[reference.ID] = append(factsByEvidenceID[reference.ID], candidate)
		}
	}
	documents := make([]contextE2ETextDocument, 0, len(units))
	unitIDs := make([]string, 0, len(units))
	for unitID := range units {
		unitIDs = append(unitIDs, unitID)
	}
	sort.Strings(unitIDs)
	for index, unitID := range unitIDs {
		unit := units[unitID]
		terms := make(map[string]struct{})
		contextE2EAddRetrievalTerms(terms, unit.ID, unit.Contribution.ID, unit.Contribution.ArtifactID, unit.Contribution.Method, unit.Locator.Path, unit.Locator.Member, string(unit.ContentState), unit.Content)
		for _, candidate := range factsByEvidenceID[unitID] {
			contextE2EAddRetrievalTerms(terms, candidate.ID, string(candidate.Predicate), candidate.Subject.ID)
			if candidate.Object != nil {
				contextE2EAddRetrievalTerms(terms, candidate.Object.ID)
			}
		}
		canonicalID := evidenceIDsByUnitID[unitID]
		documents = append(documents, contextE2ETextDocument{hit: retrieval.TextHit{
			EvidenceID:     canonicalID,
			OrganizationID: scope.OrganizationID,
			SourceID:       scope.SourceID,
			SnapshotID:     scope.SnapshotID,
			ProjectionKind: "canonical-evidence",
			ContentState:   unit.ContentState,
			Content:        unit.Content,
			ContentHash:    unit.ContentHash,
			Truncated:      unit.Truncated,
			Classification: unit.Classification,
			Rank:           float64(index + 1),
		}, terms: terms})
	}

	dependencyRetrievalID := evidenceIDsByUnitID[dependencyEvidence.ID]
	if dependencyRetrievalID == "" {
		t.Fatalf("dependency evidence %q has no retrieval identity", dependencyEvidence.ID)
	}
	if dependencyFact.Object == nil {
		t.Fatalf("dependency fact %q has no object for retrieval relation", dependencyFact.ID)
	}
	subjectEntityID := identity.CanonicalUUID("factual-projection-entity", scope.OrganizationID, scope.SourceID, scope.SnapshotID, string(dependencyFact.Subject.Kind), dependencyFact.Subject.ID)
	if err := validateUUID("canonical dependency subject", subjectEntityID); err != nil {
		t.Fatalf("dependency subject %q cannot seed retrieval relation: %v", dependencyFact.Subject.ID, err)
	}
	objectEntityID := identity.CanonicalUUID("factual-projection-entity", scope.OrganizationID, scope.SourceID, scope.SnapshotID, string(dependencyFact.Object.Kind), dependencyFact.Object.ID)
	if err := validateUUID("canonical dependency object", objectEntityID); err != nil {
		t.Fatalf("dependency object %q cannot close retrieval relation: %v", dependencyFact.Object.ID, err)
	}
	relationID := identity.CanonicalUUID("relation", organizationExternal, sourceExternal, snapshotExternal, dependencyFact.ID)
	relationHit := retrieval.RelationHit{
		RelationID:         relationID,
		OrganizationID:     scope.OrganizationID,
		SourceID:           scope.SourceID,
		SnapshotID:         scope.SnapshotID,
		RelationExternalID: dependencyFact.ID,
		FromEntityID:       subjectEntityID,
		ToEntityID:         objectEntityID,
		RelationType:       string(dependencyFact.Predicate),
		Attributes:         json.RawMessage(`{"predicate":"dependency"}`),
		Hops:               1,
		Provenance: retrieval.RelationProvenance{
			OrganizationID:     scope.OrganizationID,
			SourceID:           scope.SourceID,
			SnapshotID:         scope.SnapshotID,
			RelationID:         relationID,
			RelationExternalID: dependencyFact.ID,
			FromEntityID:       subjectEntityID,
			ToEntityID:         objectEntityID,
		},
	}
	textSearcher := &contextE2ETextSearcher{scope: scope, docs: documents}
	relationInputProvider := &contextE2ERelationInputProvider{
		scope:             scope,
		seedEvidenceID:    dependencyRetrievalID,
		anchorEntityID:    subjectEntityID,
		targetEntityID:    objectEntityID,
		targetEvidenceID:  dependencyRetrievalID,
		targetContentHash: dependencyEvidence.ContentHash,
	}
	relationStore := &contextE2ERelationStore{scope: scope, hit: relationHit}
	itemIDsByEvidenceID := make(map[string][]string, len(dependencyFact.Evidence))
	for _, reference := range dependencyFact.Evidence {
		if _, ok := units[reference.ID]; !ok {
			t.Fatalf("dependency fact %q cited evidence %q missing from canonical query units", dependencyFact.ID, reference.ID)
		}
		retrievalID := evidenceIDsByUnitID[reference.ID]
		if retrievalID == "" {
			t.Fatalf("dependency fact %q cited evidence %q without retrieval identity", dependencyFact.ID, reference.ID)
		}
		itemIDsByEvidenceID[retrievalID] = make([]string, 0, len(items))
		for _, item := range items {
			itemIDsByEvidenceID[retrievalID] = append(itemIDsByEvidenceID[retrievalID], item.ID)
		}
	}
	retriever := &HybridRetriever{
		Text:           textSearcher,
		Relations:      relationStore,
		RelationInputs: relationInputProvider,
		UnitResolver:   contextE2EUnitResolver{scope: scope, units: resolverUnits},
		Support:        ConservativeSupportAssessor{},
		Fusion: retrieval.FusionConfiguration{
			ExactWeight: 1, TextualWeight: 1, RelationWeight: 1, MaxCandidates: 16,
		},
		Limit: 16,
	}
	return &contextE2ERetrievalState{
		retriever:           retriever,
		text:                textSearcher,
		relationInputs:      relationInputProvider,
		relations:           relationStore,
		subjectEntityID:     subjectEntityID,
		objectEntityID:      objectEntityID,
		evidenceIDsByUnitID: evidenceIDsByUnitID,
		itemIDsByEvidenceID: itemIDsByEvidenceID,
	}
}

func contextE2EAddRetrievalTerms(terms map[string]struct{}, values ...string) {
	for _, value := range values {
		for _, term := range strings.Fields(strings.ToLower(value)) {
			if term != "" {
				terms[term] = struct{}{}
			}
		}
	}
}

func buildContextE2EFamily(t *testing.T, config contextE2EAnalyzer) contextE2EFamily {
	t.Helper()
	manifest := config.manifest()
	if err := manifest.Validate(); err != nil {
		t.Fatalf("%s manifest validation: %v", manifest.ID, err)
	}
	registrations, err := config.registrations(manifest)
	if err != nil {
		t.Fatalf("%s normalizer registrations: %v", manifest.ID, err)
	}
	registry, err := normalization.NewRegistry(registrations...)
	if err != nil {
		t.Fatalf("%s normalization registry: %v", manifest.ID, err)
	}

	rootPath := t.TempDir()
	for _, fixture := range config.files {
		content, err := os.ReadFile(fixture.sourcePath)
		if err != nil {
			t.Fatalf("%s read fixture %q: %v", manifest.ID, fixture.sourcePath, err)
		}
		if err := os.WriteFile(filepath.Join(rootPath, fixture.path), content, 0o600); err != nil {
			t.Fatalf("%s stage fixture %q: %v", manifest.ID, fixture.path, err)
		}
	}

	analysisRegistry, err := analysis.NewRegistry(config.analyzer)
	if err != nil {
		t.Fatalf("%s analysis registry: %v", manifest.ID, err)
	}
	runner, err := analysis.NewRunner(analysisRegistry)
	if err != nil {
		t.Fatalf("%s analysis runner: %v", manifest.ID, err)
	}
	analysisResult, err := runner.RunWithEvidence(context.Background(), analysis.Config{
		Source:         contract.Source{ID: config.sourceID, Name: manifest.ID, Type: "filesystem", Root: rootPath},
		Root:           rootPath,
		OrganizationID: config.organizationID,
		Limits:         contextE2ESourceLimits(),
		RunID:          "context-e2e-" + manifest.ID,
		ToolVersion:    "context-e2e",
	}, analysis.EvidenceConfig{
		OrganizationID: config.organizationID,
		Limits: analysis.EvidenceLimits{
			MaxUnitsPerArtifact:  64,
			MaxBytesPerUnit:      analysis.DefaultEvidenceMaxBytesPerUnit,
			MaxCharactersPerUnit: analysis.DefaultEvidenceMaxCharactersPerUnit,
		},
		Policy: config.policy,
	})
	if err != nil {
		t.Fatalf("%s RunWithEvidence(): %v", manifest.ID, err)
	}
	if err := analysisResult.Validate(); err != nil {
		t.Fatalf("%s AnalysisResult.Validate(): %v", manifest.ID, err)
	}
	if err := analysisResult.Result.Validate(); err != nil {
		t.Fatalf("%s contract.Result.Validate(): %v", manifest.ID, err)
	}

	resultManifest := analysisResult.Result.Manifest
	boundary := contextE2EProjectQueryBoundary(t, config.organizationID, resultManifest, analysisResult.Result.Artifacts, analysisResult.Result.Contributions, resultManifest.Coverage, resultManifest.Gaps, analysisResult.Evidence)
	scope := boundary.scope
	factScope := fact.Scope{OrganizationID: scope.OrganizationID, SourceID: scope.SourceID, SnapshotID: scope.SnapshotID}
	allInputs := make([]normalization.Input, 0, len(analysisResult.Result.Contributions))
	allCoverage := boundary.coverage
	allGaps := boundary.gaps
	unitsByID := boundary.evidenceByID
	locatorsByID := boundary.locatorsByID
	for _, contribution := range boundary.contributions {
		evidenceRefs := contextE2EContributionEvidenceRefs(t, contribution, boundary.evidenceByContribution[contribution.ID])
		allInputs = append(allInputs, normalization.Input{
			Scope:        factScope,
			Manifest:     manifest,
			Contribution: contribution,
			Evidence:     evidenceRefs,
		})
	}

	if len(allInputs) == 0 {
		t.Fatalf("%s produced no normalization inputs", manifest.ID)
	}
	normalized, err := registry.NormalizeAll(context.Background(), allInputs)
	if err != nil {
		t.Fatalf("%s NormalizeAll(): %v", manifest.ID, err)
	}
	if len(normalized.Facts) == 0 {
		t.Fatalf("%s normalization produced no facts", manifest.ID)
	}
	for _, candidate := range normalized.Facts {
		if err := candidate.Validate(); err != nil {
			t.Fatalf("%s fact %q invalid: %v", manifest.ID, candidate.ID, err)
		}
		if candidate.Scope != factScope {
			t.Fatalf("%s fact %q scope = %#v, want %#v", manifest.ID, candidate.ID, candidate.Scope, factScope)
		}
		wantProducer := fact.Producer{ID: manifest.ID, Version: manifest.Version, Method: manifest.Method}
		if candidate.Producer != wantProducer {
			t.Fatalf("%s fact %q producer = %#v, want %#v", manifest.ID, candidate.ID, candidate.Producer, wantProducer)
		}
		for _, reference := range candidate.Evidence {
			unit, ok := unitsByID[reference.ID]
			if !ok {
				t.Fatalf("%s fact %q references unknown evidence %q", manifest.ID, candidate.ID, reference.ID)
			}
			if reference.Locator != unit.Locator {
				t.Fatalf("%s fact %q locator differs from evidence %q", manifest.ID, candidate.ID, reference.ID)
			}
		}
	}

	sort.SliceStable(normalized.Facts, func(left, right int) bool { return normalized.Facts[left].ID < normalized.Facts[right].ID })
	dependencyFact, ok := contextE2EDependencyFact(normalized.Facts)
	if !ok {
		t.Fatalf("%s normalized corpus has no dependency fact with subject and object", manifest.ID)
	}
	if len(dependencyFact.Evidence) == 0 {
		t.Fatalf("%s dependency fact %q has no evidence", manifest.ID, dependencyFact.ID)
	}
	dependencyEvidence := contextE2EEvidenceForFact(t, dependencyFact, unitsByID)
	producer := dependencyFact.Producer
	dependencyFactItem := contextE2EContextItem(ContextItemFact, dependencyFact.ID, &dependencyFact, nil, nil, scope, producer, dependencyFact.Evidence, dependencyEvidence.Locator, []string{dependencyEvidence.ID})
	subject := dependencyFact.Subject
	object := *dependencyFact.Object
	if subject.ID == object.ID {
		t.Fatalf("%s dependency fact %q has identical endpoints %q", manifest.ID, dependencyFact.ID, subject.ID)
	}
	subjectItem := contextE2EContextItem(ContextItemEntity, subject.ID, nil, &subject, nil, scope, producer, dependencyFact.Evidence, dependencyEvidence.Locator, []string{dependencyFact.ID})
	objectItem := contextE2EContextItem(ContextItemEntity, object.ID, nil, &object, nil, scope, producer, dependencyFact.Evidence, dependencyEvidence.Locator, []string{dependencyFact.ID})
	evidenceItem := contextE2EContextItem(ContextItemEvidence, dependencyEvidence.ID, nil, nil, &dependencyEvidence, scope, producer, dependencyFact.Evidence, dependencyEvidence.Locator, nil)
	for _, item := range []ContextItem{dependencyFactItem, subjectItem, objectItem, evidenceItem} {
		if err := item.Validate(); err != nil {
			t.Fatalf("%s context item %q invalid: %v", manifest.ID, item.ID, err)
		}
	}
	relation := ContextRelation{
		ID:         contextE2ERelationID(config.organizationID, resultManifest.Source.ID, resultManifest.Snapshot.ID, dependencyFact.ID),
		Predicate:  dependencyFact.Predicate,
		Origin:     ContextKnowledgeObserved,
		Scope:      scope,
		FromID:     subject.ID,
		ToID:       object.ID,
		Path:       []string{subject.ID, object.ID},
		SupportIDs: []string{dependencyFact.ID, dependencyEvidence.ID},
		Provenance: ContextProvenance{
			Producer: &producer,
			Evidence: append([]fact.EvidenceRef(nil), dependencyFact.Evidence...),
		},
	}
	if err := relation.Validate(); err != nil {
		t.Fatalf("%s context relation invalid: %v", manifest.ID, err)
	}
	retrievalState := contextE2ENewRetrievalState(
		t,
		config.organizationID,
		resultManifest.Source.ID,
		resultManifest.Snapshot.ID,
		scope,
		normalized.Facts,
		unitsByID,
		dependencyFact,
		dependencyEvidence,
		dependencyFactItem,
		subjectItem,
		objectItem,
		evidenceItem,
	)

	return contextE2EFamily{
		name:                 manifest.ID,
		scope:                scope,
		manifest:             manifest,
		facts:                normalized.Facts,
		relation:             relation,
		coverage:             allCoverage,
		gaps:                 allGaps,
		evidence:             unitsByID,
		locators:             locatorsByID,
		producer:             producer,
		policy:               config.policy,
		dependencyFact:       dependencyFact,
		dependencyEvidenceID: dependencyEvidence.ID,
		dependencyFactItem:   dependencyFactItem,
		subjectItem:          subjectItem,
		objectItem:           objectItem,
		evidenceItem:         evidenceItem,
		retrieval:            retrievalState,
		subjectID:            subject.ID,
		objectID:             object.ID,
		symbolID:             object.ID,
		impactTargetID:       object.ID,
		evidenceID:           dependencyEvidence.ID,
	}
}

func contextE2EDependencyFact(facts []fact.CanonicalFact) (fact.CanonicalFact, bool) {
	for _, candidate := range facts {
		if candidate.Predicate == fact.PredicateDependency && candidate.Object != nil && candidate.Subject.ID != candidate.Object.ID {
			return candidate, true
		}
	}
	return fact.CanonicalFact{}, false
}

func contextE2EContextItem(kind ContextItemKind, id string, factPayload *fact.CanonicalFact, entity *fact.Participant, evidencePayload *evidence.EvidenceUnit, scope Scope, producer fact.Producer, refs []fact.EvidenceRef, locator contract.Locator, supportIDs []string) ContextItem {
	item := ContextItem{
		ID:         id,
		Kind:       kind,
		Origin:     ContextKnowledgeObserved,
		Scope:      scope,
		Locator:    locator,
		Provenance: ContextProvenance{Producer: &producer, Evidence: append([]fact.EvidenceRef(nil), refs...)},
		SupportIDs: append([]string(nil), supportIDs...),
	}
	switch kind {
	case ContextItemFact:
		item.Fact = factPayload
	case ContextItemEntity:
		item.Entity = entity
	case ContextItemEvidence:
		item.Evidence = evidencePayload
	}
	return item
}

func contextE2ERelationID(organizationExternal, sourceExternal, snapshotExternal, factID string) string {
	return identity.CanonicalUUID("relation", organizationExternal, sourceExternal, snapshotExternal, factID)
}

func contextE2ERequest(family contextE2EFamily, intent contextE2EIntent, limits ContextLimits) ContextRequest {
	requestIntent := Intent{Version: ContextVersion, Kind: intent.kind}
	switch intent.kind {
	case IntentKindQuestion:
		requestIntent.Question = intent.question
	case IntentKindEvidenceInspection:
		requestIntent.Target = IntentTarget{Kind: IntentTargetEvidence, ID: family.evidenceID}
	case IntentKindPossibleImpact:
		requestIntent.Target = IntentTarget{Kind: IntentTargetSymbol, ID: family.impactTargetID}
	default:
		requestIntent.Target = IntentTarget{Kind: intent.targetKind, ID: family.symbolID}
	}
	return ContextRequest{Version: ContextVersion, Scope: family.scope, Intent: requestIntent, Limits: limits}
}

func contextE2ELimits() ContextLimits {
	return ContextLimits{MaxTokens: 256, MaxItems: 4, MaxCharacters: 4_096, MaxBytes: 16_384}
}

func contextE2EIntents(family contextE2EFamily) []contextE2EIntent {
	return []contextE2EIntent{
		contextE2EQuestionIntent(family),
		{name: "symbol context", kind: IntentKindSymbol, targetKind: IntentTargetSymbol},
		{name: "possible impact", kind: IntentKindPossibleImpact, targetKind: IntentTargetSymbol},
		{name: "evidence inspection", kind: IntentKindEvidenceInspection, targetKind: IntentTargetEvidence},
	}
}

func contextE2EQuestionIntent(family contextE2EFamily) contextE2EIntent {
	return contextE2EIntent{
		name:     "question",
		kind:     IntentKindQuestion,
		question: family.dependencyEvidenceID + " " + family.dependencyFact.ID,
	}
}

func contextE2EItemsForIntent(family contextE2EFamily, intent contextE2EIntent) []ContextItem {
	switch intent.kind {
	case IntentKindQuestion:
		return []ContextItem{family.subjectItem, family.objectItem, family.dependencyFactItem, family.evidenceItem}
	case IntentKindSymbol:
		return []ContextItem{family.objectItem, family.dependencyFactItem, family.evidenceItem}
	case IntentKindPossibleImpact:
		return []ContextItem{family.subjectItem, family.objectItem, family.dependencyFactItem, family.evidenceItem}
	case IntentKindEvidenceInspection:
		return []ContextItem{family.evidenceItem, family.dependencyFactItem}
	default:
		return nil
	}
}

func contextE2ERetrievalInput(family contextE2EFamily, request ContextRequest) QueryRetrievalInput {
	question := request.Intent.Question
	kind := KnowledgeQuestionInventory
	switch request.Intent.Kind {
	case IntentKindSymbol:
		question = request.Intent.Target.ID
	case IntentKindPossibleImpact:
		question = request.Intent.Target.ID
		kind = KnowledgeQuestionPossibleFlow
	case IntentKindEvidenceInspection:
		question = request.Intent.Target.ID
	}
	return QueryRetrievalInput{
		ExecutionID:  "context-e2e-" + family.name,
		RequestID:    "context-e2e-" + string(request.Intent.Kind),
		Question:     question,
		QuestionKind: kind,
		Scope:        family.scope,
		Limit:        16,
	}
}

func contextE2ERetrieve(t *testing.T, family contextE2EFamily, request ContextRequest) QueryRetrievalResult {
	t.Helper()
	if family.retrieval == nil || family.retrieval.retriever == nil {
		t.Fatalf("%s has no configured hybrid retriever", family.name)
	}
	input := contextE2ERetrievalInput(family, request)
	result, err := family.retrieval.retriever.Retrieve(context.Background(), input)
	if err != nil {
		t.Fatalf("%s HybridRetriever.Retrieve(%s) error: %v", family.name, request.Intent.Kind, err)
	}
	family.retrieval.inputs = append(family.retrieval.inputs, input)
	family.retrieval.results = append(family.retrieval.results, result)
	if !containsString(result.DegradationReasons, "vector_unavailable") {
		t.Fatalf("%s retrieval %s did not record missing-vector degradation: %#v", family.name, request.Intent.Kind, result.DegradationReasons)
	}
	if result.Support.Kind != input.QuestionKind {
		t.Fatalf("%s retrieval %s support kind = %q, want %q", family.name, request.Intent.Kind, result.Support.Kind, input.QuestionKind)
	}
	if request.Intent.Kind == IntentKindPossibleImpact {
		foundRelation := false
		dependencyEvidenceRetrievalID := family.retrieval.evidenceIDsByUnitID[family.dependencyEvidenceID]
		for _, candidate := range result.Fusion.Candidates {
			for _, signal := range candidate.RelationSignals {
				foundRelation = true
				relation := signal.Relation
				if candidate.EvidenceID != dependencyEvidenceRetrievalID || signal.SeedEvidenceID != dependencyEvidenceRetrievalID || relation.RelationExternalID != family.dependencyFact.ID || relation.RelationType != string(family.dependencyFact.Predicate) || relation.FromEntityID != family.retrieval.subjectEntityID || relation.ToEntityID != family.retrieval.objectEntityID || relation.Hops != retrieval.MaxRelationHops {
					t.Fatalf("%s impact relation signal = %#v, want real dependency path", family.name, relation)
				}
				if relation.OrganizationID != family.scope.OrganizationID || relation.SourceID != family.scope.SourceID || relation.SnapshotID != family.scope.SnapshotID {
					t.Fatalf("%s impact relation signal escaped scope: %#v", family.name, relation)
				}
			}
		}
		if !foundRelation {
			t.Fatalf("%s possible-impact retrieval has no relation signal: %#v", family.name, result.Fusion)
		}
	}
	return result
}

func contextE2ECandidatesForRequest(t *testing.T, family contextE2EFamily, request ContextRequest) []ContextSelectionCandidate {
	t.Helper()
	result := contextE2ERetrieve(t, family, request)
	intent := contextE2EIntentForRequest(family, request)
	expected := contextE2EItemsForIntent(family, intent)
	expectedByID := make(map[string]ContextItem, len(expected))
	for _, item := range expected {
		expectedByID[item.ID] = item
	}
	itemIDsByEvidenceID := family.retrieval.itemIDsByEvidenceID
	unitIDByEvidenceID := make(map[string]string, len(family.retrieval.evidenceIDsByUnitID))
	for unitID, evidenceID := range family.retrieval.evidenceIDsByUnitID {
		unitIDByEvidenceID[evidenceID] = unitID
	}
	candidates := make([]ContextSelectionCandidate, 0, len(expected))
	seenItems := make(map[string]struct{}, len(expected))
	for _, fused := range result.Fusion.Candidates {
		unitID, ok := unitIDByEvidenceID[fused.EvidenceID]
		if !ok {
			t.Fatalf("%s fusion candidate %q has no canonical unit mapping", family.name, fused.EvidenceID)
		}
		allowedIDs := itemIDsByEvidenceID[fused.EvidenceID]
		if len(allowedIDs) == 0 {
			t.Fatalf("%s raw FusionCandidate %q/unit %q has no context-item mapping for intent %s", family.name, fused.EvidenceID, unitID, request.Intent.Kind)
		}
		packageCandidate, ok := contextE2EPackageCandidate(result.Candidates, fused.EvidenceID)
		if !ok {
			t.Fatalf("%s fusion candidate %q was not resolved to a package candidate", family.name, fused.EvidenceID)
		}
		if packageCandidate.CanonicalEvidenceID != fused.EvidenceID || packageCandidate.Unit.ID != unitID {
			t.Fatalf("%s package candidate boundary = %#v, want fusion %q/unit %q", family.name, packageCandidate, fused.EvidenceID, unitID)
		}
		if wantUnit, exists := family.evidence[unitID]; !exists || !reflect.DeepEqual(packageCandidate.Unit, wantUnit) {
			t.Fatalf("%s package candidate %q did not preserve canonical EvidenceUnit", family.name, fused.EvidenceID)
		}
		matchedItem := false
		for _, itemID := range allowedIDs {
			item, exists := expectedByID[itemID]
			if !exists {
				continue
			}
			if _, exists := seenItems[item.ID]; exists {
				continue
			}
			if !contextE2EItemUsesEvidence(item, unitID) {
				continue
			}
			matchedItem = true
			seenItems[item.ID] = struct{}{}
			aspects := contextE2EAspects(item, len(candidates), len(fused.RelationSignals) > 0)
			relevance := fused.Score
			if relevance < 0.01 {
				relevance = 0.01
			}
			if relevance > 1 {
				relevance = 1
			}
			candidates = append(candidates, ContextSelectionCandidate{
				Item:          item,
				Relevance:     relevance,
				Rank:          fused.Rank,
				Aspects:       aspects,
				RedundancyKey: item.ID,
				TokenCost:     8,
				CharacterCost: 64,
				ByteCost:      128,
			})
		}
		if !matchedItem {
			t.Fatalf("%s raw FusionCandidate %q/unit %q produced no eligible item for intent %s", family.name, fused.EvidenceID, unitID, request.Intent.Kind)
		}
	}
	sort.SliceStable(candidates, func(left, right int) bool {
		if candidates[left].Rank != candidates[right].Rank {
			return candidates[left].Rank < candidates[right].Rank
		}
		return candidates[left].Item.ID < candidates[right].Item.ID
	})
	return candidates
}

func contextE2EIntentForRequest(family contextE2EFamily, request ContextRequest) contextE2EIntent {
	return contextE2EIntent{
		name:       string(request.Intent.Kind),
		kind:       request.Intent.Kind,
		targetKind: request.Intent.Target.Kind,
		question:   request.Intent.Question,
	}
}

func contextE2EPackageCandidate(candidates []PackageCandidate, evidenceID string) (PackageCandidate, bool) {
	for _, candidate := range candidates {
		if candidate.Fusion.EvidenceID == evidenceID {
			return candidate, true
		}
	}
	return PackageCandidate{}, false
}

func contextE2EItemUsesEvidence(item ContextItem, evidenceID string) bool {
	if item.Evidence != nil && item.Evidence.ID == evidenceID {
		return true
	}
	for _, reference := range item.Provenance.Evidence {
		if reference.ID == evidenceID {
			return true
		}
	}
	return false
}

func contextE2EAspects(item ContextItem, _ int, relation bool) []string {
	var aspects []string
	switch item.Kind {
	case ContextItemEntity:
		aspects = []string{"symbol", "locator"}
	case ContextItemFact:
		aspects = []string{"dependency", "evidence"}
	case ContextItemEvidence:
		aspects = []string{"evidence", "source"}
	default:
		aspects = []string{"provenance", "locator"}
	}
	if relation {
		aspects = append(aspects, "relation")
	}
	return aspects
}

func contextE2EPageCandidates(t *testing.T, family contextE2EFamily, request ContextRequest) []ContextSelectionCandidate {
	candidates := contextE2ECandidatesForRequest(t, family, request)
	for index := range candidates {
		if candidates[index].Item.ID == family.evidenceID {
			candidates[index].TokenCost = 1
			candidates[index].CharacterCost = 1
			candidates[index].ByteCost = 1
			continue
		}
		candidates[index].TokenCost = contextE2ELimits().MaxTokens
		candidates[index].CharacterCost = contextE2ELimits().MaxCharacters
		candidates[index].ByteCost = contextE2ELimits().MaxBytes
	}
	return candidates
}

func contextE2ESequenceStarting(sequence []string, first string) []string {
	ordered := make([]string, 0, len(sequence))
	if containsString(sequence, first) {
		ordered = append(ordered, first)
	}
	for _, id := range sequence {
		if id != first {
			ordered = append(ordered, id)
		}
	}
	return ordered
}

func contextE2ERelationCandidates(t *testing.T, family contextE2EFamily, intent contextE2EIntent) []ContextRelationCandidate {
	t.Helper()
	if intent.kind != IntentKindPossibleImpact {
		return nil
	}
	if family.retrieval == nil || len(family.retrieval.results) == 0 {
		t.Fatalf("%s possible-impact closure has no retrieval result", family.name)
	}
	result := family.retrieval.results[len(family.retrieval.results)-1]
	if len(result.Fusion.Candidates) == 0 {
		t.Fatalf("%s possible-impact closure has no fused candidates", family.name)
	}
	endpointIDs := map[string]string{
		family.retrieval.subjectEntityID: family.subjectID,
		family.retrieval.objectEntityID:  family.objectID,
	}
	dependencyEvidenceRetrievalID := family.retrieval.evidenceIDsByUnitID[family.dependencyEvidenceID]
	var derived *ContextRelation
	var score float64
	var rank int
	for _, candidate := range result.Fusion.Candidates {
		for _, signal := range candidate.RelationSignals {
			relationHit := signal.Relation
			if candidate.EvidenceID != dependencyEvidenceRetrievalID || signal.SeedEvidenceID != dependencyEvidenceRetrievalID {
				t.Fatalf("%s closure relation signal is not dependency evidence: candidate=%q seed=%q dependency=%q", family.name, candidate.EvidenceID, signal.SeedEvidenceID, dependencyEvidenceRetrievalID)
			}
			fromID, fromOK := endpointIDs[relationHit.FromEntityID]
			toID, toOK := endpointIDs[relationHit.ToEntityID]
			if !fromOK || !toOK || relationHit.RelationExternalID != family.dependencyFact.ID || relationHit.RelationType != string(family.dependencyFact.Predicate) || relationHit.Hops != retrieval.MaxRelationHops {
				t.Fatalf("%s closure relation hit does not derive from dependency fact: %#v", family.name, relationHit)
			}
			producer := family.producer
			candidateRelation := ContextRelation{
				ID:        relationHit.RelationID,
				Predicate: fact.Predicate(relationHit.RelationType),
				Origin:    ContextKnowledgeObserved,
				Scope:     family.scope,
				FromID:    fromID,
				ToID:      toID,
				Path:      []string{fromID, toID},
				SupportIDs: []string{
					family.dependencyFact.ID,
					family.dependencyEvidenceID,
				},
				Provenance: ContextProvenance{
					Producer: &producer,
					Evidence: append([]fact.EvidenceRef(nil), family.dependencyFact.Evidence...),
				},
			}
			if err := candidateRelation.Validate(); err != nil {
				t.Fatalf("%s derived closure relation invalid: %v", family.name, err)
			}
			if !reflect.DeepEqual(candidateRelation, family.relation) {
				t.Fatalf("%s derived closure relation differs from normalized fact relation: got=%#v want=%#v", family.name, candidateRelation, family.relation)
			}
			if derived != nil {
				t.Fatalf("%s possible-impact retrieval returned duplicate relation signals", family.name)
			}
			derived = &candidateRelation
			score = signal.Contribution
			rank = signal.Rank
		}
	}
	if derived == nil {
		t.Fatalf("%s possible-impact closure has no real relation signal", family.name)
	}
	return []ContextRelationCandidate{{
		Relation:      *derived,
		Score:         score,
		Rank:          rank,
		TokenCost:     4,
		CharacterCost: 32,
		ByteCost:      64,
	}}
}

func assertContextE2ESelection(t *testing.T, family contextE2EFamily, intent contextE2EIntent, selection ContextSelectionResult, candidates []ContextSelectionCandidate) {
	t.Helper()
	expected := contextE2EItemsForIntent(family, intent)
	expectedByID := make(map[string]ContextItem, len(expected))
	for _, item := range expected {
		expectedByID[item.ID] = item
	}
	if len(selection.Items) != len(expected) || selection.BudgetExhausted {
		t.Fatalf("%s selection = items=%d budget_exhausted=%t, want %d items without budget exhaustion", intent.name, len(selection.Items), selection.BudgetExhausted, len(expected))
	}
	selected := make(map[string]ContextItem, len(selection.Items))
	for _, item := range selection.Items {
		want, ok := expectedByID[item.ID]
		if !ok {
			t.Fatalf("%s selected unexpected item %q", intent.name, item.ID)
		}
		if !reflect.DeepEqual(item, want) {
			t.Fatalf("%s item %q changed during selection", intent.name, item.ID)
		}
		selected[item.ID] = item
	}
	if len(selected) != len(expectedByID) {
		t.Fatalf("%s selected IDs = %#v, want %#v", intent.name, contextE2EMapKeys(selected), contextE2EMapKeys(expectedByID))
	}
	if len(selection.Audit) != len(candidates) {
		t.Fatalf("%s audit count = %d, want %d", intent.name, len(selection.Audit), len(candidates))
	}
	for _, audit := range selection.Audit {
		if _, ok := expectedByID[audit.ItemID]; !ok || !audit.Included || audit.Reason != ContextSelectionIncluded {
			t.Fatalf("%s audit = %#v, want included exact candidate", intent.name, audit)
		}
	}
}

func contextE2EMapKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func contextE2EPolicy(t *testing.T, family contextE2EFamily, items []ContextItem, relations []ContextRelation) ContextPolicyResult {
	t.Helper()
	request := contextE2EPolicyRequest(t, family, items, relations)
	result, err := ApplyContextPolicy(context.Background(), request)
	if err != nil {
		t.Fatalf("ApplyContextPolicy() error: %v", err)
	}
	if err := result.ValidateAgainst(request); err != nil {
		t.Fatalf("policy result.ValidateAgainst() error: %v", err)
	}
	if result.PolicyFiltered {
		t.Fatalf("all authorized context was filtered: %#v", result)
	}
	return result
}

func contextE2EPolicyRequest(t *testing.T, family contextE2EFamily, items []ContextItem, relations []ContextRelation) ContextPolicyRequest {
	t.Helper()
	authorizations := contextE2EAuthorizations(items)
	policy := family.policy
	digest, err := ContextPolicyDigest(&policy, authorizations)
	if err != nil {
		t.Fatalf("ContextPolicyDigest() error: %v", err)
	}
	return ContextPolicyRequest{Scope: family.scope, Items: items, Relations: relations, Authorizations: authorizations, TransferPolicy: &policy, PolicyDigest: digest, ContinuationIDs: contextE2EItemIDs(items)}
}

func contextE2EAuthorizations(items []ContextItem) []ContextItemAuthorization {
	authorizations := make([]ContextItemAuthorization, 0, len(items))
	for _, item := range items {
		authorizations = append(authorizations, ContextItemAuthorization{ItemID: item.ID, Decision: evidence.DecisionAllow})
	}
	return authorizations
}

func contextE2EItemIDs(items []ContextItem) []string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	return ids
}

func contextE2ECandidateIDs(candidates []ContextSelectionCandidate) []string {
	ids := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		ids = append(ids, candidate.Item.ID)
	}
	return ids
}

func contextE2EPackage(t *testing.T, family contextE2EFamily, request ContextRequest, closure ContextSupportClosureResult, policy ContextPolicyResult) ContextPackage {
	t.Helper()
	packageContext := ContextPackage{
		Version:        ContextVersion,
		Revision:       "revision-" + family.name,
		Scope:          family.scope,
		Intent:         request.Intent,
		Limits:         request.Limits,
		Items:          policy.Items,
		Relations:      policy.Relations,
		Coverage:       append([]contract.Coverage(nil), family.coverage...),
		Gaps:           append([]contract.Gap(nil), family.gaps...),
		Degradations:   []ContextDegradation{{Code: ContextDegradationVectorUnavailable}},
		Audit:          closure.Selection.Audit,
		TokenEstimate:  closure.TokenEstimate,
		CharactersUsed: closure.CharactersUsed,
		BytesUsed:      closure.BytesUsed,
	}
	if len(packageContext.Gaps) > 0 {
		packageContext.Degradations = append(packageContext.Degradations, ContextDegradation{Code: ContextDegradationCoverageIncomplete})
	}
	finalized, err := FinalizeContextPackage(context.Background(), packageContext, ContextPackageIdentityBinding{
		PolicyDigest:          policy.PolicyDigest,
		PolicyContinuationIDs: policy.ContinuationIDs,
		PolicyFiltered:        policy.PolicyFiltered,
	})
	if err != nil {
		t.Fatalf("FinalizeContextPackage() error: %v", err)
	}
	return finalized
}

func contextE2EPackageDigest(t *testing.T, packageContext ContextPackage, policy ContextPolicyResult) string {
	t.Helper()
	type material struct {
		Version               string
		Revision              string
		Scope                 Scope
		Intent                Intent
		Limits                ContextLimits
		Items                 []ContextItem
		Relations             []ContextRelation
		Coverage              []contract.Coverage
		Gaps                  []contract.Gap
		Degradations          []ContextDegradation
		Audit                 []ContextSelectionAudit
		TokenEstimate         int
		CharactersUsed        int64
		BytesUsed             int64
		Truncated             bool
		Continuation          *ContextContinuation
		PolicyDigest          string
		PolicyContinuationIDs []string
		PolicyFiltered        bool
	}
	encoded, err := json.Marshal(material{
		Version:               packageContext.Version,
		Revision:              packageContext.Revision,
		Scope:                 packageContext.Scope,
		Intent:                packageContext.Intent,
		Limits:                packageContext.Limits,
		Items:                 packageContext.Items,
		Relations:             packageContext.Relations,
		Coverage:              packageContext.Coverage,
		Gaps:                  packageContext.Gaps,
		Degradations:          packageContext.Degradations,
		Audit:                 packageContext.Audit,
		TokenEstimate:         packageContext.TokenEstimate,
		CharactersUsed:        packageContext.CharactersUsed,
		BytesUsed:             packageContext.BytesUsed,
		Truncated:             packageContext.Truncated,
		Continuation:          packageContext.Continuation,
		PolicyDigest:          policy.PolicyDigest,
		PolicyContinuationIDs: policy.ContinuationIDs,
		PolicyFiltered:        policy.PolicyFiltered,
	})
	if err != nil {
		t.Fatalf("context package digest JSON: %v", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func assertContextE2EFamily(t *testing.T, family contextE2EFamily) {
	t.Helper()
	if len(family.facts) == 0 || len(family.coverage) == 0 || len(family.gaps) == 0 {
		t.Fatalf("family corpus incomplete: facts=%d coverage=%d gaps=%d", len(family.facts), len(family.coverage), len(family.gaps))
	}
	if len(family.evidence) != len(family.locators) {
		t.Fatalf("evidence/locator index mismatch: %d/%d", len(family.evidence), len(family.locators))
	}
	for id, unit := range family.evidence {
		if err := unit.ValidatePrepared(); err != nil {
			t.Fatalf("evidence %q invalid: %v", id, err)
		}
		if unit.ID != id || unit.Locator != family.locators[id] {
			t.Fatalf("evidence %q locator/index mismatch", id)
		}
		if unit.Locator.SourceID != family.scope.SourceID || unit.Locator.ArtifactID != unit.ArtifactID || unit.Locator.Path == "" || !contextE2EConcreteLocator(unit.Locator) {
			t.Fatalf("evidence %q locator = %#v, want source/artifact/path/position", id, unit.Locator)
		}
		if unit.Contribution.AnalyzerID != family.manifest.ID || unit.Contribution.Method == "" || strings.IndexFunc(unit.Contribution.Method, unicode.IsControl) >= 0 {
			t.Fatalf("evidence %q contribution provenance = %#v", id, unit.Contribution)
		}
	}
	if family.manifest.ID == "wso2" {
		protected := false
		for _, unit := range family.evidence {
			if unit.ContentState != evidence.ContentStatePresent || unit.Classification != evidence.ClassificationSafeText || unit.ExternalTransfer != evidence.DecisionAllow {
				protected = true
				break
			}
		}
		if !protected {
			t.Fatal("WSO2 fixture produced no protected or controlled evidence state")
		}
	}
	for _, candidate := range family.facts {
		if err := candidate.Validate(); err != nil {
			t.Fatalf("fact %q invalid: %v", candidate.ID, err)
		}
		if candidate.Producer != family.producer || len(candidate.Evidence) == 0 {
			t.Fatalf("fact %q provenance = %#v evidence=%d", candidate.ID, candidate.Producer, len(candidate.Evidence))
		}
		for _, reference := range candidate.Evidence {
			unit, ok := family.evidence[reference.ID]
			if !ok || reference.Locator != unit.Locator {
				t.Fatalf("fact %q evidence %q does not transport exact locator", candidate.ID, reference.ID)
			}
		}
	}
	for _, coverage := range family.coverage {
		if err := coverage.Validate(); err != nil {
			t.Fatalf("coverage %#v invalid: %v", coverage, err)
		}
		if coverage.ID == "" || coverage.ID != contract.CoverageID(coverage.Dimension, coverage.Scope, coverage.State, coverage.AnalyzerID) || (coverage.AnalyzerID != family.manifest.ID && coverage.AnalyzerID != "") || coverage.Dimension == "" || coverage.Scope == "" || coverage.Message == "" || coverage.Locator == nil || coverage.Locator.SourceID != family.scope.SourceID || coverage.Locator.ArtifactID == "" || coverage.Locator.Path == "" {
			t.Fatalf("coverage %#v lacks canonical dimensions/state/locator", coverage)
		}
	}
	for _, gap := range family.gaps {
		if err := gap.Validate(); err != nil {
			t.Fatalf("gap %#v invalid: %v", gap, err)
		}
		if gap.ID == "" || gap.ID != contract.GapID(gap.Code, gap.Dimension, gap.Scope, gap.Message, gap.AnalyzerID) || (gap.AnalyzerID != family.manifest.ID && gap.AnalyzerID != "") || gap.Code == "" || gap.Dimension == "" || gap.Message == "" || gap.Locator == nil || gap.Locator.SourceID != family.scope.SourceID || gap.Locator.ArtifactID == "" || gap.Locator.Path == "" {
			t.Fatalf("gap %#v lacks canonical code/dimension/locator", gap)
		}
	}
}

func assertContextE2EPackage(t *testing.T, family contextE2EFamily, intent contextE2EIntent, packageContext ContextPackage, projection ContextGatewayProjection) {
	t.Helper()
	if packageContext.Scope != family.scope || packageContext.Revision == "" || packageContext.Truncated {
		t.Fatalf("package boundary = %#v", packageContext)
	}
	if packageContext.TokenEstimate > packageContext.Limits.MaxTokens || packageContext.CharactersUsed > packageContext.Limits.MaxCharacters || packageContext.BytesUsed > packageContext.Limits.MaxBytes || len(packageContext.Items) > packageContext.Limits.MaxItems {
		t.Fatalf("package exceeded applied limits: %#v", packageContext)
	}
	if !reflect.DeepEqual(packageContext.Coverage, family.coverage) || !reflect.DeepEqual(packageContext.Gaps, family.gaps) {
		t.Fatalf("coverage/gaps content changed: got=%#v/%#v want=%#v/%#v", packageContext.Coverage, packageContext.Gaps, family.coverage, family.gaps)
	}
	wantItems := contextE2EItemsForIntent(family, intent)
	wantByID := make(map[string]ContextItem, len(wantItems))
	for _, item := range wantItems {
		wantByID[item.ID] = item
	}
	byID := make(map[string]ContextItem, len(packageContext.Items))
	for _, item := range packageContext.Items {
		want, ok := wantByID[item.ID]
		if !ok || !contextE2EEquivalentItem(item, want) {
			if ok {
				t.Fatalf("%s package item %q differs: got kind=%q origin=%q locator=%#v support=%#v evidence=%#v want kind=%q origin=%q locator=%#v support=%#v evidence=%#v", intent.name, item.ID, item.Kind, item.Origin, item.Locator, item.SupportIDs, contextE2EFirstEvidenceID(item), want.Kind, want.Origin, want.Locator, want.SupportIDs, contextE2EFirstEvidenceID(want))
			}
			for expectedID, expected := range wantByID {
				if expected.Kind == item.Kind {
					t.Fatalf("%s package item %q is remapped from %q: got locator=%#v support=%#v evidence=%#v want locator=%#v support=%#v evidence=%#v", intent.name, item.ID, expectedID, item.Locator, item.SupportIDs, contextE2EFirstEvidenceID(item), expected.Locator, expected.SupportIDs, contextE2EFirstEvidenceID(expected))
				}
			}
			t.Fatalf("%s package item %q is not an intent candidate", intent.name, item.ID)
		}
		byID[item.ID] = item
		unitID := contextE2EFirstEvidenceID(item)
		unit, ok := family.evidence[unitID]
		if !ok || item.Locator != unit.Locator || item.Locator.ArtifactID != unit.ArtifactID || !contextE2EConcreteLocator(item.Locator) {
			t.Fatalf("item %q locator = %#v, want exact evidence locator", item.ID, item.Locator)
		}
		if item.Provenance.Producer == nil || *item.Provenance.Producer != family.producer || len(item.Provenance.Evidence) == 0 {
			t.Fatalf("item %q lacks producer/evidence provenance: %#v", item.ID, item.Provenance)
		}
		for _, reference := range item.Provenance.Evidence {
			unit, ok := family.evidence[reference.ID]
			if !ok || reference.Locator != unit.Locator {
				t.Fatalf("item %q provenance evidence %q differs from source unit", item.ID, reference.ID)
			}
		}
	}
	if len(byID) != len(wantByID) {
		t.Fatalf("%s package item set = %#v, want %#v", intent.name, contextE2EMapKeys(byID), contextE2EMapKeys(wantByID))
	}
	audited := make(map[string]ContextSelectionAudit, len(packageContext.Audit))
	for _, audit := range packageContext.Audit {
		if !audit.Included || audit.Reason != ContextSelectionIncluded {
			t.Fatalf("%s package audit contains non-included item decision: %#v", intent.name, audit)
		}
		audited[audit.ItemID] = audit
	}
	if len(audited) != len(wantByID) || !sameStringSet(contextE2EMapKeys(audited), contextE2EMapKeys(wantByID)) {
		t.Fatalf("%s package audit IDs = %#v, want %#v", intent.name, contextE2EMapKeys(audited), contextE2EMapKeys(wantByID))
	}
	wantRelations := 0
	if intent.kind == IntentKindPossibleImpact {
		wantRelations = 1
	}
	if len(packageContext.Relations) != wantRelations {
		t.Fatalf("%s relations = %d, want %d", intent.name, len(packageContext.Relations), wantRelations)
	}
	if wantRelations == 1 {
		relation := packageContext.Relations[0]
		if !reflect.DeepEqual(relation, family.relation) || relation.Predicate != fact.PredicateDependency || relation.Origin != ContextKnowledgeObserved || relation.Provenance.Lineage != nil || relation.FromID != family.subjectID || relation.ToID != family.objectID || !sameStringSet(relation.SupportIDs, []string{family.dependencyFact.ID, family.dependencyEvidenceID}) || relation.Provenance.Producer == nil || *relation.Provenance.Producer != family.producer {
			t.Fatalf("possible-impact relation is not real observed dependency: %#v", relation)
		}
	}
	switch intent.kind {
	case IntentKindQuestion:
		if packageContext.Intent.Question == "" || packageContext.Intent.Target != (IntentTarget{}) {
			t.Fatalf("question intent malformed: %#v", packageContext.Intent)
		}
	case IntentKindSymbol:
		if packageContext.Intent.Target.ID != family.symbolID || packageContext.Intent.Target.Kind != IntentTargetSymbol {
			t.Fatalf("symbol intent target = %#v", packageContext.Intent.Target)
		}
	case IntentKindPossibleImpact:
		if packageContext.Intent.Target.ID != family.impactTargetID || packageContext.Intent.Target.Kind != IntentTargetSymbol {
			t.Fatalf("impact intent target = %#v", packageContext.Intent.Target)
		}
	case IntentKindEvidenceInspection:
		if packageContext.Intent.Target.ID != family.evidenceID || packageContext.Intent.Target.Kind != IntentTargetEvidence {
			t.Fatalf("evidence intent target = %#v", packageContext.Intent.Target)
		}
	}
	if projection.ContextPackageID != packageContext.ID || projection.ContextPackageDigest != packageContext.Digest || len(projection.GatewayPackage.Evidence) != len(packageContext.Items)+len(packageContext.Relations) || !sameStringSequence(projection.GapIDs, contextE2EGapIDs(family.gaps)) {
		t.Fatalf("projection identity/count mismatch: %#v", projection)
	}
	for index, item := range packageContext.Items {
		expected, _, err := projectContextItem(item)
		if err != nil || projection.GatewayPackage.Evidence[index].ID != expected.Gateway.ID || projection.GatewayPackage.Evidence[index].Content != expected.Gateway.Content || projection.GatewayPackage.Evidence[index].ContentDigest != expected.Gateway.ContentDigest || projection.GatewayPackage.Evidence[index].Locator != expected.Gateway.Locator {
			t.Fatalf("projection item %q content/identity changed", item.ID)
		}
	}
	for index, relation := range packageContext.Relations {
		locator, err := contextGatewayRelationLocator(relation, contextE2EItemLocators(packageContext.Items))
		if err != nil {
			t.Fatalf("relation locator: %v", err)
		}
		expected, _, err := projectContextRelation(relation, locator)
		projected := projection.GatewayPackage.Evidence[len(packageContext.Items)+index]
		if err != nil || projected.ID != expected.Gateway.ID || projected.Content != expected.Gateway.Content || projected.ContentDigest != expected.Gateway.ContentDigest || projected.Locator != expected.Gateway.Locator {
			t.Fatalf("projection relation %q content/identity changed", relation.ID)
		}
	}
	encoded, err := json.Marshal(projection)
	if err != nil {
		t.Fatalf("projection JSON: %v", err)
	}
	for _, forbidden := range []string{"user:pass", "secret-value", "password =", "api_key"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("projection leaked forbidden source material %q", forbidden)
		}
	}
}

func contextE2EHasBudgetAudit(result ContextSelectionResult) bool {
	for _, audit := range result.Audit {
		if !audit.Included && audit.Reason == ContextSelectionExcludedBudget {
			return true
		}
	}
	return false
}

func contextE2EIntentDigest(t *testing.T, intent Intent) string {
	t.Helper()
	encoded, err := json.Marshal(intent)
	if err != nil {
		t.Fatalf("intent digest JSON: %v", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func contextE2EDigest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func sameStringSet(left, right []string) bool {
	leftCopy := append([]string(nil), left...)
	rightCopy := append([]string(nil), right...)
	sort.Strings(leftCopy)
	sort.Strings(rightCopy)
	return reflect.DeepEqual(leftCopy, rightCopy)
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func contextE2EConcreteLocator(locator contract.Locator) bool {
	return locator.Member != "" || locator.StartLine > 0 || locator.EndLine > 0 || locator.ByteOffset > 0 || locator.ByteLength > 0
}

func contextE2EEquivalentItem(left, right ContextItem) bool {
	left.SupportIDs = append([]string(nil), left.SupportIDs...)
	right.SupportIDs = append([]string(nil), right.SupportIDs...)
	if len(left.SupportIDs) == 0 {
		left.SupportIDs = nil
	}
	if len(right.SupportIDs) == 0 {
		right.SupportIDs = nil
	}
	return reflect.DeepEqual(left, right)
}

func contextE2EFirstEvidenceID(item ContextItem) string {
	if len(item.Provenance.Evidence) == 0 {
		return ""
	}
	return item.Provenance.Evidence[0].ID
}

func contextE2EGapIDs(gaps []contract.Gap) []string {
	ids := make([]string, 0, len(gaps))
	for _, gap := range gaps {
		ids = append(ids, gap.ID)
	}
	return ids
}

func contextE2EItemLocators(items []ContextItem) map[string]contract.Locator {
	locators := make(map[string]contract.Locator, len(items))
	for _, item := range items {
		locators[item.ID] = item.Locator
	}
	return locators
}

func repoRootForContextE2E() (string, error) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("runtime caller unavailable")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(filename), "../..")), nil
}

func contextE2EUUID(value int) string {
	return fmt.Sprintf("00000000-0000-0000-0000-%012d", value)
}
