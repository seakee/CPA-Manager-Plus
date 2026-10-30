// Package upgradewizard serves the two read-only routes of the Full Web Upgrade
// wizard: the preflight report and the persisted operation projection.
//
// Nothing here advances the operation, opens a writable store, migrates, or
// reads a credential. The status projection selects named columns, so a value
// this package never selected (a source fingerprint, a backup digest, a data
// key, a filesystem path) cannot reach the browser even if the row carries it.
package upgradewizard

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	// The projection opens the Manager store read-only through database/sql.
	_ "modernc.org/sqlite"

	upgradeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/upgrade"
)

// Contract is the wire contract of both routes.
const Contract = "cpamp.upgrade-wizard.v1"

// Route suffixes. Each surface mounts them under its own prefix: the fixed
// bootstrap API while the installation is unfinished, and the canonical
// continuation prefix before the product is complete.
const (
	PreflightSuffix = "/installation/upgrade/preflight"
	StatusSuffix    = "/installation/upgrade/status"
)

// DefaultTimeout bounds one read-only preflight. A 1.x source is inspected
// under a lock probe and a bounded SQLite snapshot; it must not hold a request
// open indefinitely.
const DefaultTimeout = 30 * time.Second

// Typed environment codes. They are reported instead of a bare 500 so the
// wizard can name the condition and offer a retry.
const (
	CodeLegacyRootMissing  = "upgrade_legacy_root_missing"
	CodeLegacyRootUnusable = "upgrade_legacy_root_unusable"
	CodePreflightTimeout   = "upgrade_preflight_timeout"
	CodePreflightFailed    = "upgrade_preflight_failed"
	CodeOperationUnread    = "upgrade_operation_unreadable"
)

// Stages the projection understands. The list is deliberately a string
// allow-list rather than model.UpgradeStage: a row written by a newer build
// (for example a rolled_back stage) must still be readable, and the model
// validator would reject it, turning a real observation into a 500.
const (
	StagePreflight  = "preflight"
	StageCopyIn     = "copy_in"
	StageReconcile  = "reconcile"
	StageCPAImport  = "cpa_import"
	StageExternal   = "external_connect"
	StageVerify     = "verify"
	StageComplete   = "complete"
	StageFailed     = "failed"
	StageRolledBack = "rolled_back"
)

// NextAction is the single next decision the wizard may offer.
const (
	ActionPreflight  = "preflight"
	ActionCopyIn     = "copy_in"
	ActionReconcile  = "reconcile"
	ActionComplete   = "complete"
	ActionRolledBack = "rolled_back"
)

// Handler serves both routes. It has no write path at all.
type Handler struct {
	// Authorize refuses a request with a non-zero status. The two surfaces
	// have different credentials (the installation session before cutover, the
	// canonical continuation while the product is incomplete), so the caller
	// owns that decision. A nil Authorize refuses everything.
	Authorize func(*http.Request) int
	// LegacyRoot returns the configured 1.x data directory. It is read only.
	LegacyRoot func() string
	// Database returns the Manager database path that holds the operation row.
	Database func() string
	// Timeout bounds one preflight. Zero uses DefaultTimeout.
	Timeout time.Duration
}

func (h Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r == nil || r.URL == nil || r.URL.RawQuery != "" {
		http.NotFound(w, r)
		return
	}
	path := r.URL.Path
	var preflight bool
	switch {
	case strings.HasSuffix(path, PreflightSuffix) && path != PreflightSuffix:
		preflight = true
	case strings.HasSuffix(path, StatusSuffix) && path != StatusSuffix:
	default:
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET")
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	if h.Authorize == nil {
		writeError(w, http.StatusUnauthorized, "admin_unauthorized")
		return
	}
	if status := h.Authorize(r); status != 0 {
		writeError(w, status, "admin_unauthorized")
		return
	}
	if preflight {
		h.servePreflight(w, r)
		return
	}
	h.serveStatus(w, r)
}

// preflightCheck mirrors one Upgrade preflight observation. Detail is free text
// and is scrubbed of host paths before it leaves this package.
type preflightCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Code   string `json:"code,omitempty"`
	Detail string `json:"detail,omitempty"`
}

