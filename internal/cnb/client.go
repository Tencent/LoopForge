// Package cnb is a thin client for the CNB (cnb.cool) OpenAPI. Field names
// follow https://api.cnb.cool/swagger.json: pull request numbers are JSON
// strings and branch refs carry the refs/heads/ prefix.
package cnb

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	defaultBaseURL = "https://api.cnb.cool"
	maxBody        = 8 << 20
	pageSize       = 100
	// maxFindPages bounds the client-side branch search: CNB cannot filter
	// pull requests by source branch.
	maxFindPages = 5
)

// Client talks to one CNB API with one user's access token. The token is only
// ever sent to BaseURL.
type Client struct {
	Token   string
	BaseURL string
	HTTP    *http.Client
}

// New returns a client for api.cnb.cool that never follows redirects, so the
// token cannot be handed to another host.
func New(token string) *Client {
	return &Client{
		Token:   strings.TrimSpace(token),
		BaseURL: defaultBaseURL,
		HTTP: &http.Client{
			Timeout:       30 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

// APIError is a non-2xx answer from CNB.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("CNB returned HTTP %d", e.Status)
	}
	return fmt.Sprintf("CNB returned HTTP %d: %s", e.Status, e.Message)
}

// Pull is a CNB pull request. State is "open", "closed" or "merged".
type Pull struct {
	Number       int
	State        string
	Title        string
	Body         string
	TargetBranch string
	SourceBranch string
	SourceRepo   string
	HeadSHA      string
	// ClosedAt is CNB's close or merge time; zero while the pull is open.
	ClosedAt time.Time
}

type pullRef struct {
	Ref  string `json:"ref"`
	SHA  string `json:"sha"`
	Repo struct {
		Path string `json:"path"`
	} `json:"repo"`
}

type pullJSON struct {
	Number   string  `json:"number"`
	State    string  `json:"state"`
	IsMerged bool    `json:"is_merged"`
	Title    string  `json:"title"`
	Body     string  `json:"body"`
	ClosedAt string  `json:"closed_at"`
	Base     pullRef `json:"base"`
	Head     pullRef `json:"head"`
}

func (p pullJSON) pull() (Pull, error) {
	number, err := strconv.Atoi(p.Number)
	if err != nil || number <= 0 {
		return Pull{}, fmt.Errorf("CNB returned an invalid pull request number %q", p.Number)
	}
	out := Pull{
		Number: number, State: p.State, Title: p.Title, Body: p.Body,
		TargetBranch: strings.TrimPrefix(p.Base.Ref, "refs/heads/"),
		SourceBranch: strings.TrimPrefix(p.Head.Ref, "refs/heads/"),
		SourceRepo:   p.Head.Repo.Path, HeadSHA: p.Head.SHA,
	}
	if p.IsMerged {
		out.State = "merged"
	}
	if t, err := time.Parse(time.RFC3339, p.ClosedAt); err == nil {
		out.ClosedAt = t.UTC()
	}
	return out, nil
}

func (c *Client) request(ctx context.Context, method, path string, query url.Values, input, output any) error {
	var body io.Reader
	if input != nil {
		b, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	target := strings.TrimRight(c.BaseURL, "/") + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/vnd.cnb.api+json")
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return errors.New("CNB request failed")
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, maxBody))
	if err != nil {
		return errors.New("CNB response could not be read")
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		var failure struct {
			ErrMsg string `json:"errmsg"`
		}
		_ = json.Unmarshal(data, &failure)
		return &APIError{Status: res.StatusCode, Message: strings.ReplaceAll(failure.ErrMsg, c.Token, "***")}
	}
	if output == nil {
		return nil
	}
	if err := json.Unmarshal(data, output); err != nil {
		return errors.New("CNB returned an invalid response")
	}
	return nil
}

// repoPath is the /{slug}/-/ prefix of a repository's API. The slug has two
// or more segments.
func repoPath(slug string) string {
	segments := strings.Split(strings.Trim(slug, "/"), "/")
	for i, segment := range segments {
		segments[i] = url.PathEscape(segment)
	}
	return "/" + strings.Join(segments, "/") + "/-"
}

