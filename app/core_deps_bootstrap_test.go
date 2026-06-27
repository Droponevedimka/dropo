package main

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

func writeDepsManifest(t *testing.T, base, depsVersion string) {
	t.Helper()
	m := `{"depsVersion":"` + depsVersion + `","asset":"dependencies-` + depsVersion + `.zip","sha256":"","size":1048576,"appVersion":"9.9.9","repo":"Droponevedimka/dropo"}`
	if err := os.WriteFile(filepath.Join(base, "dependencies.json"), []byte(m), 0644); err != nil {
		t.Fatal(err)
	}
}

func makeFakeBin(t *testing.T, base, marker string) {
	t.Helper()
	bin := filepath.Join(base, "bin")
	if err := os.MkdirAll(bin, 0755); err != nil {
		t.Fatal(err)
	}
	for _, n := range requiredDepBinaries {
		if err := os.WriteFile(filepath.Join(bin, n), []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if marker != "" {
		if err := os.WriteFile(filepath.Join(bin, ".deps-version"), []byte(marker), 0644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDependenciesStatus(t *testing.T) {
	// unmanaged build (no manifest): ready iff bin complete
	base := t.TempDir()
	app := &App{basePath: base}
	if app.DependenciesStatus().Managed {
		t.Fatal("no manifest should be unmanaged")
	}
	makeFakeBin(t, base, "")
	if !app.DependenciesStatus().Ready {
		t.Fatal("unmanaged build with complete bin should be ready")
	}

	// managed, marker matches -> ready
	base2 := t.TempDir()
	app2 := &App{basePath: base2}
	writeDepsManifest(t, base2, "abc123")
	makeFakeBin(t, base2, "abc123")
	st := app2.DependenciesStatus()
	if !st.Managed || !st.Ready || st.Required != "abc123" {
		t.Fatalf("managed+matching should be ready: %+v", st)
	}

	// managed, marker mismatch -> not ready
	if err := os.WriteFile(filepath.Join(base2, "bin", ".deps-version"), []byte("OLD"), 0644); err != nil {
		t.Fatal(err)
	}
	if app2.DependenciesStatus().Ready {
		t.Fatal("version mismatch must report not ready")
	}

	// managed, marker matches but a key binary missing -> not ready
	os.WriteFile(filepath.Join(base2, "bin", ".deps-version"), []byte("abc123"), 0644)
	os.Remove(filepath.Join(base2, "bin", requiredDepBinaries[0]))
	if app2.DependenciesStatus().Ready {
		t.Fatal("missing key binary must report not ready")
	}
}

func TestExtractZip(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "a.zip")
	zf, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(zf)
	add := func(name, body string) {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(body))
	}
	add("sing-box.exe", "binary")
	add("filters/version.json", "{}")
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	zf.Close()

	dest := filepath.Join(dir, "bin")
	if err := extractZip(zipPath, dest); err != nil {
		t.Fatalf("extract: %v", err)
	}
	if !fileExists(filepath.Join(dest, "sing-box.exe")) || !fileExists(filepath.Join(dest, "filters", "version.json")) {
		t.Fatal("expected extracted files missing")
	}
}

func TestExtractZipRejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "evil.zip")
	zf, _ := os.Create(zipPath)
	zw := zip.NewWriter(zf)
	w, _ := zw.Create("../escape.txt")
	w.Write([]byte("nope"))
	zw.Close()
	zf.Close()

	if err := extractZip(zipPath, filepath.Join(dir, "bin")); err == nil {
		t.Fatal("path traversal entry must be rejected")
	}
}
