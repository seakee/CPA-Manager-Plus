import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ChangeEvent,
  type ReactNode,
} from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Card } from '@/components/ui/Card';
import { Modal } from '@/components/ui/Modal';
import { Select } from '@/components/ui/Select';
import { SegmentedTabs } from '@/components/ui/SegmentedTabs';
import {
  IconCheck,
  IconChevronRight,
  IconCopy,
  IconDownload,
  IconInfo,
  IconExternalLink,
  IconRefreshCw,
  IconTriangleAlert,
} from '@/components/ui/icons';
import { useHeaderRefresh } from '@/hooks/useHeaderRefresh';
import {
  buildDashboardVersionReleaseURL,
  normalizeDashboardReleaseTag,
} from '@/features/dashboard/versionReleaseLinks';
import { copyToClipboard } from '@/utils/clipboard';
import { formatDateTime } from '@/utils/format';
import { useManagerUpdates } from './ManagerUpdates';
import { CPAUpdateCard } from './CPAUpdateCard';
import { isValidHttpsCDNPrefix } from './cpaUpdateApi';
import { useCPAUpdateFlow } from './useCPAUpdateFlow';
import {
  activateManagerSlot,
  DEFAULT_SLOT_CDN_PRESETS,
  fetchManagerUpdateStatus,
  MANAGER_SLOT_MAX_UPLOAD_BYTES,
  ManagerSelfUpdateError,
  prepareManagerSlot,
  rollbackManagerSlot,
  uploadManagerSlot,
  type DockerImageRole,
  type ReleaseInfo,
  type SelfUpdateStatus,
  type SlotPrepareOperation,
  type UpdateChannel,
  type UpdateStatus,
} from './managerUpdateApi';
import styles from './ManagerUpdatePage.module.scss';

const SLOT_FILENAME_VERSION_RE = /^cpamp-slot_(.+)_linux_(amd64|arm64)\.tar\.gz$/;
const CANONICAL_TAG_RE =
  /^v(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-(?:rc|beta|alpha)(?:\.[0-9A-Za-z-]+)?)?$/;
const RECONNECT_POLL_INTERVAL_MS = 2_000;
const RECONNECT_TIMEOUT_MS = 90_000;
const PREPARE_POLL_INTERVAL_MS = 1_000;

type SlotSourceMode = 'github' | 'cdn' | 'upload';
type SlotConfirmation =
  | { kind: 'activate'; version: string }
  | { kind: 'rollback'; activeSlot: string; targetVersion: string };

type ReconnectState = {
  action: 'activate' | 'rollback';
  targetVersion: string;
  attemptId?: string;
  startedAt: number;
  phase: 'polling' | 'trial';
};

