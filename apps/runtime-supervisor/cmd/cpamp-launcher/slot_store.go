//go:build unix

package main

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const (
	MaxSlotArchiveBytes      int64 = 192 << 20 // 192 MiB compressed
	MaxSlotUncompressedBytes int64 = 512 << 20 // 512 MiB uncompressed
	MaxSlotMemberCount       int   = 4096

	SlotStateNone             = "none"
	SlotStateTrial            = "trial"
	SlotStateCommitted        = "committed"
	SlotStateRolledBack       = "rolled_back"
	SlotStateRecoveryRequired = "recovery_required"
)

// SlotState persists slot transition state in <runtimeRoot>/cpamp/slot-state.json.
type SlotState struct {
	SchemaVersion            int    `json:"schema_version"`
	State                    string `json:"state"`
	ActiveSlot               string `json:"active_slot"`
	PreviousSlot             string `json:"previous_slot"`
	BaseVersionAtSwitch      string `json:"base_version_at_switch"`
	TrialID                  string `json:"trial_id"`
	TrialBootCount           int    `json:"trial_boot_count,omitempty"`
	ActiveAttemptID          string `json:"active_attempt_id,omitempty"`
	LastAttemptID            string `json:"last_attempt_id,omitempty"`
	SnapshotDir              string `json:"snapshot_dir"`
	SchemaGeneration         int    `json:"schema_generation"`
	PreviousSchemaGeneration int    `json:"previous_schema_generation"`
	LastFailureCode          string `json:"last_failure_code"`
	LastRolledBackSlot       string `json:"last_rolled_back_slot"`
	UpdatedAt                string `json:"updated_at"`
}

// DefaultSlotState returns the canonical initial ("none") slot state.
func DefaultSlotState() SlotState {
	return SlotState{
		SchemaVersion:            1,
		State:                    SlotStateNone,
		SchemaGeneration:         1,
		PreviousSchemaGeneration: 1,
	}
}

func validateSlotStateStruct(st SlotState) error {
	if st.SchemaVersion != 1 {
		return fmt.Errorf("unsupported slot-state schema_version %d", st.SchemaVersion)
	}
	switch st.State {
	case SlotStateNone, SlotStateTrial, SlotStateCommitted, SlotStateRolledBack, SlotStateRecoveryRequired:
	default:
		return fmt.Errorf("unsupported slot-state state %q", st.State)
	}
	if st.ActiveSlot != "" {
		if err := ValidateCanonicalSlotVersion(st.ActiveSlot); err != nil {
			return fmt.Errorf("invalid active_slot: %w", err)
		}
	}
	if st.PreviousSlot != "" {
		if err := ValidateCanonicalSlotVersion(st.PreviousSlot); err != nil {
			return fmt.Errorf("invalid previous_slot: %w", err)
		}
	}
	if st.LastRolledBackSlot != "" {
		if err := ValidateCanonicalSlotVersion(st.LastRolledBackSlot); err != nil {
			return fmt.Errorf("invalid last_rolled_back_slot: %w", err)
		}
	}
	if (st.State == SlotStateTrial || st.State == SlotStateCommitted || st.State == SlotStateRecoveryRequired) && st.ActiveSlot == "" {
		return fmt.Errorf("slot-state %q requires non-empty active_slot", st.State)
	}
	if st.State == SlotStateNone && st.ActiveSlot != "" {
		return fmt.Errorf("slot-state none must have empty active_slot")
	}
	return nil
}

// SlotStore manages verified CPAMP slot directories and slot-state.json with O_NOFOLLOW guards.
type SlotStore struct {
	CPAMPRoot        string
	SlotsDir         string
	SlotStatePath    string
	ExpectedUID      int
	CheckRootGID     bool
	BaseEpoch        int
	ExpectedPlatform string
	Keyring          map[string]ed25519.PublicKey

	afterRenameSyncHook func() error
}

// NewSlotStore returns a root-enforcing SlotStore (ExpectedUID=0, CheckRootGID=true).
func NewSlotStore(cpampRoot, slotsDir, slotStatePath string, baseEpoch int) *SlotStore {
	return &SlotStore{
		CPAMPRoot:        filepath.Clean(cpampRoot),
		SlotsDir:         filepath.Clean(slotsDir),
		SlotStatePath:    filepath.Clean(slotStatePath),
		ExpectedUID:      RootUID,
		CheckRootGID:     true,
		BaseEpoch:        baseEpoch,
		ExpectedPlatform: ExpectedSlotPlatform(),
	}
}

// NewSlotStoreWithOwner returns a SlotStore enforcing a caller-specified owner UID/GID check.
func NewSlotStoreWithOwner(cpampRoot, slotsDir, slotStatePath string, baseEpoch int, expectedUID int, checkRootGID bool) *SlotStore {
	s := NewSlotStore(cpampRoot, slotsDir, slotStatePath, baseEpoch)
	s.ExpectedUID = expectedUID
	s.CheckRootGID = checkRootGID
	return s
}

func (s *SlotStore) checkStatOwner(st unix.Stat_t, desc string) error {
	if int(st.Uid) != s.ExpectedUID {
		return fmt.Errorf("%s owner uid %d != expected %d", desc, st.Uid, s.ExpectedUID)
	}
	if s.CheckRootGID && int(st.Gid) != RootGID {
		return fmt.Errorf("%s owner gid %d != expected %d", desc, st.Gid, RootGID)
	}
	return nil
}

func openNoFollowDirFD(dirPath string) (int, unix.Stat_t, error) {
	var st unix.Stat_t
	fd, err := unix.Open(dirPath, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, st, err
	}
	if err := unix.Fstat(fd, &st); err != nil {
		_ = unix.Close(fd)
		return -1, st, err
	}
	if (uint32(st.Mode) & unix.S_IFMT) != unix.S_IFDIR {
		_ = unix.Close(fd)
		return -1, st, fmt.Errorf("%s is not a directory", dirPath)
	}
	return fd, st, nil
}

func openSubdirAtNoFollow(parentFD int, name string) (int, unix.Stat_t, error) {
	var st unix.Stat_t
	fd, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, st, err
	}
	if err := unix.Fstat(fd, &st); err != nil {
		_ = unix.Close(fd)
		return -1, st, err
	}
	if (uint32(st.Mode) & unix.S_IFMT) != unix.S_IFDIR {
		_ = unix.Close(fd)
		return -1, st, fmt.Errorf("%s is not a directory", name)
	}
	return fd, st, nil
}

