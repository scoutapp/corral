// editor.js — entry point for the corral CodeMirror 6 bundle.
//
// This is bundled by esbuild into ../static/codemirror.bundle.js as a single
// self-contained IIFE that exposes its exports on `window.CorralEditor`
// (via esbuild's --global-name=CorralEditor). The dashboard frontend is
// plain vanilla JS with NO build step and a strict CSP, so everything must be
// inlined into that one file.
//
// Public API (what dashboard files.js should call):
//
//   window.CorralEditor.createEditor(opts) -> handle
//     opts:
//       parent   : HTMLElement   (required) container to mount the editor in
//       doc      : string        (optional, default "") initial document text
//       filename : string        (optional) used to pick a language by extension
//       readOnly : boolean       (optional, default false)
//       onChange : (doc:string)=>void (optional) called when the doc changes
//     returns handle:
//       getDoc()        -> string   current document text
//       setDoc(s)       -> void     replace the whole document
//       destroy()       -> void     tear down the editor / free the DOM
//       setReadOnly(b)  -> void     toggle read-only at runtime
//       view            -> EditorView (escape hatch; the raw CM6 view)
//
//   window.CorralEditor.createDiff(opts) -> handle
//     Unified merge (diff) view — read-only display of old vs new.
//     opts:
//       parent   : HTMLElement   (required)
//       original : string        (required) the "before" / base document
//       modified : string        (required) the "after" document shown w/ diff
//       filename : string        (optional) language by extension
//     returns handle:
//       destroy() -> void
//       view      -> EditorView
//
//   window.CorralEditor.languageForFilename(filename) -> string|null
//     Introspection helper: the language key that would be picked, or null.
//
//   window.CorralEditor.version -> string

import { basicSetup, EditorView } from "codemirror";
import { EditorState, Compartment } from "@codemirror/state";
import { keymap } from "@codemirror/view";
import { indentWithTab } from "@codemirror/commands";
import { vscodeDark } from "./vscode-dark.js";
import { unifiedMergeView } from "@codemirror/merge";

// Language packages. Each factory returns a CM6 extension.
import { javascript } from "@codemirror/lang-javascript";
import { python } from "@codemirror/lang-python";
import { json } from "@codemirror/lang-json";
import { markdown } from "@codemirror/lang-markdown";
import { html } from "@codemirror/lang-html";
import { css } from "@codemirror/lang-css";
import { yaml } from "@codemirror/lang-yaml";
import { elixir } from "codemirror-lang-elixir";

// Legacy stream-parser modes (no dedicated CM6 grammar). Wrapped with
// StreamLanguage.define() to turn a StreamParser into a language extension.
import { StreamLanguage } from "@codemirror/language";
import { ruby } from "@codemirror/legacy-modes/mode/ruby";
import { lua } from "@codemirror/legacy-modes/mode/lua";
import { shell } from "@codemirror/legacy-modes/mode/shell";

// Map a normalized language key -> a function producing the language extension.
// Filenames whose extension isn't here get no language extension (plain text),
// which is fine — basicSetup still gives line numbers, selection, undo, etc.
const LANGUAGES = {
  javascript: () => javascript({ jsx: true }),
  typescript: () => javascript({ jsx: true, typescript: true }),
  python: () => python(),
  json: () => json(),
  markdown: () => markdown(),
  html: () => html(),
  css: () => css(),
  yaml: () => yaml(),
  elixir: () => elixir(),
  ruby: () => StreamLanguage.define(ruby),
  lua: () => StreamLanguage.define(lua),
  bash: () => StreamLanguage.define(shell),
  shell: () => StreamLanguage.define(shell),
};

// Extension -> language key. Anything not listed falls through to plain text.
const EXT_TO_LANG = {
  js: "javascript",
  jsx: "javascript",
  mjs: "javascript",
  cjs: "javascript",
  ts: "typescript",
  tsx: "typescript",
  py: "python",
  pyw: "python",
  json: "json",
  jsonc: "json",
  md: "markdown",
  markdown: "markdown",
  html: "html",
  htm: "html",
  css: "css",
  yml: "yaml",
  yaml: "yaml",
  rb: "ruby",
  ex: "elixir",
  exs: "elixir",
  heex: "elixir",
  lua: "lua",
  sh: "shell",
  bash: "shell",
  // Note: no dedicated lang-go in this bundle; .go renders as plain text with
  // full editing niceties. Shell (.sh/.bash) uses the legacy stream mode above.
};

function extOf(filename) {
  if (!filename || typeof filename !== "string") return "";
  const base = filename.split(/[\\/]/).pop() || "";
  const dot = base.lastIndexOf(".");
  if (dot <= 0) return ""; // no ext, or dotfile like ".bashrc"
  return base.slice(dot + 1).toLowerCase();
}

function languageForFilename(filename) {
  return EXT_TO_LANG[extOf(filename)] || null;
}

// languageExtension resolves a language extension from an explicit language key
// (opts.language, e.g. "bash") if given, else from the filename's extension.
// Unknown/absent → [] (plain text with full editing niceties).
function languageExtension(filename, language) {
  const key = (language && LANGUAGES[language] && language) || languageForFilename(filename);
  if (key && LANGUAGES[key]) {
    try {
      return LANGUAGES[key]();
    } catch (e) {
      // Never let a language factory blow up editor creation.
      return [];
    }
  }
  return [];
}

// createEditor — the primary editing surface.
function createEditor(opts) {
  opts = opts || {};
  const parent = opts.parent;
  if (!parent) throw new Error("CorralEditor.createEditor: opts.parent is required");

  const doc = typeof opts.doc === "string" ? opts.doc : "";
  const onChange = typeof opts.onChange === "function" ? opts.onChange : null;

  // Compartments let us reconfigure read-only at runtime without rebuilding.
  const readOnlyComp = new Compartment();

  const extensions = [
    basicSetup,
    keymap.of([indentWithTab]),
    vscodeDark,
    languageExtension(opts.filename, opts.language),
    readOnlyComp.of(EditorState.readOnly.of(!!opts.readOnly)),
  ];

  if (onChange) {
    extensions.push(
      EditorView.updateListener.of((update) => {
        if (update.docChanged) {
          onChange(update.state.doc.toString());
        }
      })
    );
  }

  const view = new EditorView({
    state: EditorState.create({ doc, extensions }),
    parent,
  });

  return {
    getDoc() {
      return view.state.doc.toString();
    },
    setDoc(s) {
      const next = typeof s === "string" ? s : "";
      view.dispatch({
        changes: { from: 0, to: view.state.doc.length, insert: next },
      });
    },
    setReadOnly(b) {
      view.dispatch({
        effects: readOnlyComp.reconfigure(EditorState.readOnly.of(!!b)),
      });
    },
    destroy() {
      view.destroy();
    },
    view,
  };
}

// createDiff — a read-only unified diff view (base vs modified).
function createDiff(opts) {
  opts = opts || {};
  const parent = opts.parent;
  if (!parent) throw new Error("CorralEditor.createDiff: opts.parent is required");

  const original = typeof opts.original === "string" ? opts.original : "";
  const modified = typeof opts.modified === "string" ? opts.modified : "";

  const view = new EditorView({
    state: EditorState.create({
      doc: modified,
      extensions: [
        basicSetup,
        vscodeDark,
        languageExtension(opts.filename),
        EditorState.readOnly.of(true),
        unifiedMergeView({ original, mergeControls: false }),
      ],
    }),
    parent,
  });

  return {
    destroy() {
      view.destroy();
    },
    view,
  };
}

export { createEditor, createDiff, languageForFilename };
export const version = "1.0.0";
