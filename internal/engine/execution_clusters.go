package engine

import (
	"context"
	"math/rand/v2"
	"sync"
	"time"

	"agentflow/internal/apperr"
	"agentflow/internal/domain"
	"agentflow/internal/runtime"
)

func (e *Engine) resolveLOOPExecution(ctx context.Context, requested *domain.ExecutionTarget, ec domain.TelemetryContext) (domain.ExecutionTarget, error) {
	var target domain.ExecutionTarget
	if requested != nil && requested.Pool != domain.ExecutionTargetProject {
		target = *requested
	} else {
		var err error
		target, err = e.repo.ProjectExecutionTarget(ctx, ec.ProjectID)
		if err != nil {
			return target, err
		}
	}
	switch target.Pool {
	case domain.ExecutionTargetClusterAuto:
		cluster, err := e.PickExecutionCluster(ctx, ec.ProjectID)
		return domain.ExecutionTarget{Pool: domain.ExecutionTargetCluster, ClusterID: cluster.ID}, err
	case domain.ExecutionTargetMachineAuto:
		if ec.ProjectID == "" {
			return target, apperr.New(apperr.Invalid, "执行机器自动分配需要选择项目")
		}
	case domain.ExecutionModeRunner:
		runner, ok := e.repo.Runner(target.RunnerID)
		if !ok || !e.repo.CanUseRunner(runner, ec.ProjectID, ec.ResponsibleUserID) {
			return target, apperr.New(apperr.Forbidden, "此项目无法使用所选执行机器")
		}
		if reason := runner.DefaultSelectionBlocker(); reason != "" {
			return target, apperr.New(apperr.Conflict, reason)
		}
	}
	return target, nil
}

// PickExecutionCluster chooses a reachable cluster with the fewest active
// sandboxes. The caller persists the result before any workspace is created.
func (e *Engine) PickExecutionCluster(ctx context.Context, projectID string) (domain.ExecutionCluster, error) {
	clusters, err := e.repo.ExecutionClusters(ctx)
	if err != nil {
		return domain.ExecutionCluster{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	type result struct {
		cluster domain.ExecutionCluster
		load    int
	}
	results := make(chan result, len(clusters))
	var group sync.WaitGroup
	for _, cluster := range clusters {
		if !cluster.Enabled || !cluster.AllowsProject(projectID) {
			continue
		}
		group.Go(func() {
			connection, err := e.repo.OpenSandboxClusterConnection(ctx, cluster.ID, projectID)
			if err != nil {
				return
			}
			load, err := runtime.OpenSandboxLoad(ctx, connection)
			if err == nil {
				results <- result{cluster, load}
			}
		})
	}
	group.Wait()
	close(results)
	var best []domain.ExecutionCluster
	minLoad := 0
	for candidate := range results {
		if len(best) == 0 || candidate.load < minLoad {
			minLoad, best = candidate.load, best[:0]
		}
		if candidate.load == minLoad {
			best = append(best, candidate.cluster)
		}
	}
	if len(best) == 0 {
		return domain.ExecutionCluster{}, apperr.New(apperr.Conflict, "当前没有可连接的 K8S 集群，请在设置中检查集群连接")
	}
	return best[rand.IntN(len(best))], nil
}
