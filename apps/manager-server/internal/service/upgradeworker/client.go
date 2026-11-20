// Package upgradeworker is the temporary, authenticated Docker upgrade executor.
// The operation journal remains owned by service/upgrade, not by this transport.
package upgradeworker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/controller/artifactproxy"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/controller/upgradesurface"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/processlock"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/setting"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	upgrade "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/upgrade"
	_ "modernc.org/sqlite"
)

const (
	Contract              = "cpamp.upgrade-execution.v1"
	EntryContract         = "cpamp.upgrade-auth.v1"
	WizardContract        = "cpamp.upgrade-wizard.v1"
	SocketEnvironment     = "CPAMP_UPGRADE_WORKER_SOCKET"
	ExecuteSuffix         = "/installation/upgrade/execute"
	EntryPath             = "/.well-known/cpamp-bootstrap/upgrade/authenticate"
	CodeUnavailable       = "upgrade_worker_unavailable"
	CodeAdminUnauthorized = "admin_unauthorized"
	CodeBusy              = "upgrade_execution_busy"
	CodeRequestInvalid    = "upgrade_request_invalid"
	MaxRequestBytes       = 32 * 1024
	maxResponseProxyBytes = 2 * 1024 * 1024
)

type Execution struct {
	Available        bool   `json:"available"`
	Running          bool   `json:"running"`
	Action           string `json:"action,omitempty"`
	Code             string `json:"code,omitempty"`
	ManagedAvailable bool   `json:"managedAvailable"`
}

type Request struct {
	Action                   string   `json:"action"`
	AcknowledgeDays          []string `json:"acknowledgeDays,omitempty"`
	AcknowledgeAllMismatches bool     `json:"acknowledgeAllMismatches,omitempty"`
	AcknowledgeParityGaps    *bool    `json:"acknowledgeParityGaps,omitempty"`
	PluginsEnabled           *bool    `json:"pluginsEnabled,omitempty"`
	BaseURL                  *string  `json:"baseUrl,omitempty"`
	ManagementKey            *string  `json:"managementKey,omitempty"`
}

// PreparedCredentialVerifier verifies fallback authentication using a current,
// complete private snapshot. Incomplete snapshots may identify the setup flow,
// but cannot establish which administrator credential is current.
type PreparedCredentialVerifier struct {
	databasePath string
	scratchDir   string
	openSnapshot func(context.Context, string, string) (*upgrade.Snapshot, error)
}

func NewPreparedCredentialVerifier(databasePath string) *PreparedCredentialVerifier {
	return &PreparedCredentialVerifier{databasePath: strings.TrimSpace(databasePath)}
}

func hasNonEmptyLockMarker(databasePath string) bool {
	lockPath := strings.TrimSpace(databasePath) + ".manager.lock"
	info, err := os.Lstat(lockPath)
	if os.IsNotExist(err) {
		return false
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > 0 {
		return true
	}
	if _, linkErr := processlock.InspectRegularSingleLink(lockPath); linkErr != nil {
		return true
	}
	return false
}

func isLowerHex64(raw string) bool {
	if len(raw) != 64 {
		return false
	}
	for i := range len(raw) {
		ch := raw[i]
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
			return false
		}
	}
	return true
}

// ValidPreparedOperation reports whether op is a committed upgraded-legacy
// operation record with complete SHA-256 provenance digests.
func ValidPreparedOperation(op model.UpgradeOperation) bool {
	if !validUpgradedOperationRow(op) {
		return false
	}
	if op.Stage == model.UpgradeStagePreflight || op.Stage == model.UpgradeStageRolledBack {
		return false
	}
	before := strings.ToLower(strings.TrimSpace(op.SourceFingerprintBefore))
	after := strings.ToLower(strings.TrimSpace(op.SourceFingerprintAfter))
	manifest := strings.ToLower(strings.TrimSpace(op.BackupManifestSHA256))
	return isLowerHex64(before) && isLowerHex64(after) && isLowerHex64(manifest)
}

