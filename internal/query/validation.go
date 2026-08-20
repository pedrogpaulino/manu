package query

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/pedrogpaulino/manu/internal/aigateway"
	"github.com/pedrogpaulino/manu/internal/contract"
)

var (
	// ErrInvalidPackage identifies a package context that cannot be used for
	// response validation.
	ErrInvalidPackage = errors.New("query: invalid evidence package")
	// ErrPackageMismatch identifies a response or gateway request bound to a
	// different package than the one being validated.
	ErrPackageMismatch = errors.New("query: evidence package mismatch")
	// ErrQueryMismatch identifies a response bound to a different query
	// identity than the validation context.
	ErrQueryMismatch = errors.New("query: query identity mismatch")
	// ErrRepairFailed identifies an invalid response after the one permitted
	// repair attempt.
	ErrRepairFailed = errors.New("query: response repair failed")
	// ErrInvalidRepairPolicy identifies a repair configuration outside the
	// bounded one-attempt contract.
	ErrInvalidRepairPolicy = errors.New("query: invalid repair policy")
)

const maxPackageEvidence = 10_000

// Scope is the mandatory organizational boundary for a generated response.
// It is repeated on each package evidence reference so a validator can reject
// a mixed or spoofed package without opening a database.
type Scope struct {
	OrganizationID string `json:"organization_id"`
	SourceID       string `json:"source_id"`
	SnapshotID     string `json:"snapshot_id"`
}

// PackageScope is a descriptive alias for Scope.
type PackageScope = Scope

// EvidenceReference is the bounded identity and locator made available to a
// generator. It contains no source text. ContentDigest is optional for
// callers that only retain identity metadata, but when present it is checked
// against the gateway package.
type EvidenceReference struct {
	ID             string           `json:"id"`
	OrganizationID string           `json:"organization_id"`
	SourceID       string           `json:"source_id"`
	SnapshotID     string           `json:"snapshot_id"`
	Locator        contract.Locator `json:"locator"`
	ContentDigest  string           `json:"content_digest,omitempty"`
}

// EvidenceUnitReference is a descriptive alias for EvidenceReference.
type EvidenceUnitReference = EvidenceReference

// EvidencePackage is the validation view of the exact package sent to a
// generator. The gateway package remains the transport boundary; this view
// adds the scope and structured locators required for deterministic citation
// validation.
type EvidencePackage struct {
	ID             string              `json:"id"`
	Digest         string              `json:"digest"`
	OrganizationID string              `json:"organization_id"`
	SourceID       string              `json:"source_id"`
	SnapshotID     string              `json:"snapshot_id"`
	Evidence       []EvidenceReference `json:"evidence"`
}

// Package is a concise alias for EvidencePackage.
type Package = EvidencePackage

// Scope returns the package scope as a value object.
func (p EvidencePackage) Scope() Scope {
	return Scope{OrganizationID: p.OrganizationID, SourceID: p.SourceID, SnapshotID: p.SnapshotID}
}

// Validate checks package identity, scope, locators, digests, and duplicate
// evidence IDs. It does not inspect source files or consult persistence.
func (p EvidencePackage) Validate() error {
	limits := DefaultResponseLimits()
	if err := validateOpaqueID("package id", p.ID, limits.MaxIdentifierBytes); err != nil {
		return fmt.Errorf("%w: package id", ErrInvalidPackage)
	}
	if !isSHA256(p.Digest) {
		return fmt.Errorf("%w: package digest", ErrInvalidPackage)
	}
	if err := p.Scope().Validate(); err != nil {
		return fmt.Errorf("%w: package scope", ErrInvalidPackage)
	}
	if len(p.Evidence) > maxPackageEvidence {
		return fmt.Errorf("%w: evidence count", ErrLimitExceeded)
	}
	seen := make(map[string]struct{}, len(p.Evidence))
	for _, reference := range p.Evidence {
		if err := validateEvidenceReference(reference, p.Scope(), limits); err != nil {
			return err
		}
		if _, exists := seen[reference.ID]; exists {
			return fmt.Errorf("%w: duplicate package evidence", ErrInvalidReference)
		}
		seen[reference.ID] = struct{}{}
	}
	return nil
}

