package engine

import (
	"agentflow/internal/domain"
	"strings"
	"testing"
	"time"
)

func TestDecisionAndHumanContextLocalization(t *testing.T) {
	// 1. Korean run and node
	koRun := domain.Run{
		ID:    "ko-run",
		Input: "도메인 모델 크레이트 구현",
		Nodes: map[string]*domain.NodeRun{
			"node-design": {
				NodeID:  "node-design",
				Status:  "waiting_approval",
				Attempt: 1,
			},
		},
		WorkflowSnapshot: domain.WorkflowDefinition{
			Name: "러스트 백엔드 파이프라인",
			Nodes: []domain.WorkflowNode{
				{
					ID:   "node-design",
					Name: "아키텍처 및 인터페이스 설계",
				},
			},
		},
	}

	// Verify language detection
	if lang := runNodeLanguage(&koRun, "node-design"); lang != "ko" {
		t.Fatalf("expected lang 'ko', got %q", lang)
	}

	// Test Approve on Korean run
	note := recordHumanContextWithLang(&koRun, "node-design", "수동 승인으로 계속 진행", "", "ko")
	if note != "수동 승인으로 계속 진행" {
		t.Fatalf("unexpected note: %q", note)
	}
	if strings.Contains(note, "人工") {
		t.Fatalf("note contains Chinese characters: %q", note)
	}

	// Simulate decision.md generation in Approve
	decisionKO := "# 승인\n\n수동 승인 게이트를 통과했습니다.\n\n" + note + "\n"
	appendArtifact(&koRun, "node-design", 1, "decision", "decision.md", "text/markdown", []byte(decisionKO))

	var decisionArtifact *domain.Artifact
	for i := range koRun.Artifacts {
		if koRun.Artifacts[i].Name == "decision" {
			decisionArtifact = &koRun.Artifacts[i]
			break
		}
	}
	if decisionArtifact == nil {
		t.Fatal("decision artifact not found")
	}
	if strings.Contains(decisionArtifact.Content, "人工批准继续") {
		t.Fatalf("decision.md still contains '人工批准继续':\n%s", decisionArtifact.Content)
	}
	if !strings.Contains(decisionArtifact.Content, "# 승인") || !strings.Contains(decisionArtifact.Content, "수동 승인으로 계속 진행") {
		t.Fatalf("decision.md does not contain expected Korean text:\n%s", decisionArtifact.Content)
	}

	// 2. English run
	enRun := domain.Run{
		ID:    "en-run",
		Input: "<!-- lang: en --> Implement payment gateway",
		Nodes: map[string]*domain.NodeRun{
			"node-1": {
				NodeID:  "node-1",
				Status:  "waiting_approval",
				Attempt: 1,
			},
		},
	}
	if lang := runNodeLanguage(&enRun, "node-1"); lang != "en" {
		t.Fatalf("expected lang 'en', got %q", lang)
	}
	enNote := recordHumanContextWithLang(&enRun, "node-1", "Manually approved by human operator to continue", "Priority check", "en")
	if !strings.Contains(enNote, "Manual Context:\nPriority check") {
		t.Fatalf("unexpected English note: %q", enNote)
	}
	if strings.Contains(enNote, "人工") {
		t.Fatalf("English note contains Chinese characters: %q", enNote)
	}

	// 3. Human context artifact localization
	var humanCtxArtifact *domain.Artifact
	for i := range enRun.Artifacts {
		if enRun.Artifacts[i].Name == "human-context" {
			humanCtxArtifact = &enRun.Artifacts[i]
			break
		}
	}
	if humanCtxArtifact == nil {
		t.Fatal("human-context artifact not found")
	}
	if !strings.Contains(humanCtxArtifact.Content, "# Manual Context") {
		t.Fatalf("human-context artifact does not contain English header:\n%s", humanCtxArtifact.Content)
	}

	// 4. Korean loop retro
	retroKO := buildLoopRetro(koRun, "node-design", 2, nil, "loop")
	if !strings.Contains(retroKO, "# Loop 복기 — `node-design`") || !strings.Contains(retroKO, "- 자체 수정 라운드: 2") || !strings.Contains(retroKO, "- 최종 결과: 통과") {
		t.Fatalf("Korean loop retro does not match expected format:\n%s", retroKO)
	}
	if strings.Contains(retroKO, "复盘") {
		t.Fatalf("Korean loop retro contains Chinese characters: %s", retroKO)
	}

	// 5. English loop retro
	retroEN := buildLoopRetro(enRun, "node-1", 1, nil, "loop")
	if !strings.Contains(retroEN, "# Loop Retrospective — `node-1`") || !strings.Contains(retroEN, "- Final result: Passed") {
		t.Fatalf("English loop retro does not match expected format:\n%s", retroEN)
	}
}

