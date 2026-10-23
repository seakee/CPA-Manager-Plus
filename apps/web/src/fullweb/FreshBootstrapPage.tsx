import { lazy, Suspense, useCallback, useEffect, useRef, useState } from 'react';
import { bootstrapJourneyText as journey } from '../i18n/bootstrapJourney';
import { shellText } from '../i18n/consoleShell';
import logo from '../assets/brand/cpamp-logo-horizontal-color.svg?inline';
import logoOnDark from '../assets/brand/cpamp-logo-horizontal-on-dark.svg?inline';
import { FullWebIcon } from './FullWebIcon';
import {
  BootstrapTransportClient,
  BootstrapTransportError,
  FreshOperation,
  Handoff,
} from './bootstrapTransport';

import { FreshSetupWizard } from './FreshSetupWizard';
const UpgradeAuthenticationEntry = lazy(() => import('./UpgradeAuthenticationEntry').then(
  (module) => ({ default: module.UpgradeAuthenticationEntry })
));
export interface FreshBootstrapPageProps {
  handoff: Handoff | null;
  transport: BootstrapTransportClient;
}

type BootPhase = 'no_handoff' | 'connecting' | 'ready' | 'init_error';

interface ActionFeedback {
  tone: 'danger' | 'warn' | 'info' | 'success';
  title: string;
  detail: string;
}

// Public discovery never confers write authority. Non-Fresh statuses only
// enter either the existing-Admin gate or host intervention, never Fresh claim.
type DiscoveryGuidance = ActionFeedback & { flow: 'fresh' | 'host' | 'upgrade'; retryable?: true };
function publicDiscoveryGuidance(payload: unknown): DiscoveryGuidance | null {
  if (!payload || typeof payload !== 'object') return null;
  const value = payload as Record<string, unknown>;
  if (value.contract_version !== 'cpamp.phase4-01.discovery.v1') return null;
  if (
    value.status === 'fresh_claim_required' &&
    value.mode === 'fresh' &&
    value.auth_entry === 'fresh_claim' &&
    value.reason === 'fresh_claim_required' &&
    value.authentication_required === true
  ) {
    return {
      tone: 'info',
      flow: 'fresh',
      title: '检测到需要宿主机认领的 Fresh 初始化状态',
      detail:
        '此状态仅为只读识别结果。请从受信宿主机获取一次性 Handoff 链接；本页面不会自行创建或认领安装。',
    };
  }
  if (
    value.status === 'unsupported_upgrade' &&
    value.mode === 'unsupported_upgrade' &&
    value.auth_entry === 'admin_session' &&
    value.reason === 'qualification_unavailable' &&
    value.authentication_required === true
  ) {
    return {
      tone: 'warn',
      flow: 'upgrade',
      title: '检测到尚未获得升级资格的现有来源',
      detail:
        '只能在受信宿主机检查原始来源。当前无法进入 Fresh 初始化或 Upgrade 切换；预检成功也不代表获得导入或覆盖授权。',
    };
  }
  if (
    value.status === 'recovery_required' &&
    value.mode === 'recovery' &&
    value.auth_entry === 'host_intervention' &&
    value.reason === 'recovery_host_intervention' &&
    value.authentication_required === false
  ) {
    return {
      tone: 'warn',
      flow: 'host',
      title: '检测到需要宿主机介入的恢复状态',
      detail:
        '来源不具备直接初始化资格。请先在受信宿主机进行只读预检，保护数据库、data.key 和已有备份；不得从此页面尝试 Fresh/Adopt/Upgrade 写入。',
    };
  }
  if (
    value.status === 'unavailable' &&
    (value.mode === undefined || value.mode === null) &&
    value.auth_entry === 'unavailable' &&
    value.reason === 'observation_unavailable' &&
    value.authentication_required === false
  ) {
    return {
      tone: 'warn',
      flow: 'host',
      title: '来源状态暂无法确认',
      retryable: true,
      detail:
        '来源只读检查未获得可信分类。请在受信宿主机排查；不能根据此状态认定为 Fresh，也不能据此执行初始化或迁移。',
    };
  }
  return null;
}

