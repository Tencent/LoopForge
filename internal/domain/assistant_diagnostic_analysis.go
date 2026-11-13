package domain

import (
	"fmt"
	"strings"
)

// AnalyzeAssistantDiagnostics is deterministic and works on an exported file
// without database access. It separates terminal facts from unknown causes.
func AnalyzeAssistantDiagnostics(d *AssistantDiagnostics) {
	if d == nil || d.Version < 2 {
		return
	}
	a := &AssistantDiagnosticAssessment{Outcome: "undetermined", Summary: "现有证据不足以确认失败原因"}
	d.Assessment = a
	d.Coverage = []string{
		"采集平台生命周期、CLI 进程状态、原生错误元数据及工具调用状态；内容为脱敏后的有限片段。",
		"未采集提示词、思考正文、工具参数、成功工具输出、完整 stdout、环境变量或凭证。",
		"CLI 未暴露的内部网络等待、死锁或上游账号状态无法从本文件确认。",
	}
	requestIDs := map[string]bool{}
	traceIDs := map[string]bool{}
	toolFailures, pending := 0, 0
	firstFailedTool := ""
	for i := range d.Tools {
		t := &d.Tools[i]
		if t.Status == "failed" {
			toolFailures++
			if firstFailedTool == "" {
				firstFailedTool = fmt.Sprintf("%s（来源：%s）", t.Name, t.Source)
			}
		}
		if t.Status == "running" || t.Status == "unfinished" {
			if d.Status != "running" {
				t.Status = "unfinished"
			}
			pending++
			a.Evidence = append(a.Evidence, fmt.Sprintf("工具 %s（%s，来源：%s）有开始记录，未收到结束记录", t.Name, t.ID, t.Source))
		}
		if t.RequestID != "" {
			requestIDs[t.RequestID] = true
		}
		if t.TraceID != "" {
			traceIDs[t.TraceID] = true
		}
	}
	if toolFailures > 0 {
		a.Evidence = append(a.Evidence, fmt.Sprintf("记录到 %d 次工具失败（首个：%s）；工具错误本身不直接等同于整轮生成失败", toolFailures, firstFailedTool))
	}
	hasEarlyError := false
	for i := range d.Attempts {
		attempt := &d.Attempts[i]
		if attempt.CLIVersion == "" {
			a.Unknowns = appendUniqueDiagnostic(a.Unknowns, "CLI 未提供版本，安装元数据也未提供版本。")
		}
		if attempt.ExitCode != nil {
			a.Evidence = append(a.Evidence, fmt.Sprintf("调用 %s 的进程退出码为 %d，协议结果为 %s", attempt.ID, *attempt.ExitCode, attempt.Status))
		}
		if attempt.FirstError != nil {
			hasEarlyError = true
			desc := attempt.FirstError.Kind
			if attempt.FirstError.StatusCode > 0 {
				desc = fmt.Sprintf("%s (HTTP %d)", desc, attempt.FirstError.StatusCode)
			}
			if attempt.FirstError.RequestID != "" {
				requestIDs[attempt.FirstError.RequestID] = true
			}
			if attempt.FirstError.TraceID != "" {
				traceIDs[attempt.FirstError.TraceID] = true
			}
			a.Evidence = append(a.Evidence, fmt.Sprintf("调用 %s 首次捕获异常事件：%s", attempt.ID, desc))
		}
		if p := attempt.Process; p != nil {
			if p.State == "start_failed" {
				a.Evidence = append(a.Evidence, fmt.Sprintf("调用 %s 的 CLI 进程启动失败", attempt.ID))
			}
			if p.Signal != "" {
				a.Evidence = append(a.Evidence, "调用 "+attempt.ID+" 收到进程信号 "+p.Signal)
				a.Unknowns = appendUniqueDiagnostic(a.Unknowns, "进程信号不能单独证明 OOM、管理员操作或宿主机故障。")
			}
			if (p.State == "running" || p.State == "unknown_after_interruption") && d.Status != "running" {
				p.State = "unknown_after_interruption"
				a.Evidence = append(a.Evidence, fmt.Sprintf("调用 %s 在平台记录结束前仍处于运行态，未收到进程退出事件", attempt.ID))
			}
			if (d.Status == "running" || d.Status == "cancelled" || d.Status == "interrupted") && p.State != "start_failed" && p.StdoutLines == 0 && p.StderrLines == 0 {
				a.Evidence = append(a.Evidence, fmt.Sprintf("调用 %s 的 CLI 进程已启动（PID %d），但尚未产生任何 stdout/stderr 输出", attempt.ID, p.PID))
			}
		}
		for _, e := range attempt.Events {
			if e.RequestID != "" {
				requestIDs[e.RequestID] = true
			}
			if e.TraceID != "" {
				traceIDs[e.TraceID] = true
			}
		}
	}
	if d.Termination != nil {
		switch d.Termination.Code {
		case "USER_CANCELLED":
			a.Outcome = "cancelled_by_user"
			if hasEarlyError {
				a.Evidence = append(a.Evidence, "用户停止前已捕获到 CLI 或上游错误（详见 firstError），可能是导致等待过久的原因")
			}
		case "USER_STEER":
			a.Outcome = "redirected_by_user"
		default:
			a.Outcome = "termination_recorded"
		}
		a.Summary = d.Termination.Message
		a.Evidence = append(a.Evidence, "平台记录的结束原因："+d.Termination.Code)
		if d.Termination.Code == "CHECKPOINT_LOST" {
			if d.LastCheckpointAt != nil {
				a.Evidence = append(a.Evidence, "最后一次运行实例心跳时间："+d.LastCheckpointAt.UTC().Format("2006-01-02T15:04:05Z"))
			}
			a.Unknowns = appendUniqueDiagnostic(a.Unknowns, "只确认平台未继续保存执行状态；无法仅凭此记录区分进程崩溃、服务重启、OOM 或数据库写入中断。")
			a.NextSteps = appendUniqueDiagnostic(a.NextSteps, "按 environment.instanceId、generationId 和 lastCheckpointAt 查询平台与宿主机日志。")
		}
	} else {
		switch d.Status {
		case "completed":
			a.Outcome, a.Summary = "completed", "本轮已完成；CLI 警告和已恢复的工具错误仍保留供排查"
		case "running":
			a.Outcome, a.Summary = "pending", "本轮尚未结束，当前文件是执行现场快照"
			if pending == 0 {
				a.Unknowns = appendUniqueDiagnostic(a.Unknowns, "未观察到正在执行的工具；无法仅凭没有输出区分模型推理等待、网络等待或 CLI 内部阻塞。")
			}
		case "failed":
			if d.Failure != nil {
				a.Summary = d.Failure.Message
				a.Evidence = append(a.Evidence, "失败阶段："+d.Failure.Phase+"；分类："+d.Failure.Code)
				if d.Failure.Code != "GENERATION_FAILED" && d.Failure.Code != "CLI_EXIT_NONZERO" && d.Failure.Code != "MISSING_RESULT" {
					a.Outcome = "failure_observed"
				}
				switch d.Failure.Code {
				case "AUTHENTICATION_FAILED", "PERMISSION_DENIED", "RATE_LIMITED", "QUOTA_EXCEEDED", "UPSTREAM_UNAVAILABLE":
					a.Unknowns = appendUniqueDiagnostic(a.Unknowns, "上游拒绝或服务异常已被观察到；账号配置、限流规则及服务内部原因需要上游记录确认。")
				case "NETWORK_ERROR":
					a.Unknowns = appendUniqueDiagnostic(a.Unknowns, "连接失败的具体网络节点或代理原因未被 CLI 暴露。")
				case "CLI_EXIT_NONZERO", "MISSING_RESULT", "GENERATION_FAILED":
					a.Unknowns = appendUniqueDiagnostic(a.Unknowns, "当前错误能确认执行失败，但不足以单独判定 CLI 内部根因。")
				}
			}
		case "interrupted", "cancelled":
			a.Unknowns = appendUniqueDiagnostic(a.Unknowns, "未记录明确的终止发起方；不能认定为用户主动停止。")
		}
	}
	if len(requestIDs) > 0 {
		a.NextSteps = appendUniqueDiagnostic(a.NextSteps, "记录中包含真实 requestId，可据此关联上游模型服务或网关日志。")
	}
	if len(traceIDs) > 0 {
		a.NextSteps = appendUniqueDiagnostic(a.NextSteps, "记录中包含真实 traceId，可据此关联分布式链路日志。")
	}
	if d.Status != "completed" && d.Environment != nil && d.Environment.InstanceID != "" {
		a.NextSteps = appendUniqueDiagnostic(a.NextSteps, fmt.Sprintf("可在平台服务端日志检索 diagnostic=%s 与 instance=%s 查看完整后台记录。", d.GenerationID, d.Environment.InstanceID))
	}
	if d.Truncated {
		a.Unknowns = appendUniqueDiagnostic(a.Unknowns, "部分证据已按上限截断；首次错误、起始片段及最近记录已分别保留，截断计数见 dropped。")
	}
	if len(a.Unknowns) > 0 && d.Status != "completed" {
		a.NextSteps = appendUniqueDiagnostic(a.NextSteps, "优先检查 firstError、失败/未完成工具（tools）、termination 和 timeline；无法确认的部分以 unknowns 为准。")
	}
}

func appendUniqueDiagnostic(items []string, value string) []string {
	for _, item := range items {
		if strings.EqualFold(item, value) {
			return items
		}
	}
	return append(items, value)
}
