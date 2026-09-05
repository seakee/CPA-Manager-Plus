package codexinspection

import (
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/codexquota"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	codexinspectionrepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/codexinspection"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpaauthfiles"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/credentialpolicy"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/managerconfig"
	quotasnapshotsvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/quotasnapshot"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usageidentity"
)

const (
	codexUsageURL             = "https://chatgpt.com/backend-api/wham/usage"
	codexFiveHourWindow       = 18_000
	codexWeekWindow           = 604_800
	codexMonthWindow          = 2_592_000
	codexMinMonthWindow       = 28 * 24 * 60 * 60
	codexMaxMonthWindow       = 31 * 24 * 60 * 60
	maxStoredBodyText         = 2048
	maxCPAAPICallResponseSize = 16 * 1024 * 1024
	criticalWriteTimeout      = 8 * time.Second
	processLogWriteTimeout    = 750 * time.Millisecond
	resultWriteTimeout        = 2 * time.Second
	resultPersistenceTimeout  = 8 * time.Second
	cancelledPersistTimeout   = 2 * time.Second
	processLogQueueWait       = 10 * time.Millisecond
	minimumInspectionLease    = time.Millisecond
	userCancelRequestReason   = "用户请求取消巡检"
	userCancelledReason       = "用户主动取消巡检"
)

var (
	ErrRunAlreadyActive           = errors.New("codex inspection is already running")
	ErrRunNotCancellable          = errors.New("codex inspection run cannot be cancelled")
	ErrServiceStopping            = errors.New("codex inspection service is stopping")
	ErrRunNotOwned                = errors.New("codex inspection run is owned by another instance")
	ErrTriggerAlreadyExists       = errors.New("codex inspection trigger already handled")
	ErrScheduledRunDisabled       = errors.New("scheduled codex inspection is disabled")
	ErrNotConfigured              = errors.New("usage service is not configured")
	ErrRunNotFound                = errors.New("codex inspection run not found")
	ErrRunNotCompleted            = errors.New("codex inspection run is not completed")
	ErrActionIDsRequired          = errors.New("codex inspection action result ids are required")
	ErrNoActionableResults        = errors.New("codex inspection has no actionable results")
	ErrInvalidActionOverride      = errors.New("codex inspection action override is invalid")
	errCPAAPICallResponseTooLarge = errors.New("CPA api-call response too large")
)

type Service struct {
	store                *store.Store
	managerConfigService *managerconfig.Service
	client               *http.Client
	authFileMutations    *cpaauthfiles.MutationCoordinator
	quotaSnapshots       quotaSnapshotWriter

	mu                             sync.Mutex
	cancelMu                       sync.Mutex
	active                         *localRun
	starting                       bool
	startDone                      chan struct{}
	startCancel                    context.CancelFunc
	auxiliaryRunning               bool
	auxiliaryDone                  chan struct{}
	auxiliaryCancel                context.CancelFunc
	lifecycleOps                   int
	lifecycleDone                  chan struct{}
	stopping                       bool
	ownerID                        string
	leaseDuration                  time.Duration
	heartbeatInterval              time.Duration
	manualActionPersistenceTimeout time.Duration
	logMu                          sync.Mutex
	logGate                        chan struct{}
}

type quotaSnapshotWriter interface {
	WriteCodexInspectionResult(context.Context, model.CodexInspectionResult) error
}

type ServiceOptions struct {
	OwnerID                     string
	LeaseDuration               time.Duration
	HeartbeatInterval           time.Duration
	AuthFileMutationCoordinator *cpaauthfiles.MutationCoordinator
}

var inspectionOwnerSequence atomic.Uint64

type terminationReason string

const (
	terminationNone     terminationReason = ""
	terminationUser     terminationReason = "user_cancel"
	terminationShutdown terminationReason = "service_shutdown"
	terminationLease    terminationReason = "lease_lost"
)

type localRun struct {
	runID             int64
	cancel            context.CancelFunc
	done              chan struct{}
	leaseHeartbeatAt  time.Time
	terminationReason terminationReason
	finalizing        bool
	result            RunDetail
	err               error
}

type RunRequest struct {
	TriggerType string
	TriggerKey  string
}

type RunDetail struct {
	Run     model.CodexInspectionRun      `json:"run"`
	Results []model.CodexInspectionResult `json:"results"`
	Logs    []model.CodexInspectionLog    `json:"logs"`
}

type ExecuteActionsRequest struct {
	ResultIDs       []int64                `json:"resultIds"`
	ActionOverrides []ManualActionOverride `json:"actionOverrides,omitempty"`
}

type ManualActionOverride struct {
	ResultID int64  `json:"resultId"`
	Action   string `json:"action"`
}

type ActionOutcome struct {
	ResultID        int64  `json:"resultId,omitempty"`
	AccountKey      string `json:"accountKey,omitempty"`
	FileName        string `json:"fileName"`
	DisplayAccount  string `json:"displayAccount"`
	Action          string `json:"action"`
	Status          string `json:"status"`
	Success         bool   `json:"success"`
	Error           string `json:"error,omitempty"`
	CurrentDisabled *bool  `json:"-"`
}

type ExecuteActionsResult struct {
	Outcomes []ActionOutcome `json:"outcomes"`
	Detail   RunDetail       `json:"detail"`
}

type authFile map[string]any

type account struct {
	Key                    string
	RuntimeID              string
	FileName               string
	DisplayAccount         string
	AccountSnapshot        string
	AccountSnapshotInvalid bool
	AuthIndex              string
	AccountID              string
	AccountIDInvalid       bool
	Provider               string
	Disabled               bool
	AutoRecoverOwned       bool
	Status                 string
	State                  string
	File                   authFile
}

type apiCallResponse struct {
	StatusCode    int
	HasStatusCode bool
	BodyText      string
	Body          any
}

type inspectionDecision struct {
	Action       string
	ActionReason string
	UsedPercent  *float64
	IsQuota      bool
}

type fileActionGroup struct {
	Key      string
	FileName string
	Items    []model.CodexInspectionResult
	AllItems []model.CodexInspectionResult
	Action   string
	Mixed    bool
}

type sourceFileActionPlan struct {
	CanonicalIdentity string
	Action            string
	Members           []model.CodexInspectionResult
}

type statusMutationTargetScope int

const (
	statusMutationTargetCredential statusMutationTargetScope = iota
	statusMutationTargetSourceFile
	statusMutationTargetExpandedChild
	statusMutationTargetAmbiguous
)

const (
	fileActionDuplicateReason       = "该认证目标已由另一条结果处理"
	fileActionMixedReason           = "同一认证文件下存在多个不同建议动作，文件级处理已阻止，请到凭证管理中手动处理"
	fileDeleteCoverageReason        = "实时认证文件包含未被删除建议完整覆盖的凭证，文件级删除已阻止，请人工确认"
	inspectionIdentityMissingReason = "巡检结果缺少稳定账号标识，已阻止处理，请人工确认"
	statusMutationScopeReason       = "当前凭证缺少可安全独立修改的运行时标识，或该标识代表共享源文件，已阻止状态修改，请人工确认"
)

type codexRateLimit struct {
	Allowed         *bool
	LimitReached    bool
	PrimaryWindow   *codexWindow
	SecondaryWindow *codexWindow
}

type codexWindow struct {
	UsedPercent        *float64
	LimitWindowSeconds *float64
	ResetAfterSeconds  *float64
	ResetAt            *float64
}

type codexClassifiedWindows struct {
	FiveHour    *codexWindow
	Weekly      *codexWindow
	Monthly     *codexWindow
	GenericLong *codexWindow
}

type codexWindowMeta struct {
	ID       string
	LabelKey string
}

type codexAdditionalRateLimitFamily struct {
	sourceIndex        int
	rateInfo           *codexRateLimit
	limitName          string
	modelScope         codexquota.ModelScope
	baseIDPrefix       string
	featureIDPrefix    string
	legacyBaseIDPrefix string
	legacyPrefixes     []string
	idPrefix           string
	sortKey            string
}

func (w codexClassifiedWindows) longWindow() *codexWindow {
	if w.Weekly != nil {
		return w.Weekly
	}
	if w.Monthly != nil {
		return w.Monthly
	}
	return w.GenericLong
}

func (w codexClassifiedWindows) longWindowLabel(window *codexWindow) string {
	switch window {
	case w.Weekly:
		return "周额度"
	case w.Monthly:
		return "月额度"
	case w.GenericLong:
		return "长期额度"
	default:
		return "长期额度"
	}
}

func New(st *store.Store, managerConfigService *managerconfig.Service, clients ...*http.Client) *Service {
	return NewWithOptions(st, managerConfigService, ServiceOptions{}, clients...)
}

func NewWithOptions(st *store.Store, managerConfigService *managerconfig.Service, options ServiceOptions, clients ...*http.Client) *Service {
	client := &http.Client{Timeout: 30 * time.Second}
	if len(clients) > 0 && clients[0] != nil {
		client = clients[0]
	}
	ownerID := strings.TrimSpace(options.OwnerID)
	if ownerID == "" {
		ownerID = inspectionOwnerID()
	}
	leaseDuration := options.LeaseDuration
	if leaseDuration <= 0 {
		leaseDuration = 30 * time.Second
	}
	if leaseDuration < minimumInspectionLease {
		leaseDuration = minimumInspectionLease
	}
	heartbeatInterval := options.HeartbeatInterval
	if heartbeatInterval <= 0 || heartbeatInterval >= leaseDuration {
		heartbeatInterval = leaseDuration / 3
		if heartbeatInterval <= 0 {
			heartbeatInterval = time.Nanosecond
		}
		if heartbeatInterval >= leaseDuration {
			heartbeatInterval = leaseDuration - time.Nanosecond
		}
	}
	if heartbeatInterval <= 0 {
		heartbeatInterval = time.Nanosecond
	}
	authFileMutations := options.AuthFileMutationCoordinator
	if authFileMutations == nil {
		authFileMutations = cpaauthfiles.NewMutationCoordinator()
	}
	return &Service{
		store:                          st,
		managerConfigService:           managerConfigService,
		client:                         client,
		authFileMutations:              authFileMutations,
		quotaSnapshots:                 quotasnapshotsvc.New(st),
		ownerID:                        ownerID,
		leaseDuration:                  leaseDuration,
		heartbeatInterval:              heartbeatInterval,
		manualActionPersistenceTimeout: resultPersistenceTimeout,
		logGate:                        make(chan struct{}, 1),
	}
}

func inspectionOwnerID() string {
	var randomBytes [12]byte
	randomSuffix := "unavailable"
	if _, err := cryptorand.Read(randomBytes[:]); err != nil {
		log.Printf("generate codex inspection lease owner random suffix: %v", err)
	} else {
		randomSuffix = hex.EncodeToString(randomBytes[:])
	}
	host, _ := os.Hostname()
	return fmt.Sprintf(
		"%s:%d:%d:%d:%s",
		strings.TrimSpace(host),
		os.Getpid(),
		time.Now().UnixNano(),
		inspectionOwnerSequence.Add(1),
		randomSuffix,
	)
}

func (s *Service) beginStart(startCancel context.CancelFunc) (chan struct{}, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopping {
		return nil, ErrServiceStopping
	}
	if s.starting || s.active != nil || s.auxiliaryRunning {
		return nil, ErrRunAlreadyActive
	}
	done := make(chan struct{})
	s.starting = true
	s.startDone = done
	s.startCancel = startCancel
	return done, nil
}

func (s *Service) finishStart(done chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.startDone != done {
		return
	}
	s.starting = false
	s.startDone = nil
	s.startCancel = nil
	close(done)
}

func (s *Service) finalizeUnstartedRun(run model.CodexInspectionRun, status, reason string) {
	run.Status = status
	run.Error = reason
	run.FinishedAtMS = time.Now().UnixMilli()
	finalLog := &model.CodexInspectionLog{
		RunID:   run.ID,
		Level:   "warning",
		Message: reason,
		Detail: map[string]any{
			"status": status,
			"reason": "start_aborted",
		},
	}
	finalizeCtx, cancelFinalize := context.WithTimeout(context.Background(), criticalWriteTimeout)
	defer cancelFinalize()
	if err := s.finalizeInspectionRunWithContext(finalizeCtx, run, finalLog); err != nil {
		if fallbackErr := s.forceFinalizeInspectionRunWithContext(finalizeCtx, run, finalLog); fallbackErr != nil {
			log.Printf("finalize unstarted codex inspection run %d: %v (fallback: %v)", run.ID, err, fallbackErr)
		} else {
			log.Printf("finalize unstarted codex inspection run %d via fenced recovery", run.ID)
		}
	}
}

func (s *Service) Run(ctx context.Context, req RunRequest) (RunDetail, error) {
	task, initial, err := s.startRun(ctx, req, false)
	if err != nil {
		return RunDetail{}, err
	}
	if task == nil {
		return initial, nil
	}
	<-task.done
	return task.result, task.err
}

// Start creates a run and returns immediately. The execution context is owned
// by the service, so an HTTP client disconnect cannot silently abandon a run.
func (s *Service) Start(ctx context.Context, req RunRequest) (RunDetail, error) {
	_, initial, err := s.startRun(ctx, req, true)
	return initial, err
}

func (s *Service) startRun(ctx context.Context, req RunRequest, detach bool) (*localRun, RunDetail, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	acquireCtx, cancelAcquire := context.WithCancel(ctx)
	startDone, err := s.beginStart(cancelAcquire)
	if err != nil {
		cancelAcquire()
		return nil, RunDetail{}, err
	}
	defer s.finishStart(startDone)
	defer cancelAcquire()

	triggerType := strings.TrimSpace(req.TriggerType)
	if triggerType == "" {
		triggerType = model.CodexInspectionTriggerManual
	}
	settings, setup, err := s.resolveRuntime(acquireCtx)
	if err != nil {
		return nil, RunDetail{}, err
	}
	if triggerType == model.CodexInspectionTriggerScheduled && (settings.Enabled == nil || !*settings.Enabled) {
		return nil, RunDetail{}, ErrScheduledRunDisabled
	}
	triggerKey := strings.TrimSpace(req.TriggerKey)
	acquired, err := s.store.AcquireCodexInspectionRun(acquireCtx, model.CodexInspectionRun{
		TriggerType:  triggerType,
		TriggerKey:   triggerKey,
		Status:       model.CodexInspectionStatusRunning,
		Settings:     settings,
		SettingsJSON: model.MarshalCodexInspectionSettings(settings),
	}, s.ownerID, s.leaseDuration)
	if err != nil {
		if errors.Is(err, codexinspectionrepo.ErrLeaseAlreadyActive) {
			return nil, RunDetail{}, ErrRunAlreadyActive
		}
		if errors.Is(err, codexinspectionrepo.ErrTriggerAlreadyExists) {
			return nil, RunDetail{}, ErrTriggerAlreadyExists
		}
		return nil, RunDetail{}, err
	}
	run := acquired.Run
	executionCtx := ctx
	if detach {
		executionCtx = context.WithoutCancel(ctx)
	}
	executionCtx, cancel := context.WithCancel(executionCtx)
	leaseHeartbeatAt := time.Now()
	if run.UpdatedAtMS > 0 {
		leaseHeartbeatAt = time.UnixMilli(run.UpdatedAtMS)
	}
	task := &localRun{
		runID:            run.ID,
		cancel:           cancel,
		done:             make(chan struct{}),
		leaseHeartbeatAt: leaseHeartbeatAt,
	}
	s.mu.Lock()
	if s.stopping {
		s.mu.Unlock()
		cancel()
		s.finalizeUnstartedRun(run, model.CodexInspectionStatusInterrupted, "服务关闭导致巡检未能启动")
		return nil, RunDetail{}, ErrServiceStopping
	}
	if s.active != nil || s.auxiliaryRunning {
		s.mu.Unlock()
		cancel()
		s.finalizeUnstartedRun(run, model.CodexInspectionStatusInterrupted, "本地巡检状态冲突，任务未能启动")
		return nil, RunDetail{}, ErrRunAlreadyActive
	}
	s.active = task
	s.mu.Unlock()
	go s.runTask(task, executionCtx, req, run, settings, setup)
	initial := RunDetail{
		Run:     run,
		Results: []model.CodexInspectionResult{},
		Logs:    []model.CodexInspectionLog{},
	}
	initial.Run.Active = true
	initial.Run.Cancellable = true
	return task, initial, nil
}

func (s *Service) executeRun(ctx context.Context, req RunRequest, run model.CodexInspectionRun, settings model.ManagerCodexInspectionConfig, setup store.Setup) (RunDetail, error) {
	persistCtx := context.WithoutCancel(ctx)
	triggerType := run.TriggerType
	triggerKey := run.TriggerKey

	logger := runLogger{service: s, runID: run.ID}
	logger.info(ctx, "凭证健康巡检开始", map[string]any{
		"triggerType": triggerType,
		"triggerKey":  triggerKey,
		"targetTypes": settings.TargetProviders(),
	})

	files, err := s.fetchAuthFiles(ctx, setup)
	if err != nil {
		logger.error(persistCtx, "加载认证文件列表失败", map[string]any{"error": err.Error()})
		if ctx.Err() != nil {
			run.Error = err.Error()
			return RunDetail{Run: run}, err
		}
		return s.failRun(persistCtx, run, err)
	}

	allAccounts := make([]account, 0, len(files))
	for _, file := range files {
		allAccounts = append(allAccounts, toAccount(file))
	}
	s.applyDisableOwnership(ctx, allAccounts, logger)

	accounts := make([]account, 0, len(allAccounts))
	for _, next := range allAccounts {
		if settings.HasTargetProvider(next.Provider) {
			accounts = append(accounts, next)
		}
	}
	probeSetCount := len(accounts)
	sampled := pickSamplePerProvider(accounts, settings.SampleSize)

	run.TotalFiles = len(files)
	run.ProbeSetCount = probeSetCount
	run.SampledCount = len(sampled)
	run.DisabledCount = countAccounts(sampled, true)
	run.EnabledCount = len(sampled) - run.DisabledCount
	progressCtx, cancelProgress := context.WithTimeout(persistCtx, criticalWriteTimeout)
	progressErr := s.store.UpdateCodexInspectionRunProgress(progressCtx, run, s.ownerID)
	cancelProgress()
	if progressErr != nil {
		log.Printf("update codex inspection progress run %d: %v", run.ID, progressErr)
		if errors.Is(progressErr, codexinspectionrepo.ErrLeaseLost) {
			return RunDetail{Run: run}, progressErr
		}
	}

	logger.info(ctx, "凭证健康巡检集合已准备", map[string]any{
		"totalFiles":    len(files),
		"probeSetCount": probeSetCount,
		"sampledCount":  len(sampled),
		"targetTypes":   settings.TargetProviders(),
	})

	results := s.inspectAccounts(ctx, setup, settings, sampled, logger)
	if err := ctx.Err(); err != nil {
		// Persist the partial probe set once, with a bounded budget, before the
		// lifecycle transition below. Avoid a second full pass here: a large
		// cancelled run must still reach cancelled/interrupted promptly.
		resultWriteFailures := s.persistInspectionResults(ctx, run.ID, results, logger)
		run = summarizeRun(run, results)
		// Keep the persisted row active until runTask performs the lifecycle
		// transition and lease release atomically. Synchronous callers still
		// become failed; explicit user/shutdown cancellation gets its own state.
		run.Status = model.CodexInspectionStatusRunning
		run.Error = err.Error()
		if resultWriteFailures > 0 {
			run.Error += fmt.Sprintf("；%d 个巡检结果写入失败，详见巡检日志", resultWriteFailures)
		}
		logger.warning(persistCtx, "凭证健康巡检已取消", map[string]any{"error": run.Error})
		detailCtx, cancelDetail := boundedCancelledInspectionContext(persistCtx)
		detail, detailErr := s.getRunWithResultFallback(detailCtx, run.ID, results, resultWriteFailures > 0)
		cancelDetail()
		if detailErr != nil {
			return RunDetail{}, detailErr
		}
		detail.Run = run
		return detail, nil
	}
	initialResultWriteFailures := s.persistInspectionResults(ctx, run.ID, results, logger)
	if initialResultWriteFailures > 0 {
		log.Printf("persist initial codex inspection results run %d: %d writes failed", run.ID, initialResultWriteFailures)
	}

	results = resolveAutoActionResults(settings.AutoActionMode, results)
	actionOutcomes := s.executeAutoActions(ctx, setup, settings, results, logger)
	actionSummary := summarizeActionOutcomes(actionOutcomes)
	results = applyActionOutcomes(results, actionOutcomes)
	resultWriteFailures := 0
	hasAutoActionMode := model.NormalizeCodexInspectionAutoActionMode(settings.AutoActionMode, model.CodexInspectionAutoActionNone) != model.CodexInspectionAutoActionNone || settings.AutoRecoverEnabled
	if hasAutoActionMode || initialResultWriteFailures > 0 {
		resultWriteFailures = s.persistInspectionResults(ctx, run.ID, results, logger)
	}
	run = summarizeRun(run, results)
	if err := ctx.Err(); err != nil {
		run.Status = model.CodexInspectionStatusRunning
		runErrors := []string{err.Error()}
		if resultWriteFailures > 0 {
			runErrors = append(runErrors, fmt.Sprintf("%d 个巡检结果写入失败，详见巡检日志", resultWriteFailures))
		}
		run.Error = strings.Join(runErrors, "；")
		logger.warning(persistCtx, "凭证健康巡检已取消", map[string]any{
			"error":                  run.Error,
			"actionSuccessCount":     actionSummary.Success,
			"actionFailedCount":      actionSummary.Failed,
			"actionSkippedCount":     actionSummary.Skipped,
			"actionNeedsReviewCount": actionSummary.NeedsReview,
			"resultWriteFailedCount": resultWriteFailures,
		})
		detailCtx, cancelDetail := boundedCancelledInspectionContext(persistCtx)
		detail, detailErr := s.getRunWithResultFallback(detailCtx, run.ID, results, resultWriteFailures > 0)
		cancelDetail()
		if detailErr != nil {
			return RunDetail{}, detailErr
		}
		detail.Run = run
		return detail, nil
	}
	failedActions := actionSummary.Failed
	runErrors := make([]string, 0, 2)
	if failedActions > 0 {
		runErrors = append(runErrors, fmt.Sprintf("%d 个自动处理动作执行失败，详见巡检日志", failedActions))
	}
	if resultWriteFailures > 0 {
		runErrors = append(runErrors, fmt.Sprintf("%d 个巡检结果写入失败，详见巡检日志", resultWriteFailures))
	}
	run.Error = strings.Join(runErrors, "；")
	run.Status = model.CodexInspectionStatusCompleted
	run.FinishedAtMS = time.Now().UnixMilli()
	completionDetail := map[string]any{
		"deleteCount":            run.DeleteCount,
		"disableCount":           run.DisableCount,
		"enableCount":            run.EnableCount,
		"reauthCount":            run.ReauthCount,
		"keepCount":              run.KeepCount,
		"actionSuccessCount":     actionSummary.Success,
		"actionFailedCount":      actionSummary.Failed,
		"actionSkippedCount":     actionSummary.Skipped,
		"actionNeedsReviewCount": actionSummary.NeedsReview,
		"actionErrors":           failedActionOutcomes(actionOutcomes),
		"resultWriteFailedCount": resultWriteFailures,
	}
	if failedActions > 0 || actionSummary.NeedsReview > 0 || resultWriteFailures > 0 {
		logger.warning(persistCtx, "凭证健康巡检完成", completionDetail)
	} else {
		logger.success(persistCtx, "凭证健康巡检完成", completionDetail)
	}
	detail, detailErr := s.getRunWithResultFallback(persistCtx, run.ID, results, resultWriteFailures > 0)
	if detailErr != nil {
		log.Printf("load completed codex inspection run %d before finalization: %v", run.ID, detailErr)
		return RunDetail{Run: run, Results: results}, nil
	}
	detail.Run = run
	return detail, nil
}

