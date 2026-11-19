package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

type archiveEntry struct {
	name         string
	typeFlag     byte
	data         []byte
	declaredSize int64
	linkName     string
}

func TestStoreStagesOnlyExactExecutableWithExactIdentity(t *testing.T) {
	binary := []byte("exact staged executable bytes")
	archive := tarGzip(t,
		archiveEntry{name: "README.md", typeFlag: tar.TypeReg, data: []byte("ignored")},
		archiveEntry{name: stagedExecutableName, typeFlag: tar.TypeReg, data: binary},
		archiveEntry{name: "config.example.yaml", typeFlag: tar.TypeReg, data: []byte("ignored")},
	)
	release := releaseForArchive("7.3.3", archive)
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var downloads int
	metadata, err := store.Stage(t.Context(), release, func(_ context.Context, got Release, destination io.Writer) error {
		downloads++
		if got != release {
			t.Fatalf("download release = %#v", got)
		}
		_, err := destination.Write(archive)
		return err
	})
	if err != nil {
		t.Fatalf("Stage() error = %v", err)
	}
	executableDigest := sha256.Sum256(binary)
	wantArtifactID := "sha256:" + hex.EncodeToString(executableDigest[:])
	if downloads != 1 || string(metadata.ArtifactID) != wantArtifactID ||
		metadata.SourceArchiveDigest != release.ArchiveDigest || metadata.Version != release.Version {
		t.Fatalf("Stage() downloads=%d metadata=%#v", downloads, metadata)
	}
	final := filepath.Join(store.root, release.Version)
	entries, err := os.ReadDir(final)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("final entries = %v", entries)
	}
	gotBinary, err := os.ReadFile(filepath.Join(final, stagedExecutableName))
	if err != nil || !bytes.Equal(gotBinary, binary) {
		t.Fatalf("staged executable = %q, %v", gotBinary, err)
	}
	assertMode(t, filepath.Join(final, stagedExecutableName), 0o755)
	assertMode(t, filepath.Join(final, stagedMetadataName), 0o444)
	stored, err := readMetadata(filepath.Join(final, stagedMetadataName))
	if err != nil || stored != metadata {
		t.Fatalf("stored metadata = %#v, %v", stored, err)
	}
}

func TestStoreRejectsUnsafeOrInvalidArchivesAndCleansTemporaryState(t *testing.T) {
	oversize := archiveEntry{name: stagedExecutableName, typeFlag: tar.TypeReg, declaredSize: maxExtractedBinaryBytes + 1}
	tests := map[string][]archiveEntry{
		"missing executable": {{name: "README.md", typeFlag: tar.TypeReg, data: []byte("x")}},
		"duplicate executable": {
			{name: stagedExecutableName, typeFlag: tar.TypeReg, data: []byte("one")},
			{name: stagedExecutableName, typeFlag: tar.TypeReg, data: []byte("two")},
		},
		"traversal":              {{name: "../outside", typeFlag: tar.TypeReg, data: []byte("x")}},
		"absolute":               {{name: "/tmp/outside", typeFlag: tar.TypeReg, data: []byte("x")}},
		"path alias":             {{name: "./cli-proxy-api", typeFlag: tar.TypeReg, data: []byte("x")}},
		"backslash path":         {{name: `dir\cli-proxy-api`, typeFlag: tar.TypeReg, data: []byte("x")}},
		"symlink target":         {{name: stagedExecutableName, typeFlag: tar.TypeSymlink, linkName: "elsewhere"}},
		"hardlink target":        {{name: stagedExecutableName, typeFlag: tar.TypeLink, linkName: "elsewhere"}},
		"fifo target":            {{name: stagedExecutableName, typeFlag: tar.TypeFifo}},
		"device target":          {{name: stagedExecutableName, typeFlag: tar.TypeChar}},
		"empty executable":       {{name: stagedExecutableName, typeFlag: tar.TypeReg}},
		"oversize binary":        {oversize},
		"oversize ignored entry": {{name: "README.md", typeFlag: tar.TypeReg, declaredSize: maxExpandedArchiveBytes + 1}},
	}
	for name, entries := range tests {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			store, err := NewStore(root)
			if err != nil {
				t.Fatal(err)
			}
			archive := tarGzip(t, entries...)
			release := releaseForArchive("7.3.3", archive)
			_, err = store.Stage(t.Context(), release, func(_ context.Context, _ Release, destination io.Writer) error {
				_, writeErr := destination.Write(archive)
				return writeErr
			})
			if !errors.Is(err, ErrStaging) {
				t.Fatalf("Stage() error = %v", err)
			}
			if _, err := os.Stat(filepath.Join(root, release.Version)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("invalid archive published final stage: %v", err)
			}
			entries, err := os.ReadDir(root)
			if err != nil || len(entries) != 0 {
				t.Fatalf("temporary state remains: %v, %v", entries, err)
			}
		})
	}
}