type preflightGaps struct {
	DeletedEventRefs      int      `json:"deletedEventRefs"`
	MissingProjection     int      `json:"missingProjection"`
	MismatchedProjection  int      `json:"mismatchedProjection"`
	PastWatermark         int      `json:"pastWatermark"`
	ProjectionStateUsable bool     `json:"projectionStateUsable"`
	CoverageDaysWithGaps  int      `json:"coverageDaysWithGaps"`
	AffectedDates         []string `json:"affectedDates"`
}

type preflightDocument struct {
	ContractVersion string           `json:"contractVersion"`
	SourceKind      string           `json:"sourceKind"`
	SourceVersion   string           `json:"sourceVersion"`
	Fingerprint     string           `json:"fingerprint,omitempty"`
	Checks          []preflightCheck `json:"checks"`
	ParityGaps      preflightGaps    `json:"parityGaps"`
	Blocked         []string         `json:"blocked"`
	Ready           bool             `json:"ready"`
}

func (h Handler) servePreflight(w http.ResponseWriter, r *http.Request) {
	root, status, code := h.legacyRoot()
	if status != 0 {
		writeError(w, status, code)
		return
	}
	timeout := h.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	report, err := upgradeservice.Preflight(ctx, upgradeservice.Options{LegacyRoot: root})
	if err != nil {
		if ctx.Err() != nil {
			writeError(w, http.StatusServiceUnavailable, CodePreflightTimeout)
			return
		}
		writeError(w, http.StatusServiceUnavailable, CodePreflightFailed)
		return
	}
	scrub := newScrubber(root)
	checks := make([]preflightCheck, 0, len(report.Checks))
	for _, check := range report.Checks {
		checks = append(checks, preflightCheck{
			Name:   check.Name,
			Status: check.Status,
			Code:   check.Code,
			Detail: scrub.detail(check.Detail),
		})
	}
	blocked := append([]string{}, report.Blocked...)
	if blocked == nil {
		blocked = []string{}
	}
	dates := append([]string{}, report.ParityGaps.AffectedDates...)
	if dates == nil {
		dates = []string{}
	}
	writeJSON(w, http.StatusOK, preflightDocument{
		ContractVersion: Contract,
		SourceKind:      report.SourceKind,
		SourceVersion:   report.SourceVersion,
		Fingerprint:     abbreviate(report.Fingerprint),
		Checks:          checks,
		ParityGaps: preflightGaps{
			DeletedEventRefs:      report.ParityGaps.DeletedEventRefs,
			MissingProjection:     report.ParityGaps.MissingProjection,
			MismatchedProjection:  report.ParityGaps.MismatchedProjection,
			PastWatermark:         report.ParityGaps.PastWatermark,
			ProjectionStateUsable: report.ParityGaps.ProjectionStateUsable,
			CoverageDaysWithGaps:  report.ParityGaps.CoverageDaysWithGaps,
			AffectedDates:         dates,
		},
		Blocked: blocked,
		Ready:   report.Ready,
	})
}

func (h Handler) legacyRoot() (string, int, string) {
	root := ""
	if h.LegacyRoot != nil {
		root = strings.TrimSpace(h.LegacyRoot())
	}
	if root == "" {
		return "", http.StatusServiceUnavailable, CodeLegacyRootMissing
	}
	info, err := os.Stat(root)
	switch {
	case os.IsNotExist(err):
		return "", http.StatusServiceUnavailable, CodeLegacyRootMissing
	case err != nil:
		return "", http.StatusServiceUnavailable, CodeLegacyRootUnusable
	case !info.IsDir():
		return "", http.StatusServiceUnavailable, CodeLegacyRootUnusable
	}
	return root, 0, ""
}

// statusDocument is the operation row projection. Every field is either a
// bounded count, an enumerated stage, or a string the operation itself
// recorded; no fingerprint, digest or path has a field here on purpose.
type statusDocument struct {
	ContractVersion string          `json:"contractVersion"`
	Recorded        bool            `json:"recorded"`
	Stage           string          `json:"stage"`
	StageKnown      bool            `json:"stageKnown"`
	NextAction      string          `json:"nextAction"`
	DomainsDone     []string        `json:"domainsDone"`
	SourceKind      string          `json:"sourceKind,omitempty"`
	SourceVersion   string          `json:"sourceVersion,omitempty"`
	Reconciliation  json.RawMessage `json:"reconciliation"`
	ManualItems     []string        `json:"manualItems"`
	ErrorCode       string          `json:"errorCode,omitempty"`
	ErrorDetail     string          `json:"errorDetail,omitempty"`
	UpdatedAtMS     int64           `json:"updatedAtMs"`
}

