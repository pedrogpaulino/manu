//go:build integration

package persistence_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/pedrogpaulino/manu/internal/fact"
	"github.com/pedrogpaulino/manu/internal/identity"
	"github.com/pedrogpaulino/manu/internal/ingestion"
	"github.com/pedrogpaulino/manu/internal/persistence"
	domainquery "github.com/pedrogpaulino/manu/internal/query"
	"github.com/pedrogpaulino/manu/internal/retrieval"
)

func TestProductionContextServicePostgreSQLComposesCanonicalContext(t *testing.T) {
	database := openPostgresIntegrationDatabase(t)
	content := "class IntegrationType {}"
	fixture := integrationFixture(t, "context-service", "snapshot-1", content)
	repository := persistence.NewRepository(database.pool)
	jobs := persistence.NewJobStore(database.pool)
	pipeline := newIntegrationPipeline(t, database, repository, jobs, fixture, ingestion.EmbeddingOptions{})
	runIntegrationJob(t, jobs, pipeline, fixture.job)

	factual := factualRepositoryInput(t, fixture, "evidence-present")
	if err := repository.PersistFactualSnapshot(context.Background(), factual); err != nil {
		t.Fatalf("persist factual snapshot: %v", err)
	}
	if _, err := repository.RebuildFactualProjection(
		context.Background(), factual.OrganizationID, factual.SourceID, factual.SnapshotID,
	); err != nil {
		t.Fatalf("rebuild factual projection: %v", err)
	}

	service := newPostgresContextService(t, database, repository)
	scope := domainquery.Scope{
		OrganizationID: fixture.job.OrganizationID,
		SourceID:       fixture.job.SourceID,
		SnapshotID:     fixture.job.SnapshotID,
	}
	request := domainquery.ContextRequest{
		Version: domainquery.ContextVersion,
		Scope:   scope,
		Intent: domainquery.Intent{
			Version:  domainquery.ContextVersion,
			Kind:     domainquery.IntentKindQuestion,
			Question: "IntegrationType",
		},
		Limits: productionContextIntegrationLimits(),
	}

	// Keep one direct retrieval assertion before the application service so an
	// integration failure can identify the real PostgreSQL adapter boundary;
	// BuildContext intentionally maps stage failures to a payload-free error.
	diagnosticPlan, err := domainquery.PlanContextRetrieval(context.Background(), request, 8)
	if err != nil {
		t.Fatalf("diagnostic PlanContextRetrieval() error: %v", err)
	}
	diagnosticRetriever := newPostgresContextRetriever(database)
	diagnosticResult, err := diagnosticRetriever.Retrieve(context.Background(), diagnosticPlan.Input)
	if err != nil {
		t.Fatalf("diagnostic PostgreSQL retrieval error: %v", err)
	}
	if len(diagnosticResult.Candidates) == 0 {
		t.Fatalf("diagnostic PostgreSQL retrieval returned no candidates: %#v", diagnosticResult)
	}

	first, err := service.BuildContext(context.Background(), request)
	if err != nil {
		t.Fatalf("first BuildContext() error: %v", err)
	}
	if err := first.Validate(); err != nil {
		t.Fatalf("first package validation error: %v", err)
	}
	if first.Scope != scope {
		t.Fatalf("package scope = %#v, want %#v", first.Scope, scope)
	}
	if first.Revision != "revision-snapshot-1" {
		t.Fatalf("package revision = %q, want %q", first.Revision, "revision-snapshot-1")
	}
	if len(first.Items) < 3 {
		t.Fatalf("package item count = %d, want factual, entity, and evidence items: %#v", len(first.Items), first.Items)
	}

	var evidenceContent, evidencePath string
	var foundFact, foundEvidence, foundDependencyRelation bool
	for _, item := range first.Items {
		if item.Locator.Path != "src/context-service.java" {
			t.Fatalf("item locator path = %q, want persisted artifact path", item.Locator.Path)
		}
		switch item.Kind {
		case domainquery.ContextItemEvidence:
			if item.Evidence == nil {
				t.Fatal("evidence item has no evidence payload")
			}
			evidenceContent = item.Evidence.Content
			evidencePath = item.Evidence.Locator.Path
			foundEvidence = true
		case domainquery.ContextItemFact:
			if item.Fact == nil {
				t.Fatal("fact item has no fact payload")
			}
			if item.Fact.Subject.ID != "IntegrationType" {
				t.Fatalf("fact subject = %#v, want IntegrationType", item.Fact.Subject)
			}
			foundFact = true
		}
	}
	for _, relation := range first.Relations {
		if relation.Predicate != fact.PredicateDependency {
			continue
		}
		if relation.FromID != "IntegrationType" || relation.ToID != "IntegrationDependency" {
			t.Fatalf("dependency relation endpoints = %q -> %q", relation.FromID, relation.ToID)
		}
		foundDependencyRelation = true
	}
	if !foundEvidence || evidenceContent != content || evidencePath != "src/context-service.java" {
		t.Fatalf("PostgreSQL evidence content/locator not present: content=%q path=%q", evidenceContent, evidencePath)
	}
	if !foundFact {
		t.Fatal("PostgreSQL canonical fact not present in context package")
	}
	if !foundDependencyRelation {
		t.Fatalf("PostgreSQL factual dependency relation not present: %#v", first.Relations)
	}
	if !containsContextDegradation(first, domainquery.ContextDegradationVectorUnavailable) {
		t.Fatalf("missing explicit vector degradation: %#v", first.Degradations)
	}

	repeated, err := service.BuildContext(context.Background(), request)
	if err != nil {
		t.Fatalf("repeated BuildContext() error: %v", err)
	}
	if err := repeated.Validate(); err != nil {
		t.Fatalf("repeated package validation error: %v", err)
	}
	if !reflect.DeepEqual(first, repeated) {
		t.Fatalf("repeated BuildContext() was not deterministic:\nfirst=%#v\nrepeated=%#v", first, repeated)
	}

	impactRequest := request
	impactRequest.Intent = domainquery.Intent{
		Version: domainquery.ContextVersion,
		Kind:    domainquery.IntentKindPossibleImpact,
		Target: domainquery.IntentTarget{
			Kind: domainquery.IntentTargetSymbol,
			ID:   "IntegrationType",
		},
	}
	impact, err := service.BuildContext(context.Background(), impactRequest)
	if err != nil {
		t.Fatalf("possible-impact BuildContext() error: %v", err)
	}
	if err := impact.Validate(); err != nil {
		t.Fatalf("possible-impact package validation error: %v", err)
	}
	if impact.Scope != scope || impact.Revision != first.Revision || len(impact.Items) == 0 {
		t.Fatalf("possible-impact package scope/revision/items = %#v", impact)
	}

	wrongSnapshot := request
	wrongSnapshot.Scope.SnapshotID = identity.CanonicalUUID(
		"snapshot", fixture.organizationID, fixture.sourceID, "missing-snapshot",
	)
	if _, err := service.BuildContext(context.Background(), wrongSnapshot); !errors.Is(err, domainquery.ErrContextServiceSnapshot) {
		t.Fatalf("wrong snapshot BuildContext() error = %v, want ErrContextServiceSnapshot", err)
	}

	pageRequest := request
	pageRequest.Limits.MaxItems = 1
	page, err := service.BuildContext(context.Background(), pageRequest)
	if err != nil {
		t.Fatalf("budgeted BuildContext() error: %v", err)
	}
	if err := page.Validate(); err != nil {
		t.Fatalf("budgeted package validation error: %v", err)
	}
	if len(page.Items) != 1 || !page.Truncated || page.Continuation == nil {
		t.Fatalf("budgeted package = %#v, want one item and continuation", page)
	}
	if !containsContextDegradation(page, domainquery.ContextDegradationBudgetExhausted) {
		t.Fatalf("missing budget degradation: %#v", page.Degradations)
	}

	continuedRequest := pageRequest
	continuedRequest.Continuation = page.Continuation
	continued, err := service.BuildContext(context.Background(), continuedRequest)
	if err != nil {
		t.Fatalf("continued BuildContext() error: %v", err)
	}
	if err := continued.Validate(); err != nil {
		t.Fatalf("continued package validation error: %v", err)
	}
	if len(continued.Items) != 1 || continued.Items[0].ID == page.Items[0].ID {
		t.Fatalf("continued items = %#v, want one item distinct from first page %#v", continued.Items, page.Items)
	}
}

