package query

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"unicode/utf8"

	"github.com/pedrogpaulino/manu/internal/aigateway"
	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/evidence"
)

const (
	contextGatewayProjectionVersion = "v1alpha1"
	contextGatewayMaxGapIDBytes     = 128
	contextGatewayMaxLocatorBytes   = 128
	contextGatewayLocatorVersion    = "v1"
)

var (
	// ErrInvalidContextGatewayProjection is the payload-free sentinel for an
	// invalid or unsafe projection boundary.
	ErrInvalidContextGatewayProjection = errors.New("query: invalid context gateway projection")
)

// ContextGatewayProjection is the sanitized bridge from a ContextPackage to
// the existing validation and generator-facing evidence package views.
type ContextGatewayProjection struct {
	ContextPackageID     string                    `json:"context_package_id"`
	ContextPackageDigest string                    `json:"context_package_digest"`
	ValidationPackage    EvidencePackage           `json:"validation_package"`
	GatewayPackage       aigateway.EvidencePackage `json:"gateway_package"`
	GapIDs               []string                  `json:"gap_ids,omitempty"`
	ItemCount            int                       `json:"item_count"`
	RelationCount        int                       `json:"relation_count"`
	EvidenceCount        int                       `json:"evidence_count"`
	GapCount             int                       `json:"gap_count"`
	CharacterCount       int64                     `json:"character_count"`
	ByteCount            int64                     `json:"byte_count"`
}

// Validate checks both existing views, their exact identity relationship and
// the payload-free counters recorded by the projection.
func (p ContextGatewayProjection) Validate() error {
	if !validContextID(p.ContextPackageID) || !isSHA256(p.ContextPackageDigest) {
		return ErrInvalidContextGatewayProjection
	}
	if p.ValidationPackage.ID != p.GatewayPackage.ID ||
		p.ValidationPackage.Digest != p.GatewayPackage.Digest ||
		p.ValidationPackage.ID != "package-"+p.ValidationPackage.Digest {
		return ErrInvalidContextGatewayProjection
	}
	if err := p.ValidationPackage.Validate(); err != nil {
		return ErrInvalidContextGatewayProjection
	}
	if err := p.GatewayPackage.Validate(); err != nil {
		return ErrInvalidContextGatewayProjection
	}
	if err := p.ValidateAgainstGateway(); err != nil {
		return ErrInvalidContextGatewayProjection
	}
	if len(p.GapIDs) > maxContextItems {
		return ErrInvalidContextGatewayProjection
	}
	seenGaps := make(map[string]struct{}, len(p.GapIDs))
	for _, id := range p.GapIDs {
		if !validPackageToken(id, contextGatewayMaxGapIDBytes) {
			return ErrInvalidContextGatewayProjection
		}
		if _, exists := seenGaps[id]; exists {
			return ErrInvalidContextGatewayProjection
		}
		seenGaps[id] = struct{}{}
	}
	if !sameStringSequence(p.GapIDs, p.GatewayPackage.Gaps) {
		return ErrInvalidContextGatewayProjection
	}
	digest, err := contextGatewayDigest(
		p.ContextPackageID,
		p.ContextPackageDigest,
		p.ValidationPackage.Scope(),
		p.GatewayPackage.Evidence,
		p.GapIDs,
	)
	if err != nil || digest != p.GatewayPackage.Digest {
		return ErrInvalidContextGatewayProjection
	}
	if p.ItemCount < 0 || p.RelationCount < 0 || p.EvidenceCount < 0 || p.GapCount < 0 ||
		p.CharacterCount < 0 || p.ByteCount < 0 ||
		p.ItemCount+p.RelationCount != p.EvidenceCount ||
		p.EvidenceCount != len(p.GatewayPackage.Evidence) || p.GapCount != len(p.GapIDs) {
		return ErrInvalidContextGatewayProjection
	}
	var characters int64
	var bytes int64
	for _, item := range p.GatewayPackage.Evidence {
		if !utf8.ValidString(item.Content) {
			return ErrInvalidContextGatewayProjection
		}
		characters += int64(utf8.RuneCountInString(item.Content))
		bytes += int64(len([]byte(item.Content)))
	}
	if p.CharacterCount != characters || p.ByteCount != bytes {
		return ErrInvalidContextGatewayProjection
	}
	return nil
}