func readRegularFileAtNoFollow(dirFD int, name string, maxBytes int64) ([]byte, unix.Stat_t, error) {
	var st unix.Stat_t
	fd, err := unix.Openat(dirFD, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, st, err
	}
	if err := unix.Fstat(fd, &st); err != nil {
		_ = unix.Close(fd)
		return nil, st, err
	}
	if (uint32(st.Mode) & unix.S_IFMT) != unix.S_IFREG {
		_ = unix.Close(fd)
		return nil, st, fmt.Errorf("%s is not a regular file", name)
	}
	if uint64(st.Nlink) != 1 {
		_ = unix.Close(fd)
		return nil, st, fmt.Errorf("%s has unexpected link count %d", name, st.Nlink)
	}
	if st.Size < 0 || st.Size > maxBytes {
		_ = unix.Close(fd)
		return nil, st, fmt.Errorf("%s size %d out of bounds", name, st.Size)
	}
	f := os.NewFile(uintptr(fd), name)
	defer func() { _ = f.Close() }()

	data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return nil, st, err
	}
	if int64(len(data)) != st.Size {
		return nil, st, fmt.Errorf("%s size changed while reading", name)
	}
	return data, st, nil
}

func hashRegularFileAtNoFollow(dirFD int, name string, maxBytes int64) (string, int64, error) {
	var st unix.Stat_t
	fd, err := unix.Openat(dirFD, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return "", 0, err
	}
	if err := unix.Fstat(fd, &st); err != nil {
		_ = unix.Close(fd)
		return "", 0, err
	}
	if (uint32(st.Mode) & unix.S_IFMT) != unix.S_IFREG {
		_ = unix.Close(fd)
		return "", 0, fmt.Errorf("%s is not a regular file", name)
	}
	if uint64(st.Nlink) != 1 {
		_ = unix.Close(fd)
		return "", 0, fmt.Errorf("%s has unexpected link count %d", name, st.Nlink)
	}
	if st.Size < 0 || st.Size > maxBytes {
		_ = unix.Close(fd)
		return "", 0, fmt.Errorf("%s size %d out of bounds", name, st.Size)
	}
	f := os.NewFile(uintptr(fd), name)
	defer func() { _ = f.Close() }()

	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, maxBytes+1))
	if err != nil {
		return "", 0, err
	}
	if n != st.Size {
		return "", 0, fmt.Errorf("%s size changed while hashing", name)
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

func fsyncDirPath(dirPath string) error {
	fd, _, err := openNoFollowDirFD(dirPath)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(fd) }()
	return syncIgnoreEINVAL(fd)
}

func syncIgnoreEINVAL(fd int) error {
	if err := unix.Fsync(fd); err != nil && !errors.Is(err, unix.EINVAL) && !errors.Is(err, unix.ENOTSUP) {
		return err
	}
	return nil
}

func randomHexToken(byteLen int) (string, error) {
	buf := make([]byte, byteLen)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// VerifyStoreDirs verifies <cpampRoot> and <slotsDir> exist as trusted owner-only-writable directories.
func (s *SlotStore) VerifyStoreDirs() error {
	cpampFD, cpampSt, err := openNoFollowDirFD(s.CPAMPRoot)
	if err != nil {
		return fmt.Errorf("failed to open cpamp root %s: %w", s.CPAMPRoot, err)
	}
	defer func() { _ = unix.Close(cpampFD) }()
	if err := s.checkStatOwner(cpampSt, s.CPAMPRoot); err != nil {
		return err
	}
	if uint32(cpampSt.Mode)&0o022 != 0 || uint32(cpampSt.Mode)&(unix.S_ISUID|unix.S_ISGID|unix.S_ISVTX) != 0 {
		return fmt.Errorf("cpamp root %s has unsafe mode %#o", s.CPAMPRoot, uint32(cpampSt.Mode)&0o7777)
	}

	slotsFD, slotsSt, err := openNoFollowDirFD(s.SlotsDir)
	if err != nil {
		return fmt.Errorf("failed to open slots dir %s: %w", s.SlotsDir, err)
	}
	defer func() { _ = unix.Close(slotsFD) }()
	if err := s.checkStatOwner(slotsSt, s.SlotsDir); err != nil {
		return err
	}
	if uint32(slotsSt.Mode)&0o022 != 0 || uint32(slotsSt.Mode)&(unix.S_ISUID|unix.S_ISGID|unix.S_ISVTX) != 0 {
		return fmt.Errorf("slots dir %s has unsafe mode %#o", s.SlotsDir, uint32(slotsSt.Mode)&0o7777)
	}
	if uint64(slotsSt.Dev) != uint64(cpampSt.Dev) {
		return fmt.Errorf("slots dir %s crosses filesystem boundary", s.SlotsDir)
	}
	return nil
}

// EnsureStoreDirs creates <cpampRoot> (0755) and <slotsDir> (0755) if missing and verifies them.
func (s *SlotStore) EnsureStoreDirs() error {
	if parentDir := filepath.Dir(s.CPAMPRoot); parentDir != "" && parentDir != "/" && parentDir != "." {
		if st, err := os.Lstat(parentDir); errors.Is(err, fs.ErrNotExist) {
			if mkErr := os.MkdirAll(parentDir, 0o755); mkErr != nil {
				return fmt.Errorf("mkdir parent %s: %w", parentDir, mkErr)
			}
		} else if err == nil && (st.Mode()&fs.ModeSymlink != 0 || !st.IsDir()) {
			return fmt.Errorf("refusing non-directory runtime root %s", parentDir)
		}
	}
	for _, dir := range []string{s.CPAMPRoot, s.SlotsDir} {
		var st unix.Stat_t
		if err := unix.Lstat(dir, &st); err != nil {
			if !errors.Is(err, unix.ENOENT) {
				return fmt.Errorf("lstat %s: %w", dir, err)
			}
			if mkErr := os.Mkdir(dir, 0o755); mkErr != nil && !errors.Is(mkErr, fs.ErrExist) {
				return fmt.Errorf("mkdir %s: %w", dir, mkErr)
			}
		} else if (uint32(st.Mode) & unix.S_IFMT) != unix.S_IFDIR {
			return fmt.Errorf("refusing non-directory %s", dir)
		}
		fd, _, err := openNoFollowDirFD(dir)
		if err != nil {
			return err
		}
		if s.ExpectedUID == RootUID && os.Geteuid() == 0 {
			if err := unix.Fchown(fd, RootUID, RootGID); err != nil {
				_ = unix.Close(fd)
				return err
			}
		}
		if err := unix.Fchmod(fd, 0o755); err != nil {
			_ = unix.Close(fd)
			return err
		}
		_ = unix.Close(fd)
	}
	return s.VerifyStoreDirs()
}

// ReadSlotStateIfExists reads <runtimeRoot>/cpamp/slot-state.json using O_NOFOLLOW.
// If cpampRoot or slot-state.json does not exist, it returns (DefaultSlotState(), false, nil)
// WITHOUT creating or mutating any directory on disk.
func (s *SlotStore) ReadSlotStateIfExists() (SlotState, bool, error) {
	cpampFD, cpampSt, err := openNoFollowDirFD(s.CPAMPRoot)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return DefaultSlotState(), false, nil
		}
		return SlotState{}, false, fmt.Errorf("failed to open cpamp root %s: %w", s.CPAMPRoot, err)
	}
	defer func() { _ = unix.Close(cpampFD) }()

	if err := s.checkStatOwner(cpampSt, s.CPAMPRoot); err != nil {
		return SlotState{}, false, err
	}
	if uint32(cpampSt.Mode)&0o022 != 0 {
		return SlotState{}, false, fmt.Errorf("cpamp root %s is group/world writable (%#o)", s.CPAMPRoot, uint32(cpampSt.Mode)&0o7777)
	}

	stateName := filepath.Base(s.SlotStatePath)
	raw, fileSt, err := readRegularFileAtNoFollow(cpampFD, stateName, 256*1024)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return DefaultSlotState(), false, nil
		}
		return SlotState{}, false, fmt.Errorf("failed to read %s: %w", s.SlotStatePath, err)
	}
	if err := s.checkStatOwner(fileSt, s.SlotStatePath); err != nil {
		return SlotState{}, false, err
	}
	if uint32(fileSt.Mode)&0o7777 != 0o0600 {
		return SlotState{}, false, fmt.Errorf("%s must have mode 0600, got %#o", s.SlotStatePath, uint32(fileSt.Mode)&0o7777)
	}

	var state SlotState
	if err := strictUnmarshalJSON(raw, &state); err != nil {
		return SlotState{}, true, fmt.Errorf("malformed %s: %w", s.SlotStatePath, err)
	}
	if err := validateSlotStateStruct(state); err != nil {
		return SlotState{}, true, fmt.Errorf("invalid %s: %w", s.SlotStatePath, err)
	}
	if state.SchemaGeneration <= 0 {
		state.SchemaGeneration = 1
	}
	if state.PreviousSchemaGeneration <= 0 {
		state.PreviousSchemaGeneration = 1
	}
	return state, true, nil
}

