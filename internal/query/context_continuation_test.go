package query

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestNewContextContinuationCodecRejectsShortKeys(t *testing.T) {
	t.Parallel()

	for _, key := range [][]byte{nil, {}, make([]byte, minContextContinuationKeyBytes-1)} {
		if codec, err := NewContextContinuationCodec(key); !errors.Is(err, ErrInvalidContextContinuationKey) || codec != nil {
			t.Fatalf("NewContextContinuationCodec(%d-byte key) = (%p, %v), want nil and invalid key", len(key), codec, err)
		}
	}
}

func TestContextContinuationIssueResumeValidAndOpaque(t *testing.T) {
	t.Parallel()

	codec := contextContinuationTestCodec(t)
	binding := contextContinuationTestBinding()
	sequence := contextContinuationTestSequence()
	continuation, err := codec.Issue(context.Background(), binding, sequence, 2)
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	if err := continuation.Validate(); err != nil {
		t.Fatalf("issued continuation is invalid: %v", err)
	}
	if len([]byte(continuation.Token)) > int(maxContextContinuation) {
		t.Fatalf("continuation token length = %d, want <= %d", len([]byte(continuation.Token)), maxContextContinuation)
	}

	for _, forbidden := range []string{
		binding.Scope.OrganizationID,
		binding.Scope.SourceID,
		binding.Scope.SnapshotID,
		binding.SnapshotRevision,
		binding.IntentDigest,
		binding.PolicyDigest,
		binding.AlgorithmVersion,
		binding.Ordering,
		`"scope"`,
		`"snapshot_revision"`,
		`"intent_digest"`,
		`"policy_digest"`,
		`"algorithm_version"`,
		`"ordering"`,
	} {
		if strings.Contains(continuation.Token, forbidden) {
			t.Fatalf("opaque token contains binding fragment %q in plain text", forbidden)
		}
	}

	offset, err := codec.Resume(context.Background(), continuation, binding, sequence)
	if err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	if offset != 2 {
		t.Fatalf("Resume() offset = %d, want 2", offset)
	}
}

func TestContextContinuationIssueOffsetZeroDoesNotCreateSelfRejectingCursor(t *testing.T) {
	t.Parallel()

	codec := contextContinuationTestCodec(t)
	continuation, err := codec.Issue(context.Background(), contextContinuationTestBinding(), contextContinuationTestSequence(), 0)
	if !errors.Is(err, ErrInvalidContextContinuation) {
		t.Fatalf("Issue(offset=0) error = %v, want invalid continuation", err)
	}
	if continuation.Token != "" {
		t.Fatalf("Issue(offset=0) returned a cursor token %q despite the error", continuation.Token)
	}
}

func TestContextContinuationIssueTerminalOffsetReturnsEmptyContinuation(t *testing.T) {
	t.Parallel()

	codec := contextContinuationTestCodec(t)
	sequence := contextContinuationTestSequence()
	continuation, err := codec.Issue(context.Background(), contextContinuationTestBinding(), sequence, len(sequence))
	if err != nil {
		t.Fatalf("Issue(terminal offset) error = %v", err)
	}
	if continuation != (ContextContinuation{}) {
		t.Fatalf("Issue(terminal offset) = %#v, want empty continuation", continuation)
	}
}

