package codexinspection

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/managerconfig"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

const (
	codexUsageURL       = "https://chatgpt.com/backend-api/wham/usage"
	codexFiveHourWindow = 18_000
	codexWeekWindow     = 604_800
	codexMonthWindow    = 2_592_000
	codexMinMonthWindow = 28 * 24 * 60 * 60
	codexMaxMonthWindow = 31 * 24 * 60 * 60
	maxStoredBodyText   = 2048

	// Random delay (ms) before each per-account wham/usage probe, used to spread
	// out the bulk probing cadence and reduce the chance of being flagged as
	// machine-driven batch behavior by anti-fraud systems.
	inspectionJitterMinMs = 300
	inspectionJitterMaxMs = 2000
)

var (
	ErrRunAlreadyActive    = errors.New("codex inspection is already running")
	ErrNotConfigured       = errors.New("usage service is not configured")
	ErrRunNotFound         = errors.New("codex inspection run not found")
	ErrRunNotCompleted     = errors.New("codex inspection run is not completed")
	ErrActionIDsRequired   = errors.New("codex inspection action result ids are required")
	ErrNoActionableResults = errors.New("codex inspection has no actionable results")
)

type Service struct {
	store                *store.Store
	managerConfigService *managerconfig.Service
	client               *http.Client

	mu      sync.Mutex
	running bool
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
	ResultIDs []int64 `json:"resultIds"`
}

type ActionOutcome struct {
	ResultID       int64  `json:"resultId,omitempty"`
	AccountKey     string `json:"accountKey,omitempty"`
	FileName       string `json:"fileName"`
	DisplayAccount string `json:"displayAccount"`
	Action         string `json:"action"`
	Status         string `json:"status"`
	Success        bool   `json:"success"`
	Error          string `json:"error,omitempty"`
}

type ExecuteActionsResult struct {
	Outcomes []ActionOutcome `json:"outcomes"`
	Detail   RunDetail       `json:"detail"`
}

type authFile map[string]any

type account struct {
	Key            string
	FileName       string
	DisplayAccount string
	AuthIndex      string
	AccountID      string
	Provider       string
	Disabled       bool
	Status         string
	State          string
	File           authFile
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
	FileName string
	Items    []model.CodexInspectionResult
	Action   string
	Mixed    bool
}

type unauthorizedReason string

const (
	unauthorizedReasonUnknown     unauthorizedReason = "unknown"
	unauthorizedReasonExpired     unauthorizedReason = "expired"
	unauthorizedReasonInvalidated unauthorizedReason = "invalidated"
)

const (
	fileActionDuplicateReason = "CPA 认证文件动作按文件执行，该文件已由另一条结果处理"
	fileActionMixedReason     = "同一认证文件下存在多个不同建议动作，文件级处理已阻止，请到认证文件管理中手动处理"
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
	client := &http.Client{Timeout: 30 * time.Second}
	if len(clients) > 0 && clients[0] != nil {
		client = clients[0]
	}
	return &Service{
		store:                st,
		managerConfigService: managerConfigService,
		client:               client,
	}
}

