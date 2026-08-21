package dashboard

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"time"
)

// CmdBench implements `corral bench` — measure the CORRAL-CONTROLLED boot path as
// a plain script (NO Claude in the loop, so it times the machinery, not agent
// latency — the trap that made earlier "reuse" numbers look like minutes when the
// boot was ~seconds).
//
//	corral bench boot <projectId> [--threshold <secs>]
//
// It times: start → container up → inner-docker ready (baseline images present,
// verified). This is exactly the surface that regressed (copy-seed vs shared
// mount). It does NOT run the app's own bundle/migrate/rails — that's
// app-specific (the worker's job); this measures what corral controls. Exits
// non-zero if the total exceeds --threshold (default 60s) so it can gate a
// regression.
func CmdBench(args []string) error {
	if len(args) < 2 || args[0] != "boot" {
		fmt.Fprint(os.Stderr, "usage: corral bench boot <projectId> [--threshold <secs>]\n")
		return fmt.Errorf("missing or unknown bench command")
	}
	projectID := args[1]
	threshold := 60.0
	for i := 2; i < len(args); i++ {
		if args[i] == "--threshold" && i+1 < len(args) {
			if v, err := strconv.ParseFloat(args[i+1], 64); err == nil {
				threshold = v
			}
			i++
		}
	}

	type step struct {
		name string
		secs float64
	}
	var steps []step
	overall := timeNow()

	// 1. start the container.
	t := timeNow()
	if st, body, err := dashboardRequest("POST", "/p/"+projectID+"/start", ""); err != nil || st < 200 || st >= 300 {
		return fmt.Errorf("start failed (HTTP %d): %s%v", st, string(body), err)
	}
	steps = append(steps, step{"start-request", since(t)})

	// 2. poll until the container reports up.
	t = timeNow()
	if err := pollUntil(120*time.Second, func() bool { return projectContainerUp(projectID) }); err != nil {
		return fmt.Errorf("container did not come up within 120s")
	}
	steps = append(steps, step{"container-up", since(t)})

	// 3. poll until the inner docker is a VERIFIED reuse (baseline images present),
	// or DinD is off / no cache (then "ready" = container up, nothing to seed).
	t = timeNow()
	verified := ""
	_ = pollUntil(120*time.Second, func() bool {
		v, done := innerDockerReady(projectID)
		verified = v
		return done
	})
	steps = append(steps, step{"inner-docker-ready", since(t)})

	total := since(overall)

	// Report.
	fmt.Printf("bench boot %s\n", projectID)
	for _, s := range steps {
		fmt.Printf("  %-20s %6.1fs\n", s.name, s.secs)
	}
	fmt.Printf("  %-20s %6.1fs\n", "TOTAL", total)
	if verified != "" {
		fmt.Printf("  reuse verified: %s\n", verified)
	}
	if total > threshold {
		fmt.Printf("  ✗ SLOW: %.1fs > %.0fs threshold\n", total, threshold)
		return fmt.Errorf("boot exceeded threshold (%.1fs > %.0fs)", total, threshold)
	}
	fmt.Printf("  ✓ within %.0fs threshold\n", threshold)
	return nil
}

// projectContainerUp reports whether the project's container shows up in /status.
func projectContainerUp(projectID string) bool {
	_, body, err := dashboardRequest("GET", "/status", "")
	if err != nil {
		return false
	}
	var resp struct {
		Projects []struct {
			ID          string `json:"id"`
			ContainerUp bool   `json:"container_up"`
		} `json:"projects"`
	}
	if json.Unmarshal(body, &resp) != nil {
		return false
	}
	for _, p := range resp.Projects {
		if p.ID == projectID {
			return p.ContainerUp
		}
	}
	return false
}

// innerDockerReady reports (verified, done). done is true once the reuse verdict
// is settled: verified=="yes" (baseline images landed) OR the project has no
// cache to reuse (nothing to wait for). verified=="no" keeps polling (a copy
// seed may still be landing).
func innerDockerReady(projectID string) (string, bool) {
	_, body, err := dashboardRequest("GET", "/p/"+projectID+"/dind/status", "")
	if err != nil {
		return "", false
	}
	var s struct {
		CacheName string `json:"cacheName"`
		Reused    bool   `json:"reused"`
		Verified  string `json:"verified"`
	}
	if json.Unmarshal(body, &s) != nil {
		return "", false
	}
	if s.CacheName == "" || !s.Reused {
		return "", true // no cache attached → nothing to seed; ready.
	}
	if s.Verified == "yes" {
		return "yes", true
	}
	return s.Verified, false // "no"/"" → keep waiting for the seed to land.
}

func pollUntil(max time.Duration, cond func() bool) error {
	deadline := timeNow().Add(max)
	for timeNow().Before(deadline) {
		if cond() {
			return nil
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("timed out")
}

// timeNow/since are tiny indirections so the file has no direct Date.now-style
// calls scattered around (and are trivially stubbable if ever needed).
func timeNow() time.Time        { return time.Now() }
func since(t time.Time) float64 { return time.Since(t).Seconds() }
