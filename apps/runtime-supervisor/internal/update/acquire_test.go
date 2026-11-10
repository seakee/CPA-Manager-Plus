package update

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/artifact"
)

func TestValidateCDNPrefixTable(t *testing.T) {
	valid := []struct {
		input string
		want  string
	}{
		{input: "https://cdn.example.com", want: "https://cdn.example.com"},
		{input: "https://cdn.example.com/", want: "https://cdn.example.com"},
		{input: "https://cdn.example.com/gh", want: "https://cdn.example.com/gh"},
		{input: "https://cdn.example.com/gh///", want: "https://cdn.example.com/gh"},
		{input: "https://8.8.8.8/mirror/", want: "https://8.8.8.8/mirror"},
		{input: "https://1.1.1.1:8443/mirror/cpa/", want: "https://1.1.1.1:8443/mirror/cpa"},
		{input: "https://[2606:4700:4700::1111]/mirror/", want: "https://[2606:4700:4700::1111]/mirror"},
		{input: "https://[64:ff9b::808:808]/mirror/", want: "https://[64:ff9b::808:808]/mirror"},
	}
	for _, tc := range valid {
		got, err := ValidateCDNPrefix(tc.input)
		if err != nil || got != tc.want {
			t.Fatalf("ValidateCDNPrefix(%q) = %q, %v; want %q, nil", tc.input, got, err, tc.want)
		}
	}

	invalid := []string{
		"",
		"   ",
		" https://cdn.example.com",
		"https://cdn.example.com ",
		"http://cdn.example.com",
		"ftp://cdn.example.com",
		"HTTPS://cdn.example.com",
		"https://",
		"https:///",
		"https:///mirror",
		"https://:443",
		"https://cdn.example.com:",
		"https://cdn.example.com:0",
		"https://cdn.example.com:99999",
		"https://user@cdn.example.com",
		"https://user:pass@cdn.example.com",
		"https://cdn.example.com?token=1",
		"https://cdn.example.com?",
		"https://cdn.example.com#section",
		"https://cdn.example.com#",
		"https://cdn.example.com/../mirror",
		"https://cdn.example.com/./mirror",
		"https://cdn.example.com//mirror",
		`https://cdn.example.com\mirror`,
		"https://cdn.example.com/with space",
		"https://cdn.example.com/with\x00nul",
		"https://localhost/mirror",
		"https://sub.localhost/mirror",
		"https://127.0.0.1",
		"https://127.0.0.1:8443/mirror",
		"https://127.255.255.254/mirror",
		"https://[::1]/mirror",
		"https://[::ffff:127.0.0.1]/mirror",
		"https://0.0.0.0/mirror",
		"https://0.1.2.3/mirror",
		"https://[::]/mirror",
		"https://[::ffff:0.0.0.0]/mirror",
		"https://169.254.169.254/mirror",
		"https://[fe80::1]/mirror",
		"https://[::ffff:169.254.169.254]/mirror",
		"https://224.0.0.1/mirror",
		"https://239.255.255.250/mirror",
		"https://[ff02::1]/mirror",
		"https://[ff05::1]/mirror",
		"https://[::ffff:224.0.0.1]/mirror",
		"https://10.0.0.1/mirror",
		"https://172.16.0.1/mirror",
		"https://172.31.255.254/mirror",
		"https://192.168.1.1/mirror",
		"https://[fc00::1]/mirror",
		"https://[fd12:3456:789a::1]/mirror",
		"https://[::ffff:10.0.0.1]/mirror",
		"https://[::ffff:192.168.1.1]/mirror",
		"https://100.64.0.1/mirror",
		"https://100.127.255.254/mirror",
		"https://[::ffff:100.64.0.1]/mirror",
		"https://192.0.0.170/mirror",
		"https://192.0.2.1/mirror",
		"https://192.88.99.1/mirror",
		"https://198.18.0.1/mirror",
		"https://198.51.100.1/mirror",
		"https://203.0.113.1/mirror",
		"https://255.255.255.255/mirror",
		"https://[64:ff9b::a00:1]/mirror",
		"https://[64:ff9b:1:aaaa:a:0:808:808]/mirror",
		"https://[2001::1]/mirror",
		"https://[2001:db8::1]/mirror",
		"https://[2002::1]/mirror",
		"https://[3fff::1]/mirror",
		"https://[5f00::1]/mirror",
		"https://[100::1]/mirror",
	}
	for _, input := range invalid {
		if got, err := ValidateCDNPrefix(input); !errors.Is(err, ErrInvalidCDNPrefix) {
			t.Fatalf("ValidateCDNPrefix(%q) = %q, %v; want ErrInvalidCDNPrefix", input, got, err)
		}
	}
}

