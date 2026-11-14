package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"regexp"
	"sort"
	"strings"

	"agentflow/internal/domain"
	"agentflow/internal/workspaceprep"
)

const workspaceOpenSchema = `{"type":"object","properties":{"goal":{"type":"string","minLength":1,"maxLength":500,"description":"一句话说明要做什么"},"issueId":{"type":"string","maxLength":128,"description":"切到这个需求最近一次 SOP 运行的工作区；需求必须跑过 SOP"},"workspaceRunId":{"type":"string","maxLength":128,"description":"切回本对话之前开过的工作区，或某次 SOP 运行的工作区"},"new":{"type":"boolean","description":"新开一个对话工作区"},"sandboxProfileId":{"type":"string","maxLength":128,"description":"新开时的运行环境 id，按要改的代码选择；不填使用 generic-docker"},"repositories":{"type":"array","maxItems":20,"items":{"type":"string","maxLength":200},"description":"新开时要检出的仓库名或 id；不填检出项目全部仓库；传空数组 [] 开一个不检出任何仓库的空工作区，只跑命令或开浏览器"},"branchMode":{"type":"string","enum":["auto","continue","create","readonly"],"description":"新开工作区时的分支语义：auto 由平台自动命名新建（默认，不要同时传 branch）；continue 继续已有远端分支，branch 必须已存在；create 按 branch 指定名字新建，远端同名分支已存在时报错；readonly 只读检出 branch（可以是 preview、master 等共享分支），能读、构建、跑测试，但不会提交。传了 branch/repositoryBranches 又省略本字段时按 continue 处理；repositoryBranches 里用对象写法可以给单个仓库覆盖"},"branch":{"type":"string","maxLength":255,"description":"单仓库时的分支名；配合 branchMode=continue 继续已有远端分支，create 新建分支，readonly 只读检出；只在开新工作区时可用"},"baseBranch":{"type":"string","maxLength":255,"description":"新开工作区时工作分支的起点，也是 submit_mr 的默认目标和改动比较基线，对本次检出的每个非只读仓库生效；不填用仓库配置的主分支。readonly 直接检出 branch 本身，不用起点，全部仓库都是 readonly 时不要传。hotfix 打 release、堆叠 MR 打父分支时填目标分支，例如 release/1.2；MR 要打向哪个分支，起点就要选哪个分支，否则 MR 会带上两个分支的差异提交"},"repositoryBranches":{"type":"object","additionalProperties":{"anyOf":[{"type":"string","maxLength":255},{"type":"object","properties":{"branch":{"type":"string","maxLength":255},"branchMode":{"type":"string","enum":["auto","continue","create","readonly"]},"baseBranch":{"type":"string","maxLength":255}},"additionalProperties":false}]},"description":"多个仓库分别指定分支，key 为仓库名或 id。值写分支名时使用顶层 branchMode；写对象 {branch, branchMode, baseBranch} 时这个仓库用自己的模式和起点，例如 {\"api\":{\"branch\":\"feature/review\",\"branchMode\":\"continue\"},\"web\":{\"branch\":\"feature/new\",\"branchMode\":\"create\"}}；只给 baseBranch 不给 branch 时自动命名新建。未列出的仓库按 auto 从 baseBranch（或主分支）新建工作分支；只在开新工作区时可用"},"requirement":{"type":"object","properties":{"title":{"type":"string","minLength":1,"maxLength":200,"description":"一句话写清要交付的需求"},"description":{"type":"string","maxLength":20000,"description":"背景、要改什么、验收要点"}},"required":["title"],"additionalProperties":false,"description":"用户这次是要改代码交付一个新需求（修 bug、加功能、改文案等，最后会提交 MR），而本对话还没有关联未完成的需求时填写。系统会在项目需求列表里创建这个需求，关联到本对话、工作区和之后提交的 MR，并计入数据统计。只读评审、排查、问答不要填；切到 SOP 运行时不生效"}},"required":["goal"],"additionalProperties":false}`

func workspaceOpenDescriptor() agentToolDescriptor {
	return agentToolDescriptor{
		Name:        "workbench_workspace_open",
		Description: "打开或切换本对话绑定的工作区。用户要看代码、改代码（改文案、修小 bug、补测试等）或跑命令时调用。普通工作区或已停止的 SOP 返回 handoff=true，自动切换到工作区模式并续接同一原生会话，使用 CLI 原生文件、补丁与命令工具操作并验证；执行中的 SOP 返回 handoff=false，只读查看，暂停或接管完成后才交接。用户要提交时调用 workbench_workspace_submit_mr（只为有改动的仓库发起 MR）。不走 SOP，不需要用户另开会话。一个对话同一时间只绑定一个工作区，用哪个由你按用户当前实际要处理的需求决定（@ 只是线索）：issueId、workspaceRunId、new 最多传一个；都不传时继续用当前绑定的工作区，还没有就新开。issueId 切到该需求最近一次 SOP 运行的工作区（已回收的会恢复快照），SOP 执行中可用 workbench_workspace_sop 暂停、继续或接管；workspaceRunId 切回本对话之前开过的工作区；new 新开一个对话工作区，按 sandboxProfileId 和 repositories 检出、每个仓库各建新工作分支，环境和仓库创建后不再变化。没跑过 SOP 的新需求，当前工作区的仓库和环境合适就沿用，不合适就新开。切换后旧工作区解绑，空闲时保存快照并回收，切回时恢复。用户要改代码交付新需求时传 requirement，让需求进入需求列表和数据统计。新开时的分支语义由 branchMode 决定：auto（默认，不传 branch）由平台自动命名并从起点新建；continue 继续 branch 或 repositoryBranches（按仓库名或 id）指定的已有远端分支，分支必须已存在；create 按指定名字新建分支，远端同名分支已存在、或本对话之前的工作区建过同名分支时报错，不会静默继续或覆盖；readonly 只读检出指定分支（可以是 preview 这类共享分支），用来跑测试、复现问题，不会提交。多仓库可以在 repositoryBranches 里用对象给每个仓库单独指定 branchMode 和 baseBranch，比如一个仓库 continue 别人的分支评审、另一个仓库 create 新分支。baseBranch 是工作分支的起点，也是 MR 的默认目标：MR 要打向 release 这类非主分支时，开工作区就要把 baseBranch 设成它。省略 branchMode 但传了 branch 时按 continue 处理。分支选择只在开新工作区时生效，工作分支不能是主分支、集成分支或 baseBranch。不要在终端里 git checkout/switch 切换工作分支：submit_mr 只提交工作区记录的工作分支，检出的不是它时会拒绝提交；要换分支就 new 新开一个工作区。只想跑命令或开浏览器、不需要代码时传 repositories: [] 开空工作区。子 Agent 不能调用本工具：子 Agent 运行期间打开或切换工作区会被拒绝；子 Agent 共享 Main Bot 已打开的这个工作区，使用当前模式提供的文件与命令工具。",
		InputSchema: json.RawMessage(workspaceOpenSchema),
		Kind:        "workspace",
	}
}

