package cpaupdate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

const OfficialLatestReleaseURL = "https://api.github.com/repos/router-for-me/CLIProxyAPI/releases/latest"

const (
	officialReleaseTimeout      = 10 * time.Second
	officialReleaseMaxBodyBytes = 256 << 10
)

type stableReleaseSource interface {
	LatestStable(context.Context) (string, error)
}

// Keep hot updates within the CPA v8 floor qualified for this product release.
var (
	minimumSupportedCPAVersion = stableVersion{parts: [3]string{"8", "0", "55"}}
	errUnsupportedCPAMajor     = errors.New("CPA hot updates require a v8 release >= 8.0.55")
)

func parseSupportedUpdateVersion(version string) (stableVersion, error) {
	parsed, err := parseStableVersion(version)
	if err == nil && (parsed.parts[0] != "8" || compareStableVersions(parsed, minimumSupportedCPAVersion) < 0) {
		err = errUnsupportedCPAMajor
	}
	return parsed, err
}

type officialReleaseSource struct {
	client *http.Client
}

type officialReleaseResponse struct {
	TagName    string `json:"tag_name"`
	Draft      *bool  `json:"draft"`
	Prerelease *bool  `json:"prerelease"`
}

func newOfficialReleaseSource() *officialReleaseSource {
	return &officialReleaseSource{client: &http.Client{
		Timeout: officialReleaseTimeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return errors.New("official CPA release redirects are not allowed")
		},
	}}
}

func (s *officialReleaseSource) LatestStable(ctx context.Context) (string, error) {
	endpoint, err := url.Parse(OfficialLatestReleaseURL)
	if err != nil || endpoint.Scheme != "https" || endpoint.User != nil {
		return "", errors.New("official CPA release source is invalid")
	}
	requestContext, cancel := context.WithTimeout(ctx, officialReleaseTimeout)
	defer cancel()

	request, err := http.NewRequestWithContext(requestContext, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return "", errors.New("create official CPA release request")
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	request.Header.Set("User-Agent", "CPA-Manager-Plus")

	client := s.client
	if client == nil {
		return "", errors.New("official CPA release client is unavailable")
	}
	response, err := client.Do(request)
	if err != nil {
		return "", errors.New("official CPA release request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("official CPA release source returned HTTP %d", response.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(response.Body, officialReleaseMaxBodyBytes+1))
	if err != nil {
		return "", errors.New("read official CPA release response")
	}
	if len(data) > officialReleaseMaxBodyBytes {
		return "", errors.New("official CPA release response is too large")
	}
	var release officialReleaseResponse
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&release); err != nil {
		return "", errors.New("invalid official CPA release JSON")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return "", errors.New("official CPA release response contains trailing JSON")
	}
	if release.Draft == nil || *release.Draft {
		return "", errors.New("official CPA release is draft or missing draft metadata")
	}
	if release.Prerelease == nil || *release.Prerelease {
		return "", errors.New("official CPA release is prerelease or missing prerelease metadata")
	}
	version, err := parseStableReleaseTag(release.TagName)
	if err != nil {
		return "", errors.New("official CPA release tag is not canonical stable semver")
	}
	if _, err := parseSupportedUpdateVersion(version); err != nil {
		return "", err
	}
	return version, nil
}
