package dashboard

import (
	"context"
	"time"

	"github.com/scoutapp/corral/internal/applog"
	"github.com/scoutapp/corral/internal/automations"
)

// scheduleTickInterval is how often the pseudo-cron loop reconciles schedules.
// ~1 minute: fine-grained enough that a due flow fires promptly, cheap enough to
// run forever (it's a handful of indexed rows).
const scheduleTickInterval = time.Minute

// startScheduleTick runs the flow scheduler: on start, then every minute, it
// reconciles each schedule's last_run_at against its cadence and fires the ones
// that are due. Drift-tolerant — a flow overdue by days fires ONCE, not once per
// missed interval. Modeled on startLogRetention.
//
// Scheduled runs are NOT subject to the API-writes gate: the user authorized the
// flow when they scheduled it (a browser action); the tick just fires what they
// already approved.
func (d *dashboardServer) startScheduleTick() {
	tick := func() { d.runDueSchedules(time.Now().UTC()) }
	tick()
	go func() {
		t := time.NewTicker(scheduleTickInterval)
		defer t.Stop()
		for range t.C {
			tick()
		}
	}()
}

// runDueSchedules fires every due schedule once and realigns skipped ones. Best-
// effort: a failure on one schedule doesn't stop the others, and nothing here
// blocks the tick loop meaningfully (flows run inline but are typically short;
// long ones are their own concern).
func (d *dashboardServer) runDueSchedules(now time.Time) {
	s, err := d.getStore()
	if err != nil {
		return
	}
	svc := automations.New(s)
	fire, skip, err := svc.DueSchedules(now)
	if err != nil {
		return
	}

	// Skipped (catch_up=false, missed window): realign the clock without running.
	for _, sc := range skip {
		_ = svc.MarkRan(sc.ID, now)
	}

	if len(fire) == 0 {
		return
	}
	runner := d.automationsRunner(svc)
	for _, sc := range fire {
		// Stamp last_run_at BEFORE running so a slow/failing flow can't be double-
		// fired by an overlapping tick, and so drift is measured from the fire time.
		_ = svc.MarkRan(sc.ID, now)
		res, rerr := runner.RunFlow(context.Background(), sc.FlowID, automations.TriggerSchedule, automations.RunContext{})
		d.logScheduledRun(sc, res, rerr)
	}
}

// logScheduledRun records a scheduled fire in the activity log so it's visible
// (and links to the run detail via run_id).
func (d *dashboardServer) logScheduledRun(sc automations.Schedule, res automations.FlowResult, rerr error) {
	l := d.applog()
	if rerr != nil {
		l.Errorf(applog.CatAutomation, "schedule.run",
			applog.Fmt("Scheduled flow %d failed to start", sc.FlowID), rerr,
			map[string]any{"flowId": sc.FlowID, "scheduleId": sc.ID})
		return
	}
	level := applog.LevelInfo
	if res.Status == automations.StatusError {
		level = applog.LevelError
	}
	l.Log(applog.Entry{
		Level: level, Category: applog.CatAutomation, Event: "schedule.run",
		Message: applog.Fmt("Scheduled flow %d — %s", sc.FlowID, res.Status),
		Status:  res.Status, RunID: res.RunID,
		Meta: map[string]any{"flowId": sc.FlowID, "scheduleId": sc.ID, "steps": len(res.Steps)},
	})
}
