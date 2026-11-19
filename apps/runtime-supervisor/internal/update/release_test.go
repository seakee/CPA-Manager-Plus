package update

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"testing"
	"time"
)

type doerFunc func(*http.Request) (*http.Response, error)

func (f doerFunc) Do(request *http.Request) (*http.Response, error) { return f(request) }

type cdnFixtureConn struct {
	net.Conn
	remote net.Addr
}

func (c cdnFixtureConn) RemoteAddr() net.Addr { return c.remote }

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

func TestDownloadFromCDNEnforcesOfficialSizeDigestAndURLPolicy(t *testing.T) {
	archive := []byte("verified-cdn-release-archive-bytes")
	digest := sha256.Sum256(archive)
	release := Release{
		Version:       "7.3.3",
		AssetName:     "CLIProxyAPI_7.3.3_linux_amd64.tar.gz",
		AssetURL:      "https://github.com/router-for-me/CLIProxyAPI/releases/download/v7.3.3/CLIProxyAPI_7.3.3_linux_amd64.tar.gz",
		ArchiveDigest: "sha256:" + hex.EncodeToString(digest[:]),
		ArchiveSize:   int64(len(archive)),
	}
	officialPath := "/router-for-me/CLIProxyAPI/releases/download/v7.3.3/CLIProxyAPI_7.3.3_linux_amd64.tar.gz"

	tlsServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/mirror/https://github.com" + officialPath:
			w.Header().Set("Content-Length", strconv.Itoa(len(archive)))
			_, _ = w.Write(archive)
		case "/hop-mirror/https://github.com" + officialPath:
			http.Redirect(w, r, "https://cdn2.example.com/hop-target", http.StatusFound)
		case "/hop-target":
			w.Header().Set("Content-Length", strconv.Itoa(len(archive)))
			_, _ = w.Write(archive)
		case "/redirect-fakeip/https://github.com" + officialPath:
			http.Redirect(w, r, "https://198.18.0.1/asset.tar.gz", http.StatusFound)
		case "/redirect-loopback/https://github.com" + officialPath:
			http.Redirect(w, r, "https://127.0.0.1/asset.tar.gz", http.StatusFound)
		case "/redirect-localhost-dot/https://github.com" + officialPath:
			http.Redirect(w, r, "https://sub.localhost./asset.tar.gz", http.StatusFound)
		case "/wrong-size/https://github.com" + officialPath:
			short := archive[:len(archive)-3]
			w.Header().Set("Content-Length", strconv.Itoa(len(short)))
			_, _ = w.Write(short)
		case "/extra-bytes/https://github.com" + officialPath:
			longer := append(append([]byte(nil), archive...), 'x', 'y')
			w.Header().Set("Content-Length", strconv.Itoa(len(longer)))
			_, _ = w.Write(longer)
		case "/wrong-digest/https://github.com" + officialPath:
			corrupted := append([]byte(nil), archive...)
			corrupted[0] ^= 0xff
			w.Header().Set("Content-Length", strconv.Itoa(len(corrupted)))
			_, _ = w.Write(corrupted)
		case "/oversize/https://github.com" + officialPath:
			w.Header().Set("Content-Length", strconv.FormatInt(MaxAcquisitionArchiveBytes+1, 10))
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer tlsServer.Close()

	client := newTLSServerClientForOfficialHosts(tlsServer)
	// Route approved test addresses to the local TLS fixture while preserving
	// the synthetic dial destination for the transport's peer address check.
	transport := client.Transport.(*http.Transport)
	fixtureDial := transport.DialContext
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		peer, err := net.ResolveTCPAddr(network, address)
		if err != nil {
			return nil, err
		}
		conn, err := fixtureDial(ctx, network, address)
		if err != nil {
			return nil, err
		}
		return cdnFixtureConn{Conn: conn, remote: peer}, nil
	}
	publicLookup := func(_ context.Context, host string) ([]net.IPAddr, error) {
		if host == githubAPIHost {
			return []net.IPAddr{{IP: net.ParseIP("140.82.121.6")}}, nil
		}
		return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, nil
	}
	fakeSentinelLookup := func(_ context.Context, host string) ([]net.IPAddr, error) {
		if host == githubAPIHost {
			return []net.IPAddr{{IP: net.ParseIP("198.18.0.31")}}, nil
		}
		return []net.IPAddr{{IP: net.ParseIP("198.18.0.113")}}, nil
	}

	// 1. Exact size and SHA-256 digest succeed over public DNS and over DNS-only Fake-IP sentinel mode (including safe domain redirect).
	for _, tc := range []struct {
		name   string
		prefix string
		lookup CDNLookupIPAddrFunc
	}{
		{name: "public DNS", prefix: "https://cdn.example.com/mirror/", lookup: publicLookup},
		{name: "Fake-IP DNS sentinel", prefix: "https://cdn.example.com/hop-mirror/", lookup: fakeSentinelLookup},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var dst bytes.Buffer
			var lastReceived, lastTotal int64
			err := DownloadFromCDN(
				t.Context(),
				release,
				tc.prefix,
				&dst,
				func(received, total int64) {
					lastReceived = received
					lastTotal = total
				},
				WithCDNHTTPClient(client),
				WithCDNLookupIP(tc.lookup),
			)
			if err != nil {
				t.Fatalf("DownloadFromCDN(%s) error = %v", tc.name, err)
			}
			if !bytes.Equal(dst.Bytes(), archive) || lastReceived != release.ArchiveSize || lastTotal != release.ArchiveSize {
				t.Fatalf("DownloadFromCDN(%s) bytes=%d progress=(%d/%d), want %d", tc.name, dst.Len(), lastReceived, lastTotal, release.ArchiveSize)
			}
		})
	}

	// 2. DNS resolving CDN host to Fake-IP when api.github.com is NOT in Fake-IP mode is refused.
	nonSentinelFakeLookup := func(_ context.Context, host string) ([]net.IPAddr, error) {
		if host == githubAPIHost {
			return []net.IPAddr{{IP: net.ParseIP("140.82.121.6")}}, nil
		}
		return []net.IPAddr{{IP: net.ParseIP("198.18.0.113")}}, nil
	}
	if err := DownloadFromCDN(
		t.Context(),
		release,
		"https://cdn.example.com/mirror",
		io.Discard,
		nil,
		WithCDNHTTPClient(client),
		WithCDNLookupIP(nonSentinelFakeLookup),
	); !errors.Is(err, ErrDownloadFailed) {
		t.Fatalf("DownloadFromCDN with non-sentinel Fake-IP error = %v, want ErrDownloadFailed", err)
	}

	// 3. Literal Fake-IP, private IP, and trailing-dot localhost prefixes are rejected even when Fake-IP sentinel is active.
	for _, badPrefix := range []string{
		"https://198.18.0.1/mirror",
		"https://127.0.0.1/mirror",
		"https://10.0.0.1/mirror",
		"https://localhost./mirror",
		"https://sub.localhost./mirror",
	} {
		if err := DownloadFromCDN(
			t.Context(),
			release,
			badPrefix,
			io.Discard,
			nil,
			WithCDNHTTPClient(client),
			WithCDNLookupIP(fakeSentinelLookup),
		); !errors.Is(err, ErrInvalidCDNPrefix) {
			t.Fatalf("DownloadFromCDN(%q) error = %v, want ErrInvalidCDNPrefix", badPrefix, err)
		}
	}

	// 4. Redirects to literal Fake-IP, loopback, or trailing-dot localhost are rejected even when Fake-IP sentinel is active.
	for _, redirectPrefix := range []string{
		"https://cdn.example.com/redirect-fakeip",
		"https://cdn.example.com/redirect-loopback",
		"https://cdn.example.com/redirect-localhost-dot",
	} {
		if err := DownloadFromCDN(
			t.Context(),
			release,
			redirectPrefix,
			io.Discard,
			nil,
			WithCDNHTTPClient(client),
			WithCDNLookupIP(fakeSentinelLookup),
		); !errors.Is(err, ErrDownloadFailed) {
			t.Fatalf("DownloadFromCDN(%q) redirect error = %v, want ErrDownloadFailed", redirectPrefix, err)
		}
	}

	// 5. Size mismatch, digest mismatch, and oversize archive fail closed with exact error classification.
	for _, tc := range []struct {
		prefix  string
		wantErr error
	}{
		{prefix: "https://cdn.example.com/wrong-size", wantErr: ErrArchiveInvalid},
		{prefix: "https://cdn.example.com/extra-bytes", wantErr: ErrArchiveInvalid},
		{prefix: "https://cdn.example.com/wrong-digest", wantErr: ErrArchiveInvalid},
		{prefix: "https://cdn.example.com/oversize", wantErr: ErrArchiveTooLarge},
	} {
		if err := DownloadFromCDN(
			t.Context(),
			release,
			tc.prefix,
			io.Discard,
			nil,
			WithCDNHTTPClient(client),
			WithCDNLookupIP(publicLookup),
		); !errors.Is(err, tc.wantErr) {
			t.Fatalf("DownloadFromCDN(%q) error = %v, want %v", tc.prefix, err, tc.wantErr)
		}
	}
}

