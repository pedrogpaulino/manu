package query

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/evidence"
	"github.com/pedrogpaulino/manu/internal/retrieval"
)

func TestComposeEvidencePackageProducesBothValidatedViews(t *testing.T) {
	scope := packageTestScope()
	unit := packageTestUnit(scope, "artifact-a", "src/a.go", "package a")
	request := PackageRequest{
		Scope: scope,
		Candidates: []PackageCandidate{{
			Fusion: packageTestFusionCandidate(scope, unit, 0.9, 1),
			Unit:   unit,
			Kind:   CandidateKindSymbol,
		}},
		Gaps:   []MaterialGap{{ID: "gap-no-runtime", Code: "runtime_not_observed", Dimension: "execution"}},
		Limits: PackageLimits{MaxUnits: 4, MaxCharacters: 1_000, MaxTokens: 1_000, MaxUnitsPerArtifact: 4, MaxUnitsPerType: 4},
	}

	composition, err := ComposeEvidencePackage(context.Background(), request)
	if err != nil {
		t.Fatalf("ComposeEvidencePackage() error = %v", err)
	}
	if err := composition.Validate(); err != nil {
		t.Fatalf("composition.Validate() error = %v", err)
	}
	if composition.UnitCount != 1 || composition.CharacterCount != int64(len([]rune(unit.Content))) {
		t.Fatalf("composition counts = %#v", composition)
	}
	if composition.TokenEstimate != EstimatePackageTokens(unit.Content, composition.Configuration.Limits.CharactersPerToken) {
		t.Fatalf("token estimate = %d", composition.TokenEstimate)
	}
	if len(composition.GatewayPackage.Evidence) != 1 || composition.GatewayPackage.Evidence[0].Content != unit.Content {
		t.Fatalf("gateway evidence = %#v", composition.GatewayPackage.Evidence)
	}
	if composition.GatewayPackage.Gaps[0] != "gap-no-runtime" {
		t.Fatalf("gateway gaps = %#v", composition.GatewayPackage.Gaps)
	}
	if composition.Audits[0].Reason != PackageReasonIncluded || !composition.Audits[0].Included {
		t.Fatalf("audit = %#v", composition.Audits)
	}
	if composition.Configuration.PolicyMode != "unit_decisions" || composition.Configuration.Digest == "" {
		t.Fatalf("configuration = %#v", composition.Configuration)
	}
}

func TestComposeEvidencePackageUsesCanonicalEvidenceIdentityForInspection(t *testing.T) {
	scope := packageTestScope()
	unit := packageTestUnit(scope, "a9", "x.go", "canonical evidence")
	canonicalID := packageTestUUID(9)
	composition, err := ComposeEvidencePackage(context.Background(), PackageRequest{
		Scope: scope,
		Candidates: []PackageCandidate{{
			Fusion:              packageTestFusionCandidate(scope, unit, 1, 1),
			Unit:                unit,
			CanonicalEvidenceID: canonicalID,
		}},
	})
	if err != nil {
		t.Fatalf("ComposeEvidencePackage() invalid bridge error = %v", err)
	}
	if len(composition.ValidationPackage.Evidence) != 0 || composition.Audits[0].Reason != PackageReasonInvalidCandidate {
		t.Fatalf("invalid canonical bridge was not excluded: %#v", composition.Audits)
	}
	// The canonical bridge is only valid when the fusion row carries the same
	// persisted UUID. This guards against silently citing one identity while
	// storing another.
	candidate := packageTestFusionCandidate(scope, unit, 1, 1)
	candidate.EvidenceID = canonicalID
	candidate.Provenance.EvidenceID = canonicalID
	composition, err = ComposeEvidencePackage(context.Background(), PackageRequest{
		Scope: scope,
		Candidates: []PackageCandidate{{
			Fusion:              candidate,
			Unit:                unit,
			CanonicalEvidenceID: canonicalID,
		}},
	})
	if err != nil {
		t.Fatalf("ComposeEvidencePackage() canonical bridge error = %v", err)
	}
	if len(composition.ValidationPackage.Evidence) == 0 {
		t.Fatalf("canonical bridge produced no evidence: audits=%#v fusion=%#v", composition.Audits, candidate)
	}
	if got := composition.ValidationPackage.Evidence[0].ID; got != canonicalID {
		t.Fatalf("validation evidence id = %q, want %q", got, canonicalID)
	}
}

