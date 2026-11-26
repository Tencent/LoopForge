package frontmatter

import (
	"fmt"
	"strings"
	"time"
)

// Metadata 是索引时从 frontmatter 读出的治理字段。没有 frontmatter 时
// Present 为 false，其余字段为零值；字段类型不符合预期时只丢弃该字段，
// 不影响正文写入或检索。
type Metadata struct {
	Present bool
	Title   string
	Owner   string
	Status  string
	Updated *time.Time
}

// Parse 解析文档头部的 YAML frontmatter 并返回治理字段。语法错误、未闭合
// 或安全校验失败返回 error；字段级类型问题按缺失处理，不报错。
func Parse(content []byte) (Metadata, error) {
	fm, _, err := parseFrontmatter(content)
	if err != nil {
		return Metadata{}, err
	}
	if fm == nil {
		return Metadata{}, nil
	}
	out := Metadata{Present: true}
	if value, ok := fm["title"].(string); ok {
		out.Title = strings.TrimSpace(value)
	}
	if value, ok := fm["owner"].(string); ok {
		out.Owner = strings.TrimSpace(value)
	}
	if value, ok := fm["status"]; ok && value != nil {
		out.Status = NormalizeStatus(strings.TrimSpace(toString(value)))
	}
	if value, ok := fm["updated"]; ok {
		if parsed := parseDate(value); parsed != nil {
			out.Updated = parsed
		}
	}
	return out, nil
}

// NormalizeStatus 把常见中英文写法收敛到四个生命周期值。未知状态保留原文
// （转小写），检索时按中性处理，便于以后扩展而不丢信息。
func NormalizeStatus(raw string) string {
	status := strings.ToLower(strings.TrimSpace(raw))
	switch status {
	case "draft", "wip", "in_progress", "in-progress", "草稿", "待完善":
		return "draft"
	case "verified", "published", "active", "已验证", "已发布":
		return "verified"
	case "deprecated", "obsolete", "已废弃", "废弃":
		return "deprecated"
	case "archived", "已归档", "归档":
		return "archived"
	default:
		return status
	}
}

func toString(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return fmt.Sprint(v)
	case float32, float64:
		return fmt.Sprint(v)
	case bool:
		return fmt.Sprint(v)
	default:
		return ""
	}
}

func parseDate(value any) *time.Time {
	switch v := value.(type) {
	case time.Time:
		t := v.UTC()
		return &t
	case string:
		text := strings.TrimSpace(v)
		for _, layout := range []string{"2006-01-02", time.RFC3339} {
			if parsed, err := time.Parse(layout, text); err == nil {
				t := parsed.UTC()
				return &t
			}
		}
	}
	return nil
}
