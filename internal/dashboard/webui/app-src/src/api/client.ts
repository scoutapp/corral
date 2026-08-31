// Thin fetch/WebSocket helpers for the dashboard API. Every request is
// same-origin so the sc_dash_token HttpOnly cookie (set by requireAuth on the
// first ?token= visit) rides along automatically — the SPA never handles the
// token itself.

export class ApiError extends Error {
  status: number;
  body: string;
  constructor(status: number, body: string) {
    super(body || `HTTP ${status}`);
    this.status = status;
    this.body = body;
  }
}

async function parse<T>(r: Response): Promise<T> {
  if (!r.ok) {
    const text = await r.text().catch(() => "");
    throw new ApiError(r.status, text);
  }
  // Some endpoints return no body (204-ish); guard JSON parse.
  const text = await r.text();
  return (text ? JSON.parse(text) : undefined) as T;
}

export function getJSON<T>(path: string): Promise<T> {
  return fetch(path, { credentials: "same-origin" }).then((r) => parse<T>(r));
}

export function postJSON<T>(path: string, body?: unknown): Promise<T> {
  return fetch(path, {
    method: "POST",
    credentials: "same-origin",
    headers: { "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  }).then((r) => parse<T>(r));
}

// postRaw returns the full Response so callers can branch on status (e.g. the
// 409 ssh_keys_pending flow) without an exception.
export function postRaw(path: string, body?: unknown): Promise<Response> {
  return fetch(path, {
    method: "POST",
    credentials: "same-origin",
    headers: { "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
}

export function putJSON<T>(path: string, body?: unknown): Promise<T> {
  return fetch(path, {
    method: "PUT",
    credentials: "same-origin",
    headers: { "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  }).then((r) => parse<T>(r));
}

export function delJSON<T>(path: string): Promise<T> {
  return fetch(path, { method: "DELETE", credentials: "same-origin" }).then((r) => parse<T>(r));
}

export function getText(path: string): Promise<string> {
  return fetch(path, { credentials: "same-origin" }).then((r) => {
    if (!r.ok) throw new ApiError(r.status, "");
    return r.text();
  });
}

// wsURL builds an absolute ws:// or wss:// URL for a same-origin path, matching
// the page's protocol. Used by the terminal/chat/ssh-load WebSocket surfaces.
export function wsURL(path: string): string {
  const proto = location.protocol === "https:" ? "wss:" : "ws:";
  return `${proto}//${location.host}${path}`;
}
