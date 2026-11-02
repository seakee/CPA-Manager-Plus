package upgradewizard

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/controller/upgradesurface"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/setting"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/upgradeoperation"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/legacypreflight"
	upgradeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/upgrade"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/upgrade/copyinmarker"
)

const (
	testPrefix = "/.well-known/cpamp-bootstrap/api/v2"
	canonical  = "/cpamp-preview-abc123/api/v2"
	// testAdminKey is the source administrator credential the bootstrap Upgrade
	// entry verifies; the wizard routes must accept exactly this key.
	testAdminKey = "legacy-manager-admin-credential"
	// foreignKey is a well-formed credential that is not the source's.
	foreignKey = "another-manager-admin-credential"
)

func bearer(key string) string { return "Bearer " + key }

func request(t *testing.T, handler http.Handler, method, path, authorization string, want int) (int, string) {
	t.Helper()
	r := httptest.NewRequest(method, path, nil)
	r.Host = "manager.test"
	if authorization != "" {
		r.Header.Set("Authorization", authorization)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != want {
		t.Fatalf("%s %s: got %d want %d: %s", method, path, w.Code, want, w.Body.String())
	}
	return w.Code, w.Body.String()
}

// legacySourceDir writes the smallest 1.x data directory the read-only preflight
// will inspect, carrying the administrator credential the Upgrade entry verifies.
// The report may be blocked (a synthetic source has no pinned version) — the
// route contract is a 200 report either way.
func legacySourceDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "data.key"), []byte("legacy-data-key-material"), 0o600); err != nil {
		t.Fatal(err)
	}
	credential, err := security.NewAdminCredential(testAdminKey, "test")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(credential)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`pragma journal_mode = delete;
create table settings (key text primary key, value text not null);
create table usage_events (id integer primary key autoincrement, event_hash text not null unique, timestamp_ms integer not null);`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`insert into settings (key, value) values ('admin_credential_v1', ?)`, string(raw)); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "usage.sqlite.manager.lock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func newHandler(legacyRoot, database string) *Handler {
	return &Handler{
		LegacyRoot: func() string { return legacyRoot },
		Database:   func() string { return database },
		Timeout:    10 * time.Second,
	}
}

// TestRoutesRefuseCallersWithoutTheSourceCredential is the negative identity
// contract: mounting the handler authenticates every request, so no absence of a
// credential — and no other scheme — can read as allow.
func TestRoutesRefuseCallersWithoutTheSourceCredential(t *testing.T) {
	handler := newHandler(legacySourceDir(t), "")
	for name, authorization := range map[string]string{
		"absent":           "",
		"fresh-session":    "Fresh installation-session-token",
		"other-scheme":     "Basic b3BlcmF0b3I6cGFzcw==",
		"no-value":         "Bearer ",
		"padded":           bearer(" " + testAdminKey),
		"foreign-key":      bearer(foreignKey),
		"bare-key":         testAdminKey,
		"key-as-scheme":    bearer(testAdminKey) + " extra",
		"empty-key-bearer": "Bearer",
	} {
		t.Run(name, func(t *testing.T) {
			for _, prefix := range []string{testPrefix, canonical} {
				for _, resource := range []string{PreflightSuffix, StatusSuffix} {
					code, body := request(t, handler, http.MethodGet, prefix+resource, authorization, http.StatusUnauthorized)
					if code != http.StatusUnauthorized || !strings.Contains(body, `"`+CodeAdminUnauthorized+`"`) {
						t.Fatalf("%s%s answered %d: %s", prefix, resource, code, body)
					}
				}
			}
		})
	}
	// Two Authorization values are two credentials; neither is a decision.
	r := httptest.NewRequest(http.MethodGet, testPrefix+PreflightSuffix, nil)
	r.Header.Add("Authorization", bearer(testAdminKey))
	r.Header.Add("Authorization", bearer(testAdminKey))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("duplicate Authorization = %d", w.Code)
	}
	// The credential the entry page verified is accepted on both routes.
	request(t, handler, http.MethodGet, testPrefix+PreflightSuffix, bearer(testAdminKey), http.StatusOK)
}

// TestPreflightServesAReport covers the read contract on both prefixes: the
// method and path gates, and a report with every check and no source detail.
func TestPreflightServesAReport(t *testing.T) {
	root := legacySourceDir(t)
	handler := newHandler(root, "")

	request(t, handler, http.MethodPost, testPrefix+PreflightSuffix, bearer(testAdminKey), http.StatusMethodNotAllowed)
	for _, path := range []string{
		testPrefix + PreflightSuffix + "/",
		testPrefix + "/installation/upgrade/preflightx",
		testPrefix + "/installation/upgrade",
		testPrefix + PreflightSuffix + "?legacy=/etc",
	} {
		request(t, handler, http.MethodGet, path, bearer(testAdminKey), http.StatusNotFound)
	}

	for _, prefix := range []string{testPrefix, canonical} {
		_, body := request(t, handler, http.MethodGet, prefix+PreflightSuffix, bearer(testAdminKey), http.StatusOK)
		var report preflightDocument
		if err := json.Unmarshal([]byte(body), &report); err != nil {
			t.Fatalf("decode preflight: %v: %s", err, body)
		}
		if report.ContractVersion != Contract || len(report.Checks) == 0 {
			t.Fatalf("preflight document = %+v", report)
		}
		if report.Ready && len(report.Blocked) != 0 {
			t.Fatalf("a ready report carried blocked codes: %s", body)
		}
		if report.Blocked == nil || report.ParityGaps.AffectedDates == nil {
			t.Fatalf("preflight omitted a list: %s", body)
		}
		for _, secret := range []string{root, "legacy-data-key-material", testAdminKey} {
			if strings.Contains(body, secret) {
				t.Fatalf("preflight leaked %q: %s", secret, body)
			}
		}
		if report.Fingerprint != "" && !strings.HasSuffix(report.Fingerprint, "…") && len(report.Fingerprint) > 12 {
			t.Fatalf("fingerprint is not abbreviated: %q", report.Fingerprint)
		}
	}
}

// TestPreflightMissingLegacyRootIsTyped proves a missing environment is a typed
// code the wizard can name, never a 500 and never a fabricated ready report. The
// caller still has to present a credential first.
func TestPreflightMissingLegacyRootIsTyped(t *testing.T) {
	cases := []struct {
		name, root, code string
	}{
		{name: "unconfigured", root: "", code: CodeLegacyRootMissing},
		{name: "absent", root: filepath.Join(t.TempDir(), "absent"), code: CodeLegacyRootMissing},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, body := request(t, newHandler(testCase.root, ""), http.MethodGet, testPrefix+PreflightSuffix, bearer(testAdminKey), http.StatusServiceUnavailable)
			if !strings.Contains(body, `"`+testCase.code+`"`) {
				t.Fatalf("code %s missing: %s", testCase.code, body)
			}
		})
	}
	file := filepath.Join(t.TempDir(), "legacy.sqlite")
	if err := os.WriteFile(file, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, body := request(t, newHandler(file, ""), http.MethodGet, testPrefix+PreflightSuffix, bearer(testAdminKey), http.StatusServiceUnavailable)
	if !strings.Contains(body, `"`+CodeLegacyRootUnusable+`"`) {
		t.Fatalf("unusable root code missing: %s", body)
	}
	// A source without the recorded credential proves nothing: a well-formed key
	// is not accepted because the root exists. The source here has no database at
	// all, so the answer names that, not an ownership marker.
	_, body = request(t, newHandler(t.TempDir(), ""), http.MethodGet, testPrefix+PreflightSuffix, bearer(testAdminKey), http.StatusServiceUnavailable)
	if !strings.Contains(body, `"`+CodeSourceMissing+`"`) {
		t.Fatalf("missing source code missing: %s", body)
	}
}

// TestStatusReportsAnAbsentSourceWithItsOwnCode proves a missing source is named
// as a missing source on both routes, with a typed code the wizard can describe,
// rather than as an ownership marker the mount does not have.
func TestStatusReportsAnAbsentSourceWithItsOwnCode(t *testing.T) {
	handler := newHandler(t.TempDir(), "")
	for _, resource := range []string{PreflightSuffix, StatusSuffix} {
		_, body := request(t, handler, http.MethodGet, testPrefix+resource, bearer(testAdminKey), http.StatusServiceUnavailable)
		if !strings.Contains(body, `"`+CodeSourceMissing+`"`) {
			t.Fatalf("%s: missing source code absent: %s", resource, body)
		}
		if strings.Contains(body, `"upgrade_source_lock_unavailable"`) {
			t.Fatalf("%s: an absent source was reported as an ownership marker: %s", resource, body)
		}
	}
}

