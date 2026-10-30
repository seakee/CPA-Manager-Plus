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
	"regexp"
	"sort"
	"strconv"
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
// through a read-only snapshot and a bounded SQLite read; it must not hold a
// request open indefinitely. The credential read is bounded separately by
// upgradeservice.CredentialVerifyTimeout, which is far shorter: authentication
// runs on every request, the preflight only when the wizard asks for a report.
const DefaultTimeout = 30 * time.Second

// CodeAdminUnauthorized is the negative identity verdict. It is the only 401
// this package writes.
const CodeAdminUnauthorized = "admin_unauthorized"

// Typed environment codes. They are reported instead of a bare 500 so the
// wizard can name the condition and offer a retry.
const (
	CodeLegacyRootMissing  = "upgrade_legacy_root_missing"
	CodeLegacyRootUnusable = "upgrade_legacy_root_unusable"
	CodePreflightTimeout   = "upgrade_preflight_timeout"
	CodePreflightFailed    = "upgrade_preflight_failed"
	CodeOperationUnread    = "upgrade_operation_unreadable"
	// CodeCredentialTimeout is the credential read's deadline verdict: the source
	// exists but did not answer in time. It is not an unreadable source and never
	// a wrong key.
	CodeCredentialTimeout = upgradeservice.CodeCredentialTimeout
	// CodeSourceSymlinked is the shared database file policy's refusal of a source
	// reached through a link. The preflight reports the same code for the same
	// mount.
	CodeSourceSymlinked = upgradeservice.CodeSourceSymlinked
	// CodeSourceMissing and CodeSourceUnreadable are the same codes the
	// preflight reports: the source itself is absent or cannot be read, so this
	// step can neither prove the key nor report on the source. They are never a
	// verdict on the credential, and they are never the ownership marker —
	// authentication does not inspect that marker at all.
	CodeSourceMissing    = upgradeservice.CodeSourceMissing
	CodeSourceUnreadable = upgradeservice.CodeSourceUnreadable
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
//
// Authentication is the Upgrade surface's own credential: the source
// administrator key the bootstrap Upgrade entry already verified, presented as
// `Authorization: Bearer <key>`. Each request proves it through
// upgradeservice.VerifyAdmin, which reads the configured 1.x source read-only —
// the credential row only, never a whole-file integrity check, because this runs
// on every request including every status poll. No session, cookie or Fresh
// claim is a substitute, and an absent decision cannot read as allow.
type Handler struct {
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
	if r == nil || r.URL == nil {
		http.NotFound(w, r)
		return
	}
	// The credential comes first on every surface order. An unauthenticated
	// caller therefore learns nothing about which resources exist or which verb
	// they take: an absent, wrong or unreadable credential is the only answer it
	// ever gets, whether the path is real, unknown, or read with the wrong
	// method.
	if !h.authenticate(w, r) {
		return
	}
	if r.URL.RawQuery != "" {
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
	if preflight {
		h.servePreflight(w, r)
		return
	}
	h.serveStatus(w, r)
}

// authenticate proves the caller holds the source administrator credential. The
// key is bounded and trimmed here; nothing else about the request (a session
// header, a cookie, an origin) can stand in for it.
func (h Handler) authenticate(w http.ResponseWriter, r *http.Request) bool {
	key, ok := bearerKey(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, CodeAdminUnauthorized)
		return false
	}
	root, status, code := h.legacyRoot()
	if status != 0 {
		writeError(w, status, code)
		return false
	}
	// VerifyAdmin bounds its own read: this runs on every request, including
	// every status poll, and reads the credential row only.
	switch err := upgradeservice.VerifyAdmin(r.Context(), root, key); {
	case err == nil:
		return true
	case errors.Is(err, upgradeservice.ErrAdminUnauthorized):
		writeError(w, http.StatusUnauthorized, CodeAdminUnauthorized)
	case errors.Is(err, upgradeservice.ErrCredentialVerifyTimeout):
		// A deadline is not an unreadable source: the volume may be healthy and
		// only slow, and the operator's next move is to retry, not to repair it.
		writeError(w, http.StatusServiceUnavailable, CodeCredentialTimeout)
	case errors.Is(err, upgradeservice.ErrSourceDatabaseMissing):
		writeError(w, http.StatusServiceUnavailable, CodeSourceMissing)
	case errors.Is(err, upgradeservice.ErrSourceSymlinked):
		// The preflight refuses the same mount with the same code: both paths read
		// the file itself, never whatever a link points at.
		writeError(w, http.StatusServiceUnavailable, CodeSourceSymlinked)
	default:
		// The credential is not wrong: the source could not be read to verify it.
		// Naming that condition keeps a wrong-password message off a mount the
		// operator has to fix first.
		writeError(w, http.StatusServiceUnavailable, CodeSourceUnreadable)
	}
	return false
}

// bearerKey extracts the single bounded Bearer value. Anything else — no header,
// two headers, another scheme, an empty or padded key — is not a credential.
func bearerKey(r *http.Request) (string, bool) {
	if r == nil {
		return "", false
	}
	values := r.Header.Values("Authorization")
	if len(values) != 1 || !strings.HasPrefix(values[0], "Bearer ") {
		return "", false
	}
	key := strings.TrimPrefix(values[0], "Bearer ")
	if key == "" || len(key) > 4096 || strings.TrimSpace(key) != key {
		return "", false
	}
	return key, true
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
	// Manual items are operator instructions written by another step, so they
	// pass the same content scrubber as the error detail: a copy-pasted command
	// can carry a credential, and a path in an instruction is still a path.
	document.ManualItems = scrubList(decodeStrings(row.ManualItems))
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

// scrubList applies the content scrubber to every item and keeps the list shape.
func scrubList(values []string) []string {
	scrubbed := make([]string, 0, len(values))
	for _, value := range values {
		scrubbed = append(scrubbed, scrubError(value))
	}
	return scrubbed
}

// Operator-visible strings are scrubbed by content, not by path shape: a
// credential carries no path, and an error written by another step can quote one
// without naming a file. A multi-line detail is evidence too, so it is scrubbed
// line by line instead of being dropped.
const (
	redactedText   = "<redacted>"
	sourceText     = "<source>"
	maxDetailLine  = 600
	maxDetailLines = 40
	// minProductKeyBody is the shortest key body this product issues after its
	// prefix. The key pattern and the wrapped-key rule both read it.
	minProductKeyBody = 4
)

var (
	// bearerTokenPattern is the credential half of a Bearer value.
	bearerTokenPattern = regexp.MustCompile(`(?i)(bearer\s+)[^\s,;"'()<>]+`)
	// basicCredentialPattern is an Authorization: Basic value, which is a
	// base64-encoded password: it is a credential in a different alphabet.
	basicCredentialPattern = regexp.MustCompile(`(?i)(authorization\s*:\s*basic\s+)[A-Za-z0-9+/=]{4,}`)
	// apiKeyHeaderPattern is an x-api-key header, assignment or env-style name.
	apiKeyHeaderPattern = regexp.MustCompile(`(?i)(x[-_]api[-_]key\s*[:=]\s*)("[^"]*"|'[^']*'|[^\s,;]+)`)
	// issuedKeyPattern is the issued key prefix the Manager and its clients use.
	issuedKeyPattern = regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{4,}`)
	// productKeyPrefixes and productKeyPattern are this product's own key
	// vocabulary: installation, usage and administrator keys all start with one
	// of these prefixes and carry no other marker. The prefix match is
	// case-insensitive, because a key pasted with a different prefix case is
	// still a credential, and the same list is what a key wrapped across a line
	// boundary is recognised by.
	productKeyPrefixes = []string{"cpamp_", "cmp_admin_"}
	productKeyPattern  = regexp.MustCompile(
		`(?i)\b(?:` + strings.Join(productKeyPrefixes, "|") + `)[A-Za-z0-9_.\-]{` + strconv.Itoa(minProductKeyBody) + `,}`)
	// passwordPattern is a password written as an assignment: a query string, a
	// DSN, an env-style variable or a copied command.
	passwordPattern = regexp.MustCompile(`(?i)(password\s*[=:]\s*)("[^"]*"|'[^']*'|[^\s,;&]+)`)
	// urlCredentialPattern is a URL whose authority embeds credentials.
	urlCredentialPattern = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.\-]*://)[^/\s:@]+(:[^/\s@]*)?@`)
	// localPathPattern is an absolute POSIX path at a token boundary. The colon
	// of a URL scheme is not a boundary, so a remote URL is never a local path.
	localPathPattern = regexp.MustCompile(`(^|[\s"'(=])/[^\s"'),;:]+`)
	// windowsPathPattern is a drive-letter or UNC path at a token boundary.
	windowsPathPattern = regexp.MustCompile(`(^|[\s"'(=])(?:[A-Za-z]:\\[^\s"'),;]+|\\\\[^\s"'),;]+)`)
)

// scrubError keeps an operation's own error detail while removing host paths and
// credential material, so an error written by another step cannot become a path
// or secret disclosure here.
func scrubError(detail string) string {
	return newScrubber("").text(detail)
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

// detail scrubs one operator-visible preflight detail.
func (s scrubber) detail(detail string) string {
	return s.text(detail)
}

// text scrubs a possibly multi-line value: the configured source paths first,
// then credential material and local paths, then the per-line and total bounds.
func (s scrubber) text(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	lines := strings.Split(strings.ReplaceAll(trimmed, "\r\n", "\n"), "\n")
	// A credential wrapped across two lines is still one credential: a lone
	// trailing `Bearer` and the line after it are joined before the patterns
	// see them. Nothing else is joined — a line break is evidence.
	lines = joinSplitBearer(lines)
	scrubbed := make([]string, 0, len(lines))
	// A product key broken mid-token is one credential too. The line that carries
	// the head is redacted by the key pattern, and the fragment on the next line
	// carries no prefix of its own, so the run that continues it is removed there
	// instead. The line break itself stays: it is still evidence of shape.
	openKey := false
	for _, line := range lines {
		line = strings.ReplaceAll(line, "\x00", "")
		if openKey {
			line = redactKeyTail(line)
		}
		openKey = endsWithOpenProductKey(line)
		for _, token := range s.replacements {
			if token == "" || token == "/" || token == "." {
				continue
			}
			line = strings.ReplaceAll(line, token, sourceText)
		}
		line = scrubCredentials(line)
		if len(line) > maxDetailLine {
			line = capText(line, maxDetailLine)
		}
		scrubbed = append(scrubbed, line)
	}
	if len(scrubbed) > maxDetailLines {
		scrubbed = scrubbed[:maxDetailLines]
	}
	return strings.Join(scrubbed, "\n")
}

// scrubCredentials redacts the credential shapes an error can quote. URL
// credentials go first so the userinfo cannot be read as part of a token.
func scrubCredentials(line string) string {
	line = urlCredentialPattern.ReplaceAllString(line, "$1"+redactedText+"@")
	line = basicCredentialPattern.ReplaceAllString(line, "$1"+redactedText)
	line = bearerTokenPattern.ReplaceAllString(line, "$1"+redactedText)
	line = apiKeyHeaderPattern.ReplaceAllString(line, "$1"+redactedText)
	line = productKeyPattern.ReplaceAllString(line, redactedText)
	line = issuedKeyPattern.ReplaceAllString(line, redactedText)
	line = passwordPattern.ReplaceAllString(line, "$1"+redactedText)
	line = windowsPathPattern.ReplaceAllString(line, "$1"+sourceText)
	line = localPathPattern.ReplaceAllString(line, "$1"+sourceText)
	return line
}

// joinSplitBearer joins a line whose last word is a bare `Bearer` with the line
// that carries its value, so a value wrapped onto the next line is redacted
// instead of surviving as an unlabelled secret.
func joinSplitBearer(lines []string) []string {
	joined := make([]string, 0, len(lines))
	for _, line := range lines {
		if len(joined) != 0 && endsWithBareBearer(joined[len(joined)-1]) {
			joined[len(joined)-1] += " " + strings.TrimSpace(line)
			continue
		}
		joined = append(joined, line)
	}
	return joined
}

// endsWithBareBearer reports whether a line ends with the scheme word and no
// value after it.
func endsWithBareBearer(line string) bool {
	trimmed := strings.ToLower(strings.TrimRight(line, " \t"))
	return trimmed == "bearer" || strings.HasSuffix(trimmed, " bearer")
}

// redactKeyTail removes the leading key run of a line whose predecessor ended
// inside a product key: that run is the rest of the token, so it is credential
// material even though it carries no prefix of its own. A run shorter than the
// shortest key body is left alone — it is not a fragment this product issues.
func redactKeyTail(line string) string {
	trimmed := strings.TrimLeft(line, " \t")
	run := 0
	for _, r := range trimmed {
		if !isKeyRune(r) {
			break
		}
		run += len(string(r))
	}
	if run < minProductKeyBody {
		return line
	}
	return redactedText + trimmed[run:]
}

// endsWithOpenProductKey reports whether a line ends inside a product key: the
// text from the last prefix to the end of the line is nothing but key
// characters, so the next line may carry the rest of the same token.
func endsWithOpenProductKey(line string) bool {
	trimmed := strings.TrimRight(line, " \t")
	lower := strings.ToLower(trimmed)
	index := -1
	for _, prefix := range productKeyPrefixes {
		if at := strings.LastIndex(lower, prefix); at > index {
			index = at
		}
	}
	return index >= 0 && isKeyFragment(trimmed[index:])
}

// isKeyFragment reports whether a value is nothing but key characters.
func isKeyFragment(value string) bool {
	for _, r := range value {
		if !isKeyRune(r) {
			return false
		}
	}
	return value != ""
}

// isKeyRune reports whether r can appear in a product key body.
func isKeyRune(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return true
	case r == '_', r == '.', r == '-':
		return true
	default:
		return false
	}
}

// capText truncates on a rune boundary so a cut never produces invalid text.
func capText(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "…"
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
