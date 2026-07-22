package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	bundledRuntimeManifestName = "runtime-manifest.json"
	maxBundledRuntimeFiles     = 20000
	maxBundledRuntimeBytes     = int64(1 << 30)
)

// These values are injected into the signed Windows core. The manifest hash,
// not an adjacent URL, is the trust root for the native runtime shipped inside
// the self-extracting application.
var (
	trustedRuntimeVersion        string
	trustedRuntimeManifestSHA256 string
)

type bundledRuntimeManifest struct {
	Version string                       `json:"version"`
	Files   []bundledRuntimeManifestFile `json:"files"`
}

type bundledRuntimeManifestFile struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

func bundledRuntimeEnabled() bool {
	return strings.TrimSpace(trustedRuntimeVersion) != "" &&
		len(strings.TrimSpace(trustedRuntimeManifestSHA256)) == sha256.Size*2
}

// installBundledRuntime verifies the complete embedded runtime against the
// manifest identity compiled into the signed core, then materializes it in the
// administrator-only ProgramData runtime. No dependency is downloaded at
// application startup or first connection.
func installBundledRuntime(sourceRoot, destinationRoot, version, manifestSHA256 string) error {
	manifest, manifestData, err := loadTrustedRuntimeManifest(sourceRoot, version, manifestSHA256)
	if err != nil {
		return err
	}
	if strings.TrimSpace(destinationRoot) == "" {
		return errors.New("bundled runtime destination is empty")
	}
	if err := os.MkdirAll(destinationRoot, 0755); err != nil {
		return fmt.Errorf("create bundled runtime destination: %w", err)
	}

	for _, item := range manifest.Files {
		rel, err := safeBundledRuntimePath(item.Path)
		if err != nil {
			return err
		}
		sourcePath := filepath.Join(sourceRoot, rel)
		destinationPath := filepath.Join(destinationRoot, rel)
		if verifyRegularFile(destinationPath, item.Size, item.SHA256) == nil {
			continue
		}
		if err := copyVerifiedRuntimeFile(sourcePath, destinationPath, item.Size, item.SHA256); err != nil {
			return fmt.Errorf("install bundled runtime file %s: %w", item.Path, err)
		}
	}

	manifestDestination := filepath.Join(destinationRoot, bundledRuntimeManifestName)
	if err := writeRuntimeFile(manifestDestination, manifestData, hashBytes(manifestData)); err != nil {
		return fmt.Errorf("install bundled runtime manifest: %w", err)
	}
	markerPath := filepath.Join(destinationRoot, "bin", ".deps-version")
	if err := writeRuntimeFile(markerPath, []byte(version), hashBytes([]byte(version))); err != nil {
		return fmt.Errorf("write bundled runtime version marker: %w", err)
	}
	return verifyBundledRuntime(sourceRoot, destinationRoot, version, manifestSHA256)
}

func verifyBundledRuntime(sourceRoot, destinationRoot, version, manifestSHA256 string) error {
	manifest, _, err := loadTrustedRuntimeManifest(sourceRoot, version, manifestSHA256)
	if err != nil {
		return err
	}
	for _, item := range manifest.Files {
		rel, err := safeBundledRuntimePath(item.Path)
		if err != nil {
			return err
		}
		if err := verifyRegularFile(filepath.Join(destinationRoot, rel), item.Size, item.SHA256); err != nil {
			return fmt.Errorf("verify installed runtime file %s: %w", item.Path, err)
		}
	}
	marker, err := os.ReadFile(filepath.Join(destinationRoot, "bin", ".deps-version"))
	if err != nil || strings.TrimSpace(string(marker)) != version {
		return errors.New("bundled runtime version marker is missing or invalid")
	}
	return nil
}

