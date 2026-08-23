package fact

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
)

const (
	// canonicalEncodingDomain separates a transport digest from other JSON that
	// may be hashed by the application.
	canonicalEncodingDomain = "manu:fact:canonical:v1alpha1\x00"
	// identityDomain is deliberately distinct from the transport domain. The
	// identity digest is over semantic fact fields, not the supplied ID or
	// mutable support metadata.
	identityDomain = "manu:fact:identity:v1alpha1\x00"
)

// canonicalFactEncoding is the complete transport representation. Structs,
// rather than maps, define field order in the JSON output. Collection fields
// are copied and sorted before this value is marshaled.
type canonicalFactEncoding struct {
	Version    string        `json:"version"`
	ID         string        `json:"id"`
	Scope      Scope         `json:"scope"`
	Predicate  Predicate     `json:"predicate"`
	Subject    Participant   `json:"subject"`
	Object     *Participant  `json:"object"`
	Value      *TypedValue   `json:"value"`
	Qualifiers []Qualifier   `json:"qualifiers"`
	Producer   Producer      `json:"producer"`
	Evidence   []EvidenceRef `json:"evidence"`
	Lineage    *Lineage      `json:"lineage"`
}

// factIdentityEncoding contains the semantic fields selected by decision 1.
// Evidence and lineage are transport/support metadata: changing or adding
// support must not create a second fact identity. A derived fact's rule and
// version remain distinguishable through its Producer fields, while its
// input chain remains available in Lineage for audit and rebuild.
type factIdentityEncoding struct {
	Version    string       `json:"version"`
	Scope      Scope        `json:"scope"`
	Predicate  Predicate    `json:"predicate"`
	Subject    Participant  `json:"subject"`
	Object     *Participant `json:"object"`
	Value      *TypedValue  `json:"value"`
	Qualifiers []Qualifier  `json:"qualifiers"`
	Producer   Producer     `json:"producer"`
}

// CanonicalBytes returns the deterministic transport encoding of one fact.
// It includes the supplied identity, evidence references, and lineage so a
// round-trip can preserve the complete fact record. The input fact and all of
// its slices/pointers remain unchanged.
func CanonicalBytes(fact CanonicalFact) ([]byte, error) {
	if err := fact.Validate(); err != nil {
		return nil, fmt.Errorf("encoding canonical fact: %w", err)
	}
	canonical := canonicalize(fact)
	return marshalCanonical(canonical)
}

// EncodeCanonical is an explicit verb-first spelling for CanonicalBytes.
func EncodeCanonical(fact CanonicalFact) ([]byte, error) {
	return CanonicalBytes(fact)
}

// IdentityBytes returns the deterministic semantic representation used by
// FactID. It intentionally excludes the supplied ID, evidence, and lineage;
// callers can add support or rebuild derivations without changing the fact's
// semantic identity.
func IdentityBytes(fact CanonicalFact) ([]byte, error) {
	if err := fact.validateStructure(); err != nil {
		return nil, fmt.Errorf("encoding fact identity: %w", err)
	}
	canonical := canonicalizeIdentity(fact)
	return marshalCanonical(canonical)
}

// FactID derives a stable, namespaced identity from a structurally valid fact.
// The supplied ID is excluded from the semantic material, so callers can
// derive an identity before assigning it to the fact.
func FactID(fact CanonicalFact) (string, error) {
	digest, err := IdentityDigest(fact)
	if err != nil {
		return "", err
	}
	return "fact-" + digest, nil
}

// IdentityDigest returns the lowercase SHA-256 digest of the semantic
// identity representation, without the human-readable "fact-" prefix.
func IdentityDigest(fact CanonicalFact) (string, error) {
	bytes, err := IdentityBytes(fact)
	if err != nil {
		return "", err
	}
	digest := digestDomain(identityDomain, bytes)
	return hex.EncodeToString(digest[:]), nil
}

// FactDigest is a descriptive alias for IdentityDigest.
func FactDigest(fact CanonicalFact) (string, error) {
	return IdentityDigest(fact)
}

