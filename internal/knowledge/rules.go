package knowledge

import (
	"regexp"
	"strings"

	"agentflow/internal/domain"
)

const usageRuleContent = `# 知识库使用规范

本角色可只读检索本项目知识库（MCP：` + domain.KnowledgeMCPServerID + `，只读）。

## 先查再做
- 动手前先用 kb_search 检索与任务相关的已有知识：排障手册、业务规范、历史方案、接口约定。
- 命中后用 kb_fetch 按 source + path + 行号读取原文，不要只凭摘要下结论。
- kb_list_sources 可查看当前项目可用的知识库来源；检索默认覆盖全部已授权来源。
- 当前使用关键词检索，可换关键词或更短的词再搜一次。

## 引用出处
- 采用知识库内容时，在产物中附上结果里的 citation（形如 source@版本:path#L10-L20）。
- 托管来源的版本是文档修订号，Git 来源的版本是 commit。
- 知识库没有覆盖的内容要明确说明"知识库未收录"，不要编造出处。

## 只读边界
- 知识库对你只读，不要尝试写入，也不要声称已经写回。
- 需要新增或修正内容时，在回复里说明，由用户到知识中心处理。
`

// UsageRule is the built-in rule injected with the knowledge MCP.
func UsageRule() domain.RuleDefinition {
	return domain.RuleDefinition{
		ID:          domain.KnowledgeUsageRuleID,
		Name:        "知识库使用规范",
		Description: "先只读检索本项目知识库再动手，引用附出处；知识库对 Agent 只读",
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
		Name:        "项目知识库",
		Scope:       "project",
		Description: "本项目知识库只读检索：kb_search / kb_fetch / kb_list_sources / kb_get_source",
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

// NormalizeDocPath 只接受仓库内的相对 .md 路径。含 .. 段、绝对路径、盘符或
// 反斜杠的越界路径直接拒绝，不做改写；安全的名称字符（空格等）仍会归一化。
func NormalizeDocPath(p string) (string, bool) {
	p = strings.TrimSpace(p)
	if p == "" || strings.ContainsAny(p, "\\\x00") || strings.HasPrefix(p, "/") {
		return "", false
	}
	if len(p) >= 2 && p[1] == ':' {
		return "", false
	}
	for _, r := range p {
		if r < 0x20 {
			return "", false
		}
	}
	if !strings.HasSuffix(strings.ToLower(p), ".md") {
		return "", false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return "", false
		}
	}
	p = unsafePathChars.ReplaceAllString(p, "-")
	return p, true
}

// NormalizePathPrefix 把来源的路径前缀统一成仓库内的相对目录。空串和单个
// "/" 都表示仓库根目录；含 ..、绝对路径、反斜杠或空路径段的输入直接拒绝。
func NormalizePathPrefix(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "/" {
		return "", true
	}
	if strings.HasPrefix(raw, "/") || strings.ContainsAny(raw, "\\\x00") {
		return "", false
	}
	if len(raw) >= 2 && raw[1] == ':' {
		return "", false
	}
	for _, r := range raw {
		if r < 0x20 {
			return "", false
		}
	}
	raw = strings.TrimSuffix(raw, "/")
	for _, seg := range strings.Split(raw, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return "", false
		}
	}
	return raw, true
}
