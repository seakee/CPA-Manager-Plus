package freshadopt

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
)

// maxCredentialBytes bounds one credential file. An auth file carries tokens,
// never media, so 1 MiB is far above any real file and still a hard ceiling.
const maxCredentialBytes = 1 << 20

// maxConfigBytes bounds one configuration read or write body.
const maxConfigBytes = 8 << 20

// wire is one authenticated CPA v8 Management API endpoint.
//
// Its transport is the management safe-read client itself, not a copy of it:
// no proxy, one fixed DNS resolution, no redirect following, no retry, a bounded
// header/body budget and a five-second timeout per call. The copy that used to
// live here called itself "the same posture" while allowing 30 seconds and
// re-resolving DNS on every call; reusing the real one is what makes the claim
// true, and it binds this package to the transport the External and adoption
// reads are already qualified on.
type wire struct {
	baseURL string
	key     string
	client  *http.Client
	close   func()
}

func newWire(ctx context.Context, endpoint Endpoint) (*wire, error) {
	base := cpa.NormalizeBaseURL(endpoint.BaseURL)
	parsed, err := url.Parse(base)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, ErrEndpointInvalid
	}
	if strings.TrimSpace(endpoint.ManagementKey) == "" {
		return nil, ErrEndpointInvalid
	}
	client, closeClient, err := cpa.NewDirectClient(ctx, base)
	if err != nil {
		return nil, ErrEndpointInvalid
	}
	return &wire{baseURL: strings.TrimRight(base, "/"), key: endpoint.ManagementKey, client: client, close: closeClient}, nil
}

// Close releases the connections of this endpoint's fixed resolution. It is
// safe to call more than once.
func (w *wire) Close() {
	if w != nil && w.close != nil {
		w.close()
	}
}

// ErrEndpointInvalid rejects an unusable source or destination endpoint.
var ErrEndpointInvalid = fmt.Errorf("freshadopt: invalid CPA endpoint")

