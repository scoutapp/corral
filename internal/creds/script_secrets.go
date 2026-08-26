package creds

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/scoutapp/corral/internal/config"
)

// ensureScriptSecretsDir creates ~/.corral/script-secrets (0700 — it may hold
// inline values under the file backend).
func ensureScriptSecretsDir() error {
	return os.MkdirAll(scriptSecretsDir(), 0o700)
}

// Script secrets: per-script env-var secrets a bash automation needs at run time
// (e.g. FRESHDESK_API_KEY). Stored with the SAME backend as proxy credentials —
// Keychain on macOS (value out of the plaintext file), file inline on Linux —
// under a "script:<id>" scope so scripts don't collide. The on-disk metadata file
// is ~/.corral/script-secrets/<id>.json, mapping VAR name -> entry
// {"kind":"env","name":<VAR>,"value":<secret?>} (value present only under the
// file backend). This reuses LoadCredsMap/WriteCredsMap, whose scope derives from
// the path (see scopeForPath / scriptIDFromPath).

// scriptSecretsDir is ~/.corral/script-secrets.
func scriptSecretsDir() string {
	return filepath.Join(config.CorralHome(), "script-secrets")
}

// ScriptSecretsPath is the metadata file for one script's secrets.
func ScriptSecretsPath(actionID int64) string {
	return filepath.Join(scriptSecretsDir(), strconv.FormatInt(actionID, 10)+".json")
}

// scriptIDFromPath recognizes a script-secrets file path and returns its id, so
// scopeForPath can map it to "script:<id>". Returns ok=false for other paths.
func scriptIDFromPath(path string) (string, bool) {
	if filepath.Dir(path) != scriptSecretsDir() {
		return "", false
	}
	base := strings.TrimSuffix(filepath.Base(path), ".json")
	if base == "" {
		return "", false
	}
	if _, err := strconv.ParseInt(base, 10, 64); err != nil {
		return "", false
	}
	return base, true
}

// LoadScriptSecrets returns the script's secret map (VAR -> entry, value injected
// from the backend). Empty map if none set.
func LoadScriptSecrets(actionID int64) (map[string]map[string]string, error) {
	return LoadCredsMap(ScriptSecretsPath(actionID))
}

// WriteScriptSecrets persists the script's secrets (values → backend; metadata →
// the per-script JSON). Reconciles deletions like WriteCredsMap.
func WriteScriptSecrets(actionID int64, secrets map[string]map[string]string) error {
	if err := ensureScriptSecretsDir(); err != nil {
		return err
	}
	return WriteCredsMap(ScriptSecretsPath(actionID), secrets)
}

// ScriptSecretEnv resolves a script's secrets into "VAR=value" env entries for
// the executor. Only entries with a non-empty value are returned.
func ScriptSecretEnv(actionID int64) ([]string, error) {
	m, err := LoadScriptSecrets(actionID)
	if err != nil {
		return nil, err
	}
	var out []string
	for name, entry := range m {
		if v := entry["value"]; v != "" {
			out = append(out, name+"="+v)
		}
	}
	return out, nil
}

// ScriptSecretValues returns just the non-empty secret VALUES for a script — used
// by the host-claude redactor to strip them from transcripts.
func ScriptSecretValues(actionID int64) ([]string, error) {
	m, err := LoadScriptSecrets(actionID)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, entry := range m {
		if v := entry["value"]; v != "" {
			out = append(out, v)
		}
	}
	return out, nil
}

// AllScriptSecretValues returns every script's secret values, keyed by action id
// (as a string) — for the host-claude redactor to strip the whole set from
// transcripts. Best-effort: unreadable files are skipped.
func AllScriptSecretValues() map[string][]string {
	out := map[string][]string{}
	entries, err := os.ReadDir(scriptSecretsDir())
	if err != nil {
		return out // no dir yet → no script secrets
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		id, ok := scriptIDFromPath(filepath.Join(scriptSecretsDir(), e.Name()))
		if !ok {
			continue
		}
		aid, perr := strconv.ParseInt(id, 10, 64)
		if perr != nil {
			continue
		}
		if vals, verr := ScriptSecretValues(aid); verr == nil && len(vals) > 0 {
			out[id] = vals
		}
	}
	return out
}
