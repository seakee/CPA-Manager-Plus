package update

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"
)

const (
	// MaxAcquisitionArchiveBytes caps every runtime acquisition transport
	// (official GitHub, CDN prefix, and direct archive upload) at 64 MiB.
	MaxAcquisitionArchiveBytes = 64 << 20
	// OfficialProbeTimeout bounds reachability probes to the official GitHub source.
	OfficialProbeTimeout = 5 * time.Second
	maxCDNRedirects      = 5
)

var (
	ErrInvalidCDNPrefix  = errors.New("CDN prefix is invalid")
	ErrSourceUnreachable = errors.New("official release source is unreachable")
	ErrDownloadFailed    = errors.New("release archive download failed")
	ErrArchiveTooLarge   = errors.New("release archive exceeds size limit")
	ErrArtifactMismatch  = errors.New("extracted executable digest does not match pinned artifact ID")
	ErrArtifactConflict  = errors.New("active or finalized artifact conflicts with pinned artifact ID")

	ipv6GlobalUnicastPrefix  = netip.MustParsePrefix("2000::/3")
	ipv6WellKnownNAT64Prefix = netip.MustParsePrefix("64:ff9b::/96")
	ipv4FakeIPPrefix         = netip.MustParsePrefix("198.18.0.0/15")
	ipv4IANAGlobal192_0_0_9  = netip.MustParseAddr("192.0.0.9")
	ipv4IANAGlobal192_0_0_10 = netip.MustParseAddr("192.0.0.10")
	nonPublicIPv4Prefixes    = []netip.Prefix{
		netip.MustParsePrefix("0.0.0.0/8"),
		netip.MustParsePrefix("10.0.0.0/8"),
		netip.MustParsePrefix("100.64.0.0/10"),
		netip.MustParsePrefix("127.0.0.0/8"),
		netip.MustParsePrefix("169.254.0.0/16"),
		netip.MustParsePrefix("172.16.0.0/12"),
		netip.MustParsePrefix("192.0.0.0/24"),
		netip.MustParsePrefix("192.0.2.0/24"),
		netip.MustParsePrefix("192.88.99.0/24"),
		netip.MustParsePrefix("192.168.0.0/16"),
		netip.MustParsePrefix("198.18.0.0/15"),
		netip.MustParsePrefix("198.51.100.0/24"),
		netip.MustParsePrefix("203.0.113.0/24"),
		netip.MustParsePrefix("224.0.0.0/4"),
		netip.MustParsePrefix("240.0.0.0/4"),
	}

	nonPublicIPv6GlobalPrefixes = []netip.Prefix{
		netip.MustParsePrefix("64:ff9b:1::/48"),
		netip.MustParsePrefix("2001::/23"),
		netip.MustParsePrefix("2001:db8::/32"),
		netip.MustParsePrefix("2002::/16"),
		netip.MustParsePrefix("3fff::/20"),
		netip.MustParsePrefix("5f00::/16"),
	}
)

// ProgressFunc reports byte progress during archive transfer.
type ProgressFunc func(receivedBytes, totalBytes int64)

// CDNAddrPolicy decides whether a resolved or literal IP address is permitted
// as a CDN destination. Production uses IsPublicCDNAddr.
type CDNAddrPolicy func(addr netip.Addr) bool

// CDNLookupIPAddrFunc resolves a hostname to IP addresses prior to dialing.
type CDNLookupIPAddrFunc func(ctx context.Context, host string) ([]net.IPAddr, error)

// CDNSourceOption customizes CDN transport construction (used by tests to
// supply a custom resolver or loopback policy).
type CDNSourceOption func(*cdnSourceConfig)

type cdnSourceConfig struct {
	addrPolicy         CDNAddrPolicy
	redirectAddrPolicy CDNAddrPolicy
	lookupIP           CDNLookupIPAddrFunc
	httpClient         *http.Client
}

// WithCDNAddrPolicy overrides the IP address policy for a CDNSource.
func WithCDNAddrPolicy(policy CDNAddrPolicy) CDNSourceOption {
	return func(cfg *cdnSourceConfig) {
		cfg.addrPolicy = policy
	}
}

// WithCDNRedirectAddrPolicy overrides the literal IP address policy checked on
// original URLs, HTTP redirects, and final URLs independently of dial-time resolution.
func WithCDNRedirectAddrPolicy(policy CDNAddrPolicy) CDNSourceOption {
	return func(cfg *cdnSourceConfig) {
		cfg.redirectAddrPolicy = policy
	}
}

// WithCDNLookupIP overrides the DNS resolver used at dial time by a CDNSource.
func WithCDNLookupIP(lookup CDNLookupIPAddrFunc) CDNSourceOption {
	return func(cfg *cdnSourceConfig) {
		cfg.lookupIP = lookup
	}
}