func (s *Service) runTask(task *localRun, ctx context.Context, req RunRequest, run model.CodexInspectionRun, settings model.ManagerCodexInspectionConfig, setup store.Setup) {
	heartbeatCtx, stopHeartbeat := context.WithCancel(context.WithoutCancel(ctx))
	heartbeatStopped := make(chan struct{})
	go s.heartbeatRun(task, heartbeatCtx, heartbeatStopped)
	var detail RunDetail
	var runErr error
	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				runErr = fmt.Errorf("codex inspection panic: %v", recovered)
				detail = RunDetail{Run: run}
			}
		}()
		detail, runErr = s.executeRun(ctx, req, run, settings, setup)
	}()
	if errors.Is(runErr, codexinspectionrepo.ErrLeaseLost) {
		s.markLeaseLost(task)
	}
	executionCtxErr := ctx.Err()
	stopHeartbeat()
	<-heartbeatStopped
	// Release the execution context after all probe work has stopped. This is
	// especially important for detached HTTP/scheduler runs, whose parent is
	// intentionally kept alive past the request.
	task.cancel()

	s.mu.Lock()
	task.finalizing = true
	reason := task.terminationReason
	s.mu.Unlock()
	if reason == terminationNone && executionCtxErr != nil {
		// Synchronous callers retain the historical failed-on-context-cancel
		// behavior. Detached HTTP/scheduler runs are cancelled through an
		// explicit reason above and become cancelled/interrupted instead.
		runErr = nil
		reason = terminationNone
	}
	finalRun := run
	readTimeout := criticalWriteTimeout
	if executionCtxErr != nil || reason != terminationNone {
		// Cancellation/shutdown paths should spend only a short read budget before
		// entering the single bounded terminal-write budget below.
		readTimeout = cancelledPersistTimeout
	}
	readCtx, cancelRead := context.WithTimeout(context.Background(), readTimeout)
	persisted, persistedOK, persistedErr := s.store.GetCodexInspectionRun(readCtx, run.ID)
	cancelRead()
	persistedStatus := ""
	persistedError := ""
	if persistedErr == nil && persistedOK {
		finalRun = persisted
		persistedStatus = persisted.Status
		persistedError = persisted.Error
	} else if persistedErr != nil {
		log.Printf("load codex inspection run %d before finalization: %v", run.ID, persistedErr)
	}
	if detail.Run.ID > 0 && (!persistedOK || model.IsCodexInspectionRunActive(finalRun.Status)) {
		// Prefer the in-memory counters while the persisted row is still in an
		// active state. Once another instance has committed a terminal state,
		// keep that fenced result instead of allowing a stale worker snapshot
		// to regress it back to running/failed.
		finalRun = detail.Run
		// Preserve a cancellation transition committed by the API. The in-memory
		// executeRun snapshot can already be completed when the cancellation
		// transaction wins the race immediately before finalization.
		if persistedStatus == model.CodexInspectionStatusCancelling {
			finalRun.Status = persistedStatus
			finalRun.Error = persistedError
		}
	}
	if detail.Run.Error != "" && finalRun.Error == "" {
		finalRun.Error = detail.Run.Error
	}
	userCancellationCommitted := persistedStatus == model.CodexInspectionStatusCancelling &&
		(strings.TrimSpace(persistedError) == userCancelRequestReason || strings.TrimSpace(persistedError) == userCancelledReason)
	if reason == terminationLease {
		finalRun.Status = model.CodexInspectionStatusInterrupted
		finalRun.Error = "巡检任务租约丢失，巡检已中断"
		runErr = nil
	} else if reason == terminationUser || userCancellationCommitted {
		finalRun.Status = model.CodexInspectionStatusCancelled
		finalRun.Error = userCancelledReason
		runErr = nil
	} else if reason == terminationShutdown {
		finalRun.Status = model.CodexInspectionStatusInterrupted
		finalRun.Error = "服务关闭导致巡检已中断"
		runErr = nil
	} else if finalRun.Status == model.CodexInspectionStatusCancelling {
		// A cancellation request can commit its database transition just
		// before this goroutine marks itself finalizing. Treat the persisted
		// cancelling state as authoritative so that race cannot turn a user
		// cancellation into a synthetic failure.
		finalRun.Status = model.CodexInspectionStatusCancelled
		finalRun.Error = userCancelledReason
		runErr = nil
	} else if finalRun.Status == "" || model.IsCodexInspectionRunActive(finalRun.Status) {
		finalRun.Status = model.CodexInspectionStatusFailed
		if finalRun.Error == "" && runErr != nil {
			finalRun.Error = runErr.Error()
		}
	}
	finalRun.FinishedAtMS = time.Now().UnixMilli()
	if finalRun.Error == "" && runErr != nil {
		finalRun.Error = runErr.Error()
	}
	finalRun.Active = false
	finalRun.Cancellable = false
	detail.Run = finalRun
	finalMessage := "凭证健康巡检生命周期已收尾"
	finalLevel := "info"
	switch finalRun.Status {
	case model.CodexInspectionStatusCancelled:
		finalMessage = "凭证健康巡检已取消"
		finalLevel = "warning"
	case model.CodexInspectionStatusInterrupted:
		finalMessage = "凭证健康巡检已中断"
		finalLevel = "warning"
	case model.CodexInspectionStatusFailed:
		finalLevel = "error"
	}
	finalLog := &model.CodexInspectionLog{
		RunID:   run.ID,
		Level:   finalLevel,
		Message: finalMessage,
		Detail: map[string]any{
			"status": finalRun.Status,
			"reason": string(reason),
			"error":  finalRun.Error,
		},
	}
	// Use one bounded budget for the complete terminal transition. Primary,
	// optional-log fallback, fenced recovery, and the post-write read must not
	// each receive a fresh timeout and cumulatively outlive process shutdown.
	finalizeCtx, cancelFinalize := context.WithTimeout(context.Background(), criticalWriteTimeout)
	finalizeErr := s.finalizeInspectionRunWithContext(finalizeCtx, finalRun, finalLog)
	if finalizeErr != nil {
		fallbackErr := s.forceFinalizeInspectionRunWithContext(finalizeCtx, finalRun, finalLog)
		if fallbackErr == nil {
			log.Printf("finalize codex inspection run %d via fenced recovery after primary failure: %v", run.ID, finalizeErr)
			finalizeErr = nil
		} else {
			// Even a fenced/ownership failure must be observable. Another instance
			// may have reclaimed the lease, but if it did not, startup recovery is
			// the only remaining path to repair the active row.
			log.Printf("finalize codex inspection run %d: %v (fallback: %v)", run.ID, finalizeErr, fallbackErr)
			if runErr == nil && !errors.Is(fallbackErr, codexinspectionrepo.ErrLeaseLost) {
				runErr = fallbackErr
			}
		}
	}
	if finalizeErr == nil {
		if finalized, err := s.GetRun(finalizeCtx, run.ID); err == nil {
			if len(detail.Results) > 0 {
				finalized.Results = overlayInspectionResultSnapshots(run.ID, finalized.Results, detail.Results)
			}
			detail = finalized
		} else {
			log.Printf("load finalized codex inspection run %d: %v", run.ID, err)
		}
	} else {
		// The worker is done even when the database could not accept the terminal
		// write. Keep the in-memory result terminal and non-cancellable so callers
		// do not receive a synthetic active task that no goroutine can service.
		results, logs := detail.Results, detail.Logs
		detail = RunDetail{Run: finalRun, Results: results, Logs: logs}
	}
	cancelFinalize()
	task.result = detail
	task.err = runErr
	s.mu.Lock()
	if s.active == task {
		s.active = nil
	}
	s.mu.Unlock()
	close(task.done)
}

// finalizeInspectionRun first attempts the fully atomic terminal update with
// its final lifecycle log. If the log insert itself fails, retry the terminal
// update without that optional log so a logging failure cannot strand the run
// and lease in an active state. Lease ownership errors are returned unchanged:
// another instance may already have fenced this worker.
func (s *Service) finalizeInspectionRun(run model.CodexInspectionRun, finalLog *model.CodexInspectionLog) error {
	finalizeCtx, cancelFinalize := context.WithTimeout(context.Background(), criticalWriteTimeout)
	defer cancelFinalize()
	return s.finalizeInspectionRunWithContext(finalizeCtx, run, finalLog)
}

func (s *Service) finalizeInspectionRunWithContext(ctx context.Context, run model.CodexInspectionRun, finalLog *model.CodexInspectionLog) error {
	if ctx == nil {
		ctx = context.Background()
	}
	err := s.finalizeInspectionRunAttempt(ctx, run, finalLog)
	if err == nil || errors.Is(err, codexinspectionrepo.ErrLeaseLost) || finalLog == nil {
		return err
	}

	log.Printf("final lifecycle log for codex inspection run %d failed: %v; retrying terminal state without log", run.ID, err)
	fallbackErr := s.finalizeInspectionRunAttempt(ctx, run, nil)
	if fallbackErr == nil {
		log.Printf("codex inspection run %d finalized without lifecycle log", run.ID)
		return nil
	}
	return fmt.Errorf("finalize terminal state after lifecycle log failure: %w (initial log error: %v)", fallbackErr, err)
}

func (s *Service) finalizeInspectionRunAttempt(ctx context.Context, run model.CodexInspectionRun, finalLog *model.CodexInspectionLog) error {
	return retryCriticalInspectionWrite(ctx, func() error {
		return s.store.FinalizeCodexInspectionRun(ctx, run, s.ownerID, finalLog)
	})
}

func (s *Service) forceFinalizeInspectionRun(run model.CodexInspectionRun, finalLog *model.CodexInspectionLog) error {
	finalizeCtx, cancelFinalize := context.WithTimeout(context.Background(), criticalWriteTimeout)
	defer cancelFinalize()
	return s.forceFinalizeInspectionRunWithContext(finalizeCtx, run, finalLog)
}

func (s *Service) forceFinalizeInspectionRunWithContext(ctx context.Context, run model.CodexInspectionRun, finalLog *model.CodexInspectionLog) error {
	if ctx == nil {
		ctx = context.Background()
	}
	return retryCriticalInspectionWrite(ctx, func() error {
		return s.store.ForceFinalizeCodexInspectionRun(ctx, run, s.ownerID, finalLog)
	})
}

func retryCriticalInspectionWrite(ctx context.Context, operation func() error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		lastErr = operation()
		if lastErr == nil || !codexinspectionrepo.IsSQLiteBusyError(lastErr) {
			return lastErr
		}
		if attempt == 2 {
			break
		}
		timer := time.NewTimer(time.Duration(1<<attempt) * 20 * time.Millisecond)
		select {
		case <-ctx.Done():
			stopTimer(timer)
			return ctx.Err()
		case <-timer.C:
		}
	}
	return lastErr
}

func boundedInspectionReadContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), criticalWriteTimeout)
}

func boundedCancelledInspectionContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(context.WithoutCancel(ctx), cancelledPersistTimeout)
}

func stopTimer(timer *time.Timer) {
	if timer == nil || timer.Stop() {
		return
	}
	select {
	case <-timer.C:
	default:
	}
}

func (s *Service) heartbeatRun(task *localRun, ctx context.Context, stopped chan<- struct{}) {
	defer close(stopped)
	monitorInterval := s.heartbeatInterval
	if leaseCheckInterval := s.leaseDuration / 4; leaseCheckInterval > 0 && leaseCheckInterval < monitorInterval {
		monitorInterval = leaseCheckInterval
	}
	leaseSafetyMargin := monitorInterval * 2
	if maximumMargin := s.leaseDuration / 2; leaseSafetyMargin > maximumMargin {
		leaseSafetyMargin = maximumMargin
	}
	if leaseSafetyMargin <= 0 {
		leaseSafetyMargin = time.Nanosecond
	}
	ticker := time.NewTicker(monitorInterval)
	defer ticker.Stop()
	lastSuccessfulHeartbeat := task.leaseHeartbeatAt
	if lastSuccessfulHeartbeat.IsZero() {
		lastSuccessfulHeartbeat = time.Now()
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			remainingLease := s.leaseDuration - time.Since(lastSuccessfulHeartbeat)
			// Stop before the database lease can become reclaimable. Without this
			// guard, a slow heartbeat call could run past lease expiry while another
			// instance starts the replacement inspection, briefly executing both.
			if remainingLease <= leaseSafetyMargin {
				s.markLeaseLost(task)
				return
			}
			heartbeatTimeout := s.heartbeatInterval
			if maximumTimeout := remainingLease - leaseSafetyMargin; heartbeatTimeout > maximumTimeout {
				heartbeatTimeout = maximumTimeout
			}
			if heartbeatTimeout <= 0 {
				s.markLeaseLost(task)
				return
			}
			heartbeatStartedAt := time.Now()
			heartbeatCtx, cancel := context.WithTimeout(ctx, heartbeatTimeout)
			err := s.store.HeartbeatCodexInspectionRun(heartbeatCtx, task.runID, s.ownerID, s.leaseDuration)
			callTimedOut := errors.Is(heartbeatCtx.Err(), context.DeadlineExceeded)
			cancel()
			if err == nil {
				// The repository timestamps the lease when SQLite executes the
				// statement, which can be later than this call began. Tracking the
				// call start is conservative and prevents lock wait time from being
				// mistaken for additional lease lifetime.
				lastSuccessfulHeartbeat = heartbeatStartedAt
				continue
			}
			if ctx.Err() != nil {
				return
			}
			if errors.Is(err, codexinspectionrepo.ErrLeaseLost) {
				s.markLeaseLost(task)
				return
			}
			if time.Since(lastSuccessfulHeartbeat) >= s.leaseDuration {
				s.markLeaseLost(task)
				return
			}
			if callTimedOut {
				log.Printf("heartbeat codex inspection run %d timed out; lease has not yet expired", task.runID)
				continue
			}
			log.Printf("heartbeat codex inspection run %d: %v", task.runID, err)
		}
	}
}

func (s *Service) markLeaseLost(task *localRun) {
	s.mu.Lock()
	if s.active == task && task.terminationReason == terminationNone {
		task.terminationReason = terminationLease
	}
	s.mu.Unlock()
	task.cancel()
}

func (s *Service) CancelRun(ctx context.Context, runID int64) (RunDetail, error) {
	operationDone, err := s.beginLifecycleOperation()
	if err != nil {
		return RunDetail{}, err
	}
	defer s.finishLifecycleOperation(operationDone)
	if ctx == nil {
		ctx = context.Background()
	}
	// Once an explicit cancellation request reaches the service, its lifecycle
	// transition must not be abandoned merely because the HTTP client disconnects.
	// Keep it bounded so a stuck database still returns control to shutdown.
	cancelCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), criticalWriteTimeout)
	defer cancel()
	return s.cancelRun(cancelCtx, runID)
}

func (s *Service) cancelRun(ctx context.Context, runID int64) (RunDetail, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.Lock()
	task := s.active
	starting := s.starting
	startDone := s.startDone
	if task == nil || task.runID != runID {
		s.mu.Unlock()
		if task == nil && starting && startDone != nil {
			select {
			case <-startDone:
				// The run either became active or the start attempt was
				// finalized as aborted. Re-evaluate ownership now that the
				// short acquisition window has closed.
				return s.cancelRun(ctx, runID)
			case <-ctx.Done():
				return RunDetail{}, ctx.Err()
			}
		}
		detail, err := s.GetRun(ctx, runID)
		if errors.Is(err, ErrRunNotFound) {
			return RunDetail{}, ErrRunNotFound
		}
		if err != nil {
			return RunDetail{}, err
		}
		if detail.Run.Status == model.CodexInspectionStatusCancelled || detail.Run.Status == model.CodexInspectionStatusCancelling {
			return detail, nil
		}
		if model.IsCodexInspectionRunActive(detail.Run.Status) {
			return RunDetail{}, ErrRunNotOwned
		}
		return RunDetail{}, ErrRunNotCancellable
	}
	s.mu.Unlock()
	return s.cancelOwnedRun(ctx, task, runID)
}

func (s *Service) beginLifecycleOperation() (chan struct{}, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopping {
		return nil, ErrServiceStopping
	}
	if s.lifecycleOps == 0 {
		s.lifecycleDone = make(chan struct{})
	}
	s.lifecycleOps++
	return s.lifecycleDone, nil
}

func (s *Service) finishLifecycleOperation(done chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lifecycleOps <= 0 || s.lifecycleDone != done {
		return
	}
	s.lifecycleOps--
	if s.lifecycleOps == 0 {
		close(done)
		s.lifecycleDone = nil
	}
}

func (s *Service) cancelOwnedRun(ctx context.Context, task *localRun, runID int64) (RunDetail, error) {
	// Serialize cancellation requests without holding the service state mutex
	// across SQLite I/O. This keeps heartbeat, shutdown, and finalization
	// responsive while a busy database is being retried.
	s.cancelMu.Lock()
	defer s.cancelMu.Unlock()

	s.mu.Lock()
	if s.active != task || task.runID != runID {
		s.mu.Unlock()
		return s.cancelRunFromStore(ctx, runID)
	}
	if task.finalizing {
		s.mu.Unlock()
		detail, err := s.getRunForLifecycle(runID)
		if err != nil {
			return RunDetail{}, err
		}
		if detail.Run.Status == model.CodexInspectionStatusCancelling || detail.Run.Status == model.CodexInspectionStatusCancelled {
			return detail, nil
		}
		return RunDetail{}, ErrRunNotCancellable
	}
	if task.terminationReason == terminationUser {
		cancel := task.cancel
		s.mu.Unlock()
		cancel()
		return s.getRunForLifecycle(runID)
	}
	if task.terminationReason != terminationNone {
		s.mu.Unlock()
		return RunDetail{}, ErrRunNotCancellable
	}
	s.mu.Unlock()

	markCtx, cancelMark := context.WithTimeout(ctx, criticalWriteTimeout)
	defer cancelMark()
	changed, err := s.store.MarkCodexInspectionRunCancelling(markCtx, runID, s.ownerID, userCancelRequestReason)

	s.mu.Lock()
	stillOwned := s.active == task && !task.finalizing
	if errors.Is(err, codexinspectionrepo.ErrLeaseLost) {
		if stillOwned {
			task.terminationReason = terminationLease
		}
		cancel := task.cancel
		s.mu.Unlock()
		cancel()
		return RunDetail{}, ErrRunNotOwned
	}
	if err != nil {
		s.mu.Unlock()
		return RunDetail{}, err
	}
	if !stillOwned {
		s.mu.Unlock()
		return s.cancelRunFromStoreWithBound(runID)
	}
	if task.terminationReason != terminationNone {
		s.mu.Unlock()
		return RunDetail{}, ErrRunNotCancellable
	}
	if !changed {
		s.mu.Unlock()
		detail, detailErr := s.cancelRunFromStoreWithBound(runID)
		if detailErr != nil {
			return RunDetail{}, detailErr
		}
		if detail.Run.Status == model.CodexInspectionStatusCancelled {
			return detail, nil
		}
		if detail.Run.Status != model.CodexInspectionStatusCancelling {
			// The worker can commit a terminal state between the ownership check
			// above and the cancelling transition. Do not turn that completed/failed
			// run into a successful cancellation response.
			return RunDetail{}, ErrRunNotCancellable
		}
		// A previous request may already have committed `cancelling` while
		// this local task still owns the lease. Complete the same idempotent
		// cancellation locally instead of returning a spurious conflict.
		s.mu.Lock()
		if s.active == task && !task.finalizing && task.terminationReason == terminationNone {
			task.terminationReason = terminationUser
			cancel := task.cancel
			s.mu.Unlock()
			cancel()
			return detail, nil
		}
		s.mu.Unlock()
		return detail, nil
	}
	task.terminationReason = terminationUser
	cancel := task.cancel
	cancel()
	s.mu.Unlock()
	detail, err := s.getRunForLifecycle(runID)
	if err != nil {
		return RunDetail{}, err
	}
	return detail, nil
}

func (s *Service) cancelRunFromStore(ctx context.Context, runID int64) (RunDetail, error) {
	detail, err := s.GetRun(ctx, runID)
	if err != nil {
		return RunDetail{}, err
	}
	if detail.Run.Status == model.CodexInspectionStatusCancelled || detail.Run.Status == model.CodexInspectionStatusCancelling {
		return detail, nil
	}
	if model.IsCodexInspectionRunActive(detail.Run.Status) {
		return RunDetail{}, ErrRunNotOwned
	}
	return RunDetail{}, ErrRunNotCancellable
}

func (s *Service) cancelRunFromStoreWithBound(runID int64) (RunDetail, error) {
	readCtx, cancelRead := boundedInspectionReadContext()
	defer cancelRead()
	return s.cancelRunFromStore(readCtx, runID)
}

