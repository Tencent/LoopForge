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
			Name:        ToolGrep,
			Description: "在项目已授权来源的 Markdown 正文里做精确匹配。默认按字面量，正则需把 regex 设为 true。返回路径、行号、命中行和出处；需要上下文时再用 kb_fetch。",
			InputSchema: toolSchema(`{"type":"object","properties":{
				"pattern":{"type":"string","description":"要匹配的字面量；regex 为 true 时是正则"},
				"regex":{"type":"boolean","description":"为 true 时按正则匹配，默认按字面量"},
				"sources":{"type":"array","items":{"type":"string"},"description":"限定来源 id；不传则覆盖当前项目可见来源"}
			},"required":["pattern"]}`),
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

// AgentToolDefinitions 是 ReadOnlyToolDefinitions 加上“标记文档问题”。该工具
// 只写反馈，不提供正文写入，也不出现在知识库写回路径上。
func AgentToolDefinitions() []mcpclient.Tool {
	tools := append([]mcpclient.Tool{}, ReadOnlyToolDefinitions()...)
	return append(tools, mcpclient.Tool{
		Name:        ToolReportIssue,
		Description: "标记一篇知识文档的问题（内容过时、错误、链接失效或缺少负责人等）。只记录反馈到治理面板，不修改正文，也不写回知识库。",
		InputSchema: toolSchema(`{"type":"object","properties":{
			"source":{"type":"string","description":"来源 id"},
			"path":{"type":"string","description":"来源内的相对 Markdown 路径"},
			"issue":{"type":"string","description":"具体问题说明，例如哪一段已经过时或哪里写错了"}
		},"required":["source","path","issue"]}`),
		Annotations: json.RawMessage(`{"readOnlyHint":false,"destructiveHint":false,"idempotentHint":false}`),
	})
}

func toolSchema(s string) json.RawMessage { return json.RawMessage(s) }