// WriteSlotState atomically writes slot-state.json (0600, fsync file + parent dir).
func (s *SlotStore) WriteSlotState(state SlotState) error {
	if state.SchemaVersion == 0 {
		state.SchemaVersion = 1
	}
	if state.SchemaGeneration <= 0 {
		state.SchemaGeneration = 1
	}
	if state.PreviousSchemaGeneration <= 0 {
		state.PreviousSchemaGeneration = 1
	}
	if state.UpdatedAt == "" {
		state.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	if err := validateSlotStateStruct(state); err != nil {
		return err
	}
	if err := s.EnsureStoreDirs(); err != nil {
		return err
	}

	payload, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')

	suffix, err := randomHexToken(8)
	if err != nil {
		return err
	}
	tmpName := fmt.Sprintf(".slot-state.tmp.%s", suffix)
	tmpPath := filepath.Join(s.CPAMPRoot, tmpName)

	cpampFD, _, err := openNoFollowDirFD(s.CPAMPRoot)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(cpampFD) }()

	fd, err := unix.Openat(cpampFD, tmpName, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return fmt.Errorf("create temp slot-state: %w", err)
	}
	f := os.NewFile(uintptr(fd), tmpPath)
	writeErr := func() error {
		if s.ExpectedUID == RootUID && os.Geteuid() == 0 {
			if err := unix.Fchown(int(f.Fd()), RootUID, RootGID); err != nil {
				return err
			}
		}
		if err := f.Chmod(0o600); err != nil {
			return err
		}
		if _, err := f.Write(payload); err != nil {
			return err
		}
		return syncIgnoreEINVAL(int(f.Fd()))
	}()
	closeErr := f.Close()
	if writeErr != nil {
		_ = os.Remove(tmpPath)
		return writeErr
	}
	if closeErr != nil {
		_ = os.Remove(tmpPath)
		return closeErr
	}

	if err := os.Rename(tmpPath, s.SlotStatePath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename slot-state.json: %w", err)
	}
	if s.afterRenameSyncHook != nil {
		if err := s.afterRenameSyncHook(); err != nil {
			return err
		}
	}
	return syncIgnoreEINVAL(cpampFD)
}

// ListStagedSlots returns canonical v2 slot directory names present under SlotsDir, sorted by SemVer.
func (s *SlotStore) ListStagedSlots() ([]string, error) {
	slotsFD, slotsSt, err := openNoFollowDirFD(s.SlotsDir)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return []string{}, nil
		}
		return nil, err
	}
	if err := s.checkStatOwner(slotsSt, s.SlotsDir); err != nil {
		_ = unix.Close(slotsFD)
		return nil, err
	}
	f := os.NewFile(uintptr(slotsFD), s.SlotsDir)
	defer func() { _ = f.Close() }()

	entries, err := f.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		if ValidateCanonicalSlotVersion(name) != nil {
			continue
		}
		if !entry.IsDir() {
			continue
		}
		out = append(out, name)
	}
	sort.Slice(out, func(i, j int) bool {
		cmp, err := CompareSemVerV2(out[i], out[j])
		if err != nil {
			return out[i] < out[j]
		}
		return cmp < 0
	})
	return out, nil
}

