package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

// rankingQuerier 是重排测试用的内容面：FulltextChunks 返回自然语言模式的
// 候选顺序，BooleanChunks 返回标识符精确通道的候选。
type rankingQuerier struct {
	nl        []domain.KnowledgeChunkMatch
	boolHits  []domain.KnowledgeChunkMatch
	nlErr     error
	boolErr   error
	nlLimit   int
	boolLimit int
	boolQuery string
	boolCalls int
}

func (q *rankingQuerier) FulltextChunks(_ context.Context, _ string, _ []string, limit int) ([]domain.KnowledgeChunkMatch, error) {
	q.nlLimit = limit
	return q.nl, q.nlErr
}

func (q *rankingQuerier) BooleanChunks(_ context.Context, query string, _ []string, limit int) ([]domain.KnowledgeChunkMatch, error) {
	q.boolCalls++
	q.boolQuery = query
	q.boolLimit = limit
	return q.boolHits, q.boolErr
}

func chunkMatch(path, title, heading, text string) domain.KnowledgeChunkMatch {
	return domain.KnowledgeChunkMatch{Chunk: domain.KnowledgeChunk{
		SourceID: "s", Commit: "1", Path: path, Title: title, Heading: heading,
		StartLine: 1, EndLine: 2, Text: text,
	}}
}

func paths(hits []Hit) []string {
	out := make([]string, 0, len(hits))
	for _, h := range hits {
		out = append(out, h.Path)
	}
	return out
}