// WithCDNHTTPClient overrides the HTTP client wrapped by a CDNSource.
func WithCDNHTTPClient(client *http.Client) CDNSourceOption {
	return func(cfg *cdnSourceConfig) {
		cfg.httpClient = client
	}
}

// IsPublicCDNAddr conservatively classifies globally reachable unicast IPv4
// and IPv6 addresses per the IANA IPv4/IPv6 Special-Purpose Address Registries
// (RFC 6890), refusing local-use NAT64 64:ff9b:1::/48 (RFC 8215) and inspecting
// embedded IPv4 addresses in well-known NAT64 64:ff9b::/96 (RFC 6052).
func IsPublicCDNAddr(addr netip.Addr) bool {
	if !addr.IsValid() || addr.Zone() != "" {
		return false
	}
	unmapped := addr.Unmap()
	if unmapped.Is4() {
		return isPublicIPv4(unmapped)
	}
	if ipv6WellKnownNAT64Prefix.Contains(unmapped) {
		raw := unmapped.As16()
		embeddedV4 := netip.AddrFrom4([4]byte{raw[12], raw[13], raw[14], raw[15]})
		return isPublicIPv4(embeddedV4)
	}
	if !ipv6GlobalUnicastPrefix.Contains(unmapped) {
		return false
	}
	for _, prefix := range nonPublicIPv6GlobalPrefixes {
		if prefix.Contains(unmapped) {
			return false
		}
	}
	return true
}

func isPublicIPv4(addr netip.Addr) bool {
	// 192.0.0.9/32 (PCP Anycast) and 192.0.0.10/32 (TURN Anycast) are marked Globally Reachable = True in the IANA IPv4 Special-Purpose Address Registry.
	if addr == ipv4IANAGlobal192_0_0_9 || addr == ipv4IANAGlobal192_0_0_10 {
		return true
	}
	for _, prefix := range nonPublicIPv4Prefixes {
		if prefix.Contains(addr) {
			return false
		}
	}
	return true
}

// AllowLoopbackCDNAddr permits loopback addresses in addition to public
// addresses; intended only for local httptest TLS server tests.
func AllowLoopbackCDNAddr(addr netip.Addr) bool {
	if !addr.IsValid() || addr.Zone() != "" {
		return false
	}
	if addr.Unmap().IsLoopback() {
		return true
	}
	return IsPublicCDNAddr(addr)
}

func isFakeIPv4(addr netip.Addr) bool {
	if !addr.IsValid() || addr.Zone() != "" {
		return false
	}
	unmapped := addr.Unmap()
	return unmapped.Is4() && ipv4FakeIPPrefix.Contains(unmapped)
}

// IsPublicOrFakeIPCDNAddr permits globally public addresses plus RFC 2544
// Fake-IP addresses (198.18.0.0/15) used by transparent TUN proxy DNS.
func IsPublicOrFakeIPCDNAddr(addr netip.Addr) bool {
	return isFakeIPv4(addr) || IsPublicCDNAddr(addr)
}

// CDNAddrPolicyForContext returns IsPublicOrFakeIPCDNAddr when the runtime's
// resolver indicates transparent TUN Fake-IP mode (api.github.com resolves into
// 198.18.0.0/15 without private addresses), and strict IsPublicCDNAddr otherwise.
func CDNAddrPolicyForContext(ctx context.Context, lookup CDNLookupIPAddrFunc) CDNAddrPolicy {
	if ctx == nil {
		ctx = context.Background()
	}
	if isTunnelFakeIPMode(ctx, lookup) {
		return IsPublicOrFakeIPCDNAddr
	}
	return IsPublicCDNAddr
}

func isTunnelFakeIPMode(ctx context.Context, lookup CDNLookupIPAddrFunc) bool {
	if ctx == nil {
		ctx = context.Background()
	}
	if lookup == nil {
		lookup = net.DefaultResolver.LookupIPAddr
	}
	probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	ipAddrs, err := lookup(probeCtx, githubAPIHost)
	if err != nil || len(ipAddrs) == 0 {
		return false
	}
	sawFakeIP := false
	for _, ipAddr := range ipAddrs {
		addr, ok := netip.AddrFromSlice(ipAddr.IP)
		if !ok || ipAddr.Zone != "" {
			return false
		}
		if isFakeIPv4(addr) {
			sawFakeIP = true
			continue
		}
		if !IsPublicCDNAddr(addr) {
			return false
		}
	}
	return sawFakeIP
}

// ValidateCDNPrefix enforces that a user-supplied CDN prefix is an https URL
// with a non-empty public host and without userinfo, query parameters,
// fragments, or dot path traversal segments. It returns the normalized prefix
// without trailing slashes.
func ValidateCDNPrefix(prefix string) (string, error) {
	return ValidateCDNPrefixWithPolicy(prefix, IsPublicCDNAddr)
}

