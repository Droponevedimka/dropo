package dropocore

import "testing"

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
