// CodeMirror setup for shell scripts and YAML; imported lazily by CodeEditor.vue (its own chunk).
import { EditorView, keymap, lineNumbers, drawSelection, placeholder } from '@codemirror/view';
import { EditorState } from '@codemirror/state';
import { defaultKeymap, history, historyKeymap, indentWithTab } from '@codemirror/commands';
import { StreamLanguage, syntaxHighlighting, HighlightStyle, bracketMatching, indentUnit } from '@codemirror/language';
import { shell } from '@codemirror/legacy-modes/mode/shell';
import { yaml } from '@codemirror/legacy-modes/mode/yaml';
import { tags } from '@lezer/highlight';

export { EditorView, EditorState };

const style = HighlightStyle.define([
  { tag: [tags.keyword, tags.standard(tags.variableName)], color: 'var(--accent)' },
  { tag: [tags.string, tags.quote], color: 'var(--ok)' },
  { tag: [tags.definition(tags.variableName), tags.atom], color: 'var(--warn)' },
  { tag: [tags.number, tags.attributeName], color: 'var(--s1)' },
  { tag: [tags.comment, tags.meta], color: 'var(--muted)', fontStyle: 'italic' },
  { tag: tags.operator, color: 'var(--ink-2)' },
]);

export const extensions = ({ label, placeholder: ph, readOnly, lang }, change) => [
  lineNumbers(), history(), drawSelection(), bracketMatching(), indentUnit.of('  '),
  StreamLanguage.define(lang === 'yaml' ? yaml : shell), syntaxHighlighting(style),
  // Tab indents; Esc then Tab still moves focus on (CodeMirror's built-in escape hatch).
  keymap.of([...defaultKeymap, ...historyKeymap, indentWithTab]),
  EditorView.contentAttributes.of({ 'aria-label': label || 'Code', spellcheck: 'false' }),
  EditorView.updateListener.of((u) => { if (u.docChanged) change(u.state.doc.toString()); }),
  ph ? placeholder(ph) : [],
  readOnly ? EditorState.readOnly.of(true) : [],
];