// TestCredentialDeadlineIsATimeoutNotAnUnreadableSource proves a deadline is
// named as a deadline on both routes. A slow but healthy volume and an
// unreadable one need different operator responses, so the wizard must not show
// the repair copy for a condition a retry fixes.
func TestCredentialDeadlineIsATimeoutNotAnUnreadableSource(t *testing.T) {
	handler := newHandler(legacySourceDir(t), "")
	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	for _, prefix := range []string{testPrefix, canonical} {
		for _, resource := range []string{PreflightSuffix, StatusSuffix} {
			r := httptest.NewRequest(http.MethodGet, prefix+resource, nil).WithContext(expired)
			r.Host = "manager.test"
			r.Header.Set("Authorization", bearer(testAdminKey))
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			body := w.Body.String()
			if w.Code != http.StatusServiceUnavailable {
				t.Fatalf("%s%s answered %d: %s", prefix, resource, w.Code, body)
			}
			if !strings.Contains(body, `"`+CodeCredentialTimeout+`"`) {
				t.Fatalf("%s%s did not name the deadline: %s", prefix, resource, body)
			}
			if strings.Contains(body, `"`+CodeSourceUnreadable+`"`) {
				t.Fatalf("%s%s reported a deadline as an unreadable source: %s", prefix, resource, body)
			}
		}
	}
}

// TestSymlinkedSourceStaysUsableOnBothRoutes is the reachability contract for a
// mount reached through a link. The routes resolve the credential with the
// surface's one rule and one input — the mount this request inspected — so a
// mount the shared file policy refuses holds no database, the credential is the
// Manager store, and the surface stays usable on both prefixes and both routes.
// This test used to assert the 503 the old nil resolution produced, which is
// exactly the bricking behaviour it has to prevent now; the refused file must
// never reach the credential read, and the mount's own key must be refused
// because the surface never read it.
func TestSymlinkedSourceStaysUsableOnBothRoutes(t *testing.T) {
	const managerKey = "manager-store-key-for-a-refused-mount"
	root := legacySourceDir(t)
	database := filepath.Join(root, "usage.sqlite")
	if err := os.Rename(database, database+".real"); err != nil {
		t.Fatalf("move fixture database: %v", err)
	}
	if err := os.Symlink(database+".real", database); err != nil {
		t.Skipf("create database symlink: %v", err)
	}
	handler := newHandler(root, managerCredentialStore(t, managerKey))
	for _, prefix := range []string{testPrefix, canonical} {
		for _, resource := range []string{PreflightSuffix, StatusSuffix} {
			_, body := request(t, handler, http.MethodGet, prefix+resource, bearer(managerKey), http.StatusOK)
			if !strings.Contains(body, `"`+CodeSourceSymlinked+`"`) {
				t.Fatalf("%s%s did not name the refused mount: %s", prefix, resource, body)
			}
			if strings.Contains(body, `"`+CodeAdminUnauthorized+`"`) {
				t.Fatalf("%s%s reported a symlinked mount as a credential verdict: %s", prefix, resource, body)
			}
		}
	}
	// The mount's key is not the surface's credential: nothing here read the
	// refused file, so it can neither prove nor disprove that key.
	_, body := request(t, handler, http.MethodGet, testPrefix+StatusSuffix, bearer(testAdminKey), http.StatusUnauthorized)
	if !strings.Contains(body, `"`+CodeAdminUnauthorized+`"`) {
		t.Fatalf("the refused mount's key was not refused: %s", body)
	}
}

// TestMountIsInspectedOnlyAfterTheCredentialAndOriginGates is Low-1 at the route
// boundary. The credential's shape and the browser's origin are decided from the
// request alone, so a caller that presents no credential, and a cross-site caller
// whatever it presents, never makes this handler touch the operator's mount.
//
// A caller that does present a credential shape is different on purpose: the
// surface's one credential file is the mount's own database when the shared file
// policy accepts it, so the key cannot be tested without that answer. It gets one
// bounded, shared lookup (see resolveMount), never one lookup per request it
// sends before the timeout.
func TestMountIsInspectedOnlyAfterTheCredentialAndOriginGates(t *testing.T) {
	root := legacySourceDir(t)
	handler := newHandler(root, "")
	var lookups int32
	handler.lookupMount = func(root string) mountResolution {
		atomic.AddInt32(&lookups, 1)
		return mountResolution{root: root, database: filepath.Join(root, "usage.sqlite")}
	}

	for name, authorization := range map[string]string{
		"absent":       "",
		"other-scheme": "Basic b3BlcmF0b3I6cGFzcw==",
		"no-value":     "Bearer ",
		"bare-key":     testAdminKey,
	} {
		t.Run(name, func(t *testing.T) {
			for _, resource := range []string{PreflightSuffix, StatusSuffix} {
				request(t, handler, http.MethodGet, testPrefix+resource, authorization, http.StatusUnauthorized)
			}
		})
	}

	// A well-formed credential from another site is refused before the lookup.
	crossSite := httptest.NewRequest(http.MethodGet, testPrefix+StatusSuffix, nil)
	crossSite.Host = "manager.test"
	crossSite.Header.Set("Authorization", bearer(testAdminKey))
	crossSite.Header.Set("Origin", "https://attacker.test")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, crossSite)
	if recorder.Code != http.StatusForbidden || !strings.Contains(recorder.Body.String(), `"`+upgradesurface.CodeOriginRejected+`"`) {
		t.Fatalf("cross-site read = %d: %s", recorder.Code, recorder.Body.String())
	}
	if got := atomic.LoadInt32(&lookups); got != 0 {
		t.Fatalf("%d mount lookups for callers the surface never admitted", got)
	}

	// A well-formed key is one the surface has to test, and the file it tests is
	// the mount's database: that is one lookup, then the refusal.
	request(t, handler, http.MethodGet, testPrefix+StatusSuffix, bearer(foreignKey), http.StatusUnauthorized)
	if got := atomic.LoadInt32(&lookups); got != 1 {
		t.Fatalf("a refused key performed %d mount lookups, want 1", got)
	}

	// The admitted caller pays the same one lookup per request and gets its
	// answer from the same resolution.
	request(t, handler, http.MethodGet, testPrefix+PreflightSuffix, bearer(testAdminKey), http.StatusOK)
	if got := atomic.LoadInt32(&lookups); got != 2 {
		t.Fatalf("an admitted caller performed %d mount lookups, want 2", got)
	}
}

// TestMountLookupRunsOncePerRequestInsideItsBound is Low-3 at the route
// boundary. Authentication and the sourceRoot marker ask the same question, so
// a status poll asks it once; a mount that never answers must not hold the poll
// open past the bound; and however many polls arrive while it is hung, only one
// lookup runs.
func TestMountLookupRunsOncePerRequestInsideItsBound(t *testing.T) {
	const managerKey = "manager-store-key-for-a-slow-mount"
	database := managerCredentialStore(t, managerKey)
	root := t.TempDir()
	handler := &Handler{
		LegacyRoot: func() string { return root },
		Database:   func() string { return database },
		Timeout:    10 * time.Second,
	}
	var lookups int32
	handler.lookupMount = func(string) mountResolution {
		atomic.AddInt32(&lookups, 1)
		return mountResolution{root: root, code: CodeSourceSymlinked}
	}
	_, body := request(t, handler, http.MethodGet, testPrefix+StatusSuffix, bearer(managerKey), http.StatusOK)
	if got := atomic.LoadInt32(&lookups); got != 1 {
		t.Fatalf("status poll performed %d mount lookups, want 1", got)
	}
	var status statusDocument
	if err := json.Unmarshal([]byte(body), &status); err != nil {
		t.Fatal(err)
	}
	if status.SourceRoot != CodeSourceSymlinked {
		t.Fatalf("source root marker = %q, want the resolved lookup's own code", status.SourceRoot)
	}

	// A mount whose filesystem never answers. Every poll stops at the bound and
	// names the deadline instead of waiting on it, the credential still resolves
	// — to the Manager store — so the projection is answered, and the lookup
	// itself is started once for all of them.
	release := make(chan struct{})
	finished := make(chan struct{})
	var calls, active, peak int32
	handler.lookupMount = func(string) mountResolution {
		atomic.AddInt32(&calls, 1)
		running := atomic.AddInt32(&active, 1)
		for {
			seen := atomic.LoadInt32(&peak)
			if running <= seen || atomic.CompareAndSwapInt32(&peak, seen, running) {
				break
			}
		}
		<-release
		atomic.AddInt32(&active, -1)
		close(finished)
		return mountResolution{root: root}
	}
	handler.MountTimeout = 20 * time.Millisecond

	const polls = 20
	start := make(chan struct{})
	var wait sync.WaitGroup
	codes := make([]int, polls)
	bodies := make([]string, polls)
	for index := range polls {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			poll := httptest.NewRequest(http.MethodGet, testPrefix+StatusSuffix, nil)
			poll.Host = "manager.test"
			poll.Header.Set("Authorization", bearer(managerKey))
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, poll)
			codes[index] = recorder.Code
			bodies[index] = recorder.Body.String()
		}(index)
	}
	started := time.Now()
	close(start)
	wait.Wait()
	elapsed := time.Since(started)
	if elapsed > time.Second {
		t.Fatalf("%d status polls waited %s on a mount that never answered, bound %s", polls, elapsed, handler.MountTimeout)
	}
	for index := range codes {
		if codes[index] != http.StatusOK {
			t.Fatalf("poll %d on a hung mount = %d: %s", index, codes[index], bodies[index])
		}
		if !strings.Contains(bodies[index], `"`+CodeMountTimeout+`"`) {
			t.Fatalf("poll %d did not name the mount deadline: %s", index, bodies[index])
		}
		if strings.Contains(bodies[index], `"`+CodeSourceUnreadable+`"`) {
			t.Fatalf("poll %d reported the mount deadline as an unreadable source: %s", index, bodies[index])
		}
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("%d concurrent polls started %d mount lookups, want 1", polls, got)
	}
	if got := atomic.LoadInt32(&peak); got != 1 {
		t.Fatalf("%d mount lookups ran at once, want at most 1", got)
	}
	close(release)
	<-finished
}

