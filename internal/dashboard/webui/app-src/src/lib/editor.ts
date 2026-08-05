// Loader + types for the committed CodeMirror bundle (webui/static/codemirror.bundle.js),
// which exposes a global SandclaudeEditor (IIFE, global-name build). Rather than
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
}

export interface SandclaudeEditorAPI {
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
    SandclaudeEditor?: SandclaudeEditorAPI;
  }
}

let loadPromise: Promise<SandclaudeEditorAPI> | null = null;

// loadEditor injects the committed bundle <script> once and resolves with the
// global API. Subsequent calls return the same promise.
export function loadEditor(): Promise<SandclaudeEditorAPI> {
  if (window.SandclaudeEditor) return Promise.resolve(window.SandclaudeEditor);
  if (loadPromise) return loadPromise;
  loadPromise = new Promise<SandclaudeEditorAPI>((resolve, reject) => {
    const s = document.createElement("script");
    s.src = "/static/codemirror.bundle.js";
    s.onload = () => {
      if (window.SandclaudeEditor) resolve(window.SandclaudeEditor);
      else reject(new Error("codemirror bundle loaded but SandclaudeEditor is missing"));
    };
    s.onerror = () => reject(new Error("failed to load codemirror bundle"));
    document.head.appendChild(s);
  });
  return loadPromise;
}
