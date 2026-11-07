package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// assistantToolResultBudget keeps a platform list result under the CLI's
// per-result limit. Above that limit the CLI saves the result to a one-line
// local file and asks the model to parse it with jq, but the assistant runs
// without a shell, so the data becomes unreadable.
const assistantToolResultBudget = 48 << 10

// assistantResultFieldsParam is the read-tool query parameter that selects the
// fields to return. It is consumed by the platform and never sent to handlers.
const assistantResultFieldsParam = "fields"

// assistantResultIDsParam is the list-tool query parameter that keeps only the
// items with these ids. It reaches items a trimmed list left out, including
// those of lists without a *_get tool (skills, rules, environments). Like
// fields, it is consumed by the platform.
const assistantResultIDsParam = "ids"

func assistantResultFields(raw string) []string {
	var out []string
	seen := map[string]bool{}
	for _, field := range strings.Split(raw, ",") {
		field = strings.TrimSpace(field)
		if field != "" && !seen[field] {
			seen[field] = true
			out = append(out, field)
		}
	}
	return out
}

func decodeAssistantJSON(text string) (any, bool) {
	dec := json.NewDecoder(strings.NewReader(text))
	// Keep large integer IDs exact.
	dec.UseNumber()
	var v any
	if dec.Decode(&v) != nil {
		return nil, false
	}
	if _, err := dec.Token(); err == nil {
		// Trailing data: not a single JSON document.
		return nil, false
	}
	switch v.(type) {
	case map[string]any, []any:
		return v, true
	}
	return nil, false
}

func encodeAssistantJSON(v any) (string, bool) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if enc.Encode(v) != nil {
		return "", false
	}
	return strings.TrimRight(buf.String(), "\n"), true
}

// projectAssistantResult keeps only the requested fields: of every element of
// a top-level list, of a top-level object that has any of them, or of the
// elements of lists wrapped in an object such as {"items":[...],"total":3}.
// Anything that is not JSON is returned unchanged.
func projectAssistantResult(text string, fields []string) string {
	if len(fields) == 0 {
		return text
	}
	v, ok := decodeAssistantJSON(text)
	if !ok {
		return text
	}
	keep := make(map[string]bool, len(fields))
	for _, field := range fields {
		keep[field] = true
	}
	out, ok := encodeAssistantJSON(projectAssistantValue(v, keep))
	if !ok {
		return text
	}
	return out
}

func projectAssistantValue(v any, keep map[string]bool) any {
	switch t := v.(type) {
	case []any:
		out := make([]any, len(t))
		for i, item := range t {
			out[i] = projectAssistantObject(item, keep)
		}
		return out
	case map[string]any:
		for key := range keep {
			if _, ok := t[key]; ok {
				return projectAssistantObject(t, keep)
			}
		}
		out := make(map[string]any, len(t))
		for key, val := range t {
			if list, ok := val.([]any); ok && assistantObjectList(list) {
				out[key] = projectAssistantValue(list, keep)
			} else {
				out[key] = val
			}
		}
		return out
	}
	return v
}

func projectAssistantObject(v any, keep map[string]bool) any {
	obj, ok := v.(map[string]any)
	if !ok {
		return v
	}
	out := make(map[string]any, len(keep))
	for key, val := range obj {
		if keep[key] {
			out[key] = val
		}
	}
	return out
}

// filterAssistantResultIDs keeps only the items whose id is requested: of a
// top-level list, or of the lists of objects wrapped in a top-level object such
// as {"items":[...],"total":3}. Kept items are unchanged. Anything that is not
// JSON is returned unchanged.
func filterAssistantResultIDs(text string, ids []string) string {
	if len(ids) == 0 {
		return text
	}
	v, ok := decodeAssistantJSON(text)
	if !ok {
		return text
	}
	keep := make(map[string]bool, len(ids))
	for _, id := range ids {
		keep[id] = true
	}
	var filtered any
	switch t := v.(type) {
	case []any:
		filtered = keepAssistantItems(t, keep)
	case map[string]any:
		out := make(map[string]any, len(t))
		for key, val := range t {
			if list, ok := val.([]any); ok && assistantObjectList(list) {
				out[key] = keepAssistantItems(list, keep)
			} else {
				out[key] = val
			}
		}
		filtered = out
	}
	out, ok := encodeAssistantJSON(filtered)
	if !ok {
		return text
	}
	return out
}

