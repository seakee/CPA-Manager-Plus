package maintenancebundle

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/processlock"
)

// After rename, an error is NOT a failed/rolled-back publication. The complete
// target may exist. Only explicit recovery may reconcile it; never delete it.
var ErrPublicationUncertain = errors.New("publication outcome requires recovery")

const operationPrefix = ".manager-bundle-operation-"
const stagingPrefix = ".manager-bundle-unpublished-"
const publicationStateName = "state.json"
const publicationLockName = publicationStateName + ".manager.lock"

type publicationRecord struct {
	Version     int       `json:"version"`
	Destination string    `json:"destination"` // basename only; parent is owner-controlled
	ID          string    `json:"id"`
	State       string    `json:"state"`
	Manifest    *Manifest `json:"manifest,omitempty"`
}

type publication struct {
	dest, root string
	lock       *processlock.Lock
	record     publicationRecord
	hooks      createHooks
	renamed    bool
	committed  bool
}

func (h createHooks) checkpoint(phase string) {
	if h.at != nil {
		h.at(phase)
	}
}

func unpublishedPath(p string) bool {
	for current := filepath.Clean(p); ; current = filepath.Dir(current) {
		name := strings.ToLower(filepath.Base(current))
		for _, prefix := range []string{operationPrefix, stagingPrefix, ".manager-restore-unpublished-", ".manager-recovery-unpublished-"} {
			if strings.HasPrefix(name, prefix) {
				return true
			}
		}
		if current == filepath.Dir(current) {
			return false
		}
	}
}

func operationRoot(dest string) string {
	// Namespace by the exact canonical destination. Case aliases are outside the
	// concurrent-publisher contract; existing targets are independently refused.
	digest := sha256.Sum256([]byte(dest))
	return filepath.Join(filepath.Dir(dest), operationPrefix+hex.EncodeToString(digest[:]))
}

func openPublication(dest string, h createHooks) (*publication, error) {
	root := operationRoot(dest)
	if err := safePath(root); err != nil {
		return nil, err
	}
	if err := os.Mkdir(root, 0o700); err != nil && !os.IsExist(err) {
		return nil, err
	}
	if err := syncDirectory(filepath.Dir(root)); err != nil {
		return nil, err
	}
	lock, err := processlock.Acquire(filepath.Join(root, publicationStateName))
	if err != nil {
		return nil, err
	}
	return &publication{dest: dest, root: root, lock: lock, hooks: h}, nil
}

func beginPublication(ctx context.Context, dest string, source Layout, h createHooks) (*publication, error) {
	// The journal must not be placed in an input asset either.
	root := operationRoot(dest)
	for _, input := range source.paths() {
		physical, err := physicalOverlap(input, root)
		if err != nil {
			return nil, err
		}
		if overlaps(input, root) || overlaps(root, input) || physical {
			return nil, errors.New("publication journal overlaps source")
		}
	}
	p, err := openPublication(dest, h)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*publication, error) { return nil, errors.Join(err, p.lock.Close()) }
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	err = p.readRecord()
	if err == nil {
		if err := p.validateArtifacts(); err != nil {
			return fail(err)
		}
		if p.record.State == "committed" {
			return fail(errors.New("committed publication target is missing; recovery required"))
		}
		if err := absent(dest); err != nil {
			return fail(err)
		}
		if err := p.abort(); err != nil {
			return fail(err)
		}
		if err := p.validateArtifacts(); err != nil {
			return fail(err)
		}
	} else if !os.IsNotExist(err) {
		return fail(err)
	} else {
		// No record means no authority to remove any unknown entry.
		entries, err := os.ReadDir(p.root)
		if err != nil {
			return fail(err)
		}
		for _, entry := range entries {
			if entry.Name() != publicationLockName {
				return fail(errors.New("unrecorded publication artifacts require manual inspection"))
			}
		}
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return fail(err)
	}
	p.record = publicationRecord{Version: 1, Destination: filepath.Base(dest), ID: hex.EncodeToString(id[:]), State: "preparing"}
	// Persist the exact staging identity BEFORE creating that directory. Restart
	// cleanup therefore never guesses ownership from age or a filename pattern.
	if err := p.writeRecord(); err != nil {
		return fail(err)
	}
	if err := os.Mkdir(p.staging(), 0o700); err != nil {
		return fail(err)
	}
	return p, nil
}