func TestIsPublicCDNAddrRegistryTable(t *testing.T) {
	publicAddrs := []string{
		"8.8.8.8",
		"1.1.1.1",
		"93.184.216.34",
		"192.0.0.9",
		"192.0.0.10",
		"2606:4700:4700::1111",
		"64:ff9b::808:808",
		"::ffff:8.8.8.8",
	}
	for _, raw := range publicAddrs {
		addr := netip.MustParseAddr(raw)
		if !IsPublicCDNAddr(addr) {
			t.Fatalf("IsPublicCDNAddr(%s) = false, want true", raw)
		}
	}

	nonPublicAddrs := []string{
		"0.0.0.0",
		"10.0.0.1",
		"100.64.0.1",
		"127.0.0.1",
		"169.254.169.254",
		"172.16.0.1",
		"192.0.0.170",
		"192.0.2.1",
		"192.88.99.1",
		"192.168.1.1",
		"198.18.0.1",
		"198.51.100.1",
		"203.0.113.1",
		"224.0.0.1",
		"240.0.0.1",
		"255.255.255.255",
		"::",
		"::1",
		"::ffff:127.0.0.1",
		"::ffff:192.0.0.170",
		"::ffff:198.18.0.1",
		"100::1",
		"64:ff9b::a00:1",
		"64:ff9b::c612:1",
		"64:ff9b:1:aaaa:a:0:808:808",
		"2001::1",
		"2001:2::1",
		"2001:db8::1",
		"2002::1",
		"3fff::1",
		"5f00::1",
		"fc00::1",
		"fd00::1",
		"fe80::1",
		"ff02::1",
	}
	for _, raw := range nonPublicAddrs {
		addr := netip.MustParseAddr(raw)
		if IsPublicCDNAddr(addr) {
			t.Fatalf("IsPublicCDNAddr(%s) = true, want false", raw)
		}
	}
	if IsPublicCDNAddr(netip.MustParseAddr("2606:4700:4700::1111").WithZone("eth0")) {
		t.Fatal("zoned address must be refused")
	}
}

func TestCDNReleaseAssetURLDerivationPreservesOfficialHostAllowlist(t *testing.T) {
	if allowedDownloadHost("cdn.example.com") || allowedDownloadHost("127.0.0.1") {
		t.Fatal("official allowedDownloadHost was widened")
	}
	got, err := cdnReleaseAssetURL("linux", "amd64", "https://cdn.example.com/gh/", "8.0.55")
	if err != nil {
		t.Fatalf("cdnReleaseAssetURL() error = %v", err)
	}
	want := "https://cdn.example.com/gh/https://github.com/router-for-me/CLIProxyAPI/releases/download/v8.0.55/CLIProxyAPI_8.0.55_linux_amd64.tar.gz"
	if got != want {
		t.Fatalf("cdnReleaseAssetURL() = %q, want %q", got, want)
	}

	gotArm, err := cdnReleaseAssetURL("linux", "arm64", "https://cdn.example.com", "8.0.55")
	if err != nil {
		t.Fatalf("cdnReleaseAssetURL(arm64) error = %v", err)
	}
	wantArm := "https://cdn.example.com/https://github.com/router-for-me/CLIProxyAPI/releases/download/v8.0.55/CLIProxyAPI_8.0.55_linux_aarch64.tar.gz"
	if gotArm != wantArm {
		t.Fatalf("cdnReleaseAssetURL(arm64) = %q, want %q", gotArm, wantArm)
	}
}