func TestComposeEvidencePackageJoinsFusedCandidatesAndUnits(t *testing.T) {
	scope := packageTestScope()
	unit := packageTestUnit(scope, "artifact-a", "src/a.go", "joined input")
	composition, err := ComposeEvidencePackage(context.Background(), PackageRequest{
		Scope:           scope,
		FusedCandidates: []retrieval.FusionCandidate{packageTestFusionCandidate(scope, unit, 1, 1)},
		EvidenceUnits:   []evidence.EvidenceUnit{unit},
	})
	if err != nil {
		t.Fatalf("ComposeEvidencePackage() error = %v", err)
	}
	if len(composition.GatewayPackage.Evidence) != 1 || composition.GatewayPackage.Evidence[0].ID != unit.ID {
		t.Fatalf("joined package = %#v", composition.GatewayPackage)
	}
}

func TestComposeEvidencePackageAppliesTransferPolicyWithoutLeakingDeniedContent(t *testing.T) {
	scope := packageTestScope()
	allowed := packageTestUnit(scope, "artifact-a", "src/a.go", "safe content")
	denied := packageTestUnit(scope, "artifact-b", "src/b.go", "denied content")
	denied.ExternalTransfer = evidence.DecisionDeny
	redacted := packageTestUnit(scope, "artifact-c", "src/c.go", "redacted content")
	redacted.ExternalTransfer = evidence.DecisionRedact

	composition, err := ComposeEvidencePackage(context.Background(), PackageRequest{
		Scope: scope,
		Candidates: []PackageCandidate{
			{Fusion: packageTestFusionCandidate(scope, allowed, 0.7, 1), Unit: allowed, Kind: CandidateKindText},
			{Fusion: packageTestFusionCandidate(scope, denied, 0.6, 2), Unit: denied, Kind: CandidateKindText},
			{Fusion: packageTestFusionCandidate(scope, redacted, 0.5, 3), Unit: redacted, Kind: CandidateKindText},
		},
		TransferPolicy: &evidence.Policy{
			Installation: evidence.PolicyLayer{Persist: evidence.DecisionAllow, ExternalTransfer: evidence.DecisionAllow},
		},
	})
	if err != nil {
		t.Fatalf("ComposeEvidencePackage() error = %v", err)
	}
	if len(composition.GatewayPackage.Evidence) != 2 {
		t.Fatalf("gateway evidence count = %d, want 2", len(composition.GatewayPackage.Evidence))
	}
	for _, item := range composition.GatewayPackage.Evidence {
		if item.ID == denied.ID {
			t.Fatalf("denied evidence entered gateway package")
		}
		if item.ID == redacted.ID && item.Content != evidence.RedactedContent {
			t.Fatalf("redacted content = %q", item.Content)
		}
	}
	var deniedReason, redactedIncluded bool
	for _, audit := range composition.Audits {
		if audit.EvidenceID == denied.ID {
			deniedReason = audit.Reason == PackageReasonTransferDenied
		}
		if audit.EvidenceID == redacted.ID {
			redactedIncluded = audit.Included && audit.Redacted
		}
	}
	if !deniedReason || !redactedIncluded {
		t.Fatalf("policy audits = %#v", composition.Audits)
	}
	encoded, _ := json.Marshal(composition.Audits)
	if strings.Contains(string(encoded), "denied content") || strings.Contains(string(encoded), "redacted content") {
		t.Fatal("audit contains source content")
	}
}