// ValidateCDNPrefixWithPolicy validates prefix using the given IP address policy.
func ValidateCDNPrefixWithPolicy(prefix string, policy CDNAddrPolicy) (string, error) {
	if policy == nil {
		policy = IsPublicCDNAddr
	}
	if prefix == "" || prefix != strings.TrimSpace(prefix) || strings.IndexFunc(prefix, unicode.IsSpace) >= 0 {
		return "", fmt.Errorf("%w: prefix must be a non-empty trimmed URL without whitespace", ErrInvalidCDNPrefix)
	}
	for _, r := range prefix {
		if r < 0x20 || r == 0x7f || r == '\\' || r == '?' || r == '#' || r == '@' {
			return "", fmt.Errorf("%w: prefix contains disallowed character", ErrInvalidCDNPrefix)
		}
	}
	if !strings.HasPrefix(prefix, "https://") {
		return "", fmt.Errorf("%w: scheme must be https", ErrInvalidCDNPrefix)
	}
	parsed, err := url.Parse(prefix)
	if err != nil || parsed == nil {
		return "", fmt.Errorf("%w: parse URL: %v", ErrInvalidCDNPrefix, err)
	}
	if parsed.Scheme != "https" || parsed.Opaque != "" || parsed.User != nil ||
		parsed.Host == "" || parsed.Hostname() == "" || strings.HasSuffix(parsed.Host, ":") ||
		parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.RawFragment != "" {
		return "", fmt.Errorf("%w: prefix must specify https host without userinfo, query, or fragment", ErrInvalidCDNPrefix)
	}
	if err := validateCDNHostLiteral(parsed.Hostname(), policy); err != nil {
		return "", err
	}
	if port := parsed.Port(); port != "" {
		portNum, err := strconv.ParseUint(port, 10, 16)
		if err != nil || portNum == 0 {
			return "", fmt.Errorf("%w: invalid port", ErrInvalidCDNPrefix)
		}
	}
	if parsed.Path != "" {
		trimmedPath := strings.TrimRight(parsed.Path, "/")
		if strings.Contains(trimmedPath, "//") {
			return "", fmt.Errorf("%w: empty path segment is not allowed", ErrInvalidCDNPrefix)
		}
		for _, segment := range strings.Split(trimmedPath, "/") {
			if segment == "." || segment == ".." {
				return "", fmt.Errorf("%w: dot path segments are not allowed", ErrInvalidCDNPrefix)
			}
		}
	}
	trimmed := strings.TrimRight(prefix, "/")
	if trimmed == "https:/" || trimmed == "https:" {
		return "", fmt.Errorf("%w: host is required", ErrInvalidCDNPrefix)
	}
	return trimmed, nil
}

