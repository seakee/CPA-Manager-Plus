package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/artifact"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/filetrust"
)

const (
	stagedArtifactSchemaVersion = 1
	stagedExecutableName        = "cli-proxy-api"
	stagedMetadataName          = "artifact.json"
	stagedLicenseName           = "LICENSE"
	maxExtractedLicenseBytes    = 1 << 20
	maxStagedMetadataBytes      = 16 << 10
	maxExtractedBinaryBytes     = 256 << 20
	maxExpandedArchiveBytes     = 512 << 20
	temporaryPrefix             = ".tmp-"
)

var (
	ErrStaging          = errors.New("artifact staging failed")
	ErrStageUnavailable = errors.New("finalized staged artifact is unavailable")
	ErrStageConflict    = errors.New("finalized staged artifact conflicts with official source or exact bytes")
)

// Metadata is CPAMP-owned authority for one finalized inactive CPA artifact.
type Metadata struct {
	SchemaVersion       int         `json:"schemaVersion"`
	Engine              string      `json:"engine"`
	Version             string      `json:"version"`
	ArtifactID          artifact.ID `json:"artifactId"`
	SourceArchiveDigest string      `json:"sourceArchiveDigest"`
}

// FinalizedArtifact contains Supervisor-private execution facts for one
// immutable stage. Paths never cross the Runtime Protocol boundary.
type FinalizedArtifact struct {
	Metadata       Metadata
	ExecutablePath string
	MetadataPath   string
}

func (m Metadata) validate() error {
	if m.SchemaVersion != stagedArtifactSchemaVersion || m.Engine != artifact.EngineCPA ||
		ValidateVersion(m.Version) != nil || !m.ArtifactID.IsValid() || !canonicalDigest(m.SourceArchiveDigest) {
		return errors.New("staged artifact metadata is noncanonical")
	}
	return nil
}

// Preparer combines the fixed official source with Supervisor-private storage.
type Preparer struct {
	source *Source
	store  *Store
}

func NewPreparer(root string) (*Preparer, error) {
	source, err := NewSource()
	if err != nil {
		return nil, err
	}
	store, err := NewStore(root)
	if err != nil {
		return nil, err
	}
	return &Preparer{source: source, store: store}, nil
}

func newPreparer(source *Source, store *Store) *Preparer {
	return &Preparer{source: source, store: store}
}

// NewPreparerWithStore shares one validated stage store with activation and
// selection ownership while retaining the fixed official release source.
func NewPreparerWithStore(store *Store) (*Preparer, error) {
	if store == nil {
		return nil, fmt.Errorf("%w: staging store is unavailable", ErrStaging)
	}
	source, err := NewSource()
	if err != nil {
		return nil, err
	}
	return &Preparer{source: source, store: store}, nil
}

func (p *Preparer) Resolve(ctx context.Context, version string) (Release, error) {
	if p == nil || p.source == nil {
		return Release{}, fmt.Errorf("%w: release source is unavailable", ErrReleaseMetadata)
	}
	return p.source.Resolve(ctx, version)
}

func (p *Preparer) Stage(ctx context.Context, release Release) (Metadata, error) {
	if p == nil || p.source == nil || p.store == nil {
		return Metadata{}, fmt.Errorf("%w: staging service is unavailable", ErrStaging)
	}
	return p.store.Stage(ctx, release, p.source.Download)
}

// Store owns immutable finalized stages beneath one private persistent root.
type Store struct {
	root         string
	executionGID *uint32
}

func NewStore(root string) (*Store, error) {
	return newStore(root, nil)
}

// NewStoreWithExecutionGroup exposes only search access to finalized version
// directories for the dedicated CPA group. Temporary staging workspaces and
// the staging root remain non-enumerable to that group.
func NewStoreWithExecutionGroup(root string, gid uint32) (*Store, error) {
	if gid == 0 {
		return nil, fmt.Errorf("%w: execution group must be non-zero", ErrStaging)
	}
	return newStore(root, &gid)
}

