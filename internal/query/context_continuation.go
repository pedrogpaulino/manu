package query

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	// ContextContinuationVersion identifies the signed continuation payload.
	// It is separate from ContextVersion so the wire token can evolve without
	// changing the surrounding context package contract.
	ContextContinuationVersion = "v1alpha1"

	// ContextContinuationAlgorithm identifies the stateless token codec.
	ContextContinuationAlgorithm = "hmac-sha256-v1"

	minContextContinuationKeyBytes = 32
)

var (
	// ErrInvalidContextContinuationKey identifies a key that cannot safely
	// protect a continuation token.
	ErrInvalidContextContinuationKey = errors.New("query: invalid context continuation key")
)

// ContextContinuationBinding is the complete request identity carried by a
// continuation. Every field participates in the signed payload; callers must
// provide all fields explicitly instead of relying on cursor defaults.
type ContextContinuationBinding struct {
	Scope            Scope  `json:"scope"`
	SnapshotRevision string `json:"snapshot_revision"`
	IntentDigest     string `json:"intent_digest"`
	PolicyDigest     string `json:"policy_digest"`
	AlgorithmVersion string `json:"algorithm_version"`
	Ordering         string `json:"ordering"`
}

// Validate checks the mandatory, bounded continuation binding.
func (b ContextContinuationBinding) Validate() error {
	if err := b.Scope.Validate(); err != nil {
		return ErrInvalidContextScope
	}
	if !validContextString(b.SnapshotRevision, maxContextRevisionBytes) ||
		!isSHA256(b.IntentDigest) || !isSHA256(b.PolicyDigest) ||
		!validContextID(b.AlgorithmVersion) || !validContextID(b.Ordering) {
		return ErrInvalidContextContinuation
	}
	return nil
}

// ContextContinuationPage is one deterministic page of canonical IDs. A nil
// Continuation means the page is terminal.
type ContextContinuationPage struct {
	IDs          []string             `json:"ids"`
	Continuation *ContextContinuation `json:"continuation,omitempty"`
}

// ContextContinuationCodec issues and consumes stateless HMAC-protected
// continuation tokens. The key is copied by the constructor and never
// returned by this type.
type ContextContinuationCodec struct {
	key []byte
}

// NewContextContinuationCodec constructs a codec with a private copy of key.
// Keys shorter than 32 bytes are rejected to avoid weak continuation tokens.
func NewContextContinuationCodec(key []byte) (*ContextContinuationCodec, error) {
	if len(key) < minContextContinuationKeyBytes {
		return nil, ErrInvalidContextContinuationKey
	}
	return &ContextContinuationCodec{key: append([]byte(nil), key...)}, nil
}

// contextContinuationPayload is the signed, versioned representation. The
// sequence itself is intentionally not included: its digest binds a caller's
// ordered candidate IDs without granting the token access to new data.
type contextContinuationPayload struct {
	Version          string `json:"version"`
	Scope            Scope  `json:"scope"`
	SnapshotRevision string `json:"snapshot_revision"`
	IntentDigest     string `json:"intent_digest"`
	PolicyDigest     string `json:"policy_digest"`
	AlgorithmVersion string `json:"algorithm_version"`
	Ordering         string `json:"ordering"`
	SequenceDigest   string `json:"sequence_digest"`
	NextOffset       int    `json:"next_offset"`
}

// Issue signs a continuation for sequence at nextOffset. At the terminal
// offset it returns an empty continuation and nil error; callers should expose
// that as a nil page continuation.
func (c *ContextContinuationCodec) Issue(ctx context.Context, binding ContextContinuationBinding, sequence []string, nextOffset int) (ContextContinuation, error) {
	if err := checkContinuationContext(ctx); err != nil {
		return ContextContinuation{}, err
	}
	if err := c.validate(); err != nil {
		return ContextContinuation{}, err
	}
	if err := binding.Validate(); err != nil {
		return ContextContinuation{}, err
	}
	if err := validateContinuationSequence(ctx, sequence); err != nil {
		return ContextContinuation{}, err
	}
	if nextOffset < 0 || nextOffset > len(sequence) {
		return ContextContinuation{}, ErrInvalidContextContinuation
	}
	if nextOffset == len(sequence) {
		return ContextContinuation{}, nil
	}
	if nextOffset == 0 {
		return ContextContinuation{}, ErrInvalidContextContinuation
	}

	sequenceDigest, err := continuationSequenceDigest(ctx, sequence)
	if err != nil {
		return ContextContinuation{}, err
	}
	payload := contextContinuationPayload{
		Version:          ContextContinuationVersion,
		Scope:            binding.Scope,
		SnapshotRevision: binding.SnapshotRevision,
		IntentDigest:     binding.IntentDigest,
		PolicyDigest:     binding.PolicyDigest,
		AlgorithmVersion: binding.AlgorithmVersion,
		Ordering:         binding.Ordering,
		SequenceDigest:   sequenceDigest,
		NextOffset:       nextOffset,
	}
	encodedPayload, err := json.Marshal(payload)
	if err != nil {
		return ContextContinuation{}, fmt.Errorf("%w: encode payload", ErrInvalidContextContinuation)
	}
	payloadPart := base64.RawURLEncoding.EncodeToString(encodedPayload)
	signature := c.sign([]byte(payloadPart))
	token := payloadPart + "." + base64.RawURLEncoding.EncodeToString(signature)
	if len(token) > int(maxContextContinuation) {
		return ContextContinuation{}, ErrInvalidContextContinuation
	}

	scope := binding.Scope
	return ContextContinuation{
		Token:            token,
		Scope:            &scope,
		SnapshotRevision: binding.SnapshotRevision,
		IntentDigest:     binding.IntentDigest,
		PolicyDigest:     binding.PolicyDigest,
		AlgorithmVersion: binding.AlgorithmVersion,
		Ordering:         binding.Ordering,
	}, nil
}

