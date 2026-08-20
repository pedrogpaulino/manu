package persistence

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func TestExpandRelationsSQLIsScopedParameterizedAndDeterministic(t *testing.T) {
	if !strings.Contains(expandRelationsSQL, "FROM relationships") {
		t.Fatal("relation SQL must read canonical relationships")
	}
	for _, fragment := range []string{
		"organization_id = $1::uuid",
		"source_id = $2::uuid",
		"snapshot_id = $3::uuid",
		"$4::text = 'outbound'",
		"$4::text = 'inbound'",
		"$4::text = 'both'",
		"$5::uuid",
		"ORDER BY from_entity_id ASC, to_entity_id ASC, relationship_type ASC, id ASC",
		"LIMIT $6",
	} {
		if !strings.Contains(expandRelationsSQL, fragment) {
			t.Errorf("relation SQL omits %q", fragment)
		}
	}
	seen := make(map[int]bool)
	for _, match := range regexp.MustCompile(`\$(\d+)`).FindAllStringSubmatch(expandRelationsSQL, -1) {
		parameter, err := strconv.Atoi(match[1])
		if err != nil {
			t.Fatalf("parse SQL parameter %q: %v", match[0], err)
		}
		seen[parameter] = true
	}
	for parameter := 1; parameter <= 6; parameter++ {
		if !seen[parameter] {
			t.Errorf("relation SQL omits parameter $%d", parameter)
		}
	}
	if strings.Contains(expandRelationsSQL, "fmt.") || strings.Contains(expandRelationsSQL, "%s") {
		t.Fatal("relation SQL appears to interpolate a value")
	}
}