// CanonicalDigest returns the lowercase SHA-256 digest of the complete
// transport representation, including the supplied ID, evidence, and
// lineage. It is distinct from the semantic identity digest.
func CanonicalDigest(fact CanonicalFact) (string, error) {
	bytes, err := CanonicalBytes(fact)
	if err != nil {
		return "", err
	}
	digest := digestDomain(canonicalEncodingDomain, bytes)
	return hex.EncodeToString(digest[:]), nil
}

// Identity is the domain spelling for FactID.
func Identity(fact CanonicalFact) (string, error) {
	return FactID(fact)
}

// DeriveID is an explicit alias for callers migrating from supplied IDs to
// the deterministic identity API. It never mutates the fact.
func DeriveID(fact CanonicalFact) (string, error) {
	return FactID(fact)
}

// ValidateIdentity checks that a supplied fact ID matches the deterministic
// identity of the semantic fields. It has the same final-fact contract as
// CanonicalFact.Validate.
func ValidateIdentity(fact CanonicalFact) error {
	return fact.Validate()
}

// ValidateIdentity checks the supplied ID against the deterministic identity
// without changing the fact.
func (fact CanonicalFact) ValidateIdentity() error {
	return ValidateIdentity(fact)
}

func marshalCanonical(value any) ([]byte, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshaling canonical fact: %w", err)
	}
	return payload, nil
}

func digestDomain(domain string, payload []byte) [sha256.Size]byte {
	hash := sha256.New()
	_, _ = hash.Write([]byte(domain))
	_, _ = hash.Write(payload)
	var digest [sha256.Size]byte
	copy(digest[:], hash.Sum(nil))
	return digest
}

func canonicalize(fact CanonicalFact) canonicalFactEncoding {
	qualifiers := append([]Qualifier(nil), fact.Qualifiers...)
	sort.SliceStable(qualifiers, func(i, j int) bool {
		return qualifierKey(qualifiers[i]) < qualifierKey(qualifiers[j])
	})

	evidence := append([]EvidenceRef(nil), fact.Evidence...)
	sort.SliceStable(evidence, func(i, j int) bool {
		return evidenceKey(evidence[i]) < evidenceKey(evidence[j])
	})

	var lineage *Lineage
	if fact.Lineage != nil {
		copyOfLineage := *fact.Lineage
		copyOfLineage.InputFactIDs = append([]string(nil), fact.Lineage.InputFactIDs...)
		sort.Strings(copyOfLineage.InputFactIDs)
		lineage = &copyOfLineage
	}

	return canonicalFactEncoding{
		Version:    fact.Version,
		ID:         fact.ID,
		Scope:      fact.Scope,
		Predicate:  fact.Predicate,
		Subject:    fact.Subject,
		Object:     cloneParticipant(fact.Object),
		Value:      cloneValue(fact.Value),
		Qualifiers: qualifiers,
		Producer:   fact.Producer,
		Evidence:   evidence,
		Lineage:    lineage,
	}
}

func canonicalizeIdentity(fact CanonicalFact) factIdentityEncoding {
	qualifiers := append([]Qualifier(nil), fact.Qualifiers...)
	sort.SliceStable(qualifiers, func(i, j int) bool {
		return qualifierKey(qualifiers[i]) < qualifierKey(qualifiers[j])
	})

	return factIdentityEncoding{
		Version:    fact.Version,
		Scope:      fact.Scope,
		Predicate:  fact.Predicate,
		Subject:    fact.Subject,
		Object:     cloneParticipant(fact.Object),
		Value:      cloneValue(fact.Value),
		Qualifiers: qualifiers,
		Producer:   fact.Producer,
	}
}

func cloneParticipant(participant *Participant) *Participant {
	if participant == nil {
		return nil
	}
	copyOfParticipant := *participant
	return &copyOfParticipant
}

func cloneValue(value *TypedValue) *TypedValue {
	if value == nil {
		return nil
	}
	copyOfValue := *value
	return &copyOfValue
}

func qualifierKey(qualifier Qualifier) string {
	bytes, _ := json.Marshal(qualifier)
	return qualifier.Name + "\x00" + string(bytes)
}

func evidenceKey(evidence EvidenceRef) string {
	bytes, _ := json.Marshal(evidence)
	return evidence.ID + "\x00" + string(bytes)
}
