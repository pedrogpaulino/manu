package evaluation

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pedrogpaulino/manu/internal/aigateway"
)

type liveProviderFake struct {
	calls       []aigateway.GenerationRequest
	err         error
	badResponse bool
	badCitation bool
}

func (f *liveProviderFake) Generate(_ context.Context, request aigateway.GenerationRequest) (aigateway.GenerationResult, error) {
	f.calls = append(f.calls, request)
	if f.err != nil {
		return aigateway.GenerationResult{}, f.err
	}
	if f.badResponse {
		return aigateway.GenerationResult{ExecutionID: request.ExecutionID, RequestID: request.RequestID, Provider: "api_key=leak", Model: "password=leak", Output: aigateway.GenerationEnvelope{Version: aigateway.GenerationEnvelopeVersion, Text: "secret=leak", PackageDigest: request.Package.Digest}, Usage: aigateway.Usage{InputItems: 1, OutputItems: 1, InputTokens: 1, OutputTokens: 1}, Termination: aigateway.TerminationCompleted}, nil
	}
	ids := make([]string, 0, len(request.Package.Evidence))
	for _, evidence := range request.Package.Evidence {
		ids = append(ids, evidence.ID)
	}
	if f.badCitation {
		ids = []string{"evidence-not-in-package"}
	}
	return aigateway.GenerationResult{
		ExecutionID: request.ExecutionID, RequestID: request.RequestID,
		Provider: request.Profile.Provider, Model: "model-effective",
		Output:      aigateway.GenerationEnvelope{Version: aigateway.GenerationEnvelopeVersion, Text: "live response", PackageDigest: request.Package.Digest, EvidenceIDs: ids},
		Usage:       aigateway.Usage{InputItems: 1, OutputItems: 1, InputTokens: conservativeLiveInputTokens(request), OutputTokens: 4},
		Termination: aigateway.TerminationCompleted, Latency: time.Millisecond,
	}, nil
}

func liveTestConfig(t *testing.T, provider LiveProvider) LiveConfig {
	t.Helper()
	profile := aigateway.GenerationProfile{Provider: aigateway.ProviderOpenRouter, Model: "openai/gpt-4o-mini", Version: aigateway.GenerationProfileVersion, Protocol: aigateway.ProtocolChatCompletions, MaxOutputBytes: 4 << 10}
	return LiveConfig{
		Cases: runnerFixture(t), RunID: "live-test", TopK: 5, Repeat: true,
		OptIn: true, ConfirmPolicy: true, ConfirmTransfer: true,
		TransferPolicy: LiveTransferPolicyAllow, Provider: string(profile.Provider), RequestedModel: profile.Model,
		MaxOutputTokens: 32, Generation: profile, Timeout: time.Minute, ProviderClient: provider,
		Budget:     LiveBudgetConfig{MaxRequests: 16, MaxInputTokens: 100_000, MaxOutputTokens: 1_000, MaxCostUSD: 100_000},
		PriceTable: LivePriceTable{Version: "prices-2026-08-20", Provider: string(profile.Provider), Model: profile.Model, InputTokenUSD: 0.01, OutputTokenUSD: 0.02},
		Now:        func() time.Time { return time.Unix(0, 0).UTC() },
	}
}

func TestRunLiveRequiresOptInConfirmationPolicyPriceAndProvider(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*LiveConfig)
		want   error
	}{
		{name: "opt in", mutate: func(config *LiveConfig) { config.OptIn = false }, want: ErrLiveNotOptedIn},
		{name: "confirmation", mutate: func(config *LiveConfig) { config.ConfirmTransfer = false }, want: ErrLiveConfirmation},
		{name: "policy", mutate: func(config *LiveConfig) { config.TransferPolicy = "deny" }, want: ErrLivePolicy},
		{name: "price table", mutate: func(config *LiveConfig) { config.PriceTable.Version = "" }, want: ErrLivePriceTable},
		{name: "provider", mutate: func(config *LiveConfig) { config.ProviderClient = nil }, want: ErrLiveProvider},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			provider := &liveProviderFake{}
			config := liveTestConfig(t, provider)
			test.mutate(&config)
			_, err := RunLive(context.Background(), config)
			if !errors.Is(err, test.want) {
				t.Fatalf("RunLive() error = %v, want %v", err, test.want)
			}
			if len(provider.calls) != 0 {
				t.Fatalf("provider calls = %d, want 0", len(provider.calls))
			}
		})
	}
}

