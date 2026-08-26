package automations

import (
	"reflect"
	"testing"
)

// The real Freshdesk script (auto_action id 3) — the anchor case. It reads
// FRESHDESK_API_KEY + FRESHDESK_DOMAIN (injected), plus run-context vars ($op,
// $id, $status, $limit — lowercase, script-internal), and assigns many uppercase
// internal vars (OP, LIMIT, Q, BASE, AGENT_ID, API_KEY, DOMAIN, CRED).
const freshdeskScript = `#!/usr/bin/env bash
set -euo pipefail
CRED="$HOME/.corral/freshdesk-credentials.json"
API_KEY="${FRESHDESK_API_KEY:-}"
DOMAIN="${FRESHDESK_DOMAIN:-}"
if [ -z "$API_KEY" ] && [ -f "$CRED" ]; then
  API_KEY=$(jq -r '.api_key // empty' "$CRED")
  [ -z "$DOMAIN" ] && DOMAIN=$(jq -r '.domain // empty' "$CRED")
fi
[ -z "$DOMAIN" ] && DOMAIN="scoutapm"
BASE="https://$DOMAIN.freshdesk.com/api/v2"
OP="${op:-mine}"
LIMIT="${limit:-30}"
case "$OP" in
  mine)
    AGENT_ID=$(fd /agents/me | jq -r '.id')
    Q="agent_id:$AGENT_ID"
    ;;
esac
`

func TestDetectInjectableVars_Freshdesk(t *testing.T) {
	got := DetectInjectableVars(freshdeskScript)
	want := []string{"FRESHDESK_API_KEY", "FRESHDESK_DOMAIN"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DetectInjectableVars = %v, want %v", got, want)
	}
}

func TestDetectInjectableVars_ExcludesInternalAndContext(t *testing.T) {
	script := `
set -e
FOO=bar                       # assigned → not injected
export BAZ=1                  # assigned
for ITEM in a b c; do :; done # loop-bound
read USERINPUT                # read-bound
echo "$FOO $BAZ $ITEM $USERINPUT $HOME $PATH"   # all excluded
echo "$CORRAL_PR_NUMBER"      # context var, excluded
echo "$lower_case"            # lowercase, excluded
echo "$REAL_SECRET $ANOTHER_KEY"   # read, never assigned → INJECTED
`
	got := DetectInjectableVars(script)
	want := []string{"ANOTHER_KEY", "REAL_SECRET"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DetectInjectableVars = %v, want %v", got, want)
	}
}

func TestDetectInjectableVars_Empty(t *testing.T) {
	if got := DetectInjectableVars("echo hello; echo $HOME"); len(got) != 0 {
		t.Errorf("expected no injectable vars, got %v", got)
	}
}

func TestIsUpperVar(t *testing.T) {
	cases := map[string]bool{
		"FRESHDESK_API_KEY": true, "OP": true, "F1": true, "_FOO": true,
		"op": false, "lowerCase": false, "Mixed_CASE": false, "123": false,
	}
	for in, want := range cases {
		if got := isUpperVar(in); got != want {
			t.Errorf("isUpperVar(%q) = %v, want %v", in, got, want)
		}
	}
}
