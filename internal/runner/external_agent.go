package runner

import (
	"context"
	"errors"

	"agentflow/internal/domain"
	"agentflow/internal/managedagent"
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
		// A completed turn or a provider error is a final answer; a turn that
		// ran past its budget is not. The prompt already reached the Agent and
		// the cancel was advisory, so the control plane must not resend it.
		// The sentinel cannot survive the completion report as an error value,
		// so it travels as a structured code instead of the message text.
		result := execResult{status: domain.RunnerTaskFailed, errMsg: err.Error()}
		if errors.Is(err, managedagent.ErrTurnTimedOut) {
			result.uncertain = true
		}
		return result
	}
	return execResult{status: domain.RunnerTaskSuccess, result: output}
}
