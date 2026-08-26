package evidence

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/pedrogpaulino/manu/internal/contract"
)

const (
	// Version is the version of the Evidence Unit JSON representation.
	Version = "v1alpha1"
)

var (
	// ErrInvalid identifies an Evidence Unit that cannot be accepted.
	ErrInvalid = errors.New("evidence: invalid")
	// ErrUnsupportedVersion identifies a representation version that this
	// package cannot validate.
	ErrUnsupportedVersion = errors.New("evidence: unsupported version")
	// ErrInvalidDecision identifies a persistence or transfer decision outside
	// the supported vocabulary.
	ErrInvalidDecision = errors.New("evidence: invalid decision")
	// ErrInvalidContent identifies inconsistent content, state, or digest
	// fields.
	ErrInvalidContent = errors.New("evidence: invalid content")
	// ErrInvalidDigest identifies a digest that is not a SHA-256 value.
	ErrInvalidDigest = errors.New("evidence: invalid digest")
	// ErrUnsafeLocator identifies a path or locator that could escape the
	// authorized source scope.
	ErrUnsafeLocator = errors.New("evidence: unsafe locator")
	// ErrLimitExceeded identifies a unit larger than its configured limits.
	ErrLimitExceeded = errors.New("evidence: limit exceeded")
	// ErrInvalidClassification identifies an unsupported content classification
	// or finding.
	ErrInvalidClassification = errors.New("evidence: invalid classification")
	// ErrNotPrepared identifies a unit that has not passed the central content
	// and policy preparation step.
	ErrNotPrepared = errors.New("evidence: not prepared")
)

// Decision is an independent authorization outcome for persistence or
// transfer. The zero value is invalid so callers must make both decisions
// explicit.
type Decision string

const (
	// DecisionUnknown is the unset value and is not valid in a bundle.
	DecisionUnknown Decision = ""
	// DecisionAllow keeps the relevant representation or authorizes the
	// relevant operation.
	DecisionAllow Decision = "allow"
	// DecisionRedact keeps or authorizes only a redacted representation.
	DecisionRedact Decision = "redact"
	// DecisionDeny refuses the relevant persistence or transfer operation.
	DecisionDeny Decision = "deny"
)

// PersistenceDecision is a semantic alias for a decision applied to local
// persistence.
type PersistenceDecision = Decision

// TransferDecision is a semantic alias for a decision applied to external
// transfer.
type TransferDecision = Decision

// Classification is the deterministic safety classification assigned to an
// Evidence Unit before it is retained or transferred. The zero value is kept
// readable for legacy units that predate policy metadata.
type Classification string

const (
	ClassificationUnknown             Classification = ""
	ClassificationSafeText            Classification = "safe_text"
	ClassificationSensitive           Classification = "sensitive"
	ClassificationPromptInjection     Classification = "prompt_injection_like"
	ClassificationBinary              Classification = "binary"
	ClassificationInvalid             Classification = "invalid"
	ClassificationProhibited          Classification = "prohibited"
	ClassificationSecret              Classification = ClassificationSensitive
	ClassificationPromptInjectionLike Classification = ClassificationPromptInjection
)

// Validate checks whether a classification is supported. Unknown is accepted
// only for compatibility with v1alpha1 units produced before policy metadata.
func (c Classification) Validate() error {
	switch c {
	case ClassificationUnknown, ClassificationSafeText, ClassificationSensitive,
		ClassificationPromptInjection, ClassificationBinary, ClassificationInvalid,
		ClassificationProhibited:
		return nil
	default:
		return fmt.Errorf("%w: %q", ErrInvalidClassification, c)
	}
}

// Finding is the wire spelling for a controlled classification reason.
// It is an alias so callers can use []Finding or []string interchangeably.
type Finding = string

