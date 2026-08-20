package evidence

import (
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	// RedactedContent is the only text representation emitted for content
	// that is allowed to survive as a redaction.
	RedactedContent = "[redacted]"

	redactionReasonSensitive    = "sensitive-content"
	redactionReasonPrompt       = "prompt-injection"
	redactionReasonBinary       = "binary-content"
	redactionReasonInvalid      = "invalid-text"
	redactionReasonProhibited   = "prohibited-content"
	redactionReasonPolicyRedact = "policy-redact"
	redactionReasonPolicyDeny   = "policy-deny"
)

var (
	// Match both ordinary assignments and environment-style names such as
	// OPENAI_API_KEY and AWS_SECRET_ACCESS_KEY. The identifier prefix is
	// deliberately limited to alphanumeric components, so punctuation and
	// surrounding prose do not turn arbitrary text into a secret finding.
	secretAssignmentPattern = regexp.MustCompile(`(?i)(?:^|[^[:alnum:]_])(?:[[:alnum:]]+[_-])*(?:password|passwd|secret|token|api[-_]?key|client[-_]?secret)(?:[_-][[:alnum:]]+)*\s*[:=]\s*(?:"[^"\r\n]*"|'[^'\r\n]*'|[^\s,;]+)`)
	authorizationPattern    = regexp.MustCompile(`(?i)(?:\bauthorization\b\s*[:=]\s*\S+|\bbearer\s+[A-Za-z0-9._~+/=-]+)`)
	pemPrivateKeyPattern    = regexp.MustCompile(`(?is)-----BEGIN [^-\r\n]*(?:PRIVATE KEY|RSA PRIVATE KEY)[^-\r\n]*-----`)
	promptInjectionPattern  = regexp.MustCompile(`(?i)(?:ignore\s+(?:all\s+)?(?:the\s+)?(?:previous|prior|above)\s+instructions?|disregard\s+(?:all\s+)?(?:the\s+)?(?:previous|prior|above)\s+instructions?|reveal\s+(?:the\s+)?system\s+prompt|system\s+message|you\s+are\s+(?:now\s+)?chatgpt|jailbreak)`)
)

// PolicyLayer contains independent local persistence and external-transfer
// outcomes. The zero value is neutral (allow) when it participates in a
// combination; callers that serialize a resolved layer must use explicit
// decisions.
type PolicyLayer struct {
	Persist          Decision `json:"persist"`
	ExternalTransfer Decision `json:"external_transfer"`
}

// DecisionPolicy and PolicyDecisions are descriptive aliases for callers that
// want to emphasize that both decisions are resolved together.
type DecisionPolicy = PolicyLayer
type PolicyDecisions = PolicyLayer

// Validate checks configured decisions. Unknown values are accepted as an
// unset layer and become neutral during resolution.
func (l PolicyLayer) Validate() error {
	for _, decision := range []Decision{l.Persist, l.ExternalTransfer} {
		if decision != DecisionUnknown {
			if err := decision.Validate(); err != nil {
				return err
			}
		}
	}
	return nil
}

// Policy combines installation, source, and classification decisions. A
// resolved outcome always chooses the most restrictive decision per dimension.
type Policy struct {
	Installation    PolicyLayer                    `json:"installation"`
	Source          PolicyLayer                    `json:"source"`
	Classifications map[Classification]PolicyLayer `json:"classifications,omitempty"`
}

// PolicyConfig is the configuration spelling retained for callers that keep
// policy types next to other evidence configuration.
type PolicyConfig = Policy

// IsZero reports whether no policy layer was supplied.
func (p Policy) IsZero() bool {
	return p.Installation == (PolicyLayer{}) && p.Source == (PolicyLayer{}) && len(p.Classifications) == 0
}

// Validate checks every configured layer and classification key.
func (p Policy) Validate() error {
	if err := p.Installation.Validate(); err != nil {
		return err
	}
	if err := p.Source.Validate(); err != nil {
		return err
	}
	for classification, layer := range p.Classifications {
		if err := classification.Validate(); err != nil {
			return err
		}
		if classification == ClassificationUnknown {
			return errInvalidPolicyClassification()
		}
		if err := layer.Validate(); err != nil {
			return err
		}
	}
	return nil
}

func errInvalidPolicyClassification() error {
	return ErrInvalidClassification
}

