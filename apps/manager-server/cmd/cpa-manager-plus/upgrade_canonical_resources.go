package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	usageadapter "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/adapters/sqlite/identityusage"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/buildinfo"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/identity"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/controller/artifactproxy"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/response"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/upgradeoperation"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	collectorsvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/collector"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshconfig"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeyapply"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeys"
	proxysvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/proxy"
	runtimeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/runtime"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/updatecheck"
	upgradeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/upgrade"
)

const (
	upgradeConfigGroupsSettingsKey = "upgrade_config_groups_v1"
	upgradeExternalHoldBudget      = 30 * time.Second
	upgradeExternalUsageBudget     = 30 * time.Second
	upgradeExternalUsageMaxBody    = 8192
	upgradeExternalRebindBudget    = 30 * time.Second
	upgradeExternalRebindMaxBody   = 8192
	upgradeRuntimeMutationMaxBytes = 4096
)

// upgradeCanonicalDependencies wires the completed Key workspace, data-key
// protector, and embedded runtime client from main.go into upgradeCanonicalServer
// so all post-upgrade v2 Console resources share a single rejection ledger and
// credential gate without introducing a fake FreshApplication or FreshStore.
type upgradeCanonicalDependencies struct {
	Workspace       *upgradeKeyWorkspace
	Protector       *security.Protector
	Runtime         runtimeservice.RuntimeClient
	DesiredStateOut *runtimeservice.DesiredStateStore
}

func resolveUpgradeProtector(cfg config.Config, explicit *security.Protector) (*security.Protector, error) {
	if explicit != nil {
		return explicit, nil
	}
	keyPath := strings.TrimSpace(cfg.DataKeyPath)
	if strings.TrimSpace(cfg.DataKey) == "" && keyPath == "" && strings.TrimSpace(cfg.DBPath) != "" {
		dir := filepath.Dir(cfg.DBPath)
		for _, candidate := range []string{filepath.Join(dir, "data.key"), filepath.Join(dir, ".data.key")} {
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				keyPath = candidate
				break
			}
		}
		if keyPath == "" {
			keyPath = filepath.Join(dir, "data.key")
		}
	}
	resolvedKey, _, err := security.LoadOrCreateDataKey(cfg.DataKey, keyPath)
	if err != nil {
		return nil, err
	}
	return security.NewProtector(resolvedKey)
}

func (u *upgradeCanonicalServer) initCanonicalResources(
	ctx context.Context,
	cfg config.Config,
	deps upgradeCanonicalDependencies,
) error {
	protector, err := resolveUpgradeProtector(cfg, deps.Protector)
	if err != nil {
		return fmt.Errorf("load upgrade canonical protector: %w", err)
	}
	runtimeClient := deps.Runtime
	var artifactClient *runtimeservice.ArtifactClient
	if cfg.EmbeddedRuntimeConfigured() && u.targetMode == model.UpgradeTargetManaged {
		tokenSource := runtimeservice.NewFileRuntimeTokenSource(cfg.RuntimeTokenFile)
		if runtimeClient == nil {
			runtimeClient = runtimeservice.NewEmbeddedClientWithTokenSource(cfg.RuntimeURL, tokenSource)
		}
		artifactClient = runtimeservice.NewArtifactClientWithTokenSource(cfg.RuntimeURL, tokenSource)
	}
	workspace := deps.Workspace
	if workspace == nil && u.db != nil && u.db.SQLDB() != nil {
		ws, _, wsErr := newUpgradeAPIKeysHandler(upgradeAPIKeysConfig{
			DB:              u.db.SQLDB(),
			Protector:       protector,
			Runtime:         runtimeClient,
			SupervisorRoots: cfg.TrustedSupervisorRoots(),
		})
		if wsErr != nil {
			return fmt.Errorf("init upgrade canonical workspace: %w", wsErr)
		}
		workspace = ws
	}
	if ctx == nil {
		ctx = context.Background()
	}
	u.hostCtx = ctx
	u.protector = protector
	u.workspace = workspace
	if u.workspace != nil {
		u.workspace.bindCanonicalProvenance(u.targetMode, u.sourceFingerprint)
	}
	u.runtime = runtimeClient
	u.artifact = artifactClient
	u.runtimeJournal = newFreshRuntimeJournal(u.db.SQLDB())
	if u.appCtx != nil && u.appCtx.UpdateCheckService != nil {
		u.updates = u.appCtx.UpdateCheckService
	} else if u.db != nil {
		u.updates = updatecheck.New(u.db, buildinfo.Version, buildinfo.SourceCommit, true)
	}
	if u.appCtx != nil && u.appCtx.ManagerConfigService != nil {
		u.appCtx.ManagerConfigService.SetCPAUsageProbeAllowed(func(context.Context) bool {
			return u.targetMode != model.UpgradeTargetExternal
		})
	}
	u.runtimeExternal = runtimeservice.NewExternalClientWithConnectionSource(func(callCtx context.Context) (string, string, error) {
		install, _, loadErr := u.loadFencedExternalInstall(callCtx)
		if loadErr != nil {
			return "", "", loadErr
		}
		return install.BaseURL, install.ManagementKey, nil
	})
	u.configService = &upgradeConfigService{owner: u}
	u.pluginCapability = newPluginCapabilityProbe(pluginCapabilityTTL, pluginCapabilityProbeTimeout, u.probePluginCapable)
	if ctx != nil && ctx.Done() != nil {
		go func() {
			<-ctx.Done()
			u.pluginCapability.close()
		}()
	}
	if u.targetMode == model.UpgradeTargetManaged && u.runtime != nil {
		u.pluginCapability.reprove(ctx)
		if u.db != nil && u.db.SQLDB() != nil {
			if _, _, destErr := u.loadVerifiedManagedDestination(ctx); destErr == nil {
				if _, err := u.db.SeedEmbeddedRuntimeDesiredLifecycleIfAbsent(ctx, model.EmbeddedRuntimeDesiredRunning); err != nil {
					return fmt.Errorf("seed upgraded managed runtime desired state: %w", err)
				}
			}
		}
	}
	return nil
}

type upgradeDesiredStateStore struct {
	owner *upgradeCanonicalServer
}

var (
	_ runtimeservice.DesiredStateStore = upgradeDesiredStateStore{}
	_ runtimeservice.LifecycleLocker   = upgradeDesiredStateStore{}
)

func (s upgradeDesiredStateStore) LockEmbeddedRuntimeLifecycle(ctx context.Context) (func(), error) {
	if s.owner == nil {
		return func() {}, nil
	}
	return s.owner.runtimeLifecycleMu.Lock(ctx)
}

func (s upgradeDesiredStateStore) LoadEmbeddedRuntimeDesiredState(ctx context.Context) (model.EmbeddedRuntimeDesiredState, bool, error) {
	var zero model.EmbeddedRuntimeDesiredState
	if s.owner == nil || s.owner.db == nil || s.owner.db.SQLDB() == nil {
		return zero, false, nil
	}
	if _, err := s.owner.loadCurrentOp(ctx); err != nil {
		return zero, false, err
	}
	if s.owner.targetMode != model.UpgradeTargetManaged {
		return zero, false, nil
	}
	if _, _, err := s.owner.loadVerifiedManagedDestination(ctx); err != nil {
		return zero, false, err
	}
	return s.owner.db.LoadEmbeddedRuntimeDesiredState(ctx)
}

// loadFencedExternalInstall loads the completed External Upgrade binding and
// enforces that both the verified Upgrade completion contract and any persisted
// legacy connection rows (manager_config / setup) agree with the committed URL
// and management credential.
func (u *upgradeCanonicalServer) loadFencedExternalInstall(ctx context.Context) (freshkeyapply.ExternalInstall, string, error) {
	if u == nil || u.workspace == nil || u.db == nil || u.db.SQLDB() == nil {
		return freshkeyapply.ExternalInstall{}, "", freshkeyapply.ErrExternalUnavailable
	}
	return u.workspace.loadVerifiedExternalInstall(ctx)
}

func (u *upgradeCanonicalServer) precheckExternalCredential(ctx context.Context) (freshkeyapply.ExternalInstall, string, error) {
	if u == nil || u.workspace == nil {
		return freshkeyapply.ExternalInstall{}, "", freshkeyapply.ErrExternalUnavailable
	}
	install, credDigest, err := u.loadFencedExternalInstall(ctx)
	if err != nil {
		return freshkeyapply.ExternalInstall{}, "", err
	}
	refused, err := u.workspace.rejection.check(ctx, install.Identity, credDigest)
	if err != nil {
		return freshkeyapply.ExternalInstall{}, "", cpa.ErrExternalRejectionUnavailable
	}
	if refused {
		return freshkeyapply.ExternalInstall{}, "", cpa.ErrManagementRejected
	}
	return install, credDigest, nil
}

type upgradeExternalHoldContextKey struct{}

func withUpgradeExternalHold(ctx context.Context) context.Context {
	if ctx == nil {
		return ctx
	}
	return context.WithValue(ctx, upgradeExternalHoldContextKey{}, true)
}

func hasUpgradeExternalHold(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	held, _ := ctx.Value(upgradeExternalHoldContextKey{}).(bool)
	return held
}

func (u *upgradeCanonicalServer) enterExternalCredentialHold(
	w http.ResponseWriter,
	ctx context.Context,
	budget time.Duration,
) (freshkeyapply.ExternalInstall, string, context.Context, func(), error) {
	if u == nil || u.workspace == nil {
		return freshkeyapply.ExternalInstall{}, "", nil, nil, freshkeyapply.ErrExternalUnavailable
	}
	acquiredHere := false
	if !hasUpgradeExternalHold(ctx) {
		if !u.workspace.credentialGate.acquire(ctx) {
			return freshkeyapply.ExternalInstall{}, "", nil, nil, errExternalCredentialWaitCancelled
		}
		acquiredHere = true
	}
	install, credDigest, err := u.precheckExternalCredential(ctx)
	if err != nil {
		if acquiredHere {
			u.workspace.credentialGate.release()
		}
		return freshkeyapply.ExternalInstall{}, "", nil, nil, err
	}
	if budget > 0 && w != nil {
		rc := http.NewResponseController(w)
		_ = rc.SetWriteDeadline(time.Now().Add(budget + 5*time.Second))
	}
	holdCtx := withUpgradeExternalHold(ctx)
	var cancel context.CancelFunc = func() {}
	if budget > 0 {
		holdCtx, cancel = context.WithTimeout(holdCtx, budget)
	}
	return install, credDigest, holdCtx, func() {
		cancel()
		if acquiredHere {
			u.workspace.credentialGate.release()
		}
	}, nil
}

func (u *upgradeCanonicalServer) recordExternalAuthFailureIfMatching(
	ctx context.Context,
	install freshkeyapply.ExternalInstall,
	credDigest string,
	err error,
) {
	if !errors.Is(err, cpa.ErrManagementRejected) || u == nil || u.workspace == nil || install.Identity == "" || credDigest == "" {
		return
	}
	checkCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	cur, curDigest, curErr := u.loadFencedExternalInstall(checkCtx)
	if curErr != nil || cur.Identity != install.Identity || curDigest != credDigest || !security.EqualHMAC(cur.ManagementKey, install.ManagementKey) {
		return
	}
	_ = u.workspace.rejection.record(checkCtx, install.Identity, credDigest)
}

func (u *upgradeCanonicalServer) supportPluginCapable(r *http.Request) bool {
	if u == nil || u.pluginCapability == nil {
		return false
	}
	ctx := context.Background()
	if r != nil {
		ctx = r.Context()
	}
	return u.pluginCapability.capable(ctx)
}

func (u *upgradeCanonicalServer) probePluginCapable(ctx context.Context) bool {
	if u == nil || u.configService == nil {
		return false
	}
	if u.targetMode == model.UpgradeTargetExternal {
		install, credDigest, holdCtx, release, err := u.enterExternalCredentialHold(nil, ctx, pluginCapabilityProbeTimeout)
		if err != nil {
			return false
		}
		defer release()
		tr := cpa.NewExternalConfigTransport(install.BaseURL)
		doc, docErr := tr.ReadConfigDocument(holdCtx, install.ManagementKey)
		if docErr != nil {
			u.recordExternalAuthFailureIfMatching(ctx, install, credDigest, docErr)
			return false
		}
		plugins, ok := doc["plugins"].(map[string]any)
		if !ok {
			return false
		}
		_, hasDir := plugins["dir"]
		return hasDir
	}
	return u.configService.SupportsPlugins(ctx)
}

func (u *upgradeCanonicalServer) notePluginDocumentObserved(hasPluginDir bool) {
	if u == nil || u.pluginCapability == nil {
		return
	}
	u.pluginCapability.mu.Lock()
	if !u.pluginCapability.closed {
		u.pluginCapability.proven = hasPluginDir
		u.pluginCapability.at = u.pluginCapability.now()
	}
	u.pluginCapability.mu.Unlock()
}

// --- Gateway Configuration & Plugin Runtime Resources (Store-Free) ---

type upgradeConfigScope struct {
	name         string
	managedOnly  bool
	ruleFamilies bool
	groupIDs     func() []string
	lookupGroup  func(string) (cpa.ConfigGroup, bool)
	leafPaths    func() []string
	lookupLeaf   func(string) (cpa.ConfigLeaf, bool)
	parseValue   func(string, json.RawMessage) (any, error)
}

func (sc upgradeConfigScope) ownsGroup(id string) bool {
	if sc.ruleFamilies && id == cpa.ConfigRulesGroupID {
		return true
	}
	_, ok := sc.lookupGroup(id)
	return ok
}

var (
	upgradeGatewayScope = upgradeConfigScope{
		name:         "gateway",
		ruleFamilies: true,
		groupIDs:     cpa.ConfigGroupIDs,
		lookupGroup:  cpa.LookupConfigGroup,
		leafPaths:    cpa.ConfigLeafPaths,
		lookupLeaf:   cpa.LookupConfigLeaf,
		parseValue:   cpa.ParseConfigLeafValue,
	}
	upgradePluginScope = upgradeConfigScope{
		name:        "plugin",
		managedOnly: true,
		groupIDs:    cpa.PluginConfigGroupIDs,
		lookupGroup: cpa.LookupPluginConfigGroup,
		leafPaths:   cpa.PluginConfigLeafPaths,
		lookupLeaf:  cpa.LookupPluginConfigLeaf,
		parseValue:  cpa.ParsePluginConfigLeafValue,
	}
)

type upgradeConfigWire interface {
	ReadConfigDocument(ctx context.Context, management string) (map[string]any, error)
	PatchConfigPath(ctx context.Context, management, prefix string, content []byte) error
	PutConfigValue(ctx context.Context, management, path string, content []byte) error
	DeleteConfigPath(ctx context.Context, management, path string) error
}

type upgradeConfigAuthority struct {
	capable    freshconfig.Capability
	mode       string
	sourceID   string
	generation string
	endpoint   string
	secret     string
	readOnly   string
	external   freshkeyapply.ExternalInstall
	credDigest string
	transport  upgradeConfigWire
}

type upgradeConfigService struct {
	mu    sync.Mutex
	owner *upgradeCanonicalServer
}

type upgradeConfigJournalEnvelope struct {
	Groups map[string]json.RawMessage `json:"groups"`
}

func (s *upgradeConfigService) loadJournal(ctx context.Context) (map[string]*freshstore.ConfigGroupJournal, bool, error) {
	if s == nil || s.owner == nil || s.owner.db == nil || s.owner.db.SQLDB() == nil {
		return nil, false, errors.New("upgrade config journal database unavailable")
	}
	var raw string
	err := s.owner.db.SQLDB().QueryRowContext(ctx, `select value from settings where key = ?`, upgradeConfigGroupsSettingsKey).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) || strings.TrimSpace(raw) == "" {
		return map[string]*freshstore.ConfigGroupJournal{}, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var env upgradeConfigJournalEnvelope
	if json.Unmarshal([]byte(raw), &env) != nil {
		return map[string]*freshstore.ConfigGroupJournal{}, true, nil
	}
	out := make(map[string]*freshstore.ConfigGroupJournal, len(env.Groups))
	invalid := false
	for group, msg := range env.Groups {
		dec := json.NewDecoder(bytes.NewReader(msg))
		dec.UseNumber()
		var entry *freshstore.ConfigGroupJournal
		if dec.Decode(&entry) != nil || entry == nil {
			invalid = true
			continue
		}
		if entry.Operations == nil {
			entry.Operations = map[string]freshstore.ConfigGroupOperation{}
		}
		out[group] = entry
	}
	return out, invalid, nil
}