func TestRunLiveUsesProviderWithoutFallbackAndReportsSafeUsage(t *testing.T) {
	provider := &liveProviderFake{}
	report, err := RunLive(context.Background(), liveTestConfig(t, provider))
	if err != nil {
		t.Fatalf("RunLive() error = %v", err)
	}
	if report.Mode != ModeLive || report.Live == nil {
		t.Fatalf("live report identity = %#v", report)
	}
	if report.Live.Usage.Requests != len(provider.calls) || report.Live.Usage.Requests == 0 {
		t.Fatalf("live usage/calls = %#v/%d", report.Live.Usage, len(provider.calls))
	}
	for _, item := range report.Cases {
		if item.Generation.Provider == "simulated" {
			t.Fatalf("simulated generation leaked into live report: %#v", item.Generation)
		}
	}
	encoded, err := MarshalReport(report)
	if err != nil {
		t.Fatalf("MarshalReport() error = %v", err)
	}
	text := strings.ToLower(string(encoded))
	for _, unsafe := range []string{"api_key", "password", "secret=", "simulated evidence", "resposta simulada"} {
		if strings.Contains(text, unsafe) {
			t.Fatalf("live report contains unsafe marker %q", unsafe)
		}
	}
	if len(report.Live.TransferredEvidence) == 0 {
		t.Fatal("live report did not record transferred evidence hashes")
	}
	for _, item := range report.Live.TransferredEvidence {
		if item.EvidenceID == "" || !isSHA256Digest(item.ContentHash) {
			t.Fatalf("unsafe transfer reference = %#v", item)
		}
	}
	if len(provider.calls) == 0 || len(provider.calls[0].Package.Evidence) == 0 || provider.calls[0].Package.Evidence[0].Content == "" {
		t.Fatal("live generator did not receive the authorized package content in memory")
	}
	effectiveModelFound := false
	for _, item := range report.Cases {
		if item.Generation.Model == "model-effective" && item.Generation.Provider == string(aigateway.ProviderOpenRouter) {
			effectiveModelFound = true
			break
		}
	}
	if !effectiveModelFound {
		t.Fatalf("effective model was not recorded in a live case: %#v", report.Cases)
	}
	marker := provider.calls[0].Package.Evidence[0].Content
	if len(marker) > 8 && strings.Contains(string(encoded), marker) {
		t.Fatal("live report serialized raw authorized content")
	}
}

func TestRunLiveStopsBeforeNextCallWhenConservativeBudgetIsExceeded(t *testing.T) {
	provider := &liveProviderFake{}
	config := liveTestConfig(t, provider)
	config.Budget.MaxRequests = 1
	config.Budget.MaxInputTokens = 100_000
	config.Budget.MaxOutputTokens = 1_000
	report, err := RunLive(context.Background(), config)
	if err != nil {
		t.Fatalf("RunLive() error = %v", err)
	}
	if len(provider.calls) != 1 || report.Live == nil || !report.Live.Halted || report.Live.HaltCode != "live_budget_exceeded" {
		t.Fatalf("budget halt = calls=%d live=%#v", len(provider.calls), report.Live)
	}
	if report.Live.Usage.Requests != 1 {
		t.Fatalf("reserved requests = %d, want 1", report.Live.Usage.Requests)
	}
	for _, item := range report.Cases {
		if item.FailureCode == "live_budget_not_attempted" && item.Generation.Status != StageFailed {
			t.Fatalf("unattempted case was not marked failed: %#v", item)
		}
	}

	provider = &liveProviderFake{}
	config = liveTestConfig(t, provider)
	config.Budget.MaxCostUSD = 0.01
	report, err = RunLive(context.Background(), config)
	if err != nil {
		t.Fatalf("cost RunLive() error = %v", err)
	}
	if len(provider.calls) != 0 || report.Live == nil || !report.Live.Halted {
		t.Fatalf("conservative cost reservation was not enforced: calls=%d live=%#v", len(provider.calls), report.Live)
	}
}