// OperationRevision returns the deterministic 64-hex identity of a prepared
// upgrade operation derived from its post-copy source fingerprint and backup
// manifest digest. It returns "" when op is not a valid prepared operation.
func OperationRevision(op model.UpgradeOperation) string {
	if !ValidPreparedOperation(op) {
		return ""
	}
	after := strings.ToLower(strings.TrimSpace(op.SourceFingerprintAfter))
	manifest := strings.ToLower(strings.TrimSpace(op.BackupManifestSHA256))
	sum := sha256.Sum256([]byte("cpamp-upgrade-operation-v1:" + after + ":" + manifest))
	return hex.EncodeToString(sum[:])
}

// ValidPreparedOperationInDB checks that db contains a valid prepared
// upgraded-legacy operation record.
func ValidPreparedOperationInDB(ctx context.Context, db *sql.DB) bool {
	if db == nil {
		return false
	}
	op, found, err := upgrade.ReadRecordFrom(ctx, db)
	return err == nil && found && ValidPreparedOperation(op)
}

// BaseFileSnapshot is a read-only private copy of only the base SQLite file.
// It is strictly restricted to recovering immutable prepared identity and
// non-authoritative degraded status when a root-owned -wal/-shm sidecar makes
// full WAL snapshots unreadable to unprivileged Manager; it must never be used
// to verify completion or advance an upgrade stage.
type BaseFileSnapshot struct {
	dir string
	db  *sql.DB
}

func (s *BaseFileSnapshot) DB() *sql.DB {
	if s == nil {
		return nil
	}
	return s.db
}

func (s *BaseFileSnapshot) Close() error {
	if s == nil {
		return nil
	}
	var err error
	if s.db != nil {
		err = s.db.Close()
		s.db = nil
	}
	if s.dir != "" {
		err = errors.Join(err, os.RemoveAll(s.dir))
		s.dir = ""
	}
	return err
}

func digestSingleLinkBaseFile(ctx context.Context, path string) (int64, string, error) {
	if err := ctx.Err(); err != nil {
		return 0, "", err
	}
	info, err := processlock.InspectRegularSingleLink(path)
	if err != nil {
		return 0, "", err
	}
	if info.Size() <= 0 {
		return 0, "", errors.New("upgrade_database_empty")
	}
	f, err := os.Open(path)
	if err != nil {
		return 0, "", err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return 0, "", err
	}
	if n != info.Size() {
		return 0, "", upgrade.ErrSourceChanged
	}
	return n, hex.EncodeToString(h.Sum(nil)), nil
}

func openReadOnlyBaseCopyOnce(ctx context.Context, databasePath, scratchDir string) (*BaseFileSnapshot, error) {
	beforeSize, beforeDigest, err := digestSingleLinkBaseFile(ctx, databasePath)
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(scratchDir, "cpamp-upgrade-base-snapshot-")
	if err != nil {
		return nil, fmt.Errorf("%w: %v", upgrade.ErrSnapshotFailed, err)
	}
	snap := &BaseFileSnapshot{dir: dir}
	copyPath := filepath.Join(dir, filepath.Base(databasePath))
	src, err := os.Open(databasePath)
	if err != nil {
		_ = snap.Close()
		return nil, err
	}
	dst, err := os.OpenFile(copyPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		_ = src.Close()
		_ = snap.Close()
		return nil, err
	}
	hasher := sha256.New()
	copiedBytes, copyErr := io.Copy(io.MultiWriter(dst, hasher), src)
	closeSrcErr := src.Close()
	closeDstErr := dst.Close()
	if err := errors.Join(copyErr, closeSrcErr, closeDstErr); err != nil {
		_ = snap.Close()
		return nil, err
	}
	afterSize, afterDigest, err := digestSingleLinkBaseFile(ctx, databasePath)
	if err != nil {
		_ = snap.Close()
		return nil, err
	}
	copiedDigest := hex.EncodeToString(hasher.Sum(nil))
	if beforeSize != afterSize || beforeDigest != afterDigest || copiedBytes != beforeSize || copiedDigest != beforeDigest {
		_ = snap.Close()
		return nil, upgrade.ErrSourceChanged
	}
	uriPath := filepath.ToSlash(copyPath)
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	dsn := &url.URL{Scheme: "file", Path: uriPath}
	query := dsn.Query()
	query.Set("mode", "ro")
	query.Add("_pragma", "query_only(1)")
	dsn.RawQuery = query.Encode()
	db, err := sql.Open("sqlite", dsn.String())
	if err != nil {
		_ = snap.Close()
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		_ = snap.Close()
		return nil, err
	}
	snap.db = db
	return snap, nil
}