func (s *upgradeConfigService) updateJournal(
	ctx context.Context,
	mutate func(map[string]*freshstore.ConfigGroupJournal) error,
) error {
	db := s.owner.db.SQLDB()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	now := time.Now().UnixMilli()
	if _, err := tx.ExecContext(ctx, `insert into settings(key, value, updated_at_ms) values(?, '{}', ?) on conflict(key) do update set updated_at_ms = excluded.updated_at_ms`, upgradeConfigGroupsSettingsKey, now); err != nil {
		return err
	}
	var raw string
	err = tx.QueryRowContext(ctx, `select value from settings where key = ?`, upgradeConfigGroupsSettingsKey).Scan(&raw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	groups := map[string]*freshstore.ConfigGroupJournal{}
	if strings.TrimSpace(raw) != "" && strings.TrimSpace(raw) != "{}" {
		var env upgradeConfigJournalEnvelope
		if json.Unmarshal([]byte(raw), &env) != nil {
			return errors.New("upgrade config group journal corrupted")
		}
		for group, msg := range env.Groups {
			dec := json.NewDecoder(bytes.NewReader(msg))
			dec.UseNumber()
			var entry *freshstore.ConfigGroupJournal
			if dec.Decode(&entry) != nil || entry == nil {
				return errors.New("upgrade config group journal entry corrupted")
			}
			if entry.Operations == nil {
				entry.Operations = map[string]freshstore.ConfigGroupOperation{}
			}
			groups[group] = entry
		}
	}
	if err := mutate(groups); err != nil {
		return err
	}
	rawGroups := make(map[string]json.RawMessage, len(groups))
	for group, entry := range groups {
		encoded, encErr := json.Marshal(entry)
		if encErr != nil {
			return encErr
		}
		rawGroups[group] = encoded
	}
	payload, err := json.Marshal(upgradeConfigJournalEnvelope{Groups: rawGroups})
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `update settings set value = ?, updated_at_ms = ? where key = ?`, string(payload), now, upgradeConfigGroupsSettingsKey); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *upgradeConfigService) resolveAuthority(ctx context.Context, sc upgradeConfigScope, forWrite bool) (upgradeConfigAuthority, error) {
	u := s.owner
	if u == nil || u.workspace == nil {
		return upgradeConfigAuthority{
			capable: freshconfig.Capability{Mode: freshconfig.ModeUnavailable, Reason: freshconfig.ReasonUnavailable},
			mode:    freshconfig.ModeUnavailable,
		}, nil
	}
	if u.targetMode == model.UpgradeTargetExternal {
		if sc.managedOnly {
			if forWrite {
				return upgradeConfigAuthority{}, freshconfig.ErrPluginAuthority
			}
			return upgradeConfigAuthority{
				capable: freshconfig.Capability{
					Mode:   freshconfig.ModeExternal,
					Reason: freshconfig.ReasonPluginManagedOnly,
				},
				mode:     freshconfig.ModeExternal,
				readOnly: freshconfig.ReasonPluginManagedOnly,
			}, nil
		}
		install, credDigest, err := u.precheckExternalCredential(ctx)
		switch {
		case errors.Is(err, cpa.ErrManagementRejected):
			return upgradeConfigAuthority{
				capable: freshconfig.Capability{Mode: freshconfig.ModeUnavailable, Reason: freshconfig.ReasonManagementRejected},
				mode:    freshconfig.ModeUnavailable,
			}, cpa.ErrManagementRejected
		case errors.Is(err, cpa.ErrExternalRejectionUnavailable):
			return upgradeConfigAuthority{}, cpa.ErrExternalRejectionUnavailable
		case errors.Is(err, cpa.ErrExternalTargetMoved):
			return upgradeConfigAuthority{
				capable: freshconfig.Capability{Mode: freshconfig.ModeUnavailable, Reason: freshconfig.ReasonTargetMoved},
				mode:    freshconfig.ModeUnavailable,
			}, cpa.ErrExternalTargetMoved
		case err != nil:
			return upgradeConfigAuthority{
				capable: freshconfig.Capability{Mode: freshconfig.ModeUnavailable, Reason: freshconfig.ReasonUnavailable},
				mode:    freshconfig.ModeUnavailable,
			}, nil
		}
		report, verErr := cpa.ObserveExternalVersion(ctx, install.BaseURL, install.ManagementKey)
		if verErr != nil {
			u.recordExternalAuthFailureIfMatching(ctx, install, credDigest, verErr)
			if errors.Is(verErr, cpa.ErrManagementRejected) || errors.Is(verErr, cpa.ErrManagementClientForbidden) {
				return upgradeConfigAuthority{
					capable: freshconfig.Capability{Mode: freshconfig.ModeUnavailable, Reason: freshconfig.ReasonManagementRejected},
					mode:    freshconfig.ModeUnavailable,
				}, verErr
			}
			if forWrite {
				return upgradeConfigAuthority{}, cpa.ErrProofUnavailable
			}
		}
		writable := verErr == nil && !report.WriteBlocked
		readOnlyReason := ""
		capReason := freshconfig.ReasonRulesExternal
		if verErr == nil && report.WriteBlocked {
			if forWrite {
				return upgradeConfigAuthority{}, cpa.ErrExternalVersionUnsupported
			}
			readOnlyReason = freshconfig.ReasonVersionUnsupported
			capReason = freshconfig.ReasonVersionUnsupported
		}
		return upgradeConfigAuthority{
			capable: freshconfig.Capability{
				Mode:          freshconfig.ModeExternal,
				Readable:      true,
				Writable:      writable,
				RulesWritable: false,
				Reason:        capReason,
			},
			mode:       freshconfig.ModeExternal,
			sourceID:   install.Identity,
			generation: strconv.FormatUint(install.Checkpoint, 10),
			endpoint:   install.BaseURL,
			secret:     install.ManagementKey,
			readOnly:   readOnlyReason,
			external:   install,
			credDigest: credDigest,
			transport:  cpa.NewExternalConfigTransport(install.BaseURL),
		}, nil
	}

	if u.runtime == nil || u.db == nil || u.db.SQLDB() == nil {
		return upgradeConfigAuthority{
			capable: freshconfig.Capability{Mode: freshconfig.ModeUnavailable, Reason: freshconfig.ReasonUnavailable},
			mode:    freshconfig.ModeUnavailable,
		}, nil
	}
	if _, ev, ok, err := loadVerifiedCompletedUpgradeOperation(ctx, upgradeoperation.New(u.db.SQLDB())); err != nil || !ok || ev.TargetMode != model.UpgradeTargetManaged {
		return upgradeConfigAuthority{
			capable: freshconfig.Capability{Mode: freshconfig.ModeUnavailable, Reason: freshconfig.ReasonUnavailable},
			mode:    freshconfig.ModeUnavailable,
		}, nil
	}
	rec, secret, err := u.workspace.loadManagedDestination(ctx)
	if err != nil {
		return upgradeConfigAuthority{
			capable: freshconfig.Capability{Mode: freshconfig.ModeUnavailable, Reason: freshconfig.ReasonUnavailable},
			mode:    freshconfig.ModeUnavailable,
		}, nil
	}
	_, epoch, _, err := verifyUpgradeManagedLiveRuntime(ctx, u.runtime, rec)
	if err != nil {
		return upgradeConfigAuthority{
			capable: freshconfig.Capability{Mode: freshconfig.ModeUnavailable, Reason: freshconfig.ReasonUnavailable},
			mode:    freshconfig.ModeUnavailable,
		}, nil
	}
	tr := cpa.NewTransportForEndpoint(rec.BaseURL)
	if len(u.workspace.supervisorRoots) > 0 {
		tr.SetTrustedSupervisorRoots(u.workspace.supervisorRoots)
	}
	roots := append([]string(nil), u.workspace.supervisorRoots...)
	tr.SetPostureSelector(func() (cpa.DestinationPosture, bool) {
		return cpa.PostureForWithSupervisorRoots(freshkeyapply.StrategyUpgradedManaged, roots)
	})
	return upgradeConfigAuthority{
		capable: freshconfig.Capability{
			Mode:          freshconfig.ModeManaged,
			Readable:      true,
			Writable:      true,
			RulesWritable: !sc.managedOnly,
		},
		mode:       freshconfig.ModeManaged,
		sourceID:   string(rec.Intent.ExpectedRuntimeIdentity),
		generation: strconv.FormatUint(epoch, 10),
		endpoint:   rec.BaseURL,
		secret:     secret,
		transport:  tr,
	}, nil
}

func (s *upgradeConfigService) SupportsPlugins(ctx context.Context) bool {
	if s == nil {
		return false
	}
	auth, err := s.resolveAuthority(ctx, upgradeGatewayScope, false)
	if err != nil || auth.transport == nil || auth.secret == "" {
		return false
	}
	doc, err := auth.transport.ReadConfigDocument(ctx, auth.secret)
	if err != nil {
		return false
	}
	plugins, ok := doc["plugins"].(map[string]any)
	if !ok {
		return false
	}
	_, hasDir := plugins["dir"]
	return hasDir
}

func (s *upgradeConfigService) LogsCapability(ctx context.Context) (freshconfig.Capability, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	auth, err := s.resolveAuthority(ctx, upgradeGatewayScope, false)
	if errors.Is(err, cpa.ErrExternalRejectionUnavailable) ||
		errors.Is(err, cpa.ErrManagementRejected) ||
		errors.Is(err, cpa.ErrManagementClientForbidden) ||
		errors.Is(err, cpa.ErrExternalTargetMoved) {
		return freshconfig.Capability{}, err
	}
	if err != nil {
		return freshconfig.Capability{
			Mode:          auth.capable.Mode,
			Readable:      auth.capable.Readable,
			Writable:      false,
			RulesWritable: false,
			Reason:        auth.capable.Reason,
		}, nil
	}
	if auth.mode == freshconfig.ModeExternal && !auth.capable.Writable && auth.readOnly == "" {
		return freshconfig.Capability{
			Mode:          freshconfig.ModeExternal,
			Readable:      false,
			Writable:      false,
			RulesWritable: false,
			Reason:        freshconfig.ReasonUnavailable,
		}, nil
	}
	return auth.capable, nil
}

func (s *upgradeConfigService) Read(ctx context.Context) (freshconfig.View, error) {
	return s.readScope(ctx, upgradeGatewayScope)
}

func (s *upgradeConfigService) ReadPlugin(ctx context.Context) (freshconfig.View, error) {
	return s.readScope(ctx, upgradePluginScope)
}

func (s *upgradeConfigService) readScope(ctx context.Context, sc upgradeConfigScope) (freshconfig.View, error) {
	journal, journalInvalid, err := s.loadJournal(ctx)
	if err != nil {
		return freshconfig.View{}, err
	}
	auth, resolveErr := s.resolveAuthority(ctx, sc, false)
	if errors.Is(resolveErr, cpa.ErrExternalRejectionUnavailable) {
		return freshconfig.View{}, resolveErr
	}
	view := freshconfig.View{
		Capability:    auth.capable,
		Generation:    auth.generation,
		GroupsInvalid: journalInvalid,
	}
	var document map[string]any
	if auth.capable.Readable && auth.transport != nil {
		doc, docErr := auth.transport.ReadConfigDocument(ctx, auth.secret)
		if docErr != nil {
			if auth.mode == freshconfig.ModeExternal {
				s.owner.recordExternalAuthFailureIfMatching(ctx, auth.external, auth.credDigest, docErr)
				if errors.Is(docErr, cpa.ErrManagementRejected) || errors.Is(docErr, cpa.ErrManagementClientForbidden) {
					view.Capability = freshconfig.Capability{
						Mode:   freshconfig.ModeUnavailable,
						Reason: freshconfig.ReasonManagementRejected,
					}
					auth.capable = view.Capability
				} else {
					view.Capability = freshconfig.Capability{
						Mode:   auth.mode,
						Reason: freshconfig.ReasonReadUnavailable,
					}
					auth.capable = view.Capability
				}
			} else {
				view.Capability = freshconfig.Capability{
					Mode:   auth.mode,
					Reason: freshconfig.ReasonReadUnavailable,
				}
				auth.capable = view.Capability
			}
		} else {
			document = doc
			plugins, hasPlugins := document["plugins"].(map[string]any)
			hasDir := false
			if hasPlugins {
				_, hasDir = plugins["dir"]
			}
			s.owner.notePluginDocumentObserved(hasDir)
			if sc.managedOnly && !hasDir {
				auth.capable.Writable = false
				auth.capable.Reason = freshconfig.ReasonUnavailable
				auth.readOnly = freshconfig.ReasonUnavailable
				view.Capability = auth.capable
			}
		}
	}

	blocked := map[string]string{}
	for id, entry := range journal {
		if entry == nil || !sc.ownsGroup(id) {
			continue
		}
		if op, ok := entry.Operations[entry.Latest]; ok && op.State == "unknown" {
			blocked[id] = freshconfig.ReasonUnknownOriginal
		}
	}

	for _, id := range sc.groupIDs() {
		group, ok := sc.lookupGroup(id)
		if !ok {
			continue
		}
		entry := freshconfig.GroupView{
			ID:        group.ID,
			Prefix:    group.Prefix,
			Paths:     append([]string(nil), group.Leaves...),
			Persisted: "unknown",
			Effective: "unknown",
		}
		if cur, ok := journal[id]; ok && cur != nil {
			if op, ok := cur.Operations[cur.Latest]; ok {
				copied := op.Clone()
				entry.Operation = &copied
				entry.Persisted = op.State
			}
		}
		if document != nil {
			entry.Baseline = upgradeGroupBaseline(document, group)
		}
		groupReason := auth.readOnly
		if !auth.capable.Writable && groupReason == "" {
			if auth.capable.Reason != "" {
				groupReason = auth.capable.Reason
			} else {
				groupReason = freshconfig.ReasonUnavailable
			}
		}
		if view.GroupsInvalid {
			groupReason = freshconfig.ReasonJournalInvalid
		}
		if reason, ok := blocked[id]; ok {
			groupReason = reason
		}
		entry.Reason = groupReason
		entry.Writable = auth.capable.Writable && !view.GroupsInvalid && blocked[id] == "" && document != nil
		view.Groups = append(view.Groups, entry)
	}

	for _, path := range sc.leafPaths() {
		leaf, _ := sc.lookupLeaf(path)
		field := freshconfig.FieldView{
			Path:     path,
			Type:     string(leaf.Type),
			Kind:     string(leaf.Kind),
			Group:    leaf.Group,
			Presence: "unknown",
		}
		if inherited, ok := cpa.ConfigLeafInheritable(path); ok {
			field.Inherit = &inherited
		}
		if document != nil {
			val, presence := cpa.LookupConfigValue(document, path)
			field.Presence = string(presence)
			field.Value = val
		}
		field.Reason = auth.readOnly
		if view.GroupsInvalid {
			field.Reason = freshconfig.ReasonJournalInvalid
		}
		if reason, ok := blocked[leaf.Group]; ok {
			field.Reason = reason
		}
		field.Editable = auth.capable.Writable && !view.GroupsInvalid && blocked[leaf.Group] == "" && document != nil
		if !field.Editable && field.Reason == "" {
			if auth.capable.Reason != "" {
				field.Reason = auth.capable.Reason
			} else {
				field.Reason = freshconfig.ReasonUnavailable
			}
		}
		view.Fields = append(view.Fields, field)
	}

	if sc.ruleFamilies {
		var rulesOp *freshstore.ConfigGroupOperation
		if entry := journal[cpa.ConfigRulesGroupID]; entry != nil {
			if op, ok := entry.Operations[entry.Latest]; ok {
				copied := op.Clone()
				rulesOp = &copied
			}
		}
		for _, id := range cpa.ConfigRuleFamilyIDs() {
			family, _ := cpa.LookupConfigRuleFamily(id)
			item := freshconfig.FamilyView{
				Family:    family.Family,
				Path:      family.Path,
				ReadOnly:  true,
				Operation: rulesOp,
			}
			if document != nil {
				if val, presence := cpa.LookupConfigValue(document, family.Path); presence == cpa.ConfigPresent {
					if rules, ok := val.([]any); ok {
						item.Rules = rules
					}
				}
				item.Baseline = upgradeFamilyBaseline(document, family.Path)
			}
			item.Writable = auth.capable.RulesWritable && !view.GroupsInvalid && blocked[cpa.ConfigRulesGroupID] == "" && document != nil
			item.ReadOnly = !item.Writable
			switch reason, isBlocked := blocked[cpa.ConfigRulesGroupID]; {
			case view.GroupsInvalid:
				item.Reason = freshconfig.ReasonJournalInvalid
			case isBlocked:
				item.Reason = reason
			case !auth.capable.RulesWritable:
				if auth.capable.Reason != "" {
					item.Reason = auth.capable.Reason
				} else {
					item.Reason = freshconfig.ReasonUnavailable
				}
			}
			view.Families = append(view.Families, item)
		}
	}
	return view, nil
}

