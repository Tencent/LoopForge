package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"agentflow/internal/domain"
)

const (
	gateResultArtifactName = "__agentflow_gate_result"
	gateMissingRetryMarker = "__agentflow_missing_gate_result__"
)

type verificationRun struct {
	gate      domain.VerificationGate
	onFailure string
}

func gateResultPath(nodeID string, attempt int) string {
	return filepath.ToSlash(filepath.Join(".agentflow", "gate-results", safeRunnerPath(nodeID), fmt.Sprintf("attempt-%d.json", attempt)))
}

func gateResultContract(path string) domain.ArtifactContract {
	return domain.ArtifactContract{Name: gateResultArtifactName, Path: path, MediaType: "application/json", Required: false}
}

func clearGateResult(workspace, path string) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	return os.RemoveAll(filepath.Join(workspace, filepath.FromSlash(path)))
}

func readGateResult(workspace, path string) (*domain.GateResult, error) {
	return readGateResultFrom(workspaceOutputs(workspace), path)
}

func readGateResultFrom(outputs nodeOutputs, path string) (*domain.GateResult, error) {
	raw, err := outputs.readFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var result domain.GateResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("submit_gate_result payload is invalid: %w", err)
	}
	result.Decision = strings.ToLower(strings.TrimSpace(result.Decision))
	result.Reason = strings.TrimSpace(result.Reason)
	result.ActionRequired = strings.TrimSpace(result.ActionRequired)
	result.Summary = strings.TrimSpace(result.Summary)
	switch result.Decision {
	case "pass", "rework", "fail", "human":
	default:
		return nil, fmt.Errorf("submit_gate_result decision %q is invalid", result.Decision)
	}
	if result.Reason == "" {
		return nil, fmt.Errorf("submit_gate_result reason is required")
	}
	if result.Decision == "human" && result.ActionRequired == "" {
		return nil, fmt.Errorf("submit_gate_result actionRequired is required for human decisions")
	}
	if len(result.Reason) > 4000 || len(result.ActionRequired) > 4000 || len(result.Summary) > 4000 || len(result.Findings) > 50 {
		return nil, fmt.Errorf("submit_gate_result exceeds size limits")
	}
	for _, finding := range result.Findings {
		if strings.TrimSpace(finding.Title) == "" || len(finding.Title) > 500 || len(finding.Severity) > 64 || len(finding.Evidence) > 4000 {
			return nil, fmt.Errorf("submit_gate_result contains an invalid finding")
		}
	}
	return &result, nil
}

// applyGateResult maps an Agent terminal decision onto the engine's failure
// pipeline. rejectTarget is the node's configured RejectTo: a rework decision
// without a valid reject target cannot be routed upstream, so it degrades to a
// terminal fail instead of failing with a misleading "upstream rework" error.
func applyGateResult(result *domain.GateResult, rejectTarget string) (error, string, bool) {
	if result == nil {
		return nil, "", false
	}
	switch result.Decision {
	case "pass":
		return nil, "", false
	case "rework":
		if strings.TrimSpace(rejectTarget) == "" {
			return fmt.Errorf("agent requested rework but this node has no rework target (rejectTo); treated as fail: %s", result.Reason), "fail", false
		}
		return fmt.Errorf("agent requested upstream rework: %s", result.Reason), "reject", true
	case "fail":
		return fmt.Errorf("agent submitted terminal failure: %s", result.Reason), "fail", false
	case "human":
		return fmt.Errorf("agent requested human confirmation: %s", result.Reason), "human", false
	default:
		return fmt.Errorf("unsupported agent terminal decision %q", result.Decision), "fail", false
	}
}

