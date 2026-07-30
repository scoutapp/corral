// Claude-Desktop-style chat client. Pairs with handleChatWS in chat.go: the user
// types a turn, we send {prompt} over the WebSocket, and the server streams back
// typed frames (text / tool_use / result / error / turn_end) which we render as
// message bubbles. No external deps — a tiny escape-first markdown renderer keeps
// this self-contained (the dashboard ships no CDN assets).
(function () {
  var id = document.body.dataset.id;
  if (!id) return;

  var log = document.getElementById("log");
  var input = document.getElementById("input");
  var send = document.getElementById("send");
  var stop = document.getElementById("stop");

  // Read-only tools by default; the server validates ?tools= against a whitelist.
  var proto = location.protocol === "https:" ? "wss:" : "ws:";
  var ws = new WebSocket(proto + "//" + location.host + "/p/" + id + "/chat/ws?tools=Read,Grep,Glob");
  var ready = false;
  var busy = false;
  var curAssistant = null; // the assistant bubble accumulating this turn's text
  var curText = "";
  var lastTool = null;     // the most recent tool card, so tool_result can fill it

  ws.onopen = function () { ready = true; updateSend(); };
  ws.onclose = function () { ready = false; setBusy(false); updateSend(); addMeta("disconnected — reload the panel to reconnect", true); };
  ws.onmessage = function (ev) {
    var m;
    try { m = JSON.parse(ev.data); } catch (e) { return; }
    switch (m.type) {
      case "text":
        removeTyping();
        curText += m.text;
        if (!curAssistant) curAssistant = addBubble("assistant", "");
        curAssistant.querySelector(".bubble").innerHTML = renderMarkdown(curText);
        scroll();
        break;
      case "tool_use":
        // A new tool call starts a fresh assistant text block afterward.
        curAssistant = null; curText = "";
        removeTyping();
        lastTool = addToolCard(m.tool, m.input);
        break;
      case "tool_result":
        fillToolResult(lastTool, m.result);
        break;
      case "result":
        if (m.isError) addMeta("Claude reported an error for this turn.", true);
        else if (m.costUsd || m.model) addMeta((m.model || "claude") + (m.costUsd ? " · " + m.costUsd : ""), false);
        break;
      case "error":
        addMeta(m.text || "error", true);
        break;
      case "canceled":
        addMeta("stopped", false);
        break;
      case "turn_end":
        setBusy(false); curAssistant = null; curText = ""; lastTool = null;
        removeTyping();
        updateSend();
        input.focus();
        break;
    }
  };

  function setBusy(v) { busy = v; document.body.classList.toggle("busy", v); }
  function updateSend() { send.disabled = !ready || busy || input.value.trim() === ""; }

  function submit() {
    var text = input.value.trim();
    if (!ready || busy || !text) return;
    addBubble("user", renderMarkdown(text));
    ws.send(JSON.stringify({ prompt: text }));
    input.value = ""; autoGrow();
    setBusy(true); updateSend();
    addTyping();
    scroll();
  }

  function cancel() {
    if (!ready || !busy) return;
    ws.send(JSON.stringify({ action: "cancel" }));
  }

  send.addEventListener("click", submit);
  stop.addEventListener("click", cancel);
  input.addEventListener("keydown", function (e) {
    if (e.key === "Enter" && !e.shiftKey) { e.preventDefault(); submit(); }
  });
  input.addEventListener("input", function () { autoGrow(); updateSend(); });

  function autoGrow() { input.style.height = "auto"; input.style.height = Math.min(160, input.scrollHeight) + "px"; }

  // ---- rendering helpers ----------------------------------------------------
  function addBubble(role, html) {
    var wrap = document.createElement("div");
    wrap.className = "msg " + role;
    var avatar = document.createElement("div");
    avatar.className = "avatar";
    avatar.textContent = role === "assistant" ? "✳" : "you".charAt(0).toUpperCase();
    var bubble = document.createElement("div");
    bubble.className = "bubble";
    bubble.innerHTML = html;
    wrap.appendChild(avatar); wrap.appendChild(bubble);
    log.appendChild(wrap);
    scroll();
    return wrap;
  }
  // Build an expandable tool card: a summary line (name + key input) that opens
  // to show the full input and, once it arrives, the result.
  function addToolCard(name, inputJson) {
    var wrap = document.createElement("div");
    wrap.className = "tool";
    var d = document.createElement("details");
    var summary = document.createElement("summary");
    var nameEl = document.createElement("span");
    nameEl.className = "tool-name"; nameEl.textContent = name || "tool";
    var argEl = document.createElement("span");
    argEl.className = "tool-arg"; argEl.textContent = summarizeInput(inputJson);
    summary.appendChild(nameEl); summary.appendChild(argEl);
    var body = document.createElement("div");
    body.className = "tool-body";
    if (inputJson) body.appendChild(labeledPre("input", prettyJson(inputJson)));
    d.appendChild(summary); d.appendChild(body);
    wrap.appendChild(d);
    log.appendChild(wrap);
    scroll();
    return wrap;
  }
  function fillToolResult(card, result) {
    if (!card || !result) return;
    card.querySelector(".tool-body").appendChild(labeledPre("result", result));
    scroll();
  }
  function labeledPre(label, text) {
    var frag = document.createDocumentFragment();
    var l = document.createElement("div"); l.className = "label"; l.textContent = label;
    var pre = document.createElement("pre"); pre.textContent = text;
    frag.appendChild(l); frag.appendChild(pre);
    return frag;
  }
  // Compact one-liner for the summary: prefer a recognizable field, else short JSON.
  function summarizeInput(inputJson) {
    if (!inputJson) return "";
    var o; try { o = JSON.parse(inputJson); } catch (e) { return ""; }
    var keys = ["file_path", "path", "pattern", "command", "query", "url"];
    for (var i = 0; i < keys.length; i++) {
      if (o[keys[i]] != null) return String(o[keys[i]]);
    }
    var s = inputJson.replace(/\s+/g, " ");
    return s.length > 80 ? s.slice(0, 79) + "…" : s;
  }
  function prettyJson(s) {
    try { return JSON.stringify(JSON.parse(s), null, 2); } catch (e) { return s; }
  }
  function addMeta(text, isErr) {
    var meta = document.createElement("div");
    meta.className = "meta" + (isErr ? " error" : "");
    meta.textContent = text;
    log.appendChild(meta);
    scroll();
  }
  var typingEl = null;
  function addTyping() { removeTyping(); typingEl = addBubble("assistant", '<span class="typing">Claude is thinking…</span>'); }
  function removeTyping() { if (typingEl && !curText) { typingEl.remove(); } typingEl = null; }
  function scroll() { log.scrollTop = log.scrollHeight; }

  // Minimal, escape-first markdown: escape all HTML, then re-introduce only
  // fenced code blocks, inline code, bold, and paragraph breaks. Never renders
  // raw HTML from the model.
  function esc(s) { return s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;"); }
  function renderMarkdown(src) {
    var out = "";
    var parts = src.split(/```/);
    for (var i = 0; i < parts.length; i++) {
      if (i % 2 === 1) {
        // code fence: strip an optional leading language token on the first line
        var body = parts[i].replace(/^[a-zA-Z0-9_+-]*\n/, "");
        out += "<pre><code>" + esc(body) + "</code></pre>";
      } else {
        var text = esc(parts[i]);
        text = text.replace(/`([^`]+)`/g, "<code>$1</code>");
        text = text.replace(/\*\*([^*]+)\*\*/g, "<strong>$1</strong>");
        // paragraphs on blank lines; single newlines become <br>
        text = text.split(/\n{2,}/).map(function (p) {
          return "<p>" + p.replace(/\n/g, "<br>") + "</p>";
        }).join("");
        out += text;
      }
    }
    return out;
  }

  updateSend();
})();
