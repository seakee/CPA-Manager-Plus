package updatecheck

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type memoryStore struct {
	mu     sync.Mutex
	data   []byte
	claims map[string]bool
	fail   bool
}

func (m *memoryStore) LoadUpdateCheck(context.Context) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]byte(nil), m.data...), nil
}
func (m *memoryStore) SaveUpdateCheck(_ context.Context, b []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail {
		return errors.New("disk full")
	}
	m.data = append([]byte(nil), b...)
	return nil
}
func (m *memoryStore) ClaimUpdateNotification(_ context.Context, tag string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail {
		return false, errors.New("disk full")
	}
	if m.claims == nil {
		m.claims = map[string]bool{}
	}
	if m.claims[tag] {
		return false, nil
	}
	m.claims[tag] = true
	return true, nil
}
func (m *memoryStore) DismissUpdateNotification(context.Context, string) error { return nil }

func fixture(tag string) ReleaseInfo {
	var info ReleaseInfo
	info.SchemaVersion = 2
	info.Release.Version = tag
	v, _ := ParseVersion(tag)
	info.Release.Stage = v.Stage
	info.Release.SourceCommit = "0123456789012345678901234567890123456789"
	info.Content.Summary = map[string]string{"zh": "更新", "en": "Update"}
	info.Content.Notes = map[string]string{}
	for _, lang := range []string{"zh", "en"} {
		info.Content.Notes[lang] = Repository + "/blob/" + tag + "/docs/release-notes/" + tag + "-" + lang + ".md"
	}
	info.Update.UpgradeGuideURL = info.Content.Notes["zh"]

	versionTag := strings.TrimPrefix(tag, "v")
	slimDigest := "sha256:" + strings.Repeat("1", 64)
	fullDigest := "sha256:" + strings.Repeat("2", 64)
	info.Distribution.Docker.VersionTag = versionTag
	info.Distribution.Docker.Images = []DockerImage{
		{
			Role:      "slim",
			Name:      "seakee/cpa-manager-plus",
			Tag:       versionTag,
			Digest:    slimDigest,
			Platforms: []string{"linux/amd64"},
		},
		{
			Role:      "full",
			Name:      "seakee/cpa-manager-plus",
			Tag:       versionTag + "-full",
			Digest:    fullDigest,
			Platforms: []string{"linux/amd64"},
		},
		{
			Role:      "slim",
			Name:      "ghcr.io/seakee/cpa-manager-plus",
			Tag:       versionTag,
			Digest:    slimDigest,
			Platforms: []string{"linux/amd64"},
		},
		{
			Role:      "full",
			Name:      "ghcr.io/seakee/cpa-manager-plus",
			Tag:       versionTag + "-full",
			Digest:    fullDigest,
			Platforms: []string{"linux/amd64"},
		},
	}
	info.Distribution.Native.Assets = []string{
		"cpa-manager-plus-native-v2_" + tag + "_linux_amd64.tar.gz",
	}
	composeSHA := "sha256:" + strings.Repeat("3", 64)
	info.Distribution.Compose = &ComposeDistribution{
		Filename: "docker-compose.v2-embedded.yml",
		SHA256:   composeSHA,
	}
	minCPA := "v8.0.55"
	info.Compatibility.MinimumCPAVersion = &minCPA
	info.SupportedModes = []string{"docker", "native", "panel-only"}
	info.SupportedPlatforms = map[string][]string{
		"docker": {"linux/amd64"},
		"native": {"linux/amd64"},
	}
	info.Pairing = &ReleasePairing{
		Filename:              "embedded-release-pairing.json",
		SourceCommit:          info.Release.SourceCommit,
		FullWebManifestSHA256: "sha256:" + strings.Repeat("4", 64),
		APIContractSHA256:     "sha256:" + strings.Repeat("5", 64),
		ComposeSHA256:         composeSHA,
	}
	return info
}

func targetFor(tag, sha string) *Target {
	return &Target{
		Version:           tag,
		SchemaVersion:     2,
		ReleaseInfoPath:   "releases/download/" + tag + "/release-info.json",
		ReleaseInfoSHA256: sha,
	}
}

func releaseSHA256(t *testing.T, info ReleaseInfo) ([]byte, string) {
	t.Helper()
	raw, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	return raw, "sha256:" + hex.EncodeToString(sum[:])
}

