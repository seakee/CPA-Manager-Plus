import { useMemo, type Ref } from 'react';
import CodeMirror, { type ReactCodeMirrorRef } from '@uiw/react-codemirror';
import { json } from '@codemirror/lang-json';
import { search, searchKeymap, highlightSelectionMatches } from '@codemirror/search';
import { keymap } from '@codemirror/view';

type OverrideJsonEditorProps = {
  value: string;
  onChange?: (value: string) => void;
  editorRef?: Ref<ReactCodeMirrorRef>;
  theme: 'light' | 'dark';
  editable: boolean;
  placeholder?: string;
  height?: string;
};

/** JSON Merge Patch 编辑器：与配置面板共用同一套 CodeMirror 基础配置。 */
export default function OverrideJsonEditor({
  value,
  onChange,
  editorRef,
  theme,
  editable,
  placeholder,
  height = '100%',
}: OverrideJsonEditorProps) {
  const extensions = useMemo(
    () => [json(), search(), highlightSelectionMatches(), keymap.of(searchKeymap)],
    []
  );

  return (
    <CodeMirror
      ref={editorRef}
      value={value}
      onChange={onChange}
      extensions={extensions}
      theme={theme}
      editable={editable}
      placeholder={placeholder}
      height={height}
      style={{ height }}
      basicSetup={{
        lineNumbers: true,
        highlightActiveLineGutter: true,
        highlightActiveLine: true,
        foldGutter: true,
        dropCursor: true,
        allowMultipleSelections: true,
        indentOnInput: true,
        bracketMatching: true,
        closeBrackets: true,
        autocompletion: false,
        rectangularSelection: true,
        crosshairCursor: false,
        highlightSelectionMatches: true,
        closeBracketsKeymap: true,
        searchKeymap: true,
        foldKeymap: true,
        completionKeymap: false,
        lintKeymap: true,
      }}
    />
  );
}
