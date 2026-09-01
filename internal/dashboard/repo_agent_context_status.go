package dashboard

import (
	"time"

	"github.com/scoutapp/corral/internal/automations"
	"github.com/scoutapp/corral/internal/config"
)

// AgentContextStatus is the staleness read on a repo's AGENTS.md agent context,
// shared by the repo Settings page, project pages, and PR reviews so all three
// surface the same "regenerate" nudge from one source of truth.
type AgentContextStatus struct {
	// Present is true when the repo has a saved agent context at all.
	Present bool `json:"present"`
	// UpdatedAt is when the context was last saved/regenerated (UTC
	// "YYYY-MM-DD HH:MM:SS"), empty when absent.
	UpdatedAt string `json:"updatedAt,omitempty"`
	// AgeDays is whole days since UpdatedAt (0 when absent/unparseable).
	AgeDays int `json:"ageDays"`
	// ThresholdDays is the effective staleness window; <= 0 means the check is
	// disabled.
	ThresholdDays int `json:"thresholdDays"`
	// Stale is true when Present and the check is enabled and AgeDays exceeds the
	// threshold.
	Stale bool `json:"stale"`
}

// agentContextStatus computes the staleness read for a repo. svc may be nil (no
// store) — it then reports "not present". The threshold comes from global
// settings (default ~3 months); a non-positive threshold disables the check.
func agentContextStatus(svc *automations.Service, repoID string) AgentContextStatus {
	threshold := config.ReadGlobalSettings().AgentContextStaleDaysEffective()
	st := AgentContextStatus{ThresholdDays: threshold}
	if svc == nil || repoID == "" {
		return st
	}
	content, updatedAt, ok := svc.RepoAgentContextMeta(repoID)
	if !ok || content == "" {
		return st
	}
	st.Present = true
	st.UpdatedAt = updatedAt
	// SQLite datetime('now') is UTC "2006-01-02 15:04:05".
	if t, err := time.Parse("2006-01-02 15:04:05", updatedAt); err == nil {
		age := time.Since(t)
		if age < 0 {
			age = 0
		}
		st.AgeDays = int(age.Hours() / 24)
		if threshold > 0 && st.AgeDays > threshold {
			st.Stale = true
		}
	}
	return st
}
