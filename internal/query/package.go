package query

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/pedrogpaulino/manu/internal/aigateway"
	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/evidence"
	"github.com/pedrogpaulino/manu/internal/retrieval"
)

const (
	packageVersion = "v1alpha1"

	defaultPackageMaxUnits         = 32
	defaultPackageMaxCharacters    = 32 << 10
	defaultPackageMaxBytes         = 256 << 10
	defaultPackageMaxTokens        = 8 << 10
	defaultPackageCharactersToken  = 4
	defaultPackageMaxArtifactUnits = 8
	defaultPackageMaxTypeUnits     = 16
	defaultPackageMaxGaps          = 64
	defaultPackageMaxLocatorBytes  = 128

	maxPackageUnits       = 10_000
	maxPackageCharacters  = 4 << 20
	maxPackageBytes       = 4 << 20
	maxPackageTokens      = 1 << 20
	maxPackageCharactersT = 64
	maxPackageGaps        = 1_000
)

var (
	// ErrInvalidPackageComposition identifies malformed compositor input.
	ErrInvalidPackageComposition = errors.New("query: invalid evidence package composition")
	// ErrPackageScopeMismatch identifies a package scope that is not a valid
	// organization/source/snapshot boundary. Candidate scope mismatches are
	// recorded in the audit and never enter the resulting package.
	ErrPackageScopeMismatch = errors.New("query: evidence package scope mismatch")
	// ErrInvalidPackageLimits identifies an unrepresentable package budget.
	ErrInvalidPackageLimits = errors.New("query: invalid evidence package limits")
	// ErrInvalidPackagePolicy identifies an invalid external-transfer policy.
	ErrInvalidPackagePolicy = errors.New("query: invalid evidence package policy")
)

// CandidateKind distinguishes the projection or semantic type that produced
// a fused candidate. It is deliberately an opaque, bounded token: the
// compositor does not infer meaning from source text.
type CandidateKind string

const (
	CandidateKindUnknown  CandidateKind = ""
	CandidateKindText     CandidateKind = "text"
	CandidateKindSymbol   CandidateKind = "symbol"
	CandidateKindConfig   CandidateKind = "configuration"
	CandidateKindRelation CandidateKind = "relation"
)

// PackageCandidate joins a fused retrieval candidate to its canonical,
// already-authorized Evidence Unit. Fusion itself is intentionally outside
// this package; this type only carries its deterministic ranking output into
// package composition.
type PackageCandidate struct {
	Fusion retrieval.FusionCandidate `json:"fusion"`
	Unit   evidence.EvidenceUnit     `json:"unit"`
	Kind   CandidateKind             `json:"kind,omitempty"`
	// Type is accepted as a descriptive spelling for callers that use
	// "type" rather than "kind". When both are set they must agree.
	Type CandidateKind `json:"type,omitempty"`
	// CanonicalEvidenceID bridges a persisted evidence row UUID and the
	// deterministic Evidence Unit identity. It is intentionally omitted from
	// transport JSON; when empty, the unit identity remains the package ID.
	CanonicalEvidenceID string `json:"-"`
	// ExternalEvidenceID preserves the bundle/fact evidence identity and is
	// intentionally omitted from transport JSON.
	ExternalEvidenceID string `json:"-"`
}

// FusedCandidate is a descriptive alias used by callers of the retrieval
// layer.
type FusedCandidate = PackageCandidate

// Candidate is a concise alias for PackageCandidate.
type Candidate = PackageCandidate

// MaterialGap is a bounded, material absence of support. It intentionally
// carries identifiers and controlled classification only; it never carries
// source text or a provider diagnostic.
type MaterialGap struct {
	ID        string `json:"id"`
	Code      string `json:"code"`
	Dimension string `json:"dimension,omitempty"`
}

// PackageGap is a descriptive alias for MaterialGap.
type PackageGap = MaterialGap

// PackageLimits bounds the organizational evidence package. Characters and
// bytes count only the representation that will cross the provider boundary.
// TokenEstimate is an explicit deterministic estimate, not a provider claim.
type PackageLimits struct {
	MaxUnits            int   `json:"max_units"`
	MaxCharacters       int64 `json:"max_characters"`
	MaxBytes            int64 `json:"max_bytes"`
	MaxTokens           int   `json:"max_tokens"`
	CharactersPerToken  int   `json:"characters_per_token"`
	MaxUnitsPerArtifact int   `json:"max_units_per_artifact"`
	MaxUnitsPerType     int   `json:"max_units_per_type"`
	MaxGaps             int   `json:"max_gaps"`
	MaxLocatorBytes     int   `json:"max_locator_bytes"`
}

