// Package chunk 把 Markdown 文档切成带来源行号的分块：
// 按段落边界切分（避免句子被腰斩），跟踪当前小节标题，保留 1 段重叠保证上下文。
package chunk

import (
	"bufio"
	"strings"
)

const (
	TargetRunes       = 700  // 目标块长（按 rune，中英文混合下约一段主题）
	MaxRunes          = 1200 // 普通段落的块长上限（保留自然超长段落）
	MaxEmbeddingRunes = 8000 // 异常长段落的传输上限
	OverlapParas      = 1    // 块间重叠段落数
)

type Block struct {
	Heading   string
	StartLine int
	EndLine   int
	Text      string
}

// Title 提取文档 H1（跳过 YAML frontmatter；或首个非空行兜底）。
func Title(content string) string {
	s := bufio.NewScanner(strings.NewReader(content))
	inFM := false
	fmDone := false
	for s.Scan() {
		t := strings.TrimSpace(s.Text())
		if !fmDone && t == "---" {
			inFM = !inFM
			if !inFM {
				fmDone = true
			}
			continue
		}
		if inFM {
			continue
		}
		if t == "" {
			continue
		}
		return strings.TrimSpace(strings.TrimLeft(t, "#"))
	}
	return ""
}

// Split 返回文档的分块与总行数。
func Split(content string) ([]Block, int) {
	lines := strings.Split(content, "\n")
	type para struct {
		head      string
		start     int
		end       int
		text      string
		noOverlap bool
	}
	var paras []para
	var cur strings.Builder
	curHead := ""
	curStart := 1
	lineNo := 0
	flush := func(end int) {
		t := strings.TrimSpace(cur.String())
		if t != "" {
			paras = append(paras, para{head: curHead, start: curStart, end: end, text: t})
		}
		cur.Reset()
	}
	inFence := false
	for _, raw := range lines {
		lineNo++
		trim := strings.TrimSpace(raw)
		if strings.HasPrefix(trim, "```") {
			inFence = !inFence
		}
		if !inFence && strings.HasPrefix(trim, "#") {
			flush(lineNo - 1)
			curHead = strings.TrimSpace(strings.TrimLeft(trim, "#"))
			paras = append(paras, para{head: curHead, start: lineNo, end: lineNo, text: trim})
			curStart = lineNo + 1
			continue
		}
		if !inFence && trim == "" {
			flush(lineNo - 1)
			curStart = lineNo + 1
			continue
		}
		if cur.Len() == 0 {
			curStart = lineNo
		}
		cur.WriteString(raw + "\n")
	}
	flush(lineNo)
	// 普通超长段落保持整段，避免出处被腰斩。只有单段长到无法整体传输时才切开。
	expanded := make([]para, 0, len(paras))
	pieceLimit := MaxEmbeddingRunes - 64 // 给重叠分隔和空白留余量
	for _, p := range paras {
		runes := []rune(p.text)
		if len(runes) <= MaxEmbeddingRunes {
			expanded = append(expanded, p)
			continue
		}
		for start := 0; start < len(runes); start += pieceLimit {
			end := start + pieceLimit
			if end > len(runes) {
				end = len(runes)
			}
			expanded = append(expanded, para{head: p.head, start: p.start, end: p.end, text: string(runes[start:end]), noOverlap: true})
		}
	}
	paras = expanded

	// 段落装箱：超 MaxRunes 的段落单独成块（不腰斩），其余按 TargetRunes 聚合
	var blocks []Block
	var buf []para
	size := 0
	emit := func() {
		if len(buf) == 0 {
			return
		}
		var b strings.Builder
		for i, p := range buf {
			if i > 0 {
				b.WriteString("\n\n")
			}
			b.WriteString(p.text)
		}
		blocks = append(blocks, Block{
			Heading:   buf[0].head,
			StartLine: buf[0].start,
			EndLine:   buf[len(buf)-1].end,
			Text:      b.String(),
		})
		// 保留重叠段，保证跨块上下文可检索
		if len(buf) >= OverlapParas && !buf[len(buf)-1].noOverlap {
			buf = append([]para{}, buf[len(buf)-OverlapParas:]...)
		} else {
			buf = nil
		}
		size = 0
		for _, p := range buf {
			size += len([]rune(p.text))
		}
	}
	for _, p := range paras {
		n := len([]rune(p.text))
		if n >= MaxRunes || size+n > TargetRunes && size > 0 {
			emit()
		}
		buf = append(buf, p)
		size += n
		if n >= MaxRunes {
			emit()
		}
	}
	emit()
	// 去掉纯重叠尾块（与上一块同区间）
	if len(blocks) >= 2 && blocks[len(blocks)-1].Text == blocks[len(blocks)-2].Text {
		blocks = blocks[:len(blocks)-1]
	}
	// 去掉重叠产生的“纯前缀重复”尾块
	if len(blocks) >= 2 {
		last, prev := &blocks[len(blocks)-1], &blocks[len(blocks)-2]
		if strings.HasPrefix(prev.Text, last.Text) {
			blocks = blocks[:len(blocks)-1]
		}
	}
	return blocks, lineNo
}
