package evaluation

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/pedrogpaulino/manu/internal/contract"
)

const (
	// Version identifies the physical JSON representation of an evaluation
	// case set. It is independent from the Analysis Bundle contract.
	Version = "v1alpha1"

	// CasesFileName is the canonical filename used by the repository fixture.
	CasesFileName = "cases.json"

	maxCasesFileBytes = 2 << 20
	maxCases          = 512
	maxCaseIDBytes    = 128
	maxTextBytes      = 4 << 10
	maxListItems      = 512
)

var (
	// ErrInvalidCases identifies a malformed or incomplete case set.
	ErrInvalidCases = errors.New("evaluation: invalid cases")
	// ErrUnsupportedVersion identifies a case representation that this
	// package cannot interpret.
	ErrUnsupportedVersion = errors.New("evaluation: unsupported version")
	// ErrDuplicateCase identifies duplicate case, claim, evidence, or gap
	// identities in one case set.
	ErrDuplicateCase = errors.New("evaluation: duplicate case identity")
	// ErrInvalidCaseKind identifies a kind outside the controlled vocabulary.
	ErrInvalidCaseKind = errors.New("evaluation: invalid case kind")
	// ErrInvalidAttributionStage identifies an uncontrolled failure stage.
	ErrInvalidAttributionStage = errors.New("evaluation: invalid attribution stage")
	// ErrUnsafeCase identifies traversal, secret, or raw-content material in a
	// case definition.
	ErrUnsafeCase = errors.New("evaluation: unsafe case material")
	// ErrCaseLimitExceeded identifies a case file or collection above its
	// bounded contract.
	ErrCaseLimitExceeded = errors.New("evaluation: case limit exceeded")
)

// CaseState describes the curation lifecycle of an evaluation case.
type CaseState string

const (
	CaseStateDraft   CaseState = "draft"
	CaseStateCurated CaseState = "curated"
	CaseStateRetired CaseState = "retired"
)

// CaseKind identifies the competency exercised by a case. Possible Flow is a
// static reconstruction and is deliberately distinct from execution evidence.
type CaseKind string

const (
	CaseKindInventory    CaseKind = "inventory"
	CaseKindProvenance   CaseKind = "provenance"
	CaseKindPossibleFlow CaseKind = "possible_flow"
	CaseKindAbstention   CaseKind = "abstention"
)

// ClaimOrigin identifies the epistemic state expected for an acceptable claim.
type ClaimOrigin string

const (
	ClaimOriginObserved  ClaimOrigin = "observed"
	ClaimOriginGenerated ClaimOrigin = "generated"
	ClaimOriginCurated   ClaimOrigin = "curated"
)

// ClaimMatchMode makes clear that acceptable claims are semantic criteria,
// not byte-for-byte answer strings.
type ClaimMatchMode string

const ClaimMatchSemantic ClaimMatchMode = "semantic"

// AttributionStage identifies the stage to which an evaluation failure is
// attributed. The vocabulary is intentionally closed for comparable reports.
type AttributionStage string

const (
	AttributionExtraction AttributionStage = "extraction"
	AttributionRetrieval  AttributionStage = "retrieval"
	AttributionGeneration AttributionStage = "generation"
	AttributionPolicy     AttributionStage = "policy"
)

// CaseSet is the versioned, deterministic envelope stored by the evaluation
// corpus. LoadCases returns its cases sorted by stable identity.
type CaseSet struct {
	Version string           `json:"version"`
	Cases   []EvaluationCase `json:"cases"`
}

// Cases is a concise alias for CaseSet.
type Cases = CaseSet

