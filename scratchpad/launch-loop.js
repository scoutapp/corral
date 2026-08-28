export const meta = {
  name: 'apm-launch-loop',
  description: 'Create apm PR#5670 project, loop-fix until it launches, then destroy+recreate to prove DinD baseline reuse — timing full-build vs reused-build.',
  phases: [
    { title: 'Cleanup' },
    { title: 'Launch loop' },
    { title: 'Reuse verify' },
    { title: 'Report' },
  ],
}

// --- shared constants ---
const REPO_ID = '3315137c0903'
const CREATE_BODY = JSON.stringify({
  mode: 'clone',
  repos: [{ repoId: REPO_ID, branch: '5669-agent-key-grace-period' }],
  name: 'apm-loop',
  dind: true,
  source: { kind: 'pr', repo_id: REPO_ID, number: 5670, url: 'https://github.com/scoutapp/apm/pull/5670', title: 'Grace period for Scout Key rotation' },
})

// The boot task a worker runs inside a freshly-created project. It must actually
// launch the Rails app on the checkin path and set live-view, OR report exactly
// why it failed. Kept tight; the worker preamble (injected by corral) handles the
// detached/self-wake contract.
function bootTask(projectId) {
  return [
    'Boot apm PR #5670 in Corral project ' + projectId + ' (repo scoutapp/apm, branch 5669-agent-key-grace-period) so it actually serves.',
    'Run everything inside the sandbox (corral project exec ' + projectId + ' -- <cmd>, or docker exec).',
    'TIME EACH STEP: capture `date +%s` before/after every step and print a line "STEP <name> <secs>s". Steps: image-check, services-up, deps, db-migrate, warmup, rails-boot. End with a TIMING block of all STEP lines.',
    'REUSE what the baseline seeded — do NOT redo work already present:',
    '1. image-check: echo $DOCKER_HOST; docker images; docker volume ls. If images are CORRUPT (dangling/missing layers) say so, then docker system prune -af and rebuild clean.',
    '2. services-up: Postgres (data dir on named volume apm-pgdata:/var/lib/postgresql/data) + Redis. Ruby 3.3.9 (official image). Clear HTTP_PROXY/HTTPS_PROXY (they hang bundle/rails); SCOUT_MONITOR=false.',
    '3. deps: bundle into named volume apm-bundle:/usr/local/bundle. If apm-bundle already has gems, SKIP install (report "reused").',
    '4. db-migrate: only if apm-pgdata is not already migrated (check for the previous_key column on orgs). Otherwise SKIP (report "reused"). The PR adds previous_key/previous_key_expires_at to orgs.',
    '5. warmup: cache bootsnap (tmp/cache/bootsnap) + assets in named volumes; docker commit the prepared app container to apm-prepared:latest so eager-load/assets/bootsnap are baked in next time (reuse it if it already exists).',
    '6. rails-boot: start rails on 0.0.0.0; curl a HUMAN-VIEWABLE page inside the sandbox (the app UI/login/dashboard, e.g. /users/sign_in) to confirm it renders — NOT /health_check or an API endpoint.',
    '7. Set live view on the HOST: corral api PUT /p/' + projectId + '/live-port -d \'{"port":<PORT>,"path":"<human-viewable route>"}\'. path is the URL route to a page a person wants to see; never a health/liveness probe.',
    'Return a REPORT whose FIRST line is exactly "LAUNCH: OK port=<PORT> path=<path>" (only if curl 200 AND live-port set) or "LAUNCH: FAIL reason=<short reason>". Then the TIMING block, and a line listing which artifacts were REUSED vs REBUILT this run.',
  ].join('\n')
}

// resultLine pulls the worker's final report's first meaningful LAUNCH line.
function launchVerdict(report) {
  if (!report) return { ok: false, reason: 'no worker report' }
  const m = report.match(/LAUNCH:\s*(OK|FAIL)([^\n]*)/i)
  if (!m) return { ok: false, reason: 'no LAUNCH line in report' }
  return { ok: m[1].toUpperCase() === 'OK', detail: (m[2] || '').trim(), raw: m[0] }
}