// DefaultPackageLimits returns the bounded defaults for one generation
// package. Zero fields in a request are filled from these values.
func DefaultPackageLimits() PackageLimits {
	return PackageLimits{
		MaxUnits:            defaultPackageMaxUnits,
		MaxCharacters:       defaultPackageMaxCharacters,
		MaxBytes:            defaultPackageMaxBytes,
		MaxTokens:           defaultPackageMaxTokens,
		CharactersPerToken:  defaultPackageCharactersToken,
		MaxUnitsPerArtifact: defaultPackageMaxArtifactUnits,
		MaxUnitsPerType:     defaultPackageMaxTypeUnits,
		MaxGaps:             defaultPackageMaxGaps,
		MaxLocatorBytes:     defaultPackageMaxLocatorBytes,
	}
}

// Normalize fills bounded defaults and returns the configuration that was
// actually applied by the compositor.
func (l PackageLimits) Normalize() (PackageLimits, error) {
	d := DefaultPackageLimits()
	if l.MaxUnits == 0 {
		l.MaxUnits = d.MaxUnits
	}
	if l.MaxCharacters == 0 {
		l.MaxCharacters = d.MaxCharacters
	}
	if l.MaxBytes == 0 {
		l.MaxBytes = d.MaxBytes
	}
	if l.MaxTokens == 0 {
		l.MaxTokens = d.MaxTokens
	}
	if l.CharactersPerToken == 0 {
		l.CharactersPerToken = d.CharactersPerToken
	}
	if l.MaxUnitsPerArtifact == 0 {
		l.MaxUnitsPerArtifact = d.MaxUnitsPerArtifact
	}
	if l.MaxUnitsPerType == 0 {
		l.MaxUnitsPerType = d.MaxUnitsPerType
	}
	if l.MaxGaps == 0 {
		l.MaxGaps = d.MaxGaps
	}
	if l.MaxLocatorBytes == 0 {
		l.MaxLocatorBytes = d.MaxLocatorBytes
	}
	if l.MaxUnits < 1 || l.MaxUnits > maxPackageUnits ||
		l.MaxCharacters < 1 || l.MaxCharacters > maxPackageCharacters ||
		l.MaxBytes < 1 || l.MaxBytes > maxPackageBytes ||
		l.MaxTokens < 1 || l.MaxTokens > maxPackageTokens ||
		l.CharactersPerToken < 1 || l.CharactersPerToken > maxPackageCharactersT ||
		l.MaxUnitsPerArtifact < 1 || l.MaxUnitsPerArtifact > maxPackageUnits ||
		l.MaxUnitsPerType < 1 || l.MaxUnitsPerType > maxPackageUnits ||
		l.MaxGaps < 0 || l.MaxGaps > maxPackageGaps ||
		l.MaxLocatorBytes < 1 || l.MaxLocatorBytes > defaultPackageMaxLocatorBytes {
		return PackageLimits{}, ErrInvalidPackageLimits
	}
	return l, nil
}

// EstimatePackageTokens applies the documented deterministic approximation.
// It returns zero for empty content and never returns a negative value.
func EstimatePackageTokens(content string, charactersPerToken int) int {
	if content == "" || charactersPerToken <= 0 {
		return 0
	}
	characters := utf8.RuneCountInString(content)
	return (characters + charactersPerToken - 1) / charactersPerToken
}

// AppliedPackageConfiguration records only safe, non-secret configuration
// data and its reproducible digest.
type AppliedPackageConfiguration struct {
	Version      string        `json:"version"`
	Limits       PackageLimits `json:"limits"`
	PolicyMode   string        `json:"policy_mode"`
	PolicyDigest string        `json:"policy_digest,omitempty"`
	Digest       string        `json:"digest"`
}

// PackageConfiguration is a descriptive alias.
type PackageConfiguration = AppliedPackageConfiguration