// EvaluationCase is one immutable competency case. It stores semantic
// acceptance criteria and references, never source code or a full answer.
type EvaluationCase struct {
	CaseID             string               `json:"case_id"`
	CaseVersion        int                  `json:"case_version"`
	State              CaseState            `json:"state"`
	CorpusID           string               `json:"corpus_id"`
	CorpusRevision     string               `json:"corpus_revision"`
	SourceID           string               `json:"source_id"`
	SourceRevision     string               `json:"source_revision"`
	Scope              CaseScope            `json:"scope"`
	Inclusions         []ScopeItem          `json:"inclusions"`
	Exclusions         []ScopeItem          `json:"exclusions"`
	AuthorizationRef   string               `json:"authorization_ref"`
	Audience           string               `json:"audience"`
	CompetenceQuestion string               `json:"competence_question"`
	Kind               CaseKind             `json:"kind"`
	AcceptableClaims   []AcceptableClaim    `json:"acceptable_claims"`
	ExpectedEvidence   []ExpectedEvidence   `json:"expected_evidence"`
	ExpectedGaps       []ExpectedGap        `json:"expected_gaps"`
	Authors            []string             `json:"authors"`
	Reviewers          []string             `json:"reviewers"`
	ReviewedAt         string               `json:"reviewed_at"`
	FailureAttribution []FailureAttribution `json:"failure_attribution"`
}

// Case is a concise alias for EvaluationCase.
type Case = EvaluationCase

// CaseScope records the dimensions intentionally attempted by a case.
type CaseScope struct {
	Purpose    string   `json:"purpose"`
	Dimensions []string `json:"dimensions"`
	Artifacts  []string `json:"artifacts"`
}

// ScopeItem identifies an included or excluded logical item and its reason.
type ScopeItem struct {
	Ref    string `json:"ref"`
	Reason string `json:"reason"`
}

// AcceptableClaim is a semantic acceptance criterion. Statement is reference
// prose for reviewers; the loader never compares generated text to it.
type AcceptableClaim struct {
	ClaimID     string         `json:"claim_id"`
	Statement   string         `json:"statement"`
	Origin      ClaimOrigin    `json:"origin"`
	Match       ClaimMatchMode `json:"match"`
	EvidenceIDs []string       `json:"evidence_ids"`
	GapIDs      []string       `json:"gap_ids"`
}

// ExpectedEvidence identifies evidence by stable ID, locator, or a bounded
// metadata pattern. It never carries an excerpt or source content.
type ExpectedEvidence struct {
	EvidenceID string            `json:"evidence_id,omitempty"`
	Kind       string            `json:"kind"`
	Locator    *contract.Locator `json:"locator,omitempty"`
	Pattern    *EvidencePattern  `json:"pattern,omitempty"`
}

// EvidencePattern describes metadata to locate without copying a source
// fragment. Values are paths, symbols, members, attributes, or XPath-like
// selectors only.
type EvidencePattern struct {
	PathPattern  string `json:"path_pattern,omitempty"`
	Member       string `json:"member,omitempty"`
	Symbol       string `json:"symbol,omitempty"`
	Attribute    string `json:"attribute,omitempty"`
	XPath        string `json:"xpath,omitempty"`
	ValuePattern string `json:"value_pattern,omitempty"`
}

// ExpectedGap is a mandatory limitation that a correct evaluation must keep
// visible, together with the stage responsible for recognizing it.
type ExpectedGap struct {
	GapID            string           `json:"gap_id"`
	Code             string           `json:"code"`
	Statement        string           `json:"statement"`
	AttributionStage AttributionStage `json:"attribution_stage"`
}

// FailureAttribution records a known failure/limitation classification for a
// case. It is metadata, not a result produced by the future evaluation runner.
type FailureAttribution struct {
	Stage  AttributionStage `json:"stage"`
	Code   string           `json:"code"`
	Reason string           `json:"reason"`
}

// Validate checks the complete case envelope without changing its order.
func (s CaseSet) Validate() error {
	if s.Version != Version {
		return fmt.Errorf("%w: case set version", ErrUnsupportedVersion)
	}
	if len(s.Cases) == 0 {
		return fmt.Errorf("%w: no cases", ErrInvalidCases)
	}
	if len(s.Cases) > maxCases {
		return ErrCaseLimitExceeded
	}
	seen := make(map[string]struct{}, len(s.Cases))
	for index, item := range s.Cases {
		if err := item.Validate(); err != nil {
			return fmt.Errorf("%w: case %d: %w", ErrInvalidCases, index, err)
		}
		identity := caseIdentity(item)
		if _, exists := seen[identity]; exists {
			return ErrDuplicateCase
		}
		seen[identity] = struct{}{}
	}
	return nil
}

