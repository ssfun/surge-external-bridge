package main

import (
	"bytes"
	"crypto/sha256"
	"debug/buildinfo"
	"debug/elf"
	"debug/macho"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

const (
	mihomoModule    = "github.com/metacubex/mihomo"
	minimumMacOS    = "13.0"
	lcBuildVersion  = uint32(0x32)
	macOSPlatformID = uint32(1)
)

type target struct {
	Filename string
	GOOS     string
	GOARCH   string
}

var releaseTargets = []target{
	{Filename: "vless2surge-darwin-arm64", GOOS: "darwin", GOARCH: "arm64"},
	{Filename: "vless2surge-darwin-amd64", GOOS: "darwin", GOARCH: "amd64"},
	{Filename: "vless2surge-linux-arm64", GOOS: "linux", GOARCH: "arm64"},
	{Filename: "vless2surge-linux-amd64", GOOS: "linux", GOARCH: "amd64"},
}

type module struct {
	Path    string
	Version string
	Dir     string
	Main    bool
}

type listedPackage struct {
	Module *module
}

type moduleOrigin struct {
	VCS  string
	URL  string
	Hash string
	Ref  string
}

type moduleDownload struct {
	Path     string
	Version  string
	Sum      string
	GoModSum string
	Origin   *moduleOrigin
}

func main() {
	dist := flag.String("dist", "dist", "directory containing release binaries")
	coreVersion := flag.String("core-version", "", "expected Mihomo module version")
	buildTags := flag.String("build-tags", "", "required Go build tags")
	version := flag.String("version", "", "expected vless2surge release version")
	flag.Parse()
	if err := generate(*dist, *coreVersion, *buildTags, *version); err != nil {
		fmt.Fprintln(os.Stderr, "releasegen:", err)
		os.Exit(1)
	}
}

func generate(dist, expectedCore, requiredTags, version string) error {
	if strings.TrimSpace(version) == "" {
		return errors.New("release version is required")
	}
	if strings.TrimSpace(expectedCore) == "" {
		return errors.New("Mihomo Core version is required")
	}
	dist, err := filepath.Abs(dist)
	if err != nil {
		return err
	}
	coreSource, err := resolveCoreSource(expectedCore)
	if err != nil {
		return err
	}
	checksums, buildDetails, err := inspectBinaries(dist, expectedCore, requiredTags, version, coreSource)
	if err != nil {
		return err
	}
	modules, err := linkedModules(requiredTags)
	if err != nil {
		return err
	}
	notices, err := renderNotices(modules)
	if err != nil {
		return err
	}
	if err := writeAtomic(filepath.Join(dist, "SHA256SUMS"), checksums, 0o644); err != nil {
		return err
	}
	if err := writeAtomic(filepath.Join(dist, "BUILDINFO.txt"), buildDetails, 0o644); err != nil {
		return err
	}
	return writeAtomic(filepath.Join(dist, "THIRD_PARTY_NOTICES.txt"), notices, 0o644)
}

func inspectBinaries(dist, expectedCore, requiredTags, version string, coreSource moduleDownload) ([]byte, []byte, error) {
	var sums bytes.Buffer
	var details bytes.Buffer
	details.WriteString("vless2surge release build information\n")
	details.WriteString("Generated from Go build metadata embedded in each binary.\n\n")
	fmt.Fprintf(&details, "release version: %s\n", version)
	fmt.Fprintf(&details, "Mihomo module: %s %s\n", coreSource.Path, coreSource.Version)
	fmt.Fprintf(&details, "Mihomo source: %s\n", coreSource.Origin.URL)
	fmt.Fprintf(&details, "Mihomo source ref: %s\n", coreSource.Origin.Ref)
	fmt.Fprintf(&details, "Mihomo source commit: %s\n", coreSource.Origin.Hash)
	fmt.Fprintf(&details, "Mihomo module sum: %s\n", coreSource.Sum)
	fmt.Fprintf(&details, "Mihomo go.mod sum: %s\n\n", coreSource.GoModSum)
	for _, expected := range releaseTargets {
		path := filepath.Join(dist, expected.Filename)
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, nil, fmt.Errorf("read %s: %w", expected.Filename, err)
		}
		info, err := buildinfo.ReadFile(path)
		if err != nil {
			return nil, nil, fmt.Errorf("read build information from %s: %w", expected.Filename, err)
		}
		settings := make(map[string]string, len(info.Settings))
		for _, setting := range info.Settings {
			settings[setting.Key] = setting.Value
		}
		if settings["GOOS"] != expected.GOOS || settings["GOARCH"] != expected.GOARCH {
			return nil, nil, fmt.Errorf("%s target is %s/%s, expected %s/%s", expected.Filename, settings["GOOS"], settings["GOARCH"], expected.GOOS, expected.GOARCH)
		}
		if settings["CGO_ENABLED"] != "0" {
			return nil, nil, fmt.Errorf("%s was not built with CGO_ENABLED=0", expected.Filename)
		}
		if !bytes.Contains(data, []byte("vless2surge-version:"+version)) {
			return nil, nil, fmt.Errorf("%s does not embed release version %q", expected.Filename, version)
		}
		for _, required := range splitBuildTags(requiredTags) {
			if !contains(splitBuildTags(settings["-tags"]), required) {
				return nil, nil, fmt.Errorf("%s is missing required build tag %q (embedded tags: %q)", expected.Filename, required, settings["-tags"])
			}
		}
		coreVersion := ""
		for _, dependency := range info.Deps {
			if dependency.Path == mihomoModule {
				if dependency.Replace != nil {
					return nil, nil, fmt.Errorf("%s embeds a replaced Mihomo module", expected.Filename)
				}
				coreVersion = dependency.Version
				break
			}
		}
		if coreVersion != expectedCore {
			return nil, nil, fmt.Errorf("%s embeds Mihomo %q, expected %q", expected.Filename, coreVersion, expectedCore)
		}
		sum := sha256.Sum256(data)
		fmt.Fprintf(&sums, "%s  %s\n", hex.EncodeToString(sum[:]), expected.Filename)
		fmt.Fprintf(&details, "%s\n", expected.Filename)
		fmt.Fprintf(&details, "  target: %s/%s\n", expected.GOOS, expected.GOARCH)
		fmt.Fprintf(&details, "  go: %s\n", info.GoVersion)
		fmt.Fprintf(&details, "  cgo: disabled\n")
		fmt.Fprintf(&details, "  build tags: %s\n", settings["-tags"])
		fmt.Fprintf(&details, "  embedded core: %s %s\n", mihomoModule, coreVersion)
		if expected.GOOS == "darwin" {
			minOS, sdk, err := machoBuildVersions(path)
			if err != nil {
				return nil, nil, fmt.Errorf("inspect %s platform version: %w", expected.Filename, err)
			}
			if minOS != minimumMacOS {
				return nil, nil, fmt.Errorf("%s minimum macOS is %s, expected %s", expected.Filename, minOS, minimumMacOS)
			}
			fmt.Fprintf(&details, "  minimum macOS: %s\n", minOS)
			fmt.Fprintf(&details, "  macOS SDK: %s\n", sdk)
		} else if expected.GOOS == "linux" {
			if err := validateStaticELF(path); err != nil {
				return nil, nil, fmt.Errorf("inspect %s linkage: %w", expected.Filename, err)
			}
			fmt.Fprintf(&details, "  linkage: static\n")
		}
		fmt.Fprintf(&details, "  bytes: %d\n\n", len(data))
	}
	return sums.Bytes(), details.Bytes(), nil
}

