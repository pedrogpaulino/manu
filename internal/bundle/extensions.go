package bundle

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/pedrogpaulino/manu/internal/fact"
)

// ExtensionRecord is the self-describing transport form for one v1alpha2
// extension payload. Schema is the canonical JSON representation whose bytes
// are covered by SchemaDigest; Payload is separate data and is never used as
// the schema digest. Every extension must use this envelope.
type ExtensionRecord struct {
	SchemaID      string          `json:"schema_id"`
	SchemaVersion string          `json:"schema_version"`
	SchemaDigest  string          `json:"schema_digest_sha256"`
	Schema        json.RawMessage `json:"schema"`
	Payload       json.RawMessage `json:"payload"`
}

type extensionSchemaIndex struct {
	byKey map[string]fact.ExtensionSchema
}

func newExtensionSchemaIndex(manifests []fact.FrontendManifest) (extensionSchemaIndex, error) {
	index := extensionSchemaIndex{
		byKey: make(map[string]fact.ExtensionSchema),
	}
	for manifestIndex, manifest := range manifests {
		for schemaIndex, schema := range manifest.Extensions {
			if err := schema.Validate(); err != nil {
				return extensionSchemaIndex{}, fmt.Errorf("%w: frontend manifest %d schema %d", ErrInvalidExtension, manifestIndex, schemaIndex)
			}
			identity := schema.ID + "\x00" + schema.Version
			if previous, exists := index.byKey[identity]; exists && previous.Digest != schema.Digest {
				return extensionSchemaIndex{}, fmt.Errorf("%w: conflicting schema declaration", ErrInvalidExtension)
			}
			index.byKey[identity] = schema
		}
	}
	return index, nil
}

// Validate checks that the extension envelope, its canonical schema bytes and
// its payload are valid against the complete set of declared frontend
// manifests. Nil or empty manifests are safe inputs and simply cannot declare
// a schema for a record.
func (r ExtensionRecord) Validate(manifests []fact.FrontendManifest) error {
	index, err := newExtensionSchemaIndex(manifests)
	if err != nil {
		return err
	}
	return r.validate(index)
}

func (r ExtensionRecord) validate(index extensionSchemaIndex) error {
	if err := validateIdentifier("extension schema id", r.SchemaID); err != nil {
		return fmt.Errorf("%w: malformed extension metadata", ErrInvalidExtension)
	}
	if err := validateIdentifier("extension schema version", r.SchemaVersion); err != nil {
		return fmt.Errorf("%w: malformed extension metadata", ErrInvalidExtension)
	}
	schema := fact.ExtensionSchema{ID: r.SchemaID, Version: r.SchemaVersion, Digest: r.SchemaDigest}
	if err := schema.Validate(); err != nil {
		return fmt.Errorf("%w: malformed extension metadata", ErrInvalidExtension)
	}
	declared, exists := index.byKey[r.SchemaID+"\x00"+r.SchemaVersion]
	if !exists || declared.Digest != r.SchemaDigest {
		return fmt.Errorf("%w: extension schema is not declared", ErrInvalidExtension)
	}
	canonicalSchema, err := canonicalExtensionJSON(r.Schema)
	if err != nil {
		return fmt.Errorf("%w: malformed extension schema", ErrInvalidExtension)
	}
	if err := schema.Verify(canonicalSchema); err != nil {
		return fmt.Errorf("%w: extension schema digest mismatch", ErrInvalidExtension)
	}
	if _, err := canonicalExtensionJSON(r.Payload); err != nil {
		return fmt.Errorf("%w: malformed extension payload", ErrInvalidExtension)
	}
	return nil
}

func validateImportedExtensions(manifests []fact.FrontendManifest, extensions []json.RawMessage) error {
	// Validate declarations once, even when the transport carries no extension
	// records; this preserves the bundle-level manifest validation boundary and
	// keeps validation O(manifests + extensions).
	index, err := newExtensionSchemaIndex(manifests)
	if err != nil {
		return err
	}
	for extensionIndex, raw := range extensions {
		canonical, err := canonicalExtensionJSON(raw)
		if err != nil {
			return fmt.Errorf("%w: extension %d is malformed", ErrInvalidExtension, extensionIndex)
		}
		record, err := decodeExtensionRecord(canonical)
		if err != nil {
			return fmt.Errorf("%w: extension %d", err, extensionIndex)
		}
		if err := record.validate(index); err != nil {
			return fmt.Errorf("%w: extension %d", err, extensionIndex)
		}
	}
	return nil
}

func decodeExtensionRecord(raw []byte) (ExtensionRecord, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return ExtensionRecord{}, fmt.Errorf("%w: extension envelope must be an object", ErrInvalidExtension)
	}
	_, hasID := fields["schema_id"]
	_, hasVersion := fields["schema_version"]
	_, hasDigest := fields["schema_digest_sha256"]
	_, hasSchema := fields["schema"]
	_, hasPayload := fields["payload"]
	if !hasID || !hasVersion || !hasDigest || !hasSchema || !hasPayload {
		return ExtensionRecord{}, fmt.Errorf("%w: incomplete extension metadata", ErrInvalidExtension)
	}
	var record ExtensionRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return ExtensionRecord{}, fmt.Errorf("%w: malformed extension metadata", ErrInvalidExtension)
	}
	if len(record.Schema) == 0 || strings.TrimSpace(string(record.Schema)) == "" {
		return ExtensionRecord{}, fmt.Errorf("%w: missing extension schema", ErrInvalidExtension)
	}
	if len(record.Payload) == 0 || strings.TrimSpace(string(record.Payload)) == "" {
		return ExtensionRecord{}, fmt.Errorf("%w: missing extension payload", ErrInvalidExtension)
	}
	return record, nil
}