func (s *upgradeConfigService) SaveGroup(ctx context.Context, groupID string, req freshconfig.GroupRequest) (freshstore.ConfigGroupOperation, error) {
	return s.saveGroupScope(ctx, upgradeGatewayScope, groupID, req)
}

func (s *upgradeConfigService) SavePluginGroup(ctx context.Context, groupID string, req freshconfig.GroupRequest) (freshstore.ConfigGroupOperation, error) {
	return s.saveGroupScope(ctx, upgradePluginScope, groupID, req)
}

func (s *upgradeConfigService) saveGroupScope(
	ctx context.Context,
	sc upgradeConfigScope,
	groupID string,
	req freshconfig.GroupRequest,
) (freshstore.ConfigGroupOperation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	group, ok := sc.lookupGroup(groupID)
	if !ok || !freshkeys.ValidRequestID(req.RequestID) {
		return freshstore.ConfigGroupOperation{}, freshconfig.ErrInvalid
	}
	edits := make([]freshstore.ConfigGroupEdit, 0, len(req.Edits))
	for _, edit := range req.Edits {
		switch edit.Action {
		case "default":
			if len(edit.Value) > 0 {
				return freshstore.ConfigGroupOperation{}, freshconfig.ErrInvalid
			}
			edits = append(edits, freshstore.ConfigGroupEdit{Path: edit.Path, Action: "default"})
		case "set":
			val, parseErr := sc.parseValue(edit.Path, edit.Value)
			if parseErr != nil {
				return freshstore.ConfigGroupOperation{}, freshconfig.ErrInvalid
			}
			edits = append(edits, freshstore.ConfigGroupEdit{Path: edit.Path, Action: "set", Value: val})
		default:
			return freshstore.ConfigGroupOperation{}, freshconfig.ErrInvalid
		}
	}
	if len(edits) == 0 {
		return freshstore.ConfigGroupOperation{}, freshconfig.ErrInvalid
	}

	auth, err := s.resolveAuthority(ctx, sc, true)
	if err != nil {
		return freshstore.ConfigGroupOperation{}, err
	}
	if auth.transport == nil {
		return freshstore.ConfigGroupOperation{}, cpa.ErrProofUnavailable
	}
	if !auth.capable.Writable {
		return freshstore.ConfigGroupOperation{}, freshconfig.ErrReadOnly
	}
	if req.Expected.Generation != auth.generation {
		return freshstore.ConfigGroupOperation{}, freshconfig.ErrGenerationMismatch
	}

	journal, journalInvalid, err := s.loadJournal(ctx)
	if err != nil {
		return freshstore.ConfigGroupOperation{}, err
	}
	if journalInvalid {
		return freshstore.ConfigGroupOperation{}, cpa.ErrSourceConflict
	}
	for id, entry := range journal {
		if entry == nil {
			continue
		}
		if existing, found := entry.Operations[req.RequestID]; found {
			cloned := existing.Clone()
			if id != groupID || !sameUpgradeGroupRequest(cloned, edits, req.Expected.Baseline) {
				return cloned, cpa.ErrSourceConflict
			}
			if cloned.State == "unknown" {
				return cloned, cpa.ErrApplyUnknown
			}
			return cloned, nil
		}
	}
	if entry := journal[groupID]; entry != nil {
		if latest, found := entry.Operations[entry.Latest]; found && latest.State == "unknown" {
			return freshstore.ConfigGroupOperation{}, cpa.ErrApplyUnknown
		}
	}

	doc, err := auth.transport.ReadConfigDocument(ctx, auth.secret)
	if err != nil {
		if auth.mode == freshconfig.ModeExternal {
			s.owner.recordExternalAuthFailureIfMatching(ctx, auth.external, auth.credDigest, err)
			if errors.Is(err, cpa.ErrManagementRejected) || errors.Is(err, cpa.ErrManagementClientForbidden) {
				return freshstore.ConfigGroupOperation{}, err
			}
		}
		return freshstore.ConfigGroupOperation{}, cpa.ErrProofUnavailable
	}
	if sc.managedOnly {
		plugins, hasPlugins := doc["plugins"].(map[string]any)
		hasDir := false
		if hasPlugins {
			_, hasDir = plugins["dir"]
		}
		if !hasDir {
			return freshstore.ConfigGroupOperation{}, freshconfig.ErrReadOnly
		}
	}
	if upgradeGroupBaseline(doc, group) != req.Expected.Baseline {
		return freshstore.ConfigGroupOperation{}, cpa.ErrSourceConflict
	}

	op := freshstore.ConfigGroupOperation{
		SourceID:   auth.sourceID,
		Generation: auth.generation,
		RequestID:  req.RequestID,
		Group:      group.ID,
		Target:     "group",
		Prefix:     group.Prefix,
		Baseline:   req.Expected.Baseline,
		Edits:      edits,
		State:      "unknown",
	}
	if err := s.appendOperation(ctx, group.ID, op); err != nil {
		return op, err
	}

	payload, patched := upgradeGroupPatch(group.Prefix, edits)
	applied := false
	if patched {
		if group.Prefix == "" {
			for _, edit := range edits {
				if edit.Action != "set" {
					continue
				}
				leafPayload, mErr := json.Marshal(edit.Value)
				if mErr != nil {
					return s.failOperation(ctx, group.ID, op, mErr, auth, applied)
				}
				if err := auth.transport.PutConfigValue(ctx, auth.secret, edit.Path, leafPayload); err != nil {
					return s.failOperation(ctx, group.ID, op, err, auth, applied)
				}
				applied = true
			}
		} else {
			if err := auth.transport.PatchConfigPath(ctx, auth.secret, group.Prefix, payload); err != nil {
				return s.failOperation(ctx, group.ID, op, err, auth, false)
			}
			applied = true
		}
	}
	for _, edit := range edits {
		if edit.Action != "default" {
			continue
		}
		if err := auth.transport.DeleteConfigPath(ctx, auth.secret, edit.Path); err != nil {
			return s.failOperation(ctx, group.ID, op, err, auth, applied)
		}
		applied = true
	}

	acknowledged := op
	acknowledged.HTTP200 = true
	if err := s.updateOperation(ctx, group.ID, op, acknowledged); err != nil {
		return op, err
	}
	return s.resolveOperationWithMode(ctx, group.ID, auth, acknowledged, false)
}

func (s *upgradeConfigService) SaveFamily(ctx context.Context, familyID string, req freshconfig.FamilyRequest) (freshstore.ConfigGroupOperation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	family, ok := cpa.LookupConfigRuleFamily(familyID)
	if !ok || !freshkeys.ValidRequestID(req.RequestID) {
		return freshstore.ConfigGroupOperation{}, freshconfig.ErrInvalid
	}
	parsedRules, err := cpa.ParseConfigRules(family.Family, req.Rules)
	if err != nil {
		return freshstore.ConfigGroupOperation{}, freshconfig.ErrInvalid
	}
	if s.owner.targetMode == model.UpgradeTargetExternal {
		auth, authErr := s.resolveAuthority(ctx, upgradeGatewayScope, true)
		if authErr != nil {
			return freshstore.ConfigGroupOperation{}, authErr
		}
		_ = auth
		return freshstore.ConfigGroupOperation{}, freshconfig.ErrRulesReadOnly
	}

	auth, err := s.resolveAuthority(ctx, upgradeGatewayScope, true)
	if err != nil {
		return freshstore.ConfigGroupOperation{}, err
	}
	if auth.transport == nil {
		return freshstore.ConfigGroupOperation{}, cpa.ErrProofUnavailable
	}
	if !auth.capable.RulesWritable {
		return freshstore.ConfigGroupOperation{}, freshconfig.ErrRulesReadOnly
	}
	if req.Expected.Generation != auth.generation {
		return freshstore.ConfigGroupOperation{}, freshconfig.ErrGenerationMismatch
	}

	journal, journalInvalid, err := s.loadJournal(ctx)
	if err != nil {
		return freshstore.ConfigGroupOperation{}, err
	}
	if journalInvalid {
		return freshstore.ConfigGroupOperation{}, cpa.ErrSourceConflict
	}
	for id, entry := range journal {
		if entry == nil {
			continue
		}
		if existing, found := entry.Operations[req.RequestID]; found {
			cloned := existing.Clone()
			if id != cpa.ConfigRulesGroupID || !sameUpgradeFamilyRequest(cloned, family.Path, parsedRules, req.Expected.Baseline) {
				return cloned, cpa.ErrSourceConflict
			}
			if cloned.State == "unknown" {
				return cloned, cpa.ErrApplyUnknown
			}
			return cloned, nil
		}
	}
	if entry := journal[cpa.ConfigRulesGroupID]; entry != nil {
		if latest, found := entry.Operations[entry.Latest]; found && latest.State == "unknown" {
			return freshstore.ConfigGroupOperation{}, cpa.ErrApplyUnknown
		}
	}

	doc, err := auth.transport.ReadConfigDocument(ctx, auth.secret)
	if err != nil {
		return freshstore.ConfigGroupOperation{}, cpa.ErrProofUnavailable
	}
	if upgradeFamilyBaseline(doc, family.Path) != req.Expected.Baseline {
		return freshstore.ConfigGroupOperation{}, cpa.ErrSourceConflict
	}
	payload, err := json.Marshal(parsedRules)
	if err != nil {
		return freshstore.ConfigGroupOperation{}, freshconfig.ErrInvalid
	}

	op := freshstore.ConfigGroupOperation{
		SourceID:   auth.sourceID,
		Generation: auth.generation,
		RequestID:  req.RequestID,
		Group:      cpa.ConfigRulesGroupID,
		Target:     "family",
		Prefix:     family.Path,
		Baseline:   req.Expected.Baseline,
		Rules:      parsedRules,
		State:      "unknown",
	}
	if err := s.appendOperation(ctx, cpa.ConfigRulesGroupID, op); err != nil {
		return op, err
	}
	if applyErr := auth.transport.PutConfigValue(ctx, auth.secret, family.Path, payload); applyErr != nil {
		return s.failOperation(ctx, cpa.ConfigRulesGroupID, op, applyErr, auth, false)
	}
	acknowledged := op
	acknowledged.HTTP200 = true
	if err := s.updateOperation(ctx, cpa.ConfigRulesGroupID, op, acknowledged); err != nil {
		return op, err
	}
	return s.resolveOperationWithMode(ctx, cpa.ConfigRulesGroupID, auth, acknowledged, false)
}

func (s *upgradeConfigService) failOperation(
	ctx context.Context,
	groupID string,
	op freshstore.ConfigGroupOperation,
	cause error,
	auth upgradeConfigAuthority,
	applied bool,
) (freshstore.ConfigGroupOperation, error) {
	if auth.mode == freshconfig.ModeExternal {
		s.owner.recordExternalAuthFailureIfMatching(ctx, auth.external, auth.credDigest, cause)
	}
	if !applied && errors.Is(cause, cpa.ErrManagementRejected) {
		next := op
		next.State, next.Code = "failed", freshconfig.ReasonManagementRejected
		if err := s.updateOperation(ctx, groupID, op, next); err != nil {
			return op, err
		}
		return next, cause
	}
	if !applied && errors.Is(cause, cpa.ErrExternalRejectionUnavailable) {
		next := op
		next.State, next.Code = "failed", freshconfig.ReasonUnavailable
		if err := s.updateOperation(ctx, groupID, op, next); err != nil {
			return op, err
		}
		return next, cause
	}
	var rejection cpa.ConfigWriteError
	if applied || !errors.As(cause, &rejection) || !rejection.Definite() {
		return op, cpa.ErrApplyUnknown
	}
	next := op
	next.State, next.Code = "failed", rejection.Code
	if err := s.updateOperation(ctx, groupID, op, next); err != nil {
		return op, err
	}
	field := rejection.Field
	if field == "" && op.Target == "group" && len(op.Edits) == 1 {
		field = op.Edits[0].Path
	}
	return next, freshconfig.CPARejection{Code: rejection.Code, Field: field, Message: rejection.Message}
}

func (s *upgradeConfigService) resolveOperationWithMode(
	ctx context.Context,
	groupID string,
	auth upgradeConfigAuthority,
	op freshstore.ConfigGroupOperation,
	reconcile bool,
) (freshstore.ConfigGroupOperation, error) {
	if op.State != "unknown" {
		return op.Clone(), nil
	}
	doc, err := auth.transport.ReadConfigDocument(ctx, auth.secret)
	if err != nil {
		if auth.mode == freshconfig.ModeExternal {
			s.owner.recordExternalAuthFailureIfMatching(ctx, auth.external, auth.credDigest, err)
			if errors.Is(err, cpa.ErrManagementRejected) || (reconcile && errors.Is(err, cpa.ErrExternalRejectionUnavailable)) {
				return op.Clone(), err
			}
		}
		return op.Clone(), cpa.ErrApplyUnknown
	}
	if upgradeConfigOperationInPlace(doc, op) {
		next := op
		next.State = "persisted"
		if err := s.updateOperation(ctx, groupID, op, next); err != nil {
			return op.Clone(), err
		}
		return next.Clone(), nil
	}
	if applied, total := upgradeConfigOperationProgress(doc, op); applied > 0 && applied < total {
		next := op
		next.State, next.Code = "persisted", "partial"
		if err := s.updateOperation(ctx, groupID, op, next); err != nil {
			return op.Clone(), err
		}
		return next.Clone(), nil
	}
	if op.HTTP200 {
		next := op
		next.State, next.Code = "persisted", "superseded"
		if err := s.updateOperation(ctx, groupID, op, next); err != nil {
			return op.Clone(), err
		}
		return next.Clone(), nil
	}
	next := op
	next.State, next.Code = "failed", "write_not_applied"
	if err := s.updateOperation(ctx, groupID, op, next); err != nil {
		return op.Clone(), err
	}
	return next.Clone(), nil
}

func (s *upgradeConfigService) Operation(ctx context.Context, id string, reconcile bool) (freshstore.ConfigGroupOperation, error) {
	return s.operationScope(ctx, upgradeGatewayScope, id, reconcile)
}

func (s *upgradeConfigService) PluginOperation(ctx context.Context, id string, reconcile bool) (freshstore.ConfigGroupOperation, error) {
	return s.operationScope(ctx, upgradePluginScope, id, reconcile)
}

func (s *upgradeConfigService) operationScope(
	ctx context.Context,
	sc upgradeConfigScope,
	id string,
	reconcile bool,
) (freshstore.ConfigGroupOperation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !freshkeys.ValidRequestID(id) {
		return freshstore.ConfigGroupOperation{}, freshconfig.ErrInvalid
	}
	journal, _, err := s.loadJournal(ctx)
	if err != nil {
		return freshstore.ConfigGroupOperation{}, err
	}
	for group, entry := range journal {
		if entry == nil || !sc.ownsGroup(group) {
			continue
		}
		op, found := entry.Operations[id]
		if !found {
			continue
		}
		if !reconcile || op.State != "unknown" {
			return op.Clone(), nil
		}
		auth, resolveErr := s.resolveAuthority(ctx, sc, false)
		if errors.Is(resolveErr, freshconfig.ErrPluginAuthority) ||
			errors.Is(resolveErr, cpa.ErrExternalRejectionUnavailable) ||
			errors.Is(resolveErr, cpa.ErrManagementRejected) ||
			errors.Is(resolveErr, cpa.ErrExternalTargetMoved) {
			return op.Clone(), resolveErr
		}
		if resolveErr != nil || !auth.capable.Readable || auth.transport == nil {
			return op.Clone(), cpa.ErrApplyUnknown
		}
		if op.SourceID != "" && op.SourceID != auth.sourceID {
			return op.Clone(), cpa.ErrSourceConflict
		}
		return s.resolveOperationWithMode(ctx, group, auth, op, true)
	}
	return freshstore.ConfigGroupOperation{}, freshconfig.ErrNotFound
}