// Finding values make the reason for a classification observable without
// retaining the inspected material.
const (
	FindingSecret           = "secret"
	FindingSecretAssignment = "secret_assignment"
	FindingPEMPrivateKey    = "pem_private_key"
	FindingAuthorization    = "authorization"
	FindingBearer           = "bearer"
	FindingPromptInjection  = "prompt_injection"
	FindingBinary           = "binary"
	FindingInvalidUTF8      = "invalid_utf8"
	FindingProhibited       = "prohibited"
)

var knownFindings = map[string]struct{}{
	FindingSecret: {}, FindingSecretAssignment: {}, FindingPEMPrivateKey: {},
	FindingAuthorization: {}, FindingBearer: {}, FindingPromptInjection: {},
	FindingBinary: {}, FindingInvalidUTF8: {}, FindingProhibited: {},
}

func validateFindings(findings []string) error {
	seen := make(map[string]struct{}, len(findings))
	for _, finding := range findings {
		if _, ok := knownFindings[finding]; !ok || finding == "" {
			return fmt.Errorf("%w: finding %q", ErrInvalidClassification, finding)
		}
		if _, duplicate := seen[finding]; duplicate {
			return fmt.Errorf("%w: findings must be unique", ErrInvalidClassification)
		}
		seen[finding] = struct{}{}
	}
	return nil
}

const (
	// PersistenceAllow is an explicit spelling for a persistence decision.
	PersistenceAllow = DecisionAllow
	// PersistenceRedact is an explicit spelling for a persistence decision.
	PersistenceRedact = DecisionRedact
	// PersistenceDeny is an explicit spelling for a persistence decision.
	PersistenceDeny = DecisionDeny
	// TransferAllow is an explicit spelling for a transfer decision.
	TransferAllow = DecisionAllow
	// TransferRedact is an explicit spelling for a transfer decision.
	TransferRedact = DecisionRedact
	// TransferDeny is an explicit spelling for a transfer decision.
	TransferDeny = DecisionDeny
)

// Validate checks whether the decision belongs to the supported vocabulary.
func (d Decision) Validate() error {
	switch d {
	case DecisionAllow, DecisionRedact, DecisionDeny:
		return nil
	default:
		return fmt.Errorf("%w: %q", ErrInvalidDecision, d)
	}
}

// ContentState describes whether an Evidence Unit retains usable content.
type ContentState string

const (
	// ContentStateUnknown is the unset value and is not valid.
	ContentStateUnknown ContentState = ""
	// ContentStatePresent means Content is retained as text.
	ContentStatePresent ContentState = "present"
	// ContentStateRedacted means the original content was replaced or
	// sanitized and RedactionReason explains the decision.
	ContentStateRedacted ContentState = "redacted"
	// ContentStateOmitted means no content is retained, while identity and
	// provenance may still be retained.
	ContentStateOmitted ContentState = "omitted"
	// ContentStateAbsent is a readable alias for omitted content.
	ContentStateAbsent = ContentStateOmitted
)

// RedactionState is a semantic alias for ContentState.
type RedactionState = ContentState

// Validate checks whether the content state is supported.
func (s ContentState) Validate() error {
	switch s {
	case ContentStatePresent, ContentStateRedacted, ContentStateOmitted:
		return nil
	default:
		return fmt.Errorf("%w: state %q", ErrInvalidContent, s)
	}
}

// ContributionRef identifies the contribution that produced or contextualized
// an Evidence Unit. It repeats only stable provenance, not analyzer output.
type ContributionRef struct {
	ID              string `json:"id"`
	ArtifactID      string `json:"artifact_id"`
	AnalyzerID      string `json:"analyzer_id"`
	AnalyzerVersion string `json:"analyzer_version"`
	Method          string `json:"method"`
}

