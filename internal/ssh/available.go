package ssh

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// AvailableKey describes one usable SSH key found under ~/.ssh — enough for a
// picker UI to render a labeled checkbox without the user typing paths.
type AvailableKey struct {
	Name    string `json:"name"`    // basename of the private key (e.g. "github_jackrothrock")
	Path    string `json:"path"`    // absolute path to the private key
	Type    string `json:"type"`    // key type from the .pub (e.g. "ssh-ed25519"), "" if unknown
	Comment string `json:"comment"` // trailing comment from the .pub (often an email), "" if none
}

// AvailableKeys scans ~/.ssh for private keys that have a matching .pub, and
// returns them sorted by name. A key is "available" when both files exist: the
// .pub gives us type+comment to display, and the private file is what ssh-add
// actually loads. Files without a .pub, and non-key files (config, known_hosts,
// authorized_keys, *.pub themselves), are skipped.
func AvailableKeys() []AvailableKey {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	sshDir := filepath.Join(home, ".ssh")
	entries, err := os.ReadDir(sshDir)
	if err != nil {
		return nil
	}

	// Index which basenames have a .pub so we only surface real keypairs.
	pubs := map[string]bool{}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".pub") {
			pubs[strings.TrimSuffix(e.Name(), ".pub")] = true
		}
	}

	var out []AvailableKey
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || strings.HasSuffix(name, ".pub") {
			continue
		}
		if !pubs[name] {
			continue // no matching .pub → not a key we can describe/offer
		}
		if isNonKeyFile(name) {
			continue
		}
		typ, comment := parsePub(filepath.Join(sshDir, name+".pub"))
		out = append(out, AvailableKey{
			Name:    name,
			Path:    filepath.Join(sshDir, name),
			Type:    typ,
			Comment: comment,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// isNonKeyFile filters the well-known non-key files that can still have odd
// siblings, defensively (they won't have a .pub anyway, but be explicit).
func isNonKeyFile(name string) bool {
	switch name {
	case "config", "known_hosts", "known_hosts.old", "authorized_keys":
		return true
	}
	return false
}

// parsePub reads a "<type> <base64> [comment]" public-key file and returns the
// type and trailing comment. Missing/garbled → best-effort ("", "").
func parsePub(path string) (typ, comment string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", ""
	}
	fields := strings.Fields(strings.TrimSpace(string(data)))
	if len(fields) >= 1 {
		typ = fields[0]
	}
	if len(fields) >= 3 {
		comment = strings.Join(fields[2:], " ")
	}
	return typ, comment
}
