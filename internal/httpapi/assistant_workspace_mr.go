package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/gitwork"
)

// The agent delivers a conversation workspace with its own git: it pushes and
// opens the GitHub PR itself using the speaker's Git credentials. Platform
// bookkeeping (requirement link, merge tracking, requirement status) only
// needs the resulting PRs, which registerTurnDeliveries finds after each turn.

// githubAPIBase is api.github.com; tests point it at a fake server.
var githubAPIBase = "https://api.github.com"

const (
	turnDeliveryTimeout  = 20 * time.Second
	turnDeliveryRepoMax  = 10
	turnDeliveryPullsMax = 30
)

type deliveredMR struct {
	Repository   string
	URL          string
	TargetBranch string
	// Project 与 IID 由 turn 结束时的发现逻辑直接给出（GitHub PR / GitLab MR），
	// 避免只靠 URL 反解——GitLab MR URL 不在 Gongfeng 解析规则内。
	Project string
	IID     int
}

func projectRepositories(all []domain.Repository, projectID string) []domain.Repository {
	out := []domain.Repository{}
	for _, r := range all {
		if r.ProjectID == projectID {
			out = append(out, r)
		}
	}
	return out
}

type workspacePull struct {
	Number    int        `json:"number"`
	URL       string     `json:"html_url"`
	Title     string     `json:"title"`
	Body      string     `json:"body"`
	State     string     `json:"state"`
	MergedAt  *time.Time `json:"merged_at"`
	CreatedAt time.Time  `json:"created_at"`
	User      struct {
		Login string `json:"login"`
	} `json:"user"`
	Head struct {
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
}

// registerTurnDeliveries runs when a turn ends. GitHub PRs and GitLab MRs the
// speaker opened from the conversation workspace are registered as the
// requirement's delivery (the conversation's open requirement, or a new one
// titled after the first MR), so MR polling completes the requirement once
// they merge. An MR counts when the speaker's token authored it after the
// workspace was created, a local branch in the workspace points at its head
// commit, and no requirement tracks it yet; MRs opened elsewhere are left
// alone. GitLab instances must be admin-declared (allowlist), otherwise they
// are skipped without sending any credential. It returns a note for the reply,
// empty when nothing was registered.
func (h *assistantHandlers) registerTurnDeliveries(ctx context.Context, s *AgentToolSession) string {
	if s == nil || s.RunID != "" || h.engine == nil || strings.TrimSpace(s.User.GitToken) == "" {
		return ""
	}
	runID := s.conversationWorkspace()
	run, ok := h.repo.RunState(runID)
	if runID == "" || !ok || run.ExecutionContext.ProjectID != s.ProjectID || (!run.IsWorkspace() && run.ExecutionContext.IssueID == "") {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), turnDeliveryTimeout)
	defer cancel()
	checkouts, err := h.workspaceCheckouts(ctx, run)
	if err != nil {
		log.Printf("scan conversation workspace %s checkouts: %v", run.ID, err)
		return ""
	}
	heads := map[string]bool{}
	projects := []string{}
	remotes := []string{}
	for _, checkout := range checkouts {
		for _, head := range checkout.Heads {
			heads[head] = true
		}
		for _, remote := range checkout.Remotes {
			clean := withoutURLUserinfo(remote)
			remotes = append(remotes, clean)
			info, ok := domain.ParseRepositoryURL(clean)
			if ok && strings.HasPrefix(info.Project, "github:") && !slices.Contains(projects, info.Project) && len(projects) < turnDeliveryRepoMax {
				projects = append(projects, info.Project)
			}
		}
	}
	// 公网/自建 GitLab 的裸 URL 不在 ParseRepositoryURL 的信任规则内，
	// 但工作区仓库已经绑定并过了 allowlist：按仓库记录反查远端属于哪个
	// gitlab: 项目，再逐个实例查询。
	gitlabProjects := gitlabBoundProjects(remotes, h.repo.Repositories(), s.ProjectID)
	if len(heads) == 0 || (len(projects) == 0 && len(gitlabProjects) == 0) {
		return ""
	}
	deliveries := []turnDelivery{}
	if len(projects) > 0 {
		pulls, err := workspacePulls(ctx, s.User.GitToken, projects, heads, run.CreatedAt)
		if err != nil {
			// GitLab 侧仍可登记，不能因为 GitHub 查询失败就整体放弃。
			log.Printf("find conversation workspace %s pull requests: %v", run.ID, err)
		}
		for _, p := range pulls {
			project, iid, _, ok := domain.ParseGongfengMR(p.URL)
			if !ok {
				continue
			}
			deliveries = append(deliveries, turnDelivery{URL: p.URL, Title: p.Title, Body: p.Body, TargetBranch: p.Base.Ref, Project: project, IID: iid})
		}
	}
	deliveries = append(deliveries, h.gitlabWorkspaceDeliveries(ctx, s.User.GitToken, run, gitlabProjects, heads)...)
	if len(deliveries) == 0 {
		return ""
	}
	keys := make([]domain.MRDeliveryKey, 0, len(deliveries))
	for _, d := range deliveries {
		keys = append(keys, domain.MRDeliveryKey{Project: d.Project, IID: d.IID})
	}
	tracked := h.repo.MRDeliveryStatesMap(keys)
	fresh := make([]turnDelivery, 0, len(deliveries))
	for _, d := range deliveries {
		if strings.TrimSpace(tracked[domain.MRDeliveryKey{Project: d.Project, IID: d.IID}].IssueID) != "" {
			continue
		}
		fresh = append(fresh, d)
	}
	if len(fresh) == 0 {
		return ""
	}
	gateway := &agentToolGateway{deps: h.deps}
	issue, created, err := gateway.linkConversationRequirement(s, run, conversationRequirement{Title: strings.TrimSpace(fresh[0].Title), Description: strings.TrimSpace(fresh[0].Body)}, true)
	if err != nil || issue.ID == "" {
		log.Printf("link conversation workspace %s requirement for its merge requests: %v", run.ID, err)
		return ""
	}
	mrs := make([]deliveredMR, 0, len(fresh))
	links := make([]string, 0, len(fresh))
	for _, d := range fresh {
		mrs = append(mrs, deliveredMR{URL: d.URL, TargetBranch: d.TargetBranch, Project: d.Project, IID: d.IID})
		links = append(links, fmt.Sprintf("[%s](%s)", turnDeliveryLabel(d), d.URL))
	}
	if err := gateway.recordConversationMRs(issue.ID, mrs, projectRepositories(h.repo.Repositories(), s.ProjectID)); err != nil {
		log.Printf("record conversation workspace %s delivery: %v", run.ID, err)
		return ""
	}
	if created {
		return fmt.Sprintf("已新建需求 %s「%s」并登记 %s，合入后需求自动完成。", issue.ID, truncateRunes(issue.Title, 60), strings.Join(links, "、"))
	}
	return fmt.Sprintf("已把 %s 登记到需求 %s「%s」，合入后需求自动完成。", strings.Join(links, "、"), issue.ID, truncateRunes(issue.Title, 60))
}