func (s *upgradeConfigService) appendOperation(ctx context.Context, groupID string, op freshstore.ConfigGroupOperation) error {
	return s.updateJournal(ctx, func(groups map[string]*freshstore.ConfigGroupJournal) error {
		entry := groups[groupID]
		if entry == nil {
			entry = &freshstore.ConfigGroupJournal{Operations: map[string]freshstore.ConfigGroupOperation{}}
			groups[groupID] = entry
		}
		if entry.Operations == nil {
			entry.Operations = map[string]freshstore.ConfigGroupOperation{}
		}
		if _, exists := entry.Operations[op.RequestID]; exists {
			return freshstore.ErrConflict
		}
		if latest, exists := entry.Operations[entry.Latest]; exists && latest.State == "unknown" {
			return freshstore.ErrConflict
		}
		entry.Operations[op.RequestID] = op.Clone()
		entry.Latest = op.RequestID
		return nil
	})
}

func (s *upgradeConfigService) updateOperation(ctx context.Context, groupID string, previous, next freshstore.ConfigGroupOperation) error {
	return s.updateJournal(ctx, func(groups map[string]*freshstore.ConfigGroupJournal) error {
		entry := groups[groupID]
		if entry == nil {
			return freshstore.ErrConflict
		}
		stored, exists := entry.Operations[previous.RequestID]
		if !exists || stored.State != previous.State || stored.HTTP200 != previous.HTTP200 {
			return freshstore.ErrConflict
		}
		entry.Operations[previous.RequestID] = next.Clone()
		return nil
	})
}

func upgradeGroupBaseline(document map[string]any, group cpa.ConfigGroup) string {
	snapshot := make(map[string]any, len(group.Leaves))
	for _, path := range group.Leaves {
		val, presence := cpa.LookupConfigValue(document, path)
		if presence != cpa.ConfigPresent {
			snapshot[path] = map[string]any{"presence": string(presence)}
			continue
		}
		snapshot[path] = map[string]any{"presence": string(presence), "value": val}
	}
	return upgradeCanonicalDigest(snapshot)
}

func upgradeFamilyBaseline(document map[string]any, path string) string {
	val, presence := cpa.LookupConfigValue(document, path)
	if presence != cpa.ConfigPresent {
		return upgradeCanonicalDigest(map[string]any{"presence": string(presence)})
	}
	return upgradeCanonicalDigest(map[string]any{"presence": string(presence), "value": val})
}

