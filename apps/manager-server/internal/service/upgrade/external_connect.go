package upgrade

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	managersqlite "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/upgradeoperation"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeyapply"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/upgrade/copyinmarker"
)

// External connect is the Upgrade step that carries an External install past the
// upgrade: the CPA behind that pointer is administered by the owner and may
// still be a v7, whose management surface is /v0 while everything this Manager
// writes speaks the v8 /v8 contract. The step therefore has exactly two
// outcomes.
//
// A v8 CPA is connected: the management key authenticates, the version is the
// contract this Manager writes, the model surface answers, and the connection's
// identity and generation are recorded so a later rebind can tell the target
// moved.
//
// A v7 CPA stops. Nothing is connected, the operation row keeps the stage it
// had, the typed refusal names the version that was found and the action the
// owner takes — upgrade the CPA to v8 first — and there is no "continue anyway":
// half-connecting to a /v0 surface would write the v2 configuration into a
// contract that cannot hold it.
//
// The step never invents a version detector or a second set of External codes.
// It reuses cpa.ClassifyExternalVersion for the read/write split and the Fresh
// External vocabulary for the management key, the identity, the generation and
// the moved target, so an upgrade refusal and a live External refusal say the
// same words about the same CPA.

const (
	// externalConnectEvidenceFile is the durable record of one successful
	// connect, written inside the copy-in work directory. It is evidence, not
	// authority: the operation row's stage is what authorizes later steps, and
	// this file only lets a rerun answer "already connected to this target"
	// without contacting the CPA or rewriting the row.
	externalConnectEvidenceFile = "external-connect.json"
	// externalConnectEvidenceVersion is the evidence contract. An unknown
	// version is refused rather than guessed.
	externalConnectEvidenceVersion = 1
	// externalModelSurfaceLimit bounds the model list read.
	externalModelSurfaceLimit = 1 << 20
	// externalConnectAction is the action a version refusal names. It is part of
	// the refusal's operator text, not a suggestion: there is no way past the
	// stop except a CPA that serves the v8 contract.
	externalConnectAction = "upgrade the CPA to v8 first, then run the step again"
	// externalConnectUnknownVersion is how a refusal names a CPA that answered
	// without a version and that the operator did not declare one for.
	externalConnectUnknownVersion = "unknown"
)

// The two detail strings are the shared External rebind wording, so the owner
// reads the same instruction on the upgrade page as on the rebind page. A ban
// expires; a rotated key does not.
const (
	externalConnectForbiddenDetail = "the CPA replaced this key or is refusing this client; if its address is banned, wait out the ban window and then connect with the current key"
	externalConnectRejectedDetail  = "the CPA does not accept this management key; if it was never rotated, wait out any ban window and connect with the current key"
)

// ExternalConnectEvidence is the record of one connected External target. It
// carries no credential: the address digest (the identity) and the generation
// are the comparable facts, exactly as freshkeyapply derives them.
type ExternalConnectEvidence struct {
	Version      int    `json:"version"`
	BaseURL      string `json:"baseUrl"`
	Identity     string `json:"identity"`
	Generation   string `json:"generation"`
	CPAVersion   string `json:"cpaVersion,omitempty"`
	Support      string `json:"support"`
	ClientKeys   int    `json:"clientKeys"`
	ObservedAtMS int64  `json:"observedAtMs"`
}

// ExternalConnectOptions is one invocation of the External connect step.
type ExternalConnectOptions struct {
	// TargetRoot is the v2 data directory holding usage.sqlite and the copy-in
	// work area.
	TargetRoot string
	// BaseURL is the address of the owner's CPA.
	BaseURL string
	// ManagementKey is the credential the owner supplied for that CPA. It is
	// used for the two authenticated reads and never recorded.
	ManagementKey string
	// CPAVersion is the version the operator declares for the target. It is
	// only consulted when the CPA answers without a version header, so a probe
	// that reads a version is never overridden by a declaration.
	CPAVersion string

	// observeExternalVersion is the version probe seam. Production leaves it
	// nil and cpa.ObserveExternalVersion runs on the transport every External
	// write uses.
	observeExternalVersion func(ctx context.Context, baseURL, managementKey string) (cpa.ExternalVersionReport, error)
	// readExternalKeys is the authenticated client-key read seam.
	readExternalKeys func(ctx context.Context, baseURL, managementKey string) (cpa.ExternalKeySnapshot, error)
	// proveModelSurface is the "does the gateway answer" seam.
	proveModelSurface func(ctx context.Context, baseURL, clientKey string) error
	// externalIdentity is the identity seam; production derives the address
	// digest through freshkeyapply.
	externalIdentity func(baseURL string) string
}