// TestMountDeadlineIsATimeoutNotAnUnreadableSource is Low-4 at the route
// boundary: a mount that did not answer is its own condition, and its copy can
// say so. Reporting it as an unreadable database would send the operator to
// volume permissions for a mount whose only problem is that it was slow.
func TestMountDeadlineIsATimeoutNotAnUnreadableSource(t *testing.T) {
	root := t.TempDir()
	release := make(chan struct{})
	finished := make(chan struct{})
	handler := &Handler{
		LegacyRoot:   func() string { return root },
		Timeout:      10 * time.Second,
		MountTimeout: 20 * time.Millisecond,
	}
	handler.lookupMount = func(string) mountResolution {
		<-release
		close(finished)
		return mountResolution{root: root}
	}
	for _, resource := range []string{PreflightSuffix, StatusSuffix} {
		_, body := request(t, handler, http.MethodGet, testPrefix+resource, bearer(testAdminKey), http.StatusServiceUnavailable)
		if !strings.Contains(body, `"`+CodeMountTimeout+`"`) {
			t.Fatalf("%s did not name the mount deadline: %s", resource, body)
		}
		if strings.Contains(body, `"`+CodeSourceUnreadable+`"`) {
			t.Fatalf("%s reported the mount deadline as an unreadable source: %s", resource, body)
		}
	}
	close(release)
	<-finished
}

// TestMountBoundContainsTheConfiguredRootResolution is M4 at the one place the
// bound lives. The production resolver behind Handler.LegacyRoot stats the
// configured directory — `/legacy` when the operator set nothing — and a stat on
// a hung mount waits exactly as long as the database inspection behind it.
// Resolving the root before the lookup would leave that wait outside the only
// bound this surface has, so the whole resolution has to run inside the one
// bounded, one-in-flight lookup.
//
// The measurement is that bounded lookup and nothing else. A full request also
// pays the credential read and the projection; placing those under the mount's
// bound would fail this test for a cost it does not describe, and for a rate that
// belongs to the machine. The deadline verdict at the route boundary and the
// one-lookup rule are covered by TestMountDeadlineIsATimeoutNotAnUnreadableSource
// and TestMountLookupRunsOncePerRequestInsideItsBound.
func TestMountBoundContainsTheConfiguredRootResolution(t *testing.T) {
	root := t.TempDir()
	var resolverCalls, lookups, active, peak int32
	resolved := make(chan struct{})
	var slow sync.Once
	handler := &Handler{
		LegacyRoot: func() string {
			atomic.AddInt32(&resolverCalls, 1)
			running := atomic.AddInt32(&active, 1)
			for {
				seen := atomic.LoadInt32(&peak)
				if running <= seen || atomic.CompareAndSwapInt32(&peak, seen, running) {
					break
				}
			}
			defer atomic.AddInt32(&active, -1)
			// The first resolution is the hung filesystem call. Every later one
			// answers immediately, so the handler's recovery is observable.
			slow.Do(func() {
				time.Sleep(300 * time.Millisecond)
				close(resolved)
			})
			return ""
		},
		MountTimeout: 20 * time.Millisecond,
	}
	handler.lookupMount = func(string) mountResolution {
		atomic.AddInt32(&lookups, 1)
		return mountResolution{root: root, code: CodeLegacyRootMissing}
	}

	// Every waiter joins the one flight: a hung root costs exactly one resolution
	// however many lookups arrive, and each of them stops at the bound.
	const polls = 20
	start := make(chan struct{})
	var wait sync.WaitGroup
	answers := make([]mountResolution, polls)
	for index := range polls {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			answers[index] = handler.resolveMount(context.Background())
		}(index)
	}
	started := time.Now()
	close(start)
	wait.Wait()
	elapsed := time.Since(started)
	// Five times the bound separates a lookup that stops at the timer from one
	// that waits the hung resolution out (300 ms) without measuring scheduling.
	if budget := handler.MountTimeout * 5; elapsed > budget {
		t.Fatalf("%d lookups waited %s on a root resolution bounded to %s (allowed %s)",
			polls, elapsed, handler.MountTimeout, budget)
	}
	if got := atomic.LoadInt32(&resolverCalls); got != 1 {
		t.Fatalf("%d concurrent lookups resolved the configured root %d times, want 1", polls, got)
	}
	if got := atomic.LoadInt32(&lookups); got != 0 {
		t.Fatalf("%d root inspections ran before the hung resolution answered", got)
	}
	for index, answer := range answers {
		if answer.expired || answer.code != CodeMountTimeout {
			t.Fatalf("lookup %d on a hung root = %+v, want %q", index, answer, CodeMountTimeout)
		}
	}

	// The resolution lands after every waiter was answered. It must not replace a
	// deadline verdict, and it must not strand the handler: the next lookup reads
	// the landed answer (joining that flight or starting the next one).
	<-resolved
	if landed := handler.resolveMount(context.Background()); landed.code != CodeLegacyRootMissing {
		t.Fatalf("the lookup after the late resolution = %+v", landed)
	}
	if got := atomic.LoadInt32(&peak); got != 1 {
		t.Fatalf("%d root resolutions ran at once, want at most 1", got)
	}
}

// managerCredentialStore writes the Manager's own store with an administrator
// credential and one recorded operation row: everything the surface reads when
// it is not reading a 1.x mount.
func managerCredentialStore(t *testing.T, key string) string {
	t.Helper()
	return managerStoreWithOperation(t, key, model.UpgradeOperation{
		Stage:       model.UpgradeStageCopyIn,
		DomainsDone: []string{model.UpgradeDomainSourceCopy},
		ManualItems: []string{},
	})
}

// managerStoreWithOperation is managerCredentialStore for a caller that needs a
// specific recorded stage.
func managerStoreWithOperation(t *testing.T, key string, operation model.UpgradeOperation) string {
	t.Helper()
	database := filepath.Join(t.TempDir(), "usage.sqlite")
	db, err := sqliterepo.Open(database)
	if err != nil {
		t.Fatal(err)
	}
	credential, err := security.NewAdminCredential(key, "test")
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := setting.New(db).SaveAdminCredential(context.Background(), credential); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := upgradeoperation.New(db).Put(context.Background(), operation); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return database
}

// TestRefusedMountFallsBackToTheManagerStore is the reachability contract for a
// mount whose usage.sqlite the shared file policy refuses. The mount holds no
// database the surface may read, so the one credential resolves to the Manager
// store: the entry page and both reads stay usable, the projection names the
// mount's own condition, and the preflight still reports on the mount instead of
// on the file the credential fell back to.
func TestRefusedMountFallsBackToTheManagerStore(t *testing.T) {
	const managerKey = "manager-store-administrator-key"
	root := legacySourceDir(t)
	database := filepath.Join(root, "usage.sqlite")
	if err := os.Rename(database, database+".real"); err != nil {
		t.Fatalf("move fixture database: %v", err)
	}
	if err := os.Symlink(database+".real", database); err != nil {
		t.Skipf("create database symlink: %v", err)
	}
	manager := managerCredentialStore(t, managerKey)
	handler := &Handler{
		LegacyRoot: func() string { return root },
		Database:   func() string { return manager },
		Timeout:    10 * time.Second,
	}

	// The Manager store is the surface's credential, so both reads answer with
	// its key while the mount is refused.
	_, body := request(t, handler, http.MethodGet, testPrefix+StatusSuffix, bearer(managerKey), http.StatusOK)
	var status statusDocument
	if err := json.Unmarshal([]byte(body), &status); err != nil {
		t.Fatal(err)
	}
	if !status.Recorded || status.Stage != StageCopyIn {
		t.Fatalf("status with a refused mount = %+v", status)
	}
	if status.SourceRoot != CodeSourceSymlinked {
		t.Fatalf("source root marker = %q, want %q", status.SourceRoot, CodeSourceSymlinked)
	}
	// The mount's own key is not the surface's credential any more: the surface
	// reads the file it resolved, not the file the mount points at.
	_, body = request(t, handler, http.MethodGet, testPrefix+StatusSuffix, bearer(testAdminKey), http.StatusUnauthorized)
	if !strings.Contains(body, `"`+CodeAdminUnauthorized+`"`) {
		t.Fatalf("the refused mount's key was not refused: %s", body)
	}

	// The preflight inspects the mount, so it names the condition the projection
	// named rather than reporting on the Manager store.
	_, body = request(t, handler, http.MethodGet, testPrefix+PreflightSuffix, bearer(managerKey), http.StatusOK)
	if !strings.Contains(body, `"`+CodeSourceSymlinked+`"`) {
		t.Fatalf("preflight did not name the refused mount: %s", body)
	}
	for _, secret := range []string{root, manager} {
		if strings.Contains(body, secret) {
			t.Fatalf("preflight leaked %q: %s", secret, body)
		}
	}
}