func loadTrustedRuntimeManifest(root, version, expectedSHA256 string) (*bundledRuntimeManifest, []byte, error) {
	version = strings.TrimSpace(version)
	expectedSHA256 = strings.ToLower(strings.TrimSpace(expectedSHA256))
	if version == "" || len(expectedSHA256) != sha256.Size*2 {
		return nil, nil, errors.New("bundled runtime trust identity is incomplete")
	}
	if _, err := hex.DecodeString(expectedSHA256); err != nil {
		return nil, nil, errors.New("bundled runtime manifest SHA-256 is invalid")
	}
	data, err := os.ReadFile(filepath.Join(root, bundledRuntimeManifestName))
	if err != nil {
		return nil, nil, fmt.Errorf("read bundled runtime manifest: %w", err)
	}
	if hashBytes(data) != expectedSHA256 {
		return nil, nil, errors.New("bundled runtime manifest does not match the signed core")
	}
	var manifest bundledRuntimeManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, nil, fmt.Errorf("decode bundled runtime manifest: %w", err)
	}
	if manifest.Version != version || len(manifest.Files) == 0 || len(manifest.Files) > maxBundledRuntimeFiles {
		return nil, nil, errors.New("bundled runtime manifest has an invalid version or file count")
	}
	seen := make(map[string]struct{}, len(manifest.Files))
	var total int64
	for _, item := range manifest.Files {
		rel, err := safeBundledRuntimePath(item.Path)
		if err != nil {
			return nil, nil, err
		}
		key := strings.ToLower(filepath.ToSlash(rel))
		if _, exists := seen[key]; exists {
			return nil, nil, fmt.Errorf("duplicate bundled runtime path: %s", item.Path)
		}
		seen[key] = struct{}{}
		if item.Size < 0 || item.Size > maxBundledRuntimeBytes {
			return nil, nil, fmt.Errorf("invalid bundled runtime file size: %s", item.Path)
		}
		total += item.Size
		if total > maxBundledRuntimeBytes {
			return nil, nil, errors.New("bundled runtime exceeds the maximum extracted size")
		}
		sha := strings.ToLower(strings.TrimSpace(item.SHA256))
		if len(sha) != sha256.Size*2 {
			return nil, nil, fmt.Errorf("invalid SHA-256 for bundled runtime file: %s", item.Path)
		}
		if _, err := hex.DecodeString(sha); err != nil {
			return nil, nil, fmt.Errorf("invalid SHA-256 for bundled runtime file: %s", item.Path)
		}
	}
	return &manifest, data, nil
}

func safeBundledRuntimePath(name string) (string, error) {
	name = strings.ReplaceAll(strings.TrimSpace(name), "\\", "/")
	clean := filepath.Clean(filepath.FromSlash(name))
	if name == "" || strings.HasPrefix(name, "/") || filepath.IsAbs(clean) || filepath.VolumeName(clean) != "" ||
		clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("unsafe bundled runtime path: %q", name)
	}
	parts := strings.Split(filepath.ToSlash(clean), "/")
	if len(parts) < 2 || !strings.EqualFold(parts[0], "bin") {
		return "", fmt.Errorf("bundled runtime path must be below bin/: %q", name)
	}
	return clean, nil
}

func copyVerifiedRuntimeFile(sourcePath, destinationPath string, expectedSize int64, expectedSHA256 string) error {
	sourceInfo, err := os.Lstat(sourcePath)
	if err != nil {
		return err
	}
	if !sourceInfo.Mode().IsRegular() || sourceInfo.Size() != expectedSize {
		return errors.New("source is not the expected regular file")
	}
	if err := os.MkdirAll(filepath.Dir(destinationPath), 0755); err != nil {
		return err
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer source.Close()
	temp, err := os.CreateTemp(filepath.Dir(destinationPath), ".dropo-runtime-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	keepTemp := false
	defer func() {
		_ = temp.Close()
		if !keepTemp {
			_ = os.Remove(tempPath)
		}
	}()
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(temp, hash), io.LimitReader(source, expectedSize+1))
	if copyErr != nil {
		return copyErr
	}
	if written != expectedSize || !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), expectedSHA256) {
		return errors.New("source file checksum or size mismatch")
	}
	if err := temp.Sync(); err != nil {
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Remove(destinationPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Rename(tempPath, destinationPath); err != nil {
		return err
	}
	keepTemp = true
	return verifyRegularFile(destinationPath, expectedSize, expectedSHA256)
}

func writeRuntimeFile(path string, data []byte, expectedSHA256 string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	if verifyRegularFile(path, int64(len(data)), expectedSHA256) == nil {
		return nil
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".dropo-runtime-meta-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.Rename(tempPath, path)
}

func verifyRegularFile(path string, expectedSize int64, expectedSHA256 string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() != expectedSize {
		return errors.New("file type or size mismatch")
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return err
	}
	if !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), strings.TrimSpace(expectedSHA256)) {
		return errors.New("SHA-256 mismatch")
	}
	return nil
}

func hashBytes(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}