func testService(t *testing.T, current, tag string, m *memoryStore) (*Service, *atomic.Int32, *atomic.Int32) {
	t.Helper()
	downloads, indices := &atomic.Int32{}, &atomic.Int32{}
	info := fixture(tag)
	infoBytes, infoSHA := releaseSHA256(t, info)
	v, _ := ParseVersion(tag)
	channels := map[string]*Target{
		"stable": nil,
		"rc":     nil,
		"beta":   targetFor(tag, infoSHA),
	}
	if v.Stage == "stable" {
		channels["stable"] = targetFor(tag, infoSHA)
		channels["rc"] = targetFor(tag, infoSHA)
	} else if v.Stage == "rc" {
		channels["rc"] = targetFor(tag, infoSHA)
	}
	idx := Index{
		SchemaVersion: 2,
		Revision:      1,
		GeneratedAt:   time.Now().UTC(),
		Channels:      channels,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/index":
			indices.Add(1)
			_ = json.NewEncoder(w).Encode(idx)
		case "/releases/download/" + tag + "/release-info.json":
			downloads.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(infoBytes)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	s := New(m, current, "test", true)
	s.client = srv.Client()
	s.indexURL = srv.URL + "/index"
	s.releaseBaseURL = srv.URL
	return s, downloads, indices
}

func TestSharedVersionCorpus(t *testing.T) {
	data, err := os.ReadFile("../../../../../tests/fixtures/update-versions.json")
	if err != nil {
		t.Fatal(err)
	}
	var corpus struct {
		Ordered []string
		Equal   [][]string
		Invalid []string
	}
	if err = json.Unmarshal(data, &corpus); err != nil {
		t.Fatal(err)
	}
	for i := 1; i < len(corpus.Ordered); i++ {
		a, e := ParseVersion(corpus.Ordered[i-1])
		if e != nil {
			t.Fatal(e)
		}
		b, e := ParseVersion(corpus.Ordered[i])
		if e != nil {
			t.Fatal(e)
		}
		if Compare(a, b) >= 0 {
			t.Fatal(corpus.Ordered[i])
		}
	}
	for _, pair := range corpus.Equal {
		a, _ := ParseVersion(pair[0])
		b, _ := ParseVersion(pair[1])
		if Compare(a, b) != 0 {
			t.Fatal(pair)
		}
	}
	for _, tag := range corpus.Invalid {
		if _, err := ParseVersion(tag); err == nil {
			t.Fatal(tag)
		}
	}
}

func TestConcurrentChecksAndNotificationsSurviveRestart(t *testing.T) {
	ctx := context.Background()
	m := &memoryStore{}
	s, downloads, indices := testService(t, "v2.0.0", "v2.1.0", m)
	var wg sync.WaitGroup
	var claims atomic.Int32
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Check(ctx); err != nil {
				t.Error(err)
			}
			if info, err := s.Claim(ctx); err != nil {
				t.Error(err)
			} else if info != nil {
				claims.Add(1)
			}
		}()
	}
	wg.Wait()
	if downloads.Load() != 1 || indices.Load() != 1 || claims.Load() != 1 {
		t.Fatalf("downloads=%d indices=%d claims=%d", downloads.Load(), indices.Load(), claims.Load())
	}
	_ = s.Dismiss(ctx, "v2.1.0")
	restarted, d2, _ := testService(t, "v2.0.0", "v2.1.0", m)
	restarted.now = func() time.Time { return time.Now().Add(2 * time.Minute) }
	if _, err := restarted.Check(ctx); err != nil {
		t.Fatal(err)
	}
	if info, err := restarted.Claim(ctx); info != nil || err != nil {
		t.Fatalf("repeated notification: %v %v", info, err)
	}
	if d2.Load() != 0 {
		t.Fatal("download after restart")
	}
}

func TestEqualAheadUnknownAndPersistenceFailure(t *testing.T) {
	for _, tc := range []struct {
		current, state string
		downloads      int32
	}{{"v2.1.0", "up_to_date", 1}, {"v2.2.0", "ahead_of_channel", 0}, {"dev", "unknown_version", 0}} {
		t.Run(tc.current, func(t *testing.T) {
			s, d, _ := testService(t, tc.current, "v2.1.0", &memoryStore{})
			st, err := s.Check(context.Background())
			if err != nil || st.State != tc.state || d.Load() != tc.downloads {
				t.Fatalf("%+v %v %d", st, err, d.Load())
			}
			info, _ := s.Claim(context.Background())
			if info != nil {
				t.Fatal("unexpected notification")
			}
		})
	}
	m := &memoryStore{fail: true}
	s, d, _ := testService(t, "v2.0.0", "v2.1.0", m)
	if _, err := s.Check(context.Background()); err == nil {
		t.Fatal("expected persistence error")
	}
	if info, _ := s.Claim(context.Background()); info != nil {
		t.Fatal("claim before durable metadata")
	}
	s.now = func() time.Time { return time.Now().Add(2 * time.Minute) }
	if _, err := s.Check(context.Background()); err == nil || d.Load() != 1 {
		t.Fatal("persistent storage failure redownloaded cached metadata")
	}
	m.fail = false
	if recovered, err := s.Check(context.Background()); err != nil || recovered.LastError != "" {
		t.Fatalf("persistence did not recover: %+v %v", recovered, err)
	}
	if d.Load() != 1 {
		t.Fatal("redownload after persistence error")
	}
	if info, err := s.Claim(context.Background()); info == nil || err != nil {
		t.Fatalf("notification suppressed after persistence recovered: %v %v", info, err)
	}
}

func TestChannelMigrationAndBadMetadata(t *testing.T) {
	ctx := context.Background()
	s, _, _ := testService(t, "v1.14.10", "v2.0.0", &memoryStore{})
	st, _ := s.Check(ctx)
	if st.UpgradeAction != "migration_guide" {
		t.Fatal(st)
	}
	if _, err := s.SetChannel(ctx, "bogus"); err == nil {
		t.Fatal("invalid channel")
	}
	if _, err := s.SetChannel(ctx, "beta"); err != nil {
		t.Fatal(err)
	}
	info := fixture("v2.0.0")
	info.Release.Version = "v3.0.0"
	if info.Validate("v2.0.0") == nil {
		t.Fatal("mismatched release")
	}
}