// workspaceOpenDescriptorFor lists the environments and repositories this
// conversation may choose from, so the model can pick without extra lookups.
func (h *agentToolGateway) workspaceOpenDescriptorFor(s *AgentToolSession) agentToolDescriptor {
	d := workspaceOpenDescriptor()
	var b strings.Builder
	b.WriteString(d.Description)
	if fixed := strings.TrimSpace(s.SandboxProfileID); fixed != "" {
		fmt.Fprintf(&b, "\n\n用户已在高级设置中指定运行环境 %s，不要填 sandboxProfileId。", fixed)
	} else {
		b.WriteString("\n\n可选运行环境（sandboxProfileId，根据仓库语言和工具链选择，拿不准用 generic-docker）：")
		for _, profile := range h.repo.ConversationSandboxes(s.ProjectID) {
			name := profile.Name
			if df, ok := h.repo.Dockerfile(profile.DockerfileID); ok && df.Name != "" && df.Name != name {
				name += "，镜像 " + df.Name
			}
			fmt.Fprintf(&b, "\n- %s：%s", profile.ID, truncateRunes(name, 80))
		}
	}
	b.WriteString("\n\n项目仓库（repositories，只选这次要改或要运行的仓库，拿不准就不填、全部检出）：")
	for _, r := range h.repo.Repositories() {
		if r.ProjectID == s.ProjectID {
			fmt.Fprintf(&b, "\n- %s", r.Name)
			if path := strings.Trim(r.GongfengProjectID, "/"); path != "" {
				fmt.Fprintf(&b, "（%s）", path)
			}
		}
	}
	d.Description = b.String()
	return d
}

// usableConversationWorkspace reports whether a bound workspace can still be
// used by a conversation of projectID.
func usableConversationWorkspace(run domain.Run, projectID string) bool {
	return run.ExecutionContext.ProjectID == projectID && run.IsWorkspace() && !domain.TerminalRunStatus(run.Status)
}

func repositoryMatches(id, name, gongfengPath, want string) bool {
	want = strings.Trim(strings.TrimSpace(want), "/")
	path := strings.Trim(gongfengPath, "/")
	return id == want || strings.EqualFold(name, want) || path != "" && (strings.EqualFold(path, want) || strings.HasSuffix(strings.ToLower(path), "/"+strings.ToLower(want)))
}

// selectConversationRepositories returns the wanted repositories of the
// project, or all of them when nothing is wanted; the default repository
// comes first. A scratch workspace checks nothing out.
func selectConversationRepositories(project domain.Project, all []domain.Repository, want []string, scratch bool) ([]domain.RepositoryContext, error) {
	if scratch {
		return []domain.RepositoryContext{}, nil
	}
	var candidates []domain.Repository
	names := []string{}
	for _, r := range all {
		if r.ProjectID != project.ID {
			continue
		}
		if r.ID == project.DefaultRepositoryID {
			candidates = append([]domain.Repository{r}, candidates...)
		} else {
			candidates = append(candidates, r)
		}
		names = append(names, r.Name)
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("项目「%s」还没有配置代码仓库，无法检出代码；请项目 Owner 在项目设置里添加仓库。只想跑命令或开浏览器时传 repositories: [] 开一个空工作区", project.Name)
	}
	selected := map[string]bool{}
	for _, w := range want {
		found := false
		for _, r := range candidates {
			if repositoryMatches(r.ID, r.Name, r.GongfengProjectID, w) {
				selected[r.ID], found = true, true
			}
		}
		if !found {
			return nil, fmt.Errorf("项目里没有仓库「%s」，可选：%s", strings.TrimSpace(w), strings.Join(names, "、"))
		}
	}
	contexts := []domain.RepositoryContext{}
	for _, r := range candidates {
		if len(want) == 0 || selected[r.ID] {
			contexts = append(contexts, repositoryRunContext(r))
		}
	}
	return contexts, nil
}

// missingRepositories lists the wanted repositories a reused workspace did not
// check out.
func missingRepositories(run domain.Run, want []string) []string {
	missing := []string{}
	for _, w := range want {
		found := false
		for _, rc := range run.RepositoryContexts() {
			found = found || repositoryMatches(rc.RepositoryID, rc.Name, rc.GongfengProjectID, w)
		}
		if !found {
			missing = append(missing, strings.TrimSpace(w))
		}
	}
	return missing
}

