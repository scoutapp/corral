package automations

import (
	"database/sql"
	"fmt"
	"time"
)

// TriggerSchedule marks a run fired by the pseudo-cron scheduler (see the
// dashboard's schedule tick). Distinct from manual/api/hook so run history shows
// why a flow ran.
const TriggerSchedule = "schedule"

// scheduleTimeFmt is the UTC layout stored in auto_schedules.last_run_at. Matches
// the migration's strftime and applog's ts format so comparisons are apples to
// apples.
const scheduleTimeFmt = "2006-01-02T15:04:05Z"

// Schedule is a flow's pseudo-cron entry.
type Schedule struct {
	ID             int64  `json:"id"`
	FlowID         int64  `json:"flowId"`
	CadenceSeconds int    `json:"cadenceSeconds"`
	LastRunAt      string `json:"lastRunAt,omitempty"` // UTC ISO; "" = never
	CatchUp        bool   `json:"catchUp"`             // fire once on wake when overdue
	Enabled        bool   `json:"enabled"`
}

// SetSchedule upserts the schedule for a flow (one per flow). cadenceSeconds must
// be positive. Does not change last_run_at on update, so re-saving a cadence
// doesn't reset the clock.
func (s *Service) SetSchedule(flowID int64, cadenceSeconds int, catchUp, enabled bool) (Schedule, error) {
	if cadenceSeconds <= 0 {
		return Schedule{}, fmt.Errorf("schedule: cadenceSeconds must be > 0")
	}
	_, err := s.db.Exec(`
		INSERT INTO auto_schedules (flow_id, cadence_seconds, catch_up, enabled)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(flow_id) DO UPDATE SET
		  cadence_seconds = excluded.cadence_seconds,
		  catch_up        = excluded.catch_up,
		  enabled         = excluded.enabled,
		  updated_at      = strftime('%Y-%m-%dT%H:%M:%SZ','now')
	`, flowID, cadenceSeconds, boolToInt(catchUp), boolToInt(enabled))
	if err != nil {
		return Schedule{}, err
	}
	return s.ScheduleForFlow(flowID)
}

// DeleteSchedule removes a flow's schedule (a missing schedule is not an error).
func (s *Service) DeleteSchedule(flowID int64) error {
	_, err := s.db.Exec(`DELETE FROM auto_schedules WHERE flow_id = ?`, flowID)
	return err
}

// ScheduleForFlow returns a flow's schedule, or (Schedule{}, sql.ErrNoRows) if
// none is set.
func (s *Service) ScheduleForFlow(flowID int64) (Schedule, error) {
	row := s.db.QueryRow(`
		SELECT id, flow_id, cadence_seconds, COALESCE(last_run_at,''), catch_up, enabled
		  FROM auto_schedules WHERE flow_id = ?`, flowID)
	return scanSchedule(row)
}

// ListSchedules returns every schedule (for the UI + the tick).
func (s *Service) ListSchedules() ([]Schedule, error) {
	rows, err := s.db.Query(`
		SELECT id, flow_id, cadence_seconds, COALESCE(last_run_at,''), catch_up, enabled
		  FROM auto_schedules ORDER BY flow_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Schedule{}
	for rows.Next() {
		sc, err := scanSchedule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sc)
	}
	return out, rows.Err()
}

// DueSchedules returns the enabled schedules that should fire at time `now`: those
// whose last_run_at is at least cadence ago (or never run). This is the
// drift-tolerant check — it fires ONCE when overdue regardless of how many
// intervals elapsed while the machine was off. A catch_up=false schedule that
// missed its window is realigned (last_run_at bumped) WITHOUT firing, so it waits
// for the next boundary instead; the tick handles that via MarkSkipped.
func (s *Service) DueSchedules(now time.Time) (fire []Schedule, skip []Schedule, err error) {
	all, err := s.ListSchedules()
	if err != nil {
		return nil, nil, err
	}
	for _, sc := range all {
		if !sc.Enabled {
			continue
		}
		if !sc.due(now) {
			continue
		}
		if sc.CatchUp || sc.LastRunAt == "" {
			fire = append(fire, sc)
		} else {
			// Missed its window and not catching up: realign without firing.
			skip = append(skip, sc)
		}
	}
	return fire, skip, nil
}

// due reports whether the schedule is overdue at `now`: never run, or the cadence
// has elapsed since last_run_at. An unparseable/empty last_run_at counts as due
// (fire rather than silently stall).
func (sc Schedule) due(now time.Time) bool {
	if sc.LastRunAt == "" {
		return true
	}
	last, err := time.Parse(scheduleTimeFmt, sc.LastRunAt)
	if err != nil {
		return true
	}
	return now.UTC().Sub(last) >= time.Duration(sc.CadenceSeconds)*time.Second
}

// MarkRan stamps last_run_at = now for a schedule that just fired (or was
// skipped-and-realigned). Keeps the clock moving so the next due check is
// measured from this tick.
func (s *Service) MarkRan(scheduleID int64, now time.Time) error {
	_, err := s.db.Exec(`
		UPDATE auto_schedules SET last_run_at = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%SZ','now')
		 WHERE id = ?`, now.UTC().Format(scheduleTimeFmt), scheduleID)
	return err
}

// --- helpers ---------------------------------------------------------------

type rowScanner interface {
	Scan(dest ...any) error
}

func scanSchedule(row rowScanner) (Schedule, error) {
	var sc Schedule
	var catchUp, enabled int
	if err := row.Scan(&sc.ID, &sc.FlowID, &sc.CadenceSeconds, &sc.LastRunAt, &catchUp, &enabled); err != nil {
		return Schedule{}, err
	}
	sc.CatchUp = catchUp != 0
	sc.Enabled = enabled != 0
	return sc, nil
}

var _ = sql.ErrNoRows // ScheduleForFlow surfaces it to callers