// ExternalConnectResult is the observable outcome of the step. It reports what
// was recorded, never an authority: Stage and DomainsDone are the persisted row.
type ExternalConnectResult struct {
	Stage           model.UpgradeStage `json:"stage"`
	DomainsDone     []string           `json:"domainsDone"`
	CPAVersion      string             `json:"cpaVersion,omitempty"`
	Support         string             `json:"support,omitempty"`
	Identity        string             `json:"identity,omitempty"`
	Generation      string             `json:"generation,omitempty"`
	ClientKeys      int                `json:"clientKeys"`
	AlreadyComplete bool               `json:"alreadyComplete"`
	UpdatedAtMS     int64              `json:"updatedAtMs"`
}

// ExternalConnect proves an External v8 CPA and records the connection, or stops
// on a v7 one with the typed version refusal.
func ExternalConnect(ctx context.Context, options ExternalConnectOptions) (ExternalConnectResult, error) {
	if err := ctx.Err(); err != nil {
		return ExternalConnectResult{}, err
	}
	targetRoot, err := resolveTargetRoot(options.TargetRoot)
	if err != nil {
		return ExternalConnectResult{}, err
	}
	baseURL, err := cpa.NormalizeV8BaseURL(strings.TrimSpace(options.BaseURL))
	if err != nil {
		return ExternalConnectResult{}, newUpgradeError(UpgradeErrorExternalAuthorityUnavailable,
			"the CPA address is not a usable http(s) endpoint; nothing was connected and the operation did not advance", err)
	}
	managementKey := strings.TrimSpace(options.ManagementKey)
	if !cpa.ValidV8ManagementKey(managementKey) {
		return ExternalConnectResult{}, newUpgradeError(UpgradeErrorExternalManagementRejected,
			"no usable management key was supplied for the CPA; nothing was connected and the operation did not advance", nil)
	}
	paths := newTargetPaths(targetRoot)
	recorded, found, err := readRecordedCopyIn(ctx, paths.Database)
	if err != nil {
		return ExternalConnectResult{}, err
	}
	if !found {
		return ExternalConnectResult{}, newUpgradeError(UpgradeErrorExternalNotApplicable,
			"the v2 data root holds no recorded copy-in, so there is no operation for the External connect step to continue; nothing was written", nil)
	}
	// The step may only run where it could still record its result: a store the
	// operation already carried past this stage, or one a rollback closed, can
	// never take it.
	if !recorded.Stage.CanAdvanceTo(model.UpgradeStageExternalConnect) {
		return ExternalConnectResult{}, newUpgradeError(UpgradeErrorExternalNotApplicable,
			fmt.Sprintf("the recorded operation is at stage %q, which cannot enter the External connect step; nothing was written", recorded.Stage), nil)
	}

	identity := externalConnectIdentity(options, baseURL)
	evidence, hasEvidence, err := readExternalConnectEvidence(targetRoot)
	if err != nil {
		return ExternalConnectResult{}, err
	}
	// A recorded target that is not this address is a moved target, not a new
	// connection: the owner's CPA changed behind a committed pointer, and only an
	// explicit rebind may reach it again.
	if hasEvidence && evidence.Identity != identity {
		return ExternalConnectResult{}, externalConnectRefused(ctx, paths.Database, newUpgradeError(UpgradeErrorExternalTargetMoved,
			fmt.Sprintf("the CPA at %s is not the target this operation recorded; nothing was connected and the operation did not advance", redactExternalAddress(baseURL)), nil))
	}
	// Repeating the step against the same target records nothing new: the row
	// already carries this stage, and a second record would only churn the
	// generation.
	if hasEvidence && recorded.Stage == model.UpgradeStageExternalConnect {
		return externalConnectResult(recorded, evidence, true), nil
	}

	report, err := externalConnectVersion(ctx, options, baseURL, managementKey)
	if err != nil {
		return ExternalConnectResult{}, externalConnectRefused(ctx, paths.Database, err)
	}
	if report.WriteBlocked {
		version := strings.TrimSpace(report.Version)
		if version == "" {
			version = externalConnectUnknownVersion
		}
		return ExternalConnectResult{}, externalConnectRefused(ctx, paths.Database, newUpgradeError(
			UpgradeErrorExternalVersionUnsupported,
			fmt.Sprintf("the CPA at %s answered version %s, which is not the v8 management contract this Manager writes; nothing was connected and the operation did not advance. %s",
				redactExternalAddress(baseURL), version, externalConnectAction),
			cpa.ErrExternalVersionUnsupported))
	}

	snapshot, err := externalConnectKeys(ctx, options, baseURL, managementKey)
	if err != nil {
		return ExternalConnectResult{}, externalConnectRefused(ctx, paths.Database, err)
	}
	clientKey := ""
	if len(snapshot.Keys) > 0 {
		clientKey = snapshot.Keys[0]
	}
	// The model surface is the one fact that separates "a management endpoint
	// answers" from "the gateway behind it serves". A CPA whose gateway does not
	// answer is not connected: the key would install into a target no request
	// can reach.
	if err := externalConnectModelSurface(ctx, options, baseURL, clientKey); err != nil {
		return ExternalConnectResult{}, externalConnectRefused(ctx, paths.Database, newUpgradeError(UpgradeErrorExternalAuthorityUnavailable,
			fmt.Sprintf("the CPA at %s authenticated but its model surface did not answer; nothing was connected and the operation did not advance", redactExternalAddress(baseURL)), err))
	}

	evidence = ExternalConnectEvidence{
		Version:      externalConnectEvidenceVersion,
		BaseURL:      baseURL,
		Identity:     identity,
		Generation:   identity,
		CPAVersion:   strings.TrimSpace(report.Version),
		Support:      report.Support,
		ClientKeys:   len(snapshot.Keys),
		ObservedAtMS: time.Now().UnixMilli(),
	}
	if err := writeExternalConnectEvidence(targetRoot, evidence); err != nil {
		return ExternalConnectResult{}, err
	}
	operation, err := recordExternalConnect(ctx, paths.Database, recorded)
	if err != nil {
		return ExternalConnectResult{}, err
	}
	return externalConnectResult(operation, evidence, false), nil
}