type workspaceOpenChoice struct {
	Goal         string
	SandboxID    string
	Repositories []string
	// Scratch is an explicit empty repositories list: a workspace without
	// any checkout.
	Scratch bool
	// Branch names the branch of a single repository; RepositoryBranches
	// picks per repository. BaseBranch is where new work branches start and
	// what MRs target. All only apply to a workspace this call creates.
	Branch             string
	BaseBranch         string
	RepositoryBranches map[string]workspaceBranchChoice
	// BranchMode is auto (the platform names a new branch), continue (resume
	// the named branch), create (the named branch must not exist yet) or
	// readonly (check the named branch out without a work branch). Empty
	// resolves to continue when a branch is selected, else auto.
	BranchMode string
	// At most one of IssueID, WorkspaceRunID and New picks the workspace;
	// none keeps the bound one.
	IssueID        string
	WorkspaceRunID string
	New            bool
	// Requirement is the new requirement Main Bot identified for a
	// conversation workspace.
	Requirement conversationRequirement
}

func parseWorkspaceOpenChoice(args map[string]any) (workspaceOpenChoice, error) {
	choice := workspaceOpenChoice{}
	text := func(key string) string {
		value, _ := args[key].(string)
		return strings.TrimSpace(value)
	}
	choice.Goal = text("goal")
	if choice.Goal == "" {
		return choice, fmt.Errorf("goal is required")
	}
	choice.SandboxID = text("sandboxProfileId")
	choice.Branch = text("branch")
	choice.BaseBranch = text("baseBranch")
	mode, err := parseWorkspaceBranchMode(args["branchMode"], "branchMode")
	if err != nil {
		return choice, err
	}
	choice.BranchMode = mode
	if err := parseWorkspaceRepositoryBranches(args, &choice); err != nil {
		return choice, err
	}
	choice.IssueID = text("issueId")
	choice.WorkspaceRunID = text("workspaceRunId")
	choice.New, _ = args["new"].(bool)
	requirement, err := parseConversationRequirement(args)
	if err != nil {
		return choice, err
	}
	choice.Requirement = requirement
	targets := 0
	for _, set := range []bool{choice.IssueID != "", choice.WorkspaceRunID != "", choice.New} {
		if set {
			targets++
		}
	}
	if targets > 1 {
		return choice, fmt.Errorf("issueId、workspaceRunId、new 最多传一个")
	}
	if raw, ok := args["repositories"]; ok && raw != nil {
		items, ok := raw.([]any)
		if !ok {
			return choice, fmt.Errorf("repositories 必须是仓库名数组")
		}
		choice.Scratch = len(items) == 0
		for _, item := range items {
			name, ok := item.(string)
			if !ok {
				return choice, fmt.Errorf("repositories 必须是仓库名数组")
			}
			if name = strings.TrimSpace(name); name != "" {
				choice.Repositories = append(choice.Repositories, name)
			}
		}
	}
	if err := choice.resolveBranchMode(); err != nil {
		return choice, err
	}
	return choice, nil
}

const (
	workspaceBranchModeAuto     = "auto"
	workspaceBranchModeContinue = "continue"
	workspaceBranchModeCreate   = "create"
	workspaceBranchModeReadOnly = "readonly"
)

// workspaceBranchChoice is the branch selection of one repository. Empty Mode
// and BaseBranch inherit the call's branchMode and baseBranch.
type workspaceBranchChoice struct {
	Branch     string
	Mode       string
	BaseBranch string
}

func parseWorkspaceBranchMode(raw any, field string) (string, error) {
	if raw == nil {
		return "", nil
	}
	value, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("%s 必须是 auto、continue、create 或 readonly", field)
	}
	switch mode := strings.ToLower(strings.TrimSpace(value)); mode {
	case "":
		return "", nil
	case workspaceBranchModeAuto, workspaceBranchModeContinue, workspaceBranchModeCreate, workspaceBranchModeReadOnly:
		return mode, nil
	default:
		return "", fmt.Errorf("%s 必须是 auto、continue、create 或 readonly，收到 %q", field, value)
	}
}

// branchSelected reports whether the caller chose branches for a new workspace.
func (c workspaceOpenChoice) branchSelected() bool {
	return c.Branch != "" || c.BaseBranch != "" || len(c.RepositoryBranches) > 0
}

// inheritsBranchMode reports whether some named branch uses the call's
// branchMode rather than a mode of its own.
func (c workspaceOpenChoice) inheritsBranchMode() bool {
	if c.Branch != "" {
		return true
	}
	for _, entry := range c.RepositoryBranches {
		if entry.Branch != "" && entry.Mode == "" {
			return true
		}
	}
	return false
}

// resolveBranchMode turns the optional branchMode into this call's behaviour.
// auto names a new branch, continue resumes the named one, create requires the
// name to be new and readonly checks it out without a work branch. Callers
// that already pass branch without a mode keep the continue behaviour they had
// before branchMode existed.
func (c *workspaceOpenChoice) resolveBranchMode() error {
	if c.Scratch && c.branchSelected() {
		return fmt.Errorf("repositories: [] 开的是不检出仓库的空工作区，不要同时传 branch/baseBranch/repositoryBranches")
	}
	inherits := c.inheritsBranchMode()
	switch c.BranchMode {
	case "":
		if inherits {
			c.BranchMode = workspaceBranchModeContinue
		} else {
			c.BranchMode = workspaceBranchModeAuto
		}
		return nil
	case workspaceBranchModeAuto:
		if inherits {
			return fmt.Errorf("branchMode=auto 表示由平台自动命名新建分支，不要同时传分支名：继续已有分支用 continue，指定名字新建用 create，只读检出用 readonly")
		}
		return nil
	default: // continue, create, readonly
		if !inherits {
			return fmt.Errorf("branchMode=%s 需要同时指定分支：单仓库用 branch，多仓库用 repositoryBranches", c.BranchMode)
		}
		return nil
	}
}

