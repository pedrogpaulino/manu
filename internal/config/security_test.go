package config

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateHTTPURLRejectsCredentialAndRoutingInjection(t *testing.T) {
	const secret = "config-test-secret"
	tests := []struct {
		name string
		url  string
	}{
		{name: "userinfo", url: "https://user:" + secret + "@provider.example/v1"},
		{name: "query", url: "https://provider.example/v1?api_key=" + secret},
		{name: "fragment", url: "https://provider.example/v1#" + secret},
		{name: "wrong scheme", url: "file:///tmp/provider"},
		{name: "missing host", url: "https:///v1"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateHTTPURL(test.url)
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("validateHTTPURL(%q) = %v, want ErrInvalid", test.url, err)
			}
			if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), test.url) {
				t.Fatalf("URL validation error exposed input: %v", err)
			}
		})
	}

	for _, raw := range []string{"https://provider.example/v1", "http://127.0.0.1:4318/v1"} {
		if err := validateHTTPURL(raw); err != nil {
			t.Fatalf("validateHTTPURL(%q) = %v, want nil", raw, err)
		}
	}
}
