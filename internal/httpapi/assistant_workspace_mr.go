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
	"sort"
	"strings"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/gitegress"
	"agentflow/internal/gitwork"
)

// The agent delivers a conversation workspace with its own git: it pushes and
// opens the PR or MR itself using the speaker's Git credentials. Platform
// bookkeeping (requirement link, merge tracking, requirement status) only
// needs the resulting PRs, which registerTurnDeliveries finds after each turn.

// githubAPIBase is api.github.com; tests point it at a fake server.
var githubAPIBase = "https://api.github.com"

// gitHostAPIBase and gitHostClient reach a credential's host from the control
// plane; tests point them at a fake server.
var (
	gitHostAPIBase = func(cred domain.GitHTTPCredential) string {
		if cred.Host == "github.com" {
			return githubAPIBase
		}
		return cred.APIBase()
	}
	gitHostClient = gitegress.Client
)

const (
	turnDeliveryTimeout  = 20 * time.Second
	turnDeliveryRepoMax  = 10
	turnDeliveryPullsMax = 30
)

type deliveredMR struct {
	Repository   string
	URL          string
	TargetBranch string
	RegisteredBy int64
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

// workspacePull is a GitHub PR or a GitLab MR in GitHub's field names.
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
	mergeRequest bool
}

func (p workspacePull) link() string {
	if p.mergeRequest {
		return fmt.Sprintf("[MR !%d](%s)", p.Number, p.URL)
	}
	return fmt.Sprintf("[PR #%d](%s)", p.Number, p.URL)
}