// DefaultPolicy permits safe local persistence, denies external transfer by
// default, and applies a conservative floor to sensitive classifications.
func DefaultPolicy() Policy {
	return Policy{
		Installation: PolicyLayer{
			Persist:          DecisionAllow,
			ExternalTransfer: DecisionDeny,
		},
		Classifications: map[Classification]PolicyLayer{
			ClassificationSensitive: {
				Persist:          DecisionRedact,
				ExternalTransfer: DecisionDeny,
			},
			ClassificationPromptInjection: {
				Persist:          DecisionRedact,
				ExternalTransfer: DecisionDeny,
			},
			ClassificationBinary: {
				Persist:          DecisionDeny,
				ExternalTransfer: DecisionDeny,
			},
			ClassificationInvalid: {
				Persist:          DecisionDeny,
				ExternalTransfer: DecisionDeny,
			},
			ClassificationProhibited: {
				Persist:          DecisionDeny,
				ExternalTransfer: DecisionDeny,
			},
		},
	}
}

// Resolve combines all applicable layers. Deny is more restrictive than
// redact, which is more restrictive than allow. Classification safety checks
// are enforced again during preparation so a custom layer cannot accidentally
// re-enable transfer of untrusted material.
func (p Policy) Resolve(classification Classification) (PolicyLayer, error) {
	if p.IsZero() {
		p = DefaultPolicy()
	}
	if err := p.Validate(); err != nil {
		return PolicyLayer{}, err
	}
	if err := classification.Validate(); err != nil {
		return PolicyLayer{}, err
	}
	resolved := PolicyLayer{Persist: DecisionAllow, ExternalTransfer: DecisionAllow}
	conservativeLayer := DefaultPolicy().Classifications[classification]
	for _, layer := range []PolicyLayer{p.Installation, p.Source, conservativeLayer, p.Classifications[classification]} {
		resolved.Persist = stricterDecision(resolved.Persist, layer.Persist)
		resolved.ExternalTransfer = stricterDecision(resolved.ExternalTransfer, layer.ExternalTransfer)
	}
	return resolved, nil
}

// Decisions is an alternate verb for Resolve.
func (p Policy) Decisions(classification Classification) (PolicyLayer, error) {
	return p.Resolve(classification)
}

// CombinePolicyLayers combines explicit layers without requiring callers to
// construct a Policy value.
func CombinePolicyLayers(layers ...PolicyLayer) (PolicyLayer, error) {
	resolved := PolicyLayer{Persist: DecisionAllow, ExternalTransfer: DecisionAllow}
	for _, layer := range layers {
		if err := layer.Validate(); err != nil {
			return PolicyLayer{}, err
		}
		resolved.Persist = stricterDecision(resolved.Persist, layer.Persist)
		resolved.ExternalTransfer = stricterDecision(resolved.ExternalTransfer, layer.ExternalTransfer)
	}
	return resolved, nil
}

func stricterDecision(left, right Decision) Decision {
	if right == DecisionUnknown {
		return left
	}
	if left == DecisionUnknown {
		return right
	}
	if decisionRank(right) > decisionRank(left) {
		return right
	}
	return left
}

func decisionRank(decision Decision) int {
	switch decision {
	case DecisionAllow:
		return 0
	case DecisionRedact:
		return 1
	case DecisionDeny:
		return 2
	default:
		return -1
	}
}

// ContentInspection is the result of classifying and sanitizing one bounded
// candidate. OriginalHash identifies the inspected bytes without retaining
// them in the result.
type ContentInspection struct {
	Classification  Classification
	Findings        []string
	Content         string
	State           ContentState
	OriginalHash    string
	RedactionReason string
	Redacted        bool
}

// Inspection is a concise alias for ContentInspection.
type Inspection = ContentInspection