// Validate checks one case's required identity, curation, references, and
// bounded metadata.
func (c EvaluationCase) Validate() error {
	if err := validateIdentifier("case_id", c.CaseID, maxCaseIDBytes); err != nil {
		return err
	}
	if c.CaseVersion < 1 {
		return fmt.Errorf("%w: case version", ErrInvalidCases)
	}
	if c.CaseVersion > 10_000 {
		return fmt.Errorf("%w: case version", ErrInvalidCases)
	}
	switch c.State {
	case CaseStateDraft, CaseStateCurated, CaseStateRetired:
	default:
		return fmt.Errorf("%w: case state", ErrInvalidCases)
	}
	switch c.Kind {
	case CaseKindInventory, CaseKindProvenance, CaseKindPossibleFlow, CaseKindAbstention:
	default:
		return ErrInvalidCaseKind
	}
	for name, value := range map[string]string{
		"corpus_id":         c.CorpusID,
		"corpus_revision":   c.CorpusRevision,
		"source_id":         c.SourceID,
		"source_revision":   c.SourceRevision,
		"authorization_ref": c.AuthorizationRef,
	} {
		if err := validateIdentifier(name, value, maxCaseIDBytes); err != nil {
			return err
		}
	}
	if err := c.Scope.Validate(); err != nil {
		return err
	}
	if err := validateSafeText("audience", c.Audience, maxTextBytes); err != nil {
		return err
	}
	if err := validateSafeText("competence_question", c.CompetenceQuestion, maxTextBytes); err != nil {
		return err
	}
	if err := validateScopeItems(c.Inclusions, "inclusion"); err != nil {
		return err
	}
	if err := validateScopeItems(c.Exclusions, "exclusion"); err != nil {
		return err
	}
	if len(c.AcceptableClaims) == 0 || len(c.AcceptableClaims) > maxListItems {
		return fmt.Errorf("%w: acceptable claims", ErrInvalidCases)
	}
	if len(c.ExpectedEvidence) == 0 || len(c.ExpectedEvidence) > maxListItems {
		return fmt.Errorf("%w: expected evidence", ErrInvalidCases)
	}
	if len(c.ExpectedGaps) == 0 || len(c.ExpectedGaps) > maxListItems {
		return fmt.Errorf("%w: expected gaps", ErrInvalidCases)
	}
	if len(c.FailureAttribution) == 0 || len(c.FailureAttribution) > maxListItems {
		return fmt.Errorf("%w: failure attribution", ErrInvalidCases)
	}
	if err := validateReview(c.Authors, c.Reviewers, c.ReviewedAt); err != nil {
		return err
	}

	evidenceIDs, err := validateExpectedEvidence(c.ExpectedEvidence)
	if err != nil {
		return err
	}
	gapIDs, err := validateExpectedGaps(c.ExpectedGaps)
	if err != nil {
		return err
	}
	if err := validateClaims(c.AcceptableClaims, evidenceIDs, gapIDs); err != nil {
		return err
	}
	if err := validateAttribution(c.FailureAttribution); err != nil {
		return err
	}
	if c.Kind == CaseKindAbstention && !hasClaimGap(c.AcceptableClaims) {
		return fmt.Errorf("%w: abstention case needs a gap claim", ErrInvalidCases)
	}
	return nil
}

