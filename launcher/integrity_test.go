package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestVerifyFileSHA256(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.exe")
	data := []byte("signed application")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	expected := fmt.Sprintf("%x", sha256.Sum256(data))
	if err := verifyFileSHA256(path, expected); err != nil {
		t.Fatalf("valid hash rejected: %v", err)
	}
	if err := verifyFileSHA256(path, fmt.Sprintf("%064x", 1)); err == nil {
		t.Fatal("mismatch accepted")
	}
	if err := verifyFileSHA256(path, ""); err == nil {
		t.Fatal("empty release hash accepted")
	}
}