// workspaceCheckouts lists the git checkouts in a run workspace, on the
// execution machine that holds it.
func (h *assistantHandlers) workspaceCheckouts(ctx context.Context, run domain.Run) ([]gitwork.Checkout, error) {
	if runnerID := h.engine.RunExecutionRunner(run); runnerID != "" {
		if h.runnerControls == nil {
			return nil, errors.New("remote workspace channel is not configured")
		}
		response, err := h.runnerControls.requestWorkspace(ctx, runnerID, domain.RunnerWorkspaceRequest{
			RunID: runnerWorkspaceRunID(run), RunStatus: run.Status, WorkspaceLayout: run.ExecutionContext.RunnerWorkspaceLayout,
			Operation: domain.RunnerWorkspaceOperationCheckouts,
		})
		if err != nil {
			return nil, err
		}
		if response.Error != "" {
			return nil, errors.New(response.Error)
		}
		var payload struct {
			Checkouts []gitwork.Checkout `json:"checkouts"`
		}
		err = json.Unmarshal(response.Payload, &payload)
		return payload.Checkouts, err
	}
	root, reason := h.engine.LocalRunWorkspace(run)
	if reason != "" {
		return nil, errors.New(reason)
	}
	return gitwork.Checkouts(ctx, root), nil
}

// workspacePulls returns the open or merged PRs in projects that the token's
// user created since the given time and whose head commit is in heads.
func workspacePulls(ctx context.Context, token string, projects []string, heads map[string]bool, since time.Time) ([]workspacePull, error) {
	var user struct {
		Login string `json:"login"`
	}
	if err := githubGet(ctx, token, "/user", &user); err != nil {
		return nil, err
	}
	if user.Login == "" {
		return nil, errors.New("GitHub did not return the token's user")
	}
	out := []workspacePull{}
	for _, project := range projects {
		var list []workspacePull
		path := "/repos/" + strings.TrimPrefix(project, "github:") + fmt.Sprintf("/pulls?state=all&sort=created&direction=desc&per_page=%d", turnDeliveryPullsMax)
		if err := githubGet(ctx, token, path, &list); err != nil {
			log.Printf("list pull requests of %s: %v", project, err)
			continue
		}
		for _, p := range list {
			if p.CreatedAt.Before(since) {
				break
			}
			if !strings.EqualFold(p.User.Login, user.Login) || !heads[p.Head.SHA] || (p.State == "closed" && p.MergedAt == nil) || !domain.IsGitHubPRURL(p.URL) {
				continue
			}
			out = append(out, p)
		}
	}
	return out, nil
}

func githubGet(ctx context.Context, token, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, githubAPIBase+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(token))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("GitHub request failed")
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("GitHub returned HTTP %d", res.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(res.Body, 8<<20)).Decode(out)
}

func withoutURLUserinfo(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.User == nil {
		return raw
	}
	u.User = nil
	return u.String()
}