func validateCDNHostLiteral(host string, policy CDNAddrPolicy) error {
	if policy == nil {
		policy = IsPublicCDNAddr
	}
	trimmedHost := strings.TrimSuffix(host, ".")
	if trimmedHost == "" || strings.EqualFold(trimmedHost, "localhost") || strings.HasSuffix(strings.ToLower(trimmedHost), ".localhost") {
		return fmt.Errorf("%w: loopback host is not allowed", ErrInvalidCDNPrefix)
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		if isFakeIPv4(addr) || !policy(addr) {
			return fmt.Errorf("%w: non-public CDN host address %s is not allowed", ErrInvalidCDNPrefix, host)
		}
		return nil
	}
	if ip := net.ParseIP(host); ip != nil {
		addr, ok := netip.AddrFromSlice(ip)
		if !ok || isFakeIPv4(addr) || !policy(addr) {
			return fmt.Errorf("%w: non-public CDN host address %s is not allowed", ErrInvalidCDNPrefix, host)
		}
		return nil
	}
	if strings.ContainsRune(host, ':') {
		return fmt.Errorf("%w: invalid IPv6 host literal %s", ErrInvalidCDNPrefix, host)
	}
	labels := strings.Split(trimmedHost, ".")
	lastLabel := labels[len(labels)-1]
	if lastLabel == "" || isAllDigits(lastLabel) || strings.HasPrefix(strings.ToLower(lastLabel), "0x") {
		return fmt.Errorf("%w: numeric host literal %s is not allowed", ErrInvalidCDNPrefix, host)
	}
	return nil
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// OfficialReleaseAssetURL derives the exact official GitHub release asset URL
// for the current platform and version.
func OfficialReleaseAssetURL(version string) (string, error) {
	return officialReleaseAssetURL(runtime.GOOS, runtime.GOARCH, version)
}

func officialReleaseAssetURL(goos, goarch, version string) (string, error) {
	if err := ValidateVersion(version); err != nil {
		return "", err
	}
	assetSuffix, err := platformAssetSuffix(goos, goarch)
	if err != nil {
		return "", err
	}
	assetName := "CLIProxyAPI_" + version + "_" + assetSuffix
	return (&url.URL{
		Scheme: "https",
		Host:   githubHost,
		Path:   "/" + officialRepositoryOwner + "/" + officialRepositoryName + "/releases/download/v" + version + "/" + assetName,
	}).String(), nil
}

// CDNReleaseAssetURL derives strings.TrimRight(prefix, "/") + "/" + the exact
// official GitHub release asset URL for the current platform and version.
func CDNReleaseAssetURL(prefix, version string) (string, error) {
	return cdnReleaseAssetURLWithPolicy(runtime.GOOS, runtime.GOARCH, prefix, version, IsPublicCDNAddr)
}

func cdnReleaseAssetURL(goos, goarch, prefix, version string) (string, error) {
	return cdnReleaseAssetURLWithPolicy(goos, goarch, prefix, version, IsPublicCDNAddr)
}

func cdnReleaseAssetURLWithPolicy(goos, goarch, prefix, version string, policy CDNAddrPolicy) (string, error) {
	normalizedPrefix, err := ValidateCDNPrefixWithPolicy(prefix, policy)
	if err != nil {
		return "", err
	}
	officialURL, err := officialReleaseAssetURL(goos, goarch, version)
	if err != nil {
		return "", err
	}
	return normalizedPrefix + "/" + officialURL, nil
}

// CDNSource downloads a release archive through a validated HTTPS CDN prefix.
// Trust comes exclusively from the pinned executable artifactId in the image manifest.
type CDNSource struct {
	client             httpDoer
	goos               string
	goarch             string
	prefix             string
	addrPolicy         CDNAddrPolicy
	redirectAddrPolicy CDNAddrPolicy
}

// CloseIdleConnections closes any idle keep-alive connections held by this CDN source's HTTP client.
func (s *CDNSource) CloseIdleConnections() {
	if s == nil || s.client == nil {
		return
	}
	if closer, ok := s.client.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

func NewCDNSource(prefix string, opts ...CDNSourceOption) (*CDNSource, error) {
	return NewCDNSourceForPlatformWithRoots(runtime.GOOS, runtime.GOARCH, prefix, nil, opts...)
}

func NewCDNSourceWithClient(prefix string, client *http.Client, opts ...CDNSourceOption) (*CDNSource, error) {
	return NewCDNSourceForPlatform(runtime.GOOS, runtime.GOARCH, prefix, client, opts...)
}

func NewCDNSourceWithRoots(prefix string, roots *x509.CertPool, opts ...CDNSourceOption) (*CDNSource, error) {
	return NewCDNSourceForPlatformWithRoots(runtime.GOOS, runtime.GOARCH, prefix, roots, opts...)
}

func resolveCDNSourceConfig(opts ...CDNSourceOption) cdnSourceConfig {
	cfg := cdnSourceConfig{
		addrPolicy: IsPublicCDNAddr,
		lookupIP:   net.DefaultResolver.LookupIPAddr,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	if cfg.addrPolicy == nil {
		cfg.addrPolicy = IsPublicCDNAddr
	}
	if cfg.redirectAddrPolicy == nil {
		cfg.redirectAddrPolicy = cfg.addrPolicy
	}
	if cfg.lookupIP == nil {
		cfg.lookupIP = net.DefaultResolver.LookupIPAddr
	}
	return cfg
}

// NewCDNSourceForPlatformWithRoots builds a platform-specific CDN source using
// a single-layer hardened HTTP client with roots and opts applied directly.
func NewCDNSourceForPlatformWithRoots(goos, goarch, prefix string, roots *x509.CertPool, opts ...CDNSourceOption) (*CDNSource, error) {
	cfg := resolveCDNSourceConfig(opts...)
	normalizedPrefix, err := ValidateCDNPrefixWithPolicy(prefix, cfg.redirectAddrPolicy)
	if err != nil {
		return nil, err
	}
	if _, err := platformAssetSuffix(goos, goarch); err != nil {
		return nil, err
	}
	var client *http.Client
	if cfg.httpClient != nil {
		client = wrapCDNClient(cfg.httpClient, cfg)
	} else {
		client = newCDNHTTPClient(roots, cfg)
	}
	return &CDNSource{
		client:             client,
		goos:               goos,
		goarch:             goarch,
		prefix:             normalizedPrefix,
		addrPolicy:         cfg.addrPolicy,
		redirectAddrPolicy: cfg.redirectAddrPolicy,
	}, nil
}

func NewCDNSourceForPlatform(goos, goarch, prefix string, client *http.Client, opts ...CDNSourceOption) (*CDNSource, error) {
	cfg := resolveCDNSourceConfig(opts...)
	normalizedPrefix, err := ValidateCDNPrefixWithPolicy(prefix, cfg.redirectAddrPolicy)
	if err != nil {
		return nil, err
	}
	if _, err := platformAssetSuffix(goos, goarch); err != nil {
		return nil, err
	}
	if client == nil {
		return nil, errors.New("CDN HTTP client is required")
	}
	return &CDNSource{
		client:             wrapCDNClient(client, cfg),
		goos:               goos,
		goarch:             goarch,
		prefix:             normalizedPrefix,
		addrPolicy:         cfg.addrPolicy,
		redirectAddrPolicy: cfg.redirectAddrPolicy,
	}, nil
}

func NewCDNHTTPClient(roots *x509.CertPool) *http.Client {
	return newCDNHTTPClient(roots, resolveCDNSourceConfig())
}

func newCDNHTTPClient(roots *x509.CertPool, cfg cdnSourceConfig) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = newCDNDialContextWithLiteralPolicy(nil, cfg.lookupIP, cfg.addrPolicy, cfg.redirectAddrPolicy)
	transport.TLSHandshakeTimeout = 10 * time.Second
	transport.ResponseHeaderTimeout = 30 * time.Second
	transport.ExpectContinueTimeout = time.Second
	if roots != nil {
		if transport.TLSClientConfig != nil {
			transport.TLSClientConfig = transport.TLSClientConfig.Clone()
		} else {
			transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
		}
		transport.TLSClientConfig.RootCAs = roots
	}
	return &http.Client{
		Transport:     transport,
		Timeout:       ReleaseClientTimeout,
		CheckRedirect: makeCDNRedirectChecker(cfg.redirectAddrPolicy),
	}
}

func wrapCDNClient(client *http.Client, cfg cdnSourceConfig) *http.Client {
	cloned := *client
	if transport, ok := cloned.Transport.(*http.Transport); ok && transport != nil {
		transportClone := transport.Clone()
		transportClone.Proxy = nil
		transportClone.DialContext = newCDNDialContextWithLiteralPolicy(transportClone.DialContext, cfg.lookupIP, cfg.addrPolicy, cfg.redirectAddrPolicy)
		cloned.Transport = transportClone
	} else if cloned.Transport == nil {
		defaultTransport := http.DefaultTransport.(*http.Transport).Clone()
		defaultTransport.Proxy = nil
		defaultTransport.DialContext = newCDNDialContextWithLiteralPolicy(nil, cfg.lookupIP, cfg.addrPolicy, cfg.redirectAddrPolicy)
		cloned.Transport = defaultTransport
	}
	if cloned.Timeout <= 0 {
		cloned.Timeout = ReleaseClientTimeout
	}
	cloned.CheckRedirect = makeCDNRedirectChecker(cfg.redirectAddrPolicy)
	return &cloned
}

func newCDNDialContext(
	baseDial func(ctx context.Context, network, addr string) (net.Conn, error),
	lookup CDNLookupIPAddrFunc,
	policy CDNAddrPolicy,
) func(ctx context.Context, network, address string) (net.Conn, error) {
	return newCDNDialContextWithLiteralPolicy(baseDial, lookup, policy, policy)
}

func newCDNDialContextWithLiteralPolicy(
	baseDial func(ctx context.Context, network, addr string) (net.Conn, error),
	lookup CDNLookupIPAddrFunc,
	policy CDNAddrPolicy,
	literalPolicy CDNAddrPolicy,
) func(ctx context.Context, network, address string) (net.Conn, error) {
	if policy == nil {
		policy = IsPublicCDNAddr
	}
	if literalPolicy == nil {
		literalPolicy = policy
	}
	if lookup == nil {
		lookup = net.DefaultResolver.LookupIPAddr
	}
	dialer := &net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
		Control:   checkCDNSocketControl(policy),
	}
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil || host == "" || port == "" {
			return nil, fmt.Errorf("invalid CDN dial address %q", address)
		}
		if err := validateCDNHostLiteral(host, literalPolicy); err != nil {
			return nil, err
		}
		verifiedAddrs, err := resolveAndVerifyCDNHost(ctx, host, lookup, policy)
		if err != nil {
			return nil, err
		}
		var lastErr error
		for _, addr := range verifiedAddrs {
			target := net.JoinHostPort(addr.String(), port)
			var conn net.Conn
			if baseDial != nil {
				conn, err = baseDial(ctx, network, target)
			} else {
				conn, err = dialer.DialContext(ctx, network, target)
			}
			if err != nil {
				lastErr = err
				continue
			}
			if err := verifyCDNRemoteConnAddr(conn, policy); err != nil {
				_ = conn.Close()
				return nil, err
			}
			return conn, nil
		}
		if lastErr == nil {
			lastErr = errors.New("no verified CDN addresses available")
		}
		return nil, lastErr
	}
}

