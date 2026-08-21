// Parse the "=== LIVE VIEW READY ===" success block a worker Claude emits once it
// has booted an app and verified a login through the Live View proxy (see the
// worker.boot_guidance prompt). The block is a fenced, machine-readable footer:
//
//   === LIVE VIEW READY ===
//   status: verified
//   url_path: /apps
//   port: 3000
//   login: admin@example.com / password
//   note: apm booted, seeded, and Playwright-verified
//   === END LIVE VIEW READY ===
//
// We detect it in the assistant's text and render a green "verified" callout
// instead of leaving it as raw text at the bottom of the transcript.

export interface LiveViewReady {
  status: string; // "verified" (the only value we treat as a success)
  urlPath?: string;
  port?: string;
  login?: string;
  note?: string;
  // The transcript text with the block removed, so the surrounding prose still
  // renders normally above the callout.
  rest: string;
}

const START = "=== LIVE VIEW READY ===";
const END = "=== END LIVE VIEW READY ===";

// parseLiveViewReady returns the parsed block + the text with it stripped, or
// null when no complete block is present. Tolerant of surrounding whitespace and
// key casing; keys are `key: value` lines between the delimiters.
export function parseLiveViewReady(text: string): LiveViewReady | null {
  if (!text) return null;
  const startIdx = text.indexOf(START);
  if (startIdx === -1) return null;
  const afterStart = startIdx + START.length;
  const endIdx = text.indexOf(END, afterStart);
  if (endIdx === -1) return null; // not finished streaming yet — leave as text

  const body = text.slice(afterStart, endIdx);
  const fields: Record<string, string> = {};
  for (const line of body.split("\n")) {
    const colon = line.indexOf(":");
    if (colon === -1) continue;
    const key = line.slice(0, colon).trim().toLowerCase();
    const val = line.slice(colon + 1).trim();
    if (key) fields[key] = val;
  }

  const rest = (text.slice(0, startIdx) + text.slice(endIdx + END.length)).trim();
  return {
    status: fields["status"] || "",
    urlPath: fields["url_path"] || fields["path"] || undefined,
    port: fields["port"] || undefined,
    login: fields["login"] || undefined,
    note: fields["note"] || undefined,
    rest,
  };
}
