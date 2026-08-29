package evaluation

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pedrogpaulino/manu/internal/contract"
)

func TestDirectSourceExecutorReadsOnlyScopedArtifactsAndMatchesObservedEvidence(t *testing.T) {
	root := t.TempDir()
	writeDirectSourceTestFile(t, filepath.Join(root, "main.go"), "manifest: BookingResource\nroute: /bookings\n")
	writeDirectSourceTestFile(t, filepath.Join(root, "outside.go"), "secret-value\n")
	request := directSourceTestRequest(t, VariantDirectSource, "main.go", "manifest", 0, 0)

	executor, err := NewDirectSourceExecutor(DirectSourceExecutorConfig{
		Root:   root,
		Limits: DirectSourceLimits{MaxFiles: 2, MaxBytes: 1 << 10, MaxFileBytes: 1 << 10},
	})
	if err != nil {
		t.Fatalf("NewDirectSourceExecutor() error = %v", err)
	}
	result, err := executor.Execute(context.Background(), request)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.Status != VariantStatusLimited || result.Conclusion != VariantConclusionPartial || result.OutputDigest == "" {
		t.Fatalf("result = %#v", result)
	}
	if len(result.EvidenceIDs) != 1 || result.EvidenceIDs[0] != "eval-ctx-manifest" {
		t.Fatalf("matched evidence = %#v", result.EvidenceIDs)
	}
	if len(result.ClaimIDs) != 0 || len(result.GapIDs) != 0 || len(result.Citations) != 0 {
		t.Fatalf("rubric identities leaked into result = %#v", result)
	}
	if result.Metrics == nil || result.Metrics.ToolCalls == nil || result.Metrics.FilesRead == nil || result.Metrics.BytesRead == nil || result.Metrics.Duration == nil {
		t.Fatalf("observed metrics missing = %#v", result.Metrics)
	}
	if *result.Metrics.FilesRead != 1 || *result.Metrics.BytesRead <= 0 || *result.Metrics.ToolCalls != 1 {
		t.Fatalf("observed metrics = %#v", result.Metrics)
	}
	if result.Metrics.MeasuredTokens != nil || result.Metrics.EstimatedTokens != nil || result.Metrics.ModelCalls != nil || result.Metrics.EstimatedCost != nil || result.Metrics.ActualCost != nil {
		t.Fatalf("unobserved metrics invented = %#v", result.Metrics)
	}
	if !containsDirectSourceLimitation(result.Limitations, DirectSourceGenerationNotExecuted) {
		t.Fatalf("limitations = %#v", result.Limitations)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "manifest: BookingResource") || strings.Contains(string(encoded), "secret-value") || strings.Contains(string(encoded), root) {
		t.Fatalf("result leaked source/root data: %s", encoded)
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("result validation = %v", err)
	}
}