func TestStoreReusesOnlyVerifiedImmutableFinalStageAcrossReopen(t *testing.T) {
	root := t.TempDir()
	archive := tarGzip(t, archiveEntry{name: stagedExecutableName, typeFlag: tar.TypeReg, data: []byte("binary")})
	release := releaseForArchive("7.3.3", archive)
	store, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	downloads := 0
	download := func(_ context.Context, _ Release, destination io.Writer) error {
		downloads++
		_, err := destination.Write(archive)
		return err
	}
	want, err := store.Stage(t.Context(), release, download)
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reopened.Stage(t.Context(), release, download)
	if err != nil || got != want || downloads != 1 {
		t.Fatalf("reopened Stage() = %#v downloads=%d error=%v", got, downloads, err)
	}

	if err := os.WriteFile(filepath.Join(root, release.Version, stagedExecutableName), []byte("tampered"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.Stage(t.Context(), release, download); !errors.Is(err, ErrStageConflict) || downloads != 1 {
		t.Fatalf("corrupt finalized stage error=%v downloads=%d", err, downloads)
	}
}

func TestPreparerVerifiesEveryNewUploadBeforeReusingFinalStage(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	preparer := newPreparer(&Source{asset: "linux_amd64.tar.gz"}, store)
	archive := tarGzip(t, archiveEntry{name: stagedExecutableName, data: []byte("exact executable")})
	release := releaseForArchive("7.3.3", archive)
	upload := func(reader io.Reader, length int64) (Metadata, error) {
		return preparer.StageWithOptions(t.Context(), release, StageOptions{
			SourceMode: "upload", UploadReader: reader, UploadLength: length,
		})
	}
	want, err := upload(bytes.NewReader(archive), release.ArchiveSize)
	if err != nil {
		t.Fatal(err)
	}
	final, err := store.ResolveFinalized(release.Version)
	if err != nil {
		t.Fatal(err)
	}
	original, err := os.Stat(final.ExecutablePath)
	if err != nil {
		t.Fatal(err)
	}
	wrongDigest := bytes.Clone(archive)
	wrongDigest[len(wrongDigest)-1] ^= 1
	for _, tc := range []struct {
		name   string
		bytes  []byte
		length int64
	}{
		{name: "known size mismatch", bytes: archive[:len(archive)-1], length: release.ArchiveSize - 1},
		{name: "unknown size mismatch", bytes: archive[:len(archive)-1], length: -1},
		{name: "same size wrong digest", bytes: wrongDigest, length: release.ArchiveSize},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := upload(bytes.NewReader(tc.bytes), tc.length); !errors.Is(err, ErrArchiveInvalid) || got != (Metadata{}) {
				t.Fatalf("invalid upload = %#v, %v", got, err)
			}
			if after, err := store.ResolveFinalized(release.Version); err != nil || after != final {
				t.Fatalf("invalid upload altered finalized stage: %#v, %v", after, err)
			}
		})
	}
	valid := bytes.NewReader(archive)
	if got, err := upload(valid, -1); err != nil || got != want || valid.Len() != 0 {
		t.Fatalf("valid reuse = %#v, %v; %d unread bytes", got, err, valid.Len())
	}
	after, err := os.Stat(final.ExecutablePath)
	if err != nil || !os.SameFile(original, after) {
		t.Fatalf("finalized executable was replaced: %v", err)
	}
	if entries, err := os.ReadDir(store.root); err != nil || len(entries) != 1 || entries[0].Name() != release.Version {
		t.Fatalf("unexpected stage root contents: %v, %v", entries, err)
	}
}

