package cpaupdate

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func sourceWithResponse(t *testing.T, status int, body []byte, requests *atomic.Int32) *officialReleaseSource {
	t.Helper()
	return &officialReleaseSource{client: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests.Add(1)
		if request.URL.String() != OfficialLatestReleaseURL || request.URL.Scheme != "https" {
			t.Fatalf("release request URL = %q", request.URL.String())
		}
		return &http.Response{
			StatusCode: status,
			Header:     make(http.Header),
			Body:       io.NopCloser(bytes.NewReader(body)),
			Request:    request,
		}, nil
	})}}
}

func TestOfficialReleaseSourceAcceptsOnlyCanonicalStableRelease(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "draft", body: `{"tag_name":"v8.3.4","draft":true,"prerelease":false}`},
		{name: "prerelease flag", body: `{"tag_name":"v8.3.4","draft":false,"prerelease":true}`},
		{name: "missing draft flag", body: `{"tag_name":"v8.3.4","prerelease":false}`},
		{name: "missing prerelease flag", body: `{"tag_name":"v8.3.4","draft":false}`},
		{name: "tag without v", body: `{"tag_name":"8.3.4","draft":false,"prerelease":false}`},
		{name: "short tag", body: `{"tag_name":"v8.3","draft":false,"prerelease":false}`},
		{name: "prerelease tag", body: `{"tag_name":"v8.3.4-rc.1","draft":false,"prerelease":false}`},
		{name: "leading zero", body: `{"tag_name":"v8.03.4","draft":false,"prerelease":false}`},
		{name: "trailing JSON", body: `{"tag_name":"v8.3.4","draft":false,"prerelease":false} {}`},
		{name: "v9 release", body: `{"tag_name":"v9.0.0","draft":false,"prerelease":false}`},
		{name: "v7 release", body: `{"tag_name":"v7.3.4","draft":false,"prerelease":false}`},
		{name: "below v8.0.55 floor", body: `{"tag_name":"v8.0.54","draft":false,"prerelease":false}`},
		{name: "below v8.0.55 floor without v", body: `{"tag_name":"8.0.54","draft":false,"prerelease":false}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var requests atomic.Int32
			_, err := sourceWithResponse(t, http.StatusOK, []byte(test.body), &requests).LatestStable(t.Context())
			if err == nil {
				t.Fatal("LatestStable() accepted invalid release")
			}
			if requests.Load() != 1 {
				t.Fatalf("requests = %d, want 1", requests.Load())
			}
		})
	}
}

func TestOfficialReleaseSourceNormalizesTagAndBoundsResponse(t *testing.T) {
	var requests atomic.Int32
	source := sourceWithResponse(
		t,
		http.StatusOK,
		[]byte(`{"tag_name":"v8.3.4","draft":false,"prerelease":false,"assets":[{"browser_download_url":"https://untrusted.invalid/ignored"}]}`),
		&requests,
	)
	version, err := source.LatestStable(t.Context())
	if err != nil || version != "8.3.4" {
		t.Fatalf("LatestStable() = %q, %v", version, err)
	}

	oversized := []byte(strings.Repeat("x", officialReleaseMaxBodyBytes+1))
	_, err = sourceWithResponse(t, http.StatusOK, oversized, &requests).LatestStable(context.Background())
	if err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("oversized response error = %v", err)
	}
	if requests.Load() != 2 {
		t.Fatalf("requests = %d, want 2", requests.Load())
	}
}

func TestOfficialReleaseSourceEnforcesMinimumCPAVersionBoundary(t *testing.T) {
	var requests atomic.Int32
	if _, err := sourceWithResponse(
		t,
		http.StatusOK,
		[]byte(`{"tag_name":"v8.0.54","draft":false,"prerelease":false}`),
		&requests,
	).LatestStable(t.Context()); !errors.Is(err, errUnsupportedCPAMajor) {
		t.Fatalf("LatestStable(v8.0.54) error = %v, want errUnsupportedCPAMajor", err)
	}
	if _, err := sourceWithResponse(
		t,
		http.StatusOK,
		[]byte(`{"tag_name":"8.0.54","draft":false,"prerelease":false}`),
		&requests,
	).LatestStable(t.Context()); err == nil {
		t.Fatal("LatestStable(8.0.54) accepted unprefixed tag below floor")
	}
	for _, validTag := range []struct {
		tag  string
		want string
	}{
		{tag: "v8.0.55", want: "8.0.55"},
		{tag: "v8.0.56", want: "8.0.56"},
	} {
		got, err := sourceWithResponse(
			t,
			http.StatusOK,
			[]byte(`{"tag_name":"`+validTag.tag+`","draft":false,"prerelease":false}`),
			&requests,
		).LatestStable(t.Context())
		if err != nil || got != validTag.want {
			t.Fatalf("LatestStable(%q) = (%q, %v), want (%q, nil)", validTag.tag, got, err, validTag.want)
		}
	}
	if _, err := parseSupportedUpdateVersion("8.0.54"); !errors.Is(err, errUnsupportedCPAMajor) {
		t.Fatalf("parseSupportedUpdateVersion(8.0.54) = %v, want errUnsupportedCPAMajor", err)
	}
	if _, err := parseSupportedUpdateVersion("v8.0.54"); err == nil {
		t.Fatal("parseSupportedUpdateVersion(v8.0.54) accepted v-prefixed version")
	}
	for _, supported := range []string{"8.0.55", "8.0.56"} {
		if _, err := parseSupportedUpdateVersion(supported); err != nil {
			t.Fatalf("parseSupportedUpdateVersion(%q) unexpected error = %v", supported, err)
		}
	}
}