func TestVerifyUploadStreamEnforcesBoundedSizeAndOfficialDigest(t *testing.T) {
	archive := []byte("verified-uploaded-release-archive-bytes")
	digest := sha256.Sum256(archive)
	release := Release{
		Version:       "7.3.3",
		AssetName:     "CLIProxyAPI_7.3.3_linux_amd64.tar.gz",
		AssetURL:      "https://github.com/router-for-me/CLIProxyAPI/releases/download/v7.3.3/CLIProxyAPI_7.3.3_linux_amd64.tar.gz",
		ArchiveDigest: "sha256:" + hex.EncodeToString(digest[:]),
		ArchiveSize:   int64(len(archive)),
	}

	// 1. Valid upload with exact known Content-Length and with chunked/unknown Content-Length (-1).
	for _, contentLength := range []int64{release.ArchiveSize, -1} {
		var dst bytes.Buffer
		var lastReceived, lastTotal int64
		err := VerifyUploadStream(
			t.Context(),
			release,
			bytes.NewReader(archive),
			contentLength,
			&dst,
			func(received, total int64) {
				lastReceived = received
				lastTotal = total
			},
		)
		if err != nil {
			t.Fatalf("VerifyUploadStream(contentLength=%d) error = %v", contentLength, err)
		}
		if !bytes.Equal(dst.Bytes(), archive) || lastReceived != release.ArchiveSize || lastTotal != release.ArchiveSize {
			t.Fatalf("VerifyUploadStream(contentLength=%d) bytes=%d progress=(%d/%d), want %d", contentLength, dst.Len(), lastReceived, lastTotal, release.ArchiveSize)
		}
	}

	// 2. Known Content-Length > 64 MiB fails closed with ErrArchiveTooLarge.
	if err := VerifyUploadStream(
		t.Context(),
		release,
		bytes.NewReader(archive),
		MaxAcquisitionArchiveBytes+1,
		io.Discard,
		nil,
	); !errors.Is(err, ErrArchiveTooLarge) {
		t.Fatalf("VerifyUploadStream(known >64 MiB) error = %v, want ErrArchiveTooLarge", err)
	}

	// 3. Unknown Content-Length (-1) streaming 64 MiB + 1 bytes fails closed with ErrArchiveTooLarge even when official release.ArchiveSize is smaller.
	oversizeStream := io.LimitReader(zeroReader{}, MaxAcquisitionArchiveBytes+1)
	if err := VerifyUploadStream(
		t.Context(),
		release,
		oversizeStream,
		-1,
		io.Discard,
		nil,
	); !errors.Is(err, ErrArchiveTooLarge) {
		t.Fatalf("VerifyUploadStream(chunked 64 MiB+1) error = %v, want ErrArchiveTooLarge", err)
	}

	// 4. *http.MaxBytesError from http.MaxBytesReader converts to ErrArchiveTooLarge.
	maxBytesStream := http.MaxBytesReader(nil, io.NopCloser(bytes.NewReader(archive)), int64(len(archive)-1))
	if err := VerifyUploadStream(
		t.Context(),
		release,
		maxBytesStream,
		-1,
		io.Discard,
		nil,
	); !errors.Is(err, ErrArchiveTooLarge) {
		t.Fatalf("VerifyUploadStream(*http.MaxBytesError) error = %v, want ErrArchiveTooLarge", err)
	}

	// 5. Empty stream, size mismatch, and digest mismatch fail closed with ErrArchiveInvalid.
	if err := VerifyUploadStream(t.Context(), release, bytes.NewReader(nil), 0, io.Discard, nil); !errors.Is(err, ErrArchiveInvalid) {
		t.Fatalf("VerifyUploadStream(empty contentLength=0) error = %v, want ErrArchiveInvalid", err)
	}
	if err := VerifyUploadStream(t.Context(), release, bytes.NewReader(nil), -1, io.Discard, nil); !errors.Is(err, ErrArchiveInvalid) {
		t.Fatalf("VerifyUploadStream(empty contentLength=-1) error = %v, want ErrArchiveInvalid", err)
	}
	if err := VerifyUploadStream(t.Context(), release, bytes.NewReader(archive[:len(archive)-1]), -1, io.Discard, nil); !errors.Is(err, ErrArchiveInvalid) {
		t.Fatalf("VerifyUploadStream(truncated) error = %v, want ErrArchiveInvalid", err)
	}
	extra := append(append([]byte(nil), archive...), '!')
	if err := VerifyUploadStream(t.Context(), release, bytes.NewReader(extra), -1, io.Discard, nil); !errors.Is(err, ErrArchiveInvalid) {
		t.Fatalf("VerifyUploadStream(extra byte) error = %v, want ErrArchiveInvalid", err)
	}
	corrupted := append([]byte(nil), archive...)
	corrupted[len(corrupted)-1] ^= 0xff
	if err := VerifyUploadStream(t.Context(), release, bytes.NewReader(corrupted), release.ArchiveSize, io.Discard, nil); !errors.Is(err, ErrArchiveInvalid) {
		t.Fatalf("VerifyUploadStream(digest mismatch) error = %v, want ErrArchiveInvalid", err)
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

type partialErrReader struct {
	prefix []byte
	sent   bool
	err    error
}

func (r *partialErrReader) Read(p []byte) (int, error) {
	if !r.sent && len(r.prefix) > 0 {
		r.sent = true
		return copy(p, r.prefix), nil
	}
	return 0, r.err
}

func TestDownloadFromCDNAndVerifyUploadStreamEnforceBoundsAndMetadata(t *testing.T) {
	archive := []byte("0123456789abcdef0123456789abcdef")
	digest := sha256.Sum256(archive)
	baseRelease := Release{
		Version:       "7.3.3",
		AssetName:     "CLIProxyAPI_7.3.3_linux_amd64.tar.gz",
		AssetURL:      "https://github.com/router-for-me/CLIProxyAPI/releases/download/v7.3.3/CLIProxyAPI_7.3.3_linux_amd64.tar.gz",
		ArchiveDigest: "sha256:" + hex.EncodeToString(digest[:]),
		ArchiveSize:   int64(len(archive)),
	}

	oversizeRelease := baseRelease
	oversizeRelease.ArchiveSize = MaxAcquisitionArchiveBytes + 1
	if err := DownloadFromCDN(t.Context(), oversizeRelease, "https://cdn.example.com/mirror", io.Discard, nil); !errors.Is(err, ErrArchiveTooLarge) {
		t.Fatalf("DownloadFromCDN(release.ArchiveSize > 64 MiB) = %v, want ErrArchiveTooLarge", err)
	}
	if err := VerifyUploadStream(t.Context(), oversizeRelease, bytes.NewReader(archive), oversizeRelease.ArchiveSize, io.Discard, nil); !errors.Is(err, ErrArchiveTooLarge) {
		t.Fatalf("VerifyUploadStream(release.ArchiveSize > 64 MiB) = %v, want ErrArchiveTooLarge", err)
	}

	for _, bad := range []struct {
		name   string
		mutate func(*Release)
	}{
		{name: "empty asset name", mutate: func(r *Release) { r.AssetName = "" }},
		{name: "unsupported asset name", mutate: func(r *Release) { r.AssetName = "CLIProxyAPI_7.3.3_darwin_arm64.tar.gz" }},
		{name: "empty asset URL", mutate: func(r *Release) { r.AssetURL = "" }},
		{name: "mismatched asset URL", mutate: func(r *Release) {
			r.AssetURL = "https://github.com/router-for-me/CLIProxyAPI/releases/download/v7.3.3/other.tar.gz"
		}},
	} {
		candidate := baseRelease
		bad.mutate(&candidate)
		if err := DownloadFromCDN(t.Context(), candidate, "https://cdn.example.com/mirror", io.Discard, nil); !errors.Is(err, ErrArchiveInvalid) {
			t.Fatalf("DownloadFromCDN(%s) = %v, want ErrArchiveInvalid", bad.name, err)
		}
	}

	officialPath := "/router-for-me/CLIProxyAPI/releases/download/v7.3.3/CLIProxyAPI_7.3.3_linux_amd64.tar.gz"
	tlsServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/wrong-cl/https://github.com" + officialPath:
			w.Header().Set("Content-Length", strconv.Itoa(len(archive)+50))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(bytes.Repeat([]byte("z"), len(archive)+50))
		case "/chunked-extra/https://github.com" + officialPath:
			flusher, _ := w.(http.Flusher)
			_, _ = w.Write(archive)
			if flusher != nil {
				flusher.Flush()
			}
			_, _ = w.Write(bytes.Repeat([]byte("x"), 64*1024))
		default:
			http.NotFound(w, r)
		}
	}))
	defer tlsServer.Close()

	client := newTLSServerClientForOfficialHosts(tlsServer)
	transport := client.Transport.(*http.Transport)
	fixtureDial := transport.DialContext
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		peer, err := net.ResolveTCPAddr(network, address)
		if err != nil {
			return nil, err
		}
		conn, err := fixtureDial(ctx, network, address)
		if err != nil {
			return nil, err
		}
		return cdnFixtureConn{Conn: conn, remote: peer}, nil
	}
	publicLookup := func(_ context.Context, host string) ([]net.IPAddr, error) {
		if host == githubAPIHost {
			return []net.IPAddr{{IP: net.ParseIP("140.82.121.6")}}, nil
		}
		return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, nil
	}

	var wrongCLDst bytes.Buffer
	if err := DownloadFromCDN(
		t.Context(),
		baseRelease,
		"https://cdn.example.com/wrong-cl",
		&wrongCLDst,
		func(received, total int64) {
			if received > total {
				t.Errorf("CDN wrong Content-Length reported received %d > total %d", received, total)
			}
		},
		WithCDNHTTPClient(client),
		WithCDNLookupIP(publicLookup),
	); !errors.Is(err, ErrArchiveInvalid) {
		t.Fatalf("DownloadFromCDN(wrong Content-Length) = %v, want ErrArchiveInvalid", err)
	}
	if wrongCLDst.Len() != 0 {
		t.Fatalf("DownloadFromCDN(wrong Content-Length) wrote %d bytes before header rejection, want 0", wrongCLDst.Len())
	}

	var chunkedCDNDst bytes.Buffer
	if err := DownloadFromCDN(
		t.Context(),
		baseRelease,
		"https://cdn.example.com/chunked-extra",
		&chunkedCDNDst,
		func(received, total int64) {
			if received > total {
				t.Errorf("CDN chunked overflow reported received %d > total %d", received, total)
			}
		},
		WithCDNHTTPClient(client),
		WithCDNLookupIP(publicLookup),
	); !errors.Is(err, ErrArchiveInvalid) {
		t.Fatalf("DownloadFromCDN(chunked extra) = %v, want ErrArchiveInvalid", err)
	}
	if int64(chunkedCDNDst.Len()) > baseRelease.ArchiveSize {
		t.Fatalf("DownloadFromCDN(chunked extra) wrote %d bytes, want <= %d", chunkedCDNDst.Len(), baseRelease.ArchiveSize)
	}

	var uploadOverflowDst bytes.Buffer
	err := VerifyUploadStream(
		t.Context(),
		baseRelease,
		io.LimitReader(zeroReader{}, MaxAcquisitionArchiveBytes+1),
		-1,
		&uploadOverflowDst,
		func(received, total int64) {
			if received > total {
				t.Errorf("VerifyUploadStream overflow reported received %d > total %d", received, total)
			}
		},
	)
	if !errors.Is(err, ErrArchiveTooLarge) {
		t.Fatalf("VerifyUploadStream(chunked >64 MiB) = %v, want ErrArchiveTooLarge", err)
	}
	if int64(uploadOverflowDst.Len()) > baseRelease.ArchiveSize {
		t.Fatalf("VerifyUploadStream(chunked >64 MiB) wrote %d bytes to disk, want <= %d", uploadOverflowDst.Len(), baseRelease.ArchiveSize)
	}

	readFailErr := VerifyUploadStream(
		t.Context(),
		baseRelease,
		&partialErrReader{prefix: archive[:4], err: io.ErrUnexpectedEOF},
		-1,
		io.Discard,
		nil,
	)
	if !errors.Is(readFailErr, ErrDownloadFailed) || !errors.Is(readFailErr, io.ErrUnexpectedEOF) {
		t.Fatalf("VerifyUploadStream(unexpected EOF) = %v, want ErrDownloadFailed wrapping io.ErrUnexpectedEOF", readFailErr)
	}
}

