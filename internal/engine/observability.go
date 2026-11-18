package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"agentflow/internal/domain"
)

func clarifyProcessLog(event *domain.TelemetryEvent) {
	if event == nil {
		return
	}
	if event.Category == "verification" {
		return
	}
	if event.Category != "process" || event.Name != "process.finished" {
		return
	}
	if event.Level == "error" {
		event.Message = "执行器失败：" + event.Message
		return
	}
	if event.Message == "process exited successfully" {
		event.Message = "执行器退出 0"
	}
}

func applyUsage(run *domain.Run, nr *domain.NodeRun, sessionID string, snap domain.TokenUsage) {
	if run == nil || nr == nil {
		return
	}
	if sessionID == "" {
		sessionID = nr.SessionID
	}
	if sessionID != "" && nr.UsageSessionID != sessionID {
		nr.UsageSessionID = sessionID
		nr.UsageCursor = domain.TokenUsage{}
	}
	delta := subUsage(snap, nr.UsageCursor)
	// 成本识别状态与 token 数值变化解耦：跟随最近一次带状态的快照，
	// 让"真实零成本"与"成本未识别"在 run 级汇总上也可区分。
	if snap.CostStatus != "" {
		run.Usage.CostStatus = snap.CostStatus
		nr.Usage.CostStatus = snap.CostStatus
	}
	if !usageHasValues(delta) {
		nr.UsageCursor = snap
		return
	}
	addUsage(&run.Usage, delta)
	addUsage(&nr.Usage, delta)
	nr.UsageCursor = snap
	run.Usage.TotalTokens = billedTokens(run.Usage)
	nr.Usage.TotalTokens = billedTokens(nr.Usage)
}

func billedTokens(usage domain.TokenUsage) int64 {
	return usage.InputTokens + usage.OutputTokens
}

func usageHasValues(usage domain.TokenUsage) bool {
	return usage.InputTokens > 0 || usage.OutputTokens > 0 || usage.CacheReadTokens > 0 || usage.CacheWriteTokens > 0 || usage.ReasoningTokens > 0 || usage.CostUSD != 0
}

func addUsage(dst *domain.TokenUsage, src domain.TokenUsage) {
	dst.InputTokens += src.InputTokens
	dst.OutputTokens += src.OutputTokens
	dst.CacheReadTokens += src.CacheReadTokens
	dst.CacheWriteTokens += src.CacheWriteTokens
	dst.ReasoningTokens += src.ReasoningTokens
	dst.CostUSD += src.CostUSD
}

func subUsage(cur, prev domain.TokenUsage) domain.TokenUsage {
	return domain.TokenUsage{
		InputTokens:      clampNonNeg(cur.InputTokens - prev.InputTokens),
		OutputTokens:     clampNonNeg(cur.OutputTokens - prev.OutputTokens),
		CacheReadTokens:  clampNonNeg(cur.CacheReadTokens - prev.CacheReadTokens),
		CacheWriteTokens: clampNonNeg(cur.CacheWriteTokens - prev.CacheWriteTokens),
		ReasoningTokens:  clampNonNeg(cur.ReasoningTokens - prev.ReasoningTokens),
		CostUSD:          cur.CostUSD - prev.CostUSD,
	}
}

func clampNonNeg(v int64) int64 {
	if v < 0 {
		return 0
	}
	return v
}

func shouldSelfCorrect(onFailure string) bool {
	return resolveGateOnFailure(onFailure, "") == "loop"
}

// loopRetroSignals counts one node's telemetry for the loop retro.
type loopRetroSignals struct {
	completed, missingResult, gatePass, gateFail, loopStarts, toolFinishes int
	// loopIssues holds one "- Loop：" line per agent.loop event, in order.
	loopIssues []string
}