func TestContextContinuationCodecCopiesKeyAndInputs(t *testing.T) {
	t.Parallel()

	originalKey := contextContinuationTestKey()
	suppliedKey := append([]byte(nil), originalKey...)
	codec, err := NewContextContinuationCodec(suppliedKey)
	if err != nil {
		t.Fatalf("NewContextContinuationCodec() error = %v", err)
	}
	expectedCodec, err := NewContextContinuationCodec(originalKey)
	if err != nil {
		t.Fatalf("NewContextContinuationCodec(expected) error = %v", err)
	}
	suppliedKey[0] ^= 0xff

	binding := contextContinuationTestBinding()
	sequence := contextContinuationTestSequence()
	bindingBefore := binding
	sequenceBefore := append([]string(nil), sequence...)
	got, err := codec.Issue(context.Background(), binding, sequence, 2)
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	want, err := expectedCodec.Issue(context.Background(), binding, sequence, 2)
	if err != nil {
		t.Fatalf("Issue(expected) error = %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("key mutation changed issued continuation:\n got %#v\nwant %#v", got, want)
	}
	repeated, err := codec.Issue(context.Background(), binding, sequence, 2)
	if err != nil {
		t.Fatalf("repeated Issue() error = %v", err)
	}
	if !reflect.DeepEqual(got, repeated) {
		t.Fatalf("same Issue() input produced different continuations:\n got %#v\nwant %#v", repeated, got)
	}
	if !reflect.DeepEqual(binding, bindingBefore) {
		t.Fatalf("Issue() mutated binding: got %#v, want %#v", binding, bindingBefore)
	}
	if !reflect.DeepEqual(sequence, sequenceBefore) {
		t.Fatalf("Issue() mutated sequence: got %#v, want %#v", sequence, sequenceBefore)
	}

	continuationBefore := got
	if _, err := codec.Resume(context.Background(), got, binding, sequence); err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	if !reflect.DeepEqual(got, continuationBefore) {
		t.Fatalf("Resume() mutated continuation: got %#v, want %#v", got, continuationBefore)
	}
}

func TestContextContinuationResumeAllowsOmittedPublicMetadata(t *testing.T) {
	t.Parallel()

	codec := contextContinuationTestCodec(t)
	binding := contextContinuationTestBinding()
	sequence := contextContinuationTestSequence()
	issued, err := codec.Issue(context.Background(), binding, sequence, 1)
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	opaqueOnly := ContextContinuation{Token: issued.Token}
	offset, err := codec.Resume(context.Background(), opaqueOnly, binding, sequence)
	if err != nil {
		t.Fatalf("Resume(opaque-only) error = %v", err)
	}
	if offset != 1 {
		t.Fatalf("Resume(opaque-only) offset = %d, want 1", offset)
	}
}

func TestContextContinuationResumeRejectsAdulteratedPublicMetadata(t *testing.T) {
	t.Parallel()

	codec := contextContinuationTestCodec(t)
	binding := contextContinuationTestBinding()
	sequence := contextContinuationTestSequence()
	issued, err := codec.Issue(context.Background(), binding, sequence, 1)
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}

	otherScope := binding.Scope
	otherScope.OrganizationID = contextTestUUID(4)
	cases := []struct {
		name   string
		mutate func(*ContextContinuation)
	}{
		{name: "scope", mutate: func(value *ContextContinuation) { value.Scope = &otherScope }},
		{name: "snapshot revision", mutate: func(value *ContextContinuation) { value.SnapshotRevision = "revision-other" }},
		{name: "intent digest", mutate: func(value *ContextContinuation) { value.IntentDigest = strings.Repeat("c", 64) }},
		{name: "policy digest", mutate: func(value *ContextContinuation) { value.PolicyDigest = strings.Repeat("d", 64) }},
		{name: "algorithm version", mutate: func(value *ContextContinuation) { value.AlgorithmVersion = "algorithm-v2" }},
		{name: "ordering", mutate: func(value *ContextContinuation) { value.Ordering = "ordering-v2" }},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			adulterated := issued
			tt.mutate(&adulterated)
			if _, err := codec.Resume(context.Background(), adulterated, binding, sequence); !errors.Is(err, ErrInvalidContextContinuation) {
				t.Fatalf("Resume() error = %v, want invalid continuation", err)
			}
		})
	}
}

func TestContextContinuationResumeRejectsEachBindingMismatch(t *testing.T) {
	t.Parallel()

	codec := contextContinuationTestCodec(t)
	binding := contextContinuationTestBinding()
	sequence := contextContinuationTestSequence()
	issued, err := codec.Issue(context.Background(), binding, sequence, 1)
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}

	cases := []struct {
		name   string
		mutate func(*ContextContinuationBinding)
	}{
		{name: "organization id", mutate: func(value *ContextContinuationBinding) { value.Scope.OrganizationID = contextTestUUID(4) }},
		{name: "source id", mutate: func(value *ContextContinuationBinding) { value.Scope.SourceID = contextTestUUID(5) }},
		{name: "snapshot id", mutate: func(value *ContextContinuationBinding) { value.Scope.SnapshotID = contextTestUUID(6) }},
		{name: "snapshot revision", mutate: func(value *ContextContinuationBinding) { value.SnapshotRevision = "revision-other" }},
		{name: "intent digest", mutate: func(value *ContextContinuationBinding) { value.IntentDigest = strings.Repeat("c", 64) }},
		{name: "policy digest", mutate: func(value *ContextContinuationBinding) { value.PolicyDigest = strings.Repeat("d", 64) }},
		{name: "algorithm version", mutate: func(value *ContextContinuationBinding) { value.AlgorithmVersion = "algorithm-v2" }},
		{name: "ordering", mutate: func(value *ContextContinuationBinding) { value.Ordering = "ordering-v2" }},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			incompatible := binding
			tt.mutate(&incompatible)
			if _, err := codec.Resume(context.Background(), issued, incompatible, sequence); !errors.Is(err, ErrInvalidContextContinuation) {
				t.Fatalf("Resume() error = %v, want invalid continuation", err)
			}
		})
	}
}