// Validate checks scope prose and dimensions without reading the source.
func (s CaseScope) Validate() error {
	if err := validateSafeText("scope purpose", s.Purpose, maxTextBytes); err != nil {
		return err
	}
	if len(s.Dimensions) == 0 || len(s.Dimensions) > maxListItems {
		return fmt.Errorf("%w: scope dimensions", ErrInvalidCases)
	}
	if err := validateStringList(s.Dimensions, "scope dimension", maxTextBytes); err != nil {
		return err
	}
	if len(s.Artifacts) > maxListItems {
		return fmt.Errorf("%w: scope artifacts", ErrInvalidCases)
	}
	return validateStringList(s.Artifacts, "scope artifact", maxTextBytes)
}

// Normalize validates and returns a reproducibly sorted defensive copy.
func (s CaseSet) Normalize() (CaseSet, error) {
	if err := s.Validate(); err != nil {
		return CaseSet{}, err
	}
	normalized := cloneCaseSet(s)
	sort.SliceStable(normalized.Cases, func(i, j int) bool {
		left, right := normalized.Cases[i], normalized.Cases[j]
		if left.CaseID != right.CaseID {
			return left.CaseID < right.CaseID
		}
		return left.CaseVersion < right.CaseVersion
	})
	for index := range normalized.Cases {
		normalizeCaseCollections(&normalized.Cases[index])
	}
	return normalized, nil
}

