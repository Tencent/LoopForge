package workspaceprep

import (
	"context"

	"agentflow/internal/domain"
	"agentflow/internal/procwatch"
)

// Observe forwards procwatch's redacted output to the opening request while
// preserving its existing run-event or runner-log observer. procwatch alone
// owns execution, output capture, stall detection and process cancellation.
func Observe(ctx context.Context) context.Context {
	if !Enabled(ctx) {
		return ctx
	}
	o := get(ctx)
	return procwatch.WithAdditionalObserver(ctx, func(event procwatch.Event) {
		if event.Output == "" {
			return
		}
		phase, label := o.phase, o.label
		if phase == "" {
			phase = event.Stage
			if phase == "image" {
				phase = "build"
			}
			label = "正在准备工作区"
		}
		// A command can fail before a reference-cache fallback succeeds. Only
		// Result marks the whole opening ready/failed after all phases finish.
		Report(ctx, domain.WorkspaceOpening{State: "running", Phase: phase, Message: label, Output: event.Output, TimeoutSeconds: int64(Timeout(phase).Seconds())})
	})
}
