package privateconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const Artifact = "sha256:e48c85c75a19adae186cbee590f6e6143fd8085417d8066b545e8533877175f7"
const Path = "/v1/runtime/private-configuration"
const GrantPath = "/run/cpamp-config/source.json"

// Grant is Host input, not proof by itself. The launcher must maintain the
// isolated namespace, exclusive volume and credential/mount boundaries for its
// entire lease. No container may receive a Docker socket or a writable alias.
type Grant struct {
	BootstrapRun      string   `json:"bootstrapRun,omitempty"`
	H1                *H1Grant `json:"h1,omitempty"`
	SourceID          string   `json:"sourceId"`
	Generation        string   `json:"generation"`
	Namespace         string   `json:"namespace"`
	Device            uint64   `json:"device"`
	Inode             uint64   `json:"inode"`
	SeedSHA256        string   `json:"seedSHA256"`
	IssuedAtMS        int64    `json:"issuedAtMs"`
	ExpiresAtMS       int64    `json:"expiresAtMs"`
	RecoveryOperation string   `json:"recoveryOperation,omitempty"`
	RecoverySHA256    string   `json:"recoverySHA256,omitempty"`
}
type Operation struct {
	KeeperSHA256        string `json:"keeperSHA256,omitempty"`
	ID                  string `json:"id"`
	Scope               string `json:"scope"`
	KeySHA256           string `json:"keySHA256,omitempty"`
	Retry               int    `json:"retry"`
	MaxRetryInterval    int    `json:"maxRetryInterval,omitempty"`
	MaxRetryIntervalSet bool   `json:"maxRetryIntervalSet,omitempty"`
	Generation          string `json:"generation"`
	State               string `json:"state"`
	// Config-field reservation. Appended so a pre-allowlist record keeps its exact
	// JSON and therefore its exact transition digests.
	Path  string `json:"path,omitempty"`
	Value any    `json:"value,omitempty"`
}
type record struct {
	BootstrapRun              string                     `json:"bootstrapRun,omitempty"`
	H1                        *H1Record                  `json:"h1,omitempty"`
	Version                   int                        `json:"version"`
	SourceID                  string                     `json:"sourceId"`
	SeedSHA256                string                     `json:"seedSHA256"`
	Device                    uint64                     `json:"device"`
	Inode                     uint64                     `json:"inode"`
	Revision                  uint64                     `json:"revision"`
	Installed                 bool                       `json:"installed"`
	CommittedRetry            *int                       `json:"committedRetry"`
	CommittedMaxRetryInterval *int                       `json:"committedMaxRetryInterval,omitempty"`
	CommittedFields           map[string]json.RawMessage `json:"committedFields,omitempty"`
	LastGood                  []byte                     `json:"lastGood"`
	LastGoodSHA256            string                     `json:"lastGoodSHA256"`
	Operation                 Operation                  `json:"operation"`
}
type Request struct {
	H1               *H1Request `json:"h1,omitempty"`
	Action           string     `json:"action"`
	SourceID         string     `json:"sourceId"`
	Generation       string     `json:"generation"`
	ExpectedRevision uint64     `json:"expectedRevision"`
	ExpectedSHA256   string     `json:"expectedSHA256"`
	OperationID      string     `json:"operationId"`
	Scope            string     `json:"scope"`
	KeySHA256        string     `json:"keySHA256,omitempty"`
	Path             string     `json:"path,omitempty"`
	Value            any        `json:"value,omitempty"`
}

// Result contains no raw configuration, Key or recovery material.
type Result struct {
	H1              *H1Record                  `json:"h1,omitempty"`
	SourceID        string                     `json:"sourceId"`
	Generation      string                     `json:"generation"`
	Revision        uint64                     `json:"revision"`
	SHA256          string                     `json:"sha256"`
	State           string                     `json:"state"`
	Installed       bool                       `json:"installed"`
	CommittedFields map[string]json.RawMessage `json:"committedFields"`
	Operation       Operation                  `json:"operation"`
	Created         bool                       `json:"created"`
}
type Authority struct {
	mu                             sync.Mutex
	root, config, grant, namespace string
	generation                     string
	childUID                       uint32
}