// InspectContent classifies untrusted text and returns only a safe retained
// representation. It never returns the original secret or invalid/binary
// bytes in a redacted result.
func InspectContent(content string) ContentInspection {
	inspection := ContentInspection{OriginalHash: ContentDigest(content)}
	if content == "" {
		inspection.Classification = ClassificationSafeText
		inspection.State = ContentStateOmitted
		return inspection
	}
	if !utf8.ValidString(content) {
		inspection.Classification = ClassificationInvalid
		inspection.Findings = []string{FindingInvalidUTF8}
		inspection.State = ContentStateOmitted
		inspection.RedactionReason = redactionReasonInvalid
		return inspection
	}
	if containsBinaryBytes(content) {
		inspection.Classification = ClassificationBinary
		inspection.Findings = []string{FindingBinary}
		inspection.State = ContentStateOmitted
		inspection.RedactionReason = redactionReasonBinary
		return inspection
	}

	findings := make([]string, 0, 3)
	if secretAssignmentPattern.MatchString(content) {
		findings = append(findings, FindingSecretAssignment, FindingSecret)
	}
	if authorizationPattern.MatchString(content) {
		findings = append(findings, FindingAuthorization, FindingBearer)
	}
	if pemPrivateKeyPattern.MatchString(content) || containsPrivateKeyMarker(content) {
		findings = append(findings, FindingPEMPrivateKey, FindingSecret)
	}
	prompt := promptInjectionPattern.MatchString(content)
	if prompt {
		findings = append(findings, FindingPromptInjection)
	}
	findings = canonicalFindingStrings(findings)
	if len(findings) != 0 {
		if containsSensitiveFinding(findings) {
			inspection.Classification = ClassificationSensitive
			inspection.RedactionReason = redactionReasonSensitive
		} else {
			inspection.Classification = ClassificationPromptInjection
			inspection.RedactionReason = redactionReasonPrompt
		}
		inspection.Findings = findings
		inspection.Content = RedactedContent
		inspection.State = ContentStateRedacted
		inspection.Redacted = true
		return inspection
	}

	inspection.Classification = ClassificationSafeText
	inspection.Content = content
	inspection.State = ContentStatePresent
	return inspection
}

// Inspect is a concise alias for InspectContent.
func Inspect(content string) ContentInspection {
	return InspectContent(content)
}

// ClassifyContent returns only the safety class for callers that do not need
// the sanitized representation.
func ClassifyContent(content string) Classification {
	return InspectContent(content).Classification
}

// SanitizeContent is the central compatibility-independent sanitization API.
func SanitizeContent(content string) ContentInspection {
	return InspectContent(content)
}

// SanitizeEvidenceContent keeps the descriptive name used by the analysis
// package while routing all checks through this package.
func SanitizeEvidenceContent(content string) ContentInspection {
	return InspectContent(content)
}

func containsBinaryBytes(content string) bool {
	for i := 0; i < len(content); i++ {
		value := content[i]
		if value == 0 || value == 0x7f || (value < 0x09) || (value > 0x0d && value < 0x20) {
			return true
		}
	}
	return false
}

func containsPrivateKeyMarker(content string) bool {
	lower := strings.ToLower(content)
	return strings.Contains(lower, "private key") || strings.Contains(lower, "private_key")
}

func containsSensitiveFinding(findings []string) bool {
	for _, finding := range findings {
		switch finding {
		case FindingSecret, FindingSecretAssignment, FindingPEMPrivateKey, FindingAuthorization, FindingBearer:
			return true
		}
	}
	return false
}

func canonicalFindingStrings(findings []string) []string {
	if len(findings) == 0 {
		return nil
	}
	sorted := append([]string(nil), findings...)
	sort.Strings(sorted)
	result := sorted[:0]
	for _, finding := range sorted {
		if len(result) == 0 || result[len(result)-1] != finding {
			result = append(result, finding)
		}
	}
	return result
}

func classificationRank(classification Classification) int {
	switch classification {
	case ClassificationSafeText:
		return 0
	case ClassificationPromptInjection:
		return 1
	case ClassificationSensitive:
		return 2
	case ClassificationBinary:
		return 3
	case ClassificationInvalid:
		return 4
	case ClassificationProhibited:
		return 5
	default:
		return -1
	}
}

func strongerClassification(left, right Classification) Classification {
	if left == ClassificationUnknown {
		return right
	}
	if right == ClassificationUnknown || classificationRank(left) >= classificationRank(right) {
		return left
	}
	return right
}

func mergeFindings(left, right []string) []string {
	merged := make([]string, 0, len(left)+len(right))
	merged = append(merged, left...)
	merged = append(merged, right...)
	return canonicalFindingStrings(merged)
}

func hasSensitiveReason(reason string) bool {
	lower := strings.ToLower(reason)
	return strings.Contains(lower, "secret") || strings.Contains(lower, "sensitive") || strings.Contains(lower, "private") || strings.Contains(lower, "credential")
}