// TestRerankTitleAndLengthBeatRawOrder 固化两条重排信号：标题命中查询的块
// 应排到只有正文命中的长块前面；标题没有差异时，短块不应被 8k 长块压住。
func TestRerankTitleAndLengthBeatRawOrder(t *testing.T) {
	t.Run("title", func(t *testing.T) {
		q := &fakeFulltextQuerier{matches: []domain.KnowledgeChunkMatch{
			chunkMatch("generic.md", "运维手册", "概述", strings.Repeat("测试环境搭建需要参考其他文档。", 100)),
			chunkMatch("guide.md", "测试环境搭建指南", "搭建步骤", strings.Repeat("测试环境搭建步骤。", 20)),
		}}
		hits, err := NewMySQLKeywordIndex(q).Search(context.Background(), "测试环境搭建", []string{"s"}, SearchOptions{Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		if got := paths(hits); len(got) != 2 || got[0] != "guide.md" {
			t.Fatalf("标题命中应排第一: %v", got)
		}
	})

	t.Run("length", func(t *testing.T) {
		q := &fakeFulltextQuerier{matches: []domain.KnowledgeChunkMatch{
			chunkMatch("long.md", "密钥", "", strings.Repeat("密钥", 4000)),
			chunkMatch("short.md", "密钥", "", strings.Repeat("密钥", 100)),
		}}
		hits, err := NewMySQLKeywordIndex(q).Search(context.Background(), "密钥", []string{"s"}, SearchOptions{Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		if got := paths(hits); len(got) != 2 || got[0] != "short.md" {
			t.Fatalf("等标题命中时短块应排第一: %v", got)
		}
	})
}

// TestRerankNavPageDownweightedNotDropped 固化导航页规则：README/索引页降权，
// 但仍保留在结果里，不能像上一轮实测那样直接排除。
func TestRerankNavPageDownweightedNotDropped(t *testing.T) {
	q := &fakeFulltextQuerier{matches: []domain.KnowledgeChunkMatch{
		chunkMatch("docs/README.md", "镜像删除", "", strings.Repeat("镜像删除", 100)),
		chunkMatch("docs/delete-image.md", "镜像删除", "", strings.Repeat("镜像删除", 100)),
	}}
	hits, err := NewMySQLKeywordIndex(q).Search(context.Background(), "镜像删除", []string{"s"}, SearchOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	got := paths(hits)
	if len(got) != 2 {
		t.Fatalf("导航页不应被排除: %v", got)
	}
	if got[0] != "docs/delete-image.md" {
		t.Fatalf("导航页应降权: %v", got)
	}
}

// TestRerankIdentifierExactMatchBoost 固化英文标识符精确命中：同样的标题、
// 同样的长度，正文含完整查询标识符的块应压过只含普通中文词的块。
func TestRerankIdentifierExactMatchBoost(t *testing.T) {
	q := &fakeFulltextQuerier{matches: []domain.KnowledgeChunkMatch{
		chunkMatch("generic.md", "防火墙规则", "", strings.Repeat("防火墙规则说明。", 20)),
		chunkMatch("api.md", "防火墙规则", "", strings.Repeat("防火墙规则说明。", 18)+"接口 DescribeInstanceFirewallRules 返回示例。"),
	}}
	hits, err := NewMySQLKeywordIndex(q).Search(context.Background(), "查询防火墙规则 DescribeInstanceFirewallRules", []string{"s"}, SearchOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if got := paths(hits); len(got) != 2 || got[0] != "api.md" {
		t.Fatalf("精确标识符块应排第一: %v", got)
	}
}

// TestBooleanChannelAddsExactIdentifierHits 固化精确通道：自然语言候选里
// 没有的 API 文档，从 BOOLEAN 通道补进来；只匹配到片段、没有完整标识符的
// 块不参与合并。
func TestBooleanChannelAddsExactIdentifierHits(t *testing.T) {
	q := &rankingQuerier{
		nl: []domain.KnowledgeChunkMatch{
			chunkMatch("stream.md", "AI 流式输出", "", "流式输出的返回格式与工具调用说明。"),
			chunkMatch("decision.md", "决策记录", "", "工具调用与检索策略的历史决策。"),
		},
		boolHits: []domain.KnowledgeChunkMatch{
			chunkMatch("skill.md", "kb_search 工具", "分层检索", "kb_search 工具的使用方法。"),
			chunkMatch("junk.md", "其他", "", "kb search 两个词的片段，不含下划线标识符。"),
		},
	}
	hits, err := NewMySQLKeywordIndex(q).Search(context.Background(), "kb_search 工具怎么用", []string{"s"}, SearchOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if q.boolCalls != 1 || q.boolQuery != "kb_search" {
		t.Fatalf("应只把强标识符送 BOOLEAN 通道: calls=%d query=%q", q.boolCalls, q.boolQuery)
	}
	got := paths(hits)
	merged := false
	for _, p := range got {
		if p == "skill.md" {
			merged = true
		}
		if p == "junk.md" {
			t.Fatalf("只有片段命中的块不应合并: %v", got)
		}
	}
	if !merged {
		t.Fatalf("精确标识符文档应通过 BOOLEAN 通道合并进结果: %v", got)
	}
}

// TestBooleanChannelSkippedWithoutStrongIdentifier 固化触发条件：纯中文
// 查询、只有普通小写英文词的查询都不打第二条 SQL。
func TestBooleanChannelSkippedWithoutStrongIdentifier(t *testing.T) {
	cases := []string{"测试环境搭建", "example 测试环境搭建", "防火墙规则怎么查"}
	for _, query := range cases {
		q := &rankingQuerier{nl: []domain.KnowledgeChunkMatch{chunkMatch("a.md", "文档", "", "正文")}}
		if _, err := NewMySQLKeywordIndex(q).Search(context.Background(), query, []string{"s"}, SearchOptions{Limit: 10}); err != nil {
			t.Fatal(err)
		}
		if q.boolCalls != 0 {
			t.Fatalf("查询 %q 不应触发 BOOLEAN 通道: %d 次", query, q.boolCalls)
		}
	}
}

// TestBooleanChannelFailureFallsBackToNL 固化降级：精确通道失败时仍返回
// 自然语言结果，不把整次检索打失败。
func TestBooleanChannelFailureFallsBackToNL(t *testing.T) {
	q := &rankingQuerier{
		nl:      []domain.KnowledgeChunkMatch{chunkMatch("a.md", "文档", "", "kb_search 正文")},
		boolErr: errors.New("boolean query failed"),
	}
	hits, err := NewMySQLKeywordIndex(q).Search(context.Background(), "kb_search 怎么用", []string{"s"}, SearchOptions{Limit: 10})
	if err != nil {
		t.Fatalf("精确通道失败不应影响检索: %v", err)
	}
	if len(hits) != 1 || hits[0].Path != "a.md" {
		t.Fatalf("应退回自然语言结果: %+v", hits)
	}
}

// TestNavPageDetection 验证导航页规则是通用规则，不依赖某个语料的路径。
func TestNavPageDetection(t *testing.T) {
	cases := []struct {
		path, text string
		want       bool
	}{
		{"docs/README.md", "正文", true},
		{"docs/index.md", "正文", true},
		{"docs/目录.md", "正文", true},
		{"docs/目录/page.md", "正文", true},
		{"docs/notes.md", strings.Repeat("- [条目](a.md)\n", 20), true},
		{"docs/guide.md", strings.Repeat("普通正文内容。", 20), false},
	}
	for _, tc := range cases {
		if got := navigationPage(tc.path, tc.text); got != tc.want {
			t.Fatalf("navigationPage(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

// TestStrongIdentifiers 固化强标识符判定：下划线、CamelCase、全大写缩写、
// 含数字的协议名触发精确通道；普通小写英文词不触发。
func TestStrongIdentifiers(t *testing.T) {
	cases := map[string]bool{
		"kb_search":                     true,
		"DescribeInstanceFirewallRules": true,
		"SSH":                           true,
		"CVM":                           true,
		"IPv6":                          true,
		"OpenClaw":                      true,
		"example":                       false,
		"frontend":                      false,
		"no":                            false,
	}
	for token, want := range cases {
		if got := isStrongIdentifier(token); got != want {
			t.Fatalf("isStrongIdentifier(%q) = %v, want %v", token, got, want)
		}
	}
}

// Common fragments must not hide the missing content words in a long query.
func TestKeywordRejectsUncoveredContentWords(t *testing.T) {
	q := &rankingQuerier{nl: []domain.KnowledgeChunkMatch{
		chunkMatch("api.md", "API 规范", "接口规范", "运行编排接口规范。"),
		chunkMatch("registry.md", "注册规范", "规范", "注册接口规范。"),
		chunkMatch("guide.md", "开发指南", "规范", "开发规范与约定。"),
		chunkMatch("test.md", "测试", "约定", "遵循测试规范。"),
	}}
	hits, err := NewMySQLKeywordIndex(q).Search(context.Background(), "量子纠缠香蕉皮运输规范", []string{"s"}, SearchOptions{Limit: 10})
	if err != nil || len(hits) != 0 {
		t.Fatalf("uncovered query must be empty: %v %v", paths(hits), err)
	}
}

func TestKeywordQuestionContentBeatsGenericEnvironment(t *testing.T) {
	// Deployment is retrieved only by the identifier channel, below generic
	// environment documents. Matching two channels must not outweigh intent.
	bot := chunkMatch("bot.md", "Main Bot 流程交接与交付验收", "Main Bot 流程交接与交付验收", "操作返回 job 仅表示受理。Bot 应提交简短回复后结束本轮，后台在回复和工作区保存结束后执行。不要在同一轮等待该 job 完成。当前用户消息、发起人和 generation 由会话网关绑定，模型不能通过参数伪造。取消、替换或未完成的回复不能推进流程；显式重试绑定新的用户消息。无人值守定时任务不能调用这三个控制写工具。\n\n验收凭据保存在 run 的 `workflowSnapshot.groupCompletion`，包含发起人、依据、来源会话/generation/消息序号和时间；需求操作历史也记录发起人及依据。验收不会将未运行的测试改成通过，也不表示已合并 MR。\n\n部署前可手动执行 `migrations/017_bot_sop_controls.sql`，为原交接任务表添加操作类型和消息来源字段。脚本不创建任务、不改历史数据；已由 AutoMigrate 补齐列的环境不必重复执行。\n\n回归测试默认使用内存测试库；设置 `AGENTFLOW_BOT_SOP_TEST_MYSQL_DSN` 可在专用本地 Docker MySQL 上验证完整工具调用、事务闭环、重复请求、取消后的显式重试和交接时序。该测试只接受 `127.0.0.1` 和 `agentflow_handoff_jobs_test_` 前缀的临时数据库，不需要生产凭据或模型调用。")
	deployment := chunkMatch("docs/deployment.md", "Deployment", "Native development", "Stop the foreground service with Ctrl-C. Start it again with the same command. MySQL data stays in its Compose volume. `docker compose stop mysql` stops the database without deleting it.\n\nUse `AGENTFLOW_MYSQL_DSN` for your own MySQL deployment. A fresh account command or server connection creates missing schema objects. Apply existing-installation data migrations individually after reviewing their prerequisites; do not blindly replay all historical migrations against a new database.\n\n## Container installation")
	q := &rankingQuerier{boolHits: []domain.KnowledgeChunkMatch{bot}}
	for i := 0; i < 17; i++ {
		q.nl = append(q.nl, chunkMatch(fmt.Sprintf("nl-%d.md", i), "环境", "", strings.Repeat("选择适合的环境。", 40)))
	}
	q.nl = append(q.nl, bot)
	for i := 0; i < 10; i++ {
		q.boolHits = append(q.boolHits, chunkMatch(fmt.Sprintf("generic-%d.md", i), "Environment", "", strings.Repeat("环境选择只影响客户端。ENV configuration.", 40)))
	}
	q.boolHits = append(q.boolHits, deployment)
	hits, err := NewMySQLKeywordIndex(q).Search(context.Background(), "数据库连接串的环境变量叫什么", []string{"s"}, SearchOptions{Limit: 10})
	if err != nil || len(hits) == 0 || hits[0].Path != "docs/deployment.md" {
		t.Fatalf("content words must rank deployment first: %v %v", paths(hits), err)
	}
}