// Validate checks the identity and provenance fields of a contribution
// reference.
func (r ContributionRef) Validate() error {
	fields := []struct {
		name  string
		value string
	}{
		{name: "id", value: r.ID},
		{name: "artifact_id", value: r.ArtifactID},
		{name: "analyzer_id", value: r.AnalyzerID},
		{name: "analyzer_version", value: r.AnalyzerVersion},
		{name: "method", value: r.Method},
	}
	for _, field := range fields {
		if field.name == "method" {
			if err := validateMethod(field.value); err != nil {
				return err
			}
			continue
		}
		if err := validateIdentifier(field.name, field.value); err != nil {
			return err
		}
	}
	return nil
}

// EvidenceUnit is the smallest transferable or persistible material that can
// support or contest an observation. It is not a Knowledge Claim.
type EvidenceUnit struct {
	Version           string           `json:"version"`
	ID                string           `json:"id"`
	OrganizationID    string           `json:"organization_id"`
	SourceID          string           `json:"source_id"`
	SnapshotID        string           `json:"snapshot_id"`
	ArtifactID        string           `json:"artifact_id"`
	Contribution      ContributionRef  `json:"contribution"`
	Locator           contract.Locator `json:"locator"`
	ContentState      ContentState     `json:"content_state"`
	Content           string           `json:"content,omitempty"`
	ContentHash       string           `json:"content_hash"`
	Truncated         bool             `json:"truncated,omitempty"`
	RedactionReason   string           `json:"redaction_reason,omitempty"`
	ContentBytes      int64            `json:"content_bytes"`
	ContentCharacters int64            `json:"content_characters"`
	Persist           Decision         `json:"persist"`
	ExternalTransfer  Decision         `json:"external_transfer"`
	Classification    Classification   `json:"classification,omitempty"`
	Findings          []string         `json:"findings,omitempty"`
}

// UnitLimits bounds one Evidence Unit. A zero value means no bound for that
// field; bundle-level validation normally supplies configured non-zero limits.
type UnitLimits struct {
	MaxBytes      int64
	MaxCharacters int64
}

// Validate checks that limits are representable.
func (l UnitLimits) Validate() error {
	if l.MaxBytes < 0 || l.MaxCharacters < 0 {
		return fmt.Errorf("%w: unit limits must not be negative", ErrLimitExceeded)
	}
	return nil
}

// Validate checks the unit using no additional size bound.
func (u EvidenceUnit) Validate() error {
	return u.ValidateWithLimits(UnitLimits{})
}

// ValidateWithLimits checks identity, provenance, content, decisions and
// locator safety before the unit can enter a bundle.
func (u EvidenceUnit) ValidateWithLimits(limits UnitLimits) error {
	if err := limits.Validate(); err != nil {
		return err
	}
	if u.Version != Version {
		return fmt.Errorf("%w: got %q, want %q", ErrUnsupportedVersion, u.Version, Version)
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{name: "id", value: u.ID},
		{name: "organization_id", value: u.OrganizationID},
		{name: "source_id", value: u.SourceID},
		{name: "snapshot_id", value: u.SnapshotID},
		{name: "artifact_id", value: u.ArtifactID},
	} {
		if err := validateIdentifier(field.name, field.value); err != nil {
			return err
		}
	}
	if err := u.Contribution.Validate(); err != nil {
		return fmt.Errorf("contribution: %w", err)
	}
	if u.Contribution.ArtifactID != u.ArtifactID {
		return fmt.Errorf("%w: contribution artifact id %q does not match artifact %q", ErrInvalid, u.Contribution.ArtifactID, u.ArtifactID)
	}
	if err := validateLocator(u.Locator, u.SourceID, u.ArtifactID); err != nil {
		return err
	}
	if err := u.ContentState.Validate(); err != nil {
		return err
	}
	if !isSHA256Digest(u.ContentHash) {
		return fmt.Errorf("%w: content hash", ErrInvalidDigest)
	}
	if err := validateContent(u); err != nil {
		return err
	}
	if err := u.Persist.Validate(); err != nil {
		return fmt.Errorf("persist: %w", err)
	}
	if err := u.ExternalTransfer.Validate(); err != nil {
		return fmt.Errorf("external transfer: %w", err)
	}
	if err := u.Classification.Validate(); err != nil {
		return err
	}
	if err := validateFindings(u.Findings); err != nil {
		return err
	}
	if err := validateClassificationFindings(u.Classification, u.Findings); err != nil {
		return err
	}
	if err := validatePolicyInvariants(u); err != nil {
		return err
	}
	if limits.MaxBytes > 0 && u.ContentBytes > limits.MaxBytes {
		return fmt.Errorf("%w: content bytes %d exceed %d", ErrLimitExceeded, u.ContentBytes, limits.MaxBytes)
	}
	if limits.MaxCharacters > 0 && u.ContentCharacters > limits.MaxCharacters {
		return fmt.Errorf("%w: content characters %d exceed %d", ErrLimitExceeded, u.ContentCharacters, limits.MaxCharacters)
	}
	expectedID := EvidenceID(u)
	if u.ID != expectedID {
		return fmt.Errorf("%w: id %q does not match deterministic identity %q", ErrInvalid, u.ID, expectedID)
	}
	return nil
}

