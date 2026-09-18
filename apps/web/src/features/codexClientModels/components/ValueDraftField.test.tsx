import { useState } from 'react';
import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { describe, expect, it, vi } from 'vitest';

vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}));

import { formatPlainText, parseNumberText, parseStringListText } from '../model/draftValues';
import { DraftField } from './ValueDraftField';

const input = (renderer: ReactTestRenderer) =>
  renderer.root.findAll((node) => node.type === 'input')[0];

const textarea = (renderer: ReactTestRenderer) =>
  renderer.root.findAll((node) => node.type === 'textarea')[0];

/** 数字框：取值由外层传入，调用 setValue 即可模拟别处改了取值。 */
function renderNumberField(value: unknown) {
  let renderer!: ReactTestRenderer;
  const render = (next: unknown) => (
    <DraftField
      value={next}
      format={formatPlainText}
      parse={parseNumberText}
      onCommit={() => undefined}
      ariaLabel="priority"
      singleLine
    />
  );
  act(() => {
    renderer = create(render(value));
  });
  return {
    renderer,
    setValue: (next: unknown) => {
      act(() => {
        renderer.update(render(next));
      });
    },
  };
}

/** 一行一项的数组框：像面板一样把输入写回取值。 */
function StringListProbe({
  initial,
  onCommit,
}: {
  initial: string[];
  onCommit: (value: unknown) => void;
}) {
  const [value, setValue] = useState<unknown>(initial);
  return (
    <DraftField
      value={value}
      format={(current) => (Array.isArray(current) ? current.join('\n') : '')}
      parse={parseStringListText}
      onCommit={(next) => {
        onCommit(next);
        setValue(next);
      }}
      ariaLabel="input_modalities"
    />
  );
}

describe('DraftField', () => {
  it('follows a value that another control changed', () => {
    const { renderer, setValue } = renderNumberField(6);
    expect(input(renderer).props.value).toBe('6');

    // 换个继承来源后取值变成来源模型的，输入框不能还停在旧取值上。
    setValue(1);
    expect(input(renderer).props.value).toBe('1');
  });

  it('keeps the raw text its own commit produced', () => {
    const onCommit = vi.fn();
    let renderer!: ReactTestRenderer;
    act(() => {
      renderer = create(<StringListProbe initial={['alpha']} onCommit={onCommit} />);
    });
    expect(textarea(renderer).props.value).toBe('alpha');

    act(() => {
      textarea(renderer).props.onChange({ target: { value: 'alpha\n\nbeta' } });
    });
    expect(onCommit).toHaveBeenCalledWith(['alpha', 'beta']);
    // 解析会把空行丢掉，但那行文本是用户自己敲进来的，不该被规范化顶掉。
    expect(textarea(renderer).props.value).toBe('alpha\n\nbeta');
  });
});