func New(root, config, grant, namespace string, generation uint64, uid uint32) *Authority {
	return &Authority{root: root, config: config, grant: grant, namespace: namespace, generation: strconv.FormatUint(generation, 10), childUID: uid}
}
func (a *Authority) loadGrant() (Grant, error) {
	var g Grant
	b, e := readPrivate(a.grant, os.Geteuid(), 0600, 8192)
	if e != nil || strict(b, &g) != nil {
		return g, ErrStop
	}
	now := time.Now().UnixMilli()
	if len(g.SourceID) < 16 || len(g.SourceID) > 128 || g.Generation != a.generation || !hashValid(g.SeedSHA256) ||
		g.IssuedAtMS <= 0 || g.IssuedAtMS > now || g.ExpiresAtMS <= now || g.ExpiresAtMS-g.IssuedAtMS > 600000 {
		return g, ErrStop
	}
	if g.BootstrapRun != "" && (len(g.BootstrapRun) < 16 || len(g.BootstrapRun) > 128) {
		return g, ErrStop
	}
	ns, e := os.Readlink(a.namespace)
	if e != nil || ns != g.Namespace {
		return g, ErrStop
	}
	if e := checkParents(a.config); e != nil {
		return g, e
	}
	info, e := os.Lstat(a.config)
	if e != nil || !regular(info, int(a.childUID), 0600) {
		return g, ErrStop
	}
	dev, ino, links := fileID(info)
	if dev != g.Device || ino != g.Inode || links != 1 {
		return g, ErrStop
	}
	return g, nil
}
func strict(b []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if d.Decode(v) != nil || d.Decode(new(any)) != io.EOF {
		return ErrStop
	}
	return nil
}
func (a *Authority) load(g Grant) (*record, error) {
	if _, e := os.Lstat(a.root); errors.Is(e, os.ErrNotExist) {
		return nil, nil
	}
	info, e := os.Lstat(a.root)
	if e != nil || !info.IsDir() || info.Mode().Perm() != 0700 || owner(info) != os.Geteuid() {
		return nil, ErrStop
	}
	if checkParents(filepath.Join(a.root, "record.json")) != nil {
		return nil, ErrStop
	}
	b, e := readPrivate(filepath.Join(a.root, "record.json"), os.Geteuid(), 0600, 4<<20)
	var r record
	if e != nil || strict(b, &r) != nil || (r.Version != 1 && r.Version != 2) || r.Revision == 0 || r.Revision > 1<<52 ||
		r.SourceID != g.SourceID || r.SeedSHA256 != g.SeedSHA256 || r.Device != g.Device || r.Inode != g.Inode ||
		len(r.LastGood) == 0 || digest(r.LastGood) != r.LastGoodSHA256 {
		return nil, ErrStop
	}
	if _, e := tree(r.LastGood); e != nil {
		return nil, e
	}
	if (r.Version == 2 && !validH1Record(&r)) || (r.Version == 1 && r.H1 != nil) {
		return nil, ErrStop
	}
	// A pre-allowlist committed scalar becomes an ordinary committed field before
	// any check runs, so LastGood validation and the result surface never see a
	// stale legacy copy next to the field that replaced it.
	migrateCommittedFields(&r)
	if len(r.CommittedFields) > 0 {
		node, e := tree(r.LastGood)
		if e != nil {
			return nil, e
		}
		for path, raw := range r.CommittedFields {
			var value any
			if json.Unmarshal(raw, &value) != nil || !validFieldValue(path, value) {
				return nil, ErrStop
			}
			tag, text, _ := fieldLeaf(path, value)
			v := leaf(node, strings.Split(path, ".")...)
			if !r.Installed || v == nil || v.Tag != tag || v.Value != text {
				return nil, ErrStop
			}
		}
	}
	switch r.Operation.State {
	case "":
		if r.Operation != (Operation{}) || r.Installed || r.Revision != 1 {
			return nil, ErrStop
		}
	case "unknown", "persisted", "recovered":
		if !validOperation(&r) {
			return nil, ErrStop
		}
	default:
		return nil, ErrStop
	}
	return &r, nil
}
func (a *Authority) current() ([]byte, error) {
	return readPrivate(a.config, int(a.childUID), 0600, 1<<20)
}