func resolveCoreSource(expectedVersion string) (moduleDownload, error) {
	command := exec.Command("go", "mod", "download", "-json", mihomoModule+"@"+expectedVersion)
	output, err := command.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return moduleDownload{}, fmt.Errorf("resolve Mihomo source: %w: %s", err, strings.TrimSpace(string(exitErr.Stderr)))
		}
		return moduleDownload{}, fmt.Errorf("resolve Mihomo source: %w", err)
	}
	var download moduleDownload
	if err := json.Unmarshal(output, &download); err != nil {
		return moduleDownload{}, fmt.Errorf("decode Mihomo source metadata: %w", err)
	}
	if err := validateCoreSource(download, expectedVersion); err != nil {
		return moduleDownload{}, err
	}
	return download, nil
}

func validateCoreSource(download moduleDownload, expectedVersion string) error {
	if download.Path != mihomoModule || download.Version != expectedVersion {
		return fmt.Errorf("Mihomo source metadata is %s@%s, expected %s@%s", download.Path, download.Version, mihomoModule, expectedVersion)
	}
	if download.Sum == "" || download.GoModSum == "" {
		return errors.New("Mihomo source metadata is missing module checksums")
	}
	if download.Origin == nil {
		return errors.New("Mihomo source metadata is missing VCS origin")
	}
	if download.Origin.VCS != "git" || download.Origin.URL != "https://github.com/metacubex/mihomo" {
		return fmt.Errorf("unexpected Mihomo source origin: %s %s", download.Origin.VCS, download.Origin.URL)
	}
	if download.Origin.Ref != "refs/tags/"+expectedVersion {
		return fmt.Errorf("Mihomo source ref is %q, expected refs/tags/%s", download.Origin.Ref, expectedVersion)
	}
	if !isLowerHexCommit(download.Origin.Hash) {
		return fmt.Errorf("Mihomo source commit is not a full lowercase Git hash: %q", download.Origin.Hash)
	}
	return nil
}

