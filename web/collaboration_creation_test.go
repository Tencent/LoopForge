package web

import (
	"os/exec"
	"testing"
)

func TestCollaborationResponsibleFilterRequestsAndRaces(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	if out, err := exec.Command(node, "collaboration_creation_test.js").CombinedOutput(); err != nil {
		t.Fatalf("collaboration filtering: %v\n%s", err, out)
	}
}
