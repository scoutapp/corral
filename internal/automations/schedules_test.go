package automations

import (
	"testing"
	"time"
)

func newFlow(t *testing.T, svc *Service) int64 {
	t.Helper()
	f, err := svc.CreateFlow(Flow{Name: "sched-flow"})
	if err != nil {
		t.Fatal(err)
	}
	return f.ID
}

func TestSetAndGetSchedule(t *testing.T) {
	svc := newService(t)
	fid := newFlow(t, svc)

	sc, err := svc.SetSchedule(fid, 86400, true, true)
	if err != nil {
		t.Fatal(err)
	}
	if sc.CadenceSeconds != 86400 || !sc.CatchUp || !sc.Enabled {
		t.Errorf("schedule wrong: %+v", sc)
	}
	// Upsert: changing cadence keeps the row (one per flow).
	sc2, _ := svc.SetSchedule(fid, 3600, false, true)
	if sc2.ID != sc.ID || sc2.CadenceSeconds != 3600 || sc2.CatchUp {
		t.Errorf("upsert wrong: %+v (was %+v)", sc2, sc)
	}

	got, err := svc.ScheduleForFlow(fid)
	if err != nil || got.CadenceSeconds != 3600 {
		t.Errorf("get after upsert wrong: %+v err=%v", got, err)
	}
}

func TestSetScheduleValidatesCadence(t *testing.T) {
	svc := newService(t)
	fid := newFlow(t, svc)
	if _, err := svc.SetSchedule(fid, 0, true, true); err == nil {
		t.Error("expected error for cadence 0")
	}
}

func TestDeleteSchedule(t *testing.T) {
	svc := newService(t)
	fid := newFlow(t, svc)
	svc.SetSchedule(fid, 60, true, true)
	if err := svc.DeleteSchedule(fid); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ScheduleForFlow(fid); err == nil {
		t.Error("expected no schedule after delete")
	}
	// Deleting a missing schedule is not an error.
	if err := svc.DeleteSchedule(fid); err != nil {
		t.Errorf("delete of missing schedule should be a no-op, got %v", err)
	}
}

func TestDueNeverRunFires(t *testing.T) {
	svc := newService(t)
	fid := newFlow(t, svc)
	svc.SetSchedule(fid, 3600, true, true) // last_run_at is NULL

	fire, skip, err := svc.DueSchedules(time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if len(fire) != 1 || len(skip) != 0 {
		t.Errorf("never-run schedule should fire once: fire=%d skip=%d", len(fire), len(skip))
	}
}

func TestDueRespectsCadence(t *testing.T) {
	svc := newService(t)
	fid := newFlow(t, svc)
	sc, _ := svc.SetSchedule(fid, 3600, true, true) // hourly

	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	svc.MarkRan(sc.ID, now) // just ran

	// 30 min later: not due.
	if fire, _, _ := svc.DueSchedules(now.Add(30 * time.Minute)); len(fire) != 0 {
		t.Errorf("30min into an hourly cadence should NOT be due, got %d", len(fire))
	}
	// 61 min later: due.
	if fire, _, _ := svc.DueSchedules(now.Add(61 * time.Minute)); len(fire) != 1 {
		t.Errorf("61min into an hourly cadence should be due, got %d", len(fire))
	}
}

// The core drift-tolerance property: a schedule overdue by MANY intervals fires
// exactly ONCE (fire-once-on-wake), not once per missed interval.
func TestDueFiresOnceWhenOverdueByDays(t *testing.T) {
	svc := newService(t)
	fid := newFlow(t, svc)
	sc, _ := svc.SetSchedule(fid, 86400, true, true) // daily

	base := time.Date(2026, 8, 10, 6, 0, 0, 0, time.UTC)
	svc.MarkRan(sc.ID, base)

	// Machine off for 3 days, wakes.
	wake := base.Add(3*24*time.Hour + time.Hour)
	fire, _, _ := svc.DueSchedules(wake)
	if len(fire) != 1 {
		t.Fatalf("overdue-by-3-days daily flow should fire ONCE, got %d", len(fire))
	}
	// After firing (MarkRan at wake), it's not due again until the next day.
	svc.MarkRan(fire[0].ID, wake)
	if again, _, _ := svc.DueSchedules(wake.Add(time.Hour)); len(again) != 0 {
		t.Errorf("should not re-fire right after running, got %d", len(again))
	}
}

// catch_up=false: an overdue schedule is SKIPPED (realigned, not fired).
func TestDueSkipsWhenNotCatchingUp(t *testing.T) {
	svc := newService(t)
	fid := newFlow(t, svc)
	sc, _ := svc.SetSchedule(fid, 3600, false /*catchUp*/, true)

	base := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	svc.MarkRan(sc.ID, base)

	overdue := base.Add(5 * time.Hour)
	fire, skip, _ := svc.DueSchedules(overdue)
	if len(fire) != 0 || len(skip) != 1 {
		t.Errorf("catch_up=false overdue should skip, not fire: fire=%d skip=%d", len(fire), len(skip))
	}
}

func TestDisabledScheduleNeverDue(t *testing.T) {
	svc := newService(t)
	fid := newFlow(t, svc)
	svc.SetSchedule(fid, 60, true, false /*enabled*/) // never run + disabled

	if fire, skip, _ := svc.DueSchedules(time.Now().UTC()); len(fire) != 0 || len(skip) != 0 {
		t.Errorf("disabled schedule should never be due: fire=%d skip=%d", len(fire), len(skip))
	}
}