// TestUnauthenticatedCallersLearnNothingAboutMethodOrPath is the ordering
// contract both Upgrade surfaces share: the credential is answered before the
// verb and before the path, so a caller without one cannot tell a real route
// from an unknown one, or learn which method a route takes. Only a proved
// credential reveals 405 and 404.
func TestUnauthenticatedCallersLearnNothingAboutMethodOrPath(t *testing.T) {
	handler := newHandler(legacySourceDir(t), "")
	targets := []string{
		testPrefix + PreflightSuffix,
		testPrefix + StatusSuffix,
		testPrefix + "/installation/upgrade/unknown",
		testPrefix + PreflightSuffix + "?legacy=/etc",
	}
	for _, target := range targets {
		for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodOptions} {
			_, body := request(t, handler, method, target, "", http.StatusUnauthorized)
			if !strings.Contains(body, `"`+CodeAdminUnauthorized+`"`) {
				t.Fatalf("%s %s = %s", method, target, body)
			}
		}
	}
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodOptions} {
		request(t, handler, method, testPrefix+PreflightSuffix, bearer(testAdminKey), http.StatusMethodNotAllowed)
		request(t, handler, method, testPrefix+StatusSuffix, bearer(testAdminKey), http.StatusMethodNotAllowed)
		request(t, handler, method, testPrefix+"/installation/upgrade/unknown", bearer(testAdminKey), http.StatusNotFound)
	}
}

func recordOperation(t *testing.T, database string, operation model.UpgradeOperation) {
	t.Helper()
	db, err := sqliterepo.Open(database)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if operation.SourceKind == "" {
		operation.SourceKind = "upgraded-legacy"
	}
	if operation.SourceVersion == "" {
		operation.SourceVersion = "1.14.4"
	}
	operation.SourceFingerprintBefore = "fingerprint-before-secret-value"
	operation.SourceFingerprintAfter = "fingerprint-after-secret-value"
	operation.BackupManifestSHA256 = "backup-manifest-secret-value"
	if err := upgradeoperation.New(db).Put(context.Background(), operation); err != nil {
		t.Fatal(err)
	}
}

// TestStatusProjectionOmitsSecretsAndToleratesUnknownStages is the read
// projection contract: stages map onto the wizard's next action, a stage this
// build does not know is still described, and no fingerprint, digest, path or
// credential reaches the response.
func TestStatusProjectionOmitsSecretsAndToleratesUnknownStages(t *testing.T) {
	root := legacySourceDir(t)
	database := filepath.Join(t.TempDir(), "usage.sqlite")
	recordOperation(t, database, model.UpgradeOperation{
		Stage:          model.UpgradeStageReconcile,
		DomainsDone:    []string{model.UpgradeDomainSourceCopy, model.UpgradeDomainUsageEvents},
		Reconciliation: json.RawMessage(`{"mismatchDays":["2026-10-02","2026-10-01","2026-10-01","not-a-day"],"raw":{"secret":"x"}}`),
		ManualItems:    []string{"reimport_cpa_keys"},
		ErrorCode:      "upgrade_reconcile_blocked",
		ErrorDetail:    "retained projection missing for 2026-10-01",
	})
	handler := newHandler(root, database)

	_, body := request(t, handler, http.MethodGet, canonical+StatusSuffix, bearer(testAdminKey), http.StatusOK)
	for _, secret := range []string{
		"fingerprint-before-secret-value", "fingerprint-after-secret-value",
		"backup-manifest-secret-value", database, `"raw"`, "not-a-day",
	} {
		if strings.Contains(body, secret) {
			t.Fatalf("status leaked %q: %s", secret, body)
		}
	}
	var status statusDocument
	if err := json.Unmarshal([]byte(body), &status); err != nil {
		t.Fatal(err)
	}
	if !status.Recorded || !status.StageKnown || status.Stage != string(model.UpgradeStageReconcile) {
		t.Fatalf("status = %+v", status)
	}
	if status.NextAction != ActionReconcile {
		t.Fatalf("next action = %q", status.NextAction)
	}
	if len(status.DomainsDone) != 2 || status.DomainsDone[0] != model.UpgradeDomainSourceCopy {
		t.Fatalf("domains = %v", status.DomainsDone)
	}
	if got := status.Reconciliation.MismatchDays; len(got) != 2 || got[0] != "2026-10-01" || got[1] != "2026-10-02" {
		t.Fatalf("reconciliation = %+v", status.Reconciliation)
	}
	if status.Reconciliation.MismatchDaysTotal != 2 || status.Reconciliation.MismatchDaysTruncated {
		t.Fatalf("reconciliation bounds = %+v", status.Reconciliation)
	}
	if status.SourceKind != "upgraded-legacy" || status.SourceVersion != "1.14.4" {
		t.Fatalf("source = %+v", status)
	}
	if len(status.ManualItems) != 1 || status.ManualItems[0] != "reimport_cpa_keys" || status.ErrorCode != "upgrade_reconcile_blocked" {
		t.Fatalf("operation detail = %+v", status)
	}
}

// TestStatusScrubsRecordedTextByContent proves the scrubber reads the text, not
// its shape: a credential carries no path, so a path-shaped test cannot see it,
// and a multi-line detail is evidence that must survive line by line.
func TestStatusScrubsRecordedTextByContent(t *testing.T) {
	database := filepath.Join(t.TempDir(), "usage.sqlite")
	const (
		bearerSecret = "eyJhbGciOiJIUzI1NiJ9.bGVnYWN5.c2lnbmF0dXJl"
		issuedSecret = "sk-live-424242424242"
		apiKeySecret = "cpaKeyFromHeader"
		urlSecret    = "hunter2password"
	)
	recordOperation(t, database, model.UpgradeOperation{
		Stage:       model.UpgradeStageReconcile,
		DomainsDone: []string{model.UpgradeDomainSourceCopy},
		ManualItems: []string{
			"reimport_cpa_keys",
			"call the gateway with Bearer " + bearerSecret,
			"export X_API_KEY=" + apiKeySecret,
		},
		ErrorCode: "upgrade_reconcile_blocked",
		ErrorDetail: "first line keeps its words\n" +
			"upstream answered 401 for Authorization: Bearer " + bearerSecret + "\n" +
			"retry with sk-live-424242424242 instead\n" +
			"rejected x-api-key: " + apiKeySecret + "\n" +
			"endpoint https://operator:" + urlSecret + "@cpa.internal/v0/management answered\n" +
			"leftover /srv/cpamp/data/usage.sqlite is unreadable",
	})
	handler := newHandler(legacySourceDir(t), database)
	_, body := request(t, handler, http.MethodGet, canonical+StatusSuffix, bearer(testAdminKey), http.StatusOK)

	for _, secret := range []string{bearerSecret, issuedSecret, apiKeySecret, urlSecret, "/srv/cpamp"} {
		if strings.Contains(body, secret) {
			t.Fatalf("status leaked %q: %s", secret, body)
		}
	}
	var status statusDocument
	if err := json.Unmarshal([]byte(body), &status); err != nil {
		t.Fatal(err)
	}
	// The detail is scrubbed line by line instead of being dropped whole.
	for _, fragment := range []string{"first line keeps its words", "upstream answered 401", "retry with", "rejected x-api-key", "endpoint https://", "@cpa.internal/v0/management answered", "leftover"} {
		if !strings.Contains(status.ErrorDetail, fragment) {
			t.Fatalf("detail lost %q: %q", fragment, status.ErrorDetail)
		}
	}
	if lines := strings.Split(status.ErrorDetail, "\n"); len(lines) != 6 {
		t.Fatalf("detail lines = %d, want 6: %q", len(lines), status.ErrorDetail)
	}
	if !strings.Contains(status.ErrorDetail, redactedText) || !strings.Contains(status.ErrorDetail, sourceText) {
		t.Fatalf("detail carries no redaction marker: %q", status.ErrorDetail)
	}
	// The same scrubber applies to the operation's manual items.
	if len(status.ManualItems) != 3 || status.ManualItems[0] != "reimport_cpa_keys" {
		t.Fatalf("manual items = %v", status.ManualItems)
	}
	for _, item := range status.ManualItems {
		for _, secret := range []string{bearerSecret, apiKeySecret} {
			if strings.Contains(item, secret) {
				t.Fatalf("manual item leaked %q: %q", secret, item)
			}
		}
	}
}