func TestCaseA_StableSuccessThenBetaSwitchImmediatelyFetched(t *testing.T) {
	ctx := context.Background()
	clock := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	stable := fixture("v2.1.0")
	rc := fixture("v2.2.0-rc.1")
	beta := fixture("v2.2.0-beta.1")
	stableBytes, stableSHA := releaseSHA256(t, stable)
	betaBytes, betaSHA := releaseSHA256(t, beta)
	_, rcSHA := releaseSHA256(t, rc)
	index := Index{
		SchemaVersion: 2,
		Revision:      1,
		GeneratedAt:   clock,
		Channels: map[string]*Target{
			"stable": targetFor(stable.Release.Version, stableSHA),
			"rc":     targetFor(rc.Release.Version, rcSHA),
			"beta":   targetFor(beta.Release.Version, betaSHA),
		},
	}
	var indexRequests, stableDownloads, betaDownloads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/index":
			indexRequests.Add(1)
			_ = json.NewEncoder(w).Encode(index)
		case "/releases/download/v2.1.0/release-info.json":
			stableDownloads.Add(1)
			_, _ = w.Write(stableBytes)
		case "/releases/download/v2.2.0-beta.1/release-info.json":
			betaDownloads.Add(1)
			_, _ = w.Write(betaBytes)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	store := &memoryStore{}
	s := New(store, "v2.0.0", "test", false)
	s.now = func() time.Time { return clock }
	s.client = server.Client()
	s.indexURL = server.URL + "/index"
	s.releaseBaseURL = server.URL

	if status, err := s.Check(ctx); err != nil || status.State != "update_available" {
		t.Fatalf("initial stable check: %+v %v", status, err)
	}
	if stableDownloads.Load() != 1 || betaDownloads.Load() != 0 {
		t.Fatalf("initial downloads: stable=%d beta=%d", stableDownloads.Load(), betaDownloads.Load())
	}

	status, err := s.SetChannel(ctx, "beta")
	if err != nil {
		t.Fatalf("switch channel: %v", err)
	}
	if status.Channel != "beta" || status.Target == nil || status.Target.Release.Version != beta.Release.Version || status.State != "update_available" {
		t.Fatalf("switched status: %+v", status)
	}
	if betaDownloads.Load() != 1 {
		t.Fatalf("beta target was not fetched immediately: %d", betaDownloads.Load())
	}
}

func TestCaseB_ChannelSwitchClearsPreviousChannelErrorOnSuccess(t *testing.T) {
	ctx := context.Background()
	clock := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	stable := fixture("v2.1.0")
	stableBytes, stableSHA := releaseSHA256(t, stable)
	var failIndex atomic.Bool
	index := Index{
		SchemaVersion: 2,
		Revision:      1,
		GeneratedAt:   clock,
		Channels: map[string]*Target{
			"stable": targetFor(stable.Release.Version, stableSHA),
			"rc":     nil,
			"beta":   nil,
		},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if failIndex.Load() {
			http.Error(w, "network error", http.StatusInternalServerError)
			return
		}
		switch r.URL.Path {
		case "/index":
			_ = json.NewEncoder(w).Encode(index)
		case "/releases/download/v2.1.0/release-info.json":
			_, _ = w.Write(stableBytes)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	store := &memoryStore{}
	s := New(store, "v2.0.0", "test", false)
	s.now = func() time.Time { return clock }
	s.client = server.Client()
	s.indexURL = server.URL + "/index"
	s.releaseBaseURL = server.URL

	st, err := s.Check(ctx)
	if err != nil || st.State != "update_available" || st.LastError != "" {
		t.Fatalf("stable check failed: %+v %v", st, err)
	}

	failIndex.Store(true)
	st, err = s.SetChannel(ctx, "beta")
	if err != nil {
		t.Fatalf("SetChannel error: %v", err)
	}
	if st.ChannelPreference != "beta" || st.LastError == "" {
		t.Fatalf("expected beta discovery failure: %+v", st)
	}

	failIndex.Store(false)
	st, err = s.SetChannel(ctx, "stable")
	if err != nil {
		t.Fatalf("switch back to stable error: %v", err)
	}
	if st.ChannelPreference != "stable" || st.LastError != "" || st.State != "update_available" || st.Target == nil || st.Target.Release.Version != "v2.1.0" {
		t.Fatalf("stable check should force re-discovery and clear error: %+v", st)
	}
}

func TestCaseC_NormalCheckWithinCooldownReusesResult(t *testing.T) {
	ctx := context.Background()
	clock := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	rel := fixture("v2.1.0")
	relBytes, relSHA := releaseSHA256(t, rel)
	var indexRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/index":
			indexRequests.Add(1)
			_ = json.NewEncoder(w).Encode(Index{
				SchemaVersion: 2,
				Revision:      1,
				GeneratedAt:   clock,
				Channels: map[string]*Target{
					"stable": targetFor("v2.1.0", relSHA),
					"rc":     nil,
					"beta":   nil,
				},
			})
		case "/releases/download/v2.1.0/release-info.json":
			_, _ = w.Write(relBytes)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	store := &memoryStore{}
	s := New(store, "v2.0.0", "test", false)
	s.now = func() time.Time { return clock }
	s.client = server.Client()
	s.indexURL = server.URL + "/index"
	s.releaseBaseURL = server.URL

	if _, err := s.Check(ctx); err != nil {
		t.Fatal(err)
	}
	if indexRequests.Load() != 1 {
		t.Fatalf("expected 1 index request, got %d", indexRequests.Load())
	}

	s.now = func() time.Time { return clock.Add(30 * time.Second) }
	if _, err := s.Check(ctx); err != nil {
		t.Fatal(err)
	}
	if indexRequests.Load() != 1 {
		t.Fatalf("expected cooldown reuse (1 index request), got %d", indexRequests.Load())
	}
}

func TestNoCandidateStateWhenChannelHasNoTarget(t *testing.T) {
	ctx := context.Background()
	clock := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	beta := fixture("v2.0.0-beta.1")
	_, betaSHA := releaseSHA256(t, beta)
	index := Index{
		SchemaVersion: 2,
		Revision:      1,
		GeneratedAt:   clock,
		Channels: map[string]*Target{
			"stable": nil,
			"rc":     nil,
			"beta":   targetFor(beta.Release.Version, betaSHA),
		},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/index" {
			_ = json.NewEncoder(w).Encode(index)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	store := &memoryStore{}
	s := New(store, "v2.0.0", "test", false)
	s.now = func() time.Time { return clock }
	s.client = server.Client()
	s.indexURL = server.URL + "/index"
	s.releaseBaseURL = server.URL

	st, err := s.Check(ctx)
	if err != nil {
		t.Fatalf("check failed: %v", err)
	}
	if st.State != "no_candidate" {
		t.Fatalf("expected state no_candidate, got %q", st.State)
	}
	if st.Target != nil {
		t.Fatalf("expected nil target for no_candidate, got %+v", st.Target)
	}
	if st.LastSuccess.IsZero() {
		t.Fatal("LastSuccess should be recorded on valid index discovery")
	}
	if st.LastError != "" {
		t.Fatalf("unexpected LastError: %s", st.LastError)
	}
}

func TestBreakingFlagEnforcesMigrationGuide(t *testing.T) {
	ctx := context.Background()
	clock := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)

	for _, tc := range []struct {
		name           string
		current        string
		target         string
		breaking       bool
		minDirect      *string
		expectedAction string
	}{
		{"breaking_true", "v2.0.0-beta.1", "v2.0.0-beta.2", true, nil, "migration_guide"},
		{"breaking_false", "v2.0.0-beta.1", "v2.0.0-beta.2", false, nil, "direct"},
		{"min_direct_met", "v2.0.0-beta.1", "v2.0.0-beta.2", false, new("v2.0.0-beta.1"), "direct"},
		{"min_direct_unmet", "v2.0.0-beta.1", "v2.0.0-beta.2", false, new("v2.0.0-beta.2"), "migration_guide"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rel := fixture(tc.target)
			rel.Update.Breaking = tc.breaking
			rel.Update.MigrationRequired = false
			rel.Update.MinimumDirectUpgradeVersion = tc.minDirect
			relBytes, relSHA := releaseSHA256(t, rel)
			index := Index{
				SchemaVersion: 2,
				Revision:      1,
				GeneratedAt:   clock,
				Channels: map[string]*Target{
					"stable": nil,
					"rc":     nil,
					"beta":   targetFor(rel.Release.Version, relSHA),
				},
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/index":
					_ = json.NewEncoder(w).Encode(index)
				case "/releases/download/" + rel.Release.Version + "/release-info.json":
					_, _ = w.Write(relBytes)
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()

			store := &memoryStore{}
			s := New(store, tc.current, "test", false)
			s.now = func() time.Time { return clock }
			s.client = server.Client()
			s.indexURL = server.URL + "/index"
			s.releaseBaseURL = server.URL

			st, err := s.Check(ctx)
			if err != nil {
				t.Fatalf("check failed: %v", err)
			}
			if st.State != "update_available" {
				t.Fatalf("expected update_available, got %q", st.State)
			}
			if st.UpgradeAction != tc.expectedAction {
				t.Fatalf("expected upgrade_action %q, got %q", tc.expectedAction, st.UpgradeAction)
			}
		})
	}
}

func TestSetChannelKeepsPreferenceWhenImmediateCheckFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "offline", http.StatusBadGateway)
	}))
	defer server.Close()

	store := &memoryStore{}
	s := New(store, "v2.0.0-beta.1", "test", false)
	s.client = server.Client()
	s.indexURL = server.URL + "/index"

	status, err := s.SetChannel(context.Background(), "beta")
	if err != nil {
		t.Fatalf("failed channel check should return its status: %v", err)
	}
	if status.ChannelPreference != "beta" || status.LastError == "" {
		t.Fatalf("unexpected failed channel status: %+v", status)
	}

	var persisted State
	if err := json.Unmarshal(store.data, &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.Preference != "beta" {
		t.Fatalf("channel preference was not persisted: %q", persisted.Preference)
	}
}

