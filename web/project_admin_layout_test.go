package web

import (
	"os/exec"
	"strings"
	"testing"
)

func TestProjectAdminLayout(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	out, err := exec.Command(node, "project_admin_layout_test.js", "app/projects.js", "app/repo_drawer.js", "app/issues.js").CombinedOutput()
	if err != nil {
		t.Fatalf("project admin layout: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "project admin layout passed") {
		t.Fatalf("project admin layout did not finish:\n%s", out)
	}
}
