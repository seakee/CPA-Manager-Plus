import { useEffect, useRef, useState } from 'react';
import { Link } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Card } from '@/components/ui/Card';
import { Select } from '@/components/ui/Select';
import { SegmentedTabs } from '@/components/ui/SegmentedTabs';
import {
  IconArrowUpFromLine,
  IconCheck,
  IconChevronRight,
  IconCopy,
  IconExternalLink,
  IconInfo,
  IconRefreshCw,
} from '@/components/ui/icons';
import { useHeaderRefresh } from '@/hooks/useHeaderRefresh';
import { buildDashboardVersionReleaseURL } from '@/features/dashboard/versionReleaseLinks';
import { copyToClipboard } from '@/utils/clipboard';
import { formatDateTime } from '@/utils/format';
import { useManagerUpdates } from './ManagerUpdates';
import type { ReleaseInfo, UpdateChannel, UpdateStatus } from './managerUpdateApi';
import styles from './ManagerUpdatePage.module.scss';

function ReleaseDetails({
  info,
  action,
  upgradeAllowed,
}: {
  info: ReleaseInfo;
  action: UpdateStatus['upgrade_action'];
  upgradeAllowed: boolean;
}) {
  const { t, i18n } = useTranslation();
  const [stepsOpen, setStepsOpen] = useState(false);
  const [deployment, setDeployment] = useState<'docker' | 'native'>('docker');
  const [copyState, setCopyState] = useState<'idle' | 'copied' | 'copy_failed'>('idle');
  const direct = action === 'direct' && !info.update.migration_required && !info.update.breaking;
  const releaseUrl = buildDashboardVersionReleaseURL('manager', info.release.version);
  const image = info.distribution.docker.image + ':' + info.distribution.docker.version_tag;
  const language = i18n.language.startsWith('zh') ? 'zh' : 'en';

  const copyImage = async () => {
    setCopyState((await copyToClipboard(image)) ? 'copied' : 'copy_failed');
  };

  return (
    <>
      <p className={styles.summary}>{info.content.summary[language] || info.content.summary.en}</p>
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
              <div className={styles.imageRow}>
                <code>{image}</code>
                <Button type="button" variant="ghost" size="sm" onClick={() => void copyImage()}>
                  <IconCopy size={14} aria-hidden="true" />
                  {t('manager_updates.copy_image')}
                </Button>
                {copyState !== 'idle' && (
                  <span className={styles.copyFeedback} role="status">
                    {t('manager_updates.' + copyState)}
                  </span>
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

export function ManagerUpdatePage() {
  const { t, i18n } = useTranslation();
  const { status, available, busy, error, check, setChannel } = useManagerUpdates();
  const titleRef = useRef<HTMLHeadingElement>(null);
  useHeaderRefresh(check, available);
  useEffect(() => {
    titleRef.current?.focus({ preventScroll: true });
  }, []);

  const failed = error || !!status?.last_error;
  const stale = !!status?.stale;
  const upgradeAllowed = !failed && !stale;
  const info = available && status?.state === 'update_available' ? status.target : undefined;
  const state = !available
    ? 'unavailable'
    : failed
      ? 'failed'
      : !status
        ? 'loading'
        : stale
          ? 'stale'
          : status.state;
  const channel =
    status && ['stable', 'rc', 'beta'].includes(status.channel) ? status.channel : 'auto';
  const currentVersion = status?.current_version || t('dashboard.version_unknown');
  const currentRelease = buildDashboardVersionReleaseURL('manager', status?.current_version);
  const panelVersion = __APP_VERSION__ || t('dashboard.version_unknown');
  const panelRelease = buildDashboardVersionReleaseURL('manager', __APP_VERSION__);
  const checkedAt = status?.last_success_at && new Date(status.last_success_at);
  const checkedTime =
    checkedAt && Number.isFinite(checkedAt.getTime()) && checkedAt.getUTCFullYear() > 1
      ? formatDateTime(checkedAt, i18n.language)
      : '';
  const channelOptions = (['auto', 'stable', 'rc', 'beta'] as const).map((value) => ({
    value,
    label:
      value === 'auto'
        ? t('manager_updates.auto_effective', { channel: t('manager_updates.' + channel) })
        : t('manager_updates.' + value),
  }));

  return (
    <div className={styles.page}>
      <nav className={styles.breadcrumb} aria-label={t('common.navigation')}>
        <Link to="/system">{t('nav.system_info')}</Link>
        <IconChevronRight size={13} aria-hidden="true" />
        <span aria-current="page">{t('manager_updates.title')}</span>
      </nav>
      <div className={styles.heading}>
        <div>
          <h1 ref={titleRef} tabIndex={-1}>
            {t('manager_updates.title')}
          </h1>
          <p className={styles.subtitle}>
            {checkedTime
              ? t('manager_updates.last_checked', { time: checkedTime })
              : t('manager_updates.not_checked')}
            {status && (
              <span>{t('manager_updates.channel') + ' · ' + t('manager_updates.' + channel)}</span>
            )}
          </p>
        </div>
        <Button
          type="button"
          variant="secondary"
          onClick={() => void check()}
          loading={busy}
          disabled={!available}
        >
          {!busy && <IconRefreshCw size={15} aria-hidden="true" />}
          {t('manager_updates.check_now')}
        </Button>
      </div>

      <Card className={styles.release}>
        <div className={styles.releaseTop}>
          <div className={styles.releaseHeading} aria-live="polite" aria-atomic="true">
            <p className={styles.eyebrow}>
              {t(
                info
                  ? failed || stale
                    ? 'manager_updates.cached_release'
                    : 'manager_updates.new_release'
                  : 'common.status'
              )}
            </p>
            <h2 className={info ? styles.version : styles.stateTitle}>
              {info ? info.release.version : t('manager_updates.' + state)}
              {info && ['stable', 'rc', 'beta'].includes(info.release.stage) && (
                <span className={styles.stage}>{t('manager_updates.' + info.release.stage)}</span>
              )}
            </h2>
            {info && (
              <p className={styles.subtitle}>
                {t('manager_updates.current_version', { version: currentVersion })}
              </p>
            )}
          </div>
          <span className={styles.releaseIcon} aria-hidden="true">
            {failed || stale || !available ? (
              <IconInfo size={23} />
            ) : info ? (
              <IconArrowUpFromLine size={23} />
            ) : state === 'up_to_date' ? (
              <IconCheck size={23} />
            ) : (
              <IconRefreshCw size={23} />
            )}
          </span>
        </div>
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
            upgradeAllowed={upgradeAllowed}
          />
        ) : available && (state === 'never_checked' || state === 'unknown_version') ? (
          <p className={styles.summary}>{t('manager_updates.check_hint')}</p>
        ) : null}
        <dl className={styles.runningVersions}>
          {available && (
            <div>
              <dt>Manager Server</dt>
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
      </Card>

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
            {t(status.automatic ? 'manager_updates.automatic_on' : 'manager_updates.automatic_off')}
          </p>
        </details>
      )}
    </div>
  );
}