func newStore(root string, executionGID *uint32) (*Store, error) {
	if strings.TrimSpace(root) == "" || strings.ContainsRune(root, '\x00') {
		return nil, fmt.Errorf("%w: staging root is required", ErrStaging)
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("%w: resolve staging root: %v", ErrStaging, err)
	}
	if err := os.MkdirAll(absolute, 0o700); err != nil {
		return nil, fmt.Errorf("%w: create staging root: %v", ErrStaging, err)
	}
	rootInfo, err := os.Lstat(absolute)
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%w: staging root is not a directory", ErrStaging)
	}
	if err := filetrust.RequireCurrentProcessOwner(rootInfo); err != nil {
		return nil, fmt.Errorf("%w: staging root: %w", ErrStaging, err)
	}
	rootMode := os.FileMode(0o700)
	if executionGID != nil {
		rootMode = 0o710
		if err := os.Chown(absolute, -1, int(*executionGID)); err != nil {
			return nil, fmt.Errorf("%w: assign staging execution group: %v", ErrStaging, err)
		}
	}
	if err := os.Chmod(absolute, rootMode); err != nil {
		return nil, fmt.Errorf("%w: restrict staging root: %v", ErrStaging, err)
	}
	store := &Store{root: absolute, executionGID: executionGID}
	if err := store.cleanupTemporary(); err != nil {
		return nil, err
	}
	if err := store.reconcileFinalizedExecutionCorridor(); err != nil {
		return nil, err
	}
	return store, nil
}

type downloadArchive func(context.Context, Release, io.Writer) error

func (s *Store) Stage(ctx context.Context, release Release, download downloadArchive) (Metadata, error) {
	if err := ctx.Err(); err != nil {
		return Metadata{}, err
	}
	if download == nil || ValidateVersion(release.Version) != nil || !canonicalDigest(release.ArchiveDigest) {
		return Metadata{}, fmt.Errorf("%w: invalid stage request", ErrStaging)
	}
	finalPath := filepath.Join(s.root, release.Version)
	if metadata, found, err := s.verifyPreparedFinal(finalPath, release); found || err != nil {
		return metadata, err
	}
	temporary, err := os.MkdirTemp(s.root, temporaryPrefix)
	if err != nil {
		return Metadata{}, fmt.Errorf("%w: create operation temporary directory: %v", ErrStaging, err)
	}
	defer os.RemoveAll(temporary)
	if err := os.Chmod(temporary, 0o700); err != nil {
		return Metadata{}, fmt.Errorf("%w: restrict operation temporary directory: %v", ErrStaging, err)
	}

	archivePath := filepath.Join(temporary, "source.tar.gz")
	archiveFile, err := os.OpenFile(archivePath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return Metadata{}, fmt.Errorf("%w: create temporary archive: %v", ErrStaging, err)
	}
	downloadErr := download(ctx, release, archiveFile)
	syncErr := archiveFile.Sync()
	closeErr := archiveFile.Close()
	if err := errors.Join(downloadErr, syncErr, closeErr); err != nil {
		return Metadata{}, err
	}

	publication := filepath.Join(temporary, "final")
	if err := os.Mkdir(publication, 0o700); err != nil {
		return Metadata{}, fmt.Errorf("%w: create publication directory: %v", ErrStaging, err)
	}
	artifactID, err := extractExecutable(archivePath, filepath.Join(publication, stagedExecutableName))
	if err != nil {
		return Metadata{}, err
	}
	metadata := Metadata{
		SchemaVersion:       stagedArtifactSchemaVersion,
		Engine:              artifact.EngineCPA,
		Version:             release.Version,
		ArtifactID:          artifactID,
		SourceArchiveDigest: release.ArchiveDigest,
	}
	if err := writeMetadata(filepath.Join(publication, stagedMetadataName), metadata); err != nil {
		return Metadata{}, err
	}
	if err := syncDirectory(publication); err != nil {
		return Metadata{}, fmt.Errorf("%w: sync publication directory: %v", ErrStaging, err)
	}
	if _, found, err := s.verifyPreparedFinal(finalPath, release); found || err != nil {
		return Metadata{}, err
	}
	if err := os.Rename(publication, finalPath); err != nil {
		if existing, found, verifyErr := s.verifyPreparedFinal(finalPath, release); found || verifyErr != nil {
			return existing, verifyErr
		}
		return Metadata{}, fmt.Errorf("%w: atomically publish finalized stage: %v", ErrStaging, err)
	}
	if err := s.prepareFinalizedExecutionDirectory(finalPath); err != nil {
		return Metadata{}, err
	}
	if err := syncDirectory(s.root); err != nil {
		return Metadata{}, fmt.Errorf("%w: sync staging root: %v", ErrStaging, err)
	}
	verified, found, err := s.verifyFinal(finalPath, release)
	if err != nil || !found {
		if err == nil {
			err = fmt.Errorf("%w: finalized stage disappeared", ErrStaging)
		}
		return Metadata{}, err
	}
	return verified, nil
}