func TestOfficialAndCDNTransportsOverTLSServers(t *testing.T) {
	binary := []byte("#!/bin/sh\necho s2a-cpa-binary\n")
	license := []byte("MIT License\nCopyright (c) CLIProxyAPI\n")
	archive := tarGzip(t,
		archiveEntry{name: stagedExecutableName, typeFlag: tar.TypeReg, data: binary},
		archiveEntry{name: stagedLicenseName, typeFlag: tar.TypeReg, data: license},
	)

	officialPath := "/router-for-me/CLIProxyAPI/releases/download/v8.0.55/CLIProxyAPI_8.0.55_linux_amd64.tar.gz"
	officialServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case officialPath:
			http.Redirect(w, r, "https://release-assets.githubusercontent.com/s2a-asset", http.StatusFound)
		case "/s2a-asset":
			if r.Host != "release-assets.githubusercontent.com" {
				http.Error(w, "unexpected host", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Length", strconv.Itoa(len(archive)))
			_, _ = w.Write(archive)
		default:
			http.NotFound(w, r)
		}
	}))
	defer officialServer.Close()

	officialClient := newTLSServerClientForOfficialHosts(officialServer)
	officialSource, err := NewSourceForPlatform("linux", "amd64", officialClient)
	if err != nil {
		t.Fatal(err)
	}
	if err := officialSource.Probe(t.Context(), "8.0.55"); err != nil {
		t.Fatalf("official Probe() error = %v", err)
	}
	var officialBuf bytes.Buffer
	var officialProgress int64
	if err := officialSource.DownloadArchive(t.Context(), "8.0.55", &officialBuf, func(received, _ int64) {
		officialProgress = received
	}); err != nil {
		t.Fatalf("official DownloadArchive() error = %v", err)
	}
	if !bytes.Equal(officialBuf.Bytes(), archive) || officialProgress != int64(len(archive)) {
		t.Fatalf("official downloaded %d bytes (progress %d), want %d", officialBuf.Len(), officialProgress, len(archive))
	}

	var cdnServerURL string
	cdnServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wantInitial := "/mirror/https://github.com" + officialPath
		switch r.URL.Path {
		case wantInitial:
			http.Redirect(w, r, cdnServerURL+"/hop/1", http.StatusFound)
		case "/hop/1":
			http.Redirect(w, r, cdnServerURL+"/hop/2", http.StatusFound)
		case "/hop/2":
			http.Redirect(w, r, cdnServerURL+"/hop/3", http.StatusFound)
		case "/hop/3":
			http.Redirect(w, r, cdnServerURL+"/hop/4", http.StatusFound)
		case "/hop/4":
			http.Redirect(w, r, cdnServerURL+"/hop/5", http.StatusFound)
		case "/hop/5":
			w.Header().Set("Content-Length", strconv.Itoa(len(archive)))
			_, _ = w.Write(archive)
		case "/http-redirect/https://github.com" + officialPath:
			http.Redirect(w, r, "http://127.0.0.1/plain", http.StatusFound)
		case "/too-many-redirects/https://github.com" + officialPath:
			http.Redirect(w, r, cdnServerURL+"/loop/1", http.StatusFound)
		case "/loop/1", "/loop/2", "/loop/3", "/loop/4", "/loop/5", "/loop/6":
			step, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/loop/"))
			http.Redirect(w, r, fmt.Sprintf("%s/loop/%d", cdnServerURL, step+1), http.StatusFound)
		case "/oversize/https://github.com" + officialPath:
			w.Header().Set("Content-Length", strconv.FormatInt(MaxAcquisitionArchiveBytes+1, 10))
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer cdnServer.Close()
	cdnServerURL = cdnServer.URL

	cdnSource, err := NewCDNSourceForPlatform("linux", "amd64", cdnServer.URL+"/mirror/", cdnServer.Client(), WithCDNAddrPolicy(AllowLoopbackCDNAddr))
	if err != nil {
		t.Fatal(err)
	}
	var cdnBuf bytes.Buffer
	if err := cdnSource.DownloadArchive(t.Context(), "8.0.55", &cdnBuf, nil); err != nil {
		t.Fatalf("CDN DownloadArchive() error = %v", err)
	}
	if !bytes.Equal(cdnBuf.Bytes(), archive) {
		t.Fatal("CDN downloaded archive bytes differ")
	}

	httpRedirectSource, err := NewCDNSourceForPlatform("linux", "amd64", cdnServer.URL+"/http-redirect", cdnServer.Client(), WithCDNAddrPolicy(AllowLoopbackCDNAddr))
	if err != nil {
		t.Fatal(err)
	}
	if err := httpRedirectSource.DownloadArchive(t.Context(), "8.0.55", io.Discard, nil); !errors.Is(err, ErrDownloadFailed) {
		t.Fatalf("http redirect DownloadArchive() error = %v, want ErrDownloadFailed", err)
	}

	tooManySource, err := NewCDNSourceForPlatform("linux", "amd64", cdnServer.URL+"/too-many-redirects", cdnServer.Client(), WithCDNAddrPolicy(AllowLoopbackCDNAddr))
	if err != nil {
		t.Fatal(err)
	}
	if err := tooManySource.DownloadArchive(t.Context(), "8.0.55", io.Discard, nil); !errors.Is(err, ErrDownloadFailed) {
		t.Fatalf("excessive redirects DownloadArchive() error = %v, want ErrDownloadFailed", err)
	}

	oversizeSource, err := NewCDNSourceForPlatform("linux", "amd64", cdnServer.URL+"/oversize", cdnServer.Client(), WithCDNAddrPolicy(AllowLoopbackCDNAddr))
	if err != nil {
		t.Fatal(err)
	}
	if err := oversizeSource.DownloadArchive(t.Context(), "8.0.55", io.Discard, nil); !errors.Is(err, ErrArchiveTooLarge) {
		t.Fatalf("oversize DownloadArchive() error = %v, want ErrArchiveTooLarge", err)
	}
}

