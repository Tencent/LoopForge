package tracker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"agentflow/internal/domain"
)

// codeupClient talks to the Aliyun Yunxiao Codeup central-station OpenAPI. It
// mirrors githubClient: the token is the project owner's per-host HTTPS Git
// credential for codeup.aliyun.com (the PAT doubles as the API token), and the
// repository is addressed by its URL-encoded full path orgId/group/repo.
type codeupClient struct {
	base, org, repoPath, token string
	http                       *http.Client
	repoID                     int // cached integer repository id resolved on first write/list
}

func (s *Service) codeupClient(project string) (*codeupClient, error) {
	org, repoPath, ok := domain.ParseCodeupProject(project)
	if !ok {
		return nil, fmt.Errorf("invalid Codeup repository")
	}
	var token string
	if s.projectAuth != nil {
		for _, repo := range s.projectAuth.Repositories() {
			if repo.Provider != domain.RepositoryProviderCodeup || repo.GongfengProjectID != project {
				continue
			}
			if p, found := s.projectAuth.Project(repo.ProjectID); found {
				token = s.codeupTokenForUser(p.OwnerID)
			}
			break
		}
	}
	base := s.codeupAPIBase
	if base == "" {
		base = domain.CodeupAPIBase
	}
	return &codeupClient{base: base, org: org, repoPath: repoPath, token: token, http: &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

// codeupClientForRun authenticates as the run did: its push token when set,
// else the Owner of the run's project when that project binds the repository.
func (s *Service) codeupClientForRun(project, localProjectID, gitToken string) (*codeupClient, string, error) {
	c, err := s.codeupClient(project)
	if err != nil {
		return nil, "", err
	}
	if token := strings.TrimSpace(gitToken); token != "" {
		c.token = token
		return c, "本轮责任人的 Git Token", nil
	}
	local := strings.TrimSpace(localProjectID)
	if s.projectAuth == nil || local == "" {
		return c, "首个绑定该仓库的项目 Owner 的 Git Token", nil
	}
	for _, repo := range s.projectAuth.Repositories() {
		if repo.Provider != domain.RepositoryProviderCodeup || repo.GongfengProjectID != project || repo.ProjectID != local {
			continue
		}
		c.token = ""
		if p, found := s.projectAuth.Project(local); found {
			c.token = s.codeupTokenForUser(p.OwnerID)
		}
		return c, "项目 " + local + " Owner 的 Git Token", nil
	}
	return c, "首个绑定该仓库的项目 Owner 的 Git Token", nil
}

func (s *Service) codeupTokenForUser(userID int64) string {
	if credentials, ok := s.projectAuth.(GitHTTPCredentialStore); ok {
		if credential, ok := credentials.GitHTTPCredentialForHost(context.Background(), userID, domain.CodeupRepoHost); ok {
			return credential.Token
		}
	}
	return ""
}

func (c *codeupClient) request(method, path string, input, output any) error {
	var body io.Reader
	if input != nil {
		b, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	r, err := http.NewRequest(method, c.base+path, body)
	if err != nil {
		return err
	}
	r.Header.Set("Accept", "application/json")
	r.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		r.Header.Set("x-yunxiao-token", c.token)
	}
	res, err := c.http.Do(r)
	if err != nil {
		return fmt.Errorf("Codeup request failed")
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("Codeup returned HTTP %d; check the project owner's Codeup token and repository permissions", res.StatusCode)
	}
	if output == nil {
		_, err = io.Copy(io.Discard, io.LimitReader(res.Body, 4<<20))
		return err
	}
	return json.NewDecoder(io.LimitReader(res.Body, 8<<20)).Decode(output)
}

func (c *codeupClient) repoPathEscape() string { return url.PathEscape(c.repoPath) }

// repositoryID resolves the integer repository id the create/list APIs require.
// The repositoryId path parameter accepts the URL-encoded full path, so no
// separate lookup table is needed; the result is cached for the client's life.
func (c *codeupClient) repositoryID() (int, error) {
	if c.repoID != 0 {
		return c.repoID, nil
	}
	var repo struct {
		ID int `json:"id"`
	}
	if err := c.request("GET", "/oapi/v1/codeup/organizations/"+c.org+"/repositories/"+c.repoPathEscape(), nil, &repo); err != nil {
		return 0, err
	}
	if repo.ID == 0 {
		return 0, fmt.Errorf("Codeup repository %s has no id", c.repoPath)
	}
	c.repoID = repo.ID
	return repo.ID, nil
}

// codeupChangeRequest is one merge request (变更) in a Codeup repository.
type codeupChangeRequest struct {
	LocalID         int    `json:"localId"`
	State           string `json:"state"`
	DetailURL       string `json:"detailUrl"`
	WebURL          string `json:"webUrl"`
	SourceBranch    string `json:"sourceBranch"`
	TargetBranch    string `json:"targetBranch"`
	MergedRevision  string `json:"mergedRevision"`
	ProjectID       int    `json:"projectId"`
	SourceProjectID int    `json:"sourceProjectId"`
	TargetProjectID int    `json:"targetProjectId"`
	Title           string `json:"title"`
}

// codeupMRState maps the Codeup change-request status onto the platform's
// opened/merged/closed delivery vocabulary.
func codeupMRState(state string) string {
	switch strings.ToUpper(strings.TrimSpace(state)) {
	case "UNDER_DEV", "UNDER_REVIEW", "TO_BE_MERGED":
		return "opened"
	case "MERGED":
		return "merged"
	case "CLOSED":
		return "closed"
	default:
		return ""
	}
}

// ref is the "!N" reference returned to delivery callers (localId is per-repo).
func (cr codeupChangeRequest) ref() string { return "!" + strconv.Itoa(cr.LocalID) }

func (cr codeupChangeRequest) webURL(project string) string {
	if u := strings.TrimSpace(cr.DetailURL); u != "" {
		return u
	}
	if u := strings.TrimSpace(cr.WebURL); u != "" {
		return u
	}
	return domain.GongfengMRURL(project, cr.LocalID)
}

// delivery converts a change request into a delivery MR. Codeup's list response
// exposes no source-branch head commit, so HeadSHA stays empty: an open MR then
// satisfies the coverage gate, and merged MRs are classified by state alone.
func (cr codeupChangeRequest) delivery(project string) domain.DeliveryMR {
	state := codeupMRState(cr.State)
	return domain.DeliveryMR{
		Project:       project,
		IID:           cr.LocalID,
		URL:           cr.webURL(project),
		State:         state,
		GongfengState: state,
		TargetBranch:  strings.TrimSpace(cr.TargetBranch),
	}
}

// listChangeRequests lists the repository's change requests, most recently
// updated first. The central-station list endpoint is organization-scoped and
// filters by projectIds/state only, so source/target branch matching is done by
// the caller. An empty state queries every status.
func (c *codeupClient) listChangeRequests(state string) ([]codeupChangeRequest, error) {
	id, err := c.repositoryID()
	if err != nil {
		return nil, err
	}
	var out []codeupChangeRequest
	for page := 1; page <= 20; page++ {
		q := url.Values{"projectIds": {strconv.Itoa(id)}, "perPage": {"100"}, "page": {strconv.Itoa(page)}, "orderBy": {"updated_at"}, "sort": {"desc"}}
		if state != "" {
			q.Set("state", state)
		}
		var list []codeupChangeRequest
		if err := c.request("GET", "/oapi/v1/codeup/organizations/"+c.org+"/changeRequests?"+q.Encode(), nil, &list); err != nil {
			return nil, err
		}
		out = append(out, list...)
		if len(list) < 100 {
			break
		}
	}
	return out, nil
}

func (c *codeupClient) createChangeRequest(sourceBranch, targetBranch, title, description string) (codeupChangeRequest, error) {
	id, err := c.repositoryID()
	if err != nil {
		return codeupChangeRequest{}, err
	}
	body := map[string]any{
		"sourceBranch":    sourceBranch,
		"targetBranch":    targetBranch,
		"title":           title,
		"sourceProjectId": id,
		"targetProjectId": id,
	}
	if strings.TrimSpace(description) != "" {
		body["description"] = description
	}
	var cr codeupChangeRequest
	if err := c.request("POST", "/oapi/v1/codeup/organizations/"+c.org+"/repositories/"+c.repoPathEscape()+"/changeRequests", body, &cr); err != nil {
		return codeupChangeRequest{}, err
	}
	return cr, nil
}

// mergeChangeRequest merges with a merge commit (no-fast-forward) and keeps the
// source branch, matching the Gongfeng/GitHub delivery convention.
func (c *codeupClient) mergeChangeRequest(localID int, message string) error {
	body := map[string]any{
		"mergeType":          "no-fast-forward",
		"removeSourceBranch": false,
	}
	if strings.TrimSpace(message) != "" {
		body["mergeMessage"] = message
	}
	return c.request("POST", "/oapi/v1/codeup/organizations/"+c.org+"/repositories/"+c.repoPathEscape()+"/changeRequests/"+strconv.Itoa(localID)+"/merge", body, nil)
}

func findCodeupChangeRequest(items []codeupChangeRequest, branch, target string) (codeupChangeRequest, bool) {
	for _, cr := range items {
		if domain.SameGitBranch(cr.SourceBranch, branch) && (target == "" || domain.SameGitBranch(cr.TargetBranch, target)) {
			return cr, true
		}
	}
	return codeupChangeRequest{}, false
}

// pickCodeupBranchMR returns the newest non-closed change request whose source
// is branch, else the newest closed one (mirrors pickBranchMR).
func pickCodeupBranchMR(items []codeupChangeRequest, sourceBranch string) (codeupChangeRequest, bool) {
	var best codeupChangeRequest
	found, bestClosed := false, false
	for _, item := range items {
		if s := strings.TrimSpace(item.SourceBranch); s != "" && !domain.SameGitBranch(s, sourceBranch) {
			continue
		}
		closed := codeupMRState(item.State) == "closed"
		switch {
		case !found:
		case bestClosed && !closed:
		case bestClosed == closed && item.LocalID > best.LocalID:
		default:
			continue
		}
		best, found, bestClosed = item, true, closed
	}
	return best, found
}

func (s *Service) codeupFindBranch(project, branch string) (domain.DeliveryMR, bool, error) {
	c, err := s.codeupClient(project)
	if err != nil {
		return domain.DeliveryMR{}, false, err
	}
	list, err := c.listChangeRequests("")
	if err != nil {
		return domain.DeliveryMR{}, false, err
	}
	cr, ok := pickCodeupBranchMR(list, branch)
	if !ok {
		return domain.DeliveryMR{}, false, nil
	}
	return cr.delivery(project), true, nil
}

func (s *Service) codeupMergeBranch(project, branch, target, title, description string) (string, error) {
	if branch == "" {
		return "", fmt.Errorf("source branch is required")
	}
	if branch == target {
		return "", nil
	}
	c, err := s.codeupClient(project)
	if err != nil {
		return "", err
	}
	list, err := c.listChangeRequests("")
	if err != nil {
		return "", err
	}
	cr, found := findCodeupChangeRequest(list, branch, target)
	if found && codeupMRState(cr.State) == "merged" {
		return cr.ref(), nil
	}
	if !found || codeupMRState(cr.State) == "closed" {
		if target == "" {
			return "", ErrNoOpenMR
		}
		if strings.TrimSpace(title) == "" {
			title = "LoopForge: " + branch + " → " + target
		}
		cr, err = c.createChangeRequest(branch, target, title, description)
		if err != nil {
			return "", err
		}
	}
	if err := c.mergeChangeRequest(cr.LocalID, title); err != nil {
		return "", fmt.Errorf("merge MR !%d: %w", cr.LocalID, err)
	}
	return cr.ref(), nil
}

func (s *Service) codeupFindBranchMRsInto(q domain.BranchMRQuery) ([]domain.DeliveryMR, error) {
	project := strings.TrimSpace(q.RemoteProject)
	src, target := strings.TrimSpace(q.SourceBranch), strings.TrimSpace(q.TargetBranch)
	c, identity, err := s.codeupClientForRun(project, q.ProjectID, q.GitToken)
	if err != nil {
		return nil, err
	}
	list, err := c.listChangeRequests("")
	if err != nil {
		return nil, fmt.Errorf("以%s查询云效失败：%w", identity, err)
	}
	var out []domain.DeliveryMR
	for _, cr := range list {
		if !domain.SameGitBranch(cr.SourceBranch, src) || !domain.SameGitBranch(cr.TargetBranch, target) {
			continue
		}
		out = append(out, cr.delivery(project))
	}
	return out, nil
}

// refreshCodeupDelivery re-reads one change request's state for the background
// delivery sync. Codeup's list response carries no merge timestamp, so mergedAt
// stays zero; the classified state and target branch are still refreshed.
func (s *Service) refreshCodeupDelivery(store MRDeliveryStore, project string, localID int) error {
	c, err := s.codeupClient(project)
	if err != nil {
		return err
	}
	list, err := c.listChangeRequests("")
	if err != nil {
		return err
	}
	for _, cr := range list {
		if cr.LocalID == localID {
			d := cr.delivery(project)
			return store.UpdateMRDeliveryState(project, localID, d.GongfengState, d.TargetBranch, time.Time{}, time.Now().UTC())
		}
	}
	return fmt.Errorf("Codeup change request %s!%d not found", project, localID)
}