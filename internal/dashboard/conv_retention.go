package dashboard

import (
	"time"

	"github.com/scoutapp/corral/internal/config"
	"github.com/scoutapp/corral/internal/convstore"
)

// convRetention returns the effective conversations-DB retention policy from
// global settings, falling back to the built-in defaults (30 days / 500k
// conversations) for unset fields.
func convRetention() convstore.Retention {
	gs := config.ReadGlobalSettings()
	r := convstore.DefaultRetention
	if gs.ConvRetentionDays > 0 {
		r.MaxAgeDays = gs.ConvRetentionDays
	}
	if gs.ConvMaxRows > 0 {
		r.MaxRows = gs.ConvMaxRows
	}
	return r
}

// startConvRetention prunes the conversations DB on start, then hourly, in a
// background goroutine. Best-effort — a prune error (or an unopenable convstore,
// e.g. a tag-less dev build) is ignored. Mirrors startLogRetention.
func (d *dashboardServer) startConvRetention() {
	prune := func() {
		cs, err := d.getConvStore()
		if err != nil {
			return
		}
		_, _ = cs.Prune(convRetention())
	}
	prune()
	go func() {
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for range t.C {
			prune()
		}
	}()
}
