package frontmatter

import (
	"bytes"
	"fmt"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	maxFrontmatter = 16 * 1024
	maxYAMLDepth   = 10
	maxYAMLNodes   = 500
)

type meta map[string]any

// parseFrontmatter 安全解析 YAML 头。没有分隔符时返回 nil。
// 语法错误、未闭合、别名、过深或过大的头都返回错误。
func parseFrontmatter(body []byte) (meta, int, error) {
	trimmed := bytes.TrimLeft(body, " \t\r\n")
	if !bytes.HasPrefix(trimmed, []byte("---")) {
		return nil, 0, nil
	}

	offset := len(body) - len(trimmed)
	rest := trimmed[3:]
	nlIdx := bytes.IndexByte(rest, '\n')
	if nlIdx < 0 {
		return nil, 0, fmt.Errorf("frontmatter 只有开头分隔符，缺少结束 ---")
	}
	rest = rest[nlIdx+1:]
	yamlStart := offset + 3 + nlIdx + 1

	closeIdx := bytes.Index(rest, []byte("\n---"))
	if closeIdx < 0 {
		if bytes.HasPrefix(rest, []byte("---")) {
			return meta{}, yamlStart, nil
		}
		return nil, 0, fmt.Errorf("frontmatter 缺少结束 ---")
	}

	yamlBlock := rest[:closeIdx]
	fmEndLine := bytes.Count(body[:yamlStart+closeIdx+4], []byte("\n"))
	if len(yamlBlock) > maxFrontmatter {
		return nil, 0, fmt.Errorf("frontmatter 超过 %d 字节限制", maxFrontmatter)
	}

	var node yaml.Node
	if err := yaml.Unmarshal(yamlBlock, &node); err != nil {
		return nil, 0, fmt.Errorf("frontmatter YAML 解析失败: %w", err)
	}
	if err := validateYAMLTree(&node); err != nil {
		return nil, 0, fmt.Errorf("frontmatter YAML 安全校验失败: %w", err)
	}

	var fm meta
	if err := node.Decode(&fm); err != nil {
		return nil, 0, fmt.Errorf("frontmatter YAML 解析失败: %w", err)
	}
	if fm == nil {
		fm = meta{}
	}
	return fm, fmEndLine, nil
}

func validateYAMLTree(n *yaml.Node) error {
	count := 0
	return validateYAMLNode(n, 0, &count)
}

func validateYAMLNode(n *yaml.Node, depth int, count *int) error {
	if n == nil {
		return nil
	}
	*count++
	if *count > maxYAMLNodes {
		return fmt.Errorf("节点数超过 %d 个限制", maxYAMLNodes)
	}
	if depth > maxYAMLDepth {
		return fmt.Errorf("嵌套深度超过 %d 层", maxYAMLDepth)
	}
	if n.Kind == yaml.AliasNode {
		return fmt.Errorf("不允许使用 YAML alias（*引用）")
	}
	for _, child := range n.Content {
		if err := validateYAMLNode(child, depth+1, count); err != nil {
			return err
		}
	}
	return nil
}

func validateFrontmatter(fm meta) []string {
	var warnings []string
	if fm == nil {
		return []string{"文档缺少 frontmatter"}
	}

	if title, ok := fm["title"]; !ok {
		warnings = append(warnings, "缺少 title 字段")
	} else if s, ok := title.(string); !ok || strings.TrimSpace(s) == "" {
		warnings = append(warnings, "title 字段为空或类型非法")
	}

	if cat, ok := fm["category"]; !ok {
		warnings = append(warnings, "缺少 category 字段")
	} else if s, ok := cat.(string); !ok || strings.TrimSpace(s) == "" {
		warnings = append(warnings, "category 字段为空或类型非法")
	}

	if owner, ok := fm["owner"]; !ok {
		warnings = append(warnings, "缺少 owner 字段")
	} else if s, ok := owner.(string); !ok {
		warnings = append(warnings, "owner 字段类型非法（应为字符串）")
	} else if strings.TrimSpace(s) == "" {
		warnings = append(warnings, "owner 字段为空（建议补充文档负责人）")
	}

	if status, ok := fm["status"]; ok {
		if _, ok := status.(string); !ok {
			warnings = append(warnings, "status 字段类型非法（应为字符串）")
		}
	}

	if updated, ok := fm["updated"]; ok {
		if err := validateDateField(updated); err != nil {
			warnings = append(warnings, "updated 字段无效: "+err.Error())
		}
	}

	if tags, ok := fm["tags"]; ok {
		switch values := tags.(type) {
		case []any:
			for _, value := range values {
				if _, ok := value.(string); !ok {
					warnings = append(warnings, "tags 列表元素应为字符串")
					break
				}
			}
		case nil:
		default:
			warnings = append(warnings, "tags 字段应为列表")
		}
	}
	return warnings
}

func validateDateField(v any) error {
	switch d := v.(type) {
	case string:
		if _, err := time.Parse("2006-01-02", d); err != nil {
			return fmt.Errorf("日期格式应为 YYYY-MM-DD，实际: %q", d)
		}
	case time.Time:
	default:
		return fmt.Errorf("类型应为日期字符串，实际: %T", v)
	}
	return nil
}
