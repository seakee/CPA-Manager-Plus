package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"time"
)

const (
	latestReleaseURL = "https://api.github.com/repos/seakee/CPA-Manager-Plus/releases/latest"
	maxReleaseBytes  = 2 * 1024 * 1024
)

type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type ReleaseAsset struct {
	Name               string `json:"name"`
	Size               int64  `json:"size"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Digest             string `json:"digest"`
}

type GitHubRelease struct {
	TagName     string         `json:"tag_name"`
	Name        string         `json:"name"`
	HTMLURL     string         `json:"html_url"`
	PublishedAt string         `json:"published_at"`
	Draft       bool           `json:"draft"`
	Prerelease  bool           `json:"prerelease"`
	Assets      []ReleaseAsset `json:"assets"`
}

type ReleaseCheck struct {
	CurrentVersion  string       `json:"currentVersion"`
	LatestVersion   string       `json:"latestVersion"`
	UpdateAvailable bool         `json:"updateAvailable"`
	Comparable      bool         `json:"comparable"`
	ReleaseURL      string       `json:"releaseUrl"`
	PublishedAt     string       `json:"publishedAt,omitempty"`
	Installable     bool         `json:"installable"`
	InstallReason   string       `json:"installReason,omitempty"`
	Asset           ReleaseAsset `json:"asset"`
	ChecksumsAsset  ReleaseAsset `json:"checksumsAsset"`
	ManifestAsset   ReleaseAsset `json:"manifestAsset"`
}

type ReleaseClient struct {
	HTTP HTTPDoer
	URL  string
}

func (c ReleaseClient) Check(ctx context.Context, currentVersion string) (ReleaseCheck, error) {
	release, err := c.latest(ctx)
	if err != nil {
		return ReleaseCheck{}, err
	}
	if release.Draft || release.Prerelease {
		return ReleaseCheck{}, errors.New("latest stable release response was not a published stable release")
	}
	latestVersion := NormalizeVersion(release.TagName)
	if latestVersion == "" {
		return ReleaseCheck{}, errors.New("latest release has an invalid version tag")
	}
	comparison, comparable := CompareVersions(currentVersion, latestVersion)
	check := ReleaseCheck{
		CurrentVersion:  currentVersion,
		LatestVersion:   latestVersion,
		UpdateAvailable: comparable && comparison > 0,
		Comparable:      comparable,
		ReleaseURL:      release.HTMLURL,
		PublishedAt:     release.PublishedAt,
	}
	assetName := NativeAssetName(latestVersion, runtime.GOOS, runtime.GOARCH)
	asset, assetOK := findReleaseAsset(release.Assets, assetName)
	checksums, checksumsOK := findReleaseAsset(release.Assets, "checksums.txt")
	manifest, manifestOK := findReleaseAsset(release.Assets, "update-manifest.json")
	check.Asset = asset
	check.ChecksumsAsset = checksums
	check.ManifestAsset = manifest
	if !assetOK || !checksumsOK || !manifestOK {
		check.InstallReason = "release_not_managed_update_ready"
		return check, nil
	}
	if asset.Size <= 0 || !validReleaseAssetDigest(asset.Digest) ||
		!validReleaseAssetDigest(checksums.Digest) || !validReleaseAssetDigest(manifest.Digest) {
		check.InstallReason = "release_asset_metadata_invalid"
		return check, nil
	}
	check.Installable = true
	return check, nil
}

func validReleaseAssetDigest(value string) bool {
	return validSHA256(strings.TrimPrefix(strings.TrimSpace(value), "sha256:"))
}

func (c ReleaseClient) latest(ctx context.Context) (GitHubRelease, error) {
	requestURL := strings.TrimSpace(c.URL)
	if requestURL == "" {
		requestURL = latestReleaseURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return GitHubRelease{}, fmt.Errorf("create release request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "CPA-Manager-Plus")
	client := c.HTTP
	if client == nil {
		client = trustedHTTPClient(15 * time.Second)
	}
	res, err := client.Do(req)
	if err != nil {
		return GitHubRelease{}, fmt.Errorf("fetch latest release: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
		return GitHubRelease{}, fmt.Errorf("fetch latest release: %s", res.Status)
	}
	if res.ContentLength > maxReleaseBytes {
		return GitHubRelease{}, errors.New("latest release response exceeded size limit")
	}
	var release GitHubRelease
	decoder := json.NewDecoder(io.LimitReader(res.Body, maxReleaseBytes))
	if err := decoder.Decode(&release); err != nil {
		return GitHubRelease{}, fmt.Errorf("decode latest release: %w", err)
	}
	return release, nil
}

func trustedHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("too many release redirects")
			}
			if !trustedReleaseHost(req.URL) {
				return errors.New("release redirect target is not trusted")
			}
			return nil
		},
	}
}

func trustedReleaseHost(target *url.URL) bool {
	if target == nil || target.Scheme != "https" {
		return false
	}
	host := strings.ToLower(target.Hostname())
	return host == "github.com" || host == "api.github.com" ||
		host == "objects.githubusercontent.com" || strings.HasSuffix(host, ".githubusercontent.com")
}

func NativeAssetName(version, goos, goarch string) string {
	extension := ".tar.gz"
	if goos == "windows" {
		extension = ".zip"
	}
	return fmt.Sprintf("cpa-manager-plus_%s_%s_%s%s", version, goos, goarch, extension)
}

func findReleaseAsset(assets []ReleaseAsset, name string) (ReleaseAsset, bool) {
	for _, asset := range assets {
		if asset.Name == name && trustedAssetURL(asset.BrowserDownloadURL) {
			return asset, true
		}
	}
	return ReleaseAsset{}, false
}

func trustedAssetURL(value string) bool {
	return strings.HasPrefix(value, "https://github.com/seakee/CPA-Manager-Plus/releases/download/")
}
