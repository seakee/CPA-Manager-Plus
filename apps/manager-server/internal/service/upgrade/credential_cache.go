package upgrade

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"os"
	"sync"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/upgrade/copyinmarker"
)

// A source credential is proved on every authenticated request, and a status
// poll is one of those requests. Proving it cannot avoid reading the source —
// the key must be compared with the row the source holds — and the source is
// preserved, so the read is a private copy and its digests. That cost grows with
// the database: a healthy 512 MiB source needs seconds, and a fixed deadline
// below that turned a correct credential into `upgrade_credential_timeout` on
// every attempt.
//
// The two halves of the answer are therefore separated:
//
//   - The first read of a source state is bounded by a budget scaled to the
//     source size; see credentialVerifyBudget.
//   - A proved credential is remembered for as long as the source is provably
//     the same one. Every request stats the source; an identical identity reuses
//     the verdict, and anything else — a write, a checkpoint, a replaced file, a
//     new WAL, an eviction, a restart — verifies again.
//
// The cache is in memory only and stores no credential and no key: the entry is
// the digest of the presented key plus the source identity. A rejected key is
// never stored, and a failed read is never stored as a success.

const (
	// credentialCacheEntries bounds one process's cache. A Manager serves one
	// source, so the realistic working set is one key against one source state,
	// plus the operator's key rotation and a few source states during the copy-in.
	// Eviction only costs another read.
	credentialCacheEntries = 64
	// credentialFlightLimit bounds the verifications that may run at once. A
	// success bound alone does not bound the work: every different non-empty key
	// used to start a whole-source snapshot. Each running verification holds one
	// private copy of the source, so this is the resource bound as well as the
	// state bound; a request for a key already being verified joins that flight
	// and takes no slot. Waiting for a slot honours the caller's own deadline.
	credentialFlightLimit = 4
	// credentialVerifyBytesPerSecond is the read rate the first-verification
	// budget assumes. A source read costs three full passes (digest, copy,
	// digest) plus one row; 64 MiB/s is deliberately slower than local disk so
	// the budget covers a loaded machine and a modest network volume.
	credentialVerifyBytesPerSecond = 64 << 20
	// CredentialVerifyMaxBudget caps the scaled first read. A source larger than
	// this can still be qualified by the preflight, which has its own budget; a
	// route that answers a poll must not hold a request open indefinitely.
	CredentialVerifyMaxBudget = 20 * time.Second
)

// credentialFileIdentity is one file of the source as the cache sees it: the
// volume and inode the filesystem assigned it, its size, its modification time,
// and its kernel metadata change stamp (ctime / ChangeTime) so an in-place
// rewrite that restores mtime still invalidates any cached verdict. A missing
// file carries present=false, which is itself part of the identity: creating a
// WAL is a different source than not having one.
type credentialFileIdentity struct {
	present     bool
	device      uint64
	inode       uint64
	size        int64
	modifiedNS  int64
	changeStamp string
}

// sourceCredentialIdentity names one state of a source database and every
// journal sidecar beside it, so a sidecar appearing or changing invalidates a
// cached credential verdict instead of skipping source-file validation. When
// uncacheable is true (for example on a filesystem without ctime/ChangeTime),
// verifications still coalesce per flight but are never remembered across calls.
type sourceCredentialIdentity struct {
	database    credentialFileIdentity
	wal         credentialFileIdentity
	shm         credentialFileIdentity
	journal     credentialFileIdentity
	uncacheable bool
}

// readBytes is the size the first verification must read: the database plus a
// present WAL. A source that has not checkpointed keeps most of its committed
// data in the WAL, so a budget derived from the database alone timed out a
// healthy source whose bytes merely lived in the other file. The sum saturates:
// a size that cannot be represented must ask for the capped budget, never wrap
// into a short one.
func (i sourceCredentialIdentity) readBytes() int64 {
	total := i.database.size
	if i.wal.present {
		if total < 0 || i.wal.size > math.MaxInt64-total {
			return math.MaxInt64
		}
		total += i.wal.size
	}
	if total < 0 {
		return math.MaxInt64
	}
	return total
}