func TestDownloadWithProgressClassifiesTransportAndAssetFailures(t *testing.T) {
	archive := []byte("verified-official-release-archive-bytes")
	digest := sha256.Sum256(archive)
	release := Release{
		Version:       "7.3.3",
		AssetName:     "CLIProxyAPI_7.3.3_linux_amd64.tar.gz",
		AssetURL:      "https://github.com/router-for-me/CLIProxyAPI/releases/download/v7.3.3/CLIProxyAPI_7.3.3_linux_amd64.tar.gz",
		ArchiveDigest: "sha256:" + hex.EncodeToString(digest[:]),
		ArchiveSize:   int64(len(archive)),
	}

	transportCause := errors.New("dial tcp: connection reset by peer")
	transportSrc, err := newSource("linux", "amd64", doerFunc(func(*http.Request) (*http.Response, error) {
		return nil, transportCause
	}))
	if err != nil {
		t.Fatal(err)
	}
	dlErr := transportSrc.DownloadWithProgress(t.Context(), release, io.Discard, nil)
	if !errors.Is(dlErr, ErrDownloadFailed) || !errors.Is(dlErr, transportCause) || errors.Is(dlErr, ErrArchiveInvalid) {
		t.Fatalf("DownloadWithProgress(transport error) = %v, want ErrDownloadFailed wrapping transportCause", dlErr)
	}

	statusSrc, err := newSource("linux", "amd64", doerFunc(func(request *http.Request) (*http.Response, error) {
		return responseFor(request, http.StatusServiceUnavailable, "service unavailable"), nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	dlErr = statusSrc.DownloadWithProgress(t.Context(), release, io.Discard, nil)
	if !errors.Is(dlErr, ErrDownloadFailed) || errors.Is(dlErr, ErrArchiveInvalid) {
		t.Fatalf("DownloadWithProgress(HTTP 503) = %v, want ErrDownloadFailed", dlErr)
	}

	midStreamSrc, err := newSource("linux", "amd64", doerFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode:    http.StatusOK,
			Status:        "200 OK",
			Body:          io.NopCloser(&partialErrReader{prefix: archive[:4], err: io.ErrUnexpectedEOF}),
			ContentLength: release.ArchiveSize,
			Header:        make(http.Header),
			Request:       request,
		}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	var progressBytes int64
	dlErr = midStreamSrc.DownloadWithProgress(t.Context(), release, io.Discard, func(received, _ int64) {
		progressBytes = received
	})
	if !errors.Is(dlErr, ErrDownloadFailed) || !errors.Is(dlErr, io.ErrUnexpectedEOF) || errors.Is(dlErr, ErrArchiveInvalid) {
		t.Fatalf("DownloadWithProgress(mid-stream unexpected EOF) = %v, want ErrDownloadFailed wrapping io.ErrUnexpectedEOF", dlErr)
	}
	if progressBytes != 4 {
		t.Fatalf("DownloadWithProgress(mid-stream unexpected EOF) progress = %d, want 4", progressBytes)
	}

	canceledCtx, cancel := context.WithCancel(t.Context())
	cancel()
	cancelStreamSrc, err := newSource("linux", "amd64", doerFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode:    http.StatusOK,
			Status:        "200 OK",
			Body:          io.NopCloser(bytes.NewReader(archive)),
			ContentLength: release.ArchiveSize,
			Header:        make(http.Header),
			Request:       request,
		}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	dlErr = cancelStreamSrc.DownloadWithProgress(canceledCtx, release, io.Discard, nil)
	if !errors.Is(dlErr, ErrDownloadFailed) || !errors.Is(dlErr, context.Canceled) || errors.Is(dlErr, ErrArchiveInvalid) {
		t.Fatalf("DownloadWithProgress(canceled context) = %v, want ErrDownloadFailed wrapping context.Canceled", dlErr)
	}

	untrustedReq, _ := http.NewRequest(http.MethodGet, "https://example.test/asset.tar.gz", nil)
	for _, tc := range []struct {
		name string
		req  *http.Request
	}{
		{name: "nil response request", req: nil},
		{name: "nil response request URL", req: &http.Request{}},
		{name: "disallowed redirect host", req: untrustedReq},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src, srcErr := newSource("linux", "amd64", doerFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode:    http.StatusOK,
					Status:        "200 OK",
					Body:          io.NopCloser(bytes.NewReader(archive)),
					ContentLength: release.ArchiveSize,
					Header:        make(http.Header),
					Request:       tc.req,
				}, nil
			}))
			if srcErr != nil {
				t.Fatal(srcErr)
			}
			gotErr := src.DownloadWithProgress(t.Context(), release, io.Discard, nil)
			if !errors.Is(gotErr, ErrArchiveInvalid) || errors.Is(gotErr, ErrDownloadFailed) {
				t.Fatalf("DownloadWithProgress(%s) = %v, want ErrArchiveInvalid", tc.name, gotErr)
			}
		})
	}

	redirectServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://example.test/asset.tar.gz", http.StatusFound)
	}))
	defer redirectServer.Close()
	redirectSource, err := NewSourceForPlatform("linux", "amd64", newTLSServerClientForOfficialHosts(redirectServer))
	if err != nil {
		t.Fatal(err)
	}
	dlErr = redirectSource.DownloadWithProgress(t.Context(), release, io.Discard, nil)
	if !errors.Is(dlErr, ErrArchiveInvalid) || errors.Is(dlErr, ErrDownloadFailed) {
		t.Fatalf("DownloadWithProgress(CheckRedirect disallowed host) = %v, want ErrArchiveInvalid", dlErr)
	}

	shortStreamSrc, err := newSource("linux", "amd64", doerFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode:    http.StatusOK,
			Status:        "200 OK",
			Body:          io.NopCloser(bytes.NewReader(archive[:4])),
			ContentLength: -1,
			Header:        make(http.Header),
			Request:       request,
		}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	dlErr = shortStreamSrc.DownloadWithProgress(t.Context(), release, io.Discard, nil)
	if !errors.Is(dlErr, ErrArchiveInvalid) || errors.Is(dlErr, ErrDownloadFailed) {
		t.Fatalf("DownloadWithProgress(short stream) = %v, want ErrArchiveInvalid", dlErr)
	}
}

