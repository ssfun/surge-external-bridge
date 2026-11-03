// Package update implements whole-product updates from the project's releases.
package update

import (
	"context"
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/mod/semver"
)

const Repository = "ssfun/surge-external-bridge"
const releaseAPI = "https://api.github.com/repos/" + Repository + "/releases?per_page=100"
const maxBinary = 256 << 20

type Asset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
	Size int64  `json:"size"`
}
type Release struct {
	Tag        string  `json:"tag_name"`
	URL        string  `json:"html_url"`
	Notes      string  `json:"body"`
	Draft      bool    `json:"draft"`
	Prerelease bool    `json:"prerelease"`
	Assets     []Asset `json:"assets"`
}
type Client struct {
	HTTP            *http.Client
	allowTestHTTP   bool
	proxyConfigured bool
}

func NewClient() *Client {
	client, _ := NewClientWithProxy("")
	return client
}

// NewClientWithProxy uses only the explicit update proxy; an empty URL is DIRECT.
func NewClientWithProxy(address string) (*Client, error) {
	var proxyURL *url.URL
	if address != "" {
		invalid := errors.New("更新代理地址无效：请填写 http、https、socks5 或 socks5h 的主机和端口")
		u, err := url.Parse(address)
		if err != nil {
			return nil, invalid
		}
		if u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "socks5" && u.Scheme != "socks5h" {
			return nil, invalid
		}
		host, port, err := net.SplitHostPort(u.Host)
		n, portErr := strconv.Atoi(port)
		if err != nil || host == "" || portErr != nil || n < 1 || n > 65535 || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" || strings.ContainsAny(host, " \t\r\n") || strings.Contains(address, "#") {
			return nil, invalid
		}
		proxyURL = u
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// Never implicitly inherit a system proxy or the embedded proxy being updated.
	transport.Proxy = nil
	if proxyURL != nil {
		transport.Proxy = http.ProxyURL(proxyURL)
	}
	return &Client{proxyConfigured: proxyURL != nil, HTTP: &http.Client{Transport: transport, Timeout: 5 * time.Minute, CheckRedirect: func(r *http.Request, via []*http.Request) error {
		if len(via) >= 8 {
			return errors.New("too many release redirects")
		}
		host := r.URL.Hostname()
		if r.URL.Scheme != "https" || (host != "github.com" && host != "api.github.com" && host != "release-assets.githubusercontent.com" && host != "objects.githubusercontent.com") {
			return errors.New("untrusted release redirect")
		}
		return nil
	}}}, nil
}
func normalizedVersion(v string) string {
	if !strings.HasPrefix(v, "v") {
		v = "v" + v
	}
	return v
}
func StableVersion(v string) bool {
	v = normalizedVersion(v)
	return semver.IsValid(v) && semver.Canonical(v) == v && semver.Prerelease(v) == ""
}
func selectRelease(releases []Release, current string) (*Release, error) {
	if !StableVersion(current) {
		return nil, errors.New("当前为开发或未知版本，请先手动安装正式 Release")
	}
	var best *Release
	for _, r := range releases {
		if r.Draft || r.Prerelease || !StableVersion(r.Tag) || semver.Compare(normalizedVersion(r.Tag), normalizedVersion(current)) <= 0 {
			continue
		}
		if best == nil || semver.Compare(normalizedVersion(r.Tag), normalizedVersion(best.Tag)) > 0 {
			copy := r
			best = &copy
		}
	}
	return best, nil
}
func (c *Client) request(ctx context.Context, address string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "SurgeEB-Updater")
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		if c.proxyConfigured {
			return nil, errors.New("通过更新代理请求 GitHub 失败，请检查代理地址、认证和网络连接")
		}
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("GitHub 更新请求失败：HTTP %d", resp.StatusCode)
	}
	return resp, nil
}
func (c *Client) Check(ctx context.Context, current string) (*Release, error) {
	if !StableVersion(current) {
		return nil, errors.New("当前为开发或未知版本，请先手动安装正式 Release")
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	resp, err := c.request(ctx, releaseAPI)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 4<<20 {
		return nil, errors.New("release metadata too large")
	}
	var releases []Release
	if err = json.Unmarshal(data, &releases); err != nil {
		return nil, err
	}
	selected, err := selectRelease(releases, current)
	if selected != nil {
		selected.URL = "https://github.com/" + Repository + "/releases/tag/" + url.PathEscape(selected.Tag)
	}
	return selected, err
}
func validAssetURL(address string) bool {
	u, err := url.Parse(address)
	return err == nil && u.Scheme == "https" && u.Host == "github.com" && u.User == nil && strings.HasPrefix(u.Path, "/"+Repository+"/releases/download/") && u.RawQuery == "" && u.Fragment == ""
}
func (c *Client) asset(r Release, name string) (Asset, error) {
	var matches []Asset
	for _, a := range r.Assets {
		if a.Name == name {
			matches = append(matches, a)
		}
	}
	if len(matches) != 1 {
		return Asset{}, fmt.Errorf("Release 必须包含唯一的 %s", name)
	}
	a := matches[0]
	if !c.allowTestHTTP && (!validAssetURL(a.URL) || !strings.HasPrefix(a.URL, "https://github.com/"+Repository+"/releases/download/"+url.PathEscape(r.Tag)+"/")) {
		return Asset{}, errors.New("release asset is outside the selected project release")
	}
	return a, nil
}
func checksum(data []byte, name string) (string, error) {
	found := ""
	for _, line := range strings.Split(string(data), "\n") {
		parts := strings.Fields(line)
		if len(parts) != 2 || strings.TrimPrefix(parts[1], "*") != name {
			continue
		}
		decoded, err := hex.DecodeString(parts[0])
		if err != nil || len(decoded) != 32 || found != "" {
			return "", errors.New("invalid or duplicate release checksum")
		}
		found = strings.ToLower(parts[0])
	}
	if found == "" {
		return "", errors.New("release checksum missing")
	}
	return found, nil
}
func (c *Client) Download(ctx context.Context, r Release, goos, arch, target string) (path, digest string, err error) {
	if (goos != "darwin" && goos != "linux") || (arch != "arm64" && arch != "amd64") {
		return "", "", errors.New("unsupported update platform")
	}
	name := "SurgeEB-" + goos + "-" + arch
	binary, err := c.asset(r, name)
	if err != nil {
		return "", "", err
	}
	sums, err := c.asset(r, "SHA256SUMS")
	if err != nil {
		return "", "", err
	}
	resp, err := c.request(ctx, sums.URL)
	if err != nil {
		return "", "", err
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10+1))
	resp.Body.Close()
	if err != nil {
		return "", "", err
	}
	if len(data) > 64<<10 {
		return "", "", errors.New("checksum file too large")
	}
	digest, err = checksum(data, name)
	if err != nil {
		return "", "", err
	}
	if binary.Size <= 0 || binary.Size > maxBinary {
		return "", "", errors.New("invalid release binary size")
	}
	resp, err = c.request(ctx, binary.URL)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	f, err := os.CreateTemp(directory(target), ".SurgeEB-download-*")
	if err != nil {
		return "", "", fmt.Errorf("更新目录不可写：%w", err)
	}
	path = f.Name()
	success := false
	defer func() {
		f.Close()
		if !success {
			os.Remove(f.Name())
		}
	}()
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, hash), io.LimitReader(resp.Body, maxBinary+1))
	if err != nil {
		return "", "", err
	}
	if n != binary.Size || hex.EncodeToString(hash.Sum(nil)) != digest {
		return "", "", errors.New("Release 大小或 SHA256 校验失败")
	}
	if err = f.Chmod(0o755); err == nil {
		err = f.Sync()
	}
	if err == nil {
		err = f.Close()
	}
	if err != nil {
		return "", "", err
	}
	success = true
	return path, digest, nil
}
func ValidateBinary(path, version, goos, arch string) error {
	info, err := buildinfo.ReadFile(path)
	if err != nil {
		return fmt.Errorf("invalid Go executable: %w", err)
	}
	if info.Path != "github.com/"+Repository+"/cmd/surgeeb" {
		return errors.New("not a SurgeEB executable")
	}
	settings := map[string]string{}
	for _, s := range info.Settings {
		settings[s.Key] = s.Value
	}
	if settings["GOOS"] != goos || settings["GOARCH"] != arch || settings["CGO_ENABLED"] != "0" {
		return errors.New("release architecture or static-build metadata mismatch")
	}
	// Run only after the fixed-source download's digest and executable format were verified.
	if err := checkProtocol(path); err != nil {
		return err
	}
	got, err := binaryVersion(path)
	if err != nil {
		return err
	}
	if normalizedVersion(got) != normalizedVersion(version) {
		return errors.New("binary version differs from release tag")
	}
	return nil
}