func hasPromptReason(reason string) bool {
	lower := strings.ToLower(reason)
	return strings.Contains(lower, "prompt") || strings.Contains(lower, "injection")
}

func normalizeUnit(unit EvidenceUnit) (EvidenceUnit, error) {
	raw := unit.Content
	inspection := InspectContent(raw)
	classification := unit.Classification
	reasonFindings := []string(nil)
	if classification == ClassificationUnknown {
		classification = inspection.Classification
	}
	if unit.ContentState == ContentStateRedacted {
		if hasSensitiveReason(unit.RedactionReason) {
			classification = strongerClassification(classification, ClassificationSensitive)
			reasonFindings = append(reasonFindings, FindingSecret)
		}
		if hasPromptReason(unit.RedactionReason) {
			classification = strongerClassification(classification, ClassificationPromptInjection)
			reasonFindings = append(reasonFindings, FindingPromptInjection)
		}
	}
	if classification == ClassificationProhibited {
		unit.Findings = mergeFindings(unit.Findings, []string{FindingProhibited})
	}
	if classification != ClassificationUnknown && classification != ClassificationSafeText && inspection.Classification != ClassificationSafeText {
		classification = strongerClassification(classification, inspection.Classification)
	}
	findings := mergeFindings(unit.Findings, inspection.Findings)
	findings = mergeFindings(findings, reasonFindings)
	findings = addClassificationFinding(classification, findings)
	if classification == ClassificationSafeText && len(findings) != 0 {
		classification = inspection.Classification
	}
	if classification.Validate() != nil {
		return EvidenceUnit{}, ErrInvalidClassification
	}

	originalHash := unit.ContentHash
	if originalHash == "" && raw != "" {
		originalHash = inspection.OriginalHash
	}
	if classification == ClassificationSafeText {
		if unit.ContentState == ContentStateUnknown {
			unit.ContentState = inspection.State
		}
		if unit.ContentState == ContentStatePresent {
			unit.Content = raw
			unit.ContentHash = ContentDigest(unit.Content)
			unit.RedactionReason = ""
		} else if unit.ContentState == ContentStateOmitted {
			unit.Content = ""
			unit.ContentHash = originalHash
		} else if unit.ContentState == ContentStateRedacted {
			unit.Content = RedactedContent
			unit.ContentHash = originalHash
		}
	} else if classification == ClassificationSensitive || classification == ClassificationPromptInjection {
		unit.ContentState = ContentStateRedacted
		unit.Content = RedactedContent
		unit.ContentHash = originalHash
		if classification == ClassificationPromptInjection {
			unit.RedactionReason = redactionReasonPrompt
		} else {
			unit.RedactionReason = redactionReasonSensitive
		}
	} else {
		unit.ContentState = ContentStateOmitted
		unit.Content = ""
		unit.ContentHash = originalHash
		if classification == ClassificationBinary {
			unit.RedactionReason = redactionReasonBinary
		} else if classification == ClassificationInvalid {
			unit.RedactionReason = redactionReasonInvalid
		} else {
			unit.RedactionReason = redactionReasonProhibited
		}
	}
	if unit.ContentHash == "" {
		return EvidenceUnit{}, ErrInvalidDigest
	}
	unit.Classification = classification
	unit.Findings = findings
	unit.ContentBytes = int64(len([]byte(unit.Content)))
	unit.ContentCharacters = int64(utf8.RuneCountInString(unit.Content))
	return unit, nil
}

func addClassificationFinding(classification Classification, findings []string) []string {
	switch classification {
	case ClassificationSensitive:
		if !containsSensitiveFinding(findings) {
			findings = append(findings, FindingSecret)
		}
	case ClassificationPromptInjection:
		if !containsFinding(findings, FindingPromptInjection) {
			findings = append(findings, FindingPromptInjection)
		}
	case ClassificationBinary:
		if !containsFinding(findings, FindingBinary) {
			findings = append(findings, FindingBinary)
		}
	case ClassificationInvalid:
		if !containsFinding(findings, FindingInvalidUTF8) {
			findings = append(findings, FindingInvalidUTF8)
		}
	case ClassificationProhibited:
		if !containsFinding(findings, FindingProhibited) {
			findings = append(findings, FindingProhibited)
		}
	}
	return canonicalFindingStrings(findings)
}

func containsFinding(findings []string, want string) bool {
	for _, finding := range findings {
		if finding == want {
			return true
		}
	}
	return false
}

