import { Suspense, lazy, useId, useMemo, useState, type SyntheticEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Input } from '@/components/ui/Input';
import { Select } from '@/components/ui/Select';
import { IconChevronRight } from '@/components/ui/icons';
import type { CodexClientModelEntry } from '@/services/api/codexClientModels';
import { useThemeStore } from '@/stores';
import {
  DEFAULT_MODEL_TEMPLATE_SLUG,
  buildInheritedModelPatch,
  buildModelPatchFromTemplate,
  findModelEntry,
  formatOverridePatch,
  parseOverridePatchInput,
  resolveDefaultInheritEntry,
} from '../model/codexClientModelsModel';
import {
  collectCatalogFieldOptions,
  collectHiddenFieldKeys,
  countAdvancedFields,
} from '../model/codexModelQuickFields';
import { removeOverrideKey, setOverrideValue, isPlainObject } from '../model/codexClientModelsTree';
import {
  applyInheritDirectives,
  clearFieldOverride,
  clearInheritSource,
  collectInheritSources,
  countInheritUsage,
  createInheritSourceLookup,
  describeInheritIssue,
  readInheritDirectives,
  setInheritSource,
  validateInheritDirectives,
  type FieldInheritBinding,
} from '../model/codexClientModelsInherit';
import { CodexModelQuickFields } from './CodexModelQuickFields';
import { OverrideTreeEditor } from './OverrideTreeEditor';
import styles from './CodexModelInlineEditor.module.scss';

const LazyOverrideJsonEditor = lazy(() => import('./OverrideJsonEditor'));

export interface CodexModelInlineEditorProps {
  mode: 'edit' | 'create';
  slug: string;
  /** 生效条目；被 null 补丁删除或尚未进入目录时为空。 */
  effectiveEntry: CodexClientModelEntry | null;
  /** 当前覆写补丁原值。 */
  patch: unknown;
  /** 生效目录，用来收集候选值、解析继承源与挑选默认继承源。 */
  catalog: ReadonlyArray<CodexClientModelEntry>;
  /** 整份覆写文档，用来统计本条目被多少条覆写继承。 */
  overrideDocument: Record<string, unknown>;
  /** 服务端是否支持字段继承；老版本会忽略继承指令。 */
  supportsInherit: boolean;
  isSlugTaken: (slug: string) => boolean;
  saving: boolean;
  serverError: string;
  onCancel: () => void;
  onSave: (slug: string, patch: Record<string, unknown> | null) => void;
  onDelete: (slug: string) => void;
}