// TestStatusStageToNextAction is the stage table the wizard renders against.
func TestStatusStageToNextAction(t *testing.T) {
	cases := []struct {
		stage      string
		action     string
		stageKnown bool
	}{
		{stage: "", action: ActionPreflight, stageKnown: true},
		{stage: StagePreflight, action: ActionPreflight, stageKnown: true},
		{stage: StageCopyIn, action: ActionCopyIn, stageKnown: true},
		{stage: StageReconcile, action: ActionReconcile, stageKnown: true},
		{stage: StageCPAImport, action: ActionReconcile, stageKnown: true},
		{stage: StageExternal, action: ActionReconcile, stageKnown: true},
		{stage: StageVerify, action: ActionReconcile, stageKnown: true},
		{stage: StageFailed, action: ActionReconcile, stageKnown: true},
		{stage: StageComplete, action: ActionComplete, stageKnown: true},
		{stage: StageRolledBack, action: ActionRolledBack, stageKnown: true},
		{stage: "quantum", action: "", stageKnown: false},
	}
	for _, testCase := range cases {
		if got := nextAction(testCase.stage); got != testCase.action {
			t.Fatalf("nextAction(%q) = %q, want %q", testCase.stage, got, testCase.action)
		}
		if got := knownStage(testCase.stage); got != testCase.stageKnown {
			t.Fatalf("knownStage(%q) = %t, want %t", testCase.stage, got, testCase.stageKnown)
		}
	}
}