func TestStoreAcquireStagesVerifiedArtifactAndLicenseAndHandlesConflicts(t *testing.T) {
	binary := []byte("#!/bin/sh\necho exact-s2a-binary\n")
	binarySum := sha256.Sum256(binary)
	wantArtifactID := artifact.ID("sha256:" + hex.EncodeToString(binarySum[:]))
	license := []byte("MIT License\nCPA Runtime License Text\n")
	archive := tarGzip(t,
		archiveEntry{name: "README.md", typeFlag: tar.TypeReg, data: []byte("ignored readme")},
		archiveEntry{name: stagedExecutableName, typeFlag: tar.TypeReg, data: binary},
		archiveEntry{name: stagedLicenseName, typeFlag: tar.TypeReg, data: license},
	)
	archiveSum := sha256.Sum256(archive)
	wantArchiveDigest := "sha256:" + hex.EncodeToString(archiveSum[:])

	root := t.TempDir()
	store, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}

	var downloads int
	var phases []string
	firstStage, err := store.AcquireStage(
		t.Context(),
		"8.0.55",
		wantArtifactID,
		func(_ context.Context, dst io.Writer, progress ProgressFunc) error {
			downloads++
			if progress != nil {
				progress(int64(len(archive)), int64(len(archive)))
			}
			_, writeErr := dst.Write(archive)
			return writeErr
		},
		nil,
		func(phase string) { phases = append(phases, phase) },
	)
	if err != nil {
		t.Fatalf("AcquireStage() error = %v", err)
	}
	metadata := firstStage.Metadata
	if !firstStage.CreatedStage || downloads != 1 || metadata.ArtifactID != wantArtifactID ||
		metadata.SourceArchiveDigest != wantArchiveDigest || metadata.Version != "8.0.55" {
		t.Fatalf("AcquireStage() created=%v downloads=%d metadata=%#v", firstStage.CreatedStage, downloads, metadata)
	}
	if strings.Join(phases, ",") != "downloading,verifying,installing" {
		t.Fatalf("AcquireStage() phases = %v", phases)
	}

	finalDir := filepath.Join(root, "8.0.55")
	assertMode(t, filepath.Join(finalDir, stagedExecutableName), 0o755)
	assertMode(t, filepath.Join(finalDir, stagedLicenseName), 0o444)
	assertMode(t, filepath.Join(finalDir, stagedMetadataName), 0o444)
	gotLicense, err := os.ReadFile(filepath.Join(finalDir, stagedLicenseName))
	if err != nil || !bytes.Equal(gotLicense, license) {
		t.Fatalf("staged LICENSE = %q, %v", gotLicense, err)
	}
	resolved, err := store.ResolveFinalized("8.0.55")
	if err != nil || resolved.Metadata != metadata {
		t.Fatalf("ResolveFinalized() = %#v, %v", resolved, err)
	}

	// Even when a matching finalized stage already exists, Store.Acquire must
	// still consume and verify the archive stream; a tampered archive must fail
	// with ErrArtifactMismatch.
	tamperedArchive := tarGzip(t,
		archiveEntry{name: stagedExecutableName, typeFlag: tar.TypeReg, data: []byte("#!/bin/sh\necho tampered\n")},
		archiveEntry{name: stagedLicenseName, typeFlag: tar.TypeReg, data: license},
	)
	if _, err := store.AcquireStage(
		t.Context(),
		"8.0.55",
		wantArtifactID,
		func(_ context.Context, dst io.Writer, _ ProgressFunc) error {
			downloads++
			_, writeErr := dst.Write(tamperedArchive)
			return writeErr
		},
		nil,
		nil,
	); !errors.Is(err, ErrArtifactMismatch) || downloads != 2 {
		t.Fatalf("tampered AcquireStage() against existing stage = %v, downloads=%d; want ErrArtifactMismatch, 2", err, downloads)
	}

	// Second call with a verified archive reuses the existing finalized stage with CreatedStage=false.
	reusedStage, err := store.AcquireStage(
		t.Context(),
		"8.0.55",
		wantArtifactID,
		func(_ context.Context, dst io.Writer, _ ProgressFunc) error {
			downloads++
			_, writeErr := dst.Write(archive)
			return writeErr
		},
		nil,
		nil,
	)
	if err != nil || reusedStage.CreatedStage || reusedStage.Metadata != metadata || downloads != 3 {
		t.Fatalf("reused AcquireStage() = %#v, downloads=%d, err=%v", reusedStage, downloads, err)
	}

	// Conflicting artifactId for the same version is refused before downloading.
	otherID := artifact.ID("sha256:" + strings.Repeat("b", 64))
	if _, err := store.Acquire(
		t.Context(),
		"8.0.55",
		otherID,
		func(context.Context, io.Writer, ProgressFunc) error {
			downloads++
			return nil
		},
		nil,
		nil,
	); !errors.Is(err, ErrArtifactConflict) || downloads != 3 {
		t.Fatalf("conflicting Acquire() error = %v, downloads=%d; want ErrArtifactConflict", err, downloads)
	}
	gotBinary, err := os.ReadFile(filepath.Join(finalDir, stagedExecutableName))
	if err != nil || !bytes.Equal(gotBinary, binary) {
		t.Fatalf("existing finalized executable was modified: %q, %v", gotBinary, err)
	}
}

