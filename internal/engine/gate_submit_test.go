package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func TestApplyGateResultDistinguishesTerminalDecisions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		decision     string
		rejectTarget string
		action       string
		rework       bool
		hasError     bool
	}{
		{decision: "pass"},
		{decision: "rework", rejectTarget: "implement", action: "reject", rework: true, hasError: true},
		{decision: "fail", action: "fail", hasError: true},
		{decision: "human", action: "human", hasError: true},
	}
	for _, tt := range tests {
		t.Run(tt.decision, func(t *testing.T) {
			err, action, rework := applyGateResult(&domain.GateResult{Decision: tt.decision, Reason: "reason"}, tt.rejectTarget)
			if (err != nil) != tt.hasError || action != tt.action || rework != tt.rework {
				t.Fatalf("err=%v action=%q rework=%v", err, action, rework)
			}
		})
	}
}

func TestApplyGateResultDegradesReworkWithoutTarget(t *testing.T) {
	t.Parallel()
	err, action, rework := applyGateResult(&domain.GateResult{Decision: "rework", Reason: "upstream must fix inputs"}, "")
	if err == nil || action != "fail" || rework {
		t.Fatalf("rework without rejectTo must degrade to fail: err=%v action=%q rework=%v", err, action, rework)
	}
	if !strings.Contains(err.Error(), "no rework target") || !strings.Contains(err.Error(), "upstream must fix inputs") {
		t.Fatalf("degraded error should explain the missing target and keep the agent reason: %v", err)
	}
}

func TestSubmitGateInstructionExplainsReworkAvailability(t *testing.T) {
	t.Parallel()
	withTarget := appendSubmitGateInstruction("task", "implement", false)
	for _, want := range []string{"pass", "rework", "fail", "human", "implement"} {
		if !strings.Contains(withTarget, want) {
			t.Fatalf("instruction missing %q:\n%s", want, withTarget)
		}
	}
	withoutTarget := appendSubmitGateInstruction("task", "", false)
	if !strings.Contains(withoutTarget, "rework：本节点没有配置返工目标，不可选择") {
		t.Fatalf("missing no-target warning:\n%s", withoutTarget)
	}
}

func TestSubmitGateInstructionLanguageDetection(t *testing.T) {
	t.Parallel()

	// Korean detection and instruction
	koPrompt := "Phase 1: crates/wz-model 도메인 모델 크레이트 구현"
	koResult := appendSubmitGateInstruction(koPrompt, "아키텍처_설계", false)
	for _, want := range []string{"# 노드 최종 상태 필수 제출", "submit_gate_result", "pass: 작업이 정상 완료되었습니다", "상위 노드 「아키텍처_설계」", "모든 사고 과정과 최종 노드 출력은 반드시 한국어로 작성하십시오"} {
		if !strings.Contains(koResult, want) {
			t.Fatalf("korean instruction missing %q:\n%s", want, koResult)
		}
	}

	koRetry := appendSubmitGateInstruction(koPrompt, "", true)
	if !strings.Contains(koRetry, "방금 유효한 노드 최종 상태") || !strings.Contains(koRetry, "한국어로 작성하십시오") {
		t.Fatalf("korean retry instruction invalid:\n%s", koRetry)
	}

	// English detection via directive
	enPrompt := "Build user authentication service <!-- lang: en -->"
	enResult := appendSubmitGateInstruction(enPrompt, "design", false)
	for _, want := range []string{"# Must Submit Node Terminal Status", "submit_gate_result", "pass: task completed normally", "upstream node \"design\"", "Please write all reasoning and final node output in English"} {
		if !strings.Contains(enResult, want) {
			t.Fatalf("english instruction missing %q:\n%s", want, enResult)
		}
	}

	enRetry := appendSubmitGateInstruction(enPrompt, "", true)
	if !strings.Contains(enRetry, "Did not receive a valid node terminal status") || !strings.Contains(enRetry, "write all reasoning and final node output in English") {
		t.Fatalf("english retry instruction invalid:\n%s", enRetry)
	}

	// Chinese detection (default fallback and Han text)
	zhPrompt := "实现用户认证模块"
	zhResult := appendSubmitGateInstruction(zhPrompt, "设计", false)
	if !strings.Contains(zhResult, "# 必须提交节点终态") || !strings.Contains(zhResult, "上游节点「设计」") {
		t.Fatalf("chinese instruction invalid:\n%s", zhResult)
	}
}

func TestDetectPromptLanguage(t *testing.T) {
	t.Parallel()
	if got := detectPromptLanguage("도메인 모델 크레이트 구현"); got != "ko" {
		t.Fatalf("expected ko, got %q", got)
	}
	if got := detectPromptLanguage("<!-- lang: ko --> Implement feature"); got != "ko" {
		t.Fatalf("expected ko, got %q", got)
	}
	if got := detectPromptLanguage("<!-- lang: en --> Implement feature"); got != "en" {
		t.Fatalf("expected en, got %q", got)
	}
	if got := detectPromptLanguage("实现用户认证"); got != "zh" {
		t.Fatalf("expected zh, got %q", got)
	}
	if got := detectPromptLanguage("task"); got != "zh" {
		t.Fatalf("expected default zh for ASCII task, got %q", got)
	}
}

func TestReadGateResultRequiresHumanAction(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	path := ".agentflow/gate-results/human/attempt-1.json"
	target := filepath.Join(workspace, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte(`{"decision":"human","reason":"evidence is ambiguous"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readGateResult(workspace, path); err == nil || !strings.Contains(err.Error(), "actionRequired") {
		t.Fatalf("human result without requested action must fail validation: %v", err)
	}
	if err := os.WriteFile(target, []byte(`{"decision":"human","reason":"evidence is ambiguous","actionRequired":"confirm whether the exception is acceptable"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := readGateResult(workspace, path)
	if err != nil || result.ActionRequired == "" {
		t.Fatalf("valid human result=%+v err=%v", result, err)
	}
}
