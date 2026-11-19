// Package update owns trusted CPA release resolution and inactive artifact staging.
package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"time"
)

const (
	officialRepositoryOwner = "router-for-me"
	officialRepositoryName  = "CLIProxyAPI"
	githubAPIHost           = "api.github.com"
	githubHost              = "github.com"
	// ReleaseClientTimeout bounds each official metadata or asset request. The
	// prepare-update transport has a larger, route-specific budget because a
	// complete operation also includes both requests and local staging I/O.
	ReleaseClientTimeout    = 5 * time.Minute
	maxReleaseMetadataBytes = 1 << 20
	maxReleaseArchiveBytes  = 256 << 20
	metadataAccept          = "application/vnd.github+json"
	assetAccept             = "application/octet-stream"
	userAgent               = "cpamp-runtime-supervisor/prepare-update-v1"
)

var (
	ErrUnsupportedPlatform      = errors.New("unsupported update staging platform")
	ErrReleaseMetadata          = errors.New("official release metadata is unavailable or invalid")
	ErrArchiveInvalid           = errors.New("official release asset is invalid")
	errDisallowedGitHubRedirect = errors.New("unsafe GitHub release redirect")
)

// Release is an exact, verified selection from the official GitHub Release
// metadata. URL and filenames are Supervisor-derived and never caller input.
type Release struct {
	Version       string
	AssetName     string
	AssetURL      string
	ArchiveDigest string
	ArchiveSize   int64
}

type githubRelease struct {
	TagName string        `json:"tag_name"`
	Draft   bool          `json:"draft"`
	Assets  []githubAsset `json:"assets"`
}