func resolveAndVerifyCDNHost(
	ctx context.Context,
	host string,
	lookup CDNLookupIPAddrFunc,
	policy CDNAddrPolicy,
) ([]netip.Addr, error) {
	if policy == nil {
		policy = IsPublicCDNAddr
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		if isFakeIPv4(addr) || !policy(addr) {
			return nil, fmt.Errorf("refusing CDN connection to non-public address %s", addr)
		}
		return []netip.Addr{addr.Unmap()}, nil
	}
	if err := validateCDNHostLiteral(host, policy); err != nil {
		return nil, err
	}
	ipAddrs, err := lookup(ctx, host)
	if err != nil {
		return nil, err
	}
	if len(ipAddrs) == 0 {
		return nil, fmt.Errorf("no IP addresses resolved for CDN host %s", host)
	}
	verified := make([]netip.Addr, 0, len(ipAddrs))
	for _, ipAddr := range ipAddrs {
		addr, ok := netip.AddrFromSlice(ipAddr.IP)
		if !ok || ipAddr.Zone != "" || !policy(addr) {
			return nil, fmt.Errorf("refusing CDN connection to non-public resolved address for %s", host)
		}
		verified = append(verified, addr.Unmap())
	}
	return verified, nil
}

func checkCDNSocketControl(policy CDNAddrPolicy) func(network, address string, c syscall.RawConn) error {
	return func(_, address string, _ syscall.RawConn) error {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return fmt.Errorf("invalid CDN socket address %q", address)
		}
		addr, err := netip.ParseAddr(host)
		if err != nil || !policy(addr) {
			return fmt.Errorf("refusing CDN socket connection to non-public address %s", host)
		}
		return nil
	}
}

