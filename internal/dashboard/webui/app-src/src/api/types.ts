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

// GET /p/<id>/config -> the editable project config view.
export interface CredView {
  host: string;
  kind: string;
  name: string;
  masked: string;
}
export interface ConfigView {
  id: string;
  workspace: string;
  allowed_hosts: string[];
  monitor_hosts: string[];
  monitor_all: boolean;
  mitm_preset: string; // minimal|all|none|custom
  mitm_ports: string[];
  credentials: CredView[];
  proxy_enabled: boolean;
  passthrough_firewall: boolean;
  dind_enabled: boolean;
  dind_ports: string[];
  launch_tmux: boolean;
  seccomp_mode: string;
  ssh_keys: string[];
  ssh_keys_global: string[];
  ssh_keys_effective: string[];
  container_up: boolean;
}

// POST /p/<id>/config/apply | /config/restart payload (only changed fields).
export interface CredSet {
  host: string;
  kind: string;
  name: string;
  value: string;
}
export interface ConfigEdit {
  allowed_hosts?: string[];
  monitor_hosts?: string[];
  mitm_preset?: string;
  mitm_ports?: string[];
  set_creds?: CredSet[];
  unset_creds?: string[];
  proxy_enabled?: boolean;
  passthrough_firewall?: boolean;
  dind_enabled?: boolean;
  dind_ports?: string[];
  launch_tmux?: boolean;
  seccomp_mode?: string;
  ssh_keys?: string[];
}
export interface ConfigDiffEntry {
  field: string;
  change: string;
}

// GET /p/<id>/sshkeys/available -> { keys: SSHAvailableKey[] }
export interface SSHAvailableKey {
  name: string;
  type?: string;
  comment?: string;
}

// GET /global/config -> global settings view.
export interface GlobalView {
  creds_path: string;
  ssh_keys_path: string;
  credentials: CredView[];
  monitor_hosts: string[];
  mitm_ports: string[];
  ssh_keys: string[];
  available_ssh_keys: SSHAvailableKey[];
  update_repo: string; // configured value ("" = use default)
  update_repo_default: string;
}
export interface GlobalEdit {
  set_creds?: CredSet[];
  unset_creds?: string[];
  monitor_hosts?: string[];
  mitm_ports?: string[];
  ssh_keys?: string[];
  update_repo?: string; // "" clears the override (back to default)
}

// GET /update-status -> update availability for the global banner.
export interface UpdateStatus {
  current: string;
  latest: string;
  repo: string;
  update_available: boolean;
  checked_at?: string;
  unreachable?: boolean; // couldn't reach the update host (e.g. private repo)
  error?: string;
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

// GET /repos -> { repos: CachedRepo[] }
export interface CachedRepo {
  id: string;
  name: string;
  url?: string;
  local_path?: string;
  is_private?: boolean;
}

// GET /gh/repos -> { available: bool, repos: GhRepo[] }
export interface GhRepo {
  nameWithOwner: string;
  url: string;
  isPrivate?: boolean;
}

// GET /gh/issues?repo=owner/name -> { available, issues: GhIssue[], reason? }
export interface GhIssue {
  number: number;
  title: string;
  body?: string;
  url?: string;
  createdAt?: string;
  author?: { login?: string };
}

// POST /projects/create -> { id, issue_prompt? }
export interface CreateProjectResponse {
  id: string;
  issue_prompt?: string;
}

// A repo spec sent to /projects/create (one of url|localPath|repoId, + branch).
export interface RepoSpec {
  url?: string;
  localPath?: string;
  repoId?: string;
  branch?: string;
}

// mitmweb's raw flow JSON (GET /p/<id>/mitm/flows returns an array of these).
export interface MitmMessage {
  headers?: [string, string][];
  contentLength?: number;
  timestamp_start?: number;
  timestamp_end?: number;
  status_code?: number;
  method?: string;
  pretty_host?: string;
  host?: string;
  path?: string;
}
export interface MitmFlow {
  id: string;
  request?: MitmMessage;
  response?: MitmMessage;
  timestamp_created?: number;
}

// GET /p/<id>/mitm/direct[?q=] -> { direct: DirectHost[] } — one entry per
// allowed-but-not-decrypted (direct-dialed) request from the proxy log. No
// decrypted contents exist for these. With ?q= the server searches the whole
// log (host contains q) for historical reach; without, it's the recent tail.
export interface DirectHost {
  host: string; // hostname:port
  when: string; // "YYYY/MM/DD HH:MM:SS"
  ts: number; // Unix seconds (0 if unparseable) — for chronological interleave
}

// PR Review (internal/prreview). Mirrors the Go structs in
// internal/prreview/types.go. Lists are empty until the repo is analyzed / PRs
// are fetched (writers land in later phases).

// GET /repos/<id>/forensics -> { files: PrFileStat[] }
export interface PrFileStat {
  id: number;
  repoId: string;
  filePath: string;
  totalCommits: number;
  fixCommits: number;
  churnScore?: number;
  lastAnalyzed?: string;
}

// GET /repos/<id>/prs -> { prs: PrItem[] }
export interface PrItem {
  id: number;
  repoId: string;
  number: number;
  title?: string;
  shortSummary?: string;
  githubUrl?: string;
  state?: string;
  baseSha?: string;
  headSha?: string;
  fetchedAt?: string;
}

// GET /repos/<id>/projects -> { projects: RepoProject[] }
export interface RepoProject {
  id: string;
  name: string;
  workspace: string;
}

// GET /prs/<prId>/links -> { links: PrLink[] }
export interface PrLink {
  id: number;
  prId: number;
  linkedPrId: number;
  relationship: string; // tests | tested_by | related | depends_on
  note?: string;
  linkedNumber?: number;
  linkedTitle?: string;
  linkedSummary?: string;
}

// GET /prs/<prId>/links/suggest -> { suggestions: LinkSuggestion[] }
export interface LinkSuggestion {
  prId: number;
  number: number;
  title: string;
  overlap: number;
}

// GET /prs/<prId>/risk -> { risk: PrRisk | null }
// POST /prs/<prId>/analyze -> { risk: PrRisk }
export interface PrRisk {
  meat: string;
  bugImpact: string;
  fileHealth: { file: string; risk: string; insight: string }[];
  fixHistory: string;
  overallRisk: string; // high | medium | low
  riskSummary: string;
}

// GET /prs/<prId>/blocks -> { blocks: PrBlock[] }
export interface PrBlock {
  id: number;
  prId: number;
  orderIndex: number;
  priority: number;
  filePath: string;
  lineStart: number;
  lineEnd: number;
  diffHunk?: string;
  title?: string;
  explanation?: string;
  codebaseContext?: string;
  hotnessScore?: number;
  isTest: boolean;
}
