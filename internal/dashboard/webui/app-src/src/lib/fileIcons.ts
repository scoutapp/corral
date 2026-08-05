// File-type icon: a small colored monogram keyed by extension. Inline (no icon
// font — the dashboard's CSP blocks CDNs). Ported from files.js. The glyph is a
// short label; color follows the language's conventional accent.
const ICONS: Record<string, [string, string]> = {
  js: ["JS", "#f0db4f"], jsx: ["JS", "#f0db4f"], mjs: ["JS", "#f0db4f"], cjs: ["JS", "#f0db4f"],
  ts: ["TS", "#3178c6"], tsx: ["TS", "#3178c6"],
  go: ["GO", "#00add8"], py: ["PY", "#3572a5"], rb: ["RB", "#cc342d"], rs: ["RS", "#dea584"],
  java: ["JV", "#b07219"], c: ["C", "#555555"], h: ["H", "#555555"], cpp: ["C+", "#f34b7d"],
  json: ["{}", "#cbcb41"], yml: ["YM", "#cb171e"], yaml: ["YM", "#cb171e"], toml: ["TO", "#9c4221"],
  md: ["MD", "#519aba"], markdown: ["MD", "#519aba"], txt: ["TX", "#9aa4b0"],
  html: ["<>", "#e34c26"], htm: ["<>", "#e34c26"], css: ["#", "#563d7c"], scss: ["#", "#c6538c"],
  sh: ["SH", "#89e051"], bash: ["SH", "#89e051"], zsh: ["SH", "#89e051"],
  dockerfile: ["DK", "#384d54"], sql: ["SQ", "#e38c00"], xml: ["</", "#e34c26"],
  png: ["IM", "#a074c4"], jpg: ["IM", "#a074c4"], jpeg: ["IM", "#a074c4"], gif: ["IM", "#a074c4"], svg: ["IM", "#ffb13b"],
  lock: ["LK", "#9aa4b0"], env: ["EV", "#e2c08d"], gitignore: ["GI", "#f14e32"],
};

function extOf(name: string): string {
  const n = name.toLowerCase();
  if (n === "dockerfile") return "dockerfile";
  if (n === ".gitignore") return "gitignore";
  if (n === ".env" || n.indexOf(".env.") === 0) return "env";
  const i = n.lastIndexOf(".");
  return i > 0 ? n.slice(i + 1) : "";
}

// fileIconDef returns [glyph, color] for a filename.
export function fileIconDef(name: string): [string, string] {
  return ICONS[extOf(name)] || ["·", "#8a94a6"];
}