// PackageRequest is the complete input to composition. TransferPolicy is
// optional: nil means that the canonical unit's already-resolved transfer
// decision is used; a non-nil policy is resolved before packaging.
type PackageRequest struct {
	Scope           Scope                       `json:"scope"`
	Candidates      []PackageCandidate          `json:"candidates,omitempty"`
	FusedCandidates []retrieval.FusionCandidate `json:"fused_candidates,omitempty"`
	EvidenceUnits   []evidence.EvidenceUnit     `json:"evidence_units,omitempty"`
	Gaps            []MaterialGap               `json:"gaps,omitempty"`
	Limits          PackageLimits               `json:"limits"`
	TransferPolicy  *evidence.Policy            `json:"-"`
}

// EvidencePackageRequest is a descriptive alias.
type EvidencePackageRequest = PackageRequest

// CandidateAudit records a deterministic, content-free decision for one
// candidate. ContentHash is an identity aid and never source content.
type CandidateAudit struct {
	EvidenceID     string            `json:"evidence_id,omitempty"`
	OrganizationID string            `json:"organization_id,omitempty"`
	SourceID       string            `json:"source_id,omitempty"`
	SnapshotID     string            `json:"snapshot_id,omitempty"`
	ArtifactID     string            `json:"artifact_id,omitempty"`
	Kind           CandidateKind     `json:"kind,omitempty"`
	ContentHash    string            `json:"content_hash,omitempty"`
	Rank           int               `json:"rank,omitempty"`
	Score          float64           `json:"score,omitempty"`
	Included       bool              `json:"included"`
	Redacted       bool              `json:"redacted,omitempty"`
	Reason         PackageReasonCode `json:"reason"`
}

// PackageReasonCode is the stable vocabulary used for candidate decisions.
type PackageReasonCode string

const (
	PackageReasonIncluded          PackageReasonCode = "included"
	PackageReasonScopeMismatch     PackageReasonCode = "scope_mismatch"
	PackageReasonInvalidCandidate  PackageReasonCode = "invalid_candidate"
	PackageReasonTransferDenied    PackageReasonCode = "transfer_denied"
	PackageReasonContentOmitted    PackageReasonCode = "content_omitted"
	PackageReasonContentProhibited PackageReasonCode = "content_prohibited"
	PackageReasonDuplicateID       PackageReasonCode = "duplicate_evidence_id"
	PackageReasonDuplicateHash     PackageReasonCode = "duplicate_content_hash"
	PackageReasonUnitLimit         PackageReasonCode = "unit_limit"
	PackageReasonCharacterLimit    PackageReasonCode = "character_limit"
	PackageReasonByteLimit         PackageReasonCode = "byte_limit"
	PackageReasonTokenLimit        PackageReasonCode = "token_limit"
	PackageReasonArtifactDiversity PackageReasonCode = "artifact_diversity_limit"
	PackageReasonTypeDiversity     PackageReasonCode = "type_diversity_limit"
	PackageReasonLocatorLimit      PackageReasonCode = "locator_limit"
	PackageReasonContentLimit      PackageReasonCode = "content_limit"
)

// Composition is the exact pair of packages plus audit/configuration output
// produced by the compositor.
type Composition struct {
	ValidationPackage EvidencePackage             `json:"validation_package"`
	GatewayPackage    aigateway.EvidencePackage   `json:"gateway_package"`
	Audits            []CandidateAudit            `json:"audits"`
	Gaps              []MaterialGap               `json:"gaps,omitempty"`
	Configuration     AppliedPackageConfiguration `json:"configuration"`
	UnitCount         int                         `json:"unit_count"`
	CharacterCount    int64                       `json:"character_count"`
	ByteCount         int64                       `json:"byte_count"`
	TokenEstimate     int                         `json:"token_estimate"`
}

// PackageComposition is the descriptive spelling used by callers.
type PackageComposition = Composition

// ComposedPackage is a concise alias.
type ComposedPackage = Composition

// Validate checks that both views describe the same exact package and that
// no transport item escaped the structured validation boundary.
func (c Composition) Validate() error {
	if err := c.ValidationPackage.Validate(); err != nil {
		return err
	}
	if err := c.GatewayPackage.Validate(); err != nil {
		return fmt.Errorf("%w: gateway package", ErrInvalidPackageComposition)
	}
	if err := c.ValidationPackage.ValidateAgainstGateway(c.GatewayPackage); err != nil {
		return fmt.Errorf("%w: package views differ", ErrInvalidPackageComposition)
	}
	return nil
}

