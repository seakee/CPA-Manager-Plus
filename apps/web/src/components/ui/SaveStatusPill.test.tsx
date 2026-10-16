import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { describe, expect, it, vi } from 'vitest';
import { SaveStatusPill } from './SaveStatusPill';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const readText = (value: unknown): string => {
  if (typeof value === 'string' || typeof value === 'number') return String(value);
  if (Array.isArray(value)) return value.map(readText).join('');
  if (value && typeof value === 'object' && 'children' in value) {
    return readText((value as { children?: unknown }).children);
  }
  return '';
};

const renderPill = (overrides: Partial<Parameters<typeof SaveStatusPill>[0]> = {}) => {
  const props = {
    status: 'Saved',
    statusOptions: ['Saved', 'Unsaved Changes', 'Saving…'],
    tone: 'saved' as const,
    onReset: vi.fn(),
    onSave: vi.fn(),
    resetLabel: 'Discard',
    saveLabel: 'Save Changes',
    ...overrides,
  };
  let renderer!: ReactTestRenderer;
  act(() => {
    renderer = create(<SaveStatusPill {...props} />);
  });
  return { renderer, props };
};

describe('SaveStatusPill', () => {
  it('reserves room for every status so the pill never changes width', () => {
    const { renderer } = renderPill();
    const sizers = renderer.root.findAll(
      (node) =>
        node.type === 'span' &&
        node.props['aria-hidden'] === 'true' &&
        typeof node.props.className === 'string' &&
        node.props.className.includes('statusSizer')
    );

    expect(sizers.map((node) => readText(node.props.children))).toEqual([
      'Saved',
      'Unsaved Changes',
      'Saving…',
    ]);
    expect(renderer.root.findByProps({ role: 'status' }).props.children).toBe('Saved');
  });

  it('labels both actions with text and separates the segments', () => {
    const { renderer, props } = renderPill({ dirty: true, tone: 'modified' });
    const buttons = renderer.root.findAllByType('button');
    const separators = renderer.root.findAll(
      (node) =>
        node.type === 'span' &&
        typeof node.props.className === 'string' &&
        node.props.className.includes('separator')
    );

    expect(buttons.map((button) => button.props['aria-label'])).toEqual([
      'Discard',
      'Save Changes',
    ]);
    expect(readText(renderer.toJSON())).toContain('Save Changes');
    expect(separators).toHaveLength(2);

    act(() => {
      buttons[0].props.onClick();
      buttons[1].props.onClick();
    });
    expect(props.onReset).toHaveBeenCalledTimes(1);
    expect(props.onSave).toHaveBeenCalledTimes(1);
  });

  it('disables actions independently', () => {
    const { renderer } = renderPill({ resetDisabled: true, saveDisabled: true });
    const [reset, save] = renderer.root.findAllByType('button');

    expect(reset.props.disabled).toBe(true);
    expect(save.props.disabled).toBe(true);
  });
});