// ValidatePrepared checks the stronger boundary required before persistence.
// A zero classification remains accepted for old, explicitly authorized
// units, but any unit carrying policy metadata must be internally coherent.
func (u EvidenceUnit) ValidatePrepared() error {
	if err := u.Validate(); err != nil {
		return err
	}
	if u.ContentState == ContentStatePresent {
		// Reinspect retained content at the serialization boundary. A sender
		// cannot make unsafe material safe merely by claiming safe_text.
		inspection := InspectContent(u.Content)
		if inspection.Classification != ClassificationSafeText {
			return fmt.Errorf("%w: present content is not safe text", ErrNotPrepared)
		}
		if u.Classification != ClassificationUnknown && u.Classification != ClassificationSafeText {
			return fmt.Errorf("%w: present content has non-safe classification", ErrNotPrepared)
		}
	}
	if u.Classification == ClassificationUnknown && u.Content != "" {
		inspection := InspectContent(u.Content)
		if inspection.Classification != ClassificationSafeText {
			return fmt.Errorf("%w: legacy content is not safe text", ErrNotPrepared)
		}
	}
	return nil
}

func validateClassificationFindings(classification Classification, findings []string) error {
	if classification == ClassificationUnknown {
		if len(findings) != 0 {
			return fmt.Errorf("%w: unknown classification cannot carry findings", ErrInvalidClassification)
		}
		return nil
	}

	contains := func(want string) bool {
		for _, finding := range findings {
			if finding == want {
				return true
			}
		}
		return false
	}
	sensitive := containsSensitiveFinding(findings)
	switch classification {
	case ClassificationSafeText:
		if len(findings) != 0 {
			return fmt.Errorf("%w: safe text cannot carry findings", ErrInvalidClassification)
		}
	case ClassificationSensitive:
		if !sensitive {
			return fmt.Errorf("%w: sensitive classification requires a sensitive finding", ErrInvalidClassification)
		}
	case ClassificationPromptInjection:
		if !contains(FindingPromptInjection) {
			return fmt.Errorf("%w: prompt classification requires prompt_injection finding", ErrInvalidClassification)
		}
	case ClassificationBinary:
		if !contains(FindingBinary) {
			return fmt.Errorf("%w: binary classification requires binary finding", ErrInvalidClassification)
		}
	case ClassificationInvalid:
		if !contains(FindingInvalidUTF8) {
			return fmt.Errorf("%w: invalid classification requires invalid_utf8 finding", ErrInvalidClassification)
		}
	case ClassificationProhibited:
		if !contains(FindingProhibited) {
			return fmt.Errorf("%w: prohibited classification requires prohibited finding", ErrInvalidClassification)
		}
	}
	return nil
}

