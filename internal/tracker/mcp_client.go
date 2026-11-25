package tracker

import "agentflow/internal/mcpclient"

// MCPClient is a Streamable HTTP MCP client used by issue/MR sync.
type MCPClient = mcpclient.Client

func NewMCPClient(url string, headers map[string]string) *MCPClient {
	return mcpclient.New(url, headers)
}