func TestReleasePolicyAndLinksFailClosed(t *testing.T) {
	// Valid guide URLs across GitHub repo and cpamp-docs.seakee.dev.
	for _, validGuide := range []string{
		Repository + "/releases/tag/v2.0.0-beta.1",
		Repository + "/blob/v2.0.0-beta.1/docs/release-notes/v2.0.0-beta.1-zh.md",
		"https://cpamp-docs.seakee.dev/next/operations/update.html",
		"https://cpamp-docs.seakee.dev/next/en/operations/update.html",
	} {
		info := fixture("v2.0.0-beta.1")
		info.Update.UpgradeGuideURL = validGuide
		if err := info.Validate("v2.0.0-beta.1"); err != nil {
			t.Fatalf("rejected valid guide %q: %v", validGuide, err)
		}
	}

	for _, guide := range []string{
		Repository + "/../../untrusted/guide",
		Repository + "/%2e%2e/%2e%2e/untrusted/guide",
		Repository + `/\..\..\untrusted/guide`,
		Repository + "/releases/tag/v2.0.0-beta.1?redirect=https://evil.example",
		Repository + "/releases/tag/v2.0.0-beta.1#fragment",
		"https://cpamp-docs.seakee.dev/next/operations/update.html?q=1",
		"https://cpamp-docs.seakee.dev/next/operations/update.html#section",
		"https://user:pass@cpamp-docs.seakee.dev/next/operations/update.html",
		"https://@github.com/seakee/CPA-Manager-Plus/releases",
		"http://github.com/seakee/CPA-Manager-Plus/releases",
		"http://cpamp-docs.seakee.dev/next/operations/update.html",
		"https://github.com.example/seakee/CPA-Manager-Plus/releases",
		"https://cpamp-docs.seakee.dev.evil/next/operations/update.html",
		"https://cpamp-docs.seakee.dev/",
	} {
		info := fixture("v2.0.0")
		info.Update.UpgradeGuideURL = guide
		if err := info.Validate("v2.0.0"); err == nil {
			t.Fatalf("accepted untrusted guide: %q", guide)
		}
	}
	data, err := json.Marshal(fixture("v2.0.0"))
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"breaking", "migration_required", "minimum_direct_upgrade_version"} {
		t.Run(key, func(t *testing.T) {
			var copy map[string]any
			if err := json.Unmarshal(data, &copy); err != nil {
				t.Fatal(err)
			}
			delete(copy["update"].(map[string]any), key)
			invalid, _ := json.Marshal(copy)
			var info ReleaseInfo
			if err := json.Unmarshal(invalid, &info); err == nil {
				t.Fatal("accepted omitted policy")
			}
		})
	}
}