// VerifyToken asks who the token belongs to. A token limited to repo-code and
// repo-pr cannot read /user, so 403 still means the token is valid; login is
// then empty. 401 is the only answer that marks it invalid.
func (c *Client) VerifyToken(ctx context.Context) (login string, err error) {
	var user struct {
		Username string `json:"username"`
	}
	err = c.request(ctx, http.MethodGet, "/user", nil, nil, &user)
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.Status == http.StatusForbidden {
		return "", nil
	}
	return user.Username, err
}

// DefaultBranch is the repository's default branch.
func (c *Client) DefaultBranch(ctx context.Context, slug string) (string, error) {
	var head struct {
		Name string `json:"name"`
	}
	if err := c.request(ctx, http.MethodGet, repoPath(slug)+"/git/head", nil, nil, &head); err != nil {
		return "", err
	}
	if head.Name == "" {
		return "", errors.New("CNB returned no default branch")
	}
	return head.Name, nil
}

// FindPull returns the newest pull request of the repository whose source is
// the branch source in the same repository, into target when target is set,
// in any state. A fork's branch of the same name never matches.
func (c *Client) FindPull(ctx context.Context, slug, source, target string) (Pull, bool, error) {
	for page := 1; page <= maxFindPages; page++ {
		query := url.Values{"state": {"all"}, "order_by": {"-updated_at"}, "page": {strconv.Itoa(page)}, "page_size": {strconv.Itoa(pageSize)}}
		if target != "" {
			query.Set("base_ref", target)
		}
		var list []pullJSON
		if err := c.request(ctx, http.MethodGet, repoPath(slug)+"/pulls", query, nil, &list); err != nil {
			return Pull{}, false, err
		}
		for _, item := range list {
			if strings.TrimPrefix(item.Head.Ref, "refs/heads/") != source || item.Head.Repo.Path != slug {
				continue
			}
			p, err := item.pull()
			if err != nil {
				return Pull{}, false, err
			}
			if target == "" || p.TargetBranch == target {
				return p, true, nil
			}
		}
		if len(list) == 0 {
			break
		}
	}
	return Pull{}, false, nil
}

func (c *Client) CreatePull(ctx context.Context, slug, source, target, title, body string) (Pull, error) {
	var created pullJSON
	if err := c.request(ctx, http.MethodPost, repoPath(slug)+"/pulls", nil, map[string]string{"head": source, "base": target, "title": title, "body": body}, &created); err != nil {
		return Pull{}, err
	}
	return created.pull()
}

func (c *Client) UpdatePull(ctx context.Context, slug string, number int, title, body string) (Pull, error) {
	var updated pullJSON
	if err := c.request(ctx, http.MethodPatch, repoPath(slug)+"/pulls/"+strconv.Itoa(number), nil, map[string]string{"title": title, "body": body}, &updated); err != nil {
		return Pull{}, err
	}
	return updated.pull()
}

func (c *Client) GetPull(ctx context.Context, slug string, number int) (Pull, error) {
	var p pullJSON
	if err := c.request(ctx, http.MethodGet, repoPath(slug)+"/pulls/"+strconv.Itoa(number), nil, nil, &p); err != nil {
		return Pull{}, err
	}
	return p.pull()
}

// MergePull merges with a merge commit. It fails unless CNB answers merged.
// The live CNB API rejects an empty commit_title even though swagger omits it.
func (c *Client) MergePull(ctx context.Context, slug string, number int) error {
	var result struct {
		Merged  bool   `json:"merged"`
		Message string `json:"message"`
	}
	if err := c.request(ctx, http.MethodPut, repoPath(slug)+"/pulls/"+strconv.Itoa(number)+"/merge", nil, map[string]string{"merge_style": "merge", "commit_title": fmt.Sprintf("Merge pull request #%d", number)}, &result); err != nil {
		return err
	}
	if !result.Merged {
		return fmt.Errorf("CNB did not merge pull request #%d: %s", number, strings.ReplaceAll(result.Message, c.Token, "***"))
	}
	return nil
}
