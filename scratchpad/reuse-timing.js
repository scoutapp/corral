export const meta = {
  name: 'apm-reuse-timing',
  description: 'Measure a reused boot against the EXISTING 14.9GB apm baseline now that big baselines auto-attach in SHARED mode (zero copy). Compare to the copy-mode run (2642s).',
  phases: [{ title: 'Create+attach' }, { title: 'Boot' }, { title: 'Report' }],
}

const REPO_ID = '3315137c0903'
const CREATE_BODY = JSON.stringify({
  mode: 'clone',
  repos: [{ repoId: REPO_ID, branch: '5669-agent-key-grace-period' }],
  name: 'apm-reuse',
  dind: true,
  source: { kind: 'pr', repo_id: REPO_ID, number: 5670, url: 'https://github.com/scoutapp/apm/pull/5670', title: 'Grace period for Scout Key rotation' },
})

const SH = (label, phase, cmd) =>
  agent(`Run this shell and return its FULL stdout+stderr verbatim (no commentary):\n\n\`\`\`bash\n${cmd}\n\`\`\``,
    { label, phase, agentType: 'general-purpose' })

phase('Create+attach')
const created = await SH('create', 'Create+attach', `
set -e
# Clean any prior apm-reuse project.
for pid in $(corral api GET /status | python3 -c "import sys,json;[print(p['id']) for p in json.load(sys.stdin).get('projects',[]) if 'apm-reuse' in p['name']]"); do
  corral api POST /p/$pid/stop 2>&1 | tail -1; corral api POST /p/$pid/remove 2>&1 | tail -1
done
sleep 2
echo "=== baseline size (should be ~15G → expect SHARED auto-attach) ==="
docker run --rm -v corral-dind-cache-repo-${REPO_ID}:/b alpine:3.20 du -sh /b 2>/dev/null | head -1
START=$(date +%s); echo "START_EPOCH=$START"
RESP=$(corral api POST /projects/create -d '${CREATE_BODY}'); echo "$RESP"
PID=$(echo "$RESP" | python3 -c "import sys,json;print(json.load(sys.stdin).get('id',''))"); echo "PROJECT_ID=$PID"
echo "=== attached cache + MODE (expect mode=shared for the big baseline) ==="
python3 -c "import json; d=json.load(open('$HOME/.corral/workspaces/apm-reuse/.corral/project/config.json')); print('dind_cache=', d.get('dind_cache'))" 2>&1 || true
corral api POST /p/$PID/start 2>&1 | tail -2
echo "=== dind status right after start (shared = usable immediately, no copy) ==="
corral api GET /p/$PID/dind/status
`)
const pid = (created.match(/PROJECT_ID=(\w+)/) || [])[1]
const startEpoch = parseInt((created.match(/START_EPOCH=(\d+)/) || [])[1] || '0', 10)
const modeShared = /"mode":\s*"shared"/.test(created) || /'mode':\s*'shared'/.test(created)

phase('Boot')
let boot = ''
if (pid) {
  boot = await agent([
    'Boot apm PR #5670 in Corral project ' + pid + ' (repo scoutapp/apm) so it serves. Run inside the sandbox (docker exec / corral project exec).',
    'The inner docker was attached to a large repo baseline in SHARED mode — its images + named volumes (apm-bundle, apm-pgdata, apm-prepared:latest) should ALREADY be present with ZERO copy. VERIFY and REUSE them; do NOT reinstall/re-migrate/rebuild what is present.',
    'TIME each step: print "STEP <name> <secs>s" (image-check, services-up, deps, db-migrate, warmup, rails-boot). End with a TIMING block.',
    '1. docker images / docker volume ls — confirm apm-prepared/ruby/pg/redis + apm-bundle/apm-pgdata are present (report REUSED vs REBUILT for each).',
    '2. Start postgres (mount apm-pgdata) + redis. Clear HTTP_PROXY/HTTPS_PROXY; SCOUT_MONITOR=false.',
    '3. If apm-bundle has gems, SKIP bundle. If apm-pgdata already migrated (previous_key column exists), SKIP migrate.',
    '4. Start rails on 0.0.0.0 (prefer running apm-prepared:latest); curl a HUMAN-VIEWABLE page (the app UI/login/dashboard, e.g. /users/sign_in) to confirm it renders — NOT /health_check or an API endpoint.',
    '5. corral api PUT /p/' + pid + '/live-port -d \'{"port":<PORT>,"path":"<human-viewable route>"}\'. path is the URL route to a page a person wants to see; never a health/liveness probe.',
    'FIRST line of your report: "LAUNCH: OK port=<PORT> path=<path>" or "LAUNCH: FAIL reason=<...>", then the TIMING block, then REUSED-vs-REBUILT summary.',
  ].join('\n'), { label: 'boot', phase: 'Boot', agentType: 'general-purpose' })
}

phase('Report')
const check = pid ? await SH('verify', 'Report', `
END=$(date +%s); echo "END_EPOCH=$END"
corral api GET /p/${pid}/dind/status
`) : ''
const endEpoch = parseInt((check.match(/END_EPOCH=(\d+)/) || [])[1] || '0', 10)
const reusedBuildSeconds = startEpoch && endEpoch ? endEpoch - startEpoch : null
const verdict = (boot.match(/LAUNCH:\s*(OK|FAIL)[^\n]*/i) || ['(no verdict)'])[0]
const verified = /"verified":\s*"yes"/.test(check) ? 'yes' : /"verified":\s*"no"/.test(check) ? 'no' : 'unknown'

return {
  projectId: pid,
  autoAttachedShared: modeShared,
  reusedBuildSeconds,
  verifiedReuse: verified,
  verdict,
  compareTo: { copyModeRun2Seconds: 2642, cleanBuildRun2Seconds: 1741 },
  faster: reusedBuildSeconds ? { vsCopyMode: +(2642 / reusedBuildSeconds).toFixed(2) + 'x', vsCleanBuild: +(1741 / reusedBuildSeconds).toFixed(2) + 'x' } : null,
}
