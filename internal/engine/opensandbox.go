package engine

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/opensandbox"
	"agentflow/internal/repo"
)

const openSandboxStartupTimeout = 2 * time.Minute

const DefaultOpenSandboxBackend = "default"

// ErrExecutionResourceRelease marks a provider-side release (OpenSandbox
// DELETE, lookup of a pending create, or a missing backend) that did not
// complete. Callers keep the run: its ExecutionResourceID and Runner
// registration are the only handle on the still-live sandbox, so dropping the
// record would leak the Pod until the provider TTL with nothing left to retry
// from. The HTTP layer answers 503 so the client retries the delete.
var ErrExecutionResourceRelease = errors.New("OpenSandbox 执行环境释放失败")

// ConfigureOpenSandbox installs a lifecycle backend before accepting runs.
// Each project resolves to one backend, which is frozen on the root run.
func (e *Engine) ConfigureOpenSandbox(backend string, client *opensandbox.Client, projects []string) error {
	if backend == "" || backend != strings.TrimSpace(backend) || client == nil || len(projects) == 0 {
		return errors.New("OpenSandbox backend requires an ID, lifecycle client and explicit project IDs")
	}
	if e.openSandboxBackends[backend] != nil {
		return fmt.Errorf("duplicate OpenSandbox backend %q", backend)
	}
	seen := make(map[string]bool, len(projects))
	for _, project := range projects {
		if project == "" || project == "*" || project != strings.TrimSpace(project) {
			return errors.New("OpenSandbox requires explicit project IDs")
		}
		if e.openSandboxProjects[project] != "" || seen[project] {
			return fmt.Errorf("project %q has multiple OpenSandbox bindings", project)
		}
		seen[project] = true
	}
	if e.openSandboxBackends == nil {
		e.openSandboxBackends = make(map[string]*opensandbox.Client)
		e.openSandboxProjects = make(map[string]string)
	}
	e.openSandboxBackends[backend] = client
	for project := range seen {
		e.openSandboxProjects[project] = backend
	}
	return nil
}

func (e *Engine) openSandboxForRun(run domain.Run) *opensandbox.Client {
	backend := run.ExecutionContext.ExecutionProviderID
	if backend == "" {
		backend = DefaultOpenSandboxBackend
	}
	return e.openSandboxBackends[backend]
}

func isOpenSandboxRun(run domain.Run) bool {
	return run.ExecutionContext.ExecutionPool == domain.ExecutionPoolOpenSandbox
}

func openSandboxSupportsProfile(profile domain.SandboxProfile) bool {
	return profile.Driver == domain.SandboxDriverNative && !profile.Privileged && !profile.ReadOnlyRootFS && !profile.HasResourceLimits()
}

// OpenSandboxRegistrant validates that an issued bootstrap token is still
// bound to a live workspace. It cannot register an ordinary or unrelated Runner.
func (e *Engine) OpenSandboxRegistrant(token string) (string, bool) {
	id, ok := opensandbox.WorkspaceFromBootstrap(token)
	if !ok {
		return "", false
	}
	run, ok := e.repo.RunState(id)
	if !ok {
		return "", false
	}
	ec := run.ExecutionContext
	client := e.openSandboxForRun(run)
	if !isLiveStatus(run.Status) || !isOpenSandboxRun(run) || sharesWorkspace(run) ||
		client == nil || !client.ValidBootstrap(token, id) || (!ec.ExecutionResourcePending && ec.ExecutionResourceID == "") ||
		ec.ExecutionRunnerID != domain.OpenSandboxRunnerID(id) {
		return "", false
	}
	return ec.ExecutionRunnerID, true
}

// bindOpenSandboxRunner waits for both the sandbox and its dedicated Runner.
// Child SOPs inherit the owner's binding and do not provision new resources.
func (e *Engine) bindOpenSandboxRunner(ctx context.Context, run domain.Run, nodeID string) (domain.Run, error) {
	ownerID := workspaceRunID(run)
	unlock := e.lockPoolBinding(ownerID)
	defer unlock()

	ctx, cancel := context.WithTimeout(ctx, openSandboxStartupTimeout)
	defer cancel()
	owner, ok := e.repo.RunState(ownerID)
	if !ok || !isOpenSandboxRun(owner) || owner.ExecutionContext.ProjectID != run.ExecutionContext.ProjectID ||
		owner.ExecutionContext.ExecutionProviderID != run.ExecutionContext.ExecutionProviderID {
		return run, errors.New("OpenSandbox 工作区绑定不存在")
	}
	client := e.openSandboxForRun(owner)
	if client == nil {
		return run, errors.New("此运行绑定的 OpenSandbox 未配置")
	}
	runnerID := domain.OpenSandboxRunnerID(ownerID)
	if owner.ExecutionContext.ExecutionRunnerID != runnerID || run.ExecutionContext.ExecutionRunnerID != runnerID {
		return run, errors.New("OpenSandbox Runner 与工作区绑定不符")
	}
	externalID := owner.ExecutionContext.ExecutionResourceID
	if externalID == "" {
		if sharesWorkspace(run) || !e.ownsRun(owner) {
			return run, errors.New("等待所属运行分配 OpenSandbox")
		}
		var err error
		externalID, err = e.provisionOpenSandbox(ctx, client, owner, nodeID)
		if err != nil {
			return run, err
		}
	}
	for {
		sandbox, err := client.Inspect(ctx, ownerID, externalID)
		if errors.Is(err, opensandbox.ErrNotFound) {
			return run, errors.New("OpenSandbox 已回收，原工作区不可用，请新建运行")
		}
		if err != nil {
			return run, err
		}
		switch sandbox.Status.State {
		case "Running":
			runner, ok := e.repo.Runner(runnerID)
			if ok && runner.Status == domain.RunnerStatusOnline && runner.SupportsIntegrate() && runner.SupportsReportedOutputs() {
				if fresh, ok := e.repo.RunProgress(run.ID); ok {
					return fresh, e.requireRunnerRepositoryCapabilities(runnerID, fresh.RepositoryContexts())
				}
				return run, errors.New("运行已不存在")
			}
		case "Failed", "Terminated", "Deleting", "Stopping", "Paused":
			return run, fmt.Errorf("OpenSandbox 不可执行，状态为 %s", sandbox.Status.State)
		}
		select {
		case <-ctx.Done():
			return run, fmt.Errorf("等待 OpenSandbox Runner 就绪: %w", ctx.Err())
		case <-time.After(time.Second):
		}
	}
}

