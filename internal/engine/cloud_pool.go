package engine

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"time"

	"agentflow/internal/domain"
)

// CloudRunnerPool routes runs whose responsible user has no default
// execution machine to a platform runner carrying Label, instead of the
// control-plane host. Projects limits the routing to the listed project IDs
// ("*" matches every project). An empty Label disables the pool.
//
// Required makes the pool the only execution path for those projects: this
// host never runs their nodes, a run waits for a pool runner instead, and a
// pool run whose runner went offline moves to another pool runner.
type CloudRunnerPool struct {
	Label    string
	Projects []string
	Required bool
}

// poolBindWait bounds how long a node waits for an online pool runner.
var poolBindWait = 10 * time.Minute

// ConfigureCloudRunnerPool sets the pool used by runs started afterwards.
func (e *Engine) ConfigureCloudRunnerPool(pool CloudRunnerPool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	pool.Label = strings.TrimSpace(pool.Label)
	e.cloudPool = pool
}

// ParseCloudRunnerProjects splits a comma-separated project list.
func ParseCloudRunnerProjects(raw string) []string {
	var out []string
	for _, p := range strings.Split(raw, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// cloudPoolRunner picks an online pool runner for a new run of projectID.
// It reports false when the pool is off for the project or has no runner
// able to execute a run end to end; the run then executes on this host.
func (e *Engine) cloudPoolRunner(projectID string) (domain.Runner, bool) {
	e.mu.Lock()
	pool := e.cloudPool
	e.mu.Unlock()
	if pool.Label == "" || !cloudPoolServes(pool.Projects, projectID) {
		return domain.Runner{}, false
	}
	return e.pickPoolRunner(pool.Label)
}

func (e *Engine) pickPoolRunner(label string) (domain.Runner, bool) {
	var candidates []domain.Runner
	for _, runner := range e.repo.Runners() {
		if poolRunnerEligible(runner, label) {
			candidates = append(candidates, runner)
		}
	}
	if len(candidates) == 0 {
		return domain.Runner{}, false
	}
	return candidates[rand.Intn(len(candidates))], true
}

func poolRunnerEligible(runner domain.Runner, label string) bool {
	// Pool runners receive every user's credentials, so only runners
	// registered with the platform bootstrap token (no owner) qualify;
	// a user's own machine cannot join by labelling itself.
	if runner.OwnerUserID != 0 || runner.Status != domain.RunnerStatusOnline || !runner.SupportsReportedOutputs() || !runner.SupportsIntegrate() {
		return false
	}
	for _, runnerLabel := range runner.Labels {
		if strings.TrimSpace(runnerLabel) == label {
			return true
		}
	}
	return false
}

func cloudPoolServes(projects []string, projectID string) bool {
	projectID = strings.TrimSpace(projectID)
	for _, p := range projects {
		if p == "*" || (projectID != "" && p == projectID) {
			return true
		}
	}
	return false
}

// cloudPoolRequired reports whether runs of projectID must execute in the
// pool rather than on this host.
func (e *Engine) cloudPoolRequired(projectID string) bool {
	e.mu.Lock()
	pool := e.cloudPool
	e.mu.Unlock()
	return pool.Required && pool.Label != "" && cloudPoolServes(pool.Projects, projectID)
}

// CloudPoolStatus reports whether the cloud runner pool is configured and
// how many of its runners can take work right now.
func (e *Engine) CloudPoolStatus() (configured bool, online int) {
	label := e.cloudPoolLabel()
	if label == "" {
		return false, 0
	}
	for _, runner := range e.repo.Runners() {
		if poolRunnerEligible(runner, label) {
			online++
		}
	}
	return true, online
}

func (e *Engine) cloudPoolLabel() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.cloudPool.Label
}

func isPoolRun(run domain.Run) bool {
	return run.ExecutionContext.ExecutionPool == domain.ExecutionModeCloud
}

// bindPoolRunner makes sure a pool run is bound to an online pool runner
// before a node starts: the first node of a run started while the pool was
// empty, a cloud-mode run of a project that now requires the pool, and a run
// whose runner went offline before any node changed its checkout. Once code
// changes exist they live only in that checkout, so the run stays where it
// is (or fails when that machine is gone). Runs on a user's own machine are
// left alone.
func (e *Engine) bindPoolRunner(ctx context.Context, run domain.Run, nodeID string) (domain.Run, error) {
	ec := run.ExecutionContext
	convert := ec.ExecutionMode == domain.ExecutionModeCloud && e.cloudPoolRequired(ec.ProjectID)
	if convert && hasCodeWorkspace(run) && hasWorkspaceProgress(run, nodeID) {
		// The control-plane checkout already holds this run's changes.
		convert = false
	}
	if !isPoolRun(run) && !convert {
		return run, nil
	}
	previous := strings.TrimSpace(ec.ExecutionRunnerID)
	if previous != "" && !convert {
		runner, ok := e.repo.Runner(previous)
		if ok && runner.Status == domain.RunnerStatusOnline {
			return run, nil
		}
		// The checkout lives only on that machine: nodes do not push
		// intermediate commits, so another machine would continue without
		// the earlier nodes' code changes.
		if hasCodeWorkspace(run) && hasWorkspaceProgress(run, nodeID) {
			name := previous
			if ok && strings.TrimSpace(runner.Name) != "" {
				name = runner.Name
			}
			return run, fmt.Errorf("执行机器 %s 已离线，工作区和之前节点的代码改动都在该机器上，无法换机继续；请等该机器恢复后重试，或从头重跑", name)
		}
	}
	label := e.cloudPoolLabel()
	if label == "" {
		return run, errors.New("云端执行池未配置，无法为该运行分配执行机器")
	}
	deadline := time.Now().Add(poolBindWait)
	var runner domain.Runner
	announced := false
	for {
		var ok bool
		if runner, ok = e.pickPoolRunner(label); ok {
			break
		}
		if time.Now().After(deadline) {
			return run, fmt.Errorf("云端执行池（%s）在 %d 分钟内没有在线的执行机器，请确认执行机器已启动并在线后重试", label, int(poolBindWait/time.Minute))
		}
		if !announced {
			announced = true
			e.updateRun(run.ID, func(c *domain.Run) {
				e.event(c, nodeID, "execution.waiting", fmt.Sprintf("云端执行池（%s）暂无在线的执行机器，最多等待 %d 分钟", label, int(poolBindWait/time.Minute)))
			})
		}
		select {
		case <-ctx.Done():
			return run, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	e.updateRun(run.ID, func(c *domain.Run) {
		c.ExecutionContext.ExecutionMode = domain.ExecutionModeRunner
		c.ExecutionContext.ExecutionPool = domain.ExecutionModeCloud
		c.ExecutionContext.ExecutionRunnerID = runner.ID
		c.ExecutionContext.RunnerWorkspaceLayout = domain.RunnerWorkspaceLayoutShared
		msg := fmt.Sprintf("已分配云端执行机器 %s", runner.Name)
		if previous != "" {
			name := previous
			if old, ok := e.repo.Runner(previous); ok && strings.TrimSpace(old.Name) != "" {
				name = old.Name
			}
			msg = fmt.Sprintf("执行机器 %s 不可用，已切换到云端执行机器 %s 重新拉取代码", name, runner.Name)
		}
		e.event(c, nodeID, "execution.bound", msg)
	})
	bound, ok := e.repo.RunState(run.ID)
	if !ok {
		return run, errors.New("run not found")
	}
	return bound, nil
}

// hasCodeWorkspace reports whether losing this run's checkout loses code:
// the run is bound to a repository.
func hasCodeWorkspace(r domain.Run) bool {
	for _, rc := range r.RepositoryContexts() {
		if strings.TrimSpace(rc.RepositoryID) != "" {
			return true
		}
	}
	return false
}

// hasWorkspaceProgress reports whether nodes other than nodeID already
// changed the workspace, so continuing on a fresh checkout would drop their work.
func hasWorkspaceProgress(r domain.Run, nodeID string) bool {
	for id, nr := range r.Nodes {
		if id != nodeID && nr != nil && nr.Status == "succeeded" {
			return true
		}
	}
	return false
}
