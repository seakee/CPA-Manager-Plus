// Package copyinmarker is the durable provenance of an in-flight copy-in.
//
// The copy-in materializes the v2 layout first and migrates the copied store
// second; the operation row that describes that work can only be written after
// the migration commits. A process death between those two commits leaves a v2
// store whose schema says "native-v2" while no row describes it at all. This
// marker closes that window: it is written before the migration starts and
// names the exact backup the target was materialized from, so both the copy-in
// resume and the startup admission can see that the work is unfinished.
//
// It carries provenance only. It is not an authorization, it names no operation
// stage, and it is removed only after the row that supersedes it commits.
package copyinmarker

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Version is the marker contract. An unknown version is refused, never guessed.
const Version = 2

// Phases of an in-flight copy-in. The marker is published before the first
// target member is moved and republished once every member is in place, so a
// resume knows whether an interrupted run may still hold members in its staging
// tree without opening the database it is resuming.
const (
	// PhaseMaterializing means the run may still be moving members into the v2
	// layout. A resume continues the move from its own staging tree.
	PhaseMaterializing = "materializing"
	// PhaseMigrating means every member is in place and the migration may have
	// started rewriting the copied database. A resume adopts the target instead
	// of restoring again.
	PhaseMigrating = "migrating"
)

const (
	// WorkDirectoryName is the reserved scratch area inside the v2 data root. It
	// holds the restored staging tree, the maintenance-bundle publication
	// journal for that staging root, and this marker.
	WorkDirectoryName = ".upgrade-copy-in"
	// FileName is the marker inside the work directory.
	FileName = "copy-in.json"
	// maxMarkerBytes bounds the read: the marker is a fixed, tiny record.
	maxMarkerBytes = 64 * 1024
	tempSuffix     = ".next"
)

// Marker is what the copy-in placed in the v2 layout and where it came from.
// It always names its phase, target, source kind, the source fingerprint the
// copy started from and the backup manifest digest; a marker that cannot do so
// describes nothing and is refused.
//
// Phase says how far the materialization got, and TargetDatabaseIdentity names
// the database file this copy-in moved. Both are what let a resume decide
// whether the target is the store a previous run placed: a relocated path and a
// database swapped in beside the marker are the two cases a name and a SQLite
// header cannot tell apart.
type Marker struct {
	Version                int          `json:"version"`
	Phase                  string       `json:"phase"`
	TargetDatabase         string       `json:"targetDatabase"`
	TargetDatabaseIdentity FileIdentity `json:"targetDatabaseIdentity"`
	SourceKind             string       `json:"sourceKind"`
	SourceVersion          string       `json:"sourceVersion,omitempty"`
	SourceFingerprintBefore string      `json:"sourceFingerprintBefore"`
	SourceFingerprintAfter  string      `json:"sourceFingerprintAfter,omitempty"`
	BackupManifestSHA256    string      `json:"backupManifestSha256"`
	BackupDirectory         string      `json:"backupDirectory,omitempty"`
	WrittenAtMS             int64       `json:"writtenAtMs"`
}

// FileIdentity names one file by the volume and inode the filesystem assigned
// it. A rename keeps both, so it survives a member being moved into the v2
// layout and the migration rewriting that file in place; a different file left
// under the same name has a different identity.
type FileIdentity struct {
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
}

// Valid reports whether the identity names a file.
func (i FileIdentity) Valid() bool { return i.Inode != 0 }

// Identity reads the identity of the file at path.
func Identity(path string) (FileIdentity, error) { return identity(path) }

// Directory is the reserved work directory of a v2 data root.
func Directory(targetRoot string) string {
	return filepath.Join(targetRoot, WorkDirectoryName)
}

// Path is the marker of a v2 data root.
func Path(targetRoot string) string {
	return filepath.Join(Directory(targetRoot), FileName)
}