func TestContextContinuationResumeRejectsSequenceDigestChanges(t *testing.T) {
	t.Parallel()

	codec := contextContinuationTestCodec(t)
	binding := contextContinuationTestBinding()
	sequence := contextContinuationTestSequence()
	issued, err := codec.Issue(context.Background(), binding, sequence, 1)
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}

	changedSequence := append([]string(nil), sequence...)
	changedSequence[1], changedSequence[2] = changedSequence[2], changedSequence[1]
	if _, err := codec.Resume(context.Background(), issued, binding, changedSequence); !errors.Is(err, ErrInvalidContextContinuation) {
		t.Fatalf("Resume(changed sequence) error = %v, want invalid continuation", err)
	}

	resigned := contextContinuationTestResignPayload(t, codec, issued.Token, func(payload *contextContinuationPayload) {
		payload.SequenceDigest = strings.Repeat("e", 64)
	})
	issued.Token = resigned
	if _, err := codec.Resume(context.Background(), issued, binding, sequence); !errors.Is(err, ErrInvalidContextContinuation) {
		t.Fatalf("Resume(changed digest) error = %v, want invalid continuation", err)
	}
}

func TestContextContinuationResumeRejectsTamperedAndMalformedTokens(t *testing.T) {
	t.Parallel()

	codec := contextContinuationTestCodec(t)
	binding := contextContinuationTestBinding()
	sequence := contextContinuationTestSequence()
	issued, err := codec.Issue(context.Background(), binding, sequence, 1)
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	parts := strings.Split(issued.Token, ".")
	if len(parts) != 2 {
		t.Fatalf("issued token has %d parts, want 2", len(parts))
	}

	trailingJSON := contextContinuationTestTokenWithTrailingJSON(t, codec, issued.Token)
	tests := []struct {
		name  string
		token func() string
	}{
		{name: "hmac tampered", token: func() string {
			return parts[0] + "." + contextContinuationTestFlipBase64Char(parts[1])
		}},
		{name: "payload tampered", token: func() string {
			return contextContinuationTestFlipBase64Char(parts[0]) + "." + parts[1]
		}},
		{name: "payload base64 malformed", token: func() string {
			return "!" + parts[0][1:] + "." + parts[1]
		}},
		{name: "signature base64 malformed", token: func() string {
			return parts[0] + ".!"
		}},
		{name: "payload truncated", token: func() string {
			return parts[0][:len(parts[0])-1] + "." + parts[1]
		}},
		{name: "signature truncated", token: func() string {
			return parts[0] + "." + parts[1][:len(parts[1])-1]
		}},
		{name: "separator truncated", token: func() string {
			return parts[0]
		}},
		{name: "trailing signed JSON", token: func() string {
			return trailingJSON
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			adulterated := issued
			adulterated.Token = tt.token()
			if _, err := codec.Resume(context.Background(), adulterated, binding, sequence); !errors.Is(err, ErrInvalidContextContinuation) {
				t.Fatalf("Resume() error = %v, want invalid continuation", err)
			}
		})
	}
}

func TestContextContinuationRejectsInvalidAndDuplicateIDsAndPageSizes(t *testing.T) {
	t.Parallel()

	codec := contextContinuationTestCodec(t)
	binding := contextContinuationTestBinding()
	cases := []struct {
		name     string
		sequence []string
	}{
		{name: "empty id", sequence: []string{"item-1", ""}},
		{name: "id with whitespace", sequence: []string{"item-1", "item 2"}},
		{name: "duplicate id", sequence: []string{"item-1", "item-1"}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := codec.PageIDs(context.Background(), binding, tt.sequence, 1, nil); !errors.Is(err, ErrInvalidContextContinuation) {
				t.Fatalf("PageIDs() error = %v, want invalid continuation", err)
			}
		})
	}

	for _, pageSize := range []int{0, maxContextItems + 1} {
		if _, err := codec.PageIDs(context.Background(), binding, contextContinuationTestSequence(), pageSize, nil); !errors.Is(err, ErrInvalidContextContinuation) {
			t.Fatalf("PageIDs(pageSize=%d) error = %v, want invalid continuation", pageSize, err)
		}
	}
}