func (s *Service) getRunForLifecycle(runID int64) (RunDetail, error) {
	readCtx, cancelRead := boundedInspectionReadContext()
	defer cancelRead()
	return s.GetRun(readCtx, runID)
}

func (s *Service) Recover(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	_, err := s.store.RecoverStaleCodexInspectionRuns(ctx, time.Now().UnixMilli(), "服务重启或任务租约过期，巡检已中断")
	return err
}

func (s *Service) StopAndWait(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	var task *localRun
	for {
		s.mu.Lock()
		s.stopping = true
		startDone := s.startDone
		startCancel := s.startCancel
		auxiliaryDone := s.auxiliaryDone
		auxiliaryCancel := s.auxiliaryCancel
		lifecycleDone := s.lifecycleDone
		task = s.active
		if task != nil && !task.finalizing && task.terminationReason == terminationNone {
			task.terminationReason = terminationShutdown
		}
		var taskCancel context.CancelFunc
		if task != nil && !task.finalizing {
			taskCancel = task.cancel
		}
		s.mu.Unlock()
		if startCancel != nil {
			startCancel()
		}
		if auxiliaryCancel != nil {
			auxiliaryCancel()
		}
		if taskCancel != nil {
			taskCancel()
		}
		if startDone == nil {
			if auxiliaryDone != nil {
				select {
				case <-auxiliaryDone:
				case <-ctx.Done():
					return ctx.Err()
				}
				continue
			}
			if lifecycleDone != nil {
				select {
				case <-lifecycleDone:
				case <-ctx.Done():
					return ctx.Err()
				}
				continue
			}
			break
		}
		select {
		case <-startDone:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if task == nil {
		return nil
	}
	task.cancel()
	select {
	case <-task.done:
		return nil
	case <-ctx.Done():
		log.Printf("timed out waiting for codex inspection run %d to stop: %v", task.runID, ctx.Err())
		return ctx.Err()
	}
}

func (s *Service) ActiveRunID() (int64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active == nil {
		return 0, false
	}
	return s.active.runID, true
}

func (s *Service) IsStopping() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stopping
}

func (s *Service) localRunCancellable(runID int64, status string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active == nil || s.active.runID != runID {
		return false
	}
	if s.active.finalizing {
		// A committed cancellation remains idempotently cancellable while the
		// worker is performing its final database transaction. This keeps the UI
		// on the disabled "cancelling" action instead of hiding it mid-transition.
		return status == model.CodexInspectionStatusCancelling
	}
	return s.active.terminationReason == terminationNone || s.active.terminationReason == terminationUser
}

func (s *Service) ListRuns(ctx context.Context, limit int) ([]model.CodexInspectionRun, error) {
	runs, err := s.store.ListCodexInspectionRuns(ctx, limit)
	if err != nil {
		return nil, err
	}
	lease, active, err := s.store.GetActiveCodexInspectionLease(ctx, time.Now().UnixMilli())
	if err != nil {
		return nil, err
	}
	for index := range runs {
		runs[index].Active = active && lease.RunID == runs[index].ID && model.IsCodexInspectionRunActive(runs[index].Status)
		runs[index].Cancellable = runs[index].Active && lease.OwnerID == s.ownerID && s.localRunCancellable(runs[index].ID, runs[index].Status)
	}
	return runs, nil
}

func (s *Service) GetRun(ctx context.Context, id int64) (RunDetail, error) {
	run, ok, err := s.store.GetCodexInspectionRun(ctx, id)
	if err != nil {
		return RunDetail{}, err
	}
	if !ok {
		return RunDetail{}, ErrRunNotFound
	}
	lease, active, err := s.store.GetActiveCodexInspectionLease(ctx, time.Now().UnixMilli())
	if err != nil {
		return RunDetail{}, err
	}
	run.Active = active && lease.RunID == run.ID && model.IsCodexInspectionRunActive(run.Status)
	run.Cancellable = run.Active && lease.OwnerID == s.ownerID && s.localRunCancellable(run.ID, run.Status)
	results, err := s.store.ListCodexInspectionResults(ctx, id)
	if err != nil {
		return RunDetail{}, err
	}
	logs, err := s.store.ListCodexInspectionLogs(ctx, id)
	if err != nil {
		return RunDetail{}, err
	}
	return RunDetail{Run: run, Results: results, Logs: logs}, nil
}

func (s *Service) ExecuteManualActions(ctx context.Context, runID int64, req ExecuteActionsRequest) (ExecuteActionsResult, error) {
	operationCtx, err := s.acquireAuxiliaryRun(ctx)
	if err != nil {
		return ExecuteActionsResult{}, err
	}
	defer s.releaseRun()
	ctx = operationCtx

	if len(req.ResultIDs) == 0 {
		return ExecuteActionsResult{}, ErrActionIDsRequired
	}

	settings, setup, err := s.resolveRuntime(ctx)
	if err != nil {
		return ExecuteActionsResult{}, err
	}
	detail, err := s.GetRun(ctx, runID)
	if err != nil {
		return ExecuteActionsResult{}, err
	}
	if detail.Run.Status != model.CodexInspectionStatusCompleted {
		return ExecuteActionsResult{}, ErrRunNotCompleted
	}
	if len(detail.Run.Settings.TargetProviders()) > 0 {
		settings = detail.Run.Settings
	}

	selected := map[int64]struct{}{}
	for _, id := range req.ResultIDs {
		if id > 0 {
			selected[id] = struct{}{}
		}
	}
	if len(selected) == 0 {
		return ExecuteActionsResult{}, ErrActionIDsRequired
	}

	manualResults, err := applyManualActionOverrides(detail.Results, selected, req.ActionOverrides)
	if err != nil {
		return ExecuteActionsResult{}, err
	}
	items, preflightOutcomes := selectManualActionItems(manualResults, selected)
	if len(items) == 0 && len(preflightOutcomes) == 0 {
		return ExecuteActionsResult{}, ErrNoActionableResults
	}
	authorizedResults := selectInspectionResultsByID(manualResults, selected)

	logCtx := context.WithoutCancel(ctx)
	logger := runLogger{service: s, runID: detail.Run.ID}
	logger.info(logCtx, "手动处理账号开始", map[string]any{
		"requestedCount": len(req.ResultIDs),
		"actionCount":    len(items),
	})
	logPreflightActionOutcomes(logCtx, logger, "手动处理", preflightOutcomes)

	validItems, sourceFileMembers, validationOutcomes, err := s.validateActionItems(
		ctx,
		logCtx,
		setup,
		items,
		authorizedResults,
		logger,
		"手动处理",
		func(item model.CodexInspectionResult) string { return item.Action },
	)
	if err != nil {
		return ExecuteActionsResult{}, err
	}
	outcomes := make([]ActionOutcome, 0, len(preflightOutcomes)+len(validationOutcomes)+len(validItems))
	outcomes = append(outcomes, preflightOutcomes...)
	outcomes = append(outcomes, validationOutcomes...)
	outcomes = append(outcomes, s.executeActionItems(ctx, setup, settings, validItems, sourceFileMembers, logger, "手动处理", false, func(item model.CodexInspectionResult) string {
		return item.Action
	})...)
	if len(outcomes) == 0 {
		return ExecuteActionsResult{}, ErrNoActionableResults
	}
	nextResults := applyActionOutcomes(detail.Results, outcomes)
	// Start the bounded persistence budget only after every external action has
	// finished. Slow CPA requests must not consume the time reserved for writing
	// the successful outcomes and updated run state.
	persistenceTimeout := s.manualActionPersistenceTimeout
	if persistenceTimeout <= 0 {
		persistenceTimeout = resultPersistenceTimeout
	}
	persistCtx, cancelPersist := context.WithTimeout(context.WithoutCancel(ctx), persistenceTimeout)
	defer cancelPersist()
	resultWriteFailures := s.persistInspectionResults(persistCtx, detail.Run.ID, nextResults, logger)

	run := summarizeRun(detail.Run, nextResults)
	outcomeSummary := summarizeActionOutcomes(outcomes)
	failedActions := outcomeSummary.Failed
	runErrors := make([]string, 0, 2)
	if failedActions > 0 {
		runErrors = append(runErrors, fmt.Sprintf("%d 个手动处理动作执行失败，详见巡检日志", failedActions))
	}
	if resultWriteFailures > 0 {
		runErrors = append(runErrors, fmt.Sprintf("%d 个巡检结果写入失败，详见巡检日志", resultWriteFailures))
	}
	run.Error = strings.Join(runErrors, "；")
	if err := s.store.UpdateCodexInspectionRun(persistCtx, run); err != nil {
		return ExecuteActionsResult{}, err
	}
	completionDetail := map[string]any{
		"successCount":           outcomeSummary.Success,
		"failedCount":            outcomeSummary.Failed,
		"skippedCount":           outcomeSummary.Skipped,
		"needsReviewCount":       outcomeSummary.NeedsReview,
		"resultWriteFailedCount": resultWriteFailures,
	}
	if failedActions > 0 || outcomeSummary.NeedsReview > 0 || resultWriteFailures > 0 {
		logger.warning(persistCtx, "手动处理账号完成", completionDetail)
	} else {
		logger.success(persistCtx, "手动处理账号完成", completionDetail)
	}

	nextDetail, err := s.getRunWithResultFallback(
		persistCtx,
		detail.Run.ID,
		nextResults,
		resultWriteFailures > 0,
	)
	if err != nil {
		return ExecuteActionsResult{}, err
	}
	return ExecuteActionsResult{Outcomes: outcomes, Detail: nextDetail}, nil
}

func selectInspectionResultsByID(
	results []model.CodexInspectionResult,
	selected map[int64]struct{},
) []model.CodexInspectionResult {
	selectedResults := make([]model.CodexInspectionResult, 0, len(selected))
	for _, result := range results {
		if _, ok := selected[result.ID]; ok {
			selectedResults = append(selectedResults, result)
		}
	}
	return selectedResults
}

func applyManualActionOverrides(
	results []model.CodexInspectionResult,
	selected map[int64]struct{},
	overrides []ManualActionOverride,
) ([]model.CodexInspectionResult, error) {
	if len(overrides) == 0 {
		return results, nil
	}
	overrideByID := make(map[int64]string, len(overrides))
	for _, override := range overrides {
		action := strings.ToLower(strings.TrimSpace(override.Action))
		if override.ResultID <= 0 || action != "delete" {
			return nil, ErrInvalidActionOverride
		}
		if _, ok := selected[override.ResultID]; !ok {
			return nil, ErrInvalidActionOverride
		}
		if existing, ok := overrideByID[override.ResultID]; ok && existing != action {
			return nil, ErrInvalidActionOverride
		}
		overrideByID[override.ResultID] = action
	}

	out := make([]model.CodexInspectionResult, len(results))
	copy(out, results)
	matched := make(map[int64]struct{}, len(overrideByID))
	for index := range out {
		action, ok := overrideByID[out[index].ID]
		if !ok {
			continue
		}
		if out[index].Action != "reauth" || action != "delete" {
			return nil, ErrInvalidActionOverride
		}
		out[index].Action = "delete"
		matched[out[index].ID] = struct{}{}
	}
	if len(matched) != len(overrideByID) {
		return nil, ErrInvalidActionOverride
	}
	return out, nil
}

func (s *Service) ResolveConfig(ctx context.Context) (model.ManagerCodexInspectionConfig, bool, error) {
	if s.managerConfigService == nil {
		return model.DefaultCodexInspectionConfig(), false, nil
	}
	managerCfg, _, ok, err := s.managerConfigService.ResolveManagerConfigWithSource(ctx)
	if err != nil {
		return model.ManagerCodexInspectionConfig{}, false, err
	}
	if !ok || strings.TrimSpace(managerCfg.CPAConnection.CPABaseURL) == "" ||
		strings.TrimSpace(managerCfg.CPAConnection.ManagementKey) == "" {
		return model.DefaultCodexInspectionConfig(), false, nil
	}
	return model.NormalizeCodexInspectionConfig(
		managerCfg.CodexInspection,
		model.DefaultCodexInspectionConfig(),
	), true, nil
}

func (s *Service) acquireAuxiliaryRun(ctx context.Context) (context.Context, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	operationCtx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopping {
		cancel()
		return nil, ErrServiceStopping
	}
	if s.starting || s.active != nil || s.auxiliaryRunning {
		cancel()
		return nil, ErrRunAlreadyActive
	}
	s.auxiliaryRunning = true
	s.auxiliaryDone = make(chan struct{})
	s.auxiliaryCancel = cancel
	return operationCtx, nil
}

func (s *Service) releaseRun() {
	s.mu.Lock()
	done := s.auxiliaryDone
	cancel := s.auxiliaryCancel
	s.auxiliaryDone = nil
	s.auxiliaryCancel = nil
	s.auxiliaryRunning = false
	if done != nil {
		close(done)
	}
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (s *Service) resolveRuntime(ctx context.Context) (model.ManagerCodexInspectionConfig, store.Setup, error) {
	if s.managerConfigService == nil {
		return model.ManagerCodexInspectionConfig{}, store.Setup{}, ErrNotConfigured
	}
	managerCfg, _, ok, err := s.managerConfigService.ResolveManagerConfigWithSource(ctx)
	if err != nil {
		return model.ManagerCodexInspectionConfig{}, store.Setup{}, err
	}
	if !ok || strings.TrimSpace(managerCfg.CPAConnection.CPABaseURL) == "" ||
		strings.TrimSpace(managerCfg.CPAConnection.ManagementKey) == "" {
		return model.ManagerCodexInspectionConfig{}, store.Setup{}, ErrNotConfigured
	}
	settings := model.NormalizeCodexInspectionConfig(
		managerCfg.CodexInspection,
		model.DefaultCodexInspectionConfig(),
	)
	return settings, managerconfig.SetupFromManagerConfig(managerCfg), nil
}

func (s *Service) failRun(ctx context.Context, run model.CodexInspectionRun, cause error) (RunDetail, error) {
	run.Status = model.CodexInspectionStatusFailed
	run.Error = cause.Error()
	run.FinishedAtMS = time.Now().UnixMilli()
	detail, err := s.GetRun(ctx, run.ID)
	if err != nil {
		return RunDetail{Run: run}, cause
	}
	detail.Run = run
	return detail, cause
}

func (s *Service) fetchAuthFiles(ctx context.Context, setup store.Setup) ([]authFile, error) {
	files, err := cpaauthfiles.New(s.client).Fetch(ctx, setup.CPAUpstreamURL, setup.ManagementKey)
	if err != nil {
		return nil, err
	}
	result := make([]authFile, 0, len(files))
	for _, file := range files {
		result = append(result, authFile(file.Raw))
	}
	return result, nil
}

func (s *Service) inspectAccounts(
	ctx context.Context,
	setup store.Setup,
	settings model.ManagerCodexInspectionConfig,
	accounts []account,
	logger runLogger,
) []model.CodexInspectionResult {
	if len(accounts) == 0 {
		return nil
	}
	workers := settings.Workers
	if workers <= 0 {
		workers = 1
	}

	jobs := make(chan account)
	results := make(chan model.CodexInspectionResult, len(accounts))
	var wg sync.WaitGroup
	for i := 0; i < workers && i < len(accounts); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for item := range jobs {
				results <- s.inspectSingleAccount(ctx, setup, settings, item, logger)
			}
		}()
	}

	go func() {
		defer close(jobs)
		for _, item := range accounts {
			select {
			case <-ctx.Done():
				return
			case jobs <- item:
			}
		}
	}()

	wg.Wait()
	close(results)

	out := make([]model.CodexInspectionResult, 0, len(accounts))
	for result := range results {
		out = append(out, result)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].FileName == out[j].FileName {
			return out[i].DisplayAccount < out[j].DisplayAccount
		}
		return out[i].FileName < out[j].FileName
	})
	return out
}

func (s *Service) inspectSingleAccount(
	ctx context.Context,
	setup store.Setup,
	settings model.ManagerCodexInspectionConfig,
	item account,
	logger runLogger,
) model.CodexInspectionResult {
	if item.Provider == "xai" {
		return s.inspectSingleXAIAccount(ctx, setup, settings, item, logger)
	}
	base := resultFromAccount(item)
	if item.AuthIndex == "" {
		base.Action = "keep"
		base.ActionReason = "缺少 auth_index，保留账号"
		base.Error = "缺少 auth_index"
		base.ErrorKind = "missing_auth_index"
		base.ErrorDetail = "缺少 auth_index"
		logger.warning(ctx, "账号缺少 auth_index，跳过探测", map[string]any{
			"fileName":       item.FileName,
			"displayAccount": item.DisplayAccount,
		})
		return base
	}

	var response apiCallResponse
	var err error
	for attempt := 0; attempt <= settings.Retries; attempt++ {
		response, err = s.requestCodexUsage(ctx, setup, settings, item)
		if err == nil {
			break
		}
	}
	if err != nil {
		base.Action = "keep"
		base.ActionReason = "探测异常，保留账号"
		base.Error = truncate(err.Error(), maxStoredBodyText)
		base.ErrorKind = "request_error"
		base.ErrorDetail = truncate(err.Error(), maxStoredBodyText)
		logger.warning(ctx, "账号探测异常，保留账号", map[string]any{
			"fileName":       item.FileName,
			"displayAccount": item.DisplayAccount,
			"error":          err.Error(),
		})
		return base
	}
	if !response.HasStatusCode {
		base.Action = "keep"
		base.ActionReason = "探测响应缺少 status_code，保留账号"
		base.Error = "响应缺少 status_code"
		base.ErrorKind = "missing_status"
		base.ErrorDetail = firstNonEmpty(truncate(response.BodyText, maxStoredBodyText), "响应缺少 status_code")
		logger.warning(ctx, "账号探测未返回 status_code，保留账号", map[string]any{
			"fileName":       item.FileName,
			"displayAccount": item.DisplayAccount,
			"body":           truncate(response.BodyText, maxStoredBodyText),
		})
		return base
	}

	statusCode := response.StatusCode
	base.StatusCode = &statusCode
	payload := parseRecord(response.Body)
	if payload == nil {
		payload = parseRecord(response.BodyText)
	}
	planType := normalizeCodexPlanType(readString(payload, "plan_type", "planType"))
	if planType == "" {
		planType = resolveCodexPlanType(item.File)
	}
	rateLimit := parseRateLimit(readMap(payload, "rate_limit", "rateLimit"))
	usedPercent := deriveRateLimitUsedPercent(rateLimit)
	bodyLower := strings.ToLower(response.BodyText)
	isQuota := statusCode == http.StatusPaymentRequired ||
		strings.Contains(bodyLower, "quota exhausted") ||
		strings.Contains(bodyLower, "limit reached") ||
		strings.Contains(bodyLower, "payment_required") ||
		isRateLimitReached(rateLimit) ||
		(usedPercent != nil && *usedPercent >= settings.UsedPercentThreshold)
	decision := resolveProbeAction(item, statusCode, response.BodyText, rateLimit, usedPercent, isQuota, settings.UsedPercentThreshold, planType)

	base.Action = decision.Action
	base.ActionReason = decision.ActionReason
	base.UsedPercent = decision.UsedPercent
	base.IsQuota = decision.IsQuota
	base.AutoRecoverEligible = decision.Action == "enable" && item.AutoRecoverOwned
	if decision.Action == "enable" && !base.AutoRecoverEligible {
		base.ActionReason += "；禁用来源不受巡检管理，仅允许手动启用"
	}
	base.PlanType = planType
	base.QuotaWindows = buildCodexInspectionQuotaWindows(payload, planType)
	base.QuotaInventoryObserved = codexQuotaInventoryObserved(payload)
	if base.QuotaInventoryObserved && len(base.QuotaWindows) == 0 {
		// Preserve the distinction between an explicitly observed empty inventory
		// and a successful response whose quota schema could not be recognized.
		base.QuotaWindowsJSON = "[]"
	}
	base.Error = ""
	if statusCode < 200 || statusCode >= 300 {
		base.ErrorKind = "http_status"
		base.ErrorDetail = firstNonEmpty(truncate(response.BodyText, maxStoredBodyText), fmt.Sprintf("HTTP %d", statusCode))
	}

	level := "info"
	switch decision.Action {
	case "delete", "reauth":
		level = "error"
	case "disable":
		level = "warning"
	case "enable":
		level = "success"
	}
	logger.log(ctx, level, "账号探测完成", map[string]any{
		"fileName":       item.FileName,
		"displayAccount": item.DisplayAccount,
		"action":         decision.Action,
		"statusCode":     statusCode,
		"usedPercent":    nullableFloat(decision.UsedPercent),
		"isQuota":        decision.IsQuota,
	})
	return base
}

func (s *Service) requestCodexUsage(
	ctx context.Context,
	setup store.Setup,
	settings model.ManagerCodexInspectionConfig,
	item account,
) (apiCallResponse, error) {
	result, _, err := s.requestCodexUsageAt(ctx, setup, settings, item, "/v0/management/api-call")
	return result, err
}

func (s *Service) requestCodexUsageAt(
	ctx context.Context,
	setup store.Setup,
	settings model.ManagerCodexInspectionConfig,
	item account,
	path string,
) (apiCallResponse, int, error) {
	headers := map[string]string{
		"Authorization": "Bearer $TOKEN$",
		"Content-Type":  "application/json",
		"User-Agent":    settings.UserAgent,
	}
	if strings.TrimSpace(item.AccountID) != "" {
		headers["Chatgpt-Account-Id"] = strings.TrimSpace(item.AccountID)
	}
	payload := map[string]any{
		"authIndex": item.AuthIndex,
		"method":    http.MethodGet,
		"url":       codexUsageURL,
		"header":    headers,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return apiCallResponse{}, 0, err
	}
	requestCtx := ctx
	cancel := func() {}
	if settings.Timeout > 0 {
		requestCtx, cancel = context.WithTimeout(ctx, time.Duration(settings.Timeout)*time.Millisecond)
	}
	defer cancel()

	req, err := http.NewRequestWithContext(
		requestCtx,
		http.MethodPost,
		cpa.NormalizeBaseURL(setup.CPAUpstreamURL)+path,
		bytes.NewReader(data),
	)
	if err != nil {
		return apiCallResponse{}, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+setup.ManagementKey)
	req.Header.Set("Content-Type", "application/json")
	res, err := s.client.Do(req)
	if err != nil {
		return apiCallResponse{}, 0, err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(res.Body, maxStoredBodyText))
		return apiCallResponse{}, res.StatusCode, fmt.Errorf("api-call failed: %s %s", res.Status, truncate(string(body), maxStoredBodyText))
	}

	var raw map[string]any
	if err := decodeCPAAPICallResponse(res.Body, maxCPAAPICallResponseSize, &raw); err != nil {
		return apiCallResponse{}, res.StatusCode, err
	}
	statusRaw, hasStatus := firstValue(raw, "status_code", "statusCode")
	statusCode := int(readFloat(statusRaw, 0))
	bodyRaw, _ := firstValue(raw, "body")
	bodyText, bodyValue := normalizeBody(bodyRaw)
	return apiCallResponse{
		StatusCode:    statusCode,
		HasStatusCode: hasStatus && strings.TrimSpace(fmt.Sprint(statusRaw)) != "",
		BodyText:      bodyText,
		Body:          bodyValue,
	}, res.StatusCode, nil
}