func TestEngineApproveLocalizationEndToEnd(t *testing.T) {
	e, _ := cvEEngine(t)
	runID := "e2e-ko-approve-run"
	r := domain.Run{
		ID:         runID,
		Status:     "waiting",
		Input:      "회원가입 기능 개발",
		CreatedAt:  time.Now().UTC(),
		UpdatedAt:  time.Now().UTC(),
		Nodes: map[string]*domain.NodeRun{
			"node-gate": {
				NodeID:  "node-gate",
				Status:  "waiting_approval",
				Attempt: 1,
			},
		},
		WorkflowSnapshot: domain.WorkflowDefinition{
			Name: "스프린트 워크플로우",
			Nodes: []domain.WorkflowNode{
				{ID: "node-gate", Name: "기능 검증 게이트", Type: "approval"},
			},
		},
	}
	_ = e.repo.PutRun(r)

	if err := e.Approve(runID, "node-gate", ""); err != nil {
		t.Fatalf("Approve failed: %v", err)
	}

	updated, ok := e.repo.RunState(runID)
	if !ok {
		t.Fatal("run not found after approve")
	}
	var decisionArt *domain.Artifact
	for i := range updated.Artifacts {
		if updated.Artifacts[i].Name == "decision" && updated.Artifacts[i].NodeID == "node-gate" {
			decisionArt = &updated.Artifacts[i]
			break
		}
	}
	if decisionArt == nil {
		t.Fatal("decision.md artifact not found in updated run")
	}
	if strings.Contains(decisionArt.Content, "人工批准继续") {
		t.Fatalf("decision.md still contains '人工批准继续':\n%s", decisionArt.Content)
	}
	if !strings.Contains(decisionArt.Content, "수동 승인으로 계속 진행") {
		t.Fatalf("decision.md does not contain Korean note:\n%s", decisionArt.Content)
	}
}

func TestEnginePassLocalizationEndToEnd(t *testing.T) {
	e, _ := cvEEngine(t)
	runID := "e2e-ko-pass-run"
	r := domain.Run{
		ID:         runID,
		Status:     "failed",
		Input:      "결제 모듈 개발",
		CreatedAt:  time.Now().UTC(),
		UpdatedAt:  time.Now().UTC(),
		Nodes: map[string]*domain.NodeRun{
			"node-script": {
				NodeID:  "node-script",
				Status:  "failed",
				Attempt: 1,
			},
		},
		WorkflowSnapshot: domain.WorkflowDefinition{
			ID:   "wf-pay",
			Name: "결제 워크플로우",
			Nodes: []domain.WorkflowNode{
				{ID: "node-script", Name: "스크립트 검증", Type: "script"},
			},
		},
	}
	_ = e.repo.PutRun(r)

	if err := e.Pass(runID, "node-script", ""); err != nil {
		t.Fatalf("Pass failed: %v", err)
	}

	updated, ok := e.repo.RunState(runID)
	if !ok {
		t.Fatal("run not found after pass")
	}
	nr := updated.Nodes["node-script"]
	if nr.Activity != "수동 통과 (재실행되지 않음)" {
		t.Fatalf("unexpected activity: %q", nr.Activity)
	}
	var decisionArt *domain.Artifact
	for i := range updated.Artifacts {
		if updated.Artifacts[i].Name == "decision" && updated.Artifacts[i].NodeID == "node-script" {
			decisionArt = &updated.Artifacts[i]
			break
		}
	}
	if decisionArt == nil {
		t.Fatal("decision.md artifact not found in updated run")
	}
	if strings.Contains(decisionArt.Content, "人工通过该节点，继续下游") {
		t.Fatalf("decision.md still contains Chinese text:\n%s", decisionArt.Content)
	}
	if !strings.Contains(decisionArt.Content, "이 단계를 수동으로 통과하고 후속 단계 진행") {
		t.Fatalf("decision.md does not contain Korean note:\n%s", decisionArt.Content)
	}
}