// externalConnectVersion asks the version probe once and maps its failures. A
// CPA that answers without a version header is classified from the operator's
// declared version instead of being trusted as new enough.
func externalConnectVersion(ctx context.Context, options ExternalConnectOptions, baseURL, managementKey string) (cpa.ExternalVersionReport, error) {
	probe := options.observeExternalVersion
	if probe == nil {
		probe = cpa.ObserveExternalVersion
	}
	report, err := probe(ctx, baseURL, managementKey)
	if err != nil {
		return cpa.ExternalVersionReport{}, externalConnectProbeError(err)
	}
	if strings.TrimSpace(report.Version) == "" {
		if declared := strings.TrimSpace(options.CPAVersion); declared != "" {
			return cpa.ClassifyExternalVersion(declared), nil
		}
	}
	return report, nil
}

// externalConnectKeys performs the second authenticated read: it proves the key
// against the v8 management surface and returns the client list the model
// surface probe uses.
func externalConnectKeys(ctx context.Context, options ExternalConnectOptions, baseURL, managementKey string) (cpa.ExternalKeySnapshot, error) {
	read := options.readExternalKeys
	if read == nil {
		endpoint, err := cpa.NormalizeV8BaseURL(baseURL)
		if err != nil {
			return cpa.ExternalKeySnapshot{}, newUpgradeError(UpgradeErrorExternalAuthorityUnavailable,
				"the CPA address is not a usable http(s) endpoint", err)
		}
		transport := cpa.NewTransportForEndpoint(endpoint)
		read = func(ctx context.Context, _, managementKey string) (cpa.ExternalKeySnapshot, error) {
			return transport.ExternalKeySnapshot(ctx, managementKey)
		}
	}
	snapshot, err := read(ctx, baseURL, managementKey)
	if err != nil {
		return cpa.ExternalKeySnapshot{}, externalConnectProbeError(err)
	}
	return snapshot, nil
}