func decodeCPAAPICallResponse(body io.Reader, maxBytes int64, target any) error {
	if body == nil {
		return io.EOF
	}
	if maxBytes <= 0 {
		return errors.New("CPA api-call response size limit must be positive")
	}
	limited := &io.LimitedReader{R: body, N: maxBytes + 1}
	decoder := json.NewDecoder(limited)
	if err := decoder.Decode(target); err != nil {
		if limited.N == 0 {
			return fmt.Errorf("%w: exceeds %d bytes", errCPAAPICallResponseTooLarge, maxBytes)
		}
		return err
	}
	if limited.N == 0 {
		return fmt.Errorf("%w: exceeds %d bytes", errCPAAPICallResponseTooLarge, maxBytes)
	}
	var trailing any
	trailingErr := decoder.Decode(&trailing)
	if limited.N == 0 {
		return fmt.Errorf("%w: exceeds %d bytes", errCPAAPICallResponseTooLarge, maxBytes)
	}
	if errors.Is(trailingErr, io.EOF) {
		return nil
	}
	if trailingErr == nil {
		return errors.New("api-call response contains multiple JSON values")
	}
	return fmt.Errorf("decode api-call response trailing data: %w", trailingErr)
}

func (s *Service) executeAutoActions(
	ctx context.Context,
	setup store.Setup,
	settings model.ManagerCodexInspectionConfig,
	results []model.CodexInspectionResult,
	logger runLogger,
) []ActionOutcome {
	mode := model.NormalizeCodexInspectionAutoActionMode(settings.AutoActionMode, model.CodexInspectionAutoActionNone)
	if mode == model.CodexInspectionAutoActionNone && !settings.AutoRecoverEnabled {
		return nil
	}
	items, preflightOutcomes := selectAutoActionItems(mode, settings.AutoRecoverEnabled, results)
	logCtx := context.WithoutCancel(ctx)
	requestedCount := len(items) + len(preflightOutcomes)
	if requestedCount == 0 {
		requestedCount = countSuggestedActionResults(results)
	}
	if requestedCount == 0 {
		return nil
	}
	logger.info(logCtx, "自动处理账号开始", map[string]any{
		"requestedCount": requestedCount,
		"actionCount":    len(items),
	})
	logPreflightActionOutcomes(logCtx, logger, "自动处理", preflightOutcomes)
	actionFor := func(item model.CodexInspectionResult) string {
		return resolveExecutableAction(mode, item.Action)
	}
	validItems, sourceFileMembers, validationOutcomes, validationErr := s.validateActionItems(
		ctx,
		logCtx,
		setup,
		items,
		results,
		logger,
		"自动处理",
		actionFor,
	)
	if validationErr != nil {
		validationOutcomes = completeCanceledActionOutcomes(
			items,
			validationOutcomes,
			actionFor,
			validationErr,
			logger,
			logCtx,
			"自动处理",
		)
		validItems = nil
		sourceFileMembers = nil
	}
	outcomes := make([]ActionOutcome, 0, len(preflightOutcomes)+len(validationOutcomes)+len(validItems))
	outcomes = append(outcomes, preflightOutcomes...)
	outcomes = append(outcomes, validationOutcomes...)
	if len(validItems) > 0 {
		outcomes = append(outcomes, s.executeActionItems(ctx, setup, settings, validItems, sourceFileMembers, logger, "自动处理", true, actionFor)...)
	}
	summary := summarizeActionOutcomes(outcomes)
	remainingCount := countPendingActionResults(results, outcomes)
	completionDetail := map[string]any{
		"successCount":     summary.Success,
		"failedCount":      summary.Failed,
		"skippedCount":     summary.Skipped,
		"needsReviewCount": summary.NeedsReview,
		"remainingCount":   remainingCount,
	}
	if summary.Failed > 0 || summary.NeedsReview > 0 || remainingCount > 0 {
		logger.warning(logCtx, "自动处理账号完成", completionDetail)
	} else {
		logger.success(logCtx, "自动处理账号完成", completionDetail)
	}
	return outcomes
}

func countSuggestedActionResults(results []model.CodexInspectionResult) int {
	count := 0
	for _, result := range results {
		if result.Action != "" && result.Action != "keep" {
			count++
		}
	}
	return count
}

func countPendingActionResults(results []model.CodexInspectionResult, outcomes []ActionOutcome) int {
	terminal := make(map[string]struct{}, len(outcomes))
	for _, outcome := range outcomes {
		switch outcome.Status {
		case model.CodexInspectionActionStatusSuccess,
			model.CodexInspectionActionStatusSkipped,
			model.CodexInspectionActionStatusNeedsReview:
			terminal[outcome.AccountKey] = struct{}{}
		}
	}
	count := 0
	for _, result := range results {
		if result.Action == "" || result.Action == "keep" {
			continue
		}
		if _, ok := terminal[result.AccountKey]; ok {
			continue
		}
		count++
	}
	return count
}

func (s *Service) executeActionItems(
	ctx context.Context,
	setup store.Setup,
	settings model.ManagerCodexInspectionConfig,
	items []model.CodexInspectionResult,
	sourceFileMembers map[string][]model.CodexInspectionResult,
	logger runLogger,
	logPrefix string,
	automatic bool,
	actionFor func(model.CodexInspectionResult) string,
) []ActionOutcome {
	logCtx := context.WithoutCancel(ctx)
	workers := settings.DeleteWorkers
	if workers <= 0 {
		workers = 1
	}
	jobs := make(chan model.CodexInspectionResult)
	outcomes := make(chan ActionOutcome, len(items))
	fileLocks := make(map[string]*sync.Mutex, len(items))
	for _, item := range items {
		fileName := strings.TrimSpace(item.FileName)
		if fileName != "" && fileLocks[fileName] == nil {
			fileLocks[fileName] = &sync.Mutex{}
		}
	}
	var wg sync.WaitGroup
	for i := 0; i < workers && i < len(items); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case item, ok := <-jobs:
					if !ok {
						return
					}
					action := item.Action
					if actionFor != nil {
						action = actionFor(item)
					}
					if action == "" {
						action = item.Action
					}
					actionItem := item
					actionItem.Action = action
					outcome := ActionOutcome{
						ResultID:        item.ID,
						AccountKey:      item.AccountKey,
						FileName:        item.FileName,
						DisplayAccount:  item.DisplayAccount,
						Action:          action,
						CurrentDisabled: boolPointer(item.Disabled),
					}
					sourceMembers := sourceFileMembers[inspectionActionIdentityKey(item)]
					fileLock := fileLocks[strings.TrimSpace(item.FileName)]
					executeErr := func() error {
						if fileLock != nil {
							fileLock.Lock()
							defer fileLock.Unlock()
						}
						return s.executeAction(ctx, setup, actionItem, sourceMembers, automatic)
					}()
					if executeErr != nil {
						outcome.Success = false
						outcome.Status = model.CodexInspectionActionStatusFailed
						outcome.Error = executeErr.Error()
						outcomes <- outcome
						logger.error(logCtx, logPrefix+"账号失败", map[string]any{
							"fileName":       item.FileName,
							"displayAccount": item.DisplayAccount,
							"action":         action,
							"error":          executeErr.Error(),
						})
						continue
					}
					outcome.Success = true
					outcome.Status = model.CodexInspectionActionStatusSuccess
					outcomes <- outcome
					logger.success(logCtx, logPrefix+"账号成功", map[string]any{
						"fileName":       item.FileName,
						"displayAccount": item.DisplayAccount,
						"action":         action,
					})
				}
			}
		}()
	}
	for _, item := range items {
		select {
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			close(outcomes)
			return completeCanceledActionOutcomes(
				items,
				collectActionOutcomes(outcomes, len(items)),
				actionFor,
				ctx.Err(),
				logger,
				logCtx,
				logPrefix,
			)
		case jobs <- item:
		}
	}
	close(jobs)
	wg.Wait()
	close(outcomes)

	return completeCanceledActionOutcomes(
		items,
		collectActionOutcomes(outcomes, len(items)),
		actionFor,
		ctx.Err(),
		logger,
		logCtx,
		logPrefix,
	)
}

func collectActionOutcomes(outcomes <-chan ActionOutcome, capacity int) []ActionOutcome {
	result := make([]ActionOutcome, 0, capacity)
	for outcome := range outcomes {
		result = append(result, outcome)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].FileName == result[j].FileName {
			return result[i].Action < result[j].Action
		}
		return result[i].FileName < result[j].FileName
	})
	return result
}

func completeCanceledActionOutcomes(
	items []model.CodexInspectionResult,
	outcomes []ActionOutcome,
	actionFor func(model.CodexInspectionResult) string,
	cause error,
	logger runLogger,
	logCtx context.Context,
	logPrefix string,
) []ActionOutcome {
	if cause == nil || len(outcomes) >= len(items) {
		return outcomes
	}
	completed := make(map[string]struct{}, len(outcomes))
	for _, outcome := range outcomes {
		completed[outcome.AccountKey] = struct{}{}
	}
	for _, item := range items {
		if _, ok := completed[item.AccountKey]; ok {
			continue
		}
		action := item.Action
		if actionFor != nil {
			action = actionFor(item)
		}
		message := fmt.Sprintf("动作未执行：%v", cause)
		outcome := failedActionOutcome(item, action, message)
		outcomes = append(outcomes, outcome)
		logger.error(logCtx, logPrefix+"账号失败", map[string]any{
			"fileName":       item.FileName,
			"displayAccount": item.DisplayAccount,
			"action":         action,
			"error":          message,
		})
	}
	sort.Slice(outcomes, func(i, j int) bool {
		if outcomes[i].FileName == outcomes[j].FileName {
			return outcomes[i].Action < outcomes[j].Action
		}
		return outcomes[i].FileName < outcomes[j].FileName
	})
	return outcomes
}

func (s *Service) executeAction(
	ctx context.Context,
	setup store.Setup,
	item model.CodexInspectionResult,
	sourceMembers []model.CodexInspectionResult,
	automatic bool,
) error {
	if automatic && normalizeInspectionProvider(item.Provider) == "codex" {
		if strings.TrimSpace(item.AuthIndex) == "" {
			return errors.New(inspectionIdentityMissingReason)
		}
		for _, member := range sourceMembers {
			if normalizeInspectionProvider(member.Provider) == "codex" && strings.TrimSpace(member.AuthIndex) == "" {
				return errors.New(inspectionIdentityMissingReason)
			}
		}
	}
	fileNames := make([]string, 0, len(sourceMembers)+1)
	fileNames = append(fileNames, item.FileName)
	for _, member := range sourceMembers {
		fileNames = append(fileNames, member.FileName)
	}
	if s == nil || s.authFileMutations == nil {
		return cpaauthfiles.ErrMutationCoordinatorUnavailable
	}
	releaseMutation, err := s.authFileMutations.Acquire(ctx, fileNames...)
	if err != nil {
		return fmt.Errorf("acquire auth file mutation: %w", err)
	}
	defer releaseMutation()

	isSourceFileAction := len(sourceMembers) > 0 && (item.Action == "disable" || item.Action == "enable")
	var statusTarget cpaauthfiles.StatusMutationTarget
	if item.Action == "disable" || item.Action == "enable" {
		var err error
		if isSourceFileAction {
			statusTarget, err = s.resolveVerifiedStatusActionGroup(ctx, setup, item, sourceMembers)
		} else {
			statusTarget, err = cpaauthfiles.New(s.client).ResolveVerifiedStatusMutationTarget(
				ctx,
				setup.CPAUpstreamURL,
				setup.ManagementKey,
				inspectionAuthFileIdentity(item),
			)
		}
		if err != nil {
			return fmt.Errorf("resolve current auth file status target: %w", err)
		}
		if automatic && item.Action == "disable" {
			if isSourceFileAction {
				for _, affectedFile := range statusTarget.AffectedFiles {
					if affectedFile.Disabled {
						return fmt.Errorf(
							"refuse automatic source-file disable ownership: credential auth_index %q is already disabled",
							strings.TrimSpace(affectedFile.AuthIndex),
						)
					}
				}
			} else if statusTarget.File.Disabled {
				return errors.New("refuse automatic disable ownership: current credential is already disabled")
			}
		}
	}
	var revokedOwnership []store.CodexInspectionDisableOwnership
	shouldRevokeOwnership := isSourceFileAction || item.Action == "enable" || item.Action == "delete" || (item.Action == "disable" && !automatic)
	if shouldRevokeOwnership {
		var err error
		ownershipTarget := disableOwnershipTargetForResult(item)
		if isSourceFileAction || item.Action == "delete" {
			ownershipTarget = model.CodexInspectionDisableOwnershipTarget{FileName: strings.TrimSpace(item.FileName)}
		}
		revokedOwnership, err = s.store.RevokeCodexInspectionDisableOwnership(ctx, []model.CodexInspectionDisableOwnershipTarget{
			ownershipTarget,
		}, false)
		if err != nil {
			return fmt.Errorf("revoke inspection disable ownership: %w", err)
		}
	}

	var actionErr error
	switch item.Action {
	case "delete":
		deleteMembers := sourceMembers
		if len(deleteMembers) == 0 {
			deleteMembers = []model.CodexInspectionResult{item}
		}
		identities := make([]cpaauthfiles.Identity, 0, len(deleteMembers))
		for _, member := range deleteMembers {
			identities = append(identities, inspectionAuthFileIdentity(member))
		}
		actionErr = cpaauthfiles.New(s.client).DeleteVerifiedPhysicalFile(
			ctx,
			setup.CPAUpstreamURL,
			setup.ManagementKey,
			identities,
		)
	case "disable", "enable":
		disabled := item.Action == "disable"
		authFilesClient := cpaauthfiles.New(s.client)
		if isSourceFileAction {
			actionErr = s.patchVerifiedStatusActionGroup(
				ctx,
				setup,
				item,
				sourceMembers,
				statusTarget,
				disabled,
			)
		} else {
			actionErr = authFilesClient.PatchDisabledTarget(
				ctx,
				setup.CPAUpstreamURL,
				setup.ManagementKey,
				statusTarget,
				disabled,
			)
		}
	default:
		return nil
	}
	if actionErr != nil {
		restoreCtx, cancelRestore := detachedActionContext(ctx)
		restoreErr := s.store.RestoreCodexInspectionDisableOwnership(restoreCtx, revokedOwnership)
		cancelRestore()
		if restoreErr != nil {
			return fmt.Errorf("%w; restore inspection disable ownership: %v", actionErr, restoreErr)
		}
		return actionErr
	}

	switch item.Action {
	case "disable":
		if !automatic {
			return nil
		}
		if isSourceFileAction {
			disabledAtMS := time.Now().UnixMilli()
			ownership := make([]model.CodexInspectionDisableOwnership, 0, len(sourceMembers))
			for _, member := range sourceMembers {
				ownership = append(ownership, model.CodexInspectionDisableOwnership{
					FileName:        member.FileName,
					Provider:        member.Provider,
					AuthIndex:       member.AuthIndex,
					AccountID:       member.AccountID,
					AccountSnapshot: member.AccountSnapshot,
					DisabledAtMS:    disabledAtMS,
				})
			}
			if err := s.store.UpsertCodexInspectionDisableOwnerships(ctx, ownership); err != nil {
				return s.rollbackSourceFileDisable(ctx, setup, item, sourceMembers, revokedOwnership, err)
			}
			return nil
		}
		if err := s.store.UpsertCodexInspectionDisableOwnership(ctx, model.CodexInspectionDisableOwnership{
			FileName:        item.FileName,
			Provider:        item.Provider,
			AuthIndex:       item.AuthIndex,
			AccountID:       item.AccountID,
			AccountSnapshot: item.AccountSnapshot,
			DisabledAtMS:    time.Now().UnixMilli(),
		}); err != nil {
			rollbackCtx, cancelRollback := detachedActionContext(ctx)
			authFilesClient := cpaauthfiles.New(s.client)
			rollbackTarget, rollbackErr := authFilesClient.ResolveVerifiedStatusMutationTarget(
				rollbackCtx,
				setup.CPAUpstreamURL,
				setup.ManagementKey,
				cpaauthfiles.Identity{
					AuthFileName:      statusTarget.File.Name,
					AuthIndex:         statusTarget.File.AuthIndex,
					Provider:          statusTarget.File.Provider,
					AccountSnapshot:   statusTarget.File.AccountSnapshot,
					AccountIDSnapshot: statusTarget.File.AccountID,
				},
			)
			if rollbackErr != nil {
				rollbackErr = fmt.Errorf("revalidate rollback target: %w", rollbackErr)
			} else {
				rollbackErr = authFilesClient.PatchDisabledTarget(
					rollbackCtx,
					setup.CPAUpstreamURL,
					setup.ManagementKey,
					rollbackTarget,
					false,
				)
			}
			cancelRollback()
			if rollbackErr != nil {
				return fmt.Errorf("persist inspection disable ownership: %w; rollback enable failed: %w", err, rollbackErr)
			}
			return fmt.Errorf("persist inspection disable ownership: %w", err)
		}
	}
	return nil
}

func inspectionAuthFileIdentity(item model.CodexInspectionResult) cpaauthfiles.Identity {
	provider := normalizeInspectionProvider(item.Provider)
	return cpaauthfiles.Identity{
		AuthFileName:      strings.TrimSpace(item.FileName),
		AuthIndex:         strings.TrimSpace(item.AuthIndex),
		Provider:          provider,
		AccountSnapshot:   inspectionAccountSnapshot(provider, item.FileName, item.AccountSnapshot),
		AccountIDSnapshot: strings.TrimSpace(item.AccountID),
	}
}

func inspectionAuthFileIdentities(items []model.CodexInspectionResult) []cpaauthfiles.Identity {
	identities := make([]cpaauthfiles.Identity, 0, len(items))
	for _, item := range items {
		identities = append(identities, inspectionAuthFileIdentity(item))
	}
	return identities
}

func (s *Service) resolveVerifiedStatusActionGroup(
	ctx context.Context,
	setup store.Setup,
	item model.CodexInspectionResult,
	members []model.CodexInspectionResult,
) (cpaauthfiles.StatusMutationTarget, error) {
	authIndex := strings.TrimSpace(item.AuthIndex)
	if authIndex == "" {
		for _, member := range members {
			if candidate := strings.TrimSpace(member.AuthIndex); candidate != "" {
				authIndex = candidate
				break
			}
		}
	}
	return cpaauthfiles.New(s.client).ResolveVerifiedSourceFileStatusMutationTarget(
		ctx,
		setup.CPAUpstreamURL,
		setup.ManagementKey,
		strings.TrimSpace(item.FileName),
		authIndex,
		inspectionAuthFileIdentities(members),
	)
}

func verifiedStatusActionGroupFiles(
	files []cpaauthfiles.File,
	members []model.CodexInspectionResult,
) ([]cpaauthfiles.File, error) {
	matchedFiles := make([]cpaauthfiles.File, 0, len(members))
	used := make([]bool, len(files))
	for _, member := range members {
		identity := inspectionAuthFileIdentity(member)
		matches := make([]int, 0, 1)
		for index, file := range files {
			if used[index] || cpaauthfiles.VerifyResolvedIdentity(file, identity) != nil {
				continue
			}
			matches = append(matches, index)
		}
		if len(matches) == 0 {
			return nil, fmt.Errorf("%w: grouped status member identity changed", cpaauthfiles.ErrIdentityMismatch)
		}
		if len(matches) > 1 {
			return nil, fmt.Errorf("%w: grouped status member identity is ambiguous", cpaauthfiles.ErrStatusMutationScopeAmbiguous)
		}
		used[matches[0]] = true
		matchedFiles = append(matchedFiles, files[matches[0]])
	}
	return matchedFiles, nil
}

func sourceStatusMutationTarget(
	target cpaauthfiles.StatusMutationTarget,
) (cpaauthfiles.StatusMutationTarget, bool, error) {
	physicalName := strings.TrimSpace(target.File.Name)
	var sourceFile cpaauthfiles.File
	sourceCount := 0
	for _, file := range target.AffectedFiles {
		if strings.TrimSpace(file.ID) != physicalName {
			continue
		}
		sourceFile = file
		sourceCount++
	}
	if sourceCount > 1 {
		return cpaauthfiles.StatusMutationTarget{}, false, fmt.Errorf(
			"%w: physical file %q has multiple source runtime ids",
			cpaauthfiles.ErrStatusMutationScopeAmbiguous,
			physicalName,
		)
	}
	if sourceCount == 0 {
		return cpaauthfiles.StatusMutationTarget{}, false, nil
	}
	return cpaauthfiles.StatusMutationTarget{
		Selector:      physicalName,
		File:          sourceFile,
		Scope:         cpaauthfiles.StatusMutationScopeSourceFile,
		AffectedFiles: target.AffectedFiles,
	}, true, nil
}

