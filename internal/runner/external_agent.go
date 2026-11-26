package runner

import (
	"context"

	"agentflow/internal/domain"
)

func (d *Daemon) executeExternalAgent(ctx context.Context, task domain.RunnerTask) execResult {
	if task.Provider != domain.A2AExecutorID || d.cfg.ExternalAgent == nil {
		return execResult{status: domain.RunnerTaskFailed, errMsg: "local A2A Agent is not configured"}
	}
	if err := d.client.StartTask(ctx, task.ID); err != nil {
		return execResult{status: domain.RunnerTaskFailed, errMsg: "could not start leased A2A task"}
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		select {
		case desired := <-d.taskControl(task.ID):
			if desired == domain.RunnerTaskDesiredCancel {
				cancel()
			}
		case <-ctx.Done():
		}
	}()
	batch := newEventBatcher(task, d.client)
	output, err := d.cfg.ExternalAgent.Run(ctx, task.Prompt, func(text string) {
		batch.addObservation(domain.TelemetryEvent{Category: "output", Name: "agent.output", Phase: "progress", Message: text, Provider: domain.A2AExecutorID})
		batch.flush(ctx)
	})
	if ctx.Err() != nil {
		return execResult{status: domain.RunnerTaskCanceled}
	}
	if err != nil {
		return execResult{status: domain.RunnerTaskFailed, errMsg: err.Error()}
	}
	return execResult{status: domain.RunnerTaskSuccess, result: output}
}