// TestStatusUnrecordedAndUnreadable proves the pre-operation state is a normal
// empty projection, and that an unreadable store is a typed code.
func TestStatusUnrecordedAndUnreadable(t *testing.T) {
	root := legacySourceDir(t)
	database := filepath.Join(t.TempDir(), "usage.sqlite")
	db, err := sqliterepo.Open(database)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	_, body := request(t, newHandler(root, database), http.MethodGet, testPrefix+StatusSuffix, bearer(testAdminKey), http.StatusOK)
	var status statusDocument
	if err := json.Unmarshal([]byte(body), &status); err != nil {
		t.Fatal(err)
	}
	if status.Recorded || status.Stage != "" || status.NextAction != ActionPreflight {
		t.Fatalf("unrecorded projection = %+v", status)
	}
	if status.DomainsDone == nil || status.ManualItems == nil {
		t.Fatalf("unrecorded projection omitted a list: %s", body)
	}
	request(t, newHandler(root, ""), http.MethodGet, testPrefix+StatusSuffix, bearer(testAdminKey), http.StatusServiceUnavailable)
	broken := filepath.Join(t.TempDir(), "usage.sqlite")
	if err := os.WriteFile(broken, []byte("not sqlite at all"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, body = request(t, newHandler(root, broken), http.MethodGet, testPrefix+StatusSuffix, bearer(testAdminKey), http.StatusServiceUnavailable)
	if !strings.Contains(body, `"`+CodeOperationUnread+`"`) {
		t.Fatalf("unreadable store code missing: %s", body)
	}
}

const (
	// largeSourceMegabytes is the size a real 1.x source reaches, where the
	// credential read's private copy and digests dominate a request. The fixture
	// is real bytes, not a sparse file, and it sits past the size this read used
	// to hide behind a small fixture: the first read has to fit a budget scaled
	// to the source, and a poll after it has to be answered without a second
	// copy.
	largeSourceMegabytes = 512
)

// largeSourceDir grows the smallest 1.x source into a real-sized database: the
// credential read must prove the credential on it, not just on a toy file.
func largeSourceDir(t *testing.T, megabytes int) string {
	t.Helper()
	dir, _ := growLargeSource(t, megabytes)
	return dir
}

// largeCorruptSourceDir grows the smallest 1.x source into a real-sized database
// and corrupts the root page of its padding tree. The credential row still
// reads; a whole-file integrity check cannot pass.
func largeCorruptSourceDir(t *testing.T, megabytes int) string {
	t.Helper()
	dir, paddingRoot := growLargeSource(t, megabytes)
	corruptDatabasePage(t, filepath.Join(dir, "usage.sqlite"), paddingRoot)
	return dir
}

// growLargeSource returns the fixture root and the root page of the padding tree
// that the corruption cases damage. The padding is inserted in one transaction:
// the fixture is about size, not about commit cost.
func growLargeSource(t *testing.T, megabytes int) (string, int) {
	t.Helper()
	dir := legacySourceDir(t)
	db, err := sql.Open("sqlite", filepath.Join(dir, "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`create table padding (id integer primary key, payload blob)`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	for range megabytes {
		if _, err := tx.Exec(`insert into padding (payload) values (zeroblob(1048576))`); err != nil {
			_ = tx.Rollback()
			_ = db.Close()
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	var paddingRoot int
	if err := db.QueryRow(`select rootpage from sqlite_master where name = 'padding'`).Scan(&paddingRoot); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return dir, paddingRoot
}

// corruptDatabasePage overwrites one b-tree page with bytes no page type
// accepts. The damage is confined to the tree that page belongs to: the
// integrity check reports it, every other tree still reads.
func corruptDatabasePage(t *testing.T, path string, page int) {
	t.Helper()
	file, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture database: %v", err)
	}
	pageSize := int(binary.BigEndian.Uint16(file[16:18]))
	if pageSize == 1 {
		pageSize = 65536
	}
	offset := (page - 1) * pageSize
	if page < 2 || offset+pageSize > len(file) {
		t.Fatalf("page %d does not fit a %d-byte database", page, len(file))
	}
	for index := offset; index < offset+pageSize; index++ {
		file[index] = 0xff
	}
	if err := os.WriteFile(path, file, 0o600); err != nil {
		t.Fatalf("corrupt fixture database: %v", err)
	}
}

// TestStatusPollAuthenticatesWithoutVerifyingTheWholeSource is the R1 contract at
// the route boundary. Every request proves the credential, so the read behind it
// must not scan the source: the fixture's padding tree is corrupt, which a
// whole-file check reports and a credential row read never touches. The poll
// still answers with the operation projection, and the preflight on the same
// source still runs the check and reports it. The credential read takes the
// source's private snapshot, so the poll is not timed: the corruption is what
// distinguishes the row read from an integrity scan.
func TestStatusPollAuthenticatesWithoutVerifyingTheWholeSource(t *testing.T) {
	root := largeCorruptSourceDir(t, largeSourceMegabytes)
	database := filepath.Join(t.TempDir(), "usage.sqlite")
	db, err := sqliterepo.Open(database)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	handler := newHandler(root, database)

	// The credential read takes the private snapshot every source read requires,
	// so a poll's cost is proportional to the copy and a wall-clock budget would
	// measure the machine, not the query. The corrupted padding tree is the
	// proof instead: a whole-file integrity check cannot pass on this fixture,
	// and the poll still answers with the operation projection — so the read
	// touched the credential row and never scanned the file.
	_, body := request(t, handler, http.MethodGet, testPrefix+StatusSuffix, bearer(testAdminKey), http.StatusOK)
	var status statusDocument
	if err := json.Unmarshal([]byte(body), &status); err != nil {
		t.Fatalf("decode status: %v: %s", err, body)
	}
	if status.Recorded || status.NextAction != ActionPreflight {
		t.Fatalf("status projection = %+v", status)
	}

	// The qualification path runs the whole-file check on the same source.
	_, body = request(t, handler, http.MethodGet, testPrefix+PreflightSuffix, bearer(testAdminKey), http.StatusOK)
	if !strings.Contains(body, `"`+CodeSourceUnreadable+`"`) {
		t.Fatalf("preflight did not report the failed integrity check: %s", body)
	}
}

// TestStatusFirstReadOfALargeSourceFitsTheScaledBudgetAndPollsAreCached is D3 at
// the route boundary. A healthy 512 MiB source is a real 1.x size, and proving a
// correct credential on it cannot fit a fixed 2 s deadline: the read is a private
// copy plus its digests. The first request must therefore fit a budget scaled to
// the source while staying under its cap, and every poll after it must be
// answered from the remembered verdict instead of copying the file again.
func TestStatusFirstReadOfALargeSourceFitsTheScaledBudgetAndPollsAreCached(t *testing.T) {
	root := largeSourceDir(t, largeSourceMegabytes)
	info, err := os.Stat(filepath.Join(root, "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() < 512<<20 {
		t.Fatalf("the fixture is %d bytes, want a real source of at least 512 MiB", info.Size())
	}
	database := managerStoreWithOperation(t, testAdminKey, model.UpgradeOperation{Stage: model.UpgradeStageCopyIn})
	handler := newHandler(root, database)

	started := time.Now()
	_, body := request(t, handler, http.MethodGet, testPrefix+StatusSuffix, bearer(testAdminKey), http.StatusOK)
	first := time.Since(started)
	var status statusDocument
	if err := json.Unmarshal([]byte(body), &status); err != nil {
		t.Fatal(err)
	}
	if !status.Recorded || status.Stage != StageCopyIn {
		t.Fatalf("status on a %d-byte source = %s", info.Size(), body)
	}
	t.Logf("first authenticated read of a %d-byte source: %s", info.Size(), first)
	if first >= upgradeservice.CredentialVerifyMaxBudget {
		t.Fatalf("the first read of a %d-byte source took %s, at or beyond the capped budget %s",
			info.Size(), first, upgradeservice.CredentialVerifyMaxBudget)
	}

	// A poll reuses the verdict for the source state it described: the only work
	// left is describing the source again. The comparison is relative — four
	// times faster than the read that had to copy the file — because the absolute
	// cost of a stat belongs to the machine, not to this contract.
	for index := range 3 {
		pollStarted := time.Now()
		_, body = request(t, handler, http.MethodGet, testPrefix+StatusSuffix, bearer(testAdminKey), http.StatusOK)
		poll := time.Since(pollStarted)
		t.Logf("cached poll %d on the %d-byte source: %s", index, info.Size(), poll)
		if poll*4 >= first {
			t.Fatalf("poll %d after the first read took %s against a %s first read; the verified source was not reused (%s)",
				index, poll, first, body)
		}
	}
}

// TestStatusKeepsTheTypedErrorForARowThatContradictsItself guards the projection
// refactor: a recorded row that runs backwards, or carries a list column of the
// wrong shape, is still a typed 503 upgrade_operation_invalid — never a stage the
// operator can act on and never a 500.
func TestStatusKeepsTheTypedErrorForARowThatContradictsItself(t *testing.T) {
	root := legacySourceDir(t)
	for _, test := range []struct {
		name   string
		update string
	}{
		{"reversed timestamps", `created_at_ms = 2000, updated_at_ms = 1000`},
		{"manual items not a string array", `created_at_ms = 1000, updated_at_ms = 2000, manual_items = '[1]'`},
	} {
		t.Run(test.name, func(t *testing.T) {
			database := managerStoreWithOperation(t, testAdminKey, model.UpgradeOperation{Stage: model.UpgradeStageCopyIn})
			db, err := sql.Open("sqlite", database)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`update upgrade_operations set ` + test.update + ` where id = 1`); err != nil {
				_ = db.Close()
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			_, body := request(t, newHandler(root, database), http.MethodGet, testPrefix+StatusSuffix, bearer(testAdminKey), http.StatusServiceUnavailable)
			if !strings.Contains(body, `"`+CodeOperationInvalid+`"`) {
				t.Fatalf("a contradictory row = %s", body)
			}
		})
	}
}

// TestStatusRolledBackIsAnObservation proves a stage written by a build that
// knows rollback is readable here even though this build's model validator
// would reject it.
func TestStatusRolledBackIsAnObservation(t *testing.T) {
	root := legacySourceDir(t)
	database := filepath.Join(t.TempDir(), "usage.sqlite")
	db, err := sqliterepo.Open(database)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`insert into upgrade_operations (id, stage, created_at_ms, updated_at_ms) values (1, 'rolled_back', 1, 2)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	_, body := request(t, newHandler(root, database), http.MethodGet, canonical+StatusSuffix, bearer(testAdminKey), http.StatusOK)
	var status statusDocument
	if err := json.Unmarshal([]byte(body), &status); err != nil {
		t.Fatal(err)
	}
	if !status.Recorded || status.Stage != StageRolledBack || !status.StageKnown || status.NextAction != ActionRolledBack {
		t.Fatalf("rolled_back projection = %+v", status)
	}
}

// TestScrubbersKeepHostPathsOut proves the text scrubbing rules used on
// operator-visible strings.
func TestScrubbersKeepHostPathsOut(t *testing.T) {
	scrub := newScrubber("/srv/cpamp/legacy")
	if got := scrub.detail("source /srv/cpamp/legacy/usage.sqlite is busy"); got != "source <source>/usage.sqlite is busy" {
		t.Fatalf("detail = %q", got)
	}
	if got := scrubError("open /srv/cpamp/data-key: permission denied"); strings.Contains(got, "/srv") {
		t.Fatalf("error detail leaked a path: %q", got)
	}
	if got := scrubError("retained projection missing for 2026-10-01"); got != "retained projection missing for 2026-10-01" {
		t.Fatalf("error detail = %q", got)
	}
	// A credential is redacted by its own content, with no path in sight.
	const (
		productKey   = "cpamp_sk_0123456789abcdef0123456789abcdef"
		adminKey     = "cmp_admin_rotation_0123456789abcdef"
		basicSecret  = "b3BlcmF0b3I6aHVudGVyMnBhc3N3b3JkAA=="
		passwordText = "hunter2password"
	)
	for _, testCase := range []struct{ in, secret string }{
		{in: "request failed with Authorization: Bearer abc.def.ghi", secret: "abc.def.ghi"},
		{in: "upstream key sk-live-0123456789 expired", secret: "sk-live-0123456789"},
		{in: "header x-api-key: cpaManagedSecret rejected", secret: "cpaManagedSecret"},
		{in: "header X-Api-Key=cpaManagedSecret rejected", secret: "cpaManagedSecret"},
		{in: "url https://admin:pw-12345@gateway.internal/x refused", secret: "pw-12345"},
		{in: "usage key " + productKey + " rejected", secret: productKey},
		{in: "administrator key " + adminKey + " rotated", secret: adminKey},
		{in: "usage key Cpamp_SK_0123456789 rejected", secret: "Cpamp_SK_0123456789"},
		{in: "administrator key CMP_ADMIN_Rotation_0123456789 rotated", secret: "CMP_ADMIN_Rotation_0123456789"},
		{in: "Authorization: Basic " + basicSecret + " refused", secret: basicSecret},
		{in: "gateway login password=" + passwordText + " refused", secret: passwordText},
		{in: "dsn postgres://gateway.internal/x?password=" + passwordText, secret: passwordText},
		{in: "env CPA_MANAGER_PASSWORD = " + passwordText, secret: passwordText},
	} {
		got := scrubError(testCase.in)
		if strings.Contains(got, testCase.secret) {
			t.Fatalf("%q leaked %q", testCase.in, testCase.secret)
		}
		if !strings.Contains(got, redactedText) {
			t.Fatalf("%q has no redaction marker: %q", testCase.in, got)
		}
		if strings.Contains(got, "\n") {
			t.Fatalf("%q gained a line break: %q", testCase.in, got)
		}
	}
	// A remote URL is not a host path: only its credentials are redacted.
	if got := scrubError("gateway https://gateway.internal/v0/management refused"); got != "gateway https://gateway.internal/v0/management refused" {
		t.Fatalf("remote URL was rewritten: %q", got)
	}
	// Multi-line detail survives line by line.
	if got := scrubError("first\nsecond Bearer leaked-token\nthird"); got != "first\nsecond Bearer "+redactedText+"\nthird" {
		t.Fatalf("multi-line detail = %q", got)
	}
	// A credential wrapped across a line break is still one credential: the
	// scheme word alone would leave the value on the next line unlabelled.
	const splitSecret = "eyJhbGciOiJIUzI1NiJ9.c3BsaXQ.c2lnbmF0dXJl"
	if got := scrubError("upstream refused\nAuthorization: Bearer\n" + splitSecret + "\nretry later"); strings.Contains(got, splitSecret) {
		t.Fatalf("split bearer survived: %q", got)
	} else if !strings.Contains(got, "Authorization: ") || !strings.Contains(got, redactedText) {
		// The shared content scrubber redacts the whole header value, scheme word
		// included; the wrap is still resolved, which is what this asserts.
		t.Fatalf("split bearer is not joined and redacted: %q", got)
	} else if got := strings.Count(got, "\n"); got != 2 {
		t.Fatalf("split bearer changed the detail shape: %q", got)
	}
	// A product key broken mid-token is one credential too: the head is redacted
	// by the key pattern, and the continuation carries no prefix of its own, so it
	// is removed where it sits. The line break itself stays.
	const (
		wrappedHead = "cpamp_sk_0123456789abc"
		wrappedTail = "def0123456789"
	)
	if got := scrubError("usage key " + wrappedHead + "\n" + wrappedTail + " rejected"); strings.Contains(got, wrappedHead) || strings.Contains(got, wrappedTail) {
		t.Fatalf("wrapped product key survived: %q", got)
	} else if want := "usage key " + redactedText + "\n" + redactedText + " rejected"; got != want {
		t.Fatalf("wrapped product key = %q, want %q", got, want)
	}
	// The same rule applies to a case-variant prefix, which is why the prefix
	// match is case-insensitive.
	if got := scrubError("administrator key CMP_ADMIN_rotation\n0123456789 rejected"); strings.Contains(got, "0123456789") {
		t.Fatalf("wrapped case-variant key tail survived: %q", got)
	}
	// A NUL byte is not text and never reaches the operator.
	if got := scrubError("broken\x00value"); strings.Contains(got, "\x00") {
		t.Fatalf("NUL survived: %q", got)
	}
	if got := abbreviate("0123456789abcdef"); got != "0123456789ab…" {
		t.Fatalf("abbreviate = %q", got)
	}
}

// TestStatusAnswersFromTheManagerStoreWithoutTheSourceMount is L1 at the route
// boundary. The recorded stage lives in the Manager store, so an operator whose
// source mount is gone can still read it; the projection names the missing
// source as a typed marker instead of refusing to answer, and the preflight
// keeps its typed 503.
func TestStatusAnswersFromTheManagerStoreWithoutTheSourceMount(t *testing.T) {
	// No mount at all: the Manager store is the surface's only credential
	// source and the only place the operation row can live.
	database := managerStoreWithOperation(t, testAdminKey, model.UpgradeOperation{
		Stage:       model.UpgradeStageReconcile,
		DomainsDone: []string{model.UpgradeDomainSourceCopy},
	})
	handler := &Handler{
		LegacyRoot: func() string { return "" },
		Database:   func() string { return database },
		Timeout:    10 * time.Second,
	}
	_, body := request(t, handler, http.MethodGet, testPrefix+StatusSuffix, bearer(testAdminKey), http.StatusOK)
	var status statusDocument
	if err := json.Unmarshal([]byte(body), &status); err != nil {
		t.Fatal(err)
	}
	if !status.Recorded || status.Stage != StageReconcile || status.NextAction != ActionReconcile {
		t.Fatalf("status without a source mount = %+v", status)
	}
	if status.SourceRoot != CodeLegacyRootMissing {
		t.Fatalf("source root marker = %q, want %q", status.SourceRoot, CodeLegacyRootMissing)
	}
	for _, secret := range []string{database, testAdminKey} {
		if strings.Contains(body, secret) {
			t.Fatalf("status leaked %q: %s", secret, body)
		}
	}
	// The preflight is the step that needs the source, so it keeps reporting the
	// mount the operator has to supply.
	_, body = request(t, handler, http.MethodGet, testPrefix+PreflightSuffix, bearer(testAdminKey), http.StatusServiceUnavailable)
	if !strings.Contains(body, `"`+CodeLegacyRootMissing+`"`) {
		t.Fatalf("preflight without a mount = %s", body)
	}
	// A mounted root that holds no database is the source's own condition, not
	// the mount's.
	handler.LegacyRoot = func() string { return t.TempDir() }
	_, body = request(t, handler, http.MethodGet, testPrefix+StatusSuffix, bearer(testAdminKey), http.StatusOK)
	if err := json.Unmarshal([]byte(body), &status); err != nil {
		t.Fatal(err)
	}
	if status.SourceRoot != CodeSourceMissing {
		t.Fatalf("source root marker = %q, want %q", status.SourceRoot, CodeSourceMissing)
	}
}

// TestStatusReportsTheCopyInMarkerWindow is L2. The copy-in publishes its
// provenance before the operation row commits, so a marker with no row is an
// unfinished operation; status must read it the way admission does and name the
// next action the operator actually has, not the step before any work started.
func TestStatusReportsTheCopyInMarkerWindow(t *testing.T) {
	root := legacySourceDir(t)
	database := filepath.Join(t.TempDir(), "usage.sqlite")
	db, err := sqliterepo.Open(database)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := copyinmarker.Write(filepath.Dir(database), copyinmarker.Marker{
		Version:                 copyinmarker.Version,
		Phase:                   copyinmarker.PhaseMaterializing,
		TargetDatabase:          database,
		SourceKind:              "upgraded-legacy",
		SourceVersion:           "1.14.4",
		SourceFingerprintBefore: "fingerprint-before",
		SourceFingerprintAfter:  "fingerprint-after",
		BackupManifestSHA256:    "manifest",
		WrittenAtMS:             1717171717000,
	}); err != nil {
		t.Fatal(err)
	}
	_, body := request(t, newHandler(root, database), http.MethodGet, testPrefix+StatusSuffix, bearer(testAdminKey), http.StatusOK)
	var status statusDocument
	if err := json.Unmarshal([]byte(body), &status); err != nil {
		t.Fatal(err)
	}
	if !status.Recorded || status.Evidence != EvidenceCopyInMarker {
		t.Fatalf("marker window was reported as no operation: %+v", status)
	}
	if status.Stage != StageCopyIn || !status.StageKnown || status.NextAction != ActionCopyIn {
		t.Fatalf("marker window stage = %+v", status)
	}
	if len(status.DomainsDone) != 1 || status.DomainsDone[0] != domainSourceCopy {
		t.Fatalf("marker window domains = %v", status.DomainsDone)
	}
	if status.SourceKind != "upgraded-legacy" || status.SourceVersion != "1.14.4" || status.UpdatedAtMS != 1717171717000 {
		t.Fatalf("marker window source = %+v", status)
	}
	// The next action is the one admission agrees with: the operation is in
	// progress, so it can never read as the step before any work started.
	stage, err := legacypreflight.DatabaseUpgradeAdmission{DatabasePath: database}.Admitted(context.Background())
	if err != nil || stage != legacypreflight.UpgradeStageInProgress {
		t.Fatalf("admission = %q err=%v", stage, err)
	}
	// A marker that names another database file describes nothing here. The
	// name is what identifies the store inside its data root (see SameTarget), so
	// a marker whose file name differs is the only foreign evidence: the same
	// name at another absolute path is a relocated root.
	foreign := filepath.Join(t.TempDir(), "other.sqlite")
	if err := copyinmarker.Write(filepath.Dir(database), copyinmarker.Marker{
		Version:                 copyinmarker.Version,
		Phase:                   copyinmarker.PhaseMaterializing,
		TargetDatabase:          foreign,
		SourceKind:              "upgraded-legacy",
		SourceFingerprintBefore: "fingerprint-before",
		SourceFingerprintAfter:  "fingerprint-after",
		BackupManifestSHA256:    "manifest",
	}); err != nil {
		t.Fatal(err)
	}
	_, body = request(t, newHandler(root, database), http.MethodGet, testPrefix+StatusSuffix, bearer(testAdminKey), http.StatusOK)
	if err := json.Unmarshal([]byte(body), &status); err != nil {
		t.Fatal(err)
	}
	if status.Recorded || status.Stage != "" || status.NextAction != ActionPreflight || status.Evidence != EvidenceNone {
		t.Fatalf("a foreign marker was read as this operation: %+v", status)
	}
}

// TestStatusDecidesFromTheMarkerBeforeOpeningAMaterializingTarget is the D2
// regression. The copy-in publishes a `materializing` marker before the first
// member is moved and before the migration that creates the operation table, so
// a target holding restored members cannot have a committed row. Reading the
// operation table first — as status used to — joins the restored live WAL and
// rewrites `-shm`, the member the resumed move compares with the backup, which
// makes the accepted admission's marker-first protection pointless.
func TestStatusDecidesFromTheMarkerBeforeOpeningAMaterializingTarget(t *testing.T) {
	source := legacySourceDir(t)
	writer, err := sql.Open("sqlite", filepath.Join(source, "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = writer.Close() }()
	// Committed rows stay in the WAL: the writer is kept open and auto-checkpoint
	// is off, so the copied target holds a live WAL and a real WAL index.
	if _, err := writer.Exec(`pragma journal_mode = wal; pragma wal_autocheckpoint = 0;
		create table wal_payload (value text);
		insert into wal_payload values ('committed-in-wal')`); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "usage.sqlite")
	for _, suffix := range []string{"", "-wal", "-shm"} {
		data, err := os.ReadFile(filepath.Join(source, "usage.sqlite") + suffix)
		if err != nil {
			t.Fatalf("read the restored member %q: %v", suffix, err)
		}
		if err := os.WriteFile(target+suffix, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := copyinmarker.Write(filepath.Dir(target), copyinmarker.Marker{
		Version:                 copyinmarker.Version,
		Phase:                   copyinmarker.PhaseMaterializing,
		TargetDatabase:          target,
		SourceKind:              "upgraded-legacy",
		SourceVersion:           "1.14.4",
		SourceFingerprintBefore: "fingerprint-before",
		SourceFingerprintAfter:  "fingerprint-after",
		BackupManifestSHA256:    "manifest",
		WrittenAtMS:             1717171717000,
	}); err != nil {
		t.Fatal(err)
	}
	restored := targetMemberState(t, target)
	if restored[databaseMember(target, "-shm")] == "" {
		t.Fatalf("the restored target holds no WAL index: %v", restored)
	}

	// The accepted admission decides this phase from the marker alone.
	stage, err := legacypreflight.DatabaseUpgradeAdmission{DatabasePath: target}.Admitted(context.Background())
	if err != nil || stage != legacypreflight.UpgradeStageInProgress {
		t.Fatalf("admission before status = %q err=%v", stage, err)
	}
	if after := targetMemberState(t, target); !reflect.DeepEqual(restored, after) {
		t.Fatalf("admission rewrote the materializing target: before=%v after=%v", restored, after)
	}

	// The real status route, with the source's own credential, so the Manager
	// credential open cannot be the writer.
	_, body := request(t, newHandler(source, target), http.MethodGet, testPrefix+StatusSuffix, bearer(testAdminKey), http.StatusOK)
	var status statusDocument
	if err := json.Unmarshal([]byte(body), &status); err != nil {
		t.Fatal(err)
	}
	if !status.Recorded || status.Evidence != EvidenceCopyInMarker || status.Stage != StageCopyIn ||
		!status.StageKnown || status.NextAction != ActionCopyIn {
		t.Fatalf("status on a materializing target = %s", body)
	}
	if after := targetMemberState(t, target); !reflect.DeepEqual(restored, after) {
		t.Fatalf("status rewrote the materializing target: before=%v after=%v", restored, after)
	}
}

// databaseMember names one member of the target database.
func databaseMember(database, suffix string) string {
	return filepath.Base(database) + suffix
}

// targetMemberState is the byte identity of a copied-in target's members.
func targetMemberState(t *testing.T, database string) map[string]string {
	t.Helper()
	state := map[string]string{}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		data, err := os.ReadFile(database + suffix)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(data)
		state[databaseMember(database, suffix)] = fmt.Sprintf("%x/%d", digest, len(data))
	}
	return state
}

// TestRelocatedRootKeepsTheMarkerForTheWizardAndTheAdmission is D4. The marker
// lives in the data root's own reserved work directory, so a root mounted at a
// different absolute path — the host that runs the CLI, the container that runs
// the server — is still the store with an unfinished copy-in. Admission already
// reads it that way; the wizard's projection used to compare absolute paths and
// report "no operation", sending the operator to a preflight for a store that is
// in progress. Both must apply one rule, and a marker naming a different
// database file must still fail closed for both.
func TestRelocatedRootKeepsTheMarkerForTheWizardAndTheAdmission(t *testing.T) {
	root := t.TempDir()
	database := filepath.Join(root, "usage.sqlite")
	writeMarker := func(target string) {
		t.Helper()
		if err := copyinmarker.Write(filepath.Dir(database), copyinmarker.Marker{
			Version:                 copyinmarker.Version,
			Phase:                   copyinmarker.PhaseMaterializing,
			TargetDatabase:          target,
			SourceKind:              "upgraded-legacy",
			SourceVersion:           "1.14.4",
			SourceFingerprintBefore: "fingerprint-before",
			SourceFingerprintAfter:  "fingerprint-after",
			BackupManifestSHA256:    "manifest",
			WrittenAtMS:             1717171717000,
		}); err != nil {
			t.Fatal(err)
		}
	}
	writeMarker(database)
	relocated := root + "-relocated"
	if err := os.Rename(root, relocated); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(relocated) }()
	database = filepath.Join(relocated, "usage.sqlite")

	stage, err := legacypreflight.DatabaseUpgradeAdmission{DatabasePath: database}.Admitted(context.Background())
	if err != nil || stage != legacypreflight.UpgradeStageInProgress {
		t.Fatalf("admission on the relocated root = %q/%v, want in progress", stage, err)
	}
	document, err := readStatus(context.Background(), database)
	if err != nil {
		t.Fatalf("status on the relocated root: %v", err)
	}
	if !document.Recorded || document.Evidence != EvidenceCopyInMarker || document.Stage != StageCopyIn ||
		document.NextAction != ActionCopyIn || !document.StageKnown {
		t.Fatalf("status lost the relocated provenance the admission keeps: %+v", document)
	}
	if document.SourceKind != "upgraded-legacy" || document.SourceVersion != "1.14.4" {
		t.Fatalf("status lost the recorded source evidence: %+v", document)
	}

	// A marker that names another database file is evidence for a different run.
	// The admission refuses it and the projection reads it as no record: neither
	// guesses that this store is the one that marker describes.
	writeMarker(filepath.Join(t.TempDir(), "other.sqlite"))
	if stage, err := (legacypreflight.DatabaseUpgradeAdmission{DatabasePath: database}).Admitted(context.Background()); err == nil {
		t.Fatalf("a foreign marker was admitted as %q", stage)
	}
	document, err = readStatus(context.Background(), database)
	if err != nil {
		t.Fatalf("status with a foreign marker: %v", err)
	}
	if document.Recorded || document.Evidence != EvidenceNone || document.NextAction != ActionPreflight {
		t.Fatalf("a foreign marker was read as this operation: %+v", document)
	}
}

// TestReadsRefuseCrossSiteOrigins is L4: the entry leaf's cross-site rule is the
// surface's rule, so the two reads refuse browser provenance from another site
// with the same code. The credential still comes first.
func TestReadsRefuseCrossSiteOrigins(t *testing.T) {
	database := filepath.Join(t.TempDir(), "usage.sqlite")
	recordOperation(t, database, model.UpgradeOperation{Stage: model.UpgradeStageCopyIn})
	handler := newHandler(legacySourceDir(t), database)
	handler.PublicOrigin = "https://manager.test"
	send := func(resource, authorization, origin, site string, want int) string {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, testPrefix+resource, nil)
		r.Host = "manager.test"
		if authorization != "" {
			r.Header.Set("Authorization", authorization)
		}
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if site != "" {
			r.Header.Set("Sec-Fetch-Site", site)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s origin=%q site=%q: got %d want %d: %s", resource, origin, site, w.Code, want, w.Body.String())
		}
		return w.Body.String()
	}
	for _, resource := range []string{PreflightSuffix, StatusSuffix} {
		for _, rejected := range [][2]string{
			{"https://attacker.test", ""},
			{"http://manager.test", ""}, // the configured origin is TLS-terminated
			{"https://manager.test", "cross-site"},
			{"https://manager.test", "same-site"},
		} {
			body := send(resource, bearer(testAdminKey), rejected[0], rejected[1], http.StatusForbidden)
			if !strings.Contains(body, `"`+upgradesurface.CodeOriginRejected+`"`) {
				t.Fatalf("%s did not name the origin rejection: %s", resource, body)
			}
		}
		send(resource, bearer(testAdminKey), "https://manager.test", "same-origin", http.StatusOK)
		send(resource, bearer(testAdminKey), "", "", http.StatusOK)
		// An unauthenticated caller learns nothing about the origin rule either.
		send(resource, "", "https://attacker.test", "", http.StatusUnauthorized)
	}
}

// TestStatusScrubsTheShapesTheReviewProvedLeak is the regression for the five
// shapes a content scrubber missed: a quoted bearer value, a JSON password, a
// base64 Basic value wrapped onto the next line, an assignment whose value is on
// the next line, and a bearer token cut by a line break.
func TestStatusScrubsTheShapesTheReviewProvedLeak(t *testing.T) {
	for _, secret := range []string{
		`Authorization: Bearer "opaque-review-key"`,
		`{"password":"opaque-review-password"}`,
		"Authorization: Basic\nc2VhcmNoOnNlY3JldA==",
		"password=\nopaque-review-password",
		"Bearer opaque-review-\nkeytail",
	} {
		got := scrubError(secret)
		for _, leaked := range []string{
			"opaque-review-key", "opaque-review-password", "c2VhcmNoOnNlY3JldA==",
			"opaque-review-password", "keytail",
		} {
			if strings.Contains(got, leaked) {
				t.Fatalf("%q leaked %q: %q", secret, leaked, got)
			}
		}
		if !strings.Contains(got, redactedText) {
			t.Fatalf("%q has no redaction marker: %q", secret, got)
		}
	}
}
