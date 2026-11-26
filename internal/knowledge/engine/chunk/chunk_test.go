package chunk

import (
	"strings"
	"testing"
)

func TestSplitKeepsHeadingAndLines(t *testing.T) {
	fence := "```"
	filler := strings.Repeat("填充内容。", 80) // 每节超过目标块长，强制跨节分块
	doc := "# 手册标题\n\n## 概述\n" + filler + "\n\n## 适用场景\n- 场景一\n- 场景二\n" + filler + "\n\n" +
		fence + "bash\n# 代码块里的井号不是标题\necho hi\n" + fence + "\n"
	blocks, _ := Split(doc)
	if len(blocks) < 2 {
		t.Fatalf("大文档应分多块: %+v", blocks)
	}
	var sawUsage bool
	for _, b := range blocks {
		if strings.Contains(b.Text, "场景一") && b.Heading == "适用场景" {
			sawUsage = true
		}
		if b.StartLine < 1 || b.EndLine < b.StartLine {
			t.Fatalf("行号范围非法: %+v", b)
		}
		if b.Heading == "代码块里的井号不是标题" {
			t.Fatalf("代码块内 # 被误当标题")
		}
	}
	if !sawUsage {
		t.Fatalf("未找到带“适用场景”标题的块: %+v", blocks)
	}
}

func TestSplitLongParagraphNotCut(t *testing.T) {
	long := strings.Repeat("长", MaxRunes+50)
	doc := "# 标题\n\n" + long + "\n"
	blocks, _ := Split(doc)
	if len(blocks) == 0 {
		t.Fatal("应有分块")
	}
	for _, b := range blocks {
		if strings.Contains(b.Text, "长") && !strings.Contains(b.Text, long) {
			t.Fatalf("超长段落被腰斩")
		}
	}
}

func TestSplitExceptionalParagraphForEmbeddingTransport(t *testing.T) {
	long := strings.Repeat("异常长内容。", MaxEmbeddingRunes/6+100)
	blocks, _ := Split("# 标题\n\n" + long + "\n")
	if len(blocks) < 2 {
		t.Fatalf("异常长段落应拆成多块")
	}
	var rebuilt strings.Builder
	for _, block := range blocks {
		if len([]rune(block.Text)) > MaxEmbeddingRunes {
			t.Fatalf("块仍超过长度上限: %d", len([]rune(block.Text)))
		}
		rebuilt.WriteString(block.Text)
	}
	if !strings.Contains(rebuilt.String(), "异常长内容") {
		t.Fatal("拆分后丢失原文")
	}
}

func TestTitle(t *testing.T) {
	if got := Title("---\ntitle: x\n---\n# 真正的标题\n正文"); got != "真正的标题" {
		t.Fatalf("Title=%q", got)
	}
}