func TestComposeEvidencePackageExcludesOmittedAndProhibitedUnits(t *testing.T) {
	scope := packageTestScope()
	omitted := packageTestUnit(scope, "artifact-a", "src/a.go", "omitted")
	omitted.ContentState = evidence.ContentStateOmitted
	omitted.Content = ""
	omitted.ContentBytes = 0
	omitted.ContentCharacters = 0
	omitted.ExternalTransfer = evidence.DecisionDeny
	prohibited := packageTestUnit(scope, "artifact-b", "src/b.go", "prohibited")
	prohibited.ContentState = evidence.ContentStateOmitted
	prohibited.Content = ""
	prohibited.ContentBytes = 0
	prohibited.ContentCharacters = 0
	prohibited.Classification = evidence.ClassificationProhibited
	prohibited.Findings = []string{evidence.FindingProhibited}
	prohibited.ExternalTransfer = evidence.DecisionDeny
	// The fields used to derive EvidenceID include classification/findings.
	omitted.ID = evidence.EvidenceID(omitted)
	prohibited.ID = evidence.EvidenceID(prohibited)
	if err := omitted.Validate(); err != nil {
		t.Fatalf("omitted fixture invalid: %v", err)
	}
	if err := prohibited.Validate(); err != nil {
		t.Fatalf("prohibited fixture invalid: %v", err)
	}

	composition, err := ComposeEvidencePackage(context.Background(), PackageRequest{
		Scope: scope,
		Candidates: []PackageCandidate{
			{Fusion: packageTestFusionCandidate(scope, omitted, 0.9, 1), Unit: omitted, Kind: CandidateKindText},
			{Fusion: packageTestFusionCandidate(scope, prohibited, 0.8, 2), Unit: prohibited, Kind: CandidateKindText},
		},
	})
	if err != nil {
		t.Fatalf("ComposeEvidencePackage() error = %v", err)
	}
	if len(composition.GatewayPackage.Evidence) != 0 {
		t.Fatalf("gateway evidence = %#v", composition.GatewayPackage.Evidence)
	}
	reasons := make(map[string]PackageReasonCode)
	for _, audit := range composition.Audits {
		reasons[audit.EvidenceID] = audit.Reason
	}
	if reasons[omitted.ID] != PackageReasonContentOmitted || reasons[prohibited.ID] != PackageReasonContentProhibited {
		t.Fatalf("reasons = %#v", reasons)
	}
}

func TestComposeEvidencePackageEnforcesBudgetsDiversityAndDeduplication(t *testing.T) {
	scope := packageTestScope()
	first := packageTestUnit(scope, "artifact-a", "src/a.go", "same content")
	duplicate := packageTestUnit(scope, "artifact-b", "src/b.go", "same content")
	second := packageTestUnit(scope, "artifact-a", "src/c.go", "second content")
	third := packageTestUnit(scope, "artifact-c", "src/d.go", "third content")
	fourth := packageTestUnit(scope, "artifact-d", "src/e.go", "fourth content")

	composition, err := ComposeEvidencePackage(context.Background(), PackageRequest{
		Scope: scope,
		Candidates: []PackageCandidate{
			{Fusion: packageTestFusionCandidate(scope, first, 0.9, 1), Unit: first, Kind: CandidateKindText},
			{Fusion: packageTestFusionCandidate(scope, duplicate, 0.8, 2), Unit: duplicate, Kind: CandidateKindText},
			{Fusion: packageTestFusionCandidate(scope, second, 0.7, 3), Unit: second, Kind: CandidateKindText},
			{Fusion: packageTestFusionCandidate(scope, third, 0.6, 4), Unit: third, Kind: CandidateKindRelation},
			{Fusion: packageTestFusionCandidate(scope, fourth, 0.5, 5), Unit: fourth, Kind: CandidateKindRelation},
		},
		Limits: PackageLimits{
			MaxUnits:            2,
			MaxCharacters:       100,
			MaxBytes:            100,
			MaxTokens:           100,
			CharactersPerToken:  4,
			MaxUnitsPerArtifact: 1,
			MaxUnitsPerType:     2,
		},
	})
	if err != nil {
		t.Fatalf("ComposeEvidencePackage() error = %v", err)
	}
	if composition.UnitCount != 2 {
		t.Fatalf("unit count = %d, want 2", composition.UnitCount)
	}
	reasons := make(map[string]PackageReasonCode)
	for _, audit := range composition.Audits {
		if !audit.Included {
			reasons[audit.EvidenceID] = audit.Reason
		}
	}
	if reasons[duplicate.ID] != PackageReasonDuplicateHash {
		t.Fatalf("duplicate reason = %q, audits = %#v", reasons[duplicate.ID], composition.Audits)
	}
	if reasons[second.ID] != PackageReasonArtifactDiversity {
		t.Fatalf("artifact diversity reason = %q, audits = %#v", reasons[second.ID], composition.Audits)
	}
	if reasons[fourth.ID] != PackageReasonUnitLimit {
		t.Fatalf("unit limit reason = %q, audits = %#v", reasons[fourth.ID], composition.Audits)
	}
}