// externalConnectModelSurface asks the gateway behind the CPA for its model
// list. The client key is optional: a CPA with no client keys serves the list
// anonymously, and one with keys answers the first of them.
func externalConnectModelSurface(ctx context.Context, options ExternalConnectOptions, baseURL, clientKey string) error {
	if options.proveModelSurface != nil {
		return options.proveModelSurface(ctx, baseURL, clientKey)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/v1/models", nil)
	if err != nil {
		return cpa.ErrProofUnavailable
	}
	if clientKey != "" {
		request.Header.Set("Authorization", "Bearer "+clientKey)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Cache-Control", "no-store")
	client := &http.Client{
		Timeout:       5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	response, err := client.Do(request)
	if err != nil {
		return cpa.ErrProofUnavailable
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return cpa.ErrProofUnavailable
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, externalModelSurfaceLimit+1))
	if err != nil || len(body) > externalModelSurfaceLimit {
		return cpa.ErrProofUnavailable
	}
	var models struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &models) != nil {
		return cpa.ErrProofUnavailable
	}
	for _, entry := range models.Data {
		if strings.TrimSpace(entry.ID) != "" {
			return nil
		}
	}
	return cpa.ErrProofUnavailable
}

// externalConnectProbeError maps a probe failure onto the External vocabulary.
// The order matters: a 403 is the banned-client form of a rejected credential,
// so it is asked first; a refusal or a moved target is never reported as a
// version problem, because only the version stop tells the owner to upgrade the
// CPA.
func externalConnectProbeError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, cpa.ErrManagementClientForbidden):
		return newUpgradeError(UpgradeErrorExternalManagementRejected, externalConnectForbiddenDetail, err)
	case errors.Is(err, cpa.ErrManagementRejected):
		return newUpgradeError(UpgradeErrorExternalManagementRejected, externalConnectRejectedDetail, err)
	case errors.Is(err, cpa.ErrExternalTargetMoved):
		return newUpgradeError(UpgradeErrorExternalTargetMoved,
			"the CPA no longer names the target this operation holds; only rebinding the connection reaches it again, and nothing was connected", err)
	case errors.Is(err, cpa.ErrExternalVersionUnsupported):
		return newUpgradeError(UpgradeErrorExternalVersionUnsupported, externalConnectAction, err)
	default:
		return newUpgradeError(UpgradeErrorExternalAuthorityUnavailable,
			"the CPA did not answer the management read; nothing was connected and the operation did not advance", err)
	}
}

// externalConnectRefused records the typed refusal on the row without moving the
// stage or marking a domain: the step did not advance, and a refusal must never
// read as progress. An environment failure is returned unrecorded — the operator
// retries it — while a decision about the CPA (its version, its key, its
// identity) is durable, because the wizard has to show it until it is resolved.
func externalConnectRefused(ctx context.Context, databasePath string, err error) error {
	code, ok := UpgradeErrorCodeOf(err)
	if !ok {
		return err
	}
	switch code {
	case UpgradeErrorExternalVersionUnsupported, UpgradeErrorExternalManagementRejected, UpgradeErrorExternalTargetMoved:
	default:
		return err
	}
	db, openErr := managersqlite.Open(databasePath)
	if openErr != nil {
		return fmt.Errorf("%w; the refusal could not be recorded: %v", err, openErr)
	}
	defer func() { _ = db.Close() }()
	if recordErr := upgradeoperation.New(db).RecordError(ctx, string(code), externalConnectRefusalDetail(err)); recordErr != nil {
		return fmt.Errorf("%w; the refusal could not be recorded: %v", err, recordErr)
	}
	return err
}

// externalConnectRefusalDetail is the operator text persisted beside the code.
// It is the typed message minus the code prefix, so the wizard can show the
// sentence without repeating the code it already has.
func externalConnectRefusalDetail(err error) string {
	var typed *UpgradeError
	if !errors.As(err, &typed) || typed == nil {
		return ""
	}
	return typed.Message
}

// recordExternalConnect writes the two domains this step proved and the stage
// that authorizes them in a single row write. It starts from the recorded row so
// the provenance a later rollback relies on is carried over untouched, and it
// clears any refusal a previous run recorded.
func recordExternalConnect(ctx context.Context, databasePath string, recorded model.UpgradeOperation) (model.UpgradeOperation, error) {
	db, err := managersqlite.Open(databasePath)
	if err != nil {
		return model.UpgradeOperation{}, fmt.Errorf("open the v2 database to record the External connection: %w", err)
	}
	defer func() { _ = db.Close() }()
	recorded.MarkDomain(model.UpgradeDomainCPAConfig)
	recorded.MarkDomain(model.UpgradeDomainIdentity)
	recorded.Stage = model.UpgradeStageExternalConnect
	recorded.ErrorCode = ""
	recorded.ErrorDetail = ""
	recorded.UpdatedAtMS = time.Now().UnixMilli()
	if err := upgradeoperation.New(db).Put(ctx, recorded); err != nil {
		return model.UpgradeOperation{}, fmt.Errorf("record the External connection: %w", err)
	}
	operation, found, err := upgradeoperation.New(db).Get(ctx)
	if err != nil {
		return model.UpgradeOperation{}, err
	}
	if !found {
		return model.UpgradeOperation{}, errors.New("the External connection row disappeared after it was recorded")
	}
	return operation, nil
}

