package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshconfig"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeyapply"
)

// externalRejectionKey is the one settings row that records that the user's CPA
// refused the stored External management credential. It exists because CPA bans
// the calling address for thirty minutes after five failed authentications: a page
// that keeps presenting a credential its own CPA already rejected is what turns a
// rotation into an outage for the person trying to fix it.
const externalRejectionKey = "fresh_external_rejection_v1"

// externalRejectionRecord is the refused connection, the credential envelope that
// was refused, and when. The address digest is the identity the whole External
// profile is keyed by, so a rebind to another CPA can never inherit this refusal.
// The envelope digest is what makes a refusal belong to the key that met it: a
// rebind replaces the envelope, so a refusal recorded against the previous one
// stops gating the new key the moment it is stored.
type externalRejectionRecord struct {
	Identity   string `json:"identity"`
	Credential string `json:"credential"`
	AtMS       int64  `json:"atMs"`
}

// externalReadGrant is the credential one browser-driven read will present, taken
// when the read starts. Two facts are captured together because they must be the
// ones the request uses: resolving them again after the answer arrives would name
// whatever a concurrent rebind stored in the meantime.
type externalReadGrant struct {
	// Identity is the committed connection's address digest.
	Identity string
	// Credential is the digest of the protected credential envelope in the record
	// the read resolved. It is not the credential and does not decrypt to it.
	Credential string
	// Refused reports that this exact connection and this exact envelope were
	// already refused, so the read must answer locally.
	Refused bool
}

// externalRejectionPersistBudgetDefault bounds how long refusal persistence may
// wait on SQLite lock acquisition or I/O before falling back to the process-local
// fail-closed blocker and releasing legacyCredentialGate.
const externalRejectionPersistBudgetDefault = 5 * time.Second

// errExternalRejectionUnavailable indicates that the durable refusal state could
// not be verified within its bounded persistence window (for example due to a
// SQLite read error, corrupt row, or wedged persistence slot), so callers must
// fail closed locally without presenting the stored credential to CPA.
var errExternalRejectionUnavailable = errors.New("external rejection state unavailable")

type externalRejectionGrantKey struct {
	identity   string
	credential string
}

// externalRejection is the durable refusal of the stored External credential,
// backed by an in-memory positive refusal cache and fail-closed lookup/write
// semantics when SQLite operations fail or exceed their bounded deadline. Every
// saved-key External path consults check() before sending anything, so a refused
// or unverified credential state never presents the key once more. Only a rebind,
// or a re-verify that proves the stored key works again, clears it.
type externalRejection struct {
	db              *sql.DB
	slotInit        sync.Once
	dbSlot          chan struct{}
	persistBudget   time.Duration
	newPersistTimer func(time.Duration) (<-chan time.Time, func())
	beforeRecord    func()

	memMu        sync.Mutex
	epoch        uint64
	clearSeq     uint64
	pendingClear bool
	localBlocked map[externalRejectionGrantKey]struct{}
}

type externalRejectionLoadResult struct {
	record externalRejectionRecord
	found  bool
	err    error
}

func newExternalRejection(db *sql.DB) *externalRejection {
	return &externalRejection{db: db}
}

func (r *externalRejection) slot() chan struct{} {
	r.slotInit.Do(func() {
		r.dbSlot = make(chan struct{}, 1)
	})
	return r.dbSlot
}

func (r *externalRejection) withPersistDeadline(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	budget := externalRejectionPersistBudgetDefault
	if r != nil && r.persistBudget > 0 {
		budget = r.persistBudget
	}
	if r != nil && r.newPersistTimer != nil {
		ctx, cancel := context.WithCancel(parent)
		ch, stop := r.newPersistTimer(budget)
		done := make(chan struct{})
		var stopOnce sync.Once
		go func() {
			select {
			case <-ch:
				cancel()
			case <-ctx.Done():
			case <-done:
			}
		}()
		return ctx, func() {
			stopOnce.Do(func() {
				close(done)
				if stop != nil {
					stop()
				}
				cancel()
			})
		}
	}
	return context.WithTimeout(parent, budget)
}