func collectLoopRetroSignals(run domain.Run, nodeID string) loopRetroSignals {
	var s loopRetroSignals
	for _, event := range run.Telemetry {
		if event.NodeID != nodeID {
			continue
		}
		switch {
		case event.Name == "agent.completed":
			s.completed++
		case event.Category == "process" && event.Name == "process.finished" && event.Source.Layer == "executor" && !strings.Contains(event.Message, "失败"):
			if s.completed == 0 {
				s.missingResult++
			}
		case event.Category == "verification" && event.Name == "process.finished" && event.Level != "error":
			s.gatePass++
		case event.Category == "verification" && event.Name == "process.finished" && event.Level == "error":
			s.gateFail++
		case event.Name == "agent.loop":
			s.loopStarts++
			s.loopIssues = append(s.loopIssues, "- Loop："+event.Message)
		case event.Category == "tool" && event.Name == "tool.call" && event.Phase == "finish":
			s.toolFinishes++
		}
	}
	return s
}

func buildLoopRetro(run domain.Run, nodeID string, iterations int, lastErr error, onFailure string) string {
	lang := runNodeLanguage(&run, nodeID)
	var b strings.Builder
	switch lang {
	case "ko":
		fmt.Fprintf(&b, "# Loop 복기 — `%s`\n\n", nodeID)
		fmt.Fprintf(&b, "- 자체 수정 라운드: %d\n", iterations)
		if lastErr != nil {
			fmt.Fprintf(&b, "- 최종 결과: 미통과\n- 마지막 오류: `%s`\n\n", strings.ReplaceAll(lastErr.Error(), "\n", " "))
		} else {
			b.WriteString("- 최종 결과: 통과\n\n")
		}
		signals := collectLoopRetroSignals(run, nodeID)
		issues := signals.loopIssues
		if signals.missingResult > 0 {
			if lastErr != nil && strings.Contains(lastErr.Error(), "without a terminal result") {
				issues = append(issues, "- 실행기가 종료 코드 0으로 끝났으나 stream-json에 `type:result` / `agent.completed`가 없음 (프로토콜 중단이며 게이트 실패가 아니며, max-turns로 바로 간주하지 마십시오)")
			} else {
				issues = append(issues, "- 실행기가 종료 코드 0으로 끝났으나 `agent.completed`가 없음 (max-turns 도달 또는 프로토콜 중단 가능성)")
			}
		}
		if lastErr != nil && onFailure == "reject" {
			issues = append(issues, "- 게이트 onFailure=reject 설정: 상위 노드로 반려하며 본 노드에서 자체 수정하지 않음")
		} else if lastErr != nil && onFailure == "fail" {
			issues = append(issues, "- 게이트 onFailure=fail 설정: 본 노드 실패 처리")
		} else if lastErr != nil && onFailure == "human" {
			issues = append(issues, "- 게이트 onFailure=human 설정: 수동 승인으로 전환")
		}
		if lastErr != nil && strings.Contains(lastErr.Error(), "without a terminal result") {
			issues = append(issues, "- 샌드박스 내에 보고서가 작성되었을 수 있으나, 산출물 등록 전 노드가 실패로 판정되어 화면에 loop-retro만 남았을 수 있음")
		} else if lastErr != nil && strings.Contains(lastErr.Error(), "No such file") {
			issues = append(issues, "- 산출물이 생성되지 않았거나 세션이 조기 종료됨")
		}
		fmt.Fprintf(&b, "## 신호\n\n- agent.completed: %d\n- 게이트 통과 / 실패: %d / %d\n- 도구 호출(finish): %d\n- 자체 수정 진입: %d\n\n", signals.completed, signals.gatePass, signals.gateFail, signals.toolFinishes, signals.loopStarts)
		b.WriteString("## 문제\n\n")
		if len(issues) == 0 {
			b.WriteString("- 본 노드에서 Loop 수정이 필요한 이상 징후가 발견되지 않았습니다.\n")
		} else {
			b.WriteString(strings.Join(issues, "\n") + "\n")
		}
		b.WriteString("\n## 제안\n\n")
		switch {
		case lastErr != nil && onFailure == "reject":
			b.WriteString("- 설정에 따라 상위로 반려하며, 자체 수정을 추가로 진행하지 마십시오.\n")
		case lastErr != nil && onFailure == "fail":
			b.WriteString("- 설정에 따라 본 노드를 실패로 종료합니다.\n")
		case lastErr != nil && onFailure == "human":
			b.WriteString("- 설정에 따라 수동 확인으로 전환합니다.\n")
		case lastErr != nil && onFailure == "loop":
			b.WriteString("- 자체 수정 라운드가 모두 소진되었습니다. 즉시 반려 또는 실패 처리가 필요하면 게이트의 실패 전략을 변경하십시오.\n")
		case lastErr != nil && strings.Contains(lastErr.Error(), "without a terminal result"):
			b.WriteString("- `type:result` 누락 시 노드 전체를 실패로 반려하기 전에 산출물을 게이트에서 대조하도록 구성하십시오.\n")
		default:
			b.WriteString("- 실패 전략은 게이트의 onFailure(loop / reject / fail / human) 설정을 따릅니다.\n")
		}
	case "en":
		fmt.Fprintf(&b, "# Loop Retrospective — `%s`\n\n", nodeID)
		fmt.Fprintf(&b, "- Self-correction rounds: %d\n", iterations)
		if lastErr != nil {
			fmt.Fprintf(&b, "- Final result: Failed\n- Last error: `%s`\n\n", strings.ReplaceAll(lastErr.Error(), "\n", " "))
		} else {
			b.WriteString("- Final result: Passed\n\n")
		}
		signals := collectLoopRetroSignals(run, nodeID)
		issues := signals.loopIssues
		if signals.missingResult > 0 {
			if lastErr != nil && strings.Contains(lastErr.Error(), "without a terminal result") {
				issues = append(issues, "- Executor exited with 0, but stream-json lacked `type:result` / `agent.completed` (protocol interruption, not gate failure)")
			} else {
				issues = append(issues, "- Executor exited with 0, but lacked `agent.completed` (possible max-turns hit or protocol interruption)")
			}
		}
		if lastErr != nil && onFailure == "reject" {
			issues = append(issues, "- Configured onFailure=reject: rejected to upstream, no self-correction on this node")
		} else if lastErr != nil && onFailure == "fail" {
			issues = append(issues, "- Configured onFailure=fail: this node failed")
		} else if lastErr != nil && onFailure == "human" {
			issues = append(issues, "- Configured onFailure=human: transferred to human approval")
		}
		if lastErr != nil && strings.Contains(lastErr.Error(), "without a terminal result") {
			issues = append(issues, "- Report may have been written in sandbox, but node failed before registering deliverables")
		} else if lastErr != nil && strings.Contains(lastErr.Error(), "No such file") {
			issues = append(issues, "- Deliverables not produced or session ended prematurely")
		}
		fmt.Fprintf(&b, "## Signals\n\n- agent.completed: %d\n- Gate pass / fail: %d / %d\n- Tool calls (finish): %d\n- Entered self-correction: %d\n\n", signals.completed, signals.gatePass, signals.gateFail, signals.toolFinishes, signals.loopStarts)
		b.WriteString("## Issues\n\n")
		if len(issues) == 0 {
			b.WriteString("- No loop anomalies detected on this node.\n")
		} else {
			b.WriteString(strings.Join(issues, "\n") + "\n")
		}
		b.WriteString("\n## Recommendations\n\n")
		switch {
		case lastErr != nil && onFailure == "reject":
			b.WriteString("- Send back to upstream according to configuration without additional self-correction.\n")
		case lastErr != nil && onFailure == "fail":
			b.WriteString("- Conclude this node as failed according to configuration.\n")
		case lastErr != nil && onFailure == "human":
			b.WriteString("- Transfer to human confirmation according to configuration.\n")
		case lastErr != nil && onFailure == "loop":
			b.WriteString("- Self-correction rounds exhausted. Change the gate's failure strategy if direct reject or fail is desired.\n")
		case lastErr != nil && strings.Contains(lastErr.Error(), "without a terminal result"):
			b.WriteString("- When `type:result` is missing, verify deliverables via gates instead of failing the whole chain.\n")
		default:
			b.WriteString("- Failure strategy follows gate onFailure configuration (loop / reject / fail / human).\n")
		}
	default:
		fmt.Fprintf(&b, "# Loop 复盘 — `%s`\n\n", nodeID)
		fmt.Fprintf(&b, "- 自修正轮次：%d\n", iterations)
		if lastErr != nil {
			fmt.Fprintf(&b, "- 最终结果：未通过\n- 最后错误：`%s`\n\n", strings.ReplaceAll(lastErr.Error(), "\n", " "))
		} else {
			b.WriteString("- 最终结果：通过\n\n")
		}
		signals := collectLoopRetroSignals(run, nodeID)
		issues := signals.loopIssues
		if signals.missingResult > 0 {
			if lastErr != nil && strings.Contains(lastErr.Error(), "without a terminal result") {
				issues = append(issues, "- 执行器退出 0，但 stream-json 没有 `type:result` / `agent.completed`（协议中断，不是门禁失败，也不要先当成 max-turns）")
			} else {
				issues = append(issues, "- 执行器有退出 0 但没有 `agent.completed`（可能撞 max-turns / 协议中断）")
			}
		}
		if lastErr != nil && onFailure == "reject" {
			issues = append(issues, "- 按门禁 onFailure=reject：打回上游，不在本节点自修正")
		} else if lastErr != nil && onFailure == "fail" {
			issues = append(issues, "- 按门禁 onFailure=fail：本节点失败")
		} else if lastErr != nil && onFailure == "human" {
			issues = append(issues, "- 按门禁 onFailure=human：转人工确认")
		}
		if lastErr != nil && strings.Contains(lastErr.Error(), "without a terminal result") {
			issues = append(issues, "- 沙箱里可能已经写了报告，但节点在登记交付物前就被判失败，所以页面上往往只剩 loop-retro")
		} else if lastErr != nil && strings.Contains(lastErr.Error(), "No such file") {
			issues = append(issues, "- 产物未落地或会话提前结束")
		}
		fmt.Fprintf(&b, "## 信号\n\n- agent.completed：%d\n- 门禁通过 / 失败：%d / %d\n- 工具调用（finish）：%d\n- 进入自修正：%d\n\n", signals.completed, signals.gatePass, signals.gateFail, signals.toolFinishes, signals.loopStarts)
		b.WriteString("## 问题\n\n")
		if len(issues) == 0 {
			b.WriteString("- 本节点未发现需要改 Loop 的异常。\n")
		} else {
			b.WriteString(strings.Join(issues, "\n") + "\n")
		}
		b.WriteString("\n## 建议\n\n")
		switch {
		case lastErr != nil && onFailure == "reject":
			b.WriteString("- 按页面配置打回上游，不要再开自修正。\n")
		case lastErr != nil && onFailure == "fail":
			b.WriteString("- 按页面配置结束本节点为失败。\n")
		case lastErr != nil && onFailure == "human":
			b.WriteString("- 按页面配置转人工确认。\n")
		case lastErr != nil && onFailure == "loop":
			b.WriteString("- 自修正轮次已用尽。若应直接打回或失败，到页面改这条门禁的失败策略。\n")
		case lastErr != nil && strings.Contains(lastErr.Error(), "without a terminal result"):
			b.WriteString("- 缺 `type:result` 时应交给门禁核对产物，而不是按节点失败整链打回；工具次数远低于 `--max-turns` 时不要写成撞轮次。\n")
		default:
			b.WriteString("- 失败策略只认门禁 onFailure（loop / reject / fail / human），在 Studio 里改，保存即写库。\n")
		}
	}
	return b.String()
}

func (e *Engine) attachLoopRetro(runID, nodeID, workspace string, iterations int, lastErr error, onFailure string) {
	if lastErr == nil && iterations <= 1 {
		return
	}
	live, ok := e.repo.Run(runID)
	if !ok {
		return
	}
	attempt := 0
	if nr := live.Nodes[nodeID]; nr != nil {
		attempt = nr.Attempt
	}
	body := []byte(buildLoopRetro(live, nodeID, iterations, lastErr, onFailure))
	rel := filepath.ToSlash(filepath.Join(".agentflow", "loop-retro-"+nodeID+".md"))
	if workspace != "" {
		_ = os.MkdirAll(filepath.Join(workspace, ".agentflow"), 0o755)
		_ = os.WriteFile(filepath.Join(workspace, rel), body, 0o644)
	}
	e.updateRun(runID, func(r *domain.Run) {
		appendArtifact(r, nodeID, attempt, "loop-retro", rel, "text/markdown", body)
	})
	e.observe(runID, nodeID, domain.TelemetryEvent{Category: "artifact", Name: "artifact.published", Phase: "finish", Message: "loop-retro", Attributes: map[string]any{"path": rel, "size": len(body)}})
}