func TestStoreAcquireRejectsInvalidArchivesAndCleansTemporaryState(t *testing.T) {
	binary := []byte("expected-cpa-binary")
	binarySum := sha256.Sum256(binary)
	wantArtifactID := artifact.ID("sha256:" + hex.EncodeToString(binarySum[:]))
	license := []byte("MIT License\n")

	tests := []struct {
		name    string
		rawBody []byte
		entries []archiveEntry
		wantErr error
	}{
		{
			name: "tampered executable",
			entries: []archiveEntry{
				{name: stagedExecutableName, typeFlag: tar.TypeReg, data: []byte("different-cpa-binary")},
				{name: stagedLicenseName, typeFlag: tar.TypeReg, data: license},
			},
			wantErr: ErrArtifactMismatch,
		},
		{
			name: "missing LICENSE",
			entries: []archiveEntry{
				{name: stagedExecutableName, typeFlag: tar.TypeReg, data: binary},
			},
			wantErr: ErrArchiveInvalid,
		},
		{
			name: "missing executable",
			entries: []archiveEntry{
				{name: stagedLicenseName, typeFlag: tar.TypeReg, data: license},
			},
			wantErr: ErrArchiveInvalid,
		},
		{
			name: "path traversal",
			entries: []archiveEntry{
				{name: "../outside", typeFlag: tar.TypeReg, data: []byte("x")},
				{name: stagedExecutableName, typeFlag: tar.TypeReg, data: binary},
				{name: stagedLicenseName, typeFlag: tar.TypeReg, data: license},
			},
			wantErr: ErrArchiveInvalid,
		},
		{
			name: "symlink member",
			entries: []archiveEntry{
				{name: stagedExecutableName, typeFlag: tar.TypeSymlink, linkName: "/bin/sh"},
				{name: stagedLicenseName, typeFlag: tar.TypeReg, data: license},
			},
			wantErr: ErrArchiveInvalid,
		},
		{
			name: "hardlink member",
			entries: []archiveEntry{
				{name: stagedExecutableName, typeFlag: tar.TypeLink, linkName: "other"},
				{name: stagedLicenseName, typeFlag: tar.TypeReg, data: license},
			},
			wantErr: ErrArchiveInvalid,
		},
		{
			name: "oversized binary member",
			entries: []archiveEntry{
				{name: stagedLicenseName, typeFlag: tar.TypeReg, data: license},
				{name: stagedExecutableName, typeFlag: tar.TypeReg, declaredSize: maxExtractedBinaryBytes + 1},
			},
			wantErr: ErrArchiveInvalid,
		},
		{
			name: "oversized license member",
			entries: []archiveEntry{
				{name: stagedExecutableName, typeFlag: tar.TypeReg, data: binary},
				{name: stagedLicenseName, typeFlag: tar.TypeReg, declaredSize: maxExtractedLicenseBytes + 1},
			},
			wantErr: ErrArchiveInvalid,
		},
		{
			name:    "CDN returns HTML instead of gzip",
			rawBody: []byte("<!DOCTYPE html><html><body>Gateway error</body></html>"),
			wantErr: ErrArchiveInvalid,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			store, err := NewStore(root)
			if err != nil {
				t.Fatal(err)
			}
			body := tc.rawBody
			if body == nil {
				body = tarGzip(t, tc.entries...)
			}
			_, err = store.Acquire(
				t.Context(),
				"8.0.55",
				wantArtifactID,
				CopyUploadArchive(bytes.NewReader(body), int64(len(body))),
				nil,
				nil,
			)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Acquire() error = %v, want %v", err, tc.wantErr)
			}
			dirEntries, readErr := os.ReadDir(root)
			if readErr != nil || len(dirEntries) != 0 {
				t.Fatalf("stage root not clean after failure: %v, %v", dirEntries, readErr)
			}
		})
	}
}

