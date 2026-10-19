package knowledge

import (
	"path"
	"regexp"
	"strings"

	"agentflow/internal/domain"
)

const usageRuleContent = `# 知识库使用规范

本角色已接入团队知识库（MCP：` + domain.KnowledgeMCPServerID + `，只读）。

## 先查再做
- 动手前先用 kb_search 检索与任务相关的已有知识：排障手册、业务规范、历史方案、接口约定。
- 命中后用 kb_fetch 按 source + path + 行号读取原文，不要只凭摘要下结论。
- kb_list_sources 可查看当前项目可用的知识库来源；检索默认覆盖全部已授权来源。
- 结果 degraded=true 表示语义检索降级为词法检索，可换关键词或更短的词再搜一次。

## 引用出处
- 采用知识库内容时，在产物中附上结果里的 citation（形如 source@commit:path#L10-L20）。
- 知识库没有覆盖的内容要明确说明"知识库未收录"，不要编造出处。

## 沉淀新知识
- 知识库对你只读，不要尝试写入。
- 本次任务中沉淀出的可复用知识（踩坑、排障步骤、团队约定），写到工作区根目录的 knowledge.md，
  平台会在人工审核后写回知识库。格式：

` + "```markdown" + `
---
title: 一句话标题
source: 建议写入的知识库来源 id（可选）
path: 建议路径，例如 guides/xxx.md（可选）
---
正文（Markdown）
` + "```" + `
`

// UsageRule is the built-in rule injected with the knowledge MCP.
func UsageRule() domain.RuleDefinition {
	return domain.RuleDefinition{
		ID:          domain.KnowledgeUsageRuleID,
		Name:        "知识库使用规范",
		Description: "先检索团队知识库再动手，引用附出处，新知识写入 knowledge.md 待审核沉淀",
		Content:     usageRuleContent,
		Priority:    50,
	}
}

// MCPServerDefinition describes the reserved read-only proxy for one role
// invocation. Authorization carries a project-scoped proxy token, never the
// upstream credential.
func MCPServerDefinition(baseURL, proxyToken string) domain.MCPServerDefinition {
	return domain.MCPServerDefinition{
		ID:          domain.KnowledgeMCPServerID,
		Name:        "团队知识库",
		Scope:       "project",
		Description: "团队知识库只读检索：kb_search / kb_fetch / kb_list_sources / kb_get_source",
		Transport:   "http",
		URL:         strings.TrimRight(baseURL, "/") + ProxyPath,
		Headers:     map[string]string{"Authorization": "Bearer " + proxyToken},
		TimeoutSec:  60,
	}
}

// ProxyPath lives under the /runners machine plane because gateways exempt
// only that prefix from interactive login, and remote execution machines must
// reach it without a browser session.
const ProxyPath = "/runners/knowledge/mcp"

// Distilled is the parsed form of a knowledge.md deliverable.
type Distilled struct {
	Title  string
	Source string
	Path   string
	Body   string
}

var frontMatterLine = regexp.MustCompile(`^([A-Za-z_]+)\s*:\s*(.*)$`)

// ParseDistilled reads the optional YAML-ish front matter (title/source/path)
// and falls back to the first Markdown heading for the title.
func ParseDistilled(content string) Distilled {
	text := strings.ReplaceAll(content, "\r\n", "\n")
	out := Distilled{Body: text}
	if strings.HasPrefix(text, "---\n") {
		if end := strings.Index(text[4:], "\n---"); end >= 0 {
			header := text[4 : 4+end]
			rest := text[4+end+4:]
			out.Body = strings.TrimLeft(strings.TrimPrefix(rest, "\n"), "\n")
			for _, line := range strings.Split(header, "\n") {
				m := frontMatterLine.FindStringSubmatch(strings.TrimSpace(line))
				if m == nil {
					continue
				}
				value := strings.Trim(strings.TrimSpace(m[2]), `"'`)
				switch strings.ToLower(m[1]) {
				case "title":
					out.Title = value
				case "source":
					out.Source = value
				case "path":
					out.Path = value
				}
			}
		}
	}
	if out.Title == "" {
		for _, line := range strings.Split(out.Body, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "#") {
				out.Title = strings.TrimSpace(strings.TrimLeft(line, "#"))
				break
			}
		}
	}
	return out
}

var unsafePathChars = regexp.MustCompile(`[^\p{L}\p{N}._/-]+`)

// NormalizeDocPath keeps upstream write paths repository-relative Markdown.
func NormalizeDocPath(p string) (string, bool) {
	p = strings.TrimSpace(strings.ReplaceAll(p, "\\", "/"))
	p = unsafePathChars.ReplaceAllString(p, "-")
	p = strings.TrimLeft(path.Clean("/"+p), "/")
	if p == "" || p == "." || strings.HasPrefix(p, "../") || !strings.HasSuffix(strings.ToLower(p), ".md") {
		return "", false
	}
	return p, true
}