func isLowerHexCommit(value string) bool {
	if len(value) != 40 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func machoBuildVersions(path string) (string, string, error) {
	file, err := macho.Open(path)
	if err != nil {
		return "", "", err
	}
	defer file.Close()
	for _, load := range file.Loads {
		raw := load.Raw()
		if len(raw) < 24 || file.ByteOrder.Uint32(raw[:4]) != lcBuildVersion {
			continue
		}
		if platform := file.ByteOrder.Uint32(raw[8:12]); platform != macOSPlatformID {
			return "", "", fmt.Errorf("LC_BUILD_VERSION platform is %d, expected macOS", platform)
		}
		return formatMachOVersion(file.ByteOrder.Uint32(raw[12:16])), formatMachOVersion(file.ByteOrder.Uint32(raw[16:20])), nil
	}
	return "", "", errors.New("LC_BUILD_VERSION is missing")
}
func formatMachOVersion(version uint32) string {
	major := version >> 16
	minor := (version >> 8) & 0xff
	patch := version & 0xff
	if patch == 0 {
		return fmt.Sprintf("%d.%d", major, minor)
	}
	return fmt.Sprintf("%d.%d.%d", major, minor, patch)
}

func validateStaticELF(path string) error {
	file, err := elf.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	for _, program := range file.Progs {
		if program.Type == elf.PT_INTERP {
			return errors.New("ELF contains a dynamic interpreter")
		}
	}
	libraries, err := file.ImportedLibraries()
	if err != nil {
		return err
	}
	if len(libraries) > 0 {
		return fmt.Errorf("ELF imports dynamic libraries: %s", strings.Join(libraries, ", "))
	}
	return nil
}

func linkedModules(buildTags string) ([]module, error) {
	unique := map[string]module{}
	for _, buildTarget := range releaseTargets {
		command := exec.Command("go", "list", "-tags", buildTags, "-deps", "-json", "./cmd/vless2surge")
		command.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS="+buildTarget.GOOS, "GOARCH="+buildTarget.GOARCH)
		output, err := command.Output()
		if err != nil {
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				return nil, fmt.Errorf("list modules for %s/%s: %w: %s", buildTarget.GOOS, buildTarget.GOARCH, err, strings.TrimSpace(string(exitErr.Stderr)))
			}
			return nil, fmt.Errorf("list modules for %s/%s: %w", buildTarget.GOOS, buildTarget.GOARCH, err)
		}
		decoder := json.NewDecoder(bytes.NewReader(output))
		for {
			var pkg listedPackage
			if err := decoder.Decode(&pkg); errors.Is(err, io.EOF) {
				break
			} else if err != nil {
				return nil, fmt.Errorf("decode go list output for %s/%s: %w", buildTarget.GOOS, buildTarget.GOARCH, err)
			}
			if pkg.Module == nil || pkg.Module.Main {
				continue
			}
			if pkg.Module.Path == "" || pkg.Module.Version == "" || pkg.Module.Dir == "" {
				return nil, fmt.Errorf("incomplete module metadata for %s/%s", buildTarget.GOOS, buildTarget.GOARCH)
			}
			unique[pkg.Module.Path+"@"+pkg.Module.Version] = *pkg.Module
		}
	}
	result := make([]module, 0, len(unique))
	for _, item := range unique {
		result = append(result, item)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Path == result[j].Path {
			return result[i].Version < result[j].Version
		}
		return result[i].Path < result[j].Path
	})
	return result, nil
}