func TestSchema2IndexAndReleaseValidation(t *testing.T) {
	if IndexURL != "https://raw.githubusercontent.com/seakee/CPA-Manager-Plus/update-channel/update-index-v2.json" {
		t.Fatalf("unexpected default IndexURL: %q", IndexURL)
	}

	clock := time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC)

	// Reject schema_version 1 index.
	v1Idx := Index{
		SchemaVersion: 1,
		Revision:      1,
		GeneratedAt:   clock,
		Channels: map[string]*Target{
			"stable": nil,
			"rc":     nil,
			"beta":   targetFor("v2.0.0-beta.1", "sha256:"+strings.Repeat("a", 64)),
		},
	}
	if v1Idx.Validate() == nil {
		t.Fatal("accepted schema_version 1 index")
	}

	validSHA := "sha256:" + strings.Repeat("a", 64)
	// Reject target with v1 version, wrong schema_version, bad path, missing sha256, or bad sha256.
	badTargets := []struct {
		name string
		ch   string
		t    Target
	}{
		{"v1_target", "stable", *targetFor("v1.14.10", validSHA)},
		{"target_schema_1", "beta", Target{Version: "v2.0.0-beta.1", SchemaVersion: 1, ReleaseInfoPath: "releases/download/v2.0.0-beta.1/release-info.json", ReleaseInfoSHA256: validSHA}},
		{"bad_path", "beta", Target{Version: "v2.0.0-beta.1", SchemaVersion: 2, ReleaseInfoPath: "/releases/download/v2.0.0-beta.1/release-info.json", ReleaseInfoSHA256: validSHA}},
		{"missing_sha", "beta", Target{Version: "v2.0.0-beta.1", SchemaVersion: 2, ReleaseInfoPath: "releases/download/v2.0.0-beta.1/release-info.json"}},
		{"bad_sha", "beta", Target{Version: "v2.0.0-beta.1", SchemaVersion: 2, ReleaseInfoPath: "releases/download/v2.0.0-beta.1/release-info.json", ReleaseInfoSHA256: "sha256:short"}},
	}
	for _, tc := range badTargets {
		t.Run(tc.name, func(t *testing.T) {
			idx := Index{
				SchemaVersion: 2,
				Revision:      1,
				GeneratedAt:   clock,
				Channels:      map[string]*Target{"stable": nil, "rc": nil, "beta": nil},
			}
			tcopy := tc.t
			idx.Channels[tc.ch] = &tcopy
			if idx.Validate() == nil {
				t.Fatalf("accepted invalid target %s", tc.name)
			}
		})
	}

	// Accept beta, rc, and stable stages under schema_version 2.
	for _, tag := range []string{"v2.0.0-beta.1", "v2.0.0-rc.1", "v2.0.0"} {
		info := fixture(tag)
		if err := info.Validate(tag); err != nil {
			t.Fatalf("valid schema 2 release %s failed: %v", tag, err)
		}
	}

	// Future additive field (e.g. slot) must decode cleanly without breaking validation.
	rawMap := map[string]any{}
	baseBytes, _ := json.Marshal(fixture("v2.0.0-beta.1"))
	_ = json.Unmarshal(baseBytes, &rawMap)
	rawMap["slot"] = map[string]any{"name": "default"}
	withSlot, _ := json.Marshal(rawMap)
	var slotInfo ReleaseInfo
	if err := json.Unmarshal(withSlot, &slotInfo); err != nil {
		t.Fatalf("failed to unmarshal release-info with future slot field: %v", err)
	}
	if err := slotInfo.Validate("v2.0.0-beta.1"); err != nil {
		t.Fatalf("failed to validate release-info with future slot field: %v", err)
	}

	// Reject schema 1 release, v-prefixed docker version_tag, duplicate/equal digests, legacy native asset.
	t.Run("rejects_schema1_release", func(t *testing.T) {
		info := fixture("v2.0.0-beta.1")
		info.SchemaVersion = 1
		if info.Validate("v2.0.0-beta.1") == nil {
			t.Fatal("accepted schema_version 1 release")
		}
	})
	t.Run("rejects_v_prefixed_docker_tag", func(t *testing.T) {
		info := fixture("v2.0.0-beta.1")
		info.Distribution.Docker.VersionTag = "v2.0.0-beta.1"
		if info.Validate("v2.0.0-beta.1") == nil {
			t.Fatal("accepted v-prefixed docker version_tag")
		}
	})
	t.Run("rejects_equal_slim_full_digests", func(t *testing.T) {
		info := fixture("v2.0.0-beta.1")
		same := info.Distribution.Docker.Images[0].Digest
		for i := range info.Distribution.Docker.Images {
			info.Distribution.Docker.Images[i].Digest = same
		}
		if info.Validate("v2.0.0-beta.1") == nil {
			t.Fatal("accepted identical slim and full digests")
		}
	})
	t.Run("rejects_registry_digest_mismatch", func(t *testing.T) {
		info := fixture("v2.0.0-beta.1")
		info.Distribution.Docker.Images[2].Digest = "sha256:" + strings.Repeat("9", 64)
		if info.Validate("v2.0.0-beta.1") == nil {
			t.Fatal("accepted registry digest mismatch")
		}
	})
	t.Run("rejects_wrong_full_image_tag", func(t *testing.T) {
		info := fixture("v2.0.0-beta.1")
		info.Distribution.Docker.Images[1].Tag = "2.0.0-beta.1"
		if info.Validate("v2.0.0-beta.1") == nil {
			t.Fatal("accepted full image without -full suffix")
		}
	})
	t.Run("rejects_legacy_native_asset_name", func(t *testing.T) {
		info := fixture("v2.0.0-beta.1")
		info.Distribution.Native.Assets = []string{"cpa-manager-plus_v2.0.0-beta.1_linux_amd64.tar.gz"}
		if info.Validate("v2.0.0-beta.1") == nil {
			t.Fatal("accepted legacy 1.x native asset name")
		}
	})
	t.Run("accepts_multiple_v2_native_assets", func(t *testing.T) {
		info := fixture("v2.0.0-beta.1")
		info.Distribution.Native.Assets = []string{
			"cpa-manager-plus-native-v2_v2.0.0-beta.1_linux_amd64.tar.gz",
			"cpa-manager-plus-native-v2_v2.0.0-beta.1_linux_arm64.tar.gz",
			"cpa-manager-plus-native-v2_v2.0.0-beta.1_windows_amd64.zip",
		}
		if err := info.Validate("v2.0.0-beta.1"); err != nil {
			t.Fatalf("rejected multi-platform v2 native assets: %v", err)
		}
	})
}

