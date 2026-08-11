import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import { resolve } from "path";

// The dashboard SPA builds into ../static (the go:embed'd dir the Go binary
// serves). Assets are hashed and emitted under static/app/, plus an index.html
// the Go server hands out for every client-routed page. This mirrors the
// editor-src precedent: the built output is committed and served via go:embed,
// so install.sh + `dashboard stop` deploys it with no separate asset pipeline.
//
// base "/static/app/" so hashed asset URLs resolve under the Go static route.
export default defineConfig({
  plugins: [react()],
  base: "/static/app/",
  build: {
    outDir: resolve(__dirname, "../static/app"),
    emptyOutDir: true,
    // Emit a manifest so the Go server can resolve the current hashed entry
    // file without hardcoding a hash (it reads manifest.json at render time).
    manifest: true,
    rollupOptions: {
      input: resolve(__dirname, "index.html"),
    },
  },
  // Dev server proxies API + WS to a locally running `corral dashboard`
  // (default port 7777) so `npm run dev` gives HMR against the real backend.
  server: {
    proxy: {
      "/p": { target: "http://127.0.0.1:7777", ws: true, changeOrigin: true },
      "/status": "http://127.0.0.1:7777",
      "/global": { target: "http://127.0.0.1:7777", ws: true, changeOrigin: true },
      "/repos": "http://127.0.0.1:7777",
      "/gh": { target: "http://127.0.0.1:7777", ws: true, changeOrigin: true },
      "/projects": "http://127.0.0.1:7777",
      "/healthz": "http://127.0.0.1:7777",
    },
  },
});