func TestRunLiveSanitizesProviderErrorsAndInvalidResponses(t *testing.T) {
	provider := &liveProviderFake{err: errors.New("api_key=do-not-leak")}
	report, err := RunLive(context.Background(), liveTestConfig(t, provider))
	if err != nil {
		t.Fatalf("provider error RunLive() error = %v", err)
	}
	encoded, _ := MarshalReport(report)
	if strings.Contains(strings.ToLower(string(encoded)), "do-not-leak") || strings.Contains(strings.ToLower(string(encoded)), "api_key") {
		t.Fatalf("provider error leaked: %s", encoded)
	}
	failureSeen := false
	for _, item := range report.Cases {
		if item.FailureCode == "live_provider_failed" {
			failureSeen = true
			if item.Generation.Provider != "" || item.Generation.Model != "" {
				t.Fatalf("provider identity survived failed call: %#v", item.Generation)
			}
		}
	}
	if !failureSeen {
		t.Fatal("provider failure was not attributed")
	}

	provider = &liveProviderFake{badResponse: true}
	report, err = RunLive(context.Background(), liveTestConfig(t, provider))
	if err != nil {
		t.Fatalf("invalid response RunLive() error = %v", err)
	}
	encoded, _ = MarshalReport(report)
	for _, unsafe := range []string{"api_key", "password", "secret=", "secret=leak"} {
		if strings.Contains(strings.ToLower(string(encoded)), unsafe) {
			t.Fatalf("invalid response leaked %q: %s", unsafe, encoded)
		}
	}

	provider = &liveProviderFake{badCitation: true}
	report, err = RunLive(context.Background(), liveTestConfig(t, provider))
	if err != nil {
		t.Fatalf("citation response RunLive() error = %v", err)
	}
	for _, item := range report.Live.Calls {
		if item.ErrorCode == "live_response_invalid" {
			return
		}
	}
	t.Fatal("citation outside the authorized package was accepted")
}

func TestLiveBudgetLedgerReservesConservatively(t *testing.T) {
	ledger, err := NewLiveBudgetLedger(
		LiveBudgetConfig{MaxRequests: 2, MaxInputTokens: 10, MaxOutputTokens: 10, MaxCostUSD: 1},
		LivePriceTable{Version: "v1", Provider: "provider", Model: "model", InputTokenUSD: 0.01, OutputTokenUSD: 0.02},
	)
	if err != nil {
		t.Fatal(err)
	}
	reservation, err := ledger.Reserve(LiveUsageEstimate{InputTokens: 5, OutputTokens: 5})
	if err != nil {
		t.Fatal(err)
	}
	if err := reservation.Settle(LiveUsage{InputTokens: 1, OutputTokens: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Reserve(LiveUsageEstimate{InputTokens: 6, OutputTokens: 6}); !errors.Is(err, ErrLiveBudget) {
		t.Fatalf("second reservation error = %v, want budget", err)
	}
	snapshot := ledger.Snapshot()
	if snapshot.Requests != 1 || snapshot.ReservedInputTokens != 5 || snapshot.ActualInputTokens != 1 {
		t.Fatalf("ledger snapshot = %#v", snapshot)
	}
}

func TestLiveProviderFakeUsesNoNetwork(t *testing.T) {
	provider := &liveProviderFake{}
	config := liveTestConfig(t, provider)
	config.Provider = fmt.Sprintf("%s", config.Provider)
	if _, err := RunLive(context.Background(), config); err != nil {
		t.Fatal(err)
	}
}