func TestContextContinuationPagesAreDeterministicDisjointAndTerminal(t *testing.T) {
	t.Parallel()

	codec := contextContinuationTestCodec(t)
	binding := contextContinuationTestBinding()
	sequence := []string{"item-1", "item-2", "item-3", "item-4", "item-5"}

	first, err := codec.PageIDs(context.Background(), binding, sequence, 2, nil)
	if err != nil {
		t.Fatalf("PageIDs(first) error = %v", err)
	}
	repeatedFirst, err := codec.PageIDs(context.Background(), binding, sequence, 2, nil)
	if err != nil {
		t.Fatalf("PageIDs(repeated first) error = %v", err)
	}
	if !reflect.DeepEqual(first, repeatedFirst) {
		t.Fatalf("same PageIDs input produced different pages:\n got %#v\nwant %#v", repeatedFirst, first)
	}

	allIDs := append([]string(nil), first.IDs...)
	page := first
	pageNumber := 1
	for page.Continuation != nil {
		pageNumber++
		page, err = codec.PageIDs(context.Background(), binding, sequence, 2, page.Continuation)
		if err != nil {
			t.Fatalf("PageIDs(page %d) error = %v", pageNumber, err)
		}
		allIDs = append(allIDs, page.IDs...)
	}
	if pageNumber != 3 {
		t.Fatalf("page count = %d, want 3", pageNumber)
	}
	if page.Continuation != nil {
		t.Fatal("terminal page returned a continuation")
	}
	if !reflect.DeepEqual(allIDs, sequence) {
		t.Fatalf("concatenated pages = %#v, want %#v", allIDs, sequence)
	}
	seen := make(map[string]struct{}, len(allIDs))
	for _, id := range allIDs {
		if _, exists := seen[id]; exists {
			t.Fatalf("page sequence repeated ID %q", id)
		}
		seen[id] = struct{}{}
	}
}

func TestContextContinuationPageResultAndInputAreIndependent(t *testing.T) {
	t.Parallel()

	codec := contextContinuationTestCodec(t)
	binding := contextContinuationTestBinding()
	sequence := contextContinuationTestSequence()
	sequenceBefore := append([]string(nil), sequence...)
	page, err := codec.PageIDs(context.Background(), binding, sequence, 1, nil)
	if err != nil {
		t.Fatalf("PageIDs() error = %v", err)
	}
	if !reflect.DeepEqual(sequence, sequenceBefore) {
		t.Fatalf("PageIDs() mutated input sequence: got %#v, want %#v", sequence, sequenceBefore)
	}
	page.IDs[0] = "page-result-mutated"
	if sequence[0] == page.IDs[0] {
		t.Fatal("page IDs share storage with input sequence")
	}
	if page.Continuation == nil || page.Continuation.Scope == nil {
		t.Fatal("non-terminal page has no continuation scope")
	}
	continuationBefore := *page.Continuation
	if _, err := codec.PageIDs(context.Background(), binding, sequence, 1, page.Continuation); err != nil {
		t.Fatalf("PageIDs(with continuation) error = %v", err)
	}
	if !reflect.DeepEqual(*page.Continuation, continuationBefore) {
		t.Fatalf("PageIDs() mutated continuation input: got %#v, want %#v", *page.Continuation, continuationBefore)
	}
	page.Continuation.Scope.OrganizationID = contextTestUUID(9)
	if binding.Scope.OrganizationID == page.Continuation.Scope.OrganizationID {
		t.Fatal("page continuation scope shares storage with input binding")
	}
}

