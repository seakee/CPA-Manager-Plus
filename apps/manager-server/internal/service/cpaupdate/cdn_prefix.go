package cpaupdate

import (
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"unicode"
)

// Keep this request admission check aligned with the Runtime Protocol's
// ValidateCDNPrefix. Only Supervisor resolves DNS and authorizes socket dials.
func validMutationCDNPrefix(prefix string) bool {
	prefix = strings.TrimSpace(prefix)
	if len(prefix) > 512 || !strings.HasPrefix(prefix, "https://") ||
		strings.ContainsAny(prefix, "\\?#@") || strings.IndexFunc(prefix, func(r rune) bool {
		return r < 0x20 || r == 0x7f || unicode.IsSpace(r)
	}) >= 0 {
		return false
	}
	parsed, err := url.Parse(prefix)
	if err != nil || parsed.Hostname() == "" || strings.HasSuffix(parsed.Host, ":") || parsed.User != nil {
		return false
	}
	if port := parsed.Port(); port != "" {
		if n, err := strconv.ParseUint(port, 10, 16); err != nil || n == 0 {
			return false
		}
	}
	if !validMutationCDNHost(parsed.Hostname()) {
		return false
	}
	path := strings.TrimRight(parsed.Path, "/")
	if strings.Contains(path, "//") {
		return false
	}
	for segment := range strings.SplitSeq(path, "/") {
		if segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

func validMutationCDNHost(host string) bool {
	if addr, err := netip.ParseAddr(host); err == nil {
		return publicMutationCDNAddr(addr)
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.ContainsRune(host, ':') {
		return false
	}
	last := host[strings.LastIndexByte(host, '.')+1:]
	return last != "" && !strings.HasPrefix(last, "0x") && strings.Trim(last, "0123456789") != ""
}

var (
	mutationCDNNAT64           = netip.MustParsePrefix("64:ff9b::/96")
	mutationCDNIPv6Global      = netip.MustParsePrefix("2000::/3")
	mutationCDNIPv4AnycastPCP  = netip.MustParseAddr("192.0.0.9")
	mutationCDNIPv4AnycastTURN = netip.MustParseAddr("192.0.0.10")
	mutationCDNBlockedLiterals = []netip.Prefix{
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
		netip.MustParsePrefix("64:ff9b:1::/48"),
		netip.MustParsePrefix("2001::/23"),
		netip.MustParsePrefix("2001:db8::/32"),
		netip.MustParsePrefix("2002::/16"),
		netip.MustParsePrefix("3fff::/20"),
		netip.MustParsePrefix("5f00::/16"),
	}
)

func publicMutationCDNAddr(addr netip.Addr) bool {
	if addr.Zone() != "" {
		return false
	}
	addr = addr.Unmap()
	if mutationCDNNAT64.Contains(addr) {
		raw := addr.As16()
		addr = netip.AddrFrom4([4]byte{raw[12], raw[13], raw[14], raw[15]})
	}
	if !addr.Is4() && !mutationCDNIPv6Global.Contains(addr) {
		return false
	}
	if addr == mutationCDNIPv4AnycastPCP || addr == mutationCDNIPv4AnycastTURN {
		return true
	}
	for _, blocked := range mutationCDNBlockedLiterals {
		if blocked.Contains(addr) {
			return false
		}
	}
	return true
}
