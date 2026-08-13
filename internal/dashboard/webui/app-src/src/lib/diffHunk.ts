// Split a unified diff hunk (the kind stored on a PR block: lines prefixed with
// " ", "+", "-", plus "@@" headers) into the "original" (before) and "modified"
// (after) text, so it can be rendered as a real side-by-side, syntax-highlighted
// diff via CodeMirror's createDiff. Header/metadata lines are dropped.
export function splitUnifiedHunk(hunk: string): { original: string; modified: string } {
  const original: string[] = [];
  const modified: string[] = [];
  for (const line of hunk.split("\n")) {
    // Skip diff metadata: @@ hunk headers, +++/--- file headers, diff --git,
    // index, and mode lines.
    if (
      line.startsWith("@@") ||
      line.startsWith("+++") ||
      line.startsWith("---") ||
      line.startsWith("diff ") ||
      line.startsWith("index ") ||
      line.startsWith("new file") ||
      line.startsWith("deleted file") ||
      line.startsWith("similarity ") ||
      line.startsWith("rename ")
    ) {
      continue;
    }
    if (line.startsWith("+")) {
      modified.push(line.slice(1));
    } else if (line.startsWith("-")) {
      original.push(line.slice(1));
    } else {
      // context line (leading space) or bare line — appears in both sides.
      const text = line.startsWith(" ") ? line.slice(1) : line;
      original.push(text);
      modified.push(text);
    }
  }
  return { original: original.join("\n"), modified: modified.join("\n") };
}
