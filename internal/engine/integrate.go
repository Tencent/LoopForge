package engine

import (
	"agentflow/internal/domain"
	"agentflow/internal/gitwork"
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

const maxIntegrationReworks = 3

var integrationLocks sync.Map

func shouldSeedIntegrateBranch(r domain.RepositoryContext, b string) bool {
	return strings.TrimSpace(r.IntegrateBranch) != "" && strings.TrimSpace(r.IntegrateBranch) == strings.TrimSpace(b)
}

func publishIntegrateBranch(ctx context.Context, w, b string) error {
	ensureGitIdentity(ctx, w)
	if _, err := gitC(ctx, w, "checkout", "-B", b); err != nil {
		return err
	}
	_, err := gitC(ctx, w, "push", "-u", "origin", b)
	return err
}
func ensureGitIdentity(ctx context.Context, w string) { gitwork.EnsureIdentity(ctx, w) }

// IntegrationLocker serializes merges into one repository's integration
// branch; repo.DeliveryRepo implements it across control-plane instances.
type IntegrationLocker interface {
	LockIntegration(ctx context.Context, repositoryID string, wait time.Duration) (func(), error)
}

const integrationLockWait = 10 * time.Minute

func lockIntegration(locks IntegrationLocker, id string) (func(), error) {
	if locks == nil {
		m, _ := integrationLocks.LoadOrStore(strings.TrimSpace(id), &sync.Mutex{})
		l := m.(*sync.Mutex)
		l.Lock()
		return l.Unlock, nil
	}
	return locks.LockIntegration(context.Background(), id, integrationLockWait)
}

// integrateScheduledRun merges each repository's work branch into its
// integration branch once a scheduled run succeeded. It reports true when a
// conflict sent the run back to a repair node.
func (e *Engine) integrateScheduledRun(id string) (bool, error) {
	r, ok := e.repo.RunState(id)
	if !ok || strings.TrimSpace(r.ExecutionContext.ScheduleJobID) == "" {
		return false, nil
	}
	repositories := r.RepositoryContexts()
	if len(repositories) == 0 {
		return false, nil
	}
	e.runEnvMu.Lock()
	env := e.runEnvs[id]
	e.runEnvMu.Unlock()
	if env == nil {
		return false, fmt.Errorf("run sandbox missing")
	}
	ctx := withGitIdentity(withGitToken(context.Background(), env.credentialSnapshot().GitToken), env.credentialSnapshot().GitUserName, env.credentialSnapshot().GitUserEmail)
	runner, onRunner, err := e.integrationRunner(r)
	if err != nil {
		return false, err
	}
	if !onRunner {
		if missing := e.verifyDeliveryCompleteness(r, env.Workspace()); len(missing) > 0 {
			e.updateRun(id, func(c *domain.Run) {
				e.event(c, "", "delivery.incomplete", fmt.Sprintf("交付完整性警告: 以下文件在上游节点产物中产出但未进入交付工作区（可能因跨实例接管丢失），请人工核对 MR 是否包含全部交付物: %s", strings.Join(missing, ", ")))
			})
		}
	}
	for _, rc := range repositories {
		b, w := strings.TrimSpace(rc.IntegrateBranch), strings.TrimSpace(rc.WorkBranch)
		if b == "" || w == "" {
			continue
		}
		unlock, err := lockIntegration(e.repo, rc.RepositoryID)
		if err != nil {
			return false, err
		}
		var conflicts []string
		var pushDirect func() error
		if onRunner {
			conflicts, err = e.integrateOnRunner(ctx, runner, r, rc, false)
			pushDirect = func() error {
				_, err := e.integrateOnRunner(ctx, runner, r, rc, true)
				return err
			}
		} else {
			workspace := repositoryWorkspacePath(env.Workspace(), rc)
			conflicts, err = integrateLocally(ctx, r, workspace, rc)
			pushDirect = func() error {
				_, err := gitC(ctx, workspace, "push", "origin", "HEAD:refs/heads/"+b)
				return err
			}
		}
		if err == nil && len(conflicts) > 0 {
			err = e.requeueIntegrationConflict(id, rc.IntegrateBranch, conflicts)
			unlock()
			return err == nil, err
		}
		if err == nil {
			err = e.mergeWorkBranchIntoIntegrate(id, r, rc, w, b, pushDirect)
		}
		unlock()
		if err != nil {
			return false, err
		}
	}
	return false, nil
}

// integrationRunner reports whether the run's integration happens on its
// execution machine: the run executes there with a shared checkout and the
// runner understands the integrate action. Otherwise the control-plane
// workspace is integrated.
func (e *Engine) integrationRunner(r domain.Run) (domain.Runner, bool, error) {
	runnerID := e.defaultExecutionRunner(r)
	if runnerID == "" || r.ExecutionContext.RunnerWorkspaceLayout != domain.RunnerWorkspaceLayoutShared {
		return domain.Runner{}, false, nil
	}
	runner, ok := e.repo.Runner(runnerID)
	if !ok {
		return domain.Runner{}, false, fmt.Errorf("执行机器 %s 不存在", runnerID)
	}
	return runner, runner.SupportsIntegrate(), nil
}

// integrateLocally merges the latest integration branch into the work branch
// in the control-plane workspace and pushes the work branch.
func integrateLocally(ctx context.Context, r domain.Run, workspace string, rc domain.RepositoryContext) ([]string, error) {
	if err := applyWorkspaceGitAuth(ctx, workspace); err != nil {
		return nil, err
	}
	ensureGitIdentity(ctx, workspace)
	conflicts, err := mergeLatestIntegration(ctx, workspace, rc, integrationCommitLabel(r))
	if err != nil || len(conflicts) > 0 {
		return conflicts, err
	}
	if _, err := gitC(ctx, workspace, "push", "origin", rc.WorkBranch); err != nil {
		return nil, fmt.Errorf("push work branch %s: %w", rc.WorkBranch, err)
	}
	return nil, nil
}

// mergeWorkBranchIntoIntegrate lands the pushed work branch on the
// integration branch through a Gongfeng MR, or with pushDirect when Gongfeng
// is not configured.
func (e *Engine) mergeWorkBranchIntoIntegrate(id string, r domain.Run, rc domain.RepositoryContext, work, integrate string, pushDirect func() error) error {
	if e.MergeIntoBranch != nil && strings.TrimSpace(rc.GongfengProjectID) != "" {
		title := integrateMRTitle(r, rc, work, integrate)
		ref, err := e.MergeIntoBranch(rc.GongfengProjectID, work, integrate, title, integrateMRDescription(r, work, integrate))
		if err != nil {
			e.updateRun(id, func(c *domain.Run) {
				e.event(c, "", "mr.auto_merge_failed", fmt.Sprintf("已推送仓库 %s 的工作分支 %s，基线 MR（%s → %s）自动合并失败（不会直推基线）: %v", rc.Name, work, work, integrate, err))
			})
			return fmt.Errorf("merge integrate MR %s → %s: %w", work, integrate, err)
		}
		e.updateRun(id, func(c *domain.Run) {
			e.event(c, "", "integration.succeeded", fmt.Sprintf("仓库 %s 已通过 MR %s 合入 %s（%s）", rc.Name, ref, integrate, title))
		})
		return nil
	}
	if err := pushDirect(); err != nil {
		return fmt.Errorf("push integration branch %s: %w", integrate, err)
	}
	e.updateRun(id, func(c *domain.Run) {
		e.event(c, "", "integration.succeeded", "已直推合入 "+integrate+"（未配置工蜂 MCP）")
	})
	return nil
}

func integrateMRTitle(r domain.Run, rc domain.RepositoryContext, work, integrate string) string {
	if rc.AlignOnly {
		def := strings.TrimSpace(rc.DefaultBranch)
		if def == "" {
			def = work
		}
		return fmt.Sprintf("AgentFlow: 对齐 %s → %s", def, integrate)
	}
	label := integrationCommitLabel(r)
	if label == "" || label == "scheduled SOP work" {
		return fmt.Sprintf("AgentFlow: %s → %s", work, integrate)
	}
	return fmt.Sprintf("AgentFlow: %s → %s", label, integrate)
}

func integrateMRDescription(r domain.Run, work, integrate string) string {
	if r.Repository.AlignOnly {
		return fmt.Sprintf("Align the configured main branch into the shared integration baseline.\n\n- run: %s\n- source: `%s`\n- target: `%s`\n", r.ID, work, integrate)
	}
	var b strings.Builder
	b.WriteString("Scheduled SOP merge of an issue work branch into the integration baseline.\n\n")
	if n := r.ExecutionContext.IssueNumber; n > 0 {
		fmt.Fprintf(&b, "- issue: #%d\n", n)
	}
	fmt.Fprintf(&b, "- run: %s\n", r.ID)
	fmt.Fprintf(&b, "- source: `%s`\n", work)
	fmt.Fprintf(&b, "- target: `%s`\n", integrate)
	b.WriteString("\nSOP report files are excluded from the baseline. The work branch is kept for history.\n")
	return b.String()
}
func integrationCommitLabel(r domain.Run) string {
	n := r.ExecutionContext.IssueNumber
	title := strings.TrimSpace(strings.SplitN(r.Input, "\n", 2)[0])
	if rs := []rune(title); len(rs) > 72 {
		title = string(rs[:72])
	}
	work := strings.TrimSpace(r.Repository.WorkBranch)
	switch {
	case n > 0 && title != "":
		return fmt.Sprintf("issue #%d %s", n, title)
	case n > 0:
		return fmt.Sprintf("issue #%d", n)
	case work != "":
		return work
	default:
		return "scheduled SOP work"
	}
}

func mergeLatestIntegration(ctx context.Context, w string, r domain.RepositoryContext, label string) ([]string, error) {
	return gitwork.MergeLatestIntegration(ctx, w, r.WorkBranch, r.IntegrateBranch, label)
}

func mergeLatestRemoteBranch(ctx context.Context, w, branch string) ([]string, error) {
	return gitwork.MergeLatestRemoteBranch(ctx, w, branch)
}

func (e *Engine) requeueIntegrationConflict(id, targetBranch string, files []string) error {
	r, ok := e.repo.RunState(id)
	if !ok {
		return fmt.Errorf("run not found")
	}
	if r.Repository.IntegrationReworks >= maxIntegrationReworks {
		return fmt.Errorf("integration conflict unresolved after %d attempts: %s", maxIntegrationReworks, strings.Join(files, ","))
	}
	target := integrationRepairNode(r.WorkflowSnapshot)
	if target == "" || r.Nodes[target] == nil {
		return fmt.Errorf("workflow has no repair agent")
	}
	reset := descendants(r.WorkflowSnapshot, target)
	reset[target] = true
	targetBranch = strings.TrimSpace(targetBranch)
	if targetBranch == "" {
		targetBranch = r.Repository.IntegrateBranch
	}
	feedback := fmt.Sprintf("合入 %s 时发生冲突（第 %d/%d 次）：%s。请保留前序需求的有效实现，解决全部 Git 冲突并完成 git add/commit；下游评审、测试和 MR 会重新执行。", targetBranch, r.Repository.IntegrationReworks+1, maxIntegrationReworks, strings.Join(files, ","))
	var stale []string
	e.updateRun(id, func(c *domain.Run) {
		stale = stale[:0]
		c.Repository.IntegrationReworks++
		for n := range reset {
			if nr := c.Nodes[n]; nr != nil {
				stale = append(stale, nr.ChildRunID)
				nr.Status = "pending"
				nr.Error = ""
				nr.Output = ""
				nr.ChildRunID = ""
				nr.FinishedAt = time.Time{}
				if n == target {
					nr.ReworkCount++
					nr.Feedback = feedback
				} else {
					nr.Feedback = ""
				}
			}
		}
		c.Status = "running"
		e.event(c, target, "integration.conflict", feedback)
		appendControlTelemetry(c, target, "integration.conflict", feedback)
	})
	e.cancelLiveChildRuns(stale)
	return nil
}
func integrationRepairNode(w domain.WorkflowDefinition) string {
	by := map[string]domain.WorkflowNode{}
	for _, n := range w.Nodes {
		by[n.ID] = n
	}
	for i := len(w.Nodes) - 1; i >= 0; i-- {
		if n, ok := by[strings.TrimSpace(w.Nodes[i].RejectTo)]; ok && n.Type == "agent" {
			return n.ID
		}
	}
	for _, p := range []string{"fix", "implement", "develop", "repair", "code", "change"} {
		for _, n := range w.Nodes {
			if n.Type == "agent" && strings.Contains(strings.ToLower(n.ID), p) {
				return n.ID
			}
		}
	}
	for _, n := range w.Nodes {
		if n.Type == "agent" {
			return n.ID
		}
	}
	return ""
}

// patchNewFiles extracts paths of files a unified git diff adds ("new file
// mode" entries). Binary additions are included; renames and modifications
// are ignored.
func patchNewFiles(diff string) []string {
	if !looksLikeGitDiff(diff) {
		var files []string
		for _, line := range strings.Split(diff, "\n") {
			fields := strings.Split(line, "\t")
			if len(fields) >= 2 && fields[0] == "A" && strings.TrimSpace(fields[1]) != "" {
				files = append(files, fields[1])
			}
		}
		return files
	}
	var files []string
	current := ""
	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			current = ""
			if a := strings.Index(line, " a/"); a >= 0 {
				if b := strings.Index(line, " b/"); b > a {
					current = strings.TrimSpace(line[a+3 : b])
				}
			}
		case current != "" && strings.HasPrefix(line, "new file mode"):
			files = append(files, current)
			current = ""
		}
	}
	return files
}

