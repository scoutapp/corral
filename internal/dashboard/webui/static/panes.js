// Landing page: live "panes into work." Polls /status and renders one pane per
// project, sorted by urgency so the agent that needs YOU floats to the top:
// waiting-on-you first, then working, then idle. The grid re-sorts live as
// states change — the whole point is triage at a glance.
//
// Sound alerts: when a project transitions working -> waiting (it just stopped
// and needs you), we play a short chime. Each project has a per-instance mute
// toggle (persisted in localStorage by project id) so you can silence one noisy
// agent while still hearing the others; a header toggle mutes everything.
(function () {
  var panesEl = document.getElementById("panes");
  var summaryEl = document.getElementById("summary");
  var POLL_MS = 3000;

  // Urgency order for the sort: waiting-on-you is what demands attention.
  var RANK = { waiting: 0, working: 1, off: 2 };

  // ---- Mute state (persisted) ------------------------------------------------
  // Per-project mutes live in localStorage under one JSON object keyed by id;
  // the global mute is a single flag. Both survive reloads.
  var MUTES_KEY = "sandclaude.muted";      // { "<projectId>": true }
  var GLOBAL_KEY = "sandclaude.mutedAll";  // "1" when everything is muted
  var BOOT_KEY = "sandclaude.bootId";      // last server boot id we saw

  // Drop stale per-project state when the dashboard server has restarted. The
  // server sends a fresh boot_id each launch (see /status); project ids can be
  // reused across reboots, so clearing on boot-change prevents an old mute
  // sticking to an unrelated project. Called once per poll with the current id.
  function reconcileBoot(bootId) {
    if (!bootId) return;
    var seen = null;
    try { seen = localStorage.getItem(BOOT_KEY); } catch (e) {}
    if (seen === bootId) return;
    // New (or first-seen) boot: clear mute prefs and record the id.
    muted = {};
    saveMutes(muted);
    try { localStorage.setItem(BOOT_KEY, bootId); } catch (e) {}
  }

  function loadMutes() {
    try { return JSON.parse(localStorage.getItem(MUTES_KEY) || "{}") || {}; }
    catch (e) { return {}; }
  }
  function saveMutes(m) {
    try { localStorage.setItem(MUTES_KEY, JSON.stringify(m)); } catch (e) {}
  }
  var muted = loadMutes();
  var mutedAll = localStorage.getItem(GLOBAL_KEY) === "1";

  function isMuted(id) { return mutedAll || !!muted[id]; }
  function toggleMute(id) {
    if (muted[id]) delete muted[id]; else muted[id] = true;
    saveMutes(muted);
  }
  function toggleMuteAll() {
    mutedAll = !mutedAll;
    try { localStorage.setItem(GLOBAL_KEY, mutedAll ? "1" : "0"); } catch (e) {}
  }

  // ---- Chime (WebAudio, no bundled asset) ------------------------------------
  // A short two-note chime. Created lazily on first use because browsers only
  // allow an AudioContext to start after a user gesture — the first click on
  // the page (including the mute toggles) unlocks it.
  var audioCtx = null;
  function chime() {
    try {
      if (!audioCtx) {
        var AC = window.AudioContext || window.webkitAudioContext;
        if (!AC) return;
        audioCtx = new AC();
      }
      if (audioCtx.state === "suspended") audioCtx.resume();
      var now = audioCtx.currentTime;
      [880, 1174.66].forEach(function (freq, i) { // A5 -> D6, a gentle "ding-dong"
        var osc = audioCtx.createOscillator();
        var gain = audioCtx.createGain();
        osc.type = "sine";
        osc.frequency.value = freq;
        var t = now + i * 0.16;
        gain.gain.setValueAtTime(0.0001, t);
        gain.gain.exponentialRampToValueAtTime(0.2, t + 0.02);
        gain.gain.exponentialRampToValueAtTime(0.0001, t + 0.35);
        osc.connect(gain).connect(audioCtx.destination);
        osc.start(t);
        osc.stop(t + 0.4);
      });
    } catch (e) { /* audio is best-effort; never break the page */ }
  }

  // ---- Transition tracking ---------------------------------------------------
  // Remember each project's last-seen activity so we can detect the
  // working -> waiting edge. Seeded on first poll so we don't chime for
  // projects that were already waiting when the page loaded.
  var lastActivity = {};
  var seeded = false;

  function esc(s) {
    return String(s == null ? "" : s).replace(/&/g, "&amp;").replace(/</g, "&lt;")
      .replace(/>/g, "&gt;").replace(/"/g, "&quot;");
  }

  function activityLabel(a) {
    if (a === "working") return "working";
    if (a === "waiting") return "waiting on you";
    return "idle";
  }

  function dot(up) { return '<i class="pdot ' + (up ? "on" : "off") + '"></i>'; }

  function pane(p) {
    var act = p.activity || "off";
    var peek = p.peek ? esc(p.peek) : (act === "off" ? "container not running" : "…");
    var rate = act === "working"
      ? '<span class="rate">' + p.anthropic_hits + " req/min · live</span>"
      : (act === "waiting" ? '<span class="rate quiet">idle at prompt</span>' : "");

    var m = isMuted(p.id);
    // The bell is a button inside the pane's <a>; click handler stops navigation.
    var bell =
      '<button class="mute-btn" type="button" data-id="' + esc(p.id) + '"' +
      ' title="' + (m ? "Unmute alerts for this project" : "Mute alerts for this project") + '"' +
      ' aria-pressed="' + (m ? "true" : "false") + '">' + (m ? "🔇" : "🔔") + "</button>";

    // Start (idle) / Stop (running) — a power toggle for the project's container,
    // right on the pane. Both live inside the pane's <a>, so the delegated handler
    // stops navigation. Start may 409 if ssh keys need loading (see the handler).
    var power = p.container_up
      ? '<button class="power-btn stop" type="button" data-id="' + esc(p.id) + '"' +
        ' data-name="' + esc(p.name) + '" title="Stop this project\'s container">■</button>'
      : '<button class="power-btn start" type="button" data-id="' + esc(p.id) + '"' +
        ' data-name="' + esc(p.name) + '" title="Start this project\'s container">▶</button>';

    // Remove is offered only for idle projects (nothing running to disrupt). It
    // unregisters the project from the dashboard list; on-disk config is kept.
    var remove = act === "off"
      ? '<button class="remove-btn" type="button" data-id="' + esc(p.id) + '"' +
        ' data-name="' + esc(p.name) + '" title="Remove this idle project from the dashboard">✕</button>'
      : "";

    return '' +
      '<a class="pane pane-' + act + '" href="/p/' + esc(p.id) + '">' +
        '<div class="pane-top">' +
          '<span class="pane-name">' + esc(p.name) + "</span>" +
          '<span class="pane-state state-' + act + '">' +
            '<i class="beacon"></i>' + activityLabel(act) +
            power + bell + remove +
          "</span>" +
        "</div>" +
        '<div class="pane-peek"><span class="peek-caret">&gt;</span> ' + peek + "</div>" +
        '<div class="pane-foot">' +
          '<span class="svc">' + dot(p.container_up) + "box</span>" +
          '<span class="svc">' + dot(p.mitm_up) + "proxy</span>" +
          '<span class="svc">' + dot(p.tmux_up) + "session</span>" +
          rate +
        "</div>" +
      "</a>";
  }

  function summarize(projects) {
    var w = 0, q = 0, off = 0;
    projects.forEach(function (p) {
      if (p.activity === "working") w++;
      else if (p.activity === "waiting") q++;
      else off++;
    });
    var parts = [];
    if (q) parts.push(q + " waiting on you");
    if (w) parts.push(w + " working");
    if (off) parts.push(off + " idle");
    return projects.length + " project" + (projects.length === 1 ? "" : "s") +
      (parts.length ? " — " + parts.join(", ") : "");
  }

  // Fire the chime for any project that just went working -> waiting and isn't
  // muted. Always update lastActivity afterward so each edge fires once.
  function detectAlerts(projects) {
    var play = false;
    projects.forEach(function (p) {
      var prev = lastActivity[p.id];
      if (seeded && prev === "working" && p.activity === "waiting" && !isMuted(p.id)) {
        play = true;
      }
      lastActivity[p.id] = p.activity;
    });
    if (play) chime();
    seeded = true;
  }

  function render(projects) {
    projects.sort(function (a, b) {
      var ra = RANK[a.activity] == null ? 3 : RANK[a.activity];
      var rb = RANK[b.activity] == null ? 3 : RANK[b.activity];
      if (ra !== rb) return ra - rb;
      return a.name.localeCompare(b.name);
    });

    if (!projects.length) return; // keep the server-rendered empty state

    panesEl.innerHTML = projects.map(pane).join("");
    summaryEl.textContent = summarize(projects);
    // Reflect the most urgent state on the footer for a peripheral cue.
    summaryEl.className = projects.some(function (p) { return p.activity === "waiting"; })
      ? "attention" : "muted";
    updateGlobalMuteBtn();
  }

  // Delegated click handler for the per-project bells. Stops the click from
  // navigating into the project (the bell lives inside the pane's <a>).
  panesEl.addEventListener("click", function (ev) {
    if (!ev.target.closest) return;

    var mb = ev.target.closest(".mute-btn");
    if (mb) {
      ev.preventDefault();
      ev.stopPropagation();
      toggleMute(mb.getAttribute("data-id"));
      if (lastProjects) render(lastProjects.slice()); // flip the icon immediately
      return;
    }

    var pb = ev.target.closest(".power-btn");
    if (pb) {
      ev.preventDefault();
      ev.stopPropagation();
      var pid = pb.getAttribute("data-id");
      var pname = pb.getAttribute("data-name") || "this project";
      var stopping = pb.classList.contains("stop");
      pb.disabled = true;
      pb.textContent = stopping ? "…" : "…";
      fetch("/p/" + encodeURIComponent(pid) + "/" + (stopping ? "stop" : "start"),
            { method: "POST", credentials: "same-origin" })
        .then(function (r) {
          return r.json().catch(function () { return {}; }).then(function (b) {
            return { status: r.status, body: b };
          });
        })
        .then(function (res) {
          // Start may 409 if ssh keys aren't loaded — the passphrase PTY lives on
          // the project's Config tab (Config → SSH keys → Load), so send the user
          // into the project to do that.
          if (!stopping && res.status === 409 && res.body && res.body.ssh_keys_pending) {
            summaryEl.textContent = '"' + pname + '" needs SSH keys loaded first — open it, then Config → SSH keys → Load.';
            summaryEl.className = "attention";
            location.href = "/p/" + encodeURIComponent(pid);
            return;
          }
          if (res.status >= 400) throw new Error((res.body && res.body.message) || ("HTTP " + res.status));
          // Let the next /status poll reflect the new state (it polls every ~2s).
          summaryEl.textContent = (stopping ? "stopping " : "starting ") + pname + "…";
          summaryEl.className = "";
        })
        .catch(function (err) {
          pb.disabled = false;
          pb.textContent = stopping ? "■" : "▶";
          summaryEl.textContent = (stopping ? "stop" : "start") + " failed: " + err.message;
          summaryEl.className = "attention";
        });
      return;
    }

    var rb = ev.target.closest(".remove-btn");
    if (rb) {
      ev.preventDefault();
      ev.stopPropagation();
      var id = rb.getAttribute("data-id");
      var name = rb.getAttribute("data-name") || "this project";
      if (!window.confirm('Remove "' + name + '" from the dashboard?\n\n' +
          "This only unregisters it here — its .sandclaude/ config and logs are kept, " +
          "and it reappears if you start it again.")) return;
      rb.disabled = true;
      fetch("/p/" + encodeURIComponent(id) + "/remove", { method: "POST", credentials: "same-origin" })
        .then(function (r) { if (!r.ok) throw new Error("HTTP " + r.status); })
        .then(function () {
          if (muted[id]) { delete muted[id]; saveMutes(muted); } // tidy its mute pref
          if (lastProjects) {
            lastProjects = lastProjects.filter(function (p) { return p.id !== id; });
            render(lastProjects.slice());
          }
        })
        .catch(function (err) {
          rb.disabled = false;
          summaryEl.textContent = "remove failed: " + err.message;
          summaryEl.className = "attention";
        });
      return;
    }
  });

  // ---- Global mute toggle (header) ------------------------------------------
  var globalBtn = document.getElementById("mute-all");
  function updateGlobalMuteBtn() {
    if (!globalBtn) return;
    globalBtn.textContent = mutedAll ? "🔇 muted" : "🔔 alerts";
    globalBtn.title = mutedAll ? "Unmute all alerts" : "Mute all alerts";
    globalBtn.setAttribute("aria-pressed", mutedAll ? "true" : "false");
  }
  if (globalBtn) {
    globalBtn.addEventListener("click", function () {
      toggleMuteAll();
      updateGlobalMuteBtn();
      if (lastProjects) render(lastProjects.slice());
    });
  }

  var lastProjects = null;
  function poll() {
    fetch("/status", { credentials: "same-origin" })
      .then(function (r) { if (!r.ok) throw new Error("HTTP " + r.status); return r.json(); })
      .then(function (data) {
        reconcileBoot(data.boot_id);
        var projects = data.projects || [];
        lastProjects = projects;
        detectAlerts(projects);
        render(projects);
      })
      .catch(function (err) { summaryEl.textContent = "lost connection: " + err.message; summaryEl.className = "attention"; });
  }

  poll();
  setInterval(poll, POLL_MS);
})();
