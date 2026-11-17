package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// A designer edit is a list of operations on the canvas the turn started
// from, not a rewritten graph. The server applies them to the full canvas, so
// nodes, ids, prompts, scripts and layout the user did not name stay exactly
// as they were, and the canvas receives a complete workflows_save body.
//
//	{"tool":"workflows_save","body":{"operations":[
//	  {"op":"add_node","node":{"id":"review","name":"评审","type":"approval"}},
//	  {"op":"update_node","id":"impl","set":{"prompt":"…"}},
//	  {"op":"remove_node","id":"old"},
//	  {"op":"add_edge","source":"impl","target":"review"},
//	  {"op":"remove_edge","source":"a","target":"b"},
//	  {"op":"update_workflow","set":{"name":"…","description":"…"}}]}}
type sopDesignerOperation struct {
	Op     string         `json:"op"`
	ID     string         `json:"id"`
	Node   map[string]any `json:"node"`
	Set    map[string]any `json:"set"`
	Source string         `json:"source"`
	Target string         `json:"target"`
	When   string         `json:"when"`
}

const sopDesignerOperationLimit = 200

// expandSOPDesignerReply rewrites every workflows_save draft of a designer
// reply whose body carries operations into the full workflow they produce.
// Other drafts are left for the regular validation.
func expandSOPDesignerReply(raw, canvas json.RawMessage) (json.RawMessage, error) {
	var reply map[string]json.RawMessage
	if json.Unmarshal(raw, &reply) != nil || reply == nil {
		return raw, nil
	}
	var drafts []map[string]json.RawMessage
	if json.Unmarshal(reply["drafts"], &drafts) != nil || len(drafts) == 0 {
		return raw, nil
	}
	changed := false
	for i, draft := range drafts {
		var tool string
		_ = json.Unmarshal(draft["tool"], &tool)
		if strings.TrimSpace(tool) != "workflows_save" {
			continue
		}
		body := bytes.TrimSpace(draft["body"])
		if len(body) > 0 && body[0] == '"' {
			var encoded string
			if json.Unmarshal(body, &encoded) == nil {
				body = bytes.TrimSpace([]byte(encoded))
			}
		}
		var obj map[string]json.RawMessage
		if json.Unmarshal(body, &obj) != nil {
			continue
		}
		ops, ok := obj["operations"]
		if !ok {
			// A full body keeps the canvas's identity and sandbox unless the
			// model chose them, so a new graph is not rejected for a field
			// the user never sees.
			filled, ok := fillSOPDesignerIdentity(obj, canvas)
			if !ok {
				filled, _ = json.Marshal(obj)
			}
			if restored, ok := restoreSOPDesignerScripts(filled, canvas); ok {
				filled = restored
			}
			if ok || !bytes.Equal(filled, body) {
				drafts[i]["body"] = filled
				changed = true
			}
			continue
		}
		next, err := applySOPDesignerOperations(canvas, obj, ops)
		if err != nil {
			return nil, err
		}
		if restored, ok := restoreSOPDesignerScripts(next, canvas); ok {
			next = restored
		}
		drafts[i]["body"] = next
		changed = true
	}
	if !changed {
		return raw, nil
	}
	reply["drafts"], _ = json.Marshal(drafts)
	return json.Marshal(reply)
}