func TestDownloadFromCDNAndAddrPolicyNormalizeNilContext(t *testing.T) {
	var nilCtx context.Context

	publicLookup := func(ctx context.Context, host string) ([]net.IPAddr, error) {
		if ctx == nil {
			t.Error("lookup received nil context")
		}
		if host == githubAPIHost {
			return []net.IPAddr{{IP: net.ParseIP("140.82.121.6")}}, nil
		}
		return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, nil
	}
	fakeSentinelLookup := func(ctx context.Context, host string) ([]net.IPAddr, error) {
		if ctx == nil {
			t.Error("lookup received nil context")
		}
		if host == githubAPIHost {
			return []net.IPAddr{{IP: net.ParseIP("198.18.0.31")}}, nil
		}
		return []net.IPAddr{{IP: net.ParseIP("198.18.0.113")}}, nil
	}

	publicPolicy := CDNAddrPolicyForContext(nilCtx, publicLookup)
	if !publicPolicy(netip.MustParseAddr("93.184.216.34")) || publicPolicy(netip.MustParseAddr("198.18.0.113")) {
		t.Fatal("CDNAddrPolicyForContext(nil, publicLookup) did not return strict public policy")
	}

	fakePolicy := CDNAddrPolicyForContext(nilCtx, fakeSentinelLookup)
	if !fakePolicy(netip.MustParseAddr("198.18.0.113")) || fakePolicy(netip.MustParseAddr("10.0.0.1")) {
		t.Fatal("CDNAddrPolicyForContext(nil, fakeSentinelLookup) did not return Fake-IP policy")
	}

	if !isTunnelFakeIPMode(nilCtx, fakeSentinelLookup) {
		t.Fatal("isTunnelFakeIPMode(nil, fakeSentinelLookup) = false, want true")
	}
	if isTunnelFakeIPMode(nilCtx, publicLookup) {
		t.Fatal("isTunnelFakeIPMode(nil, publicLookup) = true, want false")
	}

	archive := []byte("nil-context-cdn-release-archive-bytes")
	digest := sha256.Sum256(archive)
	release := Release{
		Version:       "7.3.3",
		AssetName:     "CLIProxyAPI_7.3.3_linux_amd64.tar.gz",
		AssetURL:      "https://github.com/router-for-me/CLIProxyAPI/releases/download/v7.3.3/CLIProxyAPI_7.3.3_linux_amd64.tar.gz",
		ArchiveDigest: "sha256:" + hex.EncodeToString(digest[:]),
		ArchiveSize:   int64(len(archive)),
	}
	officialPath := "/router-for-me/CLIProxyAPI/releases/download/v7.3.3/CLIProxyAPI_7.3.3_linux_amd64.tar.gz"

	tlsServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/mirror/https://github.com"+officialPath {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(archive)))
		_, _ = w.Write(archive)
	}))
	defer tlsServer.Close()

	client := newTLSServerClientForOfficialHosts(tlsServer)
	transport := client.Transport.(*http.Transport)
	fixtureDial := transport.DialContext
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		peer, err := net.ResolveTCPAddr(network, address)
		if err != nil {
			return nil, err
		}
		conn, err := fixtureDial(ctx, network, address)
		if err != nil {
			return nil, err
		}
		return cdnFixtureConn{Conn: conn, remote: peer}, nil
	}

	var dst bytes.Buffer
	if err := DownloadFromCDN(
		nilCtx,
		release,
		"https://cdn.example.com/mirror/",
		&dst,
		nil,
		WithCDNHTTPClient(client),
		WithCDNLookupIP(publicLookup),
	); err != nil {
		t.Fatalf("DownloadFromCDN(nil ctx) error = %v", err)
	}
	if !bytes.Equal(dst.Bytes(), archive) {
		t.Fatalf("DownloadFromCDN(nil ctx) bytes = %q, want %q", dst.Bytes(), archive)
	}
}

