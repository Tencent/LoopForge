package frontmatter

import (
	"testing"
	"time"
)

func TestParseMetadataReadsGovernanceFields(t *testing.T) {
	body := []byte("---\ntitle: 部署手册\nowner: 张三\nstatus: 已废弃\nupdated: 2024-02-03\n---\n# 部署手册\n\n正文\n")
	meta, err := Parse(body)
	if err != nil {
		t.Fatal(err)
	}
	if !meta.Present || meta.Title != "部署手册" || meta.Owner != "张三" {
		t.Fatalf("metadata = %+v", meta)
	}
	if meta.Status != "deprecated" {
		t.Fatalf("status = %q, want deprecated", meta.Status)
	}
	if meta.Updated == nil || meta.Updated.Format("2006-01-02") != "2024-02-03" {
		t.Fatalf("updated = %v", meta.Updated)
	}
}

func TestParseMetadataNormalizesLifecycleStatus(t *testing.T) {
	cases := map[string]string{
		"draft": "draft", "草稿": "draft", "DRAFT": "draft",
		"verified": "verified", "已验证": "verified",
		"deprecated": "deprecated", "已废弃": "deprecated",
		"archived": "archived", "已归档": "archived",
		"custom": "custom", "": "",
	}
	for raw, want := range cases {
		if got := NormalizeStatus(raw); got != want {
			t.Errorf("NormalizeStatus(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestParseMetadataHandlesMissingAndMalformedFrontmatter(t *testing.T) {
	meta, err := Parse([]byte("# 裸 Markdown\n"))
	if err != nil || meta.Present || meta.Owner != "" || meta.Status != "" || meta.Updated != nil {
		t.Fatalf("bare markdown = %+v err=%v", meta, err)
	}

	meta, err = Parse([]byte("---\nowner: [not-a-string]\nstatus: 3\nupdated: not-a-date\n---\n# X\n"))
	if err != nil {
		t.Fatalf("field-level type problems should not fail parsing: %v", err)
	}
	if meta.Owner != "" || meta.Status != "3" || meta.Updated != nil {
		t.Fatalf("lenient metadata = %+v", meta)
	}

	if _, err := Parse([]byte("---\ntitle: [unclosed\n---\n# X\n")); err == nil {
		t.Fatal("invalid YAML must return an error")
	}
}

func TestParseMetadataAcceptsYAMLDateNodes(t *testing.T) {
	meta, err := Parse([]byte("---\nupdated: 2025-01-02\nowner: alice\nstatus: verified\n---\n"))
	if err != nil {
		t.Fatal(err)
	}
	if meta.Updated == nil || !meta.Updated.Equal(time.Date(2025, 1, 2, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("updated = %v", meta.Updated)
	}
}
