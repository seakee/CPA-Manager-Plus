package upgrade

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
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
// wedge the cache: the flight is removed, the panic is not remembered as a
// success, and the next request verifies normally.
func TestCredentialCacheCompletesAFlightThatPanics(t *testing.T) {
	cache := newCredentialCache()
	func() {
		defer func() { _ = recover() }()
		_ = cache.verify(context.Background(), "panics", func() error { panic("the read exploded") })
	}()
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