// detectPromptLanguage inspects prompt texts to determine whether the prompt
// is primarily Korean ("ko"), English ("en"), or Chinese ("zh").
// If an explicit language directive (e.g. <!-- lang: ko -->, lang: ko) is found, it is respected.
// Otherwise, character analysis is used:
// - If Hangul characters are present and outnumber or equal Han characters, returns "ko".
// - If Han characters are present and outnumber Hangul, returns "zh".
// Defaults to "zh" to preserve backwards compatibility.
func detectPromptLanguage(texts ...string) string {
	for _, text := range texts {
		lower := strings.ToLower(text)
		if strings.Contains(lower, "<!-- lang: ko -->") || strings.Contains(lower, "lang: ko") || strings.Contains(lower, "language: ko") || strings.Contains(lower, "locale: ko") {
			return "ko"
		}
		if strings.Contains(lower, "<!-- lang: en -->") || strings.Contains(lower, "lang: en") || strings.Contains(lower, "language: en") || strings.Contains(lower, "locale: en") {
			return "en"
		}
		if strings.Contains(lower, "<!-- lang: zh -->") || strings.Contains(lower, "lang: zh") || strings.Contains(lower, "language: zh") || strings.Contains(lower, "locale: zh") {
			return "zh"
		}
	}

	hangulCount := 0
	hanCount := 0
	for _, text := range texts {
		for _, r := range text {
			if unicode.Is(unicode.Hangul, r) {
				hangulCount++
			} else if unicode.Is(unicode.Han, r) {
				hanCount++
			}
		}
	}

	if hangulCount > 0 && hangulCount*2 >= hanCount {
		return "ko"
	}
	if hanCount > 0 && hanCount > hangulCount {
		return "zh"
	}
	return "zh"
}