func (r *externalRejection) currentEpoch() uint64 {
	r.memMu.Lock()
	defer r.memMu.Unlock()
	return r.epoch
}

func (r *externalRejection) hasPendingClear() bool {
	r.memMu.Lock()
	defer r.memMu.Unlock()
	return r.pendingClear
}

func (r *externalRejection) installLocalBlocker(identity, credential string, epoch uint64) {
	r.memMu.Lock()
	defer r.memMu.Unlock()
	if r.epoch != epoch {
		return
	}
	if r.localBlocked == nil {
		r.localBlocked = make(map[externalRejectionGrantKey]struct{})
	}
	r.localBlocked[externalRejectionGrantKey{identity: identity, credential: credential}] = struct{}{}
}

func (r *externalRejection) isLocallyBlocked(identity, credential string) bool {
	r.memMu.Lock()
	defer r.memMu.Unlock()
	_, ok := r.localBlocked[externalRejectionGrantKey{identity: identity, credential: credential}]
	return ok
}

func (r *externalRejection) flushPendingClearUnderSlot(opCtx context.Context) {
	if r == nil || r.db == nil {
		return
	}
	r.memMu.Lock()
	if !r.pendingClear {
		r.memMu.Unlock()
		return
	}
	seq := r.clearSeq
	r.memMu.Unlock()
	if opCtx == nil || opCtx.Err() != nil {
		return
	}
	if _, err := r.db.ExecContext(opCtx, `delete from settings where key = ?`, externalRejectionKey); err == nil {
		r.memMu.Lock()
		if r.clearSeq == seq {
			r.pendingClear = false
		}
		r.memMu.Unlock()
	}
}

// check reports whether the CPA has refused the credential of exactly this
// connection and this envelope, consulting the in-memory refusal set first
// before reading SQLite. If SQLite lookup fails, times out, or encounters a
// corrupt row when no pending clear supersedes it, check returns
// errExternalRejectionUnavailable so callers fail closed without contacting CPA.
func (r *externalRejection) check(ctx context.Context, identity, credential string) (bool, error) {
	if r == nil || identity == "" || credential == "" {
		return false, nil
	}
	if r.isLocallyBlocked(identity, credential) {
		return true, nil
	}
	if r.db == nil {
		return false, nil
	}
	epoch := r.currentEpoch()
	record, found, err := r.load(ctx)
	if err != nil {
		return false, err
	}
	if !found {
		return false, nil
	}
	if record.Identity == identity && record.Credential == credential {
		if !r.hasPendingClear() {
			r.installLocalBlocker(identity, credential, epoch)
		}
		return true, nil
	}
	return false, nil
}

