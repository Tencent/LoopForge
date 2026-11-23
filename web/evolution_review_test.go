package web

import (
	"os/exec"
	"testing"
)

func TestEvolutionReviewInteraction(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	if out, err := exec.Command(node, "evolution_review_test.js").CombinedOutput(); err != nil {
		t.Fatalf("evolution review interaction: %v\n%s", err, out)
	}
}
