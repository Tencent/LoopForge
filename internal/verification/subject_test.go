package verification

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestStartSubjectValidatesConfig(t *testing.T) {
	ctx := context.Background()
	if _, err := StartSubject(ctx, SubjectConfig{Port: 8080}); err == nil {
		t.Fatal("missing image accepted")
	}
	if _, err := StartSubject(ctx, SubjectConfig{Image: "x"}); err == nil {
		t.Fatal("missing port accepted")
	}
	if _, err := StartSubject(ctx, SubjectConfig{Image: "x", Port: 8080, HealthPath: "health"}); err == nil {
		t.Fatal("relative health path accepted")
	}
}

// TestStartSubjectRealContainer launches a tiny HTTP server and waits for
// health. It is opt-in like the other container tests.
func TestStartSubjectRealContainer(t *testing.T) {
	if os.Getenv("LOOPFORGE_VERIFY_E2E") != "1" {
		t.Skip("set LOOPFORGE_VERIFY_E2E=1 to run the real subject container test")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("docker daemon not available")
	}
	subject, err := StartSubject(context.Background(), SubjectConfig{
		Image:          "node:22-bookworm-slim",
		Command:        []string{"node", "-e", `require("http").createServer((q,s)=>s.end("ok")).listen(8080)`},
		Port:           8080,
		HealthPath:     "/",
		StartupTimeout: 60 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer subject.Stop()
	if subject.BaseURL == "" {
		t.Fatal("subject has no base URL")
	}
}
