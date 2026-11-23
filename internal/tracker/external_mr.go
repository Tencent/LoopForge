package tracker

import (
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
	"agentflow/internal/gitegress"
)

// GitHTTPCredentialStore is implemented by a ProjectAuthStore that keeps
// users' per-host Git HTTP credentials.
type GitHTTPCredentialStore interface {
	GitHTTPCredentialForHost(ctx context.Context, userID int64, host string) (domain.GitHTTPCredentialSecret, bool)
}

// refreshExternalDelivery refreshes a GitHub Enterprise PR or GitLab MR with
// the Git HTTP credential of the user who registered it. Without one the row
// is left as is: ok is false and there is no error.
func (s *Service) refreshExternalDelivery(store MRDeliveryStore, row domain.MRDeliveryState) (ok bool, err error) {
	platform, host, path, valid := domain.ExternalMRProject(row.MRProject)
	if strings.HasPrefix(row.MRProject, "github:") {
		info, ok := domain.ParseRepositoryURL("https://github.com/" + strings.TrimPrefix(row.MRProject, "github:"))
		if ok && info.Project == row.MRProject {
			platform, host, path, valid = domain.GitPlatformGitHub, "github.com", strings.TrimPrefix(row.MRProject, "github:"), true
		}
	}
	if !valid || row.MRIID <= 0 {
		return false, fmt.Errorf("invalid MR project %q", row.MRProject)
	}
	creds, _ := s.projectAuth.(GitHTTPCredentialStore)
	if creds == nil || row.RegisteredBy <= 0 {
		return false, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cred, found := creds.GitHTTPCredentialForHost(ctx, row.RegisteredBy, host)
	if !found || cred.Token == "" || cred.Platform != platform {
		return false, nil
	}
	var state, target string
	var mergedAt time.Time
	if platform == domain.GitPlatformGitLab {
		var mr struct {
			State        string     `json:"state"`
			TargetBranch string     `json:"target_branch"`
			MergedAt     *time.Time `json:"merged_at"`
		}
		if err := s.gitHostGet(ctx, store.PlatformGitHTTPSAddresses, cred, "/projects/"+url.PathEscape(path)+"/merge_requests/"+strconv.Itoa(row.MRIID), &mr); err != nil {
			return false, err
		}
		if mr.State == "locked" {
			mr.State = "opened"
		}
		state, target = normalizeMRState(mr.State), mr.TargetBranch
		if mr.MergedAt != nil && state == "merged" {
			mergedAt = mr.MergedAt.UTC()
		}
	} else {
		var p githubPull
		if err := s.gitHostGet(ctx, store.PlatformGitHTTPSAddresses, cred, "/repos/"+path+"/pulls/"+strconv.Itoa(row.MRIID), &p); err != nil {
			return false, err
		}
		d := p.delivery(row.MRProject)
		state, target, mergedAt = d.GongfengState, d.TargetBranch, p.mergedAt()
	}
	if state == "" {
		return false, fmt.Errorf("%s returned no state for %s!%d", host, row.MRProject, row.MRIID)
	}
	return true, store.UpdateMRDeliveryState(row.MRProject, row.MRIID, state, target, mergedAt, time.Now().UTC())
}

func (s *Service) gitHostGet(ctx context.Context, lookup gitegress.HTTPSHostLookup, cred domain.GitHTTPCredentialSecret, path string, out any) error {
	base, client := cred.APIBase(), gitegress.Client(lookup)
	if cred.Host == "github.com" && s.githubAPIBase != "" {
		base, client = s.githubAPIBase, http.DefaultClient
	}
	if s.gitHostAPIBase != "" {
		base, client = s.gitHostAPIBase, http.DefaultClient
	}
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
		return fmt.Errorf("%s returned HTTP %d; check the registering user's Git access token for this host", cred.Host, res.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(out)
}
