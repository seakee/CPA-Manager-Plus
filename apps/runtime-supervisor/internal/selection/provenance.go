package selection

import (
	"crypto/sha256"
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
	OriginBaselineAcquire = "baseline_acquire"
	OriginUserUpdate      = "user_update"
	provenanceName        = "selection-provenance.json"
)

// Provenance describes the most recent selection commit. It is advisory and
// must only be interpreted alongside the matching authoritative selection.
type Provenance struct {
	SchemaVersion           int         `json:"schemaVersion"`
	ArtifactID              artifact.ID `json:"artifactId"`
	Origin                  string      `json:"origin"`
	BaselineVersionAtCommit string      `json:"baselineVersionAtCommit"`
	OperationID             string      `json:"operationId"`
}

func (p Provenance) validate() error {
	if p.SchemaVersion != SchemaVersion || !p.ArtifactID.IsValid() ||
		(p.Origin != OriginBaselineAcquire && p.Origin != OriginUserUpdate) ||
		runtimeupdate.ValidateVersion(p.BaselineVersionAtCommit) != nil || strings.TrimSpace(p.OperationID) == "" {
		return errors.New("invalid selection provenance")
	}
	return nil
}

// LoadPersistedSelection reads and validates selection.json without resolving
// staged executable bytes on disk. Absence returns (Descriptor{}, false, nil).
func (s *Store) LoadPersistedSelection() (Descriptor, bool, error) {
	if s == nil {
		return Descriptor{}, false, fmt.Errorf("%w: selection storage is not configured", ErrSelectionInvalid)
	}
	selectionPath := filepath.Join(s.root, selectionName)
	info, err := os.Lstat(selectionPath)
	if errors.Is(err, os.ErrNotExist) {
		return Descriptor{}, false, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
		info.Mode().Perm() != 0o600 || info.Size() > selectionLimit {
		return Descriptor{}, false, fmt.Errorf("%w: untrusted %s", ErrSelectionInvalid, selectionName)
	}
	if err := filetrust.RequireCurrentProcessOwner(info); err != nil {
		return Descriptor{}, false, fmt.Errorf("%w: %s: %w", ErrSelectionInvalid, selectionName, err)
	}
	selected, err := readRecord(selectionPath)
	if err != nil {
		return Descriptor{}, false, fmt.Errorf("%w: %w", ErrSelectionInvalid, err)
	}
	return Descriptor{
		Source:     SourceFinalized,
		Version:    selected.Version,
		ArtifactID: selected.ArtifactID,
	}, true, nil
}

func (s *Store) LoadProvenance() (Provenance, error) {
	var p Provenance
	if err := s.readSidecar(provenanceName, &p); err != nil {
		return Provenance{}, err
	}
	return p, p.validate()
}

func (s *Store) WriteProvenance(p Provenance) error {
	if err := p.validate(); err != nil {
		return err
	}
	return s.writeSidecar(provenanceName, p)
}

// ActivationIntent binds an activation operation to the finalized
// candidate resolved by the Supervisor before it may stop the old child.
// The separate record leaves the operation journal and selection formats intact.
type ActivationIntent struct {
	SchemaVersion      int         `json:"schemaVersion"`
	OperationID        string      `json:"operationId"`
	RuntimeIdentity    string      `json:"runtimeIdentity"`
	TargetVersion      string      `json:"targetVersion"`
	TargetArtifactID   artifact.ID `json:"targetArtifactId"`
	PreviousArtifactID artifact.ID `json:"previousArtifactId"`
}

func (r ActivationIntent) validate() error {
	if r.SchemaVersion != SchemaVersion || strings.TrimSpace(r.OperationID) == "" || strings.TrimSpace(r.RuntimeIdentity) == "" ||
		runtimeupdate.ValidateVersion(r.TargetVersion) != nil || !r.TargetArtifactID.IsValid() ||
		!r.PreviousArtifactID.IsValid() || r.TargetArtifactID == r.PreviousArtifactID {
		return errors.New("invalid activation intent")
	}
	return nil
}

func (s *Store) LoadActivationIntent(runtimeIdentity, operationID string) (ActivationIntent, error) {
	var r ActivationIntent
	if err := s.readSidecar(activationIntentName(runtimeIdentity, operationID), &r); err != nil {
		return ActivationIntent{}, err
	}
	return r, r.validate()
}

func (s *Store) WriteActivationIntent(r ActivationIntent) error {
	if err := r.validate(); err != nil {
		return err
	}
	return s.writeSidecar(activationIntentName(r.RuntimeIdentity, r.OperationID), r)
}

func activationIntentName(runtimeIdentity, operationID string) string {
	// Hash the operation key so no caller-supplied value becomes a path.
	key := sha256.Sum256(fmt.Appendf(nil, "%q:%q", runtimeIdentity, operationID))
	return fmt.Sprintf("activation-intent-%x.json", key)
}

func (s *Store) readSidecar(name string, value any) error {
	path := filepath.Join(s.root, name)
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() > selectionLimit ||
		filetrust.RequireCurrentProcessOwner(info) != nil {
		return fmt.Errorf("untrusted %s", name)
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, selectionLimit+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return fmt.Errorf("decode %s: %w", name, err)
	}
	if !errors.Is(decoder.Decode(new(any)), io.EOF) {
		return fmt.Errorf("invalid trailing data in %s", name)
	}
	return nil
}

func (s *Store) writeSidecar(name string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(data) >= selectionLimit {
		return fmt.Errorf("%s exceeds size limit", name)
	}
	temp, err := os.CreateTemp(s.root, temporaryPrefix)
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	_, writeErr := temp.Write(append(data, '\n'))
	modeErr := temp.Chmod(0o600)
	syncErr := temp.Sync()
	closeErr := temp.Close()
	if err := errors.Join(writeErr, modeErr, syncErr, closeErr); err != nil {
		return fmt.Errorf("persist %s: %w", name, err)
	}
	if err := s.rename(temp.Name(), filepath.Join(s.root, name)); err != nil {
		return fmt.Errorf("publish %s: %w", name, err)
	}
	return s.syncRoot(s.root)
}
