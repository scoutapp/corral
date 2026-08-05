// Minimal, escape-first markdown for the chat panel: escape all HTML, then
// re-introduce only fenced code blocks, inline code, bold, and paragraph
// breaks. Never renders raw HTML from the model. Port of chat.js renderMarkdown.
function esc(s: string): string {
  return s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
}

export function renderMarkdown(src: string): string {
  let out = "";
  const parts = src.split(/```/);
  for (let i = 0; i < parts.length; i++) {
    if (i % 2 === 1) {
      const body = parts[i].replace(/^[a-zA-Z0-9_+-]*\n/, "");
      out += `<pre><code>${esc(body)}</code></pre>`;
    } else {
      let text = esc(parts[i]);
      text = text.replace(/`([^`]+)`/g, "<code>$1</code>");
      text = text.replace(/\*\*([^*]+)\*\*/g, "<strong>$1</strong>");
      text = text
        .split(/\n{2,}/)
        .map((p) => `<p>${p.replace(/\n/g, "<br>")}</p>`)
        .join("");
      out += text;
    }
  }
  return out;
}