// ValidateAgainstGateway verifies the exact relationship between the two
// existing package views without examining source or persistence.
func (p ContextGatewayProjection) ValidateAgainstGateway() error {
	if err := p.ValidationPackage.ValidateAgainstGateway(p.GatewayPackage); err != nil {
		return ErrInvalidContextGatewayProjection
	}
	if len(p.ValidationPackage.Evidence) != len(p.GatewayPackage.Evidence) {
		return ErrInvalidContextGatewayProjection
	}
	for index, reference := range p.ValidationPackage.Evidence {
		gateway := p.GatewayPackage.Evidence[index]
		if reference.ID != gateway.ID || reference.ContentDigest != gateway.ContentDigest {
			return ErrInvalidContextGatewayProjection
		}
		locator, err := contextGatewayLocator(reference.Locator)
		if err != nil || locator != gateway.Locator {
			return ErrInvalidContextGatewayProjection
		}
	}
	return nil
}

// ValidateAgainst recomputes the deterministic projection for the supplied
// context package and rejects any altered projection.
func (p ContextGatewayProjection) ValidateAgainst(input ContextPackage) error {
	if err := p.Validate(); err != nil {
		return err
	}
	expected, err := projectContextPackage(context.Background(), input)
	if err != nil || !reflect.DeepEqual(p, expected) {
		return ErrInvalidContextGatewayProjection
	}
	return nil
}

// ProjectContextPackage creates both sanitized legacy views from an in-memory
// ContextPackage. It never opens a source, filesystem, database or generator.
func ProjectContextPackage(ctx context.Context, input ContextPackage) (ContextGatewayProjection, error) {
	if ctx == nil {
		return ContextGatewayProjection{}, ErrInvalidContextGatewayProjection
	}
	if err := contextGatewayContextErr(ctx); err != nil {
		return ContextGatewayProjection{}, err
	}
	return projectContextPackage(ctx, input)
}

