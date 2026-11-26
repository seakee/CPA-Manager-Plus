package main

import (
	"bufio"
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
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"gopkg.in/yaml.v3"

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
	cpaupdateservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpaupdate"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshconfig"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeyapply"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeys"
	proxysvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/proxy"
	runtimeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/runtime"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/updatecheck"
	upgradeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/upgrade"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/worker"
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
	CollectorWorker *worker.CollectorWorker
	DesiredStateOut *runtimeservice.DesiredStateStore
	Startup         *startupHTTPHandler
	CanonicalOut    **upgradeCanonicalServer
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
	var runtimeClient runtimeservice.RuntimeClient
	var artifactClient *runtimeservice.ArtifactClient
	if u.targetMode == model.UpgradeTargetManaged {
		runtimeClient = deps.Runtime
		if cfg.EmbeddedRuntimeConfigured() {
			tokenSource := runtimeservice.NewFileRuntimeTokenSource(cfg.RuntimeTokenFile)
			if runtimeClient == nil {
				runtimeClient = runtimeservice.NewEmbeddedClientWithTokenSource(cfg.RuntimeURL, tokenSource)
			}
			artifactClient = runtimeservice.NewArtifactClientWithTokenSource(cfg.RuntimeURL, tokenSource)
		}
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
		u.appCtx.ManagerConfigService.UseExternalCollectorLifecycle()
		u.appCtx.ManagerConfigService.SetCPAUsageProbeAllowed(func(context.Context) bool {
			return u.targetMode != model.UpgradeTargetExternal
		})
	}
	u.runtimeExternal = runtimeservice.NewExternalClientWithConnectionSource(func(callCtx context.Context) (string, string, error) {
		if install, ok := upgradeExternalHoldInstall(callCtx); ok {
			return install.BaseURL, install.ManagementKey, nil
		}
		install, _, loadErr := u.loadFencedExternalInstall(callCtx)
		if loadErr != nil {
			return "", "", loadErr
		}
		return install.BaseURL, install.ManagementKey, nil
	})
	if u.appCtx != nil && u.db != nil {
		u.appCtx.CPAUpdateService = cpaupdateservice.New(
			upgradeCPAUpdateStore{owner: u},
			upgradeRuntimeClientAdapter{owner: u},
			u.runtimeModeFor(u.targetMode),
		).WithArtifactClient(artifactClient).WithModeResolver(func() model.RuntimeMode {
			return u.effectiveRuntimeMode(context.Background())
		}).WithProductCompleteProbe(func(ctx context.Context) (bool, error) {
			op, err := u.loadCurrentOp(ctx)
			return op.Stage == model.UpgradeStageComplete, err
		})
	}
	u.configService = &upgradeConfigService{owner: u}
	u.pluginCapability = newPluginCapabilityProbeWithContext(ctx, pluginCapabilityTTL, pluginCapabilityProbeTimeout, u.probePluginCapable)
	if ctx != nil && ctx.Done() != nil {
		u.bgWG.Add(1)
		go func() {
			defer u.bgWG.Done()
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
	u.bindCollectorLifecycleLocker(deps.CollectorWorker)
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

type upgradeExternalHoldState struct {
	install freshkeyapply.ExternalInstall
	hasSnap bool
}

func withUpgradeExternalHoldInstall(ctx context.Context, install freshkeyapply.ExternalInstall) context.Context {
	if ctx == nil {
		return ctx
	}
	return context.WithValue(ctx, upgradeExternalHoldContextKey{}, upgradeExternalHoldState{
		install: install,
		hasSnap: true,
	})
}

func hasUpgradeExternalHold(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	_, held := ctx.Value(upgradeExternalHoldContextKey{}).(upgradeExternalHoldState)
	return held
}

func upgradeExternalHoldInstall(ctx context.Context) (freshkeyapply.ExternalInstall, bool) {
	if ctx == nil {
		return freshkeyapply.ExternalInstall{}, false
	}
	state, ok := ctx.Value(upgradeExternalHoldContextKey{}).(upgradeExternalHoldState)
	if !ok || !state.hasSnap {
		return freshkeyapply.ExternalInstall{}, false
	}
	return state.install, true
}

type upgradeExternalPrecheckErrKey struct{}

type upgradeExternalPrecheckErrState struct {
	err error
}

func withUpgradeExternalPrecheckErr(ctx context.Context, err error) context.Context {
	if ctx == nil || err == nil {
		return ctx
	}
	return context.WithValue(ctx, upgradeExternalPrecheckErrKey{}, upgradeExternalPrecheckErrState{err: err})
}

func upgradeExternalPrecheckErr(ctx context.Context) (error, bool) {
	if ctx == nil {
		return nil, false
	}
	state, ok := ctx.Value(upgradeExternalPrecheckErrKey{}).(upgradeExternalPrecheckErrState)
	if !ok || state.err == nil {
		return nil, false
	}
	return state.err, true
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
	holdCtx := withUpgradeExternalHoldInstall(ctx, install)
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

func (u *upgradeCanonicalServer) enterExternalBridgeCredentialHold(
	ctx context.Context,
	path string,
) (freshkeyapply.ExternalInstall, string, *externalCredentialHold, context.Context, error) {
	if u == nil || u.workspace == nil {
		return freshkeyapply.ExternalInstall{}, "", nil, nil, freshkeyapply.ErrExternalUnavailable
	}
	if !u.workspace.credentialGate.acquire(ctx) {
		return freshkeyapply.ExternalInstall{}, "", nil, nil, errExternalCredentialWaitCancelled
	}
	install, credDigest, err := u.precheckExternalCredential(ctx)
	if err != nil {
		u.workspace.credentialGate.release()
		return freshkeyapply.ExternalInstall{}, "", nil, nil, err
	}
	budget := upgradeExternalHoldBudget
	if externalLegacyWaitsOnProvider(path) {
		budget = 0
	}
	hold, holdCtx := beginExternalCredentialHold(withUpgradeExternalHoldInstall(ctx, install), &u.workspace.credentialGate, budget)
	return install, credDigest, hold, holdCtx, nil
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

func (u *upgradeCanonicalServer) currentPluginCapabilityEpoch() uint64 {
	if u == nil || u.pluginCapability == nil {
		return 0
	}
	u.pluginCapability.mu.Lock()
	epoch := u.pluginCapability.epoch
	u.pluginCapability.mu.Unlock()
	return epoch
}

func (u *upgradeCanonicalServer) notePluginDocumentObserved(epoch uint64, hasPluginDir bool) {
	if u == nil || u.pluginCapability == nil {
		return
	}
	u.pluginCapability.mu.Lock()
	if !u.pluginCapability.closed && u.pluginCapability.epoch == epoch {
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
	if sc.ruleFamilies && (id == cpa.ConfigRulesGroupID || id == freshconfig.ConfigSourceGroupID) {
		return true
	}
	_, ok := sc.lookupGroup(id)
	return ok
}

var (
	upgradeGatewayScope = upgradeConfigScope{
		name:         "gateway",
		ruleFamilies: true,
		groupIDs:     cpa.GatewayPageGroupIDs,
		lookupGroup:  cpa.LookupGatewayPageGroup,
		leafPaths:    cpa.GatewayPageLeafPaths,
		lookupLeaf:   cpa.LookupGatewayPageLeaf,
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
	capable     freshconfig.Capability
	mode        string
	sourceID    string
	generation  string
	endpoint    string
	secret      string
	readOnly    string
	external    freshkeyapply.ExternalInstall
	credDigest  string
	transport   upgradeConfigWire
	liveAuthErr error
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
		if preErr, ok := upgradeExternalPrecheckErr(ctx); ok {
			if errors.Is(preErr, cpa.ErrExternalTargetMoved) {
				return upgradeConfigAuthority{
					capable: freshconfig.Capability{Mode: freshconfig.ModeUnavailable, Reason: freshconfig.ReasonTargetMoved},
					mode:    freshconfig.ModeUnavailable,
				}, cpa.ErrExternalTargetMoved
			}
			return upgradeConfigAuthority{
				capable: freshconfig.Capability{Mode: freshconfig.ModeUnavailable, Reason: freshconfig.ReasonUnavailable},
				mode:    freshconfig.ModeUnavailable,
			}, nil
		}
		install, credDigest, err := u.precheckExternalCredential(ctx)
		switch {
		case errors.Is(err, cpa.ErrManagementRejected):
			return upgradeConfigAuthority{
				capable:     freshconfig.Capability{Mode: freshconfig.ModeUnavailable, Reason: freshconfig.ReasonManagementRejected},
				mode:        freshconfig.ModeUnavailable,
				liveAuthErr: cpa.ErrManagementRejected,
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
					capable:     freshconfig.Capability{Mode: freshconfig.ModeUnavailable, Reason: freshconfig.ReasonManagementRejected},
					mode:        freshconfig.ModeUnavailable,
					liveAuthErr: verErr,
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
	if auth.mode == freshconfig.ModeManaged {
		if auth.transport == nil {
			return freshconfig.Capability{
				Mode:          freshconfig.ModeManaged,
				Readable:      false,
				Writable:      false,
				RulesWritable: false,
				Reason:        freshconfig.ReasonUnavailable,
			}, nil
		}
		if _, readErr := auth.transport.ReadConfigDocument(ctx, auth.secret); readErr != nil {
			return freshconfig.Capability{
				Mode:          freshconfig.ModeManaged,
				Readable:      false,
				Writable:      false,
				RulesWritable: false,
				Reason:        freshconfig.ReasonReadUnavailable,
			}, nil
		}
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
	s.mu.Lock()
	defer s.mu.Unlock()
	journal, journalInvalid, err := s.loadJournal(ctx)
	if err != nil {
		return freshconfig.View{}, err
	}
	if journalInvalid {
		journal = nil
	}
	auth, resolveErr := s.resolveAuthority(ctx, sc, false)
	if errors.Is(resolveErr, cpa.ErrExternalRejectionUnavailable) || errors.Is(resolveErr, cpa.ErrManagementRejected) || auth.liveAuthErr != nil {
		return freshconfig.View{}, resolveErr
	}
	view := freshconfig.View{
		Capability:    auth.capable,
		Generation:    auth.generation,
		GroupsInvalid: journalInvalid,
		Groups:        []freshconfig.GroupView{},
		Fields:        []freshconfig.FieldView{},
		Families:      []freshconfig.FamilyView{},
	}
	var document map[string]any
	if auth.capable.Readable && auth.transport != nil {
		observedEpoch := s.owner.currentPluginCapabilityEpoch()
		doc, docErr := auth.transport.ReadConfigDocument(ctx, auth.secret)
		if docErr != nil {
			if auth.mode == freshconfig.ModeExternal {
				s.owner.recordExternalAuthFailureIfMatching(ctx, auth.external, auth.credDigest, docErr)
				if errors.Is(docErr, cpa.ErrManagementRejected) ||
					errors.Is(docErr, cpa.ErrManagementClientForbidden) ||
					errors.Is(docErr, cpa.ErrExternalTargetMoved) ||
					errors.Is(docErr, cpa.ErrExternalRejectionUnavailable) {
					return freshconfig.View{}, docErr
				}
			}
			view.Capability = freshconfig.Capability{
				Mode:   auth.mode,
				Reason: freshconfig.ReasonReadUnavailable,
			}
			auth.capable = view.Capability
		} else {
			document = doc
			plugins, hasPlugins := document["plugins"].(map[string]any)
			hasDir := false
			if hasPlugins {
				_, hasDir = plugins["dir"]
			}
			s.owner.notePluginDocumentObserved(observedEpoch, hasDir)
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
	var sourceUnknownOp *freshstore.ConfigGroupOperation
	if sc.ownsGroup(freshconfig.ConfigSourceGroupID) && !journalInvalid {
		if op, ok := upgradeUnknownGroupOperation(journal, freshconfig.ConfigSourceGroupID); ok {
			sourceUnknownOp = &op
			for _, id := range sc.groupIDs() {
				if blocked[id] == "" {
					blocked[id] = freshconfig.ReasonUnknownOriginal
				}
			}
			if sc.ruleFamilies && blocked[cpa.ConfigRulesGroupID] == "" {
				blocked[cpa.ConfigRulesGroupID] = freshconfig.ReasonUnknownOriginal
			}
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
		if sourceUnknownOp != nil && (entry.Operation == nil || entry.Operation.State != "unknown") {
			copied := sourceUnknownOp.Clone()
			entry.Operation = &copied
			entry.Persisted = copied.State
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
		if sourceUnknownOp != nil && (rulesOp == nil || rulesOp.State != "unknown") {
			copied := sourceUnknownOp.Clone()
			rulesOp = &copied
		}
		for _, id := range cpa.GatewayPageRuleFamilyIDs() {
			family, _ := cpa.LookupGatewayPageRuleFamily(id)
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

	if sc.managedOnly {
		if err := freshconfig.ValidatePluginGroupRequest(groupID, req); err != nil {
			return freshstore.ConfigGroupOperation{}, err
		}
	} else {
		if err := freshconfig.ValidateGroupRequest(groupID, req); err != nil {
			return freshstore.ConfigGroupOperation{}, err
		}
	}

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
	if sc.ownsGroup(freshconfig.ConfigSourceGroupID) {
		if _, ok := upgradeUnknownGroupOperation(journal, freshconfig.ConfigSourceGroupID); ok {
			return freshstore.ConfigGroupOperation{}, cpa.ErrApplyUnknown
		}
	}
	type kvLeafPut struct {
		path    string
		payload []byte
	}
	var kvPuts []kvLeafPut
	for _, edit := range edits {
		if edit.Action != "set" {
			continue
		}
		leaf, ok := sc.lookupLeaf(edit.Path)
		if !ok || leaf.Type != cpa.ConfigLeafKVMap {
			continue
		}
		encoded, encErr := cpa.EncodeConfigLeafValue(edit.Path, edit.Value)
		if encErr != nil {
			return freshstore.ConfigGroupOperation{}, freshconfig.ErrInvalid
		}
		kvPuts = append(kvPuts, kvLeafPut{path: edit.Path, payload: encoded})
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

	payload, patched := upgradeGroupPatch(group.Prefix, edits, sc.lookupLeaf)
	applied := false
	if patched {
		if group.Prefix == "" {
			for _, edit := range edits {
				if edit.Action != "set" {
					continue
				}
				if sc.lookupLeaf != nil {
					if leaf, ok := sc.lookupLeaf(edit.Path); ok && leaf.Type == cpa.ConfigLeafKVMap {
						continue
					}
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
	for _, put := range kvPuts {
		if err := auth.transport.PutConfigValue(ctx, auth.secret, put.path, put.payload); err != nil {
			return s.failOperation(ctx, group.ID, op, err, auth, applied)
		}
		applied = true
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

	if err := freshconfig.ValidateFamilyRequest(familyID, req); err != nil {
		return freshstore.ConfigGroupOperation{}, err
	}
	family, ok := cpa.LookupGatewayPageRuleFamily(familyID)
	if !ok {
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
	if _, ok := upgradeUnknownGroupOperation(journal, freshconfig.ConfigSourceGroupID); ok {
		return freshstore.ConfigGroupOperation{}, cpa.ErrApplyUnknown
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

func upgradeGroupPatch(prefix string, edits []freshstore.ConfigGroupEdit, lookupLeaf func(string) (cpa.ConfigLeaf, bool)) ([]byte, bool) {
	if prefix == "" {
		for _, edit := range edits {
			if edit.Action != "set" {
				continue
			}
			if lookupLeaf != nil {
				if leaf, ok := lookupLeaf(edit.Path); ok && leaf.Type == cpa.ConfigLeafKVMap {
					continue
				}
			}
			return []byte("{}"), true
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
		if lookupLeaf != nil {
			if leaf, ok := lookupLeaf(edit.Path); ok && leaf.Type == cpa.ConfigLeafKVMap {
				continue
			}
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

func sameUpgradeSourceRequest(operation freshstore.ConfigGroupOperation, targetPrefix, baseline string) bool {
	return operation.Target == "group" &&
		operation.Prefix == targetPrefix &&
		operation.Baseline == baseline
}

func upgradeUnknownGroupOperation(journal map[string]*freshstore.ConfigGroupJournal, groupID string) (freshstore.ConfigGroupOperation, bool) {
	entry := journal[groupID]
	if entry == nil {
		return freshstore.ConfigGroupOperation{}, false
	}
	op, ok := entry.Operations[entry.Latest]
	if !ok || op.State != "unknown" {
		return freshstore.ConfigGroupOperation{}, false
	}
	return op.Clone(), true
}

func upgradeBlockingSourceOperation(journal map[string]*freshstore.ConfigGroupJournal) (*freshstore.ConfigGroupOperation, string, error) {
	if op, ok := upgradeUnknownGroupOperation(journal, freshconfig.ConfigSourceGroupID); ok {
		return &op, freshconfig.ReasonUnknownOriginal, cpa.ErrApplyUnknown
	}
	for _, groupID := range cpa.GatewayPageGroupIDs() {
		if op, ok := upgradeUnknownGroupOperation(journal, groupID); ok {
			return &op, freshconfig.ReasonUnknownOriginal, cpa.ErrApplyUnknown
		}
	}
	if op, ok := upgradeUnknownGroupOperation(journal, cpa.ConfigRulesGroupID); ok {
		return &op, freshconfig.ReasonUnknownOriginal, cpa.ErrApplyUnknown
	}
	for _, groupID := range upgradeSortedMapKeys(journal) {
		if !upgradeGatewayScope.ownsGroup(groupID) {
			continue
		}
		if op, ok := upgradeUnknownGroupOperation(journal, groupID); ok {
			return &op, freshconfig.ReasonUnknownOriginal, cpa.ErrApplyUnknown
		}
	}
	return nil, "", nil
}

var upgradeProtectedSourceRoots = map[string]bool{
	"config-version":           true,
	"management":               true,
	"remote-management":        true,
	"access":                   true,
	"api-keys":                 true,
	"plugins":                  true,
	"observability":            true,
	"models":                   true,
	"credentials":              true,
	"credential-concurrency":   true,
	"credential-in-flight":     true,
	"pprof":                    true,
	"usage-statistics-enabled": true,
	"logging":                  true,
	"auth-dir":                 true,
	"kv-store":                 true,
	"gemini-api-key":           true,
	"interactions-api-key":     true,
	"vertex-api-key":           true,
	"codex-api-key":            true,
	"claude-api-key":           true,
	"xai-api-key":              true,
	"meta-api-key":             true,
	"openai-compatibility":     true,
}

var upgradeProtectedSourceSubtrees = map[string]map[string]bool{
	"server": {
		"host":         true,
		"port":         true,
		"discovery":    true,
		"tls":          true,
		"unix-socket":  true,
		"github-token": true,
		"kv-store":     true,
	},
	"oauth": {
		"auth-dir": true,
	},
}

var upgradeSensitiveSourceKeyNames = map[string]bool{
	"secret-key":     true,
	"secret":         true,
	"api-key":        true,
	"api-keys":       true,
	"management-key": true,
	"github-token":   true,
	"password":       true,
	"private-key":    true,
	"access-token":   true,
	"refresh-token":  true,
	"client-secret":  true,
}

func (s *upgradeConfigService) ReadSource(ctx context.Context) (freshconfig.SourceView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	journal, journalInvalid, err := s.loadJournal(ctx)
	if err != nil {
		return freshconfig.SourceView{}, err
	}
	auth, resolveErr := s.resolveAuthority(ctx, upgradeGatewayScope, false)
	if errors.Is(resolveErr, cpa.ErrExternalRejectionUnavailable) || errors.Is(resolveErr, cpa.ErrManagementRejected) || auth.liveAuthErr != nil {
		return freshconfig.SourceView{}, resolveErr
	}
	view := freshconfig.SourceView{
		Capability:    auth.capable,
		Generation:    auth.generation,
		GroupsInvalid: journalInvalid,
	}
	var document map[string]any
	if auth.capable.Readable && auth.transport != nil {
		observedEpoch := s.owner.currentPluginCapabilityEpoch()
		doc, docErr := auth.transport.ReadConfigDocument(ctx, auth.secret)
		if docErr != nil {
			if auth.mode == freshconfig.ModeExternal {
				s.owner.recordExternalAuthFailureIfMatching(ctx, auth.external, auth.credDigest, docErr)
				if errors.Is(docErr, cpa.ErrManagementRejected) ||
					errors.Is(docErr, cpa.ErrManagementClientForbidden) ||
					errors.Is(docErr, cpa.ErrExternalTargetMoved) ||
					errors.Is(docErr, cpa.ErrExternalRejectionUnavailable) {
					return freshconfig.SourceView{}, docErr
				}
			}
			view.Capability.Readable = false
			view.Capability.Writable = false
			view.Capability.RulesWritable = false
			view.Capability.Reason = freshconfig.ReasonReadUnavailable
			document = nil
		} else {
			document = doc
			plugins, hasPlugins := document["plugins"].(map[string]any)
			hasDir := false
			if hasPlugins {
				_, hasDir = plugins["dir"]
			}
			s.owner.notePluginDocumentObserved(observedEpoch, hasDir)
		}
	}

	var blockedReason string
	if !view.GroupsInvalid {
		if entry := journal[freshconfig.ConfigSourceGroupID]; entry != nil {
			if op, ok := entry.Operations[entry.Latest]; ok {
				copied := op.Clone()
				view.Operation = &copied
			}
		}
		if blockingOp, reason, _ := upgradeBlockingSourceOperation(journal); reason != "" {
			blockedReason = reason
			if view.Operation == nil || view.Operation.State != "unknown" {
				view.Operation = blockingOp
			}
		}
	}

	if document != nil {
		sanitized := upgradeSanitizeSourceDocument(document)
		view.Baseline = upgradeCanonicalDigest(sanitized)
		encoded, encErr := upgradeEncodeSourceYAML(sanitized)
		if encErr != nil {
			return freshconfig.SourceView{}, encErr
		}
		view.YAML = encoded
	}

	switch {
	case !view.Capability.Readable:
		view.SourceWritable = false
		if view.Capability.Reason != "" {
			view.Reason = view.Capability.Reason
		} else {
			view.Reason = freshconfig.ReasonUnavailable
		}
	case view.GroupsInvalid:
		view.SourceWritable = false
		view.Reason = freshconfig.ReasonJournalInvalid
	case blockedReason != "":
		view.SourceWritable = false
		view.Reason = blockedReason
	case !view.Capability.Writable:
		view.SourceWritable = false
		if view.Capability.Reason != "" {
			view.Reason = view.Capability.Reason
		} else {
			view.Reason = freshconfig.ReasonUnavailable
		}
	default:
		view.SourceWritable = true
	}
	return view, nil
}

func (s *upgradeConfigService) SaveSource(ctx context.Context, req freshconfig.SourceRequest) (freshstore.ConfigGroupOperation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := freshconfig.ValidateSourceRequest(req); err != nil {
		return freshstore.ConfigGroupOperation{}, err
	}
	targetDoc, err := upgradeParseStrictSourceYAML(req.YAML)
	if err != nil {
		return freshstore.ConfigGroupOperation{}, err
	}

	auth, err := s.resolveAuthority(ctx, upgradeGatewayScope, true)
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

	targetPrefix := freshconfig.ConfigSourceGroupID + ":" + upgradeCanonicalDigest(targetDoc)
	for id, entry := range journal {
		if entry == nil {
			continue
		}
		if existing, found := entry.Operations[req.RequestID]; found {
			cloned := existing.Clone()
			if id != freshconfig.ConfigSourceGroupID || !sameUpgradeSourceRequest(cloned, targetPrefix, req.Expected.Baseline) {
				return cloned, cpa.ErrSourceConflict
			}
			if cloned.State == "unknown" {
				return cloned, cpa.ErrApplyUnknown
			}
			return cloned, nil
		}
	}
	if _, _, blockErr := upgradeBlockingSourceOperation(journal); blockErr != nil {
		return freshstore.ConfigGroupOperation{}, blockErr
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
	baselineDoc := upgradeSanitizeSourceDocument(doc)
	if upgradeCanonicalDigest(baselineDoc) != req.Expected.Baseline {
		return freshstore.ConfigGroupOperation{}, cpa.ErrSourceConflict
	}
	if err := upgradeValidateSourceKeyCaseAgainstRaw(doc, targetDoc); err != nil {
		return freshstore.ConfigGroupOperation{}, err
	}
	if !auth.capable.RulesWritable && upgradeTargetDeletesOrNullsRequestsWithPayload(doc, baselineDoc, targetDoc) {
		return freshstore.ConfigGroupOperation{}, freshconfig.ErrRulesReadOnly
	}

	edits, ops, err := upgradePlanSourceChanges(doc, baselineDoc, targetDoc)
	if err != nil {
		return freshstore.ConfigGroupOperation{}, err
	}
	if len(edits) == 0 {
		return freshstore.ConfigGroupOperation{}, freshconfig.Rejection{Code: freshconfig.CodeSourceNoChanges}
	}
	if !auth.capable.RulesWritable && upgradeTouchesPayloadRules(doc, baselineDoc, targetDoc, edits) {
		return freshstore.ConfigGroupOperation{}, freshconfig.ErrRulesReadOnly
	}
	op := freshstore.ConfigGroupOperation{
		SourceID:   auth.sourceID,
		Generation: auth.generation,
		RequestID:  req.RequestID,
		Group:      freshconfig.ConfigSourceGroupID,
		Target:     "group",
		Prefix:     targetPrefix,
		Baseline:   req.Expected.Baseline,
		Edits:      edits,
		State:      "unknown",
	}
	if err := s.appendOperation(ctx, freshconfig.ConfigSourceGroupID, op); err != nil {
		return op, err
	}

	applied := false
	for _, writeOp := range ops {
		var writeErr error
		switch writeOp.kind {
		case upgradeSourceOpPatch:
			writeErr = auth.transport.PatchConfigPath(ctx, auth.secret, writeOp.path, writeOp.payload)
		case upgradeSourceOpPut:
			writeErr = auth.transport.PutConfigValue(ctx, auth.secret, writeOp.path, writeOp.payload)
		case upgradeSourceOpDelete:
			writeErr = auth.transport.DeleteConfigPath(ctx, auth.secret, writeOp.path)
		}
		if writeErr != nil {
			return s.failOperation(ctx, freshconfig.ConfigSourceGroupID, op, writeErr, auth, applied)
		}
		applied = true
	}

	acknowledged := op
	acknowledged.HTTP200 = true
	if err := s.updateOperation(ctx, freshconfig.ConfigSourceGroupID, op, acknowledged); err != nil {
		return op, err
	}
	return s.resolveOperationWithMode(ctx, freshconfig.ConfigSourceGroupID, auth, acknowledged, false)
}

func upgradeSanitizeSourceDocument(document map[string]any) map[string]any {
	out := map[string]any{}
	for key, val := range document {
		lower := strings.ToLower(key)
		if upgradeProtectedSourceRoots[lower] || upgradeSensitiveSourceKeyNames[lower] {
			continue
		}
		if cpa.IsConfigKVMapLeaf(key) {
			out[key] = upgradeCloneJSONValue(val)
			continue
		}
		if subMap, ok := val.(map[string]any); ok {
			cleaned := upgradeSanitizeSourceSubtree(key, subMap)
			if len(subMap) == 0 || len(cleaned) > 0 {
				out[key] = cleaned
			}
			continue
		}
		if subSlice, ok := val.([]any); ok {
			out[key] = upgradeSanitizeSourceSlice(key, subSlice)
			continue
		}
		out[key] = upgradeCloneJSONValue(val)
	}
	return out
}

func upgradeSanitizeSourceSubtree(prefix string, mapping map[string]any) map[string]any {
	out := map[string]any{}
	subBlocked := upgradeProtectedSourceSubtrees[strings.ToLower(prefix)]
	for key, val := range mapping {
		lower := strings.ToLower(key)
		if subBlocked != nil && subBlocked[lower] {
			continue
		}
		if upgradeSensitiveSourceKeyNames[lower] {
			continue
		}
		if strings.EqualFold(prefix, "requests") && lower == "proxy-url" {
			if errors.Is(cpa.ValidateProxyURL(val), cpa.ErrProxyURLCredentials) {
				continue
			}
		}
		path := prefix + "." + key
		if cpa.IsConfigKVMapLeaf(path) {
			out[key] = upgradeCloneJSONValue(val)
			continue
		}
		if child, ok := val.(map[string]any); ok {
			cleaned := upgradeSanitizeSourceSubtree(path, child)
			if len(child) == 0 || len(cleaned) > 0 {
				out[key] = cleaned
			}
			continue
		}
		if sliceVal, ok := val.([]any); ok {
			out[key] = upgradeSanitizeSourceSlice(path, sliceVal)
			continue
		}
		out[key] = upgradeCloneJSONValue(val)
	}
	return out
}

func upgradeSanitizeSourceSlice(prefix string, items []any) []any {
	out := make([]any, len(items))
	itemPrefix := prefix + "[]"
	for i, item := range items {
		switch typed := item.(type) {
		case map[string]any:
			out[i] = upgradeSanitizeSourceSubtree(itemPrefix, typed)
		case []any:
			out[i] = upgradeSanitizeSourceSlice(itemPrefix, typed)
		default:
			out[i] = upgradeCloneJSONValue(item)
		}
	}
	return out
}

func upgradeCloneJSONValue(val any) any {
	switch typed := val.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for k, v := range typed {
			out[k] = upgradeCloneJSONValue(v)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, v := range typed {
			out[i] = upgradeCloneJSONValue(v)
		}
		return out
	default:
		return val
	}
}

func upgradeHasProtectedSubtree(path string) bool {
	lower := strings.ToLower(path)
	if upgradeProtectedSourceSubtrees[lower] != nil {
		return true
	}
	prefix := lower + "."
	for blockedPrefix := range upgradeProtectedSourceSubtrees {
		if strings.HasPrefix(blockedPrefix, prefix) {
			return true
		}
	}
	return false
}

func upgradeEncodeSourceYAML(doc map[string]any) (string, error) {
	if len(doc) == 0 {
		return "{}\n", nil
	}
	node, err := upgradeToYAMLNode(doc)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(node); err != nil {
		return "", err
	}
	if err := enc.Close(); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func upgradeToYAMLNode(value any) (*yaml.Node, error) {
	switch typed := value.(type) {
	case map[string]any:
		node := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		for _, key := range upgradeSortedMapKeys(typed) {
			kNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}
			vNode, err := upgradeToYAMLNode(typed[key])
			if err != nil {
				return nil, err
			}
			node.Content = append(node.Content, kNode, vNode)
		}
		return node, nil
	case []any:
		node := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		for _, item := range typed {
			itemNode, err := upgradeToYAMLNode(item)
			if err != nil {
				return nil, err
			}
			node.Content = append(node.Content, itemNode)
		}
		return node, nil
	case json.Number:
		raw := typed.String()
		if _, err := strconv.ParseInt(raw, 10, 64); err == nil {
			return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: raw}, nil
		}
		if _, err := strconv.ParseUint(raw, 10, 64); err == nil {
			return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: raw}, nil
		}
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!float", Value: raw}, nil
	case bool:
		if typed {
			return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: "true"}, nil
		}
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: "false"}, nil
	case string:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: typed}, nil
	case nil:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: "null"}, nil
	default:
		raw, err := json.Marshal(typed)
		if err != nil {
			return nil, err
		}
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		var normalized any
		if err := dec.Decode(&normalized); err != nil {
			return nil, err
		}
		return upgradeToYAMLNode(normalized)
	}
}

func upgradeParseStrictSourceYAML(raw string) (map[string]any, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return map[string]any{}, nil
	}
	dec := yaml.NewDecoder(strings.NewReader(raw))
	var root yaml.Node
	if err := dec.Decode(&root); err != nil {
		if errors.Is(err, io.EOF) {
			return map[string]any{}, nil
		}
		return nil, freshconfig.Rejection{Code: freshconfig.CodeSourceInvalidYAML, Message: "invalid YAML syntax"}
	}
	var second yaml.Node
	if err := dec.Decode(&second); err != io.EOF {
		return nil, freshconfig.Rejection{Code: freshconfig.CodeSourceInvalidYAML, Message: "multiple YAML documents are not allowed"}
	}
	if root.Kind != yaml.DocumentNode || len(root.Content) != 1 {
		return nil, freshconfig.Rejection{Code: freshconfig.CodeSourceInvalidYAML, Message: "invalid YAML document"}
	}
	top := root.Content[0]
	if top.Kind == yaml.ScalarNode && top.Tag == "!!null" {
		return map[string]any{}, nil
	}
	if top.Kind != yaml.MappingNode {
		return nil, freshconfig.Rejection{Code: freshconfig.CodeSourceInvalidYAML, Message: "YAML root must be a mapping"}
	}
	val, err := upgradeDecodeYAMLNode(top, "", false, 0)
	if err != nil {
		return nil, err
	}
	doc, ok := val.(map[string]any)
	if !ok {
		return nil, freshconfig.Rejection{Code: freshconfig.CodeSourceInvalidYAML, Message: "YAML root must be a mapping"}
	}
	return doc, nil
}

const upgradeMaxYAMLDepth = 32

func upgradeSourceMappingKeyPolicy(path string, inDataValue bool) (allowDataKeys bool, valuesAreData bool) {
	if inDataValue {
		return true, true
	}
	lower := strings.ToLower(path)
	if cpa.IsConfigKVMapLeaf(lower) {
		return true, false
	}
	if strings.HasPrefix(lower, "requests.payload.") {
		rest := strings.TrimPrefix(lower, "requests.payload.")
		if family, tail, ok := strings.Cut(rest, "[]"); ok {
			if _, known := cpa.LookupGatewayPageRuleFamily(family); known {
				switch tail {
				case ".params", ".models[].match[]", ".models[].not-match[]":
					return true, true
				case ".models[].headers", ".replacements[]":
					return true, false
				}
			}
		}
	}
	return false, false
}

func upgradeDecodeYAMLNode(node *yaml.Node, path string, inDataValue bool, depth int) (any, error) {
	if depth > upgradeMaxYAMLDepth {
		return nil, freshconfig.Rejection{Code: freshconfig.CodeSourceInvalidYAML, Message: "YAML nesting depth exceeded"}
	}
	switch node.Kind {
	case yaml.AliasNode:
		return nil, freshconfig.Rejection{Code: freshconfig.CodeSourceInvalidYAML, Message: "YAML aliases are not allowed"}
	case yaml.MappingNode:
		if len(node.Content)%2 != 0 {
			return nil, freshconfig.Rejection{Code: freshconfig.CodeSourceInvalidYAML, Message: "invalid YAML mapping"}
		}
		allowDataKeys, valuesAreData := upgradeSourceMappingKeyPolicy(path, inDataValue)
		out := make(map[string]any, len(node.Content)/2)
		var seenLower map[string]struct{}
		if !allowDataKeys {
			seenLower = make(map[string]struct{}, len(node.Content)/2)
		}
		for i := 0; i < len(node.Content); i += 2 {
			kNode := node.Content[i]
			vNode := node.Content[i+1]
			if kNode.Kind != yaml.ScalarNode || kNode.Tag != "!!str" {
				return nil, freshconfig.Rejection{Code: freshconfig.CodeSourceInvalidYAML, Message: "YAML mapping keys must be strings"}
			}
			key := kNode.Value
			if allowDataKeys {
				if key == "" {
					return nil, freshconfig.Rejection{Code: freshconfig.CodeSourceInvalidYAML, Message: "YAML keys cannot be empty"}
				}
			} else if key == "" || strings.TrimSpace(key) != key || strings.ContainsAny(key, "./?#%\\") {
				return nil, freshconfig.Rejection{Code: freshconfig.CodeSourceInvalidYAML, Message: "YAML keys cannot be empty or contain dots"}
			}
			if _, dup := out[key]; dup {
				return nil, freshconfig.Rejection{Code: freshconfig.CodeSourceInvalidYAML, Message: fmt.Sprintf("duplicate YAML key: %s", key)}
			}
			if seenLower != nil {
				lowerKey := strings.ToLower(key)
				if _, dup := seenLower[lowerKey]; dup {
					return nil, freshconfig.Rejection{Code: freshconfig.CodeSourceInvalidYAML, Message: fmt.Sprintf("duplicate YAML key: %s", key)}
				}
				seenLower[lowerKey] = struct{}{}
			}
			childPath := key
			if path != "" {
				childPath = path + "." + key
			}
			val, err := upgradeDecodeYAMLNode(vNode, childPath, valuesAreData, depth+1)
			if err != nil {
				return nil, err
			}
			out[key] = val
		}
		return out, nil
	case yaml.SequenceNode:
		itemPath := path + "[]"
		out := make([]any, 0, len(node.Content))
		for _, item := range node.Content {
			val, err := upgradeDecodeYAMLNode(item, itemPath, inDataValue, depth+1)
			if err != nil {
				return nil, err
			}
			out = append(out, val)
		}
		return out, nil
	case yaml.ScalarNode:
		switch node.Tag {
		case "!!null":
			return nil, nil
		case "!!bool":
			var b bool
			if err := node.Decode(&b); err != nil {
				return nil, freshconfig.Rejection{Code: freshconfig.CodeSourceInvalidYAML, Message: "invalid boolean value"}
			}
			return b, nil
		case "!!int":
			if i, err := strconv.ParseInt(node.Value, 10, 64); err == nil {
				return json.Number(strconv.FormatInt(i, 10)), nil
			}
			if u, err := strconv.ParseUint(node.Value, 10, 64); err == nil {
				if u > math.MaxInt64 {
					return nil, freshconfig.Rejection{Code: freshconfig.CodeSourceInvalidYAML, Message: "invalid integer value"}
				}
				return json.Number(strconv.FormatUint(u, 10)), nil
			}
			var i int64
			if err := node.Decode(&i); err != nil {
				return nil, freshconfig.Rejection{Code: freshconfig.CodeSourceInvalidYAML, Message: "invalid integer value"}
			}
			return json.Number(strconv.FormatInt(i, 10)), nil
		case "!!float":
			var f float64
			if err := node.Decode(&f); err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
				return nil, freshconfig.Rejection{Code: freshconfig.CodeSourceInvalidYAML, Message: "invalid float value"}
			}
			raw := strconv.FormatFloat(f, 'g', -1, 64)
			if !strings.ContainsAny(raw, ".eE") {
				raw += ".0"
			}
			return json.Number(raw), nil
		case "!!str":
			return node.Value, nil
		default:
			return nil, freshconfig.Rejection{Code: freshconfig.CodeSourceInvalidYAML, Message: fmt.Sprintf("unsupported YAML tag: %s", node.Tag)}
		}
	default:
		return nil, freshconfig.Rejection{Code: freshconfig.CodeSourceInvalidYAML, Message: "unsupported YAML node"}
	}
}

type upgradeSourceOpKind int

const (
	upgradeSourceOpPatch upgradeSourceOpKind = iota + 1
	upgradeSourceOpPut
	upgradeSourceOpDelete
)

type upgradeSourceWriteOp struct {
	kind    upgradeSourceOpKind
	path    string
	payload []byte
}

func upgradeValidateSourceKeyCaseAgainstRaw(rawDoc, targetDoc map[string]any) error {
	if len(rawDoc) == 0 || len(targetDoc) == 0 {
		return nil
	}
	return upgradeValidateSourceNodeCaseAgainstRaw("", "", false, rawDoc, targetDoc)
}

func upgradeValidateSourceNodeCaseAgainstRaw(
	reportPath string,
	schemaPath string,
	inDataValue bool,
	rawNode any,
	targetNode any,
) error {
	switch targetTyped := targetNode.(type) {
	case map[string]any:
		rawMap, ok := rawNode.(map[string]any)
		if !ok || len(rawMap) == 0 || len(targetTyped) == 0 {
			return nil
		}
		allowDataKeys, valuesAreData := upgradeSourceMappingKeyPolicy(schemaPath, inDataValue)
		rawKeys := upgradeSortedMapKeys(rawMap)
		for _, targetKey := range upgradeSortedMapKeys(targetTyped) {
			childReportPath := targetKey
			if reportPath != "" {
				childReportPath = reportPath + "." + targetKey
			}
			childSchemaPath := targetKey
			if schemaPath != "" {
				childSchemaPath = schemaPath + "." + targetKey
			}
			if !allowDataKeys {
				for _, rawKey := range rawKeys {
					if strings.EqualFold(targetKey, rawKey) && targetKey != rawKey {
						if upgradeIsProtectedOrHiddenRawNode(schemaPath, rawKey, rawMap[rawKey]) {
							return freshconfig.Rejection{
								Code:    freshconfig.CodeSourceProtectedPath,
								Field:   childReportPath,
								Message: "source YAML contains protected or hidden key with conflicting case",
							}
						}
						return freshconfig.Rejection{
							Code:    freshconfig.CodeSourceInvalidYAML,
							Field:   childReportPath,
							Message: "source YAML contains key whose case conflicts with active configuration",
						}
					}
				}
			}
			if rawChild, exists := rawMap[targetKey]; exists {
				if err := upgradeValidateSourceNodeCaseAgainstRaw(childReportPath, childSchemaPath, valuesAreData, rawChild, targetTyped[targetKey]); err != nil {
					return err
				}
			}
		}
	case []any:
		if inDataValue {
			return nil
		}
		rawSlice, ok := rawNode.([]any)
		if !ok {
			return nil
		}
		itemSchemaPath := schemaPath + "[]"
		for i := range min(len(targetTyped), len(rawSlice)) {
			itemReportPath := fmt.Sprintf("%s[%d]", reportPath, i)
			if err := upgradeValidateSourceNodeCaseAgainstRaw(itemReportPath, itemSchemaPath, false, rawSlice[i], targetTyped[i]); err != nil {
				return err
			}
		}
	}
	return nil
}

func upgradeIsProtectedOrHiddenRawNode(schemaPrefix, rawKey string, rawVal any) bool {
	lowerPrefix := strings.ToLower(schemaPrefix)
	lowerKey := strings.ToLower(rawKey)
	if schemaPrefix == "" && upgradeProtectedSourceRoots[lowerKey] {
		return true
	}
	if sub := upgradeProtectedSourceSubtrees[lowerPrefix]; sub != nil && sub[lowerKey] {
		return true
	}
	if upgradeSensitiveSourceKeyNames[lowerKey] {
		return true
	}
	rawPath := rawKey
	if schemaPrefix != "" {
		rawPath = schemaPrefix + "." + rawKey
	}
	if upgradeHasProtectedSubtree(rawPath) {
		return true
	}
	return upgradeRawNodeHasProtectedOrStripped(rawPath, rawVal)
}

func upgradePlanSourceChanges(rawDoc, baselineDoc, targetDoc map[string]any) ([]freshstore.ConfigGroupEdit, []upgradeSourceWriteOp, error) {
	if err := upgradeValidateSourceKeyCaseAgainstRaw(rawDoc, targetDoc); err != nil {
		return nil, nil, err
	}
	var edits []freshstore.ConfigGroupEdit
	patchGroups := map[string]map[string]any{}
	var puts []upgradeSourceWriteOp
	var deletes []string

	recordSet := func(path string, val any) error {
		if val == nil {
			if upgradeRawPathHasProtectedOrStripped(rawDoc, path) || upgradeHasProtectedSubtree(path) {
				return freshconfig.Rejection{Code: freshconfig.CodeSourceProtectedPath, Field: path, Message: "cannot remove mapping containing protected keys"}
			}
			if _, presence := cpa.LookupConfigValue(baselineDoc, path); presence == cpa.ConfigPresent {
				edits = append(edits, freshstore.ConfigGroupEdit{Path: path, Action: "default"})
				deletes = append(deletes, path)
				return nil
			}
			return freshconfig.Rejection{Code: freshconfig.CodeSourceInvalidYAML, Field: path, Message: "null values cannot be added as new keys"}
		}
		parts := strings.Split(path, ".")
		if len(parts) == 1 && upgradeRawPathHasProtectedOrStripped(rawDoc, path) {
			return freshconfig.Rejection{Code: freshconfig.CodeSourceProtectedPath, Field: path, Message: "cannot overwrite mapping containing protected keys"}
		}
		if upgradeIsPayloadRulePath(path) && upgradeRawPathHasProtectedOrStripped(rawDoc, path) {
			return freshconfig.Rejection{Code: freshconfig.CodeSourceProtectedPath, Field: path, Message: "cannot overwrite value containing protected keys"}
		}
		edits = append(edits, freshstore.ConfigGroupEdit{Path: path, Action: "set", Value: val})
		if len(parts) == 1 || upgradeIsPayloadRulePath(path) || cpa.IsConfigKVMapLeaf(path) {
			encoded, err := json.Marshal(val)
			if err != nil {
				return err
			}
			puts = append(puts, upgradeSourceWriteOp{kind: upgradeSourceOpPut, path: path, payload: encoded})
			return nil
		}
		parent := strings.Join(parts[:len(parts)-1], ".")
		leafKey := parts[len(parts)-1]
		bucket, ok := patchGroups[parent]
		if !ok {
			bucket = map[string]any{}
			patchGroups[parent] = bucket
		}
		bucket[leafKey] = val
		return nil
	}

	var walk func(prefix string, baseMap, nextMap map[string]any) error
	walk = func(prefix string, baseMap, nextMap map[string]any) error {
		for _, key := range upgradeSortedMapKeys(baseMap) {
			path := key
			if prefix != "" {
				path = prefix + "." + key
			}
			baseVal := baseMap[key]
			nextVal, exists := nextMap[key]
			if !exists {
				if baseChild, ok := baseVal.(map[string]any); ok {
					if upgradeHasProtectedSubtree(path) || upgradeRawPathHasProtectedOrStripped(rawDoc, path) {
						if err := walk(path, baseChild, map[string]any{}); err != nil {
							return err
						}
						continue
					}
				} else if upgradeRawPathHasProtectedOrStripped(rawDoc, path) {
					return freshconfig.Rejection{Code: freshconfig.CodeSourceProtectedPath, Field: path, Message: "cannot remove value containing protected keys"}
				}
				edits = append(edits, freshstore.ConfigGroupEdit{Path: path, Action: "default"})
				deletes = append(deletes, path)
				continue
			}
			if cpa.IsConfigKVMapLeaf(path) {
				if upgradeCanonicalDigest(baseVal) != upgradeCanonicalDigest(nextVal) {
					if err := recordSet(path, nextVal); err != nil {
						return err
					}
				}
				continue
			}
			baseChild, baseIsMap := baseVal.(map[string]any)
			nextChild, nextIsMap := nextVal.(map[string]any)
			if baseIsMap && nextIsMap {
				if err := walk(path, baseChild, nextChild); err != nil {
					return err
				}
				continue
			}
			if baseIsMap && !nextIsMap {
				if upgradeRawPathHasProtectedOrStripped(rawDoc, path) || upgradeHasProtectedSubtree(path) {
					return freshconfig.Rejection{Code: freshconfig.CodeSourceProtectedPath, Field: path, Message: "cannot overwrite or remove mapping containing protected keys"}
				}
			}
			if upgradeCanonicalDigest(baseVal) != upgradeCanonicalDigest(nextVal) {
				if !baseIsMap && upgradeRawPathHasProtectedOrStripped(rawDoc, path) {
					return freshconfig.Rejection{Code: freshconfig.CodeSourceProtectedPath, Field: path, Message: "cannot overwrite value containing protected keys"}
				}
				if nextIsMap {
					if err := upgradeValidateNoNilSourceMapLeaves(path, nextChild); err != nil {
						return err
					}
					if err := recordSet(path, nextChild); err != nil {
						return err
					}
					continue
				}
				if err := recordSet(path, nextVal); err != nil {
					return err
				}
			}
		}

		for _, key := range upgradeSortedMapKeys(nextMap) {
			if _, exists := baseMap[key]; exists {
				continue
			}
			path := key
			if prefix != "" {
				path = prefix + "." + key
			}
			nextVal := nextMap[key]
			if cpa.IsConfigKVMapLeaf(path) {
				if err := recordSet(path, nextVal); err != nil {
					return err
				}
				continue
			}
			if nextChild, ok := nextVal.(map[string]any); ok {
				if len(nextChild) == 0 {
					if upgradeRawPathHasProtectedOrStripped(rawDoc, path) {
						return freshconfig.Rejection{Code: freshconfig.CodeSourceProtectedPath, Field: path, Message: "cannot overwrite mapping containing protected keys"}
					}
					if err := recordSet(path, nextChild); err != nil {
						return err
					}
					continue
				}
				if err := walk(path, map[string]any{}, nextChild); err != nil {
					return err
				}
				continue
			}
			if upgradeRawPathHasProtectedOrStripped(rawDoc, path) || (nextVal == nil && upgradeHasProtectedSubtree(path)) {
				return freshconfig.Rejection{Code: freshconfig.CodeSourceProtectedPath, Field: path, Message: "cannot overwrite mapping containing protected keys"}
			}
			if err := recordSet(path, nextVal); err != nil {
				return err
			}
		}
		return nil
	}

	if err := walk("", baselineDoc, targetDoc); err != nil {
		return nil, nil, err
	}

	sort.Slice(edits, func(i, j int) bool { return edits[i].Path < edits[j].Path })

	var ops []upgradeSourceWriteOp
	patchPrefixes := upgradeSortedMapKeys(patchGroups)
	for _, prefix := range patchPrefixes {
		encoded, err := json.Marshal(patchGroups[prefix])
		if err != nil {
			return nil, nil, err
		}
		ops = append(ops, upgradeSourceWriteOp{kind: upgradeSourceOpPatch, path: prefix, payload: encoded})
	}
	sort.Slice(puts, func(i, j int) bool { return puts[i].path < puts[j].path })
	ops = append(ops, puts...)
	sort.Strings(deletes)
	for _, path := range deletes {
		ops = append(ops, upgradeSourceWriteOp{kind: upgradeSourceOpDelete, path: path})
	}
	return edits, ops, nil
}

func upgradeRawPathHasProtectedOrStripped(rawDoc map[string]any, dotPath string) bool {
	if len(rawDoc) == 0 || dotPath == "" {
		return false
	}
	parts := strings.Split(dotPath, ".")
	found := false
	upgradeForEachRawPathFold(rawDoc, "", parts, func(matchedPath string, val any) {
		if found {
			return
		}
		if upgradeRawNodeHasProtectedOrStripped(matchedPath, val) {
			found = true
		}
	})
	return found
}

func upgradeForEachRawPathFold(current any, prefix string, parts []string, visit func(string, any)) {
	if len(parts) == 0 {
		visit(prefix, current)
		return
	}
	mapping, ok := current.(map[string]any)
	if !ok {
		return
	}
	for key, val := range mapping {
		if strings.EqualFold(key, parts[0]) {
			nextPrefix := key
			if prefix != "" {
				nextPrefix = prefix + "." + key
			}
			upgradeForEachRawPathFold(val, nextPrefix, parts[1:], visit)
		}
	}
}

func upgradeRawNodeHasProtectedOrStripped(prefix string, val any) bool {
	if cpa.IsConfigKVMapLeaf(prefix) {
		return false
	}
	if strings.EqualFold(prefix, "requests.proxy-url") && errors.Is(cpa.ValidateProxyURL(val), cpa.ErrProxyURLCredentials) {
		return true
	}
	switch typed := val.(type) {
	case map[string]any:
		subBlocked := upgradeProtectedSourceSubtrees[strings.ToLower(prefix)]
		for key, child := range typed {
			lower := strings.ToLower(key)
			if prefix == "" && upgradeProtectedSourceRoots[lower] {
				return true
			}
			if subBlocked != nil && subBlocked[lower] {
				return true
			}
			if upgradeSensitiveSourceKeyNames[lower] {
				return true
			}
			if strings.EqualFold(prefix, "requests") && lower == "proxy-url" {
				if errors.Is(cpa.ValidateProxyURL(child), cpa.ErrProxyURLCredentials) {
					return true
				}
			}
			childPath := key
			if prefix != "" {
				childPath = prefix + "." + key
			}
			if upgradeRawNodeHasProtectedOrStripped(childPath, child) {
				return true
			}
		}
		return false
	case []any:
		itemPrefix := prefix + "[]"
		for _, item := range typed {
			if upgradeRawNodeHasProtectedOrStripped(itemPrefix, item) {
				return true
			}
		}
		return false
	default:
		return false
	}
}

func upgradeIsPayloadRulePath(path string) bool {
	lower := strings.ToLower(path)
	if _, ok := cpa.LookupGatewayPageRuleFamily(strings.TrimPrefix(lower, "requests.payload.")); ok {
		return strings.HasPrefix(lower, "requests.payload.")
	}
	return lower == "requests.payload"
}

func upgradeDocHasPayloadSubtree(doc map[string]any) bool {
	for key, val := range doc {
		if !strings.EqualFold(key, "requests") {
			continue
		}
		reqMap, ok := val.(map[string]any)
		if !ok {
			continue
		}
		for childKey := range reqMap {
			if strings.EqualFold(childKey, "payload") {
				return true
			}
		}
	}
	return false
}

func upgradeTouchesPayloadRules(rawDoc, baselineDoc, targetDoc map[string]any, edits []freshstore.ConfigGroupEdit) bool {
	hasPayload := upgradeDocHasPayloadSubtree(rawDoc) || upgradeDocHasPayloadSubtree(baselineDoc) || upgradeDocHasPayloadSubtree(targetDoc)
	for _, edit := range edits {
		lower := strings.ToLower(edit.Path)
		if strings.HasPrefix(lower, "requests.payload.") || lower == "requests.payload" {
			return true
		}
		if lower == "requests" && hasPayload {
			return true
		}
	}
	return false
}

func upgradeTargetDeletesOrNullsRequestsWithPayload(rawDoc, baselineDoc, targetDoc map[string]any) bool {
	if !upgradeDocHasPayloadSubtree(rawDoc) && !upgradeDocHasPayloadSubtree(baselineDoc) {
		return false
	}
	for key, val := range targetDoc {
		if strings.EqualFold(key, "requests") {
			return val == nil
		}
	}
	return true
}

func upgradeValidateNoNilSourceMapLeaves(prefix string, m map[string]any) error {
	if cpa.IsConfigKVMapLeaf(prefix) || upgradeIsPayloadRulePath(prefix) {
		return nil
	}
	for _, key := range upgradeSortedMapKeys(m) {
		path := prefix + "." + key
		val := m[key]
		if val == nil {
			return freshconfig.Rejection{Code: freshconfig.CodeSourceInvalidYAML, Field: path, Message: "null values cannot be added as new keys"}
		}
		if child, ok := val.(map[string]any); ok {
			if err := upgradeValidateNoNilSourceMapLeaves(path, child); err != nil {
				return err
			}
		}
	}
	return nil
}
func upgradeSortedMapKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
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
				view, readErr := u.configService.Read(withUpgradeExternalPrecheckErr(r.Context(), err))
				gatewayConfigReply(w, view, readErr)
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
	case parts[0] == "source" && len(parts) == 1:
		switch r.Method {
		case http.MethodGet:
			if u.targetMode == model.UpgradeTargetExternal {
				_, _, holdCtx, release, err := u.enterExternalCredentialHold(w, r.Context(), upgradeExternalHoldBudget)
				if err != nil {
					if errors.Is(err, errExternalCredentialWaitCancelled) {
						writeExternalCredentialWaitCancelled(w)
						return
					}
					if errors.Is(err, cpa.ErrManagementRejected) {
						gatewayConfigReply(w, freshconfig.SourceView{}, cpa.ErrManagementRejected)
						return
					}
					if errors.Is(err, cpa.ErrExternalRejectionUnavailable) {
						gatewayConfigReply(w, nil, cpa.ErrExternalRejectionUnavailable)
						return
					}
					view, readErr := u.configService.ReadSource(withUpgradeExternalPrecheckErr(r.Context(), err))
					gatewayConfigReply(w, view, readErr)
					return
				}
				defer release()
				view, readErr := u.configService.ReadSource(holdCtx)
				gatewayConfigReply(w, view, readErr)
				return
			}
			view, err := u.configService.ReadSource(r.Context())
			gatewayConfigReply(w, view, err)
		case http.MethodPost:
			body, ok := gatewayConfigBody(w, r)
			if !ok {
				return
			}
			if !exactObject(body, "requestId", "expected", "yaml") || !exactExpected(body) || !exactStringField(body, "yaml") {
				gatewayConfigReply(w, nil, freshconfig.ErrInvalid)
				return
			}
			var req freshconfig.SourceRequest
			if json.Unmarshal(body, &req) != nil {
				gatewayConfigReply(w, nil, freshconfig.ErrInvalid)
				return
			}
			if err := freshconfig.ValidateSourceRequest(req); err != nil {
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
				op, saveErr := u.configService.SaveSource(holdCtx, req)
				gatewayConfigReply(w, op, saveErr)
				return
			}
			op, err := u.configService.SaveSource(r.Context(), req)
			gatewayConfigReply(w, op, err)
		default:
			methodNotAllowed(w, http.MethodGet+", "+http.MethodPost)
		}
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
			if !freshkeys.ValidRequestID(parts[1]) {
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
					if errors.Is(err, cpa.ErrManagementRejected) || errors.Is(err, cpa.ErrExternalRejectionUnavailable) {
						gatewayConfigReply(w, nil, err)
						return
					}
					op, opErr := u.configService.Operation(withUpgradeExternalPrecheckErr(r.Context(), err), parts[1], true)
					gatewayConfigReply(w, op, opErr)
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

func (u *upgradeCanonicalServer) runtimeModeFor(targetMode string) model.RuntimeMode {
	if u == nil {
		return ""
	}
	switch targetMode {
	case model.UpgradeTargetManaged:
		return model.RuntimeModeEmbedded
	case model.UpgradeTargetExternal:
		return model.RuntimeModeExternal
	default:
		return ""
	}
}

func (u *upgradeCanonicalServer) effectiveRuntimeMode(ctx context.Context) model.RuntimeMode {
	if u == nil {
		return ""
	}
	if _, err := u.loadCurrentOp(ctx); err != nil {
		return ""
	}
	return u.runtimeModeFor(u.targetMode)
}

func (u *upgradeCanonicalServer) observeExternalRuntimeStatus(ctx context.Context) (model.RuntimeObservedStatus, error) {
	if u == nil || u.runtimeExternal == nil {
		return model.RuntimeObservedStatus{}, errEmbeddedRuntimeDisabled
	}
	install, credDigest, holdCtx, release, err := u.enterExternalCredentialHold(nil, ctx, upgradeExternalHoldBudget)
	if err != nil {
		return model.RuntimeObservedStatus{}, err
	}
	defer release()
	observed, obsErr := u.runtimeExternal.Status(holdCtx)
	if obsErr != nil {
		u.recordExternalAuthFailureIfMatching(ctx, install, credDigest, obsErr)
		return model.RuntimeObservedStatus{}, obsErr
	}
	return observed, nil
}

func (u *upgradeCanonicalServer) observeManagedUpdateRuntime(ctx context.Context) (upgradeDestinationRecord, model.RuntimeObservedStatus, error) {
	rec, _, err := u.loadVerifiedManagedDestination(ctx)
	if err != nil {
		return upgradeDestinationRecord{}, model.RuntimeObservedStatus{}, err
	}
	observed, err := u.runtime.Status(ctx)
	if err != nil {
		return rec, model.RuntimeObservedStatus{}, errUpgradeManagedRuntimeUnreachable
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

type upgradeCPAUpdateStore struct {
	owner *upgradeCanonicalServer
}

var _ cpaupdateservice.Store = upgradeCPAUpdateStore{}

func (s upgradeCPAUpdateStore) LoadCPAUpdateCheck(ctx context.Context) ([]byte, error) {
	if s.owner == nil || s.owner.db == nil || !s.owner.effectiveRuntimeMode(ctx).IsValid() {
		return nil, errUpgradeManagedRuntimeUnavailable
	}
	return s.owner.db.LoadCPAUpdateCheck(ctx)
}

func (s upgradeCPAUpdateStore) SaveCPAUpdateCheck(ctx context.Context, data []byte) error {
	if s.owner == nil || s.owner.db == nil || !s.owner.effectiveRuntimeMode(ctx).IsValid() {
		return errUpgradeManagedRuntimeUnavailable
	}
	return s.owner.db.SaveCPAUpdateCheck(ctx, data)
}

type upgradeRuntimeClientAdapter struct {
	owner *upgradeCanonicalServer
}

var (
	_ runtimeservice.RuntimeClient         = upgradeRuntimeClientAdapter{}
	_ runtimeservice.PrepareUpdateUploader = upgradeRuntimeClientAdapter{}
)

func (c upgradeRuntimeClientAdapter) Status(ctx context.Context) (model.RuntimeObservedStatus, error) {
	if c.owner == nil {
		return model.RuntimeObservedStatus{}, errEmbeddedRuntimeDisabled
	}
	switch c.owner.effectiveRuntimeMode(ctx) {
	case model.RuntimeModeExternal:
		return c.owner.observeExternalRuntimeStatus(ctx)
	case model.RuntimeModeEmbedded:
		_, observed, err := c.owner.observeManagedUpdateRuntime(ctx)
		return observed, err
	default:
		return model.RuntimeObservedStatus{}, errEmbeddedRuntimeDisabled
	}
}

func (c upgradeRuntimeClientAdapter) verifyManagedTarget(
	ctx context.Context,
	expectedIdentity model.RuntimeIdentity,
	mutation bool,
) (runtimeservice.RuntimeClient, error) {
	if c.owner == nil {
		return nil, errEmbeddedRuntimeDisabled
	}
	switch c.owner.effectiveRuntimeMode(ctx) {
	case model.RuntimeModeExternal:
		if mutation {
			return nil, runtimeservice.ErrRuntimeMutationUnsupported
		}
		return nil, runtimeservice.ErrRuntimeObservationUnsupported
	case model.RuntimeModeEmbedded:
	default:
		return nil, errUpgradeManagedRuntimeUnavailable
	}
	rec, _, err := c.owner.observeManagedUpdateRuntime(ctx)
	if err != nil {
		return nil, err
	}
	if expectedIdentity == "" || expectedIdentity != rec.Intent.ExpectedRuntimeIdentity {
		return nil, errUpgradeManagedRuntimeUnavailable
	}
	if c.owner.runtime == nil {
		return nil, errUpgradeManagedRuntimeUnavailable
	}
	return c.owner.runtime, nil
}

func (c upgradeRuntimeClientAdapter) enterManagedLifecycleMutation(
	ctx context.Context,
	expectedIdentity model.RuntimeIdentity,
	requireRunningDesired bool,
) (runtimeservice.RuntimeClient, func(), error) {
	if c.owner == nil {
		return nil, nil, errEmbeddedRuntimeDisabled
	}
	switch c.owner.effectiveRuntimeMode(ctx) {
	case model.RuntimeModeExternal:
		return nil, nil, runtimeservice.ErrRuntimeMutationUnsupported
	case model.RuntimeModeEmbedded:
	default:
		return nil, nil, errUpgradeManagedRuntimeUnavailable
	}
	if _, _, err := c.owner.loadVerifiedManagedDestination(ctx); err != nil {
		return nil, nil, err
	}
	unlock, err := c.owner.runtimeLifecycleMu.Lock(ctx)
	if err != nil {
		return nil, nil, err
	}
	if c.owner.db != nil {
		desired, found, desiredErr := c.owner.db.LoadEmbeddedRuntimeDesiredState(ctx)
		if desiredErr != nil {
			unlock()
			return nil, nil, errUpgradeManagedRuntimeUnavailable
		}
		if requireRunningDesired && found && desired.DesiredLifecycle != model.EmbeddedRuntimeDesiredRunning {
			unlock()
			return nil, nil, &runtimeservice.ProtocolError{
				Code:       runtimeservice.ProtocolErrorOperationStateConflict,
				HTTPStatus: http.StatusConflict,
			}
		}
	}
	client, err := c.verifyManagedTarget(ctx, expectedIdentity, true)
	if err != nil {
		unlock()
		return nil, nil, err
	}
	return client, unlock, nil
}

func (c upgradeRuntimeClientAdapter) Start(
	ctx context.Context,
	request model.RuntimeMutationRequest,
) (model.RuntimeOperationResult, error) {
	client, unlock, err := c.enterManagedLifecycleMutation(ctx, request.ExpectedRuntimeIdentity, false)
	if err != nil {
		return model.RuntimeOperationResult{}, err
	}
	defer unlock()
	return client.Start(ctx, request)
}

func (c upgradeRuntimeClientAdapter) Stop(
	ctx context.Context,
	request model.RuntimeMutationRequest,
) (model.RuntimeOperationResult, error) {
	client, unlock, err := c.enterManagedLifecycleMutation(ctx, request.ExpectedRuntimeIdentity, false)
	if err != nil {
		return model.RuntimeOperationResult{}, err
	}
	defer unlock()
	return client.Stop(ctx, request)
}

func (c upgradeRuntimeClientAdapter) Restart(
	ctx context.Context,
	request model.RuntimeMutationRequest,
) (model.RuntimeOperationResult, error) {
	client, unlock, err := c.enterManagedLifecycleMutation(ctx, request.ExpectedRuntimeIdentity, false)
	if err != nil {
		return model.RuntimeOperationResult{}, err
	}
	defer unlock()
	return client.Restart(ctx, request)
}

func (c upgradeRuntimeClientAdapter) PrepareUpdate(
	ctx context.Context,
	request model.RuntimePrepareUpdateRequest,
) (model.RuntimeOperationResult, error) {
	client, err := c.verifyManagedTarget(ctx, request.ExpectedRuntimeIdentity, true)
	if err != nil {
		return model.RuntimeOperationResult{}, err
	}
	return client.PrepareUpdate(ctx, request)
}

func (c upgradeRuntimeClientAdapter) PrepareUpdateUpload(
	ctx context.Context,
	request model.RuntimePrepareUpdateRequest,
	archive io.Reader,
	contentLength int64,
) (model.RuntimeOperationResult, error) {
	client, err := c.verifyManagedTarget(ctx, request.ExpectedRuntimeIdentity, true)
	if err != nil {
		return model.RuntimeOperationResult{}, err
	}
	uploader, ok := client.(runtimeservice.PrepareUpdateUploader)
	if !ok {
		return model.RuntimeOperationResult{}, runtimeservice.ErrRuntimeMutationUnsupported
	}
	return uploader.PrepareUpdateUpload(ctx, request, archive, contentLength)
}

func (c upgradeRuntimeClientAdapter) ActivateUpdate(
	ctx context.Context,
	request model.RuntimeActivateUpdateRequest,
) (model.RuntimeOperationResult, error) {
	client, unlock, err := c.enterManagedLifecycleMutation(ctx, request.ExpectedRuntimeIdentity, true)
	if err != nil {
		return model.RuntimeOperationResult{}, err
	}
	defer unlock()
	return client.ActivateUpdate(ctx, request)
}

func (c upgradeRuntimeClientAdapter) ObserveUpdateOperation(
	ctx context.Context,
	request model.RuntimeObserveUpdateOperationRequest,
) (model.RuntimeUpdateOperationObservation, error) {
	client, err := c.verifyManagedTarget(ctx, request.ExpectedRuntimeIdentity, false)
	if err != nil {
		return model.RuntimeUpdateOperationObservation{}, err
	}
	return client.ObserveUpdateOperation(ctx, request)
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
	if err := u.enableAndReconcileExternalCollector(holdCtx, install, &externalUsageProbe{report: report, enabled: true}); err != nil {
		u.writeUpgradeExternalUsageError(w, err)
		return
	}
	state, err := u.currentExternalUsageState(holdCtx, true, report)
	if err != nil {
		u.writeUpgradeExternalUsageError(w, err)
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
		if err := u.reconcileCollectorWithProbe(ctx, &externalUsageProbe{report: report, enabled: enabled}); err != nil {
			return externalUsageState{}, err
		}
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
		u.reconcileCollectorBestEffort(withUpgradeExternalHoldInstall(rebindCtx, freshkeyapply.ExternalInstall{
			Identity:      binding.Identity,
			BaseURL:       canonicalURL,
			ManagementKey: candidateKey,
		}))
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
	u.reconcileCollectorBestEffort(withUpgradeExternalHoldInstall(rebindCtx, freshkeyapply.ExternalInstall{
		Identity:      updatedBinding.Identity,
		BaseURL:       canonicalURL,
		ManagementKey: candidateKey,
	}))
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
	if len(r.Header.Values("Authorization")) > 1 {
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	authorized, authErr := u.verifyAdminBearer(r.Context(), r)
	if authErr != nil {
		writeUpgradeAuthorityUnavailable(w)
		return
	}
	if !authorized {
		u.inner.ServeHTTP(w, r)
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
		install, credDigest, hold, holdCtx, err := u.enterExternalBridgeCredentialHold(r.Context(), r.URL.Path)
		if err != nil {
			u.writeExternalBridgePrecheckFailure(w, err)
			return
		}
		defer hold.finish()
		overrideCtx := proxysvc.WithSavedSetupOverride(holdCtx, install.BaseURL, install.ManagementKey)
		markedCtx, mark := proxysvc.WithSavedKeyMark(overrideCtx)
		rec := newUpgradeExternalLegacyRecorder(w, context.WithoutCancel(holdCtx), u, hold, install, credDigest, mark)
		u.inner.ServeHTTP(rec, r.WithContext(markedCtx))
		rec.finish()
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
		sub == "setup" || strings.HasPrefix(sub, "setup/") {
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
			writeLegacyRefusal(w, externalLegacyWriteReplaced)
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
	} else if cpampLegacyClientKeyWriteReplaced(clone.URL.Path, r.Method) {
		writeLegacyRefusal(w, legacyWriteReplaced)
		return
	}

	if u.targetMode == model.UpgradeTargetManaged && clone.URL.Path == "/usage-service/config" && r.Method == http.MethodPut {
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
		if clone.Body != nil && clone.Body != http.NoBody {
			const maxManagedConfigPutBytes = 64 << 10
			rawBody, readErr := io.ReadAll(io.LimitReader(clone.Body, maxManagedConfigPutBytes+1))
			if readErr != nil || len(rawBody) > maxManagedConfigPutBytes {
				w.Header().Set("Connection", "close")
				_ = http.NewResponseController(w).SetReadDeadline(time.Unix(1, 0))
				_ = clone.Body.Close()
			} else {
				_ = clone.Body.Close()
			}
			if readErr == nil {
				clone.Body = io.NopCloser(bytes.NewReader(rawBody))
				if len(rawBody) <= maxManagedConfigPutBytes && utf8.Valid(rawBody) {
					var req struct {
						Config model.ManagerConfig `json:"config"`
					}
					decoder := json.NewDecoder(bytes.NewReader(rawBody))
					if err := decoder.Decode(&req); err == nil {
						if _, err := decoder.Token(); err == io.EOF {
							conn := req.Config.CPAConnection
							if rawURL := strings.TrimSpace(conn.CPABaseURL); rawURL != "" {
								submittedCanonical, normErr := canonicalUpgradeUsageURL(rawURL)
								if normErr != nil || submittedCanonical != rec.CanonicalURL {
									w.Header().Set("Content-Type", "application/json")
									w.WriteHeader(http.StatusConflict)
									writeFreshJSON(w, struct {
										Code    string `json:"code"`
										Message string `json:"message"`
									}{
										Code:    "managed_connection_immutable",
										Message: "Managed Upgrade CPA connection cannot be replaced via usage-service config",
									})
									return
								}
							}
							if rawKey := strings.TrimSpace(conn.ManagementKey); rawKey != "" && !security.EqualHMAC(rawKey, secret) {
								w.Header().Set("Content-Type", "application/json")
								w.WriteHeader(http.StatusConflict)
								writeFreshJSON(w, struct {
									Code    string `json:"code"`
									Message string `json:"message"`
								}{
									Code:    "managed_connection_immutable",
									Message: "Managed Upgrade CPA connection cannot be replaced via usage-service config",
								})
								return
							}
						}
					}
				}
			}
		}
	}

	hasCollectorPatch := false
	hasEnabledPatch := false
	if clone.URL.Path == "/usage-service/config" && r.Method == http.MethodPatch && clone.Body != nil {
		rawBody, readErr := io.ReadAll(io.LimitReader(clone.Body, 4096+1))
		if readErr != nil || len(rawBody) > 4096 {
			w.Header().Set("Connection", "close")
			_ = http.NewResponseController(w).SetReadDeadline(time.Unix(1, 0))
			_ = clone.Body.Close()
		} else {
			_ = clone.Body.Close()
		}
		if readErr == nil {
			clone.Body = io.NopCloser(bytes.NewReader(rawBody))
			if len(rawBody) <= 4096 {
				var bodyMap map[string]json.RawMessage
				if json.Unmarshal(rawBody, &bodyMap) == nil {
					for key := range bodyMap {
						if strings.EqualFold(key, "collector") {
							hasCollectorPatch = true
						} else if strings.EqualFold(key, "enabled") {
							hasEnabledPatch = true
						}
					}
				}
			}
		}
	}
	reconcileCollectorAfter := clone.URL.Path == "/usage-service/config" &&
		(r.Method == http.MethodPut || hasCollectorPatch || hasEnabledPatch)
	sanitizer := bridgedReadSanitizer(clone.URL.Path, r.Method)
	if sanitizer != nil {
		clone.Header = clone.Header.Clone()
		stripBridgeSanitizedRequestHeaders(clone.Header)
	}
	if u.targetMode == model.UpgradeTargetExternal {
		if legacyBridgePresentsSavedKey(clone) {
			if u.appCtx != nil && u.appCtx.ProxyService != nil {
				nextReq, releaseAuthFile, status, authErr := u.appCtx.ProxyService.AcquireRequestAuthFileMutation(clone)
				if authErr != nil {
					response.Error(w, status, authErr)
					return
				}
				defer releaseAuthFile()
				clone = nextReq
			}
			install, credDigest, hold, holdCtx, err := u.enterExternalBridgeCredentialHold(clone.Context(), clone.URL.Path)
			if err != nil {
				u.writeExternalBridgePrecheckFailure(w, err)
				return
			}
			defer hold.finish()
			if strings.TrimRight(clone.URL.Path, "/") == "/v0/management/config" &&
				r.Method != http.MethodGet && r.Method != http.MethodHead {
				writeLegacyRefusal(w, externalLegacyWriteReplaced)
				return
			}
			overrideCtx := proxysvc.WithSavedSetupOverride(holdCtx, install.BaseURL, install.ManagementKey)
			markedCtx, mark := proxysvc.WithSavedKeyMark(overrideCtx)
			targetWriter := w
			var sanitizingWriter *bridgeSanitizingResponseWriter
			if sanitizer != nil {
				sanitizingWriter = newBridgeSanitizingWriter(w, sanitizer)
				targetWriter = sanitizingWriter
			}
			rec := newUpgradeExternalLegacyRecorder(targetWriter, context.WithoutCancel(holdCtx), u, hold, install, credDigest, mark)
			u.inner.ServeHTTP(rec, clone.WithContext(markedCtx))
			rec.finish()
			if sanitizingWriter != nil {
				sanitizingWriter.finish(r.Method)
			}
			if reconcileCollectorAfter && rec.statusCode() >= 200 && rec.statusCode() < 300 {
				reconcileCtx, cancelReconcile := u.detachedReconcileContext(r.Context())
				u.reconcileCollectorBestEffort(reconcileCtx)
				cancelReconcile()
			}
			return
		}
		if legacyBridgeServicePresentsSavedKey(clone) {
			if u.workspace == nil || !u.workspace.credentialGate.acquire(clone.Context()) {
				writeExternalCredentialWaitCancelled(w)
				return
			}
			install, _, err := u.precheckExternalCredential(clone.Context())
			u.workspace.credentialGate.release()
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
			reconcileCtx, cancelReconcile := u.detachedReconcileContext(r.Context())
			u.reconcileCollectorBestEffort(reconcileCtx)
			cancelReconcile()
		}
		return
	}

	if sanitizer != nil {
		sanitizingWriter := newBridgeSanitizingWriter(w, sanitizer)
		u.inner.ServeHTTP(sanitizingWriter, clone)
		sanitizingWriter.finish(r.Method)
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

func (w *statusCaptureResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

type upgradeExternalLegacyRecorder struct {
	dst          http.ResponseWriter
	header       http.Header
	status       int
	decided      bool
	intercepting bool
	streaming    bool
	recordCtx    context.Context
	owner        *upgradeCanonicalServer
	hold         *externalCredentialHold
	install      freshkeyapply.ExternalInstall
	credDigest   string
	mark         *proxysvc.SavedKeyMark
}

func newUpgradeExternalLegacyRecorder(
	dst http.ResponseWriter,
	recordCtx context.Context,
	owner *upgradeCanonicalServer,
	hold *externalCredentialHold,
	install freshkeyapply.ExternalInstall,
	credDigest string,
	mark *proxysvc.SavedKeyMark,
) *upgradeExternalLegacyRecorder {
	return &upgradeExternalLegacyRecorder{
		dst:        dst,
		header:     make(http.Header),
		status:     http.StatusOK,
		recordCtx:  recordCtx,
		owner:      owner,
		hold:       hold,
		install:    install,
		credDigest: credDigest,
		mark:       mark,
	}
}

func (r *upgradeExternalLegacyRecorder) release() {
	if r.hold != nil {
		r.hold.release()
	}
}

func (r *upgradeExternalLegacyRecorder) Header() http.Header {
	if r.streaming {
		return r.dst.Header()
	}
	return r.header
}

func (r *upgradeExternalLegacyRecorder) WriteHeader(code int) {
	if r.decided {
		return
	}
	if code < http.StatusOK {
		copyUpgradeHeaders(r.dst.Header(), r.header)
		r.header = make(http.Header)
		r.dst.WriteHeader(code)
		return
	}
	r.decided = true
	r.status = code
	if r.hold != nil {
		r.hold.claimHeader()
	}
	if r.mark != nil && r.mark.Attached() && (code == http.StatusUnauthorized || code == http.StatusForbidden) {
		r.intercepting = true
		if code == http.StatusUnauthorized {
			r.owner.recordExternalAuthFailureIfMatching(r.recordCtx, r.install, r.credDigest, cpa.ErrManagementRejected)
			writeExternalManagementRejected(r.dst)
		} else {
			r.owner.recordExternalAuthFailureIfMatching(r.recordCtx, r.install, r.credDigest, cpa.ErrManagementClientForbidden)
			writeExternalManagementForbidden(r.dst)
		}
		r.release()
		return
	}
	r.streaming = true
	copyUpgradeHeaders(r.dst.Header(), r.header)
	r.dst.WriteHeader(code)
	r.release()
}

func (r *upgradeExternalLegacyRecorder) Write(p []byte) (int, error) {
	if r.intercepting {
		return len(p), nil
	}
	if !r.decided {
		r.WriteHeader(http.StatusOK)
	}
	return r.dst.Write(p)
}

func (r *upgradeExternalLegacyRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := r.dst.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	r.release()
	r.decided = true
	return hijacker.Hijack()
}

func (r *upgradeExternalLegacyRecorder) Unwrap() http.ResponseWriter {
	return r.dst
}

func (r *upgradeExternalLegacyRecorder) Flush() {
	if r.intercepting {
		return
	}
	if flusher, ok := r.dst.(http.Flusher); ok {
		if !r.decided {
			r.WriteHeader(http.StatusOK)
		}
		flusher.Flush()
	}
}

func (r *upgradeExternalLegacyRecorder) statusCode() int {
	return r.status
}

func (r *upgradeExternalLegacyRecorder) finish() {
	if !r.decided {
		r.decided = true
		r.streaming = true
		copyUpgradeHeaders(r.dst.Header(), r.header)
		r.dst.WriteHeader(http.StatusOK)
		r.release()
	}
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
	probe *externalUsageProbe,
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
	if err := upgradeInheritSetupCollectorSettings(ctx, u.db, &mgrCfg, ok); err != nil {
		return err
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
	return u.reconcileCollectorWithProbe(ctx, probe)
}

func upgradeValidCollectorPopSide(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "left":
		return "left"
	case "right":
		return "right"
	default:
		return ""
	}
}

func upgradeInheritSetupCollectorSettings(
	ctx context.Context,
	db *store.Store,
	mgrCfg *model.ManagerConfig,
	mgrOK bool,
) error {
	if mgrCfg == nil || db == nil {
		return nil
	}
	rawPopSide := strings.TrimSpace(mgrCfg.Collector.PopSide)
	mgrCfg.Collector.Queue = strings.TrimSpace(mgrCfg.Collector.Queue)
	mgrCfg.Collector.PopSide = upgradeValidCollectorPopSide(rawPopSide)
	if !mgrOK || mgrCfg.Collector.Queue == "" || mgrCfg.Collector.PopSide == "" {
		setupCfg, setupOK, err := db.LoadSetup(ctx)
		if err != nil {
			return err
		}
		if setupOK {
			if mgrCfg.Collector.Queue == "" {
				if queue := strings.TrimSpace(setupCfg.Queue); queue != "" {
					mgrCfg.Collector.Queue = queue
				}
			}
			if (!mgrOK || rawPopSide == "") && mgrCfg.Collector.PopSide == "" {
				if side := upgradeValidCollectorPopSide(setupCfg.PopSide); side != "" {
					mgrCfg.Collector.PopSide = side
				}
			}
		}
	}
	return nil
}

func (u *upgradeCanonicalServer) healManagedStoredConnectionLocked(
	ctx context.Context,
	mgrCfg *model.ManagerConfig,
	mgrOK bool,
	rec upgradeDestinationRecord,
	secret string,
) error {
	if u == nil || u.db == nil || mgrCfg == nil {
		return nil
	}
	if err := upgradeInheritSetupCollectorSettings(ctx, u.db, mgrCfg, mgrOK); err != nil {
		return err
	}
	setupCfg, setupOK, err := u.db.LoadSetup(ctx)
	if err != nil {
		return err
	}
	mgrNeedsHeal := mgrOK && (cpa.NormalizeBaseURL(mgrCfg.CPAConnection.CPABaseURL) != cpa.NormalizeBaseURL(rec.BaseURL) ||
		!security.EqualHMAC(strings.TrimSpace(mgrCfg.CPAConnection.ManagementKey), secret))
	setupNeedsHeal := setupOK && (cpa.NormalizeBaseURL(setupCfg.CPAUpstreamURL) != cpa.NormalizeBaseURL(rec.BaseURL) ||
		!security.EqualHMAC(strings.TrimSpace(setupCfg.ManagementKey), secret))
	mgrCfg.CPAConnection.CPABaseURL = rec.BaseURL
	mgrCfg.CPAConnection.ManagementKey = secret
	if !mgrNeedsHeal && !setupNeedsHeal {
		return nil
	}
	nextSetup := setupCfg
	nextSetup.CPAUpstreamURL = rec.BaseURL
	nextSetup.ManagementKey = secret
	if strings.TrimSpace(nextSetup.Queue) == "" {
		nextSetup.Queue = mgrCfg.Collector.Queue
	}
	if upgradeValidCollectorPopSide(nextSetup.PopSide) == "" {
		nextSetup.PopSide = mgrCfg.Collector.PopSide
	}
	if mgrOK {
		return u.db.SaveManagerConfigAndSetup(ctx, *mgrCfg, nextSetup)
	}
	return u.db.SaveSetup(ctx, nextSetup)
}

func (u *upgradeCanonicalServer) reconcileCollectorBestEffort(ctx context.Context) {
	if err := u.reconcileCollector(ctx); err != nil {
		log.Printf("[upgrade-canonical] collector reconciliation skipped: %v", err)
	}
}

type externalUsageProbe struct {
	report  cpa.ExternalVersionReport
	enabled bool
}

func (u *upgradeCanonicalServer) detachedReconcileContext(reqCtx context.Context) (context.Context, context.CancelFunc) {
	if reqCtx == nil {
		reqCtx = context.Background()
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(reqCtx), upgradeExternalHoldBudget)
	if u != nil && u.hostCtx != nil {
		stopHost := context.AfterFunc(u.hostCtx, cancel)
		return ctx, func() {
			stopHost()
			cancel()
		}
	}
	return ctx, cancel
}

func (u *upgradeCanonicalServer) reconcileCollector(ctx context.Context) error {
	return u.reconcileCollectorWithProbe(ctx, nil)
}

func (u *upgradeCanonicalServer) reconcileCollectorWithProbe(ctx context.Context, probe *externalUsageProbe) error {
	if u == nil || u.appCtx == nil || u.appCtx.Collector == nil || u.db == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	mgrCfg, mgrOK, err := u.db.LoadManagerConfig(ctx)
	if err != nil {
		return err
	}
	if mgrOK && !collectorsvc.ManagerCollectorEnabled(mgrCfg) {
		u.reconcileMu.Lock()
		latestCfg, latestOK, latestErr := u.db.LoadManagerConfig(ctx)
		if latestErr != nil {
			u.reconcileMu.Unlock()
			return latestErr
		}
		if latestOK && !collectorsvc.ManagerCollectorEnabled(latestCfg) {
			if u.targetMode == model.UpgradeTargetManaged && u.workspace != nil {
				if rec, secret, destErr := u.workspace.loadManagedDestination(ctx); destErr == nil {
					_ = u.healManagedStoredConnectionLocked(ctx, &latestCfg, latestOK, rec, secret)
				}
			}
			u.appCtx.Collector.Stop()
			u.reconcileMu.Unlock()
			return nil
		}
		u.reconcileMu.Unlock()
	}
	if u.targetMode == model.UpgradeTargetExternal && u.workspace != nil && !hasUpgradeExternalHold(ctx) {
		if !u.workspace.credentialGate.acquire(ctx) {
			return errExternalCredentialWaitCancelled
		}
		defer u.workspace.credentialGate.release()
	}
	u.reconcileMu.Lock()
	defer u.reconcileMu.Unlock()

	mgrCfg, mgrOK, err = u.db.LoadManagerConfig(ctx)
	if err != nil {
		return err
	}
	if mgrOK && !collectorsvc.ManagerCollectorEnabled(mgrCfg) {
		if u.targetMode == model.UpgradeTargetManaged && u.workspace != nil {
			if rec, secret, destErr := u.workspace.loadManagedDestination(ctx); destErr == nil {
				_ = u.healManagedStoredConnectionLocked(ctx, &mgrCfg, mgrOK, rec, secret)
			}
		}
		u.appCtx.Collector.Stop()
		return nil
	}
	if !mgrOK {
		mgrCfg = model.ManagerConfig{}
	}
	if err := upgradeInheritSetupCollectorSettings(ctx, u.db, &mgrCfg, mgrOK); err != nil {
		return err
	}
	var externalInstall freshkeyapply.ExternalInstall
	var externalCredDigest string
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
		targetCfg := mgrCfg
		targetCfg.CPAConnection.CPABaseURL = install.BaseURL
		targetCfg.CPAConnection.ManagementKey = install.ManagementKey
		targetRTCfg := collectorsvc.RuntimeConfigFromManagerConfigWithFallback(targetCfg, u.appCtx.Config)
		activeWithMatchingTarget := func() bool {
			activeCfg, active := u.appCtx.Collector.ActiveRuntimeConfig()
			return active && activeCfg == targetRTCfg
		}

		var report cpa.ExternalVersionReport
		var statsEnabled bool
		if probe != nil {
			report = probe.report
			statsEnabled = probe.enabled
		} else {
			probeCtx := ctx
			var recordedRejection atomic.Bool
			if u.workspace != nil && u.workspace.rejection != nil && install.Identity != "" && credDigest != "" {
				probeCtx = cpa.WithExternalHeaderObserver(ctx, func(statusCode int, _ bool) {
					if statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden {
						if recordedRejection.CompareAndSwap(false, true) {
							errForStatus := cpa.ErrManagementRejected
							if statusCode == http.StatusForbidden {
								errForStatus = cpa.ErrManagementClientForbidden
							}
							u.recordExternalAuthFailureIfMatching(ctx, install, credDigest, errForStatus)
						}
					}
				})
			}
			var verErr error
			report, verErr = cpa.ObserveExternalVersion(probeCtx, install.BaseURL, install.ManagementKey)
			if verErr != nil {
				rejected := recordedRejection.Load() || errors.Is(verErr, cpa.ErrManagementRejected) || errors.Is(verErr, cpa.ErrManagementClientForbidden)
				if !recordedRejection.Load() {
					u.recordExternalAuthFailureIfMatching(ctx, install, credDigest, verErr)
				}
				if rejected || !activeWithMatchingTarget() {
					u.appCtx.Collector.Stop()
				}
				return verErr
			}
			if report.WriteBlocked {
				u.appCtx.Collector.Stop()
				return cpa.ErrExternalVersionUnsupported
			}
			var statsErr error
			statsEnabled, statsErr = cpa.FetchExternalUsageStatistics(probeCtx, install.BaseURL, install.ManagementKey)
			if statsErr != nil {
				rejected := recordedRejection.Load() || errors.Is(statsErr, cpa.ErrManagementRejected) || errors.Is(statsErr, cpa.ErrManagementClientForbidden)
				if !recordedRejection.Load() {
					u.recordExternalAuthFailureIfMatching(ctx, install, credDigest, statsErr)
				}
				if rejected || !activeWithMatchingTarget() {
					u.appCtx.Collector.Stop()
				}
				return statsErr
			}
		}
		if report.WriteBlocked {
			u.appCtx.Collector.Stop()
			return cpa.ErrExternalVersionUnsupported
		}
		if !statsEnabled {
			u.appCtx.Collector.Stop()
			return nil
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
		if err := u.healManagedStoredConnectionLocked(ctx, &mgrCfg, mgrOK, rec, secret); err != nil {
			return err
		}
	default:
		u.appCtx.Collector.Stop()
		return nil
	}

	rtCfg := collectorsvc.RuntimeConfigFromManagerConfigWithFallback(mgrCfg, u.appCtx.Config)
	if activeCfg, active := u.appCtx.Collector.ActiveRuntimeConfig(); active && activeCfg == rtCfg {
		return nil
	}
	collectorCtx := u.hostCtx
	if collectorCtx == nil {
		collectorCtx = context.Background()
	}
	if u.targetMode == model.UpgradeTargetExternal {
		var rejection *upgradeExternalRejection
		if u.workspace != nil {
			rejection = u.workspace.rejection
		}
		startUpgradeExternalCollector(
			collectorCtx,
			u.appCtx.Collector,
			rejection,
			externalInstall.Identity,
			externalCredDigest,
			externalInstall.BaseURL,
			externalInstall.ManagementKey,
			rtCfg,
		)
		return nil
	}
	u.appCtx.Collector.Start(collectorCtx, rtCfg)
	return nil
}
