package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"agentflow/internal/domain"
	"agentflow/internal/gitwork"
)

const squadDecisionPrefix = "LOOPFORGE_SQUAD "

// StartSquad admits an ad hoc project team through the normal run scheduler.
// Only the opening turn exists at admission. Every successor is decided by
// the active role and committed in the same transaction as its result.
func (e *Engine) StartSquad(ctx context.Context, req domain.StartSquadRequest, goal string, repos []domain.RepositoryContext, ec domain.TelemetryContext) (domain.Run, error) {
	if ec.ProjectID == "" || strings.TrimSpace(goal) == "" {
		return domain.Run{}, fmt.Errorf("请选择项目并填写小队目标")
	}
	if ec.SharedScheduleID != "" {
		return domain.Run{}, fmt.Errorf("共享定时任务仅可运行已授权的 LOOP")
	}
	if len(req.MemberIDs) < 2 || len(req.MemberIDs) > 12 {
		return domain.Run{}, fmt.Errorf("小队需要 2 至 12 个不同的角色")
	}
	if req.MaxTurns == 0 {
		req.MaxTurns = 6
	}
	if req.MaxTurns < 1 || req.MaxTurns > 16 {
		return domain.Run{}, fmt.Errorf("小队轮次上限须为 1 至 16")
	}
	squad := &domain.AgentSquad{MaxTurns: req.MaxTurns}
	seen := make(map[string]bool)
	for _, id := range req.MemberIDs {
		id = strings.TrimSpace(id)
		role, ok := e.repo.Agent(id)
		if !ok || !squadRoleAvailable(role, ec.ProjectID) {
			return domain.Run{}, fmt.Errorf("角色 %q 在当前项目不可用", id)
		}
		if seen[role.ID] {
			return domain.Run{}, fmt.Errorf("重复的小队成员 %q", role.ID)
		}
		seen[role.ID] = true
		squad.Members = append(squad.Members, domain.SquadMember{ID: role.ID, Name: role.Name, Description: role.Description})
	}
	wf := domain.WorkflowDefinition{
		ID: domain.AgentSquadWorkflowID, Name: "Agent 协作小队", ProjectID: ec.ProjectID, Version: 1, Squad: squad,
		SandboxProfileID: strings.TrimSpace(req.SandboxProfileID),
		Nodes:            []domain.WorkflowNode{squadTurn(1, squad.Members[0])},
	}
	wf.AutoSelectSandbox = wf.SandboxProfileID == ""
	if err := e.repo.ValidateWorkflowSharing(wf); err != nil {
		return domain.Run{}, err
	}
	return e.startWorkflowDefinition(ctx, wf, goal, repos, ec, false)
}

func squadRoleAvailable(role domain.AgentDefinition, projectID string) bool {
	return role.ProjectID != "" && (role.ProjectID == projectID || role.Shared) && !domain.IsPreviewBuildAgent(role.ID)
}

func squadTurn(turn int, member domain.SquadMember) domain.WorkflowNode {
	// A replay can choose a different successor at the same ordinal. Keep its
	// logs, artifacts and CLI control files distinct from the superseded turn.
	return domain.WorkflowNode{
		ID: fmt.Sprintf("turn-%d-%d", turn, time.Now().UnixNano()), Type: "agent", AgentID: member.ID,
		Name: fmt.Sprintf("%d · %s", turn, member.Name), MaxAttempts: 1,
		Position: domain.Position{X: float64(60 + (turn-1)*260), Y: 100},
	}
}

func squadTurnPrompt(run domain.Run, node domain.WorkflowNode) string {
	wf := run.WorkflowSnapshot
	if wf.Squad == nil {
		return ""
	}
	var b strings.Builder
	members, _ := json.Marshal(wf.Squad.Members)
	fmt.Fprintf(&b, "\n\n## 小队交接\n你是 %s，当前为第 %d/%d 轮。成员目录为 %s。\n", node.AgentID, len(wf.Nodes), wf.Squad.MaxTurns, members)
	for _, prev := range wf.Nodes {
		if prev.ID == node.ID {
			break
		}
		if nr := run.Nodes[prev.ID]; nr != nil && nr.SquadDecision != nil {
			fmt.Fprintf(&b, "来自 %s 的交接记录：%s\n", prev.AgentID, nr.SquadDecision.Summary)
		}
	}
	b.WriteString("围绕本次运行输入实际完成本角色的工作，按需要把下一项具体任务交给目录中的其他成员。可以重新交给之前出场的成员。目标已达成时直接结束，有真实阻碍时报告阻碍。保留正常的交付内容与门禁操作。")
	b.WriteString("最终答复的最后一行单独输出以下一种控制记录，使用单行 JSON，不加代码围栏。summary 写清本轮证据和下一项任务，最多 1600 字。\n")
	b.WriteString("LOOPFORGE_SQUAD {\"action\":\"handoff\",\"to\":\"成员ID\",\"summary\":\"交接说明\"}\nLOOPFORGE_SQUAD {\"action\":\"finish\",\"summary\":\"交付结果\"}\nLOOPFORGE_SQUAD {\"action\":\"blocked\",\"summary\":\"阻碍与所需信息\"}")
	return b.String()
}