func projectContextPackage(ctx context.Context, input ContextPackage) (ContextGatewayProjection, error) {
	if err := contextGatewayContextErr(ctx); err != nil {
		return ContextGatewayProjection{}, err
	}
	if err := input.Validate(); err != nil {
		return ContextGatewayProjection{}, ErrInvalidContextGatewayProjection
	}

	items := make([]contextGatewayProjectedEvidence, 0, len(input.Items)+len(input.Relations))
	locatorsByID := make(map[string]contract.Locator, len(input.Items)+len(input.Relations))
	for _, item := range input.Items {
		if err := contextGatewayContextErr(ctx); err != nil {
			return ContextGatewayProjection{}, err
		}
		projected, locator, err := projectContextItem(item)
		if err != nil {
			return ContextGatewayProjection{}, ErrInvalidContextGatewayProjection
		}
		items = append(items, projected)
		locatorsByID[item.ID] = locator
	}

	for _, relation := range input.Relations {
		if err := contextGatewayContextErr(ctx); err != nil {
			return ContextGatewayProjection{}, err
		}
		locator, err := contextGatewayRelationLocator(relation, locatorsByID)
		if err != nil {
			return ContextGatewayProjection{}, ErrInvalidContextGatewayProjection
		}
		projected, _, err := projectContextRelation(relation, locator)
		if err != nil {
			return ContextGatewayProjection{}, ErrInvalidContextGatewayProjection
		}
		items = append(items, projected)
	}

	gaps := make([]string, 0, len(input.Gaps))
	seenGaps := make(map[string]struct{}, len(input.Gaps))
	for _, gap := range input.Gaps {
		if err := contextGatewayContextErr(ctx); err != nil {
			return ContextGatewayProjection{}, err
		}
		// Gaps without IDs fail closed: their messages are never transported and
		// no identity is inferred for the gateway view.
		if !validPackageToken(gap.ID, contextGatewayMaxGapIDBytes) {
			return ContextGatewayProjection{}, ErrInvalidContextGatewayProjection
		}
		if _, exists := seenGaps[gap.ID]; exists {
			return ContextGatewayProjection{}, ErrInvalidContextGatewayProjection
		}
		seenGaps[gap.ID] = struct{}{}
		gaps = append(gaps, gap.ID)
	}

	validationEvidence := make([]EvidenceReference, 0, len(items))
	gatewayEvidence := make([]aigateway.AuthorizedEvidence, 0, len(items))
	var characters, bytes int64
	for _, item := range items {
		if err := contextGatewayContextErr(ctx); err != nil {
			return ContextGatewayProjection{}, err
		}
		validationEvidence = append(validationEvidence, item.Validation)
		gatewayEvidence = append(gatewayEvidence, item.Gateway)
		characters += int64(utf8.RuneCountInString(item.Gateway.Content))
		bytes += int64(len([]byte(item.Gateway.Content)))
	}
	digest, err := contextGatewayDigest(input.ID, input.Digest, input.Scope, gatewayEvidence, gaps)
	if err != nil {
		return ContextGatewayProjection{}, ErrInvalidContextGatewayProjection
	}
	packageID := "package-" + digest
	validation := EvidencePackage{
		ID:             packageID,
		Digest:         digest,
		OrganizationID: input.Scope.OrganizationID,
		SourceID:       input.Scope.SourceID,
		SnapshotID:     input.Scope.SnapshotID,
		Evidence:       validationEvidence,
	}
	gateway := aigateway.EvidencePackage{ID: packageID, Digest: digest, Evidence: gatewayEvidence, Gaps: gaps}
	result := ContextGatewayProjection{
		ContextPackageID:     input.ID,
		ContextPackageDigest: input.Digest,
		ValidationPackage:    validation,
		GatewayPackage:       gateway,
		GapIDs:               append([]string(nil), gaps...),
		ItemCount:            len(input.Items),
		RelationCount:        len(input.Relations),
		EvidenceCount:        len(items),
		GapCount:             len(gaps),
		CharacterCount:       characters,
		ByteCount:            bytes,
	}
	if err := result.Validate(); err != nil {
		return ContextGatewayProjection{}, ErrInvalidContextGatewayProjection
	}
	return result, nil
}

type contextGatewayProjectedEvidence struct {
	Validation EvidenceReference
	Gateway    aigateway.AuthorizedEvidence
}

func projectContextItem(item ContextItem) (contextGatewayProjectedEvidence, contract.Locator, error) {
	locator := item.locatorForValidation()
	if !validContextLocator(locator, item.Scope) {
		return contextGatewayProjectedEvidence{}, contract.Locator{}, ErrInvalidContextGatewayProjection
	}
	content := ""
	if item.Kind == ContextItemEvidence {
		if item.Evidence == nil || item.Evidence.ValidatePrepared() != nil {
			return contextGatewayProjectedEvidence{}, contract.Locator{}, ErrInvalidContextGatewayProjection
		}
		inspection := evidence.InspectContent(item.Evidence.Content)
		switch item.Evidence.ContentState {
		case evidence.ContentStatePresent:
			if item.Evidence.ExternalTransfer != evidence.DecisionAllow || inspection.Classification != evidence.ClassificationSafeText {
				return contextGatewayProjectedEvidence{}, contract.Locator{}, ErrInvalidContextGatewayProjection
			}
			content = item.Evidence.Content
		case evidence.ContentStateRedacted:
			if item.Evidence.ExternalTransfer == evidence.DecisionDeny || item.Evidence.Content != evidence.RedactedContent || inspection.Classification != evidence.ClassificationSafeText {
				return contextGatewayProjectedEvidence{}, contract.Locator{}, ErrInvalidContextGatewayProjection
			}
			content = evidence.RedactedContent
		default:
			return contextGatewayProjectedEvidence{}, contract.Locator{}, ErrInvalidContextGatewayProjection
		}
	} else {
		encoded, err := CanonicalContextItemJSON(item)
		if err != nil {
			return contextGatewayProjectedEvidence{}, contract.Locator{}, ErrInvalidContextGatewayProjection
		}
		inspection := evidence.InspectContent(string(encoded))
		if inspection.Classification != evidence.ClassificationSafeText {
			return contextGatewayProjectedEvidence{}, contract.Locator{}, ErrInvalidContextGatewayProjection
		}
		content = string(encoded)
	}
	return contextGatewayEvidence(item.ID, item.Scope, locator, content)
}

