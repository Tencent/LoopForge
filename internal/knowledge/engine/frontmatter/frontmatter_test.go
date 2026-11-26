package frontmatter

import (
	"fmt"
	"strings"
	"testing"
)

// 托管文档不再自动补 frontmatter：用户写的第一行就是第一行。
func TestValidateKeepsPlainMarkdown(t *testing.T) {
	in := []byte("# 标题\n\n正文\n")
	if err := Validate(in); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(in) != "# 标题\n\n正文\n" {
		t.Fatalf("普通 Markdown 不应被改写: %q", in)
	}
}

func TestValidateKeepsExistingFrontmatter(t *testing.T) {
	in := []byte("---\ntitle: \"已有\"\n---\n\n正文\n")
	if err := Validate(in); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(in) != "---\ntitle: \"已有\"\n---\n\n正文\n" {
		t.Fatalf("已有 frontmatter 不应被改写: %q", in)
	}
}

func TestValidateRejectsInvalidYAML(t *testing.T) {
	in := []byte("---\ntitle: [unclosed\n---\n正文\n")
	if err := Validate(in); err == nil {
		t.Fatal("expected error for invalid YAML in frontmatter")
	}
}

func TestValidateRejectsEmptyTitle(t *testing.T) {
	in := []byte("---\ntitle: \"\"\ncategory: guides\n---\n正文\n")
	if err := Validate(in); err == nil {
		t.Fatal("expected error for empty title")
	}
}

func TestValidateDetectsLeadingWhitespace(t *testing.T) {
	in := []byte("\n---\ntitle: x\n---\n正文\n")
	if err := Validate(in); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(in) != "\n---\ntitle: x\n---\n正文\n" {
		t.Fatalf("带前导空白的 frontmatter 不应被改写: %q", in)
	}
}

func TestValidateUnclosedReturnsError(t *testing.T) {
	in := []byte("---\ntitle: hello\n")
	if err := Validate(in); err == nil {
		t.Fatal("expected error for unclosed frontmatter")
	}
}

func TestValidateKeepsLiteralInjectionString(t *testing.T) {
	in := []byte("---\ntitle: \"!!python/object:os.system\"\ncategory: guides\n---\n")
	if err := Validate(in); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(in) != "---\ntitle: \"!!python/object:os.system\"\ncategory: guides\n---\n" {
		t.Fatalf("valid frontmatter should be preserved: %q", in)
	}
}

func TestValidateAllowsMissingOptionalFields(t *testing.T) {
	in := []byte("---\ntitle: only\n---\n正文\n")
	if err := Validate(in); err != nil {
		t.Fatalf("缺少 category/owner 只是提醒，不应拒绝写入: %v", err)
	}
	if string(in) != "---\ntitle: only\n---\n正文\n" {
		t.Fatalf("原文被改写: %q", in)
	}
}

func TestValidateRejectsSchemaErrors(t *testing.T) {
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
		if err := Validate(in); err == nil {
			t.Fatalf("非法 frontmatter 应被拒绝: %q", in)
		}
	}
}

func TestValidateRejectsAlias(t *testing.T) {
	// 展开后 title 与 category 都合法；只有拒绝 alias 才会失败。
	in := []byte("---\ntitle: &a \"ok\"\ncategory: *a\n---\n")
	err := Validate(in)
	if err == nil || !strings.Contains(err.Error(), "alias") {
		t.Fatalf("expected alias rejection, got %v", err)
	}
}

func TestValidateRejectsDeepNesting(t *testing.T) {
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
	err := Validate([]byte(b.String()))
	if err == nil {
		t.Fatal("expected error for deep nesting")
	}
	if !strings.Contains(err.Error(), "深度") {
		t.Fatalf("error should mention depth: %v", err)
	}
}

func TestValidateRejectsOversizedDocument(t *testing.T) {
	body := make([]byte, MaxDocBytes+1)
	for i := range body {
		body[i] = 'a'
	}
	if err := Validate(body); err == nil {
		t.Fatal("expected error for oversized document")
	}
}
