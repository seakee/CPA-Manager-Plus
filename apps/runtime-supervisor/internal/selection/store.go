// Package selection owns the Supervisor-private persistent CPA active
// selection. It stores only canonical stage identity, never caller paths.
package selection

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/artifact"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/filetrust"
	runtimeupdate "github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/update"
)

const (
	SchemaVersion   = 1
	selectionName   = "selection.json"
	selectionLimit  = 16 << 10
	temporaryPrefix = ".selection-"
	SourceBundled   = "bundled"
	SourceFinalized = "finalized_stage"
)

var (
	ErrSelectionInvalid              = errors.New("active selection is invalid")
	ErrSelectionNotPublished         = errors.New("active selection publication did not occur")
	ErrSelectionPublicationAmbiguous = errors.New("active selection publication durability is ambiguous")
)

// Descriptor is internal execution authority. Executable and metadata paths
// are derived locally and are never persisted in selection.json.
type Descriptor struct {
	Source         string
	Version        string
	ArtifactID     artifact.ID
	ExecutablePath string
	MetadataPath   string
}

func Bundled(executablePath, metadataPath string) Descriptor {
	return Descriptor{
		Source:         SourceBundled,
		ExecutablePath: executablePath,
		MetadataPath:   metadataPath,
	}
}

func (d Descriptor) IsFinalizedStage() bool {
	return d.Source == SourceFinalized
}

type record struct {
	SchemaVersion int         `json:"schemaVersion"`
	Engine        string      `json:"engine"`
	Version       string      `json:"version"`
	ArtifactID    artifact.ID `json:"artifactId"`
}

func (r record) validate() error {
	if r.SchemaVersion != SchemaVersion || r.Engine != artifact.EngineCPA ||
		runtimeupdate.ValidateVersion(r.Version) != nil || !r.ArtifactID.IsValid() {
		return ErrSelectionInvalid
	}
	return nil
}

type Store struct {
	root     string
	stages   *runtimeupdate.Store
	rename   func(string, string) error
	syncRoot func(string) error
}

func NewStore(root string, stages *runtimeupdate.Store) (*Store, error) {
	return newStore(root, stages, os.Rename, syncDirectory)
}

func newStore(
	root string,
	stages *runtimeupdate.Store,
	rename func(string, string) error,
	syncRoot func(string) error,
) (*Store, error) {
	if strings.TrimSpace(root) == "" || strings.ContainsRune(root, '\x00') || stages == nil || rename == nil || syncRoot == nil {
		return nil, fmt.Errorf("%w: selection storage is not configured", ErrSelectionInvalid)
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("%w: resolve selection root: %v", ErrSelectionInvalid, err)
	}
	parent := filepath.Dir(absolute)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return nil, fmt.Errorf("%w: create selection parent: %v", ErrSelectionInvalid, err)
	}
	parentInfo, err := os.Lstat(parent)
	if err != nil || !parentInfo.IsDir() || parentInfo.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%w: selection parent is not a directory", ErrSelectionInvalid)
	}
	if err := filetrust.RequireCurrentProcessOwner(parentInfo); err != nil {
		return nil, fmt.Errorf("%w: selection parent: %w", ErrSelectionInvalid, err)
	}
	if err := filetrust.RequirePrivateDirectoryPermissions(parentInfo); err != nil {
		return nil, fmt.Errorf("%w: selection parent: %w", ErrSelectionInvalid, err)
	}
	if err := os.Mkdir(absolute, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, fmt.Errorf("%w: create selection root: %v", ErrSelectionInvalid, err)
	}
	rootInfo, err := os.Lstat(absolute)
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%w: selection root is not a directory", ErrSelectionInvalid)
	}
	if err := filetrust.RequireCurrentProcessOwner(rootInfo); err != nil {
		return nil, fmt.Errorf("%w: selection root: %w", ErrSelectionInvalid, err)
	}
	if err := os.Chmod(absolute, 0o700); err != nil {
		return nil, fmt.Errorf("%w: restrict selection root: %v", ErrSelectionInvalid, err)
	}
	store := &Store{root: absolute, stages: stages, rename: rename, syncRoot: syncRoot}
	if err := store.cleanupTemporary(); err != nil {
		return nil, err
	}
	return store, nil
}

// StageStore returns the underlying validated stage store owned by this selection store.
func (s *Store) StageStore() *runtimeupdate.Store {
	if s == nil {
		return nil
	}
	return s.stages
}

