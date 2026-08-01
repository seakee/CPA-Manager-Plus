import { renderToStaticMarkup } from 'react-dom/server';
import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { describe, expect, it, vi } from 'vitest';
import type { SharedInspectionConfigDraft } from '@/features/monitoring/model/codexInspectionPresentation';
import { InspectionConfigFields } from './InspectionConfigFields';

const t = ((key: string) => key) as never;

const createDraft = (
  overrides: Partial<SharedInspectionConfigDraft> = {}
): SharedInspectionConfigDraft => ({
  targetTypes: 'codex+xai',
  usedPercentThreshold: '100',
  sampleSize: '0',
  workers: '4',
  deleteWorkers: '4',
  timeout: '15000',
  retries: '0',
  userAgent: 'codex-agent',
  xaiInferenceUserAgent: 'xai-agent',
  xaiInferenceModel: 'grok-4.5',
  xaiInferencePrompt: 'Reply with exactly OK.',
  autoActionMode: 'none',
  autoRecoverEnabled: false,
  xaiInferenceEnabled: false,
  ...overrides,
});

const renderFields = (draft: SharedInspectionConfigDraft) =>
  renderToStaticMarkup(
    <InspectionConfigFields
      draft={draft}
      errors={{}}
      t={t}
      onFieldChange={vi.fn()}
      onXaiInferenceEnabledChange={vi.fn()}
      onAutoActionModeChange={vi.fn()}
      onAutoRecoverEnabledChange={vi.fn()}
    />
  );

describe('InspectionConfigFields', () => {
  it('hides xAI inference settings when inference is off', () => {
    const markup = renderFields(createDraft());

    expect(markup).not.toContain('monitoring.codex_inspection_probe_source_xai_inference');
    expect(markup).not.toContain('id="xaiInferenceUserAgent"');
    expect(markup).not.toContain('id="xaiInferenceModel"');
    expect(markup).not.toContain('id="xaiInferencePrompt"');
  });

  it('enables xAI inference settings when inference is on', () => {
    const markup = renderFields(createDraft({ xaiInferenceEnabled: true }));

    expect(markup).toContain('monitoring.codex_inspection_probe_source_xai_inference');
    expect(markup).toContain('id="xaiInferenceModel"');
    expect(markup).not.toMatch(/id="xaiInferenceModel"[^>]*disabled/);
    expect(markup).not.toMatch(/id="xaiInferencePrompt"[^>]*disabled/);
  });

  it('hides xAI inference settings when xAI is not targeted', () => {
    const markup = renderFields(createDraft({ targetTypes: 'codex' }));

    expect(markup).not.toContain('id="xaiInferenceEnabled"');
    expect(markup).not.toContain('id="xaiInferenceModel"');
    expect(markup).not.toContain('id="xaiInferencePrompt"');
  });

  it('only shows provider-specific user agents for active providers', () => {
    const xaiMarkup = renderFields(createDraft({ targetTypes: 'xai', xaiInferenceEnabled: true }));
    const codexMarkup = renderFields(createDraft({ targetTypes: 'codex' }));

    expect(xaiMarkup).not.toContain('id="userAgent"');
    expect(xaiMarkup).toContain('id="xaiInferenceUserAgent"');
    expect(codexMarkup).toContain('id="userAgent"');
    expect(codexMarkup).not.toContain('id="xaiInferenceUserAgent"');
  });

  it('orders common execution settings before provider-specific groups', () => {
    const markup = renderFields(createDraft({ xaiInferenceEnabled: true }));
    const concurrencyGroup = markup.indexOf(
      '<span>monitoring.codex_inspection_settings_group_concurrency</span>'
    );
    const codexGroup = markup.indexOf('<span>monitoring.codex_inspection_target_codex</span>');
    const xaiGroup = markup.indexOf(
      '<span>monitoring.codex_inspection_probe_source_xai_inference</span>'
    );

    expect(concurrencyGroup).toBeGreaterThan(-1);
    expect(codexGroup).toBeGreaterThan(concurrencyGroup);
    expect(xaiGroup).toBeGreaterThan(codexGroup);
  });

  it('uses provider context for concise advanced field labels', () => {
    const markup = renderFields(createDraft({ xaiInferenceEnabled: true }));

    expect(markup).toContain('monitoring.codex_inspection_settings_provider_user_agent_label');
    expect(markup).toContain('monitoring.codex_inspection_settings_provider_model_label');
    expect(markup).toContain('monitoring.codex_inspection_settings_provider_prompt_label');
    expect(markup).not.toContain('monitoring.codex_inspection_settings_xai_user_agent_label');
    expect(markup).not.toContain('monitoring.codex_inspection_settings_xai_model_label');
    expect(markup).not.toContain('monitoring.codex_inspection_settings_xai_prompt_label');
  });

  it('places prompt actions after the prompt editor', () => {
    const markup = renderFields(createDraft({ xaiInferenceEnabled: true }));
    const promptEditor = markup.indexOf('id="xaiInferencePrompt"');
    const restoreAction = markup.indexOf(
      'monitoring.codex_inspection_settings_restore_default_prompt'
    );

    expect(promptEditor).toBeGreaterThan(-1);
    expect(restoreAction).toBeGreaterThan(promptEditor);
  });

  it('opens advanced settings when a common execution field is invalid', () => {
    const setAttribute = vi.fn();
    let renderer: ReactTestRenderer;

    act(() => {
      renderer = create(
        <InspectionConfigFields
          draft={createDraft()}
          errors={{ workers: 'invalid workers' }}
          t={t}
          onFieldChange={vi.fn()}
          onXaiInferenceEnabledChange={vi.fn()}
          onAutoActionModeChange={vi.fn()}
          onAutoRecoverEnabledChange={vi.fn()}
        />,
        {
          createNodeMock: (element) => (element.type === 'details' ? { setAttribute } : {}),
        }
      );
    });

    expect(setAttribute).toHaveBeenCalledWith('open', '');
    act(() => renderer!.unmount());
  });
});