// ComposeEvidencePackage applies policy, budgets, diversity and deterministic
// ordering to fused candidates. It does not retrieve, fuse, call a provider,
// inspect a source, or persist anything.
func ComposeEvidencePackage(ctx context.Context, request PackageRequest) (Composition, error) {
	if ctx == nil {
		return Composition{}, ErrInvalidPackageComposition
	}
	if err := ctx.Err(); err != nil {
		return Composition{}, err
	}
	if err := request.Scope.Validate(); err != nil {
		return Composition{}, fmt.Errorf("%w: %v", ErrPackageScopeMismatch, err)
	}
	limits, err := request.Limits.Normalize()
	if err != nil {
		return Composition{}, err
	}
	if request.TransferPolicy != nil {
		if err := request.TransferPolicy.Validate(); err != nil {
			return Composition{}, ErrInvalidPackagePolicy
		}
	}
	configuration := appliedPackageConfiguration(limits, request.TransferPolicy)

	gaps, err := normalizePackageGaps(request.Gaps, limits.MaxGaps)
	if err != nil {
		return Composition{}, err
	}

	candidates, err := request.packageCandidates()
	if err != nil {
		return Composition{}, err
	}
	prepared := make([]packageCandidate, 0, len(candidates))
	audits := make([]CandidateAudit, 0, len(candidates))
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return Composition{}, err
		}
		preparedCandidate, audit, include := preparePackageCandidate(candidate, request.Scope, limits, request.TransferPolicy)
		if !include {
			audits = append(audits, audit)
			continue
		}
		prepared = append(prepared, preparedCandidate)
	}

	sort.SliceStable(prepared, func(i, j int) bool {
		return packageCandidateLess(prepared[i], prepared[j])
	})

	selected := make([]packageCandidate, 0, len(prepared))
	seenIDs := make(map[string]struct{}, len(prepared))
	seenHashes := make(map[string]struct{}, len(prepared))
	artifactCounts := make(map[string]int)
	typeCounts := make(map[CandidateKind]int)
	var characterCount, byteCount int64
	tokenEstimate := 0

	for _, candidate := range prepared {
		if err := ctx.Err(); err != nil {
			return Composition{}, err
		}
		audit := candidate.audit
		if _, exists := seenIDs[candidate.unit.ID]; exists {
			audit.Reason = PackageReasonDuplicateID
			audit.Included = false
			audits = append(audits, audit)
			continue
		}
		seenIDs[candidate.unit.ID] = struct{}{}
		if _, exists := seenHashes[candidate.unit.ContentHash]; exists {
			audit.Reason = PackageReasonDuplicateHash
			audit.Included = false
			audits = append(audits, audit)
			continue
		}
		seenHashes[candidate.unit.ContentHash] = struct{}{}

		candidateCharacters := int64(utf8.RuneCountInString(candidate.content))
		candidateBytes := int64(len([]byte(candidate.content)))
		candidateTokens := EstimatePackageTokens(candidate.content, limits.CharactersPerToken)
		if len(selected) >= limits.MaxUnits {
			audit.Reason = PackageReasonUnitLimit
			audit.Included = false
			audits = append(audits, audit)
			continue
		}
		if characterCount > limits.MaxCharacters-candidateCharacters {
			audit.Reason = PackageReasonCharacterLimit
			audit.Included = false
			audits = append(audits, audit)
			continue
		}
		if byteCount > limits.MaxBytes-candidateBytes {
			audit.Reason = PackageReasonByteLimit
			audit.Included = false
			audits = append(audits, audit)
			continue
		}
		if tokenEstimate > limits.MaxTokens-candidateTokens {
			audit.Reason = PackageReasonTokenLimit
			audit.Included = false
			audits = append(audits, audit)
			continue
		}
		if artifactCounts[candidate.unit.ArtifactID] >= limits.MaxUnitsPerArtifact {
			audit.Reason = PackageReasonArtifactDiversity
			audit.Included = false
			audits = append(audits, audit)
			continue
		}
		if typeCounts[candidate.kind] >= limits.MaxUnitsPerType {
			audit.Reason = PackageReasonTypeDiversity
			audit.Included = false
			audits = append(audits, audit)
			continue
		}

		audit.Included = true
		audit.Reason = PackageReasonIncluded
		audits = append(audits, audit)
		selected = append(selected, candidate)
		characterCount += candidateCharacters
		byteCount += candidateBytes
		tokenEstimate += candidateTokens
		artifactCounts[candidate.unit.ArtifactID]++
		typeCounts[candidate.kind]++
	}

	sort.SliceStable(selected, func(i, j int) bool {
		return selected[i].unit.ID < selected[j].unit.ID
	})

	gatewayEvidence := make([]aigateway.AuthorizedEvidence, 0, len(selected))
	validationEvidence := make([]EvidenceReference, 0, len(selected))
	for _, candidate := range selected {
		gatewayEvidence = append(gatewayEvidence, aigateway.AuthorizedEvidence{
			ID:            candidate.packageEvidenceID,
			Content:       candidate.content,
			ContentDigest: candidate.transferDigest,
			Locator:       candidate.gatewayLocator,
		})
		validationEvidence = append(validationEvidence, EvidenceReference{
			ID:             candidate.packageEvidenceID,
			OrganizationID: request.Scope.OrganizationID,
			SourceID:       request.Scope.SourceID,
			SnapshotID:     request.Scope.SnapshotID,
			Locator:        candidate.unit.Locator,
			ContentDigest:  candidate.transferDigest,
		})
	}

	digest, err := packageDigest(request.Scope, configuration.Digest, gatewayEvidence, gapIDs(gaps))
	if err != nil {
		return Composition{}, err
	}
	packageID := "package-" + digest
	result := Composition{
		ValidationPackage: EvidencePackage{
			ID:             packageID,
			Digest:         digest,
			OrganizationID: request.Scope.OrganizationID,
			SourceID:       request.Scope.SourceID,
			SnapshotID:     request.Scope.SnapshotID,
			Evidence:       validationEvidence,
		},
		GatewayPackage: aigateway.EvidencePackage{
			ID:       packageID,
			Digest:   digest,
			Evidence: gatewayEvidence,
			Gaps:     gapIDs(gaps),
		},
		Audits:         audits,
		Gaps:           gaps,
		Configuration:  configuration,
		UnitCount:      len(selected),
		CharacterCount: characterCount,
		ByteCount:      byteCount,
		TokenEstimate:  tokenEstimate,
	}
	sortPackageAudits(result.Audits)
	if err := result.Validate(); err != nil {
		return Composition{}, err
	}
	return result, nil
}

