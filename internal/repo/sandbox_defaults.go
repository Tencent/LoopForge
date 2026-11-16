package repo

import "agentflow/internal/domain"

const DefaultSandboxProfileID = "local"

// DefaultSandboxProfile is available without catalog initialization. It is a
// read-only fallback, not a seed row. Operator-persisted profiles take priority.
// The runtime applies AGENTFLOW_LOCAL_SANDBOX=docker in Compose deployments;
// native installations retain their explicitly configured execution policy.
func DefaultSandboxProfile() domain.SandboxProfile {
	return domain.SandboxProfile{
		ID:     DefaultSandboxProfileID,
		Name:   "默认运行沙箱",
		Shared: true,
		Driver: domain.SandboxDriverLocal,
	}
}