// Validate checks that all scope identifiers are UUIDs, matching the wire
// citation contract established in task 8.1.
func (s Scope) Validate() error {
	for name, value := range map[string]string{
		"package organization id": s.OrganizationID,
		"package source id":       s.SourceID,
		"package snapshot id":     s.SnapshotID,
	} {
		if err := validateUUID(name, value); err != nil {
			return err
		}
	}
	return nil
}

func validateEvidenceReference(reference EvidenceReference, scope Scope, limits ResponseLimits) error {
	if err := validateOpaqueID("package evidence id", reference.ID, limits.MaxIdentifierBytes); err != nil {
		return fmt.Errorf("%w: evidence id", ErrInvalidPackage)
	}
	for name, value := range map[string]string{
		"package evidence organization id": reference.OrganizationID,
		"package evidence source id":       reference.SourceID,
		"package evidence snapshot id":     reference.SnapshotID,
	} {
		if err := validateUUID(name, value); err != nil {
			return fmt.Errorf("%w: evidence scope", ErrInvalidPackage)
		}
	}
	if !sameScope(scope, Scope{OrganizationID: reference.OrganizationID, SourceID: reference.SourceID, SnapshotID: reference.SnapshotID}) {
		return fmt.Errorf("%w: evidence scope", ErrInvalidReference)
	}
	if reference.ContentDigest != "" && !isSHA256(reference.ContentDigest) {
		return fmt.Errorf("%w: evidence content digest", ErrInvalidDigest)
	}
	if err := validateCitationLocator(reference.Locator, reference.SourceID, limits.MaxLocatorBytes); err != nil {
		return fmt.Errorf("%w: package evidence locator", ErrInvalidPackage)
	}
	return nil
}

func sameScope(left, right Scope) bool {
	return strings.EqualFold(left.OrganizationID, right.OrganizationID) &&
		strings.EqualFold(left.SourceID, right.SourceID) &&
		strings.EqualFold(left.SnapshotID, right.SnapshotID)
}

// ValidateAgainstGateway verifies that the package context and the exact
// package passed to the existing generator port have the same identity and
// evidence set. It intentionally does not compare source content.
func (p EvidencePackage) ValidateAgainstGateway(gatewayPackage aigateway.EvidencePackage) error {
	if err := p.Validate(); err != nil {
		return err
	}
	if err := gatewayPackage.Validate(); err != nil {
		return fmt.Errorf("%w: gateway package", ErrInvalidPackage)
	}
	if p.ID != gatewayPackage.ID || p.Digest != gatewayPackage.Digest || len(p.Evidence) != len(gatewayPackage.Evidence) {
		return ErrPackageMismatch
	}
	byID := make(map[string]aigateway.AuthorizedEvidence, len(gatewayPackage.Evidence))
	for _, evidence := range gatewayPackage.Evidence {
		byID[evidence.ID] = evidence
	}
	for _, reference := range p.Evidence {
		evidence, exists := byID[reference.ID]
		if !exists {
			return ErrPackageMismatch
		}
		if reference.ContentDigest != "" && reference.ContentDigest != evidence.ContentDigest {
			return fmt.Errorf("%w: evidence digest", ErrPackageMismatch)
		}
	}
	return nil
}

// NewEvidencePackageFromGateway creates a structured validation view for an
// already-authorized gateway package. Locators are supplied by ID so this
// helper never parses or guesses a source path from an opaque transport
// locator.
func NewEvidencePackageFromGateway(gatewayPackage aigateway.EvidencePackage, scope Scope, locators map[string]contract.Locator) (EvidencePackage, error) {
	result := EvidencePackage{
		ID:             gatewayPackage.ID,
		Digest:         gatewayPackage.Digest,
		OrganizationID: scope.OrganizationID,
		SourceID:       scope.SourceID,
		SnapshotID:     scope.SnapshotID,
		Evidence:       make([]EvidenceReference, 0, len(gatewayPackage.Evidence)),
	}
	for _, evidence := range gatewayPackage.Evidence {
		locator, exists := locators[evidence.ID]
		if !exists {
			return EvidencePackage{}, fmt.Errorf("%w: missing evidence locator", ErrInvalidPackage)
		}
		result.Evidence = append(result.Evidence, EvidenceReference{
			ID:             evidence.ID,
			OrganizationID: scope.OrganizationID,
			SourceID:       scope.SourceID,
			SnapshotID:     scope.SnapshotID,
			Locator:        locator,
			ContentDigest:  evidence.ContentDigest,
		})
	}
	if err := result.ValidateAgainstGateway(gatewayPackage); err != nil {
		return EvidencePackage{}, err
	}
	return result, nil
}