func parseSquadDecision(output string) (domain.SquadDecision, error) {
	var d domain.SquadDecision
	output = strings.TrimSpace(output)
	line := output[strings.LastIndexByte(output, '\n')+1:]
	raw, ok := strings.CutPrefix(line, squadDecisionPrefix)
	if !ok || json.Unmarshal([]byte(raw), &d) != nil {
		return d, fmt.Errorf("最终答复末行缺少有效的小队交接记录")
	}
	d.Summary, d.To = strings.TrimSpace(d.Summary), strings.TrimSpace(d.To)
	if d.Summary == "" || utf8.RuneCountInString(d.Summary) > 1600 {
		return d, fmt.Errorf("小队交接说明须为 1 至 1600 字")
	}
	d.Summary = gitwork.Redact(d.Summary)
	return d, nil
}

// Called inside the fenced node outcome commit, including when an otherwise
// successful role is waiting on its manual gate. The edge prevents the next
// turn from executing until the gate actually succeeds.
func (e *Engine) commitSquadDecision(r *domain.Run, node domain.WorkflowNode, output string) error {
	wf := &r.WorkflowSnapshot
	if wf.Squad == nil {
		return nil
	}
	d, err := parseSquadDecision(output)
	if err != nil {
		return err
	}
	switch d.Action {
	case "finish", "blocked":
		if d.To != "" {
			return fmt.Errorf("结束或阻塞的小队决策不能指定下一位成员")
		}
		r.Nodes[node.ID].SquadDecision = &d
		if d.Action == "blocked" {
			return fmt.Errorf("小队受阻：%s", d.Summary)
		}
		e.event(r, node.ID, "squad.finish_requested", d.Summary)
	case "handoff":
		if len(wf.Nodes) >= wf.Squad.MaxTurns {
			// Keep the requested handoff so the run shows why the turn failed.
			r.Nodes[node.ID].SquadDecision = &d
			return fmt.Errorf("小队已达 %d 轮上限，仍请求交给 %q：%s", wf.Squad.MaxTurns, d.To, d.Summary)
		}
		for _, member := range wf.Squad.Members {
			if member.ID == d.To && member.ID != node.AgentID {
				next := squadTurn(len(wf.Nodes)+1, member)
				wf.Nodes = append(wf.Nodes, next)
				wf.Edges = append(wf.Edges, domain.WorkflowEdge{ID: next.ID, Source: node.ID, Target: next.ID, When: "success"})
				r.Nodes[next.ID] = &domain.NodeRun{NodeID: next.ID, Status: "pending"}
				r.Nodes[node.ID].SquadDecision = &d
				e.event(r, node.ID, "squad.handoff", node.Name+" → "+member.Name+"："+d.Summary)
				return nil
			}
		}
		return fmt.Errorf("小队交接目标 %q 必须是其他已选成员", d.To)
	default:
		return fmt.Errorf("未知的小队决策 %q", d.Action)
	}
	return nil
}

// A replay keeps the prefix, then lets the retried member choose a fresh
// route. Logs and artifacts of the old route retain their original turn IDs.
func rewindSquad(r *domain.Run, target string) {
	wf := &r.WorkflowSnapshot
	if wf.Squad == nil {
		return
	}
	for i, node := range wf.Nodes {
		if node.ID != target {
			continue
		}
		r.Nodes[target].SquadDecision = nil
		for _, later := range wf.Nodes[i+1:] {
			r.Nodes[later.ID] = nil // delete the active node row in the same transaction
		}
		wf.Nodes = wf.Nodes[:i+1]
		wf.Edges = wf.Edges[:i]
		return
	}
}