func TestStoreRejectsChangedOfficialSourceWithoutOverwrite(t *testing.T) {
	root := t.TempDir()
	archive := tarGzip(t, archiveEntry{name: stagedExecutableName, typeFlag: tar.TypeReg, data: []byte("binary")})
	release := releaseForArchive("7.3.3", archive)
	store, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	download := func(_ context.Context, _ Release, destination io.Writer) error {
		_, err := destination.Write(archive)
		return err
	}
	if _, err := store.Stage(t.Context(), release, download); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(filepath.Join(root, release.Version, stagedExecutableName))
	if err != nil {
		t.Fatal(err)
	}
	changed := release
	changed.ArchiveDigest = "sha256:" + strings.Repeat("f", 64)
	called := false
	if _, err := store.Stage(t.Context(), changed, func(context.Context, Release, io.Writer) error {
		called = true
		return nil
	}); !errors.Is(err, ErrStageConflict) || called {
		t.Fatalf("changed source Stage() error=%v downloadCalled=%v", err, called)
	}
	after, err := os.ReadFile(filepath.Join(root, release.Version, stagedExecutableName))
	if err != nil || !bytes.Equal(after, original) {
		t.Fatalf("finalized stage was overwritten: %q, %v", after, err)
	}
}

func TestNewStoreCleansOnlyIncompleteTemporaryState(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, temporaryPrefix+"crash"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "7.3.3"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, temporaryPrefix+"crash")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary crash state remains: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "7.3.3")); err != nil {
		t.Fatalf("finalized directory was removed: %v", err)
	}
}

func releaseForArchive(version string, archive []byte) Release {
	digest := sha256.Sum256(archive)
	return Release{
		Version:       version,
		AssetName:     "CLIProxyAPI_" + version + "_linux_amd64.tar.gz",
		AssetURL:      "https://github.com/router-for-me/CLIProxyAPI/releases/download/v" + version + "/CLIProxyAPI_" + version + "_linux_amd64.tar.gz",
		ArchiveDigest: "sha256:" + hex.EncodeToString(digest[:]),
		ArchiveSize:   int64(len(archive)),
	}
}

func tarGzip(t *testing.T, entries ...archiveEntry) []byte {
	t.Helper()
	var encoded bytes.Buffer
	gzipWriter := gzip.NewWriter(&encoded)
	tarWriter := tar.NewWriter(gzipWriter)
	for _, entry := range entries {
		typeFlag := entry.typeFlag
		if typeFlag == 0 {
			typeFlag = tar.TypeReg
		}
		size := int64(len(entry.data))
		if entry.declaredSize != 0 {
			size = entry.declaredSize
		}
		header := &tar.Header{Name: entry.name, Typeflag: typeFlag, Mode: 0o755, Size: size, Linkname: entry.linkName}
		if err := tarWriter.WriteHeader(header); err != nil {
			t.Fatalf("write tar header: %v", err)
		}
		if len(entry.data) > 0 {
			if _, err := tarWriter.Write(entry.data); err != nil {
				t.Fatalf("write tar data: %v", err)
			}
		}
	}
	_ = tarWriter.Close()
	if err := gzipWriter.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	return encoded.Bytes()
}

func assertMode(t *testing.T, name string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(name)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != want {
		t.Fatalf("%s mode = %o, want %o", name, info.Mode().Perm(), want)
	}
}

type eofCancelUploadReader struct {
	r        *bytes.Reader
	onEOF    func()
	canceled bool
}