func (s *Service) patchVerifiedStatusActionGroup(
	ctx context.Context,
	setup store.Setup,
	item model.CodexInspectionResult,
	members []model.CodexInspectionResult,
	target cpaauthfiles.StatusMutationTarget,
	disabled bool,
) error {
	authFilesClient := cpaauthfiles.New(s.client)
	knownSourceTarget, hasKnownSource, err := sourceStatusMutationTarget(target)
	if err != nil {
		return err
	}
	if hasKnownSource {
		return authFilesClient.PatchDisabledTargetAllowSourceFile(
			ctx,
			setup.CPAUpstreamURL,
			setup.ManagementKey,
			knownSourceTarget,
			disabled,
		)
	}

	files, err := verifiedStatusActionGroupFiles(target.AffectedFiles, members)
	if err != nil {
		return err
	}
	patchedMembers := make([]model.CodexInspectionResult, 0, len(members))
	patchedDisabled := make([]bool, 0, len(members))
	for index, file := range files {
		credentialTarget := cpaauthfiles.StatusMutationTarget{
			Selector:      strings.TrimSpace(file.ID),
			File:          file,
			Scope:         cpaauthfiles.StatusMutationScopeCredential,
			AffectedFiles: []cpaauthfiles.File{file},
		}
		patchErr := authFilesClient.PatchDisabledTarget(
			ctx,
			setup.CPAUpstreamURL,
			setup.ManagementKey,
			credentialTarget,
			disabled,
		)
		if patchErr == nil {
			patchedMembers = append(patchedMembers, members[index])
			patchedDisabled = append(patchedDisabled, file.Disabled)
			continue
		}
		if index == 0 && cpaauthfiles.IsPluginVirtualMutationConflict(patchErr) {
			refreshedTarget, resolveErr := s.resolveVerifiedStatusActionGroup(ctx, setup, item, members)
			if resolveErr != nil {
				return fmt.Errorf("plugin source fallback preflight: %w", resolveErr)
			}
			return authFilesClient.PatchDisabledTargetAllowSourceFile(
				ctx,
				setup.CPAUpstreamURL,
				setup.ManagementKey,
				refreshedTarget,
				disabled,
			)
		}
		rollbackErr := s.restorePatchedStatusActionTargets(
			ctx,
			setup,
			patchedMembers,
			patchedDisabled,
		)
		if rollbackErr != nil {
			return fmt.Errorf("%w; rollback grouped status mutation: %v", patchErr, rollbackErr)
		}
		return patchErr
	}
	return nil
}

func (s *Service) restorePatchedStatusActionTargets(
	ctx context.Context,
	setup store.Setup,
	members []model.CodexInspectionResult,
	disabled []bool,
) error {
	if len(members) == 0 {
		return nil
	}
	rollbackCtx, cancelRollback := detachedActionContext(ctx)
	defer cancelRollback()
	authFilesClient := cpaauthfiles.New(s.client)
	var rollbackErr error
	for index := len(members) - 1; index >= 0; index-- {
		target, err := authFilesClient.ResolveVerifiedStatusMutationTarget(
			rollbackCtx,
			setup.CPAUpstreamURL,
			setup.ManagementKey,
			inspectionAuthFileIdentity(members[index]),
		)
		if err == nil && target.Scope != cpaauthfiles.StatusMutationScopeCredential {
			err = fmt.Errorf("%w: rollback target is no longer credential scoped", cpaauthfiles.ErrIdentityMismatch)
		}
		if err == nil {
			err = authFilesClient.PatchDisabledTarget(
				rollbackCtx,
				setup.CPAUpstreamURL,
				setup.ManagementKey,
				target,
				disabled[index],
			)
		}
		if err != nil {
			rollbackErr = errors.Join(rollbackErr, fmt.Errorf("restore %s: %w", members[index].FileName, err))
		}
	}
	return rollbackErr
}

func verifySourceFileStatusTarget(target cpaauthfiles.StatusMutationTarget, members []model.CodexInspectionResult) error {
	if target.Scope != cpaauthfiles.StatusMutationScopeSourceFile {
		return fmt.Errorf("%w: current target is not a source file", cpaauthfiles.ErrStatusMutationScopeAmbiguous)
	}
	if len(target.AffectedFiles) != len(members) {
		return fmt.Errorf("%w: source file membership changed", cpaauthfiles.ErrIdentityMismatch)
	}
	for _, member := range members {
		identity := inspectionAuthFileIdentity(member)
		matches := make([]cpaauthfiles.File, 0, 1)
		for _, file := range target.AffectedFiles {
			if strings.TrimSpace(file.Name) != identity.AuthFileName ||
				strings.TrimSpace(file.AuthIndex) != identity.AuthIndex {
				continue
			}
			matches = append(matches, file)
		}
		if len(matches) != 1 {
			return fmt.Errorf("%w: source file member %q auth_index %q changed", cpaauthfiles.ErrIdentityMismatch, identity.AuthFileName, identity.AuthIndex)
		}
		if _, err := cpaauthfiles.VerifyIdentity(matches, identity); err != nil {
			return fmt.Errorf("verify source file member: %w", err)
		}
	}
	return nil
}

func detachedActionContext(ctx context.Context) (context.Context, context.CancelFunc) {
	base := context.Background()
	if ctx != nil {
		base = context.WithoutCancel(ctx)
	}
	return context.WithTimeout(base, resultPersistenceTimeout)
}

func (s *Service) rollbackSourceFileDisable(
	ctx context.Context,
	setup store.Setup,
	item model.CodexInspectionResult,
	sourceMembers []model.CodexInspectionResult,
	revokedOwnership []store.CodexInspectionDisableOwnership,
	persistErr error,
) error {
	resultErr := fmt.Errorf("persist inspection disable ownership: %w", persistErr)

	rollbackCtx, cancelRollback := detachedActionContext(ctx)
	rollbackTarget, rollbackErr := s.resolveVerifiedStatusActionGroup(rollbackCtx, setup, item, sourceMembers)
	if rollbackErr == nil {
		rollbackErr = s.patchVerifiedStatusActionGroup(
			rollbackCtx,
			setup,
			item,
			sourceMembers,
			rollbackTarget,
			false,
		)
	}
	cancelRollback()
	if rollbackErr != nil {
		resultErr = fmt.Errorf("%w; rollback source-file enable failed: %w", resultErr, rollbackErr)
	}

	restoreCtx, cancelRestore := detachedActionContext(ctx)
	restoreErr := s.store.RestoreCodexInspectionDisableOwnership(restoreCtx, revokedOwnership)
	cancelRestore()
	if restoreErr != nil {
		resultErr = fmt.Errorf("%w; restore inspection disable ownership failed: %v", resultErr, restoreErr)
	}
	return resultErr
}

func (s *Service) deleteAuthFileOnly(ctx context.Context, setup store.Setup, path string, fileName string) error {
	err, _ := s.deleteAuthFile(ctx, setup, path, fileName)
	return err
}

func (s *Service) deleteAuthFile(ctx context.Context, setup store.Setup, path string, fileName string) (error, int) {
	endpoint := cpa.NormalizeBaseURL(setup.CPAUpstreamURL) + path + "?name=" + url.QueryEscape(fileName)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, endpoint, nil)
	if err != nil {
		return err, 0
	}
	return s.doCPAAction(req, setup.ManagementKey)
}

func (s *Service) patchAuthFile(ctx context.Context, setup store.Setup, path string, payload map[string]any) (error, int) {
	data, err := json.Marshal(payload)
	if err != nil {
		return err, 0
	}
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPatch,
		cpa.NormalizeBaseURL(setup.CPAUpstreamURL)+path,
		bytes.NewReader(data),
	)
	if err != nil {
		return err, 0
	}
	req.Header.Set("Content-Type", "application/json")
	return s.doCPAAction(req, setup.ManagementKey)
}

func (s *Service) doCPAAction(req *http.Request, managementKey string) (error, int) {
	req.Header.Set("Authorization", "Bearer "+managementKey)
	res, err := s.client.Do(req)
	if err != nil {
		return err, 0
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(res.Body, maxStoredBodyText))
		return fmt.Errorf("%s %s", res.Status, truncate(string(body), maxStoredBodyText)), res.StatusCode
	}
	if err := cpaauthfiles.ValidateActionResponse(res.Body); err != nil {
		return err, res.StatusCode
	}
	return nil, res.StatusCode
}

type runLogger struct {
	service *Service
	runID   int64
}

func (l runLogger) info(ctx context.Context, message string, detail any) {
	l.log(ctx, "info", message, detail)
}

func (l runLogger) success(ctx context.Context, message string, detail any) {
	l.log(ctx, "success", message, detail)
}

func (l runLogger) warning(ctx context.Context, message string, detail any) {
	l.log(ctx, "warning", message, detail)
}

func (l runLogger) error(ctx context.Context, message string, detail any) {
	l.log(ctx, "error", message, detail)
}

func (l runLogger) log(ctx context.Context, level string, message string, detail any) {
	if l.service == nil || l.runID <= 0 {
		return
	}
	// Keep inspection log writes serialized, but do not let a blocked SQLite
	// writer make every probe wait behind it. Process logs are best-effort; a
	// saturated gate drops the ordinary log and leaves lifecycle cleanup free
	// to continue.
	releaseGate := func() {}
	if l.service.logGate != nil {
		select {
		case l.service.logGate <- struct{}{}:
			releaseGate = func() { <-l.service.logGate }
		case <-time.After(processLogQueueWait):
			log.Printf("drop codex inspection log run %d: SQLite log writer is busy", l.runID)
			return
		}
	} else {
		l.service.logMu.Lock()
		releaseGate = l.service.logMu.Unlock
	}
	defer releaseGate()
	// Keep the write independent from a cancelled probe context and bounded so
	// a transient database lock cannot stall the inspection indefinitely.
	if ctx == nil {
		ctx = context.Background()
	}
	logCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), processLogWriteTimeout)
	defer cancel()
	if _, err := l.service.store.InsertCodexInspectionLog(logCtx, model.CodexInspectionLog{
		RunID:   l.runID,
		Level:   level,
		Message: message,
		Detail:  sanitizeDetail(detail),
	}); err != nil {
		log.Printf("write codex inspection log run %d: %v", l.runID, err)
	}
}

func resolveProbeAction(item account, statusCode int, bodyText string, rateLimit *codexRateLimit, usedPercent *float64, isQuota bool, threshold float64, planTypes ...string) inspectionDecision {
	if isDeactivatedWorkspaceResponse(statusCode, bodyText) {
		return resolveDeactivatedWorkspaceProbeAction(usedPercent)
	}
	planType := ""
	if len(planTypes) > 0 {
		planType = planTypes[0]
	}
	if decision := resolveWindowAwareProbeAction(item, statusCode, bodyText, rateLimit, threshold, planType); decision != nil {
		return *decision
	}
	return resolveLegacyProbeAction(item, statusCode, bodyText, usedPercent, isQuota, threshold)
}

func isDeactivatedWorkspaceResponse(statusCode int, bodyText string) bool {
	return statusCode == http.StatusPaymentRequired &&
		strings.Contains(strings.ToLower(bodyText), "deactivated_workspace")
}

func resolveDeactivatedWorkspaceProbeAction(usedPercent *float64) inspectionDecision {
	return inspectionDecision{
		Action:       "delete",
		ActionReason: "接口返回 402，工作区已停用，建议删除账号",
		UsedPercent:  usedPercent,
		IsQuota:      false,
	}
}

func resolveWindowAwareProbeAction(item account, statusCode int, bodyText string, rateLimit *codexRateLimit, threshold float64, planType string) *inspectionDecision {
	if rateLimit == nil {
		return nil
	}
	classified := classifyWindows(rateLimit, planType)
	longWindow := classified.longWindow()
	if longWindow == nil || longWindow.UsedPercent == nil {
		decision := inspectionDecision{
			Action:       "keep",
			ActionReason: "额度信息不完整，保留账号",
			UsedPercent:  deriveRateLimitUsedPercent(rateLimit),
			IsQuota:      false,
		}
		return &decision
	}
	longWindowUsedPercent := *longWindow.UsedPercent
	longWindowLabel := classified.longWindowLabel(longWindow)
	fiveHour := classified.FiveHour
	fiveHourOverThreshold := fiveHour != nil && fiveHour.UsedPercent != nil && *fiveHour.UsedPercent >= threshold

	if statusCode == http.StatusUnauthorized {
		decision := resolveUnauthorizedProbeAction(bodyText, ptrFloat(longWindowUsedPercent))
		return &decision
	}
	if longWindowUsedPercent >= threshold {
		if item.Disabled {
			return &inspectionDecision{
				Action:       "keep",
				ActionReason: fmt.Sprintf("%s达到阈值，但账号已禁用", longWindowLabel),
				UsedPercent:  ptrFloat(longWindowUsedPercent),
				IsQuota:      true,
			}
		}
		return &inspectionDecision{
			Action:       "disable",
			ActionReason: fmt.Sprintf("%s达到阈值，建议禁用账号", longWindowLabel),
			UsedPercent:  ptrFloat(longWindowUsedPercent),
			IsQuota:      true,
		}
	}
	if item.Disabled {
		if fiveHourOverThreshold {
			return &inspectionDecision{
				Action:       "keep",
				ActionReason: fmt.Sprintf("5 小时额度仍达到阈值，%s可用但继续保持禁用", longWindowLabel),
				UsedPercent:  ptrFloat(longWindowUsedPercent),
				IsQuota:      true,
			}
		}
		reason := fmt.Sprintf("%s仍可用，建议立即启用账号", longWindowLabel)
		return &inspectionDecision{
			Action:       "enable",
			ActionReason: reason,
			UsedPercent:  ptrFloat(longWindowUsedPercent),
			IsQuota:      false,
		}
	}
	if fiveHourOverThreshold {
		return &inspectionDecision{
			Action:       "keep",
			ActionReason: fmt.Sprintf("5 小时额度达到阈值，但%s仍可用，暂不禁用账号", longWindowLabel),
			UsedPercent:  ptrFloat(longWindowUsedPercent),
			IsQuota:      false,
		}
	}
	return &inspectionDecision{
		Action:       "keep",
		ActionReason: fmt.Sprintf("%s仍可用，无需处理", longWindowLabel),
		UsedPercent:  ptrFloat(longWindowUsedPercent),
		IsQuota:      false,
	}
}

func resolveLegacyProbeAction(item account, statusCode int, bodyText string, usedPercent *float64, isQuota bool, threshold float64) inspectionDecision {
	overThreshold := usedPercent != nil && *usedPercent >= threshold
	if statusCode == http.StatusUnauthorized {
		return resolveUnauthorizedProbeAction(bodyText, usedPercent)
	}
	if isQuota || overThreshold {
		if item.Disabled {
			reason := "额度已耗尽，但账号已禁用"
			if overThreshold {
				reason = "额度超阈值，但账号已禁用"
			}
			return inspectionDecision{Action: "keep", ActionReason: reason, UsedPercent: usedPercent, IsQuota: isQuota}
		}
		reason := "额度已耗尽，建议禁用账号"
		if overThreshold {
			reason = "额度超阈值，建议禁用账号"
		}
		return inspectionDecision{Action: "disable", ActionReason: reason, UsedPercent: usedPercent, IsQuota: isQuota}
	}
	if statusCode == http.StatusOK && item.Disabled && usedPercent != nil {
		return inspectionDecision{
			Action:       "enable",
			ActionReason: "账号恢复健康，建议重新启用",
			UsedPercent:  usedPercent,
			IsQuota:      false,
		}
	}
	if statusCode == http.StatusOK && item.Disabled {
		return inspectionDecision{
			Action:       "keep",
			ActionReason: "额度信息不完整，无法确认恢复，保留账号",
			UsedPercent:  usedPercent,
			IsQuota:      false,
		}
	}
	return inspectionDecision{Action: "keep", ActionReason: "无需处理", UsedPercent: usedPercent, IsQuota: false}
}

func resolveUnauthorizedProbeAction(bodyText string, usedPercent *float64) inspectionDecision {
	decision, ok := credentialpolicy.EvaluateFailure(credentialpolicy.FailureSignal{
		Provider:   "codex",
		StatusCode: http.StatusUnauthorized,
		Summary:    bodyText,
	})
	if !ok {
		return inspectionDecision{
			Action:       "reauth",
			ActionReason: "接口返回 401，认证失败，建议重新登录账号",
			UsedPercent:  usedPercent,
			IsQuota:      false,
		}
	}
	switch decision.ReasonCode {
	case credentialpolicy.ReasonInvalidCredentials:
		return inspectionDecision{
			Action:       "reauth",
			ActionReason: "接口返回 401，登录已过期，建议重新登录账号",
			UsedPercent:  usedPercent,
			IsQuota:      false,
		}
	case credentialpolicy.ReasonTokenRevoked:
		return inspectionDecision{
			Action:       "reauth",
			ActionReason: "接口返回 401，认证令牌已失效，建议重新登录账号",
			UsedPercent:  usedPercent,
			IsQuota:      false,
		}
	default:
		return inspectionDecision{
			Action:       "reauth",
			ActionReason: "接口返回 401，认证失败，建议重新登录账号",
			UsedPercent:  usedPercent,
			IsQuota:      false,
		}
	}
}

func resolveAutoActionResults(mode string, results []model.CodexInspectionResult) []model.CodexInspectionResult {
	mode = model.NormalizeCodexInspectionAutoActionMode(mode, model.CodexInspectionAutoActionNone)
	if mode == model.CodexInspectionAutoActionNone {
		return results
	}
	out := make([]model.CodexInspectionResult, len(results))
	copy(out, results)
	return out
}

func resolveExecutableAction(mode string, action string) string {
	if mode == model.CodexInspectionAutoActionDisable && action == "delete" {
		return "disable"
	}
	return action
}

func selectAutoActionItems(mode string, autoRecoverEnabled bool, results []model.CodexInspectionResult) ([]model.CodexInspectionResult, []ActionOutcome) {
	mode = model.NormalizeCodexInspectionAutoActionMode(mode, model.CodexInspectionAutoActionNone)
	if mode == model.CodexInspectionAutoActionNone && !autoRecoverEnabled {
		return nil, nil
	}

	items := make([]model.CodexInspectionResult, 0)
	outcomes := make([]ActionOutcome, 0)
	for _, group := range buildExecutableFileActionGroups(results, func(result model.CodexInspectionResult) string {
		return resolveExecutableAction(mode, result.Action)
	}) {
		if group.Mixed {
			for _, result := range group.Items {
				reason := fileActionMixedReason
				if !hasInspectionActionIdentity(result) {
					reason = inspectionIdentityMissingReason
				}
				outcomes = append(outcomes, needsReviewActionOutcome(result, result.Action, reason))
			}
			continue
		}
		eligible := make([]model.CodexInspectionResult, 0, len(group.Items))
		for _, result := range group.Items {
			if !allowAutoAction(mode, autoRecoverEnabled, result) {
				continue
			}
			if !hasInspectionActionIdentity(result) {
				outcomes = append(outcomes, needsReviewActionOutcome(result, result.Action, inspectionIdentityMissingReason))
				continue
			}
			eligible = append(eligible, result)
		}
		if len(eligible) == 0 {
			continue
		}
		items = append(items, eligible[0])
		for _, result := range eligible[1:] {
			outcomes = append(outcomes, skippedActionOutcome(result, result.Action, fileActionDuplicateReason))
		}
	}
	return items, outcomes
}

func buildExecutableFileActionGroups(
	results []model.CodexInspectionResult,
	actionResolvers ...func(model.CodexInspectionResult) string,
) []fileActionGroup {
	resolveAction := func(result model.CodexInspectionResult) string { return result.Action }
	if len(actionResolvers) > 0 && actionResolvers[0] != nil {
		resolveAction = actionResolvers[0]
	}
	groupOrder := make([]string, 0)
	itemsByFileName := map[string][]model.CodexInspectionResult{}
	for _, result := range results {
		fileName := strings.TrimSpace(result.FileName)
		if fileName == "" {
			continue
		}
		if _, ok := itemsByFileName[fileName]; !ok {
			groupOrder = append(groupOrder, fileName)
		}
		itemsByFileName[fileName] = append(itemsByFileName[fileName], result)
	}
	groups := make([]fileActionGroup, 0, len(results))
	for _, fileName := range groupOrder {
		allFileItems := itemsByFileName[fileName]
		fileItems := make([]model.CodexInspectionResult, 0, len(allFileItems))
		for _, item := range allFileItems {
			if isExecutableInspectionAction(resolveAction(item)) {
				fileItems = append(fileItems, item)
			}
		}
		if len(fileItems) == 0 {
			continue
		}
		hasDelete := false
		for _, item := range fileItems {
			if resolveAction(item) == "delete" {
				hasDelete = true
				break
			}
		}
		if hasDelete {
			group := fileActionGroup{
				Key:      "file:" + fileName,
				FileName: fileName,
				Items:    fileItems,
				AllItems: allFileItems,
				Action:   "delete",
			}
			for _, item := range allFileItems {
				if resolveAction(item) != "delete" {
					group.Mixed = true
				}
			}
			groups = append(groups, group)
			continue
		}

		identityOrder := make([]string, 0)
		identityGroups := map[string]*fileActionGroup{}
		for _, item := range fileItems {
			identityKey := inspectionActionIdentityKey(item)
			group, ok := identityGroups[identityKey]
			if !ok {
				group = &fileActionGroup{
					Key:      "credential:" + identityKey,
					FileName: fileName,
					Action:   resolveAction(item),
				}
				identityGroups[identityKey] = group
				identityOrder = append(identityOrder, identityKey)
			}
			if resolveAction(item) != group.Action {
				group.Mixed = true
			}
			group.Items = append(group.Items, item)
			group.AllItems = append(group.AllItems, item)
		}
		for _, identityKey := range identityOrder {
			groups = append(groups, *identityGroups[identityKey])
		}
	}
	return groups
}

func inspectionActionIdentityKey(result model.CodexInspectionResult) string {
	return inspectionIdentityKey(
		result.FileName,
		result.Provider,
		result.AuthIndex,
		result.AccountID,
		result.AccountSnapshot,
	)
}

func inspectionIdentityKey(fileName, provider, authIndex, accountID, accountSnapshot string) string {
	fileName = strings.TrimSpace(fileName)
	provider = normalizeInspectionProvider(provider)
	accountID = strings.TrimSpace(accountID)
	normalizedAccountSnapshot := inspectionAccountSnapshot(provider, fileName, accountSnapshot)
	if provider == "codex" && normalizedAccountSnapshot == "" {
		// chatgpt_account_id identifies a shared Workspace, not a member. Do
		// not let a Workspace-only result become the identity used to group or
		// mutate a credential when the credential-level fallback is absent.
		accountID = ""
	}
	if provider != "codex" && accountID != "" {
		normalizedAccountSnapshot = ""
	}
	encoded, _ := json.Marshal([]string{
		fileName,
		provider,
		strings.TrimSpace(authIndex),
		accountID,
		normalizedAccountSnapshot,
	})
	return string(encoded)
}