func (h Handler) serveStatus(w http.ResponseWriter, r *http.Request) {
	database := ""
	if h.Database != nil {
		database = strings.TrimSpace(h.Database())
	}
	if database == "" {
		writeError(w, http.StatusServiceUnavailable, CodeOperationUnread)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	document, err := readStatus(ctx, database)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, CodeOperationUnread)
		return
	}
	writeJSON(w, http.StatusOK, document)
}

type statusRow struct {
	Stage         string
	Domains       string
	SourceKind    sql.NullString
	SourceVersion sql.NullString
	Reconciled    string
	ManualItems   string
	ErrorCode     sql.NullString
	ErrorDetail   sql.NullString
	UpdatedAtMS   int64
}

// readStatus reads the single operation row without the model validator: a
// stage this build does not know (a newer build's rolled_back, or a failed
// attempt) is still an honest observation and must reach the operator.
func readStatus(ctx context.Context, databasePath string) (statusDocument, error) {
	document := statusDocument{
		ContractVersion: Contract,
		StageKnown:      true,
		NextAction:      ActionPreflight,
		DomainsDone:     []string{},
		ManualItems:     []string{},
		Reconciliation:  json.RawMessage("{}"),
	}
	info, err := os.Stat(databasePath)
	if os.IsNotExist(err) {
		return document, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		return document, nil
	}
	db, err := sql.Open("sqlite", readOnlyDataSourceName(databasePath))
	if err != nil {
		return statusDocument{}, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	defer func() { _ = db.Close() }()
	var present int
	if err := db.QueryRowContext(ctx, `select count(*) from sqlite_master where type = 'table' and name = 'upgrade_operations'`).Scan(&present); err != nil {
		return statusDocument{}, err
	}
	if present == 0 {
		return document, nil
	}
	var row statusRow
	err = db.QueryRowContext(ctx, `select stage, domains_done, source_kind, source_version,
		reconciliation, manual_items, error_code, error_detail, updated_at_ms
		from upgrade_operations where id = 1`).Scan(&row.Stage, &row.Domains, &row.SourceKind, &row.SourceVersion,
		&row.Reconciled, &row.ManualItems, &row.ErrorCode, &row.ErrorDetail, &row.UpdatedAtMS)
	if errors.Is(err, sql.ErrNoRows) {
		return document, nil
	}
	if err != nil {
		return statusDocument{}, err
	}
	document.Recorded = true
	document.Stage = row.Stage
	document.StageKnown = knownStage(row.Stage)
	document.NextAction = nextAction(row.Stage)
	document.DomainsDone = decodeStrings(row.Domains)
	document.ManualItems = decodeStrings(row.ManualItems)
	document.SourceKind = row.SourceKind.String
	document.SourceVersion = row.SourceVersion.String
	document.ErrorCode = row.ErrorCode.String
	document.ErrorDetail = scrubError(row.ErrorDetail.String)
	document.UpdatedAtMS = row.UpdatedAtMS
	document.Reconciliation = reconciliationSummary(row.Reconciled)
	return document, nil
}

// knownStage reports whether this build can name the recorded stage. An empty
// stage is the unrecorded operation — the wizard's own first step — so it is
// known; anything else outside the table was written by a newer build and must
// be shown verbatim instead of guessed at.
func knownStage(stage string) bool {
	switch stage {
	case "", StagePreflight, StageCopyIn, StageReconcile, StageCPAImport, StageExternal,
		StageVerify, StageComplete, StageFailed, StageRolledBack:
		return true
	default:
		return false
	}
}

// nextAction maps a stage onto the wizard's next decision. A failed operation
// resumes at reconciliation, and a stage this build does not know keeps the
// empty string: the caller must show the recorded stage verbatim rather than
// claim a step that may not be the real position.
func nextAction(stage string) string {
	switch stage {
	case "":
		return ActionPreflight
	case StagePreflight:
		return ActionPreflight
	case StageCopyIn:
		return ActionCopyIn
	case StageReconcile, StageCPAImport, StageExternal, StageVerify, StageFailed:
		return ActionReconcile
	case StageComplete:
		return ActionComplete
	case StageRolledBack:
		return ActionRolledBack
	default:
		return ""
	}
}

// reconciliationSummary projects the operation's reconciliation output through
// a fixed allow-list: only the days it names. A producer's free-form payload
// never reaches the browser wholesale.
func reconciliationSummary(raw string) json.RawMessage {
	empty := json.RawMessage("{}")
	if strings.TrimSpace(raw) == "" {
		return empty
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &object); err != nil {
		return empty
	}
	seen := map[string]bool{}
	days := []string{}
	for _, key := range []string{"mismatchDays", "affectedDates"} {
		value, ok := object[key]
		if !ok {
			continue
		}
		var list []string
		if err := json.Unmarshal(value, &list); err != nil {
			continue
		}
		for _, day := range list {
			if !isDate(day) || seen[day] {
				continue
			}
			seen[day] = true
			days = append(days, day)
		}
	}
	sort.Strings(days)
	if len(days) > 366 {
		days = days[:366]
	}
	encoded, err := json.Marshal(map[string]any{"mismatchDays": days})
	if err != nil {
		return empty
	}
	return json.RawMessage(encoded)
}

func isDate(value string) bool {
	if len(value) != 10 {
		return false
	}
	for index, r := range value {
		if index == 4 || index == 7 {
			if r != '-' {
				return false
			}
			continue
		}
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func decodeStrings(raw string) []string {
	values := []string{}
	if strings.TrimSpace(raw) == "" {
		return values
	}
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return []string{}
	}
	if values == nil {
		return []string{}
	}
	return values
}

// scrubError keeps an operation's own error detail but drops anything that
// looks like a host path, so an error written by another step cannot become a
// path disclosure here.
func scrubError(detail string) string {
	trimmed := strings.TrimSpace(detail)
	if trimmed == "" {
		return ""
	}
	if strings.ContainsAny(trimmed, "\x00\n\r") {
		return ""
	}
	for _, token := range strings.Fields(trimmed) {
		if strings.HasPrefix(token, "/") || strings.Contains(token, "\\\\") {
			return "operation reported an error that referenced a local path"
		}
	}
	if len(trimmed) > 512 {
		return trimmed[:512]
	}
	return trimmed
}

type scrubber struct{ replacements []string }

func newScrubber(root string) scrubber {
	s := scrubber{}
	if root != "" {
		s.replacements = append(s.replacements, root, filepath.Dir(root))
	}
	sort.Slice(s.replacements, func(i, j int) bool {
		return len(s.replacements[i]) > len(s.replacements[j])
	})
	return s
}

func (s scrubber) detail(detail string) string {
	out := detail
	for _, token := range s.replacements {
		if token == "" || token == "/" || token == "." {
			continue
		}
		out = strings.ReplaceAll(out, token, "<source>")
	}
	return out
}

// abbreviate keeps the short form the console shows elsewhere: enough to tell
// two sources apart, never the full digest.
func abbreviate(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	if len(trimmed) > 12 {
		return trimmed[:12] + "…"
	}
	return trimmed
}

// readOnlyDataSourceName mirrors the bounded admission DSN: read-only, no
// pragma that persists state, immutable only when there is no live WAL.
func readOnlyDataSourceName(path string) string {
	immutable := true
	if info, err := os.Lstat(path + "-wal"); err == nil && info.Size() > 0 {
		immutable = false
	}
	uriPath := filepath.ToSlash(path)
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	dsn := &url.URL{Scheme: "file", Path: uriPath}
	query := dsn.Query()
	query.Set("mode", "ro")
	query.Add("_pragma", "query_only(1)")
	if immutable {
		query.Set("immutable", "1")
	}
	dsn.RawQuery = query.Encode()
	return dsn.String()
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]any{
		"contractVersion": Contract,
		"error":           map[string]string{"code": code},
	})
}
