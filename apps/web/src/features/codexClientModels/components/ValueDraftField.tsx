import { useId, useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  describeDraftError,
  type DraftParseError,
  type DraftParseResult,
} from '../model/draftValues';
import styles from './ValueDraftField.module.scss';

export interface DraftFieldProps {
  value: unknown;
  format: (value: unknown) => string;
  parse: (text: string) => DraftParseResult;
  onCommit: (value: unknown) => void;
  ariaLabel: string;
  disabled?: boolean;
  rows?: number;
  singleLine?: boolean;
  monospace?: boolean;
  /** 关掉浏览器自带的拖拽调整大小，高度交给 rows 控制。 */
  resizable?: boolean;
  placeholder?: string;
}

/**
 * 带草稿状态的输入框：输入过程保留原始文本，解析成功才写回补丁。
 * 因此多行文本按真实换行显示，解析失败的中间态也不会污染补丁。
 * 取值被别处改动时（换个继承来源、恢复默认、删除字段）草稿跟着取值走，
 * 输入框里不会留着改之前的旧取值。
 */
export function DraftField({
  value,
  format,
  parse,
  onCommit,
  ariaLabel,
  disabled = false,
  rows = 4,
  singleLine = false,
  monospace = false,
  resizable = true,
  placeholder,
}: DraftFieldProps) {
  const { t } = useTranslation();
  // 草稿只在挂载时读一次当前取值，之后由输入驱动：切换条目、填入模板、
  // 还原覆写会重新挂载编辑器，取值在别处的改动由下面的同步跟上。
  const formatted = format(value);
  const [draft, setDraft] = useState(formatted);
  const [error, setError] = useState<DraftParseError | null>(null);
  const errorId = useId();

  // 文本和当前取值对不上时先看它解析出来还是不是同一个值：是的话说明这段文本
  // 就是用户自己输入的（比如多留了一个空行），保留原文；不是则说明取值被别处
  // 改过（切了继承来源、恢复默认、删除字段），把输入框拉回真实取值。
  if (draft !== formatted) {
    const parsed = parse(draft);
    if (!parsed.ok || format(parsed.value) !== formatted) {
      setDraft(formatted);
      setError(null);
    }
  }

  const commit = (text: string) => {
    const result = parse(text);
    if (result.ok) {
      setError(null);
      onCommit(result.value);
      return;
    }
    setError(result.error);
  };

  const handleChange = (text: string) => {
    setDraft(text);
    commit(text);
  };

  const fieldClassName = [
    monospace ? styles.mono : '',
    resizable ? '' : styles.fixedSize,
    error ? styles.invalid : '',
  ]
    .filter(Boolean)
    .join(' ');
  const shared = {
    className: singleLine ? ['input', fieldClassName].filter(Boolean).join(' ') : fieldClassName,
    value: draft,
    onChange: (event: { target: { value: string } }) => handleChange(event.target.value),
    disabled,
    'aria-label': ariaLabel,
    'aria-invalid': Boolean(error),
    'aria-describedby': error ? errorId : undefined,
    placeholder,
  };

  return (
    <div className={styles.root}>
      {singleLine ? (
        <input {...shared} />
      ) : (
        <textarea {...shared} rows={rows} spellCheck={monospace ? false : undefined} />
      )}
      {error ? (
        <div className={styles.error} id={errorId}>
          {describeDraftError(error, t)}
        </div>
      ) : null}
    </div>
  );
}
