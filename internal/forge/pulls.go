package forge

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"agentflow/internal/domain"
)

type pullBranch struct {
	Name   string `json:"label"`
	Ref    string `json:"ref"`
	Commit string `json:"sha"`
	RepoID int64  `json:"repo_id"`
}

func (b pullBranch) name() string {
	// Forgejo may use refs/pull/N/head as ref after the branch was deleted.
	// label retains the branch name.
	if b.Name != "" {
		return b.Name
	}
	return strings.TrimPrefix(b.Ref, "refs/heads/")
}

type Pull struct {
	Number   int        `json:"number"`
	State    string     `json:"state"`
	Head     pullBranch `json:"head"`
	Base     pullBranch `json:"base"`
	Merged   bool       `json:"merged"`
	MergedAt time.Time  `json:"merged_at"`
}

func (p Pull) state() string {
	if p.Merged {
		return "merged"
	}
	switch p.State {
	case "open":
		return "opened"
	case "closed":
		return "closed"
	default:
		return "unknown"
	}
}

func (p Pull) Delivery(r domain.ForgeRepository) domain.DeliveryMR {
	state := p.state()
	return domain.DeliveryMR{Project: r.Project(), IID: p.Number, URL: r.PullURL(p.Number), State: state, GongfengState: state, TargetBranch: p.Base.name(), HeadSHA: p.Head.Commit}
}

func (c *RepositoryClient) sameRepository(p Pull) bool {
	return p.Number > 0 && p.Base.RepoID == c.id && p.Head.RepoID == c.id && p.Base.name() != "" && p.Head.name() != ""
}

func (c *RepositoryClient) matches(p Pull, source, target string) bool {
	return c.sameRepository(p) && p.Head.name() == source && (target == "" || p.Base.name() == target)
}

func (c *RepositoryClient) GetPull(ctx context.Context, number int) (Pull, error) {
	var p Pull
	if number <= 0 {
		return p, errors.New("无效的 Forgejo PR 编号")
	}
	if _, err := c.request(ctx, http.MethodGet, "/pulls/"+strconv.Itoa(number), nil, &p); err != nil {
		return p, err
	}
	if p.Number != number || !c.sameRepository(p) || p.state() == "unknown" {
		return p, errors.New("Forgejo 未确认 PR 的仓库、编号或状态")
	}
	return p, nil
}

// FindPull prefers an open pull for this repository and source branch, then
// the newest merged pull, then a closed one. Mutations pass openOnly to keep
// historical deliveries out of the selection.
func (c *RepositoryClient) FindPull(ctx context.Context, source, target string, openOnly bool) (Pull, bool, error) {
	if strings.TrimSpace(source) == "" {
		return Pull{}, false, nil
	}
	state := "all"
	if openOnly {
		state = "open"
	}
	var best Pull
	rank := func(p Pull) int {
		switch p.state() {
		case "opened":
			return 3
		case "merged":
			return 2
		case "closed":
			return 1
		default:
			return 0
		}
	}
	err := c.visitBranchPulls(ctx, source, target, state, func(p Pull) bool {
		if rank(p) > rank(best) || (rank(p) == rank(best) && p.Number > best.Number) {
			best = p
		}
		return best.state() != "opened"
	})
	if err != nil {
		return Pull{}, false, err
	}
	return best, best.Number > 0, nil
}

// BranchPulls lists matching PRs for verification, including previous merges
// of a reused work branch. The verifier compares their heads to the push.
func (c *RepositoryClient) BranchPulls(ctx context.Context, source, target string) ([]Pull, error) {
	var out []Pull
	err := c.visitBranchPulls(ctx, source, target, "all", func(p Pull) bool {
		out = append(out, p)
		return true
	})
	return out, err
}