func auditSidecarsMetadataOnly(databasePath string) error {
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		info, err := processlock.InspectRegularSingleLink(databasePath + suffix)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if suffix == "-journal" && info.Size() > 0 {
			return errors.New("upgrade_pending_rollback_journal")
		}
	}
	return nil
}

// OpenReadOnlyBaseCopyWithRetry stages a private read-only copy of only the
// base SQLite file outside target roots when sidecar permissions block full
// WAL snapshots.
func OpenReadOnlyBaseCopyWithRetry(ctx context.Context, databasePath, scratchDir string) (*BaseFileSnapshot, error) {
	cleaned := strings.TrimSpace(databasePath)
	if cleaned == "" {
		return nil, errors.New(CodeUnavailable)
	}
	if hasNonEmptyLockMarker(cleaned) {
		return nil, errors.New("upgrade_fresh_marker_present")
	}
	if err := upgrade.ValidateScratchDir(scratchDir, "", filepath.Dir(cleaned)); err != nil {
		return nil, err
	}
	if err := auditSidecarsMetadataOnly(cleaned); err != nil {
		return nil, err
	}
	delay := upgrade.SnapshotBaseDelay
	var lastErr error
	for attempt := range upgrade.SnapshotAttempts {
		snap, err := openReadOnlyBaseCopyOnce(ctx, cleaned, scratchDir)
		if err == nil {
			return snap, nil
		}
		if !errors.Is(err, upgrade.ErrSourceChanged) {
			return nil, err
		}
		lastErr = err
		if attempt == upgrade.SnapshotAttempts-1 {
			break
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
		if delay < upgrade.SnapshotMaxDelay {
			delay *= 2
			if delay > upgrade.SnapshotMaxDelay {
				delay = upgrade.SnapshotMaxDelay
			}
		}
	}
	return nil, lastErr
}

func loadPreparedCredentialFromDB(ctx context.Context, db *sql.DB) (model.AdminCredential, string, string, error) {
	cred, found, err := setting.New(db).LoadAdminCredential(ctx)
	if err != nil {
		return model.AdminCredential{}, "", "", err
	}
	if !found || !security.IsValidAdminCredential(cred) {
		return model.AdminCredential{}, "", "", errors.New("upgrade_admin_credential_unavailable")
	}
	op, opFound, opErr := upgrade.ReadRecordFrom(ctx, db)
	if opErr != nil {
		return model.AdminCredential{}, "", "", opErr
	}
	if !opFound || !ValidPreparedOperation(op) {
		return model.AdminCredential{}, "", "", errors.New("upgrade_operation_not_prepared")
	}
	sourceRev := strings.ToLower(strings.TrimSpace(op.SourceFingerprintAfter))
	return cred, sourceRev, OperationRevision(op), nil
}

func (v *PreparedCredentialVerifier) snapshotOpener() func(context.Context, string, string) (*upgrade.Snapshot, error) {
	if v != nil && v.openSnapshot != nil {
		return v.openSnapshot
	}
	return upgrade.OpenSnapshot
}

func (v *PreparedCredentialVerifier) load(ctx context.Context) (model.AdminCredential, string, string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if v == nil || v.databasePath == "" || hasNonEmptyLockMarker(v.databasePath) {
		return model.AdminCredential{}, "", "", errors.New(CodeUnavailable)
	}
	if err := upgrade.ValidateScratchDir(v.scratchDir, "", filepath.Dir(v.databasePath)); err != nil {
		return model.AdminCredential{}, "", "", err
	}
	snapshot, err := upgrade.OpenSnapshotWithRetry(ctx, v.snapshotOpener(), v.databasePath, v.scratchDir)
	if err != nil {
		return model.AdminCredential{}, "", "", err
	}
	defer snapshot.Close()
	return loadPreparedCredentialFromDB(ctx, snapshot.DB())
}

// HasPreparedIdentity admits only discovery of the upgrade surface. It does
// not cache or authorize credentials when WAL recovery was unavailable.
func (v *PreparedCredentialVerifier) HasPreparedIdentity(ctx context.Context) bool {
	if v == nil || v.databasePath == "" || hasNonEmptyLockMarker(v.databasePath) {
		return false
	}
	if err := upgrade.ValidateScratchDir(v.scratchDir, "", filepath.Dir(v.databasePath)); err != nil {
		return false
	}
	snapshot, err := upgrade.OpenSnapshotWithRetry(ctx, v.snapshotOpener(), v.databasePath, v.scratchDir)
	if err == nil {
		defer snapshot.Close()
		return ValidPreparedOperationInDB(ctx, snapshot.DB())
	}
	if !errors.Is(err, os.ErrPermission) {
		return false
	}
	base, err := OpenReadOnlyBaseCopyWithRetry(ctx, v.databasePath, v.scratchDir)
	if err != nil {
		return false
	}
	defer base.Close()
	return ValidPreparedOperationInDB(ctx, base.DB())
}

func extractInheritedCredential(headerOrKey string) (string, bool) {
	if strings.HasPrefix(headerOrKey, "Bearer ") || strings.HasPrefix(strings.ToLower(strings.TrimSpace(headerOrKey)), "bearer ") {
		return parseInheritedBearerHeader(headerOrKey)
	}
	if headerOrKey == "" || len(headerOrKey) > 4096 || strings.TrimSpace(headerOrKey) != headerOrKey || strings.ContainsAny(headerOrKey, " \t\r\n") {
		return "", false
	}
	return headerOrKey, true
}

// AuthenticateIdentity verifies key (either a "Bearer <key>" Authorization
// header or a pre-extracted inherited admin key) against a single private
// snapshot of the prepared target store and returns both source and operation
// revisions.
func (v *PreparedCredentialVerifier) AuthenticateIdentity(ctx context.Context, key string) (sourceRev string, operationRev string, err error) {
	extracted, ok := extractInheritedCredential(key)
	if !ok {
		return "", "", upgrade.ErrAdminUnauthorized
	}
	cred, sourceRev, operationRev, err := v.load(ctx)
	if err != nil {
		return "", "", err
	}
	if !security.VerifyAdminKey(cred, extracted) {
		return "", "", upgrade.ErrAdminUnauthorized
	}
	return sourceRev, operationRev, nil
}

func (v *PreparedCredentialVerifier) AuthenticateAndRevision(ctx context.Context, authHeader string) (string, error) {
	key, ok := parseInheritedBearerHeader(authHeader)
	if !ok {
		return "", upgrade.ErrAdminUnauthorized
	}
	cred, rev, _, err := v.load(ctx)
	if err != nil {
		return "", err
	}
	if !security.VerifyAdminKey(cred, key) {
		return "", upgrade.ErrAdminUnauthorized
	}
	return rev, nil
}

func (v *PreparedCredentialVerifier) Authenticate(ctx context.Context, authorization string) error {
	_, err := v.AuthenticateAndRevision(ctx, authorization)
	return err
}

func (v *PreparedCredentialVerifier) RecordedRevision(ctx context.Context) string {
	_, rev, _, err := v.load(ctx)
	if err != nil {
		return ""
	}
	return rev
}

type Client struct {
	http                            *http.Client
	uploadHTTP                      *http.Client
	PublicOrigin                    string
	FallbackAuthenticateIdentity    func(ctx context.Context, key string) (sourceRev string, operationRev string, err error)
	FallbackAuthenticateAndRevision func(ctx context.Context, authorization string) (string, error)
	FallbackAuthenticate            func(ctx context.Context, authorization string) error
	FallbackRevision                func(ctx context.Context) string
}

func FromEnvironment(publicOrigin string) *Client {
	path := strings.TrimSpace(os.Getenv(SocketEnvironment))
	if path == "" {
		return nil
	}
	return NewClient(path, publicOrigin)
}

func NewClient(socket, publicOrigin string) *Client {
	dial := func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "unix", socket)
	}
	noRedirect := func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	standardTransport := &http.Transport{
		DialContext:           dial,
		MaxConnsPerHost:       8,
		MaxIdleConnsPerHost:   2,
		IdleConnTimeout:       30 * time.Second,
		ResponseHeaderTimeout: 35 * time.Second,
	}
	uploadTransport := &http.Transport{
		DialContext:         dial,
		MaxConnsPerHost:     2,
		MaxIdleConnsPerHost: 1,
		IdleConnTimeout:     30 * time.Second,
	}
	return &Client{
		http:         &http.Client{Transport: standardTransport, Timeout: 40 * time.Second, CheckRedirect: noRedirect},
		uploadHTTP:   &http.Client{Transport: uploadTransport, Timeout: 16 * time.Minute, CheckRedirect: noRedirect},
		PublicOrigin: publicOrigin,
	}
}

