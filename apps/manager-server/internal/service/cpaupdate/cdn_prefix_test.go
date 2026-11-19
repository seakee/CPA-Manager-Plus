package cpaupdate

import (
	"net/netip"
	"strings"
	"testing"
)

func TestMutationCDNPrefix(t *testing.T) {
	for _, prefix := range []string{
		" https://cdn.example.com/mirror/ ",
		"https://cdn.example.com:443/mirror",
		"https://8.8.8.8/mirror",
		"https://[2606:4700::1111]/mirror",
	} {
		if !validMutationCDNPrefix(prefix) {
			t.Errorf("rejected valid prefix %q", prefix)
		}
	}
	for _, prefix := range []string{
		"https://localhost./", "https://x.localhost/", "https://127.0.0.1/",
		"https://[::ffff:127.0.0.1]/", "https://[64:ff9b::7f00:1]/", "https://[64:ff9b:1::1]/", "https://[64:ff9b:1:ffff::1]/",
		"https://[5f00::1]/", "https://[5f00:ffff::1]/", "https://198.18.0.1/",
		"https://192.168.1.1/", "https://100.64.1.1/", "https://[fc00::1]/", "https://[fe80::1%25en0]/",
		"https://2130706433/", "https://0x7f000001/", "https://127.1/",
		"https://cdn.example.com:/", "https://cdn.example.com:0/", "https://cdn.example.com:65536/",
		"https://cdn.example.com/../x", "https://cdn.example.com/%2e/x", "https://cdn.example.com/a/%2f/b",
		"https://cdn.example.com/a b", "https://cdn.example.com/a\\b", "https://cdn.example.com/?", "https://cdn.example.com/#",
		"https://cdn.example.com/" + strings.Repeat("a", 512),
	} {
		if validMutationCDNPrefix(prefix) {
			t.Errorf("accepted invalid prefix %q", prefix)
		}
	}
}

func TestMutationCDNBlockedLiteralsIncludeNAT64LocalAndSRv6(t *testing.T) {
	for _, want := range []netip.Prefix{
		netip.MustParsePrefix("64:ff9b:1::/48"),
		netip.MustParsePrefix("5f00::/16"),
	} {
		found := false
		for _, got := range mutationCDNBlockedLiterals {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("mutationCDNBlockedLiterals missing %s", want)
		}
	}
}
