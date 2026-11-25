package repo

import (
	"fmt"
	"strings"

	"agentflow/internal/domain"
)

// ConversationSandbox validates an explicit choice without silently substituting
// a different toolchain or exposing another project's environment. A
// conversation workspace runs in a Docker sandbox with a Dockerfile, or in the
// execution machine's native lightweight sandbox.
func (r *CatalogRepo) ConversationSandbox(projectID, id string) (domain.SandboxProfile, error) {
	id = strings.TrimSpace(id)
	profile, _ := r.Sandbox(id)
	if err := validateConversationSandbox(projectID, profile); err != nil {
		return domain.SandboxProfile{}, err
	}
	return profile, nil
}

func validateConversationSandbox(projectID string, profile domain.SandboxProfile) error {
	if profile.ID == "" || !(profile.Shared || projectID != "" && profile.ProjectID == projectID) {
		if projectID == "" {
			return fmt.Errorf("请选择共享的沙箱配置")
		}
		return fmt.Errorf("请选择本项目或共享的沙箱配置")
	}
	switch profile.Driver {
	case domain.SandboxDriverDocker:
		if profile.Dockerfile == nil || strings.TrimSpace(profile.Dockerfile.Content) == "" {
			return fmt.Errorf("Docker 沙箱 %s 没有 Dockerfile，请联系项目 Owner 更新沙箱配置", profile.ID)
		}
	case domain.SandboxDriverNative, domain.SandboxDriverSeatbelt, domain.SandboxDriverBwrap:
	default:
		return fmt.Errorf("对话工作区只支持 Docker 或 native 沙箱")
	}
	return nil
}

// ResolveConversationSandbox validates the chosen sandbox, or the platform
// generic Docker sandbox when nothing was chosen. An invalid choice is
// reported instead of being replaced.
func (r *CatalogRepo) ResolveConversationSandbox(projectID, chosen string) (domain.SandboxProfile, error) {
	id := strings.TrimSpace(chosen)
	if id == "" {
		id = GenericDockerSandboxID
	}
	return r.ConversationSandbox(projectID, id)
}

// ConversationSandboxes lists every sandbox a conversation of projectID may
// use.
func (r *CatalogRepo) ConversationSandboxes(projectID string) []domain.SandboxProfile {
	out := []domain.SandboxProfile{}
	for _, profile := range r.Sandboxes() {
		if validateConversationSandbox(projectID, profile) == nil {
			out = append(out, profile)
		}
	}
	return out
}