// openConversationWorkspace binds the workspace Main Bot picked to an ordinary
// conversation and checks the code out so failures such as a missing Git
// Token surface immediately.
func (h *agentToolGateway) openConversationWorkspace(ctx context.Context, s *AgentToolSession, args map[string]any) (string, error) {
	if !s.canOpenConversationWorkspace() {
		return "", fmt.Errorf("任务会话直接使用任务工作区，不需要再开")
	}
	choice, err := parseWorkspaceOpenChoice(args)
	if err != nil {
		return "", err
	}
	bound, err := h.bindConversationWorkspace(ctx, s, choice)
	if err != nil {
		return "", err
	}
	run := bound.Run
	repositories := []map[string]any{}
	readOnly := []string{}
	for _, rc := range run.RepositoryContexts() {
		entry := map[string]any{"name": rc.Name, "dir": rc.WorkspaceDir, "baseBranch": repositoryBaseBranchName(rc), "workBranch": rc.WorkBranch}
		if rc.ReadOnly {
			entry["readOnly"] = true
			readOnly = append(readOnly, rc.Name+"（"+repositoryBaseBranchName(rc)+"）")
		}
		repositories = append(repositories, entry)
	}
	out := map[string]any{"runId": run.ID, "reused": bound.Reused, "created": bound.Created, "sandboxProfileId": run.WorkflowSnapshot.SandboxProfileID, "repositories": repositories}
	var notice strings.Builder
	switch {
	case !run.IsWorkspace():
		out["sopRunId"], out["sopStatus"], out["issueId"] = run.ID, run.Status, run.ExecutionContext.IssueID
		fmt.Fprintf(&notice, "本对话现在绑定需求 %s 的 SOP 运行 %s 的工作区。", run.ExecutionContext.IssueID, run.ID)
	case bound.Created:
		notice.WriteString("已为本对话新开工作区并绑定。")
	case bound.Reused:
		notice.WriteString("继续使用本对话当前绑定的工作区。")
	default:
		notice.WriteString("已切回本对话之前开过的工作区。")
	}
	if len(bound.Branches) > 0 {
		fmt.Fprintf(&notice, "分支：%s。", strings.Join(bound.Branches, "；"))
	}
	if len(run.RepositoryContexts()) == 0 {
		notice.WriteString("这是不检出任何仓库的空工作区，可以跑命令、起服务、开浏览器，没有可提交的代码。")
	} else {
		notice.WriteString("每个仓库检出在 repositories[].dir 目录下，文件路径和命令都相对工作区根目录（例如 cd <dir> && go test ./...）。")
	}
	if len(readOnly) > 0 {
		fmt.Fprintf(&notice, "只读检出的仓库：%s，可以读、构建、跑测试，submit_mr 不会提交它们的改动。", strings.Join(readOnly, "、"))
	}
	if run.IsWorkspace() && len(run.RepositoryContexts()) > 0 {
		notice.WriteString("交接后使用 CLI 原生命令工具跑相关测试；用户要求提交时调用 workbench_workspace_submit_mr（默认开 MR，目标是 repositories[].baseBranch；用户要把改动合并进 preview/test 这类共享分支并直接推送、不要 MR 时传 mode=direct + targetBranch）。不要在终端里 git push，也不要 git checkout/switch 切换工作分支：submit_mr 只提交 repositories[].workBranch，检出的不是它时会拒绝提交。")
	} else if !run.IsWorkspace() {
		notice.WriteString(borrowedSOPNotice(run))
	}
	if !bound.Created {
		if missing := missingRepositories(run, choice.Repositories); len(missing) > 0 {
			out["missingRepositories"] = missing
			notice.WriteString("这个工作区没有检出 missingRepositories，仓库已固定；需要这些仓库时传 new 新开一个工作区。")
		}
		if choice.SandboxID != "" && choice.SandboxID != run.WorkflowSnapshot.SandboxProfileID {
			notice.WriteString("这个工作区的环境已固定为 sandboxProfileId，本次选择的环境没有生效；需要别的环境时传 new 新开一个工作区。")
		}
	}
	if run.IsWorkspace() {
		issue, created, err := h.linkConversationRequirement(s, run, choice.Requirement, false)
		if err != nil {
			log.Printf("link conversation workspace %s to its requirement: %v", run.ID, err)
		}
		if issue.ID != "" {
			out["issueId"], out["issueTitle"] = issue.ID, issue.Title
			if created {
				fmt.Fprintf(&notice, "已在项目需求列表创建需求 %s「%s」，关联到本对话和这个工作区，之后提交的 MR 会挂到这个需求上。", issue.ID, truncateRunes(issue.Title, 60))
			} else {
				fmt.Fprintf(&notice, "这个工作区关联需求 %s「%s」。", issue.ID, truncateRunes(issue.Title, 60))
			}
		}
	}
	if previous, ok := h.repo.RunState(bound.Previous); ok {
		out["previousRunId"] = previous.ID
		fmt.Fprintf(&notice, "之前绑定的工作区 %s 已解绑，空闲时保存快照并回收，切回时恢复。", previous.ID)
		if bound.CheckpointError != "" {
			out["previousCheckpointError"] = bound.CheckpointError
			notice.WriteString("切换前保存它的快照没有成功，它的文件仍原样保留，回收前会再次保存，保存成功之前不会回收。")
		}
		if !previous.IsWorkspace() && !domain.TerminalRunStatus(previous.Status) && previous.PauseRequested {
			notice.WriteString("它所属的 SOP 运行仍处于暂停状态，要让它继续，先切回去再调用 workbench_workspace_sop resume。")
		}
	}
	treeCtx := context.WithValue(ctx, mainBotWorkspaceKey{}, run.ID)
	tree, err := h.callMainBotWorkspace(treeCtx, s, "workbench_workspace_tree", map[string]any{})
	if err != nil {
		if current, ok := h.repo.RunState(run.ID); ok && h.engine != nil {
			if progress := current.LatestWorkspaceOpening(); progress == nil || progress.State != "failed" {
				h.engine.RecordWorkspaceOpening(run.ID, domain.WorkspaceOpening{State: "failed", Message: err.Error()})
			}
		}
		if bound.Created && missingBranchHint(err, run.RepositoryContexts()) != "" {
			h.discardConversationWorkspace(s, bound)
			return "", fmt.Errorf("工作区准备失败（已放弃这个工作区）：%s%v", missingBranchHint(err, run.RepositoryContexts()), err)
		}
		return "", fmt.Errorf("工作区 %s 准备失败，已保留绑定和已完成的检出。修复后用 workbench_workspace_open 重试当前工作区（不要传 new=true）：%s", run.ID, workspaceprep.Sanitize(err.Error()))
	}
	if h.engine != nil {
		warning := h.engine.WorkspaceDockerfileWarning(run)
		if warning != "" {
			out["environmentWarning"] = warning
			notice.WriteString(warning)
		}
		if current, ok := h.repo.RunState(run.ID); ok {
			if progress := current.LatestWorkspaceOpening(); progress == nil || progress.State != "ready" || progress.Warning != warning {
				h.engine.RecordWorkspaceOpening(run.ID, domain.WorkspaceOpening{State: "ready", Message: "工作区已就绪"})
			}
		}
	}
	out["files"] = json.RawMessage(truncateJSONForTool(tree, 6000))
	out["notice"] = notice.String()
	if run.IsWorkspace() || sopStopped(run) {
		s.requestMode(domain.AssistantModeWorkspace)
		out["handoff"] = true
		notice.WriteString("工作区已准备。" + assistantHandoffNotice)
	} else {
		out["handoff"] = false
		// A running SOP owns its environment. Keep the read-only platform
		// view until pause/takeover has stopped it, then hand off the CLI.
		if s.RuntimeMode == domain.AssistantModeWorkspace {
			s.requestMode(domain.AssistantModePlatform)
		}
		notice.WriteString("当前保留平台只读查看；暂停或接管完成后进入原生工作区模式。")
	}
	out["notice"] = notice.String()
	raw, _ := json.Marshal(out)
	return string(raw), nil
}

