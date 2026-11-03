package web

import (
	"os/exec"
	"testing"
)

func TestBusinessDeliveryDashboard(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	if out, err := exec.Command(node, "business_domains_test.js").CombinedOutput(); err != nil {
		t.Fatalf("business delivery dashboard: %v\n%s", err, out)
	}
}
