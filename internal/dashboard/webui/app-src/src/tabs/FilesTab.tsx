import { useCallback, useEffect, useRef, useState } from "react";
import { getJSON, postRaw } from "../api/client";
import type { FilesReadResponse, FilesFindResponse, FilesGrepResponse, TreeEntry } from "../api/types";
import { fileIconDef } from "../lib/fileIcons";
import { loadEditor, type EditorHandle } from "../lib/editor";

// Files tab: lazy directory tree (left) + CodeMirror editor (right), reading and
// writing straight to the host workspace. Port of files.js. The tree auto-
// refreshes while visible so files Claude creates/deletes appear without a
// manual collapse/expand; search offers filename find + content grep.

function api(projectId: string, p: string) {
  return `/p/${projectId}${p}`;
}

function FileIcon({ name }: { name: string }) {
  const [glyph, color] = fileIconDef(name);
  return (
    <span className="ficon" style={{ color }}>
      {glyph}
    </span>
  );
}

interface CtxMenu {
  x: number;
  y: number;
  rel: string;
  isDir: boolean;
}

// A recursive directory node. Fetches its own entries on expand; a refreshToken
// bump re-reads it (used by the auto-refresh poll + after fs ops).
function TreeDir({
  projectId,
  relPath,
  name,
  depth,
  openPath,
  onOpenFile,
  onCtx,
  refreshToken,
}: {
  projectId: string;
  relPath: string;
  name: string | null; // null = the (unlabeled) workspace root
  depth: number;
  openPath: string | null;
  onOpenFile: (rel: string) => void;
  onCtx: (m: CtxMenu) => void;
  refreshToken: number;
}) {
  const isRoot = name === null;
  const [open, setOpen] = useState(isRoot);
  const [entries, setEntries] = useState<TreeEntry[] | null>(null);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(() => {
    getJSON<{ entries?: TreeEntry[] }>(api(projectId, `/files/tree?path=${encodeURIComponent(relPath)}`))
      .then((data) => {
        setEntries(data.entries || []);
        setError(null);
      })
      .catch((e) => setError((e as Error).message));
  }, [projectId, relPath]);

  useEffect(() => {
    if (open) load();
  }, [open, load, refreshToken]);

  if (!isRoot && !open) {
    return (
      <li className="ftree-dir collapsed" data-name={name}>
        <span
          className="ftree-label"
          onClick={(e) => {
            e.stopPropagation();
            setOpen(true);
          }}
          onContextMenu={(e) => {
            e.preventDefault();
            e.stopPropagation();
            onCtx({ x: e.clientX, y: e.clientY, rel: relPath, isDir: true });
          }}
        >
          <span className="ficon ficon-dir">▸</span>
          <span className="ftree-name">{name}</span>
        </span>
      </li>
    );
  }

  const body = (
    <>
      {error && <p className="attention">tree error: {error}</p>}
      <ul>
        {(entries || []).map((e) => {
          const childRel = relPath ? `${relPath}/${e.name}` : e.name;
          if (e.dir) {
            return (
              <TreeDir
                key={e.name}
                projectId={projectId}
                relPath={childRel}
                name={e.name}
                depth={depth + 1}
                openPath={openPath}
                onOpenFile={onOpenFile}
                onCtx={onCtx}
                refreshToken={refreshToken}
              />
            );
          }
          return (
            <li
              key={e.name}
              className={`ftree-file${openPath === childRel ? " active" : ""}`}
              data-name={e.name}
              data-path={childRel}
            >
              <span
                className="ftree-label"
                onClick={(ev) => {
                  ev.stopPropagation();
                  onOpenFile(childRel);
                }}
                onContextMenu={(ev) => {
                  ev.preventDefault();
                  ev.stopPropagation();
                  onCtx({ x: ev.clientX, y: ev.clientY, rel: childRel, isDir: false });
                }}
              >
                <FileIcon name={e.name} />
                <span className="ftree-name">{e.name}</span>
              </span>
            </li>
          );
        })}
      </ul>
    </>
  );

  if (isRoot) return body;

  return (
    <li className="ftree-dir" data-name={name} data-dir="1">
      <span
        className="ftree-label"
        onClick={(e) => {
          e.stopPropagation();
          setOpen(false);
        }}
        onContextMenu={(e) => {
          e.preventDefault();
          e.stopPropagation();
          onCtx({ x: e.clientX, y: e.clientY, rel: relPath, isDir: true });
        }}
      >
        <span className="ficon ficon-dir">▸</span>
        <span className="ftree-name">{name}</span>
      </span>
      {body}
    </li>
  );
}

