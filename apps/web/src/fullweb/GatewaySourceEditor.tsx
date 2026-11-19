import { useEffect, useImperativeHandle, useMemo, useRef, useState, type Ref } from 'react';
import CodeMirror, { ExternalChange, type ReactCodeMirrorRef } from '@uiw/react-codemirror';
import { yaml } from '@codemirror/lang-yaml';
import { highlightSelectionMatches, search, searchKeymap } from '@codemirror/search';
import { keymap } from '@codemirror/view';

export interface GatewaySourceEditorHandle {
  search: (query: string, direction?: 'next' | 'prev') => { current: number; total: number };
}

export interface GatewaySourceEditorProps {
  value: string;
  onChange: (value: string) => void;
  editable: boolean;
  placeholder?: string;
  syncKey?: number;
  editorRef?: Ref<GatewaySourceEditorHandle>;
}

function readDocumentTheme(): 'light' | 'dark' {
  if (typeof document === 'undefined') return 'light';
  return document.documentElement.getAttribute('data-theme') === 'dark' ? 'dark' : 'light';
}

export default function GatewaySourceEditor({
  value,
  onChange,
  editable,
  placeholder = '',
  editorRef,
  syncKey = 0,
}: GatewaySourceEditorProps) {
  const cmRef = useRef<ReactCodeMirrorRef | null>(null);
  const lastSyncKeyRef = useRef(syncKey);
  const [theme, setTheme] = useState<'light' | 'dark'>(readDocumentTheme);

  useEffect(() => {
    if (typeof MutationObserver === 'undefined' || typeof document === 'undefined') return;
    const observer = new MutationObserver(() => {
      setTheme(readDocumentTheme());
    });
    observer.observe(document.documentElement, {
      attributes: true,
      attributeFilter: ['data-theme'],
    });
    return () => observer.disconnect();
  }, []);
  useEffect(() => {
    if (syncKey === lastSyncKeyRef.current) return;
    const view = cmRef.current?.view;
    if (!view) return;
    lastSyncKeyRef.current = syncKey;
    const current = view.state.doc.toString();
    if (current !== value) {
      view.dispatch({
        changes: { from: 0, to: current.length, insert: value },
        annotations: [ExternalChange.of(true)],
      });
    }
  }, [syncKey, value]);

  const extensions = useMemo(
    () => [yaml(), search(), highlightSelectionMatches(), keymap.of(searchKeymap)],
    []
  );

  useImperativeHandle(
    editorRef,
    () => ({
      search(query: string, direction: 'next' | 'prev' = 'next') {
        const view = cmRef.current?.view;
        if (!query || !view) return { current: 0, total: 0 };
        const doc = view.state.doc.toString().toLowerCase();
        const lowerQuery = query.toLowerCase();
        const matches: number[] = [];
        let pos = 0;
        while (pos < doc.length) {
          const idx = doc.indexOf(lowerQuery, pos);
          if (idx === -1) break;
          matches.push(idx);
          pos = idx + 1;
        }
        if (matches.length === 0) return { current: 0, total: 0 };

        const selection = view.state.selection.main;
        let currentIndex = 0;
        if (direction === 'next') {
          currentIndex = matches.findIndex((m) =>
            selection.empty ? m >= selection.from : m > selection.from
          );
          if (currentIndex === -1) currentIndex = 0;
        } else {
          const cursor = selection.from;
          for (let i = matches.length - 1; i >= 0; i--) {
            if (matches[i] < cursor) {
              currentIndex = i;
              break;
            }
            if (i === 0) currentIndex = matches.length - 1;
          }
        }
        const matchPos = matches[currentIndex];
        view.dispatch({
          selection: { anchor: matchPos, head: matchPos + query.length },
          scrollIntoView: true,
        });
        return { current: currentIndex + 1, total: matches.length };
      },
    }),
    []
  );

  return (
    <div className="gateway-source-codemirror" data-testid="gateway-source-codemirror">
      <CodeMirror
        ref={cmRef}
        value={value}
        onChange={onChange}
        extensions={extensions}
        theme={theme}
        editable={editable}
        placeholder={placeholder}
        height="100%"
        style={{ height: '100%' }}
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
    </div>
  );
}