// Resume verifies continuation's signature, binding metadata and sequence
// digest, returning the next unread offset. Public metadata is optional for
// compatibility with opaque-token callers, but every supplied field must
// match the signed payload and binding.
func (c *ContextContinuationCodec) Resume(ctx context.Context, continuation ContextContinuation, binding ContextContinuationBinding, sequence []string) (int, error) {
	if err := checkContinuationContext(ctx); err != nil {
		return 0, err
	}
	if err := c.validate(); err != nil {
		return 0, err
	}
	if err := binding.Validate(); err != nil {
		return 0, err
	}
	if err := validateContinuationSequence(ctx, sequence); err != nil {
		return 0, err
	}
	payload, err := c.decodePayload(ctx, continuation.Token)
	if err != nil {
		return 0, err
	}
	if err := validateContinuationPayload(payload, binding, continuation, len(sequence)); err != nil {
		return 0, err
	}
	sequenceDigest, err := continuationSequenceDigest(ctx, sequence)
	if err != nil {
		return 0, err
	}
	if !constantTimeStringEqual(payload.SequenceDigest, sequenceDigest) {
		return 0, ErrInvalidContextContinuation
	}
	return payload.NextOffset, nil
}

// PageIDs returns the next deterministic page from an ordered, unique ID
// sequence. The first page uses a nil continuation; later pages consume the
// continuation returned by the previous page. Successive pages do not
// overlap; replaying a stateless token intentionally repeats its page.
func (c *ContextContinuationCodec) PageIDs(ctx context.Context, binding ContextContinuationBinding, sequence []string, pageSize int, continuation *ContextContinuation) (ContextContinuationPage, error) {
	if err := checkContinuationContext(ctx); err != nil {
		return ContextContinuationPage{}, err
	}
	if err := c.validate(); err != nil {
		return ContextContinuationPage{}, err
	}
	if err := binding.Validate(); err != nil {
		return ContextContinuationPage{}, err
	}
	if pageSize < 1 || pageSize > maxContextItems {
		return ContextContinuationPage{}, ErrInvalidContextContinuation
	}
	if err := validateContinuationSequence(ctx, sequence); err != nil {
		return ContextContinuationPage{}, err
	}

	offset := 0
	if continuation != nil {
		var err error
		offset, err = c.Resume(ctx, *continuation, binding, sequence)
		if err != nil {
			return ContextContinuationPage{}, err
		}
	}
	if offset < 0 || offset > len(sequence) {
		return ContextContinuationPage{}, ErrInvalidContextContinuation
	}
	if offset == len(sequence) {
		return ContextContinuationPage{IDs: []string{}}, nil
	}

	end := offset + pageSize
	if end < offset || end > len(sequence) {
		end = len(sequence)
	}
	page := ContextContinuationPage{IDs: append([]string(nil), sequence[offset:end]...)}
	if end < len(sequence) {
		issued, err := c.Issue(ctx, binding, sequence, end)
		if err != nil {
			return ContextContinuationPage{}, err
		}
		page.Continuation = &issued
	}
	return page, nil
}

func (c *ContextContinuationCodec) sign(payload []byte) []byte {
	hasher := hmac.New(sha256.New, c.key)
	_, _ = hasher.Write(payload)
	return hasher.Sum(nil)
}

func (c *ContextContinuationCodec) validate() error {
	if c == nil || len(c.key) < minContextContinuationKeyBytes {
		return ErrInvalidContextContinuationKey
	}
	return nil
}