// CheckStartup is called at every CPA spawn, including automatic recovery.
// A fresh Supervisor never infers admission from an existing YAML document.
func (a *Authority) CheckStartup() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	g, e := a.loadGrant()
	if e != nil {
		return e
	}
	r, e := a.load(g)
	if e != nil || r == nil || r.Operation.State == "unknown" || (r.H1 != nil && (r.H1.Parent != nil || r.H1.Generation != a.generation || g.H1 == nil || g.H1.Action != "start")) {
		return ErrStop
	}
	b, e := a.current()
	if e != nil || digest(b) != r.LastGoodSHA256 {
		return ErrStop
	}
	if r.H1 != nil && (g.H1.RunID != r.H1.RunID || g.H1.Revision != r.Revision || g.H1.SHA256 != r.LastGoodSHA256) {
		return ErrStop
	}
	return nil
}

// Do is serialized with the lifecycle executor. running is the exact owned
// child observation; unknown process state must be rejected by that caller.
func (a *Authority) Do(q Request, running bool) (Result, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	g, e := a.loadGrant()
	if e != nil {
		return Result{}, e
	}
	if q.SourceID != g.SourceID || q.Generation != g.Generation {
		return Result{}, ErrStop
	}
	r, e := a.load(g)
	if e != nil {
		return Result{}, e
	}
	b, e := a.current()
	if e != nil {
		return Result{}, e
	}
	sha := digest(b)
	if q.Action == "admit" {
		if r != nil {
			return a.result(r, sha, false), ErrStop
		}
		if running || sha != g.SeedSHA256 || sha != q.ExpectedSHA256 || g.H1 != nil {
			return Result{}, ErrStop
		}
		if _, e := tree(b); e != nil {
			return Result{}, e
		}
		if e := os.Mkdir(a.root, 0700); e != nil {
			return Result{}, ErrStop
		}
		if syncDirectory(filepath.Dir(a.root)) != nil || a.flushCurrent(g, sha) != nil || syncDirectory(filepath.Dir(a.config)) != nil {
			return Result{}, ErrStop
		}
		r = &record{Version: 1, SourceID: g.SourceID, SeedSHA256: g.SeedSHA256, Device: g.Device, Inode: g.Inode, Revision: 1, LastGood: b, LastGoodSHA256: sha}
		r.BootstrapRun = g.BootstrapRun
		if e := a.save(r); e != nil {
			return Result{}, e
		}
		return a.result(r, sha, false), nil
	}
	if r == nil {
		return Result{}, ErrStop
	}
	if q.Action == "observe" {
		return a.result(r, sha, false), nil
	}
	if r.Version == 2 || q.H1 != nil || g.H1 != nil {
		return a.doH1(q, g, r, b, running)
	}
	if q.Action == "begin" {
		if !running || r.Operation.State == "unknown" || q.ExpectedRevision != r.Revision || sha != r.LastGoodSHA256 ||
			sha != q.ExpectedSHA256 || q.OperationID == "" || len(q.OperationID) > 128 || q.OperationID == r.Operation.ID {
			return Result{}, ErrStop
		}
		if q.Scope == "initial-key" {
			if r.Installed || !hashValid(q.KeySHA256) || q.Path != "" || q.Value != nil {
				return Result{}, ErrStop
			}
			r.Operation = Operation{ID: q.OperationID, Scope: q.Scope, KeySHA256: q.KeySHA256, Generation: g.Generation, State: "unknown"}
		} else {
			// One allowlisted field is reserved at a time; the path, its type and its
			// bounds are re-validated here, never trusted from the Manager.
			if !r.Installed || q.KeySHA256 != "" || q.Path != q.Scope || !validFieldValue(q.Scope, q.Value) {
				return Result{}, ErrStop
			}
			r.Operation = Operation{ID: q.OperationID, Scope: q.Scope, Path: q.Path, Value: normalizeFieldValue(q.Value), Generation: g.Generation, State: "unknown"}
		}
		// LastGood is already durable; persist intention before any CPA bytes.
		r.Revision++
		if e := a.save(r); e != nil {
			return Result{}, e
		}
		return a.result(r, sha, true), nil
	}
	if q.OperationID != r.Operation.ID || q.ExpectedRevision != r.Revision ||
		(r.Operation.State != "unknown" && (q.Action != "recover" || r.Operation.ID == "" || sha == r.LastGoodSHA256)) {
		return Result{}, ErrStop
	}
	switch q.Action {
	case "commit":
		// After a Supervisor crash the new generation cannot start an unknown
		// file. The Host may explicitly reconcile the original operation while
		// stopped, binding its exact current fingerprint; this never repeats PUT.
		live := running && r.Operation.Generation == g.Generation
		stopped := !running && g.RecoveryOperation == r.Operation.ID && g.RecoverySHA256 == sha
		if (!live && !stopped) || q.ExpectedSHA256 != sha || !validChange(r.LastGood, b, r.Operation) {
			return Result{}, ErrStop
		}
		// Readback alone is not a durability receipt. Flush the same admitted inode
		// before recording persisted; a failed flush leaves the operation unknown.
		if e := a.flushCurrent(g, sha); e != nil {
			return Result{}, e
		}
		r.Installed = true
		if path, value, ok := operationPath(r.Operation); ok {
			raw, e := json.Marshal(value)
			if e != nil {
				return Result{}, ErrStop
			}
			if r.CommittedFields == nil {
				r.CommittedFields = map[string]json.RawMessage{}
			}
			r.CommittedFields[path] = raw
		}
		r.LastGood = b
		r.LastGoodSHA256 = sha
		r.Operation.State = "persisted"
	case "recover":
		// Host must stop/reap the CPA writer, renew the exclusive lease and name
		// BOTH the original operation and currently damaged file fingerprint.
		if running || g.RecoveryOperation != r.Operation.ID || g.RecoverySHA256 != sha || q.ExpectedSHA256 != sha {
			return Result{}, ErrStop
		}
		// Corruption may be discovered between operations. Make the recovery
		// intention durable before touching even this exclusively owned file.
		// A crash during restore must retain the same original last-good anchor.
		if r.Operation.State != "unknown" {
			r.Operation.State = "unknown"
			r.Revision++
			if e := a.save(r); e != nil {
				return Result{}, e
			}
		}
		if e := a.restore(g, r.LastGood, sha); e != nil {
			return Result{}, e
		}
		sha = r.LastGoodSHA256
		r.Operation.State = "recovered"
	default:
		return Result{}, ErrStop
	}
	r.Revision++
	if e := a.save(r); e != nil {
		return Result{}, e
	}
	return a.result(r, sha, false), nil
}
func (a *Authority) result(r *record, sha string, created bool) Result {
	state := "persisted"
	if sha != r.LastGoodSHA256 || r.Operation.State == "unknown" {
		state = "unknown"
	}
	op := r.Operation
	if path, value, ok := operationPath(op); ok {
		op.Path, op.Value = path, value
	}
	return Result{H1: r.H1, SourceID: r.SourceID, Generation: a.generation, Revision: r.Revision, SHA256: sha, State: state, Installed: r.Installed, CommittedFields: committedFieldValues(r), Operation: op, Created: created}
}
