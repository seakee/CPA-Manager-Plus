package upgrade

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
)

// TestCredentialVerifyBudgetScalesWithTheSourceAndCaps is the budget contract:
// the first read of a source gets time in proportion to the bytes it must copy,
// and no source gets more than the cap.
func TestCredentialVerifyBudgetScalesWithTheSourceAndCaps(t *testing.T) {
	previous := credentialVerifyTimeout
	credentialVerifyTimeout = 2 * time.Second
	t.Cleanup(func() { credentialVerifyTimeout = previous })
	for _, test := range []struct {
		size int64
		want time.Duration
	}{
		{size: 0, want: 2 * time.Second},
		{size: 64<<20 - 1, want: 2 * time.Second},
		{size: 64 << 20, want: 3 * time.Second},
		{size: 512 << 20, want: 10 * time.Second},
		{size: 1 << 30, want: 18 * time.Second},
		{size: 4 << 30, want: CredentialVerifyMaxBudget},
		{size: 1 << 40, want: CredentialVerifyMaxBudget},
	} {
		if got := credentialVerifyBudget(test.size); got != test.want {
			t.Fatalf("budget for %d bytes = %s, want %s", test.size, got, test.want)
		}
		if got := credentialVerifyBudget(test.size); got > CredentialVerifyMaxBudget {
			t.Fatalf("budget for %d bytes = %s, beyond the cap %s", test.size, got, CredentialVerifyMaxBudget)
		}
	}
}

// TestCredentialVerifyBudgetCountsTheWALAndSaturates is the other half of the
// budget contract: the bytes a first read carries are the database plus a
// present WAL, and an unrepresentable sum saturates into the capped budget
// instead of wrapping into a short one.
func TestCredentialVerifyBudgetCountsTheWALAndSaturates(t *testing.T) {
	previous := credentialVerifyTimeout
	credentialVerifyTimeout = 2 * time.Second
	t.Cleanup(func() { credentialVerifyTimeout = previous })
	identity := sourceCredentialIdentity{
		database: credentialFileIdentity{present: true, size: 24 << 10},
		wal:      credentialFileIdentity{present: true, size: 512 << 20},
	}
	want := 2*time.Second + time.Duration((int64(24<<10)+(512<<20))/credentialVerifyBytesPerSecond)*time.Second
	if got := credentialVerifyBudget(identity.readBytes()); got != want {
		t.Fatalf("budget for a 24 KiB database with a 512 MiB WAL = %s, want %s", got, want)
	}
	absent := identity
	absent.wal = credentialFileIdentity{}
	if got := credentialVerifyBudget(absent.readBytes()); got != 2*time.Second {
		t.Fatalf("an absent WAL added budget: %s", got)
	}
	huge := sourceCredentialIdentity{
		database: credentialFileIdentity{present: true, size: math.MaxInt64 - 1},
		wal:      credentialFileIdentity{present: true, size: 10},
	}
	if got := huge.readBytes(); got != math.MaxInt64 {
		t.Fatalf("overflowing source size = %d, want saturation at %d", got, int64(math.MaxInt64))
	}
	if got := credentialVerifyBudget(huge.readBytes()); got != CredentialVerifyMaxBudget {
		t.Fatalf("budget for a saturated source = %s, want the cap %s", got, CredentialVerifyMaxBudget)
	}
}