func verifyCDNRemoteConnAddr(conn net.Conn, policy CDNAddrPolicy) error {
	if conn == nil {
		return errors.New("CDN connection is nil")
	}
	remote := conn.RemoteAddr()
	if remote == nil {
		return nil
	}
	host, _, err := net.SplitHostPort(remote.String())
	if err != nil {
		return nil
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return nil
	}
	if !policy(addr) {
		return fmt.Errorf("refusing CDN connection to non-public remote address %s", addr)
	}
	return nil
}

func makeCDNRedirectChecker(policy CDNAddrPolicy) func(req *http.Request, via []*http.Request) error {
	if policy == nil {
		policy = IsPublicCDNAddr
	}
	return func(req *http.Request, via []*http.Request) error {
		if len(via) > maxCDNRedirects || !validCDNRequestURL(req, policy) {
			return errors.New("unsafe CDN release redirect")
		}
		for _, previous := range via {
			if !validCDNRequestURL(previous, policy) {
				return errors.New("unsafe CDN release redirect")
			}
		}
		return nil
	}
}

func validCDNRequestURL(req *http.Request, policy CDNAddrPolicy) bool {
	if req == nil || req.URL == nil || req.URL.Scheme != "https" ||
		req.URL.Hostname() == "" || req.URL.User != nil {
		return false
	}
	return validateCDNHostLiteral(req.URL.Hostname(), policy) == nil
}

// DownloadArchive fetches the release archive from the CDN prefix into destination,
// enforcing HTTPS, redirect limits, non-public address refusal, and the 64 MiB size cap.
func (s *CDNSource) DownloadArchive(ctx context.Context, version string, destination io.Writer, progress ProgressFunc) error {
	return s.downloadArchiveBounded(ctx, version, 0, destination, progress)
}

func (s *CDNSource) downloadArchiveBounded(ctx context.Context, version string, expectedSize int64, destination io.Writer, progress ProgressFunc) error {
	if s == nil || s.client == nil || destination == nil {
		return fmt.Errorf("%w: CDN source is unavailable", ErrDownloadFailed)
	}
	defer s.CloseIdleConnections()
	urlPolicy := s.redirectAddrPolicy
	if urlPolicy == nil {
		urlPolicy = s.addrPolicy
	}
	if urlPolicy == nil {
		urlPolicy = IsPublicCDNAddr
	}
	targetURL, err := cdnReleaseAssetURLWithPolicy(s.goos, s.goarch, s.prefix, version, urlPolicy)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return fmt.Errorf("%w: create CDN request: %v", ErrDownloadFailed, err)
	}
	req.Header.Set("Accept", assetAccept)
	req.Header.Set("User-Agent", userAgent)
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: CDN download request: %w", ErrDownloadFailed, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !validCDNRequestURL(resp.Request, urlPolicy) {
		return fmt.Errorf("%w: CDN download returned HTTP %d", ErrDownloadFailed, resp.StatusCode)
	}
	if resp.ContentLength > MaxAcquisitionArchiveBytes {
		return fmt.Errorf("%w: content length %d exceeds %d", ErrArchiveTooLarge, resp.ContentLength, MaxAcquisitionArchiveBytes)
	}
	if expectedSize > 0 && resp.ContentLength >= 0 && resp.ContentLength != expectedSize {
		return fmt.Errorf("%w: CDN download size differs from release metadata", ErrArchiveInvalid)
	}
	maxBytes := int64(MaxAcquisitionArchiveBytes)
	totalBytes := resp.ContentLength
	if expectedSize > 0 {
		maxBytes = min(expectedSize, int64(MaxAcquisitionArchiveBytes))
		totalBytes = expectedSize
	}
	written, err := copyBoundedArchiveWithMax(ctx, resp.Body, destination, totalBytes, maxBytes, progress)
	if err != nil {
		if errors.Is(err, ErrArchiveTooLarge) || errors.Is(err, ErrArchiveInvalid) || errors.Is(err, ErrStaging) {
			return err
		}
		return fmt.Errorf("%w: read CDN response body: %w", ErrDownloadFailed, err)
	}
	if expectedSize > 0 && written != expectedSize {
		return fmt.Errorf("%w: CDN download size differs from release metadata", ErrArchiveInvalid)
	}
	if resp.ContentLength >= 0 && written != resp.ContentLength {
		return fmt.Errorf("%w: CDN download size differs from Content-Length", ErrDownloadFailed)
	}
	if written == 0 {
		return fmt.Errorf("%w: CDN download returned empty archive", ErrArchiveInvalid)
	}
	return nil
}