const SH = (label, phase, cmd) =>
  agent(`Run this shell and return its FULL stdout+stderr verbatim (no commentary):\n\n\`\`\`bash\n${cmd}\n\`\`\``,
    { label, phase, agentType: 'general-purpose' })

// ============================ PHASE 0: cleanup ============================
phase('Cleanup')
log('Destroying any existing apm-loop / apm-5670 projects + the (possibly corrupt) repo baseline, so we start clean.')
await SH('cleanup', 'Cleanup', `
set +e
# Remove any prior loop/PR projects (frees their volumes + the baseline).
for pid in $(corral api GET /status | python3 -c "import sys,json;[print(p['id']) for p in json.load(sys.stdin).get('projects',[]) if 'apm-loop' in p['name'] or 'apm-5670' in p['name']]"); do
  echo "removing project $pid"
  corral api POST /p/$pid/stop  2>&1 | tail -1
  corral api POST /p/$pid/remove 2>&1 | tail -1
done
sleep 3
# Delete the corrupt repo baseline so a clean one is built this run.
docker volume rm -f corral-dind-cache-repo-${REPO_ID} 2>&1 | tail -1
echo "=== caches after cleanup ==="; docker volume ls --filter name=corral-dind-cache- --format '{{.Name}}' || echo none
`)

// ============================ PHASE 1: launch loop ============================
phase('Launch loop')
const MAX_ATTEMPTS = 5
let launched = null       // { projectId, port, path }
let fullBuildSeconds = null
const attemptLog = []

for (let attempt = 1; attempt <= MAX_ATTEMPTS && !launched; attempt++) {
  log(`Attempt ${attempt}/${MAX_ATTEMPTS}: create project + boot.`)

  // Create the project via the global API; capture its id + a start timestamp.
  const created = await SH(`create#${attempt}`, 'Launch loop', `
set -e
START=$(date +%s)
RESP=$(corral api POST /projects/create -d '${CREATE_BODY}')
echo "$RESP"
PID=$(echo "$RESP" | python3 -c "import sys,json;print(json.load(sys.stdin).get('id',''))")
echo "PROJECT_ID=$PID"
echo "START_EPOCH=$START"
# Start the container so DinD (and baseline seed) come up.
corral api POST /p/$PID/start 2>&1 | tail -2
`)
  const pid = (created && created.match(/PROJECT_ID=(\w+)/) || [])[1]
  const startEpoch = parseInt((created && created.match(/START_EPOCH=(\d+)/) || [])[1] || '0', 10)
  if (!pid) { attemptLog.push({ attempt, ok: false, reason: 'create failed', created }); continue }

  // Boot + verify via a corral WORKER (it can exec the sandbox). Spawn it through
  // the API, then attach and wait for its report by polling the conversation.
  const boot = await agent(bootTask(pid), {
    label: `boot#${attempt}`, phase: 'Launch loop', agentType: 'general-purpose',
  })
  const verdict = launchVerdict(boot)

  // Independently confirm reuse status + record the elapsed build time.
  const check = await SH(`verify#${attempt}`, 'Launch loop', `
END=$(date +%s)
echo "END_EPOCH=$END"
echo "=== dind status ==="; corral api GET /p/${pid}/dind/status
echo "=== dind images ==="; corral api GET /p/${pid}/dind/images
echo "=== live-port ==="; corral api GET /p/${pid}/live-port
`)
  const endEpoch = parseInt((check && check.match(/END_EPOCH=(\d+)/) || [])[1] || '0', 10)
  const elapsed = startEpoch && endEpoch ? endEpoch - startEpoch : null

  if (verdict.ok) {
    launched = { projectId: pid, verdict, elapsedSeconds: elapsed }
    fullBuildSeconds = elapsed
    attemptLog.push({ attempt, ok: true, pid, elapsed, verdict, check })
    log(`Attempt ${attempt} LAUNCHED (${elapsed}s). ${verdict.raw}`)
  } else {
    attemptLog.push({ attempt, ok: false, pid, reason: verdict.reason || verdict.detail, boot, check })
    log(`Attempt ${attempt} FAILED: ${verdict.reason || verdict.detail}. Destroying project + work tab, retrying.`)
    // Fail path: destroy the container + the work tab (the boot worker job), retry.
    await SH(`cleanup#${attempt}`, 'Launch loop', `
set +e
# delete the boot worker's Work-tab job(s)
for jid in $(corral api GET /merge-jobs | python3 -c "import sys,json;[print(j['id']) for j in json.load(sys.stdin).get('jobs',[]) if j.get('kind')=='worker']"); do
  corral api DELETE /merge-jobs/$jid 2>&1 | tail -1
done
corral api POST /p/${pid}/stop 2>&1 | tail -1
corral api POST /p/${pid}/remove 2>&1 | tail -1
sleep 3
`)
  }
}

