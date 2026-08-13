// Loader + types for the committed CodeMirror bundle (webui/static/codemirror.bundle.js),
// which exposes a global CorralEditor (IIFE, global-name build). Rather than
// re-bundle CodeMirror into the React app, we reuse the already-built,
// go:embed'd bundle: load it once on demand and call through the typed surface
// below. This keeps the React bundle small and the editor behavior identical to
// the vanilla dashboard.

export interface EditorHandle {
  getDoc(): string;
  setDoc?(doc: string): void;
  destroy(): void;
  view?: unknown;
}

export interface DiffHandle {
  destroy(): void;
  // The underlying CodeMirror EditorView (modified/"after" side is the doc).
  // Typed loosely to avoid pulling CM types into the app bundle; used for
  // line-position math (posAtCoords/lineBlockAt) when attaching line comments.
  view?: DiffEditorView;
}

// Minimal shape of the CodeMirror EditorView methods the app uses for line
// comments. Not the full CM type — just what BlockDiff needs.
export interface DiffEditorView {
  dom: HTMLElement;
  posAtCoords(coords: { x: number; y: number }): number | null;
  state: { doc: { lineAt(pos: number): { number: number }; lines: number } };
  coordsAtPos(pos: number): { top: number; bottom: number; left: number; right: number } | null;
}

export interface CorralEditorAPI {
  createEditor(opts: {
    parent: HTMLElement;
    doc: string;
    filename: string;
    onChange?: () => void;
  }): EditorHandle;
  createDiff(opts: {
    parent: HTMLElement;
    original: string;
    modified: string;
    filename: string;
  }): DiffHandle;
  scrollToLineEffect?: (pos: number) => unknown;
}

declare global {
  interface Window {
    CorralEditor?: CorralEditorAPI;
  }
}

let loadPromise: Promise<CorralEditorAPI> | null = null;

// loadEditor injects the committed bundle <script> once and resolves with the
// global API. Subsequent calls return the same promise.
export function loadEditor(): Promise<CorralEditorAPI> {
  if (window.CorralEditor) return Promise.resolve(window.CorralEditor);
  if (loadPromise) return loadPromise;
  loadPromise = new Promise<CorralEditorAPI>((resolve, reject) => {
    const s = document.createElement("script");
    s.src = "/static/codemirror.bundle.js";
    s.onload = () => {
      if (window.CorralEditor) resolve(window.CorralEditor);
      else reject(new Error("codemirror bundle loaded but CorralEditor is missing"));
    };
    s.onerror = () => reject(new Error("failed to load codemirror bundle"));
    document.head.appendChild(s);
  });
  return loadPromise;
}