func TestComposeEvidencePackageRecordsCharacterTokenAndTypeLimits(t *testing.T) {
	scope := packageTestScope()
	first := packageTestUnit(scope, "artifact-a", "src/a.go", "12345678")
	second := packageTestUnit(scope, "artifact-b", "src/b.go", "abcdefgh")
	base := PackageRequest{
		Scope: scope,
		Candidates: []PackageCandidate{
			{Fusion: packageTestFusionCandidate(scope, first, 0.9, 1), Unit: first, Kind: CandidateKindText},
			{Fusion: packageTestFusionCandidate(scope, second, 0.8, 2), Unit: second, Kind: CandidateKindText},
		},
		Limits: PackageLimits{
			MaxUnits:            4,
			MaxCharacters:       100,
			MaxBytes:            100,
			MaxTokens:           100,
			CharactersPerToken:  4,
			MaxUnitsPerArtifact: 4,
			MaxUnitsPerType:     1,
		},
	}
	composition, err := ComposeEvidencePackage(context.Background(), base)
	if err != nil {
		t.Fatalf("type-limit composition error = %v", err)
	}
	if len(composition.GatewayPackage.Evidence) != 1 {
		t.Fatalf("type-limit evidence count = %d", len(composition.GatewayPackage.Evidence))
	}
	for _, audit := range composition.Audits {
		if audit.EvidenceID == second.ID && audit.Reason != PackageReasonTypeDiversity {
			t.Fatalf("type-limit audit = %#v", audit)
		}
	}

	base.Limits.MaxUnitsPerType = 4
	base.Limits.MaxCharacters = 4
	composition, err = ComposeEvidencePackage(context.Background(), base)
	if err != nil {
		t.Fatalf("character-limit composition error = %v", err)
	}
	if len(composition.GatewayPackage.Evidence) != 0 {
		t.Fatalf("character-limit evidence = %#v", composition.GatewayPackage.Evidence)
	}
	for _, audit := range composition.Audits {
		if audit.Reason != PackageReasonCharacterLimit {
			t.Fatalf("character-limit audit = %#v", composition.Audits)
		}
	}

	base.Limits.MaxCharacters = 100
	base.Limits.MaxTokens = 1
	composition, err = ComposeEvidencePackage(context.Background(), base)
	if err != nil {
		t.Fatalf("token-limit composition error = %v", err)
	}
	if len(composition.GatewayPackage.Evidence) != 0 {
		t.Fatalf("token-limit evidence = %#v", composition.GatewayPackage.Evidence)
	}
	for _, audit := range composition.Audits {
		if audit.Reason != PackageReasonTokenLimit {
			t.Fatalf("token-limit audit = %#v", composition.Audits)
		}
	}
}

func TestComposeEvidencePackageScopeMismatchIsExcludedDeterministically(t *testing.T) {
	scope := packageTestScope()
	other := Scope{OrganizationID: packageTestUUID(9), SourceID: packageTestUUID(10), SnapshotID: packageTestUUID(11)}
	unit := packageTestUnit(other, "artifact-a", "src/a.go", "out of scope")
	composition, err := ComposeEvidencePackage(context.Background(), PackageRequest{
		Scope: scope,
		Candidates: []PackageCandidate{{
			Fusion: packageTestFusionCandidate(other, unit, 1, 1),
			Unit:   unit,
			Kind:   CandidateKindText,
		}},
	})
	if err != nil {
		t.Fatalf("ComposeEvidencePackage() error = %v", err)
	}
	if len(composition.GatewayPackage.Evidence) != 0 || len(composition.ValidationPackage.Evidence) != 0 {
		t.Fatal("out-of-scope evidence entered package")
	}
	if len(composition.Audits) != 1 || composition.Audits[0].Reason != PackageReasonScopeMismatch {
		t.Fatalf("scope audit = %#v", composition.Audits)
	}
}