// packageCandidates joins the two canonical inputs when callers provide them
// separately. The joined form remains available for callers that already
// performed this identity association upstream.
func (r PackageRequest) packageCandidates() ([]PackageCandidate, error) {
	if len(r.Candidates) != 0 && (len(r.FusedCandidates) != 0 || len(r.EvidenceUnits) != 0) {
		return nil, ErrInvalidPackageComposition
	}
	if len(r.Candidates) != 0 || (len(r.FusedCandidates) == 0 && len(r.EvidenceUnits) == 0) {
		return append([]PackageCandidate(nil), r.Candidates...), nil
	}
	if len(r.FusedCandidates) == 0 {
		return nil, ErrInvalidPackageComposition
	}
	units := make(map[string]evidence.EvidenceUnit, len(r.EvidenceUnits))
	for _, unit := range r.EvidenceUnits {
		if unit.ID == "" {
			return nil, ErrInvalidPackageComposition
		}
		if _, exists := units[unit.ID]; exists {
			return nil, ErrInvalidPackageComposition
		}
		units[unit.ID] = unit
	}
	candidates := make([]PackageCandidate, 0, len(r.FusedCandidates))
	for _, fusion := range r.FusedCandidates {
		candidates = append(candidates, PackageCandidate{Fusion: fusion, Unit: units[fusion.EvidenceID]})
	}
	return candidates, nil
}

// ComposePackage is a concise alias for ComposeEvidencePackage.
func ComposePackage(ctx context.Context, request PackageRequest) (Composition, error) {
	return ComposeEvidencePackage(ctx, request)
}

// Compose is the shortest public spelling for package composition.
func Compose(ctx context.Context, request PackageRequest) (Composition, error) {
	return ComposeEvidencePackage(ctx, request)
}

type packageCandidate struct {
	unit              evidence.EvidenceUnit
	packageEvidenceID string
	kind              CandidateKind
	content           string
	transferDigest    string
	gatewayLocator    string
	audit             CandidateAudit
}