func newPostgresContextService(t *testing.T, database *postgresIntegrationDatabase, repository *persistence.Repository) domainquery.ContextService {
	t.Helper()
	retriever := newPostgresContextRetriever(database)
	codec, err := domainquery.NewContextContinuationCodec([]byte("production-context-service-integration-key-32-bytes"))
	if err != nil {
		t.Fatalf("create context continuation codec: %v", err)
	}
	service, err := domainquery.NewProductionContextService(repository, retriever, codec, domainquery.ContextServiceConfig{
		Utility:        domainquery.DefaultContextUtilityConfiguration(),
		Estimator:      domainquery.DefaultContextTokenEstimatorConfiguration(),
		RetrievalLimit: 8,
	})
	if err != nil {
		t.Fatalf("compose production context service: %v", err)
	}
	return service
}

func newPostgresContextRetriever(database *postgresIntegrationDatabase) *domainquery.HybridRetriever {
	return &domainquery.HybridRetriever{
		Text:           retrieval.NewTextProjection(persistence.NewTextProjectionStore(database.pool)),
		UnitResolver:   persistence.NewQueryEvidenceUnitRepository(database.pool),
		Relations:      retrieval.NewRelationProjection(persistence.NewRelationProjectionStore(database.pool)),
		RelationInputs: persistence.NewFactualRelationInputProvider(database.pool),
		Support:        domainquery.ConservativeSupportAssessor{},
		Fusion:         retrieval.FusionConfiguration{},
		Limit:          8,
	}
}

func productionContextIntegrationLimits() domainquery.ContextLimits {
	return domainquery.ContextLimits{
		MaxTokens:     4_096,
		MaxItems:      32,
		MaxCharacters: 1 << 16,
		MaxBytes:      1 << 20,
	}
}

func containsContextDegradation(packageContext domainquery.ContextPackage, code domainquery.ContextDegradationCode) bool {
	for _, degradation := range packageContext.Degradations {
		if degradation.Code == code {
			return true
		}
	}
	return false
}

var _ domainquery.ContextService = (*domainquery.ProductionContextService)(nil)
