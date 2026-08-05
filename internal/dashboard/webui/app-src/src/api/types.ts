// TypeScript mirrors of the Go JSON contracts the dashboard serves. Kept in one
// place so every component shares one source of truth; field names match the Go
// `json:"..."` tags exactly. When a Go handler's shape changes, update it here.

// GET /status  -> { projects: StatusRow[], boot_id: string }
// The projects list poll. boot_id changes when the daemon restarts, so the UI
// can detect a server bounce and hard-reload.
export interface StatusRow {
  id: string;
  name: string;
  workspace: string;
  container_up: boolean;
  tmux_up: boolean;
  mitm_up: boolean;
  activity: "working" | "waiting" | "off" | string;
  anthropic_hits: number;
  peek: string;
}

export interface StatusResponse {
  projects: StatusRow[];
  boot_id: string;
}

// Per-project live status embedded in the project page bootstrap.
export interface ProjectStatus {
  container_up: boolean;
  tmux_up: boolean;
  mitm_up: boolean;
}

// GET /p/<id>/sshkeys/status
export interface SSHKeysStatus {
  configured: boolean;
  loaded: boolean;
  keys: string[];
  count: number;
  container_stale: boolean;
}

// GET /p/<id>/git/repos -> { repos: GitRepo[], rootIsRepo: bool }
export interface GitRepo {
  path: string;
  name: string;
}
export interface GitReposResponse {
  repos: GitRepo[];
  rootIsRepo: boolean;
  repo?: boolean;
}

// GET /p/<id>/git/refs
export interface GitRefsResponse {
  repo: boolean;
  current?: string;
  branches?: string[];
  tags?: string[];
}

// GET /p/<id>/git/status
export interface GitChange {
  path: string;
  status: string;
  added?: number;
  removed?: number;
}
export interface GitStatusResponse {
  repo: boolean;
  changes?: GitChange[];
}

// GET /p/<id>/files/tree?path=
export interface TreeEntry {
  name: string;
  dir: boolean;
}
export interface FilesTreeResponse {
  entries?: TreeEntry[];
}

// GET /p/<id>/files/read?path=
export interface FilesReadResponse {
  content?: string;
  filename?: string;
  too_large?: boolean;
  size?: number;
}

// GET /p/<id>/files/find?q=
export interface FilesFindResponse {
  matches?: string[];
  truncated?: boolean;
}

// GET /p/<id>/files/grep?q=
export interface GrepHit {
  path: string;
  line: number;
  text: string;
}
export interface FilesGrepResponse {
  hits?: GrepHit[];
  truncated?: boolean;
}

// GET /p/<id>/git/file?path=&base=&target=  (both sides for the diff view)
export interface GitFileResponse {
  original?: string;
  modified?: string;
  filename?: string;
}

// A single mitm flow row (GET /p/<id>/mitm/flows).
export interface MitmFlow {
  id: string;
  method: string;
  host: string;
  path: string;
  status: number;
  when: string;
  size?: number;
}
