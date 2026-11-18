package tracker

import (
	"context"
	"fmt"
	"strings"
	"time"

	"agentflow/internal/cnb"
	"agentflow/internal/domain"
)

// cnbHost serves a "cnb:<slug>" project with one user's CNB token.
type cnbHost struct {
	client *cnb.Client
	slug   string
}

var cnbCallTimeout = 30 * time.Second

func validateCNBProject(project string) (string, error) {
	slug := strings.TrimPrefix(project, "cnb:")
	info, ok := domain.ParseRepositoryURL("https://cnb.cool/" + slug)
	if !ok || info.Project != project {
		return "", fmt.Errorf("invalid CNB repository")
	}
	return slug, nil
}

func (s *Service) newCNBHost(slug, token string) *cnbHost {
	client := cnb.New(token)
	if s.cnbAPIBase != "" {
		client.BaseURL = s.cnbAPIBase
	}
	return &cnbHost{client: client, slug: slug}
}

func (s *Service) cnbHost(project string) (*cnbHost, error) {
	slug, err := validateCNBProject(project)
	if err != nil {
		return nil, err
	}
	var token string
	if s.projectAuth != nil {
		for _, repo := range s.projectAuth.Repositories() {
			if repo.Provider != domain.RepositoryProviderCNB || strings.TrimSpace(repo.GongfengProjectID) != project {
				continue
			}
			if p, found := s.projectAuth.Project(repo.ProjectID); found {
				if u, exists := s.projectAuth.User(p.OwnerID); exists {
					token = strings.TrimSpace(u.CNBToken)
				}
			}
			break
		}
	}
	if token == "" {
		return nil, fmt.Errorf("项目 Owner 未配置 CNB 访问令牌，无法操作 %s 的合并请求", project)
	}
	return s.newCNBHost(slug, token), nil
}

// cnbHostForRun selects the CNB token for a run's MR gate query: first the
// run's responsible user, then the Owner of q.ProjectID when that project binds
// project (never crossing to another project's Owner), and only when no bound
// project is specified does it fall back to the first bound project's Owner.
func (s *Service) cnbHostForRun(project string, q domain.BranchMRQuery) (*cnbHost, string, error) {
	slug, err := validateCNBProject(project)
	if err != nil {
		return nil, "", err
	}
	if q.ResponsibleUserID > 0 && s.projectAuth != nil {
		if u, ok := s.projectAuth.User(q.ResponsibleUserID); ok && strings.TrimSpace(u.CNBToken) != "" {
			return s.newCNBHost(slug, u.CNBToken), "本轮责任人的 CNB 访问令牌", nil
		}
	}
	if local := strings.TrimSpace(q.ProjectID); local != "" && s.projectAuth != nil {
		for _, repo := range s.projectAuth.Repositories() {
			if repo.Provider != domain.RepositoryProviderCNB || strings.TrimSpace(repo.GongfengProjectID) != project || repo.ProjectID != local {
				continue
			}
			if p, found := s.projectAuth.Project(local); found {
				if u, exists := s.projectAuth.User(p.OwnerID); exists && strings.TrimSpace(u.CNBToken) != "" {
					return s.newCNBHost(slug, u.CNBToken), "项目 " + local + " Owner 的 CNB 访问令牌", nil
				}
			}
			return nil, "", fmt.Errorf("本轮责任人及项目 %s Owner 均未配置 CNB 访问令牌，无法查询 %s 的合并请求", local, project)
		}
	}
	h, err := s.cnbHost(project)
	if err != nil {
		return nil, "", err
	}
	return h, "首个绑定该仓库的项目 Owner 的 CNB 访问令牌", nil
}

func hostedFromCNB(pull cnb.Pull) hostedPull {
	h := hostedPull{Number: pull.Number, State: "unknown", TargetBranch: pull.TargetBranch, HeadSHA: pull.HeadSHA}
	switch strings.ToLower(strings.TrimSpace(pull.State)) {
	case "open", "opened":
		h.State = "opened"
	case "merged":
		h.State, h.MergedAt = "merged", pull.ClosedAt
		if h.MergedAt.IsZero() {
			// Without a merge time the delivery row would never reach its final state.
			h.MergedAt = time.Now().UTC()
		}
	case "closed":
		h.State = "closed"
	}
	return h
}

func (h *cnbHost) listPullsInto(project, branch, target string) ([]domain.DeliveryMR, error) {
	ctx, cancel := context.WithTimeout(context.Background(), cnbCallTimeout)
	defer cancel()
	pulls, err := h.client.ListPulls(ctx, h.slug, branch, target)
	if err != nil {
		return nil, err
	}
	var out []domain.DeliveryMR
	for _, p := range pulls {
		out = append(out, hostedFromCNB(p).delivery(project))
	}
	return out, nil
}

func (h *cnbHost) findPull(branch, target string) (hostedPull, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), cnbCallTimeout)
	defer cancel()
	p, ok, err := h.client.FindPull(ctx, h.slug, branch, target)
	if err != nil || !ok {
		return hostedPull{}, ok, err
	}
	return hostedFromCNB(p), true, nil
}

func (h *cnbHost) createPull(branch, target, title, body string) (hostedPull, error) {
	ctx, cancel := context.WithTimeout(context.Background(), cnbCallTimeout)
	defer cancel()
	p, err := h.client.CreatePull(ctx, h.slug, branch, target, title, body)
	if err != nil {
		return hostedPull{}, err
	}
	return hostedFromCNB(p), nil
}

func (h *cnbHost) mergePull(p hostedPull) error {
	ctx, cancel := context.WithTimeout(context.Background(), cnbCallTimeout)
	defer cancel()
	return h.client.MergePull(ctx, h.slug, p.Number)
}

func (h *cnbHost) pull(number int) (hostedPull, error) {
	ctx, cancel := context.WithTimeout(context.Background(), cnbCallTimeout)
	defer cancel()
	p, err := h.client.GetPull(ctx, h.slug, number)
	if err != nil {
		return hostedPull{}, err
	}
	return hostedFromCNB(p), nil
}