// TestCredentialCacheRemembersOnlySuccesses is the cache's verdict contract. A
// proved credential is remembered; a rejected key and a failed read are not, so
// a transient source failure never becomes an accepted credential.
func TestCredentialCacheRemembersOnlySuccesses(t *testing.T) {
	cache := newCredentialCache()
	var calls int32
	run := func(err error) func() error {
		return func() error {
			atomic.AddInt32(&calls, 1)
			return err
		}
	}
	if err := cache.verify(context.Background(), "proved", run(nil)); err != nil {
		t.Fatal(err)
	}
	if err := cache.verify(context.Background(), "proved", run(nil)); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("a remembered success ran %d verifications, want 1", got)
	}

	if err := cache.verify(context.Background(), "rejected", run(ErrAdminUnauthorized)); !errors.Is(err, ErrAdminUnauthorized) {
		t.Fatalf("rejected verification = %v, want %v", err, ErrAdminUnauthorized)
	}
	if err := cache.verify(context.Background(), "rejected", run(nil)); err != nil {
		t.Fatalf("rejected verification was remembered: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Fatalf("%d verifications after a rejection, want 3 (each attempt re-ran)", got)
	}

	if err := cache.verify(context.Background(), "unreadable", run(ErrSourceDatabaseChanged)); !errors.Is(err, ErrSourceDatabaseChanged) {
		t.Fatalf("failed verification = %v, want %v", err, ErrSourceDatabaseChanged)
	}
	if err := cache.verify(context.Background(), "unreadable", run(ErrAdminUnauthorized)); !errors.Is(err, ErrAdminUnauthorized) {
		t.Fatalf("a failed read was remembered as a success: %v", err)
	}
}

// TestCredentialCacheRunsOneVerificationForConcurrentRequests is the stampede
// contract: concurrent first requests for one source state share one read.
func TestCredentialCacheRunsOneVerificationForConcurrentRequests(t *testing.T) {
	cache := newCredentialCache()
	started := make(chan struct{})
	release := make(chan struct{})
	var calls int32
	first := make(chan error, 1)
	go func() {
		first <- cache.verify(context.Background(), "concurrent", func() error {
			atomic.AddInt32(&calls, 1)
			close(started)
			<-release
			return nil
		})
	}()
	<-started
	joined := make(chan error, 7)
	for range 7 {
		go func() {
			joined <- cache.verify(context.Background(), "concurrent", func() error {
				atomic.AddInt32(&calls, 1)
				return nil
			})
		}()
	}
	close(release)
	if err := <-first; err != nil {
		t.Fatalf("the verification in flight = %v", err)
	}
	for range 7 {
		if err := <-joined; err != nil {
			t.Fatalf("a joined request = %v", err)
		}
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("8 concurrent requests ran %d verifications, want 1", got)
	}
}