function describeBootstrapError(
  err: unknown,
  stage: 'start' | 'generate' | 'commit' | 'refresh'
): ActionFeedback {
  if (err instanceof BootstrapTransportError) {
    if (err.kind === 'transport') {
      return {
        tone: 'warn',
        title: '网络连接异常（Bootstrap Transport Unavailable）',
        detail:
          stage === 'commit'
            ? '提交结果尚未确认，当前密钥与幂等提交标识（commitId）已保留在内存中。请检查网络或服务状态后点击“同步服务端状态”；确认服务端仍未初始化后才可重试。请勿刷新页面或更换密钥。'
            : stage === 'start'
              ? '无法连接至 Manager Bootstrap 服务端。您可尝试重新连接一次；若服务端已消费本次一次性 Claim Token 并返回 HTTP 401，请在宿主机重新执行 cpa-manager-plus initialize 获取新的 Handoff 链接。'
              : '无法连接至 Manager Bootstrap 服务端，请确认本地受控容器或服务进程正在运行后重试。',
      };
    }
    if (err.kind === 'rejected' || err.kind === 'authority') {
      return {
        tone: 'danger',
        title: 'Bootstrap 授权已失效或被拒绝（HTTP 401）',
        detail:
          stage === 'start'
            ? '一次性认领令牌（Claim Token）已被消费、过期，或被更新的初始化代际（Claim Generation）取代。请在宿主机重新执行 cpa-manager-plus initialize 获取最新 Handoff 链接。'
            : '当前 Bootstrap 会话授权已失效，或该操作在当前阶段已关闭。请点击“同步服务端状态”检查最新进度，或在宿主机重新执行初始化命令。',
      };
    }
    if (err.kind === 'mismatch') {
      return {
        tone: 'danger',
        title: '安装实例身份不匹配（Bootstrap Authority Mismatch）',
        detail:
          '服务端返回的 FreshOperation 身份（admissionId / installationId / generation / operationId）与 Handoff 凭证不一致，已按 Fail-Closed 策略中止操作。',
      };
    }
    if (err.kind === 'bad_request') {
      return {
        tone: 'danger',
        title: '请求参数未通过服务端校验（HTTP 400）',
        detail:
          '管理员密钥格式、字节长度或字符复杂度不符合服务端安全要求，请检查密钥规则或使用“一键生成”重新生成。',
      };
    }
    if (err.kind === 'conflict') {
      return {
        tone: 'warn',
        title: '操作状态冲突（HTTP 409）',
        detail:
          '服务端 FreshOperation 状态已发生变更（例如管理员已完成初始化或正在使用不同幂等标识提交）。请点击“同步服务端状态”获取最新状态。',
      };
    }
    return {
      tone: 'danger',
      title: `服务端暂时不可用${err.status ? `（HTTP ${err.status}）` : ''}`,
      detail: 'Bootstrap 服务端未能完成当前请求，请稍后重试或同步服务端状态。',
    };
  }
  return {
    tone: 'danger',
    title: '操作未能完成',
    detail: '发生未预期的客户端异常，请刷新服务端状态或重新获取 Handoff 链接。',
  };
}

