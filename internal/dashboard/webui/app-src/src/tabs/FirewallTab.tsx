import { useEffect, useRef, useState } from "react";

// Firewall Log tab: tails the allowlist-proxy's live log via SSE
// (/p/<id>/firewall/stream). Port of the startFirewallStream() piece of
// dashboard.js. Every request the proxy sees (ALLOWED/BLOCKED/MONITORED/DIRECT)
// shows here — the complete-coverage view, vs the Mitm tab's decrypted subset.
export function FirewallTab({ projectId }: { projectId: string }) {
  const [lines, setLines] = useState<string[]>([]);
  const preRef = useRef<HTMLPreElement | null>(null);

  useEffect(() => {
    const es = new EventSource(`/p/${projectId}/firewall/stream`);
    es.onmessage = (e) => setLines((ls) => [...ls, e.data]);
    es.addEventListener("error", () => setLines((ls) => [...ls, "[stream disconnected]"]));
    return () => es.close();
  }, [projectId]);

  useEffect(() => {
    if (preRef.current) preRef.current.scrollTop = preRef.current.scrollHeight;
  }, [lines]);

  return (
    <div className="screen-frame">
      <div className="screen-bar">
        <i className="screen-dot" />
        allowlist proxy · live log
      </div>
      <pre className="screen-body" ref={preRef}>
        {lines.join("\n")}
      </pre>
    </div>
  );
}