// TestCredentialCacheCompletesAFlightThatPanics proves a panicking read cannot
// wedge the cache: the shared work is recovered, the flight is removed, the
// panic is not remembered as a success, and the next request verifies normally.
func TestCredentialCacheCompletesAFlightThatPanics(t *testing.T) {
	cache := newCredentialCache()
	err := cache.verify(context.Background(), "panics", func() error { panic("the read exploded") })
	if !errors.Is(err, errCredentialVerificationAborted) {
		t.Fatalf("a panicking verification = %v, want %v", err, errCredentialVerificationAborted)
	}
	cache.mu.Lock()
	_, wedged := cache.flights["panics"]
	cache.mu.Unlock()
	if wedged {
		t.Fatal("a panicking verification left its flight in place")
	}
	if err := cache.verify(context.Background(), "panics", func() error { return nil }); err != nil {
		t.Fatalf("verify after a panic = %v", err)
	}
	if err := cache.verify(context.Background(), "panics", func() error {
		t.Error("the post-panic success was not remembered")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestCredentialCacheOwnerCancellationReturnsPromptly proves the caller that
// started a verification is cancellable exactly like a joiner. The shared read
// has its own lifetime and may finish for other requests, but the owner must not
// wait for it after its own context ends, and a verdict that lands afterwards is
// never returned as that cancelled request's success.
func TestCredentialCacheOwnerCancellationReturnsPromptly(t *testing.T) {
	cache := newCredentialCache()
	started := make(chan struct{})
	release := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- cache.verify(ctx, "owner-cancel", func() error {
			close(started)
			<-release
			return nil
		})
	}()
	<-started
	cancel()
	// The verdict lands after the cancellation: the owner must still report its
	// own deadline, not the shared read's later success.
	close(release)
	select {
	case err := <-done:
		if !errors.Is(err, ErrCredentialVerifyTimeout) {
			t.Fatalf("a cancelled owner = %v, want %v", err, ErrCredentialVerifyTimeout)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a cancelled owner kept waiting for the shared read")
	}
}

// TestCredentialCacheOwnerCancellationLetsAJoinerFinish proves the two waiters
// do not share a lifetime: the owner leaves on its own cancellation while a
// concurrent request for the same key still receives the shared verdict, which
// is then remembered for the source state it describes.
func TestCredentialCacheOwnerCancellationLetsAJoinerFinish(t *testing.T) {
	cache := newCredentialCache()
	started := make(chan struct{})
	release := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	owner := make(chan error, 1)
	go func() {
		owner <- cache.verify(ctx, "shared", func() error {
			close(started)
			<-release
			return nil
		})
	}()
	<-started
	joiner := make(chan error, 1)
	go func() {
		joiner <- cache.verify(context.Background(), "shared", func() error {
			t.Error("the joiner ran its own verification")
			return nil
		})
	}()
	cancel()
	if err := <-owner; !errors.Is(err, ErrCredentialVerifyTimeout) {
		t.Fatalf("cancelled owner = %v, want %v", err, ErrCredentialVerifyTimeout)
	}
	close(release)
	if err := <-joiner; err != nil {
		t.Fatalf("the joiner = %v, want the shared success", err)
	}
	if err := cache.verify(context.Background(), "shared", func() error {
		t.Error("the joiner's success was not remembered")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestCredentialCacheBoundsActiveVerifications is the P4 regression. The success
// entry bound says nothing about the work a caller can start: 80 different
// unproved keys used to create 80 active flights and 80 whole-source reads. The
// admission bound is finite, a same-key request joins the running flight instead
// of taking another slot, and the work that runs never exceeds the bound.
func TestCredentialCacheBoundsActiveVerifications(t *testing.T) {
	cache := newCredentialCache()
	const calls = 80
	started := make(chan struct{}, calls)
	release := make(chan struct{})
	var startedCount int32
	var wg sync.WaitGroup
	for index := range calls {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			_ = cache.verify(context.Background(), fmt.Sprintf("unproved-key-%d", index), func() error {
				atomic.AddInt32(&startedCount, 1)
				started <- struct{}{}
				<-release
				return ErrAdminUnauthorized
			})
		}(index)
	}
	for range credentialFlightLimit {
		<-started
	}
	time.Sleep(25 * time.Millisecond)
	cache.mu.Lock()
	flights, entries := len(cache.flights), len(cache.entries)
	cache.mu.Unlock()
	if flights > credentialFlightLimit {
		t.Fatalf("active flights = %d, beyond the bound %d", flights, credentialFlightLimit)
	}
	if got := atomic.LoadInt32(&startedCount); got != credentialFlightLimit {
		t.Fatalf("%d different unproved keys started verification, want the bound %d", got, credentialFlightLimit)
	}

	// A same-key request joins the flight and consumes no second slot.
	joined := make(chan error, 1)
	go func() {
		joined <- cache.verify(context.Background(), "unproved-key-0", func() error {
			t.Error("a same-key joiner started its own verification")
			return nil
		})
	}()
	time.Sleep(10 * time.Millisecond)
	if got := atomic.LoadInt32(&startedCount); got != credentialFlightLimit {
		t.Fatalf("a same-key joiner raised active work to %d, beyond the bound %d", got, credentialFlightLimit)
	}
	close(release)
	wg.Wait()
	if err := <-joined; !errors.Is(err, ErrAdminUnauthorized) {
		t.Fatalf("the joined wrong key = %v, want %v", err, ErrAdminUnauthorized)
	}
	cache.mu.Lock()
	flights, entries = len(cache.flights), len(cache.entries)
	cache.mu.Unlock()
	if entries != 0 {
		t.Fatalf("failed keys were remembered as successes: %d entries", entries)
	}
	if flights != 0 {
		t.Fatalf("%d flights survived their verdicts", flights)
	}
}

// TestCredentialCacheAdmissionReleasesASlotAfterCancellation proves the
// admission wait is the caller's own wait: a caller that gives up while the
// bound is saturated starts nothing, leaves no flight, and the slot it never
// took is available again once the running work completes.
func TestCredentialCacheAdmissionReleasesASlotAfterCancellation(t *testing.T) {
	cache := newCredentialCache()
	started := make(chan struct{}, credentialFlightLimit)
	release := make(chan struct{})
	var wg sync.WaitGroup
	for index := range credentialFlightLimit {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			_ = cache.verify(context.Background(), fmt.Sprintf("held-key-%d", index), func() error {
				started <- struct{}{}
				<-release
				return ErrAdminUnauthorized
			})
		}(index)
	}
	for range credentialFlightLimit {
		<-started
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := cache.verify(ctx, "cancelled-admission", func() error {
		t.Error("a cancelled caller started a verification")
		return nil
	}); !errors.Is(err, ErrCredentialVerifyTimeout) {
		t.Fatalf("a cancelled admission wait = %v, want %v", err, ErrCredentialVerifyTimeout)
	}
	cache.mu.Lock()
	_, present := cache.flights["cancelled-admission"]
	cache.mu.Unlock()
	if present {
		t.Fatal("a cancelled admission left a flight behind")
	}

	close(release)
	wg.Wait()
	if err := cache.verify(context.Background(), "after-release", func() error { return nil }); err != nil {
		t.Fatalf("a freed slot was not usable: %v", err)
	}
}

// TestCredentialCacheReportsTheWaitersOwnDeadline proves a joined request keeps
// its own bound instead of inheriting the flight's.
func TestCredentialCacheReportsTheWaitersOwnDeadline(t *testing.T) {
	cache := newCredentialCache()
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- cache.verify(context.Background(), "slow", func() error {
			close(started)
			<-release
			return nil
		})
	}()
	<-started
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := cache.verify(ctx, "slow", func() error {
		t.Error("a joined request started its own verification")
		return nil
	})
	if !errors.Is(err, ErrCredentialVerifyTimeout) {
		t.Fatalf("expired join = %v, want %v", err, ErrCredentialVerifyTimeout)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("the shared verification = %v", err)
	}
}

// TestCredentialCacheIsBoundedAndVerifiesAnEvictedSource proves the cache cannot
// grow with the number of source states, and that eviction costs a read rather
// than answering from a verdict nobody remembers any more.
func TestCredentialCacheIsBoundedAndVerifiesAnEvictedSource(t *testing.T) {
	cache := newCredentialCache()
	var calls int32
	run := func() error {
		atomic.AddInt32(&calls, 1)
		return nil
	}
	for index := range credentialCacheEntries {
		if err := cache.verify(context.Background(), fmt.Sprintf("key-%d", index), run); err != nil {
			t.Fatal(err)
		}
	}
	if err := cache.verify(context.Background(), "key-overflow", run); err != nil {
		t.Fatal(err)
	}
	cache.mu.Lock()
	size := len(cache.entries)
	cache.mu.Unlock()
	if size != credentialCacheEntries {
		t.Fatalf("cache holds %d entries, want the bound %d", size, credentialCacheEntries)
	}
	before := atomic.LoadInt32(&calls)
	if err := cache.verify(context.Background(), "key-0", run); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&calls); got != before+1 {
		t.Fatalf("an evicted source was answered without a verification")
	}
}

// TestVerifyAdminReadsAgainAfterTheSourceChanges is the invalidation contract at
// the read itself: the verdict holds for the source state it was proved against
// and no other. The credential is rotated the way a restore or a key change
// would, and the key the cache remembered is refused by the next read.
func TestVerifyAdminReadsAgainAfterTheSourceChanges(t *testing.T) {
	const rotated = "rotated-manager-credential"
	fixture := newCredentialFixture(t, "original-manager-credential")
	if err := VerifyAdmin(context.Background(), fixture.root, "original-manager-credential"); err != nil {
		t.Fatalf("verify the source credential: %v", err)
	}
	credential, err := security.NewAdminCredential(rotated, "test")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(credential)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(fixture.root, databaseFileName))
	if err != nil {
		t.Fatal(err)
	}
	execFixture(t, db, `update settings set value = ?, updated_at_ms = updated_at_ms + 1 where key = 'admin_credential_v1'`, string(raw))
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := VerifyAdmin(context.Background(), fixture.root, "original-manager-credential"); !errors.Is(err, ErrAdminUnauthorized) {
		t.Fatalf("the remembered verdict outlived the credential it described: %v", err)
	}
	if err := VerifyAdmin(context.Background(), fixture.root, rotated); err != nil {
		t.Fatalf("verify the rotated credential: %v", err)
	}
}

// TestVerifyAdminOwnerCancellationReturnsPromptly is P3 at the product read. The
// caller that started the shared snapshot is cancelled in the middle of it. The
// snapshot may finish for another request and for the cache, but this caller's
// request must return its own deadline instead of waiting out the read or
// claiming its success.
func TestVerifyAdminOwnerCancellationReturnsPromptly(t *testing.T) {
	const key = "owner-cancellation-credential"
	fixture := newCredentialFixture(t, key)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	previous := afterSnapshotCopy
	t.Cleanup(func() { afterSnapshotCopy = previous })
	afterSnapshotCopy = func() {
		cancel()
		// The shared work keeps its own lifetime: it is still running when the
		// owner's request has already been answered.
		time.Sleep(80 * time.Millisecond)
	}
	started := time.Now()
	err := VerifyAdminSource(ctx, fixture.dbPath, key)
	elapsed := time.Since(started)
	waitForCredentialFlights(t, sourceCredentialCache)
	if !errors.Is(err, ErrCredentialVerifyTimeout) {
		t.Fatalf("a cancelled owner = %v, want %v", err, ErrCredentialVerifyTimeout)
	}
	if elapsed >= 80*time.Millisecond {
		t.Fatalf("a cancelled owner held the request for %s; the caller's cancellation did not end its wait", elapsed)
	}
}

// waitForCredentialFlights waits for the shared reads a test deliberately left
// running after their callers returned. It keeps a test's temporary directory
// from being removed under a still-running snapshot.
func waitForCredentialFlights(t *testing.T, cache *credentialCache) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		cache.mu.Lock()
		live := len(cache.flights)
		cache.mu.Unlock()
		if live == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("shared verification work did not finish: %d flights", live)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// credentialNotifyingContext reports the checks a read makes on its caller's
// context. The credential read asks twice before it takes the cache mutex — once
// on its own entry and once in the cache — so the second check is the point at
// which this caller is about to wait for that mutex. The test releases the
// caller from exactly there, which is the window a cancelled request must not
// answer a success in.
type credentialNotifyingContext struct {
	context.Context
	calls    atomic.Int32
	notifyAt int32
	checked  chan struct{}
}

func (c *credentialNotifyingContext) Err() error {
	err := c.Context.Err()
	if c.calls.Add(1) == c.notifyAt {
		close(c.checked)
	}
	return err
}

// TestCancelledCacheHitDoesNotAuthenticate is the remembered-verdict half of the
// caller-cancellation contract: a request that is cancelled while it waits for
// the cache is answered with its own context error, not with the success that
// was already in the cache. The caller context is checked again after the wait
// and before the verdict is returned, both on the first lookup and on the one
// that follows admission.
func TestCancelledCacheHitDoesNotAuthenticate(t *testing.T) {
	const key = "cancelled-cache-hit"
	fixture := newCredentialFixture(t, key)
	// Warm the cache with a real read of the real source.
	if err := VerifyAdminSource(context.Background(), fixture.dbPath, key); err != nil {
		t.Fatalf("warm the credential cache: %v", err)
	}
	base, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := &credentialNotifyingContext{Context: base, notifyAt: 2, checked: make(chan struct{})}

	// Hold the cache, let the request reach it, then cancel the caller. The
	// request finds a remembered verdict when it is let in; the only correct
	// answer for a caller that is already gone is its own deadline.
	sourceCredentialCache.mu.Lock()
	result := make(chan error, 1)
	go func() { result <- VerifyAdminSource(ctx, fixture.dbPath, key) }()
	<-ctx.checked
	cancel()
	sourceCredentialCache.mu.Unlock()

	err := <-result
	t.Logf("cached product read after cancellation: result=%v caller=%v", err, base.Err())
	if !errors.Is(base.Err(), context.Canceled) {
		t.Fatalf("the caller was not cancelled: %v", base.Err())
	}
	if !errors.Is(err, ErrCredentialVerifyTimeout) {
		t.Fatalf("cancelled cache hit = %v, want %v", err, ErrCredentialVerifyTimeout)
	}
}
