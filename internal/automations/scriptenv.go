package automations

import (
	"regexp"
	"sort"
	"strings"
)

// Script secret detection.
//
// A bash script is full of $VARs: internal ones it assigns itself (OP, LIMIT, Q,
// BASE…), corral run-context ones (CORRAL_*), standard shell vars (HOME, PATH…),
// and — the ones we care about — SECRETS the user must supply from outside
// (FRESHDESK_API_KEY, FRESHDESK_DOMAIN…). DetectInjectableVars heuristically picks
// the last group: UPPER_CASE vars that are READ but never ASSIGNED in the script,
// excluding CORRAL_* and a denylist of shell builtins. The result seeds the
// Scripts UI, where the user curates (add/remove) and fills values — so a
// false positive/negative is recoverable, not fatal.

// varRefRe matches a variable REFERENCE: $NAME or ${NAME...}. The ${...} form may
// carry a suffix (:-default, //, [idx]); we take the leading NAME.
var varRefRe = regexp.MustCompile(`\$\{?([A-Za-z_][A-Za-z0-9_]*)`)

// assignRe matches a variable ASSIGNMENT at a line/command boundary: NAME=,
// export NAME=, local NAME=, declare NAME=, readonly NAME=.
var assignRe = regexp.MustCompile(`(?m)(?:^|;|\||&|\bexport\s+|\blocal\s+|\bdeclare\s+|\breadonly\s+)\s*([A-Za-z_][A-Za-z0-9_]*)=`)

// loopReadRe matches vars BOUND by the script itself: `for NAME in`, `read NAME`,
// `read -r NAME1 NAME2`, `while read NAME`. These are assignments, not injected.
var loopReadRe = regexp.MustCompile(`(?m)\b(?:for|read(?:\s+-\w+)*)\s+([A-Za-z_][A-Za-z0-9_]*(?:\s+[A-Za-z_][A-Za-z0-9_]*)*)`)

// shellBuiltins are standard env vars a script reads but the user never injects.
var shellBuiltins = map[string]bool{
	"HOME": true, "PATH": true, "PWD": true, "OLDPWD": true, "USER": true,
	"LOGNAME": true, "SHELL": true, "TERM": true, "LANG": true, "LC_ALL": true,
	"IFS": true, "PS1": true, "PS2": true, "TMPDIR": true, "HOSTNAME": true,
	"UID": true, "EUID": true, "PPID": true, "RANDOM": true, "SECONDS": true,
	"LINENO": true, "BASH": true, "BASH_VERSION": true, "SHLVL": true,
	"COLUMNS": true, "LINES": true, "EDITOR": true, "VISUAL": true, "PAGER": true,
}

// DetectInjectableVars returns the sorted set of UPPER_CASE variables a script
// READS but does not assign, excluding CORRAL_* context vars and shell builtins —
// i.e. the vars a user likely needs to inject (secrets/config). Heuristic; the UI
// lets the user curate the result.
func DetectInjectableVars(script string) []string {
	assigned := assignedVars(script)

	seen := map[string]bool{}
	var out []string
	for _, m := range varRefRe.FindAllStringSubmatch(script, -1) {
		name := m[1]
		if !isUpperVar(name) {
			continue // require UPPER_CASE (lowercase = almost always script-internal)
		}
		if strings.HasPrefix(name, "CORRAL_") || shellBuiltins[name] || assigned[name] {
			continue
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// assignedVars is the set of names the script assigns itself (so they're not
// "injected"): NAME= forms, and loop/read bindings.
func assignedVars(script string) map[string]bool {
	assigned := map[string]bool{}
	for _, m := range assignRe.FindAllStringSubmatch(script, -1) {
		assigned[m[1]] = true
	}
	for _, m := range loopReadRe.FindAllStringSubmatch(script, -1) {
		for _, name := range strings.Fields(m[1]) {
			assigned[name] = true
		}
	}
	return assigned
}

// isUpperVar reports whether name is an UPPER_CASE identifier (letters here are
// all upper, digits/underscore allowed, at least one letter). "FRESHDESK_API_KEY"
// yes; "OP" yes; "op" no; "_FOO" ok; "F1" yes.
func isUpperVar(name string) bool {
	hasLetter := false
	for _, r := range name {
		switch {
		case r >= 'A' && r <= 'Z':
			hasLetter = true
		case (r >= '0' && r <= '9') || r == '_':
		default:
			return false // a lowercase letter → not an UPPER_CASE var
		}
	}
	return hasLetter
}