// do issues exactly one request and never retries. A transport error is reported
// as unknown: the caller's only remedy is a read-back.
func (w *wire) do(ctx context.Context, method, path string, payload []byte, limit int64) (int, []byte, error) {
	var body io.Reader
	if payload != nil {
		body = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, w.baseURL+path, body)
	if err != nil {
		return 0, nil, cpa.ErrProofUnavailable
	}
	req.Header.Set("Authorization", "Bearer "+w.key)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("Cache-Control", "no-store")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := w.client.Do(req)
	if err != nil {
		return 0, nil, cpa.ErrApplyUnknown
	}
	defer resp.Body.Close()
	if encoding := resp.Header.Get("Content-Encoding"); encoding != "" && encoding != "identity" {
		return 0, nil, cpa.ErrProofUnavailable
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || int64(len(data)) > limit {
		return 0, nil, cpa.ErrApplyUnknown
	}
	return resp.StatusCode, data, nil
}

// SourceWire is the read-only source client. It has no write method at all, so
// the executor cannot mutate the source even by mistake.
type SourceWire struct{ w *wire }

// NewSource builds the read-only source of one migration run. The caller must
// Close it when the run is over.
func NewSource(ctx context.Context, endpoint Endpoint) (*SourceWire, error) {
	w, err := newWire(ctx, endpoint)
	if err != nil {
		return nil, err
	}
	return &SourceWire{w: w}, nil
}

// Close releases the source client's connections.
func (s *SourceWire) Close() {
	if s != nil {
		s.w.Close()
	}
}

func (s *SourceWire) ListCredentialFiles(ctx context.Context) ([]CredentialFile, error) {
	code, body, err := s.w.do(ctx, http.MethodGet, "/v8/management/credentials", nil, maxConfigBytes)
	if err != nil {
		return nil, err
	}
	if code != http.StatusOK {
		return nil, fmt.Errorf("freshadopt: source credential list refused with %d", code)
	}
	return decodeCredentialList(body)
}

func decodeCredentialList(body []byte) ([]CredentialFile, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var response struct {
		Files []struct {
			Name        string `json:"name"`
			Type        string `json:"type"`
			Disabled    bool   `json:"disabled"`
			RuntimeOnly bool   `json:"runtime_only"`
		} `json:"files"`
	}
	if err := decoder.Decode(&response); err != nil {
		return nil, fmt.Errorf("freshadopt: malformed credential list: %w", err)
	}
	files := make([]CredentialFile, 0, len(response.Files))
	for _, file := range response.Files {
		name := strings.TrimSpace(file.Name)
		if name == "" {
			continue
		}
		files = append(files, CredentialFile{
			Name: name, Type: strings.TrimSpace(file.Type),
			Disabled: file.Disabled, RuntimeOnly: file.RuntimeOnly,
		})
	}
	return files, nil
}

func (s *SourceWire) DownloadCredential(ctx context.Context, name string) ([]byte, error) {
	code, body, err := s.w.do(ctx, http.MethodGet, "/v8/management/credentials/download?name="+url.QueryEscape(name), nil, maxCredentialBytes)
	if err != nil {
		return nil, err
	}
	if code != http.StatusOK {
		return nil, fmt.Errorf("freshadopt: source credential %q refused with %d", name, code)
	}
	return body, nil
}

func (s *SourceWire) ReadConfigDocument(ctx context.Context) (map[string]any, error) {
	code, body, err := s.w.do(ctx, http.MethodGet, "/v8/management/config", nil, maxConfigBytes)
	if err != nil {
		return nil, err
	}
	if code != http.StatusOK {
		return nil, fmt.Errorf("freshadopt: source config refused with %d", code)
	}
	return cpa.DecodeConfigDocument(body)
}

// DestinationWire is the write and read-back client of the destination.
type DestinationWire struct{ w *wire }

// NewDestination builds the write client of one migration run. The caller must
// Close it when the run is over.
func NewDestination(ctx context.Context, endpoint Endpoint) (*DestinationWire, error) {
	w, err := newWire(ctx, endpoint)
	if err != nil {
		return nil, err
	}
	return &DestinationWire{w: w}, nil
}

// Close releases the destination client's connections.
func (d *DestinationWire) Close() {
	if d != nil {
		d.w.Close()
	}
}

func (d *DestinationWire) ListCredentialFiles(ctx context.Context) ([]CredentialFile, error) {
	code, body, err := d.w.do(ctx, http.MethodGet, "/v8/management/credentials", nil, maxConfigBytes)
	if err != nil {
		return nil, err
	}
	if code != http.StatusOK {
		return nil, fmt.Errorf("freshadopt: destination credential list refused with %d", code)
	}
	return decodeCredentialList(body)
}

func (d *DestinationWire) DownloadCredential(ctx context.Context, name string) ([]byte, error) {
	code, body, err := d.w.do(ctx, http.MethodGet, "/v8/management/credentials/download?name="+url.QueryEscape(name), nil, maxCredentialBytes)
	if err != nil {
		return nil, err
	}
	if code != http.StatusOK {
		return nil, fmt.Errorf("freshadopt: destination credential %q absent", name)
	}
	return body, nil
}

// CredentialState reports the destination's own listing entry for one file. A
// file the destination does not list is reported absent rather than as an error:
// absence is the answer that decides an unknown upload.
func (d *DestinationWire) CredentialState(ctx context.Context, name string) (CredentialState, error) {
	files, err := d.ListCredentialFiles(ctx)
	if err != nil {
		return CredentialState{}, err
	}
	for _, file := range files {
		if file.Name == name {
			return CredentialState{Present: true, Type: file.Type, Disabled: file.Disabled}, nil
		}
	}
	return CredentialState{}, nil
}

// UploadCredential posts the raw file body. CPA accepts a non-multipart body with
// `?name=` (`UploadAuthFile` in internal/api/handlers/management/auth_files_crud.go:110-135).
//
// CPA's answer for a body it cannot parse is a 500, not a 400: `writeAuthFile`
// calls `buildAuthFromFileData`, whose JSON/synthesis failure is returned to the
// handler as `500 {"error": "invalid auth file: ..."}`
// (`auth_files_crud.go:125-128,482-508`). A 400 is only a bad `name` query
// parameter or an unreadable body, which this client never sends. So both are
// reported as CPA's own verdict on the file, and the caller decides from what
// the destination holds afterwards.
func (d *DestinationWire) UploadCredential(ctx context.Context, name string, data []byte) error {
	code, body, err := d.w.do(ctx, http.MethodPost, "/v8/management/credentials?name="+url.QueryEscape(name), data, maxConfigBytes)
	if err != nil {
		return err
	}
	switch code {
	case http.StatusOK:
		return nil
	case http.StatusBadRequest:
		// A bad name or an unreadable body: no resend can succeed either.
		return fmt.Errorf("%w: %s", ErrCredentialNotUploadable, credentialRefusal(body))
	case http.StatusInternalServerError:
		// CPA's answer when it cannot take the file: `writeAuthFile` returns the
		// parse or upsert failure and the handler reports it as 500. It is a
		// verdict on the file only if the file did not land, which the caller
		// decides from the read-back.
		return fmt.Errorf("%w: %s", errCredentialServerRefusal, credentialRefusal(body))
	default:
		return cpa.ErrApplyUnknown
	}
}

// errCredentialServerRefusal reports CPA's 500 on an upload. Unlike the 400, it
// does not by itself prove the file was not stored: the caller reads the
// destination back and treats a missing file as CPA's verdict on the bytes.
var errCredentialServerRefusal = errors.New("freshadopt: credential upload refused by CPA")

// credentialRefusal keeps only the bounded code of CPA's refusal message.
func credentialRefusal(body []byte) string {
	var refused struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &refused) != nil || strings.TrimSpace(refused.Error) == "" {
		return "refused"
	}
	message := strings.TrimSpace(refused.Error)
	if len(message) > 80 {
		message = message[:80]
	}
	return message
}

