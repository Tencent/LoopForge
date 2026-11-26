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

func TestEvolutionStoryInteraction(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	if out, err := exec.Command(node, "evolution_story_test.js").CombinedOutput(); err != nil {
		t.Fatalf("evolution story interaction: %v\n%s", err, out)
	}
}

func TestEvolutionHomeInteraction(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	if out, err := exec.Command(node, "evolution_home_test.js").CombinedOutput(); err != nil {
		t.Fatalf("evolution home interaction: %v\n%s", err, out)
	}
}

func TestEvolutionSOPInteraction(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	if out, err := exec.Command(node, "evolution_sop_test.js").CombinedOutput(); err != nil {
		t.Fatalf("evolution sop interaction: %v\n%s", err, out)
	}
}
