package cpa

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
)

// External management speaks the CPA v8 contract, so a CPA that advertises v8 or
// later is qualified for writes and anything below it is refused for writes
// while safe reads keep working. A CPA that omits the version header is never
// assumed to be new enough: the class is reported instead of trusted.
const (
	VersionSupportSupported   = "supported"
	VersionSupportUnsupported = "unsupported"
	VersionSupportUnknown     = "unknown"
)

const (
	// ReasonExternalVersionUnsupported is the stable code of every write this
	// Manager refused because the user's CPA did not prove a v8 contract. It
	// covers both an old CPA and one whose version could not be read.
	ReasonExternalVersionUnsupported = "external_cpa_version_unsupported"
)

// ErrExternalVersionUnsupported reports a user's CPA that did not advertise the
// v8 contract this Manager writes. It blocks writes and leaves safe reads alone:
// the configuration is readable, but every write would target a surface CPA does
// not serve.
var ErrExternalVersionUnsupported = errors.New("external cpa version unsupported")

// ExternalVersionReport is the version an External CPA advertised together with
// the class of support this Manager can prove for it. The report never carries a
// credential.
type ExternalVersionReport struct {
	Version      string `json:"version"`
	Support      string `json:"support"`
	WriteBlocked bool   `json:"writeBlocked"`
	Reason       string `json:"reason,omitempty"`
}

// ClassifyExternalVersion maps a CPA version header to its support class. The v8
// major and every later one are the contract this Manager writes; an older major
// is unsupported, and a version this Manager cannot parse is unknown. Both of the
// latter refuse writes, because neither proves the surface a write would target,
// and neither blocks a read.
func ClassifyExternalVersion(version string) ExternalVersionReport {
	report := ExternalVersionReport{Version: strings.TrimSpace(version), Support: VersionSupportUnknown}
	major, ok := majorVersion(report.Version)
	if ok && major >= 8 {
		report.Support = VersionSupportSupported
		return report
	}
	if ok {
		report.Support = VersionSupportUnsupported
	}
	report.WriteBlocked = true
	report.Reason = ReasonExternalVersionUnsupported
	return report
}

// ObserveExternalVersion reads the version a user's CPA advertises through the
// same authenticated management read every other observation uses, and classifies
// it. A CPA that refuses the credential is the typed ErrManagementRejected
// blocker; one that cannot be reached is ErrProofUnavailable. Both are returned
// instead of a classification: neither is evidence about the version.
//
// The probe travels over the same transport every External write uses — no proxy,
// no redirect, one five-second budget — so the hop it judges is the hop a write
// would reach. A probe on a different transport could report a version this
// Manager never writes to.
func ObserveExternalVersion(ctx context.Context, baseURL string, managementKey string) (ExternalVersionReport, error) {
	endpoint, err := NormalizeV8BaseURL(baseURL)
	if err != nil {
		return ExternalVersionReport{}, ErrProofUnavailable
	}
	return NewTransportForEndpoint(endpoint).ObserveVersion(ctx, managementKey)
}

// ObserveVersion is the version probe of one resolved External target, on the
// transport that target is written through.
func (t *Transport) ObserveVersion(ctx context.Context, managementKey string) (ExternalVersionReport, error) {
	version, err := t.observeVersion(ctx, managementKey)
	if err != nil {
		return ExternalVersionReport{}, err
	}
	return ClassifyExternalVersion(version), nil
}

// observeVersion performs the one authenticated read the probe is made of and
// returns the advertised version, which may be empty.
func (t *Transport) observeVersion(ctx context.Context, managementKey string) (string, error) {
	if t == nil || t.client == nil || !validManagementKey(managementKey) {
		return "", ErrProofUnavailable
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, t.endpoint+"/v0/management/config", nil)
	if err != nil {
		return "", ErrProofUnavailable
	}
	request.Header.Set("Authorization", "Bearer "+managementKey)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Cache-Control", "no-store")
	response, err := t.client.Do(request)
	if err != nil {
		return "", ErrProofUnavailable
	}
	defer response.Body.Close()
	switch {
	case response.StatusCode >= 200 && response.StatusCode < 300:
	case response.StatusCode == http.StatusUnauthorized:
		return "", ErrManagementRejected
	case response.StatusCode == http.StatusForbidden:
		// CPA answers 403, not 401, when it refuses this client rather than the
		// credential: a banned address, or remote management disabled for it. It
		// is the same blocker for the caller, kept apart only because a ban
		// expires.
		return "", ErrManagementClientForbidden
	default:
		return "", ErrProofUnavailable
	}
	version := strings.TrimSpace(response.Header.Get("X-CPA-Version"))
	if version == "" {
		version = strings.TrimSpace(response.Header.Get("X-Server-Version"))
	}
	return version, nil
}

// majorVersion reads the leading major number of a CPA version tag. CPA versions
// are tags such as v8.0.13; a bare 8.0.13 is accepted as well.
func majorVersion(version string) (int, bool) {
	trimmed := strings.TrimPrefix(strings.TrimSpace(version), "v")
	if trimmed == "" {
		return 0, false
	}
	end := strings.IndexAny(trimmed, ".-+ ")
	if end < 0 {
		end = len(trimmed)
	}
	major, err := strconv.Atoi(trimmed[:end])
	if err != nil || major <= 0 {
		return 0, false
	}
	return major, true
}