func upgradeCanonicalDigest(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	var normalized any
	dec := json.NewDecoder(bytes.NewReader(encoded))
	dec.UseNumber()
	if dec.Decode(&normalized) == nil {
		if normBytes, normErr := json.Marshal(normalized); normErr == nil {
			encoded = normBytes
		}
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func upgradeGroupPatch(prefix string, edits []freshstore.ConfigGroupEdit) ([]byte, bool) {
	if prefix == "" {
		for _, edit := range edits {
			if edit.Action == "set" {
				return []byte("{}"), true
			}
		}
		return nil, false
	}
	prefixParts := strings.Split(prefix, ".")
	body := map[string]any{}
	written := false
	for _, edit := range edits {
		if edit.Action != "set" {
			continue
		}
		parts := strings.Split(edit.Path, ".")
		if len(parts) <= len(prefixParts) {
			return nil, false
		}
		cursor := body
		for _, part := range parts[len(prefixParts) : len(parts)-1] {
			next, ok := cursor[part].(map[string]any)
			if !ok {
				next = map[string]any{}
				cursor[part] = next
			}
			cursor = next
		}
		cursor[parts[len(parts)-1]] = edit.Value
		written = true
	}
	if !written {
		return nil, false
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, false
	}
	return payload, true
}

func upgradeConfigOperationProgress(document map[string]any, operation freshstore.ConfigGroupOperation) (int, int) {
	switch operation.Target {
	case "family":
		if upgradeConfigOperationInPlace(document, operation) {
			return 1, 1
		}
		return 0, 1
	case "group":
		applied := 0
		for _, edit := range operation.Edits {
			value, presence := cpa.LookupConfigValue(document, edit.Path)
			switch edit.Action {
			case "default":
				if presence == cpa.ConfigMissing {
					applied++
				}
			case "set":
				if presence == cpa.ConfigPresent && upgradeCanonicalDigest(value) == upgradeCanonicalDigest(edit.Value) {
					applied++
				}
			default:
				return 0, len(operation.Edits)
			}
		}
		return applied, len(operation.Edits)
	default:
		return 0, 0
	}
}

func upgradeConfigOperationInPlace(document map[string]any, operation freshstore.ConfigGroupOperation) bool {
	switch operation.Target {
	case "family":
		value, presence := cpa.LookupConfigValue(document, operation.Prefix)
		if presence != cpa.ConfigPresent {
			return false
		}
		return upgradeCanonicalDigest(value) == upgradeCanonicalDigest(operation.Rules)
	case "group":
		for _, edit := range operation.Edits {
			value, presence := cpa.LookupConfigValue(document, edit.Path)
			switch edit.Action {
			case "default":
				if presence != cpa.ConfigMissing {
					return false
				}
			case "set":
				if presence != cpa.ConfigPresent || upgradeCanonicalDigest(value) != upgradeCanonicalDigest(edit.Value) {
					return false
				}
			default:
				return false
			}
		}
		return len(operation.Edits) > 0
	default:
		return false
	}
}

func sameUpgradeGroupRequest(operation freshstore.ConfigGroupOperation, edits []freshstore.ConfigGroupEdit, baseline string) bool {
	return operation.Target == "group" &&
		operation.Baseline == baseline &&
		upgradeCanonicalDigest(operation.Edits) == upgradeCanonicalDigest(edits)
}

func sameUpgradeFamilyRequest(operation freshstore.ConfigGroupOperation, path string, rules []any, baseline string) bool {
	return operation.Target == "family" &&
		operation.Prefix == path &&
		operation.Baseline == baseline &&
		upgradeCanonicalDigest(operation.Rules) == upgradeCanonicalDigest(rules)
}

func (u *upgradeCanonicalServer) serveGatewayConfig(w http.ResponseWriter, r *http.Request, resource string) {
	if r.URL.RawQuery != "" || r.URL.EscapedPath() != r.URL.Path {
		gatewayConfigReply(w, nil, freshconfig.ErrInvalid)
		return
	}
	resource = strings.TrimPrefix(strings.TrimPrefix(resource, "gateway/config"), "/")
	parts := strings.Split(resource, "/")
	for _, part := range parts {
		if resource != "" && part == "" {
			http.NotFound(w, r)
			return
		}
	}
	switch {
	case resource == "":
		if r.Method != http.MethodGet {
			methodNotAllowed(w, http.MethodGet)
			return
		}
		if u.targetMode == model.UpgradeTargetExternal {
			_, _, holdCtx, release, err := u.enterExternalCredentialHold(w, r.Context(), upgradeExternalHoldBudget)
			if err != nil {
				if errors.Is(err, errExternalCredentialWaitCancelled) {
					writeExternalCredentialWaitCancelled(w)
					return
				}
				if errors.Is(err, cpa.ErrManagementRejected) {
					gatewayConfigReply(w, freshconfig.View{}, cpa.ErrManagementRejected)
					return
				}
				if errors.Is(err, cpa.ErrExternalRejectionUnavailable) {
					gatewayConfigReply(w, nil, cpa.ErrExternalRejectionUnavailable)
					return
				}
				view := freshconfig.View{
					Capability: freshconfig.Capability{Mode: freshconfig.ModeUnavailable, Reason: freshconfig.ReasonUnavailable},
				}
				if errors.Is(err, cpa.ErrExternalTargetMoved) {
					view.Capability.Reason = freshconfig.ReasonTargetMoved
				}
				gatewayConfigReply(w, view, nil)
				return
			}
			defer release()
			view, readErr := u.configService.Read(holdCtx)
			gatewayConfigReply(w, view, readErr)
			return
		}
		view, err := u.configService.Read(r.Context())
		gatewayConfigReply(w, view, err)

	case resource == "logs-capability":
		if r.Method != http.MethodGet {
			methodNotAllowed(w, http.MethodGet)
			return
		}
		if u.targetMode == model.UpgradeTargetExternal {
			_, _, holdCtx, release, err := u.enterExternalCredentialHold(w, r.Context(), upgradeExternalHoldBudget)
			if err != nil {
				if errors.Is(err, errExternalCredentialWaitCancelled) {
					writeExternalCredentialWaitCancelled(w)
					return
				}
				if errors.Is(err, cpa.ErrManagementRejected) ||
					errors.Is(err, cpa.ErrExternalTargetMoved) ||
					errors.Is(err, cpa.ErrExternalRejectionUnavailable) {
					gatewayConfigReply(w, nil, err)
					return
				}
				gatewayConfigReply(w, struct {
					Capability freshconfig.Capability `json:"capability"`
				}{
					Capability: freshconfig.Capability{Mode: freshconfig.ModeUnavailable, Reason: freshconfig.ReasonUnavailable},
				}, nil)
				return
			}
			defer release()
			capView, capErr := u.configService.LogsCapability(holdCtx)
			if capErr != nil {
				gatewayConfigReply(w, nil, capErr)
				return
			}
			gatewayConfigReply(w, struct {
				Capability freshconfig.Capability `json:"capability"`
			}{Capability: capView}, nil)
			return
		}
		capView, err := u.configService.LogsCapability(r.Context())
		if err != nil {
			gatewayConfigReply(w, nil, err)
			return
		}
		gatewayConfigReply(w, struct {
			Capability freshconfig.Capability `json:"capability"`
		}{Capability: capView}, nil)
	case parts[0] == "groups" && len(parts) == 2:
		if r.Method != http.MethodPost {
			methodNotAllowed(w, http.MethodPost)
			return
		}
		body, ok := gatewayConfigBody(w, r)
		if !ok {
			return
		}
		if !exactObject(body, "requestId", "expected", "edits") || !exactExpected(body) {
			gatewayConfigReply(w, nil, freshconfig.ErrInvalid)
			return
		}
		var req freshconfig.GroupRequest
		if json.Unmarshal(body, &req) != nil {
			gatewayConfigReply(w, nil, freshconfig.ErrInvalid)
			return
		}
		if err := freshconfig.ValidateGroupRequest(parts[1], req); err != nil {
			gatewayConfigReply(w, nil, err)
			return
		}
		if u.targetMode == model.UpgradeTargetExternal {
			_, _, holdCtx, release, err := u.enterExternalCredentialHold(w, r.Context(), upgradeExternalHoldBudget)
			if err != nil {
				if errors.Is(err, errExternalCredentialWaitCancelled) {
					writeExternalCredentialWaitCancelled(w)
					return
				}
				gatewayConfigReply(w, nil, err)
				return
			}
			defer release()
			op, saveErr := u.configService.SaveGroup(holdCtx, parts[1], req)
			gatewayConfigReply(w, op, saveErr)
			return
		}
		op, err := u.configService.SaveGroup(r.Context(), parts[1], req)
		gatewayConfigReply(w, op, err)

	case parts[0] == "rules" && len(parts) == 2:
		if r.Method != http.MethodPost {
			methodNotAllowed(w, http.MethodPost)
			return
		}
		body, ok := gatewayConfigBody(w, r)
		if !ok {
			return
		}
		if !exactObject(body, "requestId", "expected", "rules") || !exactExpected(body) {
			gatewayConfigReply(w, nil, freshconfig.ErrInvalid)
			return
		}
		var req freshconfig.FamilyRequest
		if json.Unmarshal(body, &req) != nil {
			gatewayConfigReply(w, nil, freshconfig.ErrInvalid)
			return
		}
		if valErr := freshconfig.ValidateFamilyRequest(parts[1], req); valErr != nil {
			gatewayConfigReply(w, nil, valErr)
			return
		}
		if u.targetMode == model.UpgradeTargetExternal {
			_, _, holdCtx, release, err := u.enterExternalCredentialHold(w, r.Context(), upgradeExternalHoldBudget)
			if err != nil {
				if errors.Is(err, errExternalCredentialWaitCancelled) {
					writeExternalCredentialWaitCancelled(w)
					return
				}
				gatewayConfigReply(w, nil, err)
				return
			}
			defer release()
			op, saveErr := u.configService.SaveFamily(holdCtx, parts[1], req)
			gatewayConfigReply(w, op, saveErr)
			return
		}
		op, err := u.configService.SaveFamily(r.Context(), parts[1], req)
		gatewayConfigReply(w, op, err)

	case parts[0] == "operations" && (len(parts) == 2 || len(parts) == 3 && parts[2] == "reconcile"):
		reconcile := len(parts) == 3
		wantMethod := http.MethodGet
		if reconcile {
			wantMethod = http.MethodPost
		}
		if r.Method != wantMethod {
			methodNotAllowed(w, wantMethod)
			return
		}
		if reconcile {
			body, ok := gatewayConfigBody(w, r)
			if !ok {
				return
			}
			if !exactObject(body) {
				gatewayConfigReply(w, nil, freshconfig.ErrInvalid)
				return
			}
			if u.targetMode == model.UpgradeTargetExternal {
				_, _, holdCtx, release, err := u.enterExternalCredentialHold(w, r.Context(), upgradeExternalHoldBudget)
				if err != nil {
					if errors.Is(err, errExternalCredentialWaitCancelled) {
						writeExternalCredentialWaitCancelled(w)
						return
					}
					gatewayConfigReply(w, nil, err)
					return
				}
				defer release()
				op, opErr := u.configService.Operation(holdCtx, parts[1], true)
				gatewayConfigReply(w, op, opErr)
				return
			}
		}
		op, err := u.configService.Operation(r.Context(), parts[1], reconcile)
		gatewayConfigReply(w, op, err)
	default:
		http.NotFound(w, r)
	}
}

func (u *upgradeCanonicalServer) servePluginRuntime(w http.ResponseWriter, r *http.Request, resource string) {
	if r.URL.RawQuery != "" || r.URL.EscapedPath() != r.URL.Path {
		gatewayConfigReply(w, nil, freshconfig.ErrInvalid)
		return
	}
	resource = strings.TrimPrefix(strings.TrimPrefix(resource, "plugins/runtime"), "/")
	parts := strings.Split(resource, "/")
	for _, part := range parts {
		if resource != "" && part == "" {
			http.NotFound(w, r)
			return
		}
	}
	switch {
	case resource == "":
		if r.Method != http.MethodGet {
			methodNotAllowed(w, http.MethodGet)
			return
		}
		view, err := u.configService.ReadPlugin(r.Context())
		gatewayConfigReply(w, view, err)
	case parts[0] == "groups" && len(parts) == 2:
		if r.Method != http.MethodPost {
			methodNotAllowed(w, http.MethodPost)
			return
		}
		body, ok := gatewayConfigBody(w, r)
		if !ok {
			return
		}
		if !exactObject(body, "requestId", "expected", "edits") || !exactExpected(body) {
			gatewayConfigReply(w, nil, freshconfig.ErrInvalid)
			return
		}
		var req freshconfig.GroupRequest
		if json.Unmarshal(body, &req) != nil {
			gatewayConfigReply(w, nil, freshconfig.ErrInvalid)
			return
		}
		if valErr := freshconfig.ValidatePluginGroupRequest(parts[1], req); valErr != nil {
			gatewayConfigReply(w, nil, valErr)
			return
		}
		op, err := u.configService.SavePluginGroup(r.Context(), parts[1], req)
		gatewayConfigReply(w, op, err)
	case parts[0] == "operations" && (len(parts) == 2 || len(parts) == 3 && parts[2] == "reconcile"):
		reconcile := len(parts) == 3
		wantMethod := http.MethodGet
		if reconcile {
			wantMethod = http.MethodPost
		}
		if r.Method != wantMethod {
			methodNotAllowed(w, wantMethod)
			return
		}
		if reconcile {
			body, ok := gatewayConfigBody(w, r)
			if !ok {
				return
			}
			if !exactObject(body) {
				gatewayConfigReply(w, nil, freshconfig.ErrInvalid)
				return
			}
		}
		op, err := u.configService.PluginOperation(r.Context(), parts[1], reconcile)
		gatewayConfigReply(w, op, err)
	default:
		http.NotFound(w, r)
	}
}

// --- Runtime Status, Lifecycle Operations, Artifact & Update Channel ---

func (u *upgradeCanonicalServer) serveRuntimeStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	if r.URL.RawQuery != "" || r.URL.EscapedPath() != r.URL.Path {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	now := time.Now().UTC()
	if u.targetMode == model.UpgradeTargetExternal {
		install, credDigest, holdCtx, release, err := u.enterExternalCredentialHold(w, r.Context(), upgradeExternalHoldBudget)
		if err != nil {
			if errors.Is(err, errExternalCredentialWaitCancelled) {
				writeExternalCredentialWaitCancelled(w)
				return
			}
			if errors.Is(err, cpa.ErrManagementRejected) {
				writeFreshJSON(w, freshRuntimeUnknownResponse("external", freshconfig.ReasonManagementRejected, u.runtimeJournal))
				return
			}
			writeFreshJSON(w, freshRuntimeUnknownResponse("external", "runtime_unreachable", u.runtimeJournal))
			return
		}
		observed, obsErr := u.runtimeExternal.Status(holdCtx)
		release()
		if obsErr != nil {
			u.recordExternalAuthFailureIfMatching(r.Context(), install, credDigest, obsErr)
			if errors.Is(obsErr, cpa.ErrManagementRejected) {
				writeFreshJSON(w, freshRuntimeUnknownResponse("external", freshconfig.ReasonManagementRejected, u.runtimeJournal))
				return
			}
			writeFreshJSON(w, freshRuntimeUnknownResponse("external", "runtime_unreachable", u.runtimeJournal))
			return
		}
		gateway := freshRuntimeGatewayStatus(observed)
		report := cpa.ClassifyExternalVersion(string(observed.CPAObservedVersion))
		gateway.VersionSupport = string(report.Support)
		gateway.WriteBlocked = report.WriteBlocked
		resp := freshRuntimeStatusResponse{
			Mode:       "external",
			Status:     string(observed.State),
			ObservedAt: &now,
			Manager:    freshRuntimeManagerStatus{State: "ready"},
			Gateway:    gateway,
		}
		u.runtimeJournal.setLast(&resp, now)
		writeFreshJSON(w, resp)
		return
	}

	_, observed, err := u.observeVerifiedManagedRuntime(r.Context())
	switch {
	case errors.Is(err, errUpgradeManagedRuntimeUnavailable):
		writeFreshJSON(w, freshRuntimeUnknownResponse("embedded", "runtime_unavailable", u.runtimeJournal))
		return
	case errors.Is(err, errUpgradeManagedRuntimeUnreachable):
		writeFreshJSON(w, freshRuntimeUnknownResponse("embedded", "runtime_unreachable", u.runtimeJournal))
		return
	case errors.Is(err, errUpgradeManagedRuntimeUnknown):
		writeFreshJSON(w, freshRuntimeUnknownResponse("embedded", "runtime_observation_unknown", u.runtimeJournal))
		return
	case err != nil:
		writeFreshJSON(w, freshRuntimeUnknownResponse("embedded", "runtime_unavailable", u.runtimeJournal))
		return
	}
	_, _ = u.refreshUpgradeRuntimeOperations(r.Context(), &observed)
	gateway := freshRuntimeGatewayStatus(observed)
	resp := freshRuntimeStatusResponse{
		Mode:       "embedded",
		Status:     string(observed.State),
		ObservedAt: &now,
		Manager:    freshRuntimeManagerStatus{State: "ready"},
		Gateway:    gateway,
	}
	u.runtimeJournal.setLast(&resp, now)
	writeFreshJSON(w, resp)
}

var (
	errUpgradeManagedRuntimeUnavailable = errors.New("upgrade managed runtime authority unavailable")
	errUpgradeManagedRuntimeUnreachable = errors.New("upgrade managed runtime unreachable")
	errUpgradeManagedRuntimeUnknown     = errors.New("upgrade managed runtime observation unknown")
)

func (u *upgradeCanonicalServer) loadVerifiedManagedDestination(ctx context.Context) (upgradeDestinationRecord, string, error) {
	if u == nil || u.targetMode != model.UpgradeTargetManaged || u.workspace == nil || u.runtime == nil || u.db == nil || u.db.SQLDB() == nil {
		return upgradeDestinationRecord{}, "", errUpgradeManagedRuntimeUnavailable
	}
	if _, err := u.loadCurrentOp(ctx); err != nil {
		return upgradeDestinationRecord{}, "", errUpgradeManagedRuntimeUnavailable
	}
	rec, secret, err := u.workspace.loadManagedDestination(ctx)
	if err != nil {
		return upgradeDestinationRecord{}, "", errUpgradeManagedRuntimeUnavailable
	}
	return rec, secret, nil
}

func (u *upgradeCanonicalServer) observeVerifiedManagedRuntime(ctx context.Context) (upgradeDestinationRecord, model.RuntimeObservedStatus, error) {
	rec, _, err := u.loadVerifiedManagedDestination(ctx)
	if err != nil {
		return upgradeDestinationRecord{}, model.RuntimeObservedStatus{}, err
	}
	observed, err := u.runtime.Status(ctx)
	if err != nil {
		return rec, model.RuntimeObservedStatus{}, errUpgradeManagedRuntimeUnreachable
	}
	if observed.State == model.RuntimeStateUnknown {
		return rec, model.RuntimeObservedStatus{}, errUpgradeManagedRuntimeUnknown
	}
	if observed.Validate() != nil ||
		observed.ProtocolVersion != "v1" ||
		observed.Identity == "" ||
		observed.Identity != rec.Intent.ExpectedRuntimeIdentity ||
		observed.Generation == 0 {
		return rec, model.RuntimeObservedStatus{}, errUpgradeManagedRuntimeUnavailable
	}
	return rec, observed, nil
}

type fencedUpgradeArtifactRuntime struct {
	owner *upgradeCanonicalServer
}

func (f fencedUpgradeArtifactRuntime) check(ctx context.Context) (*runtimeservice.ArtifactClient, error) {
	if f.owner == nil || f.owner.artifact == nil {
		return nil, errEmbeddedRuntimeDisabled
	}
	if _, _, err := f.owner.observeVerifiedManagedRuntime(ctx); err != nil {
		return nil, err
	}
	return f.owner.artifact, nil
}

func (f fencedUpgradeArtifactRuntime) Status(ctx context.Context) (runtimeservice.ArtifactStatus, error) {
	client, err := f.check(ctx)
	if err != nil {
		return runtimeservice.ArtifactStatus{}, err
	}
	return client.Status(ctx)
}

func (f fencedUpgradeArtifactRuntime) Probe(ctx context.Context) (runtimeservice.ArtifactProbeResult, error) {
	client, err := f.check(ctx)
	if err != nil {
		return runtimeservice.ArtifactProbeResult{}, err
	}
	return client.Probe(ctx)
}

func (f fencedUpgradeArtifactRuntime) Acquire(ctx context.Context, source, cdnPrefix string) (runtimeservice.ArtifactOperationEnvelope, error) {
	client, err := f.check(ctx)
	if err != nil {
		return runtimeservice.ArtifactOperationEnvelope{}, err
	}
	return client.Acquire(ctx, source, cdnPrefix)
}

func (f fencedUpgradeArtifactRuntime) Upload(ctx context.Context, body io.Reader, contentLength int64) (runtimeservice.ArtifactOperationEnvelope, error) {
	client, err := f.check(ctx)
	if err != nil {
		return runtimeservice.ArtifactOperationEnvelope{}, err
	}
	return client.Upload(ctx, body, contentLength)
}

func (u *upgradeCanonicalServer) serveRuntimeArtifact(w http.ResponseWriter, r *http.Request, resource string) {
	var client artifactproxy.ArtifactRuntime
	if u.targetMode == model.UpgradeTargetManaged && u.artifact != nil {
		client = fencedUpgradeArtifactRuntime{owner: u}
	}
	artifactproxy.ServeResource(w, r, client, resource)
}

func (u *upgradeCanonicalServer) serveRuntimeOperations(w http.ResponseWriter, r *http.Request, resource string) {
	if r.URL.RawQuery != "" || r.URL.EscapedPath() != r.URL.Path {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	rest := strings.TrimPrefix(resource, "runtime/operations")
	if rest == "" {
		if r.Method != http.MethodPost {
			methodNotAllowed(w, http.MethodPost)
			return
		}
		u.submitUpgradeRuntimeOperation(w, r)
		return
	}
	id := strings.TrimPrefix(rest, "/")
	if strings.Contains(id, "/") {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	if !freshRuntimeOperationIDPattern.MatchString(id) {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	var obsPtr *model.RuntimeObservedStatus
	var expectedIdentity model.RuntimeIdentity
	if u.targetMode == model.UpgradeTargetManaged {
		rec, liveObs, obsErr := u.observeVerifiedManagedRuntime(r.Context())
		if errors.Is(obsErr, errUpgradeManagedRuntimeUnavailable) {
			freshRuntimeOperationError(w, http.StatusServiceUnavailable, "runtime_unavailable")
			return
		}
		expectedIdentity = rec.Intent.ExpectedRuntimeIdentity
		if obsErr == nil {
			obsPtr = &liveObs
		}
	} else {
		freshRuntimeOperationError(w, http.StatusConflict, "runtime_mutation_unsupported")
		return
	}
	data, err := u.refreshUpgradeRuntimeOperations(r.Context(), obsPtr)
	if err != nil {
		if errors.Is(err, errUpgradeManagedRuntimeUnavailable) {
			freshRuntimeOperationError(w, http.StatusServiceUnavailable, "runtime_unavailable")
			return
		}
		freshRuntimeOperationError(w, http.StatusServiceUnavailable, "operation_journal_unavailable")
		return
	}
	op, found := data.Operations[id]
	if !found {
		for _, candidate := range data.Operations {
			if candidate.OperationID == id {
				op = candidate
				found = true
				break
			}
		}
	}
	if !found || op.State == freshRuntimeStateReserved {
		http.NotFound(w, r)
		return
	}
	if expectedIdentity != "" && op.RuntimeIdentity != "" && op.RuntimeIdentity != string(expectedIdentity) {
		freshRuntimeOperationError(w, http.StatusServiceUnavailable, "runtime_unavailable")
		return
	}
	writeFreshJSON(w, op.response())
}

func (u *upgradeCanonicalServer) refreshUpgradeRuntimeOperations(
	ctx context.Context,
	observed *model.RuntimeObservedStatus,
) (freshRuntimeJournalData, error) {
	var expectedIdentity model.RuntimeIdentity
	if u.targetMode == model.UpgradeTargetManaged {
		rec, _, destErr := u.loadVerifiedManagedDestination(ctx)
		if destErr != nil {
			return freshRuntimeJournalData{}, errUpgradeManagedRuntimeUnavailable
		}
		expectedIdentity = rec.Intent.ExpectedRuntimeIdentity
	}
	data, err := u.runtimeJournal.load(ctx)
	if err != nil {
		return data, err
	}
	if !freshRuntimeOperationsPending(data) {
		return data, nil
	}
	var status model.RuntimeObservedStatus
	observedOK := false
	if observed != nil {
		if expectedIdentity != "" && observed.Identity != expectedIdentity {
			return data, errUpgradeManagedRuntimeUnavailable
		}
		status, observedOK = *observed, true
	} else if u.targetMode == model.UpgradeTargetManaged {
		_, liveObs, obsErr := u.observeVerifiedManagedRuntime(ctx)
		if errors.Is(obsErr, errUpgradeManagedRuntimeUnavailable) {
			return data, errUpgradeManagedRuntimeUnavailable
		}
		if obsErr == nil {
			status, observedOK = liveObs, true
		}
	}
	now := time.Now().UTC()
	transitions := make(map[string][2]freshRuntimeOperation)
	for requestID, op := range data.Operations {
		next, changed := freshRuntimeResolveOperation(op, status, observedOK, now)
		if !changed {
			continue
		}
		transitions[requestID] = [2]freshRuntimeOperation{op, next}
	}
	if len(transitions) == 0 {
		return data, nil
	}
	u.runtimeJournal.mu.Lock()
	defer u.runtimeJournal.mu.Unlock()
	latest, err := u.runtimeJournal.load(ctx)
	if err != nil {
		return data, err
	}
	applied := false
	for requestID, transition := range transitions {
		stored, found := latest.Operations[requestID]
		if !found || stored.State != transition[0].State || !stored.UpdatedAt.Equal(transition[0].UpdatedAt) {
			continue
		}
		latest.Operations[requestID] = transition[1]
		applied = true
	}
	if applied {
		if err := u.runtimeJournal.save(ctx, latest); err != nil {
			return data, err
		}
	}
	return latest, nil
}

func (u *upgradeCanonicalServer) submitUpgradeRuntimeOperation(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, upgradeRuntimeMutationMaxBytes))
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if !exactObject(body, "requestId", "action") {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	var req struct {
		RequestID string `json:"requestId"`
		Action    string `json:"action"`
	}
	if json.Unmarshal(body, &req) != nil || !freshRuntimeRequestIDPattern.MatchString(req.RequestID) {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	switch req.Action {
	case string(model.RuntimeOperationStart), string(model.RuntimeOperationStop), string(model.RuntimeOperationRestart):
	default:
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if u.targetMode == model.UpgradeTargetExternal {
		freshRuntimeOperationError(w, http.StatusConflict, "runtime_mutation_unsupported")
		return
	}
	if _, _, preDestErr := u.loadVerifiedManagedDestination(r.Context()); preDestErr != nil {
		freshRuntimeOperationError(w, http.StatusServiceUnavailable, "runtime_unavailable")
		return
	}
	unlockLifecycle, lockErr := u.runtimeLifecycleMu.Lock(r.Context())
	if lockErr != nil {
		freshRuntimeOperationError(w, http.StatusServiceUnavailable, "runtime_unreachable")
		return
	}
	defer unlockLifecycle()
	if _, _, err := u.db.LoadEmbeddedRuntimeDesiredState(r.Context()); err != nil {
		freshRuntimeOperationError(w, http.StatusServiceUnavailable, "desired_lifecycle_unavailable")
		return
	}
	rec, observed, obsErr := u.observeVerifiedManagedRuntime(r.Context())
	switch {
	case errors.Is(obsErr, errUpgradeManagedRuntimeUnavailable):
		freshRuntimeOperationError(w, http.StatusServiceUnavailable, "runtime_unavailable")
		return
	case errors.Is(obsErr, errUpgradeManagedRuntimeUnreachable):
		freshRuntimeOperationError(w, http.StatusServiceUnavailable, "runtime_unreachable")
		return
	case errors.Is(obsErr, errUpgradeManagedRuntimeUnknown):
		freshRuntimeOperationError(w, http.StatusServiceUnavailable, "runtime_observation_unknown")
		return
	case obsErr != nil:
		freshRuntimeOperationError(w, http.StatusServiceUnavailable, "runtime_unavailable")
		return
	}
	if !observed.Capabilities.Supports(model.RuntimeCapability(req.Action)) {
		freshRuntimeOperationError(w, http.StatusConflict, "capability_not_supported")
		return
	}
	if _, err := u.refreshUpgradeRuntimeOperations(r.Context(), &observed); err != nil {
		freshRuntimeOperationError(w, http.StatusServiceUnavailable, "operation_journal_unavailable")
		return
	}

	now := time.Now().UTC()
	candidate := freshRuntimeOperation{
		RequestID:         req.RequestID,
		Action:            req.Action,
		OperationID:       "runtime-ui/v1:" + req.RequestID,
		State:             freshRuntimeStateAccepted,
		RuntimeIdentity:   string(observed.Identity),
		RuntimeGeneration: uint64(observed.Generation),
		CreatedAt:         now,
		UpdatedAt:         now,
	}

	u.runtimeJournal.mu.Lock()
	data, err := u.runtimeJournal.load(r.Context())
	if err != nil {
		u.runtimeJournal.mu.Unlock()
		freshRuntimeOperationError(w, http.StatusServiceUnavailable, "operation_journal_unavailable")
		return
	}
	if existing, found := data.Operations[req.RequestID]; found && existing.State != freshRuntimeStateReserved {
		u.runtimeJournal.mu.Unlock()
		if existing.Action != req.Action {
			freshRuntimeOperationError(w, http.StatusConflict, "request_id_conflict")
			return
		}
		writeFreshJSON(w, existing.response())
		return
	}
	if freshRuntimeOperationBlocks(data, candidate) {
		u.runtimeJournal.mu.Unlock()
		freshRuntimeOperationError(w, http.StatusConflict, "operation_in_progress")
		return
	}
	data.Operations[req.RequestID] = candidate
	if err := u.runtimeJournal.save(r.Context(), data); err != nil {
		u.runtimeJournal.mu.Unlock()
		freshRuntimeOperationError(w, http.StatusServiceUnavailable, "operation_journal_unavailable")
		return
	}
	u.runtimeJournal.mu.Unlock()

	var expectedArtifact model.RuntimeArtifactID
	opType := model.RuntimeOperationType(req.Action)
	if opType == model.RuntimeOperationStart {
		if observed.ActiveGatewayArtifact != nil {
			expectedArtifact = observed.ActiveGatewayArtifact.ArtifactID
		} else {
			expectedArtifact = rec.Intent.ExpectedActiveArtifactID
		}
	}
	mutCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), freshRuntimeMutationBudget)
	defer cancel()
	mutReq := model.RuntimeMutationRequest{
		OperationID:               candidate.OperationID,
		ExpectedRuntimeIdentity:   observed.Identity,
		ExpectedRuntimeGeneration: observed.Generation,
		ExpectedActiveArtifactID:  expectedArtifact,
	}
	var (
		mutRes model.RuntimeOperationResult
		mutErr error
	)
	switch opType {
	case model.RuntimeOperationStart:
		mutRes, mutErr = u.runtime.Start(mutCtx, mutReq)
	case model.RuntimeOperationStop:
		mutRes, mutErr = u.runtime.Stop(mutCtx, mutReq)
	case model.RuntimeOperationRestart:
		mutRes, mutErr = u.runtime.Restart(mutCtx, mutReq)
	}

	if mutErr != nil {
		if conflict := runtimeMutationConflictCode(mutErr); conflict != "" {
			candidate.State = freshRuntimeStateFailed
			candidate.FailureCode = conflict
			candidate.UpdatedAt = time.Now().UTC()
			_ = u.saveUpgradeRuntimeOperationEntry(mutCtx, candidate)
			freshRuntimeOperationError(w, http.StatusConflict, conflict)
			return
		}
		candidate.State = freshRuntimeStateUnknown
		candidate.FailureCode = "runtime_result_unknown"
		desired := model.EmbeddedRuntimeDesiredRunning
		if opType == model.RuntimeOperationStop {
			desired = model.EmbeddedRuntimeDesiredStopped
		}
		_, _ = u.db.SetEmbeddedRuntimeDesiredLifecycle(mutCtx, desired)
		candidate.UpdatedAt = time.Now().UTC()
		_ = u.saveUpgradeRuntimeOperationEntry(mutCtx, candidate)
		writeFreshJSON(w, candidate.response())
		return
	}

	if mutRes.State != model.RuntimeOperationFailed {
		desired := model.EmbeddedRuntimeDesiredRunning
		if opType == model.RuntimeOperationStop {
			desired = model.EmbeddedRuntimeDesiredStopped
		}
		_, _ = u.db.SetEmbeddedRuntimeDesiredLifecycle(mutCtx, desired)
	}

	switch mutRes.State {
	case model.RuntimeOperationSucceeded:
		candidate.State = freshRuntimeStateSucceeded
	case model.RuntimeOperationFailed:
		candidate.State = freshRuntimeStateFailed
		if mutRes.Failure != nil {
			candidate.FailureCode = mutRes.Failure.Code
		}
		if candidate.FailureCode == "" {
			candidate.FailureCode = "operation_failed"
		}
	default:
		candidate.State = freshRuntimeStateAccepted
	}
	candidate.UpdatedAt = time.Now().UTC()
	_ = u.saveUpgradeRuntimeOperationEntry(mutCtx, candidate)
	writeFreshJSON(w, candidate.response())
}

func (u *upgradeCanonicalServer) saveUpgradeRuntimeOperationEntry(ctx context.Context, entry freshRuntimeOperation) error {
	u.runtimeJournal.mu.Lock()
	defer u.runtimeJournal.mu.Unlock()
	data, err := u.runtimeJournal.load(ctx)
	if err != nil {
		return err
	}
	data.Operations[entry.RequestID] = entry
	return u.runtimeJournal.save(ctx, data)
}

func (u *upgradeCanonicalServer) serveUpdateChannel(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" || r.URL.EscapedPath() != r.URL.Path {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if u.updates == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	switch r.Method {
	case http.MethodGet:
		preference, err := u.updates.SavedChannel(r.Context())
		if err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		writeFreshJSON(w, struct {
			Preference string `json:"channel_preference"`
		}{Preference: preference})
	case http.MethodPut:
		if r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Content-Encoding") != "" {
			w.WriteHeader(http.StatusUnsupportedMediaType)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1024))
		if err != nil {
			var maxErr *http.MaxBytesError
			if errors.As(err, &maxErr) {
				w.WriteHeader(http.StatusRequestEntityTooLarge)
				return
			}
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if !exactObject(body, "channel_preference") {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var input struct {
			Preference string `json:"channel_preference"`
		}
		if json.Unmarshal(body, &input) != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		switch input.Preference {
		case "auto", "stable", "rc", "beta":
		default:
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if _, err := u.updates.PersistChannel(r.Context(), input.Preference); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		writeFreshJSON(w, struct {
			Preference string `json:"channel_preference"`
		}{Preference: input.Preference})
	default:
		methodNotAllowed(w, "GET, PUT")
	}
}

// --- External Usage & External Rebind Resources ---

func writeUpgradeUsageErrorJSON(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	writeFreshJSON(w, struct {
		Code string `json:"code"`
	}{Code: code})
}

func (u *upgradeCanonicalServer) serveExternalUsage(w http.ResponseWriter, r *http.Request, resource string) {
	if resource != "external/usage" {
		http.NotFound(w, r)
		return
	}
	if r.URL.RawQuery != "" || r.URL.EscapedPath() != r.URL.Path {
		writeUpgradeUsageErrorJSON(w, http.StatusBadRequest, "external_usage_invalid")
		return
	}
	switch r.Method {
	case http.MethodGet, http.MethodPost:
	default:
		methodNotAllowed(w, "GET, POST")
		return
	}
	if u.targetMode != model.UpgradeTargetExternal {
		writeUpgradeUsageErrorJSON(w, http.StatusConflict, "external_usage_not_applicable")
		return
	}

	if r.Method == http.MethodGet {
		install, credDigest, holdCtx, release, err := u.enterExternalCredentialHold(w, r.Context(), upgradeExternalUsageBudget)
		if err != nil {
			u.writeUpgradeExternalUsageError(w, err)
			return
		}
		defer release()
		state, stateErr := u.readUpgradeExternalUsageState(holdCtx, install)
		if stateErr != nil {
			u.recordExternalAuthFailureIfMatching(r.Context(), install, credDigest, stateErr)
			u.writeUpgradeExternalUsageError(w, stateErr)
			return
		}
		writeFreshJSON(w, state)
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, upgradeExternalUsageMaxBody))
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			return
		}
		writeUpgradeUsageErrorJSON(w, http.StatusBadRequest, "external_usage_invalid")
		return
	}
	if !exactObject(body, "requestId", "confirmEnableStatistics") {
		writeUpgradeUsageErrorJSON(w, http.StatusBadRequest, "external_usage_invalid")
		return
	}
	var input struct {
		RequestID               string `json:"requestId"`
		ConfirmEnableStatistics bool   `json:"confirmEnableStatistics"`
	}
	if json.Unmarshal(body, &input) != nil || !freshkeys.ValidRequestID(input.RequestID) || !input.ConfirmEnableStatistics {
		writeUpgradeUsageErrorJSON(w, http.StatusBadRequest, "external_usage_invalid")
		return
	}

	install, credDigest, holdCtx, release, err := u.enterExternalCredentialHold(w, r.Context(), upgradeExternalUsageBudget)
	if err != nil {
		u.writeUpgradeExternalUsageError(w, err)
		return
	}
	defer release()

	report, err := cpa.ObserveExternalVersion(holdCtx, install.BaseURL, install.ManagementKey)
	if err != nil {
		u.recordExternalAuthFailureIfMatching(r.Context(), install, credDigest, err)
		u.writeUpgradeExternalUsageError(w, err)
		return
	}
	if report.WriteBlocked {
		writeUpgradeUsageErrorJSON(w, http.StatusConflict, freshconfig.ReasonVersionUnsupported)
		return
	}
	enabled, err := cpa.FetchExternalUsageStatistics(holdCtx, install.BaseURL, install.ManagementKey)
	if err != nil {
		u.recordExternalAuthFailureIfMatching(r.Context(), install, credDigest, err)
		u.writeUpgradeExternalUsageError(w, err)
		return
	}
	if !enabled {
		if err := cpa.SetExternalUsageStatistics(holdCtx, install.BaseURL, install.ManagementKey, true); err != nil {
			u.recordExternalAuthFailureIfMatching(r.Context(), install, credDigest, err)
			u.writeUpgradeExternalUsageError(w, err)
			return
		}
		back, backErr := cpa.FetchExternalUsageStatistics(holdCtx, install.BaseURL, install.ManagementKey)
		if backErr != nil {
			u.recordExternalAuthFailureIfMatching(r.Context(), install, credDigest, backErr)
			u.writeUpgradeExternalUsageError(w, backErr)
			return
		}
		if !back {
			writeUpgradeUsageErrorJSON(w, http.StatusServiceUnavailable, "external_usage_unavailable")
			return
		}
	}
	if err := u.enableAndReconcileExternalCollector(holdCtx, install); err != nil {
		writeUpgradeUsageErrorJSON(w, http.StatusServiceUnavailable, "external_usage_unavailable")
		return
	}
	state, stateErr := u.currentExternalUsageState(holdCtx, true, report)
	if stateErr != nil {
		writeUpgradeUsageErrorJSON(w, http.StatusServiceUnavailable, "external_usage_unavailable")
		return
	}
	writeFreshJSON(w, state)
}

func (u *upgradeCanonicalServer) readUpgradeExternalUsageState(
	ctx context.Context,
	install freshkeyapply.ExternalInstall,
) (externalUsageState, error) {
	report, err := cpa.ObserveExternalVersion(ctx, install.BaseURL, install.ManagementKey)
	if err != nil {
		return externalUsageState{}, err
	}
	enabled, err := cpa.FetchExternalUsageStatistics(ctx, install.BaseURL, install.ManagementKey)
	if err != nil {
		return externalUsageState{}, err
	}
	if enabled {
		u.reconcileCollectorBestEffort(ctx)
	}
	return u.currentExternalUsageState(ctx, enabled, report)
}

func (u *upgradeCanonicalServer) currentExternalUsageState(
	ctx context.Context,
	statisticsEnabled bool,
	report cpa.ExternalVersionReport,
) (externalUsageState, error) {
	collectorState := "disabled"
	collectorRunning := false
	if u.appCtx != nil && u.appCtx.Collector != nil {
		st := u.appCtx.Collector.Status()
		if st.Collector != "" {
			collectorState = st.Collector
		}
		collectorRunning = st.Collector == "running"
	}
	var startedAtMS int64
	if u.workspace != nil && u.workspace.usage != nil {
		var err error
		startedAtMS, err = usageadapter.New(u.workspace.db).CollectionStartedAtMS(ctx)
		if err != nil {
			return externalUsageState{}, err
		}
	}
	started := collectorRunning && startedAtMS > 0
	blocked := ""
	if !statisticsEnabled && report.WriteBlocked {
		blocked = freshconfig.ReasonVersionUnsupported
	} else if statisticsEnabled && !started {
		blocked = "usage_collection_unavailable"
	}
	return externalUsageState{
		StatisticsEnabled: statisticsEnabled,
		Collector:         collectorState,
		CollectionStarted: started,
		CPA: externalVersionView{
			Version:      report.Version,
			Support:      string(report.Support),
			WriteBlocked: report.WriteBlocked,
		},
		BlockedReason: blocked,
	}, nil
}

func (u *upgradeCanonicalServer) writeUpgradeExternalUsageError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errExternalCredentialWaitCancelled):
		writeExternalCredentialWaitCancelled(w)
	case errors.Is(err, cpa.ErrManagementClientForbidden):
		writeExternalManagementForbidden(w)
	case errors.Is(err, cpa.ErrManagementRejected):
		writeExternalManagementRejected(w)
	case errors.Is(err, cpa.ErrExternalTargetMoved):
		writeUpgradeUsageErrorJSON(w, http.StatusConflict, freshconfig.ReasonTargetMoved)
	case errors.Is(err, cpa.ErrExternalVersionUnsupported):
		writeUpgradeUsageErrorJSON(w, http.StatusConflict, freshconfig.ReasonVersionUnsupported)
	default:
		writeUpgradeUsageErrorJSON(w, http.StatusServiceUnavailable, "external_usage_unavailable")
	}
}

