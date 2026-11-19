import { createRef } from 'react';
import type * as ReactModule from 'react';
import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, describe, expect, it, vi } from 'vitest';
import GatewaySourceEditor, { type GatewaySourceEditorHandle } from './GatewaySourceEditor';

const editorSpy = vi.hoisted(() => ({
  focus: vi.fn(),
  dispatch: vi.fn(),
  doc: 'routing:\n  strategy: round-robin\n  fallback: round-robin\n',
  selection: { from: 0, to: 0, empty: true },
}));

vi.mock('@uiw/react-codemirror', async () => {
  const { forwardRef, useImperativeHandle } = await vi.importActual<typeof ReactModule>('react');
  const ExternalChange = { of: (v: unknown) => v };
  const CodeMirror = forwardRef((_props: Record<string, unknown>, ref) => {
    useImperativeHandle(
      ref,
      () => ({
        view: {
          focus: editorSpy.focus,
          dispatch: (spec: { selection?: { anchor: number; head: number } }) => {
            editorSpy.dispatch(spec);
            if (spec.selection) {
              editorSpy.selection = {
                from: spec.selection.anchor,
                to: spec.selection.head,
                empty: spec.selection.anchor === spec.selection.head,
              };
            }
          },
          get state() {
            return {
              doc: { toString: () => editorSpy.doc },
              selection: { main: editorSpy.selection },
            };
          },
        },
      }),
      []
    );
    return <div data-testid="mock-codemirror" />;
  });
  return { default: CodeMirror, ExternalChange };
});

describe('GatewaySourceEditor search focus', () => {
  let renderer: ReactTestRenderer | undefined;

  afterEach(async () => {
    if (renderer) {
      await act(async () => renderer!.unmount());
      renderer = undefined;
    }
    editorSpy.focus.mockReset();
    editorSpy.dispatch.mockReset();
    editorSpy.selection = { from: 0, to: 0, empty: true };
  });

  it('advances matches on repeated search() calls without stealing focus to CodeMirror', async () => {
    const ref = createRef<GatewaySourceEditorHandle>();
    (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

    await act(async () => {
      renderer = create(
        <GatewaySourceEditor
          editorRef={ref}
          value={editorSpy.doc}
          editable={true}
          onChange={vi.fn()}
        />
      );
    });

    const first = ref.current!.search('round-robin', 'next');
    const second = ref.current!.search('round-robin', 'next');

    expect(first).toEqual({ current: 1, total: 2 });
    expect(second).toEqual({ current: 2, total: 2 });
    expect(editorSpy.dispatch).toHaveBeenCalledTimes(2);
    expect(editorSpy.focus).not.toHaveBeenCalled();
  });
});
