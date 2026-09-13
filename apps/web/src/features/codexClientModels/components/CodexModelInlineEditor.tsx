import { Suspense, lazy, useId, useMemo, useState, type SyntheticEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Input } from '@/components/ui/Input';
import { Select } from '@/components/ui/Select';
import { IconChevronRight } from '@/components/ui/icons';
import type {
  CodexClientModelEntry,
  CodexClientServedModel,
} from '@/services/api/codexClientModels';
import { useThemeStore } from '@/stores';
import {
  DEFAULT_MODEL_TEMPLATE_SLUG,
  buildAdoptedModelPatch,
  buildInheritedModelPatch,
  buildModelPatchFromTemplate,
  findModelEntry,
  formatOverridePatch,
  parseOverridePatchInput,
  readModelSlug,
  resolveDefaultInheritEntry,
  resolveServedInheritEntry,
} from '../model/codexClientModelsModel';
import {
  collectCatalogFieldOptions,
  collectHiddenFieldKeys,
  collectReasoningDescriptions,
  countAdvancedFields,
} from '../model/codexModelQuickFields';
import {
  applyServedFields,
  isPlainObject,
  removeOverrideKey,
  setOverrideValue,
} from '../model/codexClientModelsTree';
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
  /**
   * edit 编辑已有条目，create 新增条目，adopt 为已下发但还没有专属条目的模型建立条目。
   * adopt 与 create 一样从继承源起步，但 slug 由模型本身决定，不能修改。
   */
  mode: 'edit' | 'create' | 'adopt';
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
  /** adopt 模式下的已下发摘要：继承源与要保留的展示取值都来自它。 */
  served?: CodexClientServedModel | null;
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
  served,
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
  // 已下发模型只在 adopt 模式下参与：继承源是它当前使用的模板。
  const adoptServed = mode === 'adopt' ? (served ?? null) : null;
  // 新条目起步时继承的目录条目：adopt 沿用该模型当前的模板，新增则用默认继承源。
  const inheritBaseEntry = useMemo(
    () =>
      mode === 'adopt'
        ? resolveServedInheritEntry(catalog, adoptServed?.templateSlug ?? '')
        : resolveDefaultInheritEntry(catalog),
    [adoptServed, catalog, mode]
  );
  const [slugInput, setSlugInput] = useState(slug);
  const [treePatch, setTreePatch] = useState<Record<string, unknown> | null>(() => {
    if (mode === 'edit') return isPlainObject(patch) ? patch : null;
    // 老版本 CPA 不认继承指令，只能整份复制模板。
    if (!supportsInherit) return buildModelPatchFromTemplate(templateEntry, slug);
    // 新条目默认整条继承一份现有配置，身份字段与客户端已看到的取值留给自己填。
    return adoptServed
      ? buildAdoptedModelPatch(inheritBaseEntry, adoptServed)
      : buildInheritedModelPatch(inheritBaseEntry, slug);
  });
  // 整体替换补丁（应用 JSON）后用它重建各输入框，避免残留旧草稿。
  const [revision, setRevision] = useState(0);
  const [jsonOpen, setJsonOpen] = useState(false);
  const [jsonText, setJsonText] = useState(() => formatOverridePatch(treePatch));
  const [jsonError, setJsonError] = useState('');

  const trimmedSlug = slugInput.trim();
  const directives = useMemo(() => readInheritDirectives(treePatch), [treePatch]);
  // 默认模板是条目的起点：指向它的整条继承指令由编辑器写出来，不是用户挑的来源，
  // 因此界面把它当作「默认值」，字段不会显示成继承自默认模板。
  const displayDirectives = useMemo(() => {
    if (directives.get('') !== DEFAULT_MODEL_TEMPLATE_SLUG) return directives;
    const visible = new Map(directives);
    visible.delete('');
    return visible;
  }, [directives]);
  const lookup = useMemo(() => createInheritSourceLookup(catalog), [catalog]);
  // 服务端自己决定的字段：默认值就是下发取值，整条继承默认不接管它们。
  const heldFields = useMemo(() => new Set(Object.keys(served?.servedFields ?? {})), [served]);
  const sources = useMemo(
    () =>
      supportsInherit ? collectInheritSources(catalog, mode === 'edit' ? slug : undefined) : [],
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

  // 生效参照值：条目内容先换成客户端当前收到的配置，再叠上继承指令，
  // 于是字段旁预览的既是默认值，也是真正会生效的取值。
  const effective = useMemo(() => {
    const base = mode === 'edit' ? effectiveEntry : inheritBaseEntry;
    const servedBase = applyServedFields(base, served?.servedFields);
    return applyInheritDirectives(servedBase, directives, lookup, heldFields);
  }, [directives, effectiveEntry, heldFields, inheritBaseEntry, lookup, mode, served]);

  const sourceBinding: FieldInheritBinding = useMemo(
    () => ({
      directives: displayDirectives,
      sources,
      heldFields,
      issueOf: (path) => issueByPath.get(path.join('.')),
      clear: (path) => setTreePatch((previous) => clearFieldOverride(previous, path)),
      inherit: (path, sourceSlug) =>
        setTreePatch((previous) =>
          setInheritSource(removeOverrideKey(previous, path), path, sourceSlug)
        ),
      remove: (path) =>
        setTreePatch((previous) => setOverrideValue(previous, path, null, effective)),
    }),
    [displayDirectives, effective, heldFields, issueByPath, sources]
  );

  const fieldOptions = useMemo(() => collectCatalogFieldOptions(catalog), [catalog]);
  // 新增推理强度时沿用目录里已有的说明文字，避免默认说明在客户端里丢失。
  const levelDescriptions = useMemo(() => collectReasoningDescriptions(catalog), [catalog]);
  const hiddenFieldKeys = useMemo(() => collectHiddenFieldKeys(), []);
  const advancedCount = useMemo(
    () => countAdvancedFields(effective, treePatch),
    [effective, treePatch]
  );
  const inheritUsage = useMemo(
    () => (mode === 'edit' ? (countInheritUsage(overrideDocument).get(slug) ?? 0) : 0),
    [mode, overrideDocument, slug]
  );

  const rootSource = displayDirectives.get('') ?? '';
  const inheritOptions = useMemo(() => {
    const options = [{ value: '', label: t('codex_client_models.inherit_root_none') }];
    sources.forEach((source) => options.push({ value: source, label: source }));
    // 指向已经不存在的模型时保留这个取值，否则下拉框会显示成「不继承」，看不出问题。
    if (rootSource && !sources.includes(rootSource)) {
      options.push({ value: rootSource, label: rootSource });
    }
    return options;
  }, [rootSource, sources, t]);

  const inheritSourceLabel = useMemo(() => readModelSlug(inheritBaseEntry), [inheritBaseEntry]);
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
      mode === 'edit' ? treePatch : { ...treePatch, slug: trimmedSlug };
    onSave(trimmedSlug, nextPatch);
  };

  return (
    <div className={styles.editor}>
      <header className={styles.header}>
        <div className={styles.heading}>
          <h3 className={styles.title}>
            {mode === 'create'
              ? t('codex_client_models.editor_title_create')
              : mode === 'adopt'
                ? t('codex_client_models.editor_title_adopt', { slug })
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

      {mode === 'adopt' ? (
        <div className={styles.slugField}>
          <div className={styles.slugLocked}>
            <span className={styles.slugLockedLabel}>{t('codex_client_models.slug_label')}</span>
            <code className={styles.slugLockedValue}>{slug}</code>
          </div>
          <p className={styles.slugLockedHint}>{t('codex_client_models.adopt_slug_hint')}</p>
        </div>
      ) : null}

      {mode === 'adopt' ? (
        <div className={styles.notice}>
          {t('codex_client_models.adopt_notice', { source: inheritSourceLabel })}
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
        levelDescriptions={levelDescriptions}
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
