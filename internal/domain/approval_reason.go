package domain

import (
	"strings"
)

// ApprovalWaitReason explains why an approval (人工门禁) node stops the run:
// the reviewer instructions configured on the node, which finished steps
// are to be checked, what continues after approval and where a rejection
// sends the run back. Mirrors approvalWaitReason in chatbot/src/runs.js.
func ApprovalWaitReason(run Run, nodeID string) string {
	wf := run.WorkflowSnapshot
	index := -1
	for i, node := range wf.Nodes {
		if node.ID == nodeID {
			index = i
			break
		}
	}
	if index < 0 {
		return ""
	}
	node := wf.Nodes[index]
	upstream, downstream := approvalNeighbors(wf, index)
	var b strings.Builder
	b.WriteString(approvalSOPLabel(wf.Name) + "在这一步设置了人工门禁")
	if len(upstream) > 0 {
		b.WriteString("：" + strings.Join(upstream, "、") + "已完成，需要有权限的成员检查结果后确认")
	} else {
		b.WriteString("，需要有权限的成员确认")
	}
	if len(downstream) > 0 {
		b.WriteString("，确认后继续" + strings.Join(downstream, "、"))
	} else {
		b.WriteString("，确认后运行结束")
	}
	if node.RejectTo != "" {
		b.WriteString("；不通过可打回到" + approvalNodeLabel(wf, node.RejectTo) + "重做")
	}
	b.WriteString("。")
	if instructions := strings.TrimSpace(node.TaskPrompt); instructions != "" {
		b.WriteString("确认要求：" + instructions)
	}
	return b.String()
}

// approvalNeighbors returns the labels of the steps feeding into and
// following wf.Nodes[index]. Workflows without edges run in list order.
func approvalNeighbors(wf WorkflowDefinition, index int) (upstream, downstream []string) {
	nodeID := wf.Nodes[index].ID
	for _, edge := range wf.Edges {
		if edge.Target == nodeID && edge.Source != "" {
			upstream = append(upstream, approvalNodeLabel(wf, edge.Source))
		}
		if edge.Source == nodeID && edge.Target != "" {
			downstream = append(downstream, approvalNodeLabel(wf, edge.Target))
		}
	}
	if len(wf.Edges) == 0 {
		if index > 0 {
			upstream = []string{approvalNodeLabel(wf, wf.Nodes[index-1].ID)}
		}
		if index+1 < len(wf.Nodes) {
			downstream = []string{approvalNodeLabel(wf, wf.Nodes[index+1].ID)}
		}
	}
	return upstream, downstream
}

// approvalNodeLabel quotes a node's display name, falling back to its ID when
// the node is unnamed or missing from the snapshot.
func approvalNodeLabel(wf WorkflowDefinition, id string) string {
	for _, n := range wf.Nodes {
		if n.ID == id {
			if strings.TrimSpace(n.Name) != "" {
				return "「" + n.Name + "」"
			}
			break
		}
	}
	return "「" + id + "」"
}

// approvalSOPLabel names the workflow in the reason, or "该 SOP" when unnamed.
func approvalSOPLabel(name string) string {
	if name = strings.TrimSpace(name); name != "" {
		return "SOP「" + name + "」"
	}
	return "该 SOP"
}
