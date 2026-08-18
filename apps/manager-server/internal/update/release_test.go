package update

import (
	"context"
	"errors"
	"io"
	"net/http"
	"runtime"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) Do(req *http.Request) (*http.Response, error) { return f(req) }

func TestReleaseClientSelectsCurrentNativeAsset(t *testing.T) {
	asset := NativeAssetName("v1.12.0", runtime.GOOS, runtime.GOARCH)
	body := `{"tag_name":"v1.12.0","html_url":"https://github.com/seakee/CPA-Manager-Plus/releases/tag/v1.12.0","published_at":"2026-08-12T00:00:00Z","assets":[` +
		`{"name":"` + asset + `","size":123,"browser_download_url":"https://github.com/seakee/CPA-Manager-Plus/releases/download/v1.12.0/` + asset + `","digest":"sha256:abc"},` +
		`{"name":"checksums.txt","browser_download_url":"https://github.com/seakee/CPA-Manager-Plus/releases/download/v1.12.0/checksums.txt"},` +
		`{"name":"update-manifest.json","browser_download_url":"https://github.com/seakee/CPA-Manager-Plus/releases/download/v1.12.0/update-manifest.json"}]}`
	client := ReleaseClient{HTTP: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Header.Get("User-Agent") != "CPA-Manager-Plus" {
			t.Fatalf("User-Agent = %q", req.Header.Get("User-Agent"))
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}

	got, err := client.Check(context.Background(), "v1.11.12")
	if err != nil {
		t.Fatal(err)
	}
	if !got.UpdateAvailable || !got.Comparable || got.Asset.Name != asset {
		t.Fatalf("Check() = %#v", got)
	}
}

func TestDownloadRejectsUntrustedAssetURLBeforeUsingCustomClient(t *testing.T) {
	called := false
	client := roundTripFunc(func(*http.Request) (*http.Response, error) {
		called = true
		return nil, errors.New("must not be called")
	})
	_, err := DownloadBytes(context.Background(), client, ReleaseAsset{
		BrowserDownloadURL: "http://127.0.0.1:29318/package.zip",
		Digest:             "sha256:" + strings.Repeat("0", 64),
	}, 1024)
	if err == nil || called {
		t.Fatalf("DownloadBytes() error = %v called = %v", err, called)
	}
}

func TestReleaseClientRejectsOversizedResponse(t *testing.T) {
	client := ReleaseClient{HTTP: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode:    http.StatusOK,
			ContentLength: maxReleaseBytes + 1,
			Body:          io.NopCloser(strings.NewReader(`{}`)),
		}, nil
	})}
	if _, err := client.Check(context.Background(), "v1.0.0"); err == nil {
		t.Fatal("Check accepted an oversized release response")
	}
}

func TestReleaseClientReportsNonInstallableWithoutManifest(t *testing.T) {
	asset := NativeAssetName("v1.12.0", runtime.GOOS, runtime.GOARCH)
	body := `{"tag_name":"v1.12.0","assets":[` +
		`{"name":"` + asset + `","browser_download_url":"https://github.com/seakee/CPA-Manager-Plus/releases/download/v1.12.0/` + asset + `"},` +
		`{"name":"checksums.txt","browser_download_url":"https://github.com/seakee/CPA-Manager-Plus/releases/download/v1.12.0/checksums.txt"}]}`
	client := ReleaseClient{HTTP: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	got, err := client.Check(context.Background(), "v1.11.12")
	if err != nil {
		t.Fatal(err)
	}
	if got.Installable || got.InstallReason != "release_not_managed_update_ready" || !got.UpdateAvailable {
		t.Fatalf("Check() = %#v", got)
	}
}