func (d *DestinationWire) SetCredentialDisabled(ctx context.Context, name string, disabled bool) error {
	payload, err := json.Marshal(struct {
		Name     string `json:"name"`
		Disabled bool   `json:"disabled"`
	}{Name: name, Disabled: disabled})
	if err != nil {
		return cpa.ErrProofUnavailable
	}
	code, _, err := d.w.do(ctx, http.MethodPatch, "/v8/management/credentials/status", payload, maxConfigBytes)
	if err != nil {
		return err
	}
	if code != http.StatusOK {
		return cpa.ErrApplyUnknown
	}
	return nil
}

// PatchConfigPath merges one leaf below its page-group prefix.
func (d *DestinationWire) PatchConfigPath(ctx context.Context, prefix string, payload []byte) error {
	code, body, err := d.w.do(ctx, http.MethodPatch, configEndpoint(prefix), payload, maxConfigBytes)
	return classifyConfigWrite(code, body, err)
}

// PutConfigValue replaces one whole section or rule family.
func (d *DestinationWire) PutConfigValue(ctx context.Context, path string, payload []byte) error {
	code, body, err := d.w.do(ctx, http.MethodPut, configEndpoint(path), payload, maxConfigBytes)
	return classifyConfigWrite(code, body, err)
}

// ReadConfigValue reads one dotted path back from the destination. A 404 is the
// absent state: CPA serves not_found for a key that is not set.
func (d *DestinationWire) ReadConfigValue(ctx context.Context, path string) (any, cpa.ConfigPresence, error) {
	code, body, err := d.w.do(ctx, http.MethodGet, configEndpoint(path), nil, maxConfigBytes)
	if err != nil {
		return nil, "", err
	}
	switch code {
	case http.StatusNotFound:
		return nil, cpa.ConfigMissing, nil
	case http.StatusOK:
		decoder := json.NewDecoder(bytes.NewReader(body))
		decoder.UseNumber()
		var value any
		if err := decoder.Decode(&value); err != nil {
			return nil, "", cpa.ErrSourceConflict
		}
		if value == nil {
			return nil, cpa.ConfigNull, nil
		}
		return value, cpa.ConfigPresent, nil
	default:
		return nil, "", cpa.ErrProofUnavailable
	}
}

func configEndpoint(path string) string {
	return "/v8/management/config/" + strings.ReplaceAll(path, ".", "/")
}

// classifyConfigWrite turns one CPA configuration write answer into the shared
// error vocabulary: an acknowledged write is success, a definite refusal is
// CPA's own verdict, and anything else leaves the effect unknown.
func classifyConfigWrite(code int, body []byte, err error) error {
	if err != nil {
		return err
	}
	switch code {
	case http.StatusOK, http.StatusNoContent:
		return nil
	}
	var refused struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(body, &refused)
	code4xx := code >= 400 && code < 500
	if !code4xx {
		return cpa.ErrApplyUnknown
	}
	stable := strings.TrimSpace(refused.Error)
	if stable == "" {
		stable = "refused"
	}
	if len(stable) > 64 {
		stable = stable[:64]
	}
	// The message is intentionally dropped: CPA can quote the offending YAML
	// value in it, and that value may be a secret.
	return cpa.ConfigWriteError{Status: code, Code: stable}
}