func (e *Engine) provisionOpenSandbox(ctx context.Context, client *opensandbox.Client, owner domain.Run, nodeID string) (string, error) {
	sandbox, err := client.Find(ctx, owner.ID)
	if errors.Is(err, opensandbox.ErrNotFound) {
		// Persist the create intent before POST. After a response is lost or a
		// worker restarts, retry only the lookup, which cannot start another Pod.
		recorded := false
		updateErr := e.updateRunErr(owner.ID, func(current *domain.Run) {
			ec := &current.ExecutionContext
			if e.ownsRun(*current) && isLiveStatus(current.Status) && !ec.ExecutionResourcePending && ec.ExecutionResourceID == "" {
				ec.ExecutionResourcePending = true
				recorded = true
				e.event(current, nodeID, "execution.waiting", "正在创建 OpenSandbox 执行环境")
			}
		})
		if updateErr != nil {
			return "", updateErr
		}
		if !recorded {
			return "", errors.New("OpenSandbox 创建结果待确认，请检查原申请或新建运行")
		}
		sandbox, err = client.Create(ctx, owner.ID)
		var rejected *opensandbox.RejectedError
		if errors.As(err, &rejected) {
			// A provider can answer a duplicate create with 409 although the
			// sandbox exists. Look it up before dropping the intent: a run
			// deleted before the next attempt would otherwise lose its only
			// handle and leak the sandbox until the provider TTL.
			if found, findErr := client.Find(ctx, owner.ID); findErr == nil {
				sandbox, err = found, nil
			} else if !errors.Is(findErr, opensandbox.ErrNotFound) {
				// The lookup failed for another reason, so this attempt's
				// outcome is unknown. Keep the pending flag for the next one.
				return "", findErr
			} else if rejected.Status == http.StatusConflict {
				// A duplicate may exist before it appears in the provider's list.
				// Keep the intent and let the next attempt look up the same sandbox.
				return "", fmt.Errorf("OpenSandbox 创建冲突，等待原执行环境可见: %v", err)
			}
		}
		if err != nil && !errors.As(err, &rejected) {
			// A timeout or 5xx leaves the outcome unknown: keep the pending flag
			// for lookup-only recovery on the next attempt.
			return "", err
		}
		if err != nil {
			// The provider refused the request and the lookup found nothing, so
			// nothing was created. Clear the create intent, otherwise every
			// further attempt reads the pending flag as an unknown outcome and
			// the run can never recover from an invalid image or resource
			// request.
			clearErr := e.updateRunErr(owner.ID, func(current *domain.Run) {
				ec := &current.ExecutionContext
				if e.ownsRun(*current) && isLiveStatus(current.Status) && ec.ExecutionResourcePending && ec.ExecutionResourceID == "" {
					ec.ExecutionResourcePending = false
					e.event(current, nodeID, "execution.failed", "OpenSandbox 拒绝了创建请求，未创建执行环境")
				}
			})
			if clearErr != nil {
				return "", clearErr
			}
		}
	}
	if err != nil {
		return "", err
	}
	bound := false
	updateErr := e.updateRunErr(owner.ID, func(current *domain.Run) {
		ec := &current.ExecutionContext
		if e.ownsRun(*current) && isLiveStatus(current.Status) && (ec.ExecutionResourceID == "" || ec.ExecutionResourceID == sandbox.ID) {
			ec.ExecutionResourceID = sandbox.ID
			ec.ExecutionResourcePending = false
			bound = true
			e.event(current, nodeID, "execution.bound", "已绑定 OpenSandbox 执行环境")
		}
	})
	if updateErr != nil {
		return "", updateErr
	}
	if !bound {
		return "", errors.New("运行租约已变更，无法绑定 OpenSandbox")
	}
	return sandbox.ID, nil
}

func (e *Engine) releaseOpenSandbox(run domain.Run) error {
	ec := run.ExecutionContext
	id := ec.ExecutionResourceID
	if id == "" && !ec.ExecutionResourcePending {
		return nil
	}
	client := e.openSandboxForRun(run)
	if client == nil {
		return errors.New("此运行绑定的 OpenSandbox 未配置")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if id == "" {
		sandbox, err := client.Find(ctx, run.ID)
		if errors.Is(err, opensandbox.ErrNotFound) {
			return nil // any create accepted later still expires at the provider TTL
		}
		if err != nil {
			return err
		}
		id = sandbox.ID
	}
	if err := client.Release(ctx, run.ID, id); err != nil {
		return err
	}
	// Deregister the runner even when the run never recorded its ID: the run
	// start path registers it under the derived name, and leaving the record
	// online would keep listing a Runner whose sandbox is gone.
	runnerID := ec.ExecutionRunnerID
	if runnerID == "" {
		runnerID = domain.OpenSandboxRunnerID(run.ID)
	}
	if err := e.repo.DeregisterRunner(runnerID, time.Now().UTC()); err != nil && !errors.Is(err, repo.ErrRunnerNotFound) {
		return err
	}
	return nil
}