func (c *RepositoryClient) visitBranchPulls(ctx context.Context, source, target, state string, visit func(Pull) bool) error {
	const limit = 30
	seen := 0
	for page := 1; page <= 50; page++ {
		q := url.Values{"state": {state}, "head": {source}, "limit": {strconv.Itoa(limit)}, "page": {strconv.Itoa(page)}}
		if target != "" {
			q.Set("base", target)
		}
		var pulls []Pull
		header, err := c.request(ctx, http.MethodGet, "/pulls?"+q.Encode(), nil, &pulls)
		if err != nil {
			return err
		}
		keepGoing := true
		for _, p := range pulls {
			if !c.matches(p, source, target) || p.state() == "unknown" || (state == "open" && p.state() != "opened") {
				continue
			}
			if !visit(p) {
				keepGoing = false
			}
		}
		if !keepGoing {
			return nil
		}
		// Only use Link as a pagination hint; construct the next URL locally.
		seen += len(pulls)
		total, _ := strconv.Atoi(header.Get("X-Total-Count"))
		next := strings.Contains(strings.Join(header.Values("Link"), ","), `rel="next"`)
		if len(pulls) == 0 || (!next && seen >= total && len(pulls) < limit) {
			return nil
		}
	}
	return errors.New("Forgejo PR 搜索超过 50 页")
}

type Submission struct {
	Pull        Pull
	Existing    bool
	Updated     bool
	UpdateError string
}

var gitObjectID = regexp.MustCompile(`^(?:[a-f0-9]{40}|[a-f0-9]{64})$`)

func (c *RepositoryClient) BranchHead(ctx context.Context, branch string) (string, error) {
	var result struct {
		Name   string `json:"name"`
		Commit struct {
			ID string `json:"id"`
		} `json:"commit"`
	}
	if branch == "" {
		return "", errors.New("Forgejo 需要分支名")
	}
	if _, err := c.request(ctx, http.MethodGet, "/branches/"+url.PathEscape(branch), nil, &result); err != nil {
		return "", err
	}
	if result.Name != branch || !gitObjectID.MatchString(result.Commit.ID) {
		return "", errors.New("Forgejo 未确认分支的提交")
	}
	return result.Commit.ID, nil
}

// BranchContains compares immutable commits. A source with no commits ahead
// of the target is already integrated, even if the target has advanced.
func (c *RepositoryClient) BranchContains(ctx context.Context, branch, head string) (bool, error) {
	if !gitObjectID.MatchString(head) {
		return false, errors.New("Forgejo 需要有效的源提交 SHA")
	}
	target, err := c.BranchHead(ctx, branch)
	if err != nil || target == head {
		return target == head, err
	}
	var comparison struct {
		TotalCommits *int `json:"total_commits"`
	}
	if _, err := c.request(ctx, http.MethodGet, "/compare/"+target+"..."+head+"?files=false&verification=false", nil, &comparison); err != nil {
		return false, err
	}
	if comparison.TotalCommits == nil || *comparison.TotalCommits < 0 {
		return false, errors.New("Forgejo 未确认源提交与目标分支的比较结果")
	}
	return *comparison.TotalCommits == 0, nil
}

