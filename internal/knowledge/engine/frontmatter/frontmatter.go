// Package frontmatter 校验或补齐知识文档的 YAML 头。
// 已有头时只接受能安全解析、且 title 为非空字符串的原文；裸 Markdown 补一份默认头。
package frontmatter

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// MaxDocBytes 是单篇正文的写入上限。
const MaxDocBytes = 1 << 20

// Ensure 保证 content 带有可接受的 frontmatter。
// 已有头且校验通过时原样返回；没有头时用 H1 或文件名生成默认头。
func Ensure(docPath string, content []byte) ([]byte, error) {
	if len(content) > MaxDocBytes {
		return nil, fmt.Errorf("文档超过%d字节写入预算", MaxDocBytes)
	}
	trimmed := bytes.TrimSpace(content)
	if bytes.HasPrefix(trimmed, []byte("---")) {
		fm, _, err := parseFrontmatter(content)
		if err != nil {
			return nil, fmt.Errorf("frontmatter 无效: %w", err)
		}
		if fm == nil {
			return nil, fmt.Errorf("frontmatter 无效: 文档缺少 frontmatter")
		}
		title, ok := fm["title"].(string)
		if !ok || strings.TrimSpace(title) == "" {
			return nil, fmt.Errorf("frontmatter title 必须是非空字符串")
		}
		for _, warning := range validateFrontmatter(fm) {
			if strings.HasPrefix(warning, "缺少 ") || strings.HasPrefix(warning, "owner 字段为空") {
				continue
			}
			return nil, fmt.Errorf("frontmatter 无效: %s", warning)
		}
		return content, nil
	}

	title := titleFromH1(content)
	if title == "" {
		title = titleFromPath(docPath)
	}
	header := fmt.Sprintf("---\ntitle: %s\ncategory: %s\ntags: []\nowner: \"\"\nstatus: draft\nupdated: %s\n---\n\n",
		yamlString(title), yamlString(categoryFromPath(docPath)), time.Now().UTC().Format("2006-01-02"))
	return append([]byte(header), content...), nil
}

// titleFromH1 取第一个 H1，并跳过 frontmatter。找不到时返回空串。
func titleFromH1(body []byte) string {
	lines := bytes.SplitN(body, []byte("\n"), 50)
	inFM := false
	for _, line := range lines {
		t := bytes.TrimSpace(line)
		if bytes.Equal(t, []byte("---")) {
			inFM = !inFM
			continue
		}
		if inFM {
			continue
		}
		if bytes.HasPrefix(t, []byte("# ")) {
			return strings.TrimSpace(string(t[2:]))
		}
	}
	return ""
}

func titleFromPath(path string) string {
	base := filepath.Base(strings.TrimSpace(path))
	return strings.TrimSuffix(base, filepath.Ext(base))
}

func categoryFromPath(path string) string {
	clean := filepath.ToSlash(strings.TrimSpace(path))
	first := strings.Split(filepath.ToSlash(filepath.Dir(clean)), "/")[0]
	if first == "" || first == "." {
		return "guides"
	}
	return first
}

func yamlString(s string) string {
	if s == "" {
		return `""`
	}
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}