func TestDiscoverEnforcesReleaseInfoSHA256AndRejectsOmittedSHA(t *testing.T) {
	ctx := context.Background()
	clock := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	rel := fixture("v2.0.0-beta.2")
	relBytes, relSHA := releaseSHA256(t, rel)

	t.Run("mismatch_sha256_fails_before_cache", func(t *testing.T) {
		wrongSHA := "sha256:" + strings.Repeat("f", 64)
		if wrongSHA == relSHA {
			t.Fatal("collision")
		}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/index":
				_ = json.NewEncoder(w).Encode(Index{
					SchemaVersion: 2,
					Revision:      1,
					GeneratedAt:   clock,
					Channels: map[string]*Target{
						"stable": nil,
						"rc":     nil,
						"beta":   targetFor("v2.0.0-beta.2", wrongSHA),
					},
				})
			case "/releases/download/v2.0.0-beta.2/release-info.json":
				_, _ = w.Write(relBytes)
			default:
				http.NotFound(w, r)
			}
		}))
		defer srv.Close()

		store := &memoryStore{}
		s := New(store, "v2.0.0-beta.1", "test", false)
		s.client = srv.Client()
		s.indexURL = srv.URL + "/index"
		s.releaseBaseURL = srv.URL

		st, err := s.Check(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(st.LastError, "digest mismatch") {
			t.Fatalf("expected digest mismatch error, got %q", st.LastError)
		}
		if st.Target != nil || st.State == "update_available" {
			t.Fatalf("mismatched digest exposed target: %+v", st)
		}
	})

	t.Run("omitted_sha256_rejected_before_release_fetch", func(t *testing.T) {
		var releaseFetched atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/index":
				_ = json.NewEncoder(w).Encode(Index{
					SchemaVersion: 2,
					Revision:      1,
					GeneratedAt:   clock,
					Channels: map[string]*Target{
						"stable": nil,
						"rc":     nil,
						"beta":   targetFor("v2.0.0-beta.2", ""),
					},
				})
			case "/releases/download/v2.0.0-beta.2/release-info.json":
				releaseFetched.Add(1)
				_, _ = w.Write(relBytes)
			default:
				http.NotFound(w, r)
			}
		}))
		defer srv.Close()

		store := &memoryStore{}
		s := New(store, "v2.0.0-beta.1", "test", false)
		s.client = srv.Client()
		s.indexURL = srv.URL + "/index"
		s.releaseBaseURL = srv.URL

		st, err := s.Check(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if st.LastError != "invalid channel target digest" {
			t.Fatalf("expected invalid channel target digest, got %q", st.LastError)
		}
		if st.Target != nil || st.State == "update_available" || releaseFetched.Load() != 0 {
			t.Fatalf("omitted digest was not rejected before fetch: status=%+v downloads=%d", st, releaseFetched.Load())
		}
	})

	t.Run("matching_sha256_validates_and_caches", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/index":
				_ = json.NewEncoder(w).Encode(Index{
					SchemaVersion: 2,
					Revision:      1,
					GeneratedAt:   clock,
					Channels: map[string]*Target{
						"stable": nil,
						"rc":     nil,
						"beta":   targetFor("v2.0.0-beta.2", relSHA),
					},
				})
			case "/releases/download/v2.0.0-beta.2/release-info.json":
				_, _ = w.Write(relBytes)
			default:
				http.NotFound(w, r)
			}
		}))
		defer srv.Close()

		store := &memoryStore{}
		s := New(store, "v2.0.0-beta.1", "test", false)
		s.client = srv.Client()
		s.indexURL = srv.URL + "/index"
		s.releaseBaseURL = srv.URL

		st, err := s.Check(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if st.LastError != "" || st.State != "update_available" || st.Target == nil || st.Target.Release.Version != "v2.0.0-beta.2" {
			t.Fatalf("unexpected status: %+v", st)
		}
	})
}