func TestContextContinuationRejectsCanceledContexts(t *testing.T) {
	t.Parallel()

	codec := contextContinuationTestCodec(t)
	binding := contextContinuationTestBinding()
	sequence := contextContinuationTestSequence()
	issued, err := codec.Issue(context.Background(), binding, sequence, 1)
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := codec.Issue(ctx, binding, sequence, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("Issue(canceled) error = %v, want context.Canceled", err)
	}
	if _, err := codec.Resume(ctx, issued, binding, sequence); !errors.Is(err, context.Canceled) {
		t.Fatalf("Resume(canceled) error = %v, want context.Canceled", err)
	}
	if _, err := codec.PageIDs(ctx, binding, sequence, 1, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("PageIDs(canceled) error = %v, want context.Canceled", err)
	}
}

func TestContextContinuationNilReceiverDoesNotPanic(t *testing.T) {
	t.Parallel()

	binding := contextContinuationTestBinding()
	sequence := contextContinuationTestSequence()
	validCodec := contextContinuationTestCodec(t)
	issued, err := validCodec.Issue(context.Background(), binding, sequence, 1)
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}

	var codec *ContextContinuationCodec
	if err := contextContinuationTestNoPanic(t, func() error {
		_, err := codec.Issue(context.Background(), binding, sequence, 1)
		return err
	}); err == nil {
		t.Fatal("nil receiver Issue() returned nil error")
	}
	if err := contextContinuationTestNoPanic(t, func() error {
		_, err := codec.Resume(context.Background(), issued, binding, sequence)
		return err
	}); err == nil {
		t.Fatal("nil receiver Resume() returned nil error")
	}
	if err := contextContinuationTestNoPanic(t, func() error {
		_, err := codec.PageIDs(context.Background(), binding, sequence, 1, nil)
		return err
	}); err == nil {
		t.Fatal("nil receiver PageIDs() returned nil error")
	}
}

func contextContinuationTestCodec(t *testing.T) *ContextContinuationCodec {
	t.Helper()
	codec, err := NewContextContinuationCodec(contextContinuationTestKey())
	if err != nil {
		t.Fatalf("NewContextContinuationCodec() error = %v", err)
	}
	return codec
}

func contextContinuationTestKey() []byte {
	return []byte("context-continuation-test-key-32-bytes!!")
}

func contextContinuationTestBinding() ContextContinuationBinding {
	return ContextContinuationBinding{
		Scope: Scope{
			OrganizationID: contextTestUUID(1),
			SourceID:       contextTestUUID(2),
			SnapshotID:     contextTestUUID(3),
		},
		SnapshotRevision: "revision-1",
		IntentDigest:     strings.Repeat("a", 64),
		PolicyDigest:     strings.Repeat("b", 64),
		AlgorithmVersion: "algorithm-v1",
		Ordering:         "ordering-v1",
	}
}

func contextContinuationTestSequence() []string {
	return []string{"item-1", "item-2", "item-3", "item-4"}
}

func contextContinuationTestResignPayload(t *testing.T, codec *ContextContinuationCodec, token string, mutate func(*contextContinuationPayload)) string {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		t.Fatalf("token has %d parts, want 2", len(parts))
	}
	encoded, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatalf("decode token payload: %v", err)
	}
	var payload contextContinuationPayload
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatalf("unmarshal token payload: %v", err)
	}
	mutate(&payload)
	encoded, err = json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal token payload: %v", err)
	}
	payloadPart := base64.RawURLEncoding.EncodeToString(encoded)
	signature := base64.RawURLEncoding.EncodeToString(codec.sign([]byte(payloadPart)))
	return payloadPart + "." + signature
}

func contextContinuationTestTokenWithTrailingJSON(t *testing.T, codec *ContextContinuationCodec, token string) string {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		t.Fatalf("token has %d parts, want 2", len(parts))
	}
	encoded, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatalf("decode token payload: %v", err)
	}
	encoded = append(encoded, []byte(" {}")...)
	payloadPart := base64.RawURLEncoding.EncodeToString(encoded)
	signature := base64.RawURLEncoding.EncodeToString(codec.sign([]byte(payloadPart)))
	return payloadPart + "." + signature
}

func contextContinuationTestFlipBase64Char(value string) string {
	if value == "" {
		return "A"
	}
	flipped := []byte(value)
	if flipped[0] == 'A' {
		flipped[0] = 'B'
	} else {
		flipped[0] = 'A'
	}
	return string(flipped)
}

func contextContinuationTestNoPanic(t *testing.T, call func() error) (err error) {
	t.Helper()
	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("continuation codec panicked: %v", recovered)
		}
	}()
	return call()
}