func projectContextRelation(relation ContextRelation, locator contract.Locator) (contextGatewayProjectedEvidence, contract.Locator, error) {
	encoded, err := CanonicalContextRelationJSON(relation)
	if err != nil {
		return contextGatewayProjectedEvidence{}, contract.Locator{}, ErrInvalidContextGatewayProjection
	}
	inspection := evidence.InspectContent(string(encoded))
	if inspection.Classification != evidence.ClassificationSafeText || !validContextLocator(locator, relation.Scope) {
		return contextGatewayProjectedEvidence{}, contract.Locator{}, ErrInvalidContextGatewayProjection
	}
	return contextGatewayEvidence(relation.ID, relation.Scope, locator, string(encoded))
}

func contextGatewayEvidence(id string, scope Scope, locator contract.Locator, content string) (contextGatewayProjectedEvidence, contract.Locator, error) {
	gatewayLocator, err := contextGatewayLocator(locator)
	if err != nil {
		return contextGatewayProjectedEvidence{}, contract.Locator{}, ErrInvalidContextGatewayProjection
	}
	contentDigest := evidence.ContentDigest(content)
	return contextGatewayProjectedEvidence{
		Validation: EvidenceReference{
			ID:             id,
			OrganizationID: scope.OrganizationID,
			SourceID:       scope.SourceID,
			SnapshotID:     scope.SnapshotID,
			Locator:        locator,
			ContentDigest:  contentDigest,
		},
		Gateway: aigateway.AuthorizedEvidence{
			ID:            id,
			Content:       content,
			ContentDigest: contentDigest,
			Locator:       gatewayLocator,
		},
	}, locator, nil
}

