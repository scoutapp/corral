// A CodeMirror 6 theme approximating VS Code's "Dark Modern" / Dark+ palette,
// so the dashboard editor looks like the editor people already know rather than
// oneDark's bluer approximation. Two parts: the chrome (EditorView.theme) and
// the token colors (HighlightStyle via syntaxHighlighting).
import { EditorView } from "@codemirror/view";
import { HighlightStyle, syntaxHighlighting } from "@codemirror/language";
import { tags as t } from "@lezer/highlight";

// VS Code Dark Modern colors.
const bg = "#1e1e1e";
const fg = "#d4d4d4";
const caret = "#aeafad";
// Selection: VS Code's editor.selectionBackground. The selection layer sits
// BEHIND the text (CodeMirror's default), so the highlighted characters stay
// readable — do NOT raise its z-index (that paints the selection over the text).
// A slightly brighter-than-VS-Code blue keeps a small selection distinct from the
// active-line highlight without hiding the glyphs.
const selection = "#2e5aa0";
const selectionMatch = "#3a3d41";
const lineHighlight = "#2a2d2e";
const gutterBg = "#1e1e1e";
const gutterFg = "#858585";
const gutterActiveFg = "#c6c6c6";
const panelBg = "#252526";
const border = "#3c3c3c";

export const vscodeDarkTheme = EditorView.theme(
  {
    "&": { color: fg, backgroundColor: bg },
    ".cm-content": {
      caretColor: caret,
      fontFamily: "ui-monospace, SFMono-Regular, Menlo, Consolas, monospace",
    },
    ".cm-cursor, .cm-dropCursor": { borderLeftColor: caret },
    // Selection background — cover BOTH the focused and unfocused draw paths, plus
    // native ::selection. CodeMirror only applies the bright color under
    // `.cm-focused` by default; without the unfocused rule a selection that loses
    // focus (or is drawn before focus lands) falls back to a barely-visible gray.
    // The selection layer sits behind the text, so glyphs stay readable.
    ".cm-selectionBackground, .cm-content ::selection, .cm-line ::selection": {
      backgroundColor: selection + " !important",
    },
    "&.cm-focused .cm-selectionBackground": { backgroundColor: selection + " !important" },
    ".cm-selectionMatch": { backgroundColor: selectionMatch },
    ".cm-activeLine": { backgroundColor: lineHighlight },
    ".cm-activeLineGutter": { backgroundColor: lineHighlight, color: gutterActiveFg },
    ".cm-gutters": { backgroundColor: gutterBg, color: gutterFg, border: "none" },
    ".cm-lineNumbers .cm-gutterElement": { color: gutterFg },
    ".cm-foldPlaceholder": { backgroundColor: "transparent", border: "none", color: "#ddd" },
    ".cm-panels": { backgroundColor: panelBg, color: fg },
    ".cm-panels.cm-panels-top": { borderBottom: "1px solid " + border },
    ".cm-panels.cm-panels-bottom": { borderTop: "1px solid " + border },
    ".cm-searchMatch": { backgroundColor: "#613214", outline: "1px solid #7a4419" },
    ".cm-searchMatch.cm-searchMatch-selected": { backgroundColor: "#9e6a03" },
    ".cm-tooltip": { border: "1px solid " + border, backgroundColor: panelBg },
    ".cm-tooltip-autocomplete > ul > li[aria-selected]": {
      backgroundColor: "#04395e",
      color: fg,
    },
    ".cm-matchingBracket, .cm-nonmatchingBracket": {
      backgroundColor: "#0a3a5c",
      outline: "1px solid #3c7ab0",
    },
  },
  { dark: true }
);

// Token colors — VS Code Dark+ semantics.
export const vscodeHighlightStyle = HighlightStyle.define([
  { tag: [t.keyword, t.moduleKeyword, t.operatorKeyword], color: "#569cd6" },
  { tag: [t.controlKeyword], color: "#c586c0" }, // if/for/return etc.
  { tag: [t.name, t.deleted, t.character, t.macroName], color: fg },
  { tag: [t.propertyName], color: "#9cdcfe" },
  { tag: [t.variableName], color: "#9cdcfe" },
  { tag: [t.function(t.variableName), t.function(t.propertyName)], color: "#dcdcaa" },
  { tag: [t.labelName], color: "#dcdcaa" },
  { tag: [t.className, t.typeName, t.namespace], color: "#4ec9b0" },
  { tag: [t.tagName], color: "#569cd6" },
  { tag: [t.attributeName], color: "#9cdcfe" },
  { tag: [t.number, t.integer, t.float, t.bool, t.null], color: "#b5cea8" },
  { tag: [t.string, t.special(t.string), t.regexp], color: "#ce9178" },
  { tag: [t.escape], color: "#d7ba7d" },
  { tag: [t.comment, t.lineComment, t.blockComment, t.docComment], color: "#6a9955", fontStyle: "italic" },
  { tag: [t.meta, t.processingInstruction], color: "#569cd6" },
  { tag: [t.definition(t.propertyName)], color: "#9cdcfe" },
  { tag: [t.heading], color: "#569cd6", fontWeight: "bold" },
  { tag: [t.link, t.url], color: "#ce9178", textDecoration: "underline" },
  { tag: [t.emphasis], fontStyle: "italic" },
  { tag: [t.strong], fontWeight: "bold" },
  { tag: [t.invalid], color: "#f44747" },
  { tag: [t.punctuation, t.separator, t.bracket, t.brace, t.paren], color: fg },
]);

// The bundle exports this single extension to apply the full VS Code look.
export const vscodeDark = [vscodeDarkTheme, syntaxHighlighting(vscodeHighlightStyle)];