// NewSourceWithClient creates an official GitHub release source with a custom HTTP client
// while retaining the official redirect allowlist and proxy exclusion.
func NewSourceWithClient(client *http.Client) (*Source, error) {
	return NewSourceForPlatform(runtime.GOOS, runtime.GOARCH, client)
}

// NewSourceWithRoots creates an official GitHub release source trusting the given root CAs.
func NewSourceWithRoots(roots *x509.CertPool) (*Source, error) {
	return NewSourceForPlatform(runtime.GOOS, runtime.GOARCH, NewOfficialHTTPClient(roots))
}

// NewSourceForPlatform creates an official GitHub release source for the specified platform.
func NewSourceForPlatform(goos, goarch string, client *http.Client) (*Source, error) {
	if client == nil {
		return nil, errors.New("release HTTP client is required")
	}
	return newSource(goos, goarch, wrapOfficialClient(client))
}

func NewOfficialHTTPClient(roots *x509.CertPool) *http.Client {
	client := newHTTPClient()
	if roots != nil {
		transport := client.Transport.(*http.Transport).Clone()
		if transport.TLSClientConfig != nil {
			transport.TLSClientConfig = transport.TLSClientConfig.Clone()
		} else {
			transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
		}
		transport.TLSClientConfig.RootCAs = roots
		client.Transport = transport
	}
	return client
}

func wrapOfficialClient(client *http.Client) *http.Client {
	base := newHTTPClient()
	cloned := *client
	if transport, ok := cloned.Transport.(*http.Transport); ok && transport != nil {
		transportClone := transport.Clone()
		transportClone.Proxy = nil
		cloned.Transport = transportClone
	}
	if cloned.Timeout <= 0 {
		cloned.Timeout = ReleaseClientTimeout
	}
	cloned.CheckRedirect = base.CheckRedirect
	return &cloned
}

// Probe verifies reachability of the exact official GitHub release asset within a 5s bound.
func (s *Source) Probe(ctx context.Context, version string) error {
	if s == nil || s.client == nil {
		return fmt.Errorf("%w: official source is unavailable", ErrSourceUnreachable)
	}
	if err := ValidateVersion(version); err != nil {
		return err
	}
	assetName := "CLIProxyAPI_" + version + "_" + s.asset
	expectedURL := (&url.URL{
		Scheme: "https",
		Host:   githubHost,
		Path:   "/" + officialRepositoryOwner + "/" + officialRepositoryName + "/releases/download/v" + version + "/" + assetName,
	}).String()
	probeCtx, cancel := context.WithTimeout(ctx, OfficialProbeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, expectedURL, nil)
	if err != nil {
		return fmt.Errorf("%w: create probe request: %v", ErrSourceUnreachable, err)
	}
	req.Header.Set("Accept", assetAccept)
	req.Header.Set("User-Agent", userAgent)
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: official source probe failed: %v", ErrSourceUnreachable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Request == nil || resp.Request.URL == nil ||
		resp.Request.URL.Scheme != "https" || !allowedDownloadHost(resp.Request.URL.Hostname()) {
		return fmt.Errorf("%w: official source probe returned HTTP %d", ErrSourceUnreachable, resp.StatusCode)
	}
	return nil
}