func hasInspectionActionIdentity(result model.CodexInspectionResult) bool {
	fileName := strings.TrimSpace(result.FileName)
	provider := normalizeInspectionProvider(result.Provider)
	if fileName == "" || provider == "" {
		return false
	}
	if provider == "codex" {
		return strings.TrimSpace(result.AuthIndex) != ""
	}
	if strings.TrimSpace(result.AuthIndex) != "" {
		return true
	}
	return strings.TrimSpace(result.AccountID) != "" ||
		inspectionAccountSnapshot(provider, fileName, result.AccountSnapshot) != ""
}

func allowAutoAction(mode string, autoRecoverEnabled bool, result model.CodexInspectionResult) bool {
	if result.Action == "enable" {
		return autoRecoverEnabled && result.AutoRecoverEligible
	}
	switch mode {
	case model.CodexInspectionAutoActionEnable:
		return false
	case model.CodexInspectionAutoActionDisable:
		return result.Action == "disable" || result.Action == "delete"
	case model.CodexInspectionAutoActionDelete:
		return result.Action == "disable" || result.Action == "delete"
	default:
		return false
	}
}

func (s *Service) applyDisableOwnership(ctx context.Context, accounts []account, logger runLogger) {
	items, err := s.store.ListCodexInspectionDisableOwnership(ctx)
	if err != nil {
		logger.warning(ctx, "加载巡检禁用所有权失败，自动恢复将保持关闭", map[string]any{"error": err.Error()})
		return
	}
	for _, item := range items {
		if disableOwnershipHasDuplicateCredentialLocator(item, accounts) {
			logger.warning(ctx, "巡检禁用所有权凭证定位不唯一，跳过自动恢复", map[string]any{
				"fileName":  item.FileName,
				"authIndex": item.AuthIndex,
				"provider":  item.Provider,
			})
			continue
		}
		matchedIndexes := make([]int, 0, 1)
		disabledMatchCount := 0
		for index := range accounts {
			if disableOwnershipMatchesAccount(item, accounts[index]) {
				matchedIndexes = append(matchedIndexes, index)
				if accounts[index].Disabled {
					disabledMatchCount++
				}
			}
		}
		if len(matchedIndexes) > 1 && strings.TrimSpace(item.AuthIndex) != "" {
			// A legacy ownership row with a credential locator is still useful,
			// but duplicate credentials must not be narrowed by Workspace/member
			// evidence. Keep the row for a later unambiguous inventory instead of
			// authorizing recovery or deleting the ownership record.
			logger.warning(ctx, "巡检禁用所有权匹配到多个凭证，跳过自动恢复", map[string]any{
				"fileName":  item.FileName,
				"authIndex": item.AuthIndex,
				"provider":  item.Provider,
			})
			continue
		}
		if len(matchedIndexes) != 1 || disabledMatchCount == 0 {
			if err := s.store.DeleteCodexInspectionDisableOwnership(ctx, disableOwnershipTarget(item)); err != nil {
				logger.warning(ctx, "清理巡检禁用所有权失败", map[string]any{
					"fileName":  item.FileName,
					"authIndex": item.AuthIndex,
					"error":     err.Error(),
				})
			}
			continue
		}
		accounts[matchedIndexes[0]].AutoRecoverOwned = true
	}
}

func disableOwnershipHasDuplicateCredentialLocator(
	item model.CodexInspectionDisableOwnership,
	accounts []account,
) bool {
	fileName := strings.TrimSpace(item.FileName)
	authIndex := strings.TrimSpace(item.AuthIndex)
	provider := normalizeInspectionProvider(item.Provider)
	if fileName == "" || authIndex == "" || provider == "" {
		return false
	}
	count := 0
	for _, candidate := range accounts {
		if strings.TrimSpace(candidate.FileName) != fileName ||
			normalizeInspectionProvider(candidate.Provider) != provider ||
			strings.TrimSpace(candidate.AuthIndex) != authIndex {
			continue
		}
		count++
		if count > 1 {
			return true
		}
	}
	return false
}

func disableOwnershipMatchesAccount(item model.CodexInspectionDisableOwnership, candidate account) bool {
	provider := normalizeInspectionProvider(item.Provider)
	candidateProvider := normalizeInspectionProvider(candidate.Provider)
	if provider == "" || candidateProvider == "" {
		return false
	}
	if strings.TrimSpace(candidate.FileName) != strings.TrimSpace(item.FileName) ||
		candidateProvider != provider {
		return false
	}
	return inspectionIdentityMatchesAccount(
		provider,
		item.FileName,
		item.AuthIndex,
		item.AccountID,
		item.AccountSnapshot,
		candidate,
	)
}

func disableOwnershipTarget(item model.CodexInspectionDisableOwnership) model.CodexInspectionDisableOwnershipTarget {
	provider := normalizeInspectionProvider(item.Provider)
	authIndex := strings.TrimSpace(item.AuthIndex)
	accountID := strings.TrimSpace(item.AccountID)
	accountSnapshot := inspectionAccountSnapshot(provider, item.FileName, item.AccountSnapshot)
	return model.CodexInspectionDisableOwnershipTarget{
		FileName:        strings.TrimSpace(item.FileName),
		Provider:        &provider,
		AuthIndex:       &authIndex,
		AccountID:       &accountID,
		AccountSnapshot: &accountSnapshot,
	}
}

func disableOwnershipTargetForResult(item model.CodexInspectionResult) model.CodexInspectionDisableOwnershipTarget {
	return disableOwnershipTarget(model.CodexInspectionDisableOwnership{
		FileName:        item.FileName,
		Provider:        item.Provider,
		AuthIndex:       item.AuthIndex,
		AccountID:       item.AccountID,
		AccountSnapshot: item.AccountSnapshot,
	})
}

func selectManualActionItems(
	results []model.CodexInspectionResult,
	selected map[int64]struct{},
) ([]model.CodexInspectionResult, []ActionOutcome) {
	items := make([]model.CodexInspectionResult, 0, len(selected))
	outcomes := make([]ActionOutcome, 0)
	seenGroupKeys := map[string]struct{}{}
	groupByResultID := map[int64]fileActionGroup{}
	for _, group := range buildExecutableFileActionGroups(results) {
		for _, item := range group.Items {
			groupByResultID[item.ID] = group
		}
	}
	for _, result := range results {
		if _, ok := selected[result.ID]; !ok {
			continue
		}
		fileName := strings.TrimSpace(result.FileName)
		if !isExecutableInspectionAction(result.Action) {
			outcomes = append(outcomes, skippedActionOutcome(result, result.Action, "该巡检结果不是可执行动作"))
			continue
		}
		switch model.NormalizeCodexInspectionActionStatus(result.ActionStatus, result.Action) {
		case model.CodexInspectionActionStatusSuccess:
			outcomes = append(outcomes, skippedActionOutcome(result, result.Action, "该建议动作已执行成功"))
			continue
		case model.CodexInspectionActionStatusSkipped:
			outcomes = append(outcomes, skippedActionOutcome(result, result.Action, "该建议动作已跳过"))
			continue
		case model.CodexInspectionActionStatusNeedsReview:
			outcomes = append(outcomes, needsReviewActionOutcome(result, result.Action, "该建议动作需要到凭证管理中人工处理"))
			continue
		}
		if fileName == "" {
			outcomes = append(outcomes, failedActionOutcome(result, result.Action, "认证文件名为空，无法执行"))
			continue
		}
		if !hasInspectionActionIdentity(result) {
			outcomes = append(outcomes, needsReviewActionOutcome(result, result.Action, inspectionIdentityMissingReason))
			continue
		}
		group, ok := groupByResultID[result.ID]
		if !ok {
			group = fileActionGroup{
				Key:      "credential:" + inspectionActionIdentityKey(result),
				FileName: fileName,
				Items:    []model.CodexInspectionResult{result},
				Action:   result.Action,
			}
		}
		if ok && group.Mixed {
			outcomes = append(outcomes, needsReviewActionOutcome(result, result.Action, fileActionMixedReason))
			continue
		}
		if _, ok := seenGroupKeys[group.Key]; ok {
			outcomes = append(outcomes, skippedActionOutcome(result, result.Action, "该认证目标已由另一条结果处理"))
			continue
		}
		seenGroupKeys[group.Key] = struct{}{}
		items = append(items, result)
	}
	return items, outcomes
}

func isExecutableInspectionAction(action string) bool {
	return action == "delete" || action == "disable" || action == "enable"
}

func (s *Service) validateActionItems(
	ctx context.Context,
	logCtx context.Context,
	setup store.Setup,
	items []model.CodexInspectionResult,
	referenceResults []model.CodexInspectionResult,
	logger runLogger,
	logPrefix string,
	actionFor func(model.CodexInspectionResult) string,
) ([]model.CodexInspectionResult, map[string][]model.CodexInspectionResult, []ActionOutcome, error) {
	if len(items) == 0 {
		return nil, nil, nil, nil
	}
	files, err := s.fetchAuthFiles(ctx, setup)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, nil, nil, ctxErr
		}
		message := fmt.Sprintf("刷新认证文件失败，已拒绝执行：%v", err)
		outcomes := make([]ActionOutcome, 0, len(items))
		for _, item := range items {
			action := item.Action
			if actionFor != nil {
				action = actionFor(item)
			}
			outcome := failedActionOutcome(item, action, message)
			outcomes = append(outcomes, outcome)
			logger.error(logCtx, logPrefix+"账号校验失败", map[string]any{
				"fileName":       item.FileName,
				"displayAccount": item.DisplayAccount,
				"action":         action,
				"authIndex":      item.AuthIndex,
				"accountId":      item.AccountID,
				"error":          outcome.Error,
			})
		}
		return nil, nil, outcomes, nil
	}
	currentByFile := map[string][]account{}
	for _, file := range files {
		item := toAccount(file)
		currentByFile[item.FileName] = append(currentByFile[item.FileName], item)
	}
	sourceFilePlans := buildSourceFileActionPlans(items, currentByFile, actionFor)

	validItems := make([]model.CodexInspectionResult, 0, len(items))
	sourceFileMembers := make(map[string][]model.CodexInspectionResult)
	outcomes := make([]ActionOutcome, 0)
	for _, item := range items {
		action := item.Action
		if actionFor != nil {
			action = actionFor(item)
		}
		currentCandidates := currentByFile[strings.TrimSpace(item.FileName)]
		current, ok := matchCurrentAccount(currentCandidates, item)
		if !ok {
			outcome := failedActionOutcome(item, action, "认证文件不存在或账号标识已变化，已拒绝执行")
			outcomes = append(outcomes, outcome)
			logger.error(logCtx, logPrefix+"账号校验失败", map[string]any{
				"fileName":       item.FileName,
				"displayAccount": item.DisplayAccount,
				"action":         action,
				"authIndex":      item.AuthIndex,
				"accountId":      item.AccountID,
				"error":          outcome.Error,
			})
			continue
		}
		identityKey := inspectionActionIdentityKey(item)
		sourcePlan, hasSourcePlan := sourceFilePlans[strings.TrimSpace(item.FileName)]
		var actionMembers []model.CodexInspectionResult
		if hasSourcePlan && (action == "disable" || action == "enable") {
			if identityKey == sourcePlan.CanonicalIdentity {
				actionMembers = append([]model.CodexInspectionResult(nil), sourcePlan.Members...)
			} else if sourceFileActionPlanContainsIdentity(sourcePlan, identityKey) {
				outcome := skippedActionOutcome(item, action, fileActionDuplicateReason)
				outcomes = append(outcomes, outcome)
				logger.info(logCtx, logPrefix+"账号跳过", map[string]any{
					"fileName":       item.FileName,
					"displayAccount": item.DisplayAccount,
					"action":         action,
					"reason":         outcome.Error,
				})
				continue
			}
		}
		mutationScope := statusMutationTargetScopeForAccount(currentCandidates, current)
		if (action == "disable" || action == "enable") &&
			mutationScope != statusMutationTargetCredential &&
			!(hasSourcePlan && identityKey == sourcePlan.CanonicalIdentity) {
			outcome := needsReviewActionOutcome(item, action, statusMutationScopeReason)
			outcomes = append(outcomes, outcome)
			logger.warning(logCtx, logPrefix+"账号跳过", map[string]any{
				"fileName":       item.FileName,
				"displayAccount": item.DisplayAccount,
				"action":         action,
				"authIndex":      item.AuthIndex,
				"accountId":      item.AccountID,
				"status":         outcome.Status,
				"reason":         outcome.Error,
			})
			continue
		}
		deleteMembers, deleteCovered := deleteActionMembersForCurrentFile(currentCandidates, referenceResults, item.FileName, actionFor)
		if action == "delete" && !deleteCovered {
			outcome := needsReviewActionOutcome(item, action, fileDeleteCoverageReason)
			outcomes = append(outcomes, outcome)
			logger.warning(logCtx, logPrefix+"账号跳过", map[string]any{
				"fileName":       item.FileName,
				"displayAccount": item.DisplayAccount,
				"action":         action,
				"authIndex":      item.AuthIndex,
				"accountId":      item.AccountID,
				"status":         outcome.Status,
				"reason":         outcome.Error,
			})
			continue
		}
		if action == "delete" {
			actionMembers = deleteMembers
		}
		item.Disabled = current.Disabled
		allSourceMembersDisabled := false
		allSourceMembersEnabled := false
		if hasSourcePlan && identityKey == sourcePlan.CanonicalIdentity {
			allSourceMembersDisabled = allAccountsDisabled(currentCandidates)
			allSourceMembersEnabled = allAccountsEnabled(currentCandidates)
		}
		if action == "disable" && (current.Disabled && !hasSourcePlan || allSourceMembersDisabled) {
			outcome := skippedActionOutcome(item, action, "账号已是禁用状态，未重复执行")
			outcome.CurrentDisabled = boolPointer(current.Disabled)
			outcomes = append(outcomes, outcome)
			logger.info(logCtx, logPrefix+"账号跳过", map[string]any{
				"fileName":       item.FileName,
				"displayAccount": item.DisplayAccount,
				"action":         action,
				"reason":         outcome.Error,
			})
			continue
		}
		if action == "enable" && (!current.Disabled && !hasSourcePlan || allSourceMembersEnabled) {
			outcome := skippedActionOutcome(item, action, "账号已是启用状态，未重复执行")
			outcome.CurrentDisabled = boolPointer(current.Disabled)
			outcomes = append(outcomes, outcome)
			logger.info(logCtx, logPrefix+"账号跳过", map[string]any{
				"fileName":       item.FileName,
				"displayAccount": item.DisplayAccount,
				"action":         action,
				"reason":         outcome.Error,
			})
			continue
		}
		if len(actionMembers) > 0 {
			sourceFileMembers[identityKey] = actionMembers
		}
		validItems = append(validItems, item)
	}
	return validItems, sourceFileMembers, outcomes, nil
}

func buildSourceFileActionPlans(
	items []model.CodexInspectionResult,
	currentByFile map[string][]account,
	actionFor func(model.CodexInspectionResult) string,
) map[string]sourceFileActionPlan {
	plans := make(map[string]sourceFileActionPlan)
	for fileName, candidates := range currentByFile {
		if len(candidates) <= 1 {
			continue
		}
		fileItems := make([]model.CodexInspectionResult, 0, len(candidates))
		for _, item := range items {
			if strings.TrimSpace(item.FileName) == strings.TrimSpace(fileName) {
				fileItems = append(fileItems, item)
			}
		}
		members := make([]model.CodexInspectionResult, 0, len(candidates))
		used := make(map[string]struct{}, len(candidates))
		canonicalIndex := 0
		sourceCount := 0
		action := ""
		complete := true
		for candidateIndex, candidate := range candidates {
			matches := matchingInspectionResults(fileItems, candidate, used, actionFor)
			if len(matches) != 1 {
				complete = false
				break
			}
			matchedAction := matches[0].Action
			if actionFor != nil {
				matchedAction = actionFor(matches[0])
			}
			if matchedAction != "disable" && matchedAction != "enable" {
				complete = false
				break
			}
			if action == "" {
				action = matchedAction
			} else if matchedAction != action {
				complete = false
				break
			}
			identityKey := inspectionActionIdentityKey(matches[0])
			used[identityKey] = struct{}{}
			members = append(members, matches[0])
			if strings.TrimSpace(candidate.RuntimeID) == strings.TrimSpace(fileName) {
				sourceCount++
				canonicalIndex = candidateIndex
			}
		}
		if !complete || sourceCount > 1 || len(members) != len(candidates) {
			continue
		}
		canonicalItem := members[canonicalIndex]
		plans[fileName] = sourceFileActionPlan{
			CanonicalIdentity: inspectionActionIdentityKey(canonicalItem),
			Action:            action,
			Members:           members,
		}
	}
	return plans
}

func matchingInspectionResults(
	items []model.CodexInspectionResult,
	candidate account,
	used map[string]struct{},
	actionFor func(model.CodexInspectionResult) string,
) []model.CodexInspectionResult {
	matches := make([]model.CodexInspectionResult, 0, 1)
	for _, item := range items {
		identityKey := inspectionActionIdentityKey(item)
		if used != nil {
			if _, ok := used[identityKey]; ok {
				continue
			}
		}
		action := item.Action
		if actionFor != nil {
			action = actionFor(item)
		}
		if action != "disable" && action != "enable" {
			continue
		}
		if inspectionResultMatchesCurrentAccount(item, candidate) {
			matches = append(matches, item)
		}
	}
	return matches
}

func sourceFileActionPlanContainsIdentity(plan sourceFileActionPlan, identityKey string) bool {
	for _, item := range plan.Members {
		if inspectionActionIdentityKey(item) == identityKey {
			return true
		}
	}
	return false
}

func statusMutationTargetScopeForAccount(candidates []account, target account) statusMutationTargetScope {
	runtimeID := strings.TrimSpace(target.RuntimeID)
	if runtimeID == "" {
		return statusMutationTargetAmbiguous
	}
	runtimeIDMatches := 0
	for _, candidate := range candidates {
		if strings.TrimSpace(candidate.RuntimeID) == runtimeID {
			runtimeIDMatches++
		}
	}
	if runtimeIDMatches != 1 {
		return statusMutationTargetAmbiguous
	}
	if len(candidates) <= 1 {
		return statusMutationTargetCredential
	}
	sourceCount := 0
	for _, candidate := range candidates {
		if strings.TrimSpace(candidate.RuntimeID) == strings.TrimSpace(target.FileName) {
			sourceCount++
		}
	}
	if sourceCount > 1 {
		return statusMutationTargetAmbiguous
	}
	if sourceCount == 1 {
		if runtimeID == strings.TrimSpace(target.FileName) {
			return statusMutationTargetSourceFile
		}
		return statusMutationTargetExpandedChild
	}
	return statusMutationTargetCredential
}

func allAccountsDisabled(items []account) bool {
	if len(items) == 0 {
		return false
	}
	for _, item := range items {
		if !item.Disabled {
			return false
		}
	}
	return true
}

func allAccountsEnabled(items []account) bool {
	if len(items) == 0 {
		return false
	}
	for _, item := range items {
		if item.Disabled {
			return false
		}
	}
	return true
}

func deleteActionMembersForCurrentFile(
	current []account,
	results []model.CodexInspectionResult,
	fileName string,
	actionFor func(model.CodexInspectionResult) string,
) ([]model.CodexInspectionResult, bool) {
	if len(current) == 0 {
		return nil, false
	}
	fileName = strings.TrimSpace(fileName)
	deleteByIdentity := make(map[string]model.CodexInspectionResult)
	for _, result := range results {
		if strings.TrimSpace(result.FileName) != fileName {
			continue
		}
		action := result.Action
		if actionFor != nil {
			action = actionFor(result)
		}
		if action != "delete" {
			return nil, false
		}
		deleteByIdentity[inspectionActionIdentityKey(result)] = result
	}
	if len(deleteByIdentity) == 0 {
		return nil, false
	}
	deleteResults := make([]model.CodexInspectionResult, 0, len(deleteByIdentity))
	for _, result := range deleteByIdentity {
		deleteResults = append(deleteResults, result)
	}
	used := make([]bool, len(deleteResults))
	members := make([]model.CodexInspectionResult, 0, len(current))
	for _, candidate := range current {
		matchIndex := -1
		for index, result := range deleteResults {
			if used[index] || !inspectionResultMatchesCurrentAccount(result, candidate) {
				continue
			}
			if matchIndex >= 0 {
				return nil, false
			}
			matchIndex = index
		}
		if matchIndex < 0 {
			return nil, false
		}
		used[matchIndex] = true
		members = append(members, deleteResults[matchIndex])
	}
	return members, true
}

func inspectionResultMatchesCurrentAccount(result model.CodexInspectionResult, current account) bool {
	if strings.TrimSpace(result.FileName) != strings.TrimSpace(current.FileName) {
		return false
	}
	provider := normalizeInspectionProvider(result.Provider)
	currentProvider := normalizeInspectionProvider(current.Provider)
	if provider == "" || currentProvider == "" || provider != currentProvider {
		return false
	}
	return inspectionIdentityMatchesAccount(
		provider,
		result.FileName,
		result.AuthIndex,
		result.AccountID,
		result.AccountSnapshot,
		current,
	)
}

func directInspectionAccountSnapshot(fileName, value string) string {
	snapshot := strings.TrimSpace(value)
	if snapshot == "" || snapshot == strings.TrimSpace(fileName) {
		return ""
	}
	return snapshot
}

func inspectionAccountSnapshot(provider, fileName, value string) string {
	snapshot := directInspectionAccountSnapshot(fileName, value)
	if normalizeInspectionProvider(provider) != "codex" {
		return snapshot
	}
	member, ok := usageidentity.NormalizeCodexMemberSnapshot(snapshot)
	if !ok {
		return ""
	}
	return member
}