func (p *publication) staging() string { return filepath.Join(p.root, stagingPrefix+p.record.ID) }

// validateArtifacts keeps restart/retry fail-closed. A recorded operation may
// own only its exact staging directory; any other entry could be an interrupted
// replacement or an unrelated writer's artifact and must remain for inspection.
func (p *publication) validateArtifacts() error {
	return p.validateArtifactsWithNext(false)
}

func (p *publication) validateArtifactsWithNext(allowNext bool) error {
	entries, err := os.ReadDir(p.root)
	if err != nil {
		return err
	}
	stagingName := filepath.Base(p.staging())
	for _, entry := range entries {
		if allowNext && entry.Name() == "state.next" {
			continue
		}
		switch entry.Name() {
		case publicationStateName, publicationLockName:
			continue
		case stagingName:
			if p.record.State == "preparing" || p.record.State == "aborted" {
				continue
			}
		}
		return errors.New("unrecorded publication artifacts require manual inspection")
	}
	return nil
}

func validDigest(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && strings.ToLower(value) == value
}

func (p *publication) readRecord() error {
	err := p.readRecordFile(publicationStateName)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	previous, initial := p.record, os.IsNotExist(err)
	next := &publication{dest: p.dest, root: p.root}
	if nextErr := next.readRecordFile("state.next"); os.IsNotExist(nextErr) {
		return err
	} else if nextErr != nil {
		return nextErr // Partial/foreign journals are preserved for inspection.
	}
	if !validRecordTransition(previous, next.record, initial) {
		return errors.New("invalid interrupted publication transition")
	}
	if err := next.validateArtifactsWithNext(true); err != nil {
		return err
	}
	// Initial intent precedes staging creation; abort precedes retry intent.
	// A committed intent can only follow a rename that consumed staging.
	if initial || next.record.State != "preparing" || previous.ID != next.record.ID {
		if err := absent(next.staging()); err != nil {
			return errors.New("interrupted journal has unexpected staging")
		}
	}
	if initial || next.record.State == "aborted" || previous.ID != next.record.ID {
		if err := absent(p.dest); err != nil {
			return errors.New("interrupted journal has unexpected target")
		}
	}
	if err := replaceState(filepath.Join(p.root, "state.next"), filepath.Join(p.root, publicationStateName)); err != nil {
		return err
	}
	if err := syncDirectory(p.root); err != nil {
		return err
	}
	p.record = next.record
	return nil
}

func validRecordTransition(old, next publicationRecord, initial bool) bool {
	if initial {
		return next.State == "preparing" && next.Manifest == nil
	}
	if old.ID != next.ID {
		return old.State == "aborted" && next.State == "preparing" && next.Manifest == nil
	}
	if reflect.DeepEqual(old, next) {
		return true // Idempotent recovery/abort journal writes.
	}
	if old.State != "preparing" {
		return false
	}
	switch next.State {
	case "aborted":
		return next.Manifest == nil
	case "preparing":
		return old.Manifest == nil && next.Manifest != nil
	case "committed":
		return old.Manifest != nil && reflect.DeepEqual(old.Manifest, next.Manifest)
	}
	return false
}

