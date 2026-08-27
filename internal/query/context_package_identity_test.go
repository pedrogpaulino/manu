package query

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestFinalizeContextPackageIsDeterministicAndValid(t *testing.T) {
	input := contextPackageIdentityTestInput()
	binding := contextPackageIdentityTestBinding(input)

	got, err := FinalizeContextPackage(context.Background(), input, binding)
	if err != nil {
		t.Fatalf("FinalizeContextPackage() error = %v", err)
	}
	again, err := FinalizeContextPackage(context.Background(), input, binding)
	if err != nil {
		t.Fatalf("FinalizeContextPackage() second error = %v", err)
	}
	if !reflect.DeepEqual(got, again) {
		t.Fatalf("FinalizeContextPackage() is not deterministic")
	}
	if got.ID != "context-"+got.Digest || !isSHA256(got.Digest) {
		t.Fatalf("final identity = id %q digest %q", got.ID, got.Digest)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("final package Validate() error = %v", err)
	}

	policy := ContextPolicyResult{
		PolicyDigest:    binding.PolicyDigest,
		ContinuationIDs: binding.PolicyContinuationIDs,
		PolicyFiltered:  binding.PolicyFiltered,
	}
	if want := contextE2EPackageDigest(t, input, policy); got.Digest != want {
		t.Fatalf("digest = %q, want extracted helper digest %q", got.Digest, want)
	}
}

func TestFinalizeContextPackageMaterialAndBindingChangeIdentity(t *testing.T) {
	input := contextPackageIdentityTestInput()
	binding := contextPackageIdentityTestBinding(input)
	baseline, err := FinalizeContextPackage(context.Background(), input, binding)
	if err != nil {
		t.Fatalf("baseline FinalizeContextPackage() error = %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*ContextPackage, *ContextPackageIdentityBinding)
	}{
		{name: "revision", mutate: func(p *ContextPackage, _ *ContextPackageIdentityBinding) {
			p.Revision = "revision-2"
			p.Continuation.SnapshotRevision = "revision-2"
		}},
		{name: "intent", mutate: func(p *ContextPackage, _ *ContextPackageIdentityBinding) {
			p.Intent.Question = "which source explains this?"
		}},
		{name: "limits", mutate: func(p *ContextPackage, _ *ContextPackageIdentityBinding) { p.Limits.MaxTokens++ }},
		{name: "item", mutate: func(p *ContextPackage, _ *ContextPackageIdentityBinding) { p.Items[0].Locator.Path = "src/other.go" }},
		{name: "relation", mutate: func(p *ContextPackage, _ *ContextPackageIdentityBinding) { p.Relations[0].Predicate = "call" }},
		{name: "coverage", mutate: func(p *ContextPackage, _ *ContextPackageIdentityBinding) { p.Coverage[0].Message = "changed coverage" }},
		{name: "gap", mutate: func(p *ContextPackage, _ *ContextPackageIdentityBinding) { p.Gaps[0].Message = "changed gap" }},
		{name: "degradation", mutate: func(p *ContextPackage, _ *ContextPackageIdentityBinding) {
			p.Degradations[0].Code = ContextDegradationTextUnavailable
		}},
		{name: "audit", mutate: func(p *ContextPackage, _ *ContextPackageIdentityBinding) { p.Audit[0].Score = 0.94 }},
		{name: "accounting", mutate: func(p *ContextPackage, _ *ContextPackageIdentityBinding) { p.TokenEstimate++ }},
		{name: "continuation", mutate: func(p *ContextPackage, _ *ContextPackageIdentityBinding) { p.Continuation.Token = "cursor-context-2" }},
		{name: "policy digest", mutate: func(_ *ContextPackage, b *ContextPackageIdentityBinding) { b.PolicyDigest = strings.Repeat("b", 64) }},
		{name: "policy continuation order", mutate: func(_ *ContextPackage, b *ContextPackageIdentityBinding) {
			b.PolicyContinuationIDs[0], b.PolicyContinuationIDs[1] = b.PolicyContinuationIDs[1], b.PolicyContinuationIDs[0]
		}},
		{name: "policy filtered", mutate: func(_ *ContextPackage, b *ContextPackageIdentityBinding) { b.PolicyFiltered = !b.PolicyFiltered }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mutated := input.Clone()
			mutatedBinding := ContextPackageIdentityBinding{
				PolicyDigest:          binding.PolicyDigest,
				PolicyContinuationIDs: append([]string(nil), binding.PolicyContinuationIDs...),
				PolicyFiltered:        binding.PolicyFiltered,
			}
			tt.mutate(&mutated, &mutatedBinding)
			got, err := FinalizeContextPackage(context.Background(), mutated, mutatedBinding)
			if err != nil {
				t.Fatalf("mutated FinalizeContextPackage() error = %v", err)
			}
			if got.Digest == baseline.Digest {
				t.Fatalf("mutated %s retained digest %q", tt.name, got.Digest)
			}
		})
	}
}