// SubmitPull creates or reuses a same-repository PR whose source is exactly
// the pushed commit. A request from a fork with the same branch is ineligible.
func (c *RepositoryClient) SubmitPull(ctx context.Context, source, target, head, title, body string, update bool) (Submission, error) {
	if source == "" || target == "" || source == target || !gitObjectID.MatchString(head) {
		return Submission{}, errors.New("Forgejo PR 需要不同的源/目标分支和已推送的提交 SHA")
	}
	valid := func(p Pull) bool {
		return c.matches(p, source, target) && p.state() == "opened" && p.Head.Commit == head
	}
	useExisting := func(p Pull) (Submission, error) {
		if !valid(p) {
			return Submission{}, errors.New("已有的 Forgejo PR 未指向本次推送的提交")
		}
		out := Submission{Pull: p, Existing: true}
		if update {
			var edited Pull
			_, err := c.request(ctx, http.MethodPatch, "/pulls/"+strconv.Itoa(p.Number), map[string]string{"title": title, "body": body}, &edited)
			switch {
			case err != nil:
				out.UpdateError = err.Error()
			case edited.Number != p.Number || !valid(edited):
				out.UpdateError = "Forgejo 未确认更新后的 PR"
			default:
				out.Updated = true
			}
		}
		return out, nil
	}
	if p, found, err := c.FindPull(ctx, source, target, true); err != nil {
		return Submission{}, err
	} else if found {
		return useExisting(p)
	}
	var p Pull
	if _, err := c.request(ctx, http.MethodPost, "/pulls", map[string]string{"head": source, "base": target, "title": title, "body": body}, &p); err != nil {
		// A concurrent create may have won. Look up the exact open pull again
		// on a conflict, never treating a historical merge as this delivery.
		if err == statusError(http.StatusConflict) || err == statusError(http.StatusUnprocessableEntity) {
			if p, found, lookupErr := c.FindPull(ctx, source, target, true); lookupErr == nil && found {
				return useExisting(p)
			}
		}
		return Submission{}, err
	}
	if !valid(p) {
		return Submission{}, errors.New("Forgejo 未确认新建 PR 的仓库、分支和提交")
	}
	return Submission{Pull: p}, nil
}

// SubmitPushedPull confirms the remote work branch before creating or updating
// its PR, including retries where Git has already pushed the requested commit.
func (c *RepositoryClient) SubmitPushedPull(ctx context.Context, source, target, head, title, body string) (Submission, error) {
	pushed, err := c.BranchHead(ctx, source)
	if err != nil {
		return Submission{}, err
	}
	if pushed != head {
		return Submission{}, errors.New("Forgejo 上的源分支未指向本次提交，请确认推送结果")
	}
	return c.SubmitPull(ctx, source, target, head, title, body, true)
}

// MergePull uses the host's atomic head precondition and respects branch
// protection. It never opts into force merging or asynchronous merging.
func (c *RepositoryClient) MergePull(ctx context.Context, p Pull, source, target, head string) (Pull, error) {
	if !c.matches(p, source, target) || p.state() != "opened" || !gitObjectID.MatchString(head) || p.Head.Commit != head {
		return Pull{}, errors.New("Forgejo PR 与本次推送的提交不符，无法合并")
	}
	live, err := c.GetPull(ctx, p.Number)
	if err != nil {
		return Pull{}, err
	}
	if !c.matches(live, source, target) || live.state() != "opened" || live.Head.Commit != head {
		return Pull{}, errors.New("Forgejo PR 的目标分支或源提交已变更，请重新检查")
	}
	input := struct {
		Do               string `json:"do"`
		HeadCommitID     string `json:"head_commit_id"`
		DeleteAfterMerge bool   `json:"delete_branch_after_merge"`
	}{Do: "merge", HeadCommitID: head}
	if _, err := c.request(ctx, http.MethodPost, "/pulls/"+strconv.Itoa(p.Number)+"/merge", input, nil); err != nil {
		return Pull{}, err
	}
	merged, err := c.GetPull(ctx, p.Number)
	if err != nil {
		return Pull{}, err
	}
	if !merged.Merged || !c.matches(merged, source, target) {
		return Pull{}, fmt.Errorf("Forgejo 未确认 PR #%d 已合并", p.Number)
	}
	return merged, nil
}

// SubmitAndMergePull leaves an opened PR available for review when Forgejo
// refuses the merge. Include its link so the caller can resolve the blocker.
func (c *RepositoryClient) SubmitAndMergePull(ctx context.Context, source, target, head, title, body string) (Pull, error) {
	pr, err := c.SubmitPull(ctx, source, target, head, title, body, false)
	if err != nil {
		return Pull{}, err
	}
	merged, err := c.MergePull(ctx, pr.Pull, source, target, head)
	if err != nil {
		return Pull{}, fmt.Errorf("Forgejo PR %s 合并失败：%w", c.ref.PullURL(pr.Pull.Number), err)
	}
	return merged, nil
}