func (p *publication) readRecordFile(base string) error {
	name := filepath.Join(p.root, base)
	if err := safePath(name); err != nil {
		return err
	}
	f, err := os.Open(name)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > maxManifestBytes+4096 {
		return errors.New("invalid publication journal file")
	}
	if err := rejectMultipleLinks(f); err != nil {
		return err
	}
	decoder := json.NewDecoder(io.LimitReader(f, maxManifestBytes+4097))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&p.record); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("publication journal has trailing data")
	}
	r := p.record
	id, err := hex.DecodeString(r.ID)
	if r.Version != 1 || r.Destination != filepath.Base(p.dest) || err != nil || len(id) != 16 || r.ID != strings.ToLower(r.ID) ||
		(r.State != "preparing" && r.State != "committed" && r.State != "aborted") {
		return errors.New("invalid publication journal contract")
	}
	if (r.State == "aborted" && r.Manifest != nil) || (r.State == "committed" && r.Manifest == nil) {
		return errors.New("invalid publication journal manifest state")
	}
	return nil
}

func (p *publication) writeRecord() error {
	data, err := json.Marshal(p.record)
	if err != nil {
		return err
	}
	if len(data) > maxManifestBytes+4096 {
		return errors.New("publication journal exceeds bound")
	}
	next := filepath.Join(p.root, "state.next")
	// Never overwrite an unreconciled or unknown journal temporary.
	if err := safePath(next); err != nil {
		return err
	}
	if err := absent(next); err != nil {
		return err
	}
	if err := writeFile(next, data); err != nil {
		return err
	}
	phase := p.record.State
	if phase == "preparing" {
		if p.record.Manifest != nil {
			phase = "manifest"
		} else if _, err := os.Lstat(filepath.Join(p.root, publicationStateName)); os.IsNotExist(err) {
			phase = "initial"
		}
	}
	p.hooks.checkpoint("journal-" + phase + "-next-synced")
	if err := replaceState(next, filepath.Join(p.root, "state.json")); err != nil {
		return err
	}
	p.hooks.checkpoint("journal-" + phase + "-replaced")
	return syncDirectory(p.root)
}

func writeManifest(root string, m Manifest) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if len(data)+1 > maxManifestBytes {
		return errors.New("Manager bundle manifest exceeds supported bound")
	}
	return writeFile(filepath.Join(root, "manifest.json"), append(data, '\n'))
}

func (p *publication) syncDir(path string) error {
	if p.hooks.syncDirectory != nil {
		return p.hooks.syncDirectory(path)
	}
	return syncDirectory(path)
}

func syncTree(ctx context.Context, root string, syncDir func(string) error) error {
	var dirs []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := safePath(path); err != nil {
			return err
		}
		if entry.IsDir() {
			dirs = append(dirs, path)
			return nil
		}
		if !entry.Type().IsRegular() {
			return errors.New("nonregular publication file")
		}
		f, err := os.OpenFile(path, os.O_RDWR, 0)
		if err != nil {
			return err
		}
		err = f.Sync()
		return errors.Join(err, f.Close())
	})
	if err != nil {
		return err
	}
	sort.Slice(dirs, func(i, j int) bool { return len(dirs[i]) > len(dirs[j]) })
	for _, dir := range dirs {
		if err := syncDir(dir); err != nil {
			return err
		}
	}
	return nil
}

func (p *publication) publish(ctx context.Context, m Manifest, finalCheck func() error) error {
	p.record.Manifest = &m
	if err := p.writeRecord(); err != nil {
		return err
	}
	if err := syncTree(ctx, p.staging(), p.syncDir); err != nil {
		return err
	}
	p.hooks.checkpoint("before-publication")
	staged, err := verify(ctx, p.staging(), true)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(staged, m) {
		return errors.New("staged manifest changed before publication")
	}
	if err := finalCheck(); err != nil {
		return err
	}
	if err := p.lock.Validate(); err != nil {
		return err
	}
	if err := absent(p.dest); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := publishDirectory(p.staging(), p.dest); err != nil {
		return err
	}
	p.renamed = true
	p.hooks.checkpoint("renamed")
	// Cancellation after rename cannot undo the commit point. Complete durable
	// metadata and return committed, or return the explicit uncertain outcome.
	if err := p.syncDir(p.root); err != nil {
		return errors.Join(ErrPublicationUncertain, err)
	}
	if err := p.syncDir(filepath.Dir(p.dest)); err != nil {
		return errors.Join(ErrPublicationUncertain, err)
	}
	p.hooks.checkpoint("publication-synced")
	p.record.State = "committed"
	if err := p.writeRecord(); err != nil {
		return errors.Join(ErrPublicationUncertain, err)
	}
	p.committed = true
	p.hooks.checkpoint("committed")
	return nil
}