func isEntryNamespace(path string) bool {
	return path == EntryPath || strings.HasPrefix(path, "/.well-known/cpamp-bootstrap/upgrade/") || path == "/.well-known/cpamp-bootstrap/upgrade"
}

func parseInheritedBearerHeader(header string) (string, bool) {
	if !strings.HasPrefix(header, "Bearer ") {
		return "", false
	}
	key := strings.TrimPrefix(header, "Bearer ")
	if key == "" || len(key) > 4096 || strings.TrimSpace(key) != key {
		return "", false
	}
	return key, true
}

// RequestAllowed applies the public credential-shape and origin gates before
// dialing the private socket. Entry routes receive cpamp.upgrade-auth.v1
// envelopes; wizard/execute routes receive cpamp.upgrade-execution.v1.
func RequestAllowed(w http.ResponseWriter, r *http.Request, origin string) bool {
	entry := r != nil && r.URL != nil && isEntryNamespace(r.URL.Path)
	values := r.Header.Values("Authorization")
	if len(values) != 1 {
		if entry {
			writeEntryError(w, http.StatusUnauthorized, CodeAdminUnauthorized)
		} else {
			writeError(w, http.StatusUnauthorized, CodeAdminUnauthorized, nil)
		}
		return false
	}
	if _, ok := parseInheritedBearerHeader(values[0]); !ok {
		if entry {
			writeEntryError(w, http.StatusUnauthorized, CodeAdminUnauthorized)
		} else {
			writeError(w, http.StatusUnauthorized, CodeAdminUnauthorized, nil)
		}
		return false
	}
	if upgradesurface.CrossSite(r, origin) {
		if entry {
			writeEntryOriginRejected(w, origin)
		} else {
			writeError(w, http.StatusForbidden, upgradesurface.CodeOriginRejected, nil)
		}
		return false
	}
	return true
}

