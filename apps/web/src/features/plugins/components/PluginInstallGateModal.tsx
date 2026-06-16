import { useState, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Input } from '@/components/ui/Input';
import { Modal } from '@/components/ui/Modal';
import { IconExternalLink, IconPlugin, IconShield } from '@/components/ui/icons';
import { useAuthStore } from '@/stores';
import type { PluginStoreEntry } from '@/types';
import {
  buildRepositoryURL,
  getPluginConfirmToken,
  getPluginRepositorySlug,
  isDefaultPluginStoreSource,
  resolvePluginAssetURL,
} from '../pluginResources';
import styles from './PluginInstallGateModal.module.scss';

interface PluginInstallGateModalProps {
  open: boolean;
  entry: PluginStoreEntry | null;
  isUpdate: boolean;
  installing: boolean;
  onClose: () => void;
  onConfirm: () => void | Promise<void>;
}

function GateLogo({ src }: { src: string }) {
  const [failed, setFailed] = useState(false);

  return src && !failed ? (
    <img src={src} alt="" onError={() => setFailed(true)} />
  ) : (
    <IconPlugin size={26} />
  );
}

export function PluginInstallGateModal({
  open,
  entry,
  isUpdate,
  installing,
  onClose,
  onConfirm,
}: PluginInstallGateModalProps) {
  const { t } = useTranslation();
  const apiBase = useAuthStore((state) => state.apiBase);
  const [step, setStep] = useState(1);
  const [typed, setTyped] = useState('');
  const [openKey, setOpenKey] = useState('');

  const currentOpenKey = open && entry ? `${entry.storeId || entry.id}:${isUpdate}` : '';
  if (currentOpenKey !== openKey) {
    setOpenKey(currentOpenKey);
    setStep(1);
    setTyped('');
  }

  if (!entry) return null;

  const title = entry.name || entry.id;
  const repoSlug = getPluginRepositorySlug(entry.repository);
  const repositoryURL = buildRepositoryURL(entry.repository);
  const repoLabel = repoSlug || entry.id;
  const token = getPluginConfirmToken(entry);
  const logo = resolvePluginAssetURL(entry.logo, apiBase);
  const rawSourceText = entry.sourceName || entry.sourceUrl;
  const sourceText = isDefaultPluginStoreSource(entry)
    ? t('plugin_store.cli_proxy_api_source')
    : rawSourceText;
  const tokenMatches = typed.trim() === token;

  const handleClose = () => {
    if (installing) return;
    onClose();
  };

  const handleFinalConfirm = async () => {
    try {
      await onConfirm();
    } catch {
      // The caller keeps the modal open and surfaces the error via notification.
    }
  };

  const identity = (
    <div className={styles.identity}>
      <div className={styles.logoBox} aria-hidden="true">
        <GateLogo src={logo} />
      </div>
      <h3 className={styles.name}>{title}</h3>
      {repositoryURL ? (
        <a
          className={styles.repoLink}
          href={repositoryURL}
          target="_blank"
          rel="noreferrer"
          title={t('plugin_store.open_repository')}
          aria-label={t('plugin_store.open_repository')}
        >
          <span>{repoLabel}</span>
          <IconExternalLink size={12} />
        </a>
      ) : (
        <p className={styles.slug}>{repoLabel}</p>
      )}
      {sourceText ? (
        <p className={styles.source}>{t('plugin_store.source_name', { source: sourceText })}</p>
      ) : null}
    </div>
  );

  let body: ReactNode;
  let footer: ReactNode;

  if (step === 1) {
    body = identity;
    footer = (
      <Button variant="secondary" fullWidth onClick={() => setStep(2)}>
        {t('plugin_store.gate_step1_action')}
      </Button>
    );
  } else if (step === 2) {
    body = (
      <>
        {identity}
        <div className={styles.warningBanner}>
          <IconShield size={18} />
          <span>{t('plugin_store.gate_warning')}</span>
        </div>
        <ul className={styles.effects}>
          <li>{t('plugin_store.gate_effect_runs_code')}</li>
          <li>{t('plugin_store.gate_effect_no_review')}</li>
          <li>{t('plugin_store.gate_effect_restart')}</li>
        </ul>
        <div className={styles.untrustedAlert}>
          <p className={styles.untrustedText}>{t('plugin_store.gate_untrusted_alert')}</p>
          <dl className={styles.originGrid}>
            <dt>{t('plugin_store.gate_repository_label')}</dt>
            <dd>{repoLabel}</dd>
            <dt>{t('plugin_store.gate_source_label')}</dt>
            <dd>{sourceText || '-'}</dd>
          </dl>
        </div>
      </>
    );
    footer = (
      <Button variant="secondary" fullWidth onClick={() => setStep(3)}>
        {t('plugin_store.gate_step2_action')}
      </Button>
    );
  } else {
    body = (
      <>
        {identity}
        <div className={styles.confirmBlock}>
          <Input
            id="plugin-gate-confirm"
            label={t('plugin_store.gate_step3_prompt', { token })}
            value={typed}
            onChange={(event) => setTyped(event.target.value)}
            autoComplete="off"
            spellCheck={false}
            disabled={installing}
          />
          <p className={styles.confirmHint}>{t('plugin_store.gate_step3_hint')}</p>
        </div>
      </>
    );
    footer = (
      <Button
        variant="danger"
        fullWidth
        onClick={handleFinalConfirm}
        disabled={!tokenMatches || installing}
        loading={installing}
      >
        {t(isUpdate ? 'plugin_store.gate_step3_action_update' : 'plugin_store.gate_step3_action')}
      </Button>
    );
  }

  return (
    <Modal
      open={open}
      onClose={handleClose}
      title={t(isUpdate ? 'plugin_store.gate_title_update' : 'plugin_store.gate_title', {
        name: title,
      })}
      closeDisabled={installing}
      footer={footer}
      width={520}
      className={styles.gateModal}
    >
      {body}
    </Modal>
  );
}