// registerTurnDeliveries runs when a turn ends. PRs and MRs the speaker opened
// from a project conversation workspace are registered as the requirement's
// delivery (the conversation's open requirement, or a new one titled after
// the first one), so MR polling completes the requirement once they merge.
// Only hosts this turn fetched a token for are asked, on GitHub (github.com
// and Enterprise) and GitLab. A PR counts when the speaker's token authored it
// after the workspace was created, a local branch in the workspace points at
// its head commit, and no requirement tracks it yet; PRs opened elsewhere are
// left alone. It returns a note for the reply, empty when nothing was
// registered.
func (h *assistantHandlers) registerTurnDeliveries(ctx context.Context, s *AgentToolSession) string {
	if s == nil || s.ProjectID == "" || s.RunID != "" || h.engine == nil {
		return ""
	}
	hosts := s.requestedGitHosts()
	if len(hosts) == 0 {
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
	repos := map[string][]string{}
	count := 0
	for _, checkout := range checkouts {
		for _, head := range checkout.Heads {
			heads[head] = true
		}
		for _, remote := range checkout.Remotes {
			host, path, ok := remoteRepository(remote)
			if !ok || count >= turnDeliveryRepoMax || slices.Contains(repos[host], path) {
				continue
			}
			if platform := hosts[host]; platform == domain.GitPlatformGitHub || platform == domain.GitPlatformGitLab {
				repos[host] = append(repos[host], path)
				count++
			}
		}
	}
	if len(heads) == 0 || len(repos) == 0 {
		return ""
	}
	client := gitHostClient(h.repo.PlatformGitHTTPSAddresses)
	pulls := []workspacePull{}
	for _, host := range sortedKeys(repos) {
		cred, ok := h.repo.GitHTTPCredentialForHost(ctx, s.UserID, host)
		if !ok || cred.Token == "" {
			continue
		}
		var found []workspacePull
		if cred.Platform == domain.GitPlatformGitLab {
			found, err = h.workspaceMergeRequests(ctx, client, cred, repos[host], heads, run.CreatedAt)
		} else {
			found, err = workspacePulls(ctx, client, cred, repos[host], heads, run.CreatedAt)
		}
		if err != nil {
			log.Printf("find conversation workspace %s pull requests on %s: %v", run.ID, host, err)
			continue
		}
		pulls = append(pulls, found...)
	}
	keys := []domain.MRDeliveryKey{}
	for _, p := range pulls {
		if project, iid, _, ok := domain.ParseDeliveryMRURL(p.URL); ok {
			keys = append(keys, domain.MRDeliveryKey{Project: project, IID: iid})
		}
	}
	tracked := h.repo.MRDeliveryStatesMap(keys)
	fresh := []workspacePull{}
	for _, p := range pulls {
		project, iid, _, ok := domain.ParseDeliveryMRURL(p.URL)
		if !ok || strings.TrimSpace(tracked[domain.MRDeliveryKey{Project: project, IID: iid}].IssueID) != "" {
			continue
		}
		fresh = append(fresh, p)
	}
	if len(fresh) == 0 {
		return ""
	}
	gateway := &agentToolGateway{deps: h.deps}
	issue, created, err := gateway.linkConversationRequirement(s, run, conversationRequirement{Title: strings.TrimSpace(fresh[0].Title), Description: strings.TrimSpace(fresh[0].Body)}, true)
	if err != nil || issue.ID == "" {
		log.Printf("link conversation workspace %s requirement for its pull requests: %v", run.ID, err)
		return ""
	}
	mrs := make([]deliveredMR, 0, len(fresh))
	links := make([]string, 0, len(fresh))
	for _, p := range fresh {
		mrs = append(mrs, deliveredMR{URL: p.URL, TargetBranch: p.Base.Ref, RegisteredBy: s.UserID})
		links = append(links, p.link())
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

// remoteRepository reads the HTTPS host and repository path of an HTTPS or
// SSH remote; the host's API is HTTPS whichever protocol Git uses.
func remoteRepository(remote string) (host, path string, ok bool) {
	remote = withoutURLUserinfo(remote)
	if host, ok = gitwork.HTTPSRemoteHost(remote); ok {
		u, err := url.Parse(remote)
		if err != nil {
			return "", "", false
		}
		path = u.Path
	} else if parsed, err := gitwork.ParseSSHRemote(remote); err == nil {
		if host, err = gitwork.HTTPSHostScope(parsed.Host); err != nil {
			return "", "", false
		}
		path = parsed.Path
	} else {
		return "", "", false
	}
	path = strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	if strings.Count(path, "/") < 1 || strings.Contains(path, "..") {
		return "", "", false
	}
	return host, path, true
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
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

// workspacePulls returns the open or merged GitHub PRs in repos (owner/repo)
// that the token's user created since the given time and whose head commit
// is in heads.
func workspacePulls(ctx context.Context, client *http.Client, cred domain.GitHTTPCredentialSecret, repos []string, heads map[string]bool, since time.Time) ([]workspacePull, error) {
	var user struct {
		Login string `json:"login"`
	}
	if err := gitHostGet(ctx, client, cred, "/user", &user); err != nil {
		return nil, err
	}
	if user.Login == "" {
		return nil, errors.New("GitHub did not return the token's user")
	}
	out := []workspacePull{}
	for _, repo := range repos {
		var list []workspacePull
		path := "/repos/" + repo + fmt.Sprintf("/pulls?state=all&sort=created&direction=desc&per_page=%d", turnDeliveryPullsMax)
		if err := gitHostGet(ctx, client, cred, path, &list); err != nil {
			log.Printf("list pull requests of %s/%s: %v", cred.Host, repo, err)
			continue
		}
		for _, p := range list {
			if p.CreatedAt.Before(since) {
				break
			}
			if !strings.EqualFold(p.User.Login, user.Login) || !heads[p.Head.SHA] || (p.State == "closed" && p.MergedAt == nil) || !sameDeliveryHost(p.URL, cred.Host) {
				continue
			}
			out = append(out, p)
		}
	}
	return out, nil
}

// workspaceMergeRequests is workspacePulls for GitLab: MRs in repos
// (group/…/repo) the token's user authored since the given time.
func (h *assistantHandlers) workspaceMergeRequests(ctx context.Context, client *http.Client, cred domain.GitHTTPCredentialSecret, repos []string, heads map[string]bool, since time.Time) ([]workspacePull, error) {
	approvedAddresses, err := h.repo.PlatformGitHTTPSAddresses(ctx, cred.Host)
	if err != nil {
		return nil, fmt.Errorf("load Git HTTPS host authorization: %w", err)
	}

	var user struct {
		ID int64 `json:"id"`
	}
	if err := gitHostGet(ctx, client, cred, "/user", &user); err != nil {
		return nil, err
	}
	if user.ID <= 0 {
		return nil, errors.New("GitLab did not return the token's user")
	}
	ipv6Host := strings.HasPrefix(cred.Host, "[")
	out := []workspacePull{}
	for _, repo := range repos {
		var list []struct {
			IID          int        `json:"iid"`
			WebURL       string     `json:"web_url"`
			Title        string     `json:"title"`
			Description  string     `json:"description"`
			State        string     `json:"state"`
			MergedAt     *time.Time `json:"merged_at"`
			CreatedAt    time.Time  `json:"created_at"`
			SHA          string     `json:"sha"`
			TargetBranch string     `json:"target_branch"`
		}
		q := url.Values{"author_id": {fmt.Sprint(user.ID)}, "created_after": {since.UTC().Format(time.RFC3339)}, "state": {"all"}, "order_by": {"created_at"}, "sort": {"desc"}, "per_page": {fmt.Sprint(turnDeliveryPullsMax)}}
		if err := gitHostGet(ctx, client, cred, "/projects/"+url.PathEscape(repo)+"/merge_requests?"+q.Encode(), &list); err != nil {
			log.Printf("list merge requests of %s/%s: %v", cred.Host, repo, err)
			continue
		}
		for _, mr := range list {
			if mr.CreatedAt.Before(since) || !heads[mr.SHA] || mr.State == "closed" || mr.State == "locked" {
				continue
			}
			link := mr.WebURL
			matchesHost := sameDeliveryHost(link, cred.Host)
			if ipv6Host || (len(approvedAddresses) != 0 && !matchesHost) {
				// GitLab can advertise a different host or port in web_url.
				// Keep tracking on the endpoint and project that were queried.
				link = domain.GongfengMRURL(domain.MRProjectGitLab+cred.Host+"/"+repo, mr.IID)
				matchesHost = sameDeliveryHost(link, cred.Host)
			}
			if !matchesHost {
				log.Printf("skip merge request !%d from %s/%s: web link is invalid or uses a different endpoint", mr.IID, cred.Host, repo)
				continue
			}
			p := workspacePull{Number: mr.IID, URL: link, Title: mr.Title, Body: mr.Description, State: mr.State, MergedAt: mr.MergedAt, CreatedAt: mr.CreatedAt, mergeRequest: true}
			p.Head.SHA, p.Base.Ref = mr.SHA, mr.TargetBranch
			out = append(out, p)
		}
	}
	return out, nil
}

// sameDeliveryHost keeps a PR link the API returned only when it is a PR or
// MR link on the host that was asked.
func sameDeliveryHost(link, host string) bool {
	if _, _, _, ok := domain.ParseDeliveryMRURL(link); !ok {
		return false
	}
	u, err := url.Parse(link)
	if err != nil {
		return false
	}
	scope, err := gitwork.HTTPSHostScope("https://" + u.Host)
	return err == nil && scope == host
}

func gitHostGet(ctx context.Context, client *http.Client, cred domain.GitHTTPCredentialSecret, path string, out any) error {
	base := gitHostAPIBase(cred.GitHTTPCredential)
	if base == "" {
		return fmt.Errorf("%s has no API the platform can call", cred.Host)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set(cred.APIAuthHeader())
	if cred.Platform == domain.GitPlatformGitHub {
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	}
	res, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("%s request failed: %w", cred.Host, err)
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("%s returned HTTP %d", cred.Host, res.StatusCode)
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