// string is the identity's cache representation.
func (i sourceCredentialIdentity) String() string {
	file := func(value credentialFileIdentity) string {
		if !value.present {
			return "absent"
		}
		return fmt.Sprintf("%d:%d:%d:%d:%s", value.device, value.inode, value.size, value.modifiedNS, value.changeStamp)
	}
	return file(i.database) + "|" + file(i.wal) + "|" + file(i.shm) + "|" + file(i.journal)
}

// identifySourceCredential describes the source state the presented key would be
// verified against. It stats only; it never opens the database.
func identifySourceCredential(databasePath string) (sourceCredentialIdentity, error) {
	database, dbUncacheable, err := identifyCredentialFile(databasePath)
	if err != nil {
		return sourceCredentialIdentity{}, err
	}
	wal, walUncacheable, err := identifyCredentialFile(databasePath + "-wal")
	if err != nil {
		return sourceCredentialIdentity{}, err
	}
	shm, shmUncacheable, err := identifyCredentialFile(databasePath + "-shm")
	if err != nil {
		return sourceCredentialIdentity{}, err
	}
	journal, journalUncacheable, err := identifyCredentialFile(databasePath + "-journal")
	if err != nil {
		return sourceCredentialIdentity{}, err
	}
	return sourceCredentialIdentity{
		database:    database,
		wal:         wal,
		shm:         shm,
		journal:     journal,
		uncacheable: dbUncacheable || walUncacheable || shmUncacheable || journalUncacheable,
	}, nil
}

// identifyCredentialFile applies the shared source-file policy to one member and
// records its identity including its change stamp. A missing file is the absent
// state; any link, special file, multi-link file or stat failure is reported
// instead of guessed at.
func identifyCredentialFile(path string) (credentialFileIdentity, bool, error) {
	info, err := inspectSnapshotSourceFile(path)
	if os.IsNotExist(err) {
		return credentialFileIdentity{}, false, nil
	}
	if err != nil {
		return credentialFileIdentity{}, false, credentialSourceError(err)
	}
	identity, err := copyinmarker.Identity(path)
	if err != nil {
		if os.IsNotExist(err) {
			return credentialFileIdentity{}, false, ErrSourceDatabaseChanged
		}
		return credentialFileIdentity{}, false, credentialSourceError(err)
	}
	changeStamp, err := copyinmarker.ChangeStamp(path)
	if err != nil {
		if errors.Is(err, copyinmarker.ErrChangeStampUnavailable) {
			return credentialFileIdentity{
				present:    true,
				device:     identity.Device,
				inode:      identity.Inode,
				size:       info.Size(),
				modifiedNS: info.ModTime().UnixNano(),
			}, true, nil
		}
		if os.IsNotExist(err) {
			return credentialFileIdentity{}, false, ErrSourceDatabaseChanged
		}
		return credentialFileIdentity{}, false, credentialSourceError(err)
	}
	return credentialFileIdentity{
		present:     true,
		device:      identity.Device,
		inode:       identity.Inode,
		size:        info.Size(),
		modifiedNS:  info.ModTime().UnixNano(),
		changeStamp: changeStamp,
	}, false, nil
}

// credentialVerifyBudget is the deadline for one uncached verification. It
// scales with the source size from the base budget and is capped: a large source
// is read on its first request, and the poll that follows is answered from the
// cache instead of paying for the copy again.
func credentialVerifyBudget(size int64) time.Duration {
	budget := credentialVerifyTimeout
	// Divide before multiplying: size*time.Second would overflow a Duration on a
	// source of about 8 GiB, which is exactly the size that needs the budget.
	// The seconds are compared with the cap before they are converted, so a huge
	// (or saturated) size cannot overflow the addition either.
	if budget > 0 && size > 0 {
		seconds := size / credentialVerifyBytesPerSecond
		if seconds >= int64(CredentialVerifyMaxBudget/time.Second) {
			return CredentialVerifyMaxBudget
		}
		budget += time.Duration(seconds) * time.Second
	}
	if budget > CredentialVerifyMaxBudget {
		budget = CredentialVerifyMaxBudget
	}
	return budget
}