func validatePolicyInvariants(u EvidenceUnit) error {
	if u.Persist == DecisionDeny && (u.ContentState != ContentStateOmitted || u.Content != "" || u.ContentBytes != 0 || u.ContentCharacters != 0) {
		return fmt.Errorf("%w: denied persistence cannot carry content", ErrInvalidContent)
	}
	if u.Persist == DecisionRedact && u.ContentState == ContentStatePresent {
		return fmt.Errorf("%w: redacted persistence cannot be present", ErrInvalidContent)
	}
	if u.Classification != ClassificationUnknown && u.Classification != ClassificationSafeText && u.ContentState == ContentStatePresent {
		return fmt.Errorf("%w: classified content cannot be present", ErrInvalidContent)
	}
	if (u.Classification == ClassificationBinary || u.Classification == ClassificationInvalid || u.Classification == ClassificationProhibited) && u.ContentState != ContentStateOmitted {
		return fmt.Errorf("%w: binary, invalid, or prohibited content must be omitted", ErrInvalidContent)
	}
	if u.ExternalTransfer == DecisionAllow && u.Classification != ClassificationUnknown && u.Classification != ClassificationSafeText {
		return fmt.Errorf("%w: transfer allow is not authorized for classification", ErrInvalidDecision)
	}
	return nil
}

func validateContent(u EvidenceUnit) error {
	if !utf8.ValidString(u.Content) {
		return fmt.Errorf("%w: content is not valid utf-8", ErrInvalidContent)
	}
	contentBytes := int64(len([]byte(u.Content)))
	contentCharacters := int64(utf8.RuneCountInString(u.Content))
	if u.ContentBytes != contentBytes || u.ContentCharacters != contentCharacters {
		return fmt.Errorf("%w: declared content counts do not match content", ErrInvalidContent)
	}
	switch u.ContentState {
	case ContentStatePresent:
		if u.Content == "" {
			return fmt.Errorf("%w: present content is empty", ErrInvalidContent)
		}
		if u.RedactionReason != "" {
			return fmt.Errorf("%w: present content cannot have a redaction reason", ErrInvalidContent)
		}
		if ContentDigest(u.Content) != u.ContentHash {
			return fmt.Errorf("%w: content hash does not match content", ErrInvalidDigest)
		}
	case ContentStateRedacted:
		if strings.TrimSpace(u.RedactionReason) == "" {
			return fmt.Errorf("%w: redacted content requires a reason", ErrInvalidContent)
		}
		if u.Content != RedactedContent {
			return fmt.Errorf("%w: redacted content must use the fixed representation", ErrInvalidContent)
		}
	case ContentStateOmitted:
		if u.Content != "" || u.ContentBytes != 0 || u.ContentCharacters != 0 {
			return fmt.Errorf("%w: omitted content must not carry content or counts", ErrInvalidContent)
		}
	}
	return nil
}

func validateLocator(locator contract.Locator, sourceID, artifactID string) error {
	if err := locator.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrUnsafeLocator, err)
	}
	if locator.SourceID != "" && locator.SourceID != sourceID {
		return fmt.Errorf("%w: locator source id %q does not match source %q", ErrInvalid, locator.SourceID, sourceID)
	}
	if locator.ArtifactID != "" && locator.ArtifactID != artifactID {
		return fmt.Errorf("%w: locator artifact id %q does not match artifact %q", ErrInvalid, locator.ArtifactID, artifactID)
	}
	if err := validateRelativePath(locator.Path); err != nil {
		return err
	}
	for _, value := range []string{locator.URI, locator.Member} {
		for _, r := range value {
			if unicode.IsControl(r) {
				return fmt.Errorf("%w: locator contains control characters", ErrUnsafeLocator)
			}
		}
	}
	return nil
}

