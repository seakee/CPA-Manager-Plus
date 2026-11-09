package update

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

type doerFunc func(*http.Request) (*http.Response, error)

func (f doerFunc) Do(request *http.Request) (*http.Response, error) { return f(request) }

func TestValidateVersionRequiresExactCanonicalRelease(t *testing.T) {
	for _, version := range []string{
		"0.0.0", "7.3.3", "7.4.0-rc.1", "7.4.0-beta+build.2",
		"7.4.0-alpha-beta", "7.4.0-alpha-beta.1", "7.4.0-x-y-z+build-1",
	} {
		if err := ValidateVersion(version); err != nil {
			t.Fatalf("ValidateVersion(%q) = %v", version, err)
		}
	}
	for _, version := range []string{
		"", "latest", "v7.3.3", " 7.3.3", "7.3.3 ", "07.3.3", "7.03.3", "7.3.03",
		"7.3", "7.3.3-01", "7.3.3/asset", "../7.3.3", "https://example.test/7.3.3",
		"7.3.3-", "7.3.3-alpha.", "7.3.3-alpha..1",
	} {
		if err := ValidateVersion(version); err == nil {
			t.Fatalf("ValidateVersion(%q) error = nil", version)
		}
	}
}

func TestSourceResolvesOnlyExactOfficialReleaseAsset(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	source, err := newSource("linux", "amd64", doerFunc(func(request *http.Request) (*http.Response, error) {
		wantURL := "https://api.github.com/repos/router-for-me/CLIProxyAPI/releases/tags/v7.3.3"
		if request.Method != http.MethodGet || request.URL.String() != wantURL {
			t.Fatalf("metadata request = %s %s", request.Method, request.URL)
		}
		if request.Header.Get("Accept") != metadataAccept || request.Header.Get("User-Agent") != userAgent ||
			request.Header.Get("Authorization") != "" || request.Header.Get("Cookie") != "" {
			t.Fatalf("metadata headers = %#v", request.Header)
		}
		body := fmt.Sprintf(`{"tag_name":"v7.3.3","draft":false,"prerelease":true,"assets":[`+
			`{"name":"CLIProxyAPI_7.3.3_linux_amd64_no-plugin.tar.gz","digest":%q,"size":10,"browser_download_url":"https://github.com/router-for-me/CLIProxyAPI/releases/download/v7.3.3/CLIProxyAPI_7.3.3_linux_amd64_no-plugin.tar.gz"},`+
			`{"name":"CLIProxyAPI_7.3.3_linux_amd64.tar.gz","digest":%q,"size":123,"browser_download_url":"https://github.com/router-for-me/CLIProxyAPI/releases/download/v7.3.3/CLIProxyAPI_7.3.3_linux_amd64.tar.gz"}]}`, digest, digest)
		return responseFor(request, http.StatusOK, body), nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	release, err := source.Resolve(t.Context(), "7.3.3")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if release.AssetName != "CLIProxyAPI_7.3.3_linux_amd64.tar.gz" || release.ArchiveDigest != digest || release.ArchiveSize != 123 ||
		release.AssetURL != "https://github.com/router-for-me/CLIProxyAPI/releases/download/v7.3.3/CLIProxyAPI_7.3.3_linux_amd64.tar.gz" {
		t.Fatalf("Resolve() = %#v", release)
	}
}

func TestSourcePlatformMappingAndNoFallback(t *testing.T) {
	for _, test := range []struct {
		goos, goarch, suffix string
		ok                   bool
	}{
		{goos: "linux", goarch: "amd64", suffix: "linux_amd64.tar.gz", ok: true},
		{goos: "linux", goarch: "arm64", suffix: "linux_aarch64.tar.gz", ok: true},
		{goos: "linux", goarch: "386"},
		{goos: "windows", goarch: "amd64"},
	} {
		t.Run(test.goos+"-"+test.goarch, func(t *testing.T) {
			source, err := newSource(test.goos, test.goarch, doerFunc(nil))
			if test.ok {
				if err != nil || source.asset != test.suffix || !SupportedPlatform(test.goos, test.goarch) {
					t.Fatalf("newSource = %#v, %v", source, err)
				}
				return
			}
			if !errorsIs(err, ErrUnsupportedPlatform) || SupportedPlatform(test.goos, test.goarch) {
				t.Fatalf("unsupported newSource error = %v", err)
			}
		})
	}

	source, err := newSource("linux", "amd64", doerFunc(func(request *http.Request) (*http.Response, error) {
		body := `{"tag_name":"v7.3.3","draft":false,"assets":[{"name":"CLIProxyAPI_7.3.3_linux_amd64_no-plugin.tar.gz","digest":"sha256:` + strings.Repeat("a", 64) + `","size":1,"browser_download_url":"https://github.com/router-for-me/CLIProxyAPI/releases/download/v7.3.3/CLIProxyAPI_7.3.3_linux_amd64_no-plugin.tar.gz"}]}`
		return responseFor(request, http.StatusOK, body), nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.Resolve(t.Context(), "7.3.3"); !errorsIs(err, ErrReleaseMetadata) {
		t.Fatalf("no-plugin-only Resolve() error = %v", err)
	}
}

func TestSourceRejectsInvalidReleaseMetadata(t *testing.T) {
	validDigest := "sha256:" + strings.Repeat("a", 64)
	validURL := "https://github.com/router-for-me/CLIProxyAPI/releases/download/v7.3.3/CLIProxyAPI_7.3.3_linux_amd64.tar.gz"
	asset := func(digest, downloadURL string, size int64) string {
		return fmt.Sprintf(`{"name":"CLIProxyAPI_7.3.3_linux_amd64.tar.gz","digest":%q,"size":%d,"browser_download_url":%q}`, digest, size, downloadURL)
	}
	tests := map[string]string{
		"draft":          `{"tag_name":"v7.3.3","draft":true,"assets":[]}`,
		"wrong tag":      `{"tag_name":"v7.3.4","draft":false,"assets":[]}`,
		"missing digest": `{"tag_name":"v7.3.3","draft":false,"assets":[` + asset("", validURL, 1) + `]}`,
		"upper digest":   `{"tag_name":"v7.3.3","draft":false,"assets":[` + asset("sha256:"+strings.Repeat("A", 64), validURL, 1) + `]}`,
		"wrong URL":      `{"tag_name":"v7.3.3","draft":false,"assets":[` + asset(validDigest, "https://example.test/asset", 1) + `]}`,
		"zero size":      `{"tag_name":"v7.3.3","draft":false,"assets":[` + asset(validDigest, validURL, 0) + `]}`,
		"duplicate":      `{"tag_name":"v7.3.3","draft":false,"assets":[` + asset(validDigest, validURL, 1) + `,` + asset(validDigest, validURL, 1) + `]}`,
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			source, err := newSource("linux", "amd64", doerFunc(func(request *http.Request) (*http.Response, error) {
				return responseFor(request, http.StatusOK, body), nil
			}))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := source.Resolve(t.Context(), "7.3.3"); !errorsIs(err, ErrReleaseMetadata) {
				t.Fatalf("Resolve() error = %v", err)
			}
		})
	}
}

func TestSourceBoundsMetadataAndArchiveAndVerifiesDigest(t *testing.T) {
	archive := []byte("verified archive")
	digest := sha256.Sum256(archive)
	release := Release{
		Version:       "7.3.3",
		AssetName:     "CLIProxyAPI_7.3.3_linux_amd64.tar.gz",
		AssetURL:      "https://github.com/router-for-me/CLIProxyAPI/releases/download/v7.3.3/CLIProxyAPI_7.3.3_linux_amd64.tar.gz",
		ArchiveDigest: "sha256:" + hex.EncodeToString(digest[:]),
		ArchiveSize:   int64(len(archive)),
	}
	var requests int
	source, err := newSource("linux", "amd64", doerFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		if request.URL.String() != release.AssetURL || request.Header.Get("Accept") != assetAccept ||
			request.Header.Get("Authorization") != "" || request.Header.Get("Cookie") != "" {
			t.Fatalf("asset request = %s %#v", request.URL, request.Header)
		}
		response := responseFor(request, http.StatusOK, string(archive))
		response.ContentLength = int64(len(archive))
		return response, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	var destination bytes.Buffer
	if err := source.Download(t.Context(), release, &destination); err != nil || !bytes.Equal(destination.Bytes(), archive) || requests != 1 {
		t.Fatalf("Download() bytes=%q requests=%d error=%v", destination.Bytes(), requests, err)
	}

	badDigest := release
	badDigest.ArchiveDigest = "sha256:" + strings.Repeat("0", 64)
	if err := source.Download(t.Context(), badDigest, io.Discard); !errorsIs(err, ErrArchiveInvalid) {
		t.Fatalf("digest mismatch error = %v", err)
	}
	badSize := release
	badSize.ArchiveSize++
	if err := source.Download(t.Context(), badSize, io.Discard); !errorsIs(err, ErrArchiveInvalid) {
		t.Fatalf("size mismatch error = %v", err)
	}

	oversize, err := newSource("linux", "amd64", doerFunc(func(request *http.Request) (*http.Response, error) {
		response := responseFor(request, http.StatusOK, "{}")
		response.ContentLength = maxReleaseMetadataBytes + 1
		return response, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := oversize.Resolve(t.Context(), "7.3.3"); !errorsIs(err, ErrReleaseMetadata) {
		t.Fatalf("oversize metadata error = %v", err)
	}
}

func TestProductionHTTPClientIgnoresProxyEnvironmentAndRejectsUnsafeRedirects(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	client := newHTTPClient()
	if ReleaseClientTimeout != 5*time.Minute || client.Timeout != ReleaseClientTimeout {
		t.Fatalf("release client timeout = %s, want bounded 5m", client.Timeout)
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok || transport.Proxy != nil {
		t.Fatalf("production transport proxy = %#v", client.Transport)
	}
	initial, _ := http.NewRequest(http.MethodGet, "https://github.com/router-for-me/CLIProxyAPI/releases/download/v7.3.3/a", nil)
	for _, target := range []string{"http://release-assets.githubusercontent.com/a", "https://example.test/a"} {
		redirect, _ := http.NewRequest(http.MethodGet, target, nil)
		if err := client.CheckRedirect(redirect, []*http.Request{initial}); err == nil {
			t.Fatalf("unsafe redirect %s accepted", target)
		}
	}
	metadata, _ := http.NewRequest(http.MethodGet, "https://api.github.com/repos/router-for-me/CLIProxyAPI/releases/tags/v7.3.3", nil)
	redirect, _ := http.NewRequest(http.MethodGet, "https://github.com/somewhere", nil)
	if err := client.CheckRedirect(redirect, []*http.Request{metadata}); err == nil {
		t.Fatal("metadata redirect accepted")
	}
	safe, _ := http.NewRequest(http.MethodGet, "https://release-assets.githubusercontent.com/asset", nil)
	if err := client.CheckRedirect(safe, []*http.Request{initial}); err != nil {
		t.Fatalf("safe release redirect rejected: %v", err)
	}
}

func TestAcceptedV733GitHubDigestsMatchPinnedDockerArchives(t *testing.T) {
	fixtures := map[string]string{
		"CLIProxyAPI_7.3.3_linux_amd64.tar.gz":   "sha256:7af8c99cd08eee3ccc81d1596e8a31785674d3de6bd7ec61416d59493dd8fc01",
		"CLIProxyAPI_7.3.3_linux_aarch64.tar.gz": "sha256:5f320e3fae52af00f07b78201311e9d096b36e759441d948de48a10f49e71883",
	}
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller did not identify the release test source")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", "..", "..", ".."))
	dockerfile, err := os.ReadFile(filepath.Join(repositoryRoot, "Dockerfile"))
	if err != nil {
		t.Fatalf("read repository Dockerfile: %v", err)
	}
	dockerfileText := string(dockerfile)
	version := regexp.MustCompile(`cpa_version="([^"]+)"`).FindStringSubmatch(dockerfileText)
	if len(version) != 2 || version[1] != "7.3.3" {
		t.Fatalf("Dockerfile.runtime CPA version = %v, want 7.3.3", version)
	}
	readPin := func(name, pattern string) string {
		match := regexp.MustCompile(pattern).FindStringSubmatch(dockerfileText)
		if len(match) != 2 {
			t.Fatalf("Dockerfile.runtime %s archive pin is missing or malformed", name)
		}
		return match[1]
	}
	pins := map[string]string{
		"CLIProxyAPI_7.3.3_linux_amd64.tar.gz":   readPin("amd64", `amd64\)\s*asset_arch="amd64";\s*asset_sha256="([0-9a-f]{64})"`),
		"CLIProxyAPI_7.3.3_linux_aarch64.tar.gz": readPin("arm64", `arm64\)\s*asset_arch="aarch64";\s*asset_sha256="([0-9a-f]{64})"`),
	}
	for name, digest := range fixtures {
		if !canonicalDigest(digest) || !strings.Contains(name, "7.3.3") {
			t.Fatalf("invalid pinned fixture %s=%s", name, digest)
		}
		if pins[name] != strings.TrimPrefix(digest, "sha256:") {
			t.Fatalf("Dockerfile.runtime pin drift for %s: got %q want %q", name, pins[name], digest)
		}
	}
}

func responseFor(request *http.Request, status int, body string) *http.Response {
	return &http.Response{
		StatusCode:    status,
		Status:        fmt.Sprintf("%d", status),
		Body:          io.NopCloser(strings.NewReader(body)),
		ContentLength: int64(len(body)),
		Header:        make(http.Header),
		Request:       request,
	}
}

func errorsIs(err, target error) bool {
	return errors.Is(err, target)
}

func TestSourceUsesCallerContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	source, err := newSource("linux", "amd64", doerFunc(func(request *http.Request) (*http.Response, error) {
		return nil, request.Context().Err()
	}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.Resolve(ctx, "7.3.3"); err == nil {
		t.Fatal("Resolve() error = nil")
	}
}