// DecodeCases reads one bounded JSON document, rejects unknown fields and
// trailing values, then validates and sorts it.
func DecodeCases(reader io.Reader) (CaseSet, error) {
	if reader == nil {
		return CaseSet{}, fmt.Errorf("%w: nil reader", ErrInvalidCases)
	}
	data, err := io.ReadAll(io.LimitReader(reader, maxCasesFileBytes+1))
	if err != nil {
		return CaseSet{}, fmt.Errorf("%w: reading cases", ErrInvalidCases)
	}
	if len(data) > maxCasesFileBytes {
		return CaseSet{}, ErrCaseLimitExceeded
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var cases CaseSet
	if err := decoder.Decode(&cases); err != nil {
		return CaseSet{}, fmt.Errorf("%w: decoding cases", ErrInvalidCases)
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); err != io.EOF {
		return CaseSet{}, fmt.Errorf("%w: multiple JSON values", ErrInvalidCases)
	}
	return cases.Normalize()
}

// Decode is an alias for DecodeCases.
func Decode(reader io.Reader) (CaseSet, error) { return DecodeCases(reader) }

// LoadCases reads one case file without requiring or exposing an absolute
// corpus path.
func LoadCases(filePath string) (CaseSet, error) {
	if strings.TrimSpace(filePath) == "" {
		return CaseSet{}, fmt.Errorf("%w: empty path", ErrInvalidCases)
	}
	file, err := os.Open(filePath)
	if err != nil {
		return CaseSet{}, fmt.Errorf("%w: opening cases", ErrInvalidCases)
	}
	defer file.Close()
	return DecodeCases(file)
}

// Load is an alias for LoadCases.
func Load(filePath string) (CaseSet, error) { return LoadCases(filePath) }

// ReadCases is an alias for LoadCases.
func ReadCases(filePath string) (CaseSet, error) { return LoadCases(filePath) }

// MarshalCases returns normalized, indented JSON with a final newline. It is
// the canonical representation used to compare case fixtures byte-for-byte.
func MarshalCases(cases CaseSet) ([]byte, error) {
	normalized, err := cases.Normalize()
	if err != nil {
		return nil, err
	}
	encoded, err := json.MarshalIndent(normalized, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("%w: encoding cases", ErrInvalidCases)
	}
	return append(encoded, '\n'), nil
}

// WriteCases writes a normalized case set for fixture and local tooling use.
// It does not create or modify any corpus source.
func WriteCases(filePath string, cases CaseSet) error {
	data, err := MarshalCases(cases)
	if err != nil {
		return err
	}
	if strings.TrimSpace(filePath) == "" {
		return fmt.Errorf("%w: empty path", ErrInvalidCases)
	}
	if err := os.WriteFile(filePath, data, 0o644); err != nil {
		return fmt.Errorf("%w: writing cases", ErrInvalidCases)
	}
	return nil
}

func validateExpectedEvidence(items []ExpectedEvidence) (map[string]struct{}, error) {
	seen := make(map[string]struct{}, len(items))
	for index, item := range items {
		identity := "anonymous\x00" + evidenceSortKey(item)
		if item.EvidenceID != "" {
			if err := validateIdentifier("evidence_id", item.EvidenceID, maxCaseIDBytes); err != nil {
				return nil, err
			}
			if _, exists := seen[item.EvidenceID]; exists {
				return nil, ErrDuplicateCase
			}
			seen[item.EvidenceID] = struct{}{}
			identity = "id\x00" + item.EvidenceID
		}
		if err := validateSafeText("evidence kind", item.Kind, 256); err != nil {
			return nil, err
		}
		if item.Locator == nil && item.Pattern == nil {
			return nil, fmt.Errorf("%w: evidence %d has no locator or pattern", ErrInvalidCases, index)
		}
		if item.Locator != nil {
			if err := validateCaseLocator(*item.Locator); err != nil {
				return nil, err
			}
		}
		if item.Pattern != nil {
			if err := item.Pattern.Validate(); err != nil {
				return nil, err
			}
		}
		if item.EvidenceID == "" {
			if _, exists := seen[identity]; exists {
				return nil, ErrDuplicateCase
			}
			seen[identity] = struct{}{}
		}
	}
	return seen, nil
}

// Validate checks a metadata-only evidence pattern.
func (p EvidencePattern) Validate() error {
	values := []struct {
		name  string
		value string
	}{
		{"path_pattern", p.PathPattern},
		{"member", p.Member},
		{"symbol", p.Symbol},
		{"attribute", p.Attribute},
		{"xpath", p.XPath},
		{"value_pattern", p.ValuePattern},
	}
	seen := false
	for _, item := range values {
		if item.value == "" {
			continue
		}
		seen = true
		if err := validateSafeText("evidence pattern "+item.name, item.value, maxTextBytes); err != nil {
			return err
		}
		if item.name == "path_pattern" {
			if err := validatePortablePath(item.value); err != nil {
				return err
			}
		}
	}
	if !seen {
		return fmt.Errorf("%w: empty evidence pattern", ErrInvalidCases)
	}
	return nil
}

func validateExpectedGaps(items []ExpectedGap) (map[string]struct{}, error) {
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		if err := validateIdentifier("gap_id", item.GapID, maxCaseIDBytes); err != nil {
			return nil, err
		}
		if _, exists := seen[item.GapID]; exists {
			return nil, ErrDuplicateCase
		}
		seen[item.GapID] = struct{}{}
		if err := validateIdentifier("gap_code", item.Code, maxCaseIDBytes); err != nil {
			return nil, err
		}
		if err := validateSafeText("gap statement", item.Statement, maxTextBytes); err != nil {
			return nil, err
		}
		if !validAttributionStage(item.AttributionStage) {
			return nil, ErrInvalidAttributionStage
		}
	}
	return seen, nil
}

func validateClaims(items []AcceptableClaim, evidenceIDs, gapIDs map[string]struct{}) error {
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		if err := validateIdentifier("claim_id", item.ClaimID, maxCaseIDBytes); err != nil {
			return err
		}
		if _, exists := seen[item.ClaimID]; exists {
			return ErrDuplicateCase
		}
		seen[item.ClaimID] = struct{}{}
		if err := validateSafeText("claim statement", item.Statement, maxTextBytes); err != nil {
			return err
		}
		switch item.Origin {
		case ClaimOriginObserved, ClaimOriginGenerated, ClaimOriginCurated:
		default:
			return fmt.Errorf("%w: claim origin", ErrInvalidCases)
		}
		if item.Match != ClaimMatchSemantic {
			return fmt.Errorf("%w: claim match must be semantic", ErrInvalidCases)
		}
		if len(item.EvidenceIDs) == 0 && len(item.GapIDs) == 0 {
			return fmt.Errorf("%w: claim has no evidence or gap", ErrInvalidCases)
		}
		if err := validateReferences(item.EvidenceIDs, evidenceIDs, "claim evidence"); err != nil {
			return err
		}
		if err := validateReferences(item.GapIDs, gapIDs, "claim gap"); err != nil {
			return err
		}
	}
	return nil
}

