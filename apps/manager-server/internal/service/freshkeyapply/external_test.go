package freshkeyapply

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"path/filepath"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/identity"
	repo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshkeys"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeys"
)

// externalManagement is the management credential of the fake user CPA. It is
// opaque to this Manager, so the tests use a shape only the length check knows.
const externalManagement = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// fakeExternalCPA is a user-owned CPA: it carries a configuration, an arbitrary
// client-key list and one management credential, and it answers exactly the
// three v8 routes this Manager drives.
type fakeExternalCPA struct {
	keys        []string
	management  string
	rejectOnce  bool
	rejectEvery bool
	writes      int
	// onKeyRead runs before each client-key read, so a test can act as the user
	// editing their own CPA between the reads of one write.
	onKeyRead func(f *fakeExternalCPA)
	server    *httptest.Server
}

// config renders the CPA configuration the client-key list lives in. The list is
// part of that document, exactly as it is on a real CPA, so this Manager's own
// writes change the document digest.
func (f *fakeExternalCPA) config() []byte {
	keys := make([]any, 0, len(f.keys))
	for _, key := range f.keys {
		keys = append(keys, key)
	}
	data, _ := json.Marshal(map[string]any{"config-version": 8, "access": map[string]any{"api-keys": keys}})
	return data
}

func newFakeExternalCPA(t *testing.T, keys ...string) *fakeExternalCPA {
	t.Helper()
	f := &fakeExternalCPA{
		keys:       keys,
		management: externalManagement,
	}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if f.rejectEvery || (r.Header.Get("Authorization") != "Bearer "+f.management) {
			// A rotated management credential refuses every route, read included:
			// that is what makes it a blocker rather than an unavailable proof.
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case r.URL.Path == "/v8/management/config/access/api-keys" && r.Method == http.MethodGet:
			if f.onKeyRead != nil {
				f.onKeyRead(f)
			}
			_ = json.NewEncoder(w).Encode(f.keys)
		case r.URL.Path == "/v8/management/config.yaml" && r.Method == http.MethodGet:
			_, _ = w.Write(f.config())
		case r.URL.Path == "/v8/management/config/access/api-keys" && r.Method == http.MethodPut:
			f.writes++
			if f.rejectOnce {
				f.rejectOnce = false
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			var next []string
			if json.NewDecoder(r.Body).Decode(&next) != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			f.keys = next
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(f.server.Close)
	return f
}

// source is the External authority of this fake CPA: the committed connection and
// its management credential. It never contacts the CPA, exactly as the production
// source does not.
func (f *fakeExternalCPA) source() ExternalSource {
	return func(context.Context) (ExternalInstall, error) {
		return ExternalInstall{
			Identity:      ExternalIdentity(f.server.URL),
			Checkpoint:    1,
			BaseURL:       f.server.URL,
			ManagementKey: f.management,
		}, nil
	}
}

// externalHarness is one Manager workspace over one fake user CPA.
type externalHarness struct {
	cpa     *fakeExternalCPA
	keys    *freshkeys.Service
	service *Service
}

func newExternalHarness(t *testing.T, keys ...string) *externalHarness {
	t.Helper()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "manager.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err = sqlite.Migrate(db); err != nil {
		t.Fatal(err)
	}
	r := repo.New(db)
	protector, err := security.NewProtector([]byte("isolated-external-test-key"))
	if err != nil {
		t.Fatal(err)
	}
	h := &externalHarness{cpa: newFakeExternalCPA(t, keys...), keys: freshkeys.New(r, protector)}
	h.service = NewExternal(r, h.keys, h.cpa.source())
	// The fake CPA serves the v8 key surface, so the version probe the write gate
	// reads is stubbed to the contract that surface speaks. The probe itself is
	// tested against its own fake server in the cpa package.
	h.service.externalProbe = supportedExternalVersion
	return h
}

// supportedExternalVersion stands in for a CPA that advertises the v8 contract.
func supportedExternalVersion(context.Context, string, string) (cpa.ExternalVersionReport, error) {
	return cpa.ClassifyExternalVersion("v8.0.13"), nil
}

// staged stages one Manager key and returns its canonical ID and secret.
func (h *externalHarness) staged(t *testing.T, requestID string) (string, string) {
	t.Helper()
	result, _, err := h.keys.Create(context.Background(), requestID)
	if err != nil {
		t.Fatal(err)
	}
	return string(result.Key.ID), result.CreatedSecret
}

// The journal generation is the connection identity and nothing else. A user's CPA
// configuration is theirs to edit, and this Manager's own key writes land in the
// same document, so a configuration digest in the generation would invalidate
// every operation after its own successful write.
func TestExternalGenerationIsTheConnectionIdentity(t *testing.T) {
	identity := ExternalIdentity("https://cpa.example")
	if !validExternalIdentity(identity) {
		t.Fatalf("identity = %q", identity)
	}
	digest := sha256.Sum256([]byte("https://cpa.example"))
	if identity != "external:sha256:"+hex.EncodeToString(digest[:]) {
		t.Fatalf("identity is not the address digest: %q", identity)
	}
	install := ExternalInstall{Identity: identity, Checkpoint: 1, BaseURL: "https://cpa.example", ManagementKey: externalManagement}
	if install.Generation() != identity {
		t.Fatalf("generation = %q, want the connection identity", install.Generation())
	}
	other := ExternalInstall{Identity: ExternalIdentity("https://other.example")}
	if other.Generation() == identity {
		t.Fatal("a changed connection kept the generation")
	}
}

// The External authority never resolves a target of another profile, an
// incomplete installation, or a credential the tests did not supply.
func TestExternalAuthorityFailsClosed(t *testing.T) {
	harness := newExternalHarness(t)
	ctx := context.Background()
	for _, scenario := range []struct {
		name  string
		edit  func(ExternalInstall) ExternalInstall
		cause error
	}{
		{"unusable credential", func(i ExternalInstall) ExternalInstall { i.ManagementKey = " leading-space"; return i }, nil},
		{"empty address", func(i ExternalInstall) ExternalInstall { i.BaseURL = ""; return i }, nil},
		{"no checkpoint", func(i ExternalInstall) ExternalInstall { i.Checkpoint = 0; return i }, nil},
		{"foreign identity", func(i ExternalInstall) ExternalInstall {
			i.Identity = "managed:sha256:" + strings.Repeat("c", 64)
			return i
		}, nil},
		{"identity of another address", func(i ExternalInstall) ExternalInstall {
			i.BaseURL = "https://other.example"
			return i
		}, nil},
		{"rejected credential", func(i ExternalInstall) ExternalInstall { return i }, cpa.ErrManagementRejected},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			source := harness.cpa.source()
			service := NewExternal(nil, harness.keys, func(ctx context.Context) (ExternalInstall, error) {
				install, err := source(ctx)
				if err != nil {
					return ExternalInstall{}, err
				}
				return scenario.edit(install), nil
			})
			if scenario.cause != nil {
				service.externalSource = func(context.Context) (ExternalInstall, error) { return ExternalInstall{}, scenario.cause }
			}
			if _, _, err := service.ExternalTarget(ctx, 1); !errors.Is(err, ErrExternalUnavailable) &&
				!(scenario.cause != nil && errors.Is(err, scenario.cause)) {
				t.Fatalf("unresolved authority answered: %v", err)
			}
		})
	}
	if _, _, err := harness.service.ExternalTarget(ctx, 2); !errors.Is(err, ErrGeneration) {
		t.Fatalf("stale generation answered: %v", err)
	}
}