// PackageFromGateway is a concise alias for NewEvidencePackageFromGateway.
func PackageFromGateway(gatewayPackage aigateway.EvidencePackage, scope Scope, locators map[string]contract.Locator) (EvidencePackage, error) {
	return NewEvidencePackageFromGateway(gatewayPackage, scope, locators)
}

// ResponseValidationContext binds a response to one package and optionally
// to the query identity calculated by the caller. Empty query fields mean
// that only the structural 8.1 ID/digest checks apply.
type ResponseValidationContext struct {
	Package     EvidencePackage
	QueryID     string
	QueryDigest string
	Limits      ResponseLimits
}

// ValidationContext is a descriptive alias for ResponseValidationContext.
type ValidationContext = ResponseValidationContext

// ResponseRepairRequest contains the bounded input for the optional repair
// port. Candidate is the output received from the generator and may contain
// untrusted model text; it is never included in an error or diagnostic by
// this package. The package itself contains identities and locators only, so
// a repairer cannot use this port to obtain source or database access.
type ResponseRepairRequest struct {
	Package     EvidencePackage
	QueryID     string
	QueryDigest string
	Candidate   []byte
	Reason      string
}

// ResponseRepairer is the consumer-side port used for the single bounded
// repair attempt. An adapter may implement it with an aigateway.Generator,
// but the query package deliberately does not put query response types into
// the gateway contract.
type ResponseRepairer interface {
	Repair(context.Context, ResponseRepairRequest) ([]byte, error)
}

// RepairPolicy bounds response repair. MaxAttempts may only be zero or one;
// zero disables repair and one permits exactly one call after an invalid
// initial response. The policy does not create a second budget or deadline:
// the caller supplies the already-orchestrated context to the repairer.
type RepairPolicy struct {
	MaxAttempts int
}

func (p RepairPolicy) normalized() (RepairPolicy, error) {
	if p.MaxAttempts < 0 || p.MaxAttempts > 1 {
		return RepairPolicy{}, ErrInvalidRepairPolicy
	}
	return p, nil
}

// ResponseValidationResult is the accepted response and the bounded repair
// accounting. Response is populated only after complete validation against
// the exact package and query identity.
type ResponseValidationResult struct {
	Response       Response
	Repaired       bool
	RepairAttempts int
}

// ValidateAndRepairResponse decodes and validates one structured JSON
// response against the exact EvidencePackage. Invalid JSON, free text,
// unknown fields, package/scope/digest mismatches, invalid locators and
// unsupported claim support are rejected. When policy permits, exactly one
// repair call may be made; an invalid repair is never published.
//
// Errors returned by a repairer are normalized to the AI Gateway taxonomy so
// cancellation, timeout and budget failures remain inspectable without
// exposing candidate content. A nil context or repairer with an enabled
// policy is a configuration error.
func ValidateAndRepairResponse(ctx context.Context, candidate []byte, validation ResponseValidationContext, repairer ResponseRepairer, policy RepairPolicy) (ResponseValidationResult, error) {
	policy, err := policy.normalized()
	if err != nil {
		return ResponseValidationResult{}, err
	}
	if err := validation.validateConfiguration(); err != nil {
		return ResponseValidationResult{}, err
	}
	if response, err := decodeAndValidateResponse(candidate, validation); err == nil {
		return ResponseValidationResult{Response: response}, nil
	} else if policy.MaxAttempts == 0 {
		return ResponseValidationResult{}, err
	} else {
		if repairer == nil {
			return ResponseValidationResult{}, ErrInvalidRepairPolicy
		}
		if ctx == nil {
			return ResponseValidationResult{}, aigateway.NewGatewayError(aigateway.ErrorKindConfiguration, aigateway.CapabilityGeneration, nil)
		}
		if err := normalizeRepairContext(ctx); err != nil {
			return ResponseValidationResult{}, err
		}
		limits, limitsErr := validation.Limits.normalized()
		if limitsErr != nil {
			return ResponseValidationResult{}, limitsErr
		}
		// Do not send an unbounded candidate to a repair adapter. This is both
		// a response limit and the transfer budget for the repair operation.
		if int64(len(candidate)) > limits.MaxTotalBytes {
			return ResponseValidationResult{}, err
		}
		repairRequest := ResponseRepairRequest{
			Package:     cloneEvidencePackage(validation.Package),
			QueryID:     validation.QueryID,
			QueryDigest: validation.QueryDigest,
			Candidate:   append([]byte(nil), candidate...),
			Reason:      responseValidationReason(err),
		}
		repaired, repairErr := repairer.Repair(ctx, repairRequest)
		if repairErr != nil {
			return ResponseValidationResult{RepairAttempts: 1}, aigateway.NormalizeError(aigateway.CapabilityGeneration, repairErr)
		}
		response, repairedErr := decodeAndValidateResponse(repaired, validation)
		if repairedErr != nil {
			return ResponseValidationResult{RepairAttempts: 1}, fmt.Errorf("%w: %w", ErrRepairFailed, safeValidationError(repairedErr))
		}
		return ResponseValidationResult{Response: response, Repaired: true, RepairAttempts: 1}, nil
	}
}