func (r *externalRejection) load(ctx context.Context) (externalRejectionRecord, bool, error) {
	if r == nil || r.db == nil {
		return externalRejectionRecord{}, false, nil
	}
	if r.hasPendingClear() {
		return externalRejectionRecord{}, false, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	loadCtx, cancel := r.withPersistDeadline(ctx)
	defer cancel()
	slot := r.slot()
	select {
	case slot <- struct{}{}:
	case <-loadCtx.Done():
		return externalRejectionRecord{}, false, fmt.Errorf("%w: %w", errExternalRejectionUnavailable, loadCtx.Err())
	}
	done := make(chan externalRejectionLoadResult, 1)
	go func() {
		defer func() {
			r.flushPendingClearUnderSlot(loadCtx)
			<-slot
		}()
		if r.hasPendingClear() {
			done <- externalRejectionLoadResult{}
			return
		}
		var raw string
		err := r.db.QueryRowContext(loadCtx, `select value from settings where key = ?`, externalRejectionKey).Scan(&raw)
		if errors.Is(err, sql.ErrNoRows) {
			done <- externalRejectionLoadResult{}
			return
		}
		if err != nil {
			done <- externalRejectionLoadResult{err: fmt.Errorf("%w: %w", errExternalRejectionUnavailable, err)}
			return
		}
		var record externalRejectionRecord
		if json.Unmarshal([]byte(raw), &record) != nil || record.Identity == "" || record.Credential == "" {
			done <- externalRejectionLoadResult{err: errExternalRejectionUnavailable}
			return
		}
		if r.hasPendingClear() {
			done <- externalRejectionLoadResult{}
			return
		}
		done <- externalRejectionLoadResult{record: record, found: true}
	}()
	select {
	case res := <-done:
		return res.record, res.found, res.err
	case <-loadCtx.Done():
		return externalRejectionRecord{}, false, fmt.Errorf("%w: %w", errExternalRejectionUnavailable, loadCtx.Err())
	}
}

// record marks this connection and credential envelope refused. Positive refusal
// knowledge is installed in memory for the current epoch immediately and retained
// across durable writes; both SQLite slot acquisition and statement execution are
// bounded by persistBudget.
func (r *externalRejection) record(ctx context.Context, identity, credential string) error {
	if r == nil || identity == "" || credential == "" {
		return nil
	}
	epoch := r.currentEpoch()
	r.installLocalBlocker(identity, credential, epoch)
	if r.db == nil {
		return errors.New("external rejection store has no database")
	}
	raw, err := json.Marshal(externalRejectionRecord{Identity: identity, Credential: credential, AtMS: time.Now().UnixMilli()})
	if err != nil {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	persistCtx, cancel := r.withPersistDeadline(context.WithoutCancel(ctx))
	defer cancel()
	slot := r.slot()
	select {
	case slot <- struct{}{}:
	case <-persistCtx.Done():
		return persistCtx.Err()
	}
	done := make(chan error, 1)
	go func() {
		defer func() {
			if r.hasPendingClear() {
				cleanCtx := persistCtx
				var cleanCancel context.CancelFunc
				if cleanCtx.Err() != nil {
					cleanCtx, cleanCancel = r.withPersistDeadline(context.Background())
				}
				r.flushPendingClearUnderSlot(cleanCtx)
				if cleanCancel != nil {
					cleanCancel()
				}
			}
			<-slot
		}()
		if r.beforeRecord != nil {
			r.beforeRecord()
		}
		if persistCtx.Err() != nil {
			done <- persistCtx.Err()
			return
		}
		r.memMu.Lock()
		if r.epoch != epoch {
			r.memMu.Unlock()
			done <- nil
			return
		}
		r.pendingClear = false
		r.memMu.Unlock()
		_, execErr := r.db.ExecContext(
			persistCtx,
			`insert into settings(key,value,updated_at_ms) values(?,?,?) on conflict(key) do update set value=excluded.value, updated_at_ms=excluded.updated_at_ms`,
			externalRejectionKey,
			string(raw),
			time.Now().UnixMilli(),
		)
		done <- execErr
	}()
	select {
	case execErr := <-done:
		return execErr
	case <-persistCtx.Done():
		return persistCtx.Err()
	}
}

// clear forgets the refusal. It runs only behind proof that the stored credential
// authenticates again: the successful read of a rebind or of a same-key re-verify.
func (r *externalRejection) clear(ctx context.Context) {
	if r == nil {
		return
	}
	r.memMu.Lock()
	r.epoch++
	r.clearSeq++
	clear(r.localBlocked)
	r.pendingClear = r.db != nil
	r.memMu.Unlock()
	if r.db == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	clearCtx, cancel := r.withPersistDeadline(context.WithoutCancel(ctx))
	defer cancel()
	slot := r.slot()
	select {
	case slot <- struct{}{}:
	case <-clearCtx.Done():
		return
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() { <-slot }()
		r.flushPendingClearUnderSlot(clearCtx)
	}()
	select {
	case <-done:
	case <-clearCtx.Done():
	}
}

// externalReadGrant captures the credential a browser-driven read is about to
// present, and whether it was already refused. Every read calls it once, before it
// sends anything: the CPA would answer 401/403 again, and five of those ban the
// address for thirty minutes. The same grant is what records a refusal, so an
// answer that arrives after a rebind is filed against the key that was actually
// sent rather than the one now stored.
func (a *freshApplication) externalReadGrant(ctx context.Context) externalReadGrant {
	if a == nil || a.rejection == nil || a.externalSource == nil || a.store == nil {
		return externalReadGrant{}
	}
	record, err := a.store.Load(ctx)
	if err != nil {
		return externalReadGrant{}
	}
	grant, _ := a.externalReadGrantFor(ctx, record)
	return grant
}

// externalReadGrantFor is externalReadGrant for a record the caller already
// loaded. The legacy gate reads it once to decide the route, and a saved-key
// request must not pay for the same row twice before it leaves.
func (a *freshApplication) externalReadGrantFor(ctx context.Context, record freshstore.Record) (externalReadGrant, error) {
	if a == nil || a.rejection == nil || a.externalSource == nil {
		return externalReadGrant{}, nil
	}
	install, err := a.externalSource(ctx)
	if err != nil {
		// An unresolvable authority is its own typed blocker further down; it is
		// not evidence that the credential was refused.
		return externalReadGrant{}, nil
	}
	if record.Connection == nil || record.Connection.ManagementKey == "" {
		// Without the envelope there is nothing to tell this read apart from one
		// on a rebound key, so the gate stays open rather than risk hiding the
		// user's data.
		return externalReadGrant{}, nil
	}
	grant := externalReadGrant{
		Identity:   install.Identity,
		Credential: envelopeDigest(record.Connection.ManagementKey),
	}
	refused, checkErr := a.rejection.check(ctx, grant.Identity, grant.Credential)
	if checkErr != nil {
		return externalReadGrant{}, fmt.Errorf("%w: %w", freshkeyapply.ErrExternalUnavailable, checkErr)
	}
	grant.Refused = refused
	return grant, nil
}

// externalReadTargetFor resolves both the External read grant and the decrypted
// installation target from one already-loaded Fresh record snapshot so the
// refusal check, the outbound request and any recorded rejection name the same
// credential envelope even across a concurrent rebind.
func (a *freshApplication) externalReadTargetFor(ctx context.Context, record freshstore.Record) (externalReadGrant, freshkeyapply.ExternalInstall, error) {
	if a == nil || a.externalSource == nil {
		return externalReadGrant{}, freshkeyapply.ExternalInstall{}, freshkeyapply.ErrUnavailable
	}
	install, err := a.externalSource(withFreshRecordSnapshot(ctx, record))
	if err != nil {
		return externalReadGrant{}, freshkeyapply.ExternalInstall{}, err
	}
	if a.rejection == nil || record.Connection == nil || record.Connection.ManagementKey == "" {
		return externalReadGrant{}, install, nil
	}
	grant := externalReadGrant{
		Identity:   install.Identity,
		Credential: envelopeDigest(record.Connection.ManagementKey),
	}
	refused, checkErr := a.rejection.check(ctx, grant.Identity, grant.Credential)
	if checkErr != nil {
		return externalReadGrant{}, freshkeyapply.ExternalInstall{}, fmt.Errorf("%w: %w", freshkeyapply.ErrExternalUnavailable, checkErr)
	}
	grant.Refused = refused
	return grant, install, nil
}

// envelopeDigest is the stable identity of one protected credential envelope. It
// is a digest of the ciphertext, so it changes exactly when a rebind replaces the
// credential and never reveals it.
func envelopeDigest(envelope string) string {
	sum := sha256.Sum256([]byte(envelope))
	return "envelope:sha256:" + hex.EncodeToString(sum[:])
}

// externalReadRefusedError is the typed blocker a refused read answers with
// without contacting the CPA.
func externalReadRefusedError() error { return cpa.ErrManagementRejected }

// writeExternalManagementRejected answers the one wire shape the browser treats as
// a refused credential: a conflict with the typed code, which offers a rebind and
// no retry.
func writeExternalManagementRejected(w http.ResponseWriter) {
	writeExternalManagementRejection(w, "")
}

// writeExternalManagementForbidden is the same blocker for a CPA 403. CPA answers
// 403 rather than 401 to a client it refuses — a banned address, or remote
// management disabled for it — and the 2a routes already pair that with the detail
// that says the ban expires, so the bridge names it the same way.
func writeExternalManagementForbidden(w http.ResponseWriter) {
	writeExternalManagementRejection(w, externalRebindForbiddenDetail)
}

// writeExternalManagementRejection writes the local refusal. It drops every entity
// header the proxy copied from CPA first: they describe the refused upstream body,
// and a `Content-Length` left behind truncates or empties this one, which is how
// the refusal became unreadable behind a real server.
func writeExternalManagementRejection(w http.ResponseWriter, detail string) {
	for _, name := range proxiedEntityHeaders {
		w.Header().Del(name)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusConflict)
	writeFreshJSON(w, struct {
		Code   string `json:"code"`
		Detail string `json:"detail,omitempty"`
	}{freshconfig.ReasonManagementRejected, detail})
}

// proxiedEntityHeaders are the response headers a reverse proxy copies from CPA
// onto the Manager's own response. None of them can describe a body this Manager
// writes itself.
var proxiedEntityHeaders = []string{
	"Content-Length", "Content-Type", "Content-Encoding", "Transfer-Encoding",
	"Trailer", "Content-Range", "Content-Disposition", "Content-Language",
	"Accept-Ranges", "ETag", "Last-Modified", "Vary",
}

// noteExternalRejection files a refusal against the grant the request used. It runs
// on the error mapping of every browser-driven read, which is where a CPA refusal
// is first known. Recording the grant — not a fresh resolution — is what keeps a
// slow refusal sent with the replaced key from gating the rebound one. A platform
// error, a moved target and a version refusal are not refusals of the credential
// and record nothing.
func (a *freshApplication) noteExternalRejection(ctx context.Context, grant externalReadGrant, err error) {
	if a == nil || a.rejection == nil || err == nil || !errors.Is(err, cpa.ErrManagementRejected) {
		return
	}
	if recErr := a.rejection.record(ctx, grant.Identity, grant.Credential); recErr != nil {
		log.Printf("Fresh external rejection durable write failed (enforced in-memory fail-closed blocker): %v", recErr)
	}
}

// clearExternalRejection forgets a recorded refusal after a read proved that the
// stored credential authenticates again.
func (a *freshApplication) clearExternalRejection(ctx context.Context) {
	if a == nil || a.rejection == nil {
		return
	}
	a.rejection.clear(ctx)
}

// observeExternalHoldHeaders installs a response-header observer that claims
// hold before recording any 401/403 refusal so concurrent tenure-budget expiry
// cannot release legacyCredentialGate ahead of durable refusal persistence.
func (a *freshApplication) observeExternalHoldHeaders(
	ctx context.Context,
	recordCtx context.Context,
	hold *externalCredentialHold,
	releaseOnHeader bool,
	grant externalReadGrant,
	recorded *atomic.Bool,
) context.Context {
	return cpa.WithExternalHeaderObserver(ctx, func(statusCode int, releaseGate bool) {
		switch statusCode {
		case http.StatusUnauthorized:
			if hold != nil {
				hold.claimHeader()
			}
			if recorded.CompareAndSwap(false, true) {
				a.noteExternalRejection(context.WithoutCancel(recordCtx), grant, cpa.ErrManagementRejected)
			}
			if releaseOnHeader && hold != nil {
				hold.release()
			}
			return
		case http.StatusForbidden:
			if hold != nil {
				hold.claimHeader()
			}
			if recorded.CompareAndSwap(false, true) {
				a.noteExternalRejection(context.WithoutCancel(recordCtx), grant, cpa.ErrManagementClientForbidden)
			}
			if releaseOnHeader && hold != nil {
				hold.release()
			}
			return
		}
		if releaseGate && releaseOnHeader && hold != nil {
			hold.release()
		}
	})
}

// withExternalSavedCredentialGate binds outbound External CPA wire calls on ctx
// to legacyCredentialGate so each network call resolves grant + credential
// from one post-gate record snapshot and records 401/403 before releasing the
// gate.
func (a *freshApplication) withExternalSavedCredentialGate(ctx context.Context) context.Context {
	if a == nil {
		return ctx
	}
	return cpa.WithExternalSavedCredentialGate(ctx, a.runExternalSavedCredentialCall)
}

func (a *freshApplication) runExternalSavedCredentialCall(
	ctx context.Context,
	call func(requestCtx context.Context, target cpa.ExternalSavedCredentialTarget) error,
) error {
	if a == nil || a.store == nil || a.externalSource == nil {
		return freshkeyapply.ErrUnavailable
	}
	gate := &a.legacyCredentialGate
	alreadyHeld := hasExternalCredentialHold(ctx, gate)
	requestCtx := ctx
	var hold *externalCredentialHold
	if !alreadyHeld {
		if !gate.acquire(ctx) {
			return errExternalCredentialWaitCancelled
		}
		hold, requestCtx = a.beginCredentialHold(ctx, a.externalCredentialHeaderBudget())
		defer hold.finish()
	}
	var (
		record freshstore.Record
		ok     bool
	)
	if alreadyHeld {
		record, ok = freshRecordSnapshotFromContext(requestCtx)
	}
	if !ok {
		var loadErr error
		record, loadErr = a.store.Load(requestCtx)
		if loadErr != nil {
			if requestCtx.Err() != nil {
				return requestCtx.Err()
			}
			return freshkeyapply.ErrUnavailable
		}
	}
	grant, install, err := a.externalReadTargetFor(requestCtx, record)
	if err != nil {
		return err
	}
	if grant.Refused {
		return externalReadRefusedError()
	}
	var recorded atomic.Bool
	activeHold := hold
	if activeHold == nil {
		activeHold = externalCredentialHoldFromContext(requestCtx, gate)
	}
	snapshotCtx := withFreshRecordSnapshot(requestCtx, record)
	snapshotCtx = a.observeExternalHoldHeaders(snapshotCtx, requestCtx, activeHold, !alreadyHeld, grant, &recorded)
	err = call(snapshotCtx, cpa.ExternalSavedCredentialTarget{
		BaseURL:       install.BaseURL,
		ManagementKey: install.ManagementKey,
	})
	if err != nil && !recorded.Load() {
		a.noteExternalRejection(context.WithoutCancel(requestCtx), grant, err)
	}
	return err
}

// checkExternalSavedCredentialPrecheck checks under legacyCredentialGate whether
// the committed External installation's currently stored credential is already
// refused or the caller cancelled while waiting on the gate, releasing the gate
// immediately before any local service mutex is acquired.
func (a *freshApplication) checkExternalSavedCredentialPrecheck(ctx context.Context) error {
	if a == nil || a.rejection == nil || a.externalSource == nil || a.store == nil {
		return nil
	}
	if ctx.Err() != nil {
		return errExternalCredentialWaitCancelled
	}
	record, err := a.store.Load(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return errExternalCredentialWaitCancelled
		}
		return nil
	}
	if !isCommittedExternalStrategy(record.Operation.Strategy) {
		return nil
	}
	gate := &a.legacyCredentialGate
	if !hasExternalCredentialHold(ctx, gate) {
		if !gate.acquire(ctx) {
			return errExternalCredentialWaitCancelled
		}
		defer gate.release()
		record, err = a.store.Load(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return errExternalCredentialWaitCancelled
			}
			return nil
		}
		if !isCommittedExternalStrategy(record.Operation.Strategy) {
			return nil
		}
	}
	grant, _, targetErr := a.externalReadTargetFor(ctx, record)
	if targetErr != nil {
		if ctx.Err() != nil {
			return errExternalCredentialWaitCancelled
		}
		if errors.Is(targetErr, errExternalRejectionUnavailable) {
			return errExternalRejectionUnavailable
		}
		return nil
	}
	if grant.Refused {
		return externalReadRefusedError()
	}
	return nil
}

func (a *freshApplication) writeExternalSavedCredentialPrecheck(w http.ResponseWriter, ctx context.Context, onRefused func()) bool {
	err := a.checkExternalSavedCredentialPrecheck(ctx)
	switch {
	case errors.Is(err, errExternalCredentialWaitCancelled):
		writeExternalCredentialWaitCancelled(w)
		return true
	case errors.Is(err, errExternalRejectionUnavailable):
		writeExternalAuthorityUnavailable(w)
		return true
	case errors.Is(err, cpa.ErrManagementRejected):
		onRefused()
		return true
	default:
		return false
	}
}