func TestFinalizeContextPackageRejectsIdentityAndBindingTampering(t *testing.T) {
	input := contextPackageIdentityTestInput()
	binding := contextPackageIdentityTestBinding(input)

	tests := []struct {
		name   string
		mutate func(*ContextPackage, *ContextPackageIdentityBinding)
	}{
		{name: "preexisting id", mutate: func(p *ContextPackage, _ *ContextPackageIdentityBinding) { p.ID = "context-existing" }},
		{name: "preexisting digest", mutate: func(p *ContextPackage, _ *ContextPackageIdentityBinding) { p.Digest = strings.Repeat("a", 64) }},
		{name: "invalid policy digest", mutate: func(_ *ContextPackage, b *ContextPackageIdentityBinding) { b.PolicyDigest = "not-a-sha256" }},
		{name: "unknown continuation id", mutate: func(_ *ContextPackage, b *ContextPackageIdentityBinding) {
			b.PolicyContinuationIDs[0] = "context-unknown"
		}},
		{name: "duplicate continuation id", mutate: func(_ *ContextPackage, b *ContextPackageIdentityBinding) {
			b.PolicyContinuationIDs[1] = b.PolicyContinuationIDs[0]
		}},
		{name: "invalid package", mutate: func(p *ContextPackage, _ *ContextPackageIdentityBinding) { p.Items[0].ID = "" }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mutated := input.Clone()
			mutatedBinding := ContextPackageIdentityBinding{
				PolicyDigest:          binding.PolicyDigest,
				PolicyContinuationIDs: append([]string(nil), binding.PolicyContinuationIDs...),
				PolicyFiltered:        binding.PolicyFiltered,
			}
			tt.mutate(&mutated, &mutatedBinding)
			_, err := FinalizeContextPackage(context.Background(), mutated, mutatedBinding)
			if !errors.Is(err, ErrInvalidContextPackageIdentity) {
				t.Fatalf("error = %v, want ErrInvalidContextPackageIdentity", err)
			}
		})
	}
}

func TestFinalizeContextPackageHonorsCancellationAndImmutability(t *testing.T) {
	input := contextPackageIdentityTestInput()
	binding := contextPackageIdentityTestBinding(input)
	inputBefore := input.Clone()
	bindingBefore := ContextPackageIdentityBinding{
		PolicyDigest:          binding.PolicyDigest,
		PolicyContinuationIDs: append([]string(nil), binding.PolicyContinuationIDs...),
		PolicyFiltered:        binding.PolicyFiltered,
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := FinalizeContextPackage(canceled, input, binding); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled error = %v, want context.Canceled", err)
	}

	got, err := FinalizeContextPackage(context.Background(), input, binding)
	if err != nil {
		t.Fatalf("FinalizeContextPackage() error = %v", err)
	}
	got.Items[0].SupportIDs[0] = "mutated-result"
	got.Continuation.Token = "mutated-result"
	if !reflect.DeepEqual(input, inputBefore) {
		t.Fatalf("input package mutated")
	}
	if !reflect.DeepEqual(binding, bindingBefore) {
		t.Fatalf("input binding mutated")
	}
}

func contextPackageIdentityTestInput() ContextPackage {
	input := contextTestPackage()
	input.ID = ""
	input.Digest = ""
	return input
}

func contextPackageIdentityTestBinding(input ContextPackage) ContextPackageIdentityBinding {
	return ContextPackageIdentityBinding{
		PolicyDigest:          strings.Repeat("a", 64),
		PolicyContinuationIDs: []string{input.Items[0].ID, input.Items[1].ID},
	}
}