func preparePackageCandidate(candidate PackageCandidate, scope Scope, limits PackageLimits, policy *evidence.Policy) (packageCandidate, CandidateAudit, bool) {
	audit := candidateAuditFor(candidate)
	if candidate.Kind != "" && candidate.Type != "" && candidate.Kind != candidate.Type {
		audit.Reason = PackageReasonInvalidCandidate
		return packageCandidate{}, audit, false
	}
	kind := candidate.Kind
	if kind == "" {
		kind = candidate.Type
	}
	if kind == "" {
		kind = CandidateKind("evidence_unit")
	}
	if !validPackageToken(string(kind), 128) {
		audit.Reason = PackageReasonInvalidCandidate
		return packageCandidate{}, audit, false
	}
	audit.Kind = kind
	packageEvidenceID := candidate.Unit.ID
	if candidate.CanonicalEvidenceID != "" {
		packageEvidenceID = candidate.CanonicalEvidenceID
	}
	audit.EvidenceID = packageEvidenceID
	audit.ArtifactID = candidate.Unit.ArtifactID
	audit.ContentHash = candidate.Unit.ContentHash
	audit.OrganizationID = candidate.Unit.OrganizationID
	audit.SourceID = candidate.Unit.SourceID
	audit.SnapshotID = candidate.Unit.SnapshotID
	audit.Rank = candidate.Fusion.Rank
	audit.Score = candidate.Fusion.Score

	if candidate.Fusion.EvidenceID == "" || !strings.EqualFold(candidate.Fusion.EvidenceID, packageEvidenceID) ||
		candidate.Fusion.OrganizationID == "" || candidate.Fusion.SourceID == "" || candidate.Fusion.SnapshotID == "" {
		audit.Reason = PackageReasonInvalidCandidate
		return packageCandidate{}, audit, false
	}
	if !sameScope(scope, Scope{
		OrganizationID: candidate.Fusion.OrganizationID,
		SourceID:       candidate.Fusion.SourceID,
		SnapshotID:     candidate.Fusion.SnapshotID,
	}) || !sameScope(scope, Scope{
		OrganizationID: candidate.Unit.OrganizationID,
		SourceID:       candidate.Unit.SourceID,
		SnapshotID:     candidate.Unit.SnapshotID,
	}) {
		audit.Reason = PackageReasonScopeMismatch
		return packageCandidate{}, audit, false
	}
	if candidate.Fusion.Provenance.EvidenceID != "" &&
		!strings.EqualFold(candidate.Fusion.Provenance.EvidenceID, packageEvidenceID) &&
		!strings.EqualFold(candidate.Fusion.Provenance.EvidenceID, candidate.Unit.ID) {
		audit.Reason = PackageReasonInvalidCandidate
		return packageCandidate{}, audit, false
	}
	if (candidate.Fusion.Provenance.OrganizationID != "" ||
		candidate.Fusion.Provenance.SourceID != "" ||
		candidate.Fusion.Provenance.SnapshotID != "") &&
		!sameScope(scope, Scope{
			OrganizationID: candidate.Fusion.Provenance.OrganizationID,
			SourceID:       candidate.Fusion.Provenance.SourceID,
			SnapshotID:     candidate.Fusion.Provenance.SnapshotID,
		}) {
		audit.Reason = PackageReasonScopeMismatch
		return packageCandidate{}, audit, false
	}
	if candidate.Fusion.Provenance.EvidenceContentHash != "" &&
		candidate.Fusion.Provenance.EvidenceContentHash != candidate.Unit.ContentHash {
		audit.Reason = PackageReasonInvalidCandidate
		return packageCandidate{}, audit, false
	}
	if candidate.Fusion.Rank < 0 || !finitePackageFloat(candidate.Fusion.Score) {
		audit.Reason = PackageReasonInvalidCandidate
		return packageCandidate{}, audit, false
	}
	if err := candidate.Unit.ValidatePrepared(); err != nil {
		audit.Reason = PackageReasonInvalidCandidate
		return packageCandidate{}, audit, false
	}

	unit := candidate.Unit
	canonicalTransferDecision := unit.ExternalTransfer
	if policy != nil {
		prepared, err := evidence.PrepareForExternalTransfer(unit, *policy)
		if err != nil {
			audit.Reason = PackageReasonInvalidCandidate
			return packageCandidate{}, audit, false
		}
		unit = prepared
		// A source/unit decision is an authorization floor. An installation
		// policy may further restrict transfer, but it must never turn a
		// canonical deny or redact into an allow.
		if canonicalTransferDecision == evidence.DecisionDeny {
			unit.ExternalTransfer = evidence.DecisionDeny
		} else if canonicalTransferDecision == evidence.DecisionRedact {
			unit.ExternalTransfer = evidence.DecisionRedact
		}
	}
	content, redacted, reason, ok := transferableContent(unit)
	if !ok {
		audit.Reason = reason
		return packageCandidate{}, audit, false
	}
	if int64(len([]byte(content))) > int64(aigatewayMaxEvidenceContentBytes) {
		audit.Reason = PackageReasonContentLimit
		return packageCandidate{}, audit, false
	}
	locator, err := packageLocator(unit.Locator)
	if err != nil || len([]byte(locator)) > limits.MaxLocatorBytes || len([]byte(locator)) > defaultPackageMaxLocatorBytes {
		audit.Reason = PackageReasonLocatorLimit
		return packageCandidate{}, audit, false
	}
	transferDigest := evidence.ContentDigest(content)
	audit.EvidenceID = packageEvidenceID
	audit.ArtifactID = unit.ArtifactID
	audit.ContentHash = unit.ContentHash
	audit.Redacted = redacted
	audit.Reason = PackageReasonIncluded
	return packageCandidate{
		unit:              unit,
		packageEvidenceID: packageEvidenceID,
		kind:              kind,
		content:           content,
		transferDigest:    transferDigest,
		gatewayLocator:    locator,
		audit:             audit,
	}, audit, true
}

