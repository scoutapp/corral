package creds

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/scoutapp/corral/internal/config"
)

// CredentialHostnames returns the sorted set of hostnames that have an injected
// credential, merged across the global + project credentials files (project
// wins per-host). These hosts MUST always be routed through mitm — the proxy
// force-mitms them regardless of the monitor-list — because that's where the
// real credential is swapped in for the container's dummy value. Only the
// hostnames are returned; secret values never leave the creds file.
func CredentialHostnames() []string {
	seen := map[string]bool{}
	for _, path := range []string{GlobalCredentialsPath(), ProjectCredentialsPath()} {
		m, err := LoadCredsMap(path)
		if err != nil {
			continue
		}
		for host := range m {
			h := strings.TrimSpace(host)
			if h != "" {
				seen[strings.ToLower(h)] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for h := range seen {
		out = append(out, h)
	}
	sort.Strings(out)
	return out
}

// WriteCredentialHostsFile writes the credentialed hostnames (one per line) to
// path, for the allowlist-proxy's --credential-hosts input. Writes an empty file
// (removing any stale entries) when there are no credentialed hosts, so a removed
// credential stops forcing mitm on the next reload.
func WriteCredentialHostsFile(path string, hosts []string) error {
	return os.WriteFile(path, []byte(strings.Join(hosts, "\n")+"\n"), 0644)
}

// GlobalCredentialsPath returns the shared, cross-project credentials file
// (~/.corral/proxy-credentials.json).
func GlobalCredentialsPath() string {
	return filepath.Join(config.CorralHome(), "proxy-credentials.json")
}

// ProjectCredentialsPath returns the per-project credentials override
// (<cwd>/.corral/project/proxy-credentials.json).
func ProjectCredentialsPath() string {
	return filepath.Join(config.GetProjectDir(), "proxy-credentials.json")
}

// LoadCredsMap reads a proxy-credentials.json file into a domain->entry map,
// returning fully-populated entries (value included). Returns an empty map (not
// an error) when the file is absent.
//
// With the keychain backend (macOS), the on-disk JSON holds only metadata (no
// "value"); this function injects each secret from the Keychain. With the file
// backend (Linux/default), the value is already inline and is returned as-is.
// It also performs a one-time HARD MIGRATION on macOS: any value still inline in
// the JSON is moved into the Keychain and the file rewritten without it.
func LoadCredsMap(path string) (map[string]map[string]string, error) {
	creds := map[string]map[string]string{}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return creds, nil
		}
		return nil, err
	}
	if len(data) == 0 {
		return creds, nil
	}
	if err := json.Unmarshal(data, &creds); err != nil {
		return nil, fmt.Errorf("invalid JSON in %s: %w", path, err)
	}

	if selectedBackend.storesInline() {
		return creds, nil // value already present in the map
	}

	// Out-of-band (keychain) backend: inject values, migrating any that are still
	// inline (a pre-Keychain file, or one written by the file backend).
	scope := scopeForPath(path)
	migrated := false
	for host, entry := range creds {
		if inline, ok := entry["value"]; ok && inline != "" {
			// Legacy inline value → move it into the backend, then strip it.
			if err := selectedBackend.setValue(scope, host, inline); err != nil {
				return nil, fmt.Errorf("migrate credential for %s into %s: %w", host, selectedBackend.name(), err)
			}
			delete(entry, "value")
			migrated = true
		}
		if v, ok, err := selectedBackend.getValue(scope, host); err != nil {
			return nil, err
		} else if ok {
			entry["value"] = v
		}
	}
	if migrated {
		// Persist the value-stripped file so the plaintext leaves disk for good.
		if err := writeCredsJSON(path, stripValues(creds)); err != nil {
			log.Printf("Warning: failed to rewrite %s after credential migration: %v", path, err)
		} else {
			log.Printf("Migrated inline credential value(s) in %s into the %s backend", path, selectedBackend.name())
		}
	}
	return creds, nil
}

// WriteCredsMap persists a domain->entry credentials map. With the file backend
// the value is written inline (0600). With the keychain backend the value is
// stored in the Keychain and the JSON is written WITHOUT it (metadata only), so
// no plaintext secret lands on disk.
func WriteCredsMap(path string, creds map[string]map[string]string) error {
	if selectedBackend.storesInline() {
		return writeCredsJSON(path, creds)
	}
	scope := scopeForPath(path)
	// Reconcile the backend against the incoming map: set present values, and
	// delete secrets for hosts no longer in the map (so unset actually removes).
	existing, _ := loadRawJSON(path)
	for host := range existing {
		if _, still := creds[host]; !still {
			_ = selectedBackend.deleteValue(scope, host)
		}
	}
	for host, entry := range creds {
		if v, ok := entry["value"]; ok && v != "" {
			if err := selectedBackend.setValue(scope, host, v); err != nil {
				return err
			}
		}
	}
	return writeCredsJSON(path, stripValues(creds))
}

// writeCredsJSON marshals + writes the map as pretty JSON (0600 — it may hold
// secrets under the file backend).
func writeCredsJSON(path string, creds map[string]map[string]string) error {
	data, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

// loadRawJSON reads the on-disk metadata map without backend value injection.
func loadRawJSON(path string) (map[string]map[string]string, error) {
	creds := map[string]map[string]string{}
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return creds, nil
	}
	_ = json.Unmarshal(data, &creds)
	return creds, nil
}

// stripValues returns a deep-ish copy of creds with the "value" field removed
// from every entry (for the keychain backend's metadata-only JSON).
func stripValues(creds map[string]map[string]string) map[string]map[string]string {
	out := make(map[string]map[string]string, len(creds))
	for host, entry := range creds {
		e := make(map[string]string, len(entry))
		for k, v := range entry {
			if k == "value" {
				continue
			}
			e[k] = v
		}
		out[host] = e
	}
	return out
}

// ResolveCredentialsFile returns a best-effort credentials path WITHOUT creating a
// temp file — used at construction time (NewCorral) where no lifecycle owner
// exists to clean up. It prefers the global file, falling back to the project file.
// startProxy re-resolves via ResolveCredentialsFileTracked, which performs the real
// per-domain merge and records the temp file for cleanup on stopProxy.
func ResolveCredentialsFile() string {
	if _, err := os.Stat(GlobalCredentialsPath()); err == nil {
		return GlobalCredentialsPath()
	}
	if _, err := os.Stat(ProjectCredentialsPath()); err == nil {
		return ProjectCredentialsPath()
	}
	return GlobalCredentialsPath()
}

// ResolveCredentialsFileTracked returns the credentials file to hand to mitmweb,
// plus the path of any TEMP file it created (empty if it returned a real file
// directly) so the caller can delete it on stopProxy.
//
// mitmweb's addon reads secret VALUES from this file. With the keychain backend
// the on-disk proxy-credentials.json holds only metadata (no values), so we must
// ALWAYS materialize a resolved temp file — even for the single-file case —
// otherwise mitmweb would see no secrets and injection would silently break. With
// the file backend, a single existing file already has inline values and is
// returned directly (only the merge case needs a temp file). The temp file is
// 0600 and exists only while the proxy runs; the durable secret store is the
// Keychain. This is deliberate (macOS has no easy user tmpfs, and the proxy holds
// the plaintext in memory to inject it regardless) — see docs/security.md.
func ResolveCredentialsFileTracked() (credsFile string, tempFile string) {
	globalPath := GlobalCredentialsPath()
	projectPath := ProjectCredentialsPath()

	_, globalErr := os.Stat(globalPath)
	_, projectErr := os.Stat(projectPath)
	globalExists := globalErr == nil
	projectExists := projectErr == nil

	// With an inline (file) backend, a single file already carries values → hand
	// it over directly; only a real merge needs a temp file.
	if selectedBackend.storesInline() {
		switch {
		case globalExists && !projectExists:
			return globalPath, ""
		case !globalExists && projectExists:
			return projectPath, ""
		case !globalExists && !projectExists:
			return globalPath, "" // canonical location for the not-found message
		}
	} else if !globalExists && !projectExists {
		return globalPath, ""
	}

	// Build the resolved (values-injected) map. LoadCredsMap pulls values from the
	// active backend (Keychain), so `merged` contains real secrets. Project wins
	// per-domain.
	merged := map[string]map[string]string{}
	if globalExists {
		if m, err := LoadCredsMap(globalPath); err != nil {
			log.Printf("Warning: %v", err)
		} else {
			for k, v := range m {
				merged[k] = v
			}
		}
	}
	if projectExists {
		if m, err := LoadCredsMap(projectPath); err != nil {
			log.Printf("Warning: %v", err)
		} else {
			for k, v := range m {
				merged[k] = v // project overrides/extends
			}
		}
	}

	data, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		log.Printf("Warning: failed to marshal resolved credentials: %v", err)
		return globalPath, ""
	}
	tmp, err := os.CreateTemp("", "corral-merged-creds-*.json")
	if err != nil {
		log.Printf("Warning: failed to create temp credentials file: %v", err)
		return globalPath, ""
	}
	if err := os.Chmod(tmp.Name(), 0600); err != nil {
		log.Printf("Warning: failed to chmod temp credentials file: %v", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		log.Printf("Warning: failed to write resolved credentials: %v", err)
		return globalPath, ""
	}
	tmp.Close()

	config.Debugf("Resolved %d credential entries (%s backend) -> %s", len(merged), selectedBackend.name(), tmp.Name())
	return tmp.Name(), tmp.Name()
}

// DummyCredValues is the set of placeholder values written by the cmdInit template.
var DummyCredValues = map[string]bool{
	"Bearer sk-ant-oat01-...":   true,
	"token gho_real_token_here": true,
}

// HasOnlyDummyCredentials returns true when the file doesn't exist, can't be
// parsed, is empty, or every credential value matches a known placeholder. Goes
// through LoadCredsMap so values are resolved from the active backend (Keychain
// values aren't inline in the JSON).
func HasOnlyDummyCredentials(credsPath string) bool {
	creds, err := LoadCredsMap(credsPath)
	if err != nil || len(creds) == 0 {
		return true
	}
	for _, entry := range creds {
		if !DummyCredValues[entry["value"]] {
			return false
		}
	}
	return true
}