var missingRemoteBranch = regexp.MustCompile(`Remote branch (\S+) not found in upstream`)

// missingBranchHint names the repository whose start branch git could not
// find, so a misspelled baseBranch or readonly branch reads as such instead
// of a raw clone failure.
func missingBranchHint(err error, repositories []domain.RepositoryContext) string {
	match := missingRemoteBranch.FindStringSubmatch(err.Error())
	if match == nil {
		return ""
	}
	branch := match[1]
	names := []string{}
	for _, rc := range repositories {
		if repositoryBaseBranchName(rc) == branch {
			names = append(names, rc.Name)
		}
	}
	where := "仓库"
	if len(names) > 0 {
		where = "仓库 " + strings.Join(names, "、")
	}
	return fmt.Sprintf("%s的远端没有分支 %s，检查 baseBranch（readonly 时是 branch）是否拼写正确。原始错误：", where, branch)
}

// discardConversationWorkspace cancels a workspace this call created but could
// not check out, and puts the conversation back on the workspace it used before.
// A failed branch selection therefore never leaves the session bound to a
// workspace that cannot be opened.
func (h *agentToolGateway) discardConversationWorkspace(s *AgentToolSession, bound conversationWorkspaceBinding) {
	if h.engine != nil {
		if err := h.engine.Cancel(bound.Run.ID); err != nil {
			log.Printf("cancel unprepared conversation workspace %s: %v", bound.Run.ID, err)
		}
		if err := h.engine.ReleaseConversationWorkspace(bound.Run.ID); err != nil {
			log.Printf("release unprepared conversation workspace %s: %v", bound.Run.ID, err)
		}
	}
	s.workspaceRunID = bound.Previous
	if err := h.repo.SetAssistantSessionWorkspaceRun(s.ChatOwnerID, s.ChatSessionID, bound.Previous, false); err != nil {
		log.Printf("rebind conversation workspace after failure session=%s run=%s: %v", s.ChatSessionID, bound.Previous, err)
	}
}

type conversationWorkspaceBinding struct {
	Run domain.Run
	// Reused is set when Run was already bound; Created when this call opened
	// it.
	Reused  bool
	Created bool
	// Previous is the workspace this call switched away from.
	Previous string
	// Branches are the existing branches a workspace this call created
	// continues.
	Branches []string
	// CheckpointError is why saving Previous before the switch failed.
	CheckpointError string
}