// VerifyInstalledSlot performs full O_NOFOLLOW ownership, permission, Ed25519 signature,
// base_epoch, and per-file SHA-256 verification of <slotsDir>/<version>/.
// Any unlisted extra file, symlink, hardlink, or permission anomaly is rejected.
func (s *SlotStore) VerifyInstalledSlot(version string) (*SlotManifest, error) {
	if err := ValidateCanonicalSlotVersion(version); err != nil {
		return nil, newSlotError(SlotCodeInvalidRequest, http.StatusBadRequest, err.Error(), nil)
	}
	if err := s.VerifyStoreDirs(); err != nil {
		return nil, newSlotError(SlotCodeArchiveInvalid, http.StatusUnprocessableEntity, err.Error(), nil)
	}

	slotsFD, slotsSt, err := openNoFollowDirFD(s.SlotsDir)
	if err != nil {
		return nil, newSlotError(SlotCodeSlotNotStaged, http.StatusConflict, "slots directory missing", err)
	}
	defer func() { _ = unix.Close(slotsFD) }()

	slotFD, slotSt, err := openSubdirAtNoFollow(slotsFD, version)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil, newSlotError(SlotCodeSlotNotStaged, http.StatusConflict, fmt.Sprintf("slot %s is not staged", version), nil)
		}
		return nil, newSlotError(SlotCodeArchiveInvalid, http.StatusUnprocessableEntity, fmt.Sprintf("slot %s is not a valid directory", version), err)
	}
	defer func() { _ = unix.Close(slotFD) }()

	if uint64(slotSt.Dev) != uint64(slotsSt.Dev) {
		return nil, newSlotError(SlotCodeArchiveInvalid, http.StatusUnprocessableEntity, "slot directory crosses filesystem boundary", nil)
	}
	if err := s.checkStatOwner(slotSt, version); err != nil {
		return nil, newSlotError(SlotCodeArchiveInvalid, http.StatusUnprocessableEntity, err.Error(), nil)
	}
	if uint32(slotSt.Mode)&0o022 != 0 || uint32(slotSt.Mode)&(unix.S_ISUID|unix.S_ISGID|unix.S_ISVTX) != 0 {
		return nil, newSlotError(SlotCodeArchiveInvalid, http.StatusUnprocessableEntity, fmt.Sprintf("slot %s has unsafe mode %#o", version, uint32(slotSt.Mode)&0o7777), nil)
	}

	rawManifest, manSt, err := readRegularFileAtNoFollow(slotFD, SlotManifestFileName, MaxSlotMetadataBytes)
	if err != nil {
		return nil, newSlotError(SlotCodeArchiveInvalid, http.StatusUnprocessableEntity, "slot-manifest.json missing or invalid", err)
	}
	if err := s.checkStatOwner(manSt, SlotManifestFileName); err != nil {
		return nil, newSlotError(SlotCodeArchiveInvalid, http.StatusUnprocessableEntity, err.Error(), nil)
	}
	rawSig, sigSt, err := readRegularFileAtNoFollow(slotFD, SlotSignatureFileName, 64*1024)
	if err != nil {
		return nil, newSlotError(SlotCodeSignatureInvalid, http.StatusUnprocessableEntity, "slot-manifest.json.sig missing or invalid", err)
	}
	if err := s.checkStatOwner(sigSt, SlotSignatureFileName); err != nil {
		return nil, newSlotError(SlotCodeSignatureInvalid, http.StatusUnprocessableEntity, err.Error(), nil)
	}

	manifest, err := VerifySlotManifestBytes(rawManifest, rawSig, s.ExpectedPlatform, s.BaseEpoch, s.Keyring)
	if err != nil {
		return nil, err
	}
	if manifest.Version != version {
		return nil, newSlotError(
			SlotCodeArchiveInvalid,
			http.StatusUnprocessableEntity,
			fmt.Sprintf("slot directory %s contains manifest for %s", version, manifest.Version),
			nil,
		)
	}

	expectedDirs := buildExpectedSlotDirs(manifest.Files)
	seenFiles := make(map[string]struct{}, len(manifest.Files))
	if err := s.walkAndVerifySlotDir(slotFD, "", uint64(slotSt.Dev), manifest.Files, expectedDirs, seenFiles); err != nil {
		return nil, err
	}
	if len(seenFiles) != len(manifest.Files) {
		for rel := range manifest.Files {
			if _, ok := seenFiles[rel]; !ok {
				return nil, newSlotError(SlotCodeArchiveInvalid, http.StatusUnprocessableEntity, fmt.Sprintf("slot missing manifest file %s", rel), nil)
			}
		}
	}
	return manifest, nil
}

func buildExpectedSlotDirs(files map[string]SlotManifestEntry) map[string]struct{} {
	dirs := make(map[string]struct{})
	for rel := range files {
		cur := path.Dir(rel)
		for cur != "." && cur != "/" && cur != "" {
			dirs[cur] = struct{}{}
			cur = path.Dir(cur)
		}
	}
	return dirs
}

