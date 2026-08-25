package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLicenseFilesAreDeterministicAndExcludeUnrelatedFiles(t *testing.T) {
	directory := t.TempDir()
	for name, content := range map[string]string{
		"NOTICE":     "notice",
		"LICENSE.md": "license",
		"PATENTS":    "patents",
		"README.md":  "readme",
	} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(directory, "LICENSES"), 0o700); err != nil {
		t.Fatal(err)
	}
	files, err := licenseFiles(directory)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"LICENSE.md", "NOTICE", "PATENTS"}
	if !reflect.DeepEqual(files, want) {
		t.Fatalf("license files = %v, want %v", files, want)
	}
}

func TestModuleLicenseFilesFallsBackToNestedLicenses(t *testing.T) {
	directory := t.TempDir()
	for path, content := range map[string]string{
		"crypto/LICENSE": "crypto license",
		"poly/COPYING":   "poly license",
		"README.md":      "readme",
	} {
		fullPath := filepath.Join(directory, path)
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fullPath, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	files, err := moduleLicenseFiles(directory)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{filepath.Join("crypto", "LICENSE"), filepath.Join("poly", "COPYING")}
	if !reflect.DeepEqual(files, want) {
		t.Fatalf("module license files = %v, want %v", files, want)
	}
}

func TestWriteAtomicReplacesContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "output.txt")
	if err := writeAtomic(path, []byte("first"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeAtomic(path, []byte("second"), 0o644); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "second" {
		t.Fatalf("content = %q", content)
	}
}

func TestSplitBuildTags(t *testing.T) {
	got := splitBuildTags("with_utls,with_quic custom")
	want := []string{"with_utls", "with_quic", "custom"}
	if !reflect.DeepEqual(got, want) || !contains(got, "with_utls") || contains(got, "missing") {
		t.Fatalf("unexpected build tag parsing: %v", got)
	}
}

func TestFormatMachOVersion(t *testing.T) {
	for encoded, want := range map[uint32]string{
		0x000d0000: "13.0",
		0x001a0200: "26.2",
		0x000d0104: "13.1.4",
	} {
		if got := formatMachOVersion(encoded); got != want {
			t.Fatalf("formatMachOVersion(%#x) = %q, want %q", encoded, got, want)
		}
	}
}

func TestGenerateRequiresExplicitReleaseVersion(t *testing.T) {
	if err := generate(t.TempDir(), "v0.0.0-test", "with_utls,with_grpc", ""); err == nil || !strings.Contains(err.Error(), "version is required") {
		t.Fatalf("release generation accepted an empty version: %v", err)
	}
}

func TestGenerateRequiresExplicitCoreVersion(t *testing.T) {
	if err := generate(t.TempDir(), "", "with_utls,with_grpc", "0.1.0"); err == nil || !strings.Contains(err.Error(), "Core version is required") {
		t.Fatalf("release generation accepted an empty Core version: %v", err)
	}
}

func TestValidateCoreSource(t *testing.T) {
	valid := moduleDownload{
		Path:     mihomoModule,
		Version:  "v1.19.30",
		Sum:      "h1:module",
		GoModSum: "h1:gomod",
		Origin: &moduleOrigin{
			VCS:  "git",
			URL:  "https://github.com/metacubex/mihomo",
			Hash: strings.Repeat("a", 40),
			Ref:  "refs/tags/v1.19.30",
		},
	}
	if err := validateCoreSource(valid, "v1.19.30"); err != nil {
		t.Fatalf("valid Core source was rejected: %v", err)
	}

	tests := map[string]moduleDownload{
		"wrong version": func() moduleDownload { value := valid; value.Version = "v1.19.29"; return value }(),
		"missing sums":  func() moduleDownload { value := valid; value.Sum = ""; return value }(),
		"missing origin": func() moduleDownload {
			value := valid
			value.Origin = nil
			return value
		}(),
		"wrong origin": func() moduleDownload {
			value := valid
			origin := *valid.Origin
			origin.URL = "https://example.invalid/mihomo"
			value.Origin = &origin
			return value
		}(),
		"wrong ref": func() moduleDownload {
			value := valid
			origin := *valid.Origin
			origin.Ref = "refs/heads/main"
			value.Origin = &origin
			return value
		}(),
		"short hash": func() moduleDownload {
			value := valid
			origin := *valid.Origin
			origin.Hash = "abc123"
			value.Origin = &origin
			return value
		}(),
	}
	for name, download := range tests {
		t.Run(name, func(t *testing.T) {
			if err := validateCoreSource(download, "v1.19.30"); err == nil {
				t.Fatal("invalid Core source was accepted")
			}
		})
	}
}

func TestGoToolchainLicenseFilesSupportSplitInstallationLayout(t *testing.T) {
	prefix := t.TempDir()
	goRoot := filepath.Join(prefix, "libexec")
	if err := os.MkdirAll(goRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	license := filepath.Join(prefix, "LICENSE")
	patents := filepath.Join(goRoot, "PATENTS")
	if err := os.WriteFile(license, []byte("license"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(patents, []byte("patents"), 0o644); err != nil {
		t.Fatal(err)
	}
	files, err := goToolchainLicenseFiles(goRoot)
	if err != nil {
		t.Fatal(err)
	}
	license, _ = filepath.EvalSymlinks(license)
	patents, _ = filepath.EvalSymlinks(patents)
	if len(files) != 2 || !contains(files, license) || !contains(files, patents) {
		t.Fatalf("split Go license layout was not collected: %+v", files)
	}
}
