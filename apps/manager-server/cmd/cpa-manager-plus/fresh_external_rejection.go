package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshconfig"
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

// externalRejection is the durable refusal of the stored External credential.
// Every browser-driven read of the user's CPA consults it before it sends
// anything, so a refused key is answered as the typed blocker instead of being
// presented once more. Only a rebind, or a re-verify that proves the stored key
// works again, clears it.
type externalRejection struct {
	db *sql.DB
	mu sync.Mutex
}

func newExternalRejection(db *sql.DB) *externalRejection {
	return &externalRejection{db: db}
}

// refused reports whether the CPA has refused the credential of exactly this
// connection and this envelope. An unreadable row is not a refusal: this Manager
// failing to read its own note must not hide the user's data, which is what the
// read is for.
func (r *externalRejection) refused(ctx context.Context, identity, credential string) bool {
	record, ok := r.load(ctx)
	return ok && record.Identity == identity && record.Credential == credential
}

func (r *externalRejection) load(ctx context.Context) (externalRejectionRecord, bool) {
	if r == nil || r.db == nil {
		return externalRejectionRecord{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var raw string
	if err := r.db.QueryRowContext(ctx, `select value from settings where key = ?`, externalRejectionKey).Scan(&raw); err != nil {
		return externalRejectionRecord{}, false
	}
	var record externalRejectionRecord
	if json.Unmarshal([]byte(raw), &record) != nil || record.Identity == "" {
		return externalRejectionRecord{}, false
	}
	return record, true
}

// record marks this connection refused. Failing to write the row is not a blocker
// of the read that met the refusal: that read still answers with the typed code,
// and the next one simply pays one more request.
func (r *externalRejection) record(ctx context.Context, identity, credential string) {
	if r == nil || r.db == nil || identity == "" || credential == "" {
		return
	}
	raw, err := json.Marshal(externalRejectionRecord{Identity: identity, Credential: credential, AtMS: time.Now().UnixMilli()})
	if err != nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	_, _ = r.db.ExecContext(ctx,
		`insert into settings(key,value,updated_at_ms) values(?,?,?) on conflict(key) do update set value=excluded.value, updated_at_ms=excluded.updated_at_ms`,
		externalRejectionKey, string(raw), time.Now().UnixMilli())
}

// clear forgets the refusal. It runs only behind proof that the stored credential
// authenticates again: the successful read of a rebind or of a same-key re-verify.
func (r *externalRejection) clear(ctx context.Context) {
	if r == nil || r.db == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	_, _ = r.db.ExecContext(ctx, `delete from settings where key = ?`, externalRejectionKey)
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
	install, err := a.externalSource(ctx)
	if err != nil {
		// An unresolvable authority is its own typed blocker further down; it is
		// not evidence that the credential was refused.
		return externalReadGrant{}
	}
	record, err := a.store.Load(ctx)
	if err != nil || record.Connection == nil || record.Connection.ManagementKey == "" {
		// Without the envelope there is nothing to tell this read apart from one
		// on a rebound key, so the gate stays open rather than risk hiding the
		// user's data.
		return externalReadGrant{}
	}
	grant := externalReadGrant{
		Identity:   install.Identity,
		Credential: envelopeDigest(record.Connection.ManagementKey),
	}
	grant.Refused = a.rejection.refused(ctx, grant.Identity, grant.Credential)
	return grant
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
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusConflict)
	writeFreshJSON(w, struct {
		Code string `json:"code"`
	}{freshconfig.ReasonManagementRejected})
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
	a.rejection.record(ctx, grant.Identity, grant.Credential)
}

// clearExternalRejection forgets a recorded refusal after a read proved that the
// stored credential authenticates again.
func (a *freshApplication) clearExternalRejection(ctx context.Context) {
	if a == nil || a.rejection == nil {
		return
	}
	a.rejection.clear(ctx)
}