func (u *upgradeCanonicalServer) serveExternalRebind(w http.ResponseWriter, r *http.Request, resource string) {
	if resource != "external/rebind" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	if r.URL.RawQuery != "" || r.URL.EscapedPath() != r.URL.Path {
		writeExternalRebindErrorJSON(w, http.StatusBadRequest, "external_rebind_invalid", "external rebind accepts no query")
		return
	}
	if u.targetMode != model.UpgradeTargetExternal {
		writeExternalRebindErrorJSON(w, http.StatusConflict, "external_rebind_not_applicable", "rebind applies only to External CPA installations")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, upgradeExternalRebindMaxBody))
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			return
		}
		writeExternalRebindErrorJSON(w, http.StatusBadRequest, "external_rebind_invalid", "request body could not be read")
		return
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil {
		writeExternalRebindErrorJSON(w, http.StatusBadRequest, "external_rebind_invalid", "request body must be a JSON object")
		return
	}
	for k := range fields {
		switch k {
		case "requestId", "managementKey", "baseUrl":
		default:
			writeExternalRebindErrorJSON(w, http.StatusBadRequest, "external_rebind_invalid", "unknown field in rebind request")
			return
		}
	}
	var input struct {
		RequestID     string `json:"requestId"`
		ManagementKey string `json:"managementKey"`
		BaseURL       string `json:"baseUrl"`
	}
	if json.Unmarshal(body, &input) != nil || !freshkeys.ValidRequestID(input.RequestID) {
		writeExternalRebindErrorJSON(w, http.StatusBadRequest, "external_rebind_invalid", "requestId is invalid")
		return
	}
	candidateKey := strings.TrimSpace(input.ManagementKey)
	if !cpa.ValidV8ManagementKey(candidateKey) {
		writeExternalRebindErrorJSON(w, http.StatusBadRequest, "external_rebind_invalid", "managementKey must be a valid CPA v8 management key")
		return
	}

	if u.workspace == nil || !u.workspace.credentialGate.acquire(r.Context()) {
		writeExternalCredentialWaitCancelled(w)
		return
	}
	defer u.workspace.credentialGate.release()
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Now().Add(upgradeExternalRebindBudget + 5*time.Second))
	rebindCtx, cancel := context.WithTimeout(r.Context(), upgradeExternalRebindBudget)
	defer cancel()

	repo := upgradeoperation.New(u.db.SQLDB())
	op, ev, ok, err := loadVerifiedCompletedUpgradeOperation(rebindCtx, repo)
	if err != nil {
		u.writeUpgradeRebindFailure(w, err)
		return
	}
	if !ok ||
		ev.TargetMode != model.UpgradeTargetExternal ||
		strings.TrimSpace(ev.SourceFingerprint) != u.sourceFingerprint ||
		validateCompletedUpgradeRoutingRow(op, model.UpgradeTargetExternal) != nil {
		u.writeUpgradeRebindFailure(w, freshkeyapply.ErrExternalUnavailable)
		return
	}
	binding, hasBinding, err := repo.ExternalBinding(rebindCtx)
	if err != nil {
		u.writeUpgradeRebindFailure(w, err)
		return
	}
	canonicalURL := upgradeservice.FencedExternalBaseURL(op, binding, hasBinding)
	if canonicalURL == "" {
		u.writeUpgradeRebindFailure(w, cpa.ErrExternalTargetMoved)
		return
	}
	envelope := strings.TrimSpace(binding.ManagementKey)
	currentSecret, err := u.protector.UnprotectString(envelope)
	if err != nil || !cpa.ValidV8ManagementKey(strings.TrimSpace(currentSecret)) {
		u.writeUpgradeRebindFailure(w, freshkeyapply.ErrExternalUnavailable)
		return
	}
	currentSecret = strings.TrimSpace(currentSecret)

	if strings.TrimSpace(input.BaseURL) != "" {
		normInput, normErr := cpa.NormalizeV8BaseURL(strings.TrimSpace(input.BaseURL))
		if normErr != nil {
			u.writeUpgradeRebindFailure(w, freshkeyapply.ErrRebindRefused)
			return
		}
		canonInput, canonErr := identity.CanonicalExternalURL(normInput)
		if canonErr != nil || canonInput != canonicalURL {
			u.writeUpgradeRebindFailure(w, freshkeyapply.ErrRebindRefused)
			return
		}
	}

	rotating := !security.EqualHMAC(candidateKey, currentSecret)
	if rotating && u.workspace.externalAuthority != nil {
		if err := u.workspace.externalAuthority.CheckRebindJournalFence(rebindCtx, binding.Identity, binding.Generation); err != nil {
			u.writeUpgradeRebindFailure(w, err)
			return
		}
	}

	report, err := freshkeyapply.ObserveExternalRebindCredential(rebindCtx, canonicalURL, candidateKey)
	switch {
	case errors.Is(err, cpa.ErrManagementClientForbidden):
		u.writeUpgradeRebindFailure(w, cpa.ErrManagementClientForbidden)
		return
	case errors.Is(err, cpa.ErrManagementRejected):
		if !rotating {
			_ = u.workspace.rejection.record(rebindCtx, binding.Identity, envelopeDigest(envelope))
		}
		u.writeUpgradeRebindFailure(w, cpa.ErrManagementRejected)
		return
	case err != nil:
		u.writeUpgradeRebindFailure(w, err)
		return
	case report.WriteBlocked:
		u.writeUpgradeRebindFailure(w, cpa.ErrExternalVersionUnsupported)
		return
	}

	if !rotating {
		if err := u.alignStoredExternalConnection(rebindCtx, canonicalURL, candidateKey); err != nil {
			u.writeUpgradeRebindFailure(w, err)
			return
		}
		clearErr := u.workspace.rejection.clear(rebindCtx)
		u.reconcileCollectorBestEffort(rebindCtx)
		if clearErr != nil {
			u.writeUpgradeRebindFailure(w, clearErr)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeFreshJSON(w, struct {
			Identity   string `json:"identity"`
			Generation uint64 `json:"generation"`
			Status     string `json:"status"`
			Revision   uint64 `json:"revision"`
		}{
			Identity:   binding.Identity,
			Generation: uint64(binding.ObservedAtMS),
			Status:     "reverified",
			Revision:   uint64(op.Revision),
		})
		return
	}

	sealedKey, err := u.protector.ProtectString(candidateKey)
	if err != nil {
		u.writeUpgradeRebindFailure(w, freshkeyapply.ErrExternalUnavailable)
		return
	}
	if u.workspace.externalAuthority != nil {
		if err := u.workspace.externalAuthority.AbortUnattemptedLatestForRebind(rebindCtx, binding.Identity, binding.Generation); err != nil {
			u.writeUpgradeRebindFailure(w, err)
			return
		}
	}
	tx, err := u.db.SQLDB().BeginTx(rebindCtx, nil)
	if err != nil {
		u.writeUpgradeRebindFailure(w, err)
		return
	}
	defer func() { _ = tx.Rollback() }()

	updatedOp, updatedBinding, err := upgradeoperation.RebindCompletedExternalConnectionTx(
		rebindCtx,
		tx,
		binding.ObservedAtMS,
		sealedKey,
		report.Version,
		string(report.Support),
	)
	if err != nil {
		if errors.Is(err, upgradeoperation.ErrConflict) {
			u.writeUpgradeRebindFailure(w, freshkeyapply.ErrGeneration)
			return
		}
		u.writeUpgradeRebindFailure(w, err)
		return
	}
	if err := u.alignStoredExternalConnectionTx(rebindCtx, tx, canonicalURL, candidateKey); err != nil {
		u.writeUpgradeRebindFailure(w, err)
		return
	}
	if err := tx.Commit(); err != nil {
		u.writeUpgradeRebindFailure(w, err)
		return
	}
	u.syncOp(updatedOp)
	u.pluginCapability.forget()
	clearErr := u.workspace.rejection.clear(rebindCtx)
	u.reconcileCollectorBestEffort(rebindCtx)
	if clearErr != nil {
		u.writeUpgradeRebindFailure(w, clearErr)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeFreshJSON(w, struct {
		Identity   string `json:"identity"`
		Generation uint64 `json:"generation"`
		Status     string `json:"status"`
		Revision   uint64 `json:"revision"`
	}{
		Identity:   updatedBinding.Identity,
		Generation: uint64(updatedBinding.ObservedAtMS),
		Status:     "rebound",
		Revision:   uint64(updatedOp.Revision),
	})
}

func (u *upgradeCanonicalServer) alignStoredExternalConnection(ctx context.Context, baseURL, managementKey string) error {
	if u == nil || u.db == nil || u.db.SQLDB() == nil {
		return errors.New("upgrade store unavailable")
	}
	tx, err := u.db.SQLDB().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := u.alignStoredExternalConnectionTx(ctx, tx, baseURL, managementKey); err != nil {
		return err
	}
	return tx.Commit()
}

func (u *upgradeCanonicalServer) alignStoredExternalConnectionTx(
	ctx context.Context,
	tx *sql.Tx,
	baseURL, managementKey string,
) error {
	if u == nil || u.protector == nil || tx == nil {
		return errors.New("upgrade store alignment unavailable")
	}
	now := time.Now().UnixMilli()
	var rawMgr string
	mgrErr := tx.QueryRowContext(ctx, `select value from settings where key = ?`, "manager_config_v1").Scan(&rawMgr)
	mgrOK := mgrErr == nil
	if mgrErr != nil && !errors.Is(mgrErr, sql.ErrNoRows) {
		return mgrErr
	}
	var prevMgrURL, prevMgrKey string
	if mgrOK {
		var mgrCfg model.ManagerConfig
		if err := json.Unmarshal([]byte(rawMgr), &mgrCfg); err != nil {
			return err
		}
		if mgrCfg.CPAConnection.ManagementKey != "" {
			plain, err := u.protector.UnprotectString(mgrCfg.CPAConnection.ManagementKey)
			if err != nil {
				return err
			}
			prevMgrKey = plain
		}
		prevMgrURL = strings.TrimSpace(mgrCfg.CPAConnection.CPABaseURL)
		sealedMgr, err := u.protector.ProtectString(managementKey)
		if err != nil {
			return err
		}
		mgrCfg.CPAConnection.CPABaseURL = baseURL
		mgrCfg.CPAConnection.ManagementKey = sealedMgr
		mgrCfg.UpdatedAtMS = now
		encodedMgr, err := json.Marshal(mgrCfg)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`insert into settings(key, value, updated_at_ms) values(?, ?, ?) on conflict(key) do update set value = excluded.value, updated_at_ms = excluded.updated_at_ms`,
			"manager_config_v1", string(encodedMgr), now); err != nil {
			return err
		}
	}

	var rawSetup string
	setupErr := tx.QueryRowContext(ctx, `select value from settings where key = ?`, "setup").Scan(&rawSetup)
	setupOK := setupErr == nil
	if setupErr != nil && !errors.Is(setupErr, sql.ErrNoRows) {
		return setupErr
	}
	var prevSetupURL, prevSetupKey string
	if setupOK {
		var setupCfg model.Setup
		if err := json.Unmarshal([]byte(rawSetup), &setupCfg); err != nil {
			return err
		}
		if setupCfg.ManagementKey != "" {
			plain, err := u.protector.UnprotectString(setupCfg.ManagementKey)
			if err != nil {
				return err
			}
			prevSetupKey = plain
		}
		prevSetupURL = strings.TrimSpace(setupCfg.CPAUpstreamURL)
		sealedSetup, err := u.protector.ProtectString(managementKey)
		if err != nil {
			return err
		}
		setupCfg.CPAUpstreamURL = baseURL
		setupCfg.ManagementKey = sealedSetup
		encodedSetup, err := json.Marshal(setupCfg)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`insert into settings(key, value, updated_at_ms) values(?, ?, ?) on conflict(key) do update set value = excluded.value, updated_at_ms = excluded.updated_at_ms`,
			"setup", string(encodedSetup), now); err != nil {
			return err
		}
	}

	if !mgrOK || !setupOK {
		_, err := tx.ExecContext(ctx, `delete from settings where key = ?`, "cpa_connection_revision_v1")
		return err
	}
	var rawRev string
	revErr := tx.QueryRowContext(ctx, `select value from settings where key = ?`, "cpa_connection_revision_v1").Scan(&rawRev)
	if revErr != nil && !errors.Is(revErr, sql.ErrNoRows) {
		return revErr
	}
	sameConn := prevMgrURL == strings.TrimSpace(baseURL) &&
		prevSetupURL == strings.TrimSpace(baseURL) &&
		security.EqualHMAC(prevMgrKey, managementKey) &&
		security.EqualHMAC(prevSetupKey, managementKey)
	if sameConn && len(strings.TrimSpace(rawRev)) == 32 {
		return nil
	}
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx,
		`insert into settings(key, value, updated_at_ms) values(?, ?, ?) on conflict(key) do update set value = excluded.value, updated_at_ms = excluded.updated_at_ms`,
		"cpa_connection_revision_v1", hex.EncodeToString(buf[:]), now)
	return err
}

