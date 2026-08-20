// Package identity contains deterministic identifiers shared by application
// boundaries that must agree on canonical PostgreSQL UUIDs.
package identity

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// CanonicalUUID derives the stable UUID-shaped identity used by the canonical
// persistence model. The domain kind and each external component are length
// delimited, so concatenation ambiguity cannot produce a second identity.
// No timestamps, random values, or content bodies are included implicitly.
func CanonicalUUID(kind string, values ...string) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte("manu:persistence:batch:v1\x00"))
	writePart := func(value string) {
		_, _ = hash.Write([]byte(fmt.Sprintf("%d:", len(value))))
		_, _ = hash.Write([]byte(value))
		_, _ = hash.Write([]byte{'\x00'})
	}
	writePart(kind)
	for _, value := range values {
		writePart(value)
	}
	digest := hash.Sum(nil)
	digest[6] = (digest[6] & 0x0f) | 0x50
	digest[8] = (digest[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(digest[:16])
	return strings.Join([]string{
		encoded[0:8], encoded[8:12], encoded[12:16], encoded[16:20], encoded[20:32],
	}, "-")
}