func (r *eofCancelUploadReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	if errors.Is(err, io.EOF) && !r.canceled {
		r.canceled = true
		r.onEOF()
	}
	return n, err
}

func TestStageWithOptionsEnforcesAssetMetadataBoundsPhaseAndUploadContext(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	preparer := newPreparer(&Source{asset: "linux_amd64.tar.gz"}, store)
	archive := tarGzip(t, archiveEntry{name: stagedExecutableName, data: []byte("stage-options-executable")})
	release := releaseForArchive("7.3.3", archive)

	uploadCtx, cancelUpload := context.WithCancel(t.Context())
	defer cancelUpload()
	var uploadPhases []string
	meta, err := preparer.StageWithOptions(t.Context(), release, StageOptions{
		SourceMode:    "upload",
		UploadContext: uploadCtx,
		UploadReader: &eofCancelUploadReader{
			r:     bytes.NewReader(archive),
			onEOF: cancelUpload,
		},
		UploadLength: release.ArchiveSize,
		OnPhase: func(phase string) {
			uploadPhases = append(uploadPhases, phase)
		},
	})
	if err != nil || meta.Version != "7.3.3" {
		t.Fatalf("StageWithOptions(post-EOF canceled upload) = %+v, err=%v", meta, err)
	}
	if len(uploadPhases) == 0 || uploadPhases[0] != "verifying" {
		t.Fatalf("upload phases = %v, want verifying", uploadPhases)
	}

	var reusePhases []string
	reused, err := preparer.StageWithOptions(t.Context(), release, StageOptions{
		SourceMode: "github",
		OnPhase: func(phase string) {
			reusePhases = append(reusePhases, phase)
		},
	})
	if err != nil || reused != meta {
		t.Fatalf("StageWithOptions(reuse pre-staged) = %+v, err=%v; want %+v", reused, err, meta)
	}
	if len(reusePhases) == 0 {
		t.Fatal("expected verifying phase callback when reusing pre-staged directory")
	}
	for _, phase := range reusePhases {
		if phase != "verifying" {
			t.Fatalf("pre-staged reuse reported phase %q, want only verifying", phase)
		}
	}

	for _, bad := range []struct {
		name   string
		mutate func(*Release)
	}{
		{name: "empty asset name", mutate: func(r *Release) { r.AssetName = "" }},
		{name: "wrong platform asset name", mutate: func(r *Release) {
			r.AssetName = "CLIProxyAPI_7.3.3_linux_aarch64.tar.gz"
			r.AssetURL = "https://github.com/router-for-me/CLIProxyAPI/releases/download/v7.3.3/CLIProxyAPI_7.3.3_linux_aarch64.tar.gz"
		}},
		{name: "empty asset URL", mutate: func(r *Release) { r.AssetURL = "" }},
		{name: "wrong asset URL", mutate: func(r *Release) {
			r.AssetURL = "https://example.com/CLIProxyAPI_7.3.3_linux_amd64.tar.gz"
		}},
	} {
		candidate := release
		bad.mutate(&candidate)
		if _, err := preparer.StageWithOptions(t.Context(), candidate, StageOptions{SourceMode: "github"}); !errors.Is(err, ErrArchiveInvalid) {
			t.Fatalf("StageWithOptions(%s) = %v, want ErrArchiveInvalid", bad.name, err)
		}
	}

	oversize := release
	oversize.ArchiveSize = MaxAcquisitionArchiveBytes + 1
	for _, mode := range []string{"cdn", "upload"} {
		if _, err := preparer.StageWithOptions(t.Context(), oversize, StageOptions{SourceMode: mode}); !errors.Is(err, ErrArchiveTooLarge) {
			t.Fatalf("StageWithOptions(%s, >64 MiB) = %v, want ErrArchiveTooLarge", mode, err)
		}
	}
}