type githubAsset struct {
	Name               string `json:"name"`
	Digest             string `json:"digest"`
	Size               int64  `json:"size"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

type httpDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// Source is the fixed official router-for-me/CLIProxyAPI release source.
type Source struct {
	client httpDoer
	asset  string
}

// CloseIdleConnections closes any idle keep-alive connections held by this release source's HTTP client.
func (s *Source) CloseIdleConnections() {
	if s == nil || s.client == nil {
		return
	}
	if closer, ok := s.client.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

func NewSource() (*Source, error) {
	return newSource(runtime.GOOS, runtime.GOARCH, newHTTPClient())
}

func newSource(goos, goarch string, client httpDoer) (*Source, error) {
	assetSuffix, err := platformAssetSuffix(goos, goarch)
	if err != nil {
		return nil, err
	}
	if client == nil {
		return nil, errors.New("release HTTP client is required")
	}
	return &Source{client: client, asset: assetSuffix}, nil
}

func SupportedPlatform(goos, goarch string) bool {
	_, err := platformAssetSuffix(goos, goarch)
	return err == nil
}

func platformAssetSuffix(goos, goarch string) (string, error) {
	if goos != "linux" {
		return "", fmt.Errorf("%w: %s/%s", ErrUnsupportedPlatform, goos, goarch)
	}
	switch goarch {
	case "amd64":
		return "linux_amd64.tar.gz", nil
	case "arm64":
		return "linux_aarch64.tar.gz", nil
	default:
		return "", fmt.Errorf("%w: %s/%s", ErrUnsupportedPlatform, goos, goarch)
	}
}

func (s *Source) Resolve(ctx context.Context, version string) (Release, error) {
	if err := ValidateVersion(version); err != nil {
		return Release{}, err
	}
	tag := "v" + version
	assetName := "CLIProxyAPI_" + version + "_" + s.asset
	metadataURL := (&url.URL{
		Scheme: "https",
		Host:   githubAPIHost,
		Path:   "/repos/" + officialRepositoryOwner + "/" + officialRepositoryName + "/releases/tags/" + tag,
	}).String()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, metadataURL, nil)
	if err != nil {
		return Release{}, fmt.Errorf("%w: create request: %v", ErrReleaseMetadata, err)
	}
	req.Header.Set("Accept", metadataAccept)
	req.Header.Set("User-Agent", userAgent)
	resp, err := s.client.Do(req)
	if err != nil {
		return Release{}, fmt.Errorf("%w: request exact tag: %v", ErrReleaseMetadata, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Request == nil || resp.Request.URL == nil || resp.Request.URL.String() != metadataURL {
		return Release{}, fmt.Errorf("%w: exact tag returned HTTP %d", ErrReleaseMetadata, resp.StatusCode)
	}
	if resp.ContentLength > maxReleaseMetadataBytes {
		return Release{}, fmt.Errorf("%w: response exceeds size limit", ErrReleaseMetadata)
	}
	encoded, err := io.ReadAll(io.LimitReader(resp.Body, maxReleaseMetadataBytes+1))
	if err != nil || len(encoded) > maxReleaseMetadataBytes {
		return Release{}, fmt.Errorf("%w: bounded response read failed", ErrReleaseMetadata)
	}
	var metadata githubRelease
	decoder := json.NewDecoder(strings.NewReader(string(encoded)))
	if err := decoder.Decode(&metadata); err != nil {
		return Release{}, fmt.Errorf("%w: decode response: %v", ErrReleaseMetadata, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Release{}, fmt.Errorf("%w: response must contain one JSON value", ErrReleaseMetadata)
	}
	if metadata.Draft || metadata.TagName != tag {
		return Release{}, fmt.Errorf("%w: release tag or draft state is invalid", ErrReleaseMetadata)
	}
	expectedURL := (&url.URL{
		Scheme: "https",
		Host:   githubHost,
		Path:   "/" + officialRepositoryOwner + "/" + officialRepositoryName + "/releases/download/" + tag + "/" + assetName,
	}).String()
	var selected *githubAsset
	for index := range metadata.Assets {
		if metadata.Assets[index].Name != assetName {
			continue
		}
		if selected != nil {
			return Release{}, fmt.Errorf("%w: duplicate expected asset", ErrReleaseMetadata)
		}
		selected = &metadata.Assets[index]
	}
	if selected == nil || selected.BrowserDownloadURL != expectedURL ||
		!canonicalDigest(selected.Digest) || selected.Size <= 0 || selected.Size > maxReleaseArchiveBytes {
		return Release{}, fmt.Errorf("%w: expected asset metadata is invalid", ErrReleaseMetadata)
	}
	return Release{
		Version:       version,
		AssetName:     assetName,
		AssetURL:      expectedURL,
		ArchiveDigest: selected.Digest,
		ArchiveSize:   selected.Size,
	}, nil
}

// Download writes and verifies the exact selected archive. It accepts no URL,
// digest, repository, or asset override from the caller.
func (s *Source) Download(ctx context.Context, release Release, destination io.Writer) error {
	return s.DownloadWithProgress(ctx, release, destination, nil)
}

// DownloadWithProgress writes and verifies the exact selected archive while
// reporting byte progress.
func (s *Source) validateRelease(release Release) error {
	if s == nil || s.asset == "" || ValidateVersion(release.Version) != nil ||
		release.AssetName != "CLIProxyAPI_"+release.Version+"_"+s.asset ||
		!canonicalDigest(release.ArchiveDigest) || release.ArchiveSize <= 0 ||
		release.ArchiveSize > maxReleaseArchiveBytes {
		return fmt.Errorf("%w: noncanonical selected asset", ErrArchiveInvalid)
	}
	expectedURL := (&url.URL{
		Scheme: "https",
		Host:   githubHost,
		Path:   "/" + officialRepositoryOwner + "/" + officialRepositoryName + "/releases/download/v" + release.Version + "/" + release.AssetName,
	}).String()
	if release.AssetURL == "" || release.AssetURL != expectedURL {
		return fmt.Errorf("%w: asset URL differs from official source", ErrArchiveInvalid)
	}
	return nil
}

func (s *Source) DownloadWithProgress(ctx context.Context, release Release, destination io.Writer, progress ProgressFunc) error {
	if destination == nil {
		return fmt.Errorf("%w: noncanonical selected asset", ErrArchiveInvalid)
	}
	if err := s.validateRelease(release); err != nil {
		return err
	}
	if s.client == nil {
		return fmt.Errorf("%w: release HTTP client is unavailable", ErrDownloadFailed)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, release.AssetURL, nil)
	if err != nil {
		return fmt.Errorf("%w: create request: %v", ErrArchiveInvalid, err)
	}
	req.Header.Set("Accept", assetAccept)
	req.Header.Set("User-Agent", userAgent)
	resp, err := s.client.Do(req)
	if err != nil {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		if errors.Is(err, errDisallowedGitHubRedirect) {
			return fmt.Errorf("%w: download request: %w", ErrArchiveInvalid, err)
		}
		return fmt.Errorf("%w: download request: %w", ErrDownloadFailed, err)
	}
	if resp == nil || resp.Body == nil {
		return fmt.Errorf("%w: download response is empty", ErrDownloadFailed)
	}
	defer resp.Body.Close()
	if resp.Request == nil || resp.Request.URL == nil || resp.Request.URL.Scheme != "https" ||
		!allowedDownloadHost(resp.Request.URL.Hostname()) {
		return fmt.Errorf("%w: disallowed download redirect target", ErrArchiveInvalid)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: download returned HTTP %d", ErrDownloadFailed, resp.StatusCode)
	}
	if resp.ContentLength >= 0 && resp.ContentLength != release.ArchiveSize {
		return fmt.Errorf("%w: download size differs from release metadata", ErrArchiveInvalid)
	}
	return copyAndVerifyReleaseStream(ctx, resp.Body, release, destination, progress, "bounded download size verification failed")
}

// DownloadFromCDN downloads the exact official release archive through a
// validated HTTPS CDN prefix and enforces exact release size, 64 MiB cap, and SHA-256 digest.
func DownloadFromCDN(ctx context.Context, release Release, cdnPrefix string, destination io.Writer, progress ProgressFunc, opts ...CDNSourceOption) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if destination == nil || ValidateVersion(release.Version) != nil ||
		!canonicalDigest(release.ArchiveDigest) || release.ArchiveSize <= 0 {
		return fmt.Errorf("%w: noncanonical selected asset", ErrArchiveInvalid)
	}
	goos, goarch, err := resolveCDNReleasePlatform(release)
	if err != nil {
		return err
	}
	if release.ArchiveSize > MaxAcquisitionArchiveBytes {
		return fmt.Errorf("%w: official release archive size %d exceeds %d", ErrArchiveTooLarge, release.ArchiveSize, MaxAcquisitionArchiveBytes)
	}
	var probeCfg cdnSourceConfig
	for _, opt := range opts {
		if opt != nil {
			opt(&probeCfg)
		}
	}
	literalPolicy := probeCfg.redirectAddrPolicy
	if literalPolicy == nil {
		if probeCfg.addrPolicy != nil {
			literalPolicy = probeCfg.addrPolicy
		} else {
			literalPolicy = IsPublicCDNAddr
		}
	}
	// Always enforce the literal URL IP policy (strict IsPublicCDNAddr in production) on the CDN prefix URL.
	normalizedPrefix, err := ValidateCDNPrefixWithPolicy(cdnPrefix, literalPolicy)
	if err != nil {
		return err
	}
	dialPolicy := probeCfg.addrPolicy
	if dialPolicy == nil {
		dialPolicy = CDNAddrPolicyForContext(ctx, probeCfg.lookupIP)
	}
	effectiveOpts := make([]CDNSourceOption, 0, len(opts)+2)
	effectiveOpts = append(effectiveOpts, opts...)
	effectiveOpts = append(effectiveOpts,
		WithCDNAddrPolicy(dialPolicy),
		WithCDNRedirectAddrPolicy(literalPolicy),
	)
	cdnSrc, err := NewCDNSourceForPlatformWithRoots(goos, goarch, normalizedPrefix, nil, effectiveOpts...)
	if err != nil {
		return err
	}
	defer cdnSrc.CloseIdleConnections()

	hasher := sha256.New()
	writer := io.MultiWriter(destination, hasher)
	var written int64
	if err := cdnSrc.downloadArchiveBounded(ctx, release.Version, release.ArchiveSize, writer, func(received, _ int64) {
		written = received
		if progress != nil {
			progress(received, release.ArchiveSize)
		}
	}); err != nil {
		return err
	}
	if written != release.ArchiveSize {
		if ctx != nil {
			if err := ctx.Err(); err != nil {
				return fmt.Errorf("%w: CDN download interrupted: %w", ErrDownloadFailed, err)
			}
		}
		return fmt.Errorf("%w: CDN download size differs from release metadata", ErrArchiveInvalid)
	}
	actual := "sha256:" + hex.EncodeToString(hasher.Sum(nil))
	if actual != release.ArchiveDigest {
		return fmt.Errorf("%w: archive digest mismatch", ErrArchiveInvalid)
	}
	return nil
}

func resolveCDNReleasePlatform(release Release) (string, string, error) {
	if ValidateVersion(release.Version) != nil || release.AssetName == "" {
		return "", "", fmt.Errorf("%w: noncanonical selected asset", ErrArchiveInvalid)
	}
	var goos, goarch string
	switch release.AssetName {
	case "CLIProxyAPI_" + release.Version + "_linux_amd64.tar.gz":
		goos, goarch = "linux", "amd64"
	case "CLIProxyAPI_" + release.Version + "_linux_aarch64.tar.gz":
		goos, goarch = "linux", "arm64"
	default:
		return "", "", fmt.Errorf("%w: noncanonical selected asset", ErrArchiveInvalid)
	}
	expectedURL := (&url.URL{
		Scheme: "https",
		Host:   githubHost,
		Path:   "/" + officialRepositoryOwner + "/" + officialRepositoryName + "/releases/download/v" + release.Version + "/" + release.AssetName,
	}).String()
	if release.AssetURL == "" || release.AssetURL != expectedURL {
		return "", "", fmt.Errorf("%w: asset URL differs from official source", ErrArchiveInvalid)
	}
	return goos, goarch, nil
}

// VerifyUploadStream consumes an uploaded .tar.gz archive up to MaxAcquisitionArchiveBytes,
// enforcing exact official release size and SHA-256 digest while capping disk writes at ArchiveSize.
func VerifyUploadStream(ctx context.Context, release Release, src io.Reader, contentLength int64, destination io.Writer, progress ProgressFunc) error {
	if destination == nil || src == nil || ValidateVersion(release.Version) != nil ||
		!canonicalDigest(release.ArchiveDigest) || release.ArchiveSize <= 0 || contentLength < -1 {
		return fmt.Errorf("%w: noncanonical selected asset", ErrArchiveInvalid)
	}
	if release.AssetName != "" || release.AssetURL != "" {
		if _, _, err := resolveCDNReleasePlatform(release); err != nil {
			return err
		}
	}
	if release.ArchiveSize > MaxAcquisitionArchiveBytes {
		return fmt.Errorf("%w: official release archive size %d exceeds %d", ErrArchiveTooLarge, release.ArchiveSize, MaxAcquisitionArchiveBytes)
	}
	if contentLength > MaxAcquisitionArchiveBytes {
		return fmt.Errorf("%w: content length %d exceeds %d", ErrArchiveTooLarge, contentLength, MaxAcquisitionArchiveBytes)
	}
	if contentLength >= 0 && contentLength != release.ArchiveSize {
		return fmt.Errorf("%w: uploaded size differs from release metadata", ErrArchiveInvalid)
	}
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("%w: upload stream interrupted: %w", ErrDownloadFailed, err)
		}
	}
	hasher := sha256.New()
	writer := io.MultiWriter(destination, hasher)
	written, err := copyBoundedArchiveWithMax(ctx, src, writer, release.ArchiveSize, release.ArchiveSize, func(received, _ int64) {
		if progress != nil {
			progress(received, release.ArchiveSize)
		}
	})
	if err != nil {
		if contentLength < 0 && errors.Is(err, ErrArchiveInvalid) && written <= MaxAcquisitionArchiveBytes {
			if probeErr := probeChunkedUploadOverflow(ctx, src, written); probeErr != nil {
				return probeErr
			}
			return err
		}
		if errors.Is(err, ErrArchiveTooLarge) || errors.Is(err, ErrArchiveInvalid) || errors.Is(err, ErrStaging) {
			return err
		}
		return fmt.Errorf("%w: read uploaded archive: %w", ErrDownloadFailed, err)
	}
	if written != release.ArchiveSize {
		if ctx != nil {
			if err := ctx.Err(); err != nil {
				return fmt.Errorf("%w: upload stream interrupted: %w", ErrDownloadFailed, err)
			}
		}
		return fmt.Errorf("%w: uploaded size differs from release metadata", ErrArchiveInvalid)
	}
	actual := "sha256:" + hex.EncodeToString(hasher.Sum(nil))
	if actual != release.ArchiveDigest {
		return fmt.Errorf("%w: archive digest mismatch", ErrArchiveInvalid)
	}
	return nil
}

func probeChunkedUploadOverflow(ctx context.Context, src io.Reader, alreadyRead int64) error {
	remain := int64(MaxAcquisitionArchiveBytes) + 1 - alreadyRead
	if remain <= 0 {
		return fmt.Errorf("%w: archive exceeds %d bytes", ErrArchiveTooLarge, MaxAcquisitionArchiveBytes)
	}
	limited := io.LimitReader(src, remain)
	buf := make([]byte, 32*1024)
	var probed int64
	for {
		if ctx != nil {
			if err := ctx.Err(); err != nil {
				return fmt.Errorf("%w: read uploaded archive: %w", ErrDownloadFailed, err)
			}
		}
		n, readErr := limited.Read(buf)
		if n > 0 {
			probed += int64(n)
			if alreadyRead+probed > MaxAcquisitionArchiveBytes {
				return fmt.Errorf("%w: archive exceeds %d bytes", ErrArchiveTooLarge, MaxAcquisitionArchiveBytes)
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return nil
			}
			var maxBytesErr *http.MaxBytesError
			if errors.As(readErr, &maxBytesErr) {
				return fmt.Errorf("%w: %w", ErrArchiveTooLarge, readErr)
			}
			return fmt.Errorf("%w: read uploaded archive: %w", ErrDownloadFailed, readErr)
		}
	}
}

func copyAndVerifyReleaseStream(ctx context.Context, src io.Reader, release Release, destination io.Writer, progress ProgressFunc, sizeErrMsg string) error {
	if progress != nil {
		progress(0, release.ArchiveSize)
	}
	hasher := sha256.New()
	writer := io.MultiWriter(destination, hasher)
	maxBytes := min(release.ArchiveSize, int64(maxReleaseArchiveBytes))
	limited := io.LimitReader(src, maxBytes+1)
	buf := make([]byte, 32*1024)
	var written int64
	for {
		if ctx != nil {
			if err := ctx.Err(); err != nil {
				return fmt.Errorf("%w: %w", ErrDownloadFailed, err)
			}
		}
		n, readErr := limited.Read(buf)
		if n > 0 {
			next := written + int64(n)
			if next > maxBytes {
				return fmt.Errorf("%w: %s", ErrArchiveInvalid, sizeErrMsg)
			}
			written = next
			if _, writeErr := writer.Write(buf[:n]); writeErr != nil {
				return fmt.Errorf("%w: write release archive: %w", ErrStaging, writeErr)
			}
			if progress != nil {
				progress(written, release.ArchiveSize)
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return fmt.Errorf("%w: read release archive: %w", ErrDownloadFailed, readErr)
		}
	}
	if written != release.ArchiveSize {
		if ctx != nil {
			if err := ctx.Err(); err != nil {
				return fmt.Errorf("%w: download interrupted: %w", ErrDownloadFailed, err)
			}
		}
		return fmt.Errorf("%w: %s", ErrArchiveInvalid, sizeErrMsg)
	}
	actual := "sha256:" + hex.EncodeToString(hasher.Sum(nil))
	if actual != release.ArchiveDigest {
		return fmt.Errorf("%w: archive digest mismatch", ErrArchiveInvalid)
	}
	return nil
}

func canonicalDigest(value string) bool {
	if len(value) != len("sha256:")+sha256.Size*2 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, character := range value[len("sha256:"):] {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func newHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	transport.TLSHandshakeTimeout = 10 * time.Second
	transport.ResponseHeaderTimeout = 30 * time.Second
	transport.ExpectContinueTimeout = time.Second
	return &http.Client{
		Transport: transport,
		Timeout:   ReleaseClientTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if req == nil || req.URL == nil || req.URL.Scheme != "https" || !allowedDownloadHost(req.URL.Hostname()) {
				return errDisallowedGitHubRedirect
			}
			if len(via) > 0 && via[0] != nil && via[0].URL != nil && via[0].URL.Hostname() == githubAPIHost {
				return errors.New("GitHub release metadata redirect is not allowed")
			}
			if len(via) >= 5 {
				return errors.New("too many GitHub release redirects")
			}
			return nil
		},
	}
}

func allowedDownloadHost(host string) bool {
	switch strings.ToLower(host) {
	case githubHost, "release-assets.githubusercontent.com", "objects.githubusercontent.com", "github-releases.githubusercontent.com":
		return true
	default:
		return false
	}
}