func validateAttribution(items []FailureAttribution) error {
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		if !validAttributionStage(item.Stage) {
			return ErrInvalidAttributionStage
		}
		if err := validateIdentifier("attribution code", item.Code, maxCaseIDBytes); err != nil {
			return err
		}
		if err := validateSafeText("attribution reason", item.Reason, maxTextBytes); err != nil {
			return err
		}
		identity := string(item.Stage) + "\x00" + item.Code
		if _, exists := seen[identity]; exists {
			return ErrDuplicateCase
		}
		seen[identity] = struct{}{}
	}
	return nil
}

func validateReview(authors, reviewers []string, reviewedAt string) error {
	if len(authors) == 0 || len(authors) > maxListItems || len(reviewers) == 0 || len(reviewers) > maxListItems {
		return fmt.Errorf("%w: authors and reviewers", ErrInvalidCases)
	}
	if err := validateStringList(authors, "author", maxTextBytes); err != nil {
		return err
	}
	if err := validateStringList(reviewers, "reviewer", maxTextBytes); err != nil {
		return err
	}
	if _, err := time.Parse(time.RFC3339, reviewedAt); err != nil {
		return fmt.Errorf("%w: review timestamp", ErrInvalidCases)
	}
	return nil
}

func validateScopeItems(items []ScopeItem, label string) error {
	if len(items) > maxListItems {
		return fmt.Errorf("%w: %s count", ErrInvalidCases, label)
	}
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		if err := validateSafeText(label+" ref", item.Ref, maxTextBytes); err != nil {
			return err
		}
		if strings.Contains(item.Ref, "/") || strings.Contains(item.Ref, "\\") {
			if err := validatePortablePath(item.Ref); err != nil {
				return err
			}
		}
		if err := validateSafeText(label+" reason", item.Reason, maxTextBytes); err != nil {
			return err
		}
		if _, exists := seen[item.Ref]; exists {
			return ErrDuplicateCase
		}
		seen[item.Ref] = struct{}{}
	}
	return nil
}

func validateStringList(values []string, label string, maxBytes int) error {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if err := validateSafeText(label, value, maxBytes); err != nil {
			return err
		}
		if _, exists := seen[value]; exists {
			return ErrDuplicateCase
		}
		seen[value] = struct{}{}
	}
	return nil
}

