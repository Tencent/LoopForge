package engine

import (
	"agentflow/internal/knowledge"
	"agentflow/internal/mcpclient"
)

// readOnlyTools 返回 Agent 可用的知识库工具（只读检索 + 标记问题）；
// 正文写工具不在其中。
func readOnlyTools() []mcpclient.Tool {
	return knowledge.AgentToolDefinitions()
}
