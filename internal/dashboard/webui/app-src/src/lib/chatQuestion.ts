// Parse the fenced `corral-question` block the host chat may emit to ask the user
// a quick multiple-choice question (see chatQuestionGuidance in chat.go). The UI
// renders the options as one-click chips; clicking one sends that label as the
// next turn. We use a convention rather than the AskUserQuestion tool because that
// tool is auto-dismissed in headless `claude -p` mode.
//
//   ```corral-question
//   question: Which environment should I target?
//   - Staging
//   - Production
//   ```

export interface ChatQuestion {
  question: string;
  options: string[];
  // The assistant text with the block removed, so surrounding prose still renders.
  rest: string;
}

// Matches a fenced corral-question block. Tolerant of ```` ```corral-question ````
// with optional trailing spaces; captures the block body.
const BLOCK_RE = /```[ \t]*corral-question[ \t]*\n([\s\S]*?)```/i;

// parseChatQuestion returns the parsed question + the text with the block stripped,
// or null when no complete block is present (e.g. still streaming).
export function parseChatQuestion(text: string): ChatQuestion | null {
  if (!text) return null;
  const m = text.match(BLOCK_RE);
  if (!m) return null;

  const body = m[1];
  let question = "";
  const options: string[] = [];
  for (const raw of body.split("\n")) {
    const line = raw.trim();
    if (!line) continue;
    const q = line.match(/^question\s*:\s*(.+)$/i);
    if (q) {
      question = q[1].trim();
      continue;
    }
    const opt = line.match(/^[-*]\s+(.+)$/); // "- Option" or "* Option"
    if (opt) options.push(opt[1].trim());
  }
  // Need a question and at least two options to render a useful chooser.
  if (!question || options.length < 2) return null;

  const rest = (text.slice(0, m.index) + text.slice((m.index ?? 0) + m[0].length)).trim();
  return { question, options, rest };
}