/** 模型条目编辑器：常用字段可视化配置，其余字段与 JSON 收在折叠区里。 */
export function CodexModelInlineEditor({
  mode,
  slug,
  effectiveEntry,
  patch,
  catalog,
  overrideDocument,
  supportsInherit,
  isSlugTaken,
  saving,
  serverError,
  onCancel,
  onSave,
  onDelete,
}: CodexModelInlineEditorProps) {
  const { t } = useTranslation();
  const resolvedTheme = useThemeStore((state) => state.resolvedTheme);
  const sourceLabelId = useId();
  const templateEntry = useMemo(
    () => findModelEntry(catalog, DEFAULT_MODEL_TEMPLATE_SLUG),
    [catalog]
  );
  const [slugInput, setSlugInput] = useState(slug);
  const [treePatch, setTreePatch] = useState<Record<string, unknown> | null>(() => {
    if (mode !== 'create') return isPlainObject(patch) ? patch : null;
    // 新条目默认整条继承一份现有配置，只有身份字段留给自己填。
    return supportsInherit
      ? buildInheritedModelPatch(resolveDefaultInheritEntry(catalog), slug)
      : buildModelPatchFromTemplate(templateEntry, slug);
  });
  // 整体替换补丁（应用 JSON）后用它重建各输入框，避免残留旧草稿。
  const [revision, setRevision] = useState(0);
  const [jsonOpen, setJsonOpen] = useState(false);
  const [jsonText, setJsonText] = useState(() => formatOverridePatch(treePatch));
  const [jsonError, setJsonError] = useState('');

  const trimmedSlug = slugInput.trim();
  const directives = useMemo(() => readInheritDirectives(treePatch), [treePatch]);
  const lookup = useMemo(() => createInheritSourceLookup(catalog), [catalog]);
  const sources = useMemo(
    () =>
      supportsInherit ? collectInheritSources(catalog, mode === 'create' ? undefined : slug) : [],
    [catalog, mode, slug, supportsInherit]
  );
  const issues = useMemo(
    () => validateInheritDirectives({ slug: trimmedSlug, patch: treePatch, catalog }),
    [catalog, trimmedSlug, treePatch]
  );
  const issueByPath = useMemo(() => {
    const map = new Map<string, string>();
    issues.forEach((issue) => {
      if (issue.path) map.set(issue.path, describeInheritIssue(issue.code, t));
    });
    return map;
  }, [issues, t]);
  const rootIssues = useMemo(() => issues.filter((issue) => issue.path === ''), [issues]);

  // 生效参照值：本条目自身的内容，再叠上继承指令，于是字段旁预览的就是真正会生效的取值。
  const effective = useMemo(() => {
    const base = mode === 'create' ? templateEntry : effectiveEntry;
    return applyInheritDirectives(base, directives, lookup);
  }, [directives, effectiveEntry, lookup, mode, templateEntry]);

  const sourceBinding: FieldInheritBinding = useMemo(
    () => ({
      directives,
      sources,
      issueOf: (path) => issueByPath.get(path.join('.')),
      clear: (path) => setTreePatch((previous) => clearFieldOverride(previous, path)),
      inherit: (path, sourceSlug) =>
        setTreePatch((previous) =>
          setInheritSource(removeOverrideKey(previous, path), path, sourceSlug)
        ),
      remove: (path) =>
        setTreePatch((previous) => setOverrideValue(previous, path, null, effective)),
    }),
    [directives, effective, issueByPath, sources]
  );

  const fieldOptions = useMemo(() => collectCatalogFieldOptions(catalog), [catalog]);
  const hiddenFieldKeys = useMemo(() => collectHiddenFieldKeys(), []);
  const advancedCount = useMemo(
    () => countAdvancedFields(effective, treePatch),
    [effective, treePatch]
  );
  const inheritUsage = useMemo(
    () => (mode === 'create' ? 0 : (countInheritUsage(overrideDocument).get(slug) ?? 0)),
    [mode, overrideDocument, slug]
  );

  const rootSource = directives.get('') ?? '';
  const inheritOptions = useMemo(() => {
    const options = [{ value: '', label: t('codex_client_models.inherit_root_none') }];
    sources.forEach((source) => options.push({ value: source, label: source }));
    // 指向已经不存在的模型时保留这个取值，否则下拉框会显示成「不继承」，看不出问题。
    if (rootSource && !sources.includes(rootSource)) {
      options.push({ value: rootSource, label: rootSource });
    }
    return options;
  }, [rootSource, sources, t]);

  const slugMissing = mode === 'create' && !trimmedSlug;
  const slugTaken = mode === 'create' && Boolean(trimmedSlug) && isSlugTaken(trimmedSlug);
  // JSON 面板是整体替换入口：文本与当前补丁不一致时先应用或还原，避免保存出意外内容。
  const jsonDirty = jsonOpen && jsonText !== formatOverridePatch(treePatch);
  const canSave = !saving && !slugMissing && !jsonDirty;

  const handleSlugChange = (value: string) => {
    setSlugInput(value);
    if (mode !== 'create') return;
    const nextSlug = value.trim();
    const base = treePatch ?? {};
    const previousSlug = typeof base.slug === 'string' ? base.slug : '';
    const displayNameFollowsSlug =
      typeof base.display_name !== 'string' ||
      base.display_name === previousSlug ||
      base.display_name === '';
    setTreePatch({
      ...base,
      slug: nextSlug,
      ...(displayNameFollowsSlug ? { display_name: nextSlug } : {}),
    });
    // 展示名跟着 slug 走时要重建输入框，否则草稿还停在模板里的旧名字。
    if (displayNameFollowsSlug) setRevision((previous) => previous + 1);
  };

  const handleRootSourceChange = (value: string) => {
    setTreePatch((previous) =>
      value ? setInheritSource(previous, [], value) : clearInheritSource(previous, [])
    );
  };

  const handleJsonToggle = (event: SyntheticEvent<HTMLDetailsElement>) => {
    const open = event.currentTarget.open;
    setJsonOpen(open);
    if (open && !jsonDirty) {
      setJsonText(formatOverridePatch(treePatch));
      setJsonError('');
    }
  };

  const handleApplyJson = () => {
    const result = parseOverridePatchInput(jsonText);
    if (!result.ok) {
      setJsonError(
        result.error === 'empty'
          ? t('codex_client_models.patch_error_empty')
          : result.error === 'not_object'
            ? t('codex_client_models.patch_error_not_object')
            : t('codex_client_models.patch_error_invalid_json', { message: result.detail || '' })
      );
      return;
    }
    setTreePatch(result.patch);
    setJsonText(formatOverridePatch(result.patch));
    setJsonError('');
    setRevision((previous) => previous + 1);
  };

  const handleResetJson = () => {
    setJsonText(formatOverridePatch(treePatch));
    setJsonError('');
  };

  const handleSave = () => {
    if (!canSave) return;
    if (treePatch === null) {
      onSave(trimmedSlug, null);
      return;
    }
    const nextPatch: Record<string, unknown> =
      mode === 'create' ? { ...treePatch, slug: trimmedSlug } : treePatch;
    onSave(trimmedSlug, nextPatch);
  };

  return (
    <div className={styles.editor}>
      <header className={styles.header}>
        <div className={styles.heading}>
          <h3 className={styles.title}>
            {mode === 'create'
              ? t('codex_client_models.editor_title_create')
              : t('codex_client_models.editor_title_edit', { slug })}
          </h3>
          <p className={styles.subtitle}>{t('codex_client_models.editor_subtitle')}</p>
        </div>
        <div className={styles.headerActions}>
          {inheritUsage > 0 ? (
            <span className={styles.usageBadge}>
              {t('codex_client_models.inherit_usage', { value: inheritUsage })}
            </span>
          ) : null}
          {mode === 'edit' ? (
            <Button size="xs" variant="danger" disabled={saving} onClick={() => onDelete(slug)}>
              {t('codex_client_models.delete_override')}
            </Button>
          ) : null}
          <Button size="xs" variant="secondary" disabled={saving} onClick={onCancel}>
            {t('common.cancel')}
          </Button>
          <Button size="xs" loading={saving} disabled={!canSave} onClick={handleSave}>
            {t('codex_client_models.save_override')}
          </Button>
        </div>
      </header>

      {mode === 'create' ? (
        <div className={styles.slugField}>
          <Input
            label={t('codex_client_models.slug_label')}
            value={slugInput}
            onChange={(event) => handleSlugChange(event.target.value)}
            placeholder="gpt-5.6-sol"
            hint={t('codex_client_models.slug_hint')}
          />
        </div>
      ) : null}

      {supportsInherit ? (
        <div className={styles.sourceRow}>
          <span className={styles.sourceLabel} id={sourceLabelId}>
            {t('codex_client_models.inherit_root_label')}
          </span>
          <div className={styles.sourceSelect}>
            <Select
              value={rootSource}
              options={inheritOptions}
              onChange={handleRootSourceChange}
              ariaLabelledBy={sourceLabelId}
              disabled={saving}
            />
          </div>
          <span className={styles.sourceHint}>{t('codex_client_models.inherit_root_hint')}</span>
        </div>
      ) : null}

      {slugTaken ? (
        <div className={styles.warning}>{t('codex_client_models.slug_taken')}</div>
      ) : null}
      {mode === 'edit' && !effectiveEntry ? (
        <div className={styles.notice}>{t('codex_client_models.editor_removed_notice')}</div>
      ) : null}
      {rootIssues.length > 0 ? (
        <div className={styles.warning}>
          {t('codex_client_models.inherit_issue_title')}
          <ul className={styles.issueList}>
            {rootIssues.map((issue) => (
              <li key={issue.code}>{describeInheritIssue(issue.code, t)}</li>
            ))}
          </ul>
        </div>
      ) : null}

      <CodexModelQuickFields
        key={revision}
        effective={effective}
        patch={treePatch}
        fieldOptions={fieldOptions}
        sourceBinding={sourceBinding}
        onChange={setTreePatch}
        disabled={saving}
      />

      <details className={styles.disclosure}>
        <summary className={styles.disclosureSummary}>
          <IconChevronRight size={13} className={styles.disclosureIcon} />
          <span className={styles.disclosureTitle}>
            {t('codex_client_models.editor_advanced_fields')}
          </span>
          <span className={styles.disclosureMeta}>
            {t('codex_client_models.editor_advanced_count', { value: advancedCount })}
          </span>
        </summary>
        <div className={styles.disclosureBody}>
          <p className={styles.disclosureHint}>{t('codex_client_models.editor_advanced_hint')}</p>
          <OverrideTreeEditor
            key={revision}
            effective={effective}
            hiddenKeys={hiddenFieldKeys}
            patch={treePatch}
            sourceBinding={sourceBinding}
            onChange={setTreePatch}
            disabled={saving}
          />
        </div>
      </details>

      <details className={styles.disclosure} open={jsonOpen} onToggle={handleJsonToggle}>
        <summary className={styles.disclosureSummary}>
          <IconChevronRight size={13} className={styles.disclosureIcon} />
          <span className={styles.disclosureTitle}>
            {t('codex_client_models.editor_json_title')}
          </span>
          {jsonDirty ? (
            <span className={styles.disclosureDirty}>
              {t('codex_client_models.editor_json_dirty')}
            </span>
          ) : null}
        </summary>
        <div className={styles.disclosureBody}>
          <p className={styles.disclosureHint}>{t('codex_client_models.editor_json_hint')}</p>
          {jsonOpen ? (
            <>
              <div className={styles.jsonEditor}>
                <Suspense fallback={null}>
                  <LazyOverrideJsonEditor
                    value={jsonText}
                    onChange={(value) => {
                      setJsonText(value);
                      setJsonError('');
                    }}
                    theme={resolvedTheme}
                    editable={!saving}
                    placeholder={t('codex_client_models.patch_placeholder')}
                  />
                </Suspense>
              </div>
              {jsonError ? <div className="error-box">{jsonError}</div> : null}
              <div className={styles.jsonActions}>
                <Button
                  size="xs"
                  variant="secondary"
                  disabled={saving || !jsonDirty}
                  onClick={handleResetJson}
                >
                  {t('codex_client_models.editor_json_reset')}
                </Button>
                <Button
                  size="xs"
                  variant="primary"
                  disabled={saving || !jsonDirty}
                  onClick={handleApplyJson}
                >
                  {t('codex_client_models.editor_json_apply')}
                </Button>
              </div>
            </>
          ) : null}
        </div>
      </details>

      {jsonDirty ? (
        <div className={styles.notice}>{t('codex_client_models.editor_json_dirty_hint')}</div>
      ) : null}
      {serverError ? <div className="error-box">{serverError}</div> : null}
    </div>
  );
}