// verifyDeliveryCompleteness warns when source files produced by upstream
// node patches never made it into the delivery workspace. materializeInputs
// only applies direct-predecessor patches and skips them on a workspace that
// already has git work — safe on the instance owning the run workspace, but
// a fresh clone elsewhere only contains committed work, so uncommitted
// upstream files (e.g. an SOP e2e script) silently vanish from the pushed
// MR. This check never mutates the workspace; it only surfaces the loss as
// an explicit warning the operator can act on.
func (e *Engine) verifyDeliveryCompleteness(r domain.Run, workspace string) []string {
	// dir -> path -> producing node. Artifacts from a persisted legacy
	// checkout (empty RepositoryDir) still map to the workspace root.
	expected := map[string]map[string]string{}
	for _, a := range r.Artifacts {
		body := repositoryPatchApplyBody(a)
		if body == "" {
			body = a.Content
		}
		if a.Name != "repository-patch" || strings.TrimSpace(body) == "" {
			continue
		}
		dir := strings.TrimSpace(a.RepositoryDir)
		if expected[dir] == nil {
			expected[dir] = map[string]string{}
		}
		for _, path := range patchNewFiles(body) {
			if _, ok := expected[dir][path]; !ok || a.NodeID == "mr" {
				expected[dir][path] = a.NodeID
			}
		}
	}
	if len(expected) == 0 {
		return nil
	}
	knownDirs := map[string]string{}
	for _, rc := range r.RepositoryContexts() {
		knownDirs[rc.WorkspaceDir] = repositoryWorkspacePath(workspace, rc)
	}
	var missing []string
	for dir, paths := range expected {
		repoWorkspace, ok := knownDirs[dir]
		if !ok {
			repoWorkspace = repositoryWorkspacePath(workspace, domain.RepositoryContext{WorkspaceDir: dir})
		}
		out, err := gitC(context.Background(), repoWorkspace, "ls-tree", "-r", "--name-only", "HEAD")
		if err != nil {
			continue
		}
		present := map[string]bool{}
		for _, line := range strings.Split(out, "\n") {
			if line = strings.TrimSpace(line); line != "" {
				present[line] = true
			}
		}
		for path, node := range paths {
			if present[path] {
				continue
			}
			label := path
			if dir != "" {
				label = dir + "/" + path
			}
			missing = append(missing, fmt.Sprintf("%s (produced by %s)", label, node))
		}
	}
	sort.Strings(missing)
	return missing
}