func transferableContent(unit evidence.EvidenceUnit) (string, bool, PackageReasonCode, bool) {
	if unit.Classification == evidence.ClassificationProhibited ||
		unit.Classification == evidence.ClassificationBinary ||
		unit.Classification == evidence.ClassificationInvalid {
		return "", false, PackageReasonContentProhibited, false
	}
	if unit.ContentState == evidence.ContentStateOmitted || unit.Content == "" {
		return "", false, PackageReasonContentOmitted, false
	}
	if unit.ExternalTransfer == evidence.DecisionDeny {
		return "", false, PackageReasonTransferDenied, false
	}
	if unit.ExternalTransfer == evidence.DecisionRedact || unit.ContentState == evidence.ContentStateRedacted {
		if unit.ContentState == evidence.ContentStateRedacted && unit.Content != evidence.RedactedContent {
			return "", false, PackageReasonContentProhibited, false
		}
		return evidence.RedactedContent, true, PackageReasonIncluded, true
	}
	if unit.ContentState != evidence.ContentStatePresent {
		return "", false, PackageReasonContentOmitted, false
	}
	if !utf8.ValidString(unit.Content) {
		return "", false, PackageReasonContentProhibited, false
	}
	return unit.Content, false, PackageReasonIncluded, true
}

func candidateAuditFor(candidate PackageCandidate) CandidateAudit {
	return CandidateAudit{
		EvidenceID:     candidate.Fusion.EvidenceID,
		OrganizationID: candidate.Fusion.OrganizationID,
		SourceID:       candidate.Fusion.SourceID,
		SnapshotID:     candidate.Fusion.SnapshotID,
		ArtifactID:     candidate.Unit.ArtifactID,
		Kind:           candidate.Kind,
		ContentHash:    candidate.Unit.ContentHash,
		Rank:           candidate.Fusion.Rank,
		Score:          candidate.Fusion.Score,
		Reason:         PackageReasonInvalidCandidate,
	}
}

func packageCandidateLess(left, right packageCandidate) bool {
	if left.audit.Score != right.audit.Score {
		return left.audit.Score > right.audit.Score
	}
	if left.audit.Rank != right.audit.Rank {
		if left.audit.Rank == 0 {
			return false
		}
		if right.audit.Rank == 0 {
			return true
		}
		return left.audit.Rank < right.audit.Rank
	}
	if left.unit.ID != right.unit.ID {
		return left.unit.ID < right.unit.ID
	}
	if left.unit.ArtifactID != right.unit.ArtifactID {
		return left.unit.ArtifactID < right.unit.ArtifactID
	}
	return left.kind < right.kind
}

func sortPackageAudits(audits []CandidateAudit) {
	sort.SliceStable(audits, func(i, j int) bool {
		left, right := audits[i], audits[j]
		if left.EvidenceID != right.EvidenceID {
			return left.EvidenceID < right.EvidenceID
		}
		if left.ContentHash != right.ContentHash {
			return left.ContentHash < right.ContentHash
		}
		if left.Kind != right.Kind {
			return left.Kind < right.Kind
		}
		if left.Reason != right.Reason {
			return left.Reason < right.Reason
		}
		if left.Rank != right.Rank {
			return left.Rank < right.Rank
		}
		if left.Score != right.Score {
			return left.Score < right.Score
		}
		if left.Included != right.Included {
			return !left.Included
		}
		return !left.Redacted && right.Redacted
	})
}

