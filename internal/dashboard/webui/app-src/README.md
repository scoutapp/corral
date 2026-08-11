# corral dashboard (React + TypeScript)

The dashboard UI. A Vite + React 18 + TypeScript app that builds to a **committed**
bundle in `../static/app/`, which the Go binary serves via `go:embed`. Same model
as `../editor-src` (the CodeMirror bundle): the built output is committed and
reviewable, so `install.sh` + `go build` ship the UI with **no Node needed at
install time**.

## Develop

```sh
npm install
# Run the real dashboard so the API/WS endpoints exist, then:
corral dashboard            # serves on :7777 (default)
npm run dev                     # Vite dev server with HMR, proxying API/WS to :7777
```

Open the URL Vite prints. The dev server proxies `/status`, `/p`, `/global`,
`/repos`, `/gh` (incl. WebSockets) to the running dashboard on `:7777`.

## Build (required before committing UI changes)

```sh
npm run build                   # tsc -b && vite build -> ../static/app/
```

Then **commit the built output** under `../static/app/` along with your source
changes. To deploy: `install.sh` (rebuilds the host binary, embedding the bundle)
then `corral dashboard stop` (restart the daemon). No image rebuild.

## Layout

- `src/api/` — typed client (`client.ts`) + Go-contract types (`types.ts`)
- `src/pages/` — Projects, Project, Global, Repos section + modals
- `src/tabs/` — Files, Diff, Config, Mitm, Firewall
- `src/components/` — XtermPane, ChatPanel, SSHLoadModal, Toasts, Modal, Typeahead
- `src/lib/` — editor loader (reuses the committed codemirror bundle), chime,
  markdown, file icons, repo helpers
- `src/router.tsx` — dependency-free History-API router (`/`, `/global`, `/p/:id`)

Dependencies are pinned to exact versions on purpose (the bundle is trusted,
go:embed'd host-side code): update deliberately, rebuild, and review the bundle
diff.