// ValidateResponseJSON is the no-repair spelling for callers that already
// have a complete JSON response and want an accepted Response value.
func ValidateResponseJSON(candidate []byte, validation ResponseValidationContext) (Response, error) {
	result, err := ValidateAndRepairResponse(nil, candidate, validation, nil, RepairPolicy{})
	return result.Response, err
}

// ValidateOrRepairResponse is a concise compatibility spelling for the
// bounded validation operation.
func ValidateOrRepairResponse(ctx context.Context, candidate []byte, validation ResponseValidationContext, repairer ResponseRepairer, policy RepairPolicy) (ResponseValidationResult, error) {
	return ValidateAndRepairResponse(ctx, candidate, validation, repairer, policy)
}

func (c ResponseValidationContext) validateConfiguration() error {
	if err := c.Package.Validate(); err != nil {
		return err
	}
	limits, err := c.Limits.normalized()
	if err != nil {
		return err
	}
	if c.QueryID != "" {
		if err := validateOpaqueID("query id", c.QueryID, limits.MaxIdentifierBytes); err != nil {
			return err
		}
	}
	if c.QueryDigest != "" && !isSHA256(c.QueryDigest) {
		return ErrInvalidDigest
	}
	return nil
}

func decodeAndValidateResponse(candidate []byte, validation ResponseValidationContext) (Response, error) {
	limits, err := validation.Limits.normalized()
	if err != nil {
		return Response{}, err
	}
	if len(candidate) == 0 {
		return Response{}, ErrInvalidResponse
	}
	if int64(len(candidate)) > limits.MaxTotalBytes {
		return Response{}, fmt.Errorf("%w: response JSON", ErrLimitExceeded)
	}
	var response Response
	if err := json.Unmarshal(candidate, &response); err != nil {
		// Response.UnmarshalJSON intentionally returns a bounded error. Do not
		// wrap the decoder error because it may include untrusted input.
		return Response{}, ErrInvalidResponse
	}
	if err := validation.Validate(response); err != nil {
		return Response{}, safeValidationError(err)
	}
	return response, nil
}

func cloneEvidencePackage(packageContext EvidencePackage) EvidencePackage {
	packageContext.Evidence = append([]EvidenceReference(nil), packageContext.Evidence...)
	return packageContext
}

func normalizeRepairContext(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return aigateway.NormalizeError(aigateway.CapabilityGeneration, ctx.Err())
	default:
		return nil
	}
}

func responseValidationReason(err error) string {
	switch {
	case errors.Is(err, ErrPackageMismatch):
		return "package_mismatch"
	case errors.Is(err, ErrQueryMismatch):
		return "query_mismatch"
	case errors.Is(err, ErrInvalidReference):
		return "invalid_reference"
	case errors.Is(err, ErrInvalidDigest):
		return "invalid_digest"
	case errors.Is(err, ErrLimitExceeded):
		return "limit_exceeded"
	case errors.Is(err, ErrUnsupportedVersion):
		return "unsupported_version"
	default:
		return "invalid_response"
	}
}

