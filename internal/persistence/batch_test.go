package persistence

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pedrogpaulino/manu/internal/bundle"
	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/evidence"
)

type batchFakeStarter struct {
	tx         *batchFakeTransaction
	beginCalls int
}

func (s *batchFakeStarter) Begin(context.Context) (transaction, error) {
	s.beginCalls++
	return s.tx, nil
}

type batchFakeTransaction struct {
	execs         []fakeExec
	execTags      []pgconn.CommandTag
	execIndex     int
	execErr       error
	queryRows     []pgx.Row
	queryIndex    int
	commitCalls   int
	rollbackCalls int
}

func (tx *batchFakeTransaction) Exec(_ context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	tx.execs = append(tx.execs, fakeExec{query: query, args: append([]any(nil), args...)})
	if tx.execErr != nil {
		return pgconn.CommandTag{}, tx.execErr
	}
	if tx.execIndex >= len(tx.execTags) {
		return pgconn.NewCommandTag("INSERT 1"), nil
	}
	tag := tx.execTags[tx.execIndex]
	tx.execIndex++
	return tag, nil
}

func (tx *batchFakeTransaction) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return &repositoryFakeRows{}, nil
}

func (tx *batchFakeTransaction) QueryRow(context.Context, string, ...any) pgx.Row {
	if tx.queryIndex >= len(tx.queryRows) {
		return fakeRow{err: pgx.ErrNoRows}
	}
	row := tx.queryRows[tx.queryIndex]
	tx.queryIndex++
	return row
}

func (tx *batchFakeTransaction) Commit(context.Context) error {
	tx.commitCalls++
	return nil
}

func (tx *batchFakeTransaction) Rollback(context.Context) error {
	tx.rollbackCalls++
	return nil
}

func TestPersistBundleUsesOneTransactionAndHistoricalIdentities(t *testing.T) {
	tx := &fakeTransaction{}
	repository, starter := newFakeRepository(tx)

	input := batchFixture("snapshot-1", "configuration-1")
	got, err := repository.PersistBundle(context.Background(), input)
	if err != nil {
		t.Fatalf("PersistBundle() error = %v", err)
	}
	if starter.beginCalls != 1 || tx.commitCalls != 1 || tx.rollbackCalls != 0 {
		t.Fatalf("transaction calls = begin %d, commit %d, rollback %d; want 1, 1, 0", starter.beginCalls, tx.commitCalls, tx.rollbackCalls)
	}
	// organization, source, snapshot, artifact, observation, coverage, gap,
	// failure, evidence, and six historical factual identities.
	if len(tx.execs) != 15 {
		t.Fatalf("exec count = %d, want 15", len(tx.execs))
	}
	if got.FactualDigest != input.Manifest.FactualDigest || got.SnapshotID == "" {
		t.Fatalf("result identity = %#v", got)
	}
	if len(got.FactualIdentityIDs) != 6 {
		t.Fatalf("factual identity result count = %d, want 6", len(got.FactualIdentityIDs))
	}
	if got.ArtifactIDs["artifact-1"] == "" || got.ObservationIDs["contribution-1"] == "" {
		t.Fatalf("result omitted mapped sequence IDs: %#v", got)
	}
	if got.FactualIdentityIDs["artifact:artifact-1"] == "" {
		t.Fatalf("result omitted artifact factual identity: %#v", got.FactualIdentityIDs)
	}
	if tx.execs[0].args[2] != input.Manifest.Organization.ID {
		t.Fatalf("organization fallback name = %#v, want external id %q", tx.execs[0].args[2], input.Manifest.Organization.ID)
	}
	for _, exec := range tx.execs {
		if strings.Contains(strings.ToUpper(exec.query), "ACTIVATE") || strings.Contains(strings.ToUpper(exec.query), "DELETE") {
			t.Fatalf("batch unexpectedly changed active/deleted state: %s", exec.query)
		}
	}
}