func (s *SlotStore) walkAndVerifySlotDir(
	dirFD int,
	relPrefix string,
	expectedDev uint64,
	manifestFiles map[string]SlotManifestEntry,
	expectedDirs map[string]struct{},
	seenFiles map[string]struct{},
) error {
	dupFD, err := unix.FcntlInt(uintptr(dirFD), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return newSlotError(SlotCodeInternalError, http.StatusInternalServerError, "dup slot dir fd", err)
	}
	dirFile := os.NewFile(uintptr(dupFD), relPrefix)
	entries, err := dirFile.ReadDir(-1)
	_ = dirFile.Close()
	if err != nil {
		return newSlotError(SlotCodeArchiveInvalid, http.StatusUnprocessableEntity, "read slot directory", err)
	}

	for _, entry := range entries {
		name := entry.Name()
		rel := name
		if relPrefix != "" {
			rel = relPrefix + "/" + name
		}
		if relPrefix == "" && (name == SlotManifestFileName || name == SlotSignatureFileName) {
			continue
		}

		// Try opening as a non-symlink directory first when DirEntry reports directory.
		if entry.Type().IsDir() {
			if _, ok := expectedDirs[rel]; !ok {
				return newSlotError(SlotCodeArchiveInvalid, http.StatusUnprocessableEntity, fmt.Sprintf("unlisted directory %s in slot", rel), nil)
			}
			subFD, subSt, err := openSubdirAtNoFollow(dirFD, name)
			if err != nil {
				return newSlotError(SlotCodeArchiveInvalid, http.StatusUnprocessableEntity, fmt.Sprintf("invalid slot directory %s", rel), err)
			}
			if uint64(subSt.Dev) != expectedDev {
				_ = unix.Close(subFD)
				return newSlotError(SlotCodeArchiveInvalid, http.StatusUnprocessableEntity, fmt.Sprintf("slot directory %s crosses filesystem", rel), nil)
			}
			if err := s.checkStatOwner(subSt, rel); err != nil {
				_ = unix.Close(subFD)
				return newSlotError(SlotCodeArchiveInvalid, http.StatusUnprocessableEntity, err.Error(), nil)
			}
			if uint32(subSt.Mode)&0o022 != 0 || uint32(subSt.Mode)&(unix.S_ISUID|unix.S_ISGID|unix.S_ISVTX) != 0 {
				_ = unix.Close(subFD)
				return newSlotError(SlotCodeArchiveInvalid, http.StatusUnprocessableEntity, fmt.Sprintf("slot directory %s has unsafe mode %#o", rel, uint32(subSt.Mode)&0o7777), nil)
			}
			walkErr := s.walkAndVerifySlotDir(subFD, rel, expectedDev, manifestFiles, expectedDirs, seenFiles)
			_ = unix.Close(subFD)
			if walkErr != nil {
				return walkErr
			}
			continue
		}

		expected, listed := manifestFiles[rel]
		if !listed {
			return newSlotError(SlotCodeArchiveInvalid, http.StatusUnprocessableEntity, fmt.Sprintf("unlisted file or non-regular entry %s in slot", rel), nil)
		}

		fd, err := unix.Openat(dirFD, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return newSlotError(SlotCodeArchiveInvalid, http.StatusUnprocessableEntity, fmt.Sprintf("failed to open slot file %s", rel), err)
		}
		var st unix.Stat_t
		if err := unix.Fstat(fd, &st); err != nil {
			_ = unix.Close(fd)
			return newSlotError(SlotCodeArchiveInvalid, http.StatusUnprocessableEntity, fmt.Sprintf("failed to stat slot file %s", rel), err)
		}
		if (uint32(st.Mode) & unix.S_IFMT) != unix.S_IFREG {
			_ = unix.Close(fd)
			return newSlotError(SlotCodeArchiveInvalid, http.StatusUnprocessableEntity, fmt.Sprintf("slot entry %s is not a regular file", rel), nil)
		}
		if uint64(st.Dev) != expectedDev || uint64(st.Nlink) != 1 {
			_ = unix.Close(fd)
			return newSlotError(SlotCodeArchiveInvalid, http.StatusUnprocessableEntity, fmt.Sprintf("slot file %s failed dev/nlink check", rel), nil)
		}
		if err := s.checkStatOwner(st, rel); err != nil {
			_ = unix.Close(fd)
			return newSlotError(SlotCodeArchiveInvalid, http.StatusUnprocessableEntity, err.Error(), nil)
		}
		perm := uint32(st.Mode) & 0o7777
		if strings.HasPrefix(rel, "bin/") {
			if perm != 0o0555 {
				_ = unix.Close(fd)
				return newSlotError(SlotCodeArchiveInvalid, http.StatusUnprocessableEntity, fmt.Sprintf("slot binary %s must have mode 0555, got %#o", rel, perm), nil)
			}
		} else if perm != 0o0444 {
			_ = unix.Close(fd)
			return newSlotError(SlotCodeArchiveInvalid, http.StatusUnprocessableEntity, fmt.Sprintf("slot file %s must have mode 0444, got %#o", rel, perm), nil)
		}
		if st.Size != expected.Size {
			_ = unix.Close(fd)
			return newSlotError(SlotCodeArchiveInvalid, http.StatusUnprocessableEntity, fmt.Sprintf("slot file %s size mismatch (%d != %d)", rel, st.Size, expected.Size), nil)
		}

		f := os.NewFile(uintptr(fd), rel)
		h := sha256.New()
		n, copyErr := io.Copy(h, io.LimitReader(f, expected.Size+1))
		_ = f.Close()
		if copyErr != nil || n != expected.Size {
			return newSlotError(SlotCodeArchiveInvalid, http.StatusUnprocessableEntity, fmt.Sprintf("failed reading slot file %s", rel), copyErr)
		}
		gotDigest := "sha256:" + hex.EncodeToString(h.Sum(nil))
		if gotDigest != expected.SHA256 {
			return newSlotError(SlotCodeArchiveInvalid, http.StatusUnprocessableEntity, fmt.Sprintf("slot file %s sha256 mismatch", rel), nil)
		}
		seenFiles[rel] = struct{}{}
	}
	return nil
}

type archiveMeterReader struct {
	r          io.Reader
	maxBytes   int64
	readBytes  int64
	hasher     hash.Hash
	onProgress func(int64)
	overflowed bool
}

func (m *archiveMeterReader) Read(p []byte) (int, error) {
	n, err := m.r.Read(p)
	if n > 0 {
		m.readBytes += int64(n)
		if m.readBytes > m.maxBytes {
			m.overflowed = true
			return n, newSlotError(SlotCodeArchiveTooLarge, http.StatusRequestEntityTooLarge, "compressed slot archive exceeds 192 MiB limit", nil)
		}
		_, _ = m.hasher.Write(p[:n])
		if m.onProgress != nil {
			m.onProgress(m.readBytes)
		}
	}
	return n, err
}

type extractedSlotFile struct {
	size   int64
	sha256 string
}

