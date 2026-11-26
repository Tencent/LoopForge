// Package frontmatter 校验知识文档可选的 YAML 头。
// 托管文档不自动补 frontmatter：没有头时正文原样保留；用户自己写了头时，
// 只接受能安全解析、且 title 为非空白字符串的原文，不改写任何一个字节。
package frontmatter

import (
	"bytes"
	"fmt"
	"strings"
)

// MaxDocBytes 是单篇正文的写入上限。
const MaxDocBytes = 1 << 20

// Validate 检查 content 是否可以写入托管知识库。没有 frontmatter 时直接
// 通过；已有头时按与读取路径相同的规则校验，但永远返回原始内容。
func Validate(content []byte) error {
	if len(content) > MaxDocBytes {
		return fmt.Errorf("文档超过%d字节写入预算", MaxDocBytes)
	}
	trimmed := bytes.TrimSpace(content)
	if !bytes.HasPrefix(trimmed, []byte("---")) {
		return nil
	}
	fm, _, err := parseFrontmatter(content)
	if err != nil {
		return fmt.Errorf("frontmatter 无效: %w", err)
	}
	if fm == nil {
		return fmt.Errorf("frontmatter 无效: 文档缺少 frontmatter")
	}
	title, ok := fm["title"].(string)
	if !ok || strings.TrimSpace(title) == "" {
		return fmt.Errorf("frontmatter title 必须是非空字符串")
	}
	for _, warning := range validateFrontmatter(fm) {
		if strings.HasPrefix(warning, "缺少 ") || strings.HasPrefix(warning, "owner 字段为空") {
			continue
		}
		return fmt.Errorf("frontmatter 无效: %s", warning)
	}
	return nil
}