func TestDiscoverBindsCachedReleaseInfoToTargetDigest(t *testing.T) {
	ctx := context.Background()
	clock := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)

	relA := fixture("v2.0.0-beta.2")
	relA.Content.Summary["en"] = "Initial beta 2 metadata"
	bytesA, shaA := releaseSHA256(t, relA)

	relB := fixture("v2.0.0-beta.2")
	relB.Content.Summary["en"] = "Updated beta 2 metadata"
	bytesB, shaB := releaseSHA256(t, relB)
	if shaA == shaB {
		t.Fatal("expected distinct release_info_sha256 digests")
	}

	t.Run("higher_revision_with_changed_sha256_refetches_and_rejects_old_bytes", func(t *testing.T) {
		var (
			mu        sync.Mutex
			servedIdx = Index{
				SchemaVersion: 2,
				Revision:      1,
				GeneratedAt:   clock,
				Channels: map[string]*Target{
					"stable": nil,
					"rc":     nil,
					"beta":   targetFor("v2.0.0-beta.2", shaA),
				},
			}
			servedBody = bytesA
			downloads  atomic.Int32
		)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			idx := servedIdx
			body := servedBody
			mu.Unlock()
			switch r.URL.Path {
			case "/index":
				_ = json.NewEncoder(w).Encode(idx)
			case "/releases/download/v2.0.0-beta.2/release-info.json":
				downloads.Add(1)
				_, _ = w.Write(body)
			default:
				http.NotFound(w, r)
			}
		}))
		defer srv.Close()

		store := &memoryStore{}
		now := clock
		s := New(store, "v2.0.0-beta.1", "test", false)
		s.now = func() time.Time { return now }
		s.client = srv.Client()
		s.indexURL = srv.URL + "/index"
		s.releaseBaseURL = srv.URL

		st, err := s.Check(ctx)
		if err != nil || st.State != "update_available" || st.Target == nil || st.Target.Content.Summary["en"] != "Initial beta 2 metadata" {
			t.Fatalf("initial Check = %+v, %v", st, err)
		}
		if downloads.Load() != 1 {
			t.Fatalf("initial downloads = %d, want 1", downloads.Load())
		}

		// Revision 2 updates release_info_sha256 to shaB, while server still serves bytesA:
		// discover must re-fetch, fail digest verification, and keep revision 1 + relA intact.
		mu.Lock()
		servedIdx = Index{
			SchemaVersion: 2,
			Revision:      2,
			GeneratedAt:   clock.Add(2 * time.Minute),
			Channels: map[string]*Target{
				"stable": nil,
				"rc":     nil,
				"beta":   targetFor("v2.0.0-beta.2", shaB),
			},
		}
		mu.Unlock()
		now = clock.Add(2 * time.Minute)

		stMismatch, err := s.Check(ctx)
		if err != nil {
			t.Fatalf("Check on mismatched revision-2 bytes returned unexpected store error: %v", err)
		}
		if downloads.Load() != 2 || !strings.Contains(stMismatch.LastError, "digest mismatch") {
			t.Fatalf("mismatch Check = %+v, downloads=%d; want digest mismatch after re-fetch", stMismatch, downloads.Load())
		}
		if s.state.Index == nil || s.state.Index.Revision != 1 || s.state.Releases["v2.0.0-beta.2"].Content.Summary["en"] != "Initial beta 2 metadata" {
			t.Fatalf("failed verification mutated cached index or release: index=%+v release=%+v", s.state.Index, s.state.Releases["v2.0.0-beta.2"])
		}

		// Serve bytesB matching shaB: next Check verifies and updates cached ReleaseInfo + ReleaseDigests.
		mu.Lock()
		servedBody = bytesB
		mu.Unlock()
		now = clock.Add(4 * time.Minute)

		stUpdated, err := s.Check(ctx)
		if err != nil || stUpdated.LastError != "" || stUpdated.State != "update_available" || stUpdated.Target == nil || stUpdated.Target.Content.Summary["en"] != "Updated beta 2 metadata" {
			t.Fatalf("updated Check = %+v, %v", stUpdated, err)
		}
		if downloads.Load() != 3 || s.state.Index.Revision != 2 || s.state.ReleaseDigests["v2.0.0-beta.2"] != shaB {
			t.Fatalf("post-update state: downloads=%d rev=%d digests=%v", downloads.Load(), s.state.Index.Revision, s.state.ReleaseDigests)
		}
	})

	t.Run("persisted_cache_lacking_release_digests_reverifies_against_index_digest", func(t *testing.T) {
		legacyPersisted, err := json.Marshal(State{
			SchemaVersion: 1,
			Preference:    "beta",
			LastAttempt:   clock.Add(-2 * time.Minute),
			LastSuccess:   clock.Add(-2 * time.Minute),
			Index: &Index{
				SchemaVersion: 2,
				Revision:      1,
				GeneratedAt:   clock.Add(-2 * time.Minute),
				Channels: map[string]*Target{
					"stable": nil,
					"rc":     nil,
					"beta":   targetFor("v2.0.0-beta.2", shaB),
				},
			},
			Releases: map[string]ReleaseInfo{
				"v2.0.0-beta.2": relA,
			},
		})
		if err != nil {
			t.Fatal(err)
		}

		var (
			mu         sync.Mutex
			servedBody = bytesA
			downloads  atomic.Int32
		)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			body := servedBody
			mu.Unlock()
			switch r.URL.Path {
			case "/index":
				_ = json.NewEncoder(w).Encode(Index{
					SchemaVersion: 2,
					Revision:      1,
					GeneratedAt:   clock.Add(-2 * time.Minute),
					Channels: map[string]*Target{
						"stable": nil,
						"rc":     nil,
						"beta":   targetFor("v2.0.0-beta.2", shaB),
					},
				})
			case "/releases/download/v2.0.0-beta.2/release-info.json":
				downloads.Add(1)
				_, _ = w.Write(body)
			default:
				http.NotFound(w, r)
			}
		}))
		defer srv.Close()

		store := &memoryStore{data: legacyPersisted}
		now := clock
		s := New(store, "v2.0.0-beta.1", "test", false)
		s.now = func() time.Time { return now }
		s.client = srv.Client()
		s.indexURL = srv.URL + "/index"
		s.releaseBaseURL = srv.URL

		// Status before Check must not expose unverified cached Releases lacking release_digests.
		initialStatus, err := s.Status(ctx)
		if err != nil {
			t.Fatalf("Status() error = %v", err)
		}
		if initialStatus.Target != nil || initialStatus.State == "update_available" {
			t.Fatalf("Status() exposed unverified cached release without release_digests: %+v", initialStatus)
		}

		// First Check sees missing release_digests entry, downloads bytesA, and rejects mismatch against shaB.
		mismatchStatus, err := s.Check(ctx)
		if err != nil {
			t.Fatalf("Check() error = %v", err)
		}
		if downloads.Load() != 1 || !strings.Contains(mismatchStatus.LastError, "digest mismatch") || mismatchStatus.Target != nil {
			t.Fatalf("Check() with mismatched remote bytes = %+v, downloads=%d", mismatchStatus, downloads.Load())
		}

		// Serve matching bytesB: next Check verifies shaB and persists release_digests.
		mu.Lock()
		servedBody = bytesB
		mu.Unlock()
		now = clock.Add(2 * time.Minute)

		verifiedStatus, err := s.Check(ctx)
		if err != nil || verifiedStatus.LastError != "" || verifiedStatus.State != "update_available" || verifiedStatus.Target == nil || verifiedStatus.Target.Content.Summary["en"] != "Updated beta 2 metadata" {
			t.Fatalf("verified Check() = %+v, %v", verifiedStatus, err)
		}
		if downloads.Load() != 2 {
			t.Fatalf("downloads = %d, want 2", downloads.Load())
		}
		var persisted State
		if err := json.Unmarshal(store.data, &persisted); err != nil {
			t.Fatal(err)
		}
		if persisted.ReleaseDigests["v2.0.0-beta.2"] != shaB {
			t.Fatalf("persisted ReleaseDigests = %v, want %q", persisted.ReleaseDigests, shaB)
		}
	})
}