func validateReferences(values []string, known map[string]struct{}, label string) error {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if err := validateIdentifier(label, value, maxCaseIDBytes); err != nil {
			return err
		}
		if _, duplicate := seen[value]; duplicate {
			return ErrDuplicateCase
		}
		if _, exists := known[value]; !exists {
			return fmt.Errorf("%w: %s reference", ErrInvalidCases, label)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func validateCaseLocator(locator contract.Locator) error {
	if err := locator.Validate(); err != nil {
		return fmt.Errorf("%w: locator", ErrInvalidCases)
	}
	if locator.Path != "" {
		if err := validatePortablePath(locator.Path); err != nil {
			return err
		}
	}
	for name, value := range map[string]string{
		"locator_uri":      locator.URI,
		"locator_source":   locator.SourceID,
		"locator_artifact": locator.ArtifactID,
		"locator_member":   locator.Member,
	} {
		if value == "" {
			continue
		}
		if err := validateSafeText(name, value, maxTextBytes); err != nil {
			return err
		}
	}
	return nil
}

func validatePortablePath(value string) error {
	if value == "" {
		return fmt.Errorf("%w: empty path", ErrUnsafeCase)
	}
	normalized := strings.ReplaceAll(value, "\\", "/")
	if strings.HasPrefix(normalized, "/") || strings.ContainsRune(normalized, 0) || strings.Contains(value, "\\") {
		return fmt.Errorf("%w: path", ErrUnsafeCase)
	}
	if len(normalized) >= 2 && normalized[1] == ':' && unicode.IsLetter(rune(normalized[0])) {
		return fmt.Errorf("%w: path", ErrUnsafeCase)
	}
	for _, segment := range strings.Split(normalized, "/") {
		if segment == ".." {
			return fmt.Errorf("%w: path", ErrUnsafeCase)
		}
	}
	if path.Clean(normalized) == "." {
		return fmt.Errorf("%w: path", ErrUnsafeCase)
	}
	if sensitivePath(normalized) {
		return ErrUnsafeCase
	}
	return nil
}

func validateIdentifier(name, value string, maxBytes int) error {
	if value == "" || len(value) > maxBytes || strings.TrimSpace(value) != value || !utf8.ValidString(value) {
		return fmt.Errorf("%w: %s", ErrInvalidCases, name)
	}
	for _, r := range value {
		if unicode.IsSpace(r) || unicode.IsControl(r) || strings.ContainsRune("/\\", r) {
			return fmt.Errorf("%w: %s", ErrInvalidCases, name)
		}
	}
	return nil
}

func validateSafeText(name, value string, maxBytes int) error {
	if value == "" || len(value) > maxBytes || !utf8.ValidString(value) {
		return fmt.Errorf("%w: %s", ErrInvalidCases, name)
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return ErrUnsafeCase
		}
	}
	if containsSensitiveLiteral(value) {
		return ErrUnsafeCase
	}
	if containsRawSourceMarker(value) {
		return ErrUnsafeCase
	}
	return nil
}

func containsSensitiveLiteral(value string) bool {
	lower := strings.ToLower(value)
	for _, marker := range []string{
		"-----begin ",
		"password=", "passwd=", "secret=", "token=", "api_key=", "api-key=",
		"client_secret=", "credential=", "authorization:", "bearer ",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func containsRawSourceMarker(value string) bool {
	if !strings.ContainsAny(value, "{};") {
		return false
	}
	lower := strings.ToLower(value)
	for _, marker := range []string{"class ", "package ", "import ", "func ", "public class ", "<?xml"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func sensitivePath(value string) bool {
	lower := strings.ToLower(value)
	for _, marker := range []string{
		".env", ".pem", ".key", ".p12", ".pfx", "secret", "password", "credential", "token",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func validAttributionStage(stage AttributionStage) bool {
	switch stage {
	case AttributionExtraction, AttributionRetrieval, AttributionGeneration, AttributionPolicy:
		return true
	default:
		return false
	}
}

func hasClaimGap(claims []AcceptableClaim) bool {
	for _, claim := range claims {
		if len(claim.GapIDs) > 0 {
			return true
		}
	}
	return false
}

func caseIdentity(item EvaluationCase) string {
	return item.CaseID + "\x00" + fmt.Sprint(item.CaseVersion)
}

func cloneCaseSet(input CaseSet) CaseSet {
	output := input
	output.Cases = make([]EvaluationCase, len(input.Cases))
	for index, item := range input.Cases {
		output.Cases[index] = cloneCase(item)
	}
	return output
}

func cloneCase(input EvaluationCase) EvaluationCase {
	output := input
	output.Scope.Dimensions = append([]string(nil), input.Scope.Dimensions...)
	output.Scope.Artifacts = append([]string(nil), input.Scope.Artifacts...)
	output.Inclusions = append([]ScopeItem(nil), input.Inclusions...)
	output.Exclusions = append([]ScopeItem(nil), input.Exclusions...)
	output.AcceptableClaims = append([]AcceptableClaim(nil), input.AcceptableClaims...)
	for index, claim := range output.AcceptableClaims {
		output.AcceptableClaims[index].EvidenceIDs = append([]string(nil), claim.EvidenceIDs...)
		output.AcceptableClaims[index].GapIDs = append([]string(nil), claim.GapIDs...)
	}
	output.ExpectedEvidence = append([]ExpectedEvidence(nil), input.ExpectedEvidence...)
	for index, item := range output.ExpectedEvidence {
		if item.Locator != nil {
			locator := *item.Locator
			output.ExpectedEvidence[index].Locator = &locator
		}
		if item.Pattern != nil {
			pattern := *item.Pattern
			output.ExpectedEvidence[index].Pattern = &pattern
		}
	}
	output.ExpectedGaps = append([]ExpectedGap(nil), input.ExpectedGaps...)
	output.Authors = append([]string(nil), input.Authors...)
	output.Reviewers = append([]string(nil), input.Reviewers...)
	output.FailureAttribution = append([]FailureAttribution(nil), input.FailureAttribution...)
	return output
}

func normalizeCaseCollections(item *EvaluationCase) {
	if item.Inclusions == nil {
		item.Inclusions = []ScopeItem{}
	}
	if item.Exclusions == nil {
		item.Exclusions = []ScopeItem{}
	}
	if item.Scope.Dimensions == nil {
		item.Scope.Dimensions = []string{}
	}
	if item.Scope.Artifacts == nil {
		item.Scope.Artifacts = []string{}
	}
	if item.ExpectedEvidence == nil {
		item.ExpectedEvidence = []ExpectedEvidence{}
	}
	if item.ExpectedGaps == nil {
		item.ExpectedGaps = []ExpectedGap{}
	}
	if item.FailureAttribution == nil {
		item.FailureAttribution = []FailureAttribution{}
	}
	for index := range item.AcceptableClaims {
		if item.AcceptableClaims[index].EvidenceIDs == nil {
			item.AcceptableClaims[index].EvidenceIDs = []string{}
		}
		if item.AcceptableClaims[index].GapIDs == nil {
			item.AcceptableClaims[index].GapIDs = []string{}
		}
	}
	sort.Strings(item.Scope.Dimensions)
	sort.Strings(item.Scope.Artifacts)
	sort.Strings(item.Authors)
	sort.Strings(item.Reviewers)
	sort.SliceStable(item.Inclusions, func(i, j int) bool { return item.Inclusions[i].Ref < item.Inclusions[j].Ref })
	sort.SliceStable(item.Exclusions, func(i, j int) bool { return item.Exclusions[i].Ref < item.Exclusions[j].Ref })
	sort.SliceStable(item.AcceptableClaims, func(i, j int) bool { return item.AcceptableClaims[i].ClaimID < item.AcceptableClaims[j].ClaimID })
	sort.SliceStable(item.ExpectedEvidence, func(i, j int) bool {
		return evidenceSortKey(item.ExpectedEvidence[i]) < evidenceSortKey(item.ExpectedEvidence[j])
	})
	sort.SliceStable(item.ExpectedGaps, func(i, j int) bool { return item.ExpectedGaps[i].GapID < item.ExpectedGaps[j].GapID })
	sort.SliceStable(item.FailureAttribution, func(i, j int) bool {
		if item.FailureAttribution[i].Stage != item.FailureAttribution[j].Stage {
			return item.FailureAttribution[i].Stage < item.FailureAttribution[j].Stage
		}
		return item.FailureAttribution[i].Code < item.FailureAttribution[j].Code
	})
	for index := range item.AcceptableClaims {
		sort.Strings(item.AcceptableClaims[index].EvidenceIDs)
		sort.Strings(item.AcceptableClaims[index].GapIDs)
	}
}

func evidenceSortKey(item ExpectedEvidence) string {
	var builder strings.Builder
	builder.WriteString(item.EvidenceID)
	builder.WriteByte(0)
	builder.WriteString(item.Kind)
	if item.Locator != nil {
		encoded, _ := json.Marshal(item.Locator)
		builder.Write(encoded)
	}
	if item.Pattern != nil {
		encoded, _ := json.Marshal(item.Pattern)
		builder.Write(encoded)
	}
	return builder.String()
}