func applySOPDesignerOperations(canvas json.RawMessage, body map[string]json.RawMessage, rawOps json.RawMessage) (json.RawMessage, error) {
	if len(bytes.TrimSpace(canvas)) == 0 {
		return nil, fmt.Errorf("画布是空的，没有可修改的流程：请在 body 里提交完整的 name、description、nodes、edges，不要用 operations")
	}
	var ops []sopDesignerOperation
	if err := json.Unmarshal(rawOps, &ops); err != nil {
		return nil, fmt.Errorf("operations 必须是操作对象数组：%v", err)
	}
	if len(ops) == 0 {
		return nil, fmt.Errorf("operations 为空：没有要改的地方时直接用自然语言回答，不要提交草稿")
	}
	if len(ops) > sopDesignerOperationLimit {
		return nil, fmt.Errorf("一次最多 %d 个操作", sopDesignerOperationLimit)
	}
	var wf map[string]any
	if err := json.Unmarshal(canvas, &wf); err != nil || wf == nil {
		return nil, fmt.Errorf("画布数据无效")
	}
	nodes := sopDesignerObjects(wf["nodes"])
	edges := sopDesignerObjects(wf["edges"])
	indexOf := func(id string) int {
		for i, node := range nodes {
			if nodeID, _ := node["id"].(string); nodeID == id {
				return i
			}
		}
		return -1
	}
	edgeIndex := func(op sopDesignerOperation) int {
		for i, edge := range edges {
			id, _ := edge["id"].(string)
			source, _ := edge["source"].(string)
			target, _ := edge["target"].(string)
			if (op.ID != "" && id == op.ID) || (op.ID == "" && source == op.Source && target == op.Target) {
				return i
			}
		}
		return -1
	}
	for n, op := range ops {
		fail := func(format string, args ...any) error {
			return fmt.Errorf("第 %d 个操作（%s）：%s", n+1, op.Op, fmt.Sprintf(format, args...))
		}
		op.ID, op.Source, op.Target = strings.TrimSpace(op.ID), strings.TrimSpace(op.Source), strings.TrimSpace(op.Target)
		switch strings.TrimSpace(op.Op) {
		case "add_node":
			if op.Node == nil {
				return nil, fail("缺少 node")
			}
			id, _ := op.Node["id"].(string)
			id = strings.TrimSpace(id)
			if id == "" {
				return nil, fail("node.id 不能为空")
			}
			if indexOf(id) >= 0 {
				return nil, fail("节点 %s 已存在，修改已有节点用 update_node", id)
			}
			node := map[string]any{}
			for k, v := range op.Node {
				if k != "position" {
					node[k] = v
				}
			}
			node["id"] = id
			if t, _ := node["type"].(string); strings.TrimSpace(t) == "" {
				node["type"] = "agent"
			}
			nodes = append(nodes, node)
		case "update_node":
			at := indexOf(op.ID)
			if at < 0 {
				return nil, fail("节点 %s 不存在", op.ID)
			}
			if len(op.Set) == 0 {
				return nil, fail("set 为空")
			}
			for k, v := range op.Set {
				switch k {
				case "id":
					return nil, fail("不能修改节点 id；需要换 id 时先 remove_node 再 add_node")
				case "position":
					continue
				}
				if v == nil {
					delete(nodes[at], k)
				} else {
					nodes[at][k] = v
				}
			}
		case "remove_node":
			at := indexOf(op.ID)
			if at < 0 {
				return nil, fail("节点 %s 不存在", op.ID)
			}
			for _, node := range nodes {
				if reject, _ := node["rejectTo"].(string); reject == op.ID && node["id"] != op.ID {
					return nil, fail("节点 %v 的打回目标是 %s，先用 update_node 修改它的 rejectTo", node["id"], op.ID)
				}
			}
			nodes = append(nodes[:at], nodes[at+1:]...)
			kept := edges[:0]
			for _, edge := range edges {
				if edge["source"] != op.ID && edge["target"] != op.ID {
					kept = append(kept, edge)
				}
			}
			edges = kept
		case "add_edge":
			if indexOf(op.Source) < 0 || indexOf(op.Target) < 0 {
				return nil, fail("连线 %s → %s 指向不存在的节点", op.Source, op.Target)
			}
			if op.Source == op.Target {
				return nil, fail("节点不能连向自己")
			}
			if edgeIndex(sopDesignerOperation{Source: op.Source, Target: op.Target}) >= 0 {
				return nil, fail("连线 %s → %s 已存在", op.Source, op.Target)
			}
			when := strings.TrimSpace(op.When)
			if when == "" {
				when = "success"
			}
			edges = append(edges, map[string]any{"id": sopDesignerEdgeID(edges, op.Source, op.Target), "source": op.Source, "target": op.Target, "when": when})
		case "remove_edge":
			at := edgeIndex(op)
			if at < 0 {
				return nil, fail("连线 %s → %s 不存在", op.Source, op.Target)
			}
			edges = append(edges[:at], edges[at+1:]...)
		case "update_workflow":
			if len(op.Set) == 0 {
				return nil, fail("set 为空")
			}
			for k, v := range op.Set {
				switch k {
				case "id", "projectId", "nodes", "edges", "revision", "version", "status", "shared":
					return nil, fail("不能用 update_workflow 修改 %s", k)
				}
				if v == nil {
					delete(wf, k)
				} else {
					wf[k] = v
				}
			}
		default:
			return nil, fail("未知操作，可用 add_node、update_node、remove_node、add_edge、remove_edge、update_workflow")
		}
	}
	wf["nodes"], wf["edges"] = nodes, edges
	// A body may still name the workflow next to its operations.
	for _, key := range []string{"name", "description"} {
		if raw, ok := body[key]; ok {
			var value string
			if json.Unmarshal(raw, &value) == nil && strings.TrimSpace(value) != "" {
				wf[key] = value
			}
		}
	}
	return json.Marshal(wf)
}