// contextGatewayLocator returns the bounded, opaque locator accepted by the
// generator boundary. The validation view retains locator in its structured
// form; this representation carries canonical artifact identity plus useful
// position data and never contains source content.
func contextGatewayLocator(locator contract.Locator) (string, error) {
	if locator.Validate() != nil {
		return "", ErrInvalidContextGatewayProjection
	}

	payload := contextGatewayLocatorPayload{Version: contextGatewayLocatorVersion}
	switch {
	case locator.ArtifactID != "":
		payload.ArtifactID = locator.ArtifactID
	case locator.SourceID != "":
		payload.SourceID = locator.SourceID
	}

	hasLinePosition := locator.StartLine > 0 || locator.StartColumn > 0 || locator.EndLine > 0 || locator.EndColumn > 0
	hasBytePosition := locator.ByteOffset > 0 || locator.ByteLength > 0
	if hasLinePosition {
		payload.StartLine = locator.StartLine
		payload.StartColumn = locator.StartColumn
		payload.EndLine = locator.EndLine
		payload.EndColumn = locator.EndColumn
	}
	if hasBytePosition {
		payload.ByteOffset = locator.ByteOffset
		payload.ByteLength = locator.ByteLength
	}

	// Member remains useful alongside a byte or line position. Path/URI are
	// fallback identity when no canonical artifact is available. When both are
	// present, retain both so omitted locator fields cannot collide silently.
	payload.Member = locator.Member
	if locator.ArtifactID == "" {
		payload.Path = locator.Path
		payload.URI = locator.URI
	} else if !hasLinePosition && !hasBytePosition && payload.Member == "" {
		payload.Path = locator.Path
		payload.URI = locator.URI
	}

	encoded, err := marshalContextGatewayLocator(payload)
	if err != nil {
		return "", err
	}
	if len(encoded) <= contextGatewayMaxLocatorBytes {
		return string(encoded), nil
	}

	// Replace long human-readable components with a short prefix and a stable
	// digest. Digest prevents ambiguous truncation while prefix keeps direction
	// useful when the locator is inspected by a human or a tool.
	if payload.Member != "" {
		payload.Member = compactContextGatewayLocatorText(payload.Member)
	}
	if payload.Path != "" {
		payload.Path = compactContextGatewayLocatorText(payload.Path)
	}
	if payload.URI != "" {
		payload.URI = compactContextGatewayLocatorText(payload.URI)
	}
	encoded, err = marshalContextGatewayLocator(payload)
	if err != nil {
		return "", err
	}
	if len(encoded) <= contextGatewayMaxLocatorBytes {
		return string(encoded), nil
	}

	// Non-canonical identifiers are still bounded safely if a caller supplies
	// one outside the usual UUID shape. Canonical UUID artifact IDs normally
	// take the first path through this function and remain verbatim.
	if payload.ArtifactID != "" {
		payload.ArtifactID = compactContextGatewayLocatorText(payload.ArtifactID)
	}
	if payload.SourceID != "" {
		payload.SourceID = compactContextGatewayLocatorText(payload.SourceID)
	}
	encoded, err = marshalContextGatewayLocator(payload)
	if err == nil && len(encoded) <= contextGatewayMaxLocatorBytes {
		return string(encoded), nil
	}

	return contextGatewayLocatorDigestFallback(locator)
}

type contextGatewayLocatorPayload struct {
	Version     string `json:"v"`
	ArtifactID  string `json:"a,omitempty"`
	SourceID    string `json:"s,omitempty"`
	Path        string `json:"p,omitempty"`
	URI         string `json:"u,omitempty"`
	Member      string `json:"m,omitempty"`
	StartLine   int    `json:"l,omitempty"`
	StartColumn int    `json:"c,omitempty"`
	EndLine     int    `json:"el,omitempty"`
	EndColumn   int    `json:"ec,omitempty"`
	ByteOffset  int64  `json:"o,omitempty"`
	ByteLength  int64  `json:"n,omitempty"`
	Digest      string `json:"d,omitempty"`
}

func marshalContextGatewayLocator(payload contextGatewayLocatorPayload) ([]byte, error) {
	encoded, err := json.Marshal(payload)
	if err != nil || len(encoded) == 0 {
		return nil, ErrInvalidContextGatewayProjection
	}
	return encoded, nil
}

func compactContextGatewayLocatorText(value string) string {
	digest := sha256.Sum256([]byte(value))
	prefix := value
	if len([]byte(prefix)) > 16 {
		prefix = contextGatewayUTF8Prefix(prefix, 16)
	}
	return prefix + "~" + hex.EncodeToString(digest[:])[:24]
}