func inspectionIdentityMatchesAccount(
	provider, fileName, authIndex, accountID, accountSnapshot string,
	candidate account,
) bool {
	if normalizeInspectionProvider(provider) == "codex" &&
		(candidate.AccountIDInvalid || candidate.AccountSnapshotInvalid) {
		return false
	}
	authIndex = strings.TrimSpace(authIndex)
	if authIndex != "" && authIndex != strings.TrimSpace(candidate.AuthIndex) {
		return false
	}
	accountID = strings.TrimSpace(accountID)
	member := inspectionAccountSnapshot(provider, fileName, accountSnapshot)
	if normalizeInspectionProvider(provider) == "codex" {
		candidateAccountID := strings.TrimSpace(candidate.AccountID)
		candidateMember := inspectionAccountSnapshot(candidate.Provider, candidate.FileName, candidate.AccountSnapshot)
		if accountID != "" {
			if candidateAccountID != "" && candidateAccountID != accountID {
				return false
			}
			if member != "" && candidateMember != "" && candidateMember != member {
				return false
			}
			return authIndex != ""
		}
		if member != "" {
			if candidateMember != "" && candidateMember != member {
				return false
			}
			return authIndex != ""
		}
		// A Workspace-only ownership record is not a recovery identity. An
		// auth-index match is still a credential-level fallback, but a wildcard
		// record must fail closed even when only one candidate is present.
		return authIndex != ""
	}
	if accountID != "" {
		return accountID == strings.TrimSpace(candidate.AccountID)
	}
	if member == "" {
		return authIndex != ""
	}
	return member == directInspectionAccountSnapshot(candidate.FileName, candidate.AccountSnapshot)
}

func matchCurrentAccount(candidates []account, result model.CodexInspectionResult) (account, bool) {
	matches := make([]account, 0, 1)
	for _, candidate := range candidates {
		if inspectionResultMatchesCurrentAccount(result, candidate) {
			matches = append(matches, candidate)
		}
	}
	if len(matches) != 1 {
		return account{}, false
	}
	return matches[0], true
}

func summarizeRun(run model.CodexInspectionRun, results []model.CodexInspectionResult) model.CodexInspectionRun {
	run.DisabledCount = 0
	run.EnabledCount = 0
	run.DeleteCount = 0
	run.DisableCount = 0
	run.EnableCount = 0
	run.ReauthCount = 0
	run.KeepCount = 0
	for _, result := range results {
		if result.Disabled {
			run.DisabledCount++
		} else {
			run.EnabledCount++
		}
		switch result.Action {
		case "delete":
			run.DeleteCount++
		case "disable":
			run.DisableCount++
		case "enable":
			run.EnableCount++
		case "reauth":
			run.ReauthCount++
		default:
			run.KeepCount++
		}
	}
	return run
}

func applyActionOutcomes(results []model.CodexInspectionResult, outcomes []ActionOutcome) []model.CodexInspectionResult {
	if len(outcomes) == 0 {
		return results
	}
	byKey := map[string]ActionOutcome{}
	for _, outcome := range outcomes {
		byKey[outcome.AccountKey] = outcome
	}
	out := make([]model.CodexInspectionResult, len(results))
	copy(out, results)
	for i := range out {
		outcome, ok := byKey[out[i].AccountKey]
		if !ok {
			continue
		}
		if outcome.CurrentDisabled != nil {
			out[i].Disabled = *outcome.CurrentDisabled
		}
		status := model.NormalizeCodexInspectionActionStatus(outcome.Status, out[i].Action)
		currentStatus := model.NormalizeCodexInspectionActionStatus(out[i].ActionStatus, out[i].Action)
		if currentStatus == model.CodexInspectionActionStatusSuccess && status == model.CodexInspectionActionStatusSkipped {
			continue
		}
		if status == model.CodexInspectionActionStatusPending {
			if outcome.Success {
				status = model.CodexInspectionActionStatusSuccess
			} else {
				status = model.CodexInspectionActionStatusFailed
			}
		}
		out[i].ActionStatus = status
		out[i].ActionError = outcome.Error
		out[i].ExecutedAction = ""
		if status == model.CodexInspectionActionStatusSuccess {
			out[i].ExecutedAction = outcome.Action
			out[i].ActionError = ""
			switch outcome.Action {
			case "disable":
				out[i].Disabled = true
			case "enable":
				out[i].Disabled = false
			}
		}
	}
	return out
}

func failedActionOutcome(item model.CodexInspectionResult, action string, message string) ActionOutcome {
	return ActionOutcome{
		ResultID:       item.ID,
		AccountKey:     item.AccountKey,
		FileName:       item.FileName,
		DisplayAccount: item.DisplayAccount,
		Action:         action,
		Status:         model.CodexInspectionActionStatusFailed,
		Success:        false,
		Error:          message,
	}
}

func needsReviewActionOutcome(item model.CodexInspectionResult, action string, message string) ActionOutcome {
	return ActionOutcome{
		ResultID:       item.ID,
		AccountKey:     item.AccountKey,
		FileName:       item.FileName,
		DisplayAccount: item.DisplayAccount,
		Action:         action,
		Status:         model.CodexInspectionActionStatusNeedsReview,
		Success:        true,
		Error:          message,
	}
}

func skippedActionOutcome(item model.CodexInspectionResult, action string, message string) ActionOutcome {
	return ActionOutcome{
		ResultID:       item.ID,
		AccountKey:     item.AccountKey,
		FileName:       item.FileName,
		DisplayAccount: item.DisplayAccount,
		Action:         action,
		Status:         model.CodexInspectionActionStatusSkipped,
		Success:        true,
		Error:          message,
	}
}

type actionOutcomeSummary struct {
	Success     int
	Failed      int
	Skipped     int
	NeedsReview int
}

func summarizeActionOutcomes(outcomes []ActionOutcome) actionOutcomeSummary {
	summary := actionOutcomeSummary{}
	for _, outcome := range outcomes {
		switch outcome.Status {
		case model.CodexInspectionActionStatusSuccess:
			summary.Success++
		case model.CodexInspectionActionStatusFailed:
			summary.Failed++
		case model.CodexInspectionActionStatusSkipped:
			summary.Skipped++
		case model.CodexInspectionActionStatusNeedsReview:
			summary.NeedsReview++
		default:
			if outcome.Success {
				summary.Success++
			} else {
				summary.Failed++
			}
		}
	}
	return summary
}

func logPreflightActionOutcomes(
	ctx context.Context,
	logger runLogger,
	prefix string,
	outcomes []ActionOutcome,
) {
	for _, outcome := range outcomes {
		level := "info"
		message := prefix + "账号跳过"
		if outcome.Status == model.CodexInspectionActionStatusNeedsReview {
			level = "warning"
		}
		if outcome.Status == model.CodexInspectionActionStatusFailed || !outcome.Success {
			level = "error"
			message = prefix + "账号失败"
		}
		logger.log(ctx, level, message, map[string]any{
			"fileName":       outcome.FileName,
			"displayAccount": outcome.DisplayAccount,
			"action":         outcome.Action,
			"status":         outcome.Status,
			"reason":         outcome.Error,
		})
	}
}

func (s *Service) persistInspectionResults(
	ctx context.Context,
	runID int64,
	results []model.CodexInspectionResult,
	logger runLogger,
) int {
	// Probe workers only perform network work. Persist their results serially
	// here so each account is written once per lifecycle phase and SQLite does
	// not receive a burst of concurrent upserts.
	if ctx == nil {
		ctx = context.Background()
	}
	// A cancelled inspection may have a large partial result set. Keep its final
	// lifecycle transition bounded, but never impose a fixed whole-batch budget
	// on a healthy run: large successful inspections must persist every result.
	startedCancelled := ctx.Err() != nil
	persistCtx := context.WithoutCancel(ctx)
	persistCancel := func() {}
	if startedCancelled {
		persistCtx, persistCancel = context.WithTimeout(persistCtx, cancelledPersistTimeout)
	}
	defer persistCancel()
	failures := 0
	for index, result := range results {
		if !startedCancelled && ctx.Err() != nil {
			remaining := len(results) - index
			failures += remaining
			logger.error(ctx, "巡检取消后停止写入剩余账号结果", map[string]any{
				"remainingCount": remaining,
				"error":          ctx.Err().Error(),
			})
			break
		}
		if err := persistCtx.Err(); err != nil {
			remaining := len(results) - index
			failures += remaining
			logger.error(ctx, "巡检结果持久化时间预算已耗尽", map[string]any{
				"remainingCount": remaining,
				"error":          err.Error(),
			})
			break
		}
		result.RunID = runID
		writeCtx, cancel := context.WithTimeout(persistCtx, resultWriteTimeout)
		stored, err := s.store.InsertCodexInspectionResult(writeCtx, result)
		cancel()
		if err != nil {
			failures++
			logger.error(ctx, "写入巡检账号结果失败", map[string]any{
				"fileName":       result.FileName,
				"displayAccount": result.DisplayAccount,
				"retryScheduled": true,
				"error":          err.Error(),
			})
			continue
		}
		results[index] = stored
		snapshotCtx, snapshotCancel := context.WithTimeout(persistCtx, resultWriteTimeout)
		snapshotErr := s.quotaSnapshots.WriteCodexInspectionResult(snapshotCtx, stored)
		snapshotCancel()
		if snapshotErr != nil {
			failures++
			logger.warning(ctx, "写入巡检额度快照失败", map[string]any{
				"fileName":       result.FileName,
				"displayAccount": result.DisplayAccount,
				"retryScheduled": true,
				"error":          snapshotErr.Error(),
			})
		}
	}
	return failures
}

func (s *Service) getRunWithResultFallback(
	ctx context.Context,
	runID int64,
	latestResults []model.CodexInspectionResult,
	useFallback bool,
) (RunDetail, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	// Callers already detach request cancellation where lifecycle persistence
	// must continue. Preserve any shorter caller deadline so cancellation and
	// shutdown paths cannot accidentally receive a fresh full read budget here.
	readCtx, cancelRead := context.WithTimeout(ctx, criticalWriteTimeout)
	defer cancelRead()
	detail, err := s.GetRun(readCtx, runID)
	if err != nil || !useFallback {
		return detail, err
	}
	detail.Results = overlayInspectionResultSnapshots(runID, detail.Results, latestResults)
	return detail, nil
}

func overlayInspectionResultSnapshots(
	runID int64,
	persisted []model.CodexInspectionResult,
	latest []model.CodexInspectionResult,
) []model.CodexInspectionResult {
	persistedByAccount := make(map[string]model.CodexInspectionResult, len(persisted))
	for _, result := range persisted {
		persistedByAccount[result.AccountKey] = result
	}

	overlaid := make([]model.CodexInspectionResult, len(latest))
	for index, result := range latest {
		result.RunID = runID
		result.ActionStatus = model.NormalizeCodexInspectionActionStatus(result.ActionStatus, result.Action)
		if stored, ok := persistedByAccount[result.AccountKey]; ok {
			if result.ID <= 0 {
				result.ID = stored.ID
			}
			if result.CreatedAtMS <= 0 {
				result.CreatedAtMS = stored.CreatedAtMS
			}
		}
		overlaid[index] = result
	}
	return overlaid
}

func failedActionOutcomes(outcomes []ActionOutcome) []map[string]any {
	failed := make([]map[string]any, 0)
	for _, outcome := range outcomes {
		if outcome.Success {
			continue
		}
		failed = append(failed, map[string]any{
			"fileName":       outcome.FileName,
			"displayAccount": outcome.DisplayAccount,
			"action":         outcome.Action,
			"error":          outcome.Error,
		})
	}
	return failed
}

func resultFromAccount(item account) model.CodexInspectionResult {
	return model.CodexInspectionResult{
		AccountKey:      item.Key,
		FileName:        item.FileName,
		DisplayAccount:  item.DisplayAccount,
		AccountSnapshot: item.AccountSnapshot,
		AuthIndex:       item.AuthIndex,
		AccountID:       item.AccountID,
		Provider:        item.Provider,
		Disabled:        item.Disabled,
		Status:          item.Status,
		State:           item.State,
		PlanType:        resolveCodexPlanType(item.File),
		Action:          "keep",
		ActionReason:    "无需处理",
		IsQuota:         false,
	}
}

func pickSample(items []account, sampleSize int) []account {
	if sampleSize <= 0 || sampleSize >= len(items) {
		out := make([]account, len(items))
		copy(out, items)
		return out
	}
	out := make([]account, len(items))
	copy(out, items)
	rand.Shuffle(len(out), func(i, j int) {
		out[i], out[j] = out[j], out[i]
	})
	return out[:sampleSize]
}

// pickSamplePerProvider applies the configured sample size independently to
// each selected provider. This prevents a combined Codex+xAI run from randomly
// sampling only one provider and leaving the other without health evidence.
func pickSamplePerProvider(items []account, sampleSize int) []account {
	if sampleSize <= 0 {
		out := make([]account, len(items))
		copy(out, items)
		return out
	}

	groups := make(map[string][]account)
	providerOrder := make([]string, 0)
	for _, item := range items {
		if _, ok := groups[item.Provider]; !ok {
			providerOrder = append(providerOrder, item.Provider)
		}
		groups[item.Provider] = append(groups[item.Provider], item)
	}

	result := make([]account, 0, len(items))
	for _, provider := range providerOrder {
		result = append(result, pickSample(groups[provider], sampleSize)...)
	}
	return result
}

func countAccounts(items []account, disabled bool) int {
	count := 0
	for _, item := range items {
		if item.Disabled == disabled {
			count++
		}
	}
	return count
}

func toAccount(file authFile) account {
	parsed := cpaauthfiles.FromMap(map[string]any(file))
	fileName := firstNonEmpty(parsed.Name, normalizeAuthIndex(file["auth_index"]), normalizeAuthIndex(file["authIndex"]), "unknown-auth-file")
	authIndex := parsed.AuthIndex
	provider := parsed.Provider
	runtimeID := parsed.ID
	displaySnapshot := firstNonEmpty(
		readString(file, "account"),
		readString(file, "email"),
		readString(file, "display_account"),
		readString(file, "displayAccount"),
	)
	accountSnapshot := parsed.AccountSnapshot
	accountSnapshotInvalid := parsed.AccountSnapshotInvalid
	displayAccount := firstNonEmpty(
		displaySnapshot,
		readString(file, "label"),
		fileName,
	)
	accountID := parsed.AccountID
	accountIDInvalid := parsed.AccountIDInvalid
	key := fileName + "::" + authIndex
	if authIndex == "" {
		switch {
		case accountSnapshot != "" || (provider != "codex" && accountID != ""):
			key = fileName + "::-::" + inspectionIdentityKey(fileName, provider, authIndex, accountID, accountSnapshot)
		case strings.TrimSpace(runtimeID) != "" && strings.TrimSpace(runtimeID) != strings.TrimSpace(fileName):
			encoded, _ := json.Marshal([]string{provider, strings.TrimSpace(runtimeID)})
			key = fileName + "::-::runtime:" + string(encoded)
		default:
			key = fileName + "::-"
		}
	}
	return account{
		Key:                    key,
		RuntimeID:              runtimeID,
		FileName:               fileName,
		DisplayAccount:         displayAccount,
		AccountSnapshot:        accountSnapshot,
		AccountSnapshotInvalid: accountSnapshotInvalid,
		AuthIndex:              authIndex,
		AccountID:              accountID,
		AccountIDInvalid:       accountIDInvalid,
		Provider:               provider,
		Disabled:               isDisabledAuthFile(file),
		Status:                 readString(file, "status"),
		State:                  readString(file, "state"),
		File:                   file,
	}
}

func normalizeInspectionProvider(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	normalized = strings.ReplaceAll(normalized, "_", "-")
	switch normalized {
	case "x-ai", "grok":
		return "xai"
	default:
		return normalized
	}
}

func resolveCodexPlanType(file authFile) string {
	metadata := readMap(file, "metadata")
	attributes := readMap(file, "attributes")
	candidates := []any{
		file["plan_type"],
		file["planType"],
		extractCodexPlanTypeFromToken(file["id_token"]),
		readMap(file, "id_token"),
		metadata["plan_type"],
		metadata["planType"],
		extractCodexPlanTypeFromToken(metadata["id_token"]),
		readMap(metadata, "id_token"),
		attributes["plan_type"],
		attributes["planType"],
		extractCodexPlanTypeFromToken(attributes["id_token"]),
	}
	for _, candidate := range candidates {
		if planType := readCodexPlanTypeCandidate(candidate); planType != "" {
			return planType
		}
	}
	return ""
}

func extractCodexPlanTypeFromToken(value any) string {
	payload := parseIDTokenPayload(value)
	if payload == nil {
		return ""
	}
	return readCodexPlanTypeCandidate(payload)
}

func readCodexPlanTypeCandidate(value any) string {
	if value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return normalizeCodexPlanType(typed)
	case map[string]any:
		return normalizeCodexPlanType(readString(typed, "plan_type", "planType"))
	default:
		return normalizeCodexPlanType(fmt.Sprint(value))
	}
}

func parseIDTokenPayload(value any) map[string]any {
	switch typed := value.(type) {
	case map[string]any:
		return typed
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return nil
		}
		var parsed map[string]any
		if err := json.Unmarshal([]byte(trimmed), &parsed); err == nil {
			return parsed
		}
		segments := strings.Split(trimmed, ".")
		if len(segments) < 2 {
			return nil
		}
		decoded, err := base64.RawURLEncoding.DecodeString(segments[1])
		if err != nil {
			decoded, err = base64.URLEncoding.DecodeString(padBase64(segments[1]))
			if err != nil {
				return nil
			}
		}
		if err := json.Unmarshal(decoded, &parsed); err == nil {
			return parsed
		}
	}
	return nil
}

func parseRateLimit(raw map[string]any) *codexRateLimit {
	if raw == nil {
		return nil
	}
	limit := &codexRateLimit{
		PrimaryWindow:   parseWindow(readMap(raw, "primary_window", "primaryWindow")),
		SecondaryWindow: parseWindow(readMap(raw, "secondary_window", "secondaryWindow")),
	}
	if value, ok := readBoolPtr(raw, "allowed"); ok {
		limit.Allowed = value
	}
	limit.LimitReached = readBool(raw, "limit_reached", "limitReached")
	return limit
}

func parseWindow(raw map[string]any) *codexWindow {
	if raw == nil {
		return nil
	}
	window := &codexWindow{}
	if value, ok := readNumberPtr(raw, "used_percent", "usedPercent"); ok {
		window.UsedPercent = value
	}
	if value, ok := readNumberPtr(raw, "limit_window_seconds", "limitWindowSeconds"); ok {
		window.LimitWindowSeconds = value
	}
	if value, ok := readNumberPtr(raw, "reset_after_seconds", "resetAfterSeconds"); ok {
		window.ResetAfterSeconds = value
	}
	if value, ok := readNumberPtr(raw, "reset_at", "resetAt"); ok {
		window.ResetAt = value
	}
	return window
}

func classifyWindows(limit *codexRateLimit, planType string) codexClassifiedWindows {
	if limit == nil {
		return codexClassifiedWindows{}
	}
	teamPlan := normalizeCodexPlanType(planType) == "team"
	raw := []*codexWindow{limit.PrimaryWindow, limit.SecondaryWindow}
	var fiveHour *codexWindow
	var weekly *codexWindow
	var monthly *codexWindow
	var genericLong *codexWindow
	for _, window := range raw {
		if window == nil || window.LimitWindowSeconds == nil {
			continue
		}
		seconds := int(math.Round(*window.LimitWindowSeconds))
		if seconds == codexFiveHourWindow && fiveHour == nil {
			fiveHour = window
		} else if seconds == codexWeekWindow && weekly == nil {
			weekly = window
		} else if (seconds == codexMonthWindow || isCodexMonthlyWindowSeconds(seconds)) && monthly == nil {
			monthly = window
		} else if seconds > codexFiveHourWindow && genericLong == nil {
			genericLong = window
		}
	}
	if fiveHour == nil && limit.PrimaryWindow != weekly && limit.PrimaryWindow != monthly && limit.PrimaryWindow != genericLong && !hasExplicitWindowSeconds(limit.PrimaryWindow) {
		fiveHour = limit.PrimaryWindow
	}
	if teamPlan {
		if monthly == nil && limit.SecondaryWindow != fiveHour && !hasExplicitWindowSeconds(limit.SecondaryWindow) {
			monthly = limit.SecondaryWindow
		}
	} else if weekly == nil && limit.SecondaryWindow != fiveHour && !hasExplicitWindowSeconds(limit.SecondaryWindow) {
		weekly = limit.SecondaryWindow
	}
	return codexClassifiedWindows{FiveHour: fiveHour, Weekly: weekly, Monthly: monthly, GenericLong: genericLong}
}

func isCodexMonthlyWindowSeconds(seconds int) bool {
	return seconds >= codexMinMonthWindow && seconds <= codexMaxMonthWindow
}

func buildCodexInspectionQuotaWindows(payload map[string]any, planType string) []model.CodexInspectionQuotaWindow {
	if payload == nil {
		return nil
	}
	teamPlan := normalizeCodexPlanType(firstNonEmpty(planType, readString(payload, "plan_type", "planType"))) == "team"
	windows := make([]model.CodexInspectionQuotaWindow, 0)
	addCodexRateLimitWindows(
		&windows,
		parseRateLimit(readMap(payload, "rate_limit", "rateLimit")),
		codexWindowMeta{ID: "five-hour", LabelKey: "codex_quota.primary_window"},
		codexWindowMeta{ID: "weekly", LabelKey: "codex_quota.secondary_window"},
		codexWindowMeta{ID: "monthly", LabelKey: "codex_quota.monthly_window"},
		"",
		"codex_quota.generic_window",
		nil,
		teamPlan,
		codexquota.MainScope(),
	)
	addCodexRateLimitWindows(
		&windows,
		parseRateLimit(readMap(payload, "code_review_rate_limit", "codeReviewRateLimit")),
		codexWindowMeta{ID: "code-review-five-hour", LabelKey: "codex_quota.code_review_primary_window"},
		codexWindowMeta{ID: "code-review-weekly", LabelKey: "codex_quota.code_review_secondary_window"},
		codexWindowMeta{ID: "code-review-monthly", LabelKey: "codex_quota.code_review_monthly_window"},
		"code-review",
		"codex_quota.code_review_generic_window",
		nil,
		teamPlan,
		codexquota.CodeReviewScope(),
	)
	addAdditionalRateLimitWindows(&windows, readMapSlice(payload, "additional_rate_limits", "additionalRateLimits"), teamPlan)
	return windows
}