func sopDesignerObjects(value any) []map[string]any {
	items, _ := value.([]any)
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if obj, ok := item.(map[string]any); ok {
			out = append(out, obj)
		}
	}
	return out
}

func sopDesignerEdgeID(edges []map[string]any, source, target string) string {
	base := "edge-" + source + "-" + target
	id := base
	for n := 2; ; n++ {
		taken := false
		for _, edge := range edges {
			if edge["id"] == id {
				taken = true
				break
			}
		}
		if !taken {
			return id
		}
		id = fmt.Sprintf("%s-%d", base, n)
	}
}

func fillSOPDesignerIdentity(body map[string]json.RawMessage, canvas json.RawMessage) (json.RawMessage, bool) {
	var base map[string]json.RawMessage
	if json.Unmarshal(canvas, &base) != nil || base == nil {
		return nil, false
	}
	changed := false
	for _, key := range []string{"id", "projectId", "sandboxProfileId"} {
		var current string
		if raw, ok := body[key]; ok && json.Unmarshal(raw, &current) == nil && strings.TrimSpace(current) != "" {
			continue
		}
		var value string
		if json.Unmarshal(base[key], &value) == nil && strings.TrimSpace(value) != "" {
			body[key] = base[key]
			changed = true
		}
	}
	if !changed {
		return nil, false
	}
	out, err := json.Marshal(body)
	return out, err == nil
}

// sopDesignerCanvasEmpty reports a canvas without nodes: the model then
// writes a whole workflow instead of operations.
func sopDesignerCanvasEmpty(canvas json.RawMessage) bool {
	var wf struct {
		Nodes []json.RawMessage `json:"nodes"`
	}
	return len(bytes.TrimSpace(canvas)) == 0 || json.Unmarshal(canvas, &wf) != nil || len(wf.Nodes) == 0
}

// restoreSOPDesignerScripts puts back a script the model copied as the
// "omitted" marker it was shown: that marker is not a script, the canvas
// node with the same id holds the real one.
func restoreSOPDesignerScripts(body, canvas json.RawMessage) (json.RawMessage, bool) {
	var wf map[string]any
	if json.Unmarshal(body, &wf) != nil {
		return nil, false
	}
	var base struct {
		Nodes []map[string]any `json:"nodes"`
	}
	_ = json.Unmarshal(canvas, &base)
	scripts := map[string]string{}
	for _, node := range base.Nodes {
		id, _ := node["id"].(string)
		script, _ := node["script"].(string)
		scripts[id] = script
	}
	changed := false
	for _, node := range sopDesignerObjects(wf["nodes"]) {
		script, _ := node["script"].(string)
		if !sopDesignerOmittedScript(script) {
			continue
		}
		id, _ := node["id"].(string)
		if original := scripts[id]; original != "" {
			node["script"] = original
		} else {
			delete(node, "script")
		}
		changed = true
	}
	if !changed {
		return nil, false
	}
	out, err := json.Marshal(wf)
	return out, err == nil
}

func sopDesignerOmittedScript(script string) bool {
	return strings.HasPrefix(script, "（脚本共 ") && strings.Contains(script, "已省略")
}