func (s *Store) reconcileFinalizedExecutionCorridor() error {
	if s.executionGID == nil {
		return nil
	}
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return fmt.Errorf("%w: inspect finalized stages for execution corridor: %v", ErrStaging, err)
	}
	for _, entry := range entries {
		if ValidateVersion(entry.Name()) != nil {
			continue
		}
		finalPath := filepath.Join(s.root, entry.Name())
		if _, found, verifyErr := s.verifyFinalizedPath(finalPath, entry.Name()); verifyErr != nil || !found {
			if errors.Is(verifyErr, filetrust.ErrUntrustedOwner) {
				return verifyErr
			}
			// Do not widen an invalid or incomplete persisted shape. A selected or
			// requested stage still fails closed through the existing verifier.
			continue
		}
		if err := s.prepareFinalizedExecutionDirectory(finalPath); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) prepareFinalizedExecutionDirectory(name string) error {
	if s.executionGID == nil {
		return nil
	}
	if err := os.Chown(name, -1, int(*s.executionGID)); err != nil {
		return fmt.Errorf("%w: assign finalized execution group: %v", ErrStaging, err)
	}
	if err := os.Chmod(name, 0o510); err != nil {
		return fmt.Errorf("%w: restrict finalized execution directory: %v", ErrStaging, err)
	}
	if err := syncDirectory(name); err != nil {
		return fmt.Errorf("%w: sync finalized execution directory: %v", ErrStaging, err)
	}
	return nil
}

func (s *Store) cleanupTemporary() error {
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return fmt.Errorf("%w: inspect staging root: %v", ErrStaging, err)
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), temporaryPrefix) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(s.root, entry.Name())); err != nil {
			return fmt.Errorf("%w: clean incomplete temporary stage: %v", ErrStaging, err)
		}
	}
	return nil
}

// ResolveFinalized re-reads and re-hashes one exact local finalized stage.
// It performs no release lookup or other network access.
func (s *Store) ResolveFinalized(version string) (FinalizedArtifact, error) {
	if s == nil || ValidateVersion(version) != nil {
		return FinalizedArtifact{}, fmt.Errorf("%w: invalid target version", ErrStageUnavailable)
	}
	finalPath := filepath.Join(s.root, version)
	metadata, found, err := s.verifyFinalizedPath(finalPath, version)
	if err != nil {
		return FinalizedArtifact{}, err
	}
	if !found {
		return FinalizedArtifact{}, fmt.Errorf("%w: version %s", ErrStageUnavailable, version)
	}
	return FinalizedArtifact{
		Metadata:       metadata,
		ExecutablePath: filepath.Join(finalPath, stagedExecutableName),
		MetadataPath:   filepath.Join(finalPath, stagedMetadataName),
	}, nil
}

func (s *Store) verifyFinal(finalPath string, release Release) (Metadata, bool, error) {
	metadata, found, err := s.verifyFinalizedPath(finalPath, release.Version)
	if err != nil || !found {
		return metadata, found, err
	}
	if metadata.SourceArchiveDigest != release.ArchiveDigest {
		return Metadata{}, true, fmt.Errorf("%w: finalized metadata differs", ErrStageConflict)
	}
	return metadata, true, nil
}