// Load resolves the persistent authority at Supervisor startup. Absence is the
// only state that permits the bundled image descriptor fallback.
func (s *Store) Load(bundled Descriptor) (Descriptor, error) {
	if err := validateBundled(bundled); err != nil {
		return Descriptor{}, err
	}
	selectionPath := filepath.Join(s.root, selectionName)
	info, err := os.Lstat(selectionPath)
	if errors.Is(err, os.ErrNotExist) {
		return bundled, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o600 {
		return Descriptor{}, fmt.Errorf("%w: selection file type or permissions differ", ErrSelectionInvalid)
	}
	if err := filetrust.RequireCurrentProcessOwner(info); err != nil {
		return Descriptor{}, fmt.Errorf("%w: selection file: %w", ErrSelectionInvalid, err)
	}
	selected, err := readRecord(selectionPath)
	if err != nil {
		return Descriptor{}, fmt.Errorf("%w: %v", ErrSelectionInvalid, err)
	}
	resolved, err := s.ResolveFinalized(selected.Version)
	if err != nil {
		return Descriptor{}, fmt.Errorf("%w: selected stage: %v", ErrSelectionInvalid, err)
	}
	if resolved.ArtifactID != selected.ArtifactID {
		return Descriptor{}, fmt.Errorf("%w: selected artifact identity differs", ErrSelectionInvalid)
	}
	return resolved, nil
}

func validateBundled(bundled Descriptor) error {
	if bundled.Source != SourceBundled || strings.TrimSpace(bundled.ExecutablePath) == "" ||
		strings.ContainsRune(bundled.ExecutablePath, '\x00') || strings.ContainsRune(bundled.MetadataPath, '\x00') {
		return fmt.Errorf("%w: bundled descriptor is invalid", ErrSelectionInvalid)
	}
	return nil
}

// ResolveFinalized fully revalidates exact staged bytes without network I/O.
func (s *Store) ResolveFinalized(version string) (Descriptor, error) {
	finalized, err := s.stages.ResolveFinalized(version)
	if err != nil {
		return Descriptor{}, err
	}
	return Descriptor{
		Source:         SourceFinalized,
		Version:        finalized.Metadata.Version,
		ArtifactID:     finalized.Metadata.ArtifactID,
		ExecutablePath: finalized.ExecutablePath,
		MetadataPath:   finalized.MetadataPath,
	}, nil
}

// Revalidate proves that a descriptor still names the same finalized exact
// bytes. Bundled bytes are revalidated by the lifecycle artifact observer.
func (s *Store) Revalidate(descriptor Descriptor) (Descriptor, error) {
	if !descriptor.IsFinalizedStage() {
		if err := validateBundled(descriptor); err != nil {
			return Descriptor{}, err
		}
		return descriptor, nil
	}
	resolved, err := s.ResolveFinalized(descriptor.Version)
	if err != nil {
		return Descriptor{}, err
	}
	if resolved.ArtifactID != descriptor.ArtifactID ||
		resolved.ExecutablePath != descriptor.ExecutablePath || resolved.MetadataPath != descriptor.MetadataPath {
		return Descriptor{}, fmt.Errorf("%w: finalized descriptor changed", ErrSelectionInvalid)
	}
	return resolved, nil
}

// Commit atomically publishes one already finalized stage. Any error after
// rename is deliberately classified as ambiguous because the new selection
// may already be the durable authority.
func (s *Store) Commit(candidate Descriptor) error {
	resolved, err := s.Revalidate(candidate)
	if err != nil || !resolved.IsFinalizedStage() {
		return fmt.Errorf("%w: candidate is not a revalidated finalized stage", ErrSelectionNotPublished)
	}
	selected := record{
		SchemaVersion: SchemaVersion,
		Engine:        artifact.EngineCPA,
		Version:       resolved.Version,
		ArtifactID:    resolved.ArtifactID,
	}
	encoded, err := json.Marshal(selected)
	if err != nil {
		return fmt.Errorf("%w: encode selection: %v", ErrSelectionNotPublished, err)
	}
	encoded = append(encoded, '\n')
	temporary, err := os.CreateTemp(s.root, temporaryPrefix)
	if err != nil {
		return fmt.Errorf("%w: create temporary selection: %v", ErrSelectionNotPublished, err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	_, writeErr := temporary.Write(encoded)
	chmodErr := temporary.Chmod(0o600)
	syncErr := temporary.Sync()
	closeErr := temporary.Close()
	if err := errors.Join(writeErr, chmodErr, syncErr, closeErr); err != nil {
		return fmt.Errorf("%w: persist temporary selection: %v", ErrSelectionNotPublished, err)
	}
	if err := s.rename(temporaryPath, filepath.Join(s.root, selectionName)); err != nil {
		return fmt.Errorf("%w: publish selection: %v", ErrSelectionNotPublished, err)
	}
	if err := s.syncRoot(s.root); err != nil {
		return fmt.Errorf("%w: sync selection root: %v", ErrSelectionPublicationAmbiguous, err)
	}
	return nil
}

func (s *Store) cleanupTemporary() error {
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return fmt.Errorf("%w: inspect selection root: %v", ErrSelectionInvalid, err)
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), temporaryPrefix) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(s.root, entry.Name())); err != nil {
			return fmt.Errorf("%w: clean temporary selection: %v", ErrSelectionInvalid, err)
		}
	}
	return nil
}

func readRecord(name string) (record, error) {
	file, err := os.Open(name)
	if err != nil {
		return record{}, err
	}
	encoded, readErr := io.ReadAll(io.LimitReader(file, selectionLimit+1))
	if err := errors.Join(readErr, file.Close()); err != nil || len(encoded) > selectionLimit {
		return record{}, errors.New("read bounded selection")
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	var selected record
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return record{}, errors.New("selection must be a JSON object")
	}
	seen := make(map[string]bool, 4)
	for decoder.More() {
		token, err := decoder.Token()
		field, ok := token.(string)
		if err != nil || !ok || seen[field] {
			return record{}, errors.New("selection contains an invalid or duplicate field")
		}
		seen[field] = true
		switch field {
		case "schemaVersion":
			err = decoder.Decode(&selected.SchemaVersion)
		case "engine":
			err = decoder.Decode(&selected.Engine)
		case "version":
			err = decoder.Decode(&selected.Version)
		case "artifactId":
			err = decoder.Decode(&selected.ArtifactID)
		default:
			return record{}, errors.New("selection contains an unknown field")
		}
		if err != nil {
			return record{}, err
		}
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		return record{}, errors.New("selection object is incomplete")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return record{}, errors.New("selection must contain one JSON value")
	}
	return selected, selected.validate()
}

func syncDirectory(name string) error {
	directory, err := os.Open(name)
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}
