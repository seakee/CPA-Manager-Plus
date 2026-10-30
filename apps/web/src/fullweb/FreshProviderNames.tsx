import { useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import styles from './FreshProviderNames.module.scss';
import { Card } from '../components/ui/Card';
import { Button } from '../components/ui/Button';
import { Input } from '../components/ui/Input';
import { PublicApiError, type PublicApiClient } from './publicApi';

import { parseProviderNames, type Group, type View } from './providerNamesClient';

// This editor exists only in Full Web v2. Names are stored by CPA, never in
// browser storage or a Manager alias database. A failed write requires a fresh
// read; no save is automatically replayed, including after a lost response.
export function FreshProviderNames({
  client,
  credential,
  onUnauthorized,
}: {
  client: PublicApiClient;
  credential: string;
  onUnauthorized: () => void;
}) {
  const { t } = useTranslation();
  const [view, setView] = useState<View | null>(null);
  const [editing, setEditing] = useState<Group | null>(null);
  const [draft, setDraft] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [needsRead, setNeedsRead] = useState(false);
  const epoch = useRef(0);
  const unauthorized = useRef(onUnauthorized);
  useEffect(() => {
    unauthorized.current = onUnauthorized;
  }, [onUnauthorized]);
  const locked = useRef(false);
  useEffect(() => {
    setView(null);
    setEditing(null);
    setDraft('');
    setError('');
    setBusy(false);
    locked.current = false;
    const id = ++epoch.current;
    void client
      .get('providers/names', { kind: 'admin', value: credential })
      .then((value) => {
        if (epoch.current === id) setView(parseProviderNames(value));
      })
      .catch((err) => {
        if (epoch.current !== id) return;
        if (err instanceof PublicApiError && err.kind === 'authentication') unauthorized.current();
        else setError(t('provider_names.read_failed'));
      });
    const requests = epoch;
    return () => {
      requests.current++;
    };
  }, [client, credential, t]);

  async function read() {
    if (locked.current) return;
    locked.current = true;
    setBusy(true);
    const id = ++epoch.current;
    try {
      const next = parseProviderNames(
        await client.get('providers/names', { kind: 'admin', value: credential })
      );
      if (epoch.current !== id) return;
      setView(next);
      setEditing(null);
      setDraft('');
      setNeedsRead(false);
      setError('');
    } catch (err) {
      if (epoch.current !== id) return;
      if (err instanceof PublicApiError && err.kind === 'authentication') unauthorized.current();
      else setError(t('provider_names.read_failed'));
    } finally {
      if (epoch.current === id) {
        locked.current = false;
        setBusy(false);
      }
    }
  }
  async function save() {
    if (!view || !editing || locked.current || needsRead || !view.writable) return;
    locked.current = true;
    setBusy(true);
    setError('');
    const id = ++epoch.current;
    try {
      await client.put(
        'providers/names',
        {
          generation: view.generation,
          etag: view.etag,
          provider: editing.provider,
          index: editing.index,
          name: draft.trim(),
        },
        { kind: 'admin', value: credential }
      );
      // Always read back CPA's persisted names. HTTP success alone does not
      // replace the snapshot or silently claim that the runtime has reloaded.
      const next = parseProviderNames(
        await client.get('providers/names', { kind: 'admin', value: credential })
      );
      if (epoch.current !== id) return;
      const saved = next.groups.find(
        (g) => g.provider === editing.provider && g.index === editing.index
      );
      if (!saved || saved.name !== draft.trim()) throw new Error('readback mismatch');
      setView(next);
      setEditing(null);
      setDraft('');
    } catch (err) {
      if (epoch.current !== id) return;
      if (err instanceof PublicApiError && err.kind === 'authentication') unauthorized.current();
      else {
        setError(t('provider_names.save_failed'));
        setNeedsRead(true);
      }
    } finally {
      if (epoch.current === id) {
        locked.current = false;
        setBusy(false);
      }
    }
  }
  return (
    <Card
      className={styles.panel}
      title={t('provider_names.title')}
      extra={
        <Button variant="secondary" disabled={busy} onClick={() => void read()}>
          {t('provider_names.refresh')}
        </Button>
      }
    >
      <p className="hint">{t('provider_names.hint')}</p>
      {error && (
        <p role="alert" className="error-box">
          {error}
        </p>
      )}
      {!view && !error && <p role="status">{t('provider_names.loading')}</p>}
      {view && !view.writable && <p>{t('provider_names.read_only')}</p>}
      {view && view.groups.length === 0 && <p>{t('provider_names.empty')}</p>}
      {view?.groups.map((group) => (
        <div key={`${group.provider}:${group.index}`} className={styles.row}>
          <strong>{group.name || t('provider_names.unnamed')}</strong>{' '}
          <span className="hint">
            {group.provider} · #{group.index + 1}
          </span>{' '}
          <Button
            variant="secondary"
            disabled={busy || needsRead || !view.writable}
            onClick={() => {
              setEditing(group);
              setDraft(group.name);
              setError('');
            }}
          >
            {t('provider_names.edit')}
          </Button>
        </div>
      ))}
      {editing && (
        <form
          onSubmit={(event) => {
            event.preventDefault();
            void save();
          }}
        >
          <Input
            label={`${t('provider_names.name')} · ${editing.provider} #${editing.index + 1}`}
            placeholder="account@example.com"
            maxLength={120}
            value={draft}
            disabled={busy || needsRead}
            onChange={(event) => setDraft(event.target.value)}
          />
          <Button type="submit" disabled={busy || needsRead || draft.trim() === editing.name}>
            {t('provider_names.save')}
          </Button>{' '}
          <Button
            variant="secondary"
            disabled={busy}
            onClick={() => {
              setEditing(null);
              setDraft('');
            }}
          >
            {t('provider_names.cancel')}
          </Button>
        </form>
      )}
    </Card>
  );
}