// credentialFlight is one in-progress verification. A request that arrives while
// the same key is being verified against the same source state waits for that
// verification instead of starting a second copy of the whole database.
type credentialFlight struct {
	done chan struct{}
	err  error
}

// wait returns the flight's verdict, or this caller's own deadline. A caller that
// gives up does not cancel the shared read: another request is waiting for it,
// and the verdict is cached for the source state it describes. The shared read
// therefore has its own lifetime, while every caller — the owner included —
// waits under its own context. A verdict that lands after this caller was
// cancelled is never returned as its success: a request that was abandoned does
// not get to claim an authenticated answer.
func (f *credentialFlight) wait(ctx context.Context) error {
	select {
	case <-f.done:
		if f.err == nil {
			if err := ctx.Err(); err != nil {
				return credentialTimeout(err)
			}
		}
		return f.err
	case <-ctx.Done():
		return credentialTimeout(ctx.Err())
	}
}

// credentialCache remembers proved credentials by key digest and source state.
type credentialCache struct {
	mu      sync.Mutex
	entries map[string]struct{}
	// order is the insertion order the eviction bound follows.
	order   []string
	flights map[string]*credentialFlight
	// slots is the admission bound on verifications that are actually running.
	// A flight exists only while it holds one slot, so the bound covers both the
	// work and the map entry; joiners wait on the flight and take none.
	slots chan struct{}
}

func newCredentialCache() *credentialCache {
	return &credentialCache{
		entries: map[string]struct{}{},
		flights: map[string]*credentialFlight{},
		slots:   make(chan struct{}, credentialFlightLimit),
	}
}

// sourceCredentialCache is the process's cache. It holds no credential: an
// entry is a key digest and a source identity, and both are dropped on restart.
var sourceCredentialCache = newCredentialCache()

// errCredentialVerificationAborted reports a shared verification that ended
// without a verdict. It exists so a panicking read cannot leave the flight
// unfinished: waiters fail closed instead of waiting for their own deadline.
var errCredentialVerificationAborted = errors.New("source credential verification did not complete")

// verify returns a remembered success, joins an identical verification already in
// flight, or runs fn. Only a success is remembered; a rejected key and a failed
// read are returned and forgotten.
//
// The work runs on its own goroutine with its own lifetime, so the caller that
// started it can stop waiting without abandoning the verdict other requests on
// the same key are waiting for. Starting new work needs one of the finite
// admission slots; waiting for one is this caller's wait, not the shared work's,
// and a caller that gives up while waiting starts nothing and leaves no flight.
func (c *credentialCache) verify(ctx context.Context, key string, run func() error) error {
	return c.verifySource(ctx, key, true, nil, run)
}

