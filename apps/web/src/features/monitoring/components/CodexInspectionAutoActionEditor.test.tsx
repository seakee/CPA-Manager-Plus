import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { describe, expect, it } from 'vitest';
import { CodexInspectionAutoActionEditor } from './CodexInspectionAutoActionEditor';

const t = ((key: string) => key) as never;

const renderEditor = (value: 'none' | 'delete', autoRecoverEnabled = false) => {
  let renderer: ReactTestRenderer;
  act(() => {
    renderer = create(
      <CodexInspectionAutoActionEditor
        value={value}
        autoRecoverEnabled={autoRecoverEnabled}
        t={t}
        onChange={() => undefined}
        onAutoRecoverChange={() => undefined}
      />
    );
  });
  return renderer!;
};

describe('CodexInspectionAutoActionEditor', () => {
  it('only shows the active option descriptions', () => {
    const renderer = renderEditor('none');
    const text = renderer.root
      .findAllByType('small')
      .map((node) => node.children.join(''));

    expect(text).toEqual([
      'monitoring.codex_inspection_settings_auto_execution_off_desc',
    ]);
  });

  it('keeps the selected execution, problem-action and recovery descriptions', () => {
    const renderer = renderEditor('delete');
    const text = renderer.root
      .findAllByType('small')
      .map((node) => node.children.join(''));

    expect(text).toEqual(
      expect.arrayContaining([
        'monitoring.codex_inspection_settings_auto_execution_on_desc',
        'monitoring.codex_inspection_settings_problem_action_delete_desc',
        'monitoring.codex_inspection_settings_auto_recover_off_desc',
      ])
    );
    expect(text).not.toContain('monitoring.codex_inspection_settings_problem_action_none_desc');
  });
});