// StageSlotArchive streams a .tar.gz slot bundle, enforces compressed/uncompressed/member bounds,
// verifies outer SHA-256 (when provided), verifies Ed25519 manifest signature + base_epoch +
// every member SHA-256, seals immutable modes (0555/0444), fsyncs, and atomically renames into SlotsDir/<targetVersion>.
func (s *SlotStore) StageSlotArchive(
	r io.Reader,
	targetVersion string,
	expectedOuterSHA256 string,
	expectedOuterSize int64,
	onProgress func(int64),
) (*SlotManifest, int64, error) {
	if err := ValidateCanonicalSlotVersion(targetVersion); err != nil {
		return nil, 0, newSlotError(SlotCodeInvalidRequest, http.StatusBadRequest, err.Error(), nil)
	}
	if expectedOuterSHA256 != "" && !isValidPrefixedSHA256(expectedOuterSHA256) {
		return nil, 0, newSlotError(SlotCodeInvalidRequest, http.StatusBadRequest, "invalid expected_sha256 format", nil)
	}
	if expectedOuterSize < 0 {
		return nil, 0, newSlotError(SlotCodeInvalidRequest, http.StatusBadRequest, "negative expected_size", nil)
	}
	if expectedOuterSize > MaxSlotArchiveBytes {
		return nil, 0, newSlotError(SlotCodeArchiveTooLarge, http.StatusRequestEntityTooLarge, "expected_size exceeds 192 MiB limit", nil)
	}
	if err := s.EnsureStoreDirs(); err != nil {
		return nil, 0, newSlotError(SlotCodeInternalError, http.StatusInternalServerError, "failed to prepare slot store directories", err)
	}

	suffix, err := randomHexToken(8)
	if err != nil {
		return nil, 0, newSlotError(SlotCodeInternalError, http.StatusInternalServerError, "failed to generate staging suffix", err)
	}
	tmpName := ".slot-tmp-" + suffix
	tmpDir := filepath.Join(s.SlotsDir, tmpName)
	if err := os.Mkdir(tmpDir, 0o700); err != nil {
		return nil, 0, newSlotError(SlotCodeInternalError, http.StatusInternalServerError, "failed to create slot temp dir", err)
	}
	cleanupTmp := true
	defer func() {
		if cleanupTmp {
			_ = removeSlotTreeSafe(tmpDir)
		}
	}()

	tmpRootFD, _, err := openNoFollowDirFD(tmpDir)
	if err != nil {
		return nil, 0, newSlotError(SlotCodeInternalError, http.StatusInternalServerError, "failed to open slot temp dir", err)
	}
	defer func() { _ = unix.Close(tmpRootFD) }()
	if s.ExpectedUID == RootUID && os.Geteuid() == 0 {
		if err := unix.Fchown(tmpRootFD, RootUID, RootGID); err != nil {
			return nil, 0, newSlotError(SlotCodeInternalError, http.StatusInternalServerError, "failed to chown slot temp dir", err)
		}
	}

	meter := &archiveMeterReader{
		r:          r,
		maxBytes:   MaxSlotArchiveBytes,
		hasher:     sha256.New(),
		onProgress: onProgress,
	}

	br := bufio.NewReader(meter)
	gz, err := gzip.NewReader(br)
	if err != nil {
		if meter.overflowed {
			return nil, meter.readBytes, newSlotError(SlotCodeArchiveTooLarge, http.StatusRequestEntityTooLarge, "compressed slot archive exceeds 192 MiB limit", nil)
		}
		return nil, meter.readBytes, newSlotError(SlotCodeArchiveInvalid, http.StatusUnprocessableEntity, "invalid gzip stream", err)
	}
	gz.Multistream(false)
	tr := tar.NewReader(gz)
	var (
		memberCount       int
		totalUncompressed int64
		rawManifest       []byte
		rawSig            []byte
		extractedFiles    = make(map[string]extractedSlotFile)
		createdDirs       = make(map[string]struct{})
	)

	ensureTmpSubdirs := func(relDir string) error {
		if relDir == "" || relDir == "." {
			return nil
		}
		parts := strings.Split(relDir, "/")
		curFD := tmpRootFD
		var curRel string
		for i, seg := range parts {
			if i == 0 {
				curRel = seg
			} else {
				curRel = curRel + "/" + seg
			}
			err := unix.Mkdirat(curFD, seg, 0o700)
			if err != nil && !errors.Is(err, unix.EEXIST) {
				if curFD != tmpRootFD {
					_ = unix.Close(curFD)
				}
				return err
			}
			nextFD, _, openErr := openSubdirAtNoFollow(curFD, seg)
			if curFD != tmpRootFD {
				_ = unix.Close(curFD)
			}
			if openErr != nil {
				return openErr
			}
			if s.ExpectedUID == RootUID && os.Geteuid() == 0 {
				if chownErr := unix.Fchown(nextFD, RootUID, RootGID); chownErr != nil {
					_ = unix.Close(nextFD)
					return chownErr
				}
			}
			createdDirs[curRel] = struct{}{}
			curFD = nextFD
		}
		if curFD != tmpRootFD {
			_ = unix.Close(curFD)
		}
		return nil
	}

	for {
		hdr, nextErr := tr.Next()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			_ = gz.Close()
			if meter.overflowed {
				return nil, meter.readBytes, newSlotError(SlotCodeArchiveTooLarge, http.StatusRequestEntityTooLarge, "compressed slot archive exceeds 192 MiB limit", nil)
			}
			return nil, meter.readBytes, newSlotError(SlotCodeArchiveInvalid, http.StatusUnprocessableEntity, "corrupt tar archive", nextErr)
		}

		memberCount++
		if memberCount > MaxSlotMemberCount {
			_ = gz.Close()
			return nil, meter.readBytes, newSlotError(SlotCodeArchiveInvalid, http.StatusUnprocessableEntity, "slot archive exceeds 4096 member count limit", nil)
		}

		rel := strings.TrimPrefix(hdr.Name, "./")
		if rel == "" || rel == "." || rel == "/" {
			if hdr.Typeflag == tar.TypeDir {
				continue
			}
			_ = gz.Close()
			return nil, meter.readBytes, newSlotError(SlotCodeArchiveInvalid, http.StatusUnprocessableEntity, "invalid root tar entry", nil)
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			rel = strings.TrimSuffix(rel, "/")
			if err := validateSlotDirRelativePath(rel); err != nil {
				_ = gz.Close()
				return nil, meter.readBytes, newSlotError(SlotCodeArchiveInvalid, http.StatusUnprocessableEntity, err.Error(), nil)
			}
			if err := ensureTmpSubdirs(rel); err != nil {
				_ = gz.Close()
				return nil, meter.readBytes, newSlotError(SlotCodeArchiveInvalid, http.StatusUnprocessableEntity, "failed to create slot subdirectory", err)
			}

		case tar.TypeReg, tar.TypeRegA:
			if err := ValidateSlotRelativePath(rel); err != nil {
				_ = gz.Close()
				return nil, meter.readBytes, newSlotError(SlotCodeArchiveInvalid, http.StatusUnprocessableEntity, err.Error(), nil)
			}
			if _, dup := extractedFiles[rel]; dup {
				_ = gz.Close()
				return nil, meter.readBytes, newSlotError(SlotCodeArchiveInvalid, http.StatusUnprocessableEntity, fmt.Sprintf("duplicate archive entry %s", rel), nil)
			}
			if hdr.Size < 0 {
				_ = gz.Close()
				return nil, meter.readBytes, newSlotError(SlotCodeArchiveInvalid, http.StatusUnprocessableEntity, fmt.Sprintf("negative archive member size for %s", rel), nil)
			}
			if totalUncompressed+hdr.Size > MaxSlotUncompressedBytes {
				_ = gz.Close()
				return nil, meter.readBytes, newSlotError(SlotCodeArchiveTooLarge, http.StatusRequestEntityTooLarge, "uncompressed slot archive exceeds 512 MiB limit", nil)
			}

			parentRel := path.Dir(rel)
			if parentRel != "." {
				if err := ensureTmpSubdirs(parentRel); err != nil {
					_ = gz.Close()
					return nil, meter.readBytes, newSlotError(SlotCodeArchiveInvalid, http.StatusUnprocessableEntity, "failed to prepare parent dir", err)
				}
			}

			parentFD := tmpRootFD
			if parentRel != "." {
				pfd, err := openRelSubdirNoFollow(tmpRootFD, parentRel)
				if err != nil {
					_ = gz.Close()
					return nil, meter.readBytes, newSlotError(SlotCodeArchiveInvalid, http.StatusUnprocessableEntity, "failed to open parent dir", err)
				}
				parentFD = pfd
			}

			baseName := path.Base(rel)
			fileFD, openErr := unix.Openat(parentFD, baseName, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
			if parentFD != tmpRootFD {
				_ = unix.Close(parentFD)
			}
			if openErr != nil {
				_ = gz.Close()
				return nil, meter.readBytes, newSlotError(SlotCodeArchiveInvalid, http.StatusUnprocessableEntity, fmt.Sprintf("failed to create %s", rel), openErr)
			}

			outFile := os.NewFile(uintptr(fileFD), rel)
			h := sha256.New()
			var memBuf *bytes.Buffer
			var writer io.Writer = io.MultiWriter(outFile, h)
			if rel == SlotManifestFileName || rel == SlotSignatureFileName {
				if hdr.Size > MaxSlotMetadataBytes {
					_ = outFile.Close()
					_ = gz.Close()
					return nil, meter.readBytes, newSlotError(SlotCodeArchiveInvalid, http.StatusUnprocessableEntity, fmt.Sprintf("oversized metadata file %s", rel), nil)
				}
				memBuf = bytes.NewBuffer(make([]byte, 0, int(hdr.Size)))
				writer = io.MultiWriter(outFile, h, memBuf)
			}

			written, copyErr := io.Copy(writer, io.LimitReader(tr, hdr.Size+1))
			if copyErr == nil && s.ExpectedUID == RootUID && os.Geteuid() == 0 {
				copyErr = unix.Fchown(int(outFile.Fd()), RootUID, RootGID)
			}
			var finalMode fs.FileMode = SlotFileMode
			if strings.HasPrefix(rel, "bin/") {
				finalMode = SlotBinMode
			}
			if copyErr == nil {
				copyErr = outFile.Chmod(finalMode)
			}
			if copyErr == nil {
				copyErr = syncIgnoreEINVAL(int(outFile.Fd()))
			}
			closeErr := outFile.Close()
			if copyErr != nil {
				_ = gz.Close()
				if meter.overflowed {
					return nil, meter.readBytes, newSlotError(SlotCodeArchiveTooLarge, http.StatusRequestEntityTooLarge, "compressed slot archive exceeds 192 MiB limit", nil)
				}
				return nil, meter.readBytes, newSlotError(SlotCodeArchiveInvalid, http.StatusUnprocessableEntity, fmt.Sprintf("failed writing %s", rel), copyErr)
			}
			if closeErr != nil || written != hdr.Size {
				_ = gz.Close()
				return nil, meter.readBytes, newSlotError(SlotCodeArchiveInvalid, http.StatusUnprocessableEntity, fmt.Sprintf("size mismatch writing %s", rel), closeErr)
			}

			totalUncompressed += written
			extractedFiles[rel] = extractedSlotFile{
				size:   written,
				sha256: "sha256:" + hex.EncodeToString(h.Sum(nil)),
			}
			if rel == SlotManifestFileName {
				rawManifest = memBuf.Bytes()
			} else if rel == SlotSignatureFileName {
				rawSig = memBuf.Bytes()
			}

		default:
			_ = gz.Close()
			return nil, meter.readBytes, newSlotError(
				SlotCodeArchiveInvalid,
				http.StatusUnprocessableEntity,
				fmt.Sprintf("forbidden tar entry type %q for %s (symlinks/hardlinks/devices prohibited)", hdr.Typeflag, rel),
				nil,
			)
		}
	}

	if padErr := drainTarZeroPaddingToEOF(gz, 64*1024); padErr != nil {
		_ = gz.Close()
		if meter.overflowed {
			return nil, meter.readBytes, newSlotError(SlotCodeArchiveTooLarge, http.StatusRequestEntityTooLarge, "compressed slot archive exceeds 192 MiB limit", nil)
		}
		return nil, meter.readBytes, newSlotError(SlotCodeArchiveInvalid, http.StatusUnprocessableEntity, "invalid tar/gzip trailer or zero padding", padErr)
	}

	if err := gz.Close(); err != nil {
		return nil, meter.readBytes, newSlotError(SlotCodeArchiveInvalid, http.StatusUnprocessableEntity, "gzip trailer verification failed", err)
	}

	// Drain any remaining bytes from the buffered outer reader and reject trailing concatenated data.
	trailing, drainErr := io.Copy(io.Discard, br)
	if drainErr != nil {
		if meter.overflowed {
			return nil, meter.readBytes, newSlotError(SlotCodeArchiveTooLarge, http.StatusRequestEntityTooLarge, "compressed slot archive exceeds 192 MiB limit", nil)
		}
		return nil, meter.readBytes, newSlotError(SlotCodeArchiveInvalid, http.StatusUnprocessableEntity, "failed reading archive stream end", drainErr)
	}
	if trailing > 0 {
		return nil, meter.readBytes, newSlotError(SlotCodeArchiveInvalid, http.StatusUnprocessableEntity, "trailing bytes after gzip stream are prohibited", nil)
	}

	if expectedOuterSize > 0 && meter.readBytes != expectedOuterSize {
		return nil, meter.readBytes, newSlotError(
			SlotCodeArchiveInvalid,
			http.StatusUnprocessableEntity,
			fmt.Sprintf("archive byte length %d does not match expected %d", meter.readBytes, expectedOuterSize),
			nil,
		)
	}
	if expectedOuterSHA256 != "" {
		gotOuter := "sha256:" + hex.EncodeToString(meter.hasher.Sum(nil))
		if gotOuter != expectedOuterSHA256 {
			return nil, meter.readBytes, newSlotError(
				SlotCodeArchiveInvalid,
				http.StatusUnprocessableEntity,
				fmt.Sprintf("archive sha256 %s does not match expected %s", gotOuter, expectedOuterSHA256),
				nil,
			)
		}
	}

	manifest, err := VerifySlotManifestBytes(rawManifest, rawSig, s.ExpectedPlatform, s.BaseEpoch, s.Keyring)
	if err != nil {
		return nil, meter.readBytes, err
	}
	if manifest.Version != targetVersion {
		return nil, meter.readBytes, newSlotError(
			SlotCodeArchiveInvalid,
			http.StatusUnprocessableEntity,
			fmt.Sprintf("slot manifest version %s does not match requested target_version %s", manifest.Version, targetVersion),
			nil,
		)
	}

	// Enforce exact 1-to-1 correspondence between extracted files and manifest.Files.
	for rel, expected := range manifest.Files {
		got, ok := extractedFiles[rel]
		if !ok {
			return nil, meter.readBytes, newSlotError(SlotCodeArchiveInvalid, http.StatusUnprocessableEntity, fmt.Sprintf("archive missing manifest file %s", rel), nil)
		}
		if got.size != expected.Size || got.sha256 != expected.SHA256 {
			return nil, meter.readBytes, newSlotError(SlotCodeArchiveInvalid, http.StatusUnprocessableEntity, fmt.Sprintf("archive file %s failed manifest size/sha256 verification", rel), nil)
		}
	}
	for rel := range extractedFiles {
		if rel == SlotManifestFileName || rel == SlotSignatureFileName {
			continue
		}
		if _, ok := manifest.Files[rel]; !ok {
			return nil, meter.readBytes, newSlotError(SlotCodeArchiveInvalid, http.StatusUnprocessableEntity, fmt.Sprintf("archive contains unlisted file %s", rel), nil)
		}
	}

	expectedDirs := buildExpectedSlotDirs(manifest.Files)
	for relDir := range createdDirs {
		if _, ok := expectedDirs[relDir]; !ok {
			return nil, meter.readBytes, newSlotError(SlotCodeArchiveInvalid, http.StatusUnprocessableEntity, fmt.Sprintf("archive contains unlisted directory %s", relDir), nil)
		}
	}

	// Seal subdirectory permissions (0555) and fsync deepest-first, then seal root slot dir (0755) and fsync.
	sortedDirs := make([]string, 0, len(createdDirs))
	for d := range createdDirs {
		sortedDirs = append(sortedDirs, d)
	}
	sort.Slice(sortedDirs, func(i, j int) bool {
		di := strings.Count(sortedDirs[i], "/")
		dj := strings.Count(sortedDirs[j], "/")
		if di != dj {
			return di > dj
		}
		return sortedDirs[i] > sortedDirs[j]
	})
	for _, relDir := range sortedDirs {
		dfd, err := openRelSubdirNoFollow(tmpRootFD, relDir)
		if err != nil {
			return nil, meter.readBytes, newSlotError(SlotCodeInternalError, http.StatusInternalServerError, "failed opening staged subdir", err)
		}
		chmodErr := unix.Fchmod(dfd, uint32(SlotShareDirMode))
		syncErr := syncIgnoreEINVAL(dfd)
		_ = unix.Close(dfd)
		if chmodErr != nil || syncErr != nil {
			return nil, meter.readBytes, newSlotError(SlotCodeInternalError, http.StatusInternalServerError, "failed sealing staged subdir", errors.Join(chmodErr, syncErr))
		}
	}

	if err := unix.Fchmod(tmpRootFD, uint32(SlotDirMode)); err != nil {
		return nil, meter.readBytes, newSlotError(SlotCodeInternalError, http.StatusInternalServerError, "failed sealing staged slot root", err)
	}
	if err := syncIgnoreEINVAL(tmpRootFD); err != nil {
		return nil, meter.readBytes, newSlotError(SlotCodeInternalError, http.StatusInternalServerError, "failed syncing staged slot root", err)
	}

	finalSlotDir := filepath.Join(s.SlotsDir, targetVersion)
	var displacedOldDir string
	if _, statErr := os.Lstat(finalSlotDir); statErr == nil {
		displacedOldDir = filepath.Join(s.SlotsDir, ".slot-old-"+suffix)
		if err := os.Rename(finalSlotDir, displacedOldDir); err != nil {
			return nil, meter.readBytes, newSlotError(SlotCodeInternalError, http.StatusInternalServerError, "failed displacing prior staged slot", err)
		}
	}
	if err := os.Rename(tmpDir, finalSlotDir); err != nil {
		if displacedOldDir != "" {
			_ = os.Rename(displacedOldDir, finalSlotDir)
		}
		return nil, meter.readBytes, newSlotError(SlotCodeInternalError, http.StatusInternalServerError, "failed renaming staged slot into place", err)
	}
	cleanupTmp = false
	if displacedOldDir != "" {
		_ = removeSlotTreeSafe(displacedOldDir)
	}
	if err := fsyncDirPath(s.SlotsDir); err != nil {
		return nil, meter.readBytes, newSlotError(SlotCodeInternalError, http.StatusInternalServerError, "failed syncing slots parent dir", err)
	}
	return manifest, meter.readBytes, nil
}