func (s *Store) verifyPreparedFinal(finalPath string, release Release) (Metadata, bool, error) {
	metadata, found, err := s.verifyFinal(finalPath, release)
	if err != nil || !found {
		return metadata, found, err
	}
	if err := s.prepareFinalizedExecutionDirectory(finalPath); err != nil {
		return Metadata{}, true, err
	}
	return metadata, true, nil
}

func (s *Store) verifyFinalizedPath(finalPath, version string) (Metadata, bool, error) {
	info, err := os.Lstat(finalPath)
	if errors.Is(err, os.ErrNotExist) {
		return Metadata{}, false, nil
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return Metadata{}, true, fmt.Errorf("%w: finalized stage is not a directory", ErrStageConflict)
	}
	if err := filetrust.RequireCurrentProcessOwner(info); err != nil {
		return Metadata{}, true, fmt.Errorf("%w: finalized stage directory: %w", ErrStageConflict, err)
	}
	metadataPath := filepath.Join(finalPath, stagedMetadataName)
	metadataInfo, err := os.Lstat(metadataPath)
	if err != nil || !metadataInfo.Mode().IsRegular() || metadataInfo.Mode().Perm() != 0o444 {
		return Metadata{}, true, fmt.Errorf("%w: finalized metadata permissions differ", ErrStageConflict)
	}
	if err := filetrust.RequireCurrentProcessOwner(metadataInfo); err != nil {
		return Metadata{}, true, fmt.Errorf("%w: finalized metadata: %w", ErrStageConflict, err)
	}
	metadata, err := readMetadata(metadataPath)
	if err != nil || metadata.Version != version {
		return Metadata{}, true, fmt.Errorf("%w: finalized metadata differs", ErrStageConflict)
	}
	executablePath := filepath.Join(finalPath, stagedExecutableName)
	executableInfo, err := os.Lstat(executablePath)
	if err != nil || !executableInfo.Mode().IsRegular() || executableInfo.Mode().Perm() != 0o755 {
		return Metadata{}, true, fmt.Errorf("%w: finalized executable type or permissions differ", ErrStageConflict)
	}
	if err := filetrust.RequireCurrentProcessOwner(executableInfo); err != nil {
		return Metadata{}, true, fmt.Errorf("%w: finalized executable: %w", ErrStageConflict, err)
	}
	actual, err := digestFile(executablePath, maxExtractedBinaryBytes)
	if err != nil || actual != metadata.ArtifactID {
		return Metadata{}, true, fmt.Errorf("%w: finalized executable digest differs", ErrStageConflict)
	}
	licensePath := filepath.Join(finalPath, stagedLicenseName)
	licenseInfo, err := os.Lstat(licensePath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Metadata{}, true, fmt.Errorf("%w: finalized license stat failed: %v", ErrStageConflict, err)
	}
	if err == nil {
		if !licenseInfo.Mode().IsRegular() || licenseInfo.Mode().Perm() != 0o444 ||
			licenseInfo.Size() <= 0 || licenseInfo.Size() > maxExtractedLicenseBytes {
			return Metadata{}, true, fmt.Errorf("%w: finalized license type, size or permissions differ", ErrStageConflict)
		}
		if err := filetrust.RequireCurrentProcessOwner(licenseInfo); err != nil {
			return Metadata{}, true, fmt.Errorf("%w: finalized license: %w", ErrStageConflict, err)
		}
	}
	return metadata, true, nil
}

