// Package copyinmarker describes an unfinished copy-in. Migration and its
// operation row live in private staging until one synced database rename commits
// them together. The marker binds staging to a verified backup; it never proves
// an unrecorded target database's provenance.
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
const Version = 3

// Phases describe preparation, not a database identity or an authorization.
const (
	// PhaseMaterializing covers moving non-DB members and rebuilding staging.
	PhaseMaterializing = "materializing"
	// PhaseCommitting means the staged database is migrated, recorded, closed
	// and sidecar-free. Its rename may or may not have committed yet.
	PhaseCommitting = "committing"
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
type Marker struct {
	Version                 int    `json:"version"`
	Phase                   string `json:"phase"`
	TargetDatabase          string `json:"targetDatabase"`
	SourceKind              string `json:"sourceKind"`
	SourceVersion           string `json:"sourceVersion,omitempty"`
	SourceFingerprintBefore string `json:"sourceFingerprintBefore"`
	SourceFingerprintAfter  string `json:"sourceFingerprintAfter,omitempty"`
	BackupManifestSHA256    string `json:"backupManifestSha256"`
	BackupDirectory         string `json:"backupDirectory,omitempty"`
	WrittenAtMS             int64  `json:"writtenAtMs"`
}

// FileIdentity is a cache invalidation observation, not durable provenance.
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

// ValidateDirectory refuses a linked or special work path before any marker
// access. Its fixed child must itself be a real directory when it exists.
func ValidateDirectory(targetRoot string) error {
	info, err := os.Lstat(Directory(targetRoot))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect the copy-in work directory: %w", err)
	}
	if !info.IsDir() {
		return errors.New("the copy-in work path must be a real directory, not a symbolic link or special file")
	}
	return nil
}

// DatabaseSidecarsAbsent is the publication boundary shared by resume and
// admission. Even an empty or linked sidecar is not a published member.
func DatabaseSidecarsAbsent(databasePath string) error {
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Lstat(databasePath + suffix); err == nil {
			return fmt.Errorf("the copy-in target has an unexpected %s database sidecar", suffix)
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("inspect the copy-in target %s sidecar: %w", suffix, err)
		}
	}
	return nil
}

// RequirePublishedDatabase accepts only a regular database with no sidecars.
// A committed row alone cannot authorize foreign bytes beside that database.
func RequirePublishedDatabase(databasePath string) error {
	info, err := os.Lstat(databasePath)
	if err != nil {
		return fmt.Errorf("inspect the published copy-in database: %w", err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("the published copy-in database must be a regular file")
	}
	return DatabaseSidecarsAbsent(databasePath)
}

// Validate rejects a marker that cannot establish provenance for a copy-in.
func (m Marker) Validate() error {
	if m.Version != Version {
		return fmt.Errorf("copy-in marker version %d is not supported; preserve the data root and verified bundle and recover into an empty v2 root with `cpa-manager-plus upgrade run`", m.Version)
	}
	switch m.Phase {
	case PhaseMaterializing, PhaseCommitting:
	case "":
		return errors.New("copy-in marker must name its phase")
	default:
		return fmt.Errorf("copy-in marker phase %q is not supported; preserve the data root and verified bundle and recover into an empty v2 root with `cpa-manager-plus upgrade run`", m.Phase)
	}
	if !filepath.IsAbs(m.TargetDatabase) {
		return errors.New("copy-in marker must name an absolute target database")
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
	if err := ValidateDirectory(targetRoot); err != nil {
		return err
	}
	directory := Directory(targetRoot)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	if err := ValidateDirectory(targetRoot); err != nil {
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

// SyncDirectory persists the database publication on platforms supporting
// directory fsync, using the same durability boundary as marker publication.
func SyncDirectory(path string) error { return syncDirectory(path) }

// AwaitingCommit keeps readers away from a database that cannot be the atomic
// publication: materializing has no target, and committing publishes no sidecars.
func (m Marker) AwaitingCommit(databasePath string) bool {
	return m.Phase == PhaseMaterializing || RequirePublishedDatabase(databasePath) != nil
}

// Read returns the marker of a v2 data root. found is false when no copy-in is
// in flight; a marker that exists but does not validate is an error, because
// refusing startup is the only safe reading of an unreadable provenance record.
func Read(targetRoot string) (Marker, bool, error) {
	if err := ValidateDirectory(targetRoot); err != nil {
		return Marker{}, false, err
	}
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
	// Older contracts may contain fields deliberately removed from this one.
	// Inspect their version first to give recovery guidance, never a shim.
	var header struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		return Marker{}, false, fmt.Errorf("decode the copy-in marker version: %w", err)
	}
	if header.Version != Version {
		return Marker{}, false, fmt.Errorf("copy-in marker version %d is not supported; preserve the data root and verified bundle and recover into an empty v2 root with `cpa-manager-plus upgrade run`", header.Version)
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

// SameTarget reports whether a marker names the database of the data root it was
// read beside. Both the startup admission and the wizard's status projection read
// the marker, so both have to agree on what "this database" means.
//
// The marker sits in that root's own reserved work directory, so the store is the
// same store even when the root is mounted at another absolute path: a host that
// runs the CLI and a container that runs the server agree on the bytes, not on
// the path. Comparison therefore uses the file's identity inside its data root —
// its name — and never the absolute path. A marker naming a different database
// file is evidence for some other run and stays a refusal for both callers; it is
// never read as this store.
func SameTarget(recorded, actual string) bool {
	recordedName := filepath.Base(filepath.Clean(recorded))
	actualName := filepath.Base(filepath.Clean(actual))
	if recordedName == "." || recordedName == string(filepath.Separator) || actualName == "." {
		return false
	}
	return recordedName == actualName
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