// Create stages a key and installs it on the user's CPA; rename is Manager-local,
// and the CPA is read back so the list never claims an effect it cannot see.
func TestExternalInstallRenameAndReadBack(t *testing.T) {
	harness := newExternalHarness(t)
	ctx := context.Background()
	id, secret := harness.staged(t, strings.Repeat("1", 32))

	state, err := harness.service.Apply(ctx, id, 1)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !slices.Contains(harness.cpa.keys, secret) {
		t.Fatal("the user's CPA did not receive the staged key")
	}
	if !state.ManagerStaged || !state.Attempted || !state.CPAConfigured || state.RuntimeState != "effective_observed" {
		t.Fatalf("apply state = %+v", state)
	}
	// A replay of the same key must not duplicate the credential.
	before := harness.cpa.writes
	if _, err = harness.service.Apply(ctx, id, 1); err != nil {
		t.Fatalf("replay: %v", err)
	}
	if harness.cpa.writes != before {
		t.Fatal("a key the CPA already carried was written again")
	}
	if _, err := harness.service.ExternalKeyPresenceOf(ctx, "ignored"); !errors.Is(err, freshkeys.ErrRequest) {
		t.Fatalf("a malformed id was accepted: %v", err)
	}
	if present, err := harness.service.ExternalKeyPresenceOf(ctx, mustID(t, id)); err != nil || !present {
		t.Fatalf("read-back presence = %v %v", present, err)
	}
	renamed, err := harness.keys.Rename(ctx, id, "Production client", 1)
	if err != nil || renamed.NameRevision != 2 {
		t.Fatalf("rename = %+v %v", renamed, err)
	}
	if len(harness.cpa.keys) != 1 {
		t.Fatalf("rename reached the user's CPA: %v", harness.cpa.keys)
	}
}