func safeValidationError(err error) error {
	if err == nil {
		return ErrInvalidResponse
	}
	// All validation errors in this package are intentionally bounded. Keep
	// only their category chain so a future validation rule cannot accidentally
	// echo candidate text through a repair failure.
	switch {
	case errors.Is(err, ErrLimitExceeded):
		return ErrLimitExceeded
	case errors.Is(err, ErrInvalidDigest):
		return ErrInvalidDigest
	case errors.Is(err, ErrPackageMismatch):
		return ErrPackageMismatch
	case errors.Is(err, ErrQueryMismatch):
		return ErrQueryMismatch
	case errors.Is(err, ErrInvalidReference):
		return ErrInvalidReference
	case errors.Is(err, ErrInvalidPackage):
		return ErrInvalidPackage
	case errors.Is(err, ErrUnsupportedVersion):
		return ErrUnsupportedVersion
	default:
		return ErrInvalidResponse
	}
}

// Validate checks the response against the package and optional query
// identity. Package membership is deliberately performed here, not by the
// schema-only Response.Validate method.
func (c ResponseValidationContext) Validate(response Response) error {
	if err := c.validateConfiguration(); err != nil {
		return err
	}
	if err := response.ValidateWithLimits(c.Limits); err != nil {
		return err
	}
	if response.Generation.PackageID != c.Package.ID || response.Generation.PackageDigest != c.Package.Digest {
		return ErrPackageMismatch
	}
	if c.QueryID != "" {
		if response.Generation.QueryID != c.QueryID {
			return ErrQueryMismatch
		}
	}
	if c.QueryDigest != "" {
		if response.Generation.QueryDigest != c.QueryDigest {
			return ErrQueryMismatch
		}
	}
	byID := make(map[string]EvidenceReference, len(c.Package.Evidence))
	for _, reference := range c.Package.Evidence {
		byID[reference.ID] = reference
	}
	for _, citation := range response.Citations {
		reference, exists := byID[citation.EvidenceID]
		if !exists {
			return fmt.Errorf("%w: citation evidence", ErrInvalidReference)
		}
		if !sameScope(c.Package.Scope(), Scope{OrganizationID: citation.OrganizationID, SourceID: citation.SourceID, SnapshotID: citation.SnapshotID}) {
			return fmt.Errorf("%w: citation scope", ErrInvalidReference)
		}
		if !sameLocator(citation.Locator, reference.Locator) {
			return fmt.Errorf("%w: citation locator", ErrInvalidReference)
		}
	}
	return nil
}

// ValidateAgainstPackage validates a response using the package scope and
// evidence identities, without binding it to a particular query identity.
func (r Response) ValidateAgainstPackage(packageContext EvidencePackage) error {
	return (ResponseValidationContext{Package: packageContext}).Validate(r)
}

// ValidateAgainst is a concise alias for ValidateAgainstPackage.
func (r Response) ValidateAgainst(packageContext EvidencePackage) error {
	return r.ValidateAgainstPackage(packageContext)
}

// ValidateResponseAgainstPackage is the functional spelling of
// Response.ValidateAgainstPackage.
func ValidateResponseAgainstPackage(response Response, packageContext EvidencePackage) error {
	return response.ValidateAgainstPackage(packageContext)
}

func sameLocator(left, right contract.Locator) bool {
	return left.URI == right.URI &&
		strings.EqualFold(left.SourceID, right.SourceID) &&
		left.ArtifactID == right.ArtifactID &&
		left.Path == right.Path &&
		left.Member == right.Member &&
		left.StartLine == right.StartLine &&
		left.StartColumn == right.StartColumn &&
		left.EndLine == right.EndLine &&
		left.EndColumn == right.EndColumn &&
		left.ByteOffset == right.ByteOffset &&
		left.ByteLength == right.ByteLength
}

func validateQueryDigest(value string) error {
	if len(value) != sha256.Size*2 || strings.ToLower(value) != value {
		return ErrInvalidDigest
	}
	if _, err := hex.DecodeString(value); err != nil {
		return ErrInvalidDigest
	}
	return nil
}