// bindConversationWorkspace binds the workspace the choice picks: an issue's
// SOP run, a workspace the conversation opened earlier, the bound one, or a
// new one in the user's fixed sandbox, else the model's choice, else
// generic-docker. The workspace it switches away from is released.
func (h *agentToolGateway) bindConversationWorkspace(ctx context.Context, s *AgentToolSession, choice workspaceOpenChoice) (conversationWorkspaceBinding, error) {
	if s.workspaceTurn != nil {
		s.workspaceTurn.operations.Lock()
		defer s.workspaceTurn.operations.Unlock()
		// Refuse before a new workspace run is created that nothing would bind.
		if err := s.workspaceTurn.usable(); err != nil {
			return conversationWorkspaceBinding{}, err
		}
	}
	s.workspaceMu.Lock()
	defer s.workspaceMu.Unlock()
	project, ok := h.repo.Project(s.ProjectID)
	if !ok {
		return conversationWorkspaceBinding{}, fmt.Errorf("项目不存在")
	}
	conv, _ := h.repo.AssistantSession(s.ChatOwnerID, s.ChatSessionID)
	binding := conversationWorkspaceBinding{}
	switch {
	case choice.IssueID != "":
		issue, ok := h.repo.Issue(choice.IssueID)
		if !ok || issue.ProjectID != s.ProjectID {
			return binding, fmt.Errorf("需求 %s 不存在或不属于当前项目", choice.IssueID)
		}
		if binding.Run, ok = issueSOPRun(h.deps, s.ProjectID, issue.ID); !ok {
			return binding, fmt.Errorf("需求 %s 还没跑过 SOP，没有它的工作区：沿用当前工作区，或传 new 新开一个", issue.ID)
		}
	case choice.WorkspaceRunID != "":
		run, ok := h.repo.RunState(choice.WorkspaceRunID)
		if !ok || !conversationMayBind(conv, run, s.ProjectID) {
			return binding, fmt.Errorf("工作区 %s 不是本对话开过的可用工作区，也不是本项目的 SOP 运行", choice.WorkspaceRunID)
		}
		binding.Run = run
	case !choice.New && s.workspaceRunID != "":
		run, ok := h.repo.RunState(s.workspaceRunID)
		if !ok || !conversationMayBind(conv, run, s.ProjectID) {
			return binding, fmt.Errorf("当前绑定的工作区 %s 已不可用：传 new 新开一个，或切到其他工作区", s.workspaceRunID)
		}
		binding.Run = run
	default:
		run, branches, err := h.startConversationWorkspace(s, project, conv, choice)
		if err != nil {
			return binding, err
		}
		binding.Run, binding.Created, binding.Branches = run, true, branches
	}
	if binding.Run.ExecutionContext.ExecutionRunnerID != conv.Execution().RunnerID {
		return binding, fmt.Errorf("工作区与会话绑定的执行机器不同，请在相应机器新建对话")
	}
	if binding.Run.WorkflowSnapshot.SandboxSnapshot != nil && binding.Run.WorkflowSnapshot.SandboxSnapshot.Profile.Driver != "docker" {
		return binding, fmt.Errorf("对话工作区仅支持 Docker")
	}
	if !binding.Created && choice.branchSelected() {
		// A selected branch belongs to the checkout this call creates: a reused
		// workspace keeps the branch it was opened on.
		return binding, fmt.Errorf("指定分支（branchMode/branch/baseBranch/repositoryBranches）只在开新工作区时生效：当前工作区的分支已固定，需要别的分支请传 new 新开一个工作区")
	}
	if binding.Run.ID == s.workspaceRunID {
		binding.Reused = true
		return binding, nil
	}
	if s.workspaceTurn != nil {
		// A failed save only defers reclamation: the idle sweep saves a released
		// workspace again before reclaiming it and keeps its files if that save
		// fails too. Blocking the switch would strand the turn on a workspace that
		// cannot be saved (too large, or slower than the save timeout).
		if err := s.workspaceTurn.checkpoint(ctx); errors.Is(err, errAssistantTurnClosed) {
			if binding.Created {
				_ = h.engine.ReleaseConversationWorkspace(binding.Run.ID)
			}
			return conversationWorkspaceBinding{}, err
		} else if err != nil {
			binding.CheckpointError = err.Error()
			log.Printf("checkpoint conversation workspace before switch session=%s: %v", s.ChatSessionID, err)
		}
	}
	binding.Previous = s.workspaceRunID
	s.workspaceRunID = binding.Run.ID
	// The workspace still serves this turn if the binding cannot be saved; the
	// end-of-turn save writes it again.
	if err := h.repo.SetAssistantSessionWorkspaceRun(s.ChatOwnerID, s.ChatSessionID, binding.Run.ID, binding.Run.IsWorkspace()); err != nil {
		log.Printf("bind conversation workspace session=%s run=%s: %v", s.ChatSessionID, binding.Run.ID, err)
	}
	if binding.Previous != "" && h.engine != nil {
		if err := h.engine.ReleaseConversationWorkspace(binding.Previous); err != nil {
			log.Printf("release conversation workspace session=%s run=%s: %v", s.ChatSessionID, binding.Previous, err)
		}
	}
	return binding, nil
}

func (h *agentToolGateway) startConversationWorkspace(s *AgentToolSession, project domain.Project, conv domain.AssistantSession, choice workspaceOpenChoice) (domain.Run, []string, error) {
	repositories, err := selectConversationRepositories(project, h.repo.Repositories(), choice.Repositories, choice.Scratch)
	if err != nil {
		return domain.Run{}, nil, err
	}
	repositories, branches, err := selectWorkspaceBranches(repositories, choice)
	if err != nil {
		return domain.Run{}, nil, err
	}
	if err := h.refuseRecreatedBranch(conv, project.ID, repositories); err != nil {
		return domain.Run{}, nil, err
	}
	chosen := strings.TrimSpace(s.SandboxProfileID)
	if chosen == "" {
		chosen = choice.SandboxID
	}
	sandbox, err := h.repo.ResolveConversationSandbox(project.ID, chosen)
	if err != nil {
		return domain.Run{}, nil, err
	}
	if h.engine == nil {
		return domain.Run{}, nil, fmt.Errorf("执行引擎不可用")
	}
	run, err := h.engine.StartConversationWorkspace("对话改代码："+truncateRunes(choice.Goal, 200), repositories, domain.TelemetryContext{ProjectID: project.ID, ResponsibleUserID: s.UserID, ExecutionPinned: true, ExecutionMode: conv.Execution().Pool, ExecutionRunnerID: conv.Execution().RunnerID}, sandbox.ID)
	return run, branches, err
}