func (c *credentialCache) verifySource(
	ctx context.Context,
	key string,
	cacheable bool,
	revalidate func() bool,
	run func() error,
) error {
	if err := ctx.Err(); err != nil {
		return credentialTimeout(err)
	}
	c.mu.Lock()
	if _, hit := c.entries[key]; hit {
		c.mu.Unlock()
		// The entry may have landed while this caller was waiting for the
		// mutex, and the caller may have been cancelled meanwhile. Waiting is
		// this caller's time, not the shared read's: a request abandoned during
		// it is answered with its own deadline, never with a success that
		// arrived after it gave up.
		if err := ctx.Err(); err != nil {
			return credentialTimeout(err)
		}
		if revalidate != nil && !revalidate() {
			c.forget(key)
		} else {
			return nil
		}
	} else {
		if flight, running := c.flights[key]; running {
			c.mu.Unlock()
			noteFlightJoin(key)
			return flight.wait(ctx)
		}
		c.mu.Unlock()
	}

	// One slot per running verification. The send is the admission decision; a
	// cancelled or expired caller never reaches it as an owner, including one
	// cancelled while this select was waiting.
	select {
	case c.slots <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-c.slots
			return credentialTimeout(err)
		}
	case <-ctx.Done():
		return credentialTimeout(ctx.Err())
	}

	// Another request may have started the same key while this one waited for a
	// slot. Join that flight instead of copying the source twice, and give the
	// slot back: a joiner never holds one.
	c.mu.Lock()
	if _, hit := c.entries[key]; hit {
		c.mu.Unlock()
		if err := ctx.Err(); err != nil {
			<-c.slots
			return credentialTimeout(err)
		}
		if revalidate != nil && !revalidate() {
			c.forget(key)
			c.mu.Lock()
		} else {
			<-c.slots
			return nil
		}
	}
	if flight, running := c.flights[key]; running {
		c.mu.Unlock()
		<-c.slots
		noteFlightJoin(key)
		return flight.wait(ctx)
	}
	flight := &credentialFlight{done: make(chan struct{})}
	c.flights[key] = flight
	c.mu.Unlock()

	go c.runFlight(key, cacheable, flight, run)
	return flight.wait(ctx)
}

func (c *credentialCache) forget(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.entries[key]; !exists {
		return
	}
	delete(c.entries, key)
	for i, existing := range c.order {
		if existing == key {
			c.order = append(c.order[:i], c.order[i+1:]...)
			break
		}
	}
}

// afterFlightJoin is the package's synchronous test seam: it runs once a caller
// has joined an in-flight verification, after the cache mutex is released and
// before the caller waits for the verdict. A test uses it to prove that a
// same-key joiner attached before the owner was released, instead of sleeping and
// hoping for the scheduler's order. Production never sets it.
var afterFlightJoin func(key string)

func noteFlightJoin(key string) {
	if afterFlightJoin != nil {
		afterFlightJoin(key)
	}
}

// runFlight publishes the verdict exactly once, including when the read panics:
// the flight is always completed and removed, so a concurrent request can never
// wait on a flight nobody will finish, and a panic is never recorded as a
// success. The admission slot is released on every path.
func (c *credentialCache) runFlight(key string, cacheable bool, flight *credentialFlight, run func() error) (err error) {
	completed := false
	defer func() {
		if recovered := recover(); recovered != nil {
			completed = false
			err = errCredentialVerificationAborted
		}
		c.mu.Lock()
		flight.err = err
		if !completed {
			flight.err = errCredentialVerificationAborted
		} else if err == nil && cacheable {
			c.rememberLocked(key)
		}
		delete(c.flights, key)
		c.mu.Unlock()
		close(flight.done)
		<-c.slots
	}()
	err = run()
	completed = true
	return err
}

// rememberLocked stores one success and keeps the bound. The oldest entry is
// dropped first; a dropped entry only costs the next request another read.
func (c *credentialCache) rememberLocked(key string) {
	if _, exists := c.entries[key]; exists {
		return
	}
	c.entries[key] = struct{}{}
	c.order = append(c.order, key)
	if len(c.order) > credentialCacheEntries {
		oldest := c.order[0]
		c.order = c.order[1:]
		delete(c.entries, oldest)
	}
}

// credentialCacheKey is the digest of the presented key bound to the source
// state. The resolved path is part of the key as well: two different files can
// share a device, an inode number, a size and a modification time after one is
// deleted and another created, and a verdict proved for one must never answer
// for the other.
func credentialCacheKey(key string, canonicalPath string, identity sourceCredentialIdentity) string {
	digest := sha256.Sum256([]byte(key))
	return hex.EncodeToString(digest[:]) + "@" + identity.String() + "@" + canonicalPath
}