func extractExecutable(archivePath, executablePath string) (artifact.ID, error) {
	archiveFile, err := os.Open(archivePath)
	if err != nil {
		return "", fmt.Errorf("%w: open verified archive: %v", ErrStaging, err)
	}
	defer archiveFile.Close()
	gzipReader, err := gzip.NewReader(archiveFile)
	if err != nil {
		return "", fmt.Errorf("%w: open gzip archive: %v", ErrStaging, err)
	}
	defer gzipReader.Close()
	tarReader := tar.NewReader(gzipReader)
	found := false
	var expandedBytes int64
	var executableDigest artifact.ID
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", fmt.Errorf("%w: inspect tar archive: %v", ErrStaging, err)
		}
		if err := validateArchivePath(header.Name); err != nil {
			return "", err
		}
		if header.Size < 0 || header.Size > maxExpandedArchiveBytes-expandedBytes {
			return "", fmt.Errorf("%w: archive expanded size exceeds limit", ErrStaging)
		}
		expandedBytes += header.Size
		if header.Name != stagedExecutableName {
			continue
		}
		if found {
			return "", fmt.Errorf("%w: duplicate %s archive entry", ErrStaging, stagedExecutableName)
		}
		found = true
		if (header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA) || header.Size <= 0 || header.Size > maxExtractedBinaryBytes {
			return "", fmt.Errorf("%w: staged executable entry type or size is invalid", ErrStaging)
		}
		file, err := os.OpenFile(executablePath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return "", fmt.Errorf("%w: create staged executable: %v", ErrStaging, err)
		}
		hasher := sha256.New()
		written, copyErr := io.Copy(io.MultiWriter(file, hasher), io.LimitReader(tarReader, maxExtractedBinaryBytes+1))
		chmodErr := file.Chmod(0o755)
		syncErr := file.Sync()
		closeErr := file.Close()
		if err := errors.Join(copyErr, chmodErr, syncErr, closeErr); err != nil || written != header.Size || written > maxExtractedBinaryBytes {
			return "", fmt.Errorf("%w: extract staged executable", ErrStaging)
		}
		executableDigest = artifact.ID("sha256:" + hex.EncodeToString(hasher.Sum(nil)))
	}
	if !found {
		return "", fmt.Errorf("%w: archive does not contain %s", ErrStaging, stagedExecutableName)
	}
	return executableDigest, nil
}

func validateArchivePath(name string) error {
	if name == "" || strings.ContainsAny(name, "\\\x00") || path.IsAbs(name) {
		return fmt.Errorf("%w: unsafe archive path", ErrStaging)
	}
	cleaned := path.Clean(name)
	canonical := strings.TrimSuffix(name, "/")
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") || cleaned != canonical {
		return fmt.Errorf("%w: unsafe archive path", ErrStaging)
	}
	return nil
}

func writeMetadata(name string, metadata Metadata) error {
	if err := metadata.validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrStaging, err)
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return fmt.Errorf("%w: encode staged metadata: %v", ErrStaging, err)
	}
	encoded = append(encoded, '\n')
	file, err := os.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("%w: create staged metadata: %v", ErrStaging, err)
	}
	_, writeErr := file.Write(encoded)
	chmodErr := file.Chmod(0o444)
	syncErr := file.Sync()
	closeErr := file.Close()
	if err := errors.Join(writeErr, chmodErr, syncErr, closeErr); err != nil {
		return fmt.Errorf("%w: persist staged metadata: %v", ErrStaging, err)
	}
	return nil
}

func readMetadata(name string) (Metadata, error) {
	file, err := os.Open(name)
	if err != nil {
		return Metadata{}, errors.New("read bounded staged metadata")
	}
	encoded, readErr := io.ReadAll(io.LimitReader(file, maxStagedMetadataBytes+1))
	if err := errors.Join(readErr, file.Close()); err != nil || len(encoded) > maxStagedMetadataBytes {
		return Metadata{}, errors.New("read bounded staged metadata")
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var metadata Metadata
	if err := decoder.Decode(&metadata); err != nil {
		return Metadata{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Metadata{}, errors.New("staged metadata must contain one JSON value")
	}
	return metadata, metadata.validate()
}

func digestFile(name string, limit int64) (artifact.ID, error) {
	file, err := os.Open(name)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hasher := sha256.New()
	written, err := io.Copy(hasher, io.LimitReader(file, limit+1))
	if err != nil || written > limit {
		return "", errors.New("file exceeds digest limit")
	}
	return artifact.ID("sha256:" + hex.EncodeToString(hasher.Sum(nil))), nil
}

func syncDirectory(name string) error {
	directory, err := os.Open(name)
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}
