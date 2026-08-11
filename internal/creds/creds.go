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
// (~/.sandclaude/proxy-credentials.json).
func GlobalCredentialsPath() string {
	return filepath.Join(config.CorralHome(), "proxy-credentials.json")
}

// ProjectCredentialsPath returns the per-project credentials override
// (<cwd>/.sandclaude/project/proxy-credentials.json).
func ProjectCredentialsPath() string {
	return filepath.Join(config.GetProjectDir(), "proxy-credentials.json")
}

// LoadCredsMap reads a proxy-credentials.json file into a domain->entry map.
// Returns an empty map (not an error) when the file is absent.
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
	return creds, nil
}

// WriteCredsMap writes a domain->entry credentials map as pretty JSON (0600 —
// it holds secrets).
func WriteCredsMap(path string, creds map[string]map[string]string) error {
	data, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

// ResolveCredentialsFile returns a best-effort credentials path WITHOUT creating a
// temp file — used at construction time (NewSandClaude) where no lifecycle owner
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

// ResolveCredentialsFileTracked is like ResolveCredentialsFile but also returns the
// path of any temp file it created (empty string if it returned a real file directly),
// so the caller can delete it on shutdown.
func ResolveCredentialsFileTracked() (credsFile string, tempFile string) {
	globalPath := GlobalCredentialsPath()
	projectPath := ProjectCredentialsPath()

	_, globalErr := os.Stat(globalPath)
	_, projectErr := os.Stat(projectPath)
	globalExists := globalErr == nil
	projectExists := projectErr == nil

	switch {
	case globalExists && !projectExists:
		return globalPath, ""
	case !globalExists && projectExists:
		return projectPath, ""
	case !globalExists && !projectExists:
		// Neither exists — return the global path so downstream "file not found"
		// messaging points at the canonical location.
		return globalPath, ""
	}

	// Both exist: merge, project wins per-domain.
	global, err := LoadCredsMap(globalPath)
	if err != nil {
		log.Printf("Warning: %v — falling back to project credentials only", err)
		return projectPath, ""
	}
	project, err := LoadCredsMap(projectPath)
	if err != nil {
		log.Printf("Warning: %v — falling back to global credentials only", err)
		return globalPath, ""
	}

	merged := make(map[string]map[string]string, len(global)+len(project))
	for k, v := range global {
		merged[k] = v
	}
	for k, v := range project {
		merged[k] = v // project overrides/extends
	}

	data, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		log.Printf("Warning: failed to marshal merged credentials: %v — using global only", err)
		return globalPath, ""
	}

	tmp, err := os.CreateTemp("", "sandclaude-merged-creds-*.json")
	if err != nil {
		log.Printf("Warning: failed to create temp credentials file: %v — using global only", err)
		return globalPath, ""
	}
	if err := os.Chmod(tmp.Name(), 0600); err != nil {
		log.Printf("Warning: failed to chmod temp credentials file: %v", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		log.Printf("Warning: failed to write merged credentials: %v — using global only", err)
		return globalPath, ""
	}
	tmp.Close()

	config.Debugf("Merged %d global + %d project credential entries -> %s", len(global), len(project), tmp.Name())
	return tmp.Name(), tmp.Name()
}

// DummyCredValues is the set of placeholder values written by the cmdInit template.
var DummyCredValues = map[string]bool{
	"Bearer sk-ant-oat01-...":   true,
	"token gho_real_token_here": true,
}

// HasOnlyDummyCredentials returns true when the file doesn't exist, can't be
// parsed, is empty, or every credential value matches a known placeholder.
func HasOnlyDummyCredentials(credsPath string) bool {
	data, err := os.ReadFile(credsPath)
	if err != nil {
		return true
	}
	creds := map[string]map[string]string{}
	if err := json.Unmarshal(data, &creds); err != nil {
		return true
	}
	if len(creds) == 0 {
		return true
	}
	for _, entry := range creds {
		if !DummyCredValues[entry["value"]] {
			return false
		}
	}
	return true
}
