package engine

import (
	"cmp"
	"context"
	"fmt"
	"log"
	"math/rand"
	"slices"
	"strings"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

const autoAssignOfflineGrace = 2 * time.Minute

// A platform candidate can execute locally or on a pool runner. All of those
// backends share one admission bucket and enter the ranking as one machine.
type autoAssignCandidate struct {
	runner           domain.Runner
	shared           bool
	executorDefaults map[string]domain.AssistantDefaults
}

func (c autoAssignCandidate) bucket() string {
	if c.shared {
		return "runner:" + c.runner.ID
	}
	return repo.PlatformAdmissionBucketKey
}

func (c autoAssignCandidate) bind(ec domain.TelemetryContext) domain.TelemetryContext {
	ec.ExecutionPool = domain.ExecutionPoolProjectAuto
	ec.ExecutionRunnerID = c.runner.ID
	ec.ExecutionMode = domain.ExecutionModeCloud
	ec.RunnerWorkspaceLayout = ""
	if c.runner.ID != "" {
		ec.ExecutionMode = domain.ExecutionModeRunner
		ec.RunnerWorkspaceLayout = domain.RunnerWorkspaceLayoutShared
	}
	return ec
}

func (e *Engine) platformAutoCandidates(projectID string, includeOffline bool) []autoAssignCandidate {
	e.mu.Lock()
	pool := e.cloudPool
	e.mu.Unlock()
	serves := pool.serves(projectID)
	var candidates []autoAssignCandidate
	if serves {
		for _, runner := range e.repo.Runners() {
			if poolRunnerCapable(runner, pool.Label) && (includeOffline || runner.Status == domain.RunnerStatusOnline) {
				candidates = append(candidates, autoAssignCandidate{runner: runner})
			}
		}
		rand.Shuffle(len(candidates), func(i, j int) { candidates[i], candidates[j] = candidates[j], candidates[i] })
	}
	if e.localPlatformOnline() && !(pool.Required && serves) {
		candidates = append(candidates, autoAssignCandidate{})
	}
	return candidates
}

func (e *Engine) autoAssignCandidates(ec domain.TelemetryContext, includeOffline bool) []autoAssignCandidate {
	project, ok := e.repo.Project(ec.ProjectID)
	if !ok || project.ArchivedAt != nil || !project.HasWritableMember(ec.ResponsibleUserID) {
		return nil
	}
	candidates := e.platformAutoCandidates(ec.ProjectID, includeOffline)
	for _, view := range e.repo.RunnersForUser(ec.ResponsibleUserID, ec.ProjectID) {
		runner := view.Runner
		if !view.CanUseInProject {
			continue
		}
		if includeOffline {
			// Check the protocol version even when the last heartbeat is stale.
			runner.Status = domain.RunnerStatusOnline
		}
		if runner.DefaultSelectionBlocker() == "" {
			candidates = append(candidates, autoAssignCandidate{runner: view.Runner, shared: true})
		}
	}
	return candidates
}

func autoAssignRunnerCapabilities(runner domain.Runner, wf domain.WorkflowDefinition, ec domain.TelemetryContext, repositories []domain.RepositoryContext) error {
	if runner.ID == "" {
		return nil
	}
	if err := runnerRepositoryCapabilityError(runner, repositories); err != nil {
		return err
	}
	if len(wf.ProjectCredentialIDs) > 0 && !runner.SupportsProjectCredentials() {
		return fmt.Errorf("请升级执行机器以使用项目凭据")
	}
	if (ec.SharedScheduleID != "" || ec.ScheduleJobID != "") &&
		!(runner.SupportsReportedOutputs() && runner.SupportsIntegrate()) &&
		slices.ContainsFunc(repositories, func(rc domain.RepositoryContext) bool { return strings.TrimSpace(rc.IntegrateBranch) != "" }) {
		return fmt.Errorf("请升级执行机器以合入定时任务的集成分支")
	}
	return nil
}

func (e *Engine) planAutoAssignCandidate(candidate *autoAssignCandidate, wf domain.WorkflowDefinition, ec domain.TelemetryContext, repositories []domain.RepositoryContext) error {
	if err := autoAssignRunnerCapabilities(candidate.runner, wf, ec, repositories); err != nil {
		return err
	}
	if ec.ResponsibleUserID > 0 {
		defaults, requirements, err := e.planSOPExecutors(wf, candidate.bind(ec), true)
		if err != nil {
			return err
		}
		for _, req := range requirements {
			if !req.Configured {
				return fmt.Errorf("请在个人配置中添加可用的执行器凭据（%s）", cmp.Or(req.Name, req.Kind, "LOOP 默认执行器"))
			}
		}
		candidate.executorDefaults = defaults
	}
	return nil
}

func (e *Engine) candidateSupportsSandbox(c autoAssignCandidate, driver string) bool {
	if c.runner.ID == "" {
		if driver == domain.SandboxDriverDocker {
			return !e.executes() || runtime.DockerReady()
		}
		return e.executes() && runtime.NativeSandboxAvailable(driver)
	}
	// Repository and credential support were checked before native isolation.
	return driver == domain.SandboxDriverDocker || c.runner.SupportsNativeSandbox(driver)
}

func (e *Engine) prepareAutoAssign(ctx context.Context, wf domain.WorkflowDefinition, ec domain.TelemetryContext, repositories []domain.RepositoryContext, input string) ([]autoAssignCandidate, domain.SandboxProfile, string, error) {
	var profile domain.SandboxProfile
	if !wf.AutoSelectSandbox {
		var ok bool
		if profile, ok = e.repo.Sandbox(wf.SandboxProfileID); !ok {
			return nil, profile, "", fmt.Errorf("sandbox profile %q not found", wf.SandboxProfileID)
		}
	}
	var candidates []autoAssignCandidate
	var incompatible error
	for _, candidate := range e.autoAssignCandidates(ec, false) {
		if err := e.planAutoAssignCandidate(&candidate, wf, ec, repositories); err != nil {
			if incompatible == nil {
				incompatible = err
			}
			continue
		}
		candidates = append(candidates, candidate)
	}
	if len(candidates) == 0 {
		if incompatible != nil {
			return nil, profile, "", fmt.Errorf("没有在线执行机器可运行此 LOOP：%w", incompatible)
		}
		return nil, profile, "", fmt.Errorf("项目暂无可用的在线执行机器，请启动平台执行机器或共享一台在线机器")
	}
	if err := e.assignSOPWorkBranches(ctx, ec, repositories, input, time.Now().UTC(), candidates); err != nil {
		return nil, profile, "", err
	}
	// Repository naming can add a strict branch creation requirement.
	var branchIncompatible error
	candidates = slices.DeleteFunc(candidates, func(candidate autoAssignCandidate) bool {
		if err := autoAssignRunnerCapabilities(candidate.runner, wf, ec, repositories); err != nil {
			branchIncompatible = err
			return true
		}
		return false
	})
	if len(candidates) == 0 {
		return nil, profile, "", fmt.Errorf("没有在线执行机器可运行此 LOOP：%w", branchIncompatible)
	}
	var selection string
	if wf.AutoSelectSandbox {
		// Use any compatible backend with a usable selector credential. Its
		// choice is frozen before machines are ranked or a slot is reserved.
		selectorEC := ec
		for _, c := range candidates {
			if candidateEC := c.bind(ec); e.sopExecutorAccess(candidateEC).defaults().Executor != "" {
				selectorEC = candidateEC
				break
			}
		}
		var err error
		profile, selection, err = e.selectSOPSandboxForTargets(ctx, wf, selectorEC, repositories, input, "", func(profile domain.SandboxProfile) bool {
			return slices.ContainsFunc(candidates, func(c autoAssignCandidate) bool { return e.candidateSupportsSandbox(c, profile.Driver) })
		}, nil)
		if err != nil {
			return nil, profile, "", err
		}
	}
	platformAdded := false
	compatible := candidates[:0]
	for _, candidate := range candidates {
		if (!candidate.shared && platformAdded) || !e.candidateSupportsSandbox(candidate, profile.Driver) {
			continue
		}
		compatible = append(compatible, candidate)
		platformAdded = platformAdded || !candidate.shared
	}
	if len(compatible) == 0 {
		return nil, profile, "", fmt.Errorf("没有在线执行机器支持此 LOOP 的沙箱 %s（%s），请启用兼容的沙箱或执行机器", profile.ID, profile.Driver)
	}
	return compatible, profile, selection, nil
}

func rankAutoAssignCandidates(candidates []autoAssignCandidate, loads map[string]domain.RunnerLoad, platformLimit int) []autoAssignCandidate {
	available := make([]autoAssignCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		limit := platformLimit
		if candidate.shared {
			limit = candidate.runner.RunLimit
		}
		load := loads[candidate.bucket()]
		if limit == 0 || (load.OccupiedRuns < limit && load.QueuedRuns == 0) {
			available = append(available, candidate)
		}
	}
	rand.Shuffle(len(available), func(i, j int) { available[i], available[j] = available[j], available[i] })
	slices.SortStableFunc(available, func(a, b autoAssignCandidate) int {
		left, right := loads[a.bucket()], loads[b.bucket()]
		return cmp.Or(cmp.Compare(left.RunningRuns, right.RunningRuns), cmp.Compare(left.OccupiedRuns, right.OccupiedRuns))
	})
	return available
}

func (e *Engine) submitAutoAssignRun(run domain.Run, candidates []autoAssignCandidate) error {
	var sharedIDs []string
	for _, candidate := range candidates {
		if candidate.shared {
			sharedIDs = append(sharedIDs, candidate.runner.ID)
		}
	}
	loads, err := e.repo.LoadSnapshots(sharedIDs)
	if err != nil {
		return err
	}
	for _, candidate := range rankAutoAssignCandidates(candidates, loads, e.repo.PlatformRunnerRunLimit()) {
		bound := run
		bound.ExecutionContext = candidate.bind(run.ExecutionContext)
		bound.WorkflowSnapshot.ExecutorDefaults = candidate.executorDefaults
		if admitted, err := e.repo.TryAdmitRunOnCandidate(bound, candidate.bucket()); err != nil || admitted {
			return err
		}
	}
	run.Events = slices.DeleteFunc(run.Events, func(event domain.RunEvent) bool { return event.Type == "run.started" })
	return e.repo.EnqueueProjectAutoRun(run)
}

func (e *Engine) autoAssignPlan(run domain.Run, target string, runner *domain.Runner) (repo.AutoAssignPlan, bool) {
	wf, ec := run.WorkflowSnapshot, run.ExecutionContext
	if wf.SandboxSnapshot == nil {
		return repo.AutoAssignPlan{}, false
	}
	var candidates []autoAssignCandidate
	message := "已自动分配至" + domain.PlatformRunnerName
	if runner == nil {
		candidates = e.platformAutoCandidates(ec.ProjectID, false)
	} else if runner.DefaultSelectionBlocker() == "" && e.repo.CanUseRunner(*runner, ec.ProjectID, ec.ResponsibleUserID) {
		candidates = []autoAssignCandidate{{runner: *runner, shared: true}}
		message = "已自动分配至执行机器 " + cmp.Or(runner.Name, runner.ID)
	}
	for _, candidate := range candidates {
		if candidate.bucket() == target && e.planAutoAssignCandidate(&candidate, wf, ec, run.RepositoryContexts()) == nil &&
			e.candidateSupportsSandbox(candidate, wf.SandboxSnapshot.Profile.Driver) {
			return repo.AutoAssignPlan{ExecutionContext: candidate.bind(ec), ExecutorDefaults: candidate.executorDefaults, EventMessage: message}, true
		}
	}
	return repo.AutoAssignPlan{}, false
}

func (e *Engine) checkAutoRunReachable(run domain.Run, now time.Time) string {
	wf, ec := run.WorkflowSnapshot, run.ExecutionContext
	if wf.SandboxSnapshot == nil {
		return "自动分配缺少已选沙箱，请重新启动 LOOP"
	}
	offline := false
	graceStart := run.QueueEnteredAt
	for _, candidate := range e.autoAssignCandidates(ec, true) {
		if e.planAutoAssignCandidate(&candidate, wf, ec, run.RepositoryContexts()) != nil ||
			!e.candidateSupportsSandbox(candidate, wf.SandboxSnapshot.Profile.Driver) {
			continue
		}
		if candidate.runner.ID == "" || candidate.runner.Status == domain.RunnerStatusOnline {
			return ""
		}
		offline = true
		if since := runnerOfflineSince(candidate.runner); since.After(graceStart) {
			graceStart = since
		}
	}
	if offline {
		if now.Sub(graceStart) <= autoAssignOfflineGrace {
			return ""
		}
		return "兼容的项目执行机器已离线超过 2 分钟，请启动执行机器后重试"
	}
	return "项目已无兼容的执行机器，请检查机器共享、沙箱和执行器凭据后重试"
}

func runnerOfflineSince(runner domain.Runner) time.Time {
	// Offline transitions stamp UpdatedAt. Edits to an offline runner can
	// also advance it, conservatively extending the grace period.
	if runner.LastSeenAt != nil && runner.LastSeenAt.After(runner.UpdatedAt) {
		return *runner.LastSeenAt
	}
	return runner.UpdatedAt
}

func (e *Engine) validateAutoAssignBinding(run domain.Run) error {
	id := run.ExecutionContext.ExecutionRunnerID
	if id == "" {
		if run.ExecutionContext.ExecutionMode == domain.ExecutionModeCloud {
			return nil
		}
		return fmt.Errorf("LOOP 正在等待自动分配执行机器")
	}
	runner, ok := e.repo.Runner(id)
	if !ok || runner.Status != domain.RunnerStatusOnline {
		return fmt.Errorf("无法重连原执行环境：执行机器 %s 暂不可用", id)
	}
	if run.ConcurrencyExempt {
		if !e.repo.CanUseRunner(runner, run.ExecutionContext.ProjectID, run.ExecutionContext.ResponsibleUserID) {
			return repo.ErrRunnerProjectDenied
		}
	} else if runner.OwnerUserID != 0 {
		return fmt.Errorf("原执行机器 %s 已退出平台执行池", id)
	}
	return nil
}

func (e *Engine) autoAssignEvaluator() *repo.AutoAssignEvaluator {
	return &repo.AutoAssignEvaluator{
		EvaluateAutoRun:       e.autoAssignPlan,
		CheckAutoRunReachable: e.checkAutoRunReachable,
		OnAutoRunFailed:       e.publish,
	}
}

func (e *Engine) promoteQueuedRuns() {
	promoted, err := e.repo.PromoteQueuedRunsWithAuto(e.autoAssignEvaluator())
	if err != nil {
		log.Printf("promote queued runs: %v", err)
	}
	for _, id := range promoted {
		e.publish(id)
	}
}
