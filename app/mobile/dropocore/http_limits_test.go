package dropocore

import (
	"strings"
	"testing"
)

func TestReadHTTPBodyLimited(t *testing.T) {
	body, err := readHTTPBodyLimited(strings.NewReader("1234"), 4)
	if err != nil || string(body) != "1234" {
		t.Fatalf("exact-limit read = %q, %v", body, err)
	}
	if _, err := readHTTPBodyLimited(strings.NewReader("12345"), 4); err == nil {
		t.Fatal("oversized response was accepted")
	}
}
