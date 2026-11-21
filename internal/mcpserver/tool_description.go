package mcpserver

import (
	"strings"

	"agentflow/internal/application"
)

// ToolDescription describes the argument envelopes advertised by ToolInputSchema.
// Both public and session MCP catalogs use it so HTTP parameter descriptions
// cannot be mistaken for top-level MCP arguments.
func ToolDescription(op application.Operation) string {
	properties := contractObject(ToolInputSchema(op)["properties"])
	var hints []string
	if properties["path"] != nil {
		hints = append(hints, `URL path parameters inside "path"`)
	}
	if properties["query"] != nil {
		hints = append(hints, `query parameters inside "query"`)
	}
	if properties["body"] != nil {
		hints = append(hints, `JSON request body inside "body"`)
	}
	return op.Description + " (" + op.Method + " " + op.Path + "). MCP arguments: " +
		strings.Join(hints, "; ") + ". Do not put endpoint fields at the top level."
}
