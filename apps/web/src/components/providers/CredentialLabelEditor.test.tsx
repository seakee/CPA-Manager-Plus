import { act } from 'react';
import { create, type ReactTestRenderer } from 'react-test-renderer';
import { describe, expect, it, vi } from 'vitest';
import { CredentialLabelEditor } from './CredentialLabelEditor';
import { Input } from '@/components/ui/Input';
import { Button } from '@/components/ui/Button';
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (key: string) => key }) }));
vi.mock('@/components/ui/Modal', () => ({
  Modal: ({ children, footer }: { children: React.ReactNode; footer: React.ReactNode }) => (
    <div>
      {children}
      {footer}
    </div>
  ),
}));
const saveButton = (r: ReactTestRenderer) =>
  r.root.findAllByType(Button).find((button) => button.props.children === 'common.save')!;
describe('credential label editor', () => {
  it('keeps the draft when saving fails and supports retry and clearing', async () => {
    const save = vi.fn().mockRejectedValueOnce(new Error('offline')).mockResolvedValue(true);
    const close = vi.fn();
    let r!: ReactTestRenderer;
    act(() => {
      r = create(
        <CredentialLabelEditor
          initialLabel="Old"
          maskedKey="sk…12"
          saving={false}
          onClose={close}
          onSave={save}
        />
      );
    });
    act(() => r.root.findByType(Input).props.onChange({ target: { value: '  New account  ' } }));
    await act(async () => saveButton(r).props.onClick());
    expect(close).not.toHaveBeenCalled();
    expect(r.root.findByType(Input).props.value).toBe('  New account  ');
    expect(r.root.findByType(Input).props.error).toBe('ai_providers.credential_label_save_error');
    act(() => r.root.findByType(Input).props.onChange({ target: { value: '  ' } }));
    await act(async () => saveButton(r).props.onClick());
    expect(save).toHaveBeenLastCalledWith('');
    expect(close).toHaveBeenCalledOnce();
    act(() => r.unmount());
  });
  it('blocks overlong or control-character labels', () => {
    let r!: ReactTestRenderer;
    act(() => {
      r = create(
        <CredentialLabelEditor
          initialLabel=""
          maskedKey="sk…12"
          saving={false}
          onClose={vi.fn()}
          onSave={vi.fn()}
        />
      );
    });
    for (const value of ['x'.repeat(121), 'one\ntwo']) {
      act(() => r.root.findByType(Input).props.onChange({ target: { value } }));
      expect(saveButton(r).props.disabled).toBe(true);
    }
    act(() => r.root.findByType(Input).props.onChange({ target: { value: '界'.repeat(120) } }));
    expect(saveButton(r).props.disabled).toBe(false);
    act(() => r.unmount());
  });
});
