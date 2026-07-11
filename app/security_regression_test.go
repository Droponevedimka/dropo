package main

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

func TestTrustedDepsManifestIgnoresAdjacentManifest(t *testing.T) {
	old := []string{trustedDepsVersion, trustedDepsAsset, trustedDepsSHA256, trustedDepsSize, trustedDepsURL, trustedDepsRequired}
	t.Cleanup(func() {
		trustedDepsVersion, trustedDepsAsset, trustedDepsSHA256, trustedDepsSize, trustedDepsURL, trustedDepsRequired = old[0], old[1], old[2], old[3], old[4], old[5]
	})
	trustedDepsVersion = "0123456789ab"
	trustedDepsAsset = "dropo-Windows-Dependencies-x64.zip"
	trustedDepsSHA256 = "687d903d03f5dcbda1dbd0f66231fa32ecfd67a9dc0e451aa34a669e49db1f89"
	trustedDepsSize = "123"
	trustedDepsURL = "https://github.com/Droponevedimka/dropo/releases/download/v2.1.0/dropo-Windows-Dependencies-x64.zip"
	trustedDepsRequired = "sing-box.exe,winws.exe,WinDivert.dll"

	base := t.TempDir()
	if err := os.WriteFile(filepath.Join(base, "dependencies.json"), []byte(`{"url":"https://evil.invalid/payload.zip","sha256":""}`), 0600); err != nil {
		t.Fatal(err)
	}
	m, ok := (&App{basePath: base}).loadDepsManifest()
	if !ok || m.URL != trustedDepsURL || m.SHA256 != trustedDepsSHA256 || m.Size != 123 {
		t.Fatalf("signed dependency identity was not authoritative: ok=%v manifest=%+v", ok, m)
	}
}

func TestExtractedFilesMatchTrustedArchive(t *testing.T) {
	root := t.TempDir()
	archive := filepath.Join(root, "dependencies.zip")
	dest := filepath.Join(root, "bin")
	if err := os.MkdirAll(dest, 0700); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	w, err := zw.Create("sing-box.exe")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write([]byte("trusted"))
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	path := filepath.Join(dest, "sing-box.exe")
	if err := os.WriteFile(path, []byte("trusted"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := extractedFilesMatchArchive(archive, dest); err != nil {
		t.Fatalf("matching extraction rejected: %v", err)
	}
	if err := os.WriteFile(path, []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := extractedFilesMatchArchive(archive, dest); err == nil {
		t.Fatal("tampered executable accepted")
	}
}

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

func TestNormalizeGitHubSHA256(t *testing.T) {
	digest := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if got := normalizeGitHubSHA256("sha256:" + digest); got != digest {
		t.Fatalf("digest = %q", got)
	}
	if got := normalizeGitHubSHA256("sha256:not-a-hash"); got != "" {
		t.Fatalf("invalid digest accepted: %q", got)
	}
}