func splitBuildTags(value string) []string {
	return strings.FieldsFunc(value, func(character rune) bool {
		return character == ',' || character == ' ' || character == '\t'
	})
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func renderNotices(modules []module) ([]byte, error) {
	var output bytes.Buffer
	output.WriteString("vless2surge third-party notices\n")
	output.WriteString("Targets: darwin/arm64, darwin/amd64, linux/arm64, linux/amd64\n")
	output.WriteString("This inventory contains the Go toolchain license and the license and notice files supplied by each linked Go module.\n")
	output.WriteString("vless2surge is independent from the Mihomo project and does not imply association or endorsement.\n\n")
	toolchainFiles, err := goToolchainLicenseFiles(runtime.GOROOT())
	if err != nil {
		return nil, err
	}
	for _, path := range toolchainFiles {
		content, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		output.WriteString(strings.Repeat("=", 80) + "\n")
		fmt.Fprintf(&output, "Go toolchain %s\nSource file: %s\n", runtime.Version(), filepath.Base(path))
		output.WriteString(strings.Repeat("=", 80) + "\n")
		output.Write(bytes.TrimSpace(content))
		output.WriteString("\n\n")
	}
	for _, item := range modules {
		files, err := moduleLicenseFiles(item.Dir)
		if err != nil {
			return nil, fmt.Errorf("licenses for %s@%s: %w", item.Path, item.Version, err)
		}
		if len(files) == 0 {
			return nil, fmt.Errorf("module %s@%s has no license or notice file", item.Path, item.Version)
		}
		for _, filename := range files {
			content, err := os.ReadFile(filepath.Join(item.Dir, filename))
			if err != nil {
				return nil, err
			}
			output.WriteString(strings.Repeat("=", 80) + "\n")
			fmt.Fprintf(&output, "%s %s\nSource file: %s\n", item.Path, item.Version, filename)
			output.WriteString(strings.Repeat("=", 80) + "\n")
			output.Write(bytes.TrimSpace(content))
			output.WriteString("\n\n")
		}
	}
	return output.Bytes(), nil
}

func goToolchainLicenseFiles(goRoot string) ([]string, error) {
	if goRoot == "" {
		return nil, errors.New("Go runtime did not report GOROOT")
	}
	candidates := []string{
		filepath.Join(goRoot, "LICENSE"),
		filepath.Join(goRoot, "PATENTS"),
		filepath.Join(filepath.Dir(goRoot), "LICENSE"),
		filepath.Join(filepath.Dir(goRoot), "PATENTS"),
	}
	seen := map[string]bool{}
	var result []string
	for _, candidate := range candidates {
		resolved, err := filepath.EvalSymlinks(candidate)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, err
		}
		if seen[resolved] {
			continue
		}
		info, err := os.Stat(resolved)
		if err != nil {
			return nil, err
		}
		if info.Mode().IsRegular() {
			seen[resolved] = true
			result = append(result, resolved)
		}
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("Go toolchain license was not found under %s or its parent", goRoot)
	}
	sort.Strings(result)
	return result, nil
}

func licenseFiles(directory string) ([]string, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, err
	}
	var result []string
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			continue
		}
		if isLicenseFilename(entry.Name()) {
			result = append(result, entry.Name())
		}
	}
	sort.Strings(result)
	return result, nil
}

func moduleLicenseFiles(directory string) ([]string, error) {
	root, err := licenseFiles(directory)
	if err != nil || len(root) > 0 {
		return root, err
	}
	var result []string
	err = filepath.WalkDir(directory, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == directory || entry.IsDir() || !entry.Type().IsRegular() || !isLicenseFilename(entry.Name()) {
			return nil
		}
		relative, err := filepath.Rel(directory, path)
		if err != nil {
			return err
		}
		result = append(result, relative)
		return nil
	})
	sort.Strings(result)
	return result, err
}

func isLicenseFilename(filename string) bool {
	name := strings.ToLower(filename)
	return strings.HasPrefix(name, "license") || strings.HasPrefix(name, "copying") || strings.HasPrefix(name, "notice") || strings.HasPrefix(name, "patents")
}

func writeAtomic(path string, content []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".releasegen-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(mode); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(content); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}
