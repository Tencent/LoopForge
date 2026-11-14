package runeval

import (
	"testing"

	"agentflow/internal/domain"
)

func TestRecommendationGateIDPrefersGateID(t *testing.T) {
	cases := []struct {
		name string
		rec  domain.RunEvalRecommendation
		want string
	}{
		{"empty", domain.RunEvalRecommendation{}, ""},
		{"set only", domain.RunEvalRecommendation{Set: map[string]any{"gateId": " g1 "}}, "g1"},
		{"gate wins", domain.RunEvalRecommendation{Set: map[string]any{"gateId": "g1"}, Gate: &domain.VerificationGate{ID: " g2 "}}, "g2"},
		{"blank gate falls back", domain.RunEvalRecommendation{Set: map[string]any{"gateId": "g1"}, Gate: &domain.VerificationGate{ID: "  "}}, "g1"},
	}
	for _, tc := range cases {
		if got := recommendationGateID(tc.rec); got != tc.want {
			t.Errorf("%s: got %q want %q", tc.name, got, tc.want)
		}
	}
}

func TestRemoveGate(t *testing.T) {
	gates := []domain.VerificationGate{{ID: "a"}, {ID: "b"}, {ID: "c"}}
	out, err := removeGate(gates, "b")
	if err != nil || len(out) != 2 || out[0].ID != "a" || out[1].ID != "c" {
		t.Fatalf("remove b: %+v %v", out, err)
	}
	orig := []domain.VerificationGate{{ID: "a"}}
	out, err = removeGate(orig, "missing")
	if err == nil || err.Error() != `gate "missing" not found` || len(out) != 1 {
		t.Fatalf("remove missing: %+v %v", out, err)
	}
}

func TestUpsertGate(t *testing.T) {
	out, err := upsertGate(nil, "new", domain.RunEvalRecommendation{})
	if err != nil || len(out) != 1 || out[0].ID != "new" || out[0].Mode != "command" || !out[0].Required {
		t.Fatalf("default gate: %+v %v", out, err)
	}
	gates := []domain.VerificationGate{{ID: "x", Name: "old"}, {ID: "y"}}
	out, err = upsertGate(gates, "x", domain.RunEvalRecommendation{Gate: &domain.VerificationGate{ID: "ignored", Name: "new", OnFailure: "loop"}})
	if err != nil || len(out) != 2 || out[0].ID != "x" || out[0].Name != "new" || out[0].Mode != "command" || out[0].Required {
		t.Fatalf("replace gate: %+v %v", out, err)
	}
	if _, err := upsertGate(nil, "z", domain.RunEvalRecommendation{Gate: &domain.VerificationGate{OnFailure: "bad"}}); err == nil || err.Error() != `unsupported gate onFailure "bad"` {
		t.Fatalf("bad onFailure: %v", err)
	}
}

func TestApplyGateListRequiresID(t *testing.T) {
	for _, remove := range []bool{false, true} {
		if _, err := applyGateList(nil, domain.RunEvalRecommendation{}, remove); err == nil || err.Error() != "gate id is required" {
			t.Fatalf("remove=%v: %v", remove, err)
		}
	}
}
