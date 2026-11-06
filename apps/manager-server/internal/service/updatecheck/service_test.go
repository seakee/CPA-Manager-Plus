package updatecheck

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
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
	info.SchemaVersion = 1
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
	info.Distribution.Docker.Image = "seakee/cpa-manager-plus"
	info.Distribution.Docker.VersionTag = tag
	for _, os := range []string{"darwin", "linux", "windows"} {
		for _, arch := range []string{"amd64", "arm64"} {
			ext := ".tar.gz"
			if os == "windows" {
				ext = ".zip"
			}
			info.Distribution.Native.Assets = append(info.Distribution.Native.Assets, "cpa-manager-plus_"+tag+"_"+os+"_"+arch+ext)
		}
	}
	return info
}
func testService(t *testing.T, current, tag string, m *memoryStore) (*Service, *atomic.Int32, *atomic.Int32) {
	t.Helper()
	downloads, indices := &atomic.Int32{}, &atomic.Int32{}
	info := fixture(tag)
	idx := Index{SchemaVersion: 1, Revision: 1, GeneratedAt: time.Now(), Channels: map[string]*Target{"stable": {Version: tag}, "rc": {Version: tag}, "beta": {Version: tag}}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/index" {
			indices.Add(1)
			_ = json.NewEncoder(w).Encode(idx)
		} else {
			downloads.Add(1)
			_ = json.NewEncoder(w).Encode(info)
		}
	}))
	t.Cleanup(srv.Close)
	s := New(m, current, "test", true)
	s.client = srv.Client()
	s.indexURL = srv.URL + "/index"
	s.releaseURL = func(string) string { return srv.URL + "/info" }
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
	s, downloads, indices := testService(t, "v1.0.0", "v1.1.0", m)
	var wg sync.WaitGroup
	var claims atomic.Int32
	for i := 0; i < 20; i++ {
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
	_ = s.Dismiss(ctx, "v1.1.0")
	restarted, d2, _ := testService(t, "v1.0.0", "v1.1.0", m)
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
	}{{"v1.1.0", "up_to_date", 1}, {"v2.0.0", "ahead_of_channel", 0}, {"dev", "unknown_version", 0}} {
		t.Run(tc.current, func(t *testing.T) {
			s, d, _ := testService(t, tc.current, "v1.1.0", &memoryStore{})
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
	s, d, _ := testService(t, "v1.0.0", "v1.1.0", m)
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
	s, _, _ := testService(t, "v1.0.0", "v2.0.0", &memoryStore{})
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
	clock := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	stable := fixture("v1.1.0")
	beta := fixture("v1.2.0-beta.1")
	index := Index{
		SchemaVersion: 1,
		Revision:      1,
		GeneratedAt:   clock,
		Channels: map[string]*Target{
			"stable": {Version: stable.Release.Version},
			"rc":     {Version: "v1.1.0-rc.1"},
			"beta":   {Version: beta.Release.Version},
		},
	}
	var indexRequests, stableDownloads, betaDownloads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/index" {
			indexRequests.Add(1)
			_ = json.NewEncoder(w).Encode(index)
			return
		}
		switch r.URL.Path {
		case "/v1.1.0":
			stableDownloads.Add(1)
			_ = json.NewEncoder(w).Encode(stable)
		case "/v1.2.0-beta.1":
			betaDownloads.Add(1)
			_ = json.NewEncoder(w).Encode(beta)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	store := &memoryStore{}
	s := New(store, "v1.0.0", "test", false)
	s.now = func() time.Time { return clock }
	s.client = server.Client()
	s.indexURL = server.URL + "/index"
	s.releaseURL = func(tag string) string { return server.URL + "/" + tag }

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
	clock := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	stable := fixture("v1.1.0")
	var failIndex atomic.Bool
	index := Index{
		SchemaVersion: 1,
		Revision:      1,
		GeneratedAt:   clock,
		Channels: map[string]*Target{
			"stable": {Version: stable.Release.Version},
			"rc":     nil,
			"beta":   nil,
		},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if failIndex.Load() {
			http.Error(w, "network error", http.StatusInternalServerError)
			return
		}
		if r.URL.Path == "/index" {
			_ = json.NewEncoder(w).Encode(index)
			return
		}
		if r.URL.Path == "/v1.1.0" {
			_ = json.NewEncoder(w).Encode(stable)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	store := &memoryStore{}
	s := New(store, "v1.0.0", "test", false)
	s.now = func() time.Time { return clock }
	s.client = server.Client()
	s.indexURL = server.URL + "/index"
	s.releaseURL = func(tag string) string { return server.URL + "/" + tag }

	// 1. Initial stable check succeeds.
	st, err := s.Check(ctx)
	if err != nil || st.State != "update_available" || st.LastError != "" {
		t.Fatalf("stable check failed: %+v %v", st, err)
	}

	// 2. Beta discovery fails.
	failIndex.Store(true)
	st, err = s.SetChannel(ctx, "beta")
	if err != nil {
		t.Fatalf("SetChannel error: %v", err)
	}
	if st.ChannelPreference != "beta" || st.LastError == "" {
		t.Fatalf("expected beta discovery failure: %+v", st)
	}

	// 3. Switch back to stable within cooldown window (clock unchanged).
	failIndex.Store(false)
	st, err = s.SetChannel(ctx, "stable")
	if err != nil {
		t.Fatalf("switch back to stable error: %v", err)
	}
	if st.ChannelPreference != "stable" || st.LastError != "" || st.State != "update_available" || st.Target == nil || st.Target.Release.Version != "v1.1.0" {
		t.Fatalf("stable check should force re-discovery and clear error: %+v", st)
	}
}

func TestCaseC_NormalCheckWithinCooldownReusesResult(t *testing.T) {
	ctx := context.Background()
	clock := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	var indexRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/index" {
			indexRequests.Add(1)
			_ = json.NewEncoder(w).Encode(Index{
				SchemaVersion: 1,
				Revision:      1,
				GeneratedAt:   clock,
				Channels: map[string]*Target{
					"stable": {Version: "v1.1.0"},
					"rc":     nil,
					"beta":   nil,
				},
			})
			return
		}
		if r.URL.Path == "/v1.1.0" {
			_ = json.NewEncoder(w).Encode(fixture("v1.1.0"))
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	store := &memoryStore{}
	s := New(store, "v1.0.0", "test", false)
	s.now = func() time.Time { return clock }
	s.client = server.Client()
	s.indexURL = server.URL + "/index"
	s.releaseURL = func(tag string) string { return server.URL + "/" + tag }

	if _, err := s.Check(ctx); err != nil {
		t.Fatal(err)
	}
	if indexRequests.Load() != 1 {
		t.Fatalf("expected 1 index request, got %d", indexRequests.Load())
	}

	// Immediate second Check() within 60s cooldown.
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
	clock := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	beta := fixture("v2.0.0-beta.1")
	index := Index{
		SchemaVersion: 1,
		Revision:      1,
		GeneratedAt:   clock,
		Channels: map[string]*Target{
			"stable": nil,
			"rc":     nil,
			"beta":   {Version: beta.Release.Version},
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
	s := New(store, "v1.0.0", "test", false)
	s.now = func() time.Time { return clock }
	s.client = server.Client()
	s.indexURL = server.URL + "/index"
	s.releaseURL = func(tag string) string { return server.URL + "/" + tag }

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
	clock := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)

	for _, tc := range []struct {
		name           string
		breaking       bool
		expectedAction string
	}{
		{"breaking_true", true, "migration_guide"},
		{"breaking_false", false, "direct"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rel := fixture("v1.1.0")
			rel.Update.Breaking = tc.breaking
			rel.Update.MigrationRequired = false
			index := Index{
				SchemaVersion: 1,
				Revision:      1,
				GeneratedAt:   clock,
				Channels: map[string]*Target{
					"stable": {Version: rel.Release.Version},
					"rc":     nil,
					"beta":   nil,
				},
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/index" {
					_ = json.NewEncoder(w).Encode(index)
					return
				}
				if r.URL.Path == "/v1.1.0" {
					_ = json.NewEncoder(w).Encode(rel)
					return
				}
				http.NotFound(w, r)
			}))
			defer server.Close()

			store := &memoryStore{}
			s := New(store, "v1.0.0", "test", false)
			s.now = func() time.Time { return clock }
			s.client = server.Client()
			s.indexURL = server.URL + "/index"
			s.releaseURL = func(tag string) string { return server.URL + "/" + tag }

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
	s := New(store, "v1.0.0", "test", false)
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
	for _, guide := range []string{
		Repository + "/../../untrusted/guide",
		Repository + "/%2e%2e/%2e%2e/untrusted/guide",
		Repository + `/\..\..\untrusted/guide`,
		"http://github.com/seakee/CPA-Manager-Plus/releases",
		"https://github.com.example/seakee/CPA-Manager-Plus/releases",
	} {
		info := fixture("v2.0.0")
		info.Update.UpgradeGuideURL = guide
		if err := info.Validate("v2.0.0"); err == nil {
			t.Fatalf("accepted guide outside trusted repository: %q", guide)
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

// The released v1.14.10 binary is the consumer that must never treat the v2
// Beta as a directly installable 1.x upgrade. Its service.go discovery logic is
// preserved in this package (only persistence helpers changed after v1.14.10),
// and metadata.go/version.go are byte-identical to the v1.14.10 tag, so this
// test exercises the real released gate against the v2 release-info fixture.
func TestV11410ConsumerWithV2BetaIndexNeverOffersDirectV2Install(t *testing.T) {
	ctx := context.Background()
	clock := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	fixtureBytes, err := os.ReadFile("../../../../../tests/fixtures/release-v2.0.0-beta.1-info.json")
	if err != nil {
		t.Fatal(err)
	}
	var beta ReleaseInfo
	if err = json.Unmarshal(fixtureBytes, &beta); err != nil {
		t.Fatal(err)
	}
	if beta.SchemaVersion != 2 || beta.Release.Version != "v2.0.0-beta.1" {
		t.Fatalf("unexpected v2 fixture identity: schema=%d version=%q", beta.SchemaVersion, beta.Release.Version)
	}
	// The released 1.x validator rejects schema_version 2 outright.
	if err = beta.Validate(beta.Release.Version); err == nil {
		t.Fatal("v1.14.10 consumer accepted a schema_version 2 release info")
	}
	stableInfo := fixture("v1.14.10")
	// Exact producer shape: a v2 Beta beta-channel target carries
	// schema_version 2, which the 1.x consumer ignores at the index level and
	// rejects when it reads the release info.
	mixedIndexJSON := `{"schema_version":1,"revision":1,"generated_at":"2026-11-06T12:00:00Z","channels":{"stable":{"version":"v1.14.10"},"rc":null,"beta":{"version":"v2.0.0-beta.1","schema_version":2}}}`
	var mixedIndex Index
	if err = json.Unmarshal([]byte(mixedIndexJSON), &mixedIndex); err != nil {
		t.Fatal(err)
	}
	if err = mixedIndex.Validate(); err != nil {
		t.Fatalf("v1.14.10 consumer rejected the mixed update index: %v", err)
	}
	serve := func(t *testing.T, betaDownloads *atomic.Int32) *httptest.Server {
		t.Helper()
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/index":
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(mixedIndexJSON))
			case "/v1.14.10":
				_ = json.NewEncoder(w).Encode(stableInfo)
			case "/v2.0.0-beta.1":
				betaDownloads.Add(1)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write(fixtureBytes)
			default:
				http.NotFound(w, r)
			}
		}))
		t.Cleanup(server.Close)
		return server
	}

	for _, preference := range []string{"auto", "stable"} {
		t.Run("prefers_"+preference, func(t *testing.T) {
			var betaDownloads atomic.Int32
			server := serve(t, &betaDownloads)
			preferenceJSON := `{"schema_version":1,"channel_preference":"auto","releases":{}}`
			if preference != "auto" {
				preferenceJSON = `{"schema_version":1,"channel_preference":"` + preference + `","releases":{}}`
			}
			store := &memoryStore{data: []byte(preferenceJSON)}
			s := New(store, "v1.14.10", "test", false)
			s.now = func() time.Time { return clock }
			s.client = server.Client()
			s.indexURL = server.URL + "/index"
			s.releaseURL = func(tag string) string { return server.URL + "/" + tag }

			status, checkErr := s.Check(ctx)
			if checkErr != nil {
				t.Fatalf("check failed: %v", checkErr)
			}
			if status.LastError != "" {
				t.Fatalf("default %s channel must stay error-free: %q", preference, status.LastError)
			}
			if status.State != "up_to_date" {
				t.Fatalf("expected up_to_date on v1.14.10, got %q", status.State)
			}
			if status.UpgradeAction != "" {
				t.Fatalf("v2 Beta leaked an upgrade action on the %s channel: %q", preference, status.UpgradeAction)
			}
			if status.Target == nil || status.Target.Release.Version != "v1.14.10" {
				t.Fatalf("expected the 1.x stable target, got %+v", status.Target)
			}
			if betaDownloads.Load() != 0 {
				t.Fatal("default channels must never fetch the v2 Beta release info")
			}
			if info, claimErr := s.Claim(ctx); info != nil || claimErr != nil {
				t.Fatalf("v2 Beta must not raise a notification: %v %v", info, claimErr)
			}
		})
	}

	t.Run("manual_beta_fails_closed", func(t *testing.T) {
		var betaDownloads atomic.Int32
		server := serve(t, &betaDownloads)
		store := &memoryStore{}
		s := New(store, "v1.14.10", "test", false)
		s.now = func() time.Time { return clock }
		s.client = server.Client()
		s.indexURL = server.URL + "/index"
		s.releaseURL = func(tag string) string { return server.URL + "/" + tag }

		status, setErr := s.SetChannel(ctx, "beta")
		if setErr != nil {
			t.Fatalf("channel switch must surface a status, not an error: %v", setErr)
		}
		if status.Channel != "beta" || status.ChannelPreference != "beta" {
			t.Fatalf("beta channel was not selected: %+v", status)
		}
		if status.LastError == "" {
			t.Fatal("schema_version 2 must fail closed on the beta channel")
		}
		if status.Target != nil {
			t.Fatalf("v1.14.10 must not expose the v2 Beta target: %+v", status.Target)
		}
		if status.State == "update_available" {
			t.Fatalf("v1.14.10 treated the v2 Beta as an available upgrade")
		}
		if status.UpgradeAction == "direct" {
			t.Fatal("v1.14.10 must never offer a direct v2 install")
		}
		if betaDownloads.Load() == 0 {
			t.Fatal("the beta target should be fetched and rejected, not silently skipped")
		}
		if info, claimErr := s.Claim(ctx); info != nil || claimErr != nil {
			t.Fatalf("rejected v2 Beta must not raise a notification: %v %v", info, claimErr)
		}
		var persisted State
		if err := json.Unmarshal(store.data, &persisted); err != nil {
			t.Fatal(err)
		}
		if _, cached := persisted.Releases["v2.0.0-beta.1"]; cached {
			t.Fatal("rejected v2 Beta release info must not be cached")
		}
	})
}
