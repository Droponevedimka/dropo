package main

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestSafeRelativePathRejectsTraversal(t *testing.T) {
	for _, value := range []string{"../dropo.exe", "resources/../../dropo.exe", "C:/dropo.exe", "/dropo.exe"} {
		if _, err := safeRelativePath(value); err == nil {
			t.Fatalf("safeRelativePath(%q) accepted traversal or absolute path", value)
		}
	}
	if got, err := safeRelativePath("resources/dropo-ui.exe"); err != nil || got == "" {
		t.Fatalf("safe path rejected: got %q, err %v", got, err)
	}
}

func TestValidVersion(t *testing.T) {
	if !validVersion("3.0.1") || validVersion("../3.0.1") || validVersion("3.0.1/evil") {
		t.Fatal("version validation does not enforce a single safe path segment")
	}
}

func TestExtractPayloadRecognizesWindowsDirectoryEntry(t *testing.T) {
	var payload bytes.Buffer
	writer := zip.NewWriter(&payload)
	if _, err := writer.Create("resources\\data\\"); err != nil {
		t.Fatal(err)
	}
	file, err := writer.Create("resources\\data\\asset.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("ok")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	destination := t.TempDir()
	if err := extractPayload(destination, payload.Bytes()); err != nil {
		t.Fatalf("extractPayload: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(destination, "resources", "data", "asset.txt")); err != nil || string(data) != "ok" {
		t.Fatalf("extracted file = %q, err %v", data, err)
	}
}