func appendSubmitGateInstruction(prompt, rejectTarget string, retry bool) string {
	lang := detectPromptLanguage(prompt)
	if retry {
		switch lang {
		case "ko":
			return "방금 유효한 노드 최종 상태(submit_gate_result)를 수신하지 못했습니다. 작업을 중복 실행하거나 텍스트로만 출력하지 마십시오. 즉시 submit_gate_result 도구를 호출하여 pass, rework, fail, human 중 하나와 구체적인 사유(reason)를 제출하십시오. human을 선택할 경우 actionRequired에 수동 확인이 필요한 내용을 반드시 명시하십시오. 도구 호출이 성공해야 본 노드가 완료됩니다.\n\n- 모든 사고 과정과 최종 노드 출력은 반드시 한국어로 작성하십시오."
		case "en":
			return "Did not receive a valid node terminal status just now. Do not re-execute the task from scratch, and do not output text only; please immediately call the tool submit_gate_result to submit one of pass, rework, fail, or human along with a specific reason. When choosing human, actionRequired is required to specify what needs human confirmation. Only a successful tool call completes this node.\n\n- Please write all reasoning and final node output in English."
		default:
			return "刚才没有收到有效的节点终态。不要重复执行任务，也不要只输出文字；请立刻调用 submit_gate_result，提交 pass、rework、fail 或 human 之一以及具体 reason。选择 human 时还必须填写 actionRequired，明确需要人工确认什么。只有工具调用成功才算完成本节点。"
		}
	}

	switch lang {
	case "ko":
		reworkInstruction := "- rework: 상위(업스트림) 노드에서 수정해야 하는 문제가 있습니다. 플랫폼이 해당 노드로 자동 반려합니다."
		if strings.TrimSpace(rejectTarget) == "" {
			reworkInstruction = "- rework: 본 노드에는 반려 대상이 설정되어 있지 않아 선택할 수 없습니다. 즉시 중단해야 하는 경우 fail을 선택하십시오."
		} else {
			reworkInstruction = fmt.Sprintf("- rework: 상위 노드 「%s」에서 수정해야 하는 문제가 있습니다. 플랫폼이 해당 노드로 자동 반려합니다.", rejectTarget)
		}
		return strings.TrimRight(prompt, "\n") + fmt.Sprintf(`

# 노드 최종 상태 필수 제출

작업을 완료한 후 반드시 MCP 도구 submit_gate_result를 호출하여 최종 상태를 제출해야 합니다:
- pass: 작업이 정상 완료되었습니다. 플랫폼이 다음 하위 노드로 자동 진행합니다.
%s
- fail: 본 노드를 완료할 수 없거나 복구 불가능한 오류가 발생했습니다. 상위 노드로 반려하지 않고 즉시 실패 처리합니다.
- human: 자동으로 신뢰성 있게 판단할 수 없거나 사람의 결정이 필요합니다. 플랫폼이 일시 중지되고 수동 승인을 대기합니다. 이때 actionRequired는 필수이며 사람이 확인하거나 결정해야 할 사항을 구체적으로 작성해야 합니다.

현재 실제 상황에 맞춰 선택하십시오. fail 상황을 rework로 처리하거나, 명백한 실패를 human으로 회피하지 마십시오. reason에는 해당 최종 상태를 결정한 객관적 근거를 설명해야 합니다. rework 시 actionRequired에 재작업 요구사항을 명시할 수 있으며, findings 및 evidence를 첨부할 수 있습니다. 보고서 텍스트나 일반 응답으로 도구 호출을 대신할 수 없습니다. 이 도구 호출이 성공해야 본 노드가 완료됩니다.

- 모든 사고 과정과 최종 노드 출력은 반드시 한국어로 작성하십시오.`, reworkInstruction)

	case "en":
		reworkInstruction := "- rework: there are issues that should be reworked by an upstream node; the platform will automatically send it back;"
		if strings.TrimSpace(rejectTarget) == "" {
			reworkInstruction = "- rework: this node has no rework target configured and cannot be selected; choose fail if direct termination is needed;"
		} else {
			reworkInstruction = fmt.Sprintf("- rework: there are issues that should be reworked by the upstream node \"%s\"; the platform will automatically send it back;", rejectTarget)
		}
		return strings.TrimRight(prompt, "\n") + fmt.Sprintf(`

# Must Submit Node Terminal Status

After completing the task, you must call the MCP tool submit_gate_result to submit a single terminal status:
- pass: task completed normally; platform will advance to downstream nodes automatically;
%s
- fail: this node cannot be completed or encountered an unrecoverable error; platform will mark failure directly without sending back upstream;
- human: cannot be reliably judged automatically or requires human decision; platform will pause and wait for manual confirmation, in which case actionRequired is mandatory and must specify what needs human confirmation or decision.

Choose according to the real situation. Do not treat fail as rework, nor use human to mask a definite failure. reason must explain the factual basis for the decision; rework can use actionRequired to state rework requirements; findings and evidence may be attached. Do not substitute tool calls with report text or final chat replies. Only a successful call to this tool completes this node.

- Please write all reasoning and final node output in English.`, reworkInstruction)

	default:
		reworkInstruction := "- rework：存在应由上游节点返工的问题；平台会自动打回该节点；"
		if strings.TrimSpace(rejectTarget) == "" {
			reworkInstruction = "- rework：本节点没有配置返工目标，不可选择；需要直接终止时请选择 fail；"
		} else {
			reworkInstruction = fmt.Sprintf("- rework：存在应由上游节点「%s」返工的问题；平台会自动打回；", rejectTarget)
		}
		return strings.TrimRight(prompt, "\n") + fmt.Sprintf(`

# 必须提交节点终态

完成任务后必须调用 MCP 工具 submit_gate_result，提交唯一终态：
- pass：任务正常完成；平台会自动流转到下游节点；
%s
- fail：本节点无法完成或出现不可恢复错误；平台会直接标记失败，不打回上游；
- human：无法可靠自动判断，或必须由人决策；平台会暂停并等待人工确认，此时 actionRequired 必填，必须明确说明需要人确认或决定什么。

必须根据当前真实情况选择，不要把 fail 当成 rework，也不要用 human 掩盖明确失败。reason 必须解释作出该终态的事实依据；rework 可用 actionRequired 写清返工要求；可附带 findings 和 evidence。不要用报告文本或最终回复代替工具调用。只有该工具调用成功，本节点才算完成。`, reworkInstruction)
	}
}
