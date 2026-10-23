import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useNavigate } from 'react-router-dom';
import { Modal } from '../components/ui/Modal';
import { configurationText as c } from '../i18n/configurationWorkspace';

export function FreshSystemWorkspace({ basePath }: { basePath: string }) {
  const { i18n } = useTranslation();
  const navigate = useNavigate();
  const [recoveryModalOpen, setRecoveryModalOpen] = useState(false);
  const docsLanguage = i18n.resolvedLanguage?.startsWith('zh') ? '' : 'en/';
  return (
    <div className="fresh-system-workspace" data-testid="fresh-system-workspace">
      <div className="page-head">
        <div>
          <h1 id="system-workspace-title" tabIndex={-1}>
            {c('system')}
          </h1>
          <p className="muted">{c('systemHelp')}</p>
        </div>
        <span className="badge">CPAMP</span>
      </div>
      <div className="grid">
        <div className="stack">
          <section className="card" data-testid="system-entry-card">
            <div className="card-head">
              <div>
                <h2>{c('entry')}</h2>
                <p className="muted">{c('entryHelp')}</p>
              </div>
              <span className="badge badge-emerald">{c('verified')}</span>
            </div>
            <div className="row">
              <div>
                <label>{c('entryHelp')}</label>
                <span className="path" data-testid="system-entry-path">
                  <code>{basePath}</code>
                </span>
                <small>{c('entrySource')}</small>
              </div>
            </div>
            <div className="notice">{c('entryNotice')}</div>
          </section>
          <section className="card" data-testid="system-startup-card">
            <div className="card-head">
              <h2>{c('startup')}</h2>
              <span className="badge">{c('deployment')}</span>
            </div>
            <div className="row">
              <p>{c('startupHelp')}</p>
              <span className="badge">{c('notRead')}</span>
            </div>
            <p className="detail" style={{ marginTop: 16 }}>
              {c('preferences')}
            </p>
          </section>
        </div>
        <div className="stack">
          <section className="card" data-testid="system-security-card">
            <div className="card-head">
              <h2>{c('security')}</h2>
              <span className="badge badge-emerald">{c('verified')}</span>
            </div>
            <p>{c('session')}</p>
            <p className="muted">{c('securityHelp')}</p>
            <div className="actions">
              <button
                className="btn btn-secondary"
                data-testid="view-offline-recovery-btn"
                onClick={() => setRecoveryModalOpen(true)}
              >
                {c('recovery')}
              </button>
            </div>
            <p className="detail" style={{ marginTop: 15 }}>
              {c('noReset')}
            </p>
          </section>
          <section className="card" data-testid="system-ownership-card">
            <div className="card-head">
              <h2>{c('owners')}</h2>
            </div>
            <p className="muted">{c('systemOwners')}</p>
            <div className="actions">
              <button
                className="btn btn-primary"
                data-testid="goto-gateway-workspace-btn"
                onClick={() => navigate('/gateway')}
              >
                {c('gotoGateway')}
              </button>
            </div>
          </section>
        </div>
      </div>
      <Modal
        open={recoveryModalOpen}
        title={c('recoveryTitle')}
        onClose={() => setRecoveryModalOpen(false)}
        footer={
          <button
            className="btn btn-primary"
            data-testid="close-recovery-btn"
            onClick={() => setRecoveryModalOpen(false)}
          >
            {c('understood')}
          </button>
        }
      >
        <div data-testid="recovery-modal">
          <ol className="recovery-steps">
            <li>{c('backup')}</li>
            <li>{c('stop')}</li>
            <li>
              {c('reset')} <code>cpa-manager-plus reset-admin-key</code>
            </li>
            <li>{c('restart')}</li>
          </ol>
          <div className="notice warn">{c('recoveryWarning')}</div>
          <p>
            <a
              href={
                'https://seakee.github.io/CPA-Manager-Plus/docs/' +
                docsLanguage +
                'operations/reset-admin-key.html'
              }
              target="_blank"
              rel="noopener noreferrer"
            >
              {c('recoveryDocs')}
            </a>
          </p>
        </div>
      </Modal>
    </div>
  );
}
