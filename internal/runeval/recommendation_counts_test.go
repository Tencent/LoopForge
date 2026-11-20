package runeval

import (
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func TestApplyRecommendationRejectsMalformedRetryCounts(t *testing.T) {
	for _, tc := range []struct {
		name string
		key  string
		raw  any
	}{
		{"fraction", "maxReworks", 2.5},
		{"text suffix", "maxAttempts", "3 retries"},
		{"invalid type", "maxReworks", true},
		{"overflow", "maxAttempts", 1e30},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wf := domain.WorkflowDefinition{Nodes: []domain.WorkflowNode{{ID: "n", MaxAttempts: 5, MaxReworks: 4}}}
			err := ApplyRecommendation(domain.RunEvalRecommendation{
				Target: "workflow", Action: "update_node", NodeID: "n",
				Set: map[string]any{tc.key: tc.raw},
			}, &wf, nil)
			if err == nil || !strings.Contains(err.Error(), tc.key) {
				t.Fatalf("want a reported %s error, got %v", tc.key, err)
			}
			if wf.Nodes[0].MaxAttempts != 5 || wf.Nodes[0].MaxReworks != 4 {
				t.Fatalf("invalid recommendation changed retry counts: %+v", wf.Nodes[0])
			}
		})
	}
}
