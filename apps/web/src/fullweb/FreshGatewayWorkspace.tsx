import { useEffect, useMemo, useRef, useState, type KeyboardEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router-dom';
import { Modal } from '../components/ui/Modal';
import {
  gatewaySections,
  gatewayOwners,
  COMMON_GATEWAY_PATHS,
  RETRY_COUNT_PATH,
  RETRY_INTERVAL_PATH,
  type GatewayOwnerMeta,
} from './gatewayCatalog';
import { FreshRetryConfiguration, type RetryDraftDiff } from './FreshRetryConfiguration';
import type { createPublicApiClient } from './publicApi';
import { configurationText as c } from '../i18n/configurationWorkspace';

export type GatewayTabKey =
  | 'overview'
  | 'common'
  | 'routing'
  | 'retry'
  | 'requests'
  | 'media'
  | 'compat'
  | 'owners';

export function FreshGatewayWorkspace({
  client,
  credential,
  onUnauthorized,
  onPending,
  onDirtyChange,
  navigationRequest = 0,
}: {
  client: ReturnType<typeof createPublicApiClient>;
  credential: string;
  onUnauthorized: () => void;
  onPending: (pending: boolean) => void;
  onDirtyChange?: (dirty: boolean) => void;
  navigationRequest?: number;
}) {
  // Subscribe to language changes just like the original Console pages.
  useTranslation();
  const [currentTab, setCurrentTab] = useState<GatewayTabKey>('overview');
  const [searchText, setSearchText] = useState('');
  const [changedOnly, setChangedOnly] = useState(false);
  const [activeOwnerPath, setActiveOwnerPath] = useState<string | null>(null);
  const [diffModalOpen, setDiffModalOpen] = useState(false);
  const [discardModalOpen, setDiscardModalOpen] = useState(false);
  const [discardEpoch, setDiscardEpoch] = useState(0);
  const [draftDiffs, setDraftDiffs] = useState<RetryDraftDiff[]>([]);
  const tabRefs = useRef<Partial<Record<GatewayTabKey, HTMLButtonElement | null>>>({});
  const dirtyDiffs = useMemo(() => draftDiffs.filter((d) => d.dirty), [draftDiffs]);
  const dirtyCount = dirtyDiffs.length;
  const sections = gatewaySections();
  const owners = gatewayOwners();
  const activeOwner = owners.find((owner) => owner.path === activeOwnerPath);

  useEffect(() => {
    onDirtyChange?.(dirtyCount > 0);
  }, [dirtyCount, onDirtyChange]);
  useEffect(() => {
    if (
      !dirtyCount ||
      typeof window === 'undefined' ||
      typeof window.addEventListener !== 'function'
    )
      return;
    const beforeUnload = (event: BeforeUnloadEvent) => {
      event.preventDefault();
      event.returnValue = '';
    };
    window.addEventListener('beforeunload', beforeUnload);
    return () => window.removeEventListener('beforeunload', beforeUnload);
  }, [dirtyCount]);

  const query = searchText.trim().toLowerCase();
  // Apply one predicate to the entire catalog, including the two retained editors.
  // A search is global, independent of the tab from which it was started.
  const filteredSections = sections
    .map((section) => ({
      ...section,
      fields: section.fields.filter((field) => {
        if (changedOnly && !dirtyDiffs.some((diff) => diff.path === field.path)) return false;
        if (query) {
          return [section.title, section.description, field.label, field.path].some((text) =>
            text.toLowerCase().includes(query)
          );
        }
        if (changedOnly && (currentTab === 'overview' || currentTab === 'owners')) return true;
        return currentTab === 'common'
          ? COMMON_GATEWAY_PATHS.has(field.path)
          : section.tab === currentTab;
      }),
    }))
    .filter((section) => section.fields.length > 0);
  const visiblePaths = new Set(
    filteredSections.flatMap((section) => section.fields.map((field) => field.path))
  );
  const countVisible = visiblePaths.has(RETRY_COUNT_PATH);
  const intervalVisible = visiblePaths.has(RETRY_INTERVAL_PATH);
  const referenceSections = filteredSections
    .map((section) => ({
      ...section,
      fields: section.fields.filter(
        (field) => field.path !== RETRY_COUNT_PATH && field.path !== RETRY_INTERVAL_PATH
      ),
    }))
    .filter((section) => section.fields.length > 0);
  const showOverview = currentTab === 'overview' && !query && !changedOnly;
  const showOwners = currentTab === 'owners' && !query && !changedOnly;
  const tabs: GatewayTabKey[] = [
    'overview',
    'common',
    'routing',
    'retry',
    'requests',
    'media',
    'compat',
    'owners',
  ];
  const tabMetrics = (tab: GatewayTabKey) => {
    const paths =
      tab === 'common'
        ? COMMON_GATEWAY_PATHS
        : new Set(
            sections
              .filter((section) => section.tab === tab)
              .flatMap((section) => section.fields.map((field) => field.path))
          );
    const drafts = dirtyDiffs.filter((diff) => paths.has(diff.path));
    return { dirty: drafts.length, errors: drafts.filter((diff) => diff.invalid).length };
  };
  function selectTab(tab: GatewayTabKey) {
    setCurrentTab(tab);
    setSearchText('');
  }
  function handleTabKey(event: KeyboardEvent<HTMLButtonElement>, tab: GatewayTabKey) {
    if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return;
    event.preventDefault();
    const index = tabs.indexOf(tab);
    const next =
      tabs[
        event.key === 'Home'
          ? 0
          : event.key === 'End'
            ? tabs.length - 1
            : (index + (event.key === 'ArrowRight' ? 1 : -1) + tabs.length) % tabs.length
      ];
    selectTab(next);
    tabRefs.current[next]?.focus({ preventScroll: true });
  }
  useEffect(() => {
    tabRefs.current[currentTab]?.scrollIntoView?.({ block: 'nearest', inline: 'nearest' });
  }, [currentTab]);

  const renderOwners = (prefix: string) => (
    <div className="owner-grid">
      {owners.map((owner: GatewayOwnerMeta, index) => (
        <section key={owner.path} className="card owner-card">
          <span className="badge">{c('independent')}</span>
          <h3>{owner.owner}</h3>
          <code>{owner.path}</code>
          <p className="muted">{owner.description}</p>
          <div className="owner-actions">
            <button
              className="btn btn-secondary btn-sm"
              data-testid={prefix + index}
              onClick={() => setActiveOwnerPath(owner.path)}
            >
              {c('owners')}
            </button>
            {owner.target && (
              <Link
                className="btn btn-secondary btn-sm"
                to={owner.target}
                data-testid={prefix + index + '-link'}
              >
                {c('openOwner')}
              </Link>
            )}
          </div>
        </section>
      ))}
    </div>
  );

  return (
    <div className="fresh-gateway-workspace" data-testid="fresh-gateway-workspace">
      <div className="page-head">
        <div>
          <h1 id="gateway-workspace-title" data-testid="gateway-workspace-title" tabIndex={-1}>
            {c('gateway')}
          </h1>
          <p className="muted">{c('gatewayHelp')}</p>
        </div>
        <span className="badge">{c('workspace')}</span>
      </div>
      <div className="gateway-toolbar" data-testid="gateway-toolbar">
        <label className="search-box">
          <span className="sr-only">{c('search')}</span>
          <input
            id="field-search"
            data-testid="gateway-search-input"
            type="text"
            placeholder={c('search')}
            value={searchText}
            onChange={(event) => setSearchText(event.target.value)}
          />
        </label>
        <label className="check-label">
          <input
            id="changed-only"
            data-testid="gateway-changed-only"
            type="checkbox"
            checked={changedOnly}
            onChange={(event) => setChangedOnly(event.target.checked)}
          />
          {c('changedOnly')}
        </label>
        <div className="gateway-toolbar-status">
          <span
            className={'badge' + (dirtyCount ? ' badge-amber' : '')}
            data-testid="gateway-dirty-count"
          >
            {dirtyCount ? c('unsaved', dirtyCount) : c('noDrafts')}
          </span>
        </div>
        <div className="gateway-toolbar-actions">
          <button
            className="btn btn-secondary btn-sm"
            data-testid="view-changes-btn"
            disabled={!dirtyCount}
            onClick={() => setDiffModalOpen(true)}
          >
            {c('viewChanges')}
          </button>
          <button
            className="btn btn-secondary btn-sm"
            data-testid="discard-drafts-btn"
            disabled={!dirtyCount}
            onClick={() => setDiscardModalOpen(true)}
          >
            {c('discard')}
          </button>
        </div>
      </div>
      <div className="tabs gateway-tabs" role="tablist" aria-label={c('gateway')}>
        {tabs.map((tab) => {
          const metrics = tabMetrics(tab);
          return (
            <button
              key={tab}
              ref={(node) => {
                tabRefs.current[tab] = node;
              }}
              id={'gateway-tab-' + tab}
              role="tab"
              aria-selected={currentTab === tab}
              aria-controls="gateway-workspace-panel"
              aria-label={[
                c(tab),
                metrics.dirty ? c('unsaved', metrics.dirty) : '',
                metrics.errors ? c('validationIssues', metrics.errors) : '',
              ]
                .filter(Boolean)
                .join(', ')}
              tabIndex={currentTab === tab ? 0 : -1}
              className={'tab-btn' + (currentTab === tab ? ' active' : '')}
              data-tab={tab}
              data-testid={'gateway-tab-' + tab}
              onClick={() => selectTab(tab)}
              onKeyDown={(event) => handleTabKey(event, tab)}
            >
              {c(tab)}
              {metrics.dirty > 0 && (
                <span
                  className={'gateway-tab-count' + (metrics.errors ? ' has-errors' : '')}
                  aria-hidden="true"
                  data-testid={'gateway-tab-state-' + tab}
                >
                  {metrics.errors ? c('validationIssues', metrics.errors) : metrics.dirty}
                </span>
              )}
            </button>
          );
        })}
      </div>
      <div
        id="gateway-workspace-panel"
        role="tabpanel"
        aria-labelledby={'gateway-tab-' + currentTab}
        tabIndex={0}
      >
        {showOverview && (
          <div className="gateway-overview-content" data-testid="gateway-overview-content">
            <div className="summary">
              <section className="card">
                <span className="muted">{c('mapped')}</span>
                <strong>
                  {c(
                    'fields',
                    sections.reduce((total, section) => total + section.fields.length, 0)
                  )}
                </strong>
              </section>
              <section className="card">
                <span className="muted">{c('owners')}</span>
                <strong>{c('ownerGroups', owners.length)}</strong>
              </section>
              <section className="card">
                <span className="muted">{c('connected')}</span>
                <strong>{c('retryControls')}</strong>
              </section>
            </div>
            <div className="notice">{c('catalogNotice')}</div>
            <div className="overview-grid">
              {sections.map((section) => (
                <button
                  key={section.id}
                  className="overview-card card"
                  data-testid={'open-section-' + section.id}
                  onClick={() => setCurrentTab(section.tab)}
                >
                  <span className="eyebrow">{c('fields', section.fields.length)}</span>
                  <h2>{section.title}</h2>
                  <p>{section.description}</p>
                  <span className="badge">
                    {section.id === 'retry' && dirtyCount
                      ? c('unsaved', dirtyCount)
                      : c('viewConfig')}
                  </span>
                </button>
              ))}
            </div>
            <h2 className="section-heading" style={{ marginTop: 28 }}>
              {c('owners')}
            </h2>
            {renderOwners('view-owner-')}
          </div>
        )}
        {showOwners && (
          <div className="gateway-owners-content" data-testid="gateway-owners-content">
            <section className="card" style={{ marginBottom: 18 }}>
              <h2>{c('owners')}</h2>
              <p className="muted">{c('ownerHelp')}</p>
              <div className="notice">{c('noYaml')}</div>
            </section>
            {renderOwners('owners-view-')}
          </div>
        )}
        <div
          className="retained-retry-container"
          data-testid="fresh-managed-retry"
          hidden={!countVisible && !intervalVisible}
        >
          <section className="card gateway-retry-group" data-testid="gateway-retry-group">
            <div className="card-head">
              <div>
                <h2>{c('retryTitle')}</h2>
                <p className="muted">{c('diffNotice')}</p>
              </div>
              {dirtyCount > 0 && (
                <span className="badge badge-amber">{c('unsaved', dirtyCount)}</span>
              )}
            </div>
            <FreshRetryConfiguration
              compact
              client={client}
              credential={credential}
              onUnauthorized={onUnauthorized}
              onPending={onPending}
              navigationRequest={navigationRequest}
              discardEpoch={discardEpoch}
              onDraftChange={setDraftDiffs}
              countVisible={countVisible}
              intervalVisible={intervalVisible}
            />
          </section>
        </div>
        {!showOverview && !showOwners && (
          <div className="gateway-sections-list stack" style={{ marginTop: 18 }}>
            {referenceSections.map((section) => (
              <section
                key={section.id}
                className="card v8-section"
                data-section={section.id}
                data-testid={'section-' + section.id}
              >
                <div className="card-head">
                  <div>
                    <div className="eyebrow">{c('fields', section.fields.length)}</div>
                    <h2>{section.title}</h2>
                    <p className="muted">{section.description}</p>
                  </div>
                  <span className="badge">{c('reference')}</span>
                </div>
                {section.fields.map((field) => (
                  <div key={field.path} className="config-field">
                    <div>
                      <label htmlFor={'field-' + field.path}>
                        {field.label}
                        {field.risk && (
                          <span className="badge badge-amber" style={{ marginLeft: 8 }}>
                            {c('risk')}
                          </span>
                        )}
                      </label>
                      <code>{field.path}</code>
                      <small>{c('notConnected')}</small>
                    </div>
                    <div className="field-control">
                      <input
                        id={'field-' + field.path}
                        type="text"
                        disabled
                        value=""
                        placeholder={c('notRead')}
                        readOnly
                      />
                    </div>
                  </div>
                ))}
              </section>
            ))}
            {!filteredSections.length && <p role="status">{c('noMatches')}</p>}
          </div>
        )}
      </div>
      <Modal
        open={diffModalOpen}
        title={c('diffTitle')}
        onClose={() => setDiffModalOpen(false)}
        footer={
          <button
            className="btn btn-primary"
            data-testid="close-diff-btn"
            onClick={() => setDiffModalOpen(false)}
          >
            {c('back')}
          </button>
        }
      >
        <div data-testid="diff-modal">
          <p className="muted">{c('diffHelp')}</p>
          <div className="diff-list">
            {dirtyDiffs.map((diff) => (
              <div key={diff.path} className="diff-item">
                <div className="diff-header">
                  <strong>{diff.label}</strong>
                  <code>{diff.path}</code>
                </div>
                <div className="diff-values">
                  <span className="diff-old">{diff.oldVal}</span>
                  <span className="diff-arrow">→</span>
                  <span className="diff-new">{diff.newVal}</span>
                </div>
              </div>
            ))}
          </div>
          <div className="notice" style={{ marginTop: 14 }}>
            {c('diffNotice')}
          </div>
        </div>
      </Modal>
      <Modal
        open={discardModalOpen}
        title={c('discardTitle')}
        onClose={() => setDiscardModalOpen(false)}
        footer={
          <>
            <button className="btn btn-secondary" onClick={() => setDiscardModalOpen(false)}>
              {c('keep')}
            </button>
            <button
              className="btn btn-danger"
              data-testid="confirm-discard-btn"
              onClick={() => {
                setDiscardEpoch((epoch) => epoch + 1);
                setDiscardModalOpen(false);
              }}
            >
              {c('confirmDiscard')}
            </button>
          </>
        }
      >
        <p data-testid="discard-modal">{c('discardHelp', dirtyCount)}</p>
      </Modal>
      <Modal
        open={!!activeOwner}
        title={activeOwner?.owner}
        onClose={() => setActiveOwnerPath(null)}
        footer={
          <>
            {activeOwner?.target && (
              <Link
                className="btn btn-primary"
                to={activeOwner.target}
                data-testid="open-owner-workspace"
                onClick={() => setActiveOwnerPath(null)}
              >
                {c('openOwner')}
              </Link>
            )}
            <button
              className="btn btn-secondary"
              data-testid="close-owner-btn"
              onClick={() => setActiveOwnerPath(null)}
            >
              {c('close')}
            </button>
          </>
        }
      >
        {activeOwner && (
          <div data-testid="owner-detail-modal">
            <p>{activeOwner.description}</p>
            <div className="info-card" style={{ marginTop: 14 }}>
              <span className="info-key">{c('pathRange')}</span>
              <code className="info-val">{activeOwner.path}</code>
            </div>
            <div className="notice" style={{ marginTop: 14 }}>
              {c(activeOwner.target ? 'ownerTargetHelp' : 'ownerUnavailable')}
            </div>
          </div>
        )}
      </Modal>
    </div>
  );
}