func isArtifactUploadRequest(r *http.Request) bool {
	if r == nil || r.URL == nil || r.Method != http.MethodPut {
		return false
	}
	path := r.URL.Path
	const suffix = "/installation/upgrade/runtime/artifact/upload"
	return strings.HasSuffix(path, suffix) && path != suffix
}

// Forward proxies an already shape/origin-gated request to the private worker
// socket. It returns false without writing to w only when dialing or reading
// response headers from the private worker failed before any HTTP response was
// committed, allowing callers (such as upgradewizard status) to project a
// degraded unavailable state without folding transport failures into 401.
func (c *Client) Forward(w http.ResponseWriter, r *http.Request) bool {
	clone := r.Clone(r.Context())
	clone.RequestURI = ""
	clone.URL.Scheme, clone.URL.Host = "http", "upgrade-worker"
	clone.Header = make(http.Header)
	for _, name := range []string{"Authorization", "Origin", "Sec-Fetch-Site", "Content-Type", "Accept", "X-CPAMP-Upgrade-Source"} {
		for _, value := range r.Header.Values(name) {
			clone.Header.Add(name, value)
		}
	}
	if r.TLS != nil {
		clone.Header.Set("X-CPAMP-Worker-Scheme", "https")
	}
	client := c.http
	if isArtifactUploadRequest(r) {
		client = c.uploadHTTP
		clone.Body = http.MaxBytesReader(w, r.Body, artifactproxy.MaxUploadBytes+1024)
		clone.ContentLength = r.ContentLength
	} else {
		clone.Body = http.MaxBytesReader(w, r.Body, MaxRequestBytes)
	}
	response, err := client.Do(clone)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			if isArtifactUploadRequest(r) {
				writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{"code": "archive_too_large"})
			} else if isEntryNamespace(r.URL.Path) {
				writeEntryError(w, http.StatusBadRequest, "invalid_request")
			} else {
				writeError(w, http.StatusBadRequest, CodeRequestInvalid, nil)
			}
			return true
		}
		return false
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseProxyBytes+1))
	if err != nil || len(body) > maxResponseProxyBytes {
		if isEntryNamespace(r.URL.Path) {
			writeEntryError(w, http.StatusServiceUnavailable, "source_unavailable")
		} else {
			state := Execution{Code: CodeUnavailable}
			writeError(w, http.StatusServiceUnavailable, CodeUnavailable, &state)
		}
		return true
	}
	for _, name := range []string{"Content-Type", "Allow", "Cache-Control", "Referrer-Policy", "X-Content-Type-Options", "WWW-Authenticate", "Vary"} {
		if value := response.Header.Get(name); value != "" {
			w.Header().Set(name, value)
		}
	}
	w.WriteHeader(response.StatusCode)
	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
	return true
}

