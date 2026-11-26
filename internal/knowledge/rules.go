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
- 查错误码、实例 ID、接口名、配置项或其他必须逐字命中的标识时用 kb_grep。默认按字面量匹配；只有模式本身是正则时才把 regex 设为 true。
- 命中后用 kb_fetch 按 source + path + 行号读取原文，不要只凭摘要或命中行下结论。
- kb_list_sources 可查看当前项目可用的知识库来源；检索和精确匹配默认覆盖全部已授权来源。
- 当前使用关键词检索，可换关键词或更短的词再搜一次。
- 发现文档过时、写错、链接失效或缺少负责人时，用 kb_report_issue 标记 source + path + 问题说明；它只记录问题，不修改正文。

## 引用出处
- 采用知识库内容时，在产物中附上结果里的 citation（形如 source@版本:path#L10-L20）。
- 工具结果里的 version 字段给出人类可读版本：托管来源是「修订 N」，Git 来源是「commit 短号」。提到版本时直接使用它；托管来源不要写成 commit。
- 知识库没有覆盖的内容要明确说明"知识库未收录"，不要编造出处。

## 只读边界与沉淀
- 知识库正文对你只读（可以检索、读取和标记问题），不要尝试写入，也不要声称已经写回。
- 运行到达闭环（LOOP 成功、合并请求合并、需求完成或人工关卡通过）后，平台会把本角色产出的 knowledge.md 自动加入审核队列；人工审核通过或知识库配置为门禁全过自动发布时，才由平台写回知识库。
- 需要新增或修正内容时，把可复用的结论写进 knowledge.md 产物；不要把它当作已发布知识引用。
`

// UsageRule is the built-in rule injected with the knowledge MCP.
func UsageRule() domain.RuleDefinition {
	return domain.RuleDefinition{
		ID:          domain.KnowledgeUsageRuleID,
		Name:        "知识库使用规范",
		Description: "先只读检索本项目知识库再动手，引用附出处；发现文档问题用 kb_report_issue 记录；运行产出的 knowledge.md 由平台在闭环后自动入审核队列",
		Content:     usageRuleContent,
		Priority:    50,
	}
}

// UsageRuleForProvider keeps the external rule limited to available tools.
func UsageRuleForProvider(provider string) domain.RuleDefinition {
	rule := UsageRule()
	if provider == domain.KnowledgeProviderLightCloud {
		rule.Description = "只读检索当前项目的 LightCloud 知识库并引用出处"
		rule.Content = `# 知识库使用规范

- 先用 kb_search 检索当前项目已授权的来源，命中后用 kb_fetch 读取原文。
- 可用 kb_list_sources 和 kb_get_source 查看授权来源。使用知识库内容时附上结果里的 citation。
- Agent 只读。需要新增或修正内容时，把可复用的结论写入 knowledge.md，由人工提交审核并写回。
`
	}
	return rule
}

// MCPServerDefinition describes the reserved read-only proxy for one role
// invocation. Authorization carries a project-scoped proxy token, never the
// upstream credential.
func MCPServerDefinition(baseURL, proxyToken string) domain.MCPServerDefinition {
	return domain.MCPServerDefinition{
		ID:          domain.KnowledgeMCPServerID,
		Name:        "项目知识库",
		Scope:       "project",
		Description: "本项目知识库检索与问题标记：kb_search / kb_fetch / kb_grep / kb_list_sources / kb_get_source / kb_report_issue；产出 knowledge.md 由平台自动沉淀",
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
	if header, body, ok := splitDistilledFrontMatter(text); ok {
		out.Body = body
		applyDistilledHeader(&out, header)
	}
	if out.Title == "" {
		out.Title = firstMarkdownHeading(out.Body)
	}
	return out
}

// splitDistilledFrontMatter separates a leading "---" front matter block from
// the body. ok is false when text has no closed front matter.
func splitDistilledFrontMatter(text string) (header, body string, ok bool) {
	if !strings.HasPrefix(text, "---\n") {
		return "", text, false
	}
	end := strings.Index(text[4:], "\n---")
	if end < 0 {
		return "", text, false
	}
	rest := text[4+end+4:]
	return text[4 : 4+end], strings.TrimLeft(strings.TrimPrefix(rest, "\n"), "\n"), true
}

// applyDistilledHeader copies the recognised title/source/path keys of a
// front matter header into out; other lines are ignored.
func applyDistilledHeader(out *Distilled, header string) {
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

// firstMarkdownHeading returns the text of the first "#" heading in body, or
// "" when there is none.
func firstMarkdownHeading(body string) string {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") {
			return strings.TrimSpace(strings.TrimLeft(line, "#"))
		}
	}
	return ""
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