func truncateJSONForTool(raw string, limit int) string {
	if len(raw) <= limit {
		return raw
	}
	data, _ := json.Marshal(map[string]any{"truncated": true, "preview": truncateRunes(raw, limit)})
	return string(data)
}

func repositoryRunContext(r domain.Repository) domain.RepositoryContext {
	return domain.RepositoryContext{RepositoryID: r.ID, Provider: r.Provider, Name: r.Name, GongfengProjectID: r.GongfengProjectID, WebURL: r.WebURL, CloneURL: r.CloneURL, BaseBranch: r.DefaultBranch, DefaultBranch: r.DefaultBranch, LocalPath: r.LocalPath, PipelineProjectID: r.PipelineProjectID, PipelineDefID: r.PipelineDefID, PipelineURLPattern: r.PipelineURLPattern}
}

// parseWorkspaceRepositoryBranches reads the per-repository branch selection.
// Keys are repository names or IDs; a value is either a branch name that uses
// the call's branchMode, or {branch, branchMode, baseBranch} for a repository
// with semantics of its own.
func parseWorkspaceRepositoryBranches(args map[string]any, choice *workspaceOpenChoice) error {
	raw, ok := args["repositoryBranches"]
	if !ok || raw == nil {
		return nil
	}
	items, ok := raw.(map[string]any)
	if !ok {
		return fmt.Errorf("repositoryBranches 必须是仓库到分支的对象")
	}
	for key, value := range items {
		key = strings.TrimSpace(key)
		entry := workspaceBranchChoice{}
		switch value := value.(type) {
		case string:
			entry.Branch = strings.TrimSpace(value)
		case map[string]any:
			for field, raw := range value {
				switch field {
				case "branch", "baseBranch":
					text, ok := raw.(string)
					if !ok && raw != nil {
						return fmt.Errorf("repositoryBranches 里仓库 %s 的 %s 必须是字符串", key, field)
					}
					if field == "branch" {
						entry.Branch = strings.TrimSpace(text)
					} else {
						entry.BaseBranch = strings.TrimSpace(text)
					}
				case "branchMode":
					mode, err := parseWorkspaceBranchMode(raw, "repositoryBranches 里仓库 "+key+" 的 branchMode")
					if err != nil {
						return err
					}
					entry.Mode = mode
				default:
					return fmt.Errorf("repositoryBranches 里仓库 %s 只支持 branch、branchMode、baseBranch，收到 %s", key, field)
				}
			}
		default:
			return fmt.Errorf("repositoryBranches 里仓库 %s 的值必须是分支名，或 {branch, branchMode, baseBranch} 对象", key)
		}
		if key == "" || entry == (workspaceBranchChoice{}) {
			continue
		}
		if choice.RepositoryBranches == nil {
			choice.RepositoryBranches = map[string]workspaceBranchChoice{}
		}
		choice.RepositoryBranches[key] = entry
	}
	return nil
}

// workspaceBranchCheckout is how one repository of a new workspace is checked
// out, after inheriting the call's defaults.
type workspaceBranchCheckout struct {
	Mode       string
	Branch     string
	BaseBranch string
}

// resolveRepositoryBranch merges one repository's selection with the call's
// branchMode and baseBranch. A repository without a branch of its own is
// auto, whatever the call's mode is.
func (c workspaceOpenChoice) resolveRepositoryBranch(entry workspaceBranchChoice, single bool) workspaceBranchCheckout {
	out := workspaceBranchCheckout{Mode: entry.Mode, Branch: entry.Branch, BaseBranch: entry.BaseBranch}
	if out.Branch == "" && single {
		out.Branch = c.Branch
	}
	if out.Mode == "" {
		out.Mode = workspaceBranchModeAuto
		if out.Branch != "" {
			out.Mode = c.BranchMode
			if out.Mode == "" {
				out.Mode = workspaceBranchModeContinue
			}
		}
	}
	if out.BaseBranch == "" && out.Mode != workspaceBranchModeReadOnly {
		out.BaseBranch = c.BaseBranch
	}
	return out
}