func validateRelativePath(value string) error {
	if value == "" {
		return nil
	}
	normalized := strings.ReplaceAll(value, "\\", "/")
	if path.IsAbs(normalized) || hasWindowsDrivePrefix(normalized) {
		return fmt.Errorf("%w: path %q is absolute", ErrUnsafeLocator, value)
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return fmt.Errorf("%w: path contains control characters", ErrUnsafeLocator)
		}
	}
	for _, segment := range strings.Split(normalized, "/") {
		if segment == ".." {
			return fmt.Errorf("%w: path %q contains parent traversal", ErrUnsafeLocator, value)
		}
	}
	if path.Clean(normalized) == "." {
		return fmt.Errorf("%w: path %q is empty after cleaning", ErrUnsafeLocator, value)
	}
	return nil
}

func hasWindowsDrivePrefix(value string) bool {
	if len(value) < 2 || value[1] != ':' {
		return false
	}
	return (value[0] >= 'a' && value[0] <= 'z') || (value[0] >= 'A' && value[0] <= 'Z')
}

func validateIdentifier(name, value string) error {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return fmt.Errorf("%w: %s is required", ErrInvalid, name)
	}
	for _, r := range value {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return fmt.Errorf("%w: %s contains whitespace or control characters", ErrInvalid, name)
		}
	}
	return nil
}

func validateMethod(value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%w: method is required", ErrInvalid)
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return fmt.Errorf("%w: method contains control characters", ErrInvalid)
		}
	}
	return nil
}

func isSHA256Digest(value string) bool {
	if len(value) != sha256.Size*2 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

// ContentDigest computes the deterministic SHA-256 digest of retained text.
func ContentDigest(content string) string {
	return Digest([]byte(content))
}

// Digest computes a lowercase hexadecimal SHA-256 digest.
func Digest(content []byte) string {
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])
}

// EvidenceID computes a deterministic identity from stable provenance,
// content, and prepared classification fields. It never includes policy
// decisions, timestamps, or machine-local state.
func EvidenceID(unit EvidenceUnit) string {
	parts := []string{
		unit.Version,
		unit.OrganizationID,
		unit.SourceID,
		unit.SnapshotID,
		unit.ArtifactID,
		unit.Contribution.ID,
		unit.Contribution.AnalyzerID,
		unit.Contribution.AnalyzerVersion,
		unit.Contribution.Method,
		unit.Locator.URI,
		unit.Locator.SourceID,
		unit.Locator.ArtifactID,
		unit.Locator.Path,
		unit.Locator.Member,
		strconv.Itoa(unit.Locator.StartLine),
		strconv.Itoa(unit.Locator.StartColumn),
		strconv.Itoa(unit.Locator.EndLine),
		strconv.Itoa(unit.Locator.EndColumn),
		strconv.FormatInt(unit.Locator.ByteOffset, 10),
		strconv.FormatInt(unit.Locator.ByteLength, 10),
		strconv.FormatBool(unit.Truncated),
		unit.ContentHash,
	}
	// Keep the identity of legacy units stable while making policy-relevant
	// classification and findings part of every prepared unit's identity.
	if unit.Classification != ClassificationUnknown || len(unit.Findings) != 0 {
		parts = append(parts, string(unit.Classification), strings.Join(canonicalFindings(unit.Findings), "\x00"))
	}
	return "evidence-" + Digest(joinParts(parts))
}

func canonicalFindings(findings []string) []string {
	if len(findings) == 0 {
		return nil
	}
	canonical := append([]string(nil), findings...)
	// Findings are short controlled strings. Insertion sort avoids importing a
	// second helper package into the identity path and keeps the copy local.
	for i := 1; i < len(canonical); i++ {
		for j := i; j > 0 && canonical[j] < canonical[j-1]; j-- {
			canonical[j], canonical[j-1] = canonical[j-1], canonical[j]
		}
	}
	return canonical
}

func joinParts(parts []string) []byte {
	var builder strings.Builder
	for _, part := range parts {
		builder.WriteString(strconv.Itoa(len(part)))
		builder.WriteByte(':')
		builder.WriteString(part)
	}
	return []byte(builder.String())
}