func validateSlotDirRelativePath(rel string) error {
	if rel == "" || len(rel) > 512 {
		return fmt.Errorf("empty or oversized slot directory path")
	}
	if strings.HasPrefix(rel, "/") || strings.HasSuffix(rel, "/") || strings.Contains(rel, "\\") {
		return fmt.Errorf("invalid slot directory path %q", rel)
	}
	for i := range len(rel) {
		if rel[i] < 0x20 || rel[i] == 0x7f {
			return fmt.Errorf("control character in slot directory path %q", rel)
		}
	}
	parts := strings.Split(rel, "/")
	for _, seg := range parts {
		if seg == "" || seg == "." || seg == ".." {
			return fmt.Errorf("unsafe directory segment in %q", rel)
		}
	}
	if rel == "bin" || rel == "share" || strings.HasPrefix(rel, "share/") {
		return nil
	}
	return fmt.Errorf("slot directory %q is outside permitted bin/ and share/ trees", rel)
}

func openRelSubdirNoFollow(rootFD int, relDir string) (int, error) {
	parts := strings.Split(relDir, "/")
	curFD := rootFD
	for _, seg := range parts {
		nextFD, _, err := openSubdirAtNoFollow(curFD, seg)
		if curFD != rootFD {
			_ = unix.Close(curFD)
		}
		if err != nil {
			return -1, err
		}
		curFD = nextFD
	}
	return curFD, nil
}

