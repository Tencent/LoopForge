package engine

import (
	"agentflow/internal/knowledge"
	"agentflow/internal/mcpclient"
)

// readOnlyTools 复用 knowledge 包的固定只读工具说明；写工具不在其中。
func readOnlyTools() []mcpclient.Tool {
	return knowledge.ReadOnlyToolDefinitions()
}
