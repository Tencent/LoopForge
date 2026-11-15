package tracker

import (
	"context"
	"fmt"
	"strings"
	"time"

	"agentflow/internal/cnb"
)

// cnbHost serves a "cnb:<slug>" project with the project owner's CNB token.
type cnbHost struct {
	client *cnb.Client
	slug   string
}

const cnbCallTimeout = 30 * time.Second

func (s *Service) cnbHost(project string) (*cnbHost, error) {
	var token string
	if s.projectAuth != nil {
		for _, repo := range s.projectAuth.Repositories() {
			if repo.Provider != "cnb" || repo.GongfengProjectID != project {
				continue
			}
			if p, found := s.projectAuth.Project(repo.ProjectID); found {
				if u, exists := s.projectAuth.User(p.OwnerID); exists {
					token = u.CNBToken
				}
			}
			break
		}
	}
	if token == "" {
		return nil, fmt.Errorf("项目 Owner 未配置 CNB 访问令牌，无法操作 %s 的合并请求", project)
	}
	client := cnb.New(token)
	if s.cnbAPIBase != "" {
		client.BaseURL = s.cnbAPIBase
	}
	return &cnbHost{client: client, slug: strings.TrimPrefix(project, "cnb:")}, nil
}

func hostedFromCNB(pull cnb.Pull) hostedPull {
	h := hostedPull{Number: pull.Number, State: "opened", TargetBranch: pull.TargetBranch, HeadSHA: pull.HeadSHA}
	switch pull.State {
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