// RemoveSlot removes <slotsDir>/<version> safely (making immutable 0555 dirs writable first).
func (s *SlotStore) RemoveSlot(version string) error {
	if err := ValidateCanonicalSlotVersion(version); err != nil {
		return err
	}
	target := filepath.Join(s.SlotsDir, version)
	var st unix.Stat_t
	if err := unix.Lstat(target, &st); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil
		}
		return err
	}
	if err := removeSlotTreeSafe(target); err != nil {
		return err
	}
	return fsyncDirPath(s.SlotsDir)
}

// PruneSlotsWhere deletes every staged slot directory in SlotsDir matching predicate(version).
func (s *SlotStore) PruneSlotsWhere(predicate func(version string) bool) error {
	slots, err := s.ListStagedSlots()
	if err != nil {
		return err
	}
	for _, ver := range slots {
		if predicate(ver) {
			if err := s.RemoveSlot(ver); err != nil {
				return err
			}
		}
	}
	return nil
}

// removeSlotTreeSafe makes real directories inside root writable (0700) without following symlinks
// before invoking os.RemoveAll, allowing clean removal of 0555 slot directories even when unprivileged.
func removeSlotTreeSafe(root string) error {
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.Type().IsDir() {
			if fd, _, openErr := openNoFollowDirFD(p); openErr == nil {
				_ = unix.Fchmod(fd, 0o700)
				_ = unix.Close(fd)
			}
		}
		return nil
	})
	return os.RemoveAll(root)
}

func drainTarZeroPaddingToEOF(r io.Reader, maxZeroBytes int64) error {
	var total int64
	var buf [4096]byte
	for {
		n, err := r.Read(buf[:])
		if n > 0 {
			for _, b := range buf[:n] {
				if b != 0 {
					return errors.New("non-zero bytes after tar EOF marker")
				}
			}
			total += int64(n)
			if total > maxZeroBytes {
				return fmt.Errorf("trailing tar zero padding exceeds %d bytes", maxZeroBytes)
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}