func (u *upgradeCanonicalServer) writeUpgradeRebindFailure(w http.ResponseWriter, err error) {
	status, code, detail := http.StatusServiceUnavailable, "external_rebind_unavailable", ""
	switch {
	case errors.Is(err, cpa.ErrManagementClientForbidden):
		status, code, detail = http.StatusConflict, freshconfig.ReasonManagementRejected, externalRebindForbiddenDetail
	case errors.Is(err, cpa.ErrManagementRejected):
		status, code, detail = http.StatusConflict, freshconfig.ReasonManagementRejected, externalRebindRejectedDetail
	case errors.Is(err, cpa.ErrExternalVersionUnsupported):
		status, code = http.StatusConflict, freshconfig.ReasonVersionUnsupported
	case errors.Is(err, freshkeyapply.ErrRebindRefused):
		status, code = http.StatusConflict, "external_rebind_refused"
	case errors.Is(err, freshkeyapply.ErrGeneration), errors.Is(err, upgradeoperation.ErrConflict):
		status, code = http.StatusConflict, "external_rebind_stale_generation"
	case errors.Is(err, cpa.ErrExternalTargetMoved):
		status, code = http.StatusConflict, freshconfig.ReasonTargetMoved
	case errors.Is(err, freshkeyapply.ErrExternalUnavailable):
		status, code = http.StatusConflict, "external_cpa_authority_unavailable"
	}
	writeExternalRebindErrorJSON(w, status, code, detail)
}

func writeExternalRebindErrorJSON(w http.ResponseWriter, status int, code, detail string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	writeFreshJSON(w, struct {
		Code   string `json:"code"`
		Detail string `json:"detail,omitempty"`
	}{Code: code, Detail: detail})
}

// --- Console Model List & cpamp/* Compatibility Bridge ---

func (u *upgradeCanonicalServer) serveConsoleModelList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	if r.URL.RawQuery != "" || r.URL.EscapedPath() != r.URL.Path {
		response.Error(w, http.StatusBadRequest, errors.New("invalid model list request"))
		return
	}
	if u.targetMode == model.UpgradeTargetExternal {
		install, credDigest, holdCtx, release, err := u.enterExternalCredentialHold(w, r.Context(), upgradeExternalHoldBudget)
		if err != nil {
			switch {
			case errors.Is(err, errExternalCredentialWaitCancelled):
				writeExternalCredentialWaitCancelled(w)
			case errors.Is(err, cpa.ErrManagementRejected):
				writeExternalManagementRejected(w)
			case errors.Is(err, cpa.ErrExternalRejectionUnavailable):
				writeExternalAuthorityUnavailable(w)
			default:
				writeConsoleModelListUnavailable(w)
			}
			return
		}
		tr := cpa.NewExternalConfigTransport(install.BaseURL)
		doc, docErr := tr.ReadConfigDocument(holdCtx, install.ManagementKey)
		release()
		if docErr != nil {
			u.recordExternalAuthFailureIfMatching(r.Context(), install, credDigest, docErr)
			switch {
			case errors.Is(docErr, cpa.ErrManagementClientForbidden):
				writeExternalManagementForbidden(w)
			case errors.Is(docErr, cpa.ErrManagementRejected):
				writeExternalManagementRejected(w)
			default:
				writeConsoleModelListUnavailable(w)
			}
			return
		}
		clientKey := firstCPAClientKey(doc)
		if clientKey == "" {
			writeConsoleModelListEmpty(w)
			return
		}
		proxysvc.ProxyModelListWithClientKey(w, r, install.BaseURL, clientKey, response.Error, response.MethodNotAllowed)
		return
	}

	if u.workspace == nil || u.runtime == nil || u.db == nil || u.db.SQLDB() == nil {
		writeConsoleModelListUnavailable(w)
		return
	}
	if _, ev, ok, err := loadVerifiedCompletedUpgradeOperation(r.Context(), upgradeoperation.New(u.db.SQLDB())); err != nil || !ok || ev.TargetMode != model.UpgradeTargetManaged {
		writeConsoleModelListUnavailable(w)
		return
	}
	rec, secret, err := u.workspace.loadManagedDestination(r.Context())
	if err != nil {
		writeConsoleModelListUnavailable(w)
		return
	}
	if _, _, _, err := verifyUpgradeManagedLiveRuntime(r.Context(), u.runtime, rec); err != nil {
		writeConsoleModelListUnavailable(w)
		return
	}
	tr := cpa.NewTransportForEndpoint(rec.BaseURL)
	if len(u.workspace.supervisorRoots) > 0 {
		tr.SetTrustedSupervisorRoots(u.workspace.supervisorRoots)
	}
	roots := append([]string(nil), u.workspace.supervisorRoots...)
	tr.SetPostureSelector(func() (cpa.DestinationPosture, bool) {
		return cpa.PostureForWithSupervisorRoots(freshkeyapply.StrategyUpgradedManaged, roots)
	})
	doc, err := tr.ReadConfigDocument(r.Context(), secret)
	if err != nil {
		writeConsoleModelListUnavailable(w)
		return
	}
	clientKey := firstCPAClientKey(doc)
	if clientKey == "" {
		writeConsoleModelListEmpty(w)
		return
	}
	proxysvc.ProxyModelListWithClientKey(w, r, rec.BaseURL, clientKey, response.Error, response.MethodNotAllowed)
}

