package dropocore

import (
	"strings"
	"testing"
)

func TestSubscriptionURLRequiresHTTPS(t *testing.T) {
	if err := validateSubscriptionURL("https://example.com/sub/token"); err != nil {
		t.Fatalf("HTTPS rejected: %v", err)
	}
	for _, raw := range []string{"http://example.com/sub", "file:///tmp/sub", "https://user:pass@example.com/sub"} {
		if err := validateSubscriptionURL(raw); err == nil {
			t.Errorf("unsafe subscription URL accepted: %s", raw)
		}
	}
}

func TestInvalidSubscriptionURLErrorDoesNotEchoInput(t *testing.T) {
	const secret = "secret-token"
	err := validateSubscriptionURL("https://example.test/%zz" + secret)
	if err == nil {
		t.Fatal("validateSubscriptionURL() error = nil")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("validation error leaked subscription input: %q", err)
	}
}
