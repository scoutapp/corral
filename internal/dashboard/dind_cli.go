package dashboard

import (
	"fmt"
	"os"
)

// CmdDind implements `corral dind` — inspect DinD data caches and a project's
// cache/image reuse. It drives the dashboard HTTP API, so a dashboard must be
// running.
//
//	corral dind caches                  list known DinD caches (name, size)
//	corral dind status <projectId>      is this project reusing a cache? (cheap)
//	corral dind images <projectId>      images in the project's inner docker (live)
//
// Caches are host-wide and reusable; a repo baseline is named repo-<repoId> and
// is auto-attached to new projects from that repo. See also the Config tab.
func CmdDind(args []string) error {
	if len(args) == 0 {
		return dindUsage()
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "caches":
		return dindGet("/api/dind/caches")
	case "status":
		if len(rest) < 1 {
			return fmt.Errorf("usage: corral dind status <projectId>")
		}
		return dindGet("/p/" + rest[0] + "/dind/status")
	case "images":
		if len(rest) < 1 {
			return fmt.Errorf("usage: corral dind images <projectId>")
		}
		return dindGet("/p/" + rest[0] + "/dind/images")
	default:
		return dindUsage()
	}
}

func dindUsage() error {
	fmt.Fprint(os.Stderr, `usage: corral dind <command>

  caches                  list known DinD data caches (name + on-disk size)
  status  <projectId>     whether this project reuses a cache (cheap; no exec)
  images  <projectId>     images in the project's inner docker (live; container must be up)

A repo baseline cache is named repo-<repoId> and auto-attaches to new projects
from that repo. Save one from a project's Config tab ("Save as repo baseline").
`)
	return fmt.Errorf("missing or unknown dind command")
}

// dindGet does a GET and prints the JSON body (or the error to stderr).
func dindGet(path string) error {
	status, body, err := dashboardRequest("GET", path, "")
	if err != nil {
		return err
	}
	return prPrintResult(status, body) // reuse the pr CLI's status-aware printer
}
