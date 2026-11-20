package runner

import (
	"context"
	"net/http"

	"agentflow/internal/domain"
	"agentflow/internal/runtime"
)

func (d *Daemon) runnerLoginModels(ctx context.Context, provider string) domain.RunnerWorkspaceResponse {
	for _, cli := range d.currentRuntimes() {
		if cli.Provider != provider || cli.BinaryPath == "" || cli.Status != domain.RuntimeStatusOnline {
			continue
		}
		models, err := runtime.DiscoverRunnerLoginModels(ctx, provider, cli.BinaryPath)
		if err != nil {
			return domain.RunnerWorkspaceResponse{Status: http.StatusBadGateway, Error: err.Error()}
		}
		return runnerWorkspacePayload(models)
	}
	return domain.RunnerWorkspaceResponse{Status: http.StatusNotFound, Error: "执行机器上的 CLI 当前不可用"}
}