func codexQuotaInventoryObserved(payload map[string]any) bool {
	if payload == nil {
		return false
	}
	for _, key := range []string{"rate_limit", "rateLimit", "code_review_rate_limit", "codeReviewRateLimit"} {
		raw, exists := payload[key]
		if !exists {
			continue
		}
		if _, ok := raw.(map[string]any); ok {
			return true
		}
	}
	for _, key := range []string{"additional_rate_limits", "additionalRateLimits"} {
		raw, exists := payload[key]
		if !exists {
			continue
		}
		switch raw.(type) {
		case []any, []map[string]any:
			return true
		}
	}
	return false
}

func addCodexRateLimitWindows(
	windows *[]model.CodexInspectionQuotaWindow,
	limit *codexRateLimit,
	fiveHourMeta codexWindowMeta,
	weeklyMeta codexWindowMeta,
	monthlyMeta codexWindowMeta,
	genericIDPrefix string,
	genericLabelKey string,
	genericLabelParams map[string]any,
	teamPlan bool,
	modelScope codexquota.ModelScope,
) {
	if limit == nil {
		return
	}
	classified := classifyWindows(limit, codexPlanTypeForTeam(teamPlan))
	added := make(map[*codexWindow]bool)
	addCodexWindowInfo(windows, fiveHourMeta.ID, fiveHourMeta.LabelKey, genericLabelParams, classified.FiveHour, limit.LimitReached, limit.Allowed, modelScope)
	if classified.FiveHour != nil {
		added[classified.FiveHour] = true
	}
	addCodexWindowInfo(windows, weeklyMeta.ID, weeklyMeta.LabelKey, genericLabelParams, classified.Weekly, limit.LimitReached, limit.Allowed, modelScope)
	if classified.Weekly != nil {
		added[classified.Weekly] = true
	}
	addCodexWindowInfo(windows, monthlyMeta.ID, monthlyMeta.LabelKey, genericLabelParams, classified.Monthly, limit.LimitReached, limit.Allowed, modelScope)
	if classified.Monthly != nil {
		added[classified.Monthly] = true
	}
	for index, window := range codexRateLimitWindows(limit) {
		if window == nil || added[window] {
			continue
		}
		duration := formatCodexWindowDuration(window.LimitWindowSeconds)
		prefix := normalizeCodexWindowID(genericIDPrefix)
		if prefix != "" {
			prefix += "-"
		}
		addCodexWindowInfo(
			windows,
			fmt.Sprintf("%swindow-%s-%d", prefix, duration, index),
			genericLabelKey,
			withCodexWindowDurationParam(genericLabelParams, duration),
			window,
			limit.LimitReached,
			limit.Allowed,
			modelScope,
		)
	}
}

func codexPlanTypeForTeam(teamPlan bool) string {
	if teamPlan {
		return "team"
	}
	return ""
}

func codexRateLimitWindows(limit *codexRateLimit) []*codexWindow {
	if limit == nil {
		return nil
	}
	return []*codexWindow{limit.PrimaryWindow, limit.SecondaryWindow}
}

func addCodexWindowInfo(
	windows *[]model.CodexInspectionQuotaWindow,
	id string,
	labelKey string,
	labelParams map[string]any,
	window *codexWindow,
	limitReached bool,
	allowed *bool,
	modelScope codexquota.ModelScope,
) {
	if window == nil {
		return
	}
	observedAt := time.Now()
	resetAtMS, resetAccuracy := resolveCodexInspectionReset(window, observedAt)
	resetLabel := formatCodexResetLabelAt(window, observedAt)
	usedPercent := window.UsedPercent
	if usedPercent == nil && (limitReached || (allowed != nil && !*allowed)) && resetLabel != "-" {
		usedPercent = ptrFloat(100)
	}
	*windows = append(*windows, model.CodexInspectionQuotaWindow{
		ID:                 id,
		LabelKey:           labelKey,
		LabelParams:        copyCodexLabelParams(labelParams),
		UsedPercent:        usedPercent,
		ResetLabel:         resetLabel,
		ResetAtMS:          resetAtMS,
		ResetAccuracy:      resetAccuracy,
		LimitWindowSeconds: window.LimitWindowSeconds,
		ModelScope:         codexInspectionQuotaModelScope(modelScope),
	})
}

func codexInspectionQuotaModelScope(scope codexquota.ModelScope) *model.CodexInspectionQuotaModelScope {
	return &model.CodexInspectionQuotaModelScope{
		Kind:     scope.Kind,
		Key:      scope.Key,
		Models:   append([]string(nil), scope.Models...),
		Complete: scope.Complete,
	}
}

func addAdditionalRateLimitWindows(windows *[]model.CodexInspectionQuotaWindow, additionalRateLimits []map[string]any, teamPlan bool) {
	families := make([]codexAdditionalRateLimitFamily, 0, len(additionalRateLimits))
	baseIDPrefixCounts := make(map[string]int)
	legacyBaseIDPrefixCounts := make(map[string]int)
	for index, limitItem := range additionalRateLimits {
		rateInfo := parseRateLimit(readMap(limitItem, "rate_limit", "rateLimit"))
		if rateInfo == nil {
			continue
		}
		meteredFeature := readString(limitItem, "metered_feature", "meteredFeature")
		limitName := firstNonEmpty(
			readString(limitItem, "limit_name", "limitName"),
			meteredFeature,
			fmt.Sprintf("additional-%d", index+1),
		)
		scopeResolution := codexquota.ResolveAdditionalScope(codexquota.AdditionalScopeInput{
			MeteredFeature:    meteredFeature,
			LimitName:         readString(limitItem, "limit_name", "limitName"),
			AnonymousIdentity: anonymousCodexAdditionalIdentity(rateInfo),
		})
		legacyBaseIDPrefix := normalizeCodexWindowID(limitName)
		if legacyBaseIDPrefix == "" {
			legacyBaseIDPrefix = fmt.Sprintf("additional-%d", index+1)
		}
		families = append(families, codexAdditionalRateLimitFamily{
			sourceIndex:        index,
			rateInfo:           rateInfo,
			limitName:          limitName,
			modelScope:         scopeResolution.Scope,
			baseIDPrefix:       scopeResolution.ProviderWindowPrefix,
			featureIDPrefix:    normalizeCodexWindowID(meteredFeature),
			legacyBaseIDPrefix: legacyBaseIDPrefix,
			legacyPrefixes:     append([]string(nil), scopeResolution.LegacyPrefixes...),
			sortKey:            codexAdditionalRateLimitSortKey(rateInfo),
		})
		baseIDPrefixCounts[scopeResolution.ProviderWindowPrefix]++
		legacyBaseIDPrefixCounts[legacyBaseIDPrefix]++
	}

	familiesByIDPrefix := make(map[string][]int)
	for index := range families {
		family := &families[index]
		family.idPrefix = family.baseIDPrefix
		if baseIDPrefixCounts[family.baseIDPrefix] > 1 && family.featureIDPrefix != "" && family.featureIDPrefix != family.baseIDPrefix {
			family.idPrefix += "--" + family.featureIDPrefix
		}
		legacyPrefix := family.legacyBaseIDPrefix
		if legacyBaseIDPrefixCounts[legacyPrefix] > 1 && family.featureIDPrefix != "" && family.featureIDPrefix != legacyPrefix {
			legacyPrefix += "--" + family.featureIDPrefix
		}
		family.legacyPrefixes = append(family.legacyPrefixes, legacyPrefix)
		familiesByIDPrefix[family.idPrefix] = append(familiesByIDPrefix[family.idPrefix], index)
	}
	familyIndexes := make(map[int]int, len(families))
	for _, indexes := range familiesByIDPrefix {
		sort.SliceStable(indexes, func(left, right int) bool {
			leftFamily := families[indexes[left]]
			rightFamily := families[indexes[right]]
			if leftFamily.sortKey != rightFamily.sortKey {
				return leftFamily.sortKey < rightFamily.sortKey
			}
			return leftFamily.sourceIndex < rightFamily.sourceIndex
		})
		for familyIndex, index := range indexes {
			familyIndexes[index] = familyIndex
		}
	}

	for index, family := range families {
		familyIndex := familyIndexes[index]
		start := len(*windows)
		addCodexRateLimitWindows(
			windows,
			family.rateInfo,
			codexWindowMeta{ID: fmt.Sprintf("%s-five-hour-%d", family.idPrefix, familyIndex), LabelKey: "codex_quota.additional_primary_window"},
			codexWindowMeta{ID: fmt.Sprintf("%s-weekly-%d", family.idPrefix, familyIndex), LabelKey: "codex_quota.additional_secondary_window"},
			codexWindowMeta{ID: fmt.Sprintf("%s-monthly-%d", family.idPrefix, familyIndex), LabelKey: "codex_quota.additional_monthly_window"},
			fmt.Sprintf("%s-%d", family.idPrefix, familyIndex),
			"codex_quota.additional_generic_window",
			map[string]any{"name": family.limitName},
			teamPlan,
			family.modelScope,
		)
		applyCodexProviderWindowAliases((*windows)[start:], family.idPrefix, family.legacyPrefixes)
	}
}

func applyCodexProviderWindowAliases(
	windows []model.CodexInspectionQuotaWindow,
	providerWindowPrefix string,
	legacyPrefixes []string,
) {
	aliases := make([]string, 0, len(legacyPrefixes))
	seen := make(map[string]struct{}, len(legacyPrefixes))
	for _, prefix := range legacyPrefixes {
		prefix = strings.ToLower(strings.TrimSpace(prefix))
		if prefix == "" || prefix == providerWindowPrefix {
			continue
		}
		if _, exists := seen[prefix]; exists {
			continue
		}
		seen[prefix] = struct{}{}
		aliases = append(aliases, prefix)
	}
	sort.Strings(aliases)
	for index := range windows {
		if !strings.HasPrefix(windows[index].ID, providerWindowPrefix) {
			continue
		}
		suffix := strings.TrimPrefix(windows[index].ID, providerWindowPrefix)
		windows[index].ProviderWindowAliases = make([]string, 0, len(aliases))
		for _, prefix := range aliases {
			windows[index].ProviderWindowAliases = append(
				windows[index].ProviderWindowAliases,
				prefix+suffix,
			)
		}
	}
}

func anonymousCodexAdditionalIdentity(rateInfo *codexRateLimit) string {
	if rateInfo == nil {
		return "additional-p-none-s-none"
	}
	return strings.Join([]string{
		"additional",
		"p",
		codexAdditionalWindowIdentityPart(rateInfo.PrimaryWindow),
		"s",
		codexAdditionalWindowIdentityPart(rateInfo.SecondaryWindow),
	}, "-")
}

func codexAdditionalWindowIdentityPart(window *codexWindow) string {
	if window == nil {
		return "none"
	}
	if window.LimitWindowSeconds == nil {
		return "unknown"
	}
	return fmt.Sprintf("%g", *window.LimitWindowSeconds)
}

func codexAdditionalRateLimitSortKey(rateInfo *codexRateLimit) string {
	if rateInfo == nil {
		return ""
	}
	allowed := ""
	if rateInfo.Allowed != nil {
		allowed = fmt.Sprintf("%t", *rateInfo.Allowed)
	}
	return strings.Join([]string{
		codexAdditionalWindowSortKey(rateInfo.PrimaryWindow),
		codexAdditionalWindowSortKey(rateInfo.SecondaryWindow),
		allowed,
		fmt.Sprintf("%t", rateInfo.LimitReached),
	}, "|")
}

func codexAdditionalWindowSortKey(window *codexWindow) string {
	if window == nil {
		return "none"
	}
	values := []*float64{
		window.LimitWindowSeconds,
		window.UsedPercent,
		window.ResetAfterSeconds,
		window.ResetAt,
	}
	parts := make([]string, 0, len(values))
	for _, value := range values {
		if value == nil {
			parts = append(parts, "")
			continue
		}
		parts = append(parts, fmt.Sprintf("%g", *value))
	}
	return strings.Join(parts, ":")
}

func readMapSlice(record map[string]any, keys ...string) []map[string]any {
	value, ok := firstValue(record, keys...)
	if !ok || value == nil {
		return nil
	}
	switch typed := value.(type) {
	case []map[string]any:
		return typed
	case []any:
		items := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			if record, ok := item.(map[string]any); ok {
				items = append(items, record)
			}
		}
		return items
	}
	return nil
}

func formatCodexResetLabel(window *codexWindow) string {
	return formatCodexResetLabelAt(window, time.Now())
}

func formatCodexResetLabelAt(window *codexWindow, observedAt time.Time) string {
	if window == nil {
		return "-"
	}
	if window.ResetAt != nil && *window.ResetAt > 0 {
		return formatUnixSeconds(*window.ResetAt)
	}
	if window.ResetAfterSeconds != nil && *window.ResetAfterSeconds > 0 {
		targetSeconds := float64(observedAt.Unix()) + math.Floor(*window.ResetAfterSeconds)
		return formatUnixSeconds(targetSeconds)
	}
	return "-"
}

func resolveCodexInspectionReset(window *codexWindow, observedAt time.Time) (int64, string) {
	if window == nil {
		return 0, "unknown"
	}
	if window.ResetAt != nil && *window.ResetAt > 0 {
		return int64(math.Floor(*window.ResetAt)) * 1000, "exact"
	}
	if window.ResetAfterSeconds != nil && *window.ResetAfterSeconds > 0 {
		return observedAt.Add(time.Duration(*window.ResetAfterSeconds * float64(time.Second))).UnixMilli(), "derived"
	}
	return 0, "unknown"
}

func formatUnixSeconds(seconds float64) string {
	if seconds <= 0 {
		return "-"
	}
	unixSeconds := int64(math.Floor(seconds))
	if unixSeconds <= 0 {
		return "-"
	}
	return time.Unix(unixSeconds, 0).Local().Format("01/02 15:04")
}

func formatCodexWindowDuration(seconds *float64) string {
	if seconds == nil || *seconds <= 0 {
		return "unknown"
	}
	rounded := int(math.Round(*seconds))
	const daySeconds = 86_400
	const hourSeconds = 3_600
	if rounded%daySeconds == 0 {
		return fmt.Sprintf("%dd", rounded/daySeconds)
	}
	if rounded%hourSeconds == 0 {
		return fmt.Sprintf("%dh", rounded/hourSeconds)
	}
	return fmt.Sprintf("%ds", rounded)
}

func normalizeCodexWindowID(raw string) string {
	trimmed := strings.ToLower(strings.TrimSpace(raw))
	if trimmed == "" {
		return ""
	}
	var builder strings.Builder
	lastDash := false
	for _, char := range trimmed {
		isAlphaNumeric := (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9')
		if isAlphaNumeric {
			builder.WriteRune(char)
			lastDash = false
			continue
		}
		if !lastDash && builder.Len() > 0 {
			builder.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(builder.String(), "-")
}

func copyCodexLabelParams(params map[string]any) map[string]any {
	if len(params) == 0 {
		return nil
	}
	out := make(map[string]any, len(params))
	for key, value := range params {
		out[key] = value
	}
	return out
}

func withCodexWindowDurationParam(params map[string]any, duration string) map[string]any {
	out := copyCodexLabelParams(params)
	if out == nil {
		out = map[string]any{}
	}
	out["duration"] = duration
	return out
}

func normalizeCodexPlanType(value string) string {
	normalized := strings.TrimSpace(strings.ToLower(value))
	normalized = strings.ReplaceAll(normalized, "_", "-")
	return normalized
}

func hasExplicitWindowSeconds(window *codexWindow) bool {
	return window != nil && window.LimitWindowSeconds != nil
}

func deriveRateLimitUsedPercent(limit *codexRateLimit) *float64 {
	if limit == nil {
		return nil
	}
	var values []float64
	for _, window := range []*codexWindow{limit.PrimaryWindow, limit.SecondaryWindow} {
		if window != nil && window.UsedPercent != nil {
			values = append(values, *window.UsedPercent)
		}
	}
	if len(values) == 0 {
		return nil
	}
	max := values[0]
	for _, value := range values[1:] {
		if value > max {
			max = value
		}
	}
	return &max
}

func isRateLimitReached(limit *codexRateLimit) bool {
	if limit == nil {
		return false
	}
	if limit.Allowed != nil && !*limit.Allowed {
		return true
	}
	if limit.LimitReached {
		return true
	}
	for _, window := range []*codexWindow{limit.PrimaryWindow, limit.SecondaryWindow} {
		if window != nil && window.UsedPercent != nil && *window.UsedPercent >= 100 {
			return true
		}
	}
	return false
}

func normalizeBody(input any) (string, any) {
	if input == nil {
		return "", nil
	}
	if text, ok := input.(string); ok {
		trimmed := strings.TrimSpace(text)
		if trimmed == "" {
			return text, nil
		}
		var parsed any
		if err := json.Unmarshal([]byte(trimmed), &parsed); err == nil {
			return text, parsed
		}
		return text, text
	}
	data, err := json.Marshal(input)
	if err != nil {
		return fmt.Sprint(input), input
	}
	return string(data), input
}

func parseRecord(input any) map[string]any {
	switch typed := input.(type) {
	case map[string]any:
		return typed
	case string:
		var parsed map[string]any
		if err := json.Unmarshal([]byte(strings.TrimSpace(typed)), &parsed); err == nil {
			return parsed
		}
	}
	return nil
}

func readMap(record map[string]any, keys ...string) map[string]any {
	for _, key := range keys {
		value, ok := record[key]
		if !ok || value == nil {
			continue
		}
		if typed, ok := value.(map[string]any); ok {
			return typed
		}
	}
	return nil
}

func firstValue(record map[string]any, keys ...string) (any, bool) {
	for _, key := range keys {
		value, ok := record[key]
		if ok {
			return value, true
		}
	}
	return nil, false
}

func readString(record map[string]any, keys ...string) string {
	for _, key := range keys {
		value, ok := record[key]
		if !ok || value == nil {
			continue
		}
		text := strings.TrimSpace(fmt.Sprint(value))
		if text != "" {
			return text
		}
	}
	return ""
}

func normalizeAuthIndex(value any) string {
	if value == nil {
		return ""
	}
	switch typed := value.(type) {
	case float64:
		if math.Trunc(typed) == typed {
			return fmt.Sprintf("%.0f", typed)
		}
	case int:
		return fmt.Sprint(typed)
	case int64:
		return fmt.Sprint(typed)
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func isDisabledAuthFile(file authFile) bool {
	status := strings.ToLower(firstNonEmpty(readString(file, "status"), readString(file, "state")))
	if status == "disabled" || status == "inactive" {
		return true
	}
	value, ok := file["disabled"]
	if !ok || value == nil {
		return false
	}
	switch typed := value.(type) {
	case bool:
		return typed
	case float64:
		return typed != 0
	case string:
		normalized := strings.ToLower(strings.TrimSpace(typed))
		return normalized == "true" || normalized == "1"
	default:
		return false
	}
}

func readBool(record map[string]any, keys ...string) bool {
	for _, key := range keys {
		value, ok := record[key]
		if !ok {
			continue
		}
		switch typed := value.(type) {
		case bool:
			return typed
		case string:
			normalized := strings.ToLower(strings.TrimSpace(typed))
			return normalized == "true" || normalized == "1" || normalized == "yes" || normalized == "on"
		case float64:
			return typed != 0
		}
	}
	return false
}

func readBoolPtr(record map[string]any, keys ...string) (*bool, bool) {
	for _, key := range keys {
		value, ok := record[key]
		if !ok || value == nil {
			continue
		}
		switch typed := value.(type) {
		case bool:
			return &typed, true
		case string:
			normalized := strings.ToLower(strings.TrimSpace(typed))
			if normalized == "true" || normalized == "1" || normalized == "yes" || normalized == "on" {
				result := true
				return &result, true
			}
			if normalized == "false" || normalized == "0" || normalized == "no" || normalized == "off" {
				result := false
				return &result, true
			}
		case float64:
			result := typed != 0
			return &result, true
		}
	}
	return nil, false
}

func readNumberPtr(record map[string]any, keys ...string) (*float64, bool) {
	for _, key := range keys {
		value, ok := record[key]
		if !ok || value == nil {
			continue
		}
		switch typed := value.(type) {
		case float64:
			return &typed, true
		case int:
			value := float64(typed)
			return &value, true
		case string:
			parsed, err := strconvParseFloat(typed)
			if err == nil {
				return &parsed, true
			}
		}
	}
	return nil, false
}

func readFloat(value any, fallback float64) float64 {
	switch typed := value.(type) {
	case float64:
		return typed
	case int:
		return float64(typed)
	case string:
		if parsed, err := strconvParseFloat(typed); err == nil {
			return parsed
		}
	}
	return fallback
}

func strconvParseFloat(value string) (float64, error) {
	return strconvParseFloat64(strings.TrimSpace(strings.TrimSuffix(value, "%")))
}

func strconvParseFloat64(value string) (float64, error) {
	var parsed float64
	_, err := fmt.Sscan(value, &parsed)
	return parsed, err
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func ptrFloat(value float64) *float64 {
	return &value
}

func boolPointer(value bool) *bool {
	return &value
}

func nullableFloat(value *float64) any {
	if value == nil {
		return nil
	}
	return *value
}

func truncate(value string, limit int) string {
	if limit <= 0 || len(value) <= limit {
		return value
	}
	return value[:limit] + "...(truncated)"
}

func sanitizeDetail(detail any) any {
	if detail == nil {
		return nil
	}
	data, err := json.Marshal(detail)
	if err != nil {
		return detail
	}
	var parsed any
	if err := json.Unmarshal(data, &parsed); err != nil {
		return detail
	}
	return redactValue(parsed)
}

func redactValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			if isSecretKey(key) {
				result[key] = "[redacted]"
				continue
			}
			result[key] = redactValue(item)
		}
		return result
	case []any:
		result := make([]any, len(typed))
		for i, item := range typed {
			result[i] = redactValue(item)
		}
		return result
	default:
		return typed
	}
}

func isSecretKey(key string) bool {
	normalized := strings.ToLower(key)
	if normalized == "triggerkey" {
		return false
	}
	return strings.Contains(normalized, "token") ||
		strings.Contains(normalized, "secret") ||
		strings.Contains(normalized, "authorization") ||
		strings.Contains(normalized, "key")
}

func padBase64(value string) string {
	switch len(value) % 4 {
	case 2:
		return value + "=="
	case 3:
		return value + "="
	default:
		return value
	}
}