func (p *publication) abort() error {
	stage := p.staging()
	if err := safePath(stage); err != nil {
		return err
	}
	if err := os.RemoveAll(stage); err != nil {
		return err
	}
	p.record.State = "aborted"
	p.record.Manifest = nil
	return p.writeRecord()
}

func (p *publication) finish() error {
	var err error
	if !p.renamed && !p.committed {
		err = p.abort()
	}
	closeErr := p.lock.Close()
	if closeErr != nil && p.renamed {
		closeErr = errors.Join(ErrPublicationUncertain, closeErr)
	}
	return errors.Join(err, closeErr)
}

// RecoverPublication reconciles only the recorded operation for this exact
// destination. It never removes or replaces a destination, adopts an untracked
// directory, scans sibling operations, or grants writer/upgrade authority.
func RecoverPublication(ctx context.Context, destination string) (string, error) {
	return RecoverPublicationWithScratch(ctx, destination, "")
}

// RecoverPublicationWithScratch resolves an unfinished publication using
// scratchDir (or os.TempDir when empty) for transient SQLite validation copies.
func RecoverPublicationWithScratch(ctx context.Context, destination, scratchDir string) (string, error) {
	dest, err := filepath.Abs(destination)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(destination) == "" || unpublishedPath(dest) {
		return "", errors.New("explicit nonreserved destination required")
	}
	if err := safePath(dest); err != nil {
		return "", err
	}
	if err := safePath(operationRoot(dest)); err != nil {
		return "", err
	}
	// Recovery does not create journals for unknown destinations.
	if _, err := os.Lstat(operationRoot(dest)); err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	p, err := openPublication(dest, createHooks{})
	if err != nil {
		return "", err
	}
	defer p.lock.Close()
	if err := p.readRecord(); err != nil {
		return "", err
	}
	if err := p.validateArtifacts(); err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := absent(dest); err == nil {
		if p.record.State == "committed" {
			return "", errors.New("committed target is missing")
		}
		if err := p.abort(); err != nil {
			return "", err
		}
		return "aborted", nil
	} else if _, err := os.Lstat(dest); err != nil {
		return "", err
	}
	if p.record.State == "aborted" || p.record.Manifest == nil {
		return "", errors.New("unexpected target; publication recovery refused")
	}
	if err := absent(p.staging()); err != nil {
		return "", errors.New("target and recorded staging coexist; publication recovery refused")
	}
	m, err := Verify(ctx, dest)
	if err != nil {
		return "", fmt.Errorf("published target is invalid: %w", err)
	}
	if !reflect.DeepEqual(m, *p.record.Manifest) {
		return "", errors.New("target does not match recorded publication")
	}
	if err := validateMatchingEvidence(ctx, BundleLayout(dest), scratchDir, dest, p.root); err != nil {
		return "", err
	}
	if err := syncTree(ctx, dest, syncDirectory); err != nil {
		return "", err
	}
	if err := syncDirectory(p.root); err != nil {
		return "", err
	}
	if err := syncDirectory(filepath.Dir(dest)); err != nil {
		return "", err
	}
	current, err := Verify(ctx, dest)
	if err != nil {
		return "", err
	}
	if !reflect.DeepEqual(current, m) {
		return "", errors.New("publication target changed during recovery")
	}
	p.record.State = "committed"
	if err := p.writeRecord(); err != nil {
		return "", err
	}
	return "committed", nil
}