if (!launched) {
  phase('Report')
  return { launched: false, attempts: attemptLog, note: 'Did not reach a launch within MAX_ATTEMPTS; see per-attempt reasons.' }
}

// ============================ PHASE 2: reuse verify ============================
phase('Reuse verify')
log(`Launched on project ${launched.projectId} in ${fullBuildSeconds}s (full build). Now saving the baseline (stop), then recreating to prove reuse.`)

// Stop the launched project cleanly → auto-save should create repo-<id> baseline.
await SH('save-baseline', 'Reuse verify', `
set +e
echo "=== stopping launched project to trigger auto-save of the baseline ==="
corral api POST /p/${launched.projectId}/stop 2>&1 | tail -2
# auto-save runs in the background after stop; wait for the repo baseline to appear.
for i in $(seq 1 60); do
  if docker volume ls --filter name=corral-dind-cache-repo-${REPO_ID} -q | grep -q .; then echo "BASELINE_SAVED"; break; fi
  sleep 10
done
docker volume ls --filter name=corral-dind-cache-repo-${REPO_ID} --format '{{.Name}}'
corral api POST /p/${launched.projectId}/remove 2>&1 | tail -1
`)

// Recreate + boot again; this time the baseline should seed → faster.
const created2 = await SH('recreate', 'Reuse verify', `
set -e
START=$(date +%s); echo "START_EPOCH=$START"
RESP=$(corral api POST /projects/create -d '${CREATE_BODY}'); echo "$RESP"
PID=$(echo "$RESP" | python3 -c "import sys,json;print(json.load(sys.stdin).get('id',''))"); echo "PROJECT_ID=$PID"
corral api POST /p/$PID/start 2>&1 | tail -2
echo "=== dind status (should say REUSED / seeded copy) ==="; corral api GET /p/$PID/dind/status
`)
const pid2 = (created2.match(/PROJECT_ID=(\w+)/) || [])[1]
const start2 = parseInt((created2.match(/START_EPOCH=(\d+)/) || [])[1] || '0', 10)
const reusedFlag = /"reused"\s*:\s*true/.test(created2)

const boot2 = pid2 ? await agent(bootTask(pid2), { label: 'boot#reuse', phase: 'Reuse verify', agentType: 'general-purpose' }) : null
const verdict2 = launchVerdict(boot2)
const check2 = pid2 ? await SH('verify#reuse', 'Reuse verify', `
END=$(date +%s); echo "END_EPOCH=$END"
corral api GET /p/${pid2}/dind/status
corral api GET /p/${pid2}/dind/images
`) : ''
const end2 = parseInt((check2.match(/END_EPOCH=(\d+)/) || [])[1] || '0', 10)
const reusedBuildSeconds = start2 && end2 ? end2 - start2 : null

// ============================ PHASE 3: report ============================
phase('Report')
return {
  launched: true,
  firstLaunch: { projectId: launched.projectId, fullBuildSeconds, verdict: launched.verdict.raw },
  attempts: attemptLog.map(a => ({ attempt: a.attempt, ok: a.ok, reason: a.reason, elapsed: a.elapsed })),
  reuse: {
    projectId: pid2,
    baselineReusedFlag: reusedFlag,
    reusedBuildSeconds,
    secondLaunch: verdict2.raw || 'no verdict',
  },
  timing: {
    fullBuildSeconds,
    reusedBuildSeconds,
    speedup: fullBuildSeconds && reusedBuildSeconds ? +(fullBuildSeconds / reusedBuildSeconds).toFixed(2) : null,
    savedSeconds: fullBuildSeconds && reusedBuildSeconds ? fullBuildSeconds - reusedBuildSeconds : null,
  },
}