func keepAssistantItems(list []any, keep map[string]bool) []any {
	out := make([]any, 0, len(keep))
	for _, item := range list {
		obj, ok := item.(map[string]any)
		if !ok {
			continue
		}
		switch id := obj["id"].(type) {
		case string:
			ok = keep[id]
		case json.Number:
			ok = keep[id.String()]
		default:
			ok = false
		}
		if ok {
			out = append(out, item)
		}
	}
	return out
}

func assistantObjectList(list []any) bool {
	if len(list) == 0 {
		return false
	}
	for _, item := range list {
		if _, ok := item.(map[string]any); !ok {
			return false
		}
	}
	return true
}

// fitAssistantToolResult keeps an oversized *_list result below the budget by
// returning only its leading items, each one complete, with a notice. Values
// are never truncated: the model copies listed definitions back into *_save
// calls (rules, skills and environments have no *_get), and a shortened
// string or node list would be written back as data loss. *_get and other
// results are returned unchanged for the same reason, as is any text that is
// not one JSON document.
func fitAssistantToolResult(tool, text string) string {
	if len(text) <= assistantToolResultBudget || !strings.HasSuffix(tool, "_list") {
		return text
	}
	v, ok := decodeAssistantJSON(text)
	if !ok {
		return text
	}
	// Indented JSON may fit once compacted, with nothing dropped.
	if compact, ok := encodeAssistantJSON(v); ok && len(compact) <= assistantToolResultBudget {
		return compact
	}
	if out, ok := keepLeadingAssistantItems(len(text), v); ok {
		return out
	}
	return text
}

// keepLeadingAssistantItems trims the top-level list, or the largest list
// wrapped in a top-level object, to the most leading items that fit.
func keepLeadingAssistantItems(original int, v any) (string, bool) {
	list, rebuild := []any(nil), func([]any) any { return nil }
	switch t := v.(type) {
	case []any:
		list, rebuild = t, func(items []any) any { return items }
	case map[string]any:
		largest := ""
		for key, val := range t {
			if items, ok := val.([]any); ok && (largest == "" || len(items) > len(list)) {
				largest, list = key, items
			}
		}
		if largest == "" {
			return "", false
		}
		rebuild = func(items []any) any {
			out := make(map[string]any, len(t))
			for key, val := range t {
				out[key] = val
			}
			out[largest] = items
			return out
		}
	default:
		return "", false
	}
	lo, hi, best := 0, len(list), ""
	for lo <= hi {
		mid := (lo + hi) / 2
		if out, ok := wrapFittedAssistantResult(original, rebuild(list[:mid]), mid, len(list)); ok {
			best, lo = out, mid+1
		} else {
			hi = mid - 1
		}
	}
	return best, best != ""
}

func wrapFittedAssistantResult(original int, v any, kept, total int) (string, bool) {
	notice := fmt.Sprintf("结果原长 %d 字节，超过单次返回上限 %d 字节，平台只返回了前 %d 条（共 %d 条），每条内容完整。未列出的条目不代表不存在：判断是否存在、检查引用或需要全部条目时，在 query 里传 fields（逗号分隔，如 fields=id,name,model）只取需要的字段后重新查询；需要某几条的完整定义时，在 query 里传 ids（逗号分隔的 id）只取这几条，有 *_get 工具的也可以按 id 读取。", original, assistantToolResultBudget, kept, total)
	out, ok := encodeAssistantJSON(map[string]any{"notice": notice, "result": v})
	if !ok || len(out) > assistantToolResultBudget {
		return "", false
	}
	return out, true
}
