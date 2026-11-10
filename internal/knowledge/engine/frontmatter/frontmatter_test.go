package frontmatter

import (
	"fmt"
	"strings"
	"testing"
)

func TestEnsureGeneratesWhenMissing(t *testing.T) {
	result, err := Ensure("guides/gpu-oom.md", []byte("# 标题\n\n正文\n"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := string(result)
	if !strings.HasPrefix(got, "---\n") {
		t.Fatalf("missing frontmatter must be generated: %q", got)
	}
	for _, want := range []string{
		"category: \"guides\"",
		"status: draft",
		"tags: []",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("frontmatter 缺少 %q: %q", want, got)
		}
	}
	if !strings.Contains(got, "updated: 20") {
		t.Fatalf("frontmatter 缺少 updated 日期: %q", got)
	}
	if !strings.HasSuffix(got, "# 标题\n\n正文\n") {
		t.Fatalf("正文被破坏: %q", got)
	}
}

func TestEnsureGeneratesTitleFromH1(t *testing.T) {
	result, err := Ensure("guides/gpu-oom.md", []byte("# GPU OOM 排障指南\n\n正文\n"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(string(result), `title: "GPU OOM 排障指南"`) {
		t.Fatalf("title should be extracted from H1: %q", result)
	}
}

func TestEnsureKeepsExisting(t *testing.T) {
	in := []byte("---\ntitle: \"已有\"\n---\n\n正文\n")
	got, err := Ensure("guides/a.md", in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(got) != string(in) {
		t.Fatalf("已有 frontmatter 不应被改写: %q", got)
	}
}

func TestEnsureRejectsInvalidYAML(t *testing.T) {
	in := []byte("---\ntitle: [unclosed\n---\n正文\n")
	if _, err := Ensure("guides/a.md", in); err == nil {
		t.Fatal("expected error for invalid YAML in frontmatter")
	}
}

func TestEnsureRejectsEmptyTitle(t *testing.T) {
	in := []byte("---\ntitle: \"\"\ncategory: guides\n---\n正文\n")
	if _, err := Ensure("guides/a.md", in); err == nil {
		t.Fatal("expected error for empty title")
	}
}

func TestEnsureDetectsLeadingWhitespace(t *testing.T) {
	in := []byte("\n---\ntitle: x\n---\n正文\n")
	got, err := Ensure("guides/a.md", in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(string(got), "\n---") {
		t.Fatalf("带前导空白的 frontmatter 不应被重复生成: %q", got)
	}
}

func TestEnsureUnclosedReturnsError(t *testing.T) {
	in := []byte("---\ntitle: hello\n")
	if _, err := Ensure("a.md", in); err == nil {
		t.Fatal("expected error for unclosed frontmatter")
	}
}

func TestEnsureKeepsLiteralInjectionString(t *testing.T) {
	in := []byte("---\ntitle: \"!!python/object:os.system\"\ncategory: guides\n---\n")
	got, err := Ensure("a.md", in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(got) != string(in) {
		t.Fatalf("valid frontmatter should be preserved: %q", got)
	}
}

func TestEnsureAllowsMissingOptionalFields(t *testing.T) {
	in := []byte("---\ntitle: only\n---\n正文\n")
	got, err := Ensure("guides/a.md", in)
	if err != nil {
		t.Fatalf("缺少 category/owner 只是提醒，不应拒绝写入: %v", err)
	}
	if string(got) != string(in) {
		t.Fatalf("原文被改写: %q", got)
	}
}

func TestEnsureRejectsSchemaErrors(t *testing.T) {
	cases := [][]byte{
		[]byte("---\ntitle: ok\ncategory: \"\"\n---\n"),
		[]byte("---\ntitle: ok\ncategory: 1\n---\n"),
		[]byte("---\ntitle: ok\nowner: 1\n---\n"),
		[]byte("---\ntitle: ok\nstatus: 1\n---\n"),
		[]byte("---\ntitle: ok\nupdated: 2026/01/15\n---\n"),
		[]byte("---\ntitle: ok\ntags: nope\n---\n"),
		[]byte("---\ntitle: ok\ntags:\n  - 1\n---\n"),
	}
	for _, in := range cases {
		if _, err := Ensure("guides/a.md", in); err == nil {
			t.Fatalf("非法 frontmatter 应被拒绝: %q", in)
		}
	}
}

func TestEnsureRejectsAlias(t *testing.T) {
	// 展开后 title 与 category 都合法；只有拒绝 alias 才会失败。
	in := []byte("---\ntitle: &a \"ok\"\ncategory: *a\n---\n")
	_, err := Ensure("guides/a.md", in)
	if err == nil || !strings.Contains(err.Error(), "alias") {
		t.Fatalf("expected alias rejection, got %v", err)
	}
}

func TestEnsureRejectsDeepNesting(t *testing.T) {
	var b strings.Builder
	b.WriteString("---\n")
	for i := 0; i < 15; i++ {
		for j := 0; j < i; j++ {
			b.WriteString("  ")
		}
		b.WriteString(fmt.Sprintf("l%d:\n", i))
	}
	for j := 0; j < 15; j++ {
		b.WriteString("  ")
	}
	b.WriteString("val: x\n---\n")
	_, err := Ensure("a.md", []byte(b.String()))
	if err == nil {
		t.Fatal("expected error for deep nesting")
	}
	if !strings.Contains(err.Error(), "深度") {
		t.Fatalf("error should mention depth: %v", err)
	}
}

func TestEnsureRejectsOversizedDocument(t *testing.T) {
	body := make([]byte, MaxDocBytes+1)
	for i := range body {
		body[i] = 'a'
	}
	if _, err := Ensure("a.md", body); err == nil {
		t.Fatal("expected error for oversized document")
	}
}

func TestEnsureGeneratedOutputIsAcceptedAgain(t *testing.T) {
	once, err := Ensure("concepts/架构.md", []byte("# 架构\n\n正文\n"))
	if err != nil {
		t.Fatal(err)
	}
	twice, err := Ensure("concepts/架构.md", once)
	if err != nil {
		t.Fatalf("生成结果应能再次通过校验: %v", err)
	}
	if string(twice) != string(once) {
		t.Fatalf("再次校验不应改写: %q", twice)
	}
}

func TestTitleFromPath(t *testing.T) {
	cases := map[string]string{
		"guides/gpu-oom.md": "gpu-oom",
		"faq/退款.md":         "退款",
		"README.md":         "README",
	}
	for path, want := range cases {
		if got := titleFromPath(path); got != want {
			t.Fatalf("titleFromPath(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestCategoryFromPath(t *testing.T) {
	cases := map[string]string{
		"guides/gpu-oom.md": "guides",
		"concepts/架构.md":    "concepts",
		"README.md":         "guides",
		"a/b/c.md":          "a",
	}
	for path, want := range cases {
		if got := categoryFromPath(path); got != want {
			t.Fatalf("categoryFromPath(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestYamlStringEscapes(t *testing.T) {
	if got := yamlString(`a"b\c`); got != `"a\"b\\c"` {
		t.Fatalf("yamlString 转义错误: %q", got)
	}
	if got := yamlString(""); got != `""` {
		t.Fatalf("空字符串应输出空引号: %q", got)
	}
}

func TestTitleFromH1(t *testing.T) {
	cases := []struct {
		body []byte
		want string
	}{
		{[]byte("# My Title\n\nbody\n"), "My Title"},
		{[]byte("---\ntitle: x\n---\n\n# Real Title\n"), "Real Title"},
		{[]byte("no heading here\n"), ""},
	}
	for _, tc := range cases {
		got := titleFromH1(tc.body)
		if got != tc.want {
			t.Errorf("titleFromH1(%q) = %q, want %q", tc.body, got, tc.want)
		}
	}
}