// A revoke removes the live credential from the user's CPA and proves it; the
// last one is refused before anything is attempted, because removing it would
// leave the user's CPA accepting unauthenticated clients.
func TestExternalRevokeAndLastKeyProtection(t *testing.T) {
	harness := newExternalHarness(t)
	ctx := context.Background()
	target, targetSecret := harness.staged(t, strings.Repeat("2", 32))
	keeper, keeperSecret := harness.staged(t, strings.Repeat("3", 32))
	if _, err := harness.service.Apply(ctx, target, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := harness.service.Apply(ctx, keeper, 1); err != nil {
		t.Fatal(err)
	}
	// Two credentials: revoking either is allowed.
	result, err := harness.service.Prepare(ctx, strings.Repeat("4", 32), target, keeper, "revoke", 1)
	if err != nil {
		t.Fatalf("prepare revoke: %v", err)
	}
	if result.Operation == nil || result.NextStep != "revoke" {
		t.Fatalf("prepared operation = %+v", result)
	}
	result, err = harness.service.ContinueOperation(ctx, result.Operation.ID, 1)
	if err != nil {
		t.Fatalf("continue revoke: %v", err)
	}
	if slices.Contains(harness.cpa.keys, targetSecret) {
		t.Fatal("the revoked credential is still carried by the user's CPA")
	}
	if !slices.Contains(harness.cpa.keys, keeperSecret) {
		t.Fatal("the keeper credential was lost")
	}
	if result.Operation.Outcome != "committed" {
		t.Fatalf("revoke outcome = %q", result.Operation.Outcome)
	}

	// Replace installs the new credential and then removes the old one.
	replacement, replacementSecret := harness.staged(t, strings.Repeat("5", 32))
	result, err = harness.service.Prepare(ctx, strings.Repeat("6", 32), keeper, replacement, "replace", 1)
	if err != nil {
		t.Fatalf("prepare replace: %v", err)
	}
	for result.NextStep != "" && result.Operation != nil && result.Operation.Outcome != "committed" {
		result, err = harness.service.ContinueOperation(ctx, result.Operation.ID, 1)
		if err != nil {
			t.Fatalf("continue replace: %v", err)
		}
	}
	if !slices.Contains(harness.cpa.keys, replacementSecret) {
		t.Fatal("the replacement credential is not carried")
	}
	if slices.Contains(harness.cpa.keys, keeperSecret) {
		t.Fatal("the replaced credential is still carried")
	}

	// One credential left: revoking it is refused and nothing is attempted.
	sole, _ := harness.staged(t, strings.Repeat("7", 32))
	spare, _ := harness.staged(t, strings.Repeat("8", 32))
	if _, err := harness.service.Apply(ctx, sole, 1); err != nil {
		t.Fatal(err)
	}
	harness.cpa.keys = []string{replacementSecret}
	harness.cpa.writes = 0
	if _, err = harness.service.Prepare(ctx, strings.Repeat("9", 32), replacement, spare, "revoke", 1); !errors.Is(err, ErrLastKey) {
		t.Fatalf("last key revoke = %v", err)
	}
	if harness.cpa.writes != 0 {
		t.Fatal("a refused revoke reached the user's CPA")
	}
}

// An edit the user makes between Prepare and Continue settles by read-back. The
// journal's generation is the connection, so the edit never invalidates it; the
// step's own fact is what it settles on, so the operation reaches a terminal state
// and the next operation on the same connection is still accepted.
func TestExternalOperationSettlesWhenTheUserEditsBetweenPrepareAndContinue(t *testing.T) {
	harness := newExternalHarness(t)
	ctx := context.Background()
	target, targetSecret := harness.staged(t, strings.Repeat("2", 32))
	keeper, keeperSecret := harness.staged(t, strings.Repeat("3", 32))
	if _, err := harness.service.Apply(ctx, target, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := harness.service.Apply(ctx, keeper, 1); err != nil {
		t.Fatal(err)
	}
	result, err := harness.service.Prepare(ctx, strings.Repeat("4", 32), target, keeper, "revoke", 1)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	// The user revokes the credential the operation was going to remove, directly
	// on their own CPA, before the Manager continues.
	harness.cpa.keys = slices.DeleteFunc(harness.cpa.keys, func(key string) bool { return key == targetSecret })
	writes := harness.cpa.writes
	result, err = harness.service.ContinueOperation(ctx, result.Operation.ID, 1)
	if err != nil {
		t.Fatalf("continue after the user's own edit: %v", err)
	}
	if result.Operation == nil || result.Operation.Outcome != "committed" {
		t.Fatalf("the settled operation = %+v", result.Operation)
	}
	if harness.cpa.writes != writes {
		t.Fatal("a step whose fact already held was written again")
	}
	if !slices.Contains(harness.cpa.keys, keeperSecret) {
		t.Fatal("the keeper credential was lost")
	}
	// The journal is terminal, so the next operation is accepted: an operation
	// stuck at an unknown effect would have refused it at BeginOperation.
	next, nextSecret := harness.staged(t, strings.Repeat("5", 32))
	if _, err = harness.service.Apply(ctx, next, 1); err != nil {
		t.Fatalf("apply after a settled operation: %v", err)
	}
	result, err = harness.service.Prepare(ctx, strings.Repeat("6", 32), keeper, next, "revoke", 1)
	if err != nil {
		t.Fatalf("prepare after a settled operation: %v", err)
	}
	if _, err = harness.service.ContinueOperation(ctx, result.Operation.ID, 1); err != nil {
		t.Fatalf("continue after a settled operation: %v", err)
	}
	if slices.Contains(harness.cpa.keys, keeperSecret) || !slices.Contains(harness.cpa.keys, nextSecret) {
		t.Fatalf("the later operation did not take effect: %v", harness.cpa.keys)
	}
}

// A 401 from the user's CPA is the typed blocker on every External write: it is
// reported as such, never retried, and never followed by another write.
func TestExternalRejectedManagementKeyIsNeverRetried(t *testing.T) {
	harness := newExternalHarness(t)
	ctx := context.Background()
	id, _ := harness.staged(t, strings.Repeat("a", 32))
	harness.cpa.rejectEvery = true
	state, err := harness.service.Apply(ctx, id, 1)
	if !errors.Is(err, cpa.ErrManagementRejected) {
		t.Fatalf("apply on a rejected credential = %v", err)
	}
	if state.RuntimeState != "effect_unknown" {
		t.Fatalf("a rejected credential claimed an effect: %+v", state)
	}
	// The management credential is refused before the list is even read, so no
	// write is attempted and none is retried.
	if harness.cpa.writes != 0 {
		t.Fatalf("a rejected credential still wrote: %d", harness.cpa.writes)
	}
	// The authority is the committed connection alone, so it still resolves: the
	// refusal is the typed blocker of the read that met it and never a claim about
	// the target itself.
	if _, _, err := harness.service.ExternalTarget(ctx, 1); err != nil {
		t.Fatalf("authority on a rejected credential = %v", err)
	}
}

// A revoke whose write the CPA refuses keeps the credential and journals the step
// as not applied. A 401 proves nothing was written, so the step is taken again
// once the connection accepts the credential — as it will after a rebind — instead
// of the journal staying unknown forever.
func TestExternalRevokeRejectionIsNotAnAppliedStep(t *testing.T) {
	harness := newExternalHarness(t)
	ctx := context.Background()
	target, targetSecret := harness.staged(t, strings.Repeat("b", 32))
	keeper, _ := harness.staged(t, strings.Repeat("c", 32))
	if _, err := harness.service.Apply(ctx, target, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := harness.service.Apply(ctx, keeper, 1); err != nil {
		t.Fatal(err)
	}
	harness.cpa.rejectOnce = true
	result, err := harness.service.Prepare(ctx, strings.Repeat("d", 32), target, keeper, "revoke", 1)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if _, err = harness.service.ContinueOperation(ctx, result.Operation.ID, 1); !errors.Is(err, cpa.ErrManagementRejected) {
		t.Fatalf("refused revoke = %v", err)
	}
	if !slices.Contains(harness.cpa.keys, targetSecret) {
		t.Fatal("a refused write removed the credential")
	}
	// The journal records the refusal as "not applied", not as an unknown effect.
	journal, err := harness.service.repo.Operation(ctx, result.Operation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if journal.Outcome != "pending" || journal.Steps[0].State != "unattempted" {
		t.Fatalf("a refused write was journaled as %q with step %q", journal.Outcome, journal.Steps[0].State)
	}
	// The CPA accepts the credential again, and the step is taken once.
	result, err = harness.service.ContinueOperation(ctx, result.Operation.ID, 1)
	if err != nil {
		t.Fatalf("retry after the CPA accepts the credential: %v", err)
	}
	if slices.Contains(harness.cpa.keys, targetSecret) {
		t.Fatal("the revoked credential is still carried by the user's CPA")
	}
	if result.Operation == nil || result.Operation.Outcome != "committed" {
		t.Fatalf("retried revoke outcome = %+v", result.Operation)
	}
}

// A revoke is refused — before anything is written — unless the CPA carries the
// keeper this Manager leaves behind. A credential from outside this Manager that
// merely keeps the last-key count above one is not a keeper the journal can
// settle on, so the target is never deleted without it.
func TestExternalRevokeRequiresACarriedKeeper(t *testing.T) {
	harness := newExternalHarness(t)
	ctx := context.Background()
	target, targetSecret := harness.staged(t, strings.Repeat("b", 32))
	keeper, keeperSecret := harness.staged(t, strings.Repeat("c", 32))
	if _, err := harness.service.Apply(ctx, target, 1); err != nil {
		t.Fatal(err)
	}
	harness.cpa.keys = []string{targetSecret, "credential-from-outside"}
	harness.cpa.writes = 0
	if _, err := harness.service.Prepare(ctx, strings.Repeat("d", 32), target, keeper, "revoke", 1); !errors.Is(err, cpa.ErrSourceConflict) {
		t.Fatalf("prepare with an uninstalled keeper = %v", err)
	}
	if harness.cpa.writes != 0 {
		t.Fatal("a refused revoke reached the user's CPA")
	}
	// The keeper is carried when the operation is journaled and removed before it
	// continues: that write is refused in exactly the same way.
	if _, err := harness.service.Apply(ctx, keeper, 1); err != nil {
		t.Fatal(err)
	}
	result, err := harness.service.Prepare(ctx, strings.Repeat("e", 32), target, keeper, "revoke", 1)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	harness.cpa.keys = []string{targetSecret, "credential-from-outside"}
	harness.cpa.writes = 0
	if _, err = harness.service.ContinueOperation(ctx, result.Operation.ID, 1); !errors.Is(err, cpa.ErrSourceConflict) {
		t.Fatalf("continue with a removed keeper = %v", err)
	}
	if harness.cpa.writes != 0 || !slices.Contains(harness.cpa.keys, targetSecret) {
		t.Fatal("a revoke without a carried keeper was written")
	}
	if !slices.Contains(harness.cpa.keys, "credential-from-outside") {
		t.Fatal("a refused revoke rewrote the user's own credential list")
	}
	if keeperSecret == "" {
		t.Fatal("keeper secret missing")
	}
}

// The list reports the presence the user's CPA answers: a staged key it does not
// carry is never shown as applied, and a key it does carry is.
func TestExternalKeyListReportsTheCPAsPresence(t *testing.T) {
	harness := newExternalHarness(t)
	ctx := context.Background()
	applied, appliedSecret := harness.staged(t, strings.Repeat("e", 32))
	_, pendingSecret := harness.staged(t, strings.Repeat("f", 32))
	if _, err := harness.service.Apply(ctx, applied, 1); err != nil {
		t.Fatal(err)
	}
	// The user installed the second credential out of band.
	harness.cpa.keys = append(harness.cpa.keys, pendingSecret)
	harness.cpa.keys = slices.DeleteFunc(harness.cpa.keys, func(key string) bool { return key == appliedSecret })
	harness.cpa.keys = append(harness.cpa.keys, appliedSecret)

	keys, err := harness.keys.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	listed, unverified, err := harness.service.ExternalKeyList(ctx, keys)
	if err != nil || unverified {
		t.Fatalf("listed %d keys, unverified=%v, err=%v", len(listed), unverified, err)
	}
	if len(listed) != 2 {
		t.Fatalf("listed %d keys", len(listed))
	}
	for _, key := range listed {
		if key.RuntimeState != "effective_observed" {
			t.Fatalf("key %s state = %q", key.ID, key.RuntimeState)
		}
	}
	// A key the CPA no longer carries is not claimed.
	harness.cpa.keys = slices.DeleteFunc(harness.cpa.keys, func(key string) bool { return key == appliedSecret })
	listed, _, err = harness.service.ExternalKeyList(ctx, keys)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range listed {
		if string(key.ID) == applied && key.RuntimeState == "effective_observed" {
			t.Fatal("a key the CPA does not carry was reported as applied")
		}
	}
	// Another profile's list is returned untouched.
	private := New(nil, harness.keys, nil)
	same, unverified, err := private.ExternalKeyList(ctx, keys)
	if err != nil || unverified || len(same) != len(keys) {
		t.Fatalf("non-external list = %d unverified=%v %v", len(same), unverified, err)
	}
}

func mustID(t *testing.T, raw string) identity.APIKeyID {
	t.Helper()
	parsed, err := identity.ParseAPIKeyID(raw)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

// The management credential is chosen by the user and Connect External accepts any
// printable key up to 4096 characters (cpa/v8.go). Only that rule may refuse one
// here: an exact-length check would report a usable installation as unavailable.
func TestExternalAuthorityUsesTheConnectExternalKeyRule(t *testing.T) {
	harness := newExternalHarness(t)
	ctx := context.Background()
	install := ExternalInstall{
		Identity:      ExternalIdentity("https://cpa.example"),
		Checkpoint:    1,
		BaseURL:       "https://cpa.example",
		ManagementKey: "short-key-2024",
	}
	service := NewExternal(nil, harness.keys, func(context.Context) (ExternalInstall, error) { return install, nil })
	if _, key, err := service.ExternalTarget(ctx, 1); err != nil || key != install.ManagementKey {
		t.Fatalf("a %d-character management key = %q, %v", len(install.ManagementKey), key, err)
	}
	for _, refused := range []string{"", " padded ", "line\nbreak", strings.Repeat("k", 4097)} {
		install.ManagementKey = refused
		service.externalSource = func(context.Context) (ExternalInstall, error) { return install, nil }
		if _, _, err := service.ExternalTarget(ctx, 1); !errors.Is(err, ErrExternalUnavailable) {
			t.Fatalf("a key the Connect External rule refuses was accepted: %q", refused)
		}
	}
}

// The key list is the Manager's own record when the user's CPA cannot be read, and
// it says so. An outage is not a blocker: hiding the staged keys behind an error
// would report the Manager's own knowledge as unknown.
func TestExternalKeyListSurvivesACPAOutage(t *testing.T) {
	harness := newExternalHarness(t)
	ctx := context.Background()
	id, _ := harness.staged(t, strings.Repeat("a", 32))
	if _, err := harness.service.Apply(ctx, id, 1); err != nil {
		t.Fatal(err)
	}
	keys, err := harness.keys.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// The CPA is unreachable: the list still answers with the Manager's keys and
	// marks their presence unverified.
	harness.cpa.server.Close()
	listed, unverified, err := harness.service.ExternalKeyList(ctx, keys)
	if err != nil {
		t.Fatalf("an unreachable CPA failed the list: %v", err)
	}
	if !unverified || len(listed) != 1 || string(listed[0].ID) != id {
		t.Fatalf("outage list = %d keys, unverified=%v", len(listed), unverified)
	}
	if listed[0].RuntimeState == "effective_observed" {
		t.Fatal("a list read from an unreachable CPA claimed an observed effect")
	}
}

// A credential the user's CPA refuses is the typed blocker on the list: no read can
// settle it, and only a rebind resolves it.
func TestExternalKeyListReportsARejectedCredential(t *testing.T) {
	harness := newExternalHarness(t)
	ctx := context.Background()
	harness.staged(t, strings.Repeat("a", 32))
	keys, err := harness.keys.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	harness.cpa.rejectEvery = true
	if _, _, err = harness.service.ExternalKeyList(ctx, keys); !errors.Is(err, cpa.ErrManagementRejected) {
		t.Fatalf("list on a rejected credential = %v", err)
	}
	// A moved target is the other blocker: the keys are not an effect of the CPA
	// the checkpoint no longer names.
	moved := NewExternal(nil, harness.keys, func(context.Context) (ExternalInstall, error) {
		return ExternalInstall{}, cpa.ErrExternalTargetMoved
	})
	if _, _, err = moved.ExternalKeyList(ctx, keys); !errors.Is(err, cpa.ErrExternalTargetMoved) {
		t.Fatalf("list on a moved target = %v", err)
	}
}

// Apply replaces the CPA's whole list, so it never builds that list from a read the
// user has since edited: a credential they revoked directly on their CPA is not
// added back by the read this Manager took before that edit.
func TestExternalApplyRefusesAConcurrentExternalEdit(t *testing.T) {
	harness := newExternalHarness(t)
	ctx := context.Background()
	present, presentSecret := harness.staged(t, strings.Repeat("1", 32))
	if _, err := harness.service.Apply(ctx, present, 1); err != nil {
		t.Fatal(err)
	}
	staged, stagedSecret := harness.staged(t, strings.Repeat("2", 32))
	reads := 0
	harness.cpa.onKeyRead = func(f *fakeExternalCPA) {
		reads++
		if reads == 2 {
			// The user revokes their credential on their own CPA between the read
			// this write was built from and the read that gates it.
			f.keys = slices.DeleteFunc(f.keys, func(key string) bool { return key == presentSecret })
		}
	}
	harness.cpa.writes = 0
	if _, err := harness.service.Apply(ctx, staged, 1); !errors.Is(err, cpa.ErrSourceConflict) {
		t.Fatalf("apply over an external edit = %v", err)
	}
	if harness.cpa.writes != 0 {
		t.Fatal("a list built from a stale read was written")
	}
	if slices.Contains(harness.cpa.keys, stagedSecret) {
		t.Fatal("the staged key was written over the user's edit")
	}
	if slices.Contains(harness.cpa.keys, presentSecret) {
		t.Fatal("the credential the user revoked was added back")
	}
}

// A journaled operation owns the key set until it settles: Apply refuses instead of
// writing a list the journal's step is about to change.
func TestExternalApplyRefusesWhileAnOperationIsJournaled(t *testing.T) {
	harness := newExternalHarness(t)
	ctx := context.Background()
	target, _ := harness.staged(t, strings.Repeat("3", 32))
	keeper, _ := harness.staged(t, strings.Repeat("4", 32))
	if _, err := harness.service.Apply(ctx, target, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := harness.service.Apply(ctx, keeper, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := harness.service.Prepare(ctx, strings.Repeat("5", 32), target, keeper, "revoke", 1); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	staged, stagedSecret := harness.staged(t, strings.Repeat("6", 32))
	harness.cpa.writes = 0
	if _, err := harness.service.Apply(ctx, staged, 1); !errors.Is(err, freshkeys.ErrConflict) {
		t.Fatalf("apply while an operation is journaled = %v", err)
	}
	if harness.cpa.writes != 0 || slices.Contains(harness.cpa.keys, stagedSecret) {
		t.Fatal("an apply during a journaled operation reached the user's CPA")
	}
}

// A CPA that does not prove the v8 contract is refused for every write — install,
// revoke and replace — with the typed version refusal and not one PUT, while the
// key list still reads it. The version bounds what may be written, never what may
// be shown: an old CPA's keys are still its own answer.
func TestExternalWritesAreRefusedForAnUnsupportedVersion(t *testing.T) {
	harness := newExternalHarness(t)
	ctx := context.Background()
	target, targetSecret := harness.staged(t, strings.Repeat("2", 32))
	keeper, keeperSecret := harness.staged(t, strings.Repeat("3", 32))
	if _, err := harness.service.Apply(ctx, target, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := harness.service.Apply(ctx, keeper, 1); err != nil {
		t.Fatal(err)
	}
	// The user's CPA now advertises a major this Manager never wrote against.
	harness.service.externalProbe = func(context.Context, string, string) (cpa.ExternalVersionReport, error) {
		return cpa.ClassifyExternalVersion("v7.2.130"), nil
	}
	harness.cpa.writes = 0

	staged, stagedSecret := harness.staged(t, strings.Repeat("4", 32))
	if _, err := harness.service.Apply(ctx, staged, 1); !errors.Is(err, cpa.ErrExternalVersionUnsupported) {
		t.Fatalf("install on an unsupported CPA = %v", err)
	}
	if _, err := harness.service.Prepare(ctx, strings.Repeat("5", 32), target, keeper, "revoke", 1); !errors.Is(err, cpa.ErrExternalVersionUnsupported) {
		t.Fatalf("revoke on an unsupported CPA = %v", err)
	}
	replacement, replacementSecret := harness.staged(t, strings.Repeat("6", 32))
	if _, err := harness.service.Prepare(ctx, strings.Repeat("7", 32), keeper, replacement, "replace", 1); !errors.Is(err, cpa.ErrExternalVersionUnsupported) {
		t.Fatalf("replace on an unsupported CPA = %v", err)
	}
	if harness.cpa.writes != 0 {
		t.Fatalf("a refused write reached the user's CPA: %d writes", harness.cpa.writes)
	}
	if slices.Contains(harness.cpa.keys, stagedSecret) || slices.Contains(harness.cpa.keys, replacementSecret) {
		t.Fatal("a refused write mutated the user's key list")
	}
	if !slices.Contains(harness.cpa.keys, targetSecret) || !slices.Contains(harness.cpa.keys, keeperSecret) {
		t.Fatal("a refused write disturbed the credentials the CPA carries")
	}

	// Reads are untouched: the list still reports the presence the CPA answers.
	keys, err := harness.keys.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	listed, unverified, err := harness.service.ExternalKeyList(ctx, keys)
	if err != nil || unverified {
		t.Fatalf("list on an unsupported CPA = %d keys, unverified=%v, %v", len(listed), unverified, err)
	}
	observed := 0
	for _, key := range listed {
		if key.RuntimeState == "effective_observed" {
			observed++
		}
	}
	if observed != 2 {
		t.Fatalf("observed %d of the credentials the CPA carries, want 2", observed)
	}
}

// A CPA whose version cannot be read at all is refused for writes as well: an
// unproven version is not a licence to write, and the refusal names the class it
// actually met.
func TestExternalWritesAreRefusedWithoutAProvenVersion(t *testing.T) {
	harness := newExternalHarness(t)
	ctx := context.Background()
	id, secret := harness.staged(t, strings.Repeat("8", 32))
	for _, version := range []string{"", "latest"} {
		harness.service.externalProbe = func(context.Context, string, string) (cpa.ExternalVersionReport, error) {
			return cpa.ClassifyExternalVersion(version), nil
		}
		harness.cpa.writes = 0
		if _, err := harness.service.Apply(ctx, id, 1); !errors.Is(err, cpa.ErrExternalVersionUnsupported) {
			t.Fatalf("install with version %q = %v", version, err)
		}
		if harness.cpa.writes != 0 || slices.Contains(harness.cpa.keys, secret) {
			t.Fatalf("a write with version %q reached the user's CPA", version)
		}
	}
}

// A refused credential stays the blocker of the probe itself: a CPA that answered
// 401 is a rebind, which is a different refusal from a version this Manager does
// not write.
//
// A user's CPA whose version stops proving the v8 contract between Prepare and
// Continue sends no write at all. The gate runs before the step is journaled as
// attempted, so the step stays unattempted and the journal does not answer
// ErrApplyUnknown forever: the later operation on the same connection is accepted
// instead of being refused by a step that could never be settled.
func TestExternalVersionGateRunsBeforeTheStepIsAttempted(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		probe  func() (cpa.ExternalVersionReport, error)
		causes []error
	}{
		{
			name:   "version became unsupported",
			probe:  func() (cpa.ExternalVersionReport, error) { return cpa.ClassifyExternalVersion("v7.2.130"), nil },
			causes: []error{cpa.ErrExternalVersionUnsupported},
		},
		{
			name:   "probe timed out",
			probe:  func() (cpa.ExternalVersionReport, error) { return cpa.ExternalVersionReport{}, cpa.ErrProofUnavailable },
			causes: []error{cpa.ErrProofUnavailable},
		},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			harness := newExternalHarness(t)
			ctx := context.Background()
			target, targetSecret := harness.staged(t, strings.Repeat("2", 32))
			keeper, keeperSecret := harness.staged(t, strings.Repeat("3", 32))
			if _, err := harness.service.Apply(ctx, target, 1); err != nil {
				t.Fatal(err)
			}
			if _, err := harness.service.Apply(ctx, keeper, 1); err != nil {
				t.Fatal(err)
			}
			result, err := harness.service.Prepare(ctx, strings.Repeat("4", 32), target, keeper, "revoke", 1)
			if err != nil {
				t.Fatalf("prepare: %v", err)
			}
			// The CPA was upgraded — or stopped answering — after the intent was
			// journaled, so the probe answers differently from here on.
			harness.service.externalProbe = func(context.Context, string, string) (cpa.ExternalVersionReport, error) {
				return scenario.probe()
			}
			writes := harness.cpa.writes
			_, err = harness.service.ContinueOperation(ctx, result.Operation.ID, 1)
			for _, cause := range scenario.causes {
				if errors.Is(err, cause) {
					err = nil
					break
				}
			}
			if err != nil {
				t.Fatalf("continue behind the version gate = %v", err)
			}
			if harness.cpa.writes != writes {
				t.Fatal("a write was sent for a step the version gate refused")
			}
			if !slices.Contains(harness.cpa.keys, targetSecret) || !slices.Contains(harness.cpa.keys, keeperSecret) {
				t.Fatalf("a refused step changed the CPA's credentials: %v", harness.cpa.keys)
			}
			journal, err := harness.service.repo.Operation(ctx, result.Operation.ID)
			if err != nil {
				t.Fatal(err)
			}
			if journal.Outcome != "pending" || journal.Steps[0].State != "unattempted" {
				t.Fatalf("a refused step was journaled as %q with %q", journal.Outcome, journal.Steps[0].State)
			}
			// Nothing was sent, so no read-back can ever settle this step: the
			// journal must not keep reporting an unknown effect. The CPA serves
			// the contract again, and the same journaled step is taken once.
			harness.service.externalProbe = supportedExternalVersion
			result, err = harness.service.ContinueOperation(ctx, result.Operation.ID, 1)
			if err != nil {
				t.Fatalf("continue once the CPA serves the contract again: %v", err)
			}
			if result.Operation == nil || result.Operation.Outcome != "committed" {
				t.Fatalf("the step refused by the gate never settled: %+v", result.Operation)
			}
			if slices.Contains(harness.cpa.keys, targetSecret) {
				t.Fatal("the credential the settled step removes is still carried")
			}
			// The journal is terminal, so an unrelated key operation on the same
			// connection is accepted instead of being refused by a stuck journal.
			next, nextSecret := harness.staged(t, strings.Repeat("5", 32))
			if _, err := harness.service.Apply(ctx, next, 1); err != nil {
				t.Fatalf("a later operation on the same connection = %v", err)
			}
			if !slices.Contains(harness.cpa.keys, nextSecret) {
				t.Fatal("the later operation did not reach the user's CPA")
			}
		})
	}
}

// A probe that answers 401 in front of the write is the credential blocker on
// Prepare and on every later step: it is reported as such, never as a version
// class, and never followed by a write.
func TestExternalWriteGateKeepsTheProbeBlocker(t *testing.T) {
	harness := newExternalHarness(t)
	ctx := context.Background()
	id, secret := harness.staged(t, strings.Repeat("9", 32))
	harness.service.externalProbe = func(context.Context, string, string) (cpa.ExternalVersionReport, error) {
		return cpa.ExternalVersionReport{}, cpa.ErrManagementRejected
	}
	harness.cpa.writes = 0
	_, err := harness.service.Apply(ctx, id, 1)
	if !errors.Is(err, cpa.ErrManagementRejected) {
		t.Fatalf("install on a rejected probe = %v", err)
	}
	if errors.Is(err, cpa.ErrExternalVersionUnsupported) {
		t.Fatal("a rejected credential was reported as an unsupported version")
	}
	if harness.cpa.writes != 0 || slices.Contains(harness.cpa.keys, secret) {
		t.Fatal("a write behind a rejected probe reached the user's CPA")
	}
}

// A journaled External step asks the version probe exactly once: the gate in front
// of the journaled intent and the gate in front of the single PUT are the same
// ask, and the wire itself carries none. The earlier double ask charged two round
// trips per step and, worse, could refuse inside the write after the journal had
// already recorded it as attempted.
func TestExternalStepAsksTheVersionProbeOnce(t *testing.T) {
	harness := newExternalHarness(t)
	ctx := context.Background()
	probes := 0
	harness.service.externalProbe = func(context.Context, string, string) (cpa.ExternalVersionReport, error) {
		probes++
		return cpa.ClassifyExternalVersion("v8.0.13"), nil
	}
	target, targetSecret := harness.staged(t, strings.Repeat("2", 32))
	keeper, _ := harness.staged(t, strings.Repeat("3", 32))
	if _, err := harness.service.Apply(ctx, target, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := harness.service.Apply(ctx, keeper, 1); err != nil {
		t.Fatal(err)
	}
	if probes != 2 {
		t.Fatalf("two installs asked the probe %d times, want one each", probes)
	}

	prepared, err := harness.service.Prepare(ctx, strings.Repeat("4", 32), target, keeper, "revoke", 1)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if probes != 3 {
		t.Fatalf("prepare asked the probe %d times in total, want one more", probes)
	}
	writes := harness.cpa.writes
	probes = 0
	result, err := harness.service.ContinueOperation(ctx, prepared.Operation.ID, 1)
	if err != nil {
		t.Fatalf("continue: %v", err)
	}
	if probes != 1 {
		t.Fatalf("one step asked the probe %d times, want exactly one", probes)
	}
	if harness.cpa.writes != writes+1 {
		t.Fatalf("writes = %d, want exactly one PUT for the step", harness.cpa.writes-writes)
	}
	if result.Operation == nil || result.Operation.Outcome != "committed" {
		t.Fatalf("the step did not settle: %+v", result.Operation)
	}
	if slices.Contains(harness.cpa.keys, targetSecret) {
		t.Fatal("the credential the step removes is still carried")
	}
}

// TestExternalApplyRefusesAnUnsupportedVersionBeforeItsPut proves the install
// write carries its own gate. The wire has none, so a missing gate here would be
// a write straight to a CPA that does not serve the v8 contract.
func TestExternalApplyRefusesAnUnsupportedVersionBeforeItsPut(t *testing.T) {
	harness := newExternalHarness(t)
	ctx := context.Background()
	target, _ := harness.staged(t, strings.Repeat("2", 32))
	harness.service.externalProbe = func(context.Context, string, string) (cpa.ExternalVersionReport, error) {
		return cpa.ClassifyExternalVersion("v7.2.130"), nil
	}
	if _, err := harness.service.Apply(ctx, target, 1); !errors.Is(err, cpa.ErrExternalVersionUnsupported) {
		t.Fatalf("apply on an unsupported version = %v, want the typed refusal", err)
	}
	if harness.cpa.writes != 0 {
		t.Fatalf("apply wrote to a CPA that does not serve the contract: %d writes", harness.cpa.writes)
	}
	if len(harness.cpa.keys) != 0 {
		t.Fatalf("the CPA's credentials changed: %v", harness.cpa.keys)
	}
}
