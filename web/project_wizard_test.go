package web

import (
	"os/exec"
	"testing"
)

func TestProjectWizard(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	if out, err := exec.Command(node, "project_wizard_test.js", "app/projects.js", "app/repo_drawer.js", "app/project-wizard.js", "app/issues.js").CombinedOutput(); err != nil {
		t.Fatalf("project wizard: %v\n%s", err, out)
	}
}