func (c *ContextContinuationCodec) decodePayload(ctx context.Context, token string) (contextContinuationPayload, error) {
	if err := checkContinuationContext(ctx); err != nil {
		return contextContinuationPayload{}, err
	}
	if err := c.validate(); err != nil {
		return contextContinuationPayload{}, err
	}
	if !validContextString(token, maxContextContinuation) {
		return contextContinuationPayload{}, ErrInvalidContextContinuation
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return contextContinuationPayload{}, ErrInvalidContextContinuation
	}
	payloadPart, signaturePart := parts[0], parts[1]
	encodedPayload, err := base64.RawURLEncoding.DecodeString(payloadPart)
	if err != nil || len(encodedPayload) == 0 {
		return contextContinuationPayload{}, ErrInvalidContextContinuation
	}
	signature, err := base64.RawURLEncoding.DecodeString(signaturePart)
	if err != nil || len(signature) != sha256.Size {
		return contextContinuationPayload{}, ErrInvalidContextContinuation
	}
	if !hmac.Equal(signature, c.sign([]byte(payloadPart))) {
		return contextContinuationPayload{}, ErrInvalidContextContinuation
	}

	decoder := json.NewDecoder(strings.NewReader(string(encodedPayload)))
	decoder.DisallowUnknownFields()
	var payload contextContinuationPayload
	if err := decoder.Decode(&payload); err != nil {
		return contextContinuationPayload{}, ErrInvalidContextContinuation
	}
	var trailing struct{}
	if err := decoder.Decode(&trailing); err != io.EOF {
		return contextContinuationPayload{}, ErrInvalidContextContinuation
	}
	if err := checkContinuationContext(ctx); err != nil {
		return contextContinuationPayload{}, err
	}
	return payload, nil
}

func validateContinuationPayload(payload contextContinuationPayload, binding ContextContinuationBinding, continuation ContextContinuation, sequenceLength int) error {
	if payload.Version != ContextContinuationVersion || payload.Scope.Validate() != nil ||
		!sameScope(payload.Scope, binding.Scope) ||
		!constantTimeStringEqual(payload.SnapshotRevision, binding.SnapshotRevision) ||
		!constantTimeStringEqual(payload.IntentDigest, binding.IntentDigest) ||
		!constantTimeStringEqual(payload.PolicyDigest, binding.PolicyDigest) ||
		!constantTimeStringEqual(payload.AlgorithmVersion, binding.AlgorithmVersion) ||
		!constantTimeStringEqual(payload.Ordering, binding.Ordering) ||
		!isSHA256(payload.SequenceDigest) || payload.NextOffset < 1 || payload.NextOffset >= sequenceLength {
		return ErrInvalidContextContinuation
	}
	if continuation.Scope != nil && !sameScope(*continuation.Scope, payload.Scope) {
		return ErrInvalidContextContinuation
	}
	if continuation.SnapshotRevision != "" && !constantTimeStringEqual(continuation.SnapshotRevision, payload.SnapshotRevision) {
		return ErrInvalidContextContinuation
	}
	if continuation.IntentDigest != "" && !constantTimeStringEqual(continuation.IntentDigest, payload.IntentDigest) {
		return ErrInvalidContextContinuation
	}
	if continuation.PolicyDigest != "" && !constantTimeStringEqual(continuation.PolicyDigest, payload.PolicyDigest) {
		return ErrInvalidContextContinuation
	}
	if continuation.AlgorithmVersion != "" && !constantTimeStringEqual(continuation.AlgorithmVersion, payload.AlgorithmVersion) {
		return ErrInvalidContextContinuation
	}
	if continuation.Ordering != "" && !constantTimeStringEqual(continuation.Ordering, payload.Ordering) {
		return ErrInvalidContextContinuation
	}
	return nil
}

func validateContinuationSequence(ctx context.Context, sequence []string) error {
	if len(sequence) > maxContextItems {
		return ErrInvalidContextContinuation
	}
	seen := make(map[string]struct{}, len(sequence))
	for _, id := range sequence {
		if err := checkContinuationContext(ctx); err != nil {
			return err
		}
		if !validContextID(id) {
			return ErrInvalidContextContinuation
		}
		if _, exists := seen[id]; exists {
			return ErrInvalidContextContinuation
		}
		seen[id] = struct{}{}
	}
	return nil
}

func continuationSequenceDigest(ctx context.Context, sequence []string) (string, error) {
	canonical := make([]string, len(sequence))
	for index, id := range sequence {
		if err := checkContinuationContext(ctx); err != nil {
			return "", err
		}
		canonical[index] = id
	}
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return "", fmt.Errorf("%w: sequence digest", ErrInvalidContextContinuation)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func constantTimeStringEqual(left, right string) bool {
	return hmac.Equal([]byte(left), []byte(right))
}

func checkContinuationContext(ctx context.Context) error {
	if ctx == nil {
		return ErrInvalidContextContinuation
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}