func (u *upgradeCanonicalServer) servePluginResource(w http.ResponseWriter, r *http.Request) {
	if u == nil || u.inner == nil || u.db == nil || u.db.SQLDB() == nil {
		http.NotFound(w, r)
		return
	}
	if _, err := u.loadCurrentOp(r.Context()); err != nil {
		writeUpgradeAuthorityUnavailable(w)
		return
	}
	if _, ev, ok, err := loadVerifiedCompletedUpgradeOperation(r.Context(), upgradeoperation.New(u.db.SQLDB())); err != nil || !ok || ev.TargetMode != u.targetMode {
		writeUpgradeAuthorityUnavailable(w)
		return
	}
	if u.targetMode == model.UpgradeTargetExternal {
		install, credDigest, holdCtx, release, err := u.enterExternalCredentialHold(w, r.Context(), upgradeExternalHoldBudget)
		if err != nil {
			u.writeExternalBridgePrecheckFailure(w, err)
			return
		}
		defer release()
		overrideCtx := proxysvc.WithSavedSetupOverride(holdCtx, install.BaseURL, install.ManagementKey)
		markedCtx, mark := proxysvc.WithSavedKeyMark(overrideCtx)
		rec := newUpgradeExternalLegacyRecorder(w, mark)
		u.inner.ServeHTTP(rec, r.WithContext(markedCtx))
		rec.finish(r.Context(), u, install, credDigest)
		return
	}
	if u.workspace == nil || u.runtime == nil {
		writeUpgradeAuthorityUnavailable(w)
		return
	}
	rec, secret, err := u.workspace.loadManagedDestination(r.Context())
	if err != nil {
		writeUpgradeAuthorityUnavailable(w)
		return
	}
	if _, _, _, err := verifyUpgradeManagedLiveRuntime(r.Context(), u.runtime, rec); err != nil {
		writeUpgradeAuthorityUnavailable(w)
		return
	}
	overrideCtx := proxysvc.WithSavedSetupOverride(r.Context(), rec.BaseURL, secret)
	u.inner.ServeHTTP(w, r.WithContext(overrideCtx))
}

func (u *upgradeCanonicalServer) serveBridgedLegacyResource(w http.ResponseWriter, r *http.Request, resource string) {
	if u.inner == nil || r == nil || r.URL == nil {
		http.NotFound(w, r)
		return
	}
	if r.URL.EscapedPath() != r.URL.Path {
		http.NotFound(w, r)
		return
	}
	sub := strings.TrimPrefix(resource, "cpamp/")
	if sub == "" ||
		sub == "setup" || strings.HasPrefix(sub, "setup/") ||
		sub == "v8/management/config/api-keys" || strings.HasPrefix(sub, "v8/management/config/api-keys/") {
		http.NotFound(w, r)
		return
	}
	if consoleModelListResource(sub, r.Method) {
		u.serveConsoleModelList(w, r)
		return
	}

	var parsed *url.URL
	switch {
	case sub == "dashboard" && r.Method == http.MethodGet:
		today := time.Now()
		start := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, today.Second()*0, 0, today.Location()).UnixMilli()
		var parseErr error
		parsed, parseErr = url.Parse("/v0/management/dashboard/summary?today_start_ms=" + strconv.FormatInt(start, 10))
		if parseErr != nil {
			http.Error(w, "resource unavailable", http.StatusServiceUnavailable)
			return
		}
	case sub == "config" && (r.Method == http.MethodGet || r.Method == http.MethodPut || r.Method == http.MethodPatch):
		parsed = &url.URL{Path: "/usage-service/config", RawQuery: r.URL.RawQuery}
	case sub == "status" && r.Method == http.MethodGet:
		parsed = &url.URL{Path: "/status", RawQuery: r.URL.RawQuery}
	case sub == "usage" && r.Method == http.MethodGet:
		parsed = &url.URL{Path: "/v0/management/usage", RawQuery: r.URL.RawQuery}
	case sub == "model-prices" && (r.Method == http.MethodGet || r.Method == http.MethodPut):
		parsed = &url.URL{Path: "/v0/management/model-prices", RawQuery: r.URL.RawQuery}
	case sub == "monitoring" && r.Method == http.MethodPost:
		parsed = &url.URL{Path: "/v0/management/monitoring/analytics", RawQuery: r.URL.RawQuery}
	case legacyBridgePathAllowed(sub, r.Method):
		var parseErr error
		parsed, parseErr = url.ParseRequestURI("/" + sub)
		if parseErr != nil || parsed.Path == "" || parsed.Path == "/" {
			http.NotFound(w, r)
			return
		}
		parsed.RawQuery = r.URL.RawQuery
	default:
		http.NotFound(w, r)
		return
	}

	clone := r.Clone(r.Context())
	clone.URL = parsed
	clone.RequestURI = parsed.RequestURI()

	if u.targetMode == model.UpgradeTargetExternal {
		if externalLegacyReplacedWrite(clone.URL.Path, r.Method) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			writeFreshJSON(w, struct {
				Code string `json:"code"`
			}{Code: "external_legacy_write_replaced"})
			return
		}
		if externalLegacyLogConfigWrite(clone.URL.Path, r.Method) {
			install, credDigest, holdCtx, release, err := u.enterExternalCredentialHold(w, r.Context(), upgradeExternalHoldBudget)
			if err != nil {
				u.writeExternalBridgePrecheckFailure(w, err)
				return
			}
			report, verErr := cpa.ObserveExternalVersion(holdCtx, install.BaseURL, install.ManagementKey)
			release()
			if verErr != nil {
				u.recordExternalAuthFailureIfMatching(r.Context(), install, credDigest, verErr)
				u.writeExternalBridgePrecheckFailure(w, verErr)
				return
			}
			if report.WriteBlocked {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusConflict)
				writeFreshJSON(w, struct {
					Code string `json:"code"`
				}{Code: freshconfig.ReasonVersionUnsupported})
				return
			}
		}
	}

	reconcileCollectorAfter := clone.URL.Path == "/usage-service/config" &&
		(r.Method == http.MethodPut || r.Method == http.MethodPatch)

	if u.targetMode == model.UpgradeTargetExternal {
		if legacyBridgePresentsSavedKey(clone) {
			install, credDigest, holdCtx, release, err := u.enterExternalCredentialHold(w, clone.Context(), upgradeExternalHoldBudget)
			if err != nil {
				u.writeExternalBridgePrecheckFailure(w, err)
				return
			}
			defer release()
			overrideCtx := proxysvc.WithSavedSetupOverride(holdCtx, install.BaseURL, install.ManagementKey)
			markedCtx, mark := proxysvc.WithSavedKeyMark(overrideCtx)
			rec := newUpgradeExternalLegacyRecorder(w, mark)
			u.inner.ServeHTTP(rec, clone.WithContext(markedCtx))
			rec.finish(r.Context(), u, install, credDigest)
			if reconcileCollectorAfter && rec.statusCode() >= 200 && rec.statusCode() < 300 {
				u.reconcileCollectorBestEffort(r.Context())
			}
			return
		}
		if legacyBridgeServicePresentsSavedKey(clone) {
			install, _, err := u.precheckExternalCredential(clone.Context())
			if err != nil {
				u.writeExternalBridgePrecheckFailure(w, err)
				return
			}
			gatedCtx := u.workspace.withExternalGate(clone.Context())
			clone = clone.WithContext(proxysvc.WithSavedSetupOverride(gatedCtx, install.BaseURL, install.ManagementKey))
		}
	} else if u.targetMode == model.UpgradeTargetManaged && legacyBridgePresentsSavedKey(clone) {
		if u.workspace == nil || u.runtime == nil {
			writeUpgradeAuthorityUnavailable(w)
			return
		}
		rec, secret, err := u.workspace.loadManagedDestination(clone.Context())
		if err != nil {
			writeUpgradeAuthorityUnavailable(w)
			return
		}
		if _, _, _, err := verifyUpgradeManagedLiveRuntime(clone.Context(), u.runtime, rec); err != nil {
			writeUpgradeAuthorityUnavailable(w)
			return
		}
		clone = clone.WithContext(proxysvc.WithSavedSetupOverride(clone.Context(), rec.BaseURL, secret))
	}

	if reconcileCollectorAfter {
		rec := &statusCaptureResponseWriter{ResponseWriter: w, code: http.StatusOK}
		u.inner.ServeHTTP(rec, clone)
		if rec.code >= 200 && rec.code < 300 {
			u.reconcileCollectorBestEffort(r.Context())
		}
		return
	}

	u.inner.ServeHTTP(w, clone)
}

func (u *upgradeCanonicalServer) writeExternalBridgePrecheckFailure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errExternalCredentialWaitCancelled):
		writeExternalCredentialWaitCancelled(w)
	case errors.Is(err, cpa.ErrManagementClientForbidden):
		writeExternalManagementForbidden(w)
	case errors.Is(err, cpa.ErrManagementRejected):
		writeExternalManagementRejected(w)
	case errors.Is(err, cpa.ErrExternalTargetMoved):
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		writeFreshJSON(w, struct {
			Code string `json:"code"`
		}{Code: freshconfig.ReasonTargetMoved})
	default:
		writeExternalAuthorityUnavailable(w)
	}
}

type statusCaptureResponseWriter struct {
	http.ResponseWriter
	code        int
	wroteHeader bool
}

func (w *statusCaptureResponseWriter) WriteHeader(statusCode int) {
	if !w.wroteHeader {
		w.code = statusCode
		w.wroteHeader = true
	}
	w.ResponseWriter.WriteHeader(statusCode)
}

func (w *statusCaptureResponseWriter) Write(p []byte) (int, error) {
	if !w.wroteHeader {
		w.wroteHeader = true
	}
	return w.ResponseWriter.Write(p)
}

type upgradeExternalLegacyRecorder struct {
	dst          http.ResponseWriter
	header       http.Header
	body         bytes.Buffer
	status       int
	wroteHeader  bool
	intercepting bool
	streaming    bool
	mark         *proxysvc.SavedKeyMark
}

func newUpgradeExternalLegacyRecorder(dst http.ResponseWriter, mark *proxysvc.SavedKeyMark) *upgradeExternalLegacyRecorder {
	return &upgradeExternalLegacyRecorder{
		dst:    dst,
		header: make(http.Header),
		status: http.StatusOK,
		mark:   mark,
	}
}

func (r *upgradeExternalLegacyRecorder) Header() http.Header {
	if r.streaming {
		return r.dst.Header()
	}
	return r.header
}

func (r *upgradeExternalLegacyRecorder) WriteHeader(code int) {
	if r.wroteHeader {
		return
	}
	r.wroteHeader = true
	r.status = code
	if r.mark != nil && r.mark.Attached() && (code == http.StatusUnauthorized || code == http.StatusForbidden) {
		r.intercepting = true
		return
	}
	r.streaming = true
	copyUpgradeHeaders(r.dst.Header(), r.header)
	r.dst.WriteHeader(code)
}

func (r *upgradeExternalLegacyRecorder) Write(p []byte) (int, error) {
	if !r.wroteHeader {
		r.WriteHeader(http.StatusOK)
	}
	if r.intercepting {
		if r.body.Len() < 8<<10 {
			_, _ = r.body.Write(p)
		}
		return len(p), nil
	}
	return r.dst.Write(p)
}

func (r *upgradeExternalLegacyRecorder) statusCode() int {
	return r.status
}

func (r *upgradeExternalLegacyRecorder) finish(
	ctx context.Context,
	owner *upgradeCanonicalServer,
	install freshkeyapply.ExternalInstall,
	credDigest string,
) {
	if !r.wroteHeader {
		copyUpgradeHeaders(r.dst.Header(), r.header)
		r.dst.WriteHeader(http.StatusOK)
		return
	}
	if !r.intercepting {
		return
	}
	if r.status == http.StatusUnauthorized {
		owner.recordExternalAuthFailureIfMatching(ctx, install, credDigest, cpa.ErrManagementRejected)
		writeExternalManagementRejected(r.dst)
		return
	}
	owner.recordExternalAuthFailureIfMatching(ctx, install, credDigest, cpa.ErrManagementClientForbidden)
	writeExternalManagementForbidden(r.dst)
}

func copyUpgradeHeaders(dst, src http.Header) {
	for k, vals := range src {
		dst.Del(k)
		for _, v := range vals {
			dst.Add(k, v)
		}
	}
}

// --- Collector Reconciliation Bound Strictly to Verified Target ---

func (u *upgradeCanonicalServer) enableAndReconcileExternalCollector(
	ctx context.Context,
	install freshkeyapply.ExternalInstall,
) error {
	if u == nil || u.db == nil {
		return errors.New("upgrade store unavailable")
	}
	mgrCfg, ok, err := u.db.LoadManagerConfig(ctx)
	if err != nil {
		return err
	}
	if !ok {
		mgrCfg = model.ManagerConfig{}
	}
	enabled := true
	mgrCfg.CPAConnection.CPABaseURL = install.BaseURL
	mgrCfg.CPAConnection.ManagementKey = install.ManagementKey
	mgrCfg.Collector.Enabled = &enabled
	if err := u.db.SaveManagerConfig(ctx, mgrCfg); err != nil {
		return err
	}
	if err := u.alignStoredExternalConnection(ctx, install.BaseURL, install.ManagementKey); err != nil {
		return err
	}
	return u.reconcileCollector(ctx)
}

func (u *upgradeCanonicalServer) reconcileCollectorBestEffort(ctx context.Context) {
	if err := u.reconcileCollector(ctx); err != nil {
		log.Printf("[upgrade-canonical] collector reconciliation skipped: %v", err)
	}
}

func (u *upgradeCanonicalServer) reconcileCollector(ctx context.Context) error {
	if u == nil || u.appCtx == nil || u.appCtx.Collector == nil || u.db == nil {
		return nil
	}
	mgrCfg, mgrOK, err := u.db.LoadManagerConfig(ctx)
	if err != nil {
		return err
	}
	if mgrOK && !collectorsvc.ManagerCollectorEnabled(mgrCfg) {
		u.appCtx.Collector.Stop()
		return nil
	}
	if !mgrOK {
		mgrCfg = model.ManagerConfig{}
	}

	var (
		externalInstall    freshkeyapply.ExternalInstall
		externalCredDigest string
	)
	switch u.targetMode {
	case model.UpgradeTargetExternal:
		install, credDigest, err := u.precheckExternalCredential(ctx)
		if err != nil {
			u.appCtx.Collector.Stop()
			return err
		}
		if err := u.alignStoredExternalConnection(ctx, install.BaseURL, install.ManagementKey); err != nil {
			return err
		}
		mgrCfg.CPAConnection.CPABaseURL = install.BaseURL
		mgrCfg.CPAConnection.ManagementKey = install.ManagementKey
		externalInstall = install
		externalCredDigest = credDigest
	case model.UpgradeTargetManaged:
		if u.workspace == nil || u.runtime == nil {
			u.appCtx.Collector.Stop()
			return nil
		}
		rec, secret, err := u.workspace.loadManagedDestination(ctx)
		if err != nil {
			u.appCtx.Collector.Stop()
			return err
		}
		if _, _, _, err := verifyUpgradeManagedLiveRuntime(ctx, u.runtime, rec); err != nil {
			u.appCtx.Collector.Stop()
			return nil
		}
		if mgrOK && (mgrCfg.CPAConnection.CPABaseURL != rec.BaseURL || !security.EqualHMAC(mgrCfg.CPAConnection.ManagementKey, secret)) {
			mgrCfg.CPAConnection.CPABaseURL = rec.BaseURL
			mgrCfg.CPAConnection.ManagementKey = secret
			if err := u.db.SaveManagerConfig(ctx, mgrCfg); err != nil {
				return err
			}
		} else {
			mgrCfg.CPAConnection.CPABaseURL = rec.BaseURL
			mgrCfg.CPAConnection.ManagementKey = secret
		}
	default:
		u.appCtx.Collector.Stop()
		return nil
	}

	collectorCtx := u.hostCtx
	if collectorCtx == nil {
		collectorCtx = context.Background()
	}
	rtCfg := collectorsvc.RuntimeConfigFromManagerConfigWithFallback(mgrCfg, u.appCtx.Config)
	if u.targetMode == model.UpgradeTargetExternal && u.workspace != nil && u.workspace.rejection != nil {
		u.workspace.rejection.noteCollectorStart(externalInstall.Identity, externalCredDigest, externalInstall.BaseURL, externalInstall.ManagementKey)
	}
	u.appCtx.Collector.Start(collectorCtx, rtCfg)
	return nil
}