func TestLoadClearsLegacySchema1SQLiteCache(t *testing.T) {
	ctx := context.Background()
	clock := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)

	legacyBlob := `{
		"schema_version": 1,
		"channel_preference": "beta",
		"last_attempt_at": "2026-11-25T11:59:30Z",
		"last_success_at": "2026-11-25T11:59:30Z",
		"last_error": "old error",
		"index": {
			"schema_version": 1,
			"revision": 9,
			"generated_at": "2026-11-20T00:00:00Z",
			"channels": {
				"stable": {"version": "v1.14.10"},
				"rc": null,
				"beta": {"version": "v1.14.11-beta.1"}
			}
		},
		"releases": {
			"v1.14.10": {
				"schema_version": 1,
				"release": {"version": "v1.14.10", "stage": "stable", "source_commit": "0123456789012345678901234567890123456789"},
				"content": {"summary": {"zh": "旧版", "en": "Old"}, "notes": {"zh": "https://github.com/seakee/CPA-Manager-Plus/blob/v1.14.10/docs/release-notes/v1.14.10-zh.md", "en": "https://github.com/seakee/CPA-Manager-Plus/blob/v1.14.10/docs/release-notes/v1.14.10-en.md"}},
				"update": {"breaking": false, "migration_required": false, "minimum_direct_upgrade_version": null, "upgrade_guide_url": "https://github.com/seakee/CPA-Manager-Plus/releases/tag/v1.14.10"},
				"distribution": {"docker": {"image": "seakee/cpa-manager-plus", "version_tag": "v1.14.10"}, "native": {"assets": ["cpa-manager-plus_v1.14.10_linux_amd64.tar.gz"]}},
				"compatibility": {"minimum_cpa_version": null}
			}
		}
	}`

	store := &memoryStore{data: []byte(legacyBlob)}
	s, downloads, indices := testService(t, "v2.0.0-beta.1", "v2.0.0-beta.2", store)
	s.now = func() time.Time { return clock }

	// SavedChannel preserves preference without error.
	pref, err := s.SavedChannel(ctx)
	if err != nil || pref != "beta" {
		t.Fatalf("SavedChannel = %q, %v; want beta, nil", pref, err)
	}

	// Status smoothly drops legacy schema_version 1 cache without crashing.
	st, err := s.Status(ctx)
	if err != nil {
		t.Fatalf("Status on legacy cache failed: %v", err)
	}
	if st.State != "never_checked" || st.ChannelPreference != "beta" || st.Target != nil || st.LastError != "" || !st.Stale {
		t.Fatalf("unexpected status after legacy cache clear: %+v", st)
	}

	// Immediate Check must bypass old cooldown and fetch v2 index & release.
	st, err = s.Check(ctx)
	if err != nil {
		t.Fatalf("Check after legacy cache failed: %v", err)
	}
	if indices.Load() != 1 || downloads.Load() != 1 {
		t.Fatalf("expected immediate v2 fetch, indices=%d downloads=%d", indices.Load(), downloads.Load())
	}
	if st.State != "update_available" || st.Target == nil || st.Target.Release.Version != "v2.0.0-beta.2" {
		t.Fatalf("unexpected status after v2 check: %+v", st)
	}
}

func TestComposeAndPairingValidation(t *testing.T) {
	info := fixture("v2.0.0-beta.1")
	info.Distribution.Compose.SHA256 = "sha256:bad"
	if info.Validate("v2.0.0-beta.1") == nil {
		t.Fatal("accepted invalid compose sha256")
	}

	info = fixture("v2.0.0-beta.1")
	info.Pairing.ComposeSHA256 = "sha256:" + strings.Repeat("a", 64)
	if info.Validate("v2.0.0-beta.1") == nil {
		t.Fatal("accepted pairing compose sha256 mismatch")
	}

	info = fixture("v2.0.0-beta.1")
	info.Pairing.SourceCommit = strings.Repeat("b", 40)
	if info.Validate("v2.0.0-beta.1") == nil {
		t.Fatal("accepted pairing source_commit mismatch")
	}
}

func TestProducerReleaseInfoFixtureValidatesInGoConsumer(t *testing.T) {
	raw, err := os.ReadFile("../../../../../tests/fixtures/release-v2.0.0-beta.1-info.json")
	if err != nil {
		t.Fatalf("read producer release-info fixture: %v", err)
	}
	var info ReleaseInfo
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&info); err != nil {
		t.Fatalf("strict decode producer release-info fixture: %v", err)
	}
	if err := info.Validate("v2.0.0-beta.1"); err != nil {
		t.Fatalf("Validate producer release-info fixture: %v", err)
	}
}