// selectWorkspaceBranches applies the branch selection to the repositories of
// a new conversation workspace: each repository continues, creates, read-only
// checks out or auto-names its branch, starting from its own base. It returns
// the repositories to check out and the per-repository lines to report back.
func selectWorkspaceBranches(repositories []domain.RepositoryContext, choice workspaceOpenChoice) ([]domain.RepositoryContext, []string, error) {
	if !choice.branchSelected() {
		return repositories, nil, nil
	}
	if choice.Branch != "" && len(repositories) != 1 {
		return nil, nil, fmt.Errorf("branch 只支持单个代码仓库；多个仓库请用 repositoryBranches 按仓库分别指定分支，需要不同模式时写成 {branch, branchMode, baseBranch} 对象")
	}
	selected := append([]domain.RepositoryContext(nil), repositories...)
	entries := make([]workspaceBranchChoice, len(selected))
	keys := make([]string, 0, len(choice.RepositoryBranches))
	for key := range choice.RepositoryBranches {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		index := -1
		for i, repository := range selected {
			if repositoryMatches(repository.RepositoryID, repository.Name, repository.GongfengProjectID, key) {
				index = i
				break
			}
		}
		if index < 0 {
			names := make([]string, 0, len(selected))
			for _, repository := range selected {
				names = append(names, repository.Name)
			}
			return nil, nil, fmt.Errorf("本次检出的仓库里没有「%s」，可选：%s", key, strings.Join(names, "、"))
		}
		entries[index] = choice.RepositoryBranches[key]
	}
	picked := []string{}
	baseUsed := false
	for index := range selected {
		checkout := choice.resolveRepositoryBranch(entries[index], len(selected) == 1)
		if entries[index].BaseBranch == "" && checkout.Mode != workspaceBranchModeReadOnly {
			baseUsed = true
		}
		line, err := applyWorkspaceBranch(&selected[index], checkout)
		if err != nil {
			return nil, nil, err
		}
		if line != "" {
			picked = append(picked, line)
		}
	}
	if choice.BaseBranch != "" && !baseUsed {
		return nil, nil, fmt.Errorf("baseBranch=%s 没有仓库用得上：readonly 直接只读检出 branch 指定的分支，不从起点拉出；只想看 %s 就把 branch 设成它，要从它新建分支用 create 或 auto", choice.BaseBranch, choice.BaseBranch)
	}
	return selected, picked, nil
}

// applyWorkspaceBranch sets one repository's checkout and returns the line
// that reports it, or "" for a repository left entirely on the defaults.
func applyWorkspaceBranch(repository *domain.RepositoryContext, checkout workspaceBranchCheckout) (string, error) {
	branch, err := normalizeIssueBaseBranch(checkout.Branch)
	if err != nil {
		return "", fmt.Errorf("仓库 %s：%w", repository.Name, err)
	}
	base, err := normalizeIssueBaseBranch(checkout.BaseBranch)
	if err != nil {
		return "", fmt.Errorf("仓库 %s 的 baseBranch %q 不是合法的分支名", repository.Name, checkout.BaseBranch)
	}
	if base != "" {
		repository.BaseBranch = base
	}
	base = repositoryBaseBranchName(*repository)
	label := repository.Name + " → "
	switch checkout.Mode {
	case workspaceBranchModeAuto:
		if branch != "" {
			return "", fmt.Errorf("仓库 %s：branchMode=auto 由平台自动命名新建分支，不要同时给分支名 %s", repository.Name, branch)
		}
		if checkout.BaseBranch == "" {
			return "", nil
		}
		return label + "自动命名新分支（起点 " + base + "）", nil
	case workspaceBranchModeReadOnly:
		if branch == "" {
			return "", fmt.Errorf("仓库 %s：branchMode=readonly 需要指定要检出的分支", repository.Name)
		}
		if checkout.BaseBranch != "" {
			return "", fmt.Errorf("仓库 %s：branchMode=readonly 直接只读检出 %s，不要同时传 baseBranch", repository.Name, branch)
		}
		repository.BaseBranch, repository.WorkBranch, repository.ReadOnly = branch, "", true
		return label + branch + "（只读，不提交）", nil
	}
	if branch == "" {
		return "", fmt.Errorf("仓库 %s：branchMode=%s 需要指定分支名", repository.Name, checkout.Mode)
	}
	if workspaceSharedBranch(branch, *repository) {
		return "", fmt.Errorf("仓库 %s：不能直接在共享分支 %s 上开发：继续或新建请填写功能分支，只想在它上面跑测试或复现问题用 branchMode=readonly", repository.Name, branch)
	}
	repository.WorkBranch = branch
	repository.RequireExistingWorkBranch = checkout.Mode == workspaceBranchModeContinue
	repository.CreateNewWorkBranch = checkout.Mode == workspaceBranchModeCreate
	if repository.CreateNewWorkBranch {
		return label + branch + "（新建，起点 " + base + "）", nil
	}
	return label + branch + "（继续已有分支，MR 默认目标 " + base + "）", nil
}

// refuseRecreatedBranch refuses to create a branch that a workspace this
// conversation opened earlier already works on. Its commits may
// never have reached the remote, so creating the branch again from the base
// would silently start over without them.
func (h *agentToolGateway) refuseRecreatedBranch(conv domain.AssistantSession, projectID string, repositories []domain.RepositoryContext) error {
	for _, rc := range repositories {
		if !rc.CreateNewWorkBranch {
			continue
		}
		for _, id := range ownWorkspaceRunIDs(conv) {
			run, ok := h.repo.RunState(id)
			if !ok || !usableConversationWorkspace(run, projectID) {
				continue
			}
			for _, earlier := range run.RepositoryContexts() {
				if earlier.RepositoryID == rc.RepositoryID && earlier.WorkBranch == rc.WorkBranch {
					return fmt.Errorf("仓库 %s 的分支 %s 是本对话之前的工作区 %s 建的，没推送的提交只在那个工作区（及其快照）里，重新新建会从起点开始、丢掉这些提交：要接着做就传 workspaceRunId=%s 切回去；确实要重新开始就换一个分支名", rc.Name, rc.WorkBranch, run.ID, run.ID)
				}
			}
		}
	}
	return nil
}

// workspaceSharedBranch reports whether branch is a baseline a conversation
// must never work on directly.
func workspaceSharedBranch(branch string, repository domain.RepositoryContext) bool {
	branch = strings.TrimSpace(branch)
	if branch == "" {
		return false
	}
	for _, shared := range []string{repository.BaseBranch, repository.DefaultBranch, repository.IntegrateBranch, "master", "main"} {
		if branch == strings.TrimSpace(shared) {
			return true
		}
	}
	return false
}
