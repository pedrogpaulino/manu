package java

import (
	"testing"
)

func TestParseExtractsConfigurationOnlyOutsideLiterals(t *testing.T) {
	parsed := parse(`package example;
String fake = "@ConfigProperty(name = 'fake.key') System.getenv('fake.env')";
// @ConfigProperty(name = "comment.key")
@ConfigProperty(name = "real.key")
void configure() { System.getenv("REAL_ENV"); }`)

	keys := make(map[string]bool)
	for _, observation := range parsed.observations {
		if observation.Type != "java.configuration" {
			continue
		}
		key, ok := observation.Value["key"].(string)
		if ok {
			keys[key] = true
		}
	}
	if !keys["real.key"] || !keys["REAL_ENV"] {
		t.Fatalf("configuration keys = %#v, want real.key and REAL_ENV", keys)
	}
	if keys["fake.key"] || keys["fake.env"] || keys["comment.key"] {
		t.Fatalf("configuration literal/comment false positives = %#v", keys)
	}
}

func TestParseExtractsDirectJavaRelations(t *testing.T) {
	parsed := parse(`class BookingService extends BaseService implements Auditable, Traceable {
    Booking create() { return repository.save(new Booking()); }
}`)
	methods := make(map[string]bool)
	for _, observation := range parsed.observations {
		methods[observation.Method] = true
	}
	for _, want := range []string{
		"relation:BookingService:extends:BaseService",
		"relation:BookingService:implements:Auditable",
		"relation:BookingService:implements:Traceable",
	} {
		if !methods[want] {
			t.Fatalf("missing relation %q in %#v", want, methods)
		}
	}
}