func TestDownloadWithProgressReturnsDownloadFailedOnExcessiveOfficialRedirectChain(t *testing.T) {
	archive := []byte("verified-redirect-chain-archive-bytes")
	digest := sha256.Sum256(archive)
	release := Release{
		Version:       "7.3.3",
		AssetName:     "CLIProxyAPI_7.3.3_linux_amd64.tar.gz",
		AssetURL:      "https://github.com/router-for-me/CLIProxyAPI/releases/download/v7.3.3/CLIProxyAPI_7.3.3_linux_amd64.tar.gz",
		ArchiveDigest: "sha256:" + hex.EncodeToString(digest[:]),
		ArchiveSize:   int64(len(archive)),
	}

	var fourHopRequests int
	fourHopServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fourHopRequests++
		if fourHopRequests <= 4 {
			http.Redirect(w, r, fmt.Sprintf("https://release-assets.githubusercontent.com/hop-%d", fourHopRequests), http.StatusFound)
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(archive)))
		_, _ = w.Write(archive)
	}))
	defer fourHopServer.Close()

	fourHopSource, err := NewSourceForPlatform("linux", "amd64", newTLSServerClientForOfficialHosts(fourHopServer))
	if err != nil {
		t.Fatal(err)
	}
	var dst bytes.Buffer
	if err := fourHopSource.DownloadWithProgress(t.Context(), release, &dst, nil); err != nil {
		t.Fatalf("DownloadWithProgress(4 official redirects) error = %v", err)
	}
	if !bytes.Equal(dst.Bytes(), archive) || fourHopRequests != 5 {
		t.Fatalf("DownloadWithProgress(4 official redirects) bytes=%q requests=%d", dst.Bytes(), fourHopRequests)
	}

	var fiveHopRequests int
	fiveHopServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fiveHopRequests++
		http.Redirect(w, r, fmt.Sprintf("https://release-assets.githubusercontent.com/hop-%d", fiveHopRequests), http.StatusFound)
	}))
	defer fiveHopServer.Close()

	fiveHopSource, err := NewSourceForPlatform("linux", "amd64", newTLSServerClientForOfficialHosts(fiveHopServer))
	if err != nil {
		t.Fatal(err)
	}
	dlErr := fiveHopSource.DownloadWithProgress(t.Context(), release, io.Discard, nil)
	if !errors.Is(dlErr, ErrDownloadFailed) || errors.Is(dlErr, ErrArchiveInvalid) {
		t.Fatalf("DownloadWithProgress(5 official redirects) = %v, want ErrDownloadFailed and not ErrArchiveInvalid", dlErr)
	}
	if !strings.Contains(dlErr.Error(), "too many GitHub release redirects") {
		t.Fatalf("DownloadWithProgress(5 official redirects) = %v, want redirect limit message", dlErr)
	}
	if fiveHopRequests != 5 {
		t.Fatalf("DownloadWithProgress(5 official redirects) requests = %d, want 5", fiveHopRequests)
	}
}
