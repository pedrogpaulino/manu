package persistence_test

import "testing"

func TestSafeIntegrationIdentifier(t *testing.T) {
	for _, value := range []string{"manu_it_123", "_manu_it", "Manu_42"} {
		t.Run("valid/"+value, func(t *testing.T) {
			if !safeIntegrationIdentifier(value) {
				t.Fatalf("safeIntegrationIdentifier(%q) = false, want true", value)
			}
		})
	}

	for _, value := range []string{"", "123manu", `manu"it`, "manu;DROP SCHEMA other", "manu-it", "manu it"} {
		t.Run("invalid/"+value, func(t *testing.T) {
			if safeIntegrationIdentifier(value) {
				t.Fatalf("safeIntegrationIdentifier(%q) = true, want false", value)
			}
		})
	}
}

func safeIntegrationIdentifier(value string) bool {
	if value == "" {
		return false
	}
	for index := 0; index < len(value); index++ {
		character := value[index]
		if index == 0 {
			if !integrationASCIILetter(character) && character != '_' {
				return false
			}
			continue
		}
		if !integrationASCIILetter(character) && (character < '0' || character > '9') && character != '_' {
			return false
		}
	}
	return true
}

func integrationASCIILetter(character byte) bool {
	return (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z')
}
