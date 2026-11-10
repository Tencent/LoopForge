package knowledge

import (
	"encoding/json"

	"agentflow/internal/mcpclient"
)

// ReadOnlyToolDefinitions 是暴露给 Agent 的固定只读工具说明。写工具不在这里
// 列出，托管文档的写入只走知识中心，Agent 完全只读。SOP 代理与 Main Bot 目录
// 共用这一份定义，避免两处 JSON 契约漂移。
func ReadOnlyToolDefinitions() []mcpclient.Tool {
	return []mcpclient.Tool{
		{
			Name:        ToolSearch,
			Description: "在项目已授权的知识库来源里做关键词检索，返回带出处和行号的结果。",
			InputSchema: toolSchema(`{"type":"object","properties":{
				"query":{"type":"string","description":"要检索的问题或关键词"},
				"sources":{"type":"array","items":{"type":"string"},"description":"限定检索的来源 id；不传则由调用方按当前项目收窄"}
			},"required":["query"]}`),
		},
		{
			Name:        ToolFetch,
			Description: "按 source + path + 行号读取知识库 Markdown 原文。",
			InputSchema: toolSchema(`{"type":"object","properties":{
				"source":{"type":"string","description":"来源 id"},
				"path":{"type":"string","description":"来源内的相对 Markdown 路径"},
				"commit":{"type":"string","description":"可选版本，仅接受当前已索引版本"},
				"start_line":{"type":"integer","description":"起始行，one-based"},
				"end_line":{"type":"integer","description":"结束行，含"}
			},"required":["source","path"]}`),
		},
		{
			Name:        ToolListSources,
			Description: "列出当前项目可用的知识库来源及其状态、篇数。",
			InputSchema: toolSchema(`{"type":"object","properties":{}}`),
		},
		{
			Name:        ToolGetSource,
			Description: "读取单个来源的状态与文档清单。",
			InputSchema: toolSchema(`{"type":"object","properties":{
				"source":{"type":"string","description":"来源 id"}
			},"required":["source"]}`),
		},
	}
}

func toolSchema(s string) json.RawMessage { return json.RawMessage(s) }