func TestStageWithOptionsCDNSourceEndToEnd(t *testing.T) {
	binary := []byte("cdn-staged-executable-7.3.4")
	binarySum := sha256.Sum256(binary)
	wantArtifactID := "sha256:" + hex.EncodeToString(binarySum[:])
	archive := tarGzip(t, archiveEntry{name: stagedExecutableName, data: binary})
	release := releaseForArchive("7.3.4", archive)
	officialPath := "/router-for-me/CLIProxyAPI/releases/download/v7.3.4/CLIProxyAPI_7.3.4_linux_amd64.tar.gz"

	tlsServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/mirror/https://github.com" + officialPath:
			w.Header().Set("Content-Length", strconv.Itoa(len(archive)))
			_, _ = w.Write(archive)
		case "/corrupt/https://github.com" + officialPath:
			corrupted := append([]byte(nil), archive...)
			corrupted[0] ^= 0xff
			w.Header().Set("Content-Length", strconv.Itoa(len(corrupted)))
			_, _ = w.Write(corrupted)
		default:
			http.NotFound(w, r)
		}
	}))
	defer tlsServer.Close()

	cdnClient := newTLSServerClientForOfficialHosts(tlsServer)
	transport := cdnClient.Transport.(*http.Transport)
	fixtureDial := transport.DialContext
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		peer, err := net.ResolveTCPAddr(network, address)
		if err != nil {
			return nil, err
		}
		conn, err := fixtureDial(ctx, network, address)
		if err != nil {
			return nil, err
		}
		return cdnFixtureConn{Conn: conn, remote: peer}, nil
	}
	publicLookup := func(_ context.Context, host string) ([]net.IPAddr, error) {
		if host == githubAPIHost {
			return []net.IPAddr{{IP: net.ParseIP("140.82.121.6")}}, nil
		}
		return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, nil
	}

	stageRoot := t.TempDir()
	store, err := NewStore(stageRoot)
	if err != nil {
		t.Fatal(err)
	}
	preparer := newPreparer(&Source{asset: "linux_amd64.tar.gz"}, store)
	preparer.SetCDNOptionsForTesting(WithCDNHTTPClient(cdnClient), WithCDNLookupIP(publicLookup))

	// Corrupted CDN archive fails closed with ErrArchiveInvalid and leaves no stage files.
	if _, err := preparer.StageWithOptions(t.Context(), release, StageOptions{
		SourceMode: "cdn",
		CDNPrefix:  "https://cdn.example.com/corrupt/",
	}); !errors.Is(err, ErrArchiveInvalid) {
		t.Fatalf("StageWithOptions(cdn corrupt) = %v, want ErrArchiveInvalid", err)
	}
	if entries, readErr := os.ReadDir(stageRoot); readErr != nil || len(entries) != 0 {
		t.Fatalf("corrupt CDN stage left files: %v, %v", entries, readErr)
	}

	var phases []string
	var lastReceived, lastTotal int64
	meta, err := preparer.StageWithOptions(t.Context(), release, StageOptions{
		SourceMode: "cdn",
		CDNPrefix:  "https://cdn.example.com/mirror/",
		OnPhase: func(phase string) {
			phases = append(phases, phase)
		},
		Progress: func(received, total int64) {
			lastReceived = received
			lastTotal = total
		},
	})
	if err != nil {
		t.Fatalf("StageWithOptions(cdn valid) error = %v", err)
	}
	if meta.Version != "7.3.4" || string(meta.ArtifactID) != wantArtifactID || meta.SourceArchiveDigest != release.ArchiveDigest {
		t.Fatalf("StageWithOptions(cdn valid) metadata = %+v", meta)
	}
	if lastReceived != release.ArchiveSize || lastTotal != release.ArchiveSize {
		t.Fatalf("StageWithOptions(cdn valid) progress = (%d/%d), want %d", lastReceived, lastTotal, release.ArchiveSize)
	}
	sawDownloading := false
	for _, p := range phases {
		if p == "downloading" {
			sawDownloading = true
		}
	}
	if !sawDownloading {
		t.Fatalf("StageWithOptions(cdn valid) phases = %v, expected downloading", phases)
	}
}