func normalizePackageGaps(input []MaterialGap, max int) ([]MaterialGap, error) {
	if len(input) > max {
		return nil, fmt.Errorf("%w: gap count", ErrInvalidPackageLimits)
	}
	byID := make(map[string]MaterialGap, len(input))
	for _, gap := range input {
		gap.ID = strings.TrimSpace(gap.ID)
		gap.Code = strings.TrimSpace(gap.Code)
		gap.Dimension = strings.TrimSpace(gap.Dimension)
		if !validPackageToken(gap.ID, 128) || !validPackageToken(gap.Code, 128) ||
			(gap.Dimension != "" && !validPackageToken(gap.Dimension, 128)) {
			return nil, fmt.Errorf("%w: gap", ErrInvalidPackageComposition)
		}
		if existing, exists := byID[gap.ID]; exists {
			if packageGapLess(gap, existing) {
				byID[gap.ID] = gap
			}
			continue
		}
		byID[gap.ID] = gap
	}
	result := make([]MaterialGap, 0, len(byID))
	for _, gap := range byID {
		result = append(result, gap)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].ID != result[j].ID {
			return result[i].ID < result[j].ID
		}
		if result[i].Code != result[j].Code {
			return result[i].Code < result[j].Code
		}
		return result[i].Dimension < result[j].Dimension
	})
	return result, nil
}

func packageGapLess(left, right MaterialGap) bool {
	if left.Code != right.Code {
		return left.Code < right.Code
	}
	return left.Dimension < right.Dimension
}

func gapIDs(gaps []MaterialGap) []string {
	ids := make([]string, len(gaps))
	for i, gap := range gaps {
		ids[i] = gap.ID
	}
	return ids
}

func appliedPackageConfiguration(limits PackageLimits, policy *evidence.Policy) AppliedPackageConfiguration {
	mode := "unit_decisions"
	policyDigest := ""
	if policy != nil {
		mode = "resolved_policy"
		encodedPolicy, _ := json.Marshal(policy)
		policyDigest = evidence.Digest(encodedPolicy)
	}
	input := struct {
		Version      string        `json:"version"`
		Limits       PackageLimits `json:"limits"`
		PolicyMode   string        `json:"policy_mode"`
		PolicyDigest string        `json:"policy_digest,omitempty"`
	}{Version: packageVersion, Limits: limits, PolicyMode: mode, PolicyDigest: policyDigest}
	encoded, _ := json.Marshal(input)
	return AppliedPackageConfiguration{
		Version:      packageVersion,
		Limits:       limits,
		PolicyMode:   mode,
		PolicyDigest: policyDigest,
		Digest:       evidence.Digest(encoded),
	}
}

type packageDigestInput struct {
	Version             string                         `json:"version"`
	Scope               Scope                          `json:"scope"`
	ConfigurationDigest string                         `json:"configuration_digest"`
	Evidence            []aigateway.AuthorizedEvidence `json:"evidence"`
	Gaps                []string                       `json:"gaps"`
}

func packageDigest(scope Scope, configurationDigest string, evidenceItems []aigateway.AuthorizedEvidence, gaps []string) (string, error) {
	input := packageDigestInput{
		Version:             packageVersion,
		Scope:               scope,
		ConfigurationDigest: configurationDigest,
		Evidence:            evidenceItems,
		Gaps:                gaps,
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return "", fmt.Errorf("%w: package digest", ErrInvalidPackageComposition)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func packageLocator(locator contract.Locator) (string, error) {
	encoded, err := json.Marshal(locator)
	if err != nil || len(encoded) == 0 {
		return "", ErrInvalidPackageComposition
	}
	return string(encoded), nil
}

func validPackageToken(value string, maxBytes int) bool {
	if value == "" || len([]byte(value)) > maxBytes || strings.TrimSpace(value) != value {
		return false
	}
	for _, r := range value {
		if r == '/' || r == '\\' || r == ':' || r == '\n' || r == '\r' || r == '\t' || r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func finitePackageFloat(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

// aigateway keeps this bound private because the package must not depend on
// a provider implementation. It mirrors the stable gateway contract.
const aigatewayMaxEvidenceContentBytes = 256 << 10