func TestComposeEvidencePackageIsIndependentOfInputOrdering(t *testing.T) {
	scope := packageTestScope()
	first := packageTestUnit(scope, "artifact-b", "src/b.go", "first")
	second := packageTestUnit(scope, "artifact-a", "src/a.go", "second")
	base := PackageRequest{
		Scope: scope,
		Candidates: []PackageCandidate{
			{Fusion: packageTestFusionCandidate(scope, first, 0.8, 2), Unit: first, Kind: CandidateKindText},
			{Fusion: packageTestFusionCandidate(scope, second, 0.8, 1), Unit: second, Kind: CandidateKindSymbol},
		},
		Gaps: []MaterialGap{
			{ID: "gap-z", Code: "z"},
			{ID: "gap-a", Code: "a"},
		},
	}
	reversed := base
	reversed.Candidates = []PackageCandidate{base.Candidates[1], base.Candidates[0]}
	reversed.Gaps = []MaterialGap{base.Gaps[1], base.Gaps[0]}

	left, err := ComposeEvidencePackage(context.Background(), base)
	if err != nil {
		t.Fatalf("left composition error = %v", err)
	}
	right, err := ComposeEvidencePackage(context.Background(), reversed)
	if err != nil {
		t.Fatalf("right composition error = %v", err)
	}
	if !reflect.DeepEqual(left, right) {
		t.Fatalf("composition depends on input order:\nleft=%#v\nright=%#v", left, right)
	}
}

func TestComposeEvidencePackageRejectsInvalidScopeAndHonorsCancellation(t *testing.T) {
	_, err := ComposeEvidencePackage(context.Background(), PackageRequest{})
	if !errors.Is(err, ErrPackageScopeMismatch) {
		t.Fatalf("invalid scope error = %v, want scope mismatch", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = ComposeEvidencePackage(cancelled, PackageRequest{Scope: packageTestScope()})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled error = %v, want context canceled", err)
	}
}

func packageTestScope() Scope {
	return Scope{OrganizationID: packageTestUUID(1), SourceID: packageTestUUID(2), SnapshotID: packageTestUUID(3)}
}

func packageTestUUID(value byte) string {
	return "00000000-0000-0000-0000-00000000000" + string([]byte{'0' + value})
}

func packageTestUnit(scope Scope, artifactID, path, content string) evidence.EvidenceUnit {
	unit := evidence.EvidenceUnit{
		Version:        evidence.Version,
		OrganizationID: scope.OrganizationID,
		SourceID:       scope.SourceID,
		SnapshotID:     scope.SnapshotID,
		ArtifactID:     artifactID,
		Contribution: evidence.ContributionRef{
			ID:              "contribution-" + artifactID,
			ArtifactID:      artifactID,
			AnalyzerID:      "generic",
			AnalyzerVersion: "v1",
			Method:          "paragraph",
		},
		Locator: contract.Locator{
			SourceID:   scope.SourceID,
			ArtifactID: artifactID,
			Path:       path,
			StartLine:  1,
			EndLine:    1,
		},
		ContentState:      evidence.ContentStatePresent,
		Content:           content,
		ContentHash:       evidence.ContentDigest(content),
		ContentBytes:      int64(len([]byte(content))),
		ContentCharacters: int64(len([]rune(content))),
		Persist:           evidence.DecisionAllow,
		ExternalTransfer:  evidence.DecisionAllow,
		Classification:    evidence.ClassificationSafeText,
	}
	unit.ID = evidence.EvidenceID(unit)
	return unit
}

func packageTestFusionCandidate(scope Scope, unit evidence.EvidenceUnit, score float64, rank int) retrieval.FusionCandidate {
	return retrieval.FusionCandidate{
		EvidenceID:     unit.ID,
		OrganizationID: scope.OrganizationID,
		SourceID:       scope.SourceID,
		SnapshotID:     scope.SnapshotID,
		Score:          score,
		Rank:           rank,
		Provenance: retrieval.FusionProvenance{
			EvidenceID:          unit.ID,
			OrganizationID:      scope.OrganizationID,
			SourceID:            scope.SourceID,
			SnapshotID:          scope.SnapshotID,
			EvidenceContentHash: unit.ContentHash,
		},
	}
}