// Validate rejects a marker that cannot establish provenance for a copy-in.
func (m Marker) Validate() error {
	if m.Version != Version {
		return fmt.Errorf("copy-in marker version %d is not supported", m.Version)
	}
	switch m.Phase {
	case PhaseMaterializing, PhaseMigrating:
	case "":
		return errors.New("copy-in marker must name its phase")
	default:
		return fmt.Errorf("copy-in marker phase %q is not supported", m.Phase)
	}
	if !filepath.IsAbs(m.TargetDatabase) {
		return errors.New("copy-in marker must name an absolute target database")
	}
	if m.Phase == PhaseMigrating {
		// A migrating marker claims the database beside it is the one this
		// copy-in moved. Without that identity the claim is only a file name and
		// a SQLite header, which any v2 store satisfies.
		if !m.TargetDatabaseIdentity.Valid() {
			return errors.New("a migrating copy-in marker must name the database it materialized")
		}
	}
	if m.SourceKind == "" {
		return errors.New("copy-in marker must name the source kind")
	}
	if m.SourceFingerprintBefore == "" {
		return errors.New("copy-in marker must name the source fingerprint before the copy")
	}
	if m.BackupManifestSHA256 == "" {
		return errors.New("copy-in marker must name the backup manifest digest")
	}
	if m.WrittenAtMS < 0 {
		return errors.New("copy-in marker timestamp must not be negative")
	}
	return nil
}

// Write publishes the marker atomically: a reader either sees the previous
// marker or this one, never a partial record. The work directory is created
// when absent.
func Write(targetRoot string, marker Marker) error {
	if err := marker.Validate(); err != nil {
		return err
	}
	data, err := json.Marshal(marker)
	if err != nil {
		return err
	}
	directory := Directory(targetRoot)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	next := Path(targetRoot) + tempSuffix
	if err := os.Remove(next); err != nil && !os.IsNotExist(err) {
		return err
	}
	file, err := os.OpenFile(next, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	writeErr := writeAll(file, data)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		_ = os.Remove(next)
		return err
	}
	if err := replaceFile(next, Path(targetRoot)); err != nil {
		_ = os.Remove(next)
		return err
	}
	return syncDirectory(directory)
}

// Read returns the marker of a v2 data root. found is false when no copy-in is
// in flight; a marker that exists but does not validate is an error, because
// refusing startup is the only safe reading of an unreadable provenance record.
func Read(targetRoot string) (Marker, bool, error) {
	path := Path(targetRoot)
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return Marker{}, false, nil
	}
	if err != nil {
		return Marker{}, false, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxMarkerBytes {
		return Marker{}, false, errors.New("copy-in marker is not a bounded regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return Marker{}, false, err
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, maxMarkerBytes+1))
	if err != nil {
		return Marker{}, false, err
	}
	if len(data) > maxMarkerBytes {
		return Marker{}, false, errors.New("copy-in marker exceeds its bound")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var marker Marker
	if err := decoder.Decode(&marker); err != nil {
		return Marker{}, false, fmt.Errorf("decode the copy-in marker: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return Marker{}, false, errors.New("copy-in marker has trailing content")
	}
	if err := marker.Validate(); err != nil {
		return Marker{}, false, err
	}
	return marker, true, nil
}

// SameTarget reports whether a marker names the database it was read beside.
// Both the startup admission and the wizard's status projection read the marker,
// so both have to agree on what "this database" means: parent symlinks are
// resolved so a canonical path and its alias describe the same file.
func SameTarget(recorded, actual string) bool {
	canonical := func(path string) string {
		absolute, err := filepath.Abs(path)
		if err != nil {
			return filepath.Clean(path)
		}
		if resolved, err := filepath.EvalSymlinks(filepath.Dir(absolute)); err == nil {
			absolute = filepath.Join(resolved, filepath.Base(absolute))
		}
		return filepath.Clean(absolute)
	}
	return canonical(recorded) == canonical(actual)
}

func writeAll(file *os.File, data []byte) error {
	for len(data) != 0 {
		written, err := file.Write(data)
		if err != nil {
			return err
		}
		data = data[written:]
	}
	return nil
}
