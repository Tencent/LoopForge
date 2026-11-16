package repo

import (
	"testing"

	"agentflow/internal/domain"
)

func TestNodeMetricStage(t *testing.T) {
	cases := []struct {
		name       string
		nodeID     string
		definition domain.WorkflowNode
		lane       string
		frozen     bool
		wantStage  string
		wantSource string
	}{
		{
			name:       "explicit stage from frozen snapshot",
			nodeID:     "build",
			definition: domain.WorkflowNode{Name: "需求澄清", MetricStage: " development "},
			frozen:     true,
			wantStage:  "development",
			wantSource: "explicit",
		},
		{
			name:       "explicit stage from live fallback definition is inferred",
			nodeID:     "build",
			definition: domain.WorkflowNode{MetricStage: "testing"},
			frozen:     false,
			wantStage:  "testing",
			wantSource: "inferred",
		},
		{
			name:       "explicit none is passed through",
			nodeID:     "notify",
			definition: domain.WorkflowNode{Name: "测试通知", MetricStage: "none"},
			frozen:     true,
			wantStage:  "none",
			wantSource: "explicit",
		},
		{
			name:       "blank stage on frozen snapshot falls back to name inference",
			nodeID:     "verify",
			definition: domain.WorkflowNode{Name: "测试验证", MetricStage: "   "},
			frozen:     true,
			wantStage:  "testing",
			wantSource: "inferred",
		},
		{
			name:       "blank stage falls back to lane inference",
			nodeID:     "draft",
			definition: domain.WorkflowNode{Name: "整理"},
			lane:       domain.SOPLaneProduct,
			frozen:     true,
			wantStage:  "clarification",
			wantSource: "inferred",
		},
		{
			name:       "unrecognised node stays unclassified",
			nodeID:     "misc",
			definition: domain.WorkflowNode{Name: "杂项"},
			frozen:     false,
			wantStage:  "",
			wantSource: "inferred",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stage, source := nodeMetricStage(tc.nodeID, tc.definition, tc.lane, tc.frozen)
			if stage != tc.wantStage || source != tc.wantSource {
				t.Fatalf("nodeMetricStage() = (%q, %q), want (%q, %q)", stage, source, tc.wantStage, tc.wantSource)
			}
		})
	}
}