func (s *Service) Run(ctx context.Context, req RunRequest) (RunDetail, error) {
	if err := s.acquireRun(); err != nil {
		return RunDetail{}, err
	}
	defer s.releaseRun()

	settings, setup, err := s.resolveRuntime(ctx)
	if err != nil {
		return RunDetail{}, err
	}

	triggerType := strings.TrimSpace(req.TriggerType)
	if triggerType == "" {
		triggerType = model.CodexInspectionTriggerManual
	}
	startedAt := time.Now().UnixMilli()
	run, err := s.store.CreateCodexInspectionRun(ctx, model.CodexInspectionRun{
		TriggerType:  triggerType,
		TriggerKey:   strings.TrimSpace(req.TriggerKey),
		Status:       model.CodexInspectionStatusRunning,
		StartedAtMS:  startedAt,
		Settings:     settings,
		SettingsJSON: model.MarshalCodexInspectionSettings(settings),
	})
	if err != nil {
		return RunDetail{}, err
	}
	persistCtx := context.WithoutCancel(ctx)

	logger := runLogger{service: s, runID: run.ID}
	logger.info(ctx, "Codex 巡检开始", map[string]any{
		"triggerType": triggerType,
		"triggerKey":  strings.TrimSpace(req.TriggerKey),
		"targetType":  settings.TargetType,
	})

	files, err := s.fetchAuthFiles(ctx, setup)
	if err != nil {
		logger.error(persistCtx, "加载认证文件列表失败", map[string]any{"error": err.Error()})
		return s.failRun(persistCtx, run, err)
	}

	accounts := make([]account, 0, len(files))
	for _, file := range files {
		next := toAccount(file)
		if next.Provider == settings.TargetType {
			accounts = append(accounts, next)
		}
	}
	probeSetCount := len(accounts)
	sampled := pickSample(accounts, settings.SampleSize)

	run.TotalFiles = len(files)
	run.ProbeSetCount = probeSetCount
	run.SampledCount = len(sampled)
	run.DisabledCount = countAccounts(sampled, true)
	run.EnabledCount = len(sampled) - run.DisabledCount
	_ = s.store.UpdateCodexInspectionRun(persistCtx, run)

	logger.info(ctx, "Codex 巡检集合已准备", map[string]any{
		"totalFiles":    len(files),
		"probeSetCount": probeSetCount,
		"sampledCount":  len(sampled),
	})

	results := s.inspectAccounts(ctx, setup, settings, run.ID, sampled, logger)
	if err := ctx.Err(); err != nil {
		for _, result := range results {
			result.RunID = run.ID
			_, _ = s.store.InsertCodexInspectionResult(persistCtx, result)
		}
		run = summarizeRun(run, results)
		run.Status = model.CodexInspectionStatusFailed
		run.Error = err.Error()
		run.FinishedAtMS = time.Now().UnixMilli()
		if err := s.store.UpdateCodexInspectionRun(persistCtx, run); err != nil {
			return RunDetail{}, err
		}
		logger.warning(persistCtx, "Codex 巡检已取消", map[string]any{"error": run.Error})
		return s.GetRun(persistCtx, run.ID)
	}

	results = resolveAutoActionResults(settings.AutoActionMode, results)
	actionOutcomes := s.executeAutoActions(ctx, setup, settings, results, logger)
	results = applyActionOutcomes(results, actionOutcomes)
	for _, result := range results {
		result.RunID = run.ID
		_, _ = s.store.InsertCodexInspectionResult(persistCtx, result)
	}
	run = summarizeRun(run, results)
	if failed := countFailedOutcomes(actionOutcomes); failed > 0 {
		run.Error = fmt.Sprintf("%d 个自动处理动作执行失败，详见巡检日志", failed)
	}
	run.Status = model.CodexInspectionStatusCompleted
	run.FinishedAtMS = time.Now().UnixMilli()
	if err := s.store.UpdateCodexInspectionRun(persistCtx, run); err != nil {
		return RunDetail{}, err
	}
	logger.success(persistCtx, "Codex 巡检完成", map[string]any{
		"deleteCount":  run.DeleteCount,
		"disableCount": run.DisableCount,
		"enableCount":  run.EnableCount,
		"keepCount":    run.KeepCount,
		"actionErrors": failedActionOutcomes(actionOutcomes),
	})
	return s.GetRun(persistCtx, run.ID)
}

func (s *Service) ListRuns(ctx context.Context, limit int) ([]model.CodexInspectionRun, error) {
	return s.store.ListCodexInspectionRuns(ctx, limit)
}

