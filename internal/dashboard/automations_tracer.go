package dashboard

import (
	"context"

	"github.com/scoutapp/corral/internal/applog"
	"github.com/scoutapp/corral/internal/automations"
)

// automationsTracer adapts the dashboard's applog logger to the automations
// package's Tracer interface, so a flow run and its steps appear in the Logs
// trace waterfall (built on the #0 span spine). It's a thin bridge: the runner
// calls StartSpan, we open an applog span and hand back an end func.
type automationsTracer struct{ l *applog.Logger }

func (t automationsTracer) StartSpan(ctx context.Context, category, event, message string, meta map[string]any) (context.Context, func(error)) {
	if t.l == nil {
		return ctx, func(error) {}
	}
	c, end := t.l.StartSpan(ctx, applog.Entry{
		Category: category,
		Event:    event,
		Message:  message,
		Meta:     meta,
	})
	return c, func(err error) { end(err) }
}

// automationsRunner builds a runner wired with the built-in registry AND the
// applog-backed tracer, so every flow/action run it drives is traceable. Use this
// instead of automations.NewRunner(svc, automationsRegistry()) at the dashboard
// call sites that want trace spans.
func (d *dashboardServer) automationsRunner(svc *automations.Service) *automations.Runner {
	return automations.NewRunner(svc, automationsRegistry()).
		WithTracer(automationsTracer{l: d.applog()})
}