func TestPersistBundleIncrementalKeepsStableFactualIdentityKeyAcrossSnapshots(t *testing.T) {
	previous := batchFixture("snapshot-1", "configuration-1")
	current := batchFixture("snapshot-2", "configuration-1")
	firstTx := &fakeTransaction{}
	first, _ := newFakeRepository(firstTx)
	if _, err := first.PersistBundle(context.Background(), previous); err != nil {
		t.Fatalf("PersistBundle(previous) error = %v", err)
	}
	secondTx := &fakeTransaction{}
	second, _ := newFakeRepository(secondTx)
	if _, report, err := second.PersistBundleIncremental(context.Background(), previous, current); err != nil {
		t.Fatalf("PersistBundleIncremental() error = %v", err)
	} else if len(report.Reused) == 0 {
		t.Fatalf("incremental report did not reuse unchanged facts: %#v", report)
	}
	firstKeys := factualIdentityInsertKeys(firstTx)
	secondKeys := factualIdentityInsertKeys(secondTx)
	if len(firstKeys) == 0 || len(secondKeys) == 0 {
		t.Fatalf("factual identity writes = %d/%d", len(firstKeys), len(secondKeys))
	}
	if firstKeys[0] != secondKeys[0] || strings.Contains(firstKeys[0], "\x00") || !strings.HasPrefix(firstKeys[0], "v1:") {
		t.Fatalf("stable factual identity keys = %q/%q", firstKeys[0], secondKeys[0])
	}
}

func factualIdentityInsertKeys(tx *fakeTransaction) []string {
	keys := make([]string, 0)
	for _, exec := range tx.execs {
		if strings.Contains(exec.query, "INSERT INTO factual_identities") && len(exec.args) > 4 {
			if key, ok := exec.args[4].(string); ok {
				keys = append(keys, key)
			}
		}
	}
	return keys
}

func TestPersistBundleRollbackDoesNotCommit(t *testing.T) {
	tx := &fakeTransaction{execErr: errors.New("write failed")}
	repository, _ := newFakeRepository(tx)

	_, err := repository.PersistBundle(context.Background(), batchFixture("snapshot-1", "configuration-1"))
	if err == nil {
		t.Fatal("PersistBundle() error = nil, want rollback error")
	}
	if tx.commitCalls != 0 || tx.rollbackCalls != 1 {
		t.Fatalf("transaction calls = commit %d, rollback %d; want 0, 1", tx.commitCalls, tx.rollbackCalls)
	}
	if len(tx.execs) != 1 {
		t.Fatalf("writes before failure = %d, want 1", len(tx.execs))
	}
}

func TestPersistBundleRejectsOrphanBeforeOpeningTransaction(t *testing.T) {
	tx := &batchFakeTransaction{}
	starter := &batchFakeStarter{tx: tx}
	repository := newRepositoryWithStarter(starter)

	input := batchFixture("snapshot-1", "configuration-1")
	input.Manifest.Failures = append(input.Manifest.Failures, contract.Failure{
		ID: "orphan-failure", Code: "parse", Operation: "parse", Message: "artifact was unavailable", ArtifactID: "missing-artifact",
	})
	setBatchDigest(t, &input)
	_, err := repository.PersistBundle(context.Background(), input)
	if !errors.Is(err, bundle.ErrInvalidReference) {
		t.Fatalf("PersistBundle() error = %v, want ErrInvalidReference", err)
	}
	if starter.beginCalls != 0 || len(tx.execs) != 0 {
		t.Fatalf("orphan validation opened/wrote transaction: begin %d, writes %d", starter.beginCalls, len(tx.execs))
	}
}

func TestPersistBundleRetriesIdempotently(t *testing.T) {
	input := emptyBatchFixture("snapshot-1", "configuration-1")
	firstTx := &batchFakeTransaction{}
	first := newRepositoryWithStarter(&batchFakeStarter{tx: firstTx})
	firstResult, err := first.PersistBundle(context.Background(), input)
	if err != nil {
		t.Fatalf("first PersistBundle() error = %v", err)
	}

	secondTx := &batchFakeTransaction{
		execTags: []pgconn.CommandTag{
			pgconn.NewCommandTag("INSERT 0"),
			pgconn.NewCommandTag("INSERT 0"),
			pgconn.NewCommandTag("INSERT 0"),
		},
		queryRows: batchIdentityRows(input),
	}
	second := newRepositoryWithStarter(&batchFakeStarter{tx: secondTx})
	secondResult, err := second.PersistBundle(context.Background(), input)
	if err != nil {
		t.Fatalf("idempotent retry error = %v", err)
	}
	if !reflect.DeepEqual(firstResult, secondResult) {
		t.Fatalf("retry result differs:\nfirst %#v\nsecond %#v", firstResult, secondResult)
	}
	if secondTx.commitCalls != 1 || secondTx.rollbackCalls != 0 || secondTx.queryIndex != 3 {
		t.Fatalf("retry transaction calls/reads = commit %d rollback %d rows %d", secondTx.commitCalls, secondTx.rollbackCalls, secondTx.queryIndex)
	}
}