func contextGatewayLocatorDigestFallback(locator contract.Locator) (string, error) {
	canonical, err := json.Marshal(locator)
	if err != nil || len(canonical) == 0 {
		return "", ErrInvalidContextGatewayProjection
	}
	digest := sha256.Sum256(canonical)
	payload := contextGatewayLocatorPayload{
		Version: contextGatewayLocatorVersion,
		Digest:  hex.EncodeToString(digest[:]),
	}
	switch {
	case locator.ArtifactID != "":
		payload.ArtifactID = locator.ArtifactID
	case locator.SourceID != "":
		payload.SourceID = locator.SourceID
	}

	encoded, err := marshalContextGatewayLocator(payload)
	if err == nil && len(encoded) <= contextGatewayMaxLocatorBytes {
		return string(encoded), nil
	}
	if payload.ArtifactID != "" {
		payload.ArtifactID = compactContextGatewayLocatorText(payload.ArtifactID)
	}
	if payload.SourceID != "" {
		payload.SourceID = compactContextGatewayLocatorText(payload.SourceID)
	}
	encoded, err = marshalContextGatewayLocator(payload)
	if err == nil && len(encoded) <= contextGatewayMaxLocatorBytes {
		return string(encoded), nil
	}

	// Full digest remains an unambiguous identity even when an unsafe-size
	// source/artifact token cannot share the 128-byte gateway budget.
	payload.ArtifactID = ""
	payload.SourceID = ""
	encoded, err = marshalContextGatewayLocator(payload)
	if err != nil || len(encoded) > contextGatewayMaxLocatorBytes {
		return "", ErrInvalidContextGatewayProjection
	}
	return string(encoded), nil
}

func contextGatewayUTF8Prefix(value string, maxBytes int) string {
	if len([]byte(value)) <= maxBytes {
		return value
	}
	prefix := value[:maxBytes]
	for len(prefix) > 0 && !utf8.ValidString(prefix) {
		prefix = prefix[:len(prefix)-1]
	}
	return prefix
}

func contextGatewayRelationLocator(relation ContextRelation, locators map[string]contract.Locator) (contract.Locator, error) {
	provenanceIDs := make([]string, 0, len(relation.Provenance.Evidence))
	for _, reference := range relation.Provenance.Evidence {
		provenanceIDs = append(provenanceIDs, reference.ID)
	}
	sort.Strings(provenanceIDs)
	for _, id := range provenanceIDs {
		if locator, exists := locators[id]; exists {
			return locator, nil
		}
	}
	ids := append([]string(nil), relation.SupportIDs...)
	ids = append(ids, relation.FromID, relation.ToID)
	sort.Strings(ids)
	for _, id := range ids {
		if locator, exists := locators[id]; exists {
			return locator, nil
		}
	}
	return contract.Locator{}, ErrInvalidContextGatewayProjection
}

func contextGatewayDigest(
	contextPackageID string,
	contextPackageDigest string,
	scope Scope,
	items []aigateway.AuthorizedEvidence,
	gaps []string,
) (string, error) {
	digestItems := make([]contextGatewayDigestEvidence, len(items))
	for index, item := range items {
		digestItems[index] = contextGatewayDigestEvidence{
			ID:            item.ID,
			ContentDigest: item.ContentDigest,
			Locator:       item.Locator,
		}
	}
	encoded, err := json.Marshal(struct {
		Version              string                         `json:"version"`
		ContextPackageID     string                         `json:"context_package_id"`
		ContextPackageDigest string                         `json:"context_package_digest"`
		Scope                Scope                          `json:"scope"`
		Evidence             []contextGatewayDigestEvidence `json:"evidence"`
		Gaps                 []string                       `json:"gaps"`
	}{
		Version:              contextGatewayProjectionVersion,
		ContextPackageID:     contextPackageID,
		ContextPackageDigest: contextPackageDigest,
		Scope:                scope,
		Evidence:             digestItems,
		Gaps:                 gaps,
	})
	if err != nil {
		return "", ErrInvalidContextGatewayProjection
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

type contextGatewayDigestEvidence struct {
	ID            string `json:"id"`
	ContentDigest string `json:"content_digest"`
	Locator       string `json:"locator"`
}

func contextGatewayContextErr(ctx context.Context) error {
	if ctx == nil {
		return ErrInvalidContextGatewayProjection
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

func sameStringSequence(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
