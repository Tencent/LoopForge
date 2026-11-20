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
	profile, ok := r.Sandbox(id)
	if !ok || !(profile.Shared || projectID != "" && profile.ProjectID == projectID) {
		if projectID == "" {
			return domain.SandboxProfile{}, fmt.Errorf("请选择共享的沙箱配置")
		}
		return domain.SandboxProfile{}, fmt.Errorf("请选择本项目或共享的沙箱配置")
	}
	switch profile.Driver {
	case domain.SandboxDriverDocker:
		if profile.DockerfileID == "" {
			return domain.SandboxProfile{}, fmt.Errorf("Docker 沙箱需要配置 Dockerfile")
		}
		if _, ok := r.Dockerfile(profile.DockerfileID); !ok {
			return domain.SandboxProfile{}, fmt.Errorf("沙箱的 Dockerfile 不存在，请联系项目 Owner 更新运行环境")
		}
	case domain.SandboxDriverNative, domain.SandboxDriverSeatbelt, domain.SandboxDriverBwrap:
	default:
		return domain.SandboxProfile{}, fmt.Errorf("对话工作区只支持 Docker 或 native 沙箱")
	}
	return profile, nil
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
		if _, err := r.ConversationSandbox(projectID, profile.ID); err == nil {
			out = append(out, profile)
		}
	}
	return out
}