func (s *Service) GetRun(ctx context.Context, id int64) (RunDetail, error) {
	run, ok, err := s.store.GetCodexInspectionRun(ctx, id)
	if err != nil {
		return RunDetail{}, err
	}
	if !ok {
		return RunDetail{}, ErrRunNotFound
	}
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
	if err := s.acquireRun(); err != nil {
		return ExecuteActionsResult{}, err
	}
	defer s.releaseRun()

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
	if detail.Run.Settings.TargetType != "" {
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

	items, preflightOutcomes := selectManualActionItems(detail.Results, selected)
	if len(items) == 0 && len(preflightOutcomes) == 0 {
		return ExecuteActionsResult{}, ErrNoActionableResults
	}

	persistCtx := context.WithoutCancel(ctx)
	logger := runLogger{service: s, runID: detail.Run.ID}
	logger.info(persistCtx, "手动处理账号开始", map[string]any{
		"requestedCount": len(req.ResultIDs),
		"actionCount":    len(items),
	})
	for _, outcome := range preflightOutcomes {
		logger.warning(persistCtx, "手动处理账号跳过", map[string]any{
			"fileName":       outcome.FileName,
			"displayAccount": outcome.DisplayAccount,
			"action":         outcome.Action,
			"reason":         outcome.Error,
		})
	}

	validItems, validationOutcomes, err := s.validateManualActionItems(ctx, persistCtx, setup, items, logger)
	if err != nil {
		return ExecuteActionsResult{}, err
	}
	outcomes := make([]ActionOutcome, 0, len(preflightOutcomes)+len(validationOutcomes)+len(validItems))
	outcomes = append(outcomes, preflightOutcomes...)
	outcomes = append(outcomes, validationOutcomes...)
	outcomes = append(outcomes, s.executeActionItems(ctx, setup, settings, validItems, logger, "手动处理", func(item model.CodexInspectionResult) string {
		return item.Action
	})...)
	if len(outcomes) == 0 {
		return ExecuteActionsResult{}, ErrNoActionableResults
	}
	nextResults := applyActionOutcomes(detail.Results, outcomes)
	for _, result := range nextResults {
		result.RunID = detail.Run.ID
		_, _ = s.store.InsertCodexInspectionResult(persistCtx, result)
	}

	run := summarizeRun(detail.Run, nextResults)
	if failed := countFailedOutcomes(outcomes); failed > 0 {
		run.Error = fmt.Sprintf("%d 个手动处理动作执行失败，详见巡检日志", failed)
	} else {
		run.Error = ""
	}
	if err := s.store.UpdateCodexInspectionRun(persistCtx, run); err != nil {
		return ExecuteActionsResult{}, err
	}
	logger.success(persistCtx, "手动处理账号完成", map[string]any{
		"successCount": len(outcomes) - countFailedOutcomes(outcomes),
		"failedCount":  countFailedOutcomes(outcomes),
	})

	nextDetail, err := s.GetRun(persistCtx, detail.Run.ID)
	if err != nil {
		return ExecuteActionsResult{}, err
	}
	return ExecuteActionsResult{Outcomes: outcomes, Detail: nextDetail}, nil
}

func (s *Service) ResolveConfig(ctx context.Context) (model.ManagerCodexInspectionConfig, bool, error) {
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

func (s *Service) acquireRun() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return ErrRunAlreadyActive
	}
	s.running = true
	return nil
}

func (s *Service) releaseRun() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.running = false
}

func (s *Service) resolveRuntime(ctx context.Context) (model.ManagerCodexInspectionConfig, store.Setup, error) {
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
	_ = s.store.UpdateCodexInspectionRun(ctx, run)
	detail, err := s.GetRun(ctx, run.ID)
	if err != nil {
		return RunDetail{}, err
	}
	return detail, cause
}

func (s *Service) fetchAuthFiles(ctx context.Context, setup store.Setup) ([]authFile, error) {
	files, _, err := s.fetchAuthFilesAt(ctx, setup, "/v0/management/auth-files")
	return files, err
}