func (c *Client) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !RequestAllowed(w, r, c.PublicOrigin) {
		return
	}
	if c.Forward(w, r) {
		return
	}
	if isEntryNamespace(r.URL.Path) {
		c.serveEntryUnavailable(w, r)
		return
	}
	state := Execution{Code: CodeUnavailable}
	writeError(w, http.StatusServiceUnavailable, CodeUnavailable, &state)
}

func (c *Client) serveEntryUnavailable(w http.ResponseWriter, r *http.Request) {
	fallbackRev := ""
	opRev := ""
	if c.FallbackAuthenticateIdentity != nil {
		var err error
		fallbackRev, opRev, err = c.FallbackAuthenticateIdentity(r.Context(), r.Header.Get("Authorization"))
		if err != nil {
			if errors.Is(err, upgrade.ErrAdminUnauthorized) {
				writeEntryError(w, http.StatusUnauthorized, "admin_unauthorized")
				return
			}
			writeEntryError(w, http.StatusServiceUnavailable, "source_unavailable")
			return
		}
	} else if c.FallbackAuthenticateAndRevision != nil {
		var err error
		fallbackRev, err = c.FallbackAuthenticateAndRevision(r.Context(), r.Header.Get("Authorization"))
		if err != nil {
			if errors.Is(err, upgrade.ErrAdminUnauthorized) {
				writeEntryError(w, http.StatusUnauthorized, "admin_unauthorized")
				return
			}
			writeEntryError(w, http.StatusServiceUnavailable, "source_unavailable")
			return
		}
		if isLowerHex64(fallbackRev) {
			opRev = fallbackRev
		}
	} else if c.FallbackAuthenticate != nil {
		if err := c.FallbackAuthenticate(r.Context(), r.Header.Get("Authorization")); err != nil {
			if errors.Is(err, upgrade.ErrAdminUnauthorized) {
				writeEntryError(w, http.StatusUnauthorized, "admin_unauthorized")
				return
			}
			writeEntryError(w, http.StatusServiceUnavailable, "source_unavailable")
			return
		}
		if c.FallbackRevision != nil {
			fallbackRev = c.FallbackRevision(r.Context())
			if isLowerHex64(fallbackRev) {
				opRev = fallbackRev
			}
		}
	} else {
		writeEntryError(w, http.StatusServiceUnavailable, "source_unavailable")
		return
	}
	if r.URL.Path != EntryPath || r.URL.RawQuery != "" {
		setSecurityHeaders(w)
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeEntryError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	if r.ContentLength > 0 {
		writeEntryError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	if r.Body != nil {
		var buf [1]byte
		if n, err := r.Body.Read(buf[:]); n > 0 || (err != nil && !errors.Is(err, io.EOF)) {
			writeEntryError(w, http.StatusBadRequest, "invalid_request")
			return
		}
	}
	expected, ok := parseSourceRevisionHeader(r.Header.Values("X-CPAMP-Upgrade-Source"))
	if !ok {
		writeEntryError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	if !isLowerHex64(fallbackRev) {
		writeEntryError(w, http.StatusServiceUnavailable, "source_unavailable")
		return
	}
	if expected != "" && expected != fallbackRev {
		writeEntryError(w, http.StatusServiceUnavailable, "source_unavailable")
		return
	}
	doc := map[string]any{
		"contract_version":    EntryContract,
		"authenticated":       true,
		"source_revision":     fallbackRev,
		"status":              "observation_unavailable",
		"migration_available": false,
		"expires_in_seconds":  60,
	}
	if isLowerHex64(opRev) {
		doc["operation_revision"] = opRev
	}
	writeJSON(w, http.StatusOK, doc)
}

func parseSourceRevisionHeader(values []string) (string, bool) {
	if len(values) == 0 {
		return "", true
	}
	if len(values) != 1 {
		return "", false
	}
	raw := values[0]
	if len(raw) != 64 {
		return "", false
	}
	for i := range len(raw) {
		ch := raw[i]
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
			return "", false
		}
	}
	return raw, true
}

func (c *Client) VerifyHeader(ctx context.Context, authorization string) (bool, error) {
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://upgrade-worker"+EntryPath, nil)
	if err != nil {
		return false, err
	}
	r.Header.Set("Authorization", authorization)
	if normalized, normErr := upgradesurface.NormalizeOrigin(c.PublicOrigin); normErr == nil && normalized != "" {
		r.Header.Set("Origin", normalized)
	}
	response, err := c.http.Do(r)
	if err != nil {
		if c.FallbackAuthenticateIdentity != nil {
			_, _, authErr := c.FallbackAuthenticateIdentity(ctx, authorization)
			if authErr == nil {
				return true, nil
			}
			if errors.Is(authErr, upgrade.ErrAdminUnauthorized) {
				return false, nil
			}
		} else if c.FallbackAuthenticateAndRevision != nil {
			_, authErr := c.FallbackAuthenticateAndRevision(ctx, authorization)
			if authErr == nil {
				return true, nil
			}
			if errors.Is(authErr, upgrade.ErrAdminUnauthorized) {
				return false, nil
			}
		} else if c.FallbackAuthenticate != nil {
			authErr := c.FallbackAuthenticate(ctx, authorization)
			if authErr == nil {
				return true, nil
			}
			if errors.Is(authErr, upgrade.ErrAdminUnauthorized) {
				return false, nil
			}
		}
		return false, errors.New(CodeUnavailable)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized {
		return false, nil
	}
	if response.StatusCode != http.StatusOK {
		return false, errors.New(CodeUnavailable)
	}
	return true, nil
}

func setSecurityHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	_ = enc.Encode(body)
	setSecurityHeaders(w)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(buf.Len()))
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

func writeEntryError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]any{
		"contract_version": EntryContract,
		"error":            map[string]string{"code": code},
	})
}

func writeEntryOriginRejected(w http.ResponseWriter, publicOrigin string) {
	errBody := map[string]string{"code": upgradesurface.CodeOriginRejected}
	if normalized, err := upgradesurface.NormalizeOrigin(publicOrigin); err == nil && normalized != "" {
		errBody["expected_origin"] = normalized
	}
	writeJSON(w, http.StatusForbidden, map[string]any{
		"contract_version": EntryContract,
		"error":            errBody,
	})
}

func writeError(w http.ResponseWriter, status int, code string, execution *Execution) {
	body := map[string]any{
		"contractVersion": Contract,
		"code":            code,
		"error":           map[string]string{"code": code},
	}
	if execution != nil {
		body["execution"] = execution
	}
	writeJSON(w, status, body)
}