func TestDirectSourceExecutorRejectsTraversalAndExternalSymlinkWithoutLeakingPath(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "private.go")
	writeDirectSourceTestFile(t, outsideFile, "secret-value\n")
	link := filepath.Join(root, "link.go")
	if err := os.Symlink(outsideFile, link); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}
	executor, err := NewDirectSourceExecutor(DirectSourceExecutorConfig{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	for _, artifact := range []string{"../private.go", "link.go"} {
		request := directSourceTestRequest(t, VariantDirectSource, "link.go", "manifest", 0, 0)
		request.Case.Scope.Artifacts = []string{artifact}
		_, err := executor.Execute(context.Background(), request)
		if !errors.Is(err, ErrDirectSourceUnsafePath) {
			t.Fatalf("artifact %q error = %v, want unsafe path", artifact, err)
		}
		if strings.Contains(err.Error(), "private.go") || strings.Contains(err.Error(), root) || strings.Contains(err.Error(), "secret-value") {
			t.Fatalf("unsafe error leaked data: %v", err)
		}
	}
}

func TestDirectSourceExecutorReportsBoundedPartialReadAndDoesNotMatchIncompleteRange(t *testing.T) {
	root := t.TempDir()
	writeDirectSourceTestFile(t, filepath.Join(root, "main.go"), "manifest: BookingResource\nsecond line\nthird line\n")
	request := directSourceTestRequest(t, VariantDirectSource, "main.go", "manifest", 1, 3)
	executor, err := NewDirectSourceExecutor(DirectSourceExecutorConfig{
		Root:   root,
		Limits: DirectSourceLimits{MaxFiles: 1, MaxBytes: 8, MaxFileBytes: 8},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := executor.Execute(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.EvidenceIDs) != 0 {
		t.Fatalf("incomplete file matched evidence = %#v", result.EvidenceIDs)
	}
	if !containsDirectSourceLimitation(result.Limitations, DirectSourceLimitsReached) {
		t.Fatalf("bounded result limitations = %#v", result.Limitations)
	}
	if result.Metrics == nil || result.Metrics.BytesRead == nil || *result.Metrics.BytesRead != 8 {
		t.Fatalf("bounded bytes = %#v", result.Metrics)
	}
}

func TestDirectSourceExecutorDoesNotMatchLocatorMemberOutsideLineRange(t *testing.T) {
	root := t.TempDir()
	writeDirectSourceTestFile(t, filepath.Join(root, "main.go"), "manifest: outside\ninside selected range\n")
	request := directSourceTestRequest(t, VariantDirectSource, "main.go", "manifest", 2, 2)
	executor, err := NewDirectSourceExecutor(DirectSourceExecutorConfig{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	result, err := executor.Execute(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.EvidenceIDs) != 0 {
		t.Fatalf("member outside locator range matched evidence = %#v", result.EvidenceIDs)
	}
}

func TestDirectSourceExecutorValidatesReadOnlyPolicyAndCancellation(t *testing.T) {
	root := t.TempDir()
	writeDirectSourceTestFile(t, filepath.Join(root, "main.go"), "manifest\n")
	executor, err := NewDirectSourceExecutor(DirectSourceExecutorConfig{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	request := directSourceTestRequest(t, VariantDirectSource, "main.go", "manifest", 0, 0)
	for name, mutate := range map[string]func(*EvaluationPolicy){
		"network":  func(policy *EvaluationPolicy) { policy.NetworkAccess = "allow" },
		"mutation": func(policy *EvaluationPolicy) { policy.MutationAccess = "read-only" },
		"transfer": func(policy *EvaluationPolicy) { policy.ExternalTransfer = "allow" },
	} {
		invalid := request.Clone()
		mutate(&invalid.Policy)
		if _, err := executor.Execute(context.Background(), invalid); !errors.Is(err, ErrInvalidVariantRequest) {
			t.Fatalf("%s policy error = %v", name, err)
		}
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := executor.Execute(canceled, request); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
}

func TestDirectSourceExecutorRejectsInvalidRootAndSafeErrors(t *testing.T) {
	if _, err := NewDirectSourceExecutor(DirectSourceExecutorConfig{Root: "relative"}); !errors.Is(err, ErrInvalidDirectSourceExecutor) {
		t.Fatalf("relative root error = %v", err)
	}
	root := t.TempDir()
	if _, err := NewDirectSourceExecutor(DirectSourceExecutorConfig{Root: root, Limits: DirectSourceLimits{MaxFiles: 0, MaxBytes: 1, MaxFileBytes: 1}}); !errors.Is(err, ErrInvalidDirectSourceExecutor) {
		t.Fatalf("invalid limits error = %v", err)
	}
}

func directSourceTestRequest(t *testing.T, kind VariantKind, artifact, member string, startLine, endLine int) VariantExecutionRequest {
	t.Helper()
	cases := loadCurrentEvaluationFixture(t)
	item := cloneCase(cases.Cases[0])
	item.Scope.Artifacts = []string{artifact}
	item.Inclusions = []ScopeItem{{Ref: artifact, Reason: "test fixture"}}
	item.ExpectedEvidence = []ExpectedEvidence{{
		EvidenceID: "eval-ctx-manifest", Kind: "manifest",
		Locator: &contract.Locator{Path: artifact, Member: member, StartLine: startLine, EndLine: endLine},
	}}
	for index := range item.AcceptableClaims {
		item.AcceptableClaims[index].EvidenceIDs = []string{"eval-ctx-manifest"}
	}
	for index := range item.Criteria.Items {
		if item.Criteria.Items[index].Kind == CriterionCorrectness || item.Criteria.Items[index].Kind == CriterionEvidence || item.Criteria.Items[index].Kind == CriterionCitation {
			item.Criteria.Items[index].EvidenceIDs = []string{"eval-ctx-manifest"}
		}
	}
	for index := range item.Variants {
		if item.Variants[index].Kind == kind {
			return mustDirectSourceBuildRequest(t, item, item.Variants[index])
		}
	}
	t.Fatalf("variant %q not found", kind)
	return VariantExecutionRequest{}
}

func mustDirectSourceBuildRequest(t *testing.T, item EvaluationCase, variant EvaluationVariant) VariantExecutionRequest {
	t.Helper()
	request, err := buildVariantRequest(item, variant)
	if err != nil {
		t.Fatalf("buildVariantRequest() error = %v", err)
	}
	return request
}

func writeDirectSourceTestFile(t *testing.T, filePath, content string) {
	t.Helper()
	if err := os.WriteFile(filePath, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", filePath, err)
	}
}

func containsDirectSourceLimitation(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