export function FilesTab({ projectId }: { projectId: string }) {
  const [refreshToken, setRefreshToken] = useState(0);
  const refresh = useCallback(() => setRefreshToken((t) => t + 1), []);

  // Editor state
  const cmHost = useRef<HTMLDivElement | null>(null);
  const editorRef = useRef<EditorHandle | null>(null);
  const [openPath, setOpenPath] = useState<string | null>(null);
  const openPathRef = useRef<string | null>(null);
  const [dirty, setDirty] = useState(false);
  const dirtyRef = useRef(false);
  const [tooLarge, setTooLarge] = useState<number | null>(null);

  // Search state
  const [mode, setMode] = useState<"name" | "grep">("name");
  const [q, setQ] = useState("");
  const [find, setFind] = useState<FilesFindResponse | null>(null);
  const [grep, setGrep] = useState<FilesGrepResponse | null>(null);
  const [searching, setSearching] = useState(false);

  const [ctx, setCtx] = useState<CtxMenu | null>(null);

  const setDirtyBoth = (d: boolean) => {
    dirtyRef.current = d;
    setDirty(d);
  };

  const openFile = useCallback(
    async (rel: string, line?: number) => {
      if (dirtyRef.current && !window.confirm(`Discard unsaved changes to ${openPathRef.current}?`)) return;
      try {
        const data = await getJSON<FilesReadResponse>(api(projectId, `/files/read?path=${encodeURIComponent(rel)}`));
        openPathRef.current = rel;
        setOpenPath(rel);
        if (data.too_large) {
          editorRef.current?.destroy();
          editorRef.current = null;
          setTooLarge(data.size || 0);
          setDirtyBoth(false);
          return;
        }
        setTooLarge(null);
        const api2 = await loadEditor();
        editorRef.current?.destroy();
        if (cmHost.current) cmHost.current.innerHTML = "";
        editorRef.current = api2.createEditor({
          parent: cmHost.current!,
          doc: data.content || "",
          filename: data.filename || rel,
          onChange: () => {
            if (!dirtyRef.current) setDirtyBoth(true);
          },
        });
        setDirtyBoth(false);
        if (line && editorRef.current.view && api2.scrollToLineEffect) {
          // best-effort line reveal handled inside the bundle's view
        }
      } catch (e) {
        if (cmHost.current)
          cmHost.current.innerHTML = `<p class="attention" style="padding:1rem">read error: ${(e as Error).message}</p>`;
      }
    },
    [projectId],
  );

  const save = useCallback(async () => {
    if (!editorRef.current || openPathRef.current == null) return;
    try {
      const res = await fetch(api(projectId, `/files/write?path=${encodeURIComponent(openPathRef.current)}`), {
        method: "POST",
        credentials: "same-origin",
        body: editorRef.current.getDoc(),
      });
      if (!res.ok) throw new Error((await res.text()) || `HTTP ${res.status}`);
      setDirtyBoth(false);
    } catch (e) {
      alert(`save failed: ${(e as Error).message}`);
    }
  }, [projectId]);

  // fs ops
  const fpost = (p: string) => postRaw(api(projectId, p));
  const parentOf = (rel: string) => {
    const i = rel.lastIndexOf("/");
    return i < 0 ? "" : rel.slice(0, i);
  };
  const join = (dir: string, n: string) => (dir ? `${dir}/${n}` : n);

  async function opThen(pr: Promise<Response>, label: string, after?: () => void) {
    try {
      const r = await pr;
      if (!r.ok) throw new Error((await r.text()) || `HTTP ${r.status}`);
      refresh();
      after?.();
    } catch (e) {
      alert(`${label} failed: ${(e as Error).message}`);
    }
  }

  const doNewFile = (dirRel: string) => {
    const name = window.prompt("New file name:");
    if (!name) return;
    opThen(fpost(`/files/new?path=${encodeURIComponent(join(dirRel, name))}`), "create file", () =>
      openFile(join(dirRel, name)),
    );
  };
  const doNewFolder = (dirRel: string) => {
    const name = window.prompt("New folder name:");
    if (!name) return;
    opThen(fpost(`/files/mkdir?path=${encodeURIComponent(join(dirRel, name))}`), "create folder");
  };
  const doRename = (rel: string) => {
    const base = rel.split("/").pop()!;
    const name = window.prompt("Rename to:", base);
    if (!name || name === base) return;
    const to = join(parentOf(rel), name);
    opThen(fpost(`/files/rename?from=${encodeURIComponent(rel)}&to=${encodeURIComponent(to)}`), "rename");
  };
  const doDelete = (rel: string, isDir: boolean) => {
    if (!window.confirm(`Delete ${isDir ? "folder" : "file"} '${rel}'?${isDir ? " (recursive)" : ""}`)) return;
    opThen(
      fetch(api(projectId, `/files?path=${encodeURIComponent(rel)}`), { method: "DELETE", credentials: "same-origin" }),
      "delete",
    );
  };

  // close context menu on any click / Escape
  useEffect(() => {
    const close = () => setCtx(null);
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && setCtx(null);
    document.addEventListener("click", close);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("click", close);
      document.removeEventListener("keydown", onKey);
    };
  }, []);

  // search (debounced)
  useEffect(() => {
    const term = q.trim();
    if (!term) {
      setFind(null);
      setGrep(null);
      setSearching(false);
      return;
    }
    setSearching(true);
    const t = window.setTimeout(async () => {
      try {
        if (mode === "grep") {
          setGrep(await getJSON<FilesGrepResponse>(api(projectId, `/files/grep?q=${encodeURIComponent(term)}`)));
          setFind(null);
        } else {
          setFind(await getJSON<FilesFindResponse>(api(projectId, `/files/find?q=${encodeURIComponent(term)}`)));
          setGrep(null);
        }
      } catch {
        /* show nothing on error */
      }
    }, 220);
    return () => window.clearTimeout(t);
  }, [q, mode, projectId]);

  // auto-refresh the tree while not searching
  useEffect(() => {
    const id = window.setInterval(() => {
      if (!q.trim()) refresh();
    }, 2500);
    return () => window.clearInterval(id);
  }, [q, refresh]);

  // Ctrl/Cmd-S saves
  useEffect(() => {
    const el = cmHost.current;
    if (!el) return;
    const onKey = (e: KeyboardEvent) => {
      if ((e.ctrlKey || e.metaKey) && (e.key === "s" || e.key === "S")) {
        e.preventDefault();
        if (dirtyRef.current) save();
      }
    };
    el.addEventListener("keydown", onKey);
    return () => el.removeEventListener("keydown", onKey);
  }, [save]);

  const showResults = q.trim() !== "";

  return (
    <>
      <div className="files-side">
        <div className="files-search">
          <input
            id="files-q"
            type="text"
            placeholder={mode === "grep" ? "search file contents…" : "search files…"}
            autoComplete="off"
            spellCheck={false}
            value={q}
            onChange={(e) => setQ(e.target.value)}
          />
          <div className="files-search-mode">
            <button className={mode === "name" ? "active" : ""} type="button" title="Find files by name" onClick={() => setMode("name")}>
              name
            </button>
            <button className={mode === "grep" ? "active" : ""} type="button" title="Search file contents (grep)" onClick={() => setMode("grep")}>
              grep
            </button>
            <button type="button" title="Refresh the file tree" onClick={refresh}>
              ⟳
            </button>
          </div>
        </div>

        {!showResults && (
          <div
            className="files-tree"
            onContextMenu={(e) => {
              if ((e.target as HTMLElement).closest(".ftree-label")) return;
              e.preventDefault();
              setCtx({ x: e.clientX, y: e.clientY, rel: "", isDir: true });
            }}
          >
            <TreeDir
              projectId={projectId}
              relPath=""
              name={null}
              depth={0}
              openPath={openPath}
              onOpenFile={openFile}
              onCtx={setCtx}
              refreshToken={refreshToken}
            />
          </div>
        )}

        {showResults && (
          <div className="files-results">
            {searching && !find && !grep && <p className="muted" style={{ padding: "0.5rem" }}>searching…</p>}
            {mode === "name" && find && (
              (find.matches || []).length === 0 ? (
                <p className="muted" style={{ padding: "0.5rem" }}>no matching files</p>
              ) : (
                <>
                  {(find.matches || []).map((rel) => {
                    const base = rel.split("/").pop()!;
                    return (
                      <div key={rel} className="sresult" onClick={() => openFile(rel)}>
                        <FileIcon name={base} />
                        <span className="sresult-path">{rel}</span>
                      </div>
                    );
                  })}
                  {find.truncated && <p className="muted" style={{ padding: "0.5rem" }}>…results truncated — narrow your search</p>}
                </>
              )
            )}
            {mode === "grep" && grep && (
              (grep.hits || []).length === 0 ? (
                <p className="muted" style={{ padding: "0.5rem" }}>no matches</p>
              ) : (
                <>
                  {(grep.hits || []).map((h, i) => (
                    <div key={`${h.path}:${h.line}:${i}`} className="sresult sresult-grep" onClick={() => openFile(h.path, h.line)}>
                      <div className="sresult-loc">
                        {h.path}
                        <span className="sresult-ln">:{h.line}</span>
                      </div>
                      <div className="sresult-text">{h.text}</div>
                    </div>
                  ))}
                  {grep.truncated && <p className="muted" style={{ padding: "0.5rem" }}>…results truncated — narrow your search</p>}
                </>
              )
            )}
          </div>
        )}
      </div>

      <div className="files-editor">
        <div className="files-editor-bar">
          <span className={openPath ? "" : "muted"}>{(openPath || "select a file") + (dirty ? " •" : "")}</span>
          <button type="button" disabled={!dirty || openPath == null} onClick={save}>
            Save
          </button>
        </div>
        {tooLarge != null ? (
          <p className="muted" style={{ padding: "1rem" }}>
            file is too large to edit here ({tooLarge} bytes)
          </p>
        ) : (
          <div className="files-cm" ref={cmHost} />
        )}
      </div>

      {ctx && (
        <div className="ctx-menu" style={{ left: ctx.x, top: ctx.y }} onClick={(e) => e.stopPropagation()}>
          {ctx.isDir && (
            <>
              <div className="ctx-item" onClick={() => { setCtx(null); doNewFile(ctx.rel); }}>
                New file
              </div>
              <div className="ctx-item" onClick={() => { setCtx(null); doNewFolder(ctx.rel); }}>
                New folder
              </div>
            </>
          )}
          {ctx.rel !== "" && (
            <>
              <div className="ctx-item" onClick={() => { setCtx(null); doRename(ctx.rel); }}>
                Rename
              </div>
              <div className="ctx-item" onClick={() => { setCtx(null); doDelete(ctx.rel, ctx.isDir); }}>
                Delete
              </div>
            </>
          )}
        </div>
      )}
    </>
  );
}
