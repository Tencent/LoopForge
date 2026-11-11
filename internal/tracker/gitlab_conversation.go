package tracker

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"agentflow/internal/domain"
)

// GitLabBranchMR is an opened conversation delivery. Creating or updating it
// never accepts the MR or merges either branch.
type GitLabBranchMR struct {
	IID         int64
	URL         string
	Existing    bool
	Updated     bool
	UpdateError string
}

// CreateGitLabBranchMR uses the explicit requesting user's credentials. Unlike
// scheduled delivery it never resolves the repository owner's token.
func CreateGitLabBranchMR(ctx context.Context, base, token, project, source, target, head, title, description string) (GitLabBranchMR, error) {
	info, ok := domain.ParseGitLabRepositoryURL(project, base)
	if !ok || info.Project != project || strings.TrimSpace(token) == "" {
		return GitLabBranchMR{}, fmt.Errorf("GitLab repository does not match the requesting user's configured instance")
	}
	if strings.TrimSpace(source) == "" || strings.TrimSpace(target) == "" || source == target || strings.TrimSpace(head) == "" {
		return GitLabBranchMR{}, fmt.Errorf("GitLab delivery requires distinct source and target branches and a pushed commit")
	}
	base = strings.TrimRight(base, "/")
	path := strings.TrimPrefix(strings.TrimPrefix(project, "gitlab:"), base+"/")
	g := &gitLabClient{base: base, path: path, token: token, ctx: ctx, http: &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}}
	if err := g.resolveProject(); err != nil {
		return GitLabBranchMR{}, err
	}
	result := func(m gitLabMR, existing bool) GitLabBranchMR {
		return GitLabBranchMR{IID: int64(m.IID), URL: base + "/" + path + "/-/merge_requests/" + strconv.Itoa(m.IID), Existing: existing}
	}
	valid := func(m gitLabMR) bool { return g.matches(m, source, target) && m.State == "opened" && m.SHA == head }
	// Only opened requests are eligible. Historical merges do not describe the
	// current submission, and fork branch names are not repository identities.
	for page := 1; page <= 100; page++ {
		q := url.Values{"scope": {"all"}, "state": {"opened"}, "source_branch": {source}, "target_branch": {target}, "per_page": {"100"}, "page": {strconv.Itoa(page)}}
		var rows []gitLabMR
		if err := g.request("GET", "/merge_requests?"+q.Encode(), nil, &rows); err != nil {
			return GitLabBranchMR{}, err
		}
		for _, m := range rows {
			if !g.matches(m, source, target) || m.State != "opened" {
				continue
			}
			if !valid(m) {
				return GitLabBranchMR{}, fmt.Errorf("GitLab has not confirmed the pushed commit in the opened merge request")
			}
			out := result(m, true)
			var updated gitLabMR
			err := g.request("PUT", "/merge_requests/"+strconv.Itoa(m.IID), map[string]string{"title": title, "description": description}, &updated)
			if err != nil {
				out.UpdateError = err.Error()
			} else if !valid(updated) || updated.IID != m.IID {
				out.UpdateError = "GitLab did not confirm the updated merge request identity"
			} else {
				out.Updated = true
			}
			return out, nil
		}
		if len(rows) < 100 {
			break
		}
		if page == 100 {
			return GitLabBranchMR{}, fmt.Errorf("GitLab MR lookup exceeded 100 pages")
		}
	}
	var created gitLabMR
	if err := g.request("POST", "/merge_requests", map[string]string{"source_branch": source, "target_branch": target, "title": title, "description": description}, &created); err != nil {
		return GitLabBranchMR{}, err
	}
	if !valid(created) {
		return GitLabBranchMR{}, fmt.Errorf("GitLab did not confirm the created merge request identity and pushed commit")
	}
	return result(created, false), nil
}
