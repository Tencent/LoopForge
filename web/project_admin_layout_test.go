package web

import (
	"os/exec"
	"testing"
)

func TestProjectAdminLayout(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	if out, err := exec.Command(node, "project_admin_layout_test.js", "app/projects.js", "app/repo_drawer.js", "app/issues.js").CombinedOutput(); err != nil {
		t.Fatalf("project admin layout: %v\n%s", err, out)
	}
}