export function FreshBootstrapPage({ handoff, transport }: FreshBootstrapPageProps) {
  const [lightTheme, setLightTheme] = useState(true);
  const [phase, setPhase] = useState<BootPhase>(handoff ? 'connecting' : 'no_handoff');
  const [operation, setOperation] = useState<FreshOperation | null>(null);
  const [initError, setInitError] = useState<ActionFeedback | null>(null);
  const [initErrorKind, setInitErrorKind] = useState<string | null>(null);
  const [discoveryNote, setDiscoveryNote] = useState<DiscoveryGuidance | null>(null);
  const [discoveryChecked, setDiscoveryChecked] = useState(false);
  const [discoveryAttempt, setDiscoveryAttempt] = useState(0);
  const startedRef = useRef(false);
  useEffect(() => {
    document.documentElement.classList.toggle('light', lightTheme);
    document.documentElement.setAttribute('data-theme', lightTheme ? 'white' : 'dark');
  }, [lightTheme]);
  const executeStart = useCallback(
    async (targetHandoff: Handoff) => {
      setPhase('connecting');
      setInitError(null);
      setInitErrorKind(null);
      try {
        const op = await transport.start(targetHandoff);
        setOperation(op);
        setPhase('ready');
      } catch (err) {
        const kind = err instanceof BootstrapTransportError ? err.kind : 'unknown';
        setInitErrorKind(kind);
        setInitError(describeBootstrapError(err, 'start'));
        setPhase('init_error');
      }
    },
    [transport]
  );

  useEffect(() => {
    if (handoff) return;
    let active = true;
    void fetch('/.well-known/cpamp-bootstrap', {
      method: 'GET',
      mode: 'same-origin',
      credentials: 'omit',
      redirect: 'error',
      cache: 'no-store',
      headers: { Accept: 'application/json' },
    })
      .then(async (response) => {
        if (response.status !== 200 && response.status !== 503) return null;
        const payload: unknown = await response.json();
        if (!payload || typeof payload !== 'object') return null;
        // Public discovery uses 200 for classified sources and 503 only for
        // unavailable. A mismatched HTTP status must never suggest Fresh setup.
        const unavailable = (payload as Record<string, unknown>).status === 'unavailable';
        return unavailable === (response.status === 503) ? payload : null;
      })
      .then((payload: unknown) => {
        if (!active) return;
        setDiscoveryNote(publicDiscoveryGuidance(payload));
        setDiscoveryChecked(true);
      })
      .catch(() => {
        // An unverified source must not receive Fresh setup guidance.
        if (active) setDiscoveryChecked(true);
      });
    return () => {
      active = false;
    };
  }, [handoff, discoveryAttempt]);

  useEffect(() => {
    // main.tsx remounts this boundary for each handoff/hash change. The
    // initial phase already covers a missing handoff without another render.
    if (!handoff) return;
    if (startedRef.current) {
      return;
    }
    startedRef.current = true;
    // A newly mounted host handoff starts one external claim exchange. The
    // callback also owns the pending/error transitions used by manual retry.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    void executeStart(handoff);
  }, [handoff, executeStart]);

  if (!handoff && discoveryNote?.flow === 'upgrade') return <Suspense fallback={<></>}><UpgradeAuthenticationEntry /></Suspense>;

  return (
    <div className="bootstrap-shell" data-testid="fresh-bootstrap-shell">
      <header className="bootstrap-header">
        <div className="brand-group">
          <img className="journey-logo" src={lightTheme ? logo : logoOnDark} alt="CPAMP" />
          <span className="badge badge-indigo">
            {phase === 'no_handoff' && discoveryNote?.flow !== 'fresh'
              ? journey('preflight')
              : journey('preview')}
          </span>
          {operation && (
            <span
              className={`badge ${operation.productComplete ? 'badge-emerald' : 'badge-amber'}`}
              data-testid="gateway-status-badge"
            >
              {operation.productComplete ? shellText('ready') : journey('pending')}
            </span>
          )}
        </div>
        <div className="header-actions">
          <button
            type="button"
            className="btn btn-ghost btn-icon-only"
            onClick={() => setLightTheme((prev) => !prev)}
            aria-label={journey('theme')}
            title={journey('theme')}
          >
            <FullWebIcon name={lightTheme ? 'moon' : 'sun'} size={24} />
          </button>
          <span className="journey-version">v2</span>
        </div>
      </header>

      <main className="bootstrap-main">
        <div className={`wizard-container ${phase === 'ready' ? 'wizard-container-setup' : ''}`}>
          {phase === 'no_handoff' && (
            <section className="wizard-card" data-testid="bootstrap-no-handoff">
              <div className="wizard-card-header">
                <div className="wizard-step-tag">
                  <span>
                    {discoveryNote?.flow === 'fresh'
                      ? 'Bootstrap Gate · 需要安装主机授权'
                      : 'Source Preflight · 只读来源检查'}
                  </span>
                </div>
                <h1 className="wizard-title">
                  {discoveryNote?.flow === 'host'
                    ? '当前来源需要宿主机预检，不能直接 Fresh 初始化'
                    : discoveryNote?.flow === 'fresh'
                      ? journey('claimRequired')
                      : discoveryChecked
                        ? '来源状态无法确认，禁止直接初始化'
                        : '正在确认来源状态，禁止直接初始化'}
                </h1>
                <p className="wizard-desc">
                  {discoveryNote?.flow === 'host'
                    ? '公共 Discovery 仅提供来源类型提示，不读取或修改现有数据；后续操作必须在受信宿主机执行。'
                    : discoveryNote?.flow === 'fresh'
                      ? journey('claimRequiredHelp')
                      : '尚未取得可信的 Fresh 来源分类；请检查宿主机上的来源与服务状态，不能依据当前页面执行初始化或迁移。'}
                </p>
              </div>
              <div className="wizard-card-body">
                {discoveryNote && (
                  <div
                    className={`alert ${discoveryNote.tone === 'warn' ? 'alert-warn' : 'alert-info'}`}
                    data-testid="bootstrap-discovery-guidance"
                    role="status"
                  >
                    <div className="alert-content">
                      <div className="alert-title">{discoveryNote.title}</div>
                      <div className="alert-desc">{discoveryNote.detail}</div>
                    </div>
                  </div>
                )}

                {discoveryNote?.flow === 'host' ? (
                  <div className="alert alert-warn" data-testid="host-preflight-instructions">
                    <div className="alert-content">
                      <div className="alert-title">下一步：在宿主机执行只读来源预检</div>
                      <div className="alert-desc">
                        把路径替换为真实来源数据库路径。此命令不会授权迁移，且不要在来源写入仍运行时声称已停止。
                      </div>
                      <div className="code-block" data-testid="host-preflight-command">
                        cpa-manager-plus legacy-preflight --db /path/to/usage.sqlite --summary
                      </div>
                    </div>
                  </div>
                ) : discoveryNote?.flow === 'fresh' ? (
                  <>
                    <div className="alert alert-warn">
                      <FullWebIcon name="alert" size={22} />
                      <div className="alert-content">
                        <div className="alert-title">如何获取或恢复 Bootstrap 授权链接？</div>
                        <div className="alert-desc">
                          请在受信宿主机运行初始化命令，并使用输出的带 <code>#handoff=...</code>{' '}
                          片段的完整 URL 重新打开本页面：
                        </div>
                      </div>
                    </div>
                    <div className="code-block" data-testid="cli-handoff-command">
                      cpa-manager-plus initialize
                    </div>
                  </>
                ) : (
                  <div
                    className="alert alert-warn"
                    data-testid="bootstrap-discovery-unconfirmed"
                    role="status"
                  >
                    <div className="alert-content">
                      <div className="alert-title">
                        {discoveryChecked ? '来源状态无法确认' : '正在读取来源状态'}
                      </div>
                      <div className="alert-desc">
                        不提供 Fresh
                        初始化命令；请在受信宿主机确认来源、日志与服务状态。未知结果不会授予任何写入权限。
                      </div>
                    </div>
                  </div>
                )}
                {discoveryChecked && (!discoveryNote || discoveryNote.retryable) && (
                  <button
                    className="btn btn-secondary"
                    type="button"
                    data-testid="bootstrap-recheck-discovery"
                    onClick={() => {
                      // Reissue only the public read-only discovery GET.
                      // An unverified result never grants a claim or write authority.
                      setDiscoveryChecked(false);
                      setDiscoveryNote(null);
                      setDiscoveryAttempt((attempt) => attempt + 1);
                    }}
                  >
                    重新检查来源状态
                  </button>
                )}
              </div>
            </section>
          )}

          {phase === 'connecting' && (
            <section className="wizard-card" data-testid="bootstrap-connecting">
              <div className="wizard-card-header">
                <div className="wizard-step-tag">
                  <span>{journey('claim')}</span>
                </div>
                <h1 className="wizard-title">
                  {handoff?.resume ? journey('resuming') : journey('claiming')}
                </h1>
                <p className="wizard-desc">{journey('claimingHelp')}</p>
              </div>
              <div className="wizard-card-body">
                <details>
                  <summary>{journey('diagnostics')}</summary>
                  <div className="info-card">
                    <div className="info-row">
                      <span className="info-key">授权模式</span>
                      <span className="info-val">
                        {handoff?.resume
                          ? 'Resume Session (GET operation)'
                          : 'Claim Exchange (POST claim/exchange)'}
                      </span>
                    </div>
                    {handoff && (
                      <>
                        <div className="info-row">
                          <span className="info-key">Operation ID</span>
                          <span className="info-val">{handoff.identity.operationId}</span>
                        </div>
                        <div className="info-row">
                          <span className="info-key">Installation / Gen</span>
                          <span className="info-val">
                            {handoff.identity.installationId} · {handoff.identity.generation}
                          </span>
                        </div>
                      </>
                    )}
                  </div>
                </details>
              </div>
            </section>
          )}

          {phase === 'init_error' && initError && (
            <section className="wizard-card" data-testid="bootstrap-authority-error">
              <div className="wizard-card-header">
                <div className="wizard-step-tag">
                  <span>Bootstrap Authority Failed</span>
                </div>
                <h1 className="wizard-title">无法建立 Fresh Bootstrap 受控会话</h1>
                <p className="wizard-desc">
                  客户端已完成地址栏敏感片段清理，但与 Manager Bootstrap
                  后端交换或恢复授权时未通过验证。
                </p>
              </div>
              <div className="wizard-card-body">
                <div
                  className={`alert ${initError.tone === 'warn' ? 'alert-warn' : 'alert-danger'}`}
                  role="alert"
                >
                  <FullWebIcon name="alert" size={22} />
                  <div className="alert-content">
                    <div className="alert-title" data-testid="init-error-title">
                      {initError.title}
                    </div>
                    <div className="alert-desc" data-testid="init-error-detail">
                      {initError.detail}
                    </div>
                  </div>
                </div>

                {handoff && (
                  <div className="info-card">
                    <div className="info-row">
                      <span className="info-key">Operation ID</span>
                      <span className="info-val">{handoff.identity.operationId}</span>
                    </div>
                    <div className="info-row">
                      <span className="info-key">Claim Generation</span>
                      <span className="info-val">#{handoff.claimGeneration}</span>
                    </div>
                    <div className="info-row">
                      <span className="info-key">Handoff 模式</span>
                      <span className="info-val">{handoff.resume ? 'Resume' : 'Fresh Claim'}</span>
                    </div>
                  </div>
                )}

                <div className="code-block">cpa-manager-plus initialize</div>
              </div>
              {handoff && (initErrorKind === 'transport' || initErrorKind === 'unavailable') && (
                <div className="wizard-card-footer">
                  <span style={{ fontSize: 12, color: 'var(--text-tertiary)' }}>
                    可尝试重新连接一次；若返回 HTTP 401（令牌已被消费），请在宿主机重新运行
                    cpa-manager-plus initialize 获取新 Handoff
                  </span>
                  <button
                    type="button"
                    className="btn btn-primary"
                    data-testid="retry-start-btn"
                    onClick={() => void executeStart(handoff)}
                  >
                    重试连接 Bootstrap 服务
                  </button>
                </div>
              )}
            </section>
          )}

          {phase === 'ready' && operation && (
            <FreshSetupWizard
              initialOperation={operation}
              transport={transport}
              onOperation={setOperation}
            />
          )}
        </div>
      </main>
      <footer className="journey-copyright">{journey('footer')}</footer>
    </div>
  );
}
