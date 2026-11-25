import { useCallback, useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Card } from '@/components/ui/Card';
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
import { useCPAUpdateFlow } from './useCPAUpdateFlow';
import type {
  DockerImageRole,
  ReleaseInfo,
  UpdateChannel,
  UpdateStatus,
} from './managerUpdateApi';
import styles from './ManagerUpdatePage.module.scss';

function ReleaseDetails({
  info,
  action,
  deploymentVariant,
  upgradeAllowed,
}: {
  info: ReleaseInfo;
  action: UpdateStatus['upgrade_action'];
  deploymentVariant: UpdateStatus['deployment_variant'];
  upgradeAllowed: boolean;
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

  const scheduleReset = (
    timerRef: { current: number | undefined },
    reset: () => void
  ) => {
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
      <div className={styles.actions}>
        {upgradeAllowed &&
          (direct ? (
            <Button
              type="button"
              onClick={() => setStepsOpen((open) => !open)}
              aria-expanded={stepsOpen}
              aria-controls="manager-upgrade-steps"
            >
              {t(stepsOpen ? 'manager_updates.hide_steps' : 'manager_updates.show_steps')}
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
  const os = hint.includes('mac') || hint.includes('darwin')
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
  const { status, available, busy, error, refresh, check, setChannel } = useManagerUpdates();
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
              disabled={!available}
            >
              {!busy && <IconRefreshCw size={15} aria-hidden="true" />}
              {t('manager_updates.check_now')}
            </Button>
          </div>

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
    </div>
  );
}