func externalConnectResult(operation model.UpgradeOperation, evidence ExternalConnectEvidence, already bool) ExternalConnectResult {
	return ExternalConnectResult{
		Stage:           operation.Stage,
		DomainsDone:     operation.DomainsDone,
		CPAVersion:      evidence.CPAVersion,
		Support:         evidence.Support,
		Identity:        evidence.Identity,
		Generation:      evidence.Generation,
		ClientKeys:      evidence.ClientKeys,
		AlreadyComplete: already,
		UpdatedAtMS:     operation.UpdatedAtMS,
	}
}

// externalConnectIdentity derives the connection identity of the target, which
// is also its generation: a user's CPA is administered by the user, so its
// configuration digest changes for reasons this Manager does not control (its
// own key writes land in that document), and binding an operation to the digest
// would invalidate it after its own successful write.
func externalConnectIdentity(options ExternalConnectOptions, baseURL string) string {
	if options.externalIdentity != nil {
		return options.externalIdentity(baseURL)
	}
	return freshkeyapply.ExternalIdentity(baseURL)
}

// redactExternalAddress names the target in operator text without carrying the
// path it may have been configured with.
func redactExternalAddress(baseURL string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if index := strings.Index(trimmed, "://"); index >= 0 {
		return trimmed[:index+3] + strings.SplitN(trimmed[index+3:], "/", 2)[0]
	}
	return trimmed
}

// externalConnectEvidencePath is the evidence file of a v2 data root. It shares
// the copy-in work directory, so a completed upgrade leaves one reserved area
// rather than two.
func externalConnectEvidencePath(targetRoot string) string {
	return filepath.Join(copyinmarker.Directory(targetRoot), externalConnectEvidenceFile)
}

// writeExternalConnectEvidence publishes the record atomically: a reader sees
// the previous record or this one, never a partial file.
func writeExternalConnectEvidence(targetRoot string, evidence ExternalConnectEvidence) error {
	if evidence.Identity == "" || evidence.Generation == "" {
		return errors.New("External connect evidence must name the identity and the generation of the target")
	}
	if evidence.Identity != evidence.Generation {
		return errors.New("External connect evidence must record the identity as the generation")
	}
	data, err := json.Marshal(evidence)
	if err != nil {
		return err
	}
	directory := copyinmarker.Directory(targetRoot)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	path := externalConnectEvidencePath(targetRoot)
	next := path + ".next"
	if err := os.Remove(next); err != nil && !os.IsNotExist(err) {
		return err
	}
	file, err := os.OpenFile(next, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	writeErr := error(nil)
	if _, err := file.Write(data); err != nil {
		writeErr = err
	} else if err := file.Sync(); err != nil {
		writeErr = err
	}
	closeErr := file.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		_ = os.Remove(next)
		return err
	}
	if err := os.Rename(next, path); err != nil {
		// Windows refuses a rename onto an existing file.
		if removeErr := os.Remove(path); removeErr != nil && !os.IsNotExist(removeErr) {
			_ = os.Remove(next)
			return err
		}
		if retryErr := os.Rename(next, path); retryErr != nil {
			_ = os.Remove(next)
			return retryErr
		}
	}
	return nil
}

// readExternalConnectEvidence returns the record of a previous connect. A record
// that exists but does not validate is an error: refusing the step is the only
// safe reading of an unreadable provenance file.
func readExternalConnectEvidence(targetRoot string) (ExternalConnectEvidence, bool, error) {
	path := externalConnectEvidencePath(targetRoot)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return ExternalConnectEvidence{}, false, nil
	}
	if err != nil {
		return ExternalConnectEvidence{}, false, fmt.Errorf("read the External connect evidence: %w", err)
	}
	var evidence ExternalConnectEvidence
	if err := json.Unmarshal(data, &evidence); err != nil {
		return ExternalConnectEvidence{}, false, fmt.Errorf("decode the External connect evidence: %w", err)
	}
	if evidence.Version != externalConnectEvidenceVersion {
		return ExternalConnectEvidence{}, false, fmt.Errorf("External connect evidence version %d is not supported", evidence.Version)
	}
	if evidence.Identity == "" || evidence.Generation == "" || evidence.BaseURL == "" {
		return ExternalConnectEvidence{}, false, errors.New("External connect evidence does not name the target it recorded")
	}
	return evidence, true, nil
}
