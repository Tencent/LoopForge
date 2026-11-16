//go:build !windows

package runtime

import (
	"context"

	"agentflow/internal/gitwork"
)

func gitSSHAgentFactory(context.Context, Sandbox) gitwork.SSHAgentFactory { return nil }