func TestPersistBundleRejectsIncompatibleSnapshotConfigurationOrDigest(t *testing.T) {
	base := emptyBatchFixture("snapshot-1", "configuration-1")
	tests := []struct {
		name   string
		mutate func(*bundle.Bundle)
	}{
		{
			name: "configuration",
			mutate: func(input *bundle.Bundle) {
				input.Manifest.Analysis.ConfigurationID = "configuration-2"
				input.Manifest.Execution.ConfigurationID = "configuration-2"
			},
		},
		{
			name: "factual digest",
			mutate: func(input *bundle.Bundle) {
				input.Manifest.Failures = append(input.Manifest.Failures, contract.Failure{
					ID: "failure-1", Code: "parse", Operation: "parse", Message: "changed fact",
				})
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := base
			test.mutate(&input)
			setBatchDigest(t, &input)
			tx := &batchFakeTransaction{
				execTags: []pgconn.CommandTag{
					pgconn.NewCommandTag("INSERT 0"),
					pgconn.NewCommandTag("INSERT 0"),
					pgconn.NewCommandTag("INSERT 0"),
				},
				queryRows: batchIdentityRows(base),
			}
			repository := newRepositoryWithStarter(&batchFakeStarter{tx: tx})
			_, err := repository.PersistBundle(context.Background(), input)
			if !errors.Is(err, ErrIncompatibleSnapshot) {
				t.Fatalf("PersistBundle() error = %v, want ErrIncompatibleSnapshot", err)
			}
			if tx.commitCalls != 0 || tx.rollbackCalls != 1 {
				t.Fatalf("transaction calls = commit %d rollback %d, want 0, 1", tx.commitCalls, tx.rollbackCalls)
			}
		})
	}
}

func TestPersistBundleNewSnapshotKeepsPreviousHistoricalIdentity(t *testing.T) {
	firstTx := &fakeTransaction{}
	firstRepository, _ := newFakeRepository(firstTx)
	first, err := firstRepository.PersistBundle(context.Background(), batchFixture("snapshot-1", "configuration-1"))
	if err != nil {
		t.Fatalf("first PersistBundle() error = %v", err)
	}

	secondTx := &fakeTransaction{}
	secondRepository, _ := newFakeRepository(secondTx)
	second, err := secondRepository.PersistBundle(context.Background(), batchFixture("snapshot-2", "configuration-1"))
	if err != nil {
		t.Fatalf("second PersistBundle() error = %v", err)
	}
	if first.SourceID != second.SourceID || first.SnapshotID == second.SnapshotID {
		t.Fatalf("snapshot/source IDs = first (%s, %s), second (%s, %s)", first.SourceID, first.SnapshotID, second.SourceID, second.SnapshotID)
	}
	if first.FactualIdentityIDs["artifact:artifact-1"] == second.FactualIdentityIDs["artifact:artifact-1"] {
		t.Fatal("new snapshot reused snapshot-scoped factual identity row")
	}
	if len(firstTx.execs) == 0 || len(secondTx.execs) == 0 {
		t.Fatal("expected writes for both snapshots")
	}
}