func TestCDNRefusesNonPublicDialTargetsAndLoopbackRedirects(t *testing.T) {
	officialPath := "/router-for-me/CLIProxyAPI/releases/download/v8.0.55/CLIProxyAPI_8.0.55_linux_amd64.tar.gz"
	var serverURL string
	tlsServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect-loopback/https://github.com"+officialPath {
			http.Redirect(w, r, serverURL+"/loopback-target", http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer tlsServer.Close()
	serverURL = tlsServer.URL

	nonPublicIPs := []string{
		"127.0.0.1",
		"10.0.0.1",
		"172.16.0.1",
		"192.168.1.1",
		"100.64.0.1",
		"169.254.169.254",
		"0.0.0.0",
		"::1",
		"fc00::1",
		"fe80::1",
		"::ffff:127.0.0.1",
		"::ffff:10.0.0.1",
		"192.0.0.170",
		"198.18.0.1",
		"198.51.100.1",
		"203.0.113.1",
		"64:ff9b:1:aaaa:a:0:808:808",
		"64:ff9b::a00:1",
		"2001::1",
		"2001:db8::1",
		"2002::1",
		"3fff::1",
		"5f00::1",
	}
	for _, rawIP := range nonPublicIPs {
		parsedIP := net.ParseIP(rawIP)
		if parsedIP == nil {
			t.Fatalf("failed to parse test IP %q", rawIP)
		}
		source, err := NewCDNSourceForPlatform(
			"linux",
			"amd64",
			"https://cdn.example.com/mirror",
			tlsServer.Client(),
			WithCDNLookupIP(func(context.Context, string) ([]net.IPAddr, error) {
				return []net.IPAddr{{IP: parsedIP}}, nil
			}),
		)
		if err != nil {
			t.Fatalf("NewCDNSourceForPlatform(%s) error = %v", rawIP, err)
		}
		if err := source.DownloadArchive(t.Context(), "8.0.55", io.Discard, nil); !errors.Is(err, ErrDownloadFailed) {
			t.Fatalf("DownloadArchive(resolved %s) error = %v, want ErrDownloadFailed", rawIP, err)
		}
	}

	// Redirect from a public hostname to https://127.0.0.1 is refused under the default strict policy.
	redirectClient := newTLSServerClientForOfficialHosts(tlsServer)
	redirectSource, err := NewCDNSourceForPlatform(
		"linux",
		"amd64",
		"https://cdn.example.com/redirect-loopback",
		redirectClient,
		WithCDNLookupIP(func(context.Context, string) ([]net.IPAddr, error) {
			return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, nil
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := redirectSource.DownloadArchive(t.Context(), "8.0.55", io.Discard, nil); !errors.Is(err, ErrDownloadFailed) {
		t.Fatalf("redirect to https://127.0.0.1 DownloadArchive() error = %v, want ErrDownloadFailed", err)
	}

	// Representative public DNS resolutions (including 64:ff9b::808:808) pass dial-time verification.
	for _, publicIP := range []string{"8.8.8.8", "1.1.1.1", "2606:4700:4700::1111", "64:ff9b::808:808"} {
		verified, err := resolveAndVerifyCDNHost(
			t.Context(),
			"cdn.example.com",
			func(context.Context, string) ([]net.IPAddr, error) {
				return []net.IPAddr{{IP: net.ParseIP(publicIP)}}, nil
			},
			IsPublicCDNAddr,
		)
		if err != nil || len(verified) != 1 {
			t.Fatalf("resolveAndVerifyCDNHost(%s) = %v, %v; want 1 verified address", publicIP, verified, err)
		}
	}
}

func TestStoreAcquireBackfillsMissingLicenseOnPreparedStage(t *testing.T) {
	binary := []byte("#!/bin/sh\necho prepared-then-acquired-cpa\n")
	binarySum := sha256.Sum256(binary)
	wantArtifactID := artifact.ID("sha256:" + hex.EncodeToString(binarySum[:]))
	license := []byte("MIT License\nBackfilled CPA License\n")

	archiveWithoutLicense := tarGzip(t,
		archiveEntry{name: stagedExecutableName, typeFlag: tar.TypeReg, data: binary},
	)
	archiveWithoutLicenseSum := sha256.Sum256(archiveWithoutLicense)
	preparedRelease := Release{
		Version:       "8.0.55",
		AssetName:     "CLIProxyAPI_8.0.55_linux_amd64.tar.gz",
		AssetURL:      "https://github.com/router-for-me/CLIProxyAPI/releases/download/v8.0.55/CLIProxyAPI_8.0.55_linux_amd64.tar.gz",
		ArchiveDigest: "sha256:" + hex.EncodeToString(archiveWithoutLicenseSum[:]),
		ArchiveSize:   int64(len(archiveWithoutLicense)),
	}

	root := t.TempDir()
	store, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}

	// 1. Ordinary Store.Stage succeeds without LICENSE.
	stagedMeta, err := store.Stage(t.Context(), preparedRelease, func(_ context.Context, _ Release, dst io.Writer) error {
		_, writeErr := dst.Write(archiveWithoutLicense)
		return writeErr
	})
	if err != nil {
		t.Fatalf("Stage() error = %v", err)
	}
	finalDir := filepath.Join(root, "8.0.55")
	if _, err := os.Lstat(filepath.Join(finalDir, stagedLicenseName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("prepared stage unexpectedly has LICENSE: %v", err)
	}
	if store.HasAcquiredStage("8.0.55", wantArtifactID) {
		t.Fatal("HasAcquiredStage() = true on stage without LICENSE")
	}

	// 2. Store.Acquire on the same version/artifactId with an archive lacking LICENSE fails with ErrArchiveInvalid and leaves stage unchanged.
	var acquireDownloads int
	if _, err := store.Acquire(
		t.Context(),
		"8.0.55",
		wantArtifactID,
		func(_ context.Context, dst io.Writer, _ ProgressFunc) error {
			acquireDownloads++
			_, writeErr := dst.Write(archiveWithoutLicense)
			return writeErr
		},
		nil,
		nil,
	); !errors.Is(err, ErrArchiveInvalid) || acquireDownloads != 1 {
		t.Fatalf("Acquire(without LICENSE) error = %v, downloads = %d; want ErrArchiveInvalid, 1", err, acquireDownloads)
	}
	if _, err := os.Lstat(filepath.Join(finalDir, stagedLicenseName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed Acquire created LICENSE: %v", err)
	}
	entriesAfterFailure, err := os.ReadDir(finalDir)
	if err != nil || len(entriesAfterFailure) != 2 {
		t.Fatalf("stage entries after failed Acquire = %v, %v; want only artifact.json and cli-proxy-api", entriesAfterFailure, err)
	}

	// 3. Store.Acquire with a valid archive containing LICENSE atomically adds LICENSE to the existing stage.
	archiveWithLicense := tarGzip(t,
		archiveEntry{name: stagedExecutableName, typeFlag: tar.TypeReg, data: binary},
		archiveEntry{name: stagedLicenseName, typeFlag: tar.TypeReg, data: license},
	)
	acquiredStage, err := store.AcquireStage(
		t.Context(),
		"8.0.55",
		wantArtifactID,
		func(_ context.Context, dst io.Writer, _ ProgressFunc) error {
			acquireDownloads++
			_, writeErr := dst.Write(archiveWithLicense)
			return writeErr
		},
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("AcquireStage(with LICENSE) error = %v", err)
	}
	if acquiredStage.CreatedStage || acquireDownloads != 2 || acquiredStage.Metadata != stagedMeta {
		t.Fatalf("AcquireStage(with LICENSE) created=%v downloads=%d meta=%+v, want false, 2 and %+v", acquiredStage.CreatedStage, acquireDownloads, acquiredStage.Metadata, stagedMeta)
	}
	assertMode(t, filepath.Join(finalDir, stagedLicenseName), 0o444)
	gotLicense, err := os.ReadFile(filepath.Join(finalDir, stagedLicenseName))
	if err != nil || !bytes.Equal(gotLicense, license) {
		t.Fatalf("backfilled LICENSE = %q, %v", gotLicense, err)
	}
	if !store.HasAcquiredStage("8.0.55", wantArtifactID) {
		t.Fatal("HasAcquiredStage() = false after LICENSE backfill")
	}

	// 4. Subsequent Store.AcquireStage still verifies archive bytes and reuses the existing stage with CreatedStage=false.
	reusedStage, err := store.AcquireStage(
		t.Context(),
		"8.0.55",
		wantArtifactID,
		func(_ context.Context, dst io.Writer, _ ProgressFunc) error {
			acquireDownloads++
			_, writeErr := dst.Write(archiveWithLicense)
			return writeErr
		},
		nil,
		nil,
	)
	if err != nil || reusedStage.CreatedStage || reusedStage.Metadata != stagedMeta || acquireDownloads != 3 {
		t.Fatalf("reused AcquireStage() = %+v, downloads=%d, err=%v", reusedStage, acquireDownloads, err)
	}
}

func TestStoreAcquireStageRemovesNewlyCreatedStageOnPostRenameFailure(t *testing.T) {
	binary := []byte("#!/bin/sh\necho post-rename-failure-cpa\n")
	binarySum := sha256.Sum256(binary)
	wantArtifactID := artifact.ID("sha256:" + hex.EncodeToString(binarySum[:]))
	license := []byte("MIT License\n")
	archive := tarGzip(t,
		archiveEntry{name: stagedExecutableName, typeFlag: tar.TypeReg, data: binary},
		archiveEntry{name: stagedLicenseName, typeFlag: tar.TypeReg, data: license},
	)

	root := t.TempDir()
	store, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}

	var observedFinalOnDisk bool
	acquirePostRenameHook = func(finalPath string) error {
		if info, statErr := os.Lstat(finalPath); statErr == nil && info.IsDir() {
			observedFinalOnDisk = true
		}
		return errors.New("injected post-rename failure")
	}
	defer func() {
		acquirePostRenameHook = nil
	}()

	if _, err := store.AcquireStage(
		t.Context(),
		"8.0.55",
		wantArtifactID,
		CopyUploadArchive(bytes.NewReader(archive), int64(len(archive))),
		nil,
		nil,
	); !errors.Is(err, ErrStaging) {
		t.Fatalf("AcquireStage() with post-rename failure = %v, want ErrStaging", err)
	}
	if !observedFinalOnDisk {
		t.Fatal("acquirePostRenameHook did not observe renamed final stage on disk")
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("stage root after post-rename failure = %v, %v; want newly created stage removed", entries, err)
	}

	// When RemoveCreatedStage itself fails after a post-rename error, the returned
	// error joins both the post-rename failure and the cleanup failure while the
	// leftover stage remains on disk; a subsequent AcquireStage still verifies and
	// reuses matching bytes or refuses a conflicting artifactId.
	postErr := errors.New("injected post-rename failure 2")
	cleanupErr := errors.New("injected stage cleanup failure")
	acquirePostRenameHook = func(finalPath string) error {
		_ = store.prepareFinalizedExecutionDirectoryForTest(finalPath)
		return postErr
	}
	acquireRemoveCreatedStageHook = func(string) error {
		return cleanupErr
	}
	defer func() {
		acquireRemoveCreatedStageHook = nil
	}()
	_, joinedErr := store.AcquireStage(
		t.Context(),
		"8.0.55",
		wantArtifactID,
		CopyUploadArchive(bytes.NewReader(archive), int64(len(archive))),
		nil,
		nil,
	)
	if !errors.Is(joinedErr, ErrStaging) || !strings.Contains(joinedErr.Error(), postErr.Error()) || !strings.Contains(joinedErr.Error(), cleanupErr.Error()) {
		t.Fatalf("AcquireStage() with post-rename + cleanup failure = %v; want both %q and %q", joinedErr, postErr, cleanupErr)
	}
	if _, statErr := os.Lstat(filepath.Join(root, "8.0.55")); statErr != nil {
		t.Fatalf("expected leftover stage to remain when RemoveCreatedStage failed: %v", statErr)
	}
	acquirePostRenameHook = nil
	acquireRemoveCreatedStageHook = nil

	otherID := artifact.ID("sha256:" + strings.Repeat("c", 64))
	if _, conflictErr := store.AcquireStage(
		t.Context(),
		"8.0.55",
		otherID,
		CopyUploadArchive(bytes.NewReader(archive), int64(len(archive))),
		nil,
		nil,
	); !errors.Is(conflictErr, ErrArtifactConflict) {
		t.Fatalf("AcquireStage() with conflicting artifactId against leftover stage = %v, want ErrArtifactConflict", conflictErr)
	}
	reusedRes, reuseErr := store.AcquireStage(
		t.Context(),
		"8.0.55",
		wantArtifactID,
		CopyUploadArchive(bytes.NewReader(archive), int64(len(archive))),
		nil,
		nil,
	)
	if reuseErr != nil || reusedRes.CreatedStage || reusedRes.Metadata.ArtifactID != wantArtifactID {
		t.Fatalf("AcquireStage() reusing verified leftover stage = %+v, %v", reusedRes, reuseErr)
	}
}

func newTLSServerClientForOfficialHosts(server *httptest.Server) *http.Client {
	baseTransport := server.Client().Transport.(*http.Transport).Clone()
	targetAddr := server.Listener.Addr().String()
	baseTransport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, targetAddr)
	}
	if baseTransport.TLSClientConfig == nil {
		baseTransport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	} else {
		baseTransport.TLSClientConfig = baseTransport.TLSClientConfig.Clone()
	}
	// httptest TLS certificates are valid for example.com and 127.0.0.1.
	baseTransport.TLSClientConfig.ServerName = "example.com"
	return &http.Client{Transport: baseTransport}
}

func (s *Store) prepareFinalizedExecutionDirectoryForTest(finalPath string) error {
	return s.prepareFinalizedExecutionDirectory(finalPath)
}
