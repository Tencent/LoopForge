package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

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

func appendSubmitGateInstruction(prompt, rejectTarget string, retry bool) string {
	if retry {
		return "刚才没有收到有效的节点终态。不要重复执行任务，也不要只输出文字；请立刻调用 submit_gate_result，提交 pass、rework、fail 或 human 之一以及具体 reason。选择 human 时还必须填写 actionRequired，明确需要人工确认什么。只有工具调用成功才算完成本节点。"
	}
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