func batchFixture(snapshotID, configurationID string) bundle.Bundle {
	const (
		organizationID = "organization-1"
		sourceID       = "source-1"
		revision       = "revision-1"
	)
	hash := strings.Repeat("b", 64)
	source := contract.Source{ID: sourceID, Name: "fixture", Type: "filesystem", Revision: revision, Hash: hash}
	snapshot := contract.Snapshot{ID: snapshotID, SourceID: sourceID, Revision: revision, Hash: hash, CapturedAt: time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)}
	artifact := contract.Artifact{ID: "artifact-1", SourceID: sourceID, Path: "src/main.go", Type: "go", Hash: strings.Repeat("a", 64), Size: 10}
	contribution := contract.Contribution{
		ID: "contribution-1", ArtifactID: artifact.ID, AnalyzerID: "analyzer", AnalyzerVersion: "1", Method: "symbols", Type: "symbol",
		Locator: contract.Locator{SourceID: sourceID, ArtifactID: artifact.ID, Path: artifact.Path, StartLine: 1, EndLine: 1},
		Value:   []byte(`{"name":"main"}`), ObservedAt: time.Date(2026, time.January, 2, 3, 4, 6, 0, time.UTC),
	}
	content := "safe text"
	unit := evidence.EvidenceUnit{
		Version: evidence.Version, OrganizationID: organizationID, SourceID: sourceID, SnapshotID: snapshotID,
		ArtifactID: artifact.ID, Contribution: evidence.ContributionRef{
			ID: contribution.ID, ArtifactID: artifact.ID, AnalyzerID: contribution.AnalyzerID,
			AnalyzerVersion: contribution.AnalyzerVersion, Method: contribution.Method,
		}, Locator: contribution.Locator, ContentState: evidence.ContentStatePresent, Content: content,
		ContentHash: evidence.ContentDigest(content), ContentBytes: int64(len(content)), ContentCharacters: int64(len(content)),
		Persist: evidence.DecisionAllow, ExternalTransfer: evidence.DecisionAllow, Classification: evidence.ClassificationSafeText,
	}
	unit.ID = evidence.EvidenceID(unit)
	legacy := contract.Manifest{
		ContractVersion: contract.Version, ResultID: "result-1", Source: source, Snapshot: snapshot,
		Execution:     contract.ExecutionMetadata{RunID: "run-1", ConfigurationID: configurationID, StartedAt: snapshot.CapturedAt},
		ArtifactCount: 1, ContributionCount: 1,
		Coverage: []contract.Coverage{{ID: "coverage-1", Dimension: "inventory", State: contract.CoverageProduced}},
		Gaps:     []contract.Gap{{ID: "gap-1", Code: "unsupported", Message: "not measured"}},
		Failures: []contract.Failure{{ID: "failure-1", Code: "partial", Operation: "optional", Message: "optional analyzer failed", ArtifactID: artifact.ID}},
	}
	input := bundle.Bundle{
		Manifest: bundle.Manifest{
			Version: bundle.Version, Organization: bundle.Organization{ID: organizationID}, Manifest: legacy,
			Analysis: bundle.Analysis{ID: "analysis-1", ConfigurationID: configurationID, Revision: "analysis-revision-1"},
			Files: []bundle.File{
				{Name: bundle.ArtifactsFileName, Bytes: 1, Count: 1, Digest: strings.Repeat("c", 64)},
				{Name: bundle.ContributionsFileName, Bytes: 1, Count: 1, Digest: strings.Repeat("c", 64)},
				{Name: bundle.EvidenceFileName, Bytes: 1, Count: 1, Digest: strings.Repeat("c", 64)},
			},
			Counts:   bundle.Counts{ArtifactCount: 1, ContributionCount: 1, EvidenceUnitCount: 1},
			Limits:   bundle.Limits{MaxBundleBytes: 1 << 20, MaxManifestBytes: 1 << 16, MaxEvidenceBytes: 1 << 16, MaxArtifacts: 10, MaxContributions: 10, MaxEvidenceUnits: 10},
			Evidence: bundle.EvidenceMetadata{State: bundle.EvidenceStateAvailable},
		},
		Artifacts: []contract.Artifact{artifact}, Contributions: []contract.Contribution{contribution}, Evidence: []evidence.EvidenceUnit{unit},
	}
	digest, err := input.FactualDigest()
	if err != nil {
		panic(err)
	}
	input.Manifest.FactualDigest = digest
	return input
}

func emptyBatchFixture(snapshotID, configurationID string) bundle.Bundle {
	input := batchFixture(snapshotID, configurationID)
	input.Artifacts = nil
	input.Contributions = nil
	input.Evidence = nil
	input.Manifest.ArtifactCount = 0
	input.Manifest.ContributionCount = 0
	input.Manifest.Coverage = nil
	input.Manifest.Gaps = nil
	input.Manifest.Failures = nil
	input.Manifest.Counts = bundle.Counts{}
	input.Manifest.Files = []bundle.File{
		{Name: bundle.ArtifactsFileName, Digest: strings.Repeat("c", 64)},
		{Name: bundle.ContributionsFileName, Digest: strings.Repeat("c", 64)},
	}
	input.Manifest.Evidence = bundle.EvidenceMetadata{State: bundle.EvidenceStateLimited}
	setBatchDigest(nil, &input)
	return input
}

func setBatchDigest(t *testing.T, input *bundle.Bundle) {
	digest, err := input.FactualDigest()
	if err != nil {
		if t != nil {
			t.Fatalf("FactualDigest() error = %v", err)
		}
		panic(err)
	}
	input.Manifest.FactualDigest = digest
}

func batchIdentityRows(input bundle.Bundle) []pgx.Row {
	organizationName := input.Manifest.Organization.Name
	if organizationName == "" {
		organizationName = input.Manifest.Organization.ID
	}
	return []pgx.Row{
		fakeRow{values: []any{input.Manifest.Organization.ID, organizationName}},
		fakeRow{values: []any{input.Manifest.Source.ID, input.Manifest.Source.Name, input.Manifest.Source.Type, nil}},
		fakeRow{values: []any{
			input.Manifest.Snapshot.ID, input.Manifest.Snapshot.Revision, input.Manifest.Snapshot.Hash,
			input.Manifest.Analysis.ConfigurationID, input.Manifest.FactualDigest, input.Manifest.Snapshot.CapturedAt,
		}},
	}
}
