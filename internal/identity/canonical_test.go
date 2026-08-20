package identity

import "testing"

func TestCanonicalUUIDIsStableAndNamespaced(t *testing.T) {
	tests := []struct {
		name   string
		kind   string
		values []string
	}{
		{name: "organization", kind: "organization", values: []string{"local"}},
		{name: "length delimited", kind: "source", values: []string{"ab", "c"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			first := CanonicalUUID(tt.kind, tt.values...)
			second := CanonicalUUID(tt.kind, tt.values...)
			if first != second {
				t.Fatalf("CanonicalUUID() changed across calls: %q != %q", first, second)
			}
			if len(first) != 36 || first[14] != '5' || first[19] < '8' {
				t.Fatalf("CanonicalUUID() = %q, want UUID-shaped version 5", first)
			}
		})
	}
	if CanonicalUUID("source", "ab", "c") == CanonicalUUID("source", "a", "bc") {
		t.Fatal("length-delimited identities collided")
	}
	if CanonicalUUID("organization", "local") == CanonicalUUID("organization", "other") {
		t.Fatal("distinct external identities collided")
	}
}
