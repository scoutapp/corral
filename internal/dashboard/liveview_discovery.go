package dashboard

import (
	"context"
	"net/http"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/scoutapp/corral/internal/session"
)

// Live View port discovery (#6, PR 2). Lists the TCP ports currently listening
// inside a project's container so the Live View tab can offer them as one-click
// targets. We read the kernel's listening-socket table with `ss -ltn` (one exec,
// exact, ~tens of ms) rather than scanning ports (slow and only ever a probe).
//
// Because the reverse-proxy dials the DinD-host namespace (the outer container's
// view), the ports `ss` sees there are exactly the ones the proxy can reach —
// including inner-DinD containers published with `-p` (their docker-proxy
// listeners show up here). An inner service that wasn't `-p`-published won't
// appear, which is correct: it also wouldn't be reachable. So discovery and
// reachability agree.
//
//	GET /p/<id>/live-ports -> { ports: [3000, 5173, …] }

// liveInternalPorts are corral's own in-container listeners, filtered out of
// discovery so the user only sees their app ports:
//   - 3128 explicit allowlist-proxy (HTTP CONNECT)
//   - 3129 transparent allowlist-proxy (DinD REDIRECT target)
//   - 53   DNS
var liveInternalPorts = map[int]bool{3128: true, 3129: true, 53: true}

func (d *dashboardServer) handleLivePorts(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	workspace, err := lookupWorkspaceByID(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	container := session.ContainerNameForWorkspace(workspace)
	if !session.DockerContainerRunning(container) {
		// Not an error — the tab shows an appropriate empty/"start the project"
		// state. Return an empty list so the client renders cleanly.
		writeJSON(w, map[string]any{"ports": []int{}, "container_up": false})
		return
	}
	ports := discoverListeningPorts(container)
	writeJSON(w, map[string]any{"ports": ports, "container_up": true})
}

// discoverListeningPorts execs `ss -ltnH` in the container and returns the
// distinct listening TCP ports, corral-internal ones filtered out, sorted. A
// failure (ss missing, exec error) yields an empty slice — discovery is a
// convenience over the always-available free-text port box, never load-bearing.
func discoverListeningPorts(container string) []int {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", "exec", container, "ss", "-ltnH").Output()
	if err != nil {
		return []int{}
	}
	seen := map[int]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		// ss -ltnH columns: State Recv-Q Send-Q Local-Address:Port Peer:Port …
		if len(fields) < 4 {
			continue
		}
		local := fields[3]
		i := strings.LastIndexByte(local, ':')
		if i < 0 {
			continue
		}
		port, err := strconv.Atoi(local[i+1:])
		if err != nil || port < 1 || port > 65535 {
			continue
		}
		if liveInternalPorts[port] {
			continue
		}
		seen[port] = true
	}
	ports := make([]int, 0, len(seen))
	for p := range seen {
		ports = append(ports, p)
	}
	sort.Ints(ports)
	return ports
}