func (s *Service) fetchAuthFilesAt(ctx context.Context, setup store.Setup, path string) ([]authFile, int, error) {
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		cpa.NormalizeBaseURL(setup.CPAUpstreamURL)+path,
		nil,
	)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+setup.ManagementKey)
	res, err := s.client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 8*1024*1024))
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, res.StatusCode, fmt.Errorf("auth files request failed: %s %s", res.Status, truncate(string(body), maxStoredBodyText))
	}
	var payload struct {
		Files []authFile `json:"files"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, res.StatusCode, err
	}
	return payload.Files, res.StatusCode, nil
}

func (s *Service) inspectAccounts(
	ctx context.Context,
	setup store.Setup,
	settings model.ManagerCodexInspectionConfig,
	runID int64,
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
				// Random sleep before probing to spread out the bulk request
				// cadence; exit immediately when ctx is cancelled.
				if !sleepWithJitter(ctx, inspectionJitterMinMs, inspectionJitterMaxMs) {
					return
				}
				result := s.inspectSingleAccount(ctx, setup, settings, item, logger)
				result.RunID = runID
				if _, err := s.store.InsertCodexInspectionResult(ctx, result); err != nil {
					logger.error(ctx, "写入巡检账号结果失败", map[string]any{
						"fileName": item.FileName,
						"error":    err.Error(),
					})
				}
				results <- result
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

// sleepWithJitter sleeps for a random duration within [minMs, maxMs]. It returns
// false when ctx is cancelled during the wait, so callers can stop early. When
// minMs/maxMs are invalid it skips the jitter and returns immediately.
func sleepWithJitter(ctx context.Context, minMs, maxMs int) bool {
	if maxMs <= 0 || maxMs < minMs {
		return ctx.Err() == nil
	}
	delay := minMs
	if span := maxMs - minMs; span > 0 {
		delay += rand.Intn(span + 1)
	}
	timer := time.NewTimer(time.Duration(delay) * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (s *Service) inspectSingleAccount(
	ctx context.Context,
	setup store.Setup,
	settings model.ManagerCodexInspectionConfig,
	item account,
	logger runLogger,
) model.CodexInspectionResult {
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
	base.PlanType = planType
	base.QuotaWindows = buildCodexInspectionQuotaWindows(payload, planType)
	base.Error = ""
	if statusCode < 200 || statusCode >= 300 {
		base.ErrorKind = "http_status"
		base.ErrorDetail = firstNonEmpty(truncate(response.BodyText, maxStoredBodyText), fmt.Sprintf("HTTP %d", statusCode))
	}

	level := "info"
	switch decision.Action {
	case "delete":
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
	// wham/usage is a GET request and real Codex clients don't send Content-Type,
	// so we don't add it either to avoid diverging from the real client
	// fingerprint. User-Agent still uses the inspection's own config (separate
	// from the quota-lookup UA).
	headers := map[string]string{
		"Authorization": "Bearer $TOKEN$",
		"User-Agent":    settings.UserAgent,
	}
	if strings.TrimSpace(item.AccountID) != "" {
		headers["ChatGPT-Account-Id"] = strings.TrimSpace(item.AccountID)
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
	body, _ := io.ReadAll(io.LimitReader(res.Body, 8*1024*1024))
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return apiCallResponse{}, res.StatusCode, fmt.Errorf("api-call failed: %s %s", res.Status, truncate(string(body), maxStoredBodyText))
	}

	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
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

func (s *Service) executeAutoActions(
	ctx context.Context,
	setup store.Setup,
	settings model.ManagerCodexInspectionConfig,
	results []model.CodexInspectionResult,
	logger runLogger,
) []ActionOutcome {
	mode := model.NormalizeCodexInspectionAutoActionMode(settings.AutoActionMode, model.CodexInspectionAutoActionNone)
	items, preflightOutcomes := selectAutoActionItems(mode, results)
	if len(items) == 0 {
		return preflightOutcomes
	}
	outcomes := make([]ActionOutcome, 0, len(preflightOutcomes)+len(items))
	outcomes = append(outcomes, preflightOutcomes...)
	outcomes = append(outcomes, s.executeActionItems(ctx, setup, settings, items, logger, "自动处理", func(item model.CodexInspectionResult) string {
		return resolveExecutableAction(mode, item.Action)
	})...)
	return outcomes
}

func (s *Service) executeActionItems(
	ctx context.Context,
	setup store.Setup,
	settings model.ManagerCodexInspectionConfig,
	items []model.CodexInspectionResult,
	logger runLogger,
	logPrefix string,
	actionFor func(model.CodexInspectionResult) string,
) []ActionOutcome {
	workers := settings.DeleteWorkers
	if workers <= 0 {
		workers = 1
	}
	jobs := make(chan model.CodexInspectionResult)
	outcomes := make(chan ActionOutcome, len(items))
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
						ResultID:       item.ID,
						AccountKey:     item.AccountKey,
						FileName:       item.FileName,
						DisplayAccount: item.DisplayAccount,
						Action:         action,
					}
					if err := s.executeAction(ctx, setup, actionItem); err != nil {
						outcome.Success = false
						outcome.Status = model.CodexInspectionActionStatusFailed
						outcome.Error = err.Error()
						outcomes <- outcome
						logger.error(ctx, logPrefix+"账号失败", map[string]any{
							"fileName":       item.FileName,
							"displayAccount": item.DisplayAccount,
							"action":         action,
							"error":          err.Error(),
						})
						continue
					}
					outcome.Success = true
					outcome.Status = model.CodexInspectionActionStatusSuccess
					outcomes <- outcome
					logger.success(ctx, logPrefix+"账号成功", map[string]any{
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
			return collectActionOutcomes(outcomes, len(items))
		case jobs <- item:
		}
	}
	close(jobs)
	wg.Wait()
	close(outcomes)

	return collectActionOutcomes(outcomes, len(items))
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

func (s *Service) executeAction(ctx context.Context, setup store.Setup, item model.CodexInspectionResult) error {
	switch item.Action {
	case "delete":
		return s.deleteAuthFileOnly(ctx, setup, "/v0/management/auth-files", item.FileName)
	case "disable", "enable":
		disabled := item.Action == "disable"
		payload := map[string]any{"name": item.FileName, "disabled": disabled}
		err, _ := s.patchAuthFile(ctx, setup, "/v0/management/auth-files/status", payload)
		return err
	default:
		return nil
	}
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
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1024*1024))
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("%s %s", res.Status, truncate(string(body), maxStoredBodyText)), res.StatusCode
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err == nil {
		if failed, ok := payload["failed"].([]any); ok && len(failed) > 0 {
			return fmt.Errorf("CPA action failed: %s", truncate(fmt.Sprint(failed[0]), maxStoredBodyText)), res.StatusCode
		}
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
	_, _ = l.service.store.InsertCodexInspectionLog(ctx, model.CodexInspectionLog{
		RunID:   l.runID,
		Level:   level,
		Message: message,
		Detail:  sanitizeDetail(detail),
	})
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
		return nil
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
		reason := fmt.Sprintf("%s仍可用，建议立即启用账号", longWindowLabel)
		if fiveHourOverThreshold {
			reason = fmt.Sprintf("5 小时额度达到阈值，但%s仍可用，建议立即启用账号", longWindowLabel)
		}
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
	if statusCode == http.StatusOK && item.Disabled {
		return inspectionDecision{
			Action:       "enable",
			ActionReason: "账号恢复健康，建议重新启用",
			UsedPercent:  usedPercent,
			IsQuota:      false,
		}
	}
	return inspectionDecision{Action: "keep", ActionReason: "无需处理", UsedPercent: usedPercent, IsQuota: false}
}

func resolveUnauthorizedProbeAction(bodyText string, usedPercent *float64) inspectionDecision {
	switch classifyUnauthorizedReason(bodyText) {
	case unauthorizedReasonExpired:
		return inspectionDecision{
			Action:       "reauth",
			ActionReason: "接口返回 401，登录已过期，建议重新登录账号",
			UsedPercent:  usedPercent,
			IsQuota:      false,
		}
	case unauthorizedReasonInvalidated:
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

func classifyUnauthorizedReason(bodyText string) unauthorizedReason {
	normalized := strings.ToLower(strings.TrimSpace(bodyText))
	switch {
	case strings.Contains(normalized, "provided authentication token is expired") ||
		strings.Contains(normalized, "authentication token is expired") ||
		strings.Contains(normalized, "token is expired"):
		return unauthorizedReasonExpired
	case strings.Contains(normalized, "authentication token has been invalidated") ||
		strings.Contains(normalized, "token has been invalidated") ||
		strings.Contains(normalized, "token is invalidated"):
		return unauthorizedReasonInvalidated
	default:
		return unauthorizedReasonUnknown
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

func selectAutoActionItems(mode string, results []model.CodexInspectionResult) ([]model.CodexInspectionResult, []ActionOutcome) {
	mode = model.NormalizeCodexInspectionAutoActionMode(mode, model.CodexInspectionAutoActionNone)
	if mode == model.CodexInspectionAutoActionNone {
		return nil, nil
	}

	items := make([]model.CodexInspectionResult, 0)
	outcomes := make([]ActionOutcome, 0)
	for _, group := range buildExecutableFileActionGroups(results) {
		if group.Mixed {
			for _, result := range group.Items {
				outcomes = append(outcomes, needsReviewActionOutcome(result, result.Action, fileActionMixedReason))
			}
			continue
		}
		if len(group.Items) == 0 || !allowAutoAction(mode, group.Items[0]) {
			continue
		}
		items = append(items, group.Items[0])
		for _, result := range group.Items[1:] {
			outcomes = append(outcomes, skippedActionOutcome(result, result.Action, fileActionDuplicateReason))
		}
	}
	return items, outcomes
}

func buildExecutableFileActionGroups(results []model.CodexInspectionResult) []fileActionGroup {
	groupOrder := make([]string, 0)
	groupsByFileName := map[string]*fileActionGroup{}
	for _, result := range results {
		if !isExecutableInspectionAction(result.Action) {
			continue
		}
		fileName := strings.TrimSpace(result.FileName)
		if fileName == "" {
			continue
		}
		group, ok := groupsByFileName[fileName]
		if !ok {
			group = &fileActionGroup{FileName: fileName, Action: result.Action}
			groupsByFileName[fileName] = group
			groupOrder = append(groupOrder, fileName)
		}
		if result.Action != group.Action {
			group.Mixed = true
		}
		group.Items = append(group.Items, result)
	}
	groups := make([]fileActionGroup, 0, len(groupOrder))
	for _, fileName := range groupOrder {
		groups = append(groups, *groupsByFileName[fileName])
	}
	return groups
}

func allowAutoAction(mode string, result model.CodexInspectionResult) bool {
	switch mode {
	case model.CodexInspectionAutoActionEnable:
		return result.Action == "enable"
	case model.CodexInspectionAutoActionDisable:
		return result.Action == "enable" || result.Action == "disable" || result.Action == "delete"
	case model.CodexInspectionAutoActionDelete:
		return result.Action == "enable" || result.Action == "disable" || result.Action == "delete"
	default:
		return false
	}
}

func selectManualActionItems(
	results []model.CodexInspectionResult,
	selected map[int64]struct{},
) ([]model.CodexInspectionResult, []ActionOutcome) {
	items := make([]model.CodexInspectionResult, 0, len(selected))
	outcomes := make([]ActionOutcome, 0)
	seenFileNames := map[string]struct{}{}
	groupByFileName := map[string]fileActionGroup{}
	for _, group := range buildExecutableFileActionGroups(results) {
		groupByFileName[group.FileName] = group
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
			outcomes = append(outcomes, needsReviewActionOutcome(result, result.Action, "该建议动作需要到认证文件管理中人工处理"))
			continue
		}
		if fileName == "" {
			outcomes = append(outcomes, failedActionOutcome(result, result.Action, "认证文件名为空，无法执行"))
			continue
		}
		group, ok := groupByFileName[fileName]
		if ok && group.Mixed {
			outcomes = append(outcomes, needsReviewActionOutcome(result, result.Action, fileActionMixedReason))
			continue
		}
		if ok && len(group.Items) > 0 && group.Items[0].ID != result.ID {
			outcomes = append(outcomes, skippedActionOutcome(result, result.Action, "CPA 认证文件动作按文件执行，该文件已有另一条结果作为可执行项"))
			continue
		}
		if _, ok := seenFileNames[fileName]; ok {
			outcomes = append(outcomes, skippedActionOutcome(result, result.Action, "CPA 认证文件动作按文件执行，同名文件已由另一条结果处理"))
			continue
		}
		seenFileNames[fileName] = struct{}{}
		items = append(items, result)
	}
	return items, outcomes
}

func isExecutableInspectionAction(action string) bool {
	return action == "delete" || action == "disable" || action == "enable"
}

func (s *Service) validateManualActionItems(
	ctx context.Context,
	logCtx context.Context,
	setup store.Setup,
	items []model.CodexInspectionResult,
	logger runLogger,
) ([]model.CodexInspectionResult, []ActionOutcome, error) {
	if len(items) == 0 {
		return nil, nil, nil
	}
	files, err := s.fetchAuthFiles(ctx, setup)
	if err != nil {
		return nil, nil, err
	}
	currentByFile := map[string][]account{}
	for _, file := range files {
		item := toAccount(file)
		currentByFile[item.FileName] = append(currentByFile[item.FileName], item)
	}

	validItems := make([]model.CodexInspectionResult, 0, len(items))
	outcomes := make([]ActionOutcome, 0)
	for _, item := range items {
		current, ok := matchCurrentAccount(currentByFile[item.FileName], item)
		if !ok {
			outcome := failedActionOutcome(item, item.Action, "认证文件不存在或账号标识已变化，已拒绝执行")
			outcomes = append(outcomes, outcome)
			logger.warning(logCtx, "手动处理账号校验失败", map[string]any{
				"fileName":       item.FileName,
				"displayAccount": item.DisplayAccount,
				"authIndex":      item.AuthIndex,
				"accountId":      item.AccountID,
				"error":          outcome.Error,
			})
			continue
		}
		if item.Action == "disable" && current.Disabled {
			outcome := skippedActionOutcome(item, item.Action, "账号已是禁用状态，未重复执行")
			outcomes = append(outcomes, outcome)
			logger.info(logCtx, "手动处理账号跳过", map[string]any{
				"fileName":       item.FileName,
				"displayAccount": item.DisplayAccount,
				"action":         item.Action,
				"reason":         outcome.Error,
			})
			continue
		}
		if item.Action == "enable" && !current.Disabled {
			outcome := skippedActionOutcome(item, item.Action, "账号已是启用状态，未重复执行")
			outcomes = append(outcomes, outcome)
			logger.info(logCtx, "手动处理账号跳过", map[string]any{
				"fileName":       item.FileName,
				"displayAccount": item.DisplayAccount,
				"action":         item.Action,
				"reason":         outcome.Error,
			})
			continue
		}
		validItems = append(validItems, item)
	}
	return validItems, outcomes, nil
}

func matchCurrentAccount(candidates []account, result model.CodexInspectionResult) (account, bool) {
	if len(candidates) == 0 {
		return account{}, false
	}
	authIndex := strings.TrimSpace(result.AuthIndex)
	accountID := strings.TrimSpace(result.AccountID)
	if authIndex == "" && accountID == "" {
		return candidates[0], true
	}
	for _, candidate := range candidates {
		if authIndex != "" && candidate.AuthIndex != authIndex {
			continue
		}
		if accountID != "" && candidate.AccountID != accountID {
			continue
		}
		return candidate, true
	}
	return account{}, false
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
		status := model.NormalizeCodexInspectionActionStatus(outcome.Status, out[i].Action)
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

func countFailedOutcomes(outcomes []ActionOutcome) int {
	count := 0
	for _, outcome := range outcomes {
		if !outcome.Success {
			count++
		}
	}
	return count
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
		AccountKey:     item.Key,
		FileName:       item.FileName,
		DisplayAccount: item.DisplayAccount,
		AuthIndex:      item.AuthIndex,
		AccountID:      item.AccountID,
		Provider:       item.Provider,
		Disabled:       item.Disabled,
		Status:         item.Status,
		State:          item.State,
		PlanType:       resolveCodexPlanType(item.File),
		Action:         "keep",
		ActionReason:   "无需处理",
		IsQuota:        false,
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
	fileName := firstNonEmpty(readString(file, "name"), readString(file, "id"), normalizeAuthIndex(file["auth_index"]), normalizeAuthIndex(file["authIndex"]), "unknown-auth-file")
	authIndex := firstNonEmpty(normalizeAuthIndex(file["auth_index"]), normalizeAuthIndex(file["authIndex"]), normalizeAuthIndex(file["auth-index"]))
	provider := strings.ToLower(firstNonEmpty(readString(file, "provider"), readString(file, "type")))
	displayAccount := firstNonEmpty(
		readString(file, "account"),
		readString(file, "email"),
		readString(file, "label"),
		fileName,
	)
	key := fileName + "::" + authIndex
	if authIndex == "" {
		key = fileName + "::-"
	}
	return account{
		Key:            key,
		FileName:       fileName,
		DisplayAccount: displayAccount,
		AuthIndex:      authIndex,
		AccountID:      resolveCodexAccountID(file),
		Provider:       provider,
		Disabled:       isDisabledAuthFile(file),
		Status:         readString(file, "status"),
		State:          readString(file, "state"),
		File:           file,
	}
}

func resolveCodexAccountID(file authFile) string {
	metadata := readMap(file, "metadata")
	attributes := readMap(file, "attributes")
	candidates := []any{
		file["chatgpt_account_id"],
		file["chatgptAccountId"],
		file["account_id"],
		file["accountId"],
		metadata["chatgpt_account_id"],
		metadata["chatgptAccountId"],
		metadata["account_id"],
		metadata["accountId"],
		attributes["chatgpt_account_id"],
		attributes["chatgptAccountId"],
		attributes["account_id"],
		attributes["accountId"],
	}
	for _, candidate := range candidates {
		if id := extractDirectCodexAccountID(candidate); id != "" {
			return id
		}
	}
	tokenCandidates := []any{
		file["id_token"],
		metadata["id_token"],
		attributes["id_token"],
	}
	for _, candidate := range tokenCandidates {
		if id := extractCodexAccountIDFromToken(candidate); id != "" {
			return id
		}
	}
	return ""
}

func extractDirectCodexAccountID(value any) string {
	if direct := readPlainString(value); direct != "" {
		return direct
	}
	if direct := readAccountIDCandidate(value); direct != "" {
		return direct
	}
	return ""
}

func extractCodexAccountIDFromToken(value any) string {
	payload := parseIDTokenPayload(value)
	if payload == nil {
		return ""
	}
	return readAccountIDCandidate(payload)
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

func readPlainString(value any) string {
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(text)
}

func readAccountIDCandidate(value any) string {
	record, ok := value.(map[string]any)
	if !ok {
		return ""
	}
	return firstNonEmpty(
		readString(record, "chatgpt_account_id"),
		readString(record, "chatgptAccountId"),
		readString(record, "account_id"),
		readString(record, "accountId"),
	)
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
		"codex_quota.generic_window",
		nil,
		teamPlan,
	)
	addCodexRateLimitWindows(
		&windows,
		parseRateLimit(readMap(payload, "code_review_rate_limit", "codeReviewRateLimit")),
		codexWindowMeta{ID: "code-review-five-hour", LabelKey: "codex_quota.code_review_primary_window"},
		codexWindowMeta{ID: "code-review-weekly", LabelKey: "codex_quota.code_review_secondary_window"},
		codexWindowMeta{ID: "code-review-monthly", LabelKey: "codex_quota.code_review_monthly_window"},
		"codex_quota.code_review_generic_window",
		nil,
		teamPlan,
	)
	addAdditionalRateLimitWindows(&windows, readMapSlice(payload, "additional_rate_limits", "additionalRateLimits"), teamPlan)
	return windows
}

func addCodexRateLimitWindows(
	windows *[]model.CodexInspectionQuotaWindow,
	limit *codexRateLimit,
	fiveHourMeta codexWindowMeta,
	weeklyMeta codexWindowMeta,
	monthlyMeta codexWindowMeta,
	genericLabelKey string,
	genericLabelParams map[string]any,
	teamPlan bool,
) {
	if limit == nil {
		return
	}
	classified := classifyWindows(limit, codexPlanTypeForTeam(teamPlan))
	added := make(map[*codexWindow]bool)
	addCodexWindowInfo(windows, fiveHourMeta.ID, fiveHourMeta.LabelKey, genericLabelParams, classified.FiveHour, limit.LimitReached, limit.Allowed)
	if classified.FiveHour != nil {
		added[classified.FiveHour] = true
	}
	addCodexWindowInfo(windows, weeklyMeta.ID, weeklyMeta.LabelKey, genericLabelParams, classified.Weekly, limit.LimitReached, limit.Allowed)
	if classified.Weekly != nil {
		added[classified.Weekly] = true
	}
	addCodexWindowInfo(windows, monthlyMeta.ID, monthlyMeta.LabelKey, genericLabelParams, classified.Monthly, limit.LimitReached, limit.Allowed)
	if classified.Monthly != nil {
		added[classified.Monthly] = true
	}
	for index, window := range codexRateLimitWindows(limit) {
		if window == nil || added[window] {
			continue
		}
		duration := formatCodexWindowDuration(window.LimitWindowSeconds)
		prefix := ""
		if name, ok := genericLabelParams["name"]; ok {
			if normalizedName := normalizeCodexWindowID(fmt.Sprint(name)); normalizedName != "" {
				prefix = normalizedName + "-"
			}
		}
		addCodexWindowInfo(
			windows,
			fmt.Sprintf("%swindow-%s-%d", prefix, duration, index),
			genericLabelKey,
			withCodexWindowDurationParam(genericLabelParams, duration),
			window,
			limit.LimitReached,
			limit.Allowed,
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
) {
	if window == nil {
		return
	}
	resetLabel := formatCodexResetLabel(window)
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
		LimitWindowSeconds: window.LimitWindowSeconds,
	})
}

func addAdditionalRateLimitWindows(windows *[]model.CodexInspectionQuotaWindow, additionalRateLimits []map[string]any, teamPlan bool) {
	for index, limitItem := range additionalRateLimits {
		rateInfo := parseRateLimit(readMap(limitItem, "rate_limit", "rateLimit"))
		if rateInfo == nil {
			continue
		}
		limitName := firstNonEmpty(
			readString(limitItem, "limit_name", "limitName"),
			readString(limitItem, "metered_feature", "meteredFeature"),
			fmt.Sprintf("additional-%d", index+1),
		)
		idPrefix := normalizeCodexWindowID(limitName)
		if idPrefix == "" {
			idPrefix = fmt.Sprintf("additional-%d", index+1)
		}
		addCodexRateLimitWindows(
			windows,
			rateInfo,
			codexWindowMeta{ID: fmt.Sprintf("%s-five-hour-%d", idPrefix, index), LabelKey: "codex_quota.additional_primary_window"},
			codexWindowMeta{ID: fmt.Sprintf("%s-weekly-%d", idPrefix, index), LabelKey: "codex_quota.additional_secondary_window"},
			codexWindowMeta{ID: fmt.Sprintf("%s-monthly-%d", idPrefix, index), LabelKey: "codex_quota.additional_monthly_window"},
			"codex_quota.additional_generic_window",
			map[string]any{"name": limitName},
			teamPlan,
		)
	}
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
	if window == nil {
		return "-"
	}
	if window.ResetAt != nil && *window.ResetAt > 0 {
		return formatUnixSeconds(*window.ResetAt)
	}
	if window.ResetAfterSeconds != nil && *window.ResetAfterSeconds > 0 {
		targetSeconds := float64(time.Now().Unix()) + math.Floor(*window.ResetAfterSeconds)
		return formatUnixSeconds(targetSeconds)
	}
	return "-"
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
