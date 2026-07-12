package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBundledPetCertificateIdentity(t *testing.T) {
	path := filepath.Join("..", "scripts", "signing", "certificate", "dropo-pet-code-signing.cer")
	cert, der, err := loadAndValidatePetCertificate(path)
	if err != nil {
		t.Fatalf("bundled certificate rejected: %v", err)
	}
	if cert.Subject.CommonName != "dropo Pet Project Code Signing" || len(der) == 0 {
		t.Fatalf("unexpected bundled certificate: %s", cert.Subject)
	}
}

func TestPetCertificateReleasePath(t *testing.T) {
	root := filepath.Join("C:", "dropo")
	want := filepath.Join(root, "resources", "cert", "dropo-pet-code-signing.cer")
	if got := petCertificatePath(root); got != want {
		t.Fatalf("pet certificate path = %q, want %q", got, want)
	}
}

func TestPetCertificateRejectsTampering(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "scripts", "signing", "certificate", "dropo-pet-code-signing.cer"))
	if err != nil {
		t.Fatal(err)
	}
	data[len(data)-1] ^= 0xff
	path := filepath.Join(t.TempDir(), "tampered.cer")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadAndValidatePetCertificate(path); err == nil {
		t.Fatal("tampered certificate accepted")
	}
}