func applyRepresentation(unit EvidenceUnit, decision Decision, forTransfer bool) EvidenceUnit {
	if decision == DecisionRedact && (unit.Classification == ClassificationBinary || unit.Classification == ClassificationInvalid || unit.Classification == ClassificationProhibited) {
		decision = DecisionDeny
	}
	switch decision {
	case DecisionDeny:
		unit.ContentState = ContentStateOmitted
		unit.Content = ""
		unit.ContentBytes = 0
		unit.ContentCharacters = 0
		if forTransfer {
			unit.RedactionReason = redactionReasonPolicyDeny
		} else {
			unit.RedactionReason = redactionReasonPolicyDeny
		}
	case DecisionRedact:
		unit.ContentState = ContentStateRedacted
		unit.Content = RedactedContent
		unit.ContentBytes = int64(len([]byte(unit.Content)))
		unit.ContentCharacters = int64(utf8.RuneCountInString(unit.Content))
		unit.RedactionReason = redactionReasonPolicyRedact
	}
	return unit
}

func prepareUnit(unit EvidenceUnit, policy Policy, forTransfer bool) (EvidenceUnit, error) {
	if err := policy.Validate(); err != nil {
		return EvidenceUnit{}, err
	}
	normalized, err := normalizeUnit(unit)
	if err != nil {
		return EvidenceUnit{}, err
	}
	decisions, err := policy.Resolve(normalized.Classification)
	if err != nil {
		return EvidenceUnit{}, err
	}
	if normalized.Classification != ClassificationSafeText && decisions.ExternalTransfer == DecisionAllow {
		// Classification is a safety floor: an installation/source layer cannot
		// re-enable transfer of material already identified as untrusted.
		decisions.ExternalTransfer = DecisionDeny
	}
	if normalized.Classification == ClassificationPromptInjection ||
		normalized.Classification == ClassificationBinary ||
		normalized.Classification == ClassificationInvalid ||
		normalized.Classification == ClassificationProhibited {
		// These classes are never useful as provider input, even as a fixed
		// redaction: prompt-shaped text must not cross the trust boundary and
		// binary/prohibited/invalid material has no transferable text view.
		decisions.ExternalTransfer = DecisionDeny
	}
	normalized.Persist = decisions.Persist
	normalized.ExternalTransfer = decisions.ExternalTransfer
	if forTransfer {
		normalized = applyRepresentation(normalized, decisions.ExternalTransfer, true)
	} else {
		normalized = applyRepresentation(normalized, decisions.Persist, false)
	}
	normalized.ID = EvidenceID(normalized)
	if err := normalized.Validate(); err != nil {
		return EvidenceUnit{}, err
	}
	return normalized, nil
}

// PrepareForPersistence sanitizes and applies only the local persistence
// representation. The returned unit still records the independently resolved
// external decision for later transfer preparation.
func PrepareForPersistence(unit EvidenceUnit, policy Policy) (EvidenceUnit, error) {
	return prepareUnit(unit, policy, false)
}

// PrepareForExternalTransfer creates a separate representation for an
// external provider. Local persistence content and decision are retained in
// the returned value, while the transfer decision controls its content.
func PrepareForExternalTransfer(unit EvidenceUnit, policy Policy) (EvidenceUnit, error) {
	return prepareUnit(unit, policy, true)
}

// PrepareForTransfer is the concise spelling of PrepareForExternalTransfer.
func PrepareForTransfer(unit EvidenceUnit, policy Policy) (EvidenceUnit, error) {
	return PrepareForExternalTransfer(unit, policy)
}

// PreparedEvidence contains independently prepared local and external views.
type PreparedEvidence struct {
	Persistence      EvidenceUnit `json:"persistence"`
	ExternalTransfer EvidenceUnit `json:"external_transfer"`
}

// PrepareEvidence produces both views from the same untrusted candidate.
func PrepareEvidence(unit EvidenceUnit, policy Policy) (PreparedEvidence, error) {
	persistence, err := PrepareForPersistence(unit, policy)
	if err != nil {
		return PreparedEvidence{}, err
	}
	transfer, err := PrepareForExternalTransfer(unit, policy)
	if err != nil {
		return PreparedEvidence{}, err
	}
	return PreparedEvidence{Persistence: persistence, ExternalTransfer: transfer}, nil
}
