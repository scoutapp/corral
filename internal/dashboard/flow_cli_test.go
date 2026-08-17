package dashboard

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/scoutapp/corral/internal/automations"
)

// pointCLIAt makes the dashboard request helpers target srv via env overrides.
func pointCLIAt(t *testing.T, srv *httptest.Server) {
	t.Helper()
	t.Setenv("CORRAL_DASH_URL", srv.URL)
	t.Setenv("CORRAL_DASH_TOKEN", "sess")
}

func newFlowCLIServer(t *testing.T) (*httptest.Server, *automations.Service) {
	t.Helper()
	t.Setenv("CORRAL_HOME", t.TempDir())
	d := newDashboardServer("sess")
	d.apiToken = "apitok"
	srv := httptest.NewServer(d.routes())
	s, err := d.getStore()
	if err != nil {
		t.Fatal(err)
	}
	return srv, automations.New(s)
}

func TestResolveFlowIDByName(t *testing.T) {
	srv, svc := newFlowCLIServer(t)
	defer srv.Close()
	pointCLIAt(t, srv)

	f, _ := svc.CreateFlow(automations.Flow{Name: "nightly-triage"})

	id, err := resolveFlowID("nightly-triage")
	if err != nil {
		t.Fatal(err)
	}
	if id != f.ID {
		t.Errorf("resolved id = %d, want %d", id, f.ID)
	}
	// Case-insensitive.
	if id2, err := resolveFlowID("NIGHTLY-TRIAGE"); err != nil || id2 != f.ID {
		t.Errorf("case-insensitive lookup failed: %d %v", id2, err)
	}
	// Unknown name errors clearly.
	if _, err := resolveFlowID("does-not-exist"); err == nil {
		t.Error("expected error for unknown flow name")
	}
}

func TestFlowListCLI(t *testing.T) {
	srv, svc := newFlowCLIServer(t)
	defer srv.Close()
	pointCLIAt(t, srv)

	svc.CreateFlow(automations.Flow{Name: "alpha"})
	svc.CreateFlow(automations.Flow{Name: "beta"})

	out := captureStdout(t, func() {
		if err := cmdFlowList(nil); err != nil {
			t.Errorf("list: %v", err)
		}
	})
	if !strings.Contains(out, "alpha") || !strings.Contains(out, "beta") {
		t.Errorf("flow list missing entries:\n%s", out)
	}
}

func TestFlowRunCLI(t *testing.T) {
	srv, svc := newFlowCLIServer(t)
	defer srv.Close()
	pointCLIAt(t, srv)

	// A flow with no steps runs ok (empty pipeline).
	svc.CreateFlow(automations.Flow{Name: "empty-flow"})

	out := captureStdout(t, func() {
		if err := cmdFlowRun([]string{"empty-flow"}); err != nil {
			t.Errorf("run: %v", err)
		}
	})
	if !strings.Contains(out, "empty-flow") || !strings.Contains(out, "ok") {
		t.Errorf("flow run output unexpected:\n%s", out)
	}
}