// DownloadArchive downloads the official GitHub release asset for version into destination,
// enforcing the official host allowlist and the 64 MiB size cap.
func (s *Source) DownloadArchive(ctx context.Context, version string, destination io.Writer, progress ProgressFunc) error {
	if s == nil || s.client == nil || destination == nil {
		return fmt.Errorf("%w: official source is unavailable", ErrDownloadFailed)
	}
	if err := ValidateVersion(version); err != nil {
		return err
	}
	assetName := "CLIProxyAPI_" + version + "_" + s.asset
	expectedURL := (&url.URL{
		Scheme: "https",
		Host:   githubHost,
		Path:   "/" + officialRepositoryOwner + "/" + officialRepositoryName + "/releases/download/v" + version + "/" + assetName,
	}).String()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, expectedURL, nil)
	if err != nil {
		return fmt.Errorf("%w: create official download request: %v", ErrDownloadFailed, err)
	}
	req.Header.Set("Accept", assetAccept)
	req.Header.Set("User-Agent", userAgent)
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: official download request: %v", ErrDownloadFailed, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Request == nil || resp.Request.URL == nil ||
		resp.Request.URL.Scheme != "https" || !allowedDownloadHost(resp.Request.URL.Hostname()) {
		return fmt.Errorf("%w: official download returned HTTP %d", ErrDownloadFailed, resp.StatusCode)
	}
	if resp.ContentLength > MaxAcquisitionArchiveBytes {
		return fmt.Errorf("%w: content length %d exceeds %d", ErrArchiveTooLarge, resp.ContentLength, MaxAcquisitionArchiveBytes)
	}
	written, err := copyBoundedArchiveWithMax(ctx, resp.Body, destination, resp.ContentLength, MaxAcquisitionArchiveBytes, progress)
	if err != nil {
		if errors.Is(err, ErrArchiveTooLarge) || errors.Is(err, ErrStaging) {
			return err
		}
		return fmt.Errorf("%w: read official download body: %w", ErrDownloadFailed, err)
	}
	if resp.ContentLength >= 0 && written != resp.ContentLength {
		return fmt.Errorf("%w: official download size differs from Content-Length", ErrDownloadFailed)
	}
	if written == 0 {
		return fmt.Errorf("%w: official download returned empty archive", ErrArchiveInvalid)
	}
	return nil
}

func copyBoundedArchive(src io.Reader, dst io.Writer, contentLength int64, progress ProgressFunc) (int64, error) {
	return copyBoundedArchiveWithMax(context.Background(), src, dst, contentLength, MaxAcquisitionArchiveBytes, progress)
}

func copyBoundedArchiveWithMax(
	ctx context.Context,
	src io.Reader,
	dst io.Writer,
	contentLength int64,
	maxBytes int64,
	progress ProgressFunc,
) (int64, error) {
	if contentLength > MaxAcquisitionArchiveBytes {
		return 0, fmt.Errorf("%w: content length %d exceeds %d", ErrArchiveTooLarge, contentLength, MaxAcquisitionArchiveBytes)
	}
	if maxBytes <= 0 || maxBytes > MaxAcquisitionArchiveBytes {
		maxBytes = MaxAcquisitionArchiveBytes
	}
	if contentLength > maxBytes {
		return 0, fmt.Errorf("%w: content length %d exceeds expected %d", ErrArchiveInvalid, contentLength, maxBytes)
	}
	var totalBytes int64
	if contentLength > 0 {
		totalBytes = contentLength
	}
	writeLimit := maxBytes
	if contentLength >= 0 && contentLength < writeLimit {
		writeLimit = contentLength
	}
	if progress != nil {
		progress(0, totalBytes)
	}
	limited := io.LimitReader(src, min(writeLimit+1, int64(MaxAcquisitionArchiveBytes)+1))
	buf := make([]byte, 32*1024)
	var received int64
	for {
		if ctx != nil {
			if err := ctx.Err(); err != nil {
				return received, err
			}
		}
		n, readErr := limited.Read(buf)
		if n > 0 {
			next := received + int64(n)
			if next > MaxAcquisitionArchiveBytes {
				return next, fmt.Errorf("%w: archive exceeds %d bytes", ErrArchiveTooLarge, MaxAcquisitionArchiveBytes)
			}
			if next > writeLimit {
				if maxBytes < MaxAcquisitionArchiveBytes && next > maxBytes {
					return next, fmt.Errorf("%w: archive exceeds expected %d bytes", ErrArchiveInvalid, maxBytes)
				}
				return next, nil
			}
			received = next
			if _, writeErr := dst.Write(buf[:n]); writeErr != nil {
				if errors.Is(writeErr, ErrArchiveTooLarge) {
					return received, writeErr
				}
				return received, fmt.Errorf("%w: write archive: %w", ErrStaging, writeErr)
			}
			if progress != nil {
				progress(received, totalBytes)
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			var maxBytesErr *http.MaxBytesError
			if errors.As(readErr, &maxBytesErr) {
				return received, fmt.Errorf("%w: %w", ErrArchiveTooLarge, readErr)
			}
			return received, readErr
		}
	}
	return received, nil
}
