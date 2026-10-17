package freshrouting

import (
	"bytes"
	"context"
	"encoding/json"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

)

const (
	InternalPath  = "/internal/routing/fresh"
	AuthorityPath = "/internal/routing/fresh-authority"
)

type HTTPConfig struct {
	IngressControlURL string
	PublicOrigin      string
	Client            *http.Client
}
type HTTPDriver struct {
	control string
	public  string
	client  *http.Client
}

func NewHTTPDriver(c HTTPConfig) (*HTTPDriver, error) {
	validOrigin := func(s string, internal bool) bool {
		u, e := url.Parse(s)
		return e == nil && u.Host != "" && u.User == nil && (u.Path == "" || u.Path == "/") && u.RawQuery == "" && u.Fragment == "" && (u.Scheme == "http" || (!internal && u.Scheme == "https"))
	}
	if !validOrigin(c.IngressControlURL, true) || !validOrigin(c.PublicOrigin, false) {
		return nil, ErrBlocked
	}
	client := c.Client
	if client == nil {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.Proxy = nil
		client = &http.Client{Transport: transport, Timeout: 10 * time.Second}
	}
	copyClient := *client
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &HTTPDriver{control: strings.TrimRight(c.IngressControlURL, "/"), public: strings.TrimRight(c.PublicOrigin, "/"), client: &copyClient}, nil
}
func (d *HTTPDriver) controlRequest(ctx context.Context, method string, state *State) (State, error) {
	var body io.Reader
	if state != nil {
		data, _ := json.Marshal(state)
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, d.control+InternalPath, body)
	if err != nil {
		return State{}, ErrBlocked
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cache-Control", "no-store")
	response, err := d.client.Do(req)
	if err != nil {
		return State{}, ErrBlocked
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusConflict {
		return State{}, ErrConflict
	}
	if response.StatusCode != http.StatusOK {
		return State{}, ErrBlocked
	}
	var result State
	if json.NewDecoder(io.LimitReader(response.Body, 8192)).Decode(&result) != nil {
		return State{}, ErrBlocked
	}
	return result, nil
}
func (d *HTTPDriver) Inspect(ctx context.Context) (State, error) {
	return d.controlRequest(ctx, http.MethodGet, nil)
}
func (d *HTTPDriver) Apply(ctx context.Context, s State) error {
	actual, e := d.controlRequest(ctx, http.MethodPut, &s)
	if e == nil && actual != s {
		return ErrConflict
	}
	return e
}

var initialAsset = regexp.MustCompile(`(?:src|href)=["']([^"']+/assets/[^"']+-[A-Za-z0-9_-]{8,}\.(?:js|css))["']`)

func (d *HTTPDriver) get(ctx context.Context, p string, html bool) (*http.Response, []byte, error) {
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, d.public+p, nil)
	if e != nil {
		return nil, nil, ErrUnverified
	}
	if html {
		req.Header.Set("Accept", "text/html")
	}
	res, e := d.client.Do(req)
	if e != nil {
		return nil, nil, ErrUnverified
	}
	defer res.Body.Close()
	body, e := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if e != nil {
		return nil, nil, ErrUnverified
	}
	return res, body, nil
}
func (d *HTTPDriver) Verify(ctx context.Context, s State) error {
	res, body, e := d.get(ctx, s.Target, true)
	if e != nil || res.StatusCode != 200 || res.Header.Get("X-CPAMP-Route-Owner") != "manager" || !strings.Contains(res.Header.Get("Content-Type"), "text/html") || !bytes.Contains(body, []byte(`id="root"`)) {
		return ErrUnverified
	}
	assets := initialAsset.FindAllSubmatch(body, -1)
	if len(assets) == 0 {
		return ErrUnverified
	}
	for _, asset := range assets {
		p := html.UnescapeString(string(asset[1]))
		if !strings.HasPrefix(p, s.Target+"/assets/") {
			return ErrUnverified
		}
		res, data, e := d.get(ctx, p, false)
		if e != nil || res.StatusCode != 200 || res.Header.Get("X-CPAMP-Route-Owner") != "manager" || len(data) == 0 || strings.Contains(res.Header.Get("Content-Type"), "text/html") {
			return ErrUnverified
		}
	}
	res, body, e = d.get(ctx, s.Target+"/api/v2/bootstrap/identity", false)
	if e != nil || res.StatusCode != 200 || res.Header.Get("X-CPAMP-Route-Owner") != "manager" {
		return ErrUnverified
	}
	var identity State
	if json.Unmarshal(body, &identity) != nil {
		return ErrUnverified
	}
	identity.Committed = s.Committed
	if identity != s {
		return ErrUnverified
	}
	// A segment lookalike must retain Gateway ownership, with its model path
	// closed while Fresh has not proved data-plane configuration.
	res, _, e = d.get(ctx, s.Target+"-gateway-probe/v1/models", false)
	if e != nil || res.StatusCode != http.StatusServiceUnavailable || res.Header.Get("X-CPAMP-Route-Owner") != "gateway" {
		return ErrUnverified
	}
	return nil
}