const SEMVER_RE =
  /^v?(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?(?:\+([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?$/;

function parseSemverParts(
  version: string
): { major: number; minor: number; patch: number; pre: string[] } | null {
  const match = SEMVER_RE.exec(version.trim());
  if (!match) return null;
  const pre = match[4] ? match[4].split('.') : [];
  for (const part of pre) {
    if (/^\d+$/.test(part) && part.length > 1 && part.startsWith('0')) {
      return null;
    }
  }
  return {
    major: Number(match[1]),
    minor: Number(match[2]),
    patch: Number(match[3]),
    pre,
  };
}

function compareSemver(a: string, b: string): number | null {
  const pa = parseSemverParts(a);
  const pb = parseSemverParts(b);
  if (!pa || !pb) return null;
  if (pa.major !== pb.major) return pa.major < pb.major ? -1 : 1;
  if (pa.minor !== pb.minor) return pa.minor < pb.minor ? -1 : 1;
  if (pa.patch !== pb.patch) return pa.patch < pb.patch ? -1 : 1;
  if (pa.pre.length === 0 && pb.pre.length === 0) return 0;
  if (pa.pre.length === 0) return 1;
  if (pb.pre.length === 0) return -1;
  const len = Math.min(pa.pre.length, pb.pre.length);
  for (let i = 0; i < len; i += 1) {
    const x = pa.pre[i];
    const y = pb.pre[i];
    if (x === y) continue;
    const xn = /^\d+$/.test(x);
    const yn = /^\d+$/.test(y);
    if (xn && yn) {
      return Number(x) < Number(y) ? -1 : 1;
    }
    if (xn !== yn) return xn ? -1 : 1;
    return x < y ? -1 : 1;
  }
  if (pa.pre.length === pb.pre.length) return 0;
  return pa.pre.length < pb.pre.length ? -1 : 1;
}

type SlotCompletedState = {
  action: 'activate' | 'rollback';
  version: string;
};

function formatMB(bytes: number): string {
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

function isInProgressPhase(phase?: SlotPrepareOperation['phase']): boolean {
  return phase === 'downloading' || phase === 'verifying' || phase === 'staging';
}

function formatSlotErrorMessage(
  err: unknown,
  t: (key: string, options?: Record<string, unknown>) => string
): string {
  if (err instanceof ManagerSelfUpdateError) {
    return t('manager_updates.slot_error_failed', {
      code: err.code,
      message: err.messageText,
    });
  }
  const message = err instanceof Error ? err.message : t('common.unknown_error');
  return t('manager_updates.slot_error_failed', {
    code: 'request_failed',
    message,
  });
}

function ReleaseDetails({
  info,
  action,
  deploymentVariant,
  upgradeAllowed,
  selfUpdateSlotSection,
}: {
  info: ReleaseInfo;
  action: UpdateStatus['upgrade_action'];
  deploymentVariant: UpdateStatus['deployment_variant'];
  upgradeAllowed: boolean;
  selfUpdateSlotSection?: ReactNode;
}) {
  const { t, i18n } = useTranslation();
  const [stepsOpen, setStepsOpen] = useState(false);
  const [deployment, setDeployment] = useState<'docker' | 'native'>(() =>
    deploymentVariant === 'native' ? 'native' : 'docker'
  );
  const [dockerVariant, setDockerVariant] = useState<DockerImageRole>(() =>
    deploymentVariant === 'docker-slim' ? 'slim' : 'full'
  );
  const [dockerRegistry, setDockerRegistry] = useState<'dockerhub' | 'ghcr'>('dockerhub');
  const [dockerMode, setDockerMode] = useState<'compose' | 'run'>('compose');
  const [dockerTarget, setDockerTarget] = useState('cpa-manager-plus');
  const [copyState, setCopyState] = useState<'idle' | 'copied' | 'copy_failed'>('idle');
  const [commandCopyState, setCommandCopyState] = useState<'idle' | 'copied' | 'copy_failed'>(
    'idle'
  );
  const [nativeCommandCopyState, setNativeCommandCopyState] = useState<
    'idle' | 'copied' | 'copy_failed'
  >('idle');
  const copyTimerRef = useRef<number | undefined>(undefined);
  const commandTimerRef = useRef<number | undefined>(undefined);
  const nativeTimerRef = useRef<number | undefined>(undefined);
  const direct = action === 'direct' && !info.update.migration_required && !info.update.breaking;
  const releaseUrl = buildDashboardVersionReleaseURL('manager', info.release.version);
  const releaseTag = normalizeDashboardReleaseTag(info.release.version) || info.release.version;
  const dockerImages = info.distribution.docker.images ?? [];
  const registryRepo =
    dockerRegistry === 'ghcr'
      ? 'ghcr.io/seakee/cpa-manager-plus'
      : 'seakee/cpa-manager-plus';
  const matchedImage =
    dockerImages.find((entry) => entry.role === dockerVariant && entry.name === registryRepo) ??
    dockerImages.find((entry) => entry.role === dockerVariant) ??
    dockerImages.find((entry) => entry.name === registryRepo) ??
    dockerImages[0];
  const baseVersionTag = (info.distribution.docker.version_tag || info.release.version).replace(
    /^v/,
    ''
  );
  const fallbackTag = dockerVariant === 'full' ? `${baseVersionTag}-full` : baseVersionTag;
  const image = matchedImage
    ? matchedImage.digest
      ? `${matchedImage.name}:${matchedImage.tag}@${matchedImage.digest}`
      : `${matchedImage.name}:${matchedImage.tag}`
    : `${registryRepo}:${fallbackTag}`;
  const language = i18n.language.startsWith('zh') ? 'zh' : 'en';
  const releaseNotes = (info.content.notes[language] || info.content.notes.en || '').trim();
  const nativeAssets = info.distribution.native.assets;
  const nativeAssetsKey = nativeAssets.join('\0');
  const [selectedAsset, setSelectedAsset] = useState(() => pickDefaultNativeAsset(nativeAssets));
  const hasSelfUpdate = Boolean(selfUpdateSlotSection);

  useEffect(() => {
    setDeployment(deploymentVariant === 'native' ? 'native' : 'docker');
    setDockerVariant(deploymentVariant === 'docker-slim' ? 'slim' : 'full');
  }, [deploymentVariant]);
  useEffect(() => {
    setSelectedAsset(pickDefaultNativeAsset(nativeAssets));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [nativeAssetsKey]);
  useEffect(
    () => () => {
      if (copyTimerRef.current !== undefined) globalThis.clearTimeout(copyTimerRef.current);
      if (commandTimerRef.current !== undefined) globalThis.clearTimeout(commandTimerRef.current);
      if (nativeTimerRef.current !== undefined) globalThis.clearTimeout(nativeTimerRef.current);
    },
    []
  );

  const scheduleReset = (timerRef: { current: number | undefined }, reset: () => void) => {
    if (timerRef.current !== undefined) globalThis.clearTimeout(timerRef.current);
    timerRef.current = globalThis.setTimeout(() => {
      timerRef.current = undefined;
      reset();
    }, 1800) as unknown as number;
  };

  const copyImage = async () => {
    const ok = await copyToClipboard(image);
    setCopyState(ok ? 'copied' : 'copy_failed');
    scheduleReset(copyTimerRef, () => setCopyState('idle'));
  };

  const safeTarget = normalizeComposeServiceName(dockerTarget);
  const safeImage = sanitizeShellArg(image);
  const dockerCommand =
    dockerMode === 'compose'
      ? `docker compose pull ${safeTarget} && docker compose up -d ${safeTarget}`
      : `docker pull ${safeImage}`;

  const copyDockerCommand = async () => {
    const ok = await copyToClipboard(dockerCommand);
    setCommandCopyState(ok ? 'copied' : 'copy_failed');
    scheduleReset(commandTimerRef, () => setCommandCopyState('idle'));
  };

  const activeAsset = nativeAssets.includes(selectedAsset)
    ? selectedAsset
    : nativeAssets[0] || '';
  const assetDownloadUrl = activeAsset
    ? `https://github.com/seakee/CPA-Manager-Plus/releases/download/${encodeURIComponent(releaseTag)}/${encodeURIComponent(activeAsset)}`
    : '';
  const extractedDir = activeAsset.endsWith('.tar.gz')
    ? activeAsset.slice(0, -'.tar.gz'.length)
    : activeAsset.endsWith('.zip')
      ? activeAsset.slice(0, -'.zip'.length)
      : '';
  const safeDownloadUrl = assetDownloadUrl ? sanitizeShellArg(assetDownloadUrl) : '';
  const safeAsset = sanitizeShellArg(activeAsset);
  const safeExtractedDir = sanitizeShellArg(extractedDir);
  const nativeCommand = safeDownloadUrl
    ? activeAsset.endsWith('.zip')
      ? `curl -fLO ${safeDownloadUrl}`
      : extractedDir
        ? `curl -fLO ${safeDownloadUrl} && tar -xzf ${safeAsset} && sudo ./${safeExtractedDir}/cpa-manager-plusctl upgrade --home /opt/cpa-manager-plus`
        : `curl -fLO ${safeDownloadUrl} && tar -xzf ${safeAsset}`
    : '';
  const copyNativeCommand = async () => {
    if (!nativeCommand) return;
    const ok = await copyToClipboard(nativeCommand);
    setNativeCommandCopyState(ok ? 'copied' : 'copy_failed');
    scheduleReset(nativeTimerRef, () => setNativeCommandCopyState('idle'));
  };

  return (
    <>
      <p className={styles.summary}>{info.content.summary[language] || info.content.summary.en}</p>
      {releaseNotes && (
        <details className={styles.notesDetails}>
          <summary>
            <IconChevronRight size={14} aria-hidden="true" />
            {t('manager_updates.notes_toggle')}
          </summary>
          <div className={styles.notesBody}>{releaseNotes}</div>
        </details>
      )}
      {(!direct || info.compatibility.minimum_cpa_version) && (
        <div className={styles.requirements} role="note">
          <IconInfo size={18} aria-hidden="true" />
          <div>
            {!direct && <p>{t('manager_updates.migration')}</p>}
            {info.compatibility.minimum_cpa_version && (
              <p>
                {t('manager_updates.minimum_cpa', {
                  version: info.compatibility.minimum_cpa_version,
                })}
              </p>
            )}
          </div>
        </div>
      )}
      {selfUpdateSlotSection}
      <div className={styles.actions}>
        {upgradeAllowed &&
          (direct ? (
            <Button
              type="button"
              variant={hasSelfUpdate ? 'secondary' : 'primary'}
              onClick={() => setStepsOpen((open) => !open)}
              aria-expanded={stepsOpen}
              aria-controls="manager-upgrade-steps"
            >
              {t(
                hasSelfUpdate
                  ? stepsOpen
                    ? 'manager_updates.manual_commands_hide'
                    : 'manager_updates.manual_commands_show'
                  : stepsOpen
                    ? 'manager_updates.hide_steps'
                    : 'manager_updates.show_steps'
              )}
              <IconChevronRight size={15} aria-hidden="true" />
            </Button>
          ) : (
            <a
              className="btn btn-primary"
              href={info.update.upgrade_guide_url}
              target="_blank"
              rel="noopener noreferrer"
            >
              {t('manager_updates.migration_guide')}
              <IconExternalLink size={15} aria-hidden="true" />
            </a>
          ))}
        {releaseUrl && (
          <a
            className={styles.secondaryLink}
            href={releaseUrl}
            target="_blank"
            rel="noopener noreferrer"
          >
            {t('manager_updates.release_notes')}
            <IconExternalLink size={14} aria-hidden="true" />
          </a>
        )}
      </div>
      {upgradeAllowed && direct && (
        <section
          id="manager-upgrade-steps"
          className={styles.steps}
          hidden={!stepsOpen}
          aria-labelledby="manager-upgrade-title"
        >
          <h3 id="manager-upgrade-title">
            {t('manager_updates.upgrade_to', { version: info.release.version })}
          </h3>
          <SegmentedTabs
            idBase="manager-deployment"
            items={[
              { id: 'docker', label: t('manager_updates.docker') },
              { id: 'native', label: t('manager_updates.native') },
            ]}
            activeTab={deployment}
            onChange={setDeployment}
            ariaLabel={t('manager_updates.deployment')}
            className={styles.deploymentTabs}
          />
          <div role="tabpanel" aria-labelledby={'manager-deployment-' + deployment}>
            <ol className={styles.instructions}>
              <li>{t('manager_updates.backup_step')}</li>
              <li>
                {t(
                  deployment === 'docker'
                    ? 'manager_updates.docker_step'
                    : 'manager_updates.native_step'
                )}
              </li>
              <li>{t('manager_updates.restart_step')}</li>
            </ol>
            {deployment === 'docker' ? (
              <>
                <div className={styles.dockerControls}>
                  <div className={styles.commandTargetRow}>
                    <span className={styles.commandLabel}>
                      {t('manager_updates.docker_variant_label')}
                    </span>
                    <SegmentedTabs
                      idBase="manager-docker-variant"
                      variant="joined"
                      responsiveFullWidth={false}
                      items={[
                        { id: 'full', label: t('manager_updates.docker_variant_full') },
                        { id: 'slim', label: t('manager_updates.docker_variant_slim') },
                      ]}
                      activeTab={dockerVariant}
                      onChange={(value) => {
                        setDockerVariant(value);
                        setCopyState('idle');
                        setCommandCopyState('idle');
                      }}
                      ariaLabel={t('manager_updates.docker_variant_label')}
                      className={styles.commandSubTabs}
                    />
                  </div>
                  <div className={styles.commandTargetRow}>
                    <span className={styles.commandLabel}>
                      {t('manager_updates.docker_registry_label')}
                    </span>
                    <SegmentedTabs
                      idBase="manager-docker-registry"
                      variant="joined"
                      responsiveFullWidth={false}
                      items={[
                        { id: 'dockerhub', label: t('manager_updates.docker_registry_dockerhub') },
                        { id: 'ghcr', label: t('manager_updates.docker_registry_ghcr') },
                      ]}
                      activeTab={dockerRegistry}
                      onChange={(value) => {
                        setDockerRegistry(value);
                        setCopyState('idle');
                        setCommandCopyState('idle');
                      }}
                      ariaLabel={t('manager_updates.docker_registry_label')}
                      className={styles.commandSubTabs}
                    />
                  </div>
                </div>
                <div className={styles.imageRow}>
                  <code>{image}</code>
                  <Button type="button" variant="ghost" size="sm" onClick={() => void copyImage()}>
                    {copyState === 'copied' ? (
                      <IconCheck size={14} aria-hidden="true" />
                    ) : (
                      <IconCopy size={14} aria-hidden="true" />
                    )}
                    {t('manager_updates.copy_image')}
                  </Button>
                  {copyState !== 'idle' && (
                    <span className={styles.copyFeedback} role="status">
                      {t('manager_updates.' + copyState)}
                    </span>
                  )}
                </div>
                <div className={styles.commandGenerator}>
                  <div className={styles.commandHead}>
                    <span className={styles.commandLabel}>
                      {t('manager_updates.docker_command_label')}
                    </span>
                    <SegmentedTabs
                      idBase="manager-docker-mode"
                      items={[
                        { id: 'compose', label: t('manager_updates.docker_compose') },
                        { id: 'run', label: t('manager_updates.docker_run') },
                      ]}
                      activeTab={dockerMode}
                      onChange={setDockerMode}
                      ariaLabel={t('manager_updates.docker_mode')}
                      className={styles.commandSubTabs}
                    />
                  </div>
                  {dockerMode === 'compose' && (
                    <div className={styles.commandTargetRow}>
                      <label htmlFor="manager-docker-target">
                        {t('manager_updates.docker_target_label')}
                      </label>
                      <input
                        id="manager-docker-target"
                        type="text"
                        className={styles.commandInput}
                        value={dockerTarget}
                        placeholder={t('manager_updates.docker_target_placeholder')}
                        onChange={(event) => setDockerTarget(event.target.value)}
                      />
                    </div>
                  )}
                  <p className={styles.commandHint}>
                    {t(
                      dockerMode === 'compose'
                        ? 'manager_updates.docker_compose_hint'
                        : 'manager_updates.docker_run_hint',
                      dockerMode === 'compose' ? { image } : undefined
                    )}
                  </p>
                  <div className={styles.commandCodeRow}>
                    <pre>{dockerCommand}</pre>
                    <Button
                      type="button"
                      variant="ghost"
                      size="sm"
                      onClick={() => void copyDockerCommand()}
                    >
                      {commandCopyState === 'copied' ? (
                        <IconCheck size={14} aria-hidden="true" />
                      ) : (
                        <IconCopy size={14} aria-hidden="true" />
                      )}
                      {t('manager_updates.copy_command')}
                    </Button>
                    {commandCopyState !== 'idle' && (
                      <span className={styles.copyFeedback} role="status">
                        {t(
                          commandCopyState === 'copied'
                            ? 'manager_updates.copied'
                            : 'manager_updates.copy_command_failed'
                        )}
                      </span>
                    )}
                  </div>
                </div>
              </>
            ) : (
              <>
                {nativeAssets.length > 0 ? (
                  <div className={styles.commandGenerator}>
                    <div className={styles.commandHead}>
                      <span className={styles.commandLabel}>
                        {t('manager_updates.native_package')}
                      </span>
                    </div>
                    {nativeAssets.length > 1 && (
                      <Select
                        id="manager-native-asset"
                        value={activeAsset}
                        options={nativeAssets.map((asset) => ({ value: asset, label: asset }))}
                        onChange={setSelectedAsset}
                        ariaLabel={t('manager_updates.native_package')}
                        className={styles.nativeSelect}
                      />
                    )}
                    <div className={styles.nativeAssetActions}>
                      {assetDownloadUrl && (
                        <a
                          className="btn btn-secondary btn-sm"
                          href={assetDownloadUrl}
                          target="_blank"
                          rel="noopener noreferrer"
                        >
                          <IconDownload size={14} aria-hidden="true" />
                          {t('manager_updates.native_direct_download', { asset: activeAsset })}
                        </a>
                      )}
                      {releaseUrl && (
                        <a
                          className={styles.secondaryLink}
                          href={releaseUrl}
                          target="_blank"
                          rel="noopener noreferrer"
                        >
                          {t('manager_updates.native_download', { version: info.release.version })}
                          <IconExternalLink size={14} aria-hidden="true" />
                        </a>
                      )}
                    </div>
                    {nativeCommand && (
                      <>
                        <span className={styles.commandLabel}>
                          {t('manager_updates.native_command_label')}
                        </span>
                        <p className={styles.commandHint}>
                          {t('manager_updates.native_command_hint')}
                        </p>
                        <div className={styles.commandCodeRow}>
                          <pre>{nativeCommand}</pre>
                          <Button
                            type="button"
                            variant="ghost"
                            size="sm"
                            onClick={() => void copyNativeCommand()}
                          >
                            {nativeCommandCopyState === 'copied' ? (
                              <IconCheck size={14} aria-hidden="true" />
                            ) : (
                              <IconCopy size={14} aria-hidden="true" />
                            )}
                            {t('manager_updates.copy_command')}
                          </Button>
                          {nativeCommandCopyState !== 'idle' && (
                            <span className={styles.copyFeedback} role="status">
                              {t(
                                nativeCommandCopyState === 'copied'
                                  ? 'manager_updates.copied'
                                  : 'manager_updates.copy_command_failed'
                              )}
                            </span>
                          )}
                        </div>
                      </>
                    )}
                  </div>
                ) : releaseUrl ? (
                  <a
                    className={styles.secondaryLink}
                    href={releaseUrl}
                    target="_blank"
                    rel="noopener noreferrer"
                  >
                    {t('manager_updates.native_download', { version: info.release.version })}
                    <IconExternalLink size={14} aria-hidden="true" />
                  </a>
                ) : null}
              </>
            )}
            <a
              className={styles.guideLink}
              href={info.update.upgrade_guide_url}
              target="_blank"
              rel="noopener noreferrer"
            >
              {t('manager_updates.upgrade_guide')}
              <IconExternalLink size={14} aria-hidden="true" />
            </a>
          </div>
        </section>
      )}
    </>
  );
}

function sanitizeShellArg(value: string): string {
  return /^[A-Za-z0-9._:@/-]+$/.test(value) ? value : `'${value.replace(/'/g, `'\\''`)}'`;
}

function normalizeComposeServiceName(raw: string): string {
  const trimmed = raw.trim();
  return /^[A-Za-z0-9][A-Za-z0-9._-]*$/.test(trimmed) ? trimmed : 'cpa-manager-plus';
}

function pickDefaultNativeAsset(assets: string[]): string {
  if (assets.length === 0) return '';
  if (typeof navigator === 'undefined') return assets[0];
  const hint = `${navigator.userAgent} ${navigator.platform}`.toLowerCase();
  const os =
    hint.includes('mac') || hint.includes('darwin')
      ? 'darwin'
      : hint.includes('win')
        ? 'windows'
        : 'linux';
  const arch = hint.includes('arm64') || hint.includes('aarch64') ? 'arm64' : 'amd64';
  return (
    assets.find((asset) => {
      const lower = asset.toLowerCase();
      return lower.includes(os) && lower.includes(arch);
    }) ||
    assets.find((asset) => asset.toLowerCase().includes(os)) ||
    assets[0]
  );
}

export function ManagerUpdatePage() {
  const { t, i18n } = useTranslation();
  const { status, available, busy, error, connection, refresh, check, setChannel } =
    useManagerUpdates();
  const effectiveConnection =
    connection && connection.base && connection.key ? connection : null;

  const cpa = useCPAUpdateFlow();
  const refreshCPA = cpa.refresh;
  const refreshUpdates = useCallback(async () => {
    await Promise.all([refresh(), refreshCPA()]);
  }, [refresh, refreshCPA]);
  const titleRef = useRef<HTMLHeadingElement>(null);
  useHeaderRefresh(refreshUpdates, available || cpa.available);
  useEffect(() => {
    titleRef.current?.focus({ preventScroll: true });
  }, []);

  const isUnknownVersionFailure =
    !error &&
    status?.state === 'unknown_version' &&
    (!status.last_error || status.last_error === 'unknown running version');
  const failed = error || (!!status?.last_error && !isUnknownVersionFailure);
  const stale = !!status?.stale;
  const upgradeAllowed = !failed && !stale;
  const info = available && status?.state === 'update_available' ? status.target : undefined;
  const selfUpdate = status?.self_update;

  const [sourceMode, setSourceMode] = useState<SlotSourceMode>('github');
  const [cdnPrefix, setCdnPrefix] = useState('');
  const [uploadFile, setUploadFile] = useState<File | null>(null);
  const [offlineVersion, setOfflineVersion] = useState('');
  const [uploadProgress, setUploadProgress] = useState<{ loaded: number; total: number } | null>(
    null
  );
  const [liveSelfUpdate, setLiveSelfUpdate] = useState<{
    base: SelfUpdateStatus | undefined;
    value: SelfUpdateStatus;
  } | null>(null);
  const [slotBusy, setSlotBusy] = useState(false);
  const [slotError, setSlotError] = useState<string | null>(null);
  const [confirmation, setConfirmation] = useState<SlotConfirmation | null>(null);
  const [reconnect, setReconnect] = useState<ReconnectState | null>(null);
  const [reconnectTimedOut, setReconnectTimedOut] = useState(false);
  const [slotCompleted, setSlotCompleted] = useState<SlotCompletedState | null>(null);
  const [optimisticStaged, setOptimisticStaged] = useState<string | null>(null);

  const activeSelfUpdate =
    liveSelfUpdate && liveSelfUpdate.base === selfUpdate ? liveSelfUpdate.value : selfUpdate;
  const recoveryRequired = activeSelfUpdate?.state === 'recovery_required';
  const runningSlot = status?.active_slot || activeSelfUpdate?.active_slot || '';
  const baseImageVersion = status?.base_image_version || activeSelfUpdate?.base_version || '';
  const showDualVersions = Boolean(
    status &&
      (runningSlot !== '' ||
        (baseImageVersion !== '' && baseImageVersion !== status.current_version))
  );

  const stagedSlots = useMemo(() => {
    const base = activeSelfUpdate?.staged_slots ?? [];
    if (optimisticStaged && !base.includes(optimisticStaged)) {
      return [...base, optimisticStaged];
    }
    return base;
  }, [activeSelfUpdate?.staged_slots, optimisticStaged]);

  const targetVersion = info?.release.version ?? '';
  const targetHasSlotAsset = Boolean(
    info?.distribution.slot?.assets?.some((asset) => asset.platform.startsWith('linux/'))
  );
  const directSlotUpgradeReady = Boolean(
    available &&
      info &&
      upgradeAllowed &&
      status?.upgrade_action === 'direct' &&
      !info.update.breaking &&
      !info.update.migration_required &&
      activeSelfUpdate?.supported &&
      targetHasSlotAsset
  );
  const blockedByMigration = Boolean(
    info &&
      (status?.upgrade_action === 'migration_guide' ||
        info.update.breaking ||
        info.update.migration_required)
  );
  const showOfflineUploadPanel = Boolean(
    available && activeSelfUpdate?.supported && !directSlotUpgradeReady && !blockedByMigration
  );

  const activePrepareOp = activeSelfUpdate?.prepare_operation ?? null;
  const preparingAsync = isInProgressPhase(activePrepareOp?.phase);

  const selfUpdateRef = useRef(selfUpdate);
  useEffect(() => {
    selfUpdateRef.current = selfUpdate;
  }, [selfUpdate]);

  const preparePollTargetRef = useRef<string | null>(null);
  useEffect(() => {
    if (!preparingAsync || !effectiveConnection) return;
    let cancelled = false;
    const controller = new AbortController();
    let timer: Parameters<typeof clearTimeout>[0];

    const tick = async () => {
      if (cancelled) return;
      try {
        const next = await fetchManagerUpdateStatus(
          effectiveConnection.base,
          effectiveConnection.key,
          controller.signal
        );
        if (cancelled) return;
        const nextSelf = next.self_update ?? null;
        if (nextSelf) setLiveSelfUpdate({ base: selfUpdateRef.current, value: nextSelf });
        const op = nextSelf?.prepare_operation;
        if (op && !isInProgressPhase(op.phase)) {
          setSlotBusy(false);
          if (op.phase === 'failed') {
            setSlotError(
              t('manager_updates.slot_error_failed', {
                code: op.error_code || 'prepare_failed',
                message: op.error_code || 'Slot prepare failed',
              })
            );
          } else if (op.phase === 'succeeded') {
            const ver = op.target_version || preparePollTargetRef.current || targetVersion;
            if (ver) {
              setOptimisticStaged(ver);
              setConfirmation({ kind: 'activate', version: ver });
            }
          }
          await refresh();
          return;
        }
      } catch {
        // Keep polling while prepare is running.
      }
      if (!cancelled) {
        timer = setTimeout(() => void tick(), PREPARE_POLL_INTERVAL_MS);
      }
    };

    timer = setTimeout(() => void tick(), PREPARE_POLL_INTERVAL_MS);
    return () => {
      cancelled = true;
      controller.abort();
      clearTimeout(timer);
    };
  }, [preparingAsync, effectiveConnection, refresh, targetVersion, t]);

  useEffect(() => {
    if (!reconnect || !effectiveConnection) return;
    let cancelled = false;
    const controller = new AbortController();
    let timer: Parameters<typeof clearTimeout>[0];

    const scheduleNext = () => {
      if (cancelled) return;
      if (Date.now() - reconnect.startedAt >= RECONNECT_TIMEOUT_MS) {
        setReconnect(null);
        setReconnectTimedOut(true);
        return;
      }
      timer = setTimeout(() => void poll(), RECONNECT_POLL_INTERVAL_MS);
    };

    const poll = async () => {
      if (cancelled) return;
      if (Date.now() - reconnect.startedAt >= RECONNECT_TIMEOUT_MS) {
        setReconnect(null);
        setReconnectTimedOut(true);
        return;
      }
      try {
        const next = await fetchManagerUpdateStatus(
          effectiveConnection.base,
          effectiveConnection.key,
          controller.signal
        );
        if (cancelled) return;
        const nextSelf = next.self_update;
        if (nextSelf) setLiveSelfUpdate({ base: selfUpdateRef.current, value: nextSelf });

        if (reconnect.action === 'activate') {
          const matchesAttempt =
            Boolean(reconnect.attemptId) && nextSelf?.last_attempt_id === reconnect.attemptId;
          if (
            matchesAttempt &&
            (nextSelf?.last_rolled_back_slot === reconnect.targetVersion ||
              nextSelf?.state === 'recovery_required')
          ) {
            setReconnect(null);
            setReconnectTimedOut(false);
            setOptimisticStaged(null);
            await refresh();
            return;
          }
        } else if (
          nextSelf?.state === 'recovery_required' &&
          (!reconnect.attemptId || nextSelf?.last_attempt_id === reconnect.attemptId)
        ) {
          setReconnect(null);
          setReconnectTimedOut(false);
          setOptimisticStaged(null);
          await refresh();
          return;
        }
        if (next.current_version === reconnect.targetVersion) {
          if (nextSelf?.state === 'trial') {
            const trialMatches =
              reconnect.action === 'activate'
                ? Boolean(reconnect.attemptId) &&
                  (!nextSelf.active_attempt_id || nextSelf.active_attempt_id === reconnect.attemptId)
                : !reconnect.attemptId ||
                  !nextSelf.active_attempt_id ||
                  nextSelf.active_attempt_id === reconnect.attemptId;
            if (trialMatches) {
              setReconnect((prev) =>
                prev && prev.targetVersion === reconnect.targetVersion
                  ? { ...prev, phase: 'trial' }
                  : prev
              );
            }
            scheduleNext();
            return;
          }
          const activateCommitted =
            reconnect.action !== 'activate' ||
            (Boolean(reconnect.attemptId) &&
              nextSelf?.state === 'committed' &&
              nextSelf.last_attempt_id === reconnect.attemptId);
          if (nextSelf?.state !== 'recovery_required' && activateCommitted) {
            setReconnect(null);
            setReconnectTimedOut(false);
            setOptimisticStaged(null);
            setSlotCompleted({ action: reconnect.action, version: reconnect.targetVersion });
            await refresh();
            return;
          }
        }
      } catch {
        // Tolerate transient network drops, 502, and 503 upgrade_trial_verifying during unix.Exec + Trial.
      }
      scheduleNext();
    };

    timer = setTimeout(() => void poll(), RECONNECT_POLL_INTERVAL_MS);
    return () => {
      cancelled = true;
      controller.abort();
      clearTimeout(timer);
    };
  }, [reconnect, effectiveConnection, refresh]);

  const trimmedPrefix = cdnPrefix.trim();
  const cdnPrefixValid = trimmedPrefix !== '' && isValidHttpsCDNPrefix(trimmedPrefix);
  const uploadErrorKey = (() => {
    if (!uploadFile) return null;
    if (uploadFile.size <= 0) return 'manager_updates.slot_upload_file_empty';
    if (uploadFile.size > MANAGER_SLOT_MAX_UPLOAD_BYTES)
      return 'manager_updates.slot_upload_file_too_large';
    return null;
  })();
  const offlineVersionTrimmed = offlineVersion.trim();
  const offlineVersionValid = CANONICAL_TAG_RE.test(offlineVersionTrimmed);

  const handleUploadFileChange = (event: ChangeEvent<HTMLInputElement>) => {
    const file = event.target.files?.[0] ?? null;
    setUploadFile(file);
    setSlotError(null);
    if (file && !offlineVersionTrimmed) {
      const parsed = SLOT_FILENAME_VERSION_RE.exec(file.name.trim())?.[1];
      if (parsed) setOfflineVersion(parsed);
    }
    try {
      event.target.value = '';
    } catch {
      // Ignore read-only synthetic event targets in tests.
    }
  };

  const handlePrepare = async () => {
    if (!effectiveConnection || !targetVersion || slotBusy || reconnect || recoveryRequired) return;
    setSlotError(null);
    setSlotCompleted(null);
    setReconnectTimedOut(false);
    preparePollTargetRef.current = targetVersion;
    setSlotBusy(true);
    try {
      let nextSelf: SelfUpdateStatus;
      if (sourceMode === 'upload') {
        if (!uploadFile || uploadErrorKey) {
          setSlotBusy(false);
          return;
        }
        const fileToUpload = uploadFile;
        setUploadProgress({ loaded: 0, total: fileToUpload.size });
        nextSelf = await uploadManagerSlot(
          effectiveConnection.base,
          effectiveConnection.key,
          targetVersion,
          fileToUpload,
          (loaded, total) => setUploadProgress({ loaded, total })
        );
        setUploadFile(null);
        setUploadProgress(null);
      } else {
        if (sourceMode === 'cdn' && !cdnPrefixValid) {
          setSlotBusy(false);
          return;
        }
        nextSelf = await prepareManagerSlot(effectiveConnection.base, effectiveConnection.key, {
          version: targetVersion,
          source: sourceMode,
          ...(sourceMode === 'cdn' ? { cdn_prefix: trimmedPrefix } : {}),
        });
      }
      setLiveSelfUpdate({ base: selfUpdateRef.current, value: nextSelf });
      if (
        nextSelf.staged_slots.includes(targetVersion) ||
        nextSelf.prepare_operation?.phase === 'succeeded'
      ) {
        setSlotBusy(false);
        setOptimisticStaged(targetVersion);
        await refresh();
        setConfirmation({ kind: 'activate', version: targetVersion });
      } else if (nextSelf.prepare_operation?.phase === 'failed') {
        setSlotBusy(false);
        setSlotError(
          t('manager_updates.slot_error_failed', {
            code: nextSelf.prepare_operation.error_code || 'prepare_failed',
            message: nextSelf.prepare_operation.error_code || 'Slot prepare failed',
          })
        );
      }
    } catch (err) {
      setSlotBusy(false);
      setUploadProgress(null);
      setSlotError(formatSlotErrorMessage(err, t));
    }
  };

  const handleOfflineUpload = async () => {
    if (
      !effectiveConnection ||
      !uploadFile ||
      uploadErrorKey ||
      !offlineVersionValid ||
      slotBusy ||
      reconnect ||
      recoveryRequired
    ) {
      return;
    }
    const ver = offlineVersionTrimmed;
    const fileToUpload = uploadFile;
    setSlotError(null);
    setSlotCompleted(null);
    setReconnectTimedOut(false);
    preparePollTargetRef.current = ver;
    setSlotBusy(true);
    setUploadProgress({ loaded: 0, total: fileToUpload.size });
    try {
      const nextSelf = await uploadManagerSlot(
        effectiveConnection.base,
        effectiveConnection.key,
        ver,
        fileToUpload,
        (loaded, total) => setUploadProgress({ loaded, total })
      );
      setUploadFile(null);
      setUploadProgress(null);
      setLiveSelfUpdate({ base: selfUpdateRef.current, value: nextSelf });
      setSlotBusy(false);
      if (nextSelf.staged_slots.includes(ver) || nextSelf.prepare_operation?.phase === 'succeeded') {
        setOptimisticStaged(ver);
        await refresh();
        setConfirmation({ kind: 'activate', version: ver });
      }
    } catch (err) {
      setSlotBusy(false);
      setUploadProgress(null);
      setSlotError(formatSlotErrorMessage(err, t));
    }
  };

  const handleConfirmAction = async () => {
    if (!confirmation || !effectiveConnection || slotBusy || reconnect || recoveryRequired) return;
    const current = confirmation;
    setConfirmation(null);
    setSlotError(null);
    setSlotCompleted(null);
    setReconnectTimedOut(false);
    setSlotBusy(true);
    try {
      if (current.kind === 'activate') {
        const res = await activateManagerSlot(
          effectiveConnection.base,
          effectiveConnection.key,
          current.version
        );
        setSlotBusy(false);
        if (res.accepted) {
          const attemptId = typeof res.attempt_id === 'string' ? res.attempt_id.trim() : '';
          setReconnect({
            action: 'activate',
            targetVersion: res.target_version || current.version,
            attemptId,
            startedAt: Date.now(),
            phase: 'polling',
          });
        }
      } else {
        const res = await rollbackManagerSlot(
          effectiveConnection.base,
          effectiveConnection.key,
          current.activeSlot
        );
        setSlotBusy(false);
        if (res.accepted) {
          setReconnect({
            action: 'rollback',
            targetVersion: res.target_version || current.targetVersion,
            attemptId: res.attempt_id,
            startedAt: Date.now(),
            phase: 'polling',
          });
        }
      }
    } catch (err) {
      setSlotBusy(false);
      setSlotError(formatSlotErrorMessage(err, t));
    }
  };

  const presets =
    activeSelfUpdate?.cdn_presets && activeSelfUpdate.cdn_presets.length > 0
      ? activeSelfUpdate.cdn_presets
      : [...DEFAULT_SLOT_CDN_PRESETS];

  const isTargetStaged = Boolean(targetVersion && stagedSlots.includes(targetVersion));
  const effectiveRunningVersion = runningSlot || status?.current_version || '';
  const offlineStagedTarget = useMemo(() => {
    if (!effectiveRunningVersion) return '';
    const newer = stagedSlots.filter((ver) => {
      const cmp = compareSemver(ver, effectiveRunningVersion);
      return cmp !== null && cmp > 0;
    });
    if (newer.length === 0) return '';
    newer.sort((a, b) => compareSemver(b, a) ?? 0);
    return newer[0] ?? '';
  }, [stagedSlots, effectiveRunningVersion]);

  const progressBox = (() => {
    if (uploadProgress) {
      const pct =
        uploadProgress.total > 0
          ? Math.min(100, Math.round((uploadProgress.loaded / uploadProgress.total) * 100))
          : null;
      return {
        label: t('manager_updates.slot_progress_uploading'),
        received: uploadProgress.loaded,
        total: uploadProgress.total,
        pct,
      };
    }
    if (activePrepareOp && (isInProgressPhase(activePrepareOp.phase) || slotBusy)) {
      const phaseKey =
        activePrepareOp.phase === 'verifying'
          ? 'manager_updates.slot_progress_verifying'
          : activePrepareOp.phase === 'staging'
            ? 'manager_updates.slot_progress_staging'
            : 'manager_updates.slot_progress_downloading';
      const pct =
        activePrepareOp.total_bytes > 0
          ? Math.min(
              100,
              Math.round((activePrepareOp.received_bytes / activePrepareOp.total_bytes) * 100)
            )
          : null;
      return {
        label: t(phaseKey),
        received: activePrepareOp.received_bytes,
        total: activePrepareOp.total_bytes,
        pct,
      };
    }
    if (slotBusy && !confirmation && !reconnect) {
      return {
        label: t('manager_updates.slot_progress_downloading'),
        received: 0,
        total: 0,
        pct: null,
      };
    }
    return null;
  })();

  const checkedAt = status?.last_success_at && new Date(status.last_success_at);
  const checkedTime =
    checkedAt && Number.isFinite(checkedAt.getTime()) && checkedAt.getUTCFullYear() > 1
      ? formatDateTime(checkedAt, i18n.language)
      : '';
  const hasCachedResult = Boolean(info || checkedTime);
  const state = !available
    ? 'unavailable'
    : failed
      ? hasCachedResult
        ? 'failed'
        : 'check_failed_no_cache'
      : !status
        ? 'loading'
        : isUnknownVersionFailure
          ? 'unknown_version'
          : stale
            ? 'stale'
            : status.state;
  const channel =
    status && ['stable', 'rc', 'beta'].includes(status.channel) ? status.channel : 'auto';
  const currentVersion = status?.current_version || t('dashboard.version_unknown');
  const currentRelease = buildDashboardVersionReleaseURL('manager', status?.current_version);
  const panelVersion = __APP_VERSION__ || t('dashboard.version_unknown');
  const panelRelease = buildDashboardVersionReleaseURL('manager', __APP_VERSION__);
  const channelOptions = (['auto', 'stable', 'rc', 'beta'] as const).map((value) => ({
    value,
    label:
      value === 'auto'
        ? t('manager_updates.auto_effective', { channel: t('manager_updates.' + channel) })
        : t('manager_updates.' + value),
  }));
  const statusTone =
    state === 'up_to_date'
      ? 'is-success'
      : state === 'update_available' || state === 'ahead_of_channel'
        ? 'is-info'
        : state === 'unknown_version'
          ? 'is-neutral'
          : failed || stale
            ? 'is-warning'
            : 'is-neutral';
  const statusBadgeText =
    state === 'update_available'
      ? t('manager_updates.available_badge')
      : state === 'up_to_date'
        ? t('manager_updates.badge_latest')
        : state === 'ahead_of_channel'
          ? t('manager_updates.badge_ahead')
          : state === 'unknown_version'
            ? t('manager_updates.badge_dev')
            : failed || stale
              ? t('manager_updates.badge_attention')
              : t('manager_updates.badge_pending');

  const slotProgressUI = progressBox && (
    <div className={styles.progressBox} role="status" aria-live="polite">
      <div className={styles.progressMeta}>
        <span>{progressBox.label}</span>
        <span>
          {progressBox.total > 0 && progressBox.pct !== null
            ? `${progressBox.pct}% · ${formatMB(progressBox.received)} / ${formatMB(progressBox.total)}`
            : ''}
        </span>
      </div>
      <div
        className={styles.progressTrack}
        role="progressbar"
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuenow={progressBox.pct ?? undefined}
      >
        <div
          className={`${styles.progressFill} ${
            progressBox.pct === null ? styles['is-indeterminate'] : ''
          }`}
          style={progressBox.pct !== null ? { width: `${progressBox.pct}%` } : undefined}
        />
      </div>
    </div>
  );

  return (
    <div className={styles.page}>
      <CPAUpdateCard key={cpa.scope} flow={cpa} />
      <section className={styles.productSection} aria-labelledby="cpamp-update-title">
        <Card className={styles.release}>
          <div className={styles.cardHead}>
            <div className={styles.cardTitleGroup}>
              <div className={styles.cardTitleRow}>
                <h2 id="cpamp-update-title" ref={titleRef} tabIndex={-1}>
                  {t('manager_updates.product')}
                </h2>
                <span className={`status-pill ${statusTone}`}>
                  <span className={`status-dot ${statusTone}`} aria-hidden="true" />
                  {statusBadgeText}
                </span>
              </div>
              <p className={styles.subtitle}>
                <span>
                  {checkedTime
                    ? t('manager_updates.last_checked', { time: checkedTime })
                    : t('manager_updates.not_checked')}
                </span>
                {status && (
                  <>
                    <span className={styles.subtitleSep} aria-hidden="true">
                      ·
                    </span>
                    <span>
                      {t('manager_updates.channel') + ': ' + t('manager_updates.' + channel)}
                    </span>
                  </>
                )}
              </p>
            </div>
            <Button
              type="button"
              variant="secondary"
              className={styles.checkButton}
              onClick={() => void check()}
              loading={busy}
              disabled={!available || slotBusy || !!reconnect}
            >
              {!busy && <IconRefreshCw size={15} aria-hidden="true" />}
              {t('manager_updates.check_now')}
            </Button>
          </div>

          {showDualVersions && status && (
            <div
              className={styles.slotVersionPills}
              aria-label={t('manager_updates.running_slot_label')}
            >
              <span className="status-pill is-info">
                <span className="status-dot is-info" aria-hidden="true" />
                {t('manager_updates.running_slot_label')}:{' '}
                {runningSlot || status.current_version}
              </span>
              {baseImageVersion && (
                <span className="status-pill is-neutral">
                  <span className="status-dot is-neutral" aria-hidden="true" />
                  {t('manager_updates.base_image_label')}: {baseImageVersion}
                </span>
              )}
            </div>
          )}

          {recoveryRequired && (
            <div className={`${styles.recoveryBanner} ${styles['is-danger']}`} role="alert">
              <IconTriangleAlert size={17} aria-hidden="true" />
              <div>
                <p className={styles.recoveryBannerTitle}>
                  {t('manager_updates.slot_recovery_required_title')}
                </p>
                <p className={styles.recoveryBannerText}>
                  {activeSelfUpdate?.last_failure_code
                    ? t('manager_updates.slot_recovery_required_body', {
                        code: activeSelfUpdate.last_failure_code,
                      })
                    : t('manager_updates.slot_recovery_required_body_no_code')}
                </p>
              </div>
            </div>
          )}

          {activeSelfUpdate?.last_rolled_back_slot && (
            <div className={`${styles.recoveryBanner} ${styles['is-warning']}`} role="alert">
              <IconTriangleAlert size={17} aria-hidden="true" />
              <div>
                <p className={styles.recoveryBannerTitle}>
                  {t('manager_updates.trial_rollback_title')}
                </p>
                <p className={styles.recoveryBannerText}>
                  {activeSelfUpdate.last_failure_code
                    ? t('manager_updates.trial_rollback_body', {
                        version: activeSelfUpdate.last_rolled_back_slot,
                        code: activeSelfUpdate.last_failure_code,
                      })
                    : t('manager_updates.trial_rollback_body_no_code', {
                        version: activeSelfUpdate.last_rolled_back_slot,
                      })}
                </p>
              </div>
            </div>
          )}

          {reconnect && (
            <div className={styles.progressBox} role="status" aria-live="polite">
              <div className={styles.progressMeta}>
                <span>
                  {t('manager_updates.slot_reconnecting_title', {
                    version: reconnect.targetVersion,
                  })}
                </span>
              </div>
              <p className={styles.hint}>
                {reconnect.phase === 'trial'
                  ? t('manager_updates.slot_reconnecting_trial', {
                      version: reconnect.targetVersion,
                    })
                  : t('manager_updates.slot_reconnecting_exec')}
              </p>
              <div className={styles.progressTrack} role="progressbar">
                <div className={`${styles.progressFill} ${styles['is-indeterminate']}`} />
              </div>
            </div>
          )}

          {reconnectTimedOut && (
            <p className={styles.warning} role="alert">
              {t('manager_updates.slot_reconnecting_timeout')}
            </p>
          )}

          {slotCompleted && (
            <div className={styles.completedBanner} role="status" aria-live="polite">
              <div className={styles.completedText}>
                <IconCheck size={16} aria-hidden="true" />
                <p>
                  {t(
                    slotCompleted.action === 'rollback'
                      ? 'manager_updates.slot_rollback_succeeded'
                      : 'manager_updates.slot_upgrade_succeeded',
                    { version: slotCompleted.version }
                  )}
                </p>
              </div>
              <Button
                type="button"
                variant="ghost"
                size="sm"
                onClick={() => setSlotCompleted(null)}
              >
                {t('cpa_updates.dismiss')}
              </Button>
            </div>
          )}

          {slotError && (
            <p className={styles.warning} role="alert">
              {slotError}
            </p>
          )}

          <div className={styles.statusBody} aria-live="polite" aria-atomic="true">
            {info && (
              <p className={styles.eyebrow}>
                {t(
                  failed || stale
                    ? 'manager_updates.cached_release'
                    : 'manager_updates.new_release'
                )}
              </p>
            )}
            <h3 id="manager-release-status" className={info ? styles.version : styles.stateTitle}>
              {info ? info.release.version : t('manager_updates.' + state)}
              {info && ['stable', 'rc', 'beta'].includes(info.release.stage) && (
                <span className={styles.stage}>{t('manager_updates.' + info.release.stage)}</span>
              )}
            </h3>
            {info && (
              <p className={styles.subtitle}>
                {t('manager_updates.current_version', { version: currentVersion })}
              </p>
            )}
            {info && (failed || stale) && (
              <p className={styles.warning} role="status">
                {t(failed ? 'manager_updates.failed' : 'manager_updates.stale')}
              </p>
            )}
            {info ? (
              <ReleaseDetails
                key={channel + ':' + info.release.version}
                info={info}
                action={status?.upgrade_action}
                deploymentVariant={status?.deployment_variant}
                upgradeAllowed={upgradeAllowed}
                selfUpdateSlotSection={
                  directSlotUpgradeReady ? (
                    <div className={styles.selfUpdatePanel}>
                      <div className={styles.selfUpdateHeader}>
                        <h4 className={styles.selfUpdateTitle}>
                          {t('manager_updates.slot_section_title')}
                        </h4>
                        <SegmentedTabs
                          idBase="manager-slot-source"
                          items={[
                            { id: 'github', label: t('manager_updates.slot_source_github') },
                            { id: 'cdn', label: t('manager_updates.slot_source_cdn') },
                            { id: 'upload', label: t('manager_updates.slot_source_upload') },
                          ]}
                          activeTab={sourceMode}
                          onChange={(next) => {
                            setSourceMode(next);
                            setSlotError(null);
                          }}
                          ariaLabel={t('manager_updates.slot_source_label')}
                        />
                      </div>

                      {!isTargetStaged && sourceMode === 'cdn' && (
                        <>
                          <div className={styles.cdnPresetsRow}>
                            <span className={styles.cdnPresetsLabel}>
                              {t('manager_updates.slot_cdn_presets_label')}
                            </span>
                            {presets.map((preset) => (
                              <button
                                key={preset}
                                type="button"
                                className={`${styles.cdnPresetBtn} ${
                                  trimmedPrefix === preset ? styles['is-selected'] : ''
                                }`}
                                onClick={() => setCdnPrefix(preset)}
                              >
                                {preset}
                              </button>
                            ))}
                          </div>
                          <label className={styles.field}>
                            <span>{t('manager_updates.slot_cdn_prefix_label')}</span>
                            <input
                              type="url"
                              className={styles.sourceInput}
                              value={cdnPrefix}
                              onChange={(event) => setCdnPrefix(event.target.value)}
                              placeholder={t('manager_updates.slot_cdn_prefix_placeholder')}
                              aria-label={t('manager_updates.slot_cdn_prefix_label')}
                            />
                            {trimmedPrefix !== '' && !cdnPrefixValid && (
                              <span className={styles.sourceError} role="alert">
                                {t('manager_updates.slot_cdn_prefix_invalid')}
                              </span>
                            )}
                            <span className={styles.sourceHint}>
                              {t('manager_updates.slot_cdn_prefix_hint')}
                            </span>
                          </label>
                        </>
                      )}

                      {!isTargetStaged && sourceMode === 'upload' && (
                        <div className={styles.uploadRow}>
                          <label className={styles.uploadFileLabel}>
                            <input
                              type="file"
                              accept=".tar.gz,application/gzip,application/x-gzip"
                              className={styles.uploadFileInput}
                              onChange={handleUploadFileChange}
                              aria-label={t('manager_updates.slot_upload_choose_file')}
                            />
                            {t('manager_updates.slot_upload_choose_file')}
                          </label>
                          {uploadFile && (
                            <span className={styles.uploadFileName}>
                              {uploadFile.name} ({formatMB(uploadFile.size)})
                            </span>
                          )}
                          {uploadErrorKey && (
                            <span className={styles.sourceError} role="alert">
                              {t(uploadErrorKey)}
                            </span>
                          )}
                          <span className={styles.sourceHint}>
                            {t('manager_updates.slot_upload_file_hint')}
                          </span>
                        </div>
                      )}

                      {slotProgressUI}

                      {isTargetStaged && (
                        <p className={styles.hint} role="status">
                          {t('manager_updates.slot_staged_hint', { version: targetVersion })}
                        </p>
                      )}

                      <div className={styles.slotActionsRow}>
                        {!isTargetStaged ? (
                          <Button
                            type="button"
                            disabled={
                              !effectiveConnection ||
                              slotBusy ||
                              !!reconnect ||
                              recoveryRequired ||
                              (sourceMode === 'cdn' && !cdnPrefixValid) ||
                              (sourceMode === 'upload' && (!uploadFile || !!uploadErrorKey))
                            }
                            loading={slotBusy}
                            onClick={() => void handlePrepare()}
                          >
                            {sourceMode === 'upload'
                              ? t('manager_updates.slot_upload_cta')
                              : t('manager_updates.slot_prepare_cta', { version: targetVersion })}
                          </Button>
                        ) : (
                          <Button
                            type="button"
                            disabled={!effectiveConnection || slotBusy || !!reconnect || recoveryRequired}
                            loading={slotBusy}
                            onClick={() =>
                              setConfirmation({ kind: 'activate', version: targetVersion })
                            }
                          >
                            {t('manager_updates.slot_activate_cta', { version: targetVersion })}
                          </Button>
                        )}
                      </div>
                    </div>
                  ) : null
                }
              />
            ) : failed ? (
              <p className={styles.summary}>
                {t('manager_updates.check_failed_hint')}
                {status?.last_error ? ` (${status.last_error})` : ''}
              </p>
            ) : available && state === 'unknown_version' ? (
              <p className={styles.summary}>{t('manager_updates.unknown_version_hint')}</p>
            ) : available && state === 'never_checked' ? (
              <p className={styles.summary}>{t('manager_updates.check_hint')}</p>
            ) : null}
          </div>

          {showOfflineUploadPanel && (
            <div className={styles.selfUpdatePanel}>
              <h4 className={styles.selfUpdateTitle}>
                {t('manager_updates.slot_offline_upload_title')}
              </h4>
              <p className={styles.sourceHint}>{t('manager_updates.slot_offline_upload_hint')}</p>
              <label className={styles.slotVersionField}>
                <span>{t('manager_updates.slot_upload_version_label')}</span>
                <input
                  type="text"
                  className={styles.sourceInput}
                  value={offlineVersion}
                  onChange={(event) => {
                    setOfflineVersion(event.target.value);
                    setSlotError(null);
                  }}
                  placeholder={t('manager_updates.slot_upload_version_placeholder')}
                  aria-label={t('manager_updates.slot_upload_version_label')}
                />
                {offlineVersionTrimmed !== '' && !offlineVersionValid && (
                  <span className={styles.sourceError} role="alert">
                    {t('manager_updates.slot_upload_version_invalid')}
                  </span>
                )}
              </label>
              <div className={styles.uploadRow}>
                <label className={styles.uploadFileLabel}>
                  <input
                    type="file"
                    accept=".tar.gz,application/gzip,application/x-gzip"
                    className={styles.uploadFileInput}
                    onChange={handleUploadFileChange}
                    aria-label={t('manager_updates.slot_upload_choose_file')}
                  />
                  {t('manager_updates.slot_upload_choose_file')}
                </label>
                {uploadFile && (
                  <span className={styles.uploadFileName}>
                    {uploadFile.name} ({formatMB(uploadFile.size)})
                  </span>
                )}
                {uploadErrorKey && (
                  <span className={styles.sourceError} role="alert">
                    {t(uploadErrorKey)}
                  </span>
                )}
                <span className={styles.sourceHint}>
                  {t('manager_updates.slot_upload_file_hint')}
                </span>
              </div>
              {slotProgressUI}
              <div className={styles.slotActionsRow}>
                <Button
                  type="button"
                  variant="secondary"
                  disabled={
                    !effectiveConnection ||
                    slotBusy ||
                    !!reconnect ||
                    recoveryRequired ||
                    !offlineVersionValid ||
                    !uploadFile ||
                    !!uploadErrorKey
                  }
                  loading={slotBusy && !!uploadProgress}
                  onClick={() => void handleOfflineUpload()}
                >
                  {t('manager_updates.slot_upload_cta')}
                </Button>
                {offlineStagedTarget && (
                  <Button
                    type="button"
                    disabled={!effectiveConnection || slotBusy || !!reconnect || recoveryRequired}
                    onClick={() =>
                      setConfirmation({ kind: 'activate', version: offlineStagedTarget })
                    }
                  >
                    {t('manager_updates.slot_activate_cta', { version: offlineStagedTarget })}
                  </Button>
                )}
              </div>
            </div>
          )}

          {activeSelfUpdate?.active_slot && activeSelfUpdate.state !== 'trial' && (
            <div className={styles.rollbackSection}>
              <div className={styles.rollbackHeader}>
                <h4 className={styles.rollbackTitle}>{t('manager_updates.slot_rollback_title')}</h4>
                {activeSelfUpdate.rollback_available &&
                activeSelfUpdate.rollback_same_schema &&
                activeSelfUpdate.rollback_target_version ? (
                  <Button
                    type="button"
                    variant="secondary"
                    size="sm"
                    disabled={!effectiveConnection || slotBusy || !!reconnect || recoveryRequired}
                    onClick={() =>
                      setConfirmation({
                        kind: 'rollback',
                        activeSlot: activeSelfUpdate.active_slot,
                        targetVersion: activeSelfUpdate.rollback_target_version,
                      })
                    }
                  >
                    {t('manager_updates.slot_rollback_cta', {
                      version: activeSelfUpdate.rollback_target_version,
                    })}
                  </Button>
                ) : (
                  <Button type="button" variant="secondary" size="sm" disabled>
                    {t('manager_updates.slot_rollback_cta', {
                      version:
                        activeSelfUpdate.rollback_target_version ||
                        baseImageVersion ||
                        activeSelfUpdate.active_slot,
                    })}
                  </Button>
                )}
              </div>
              {!activeSelfUpdate.rollback_same_schema && (
                <p className={styles.rollbackHint}>
                  {t('manager_updates.slot_rollback_schema_blocked', {
                    version:
                      activeSelfUpdate.rollback_target_version ||
                      baseImageVersion ||
                      status?.current_version ||
                      '',
                  })}
                </p>
              )}
            </div>
          )}

          <dl className={styles.runningVersions}>
            {available && (
              <div>
                <dt>{t('manager_updates.server')}</dt>
                <dd>
                  {currentRelease ? (
                    <a href={currentRelease} target="_blank" rel="noopener noreferrer">
                      {currentVersion}
                      <IconExternalLink size={12} aria-hidden="true" />
                    </a>
                  ) : (
                    currentVersion
                  )}
                </dd>
              </div>
            )}
            <div>
              <dt>{t('manager_updates.panel')}</dt>
              <dd>
                {panelRelease ? (
                  <a href={panelRelease} target="_blank" rel="noopener noreferrer">
                    {panelVersion}
                    <IconExternalLink size={12} aria-hidden="true" />
                  </a>
                ) : (
                  panelVersion
                )}
              </dd>
            </div>
          </dl>

          {available && status && (
            <details className={styles.settings}>
              <summary>
                <IconChevronRight size={14} aria-hidden="true" />
                {t('manager_updates.settings')}
              </summary>
              <div className={styles.settingBody}>
                <div>
                  <label id="manager-update-channel-label" htmlFor="manager-update-channel">
                    {t('manager_updates.channel')}
                  </label>
                  <p id="manager-update-channel-hint" className={styles.settingDescription}>
                    {t('manager_updates.channel_hint')}
                  </p>
                </div>
                <Select
                  id="manager-update-channel"
                  value={status.channel_preference}
                  options={channelOptions}
                  onChange={(value) => void setChannel(value as UpdateChannel)}
                  disabled={busy}
                  ariaLabelledBy="manager-update-channel-label"
                  ariaDescribedBy="manager-update-channel-hint"
                  className={styles.channelSelect}
                />
              </div>
              <p className={styles.settingDescription}>
                {t(
                  status.automatic ? 'manager_updates.automatic_on' : 'manager_updates.automatic_off'
                )}
              </p>
            </details>
          )}
        </Card>
      </section>

      <Modal
        open={!!confirmation}
        className={styles.cpaConfirmation}
        onClose={() => setConfirmation(null)}
        title={t(
          confirmation?.kind === 'rollback'
            ? 'manager_updates.slot_rollback_confirm_title'
            : 'manager_updates.slot_activate_title',
          {
            version:
              confirmation?.kind === 'rollback'
                ? confirmation.targetVersion
                : (confirmation?.version ?? ''),
          }
        )}
        footer={
          <>
            <Button type="button" variant="ghost" onClick={() => setConfirmation(null)}>
              {t('common.cancel')}
            </Button>
            <Button
              type="button"
              variant="primary"
              disabled={!effectiveConnection || slotBusy || !!reconnect || recoveryRequired}
              onClick={() => void handleConfirmAction()}
            >
              {t(
                confirmation?.kind === 'rollback'
                  ? 'manager_updates.slot_rollback_confirm_btn'
                  : 'manager_updates.slot_activate_confirm'
              )}
            </Button>
          </>
        }
      >
        <p className={styles.confirmationText}>
          {t(
            confirmation?.kind === 'rollback'
              ? 'manager_updates.slot_rollback_confirm_warning'
              : 'manager_updates.slot_activate_warning',
            {
              version:
                confirmation?.kind === 'rollback'
                  ? confirmation.targetVersion
                  : (confirmation?.version ?? ''),
            }
          )}
        </p>
      </Modal>
    </div>
  );
}
