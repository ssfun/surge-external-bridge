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
