package gitwork

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestGitLabLiveReadOnly(t *testing.T) {
	token, remote := os.Getenv("LOOPFORGE_TEST_GITLAB_TOKEN"), os.Getenv("LOOPFORGE_TEST_GITLAB_REMOTE")
	if token == "" || remote == "" {
		t.Skip("explicit live test not configured")
	}
	base := os.Getenv("LOOPFORGE_TEST_GITLAB_BASE")
	ctx := WithHostCredential(context.Background(), base, token)
	out, err := Run(ctx, t.TempDir(), "ls-remote", "--heads", remote)
	if err != nil {
		t.Fatalf("ls-remote failed: %v", err)
	}
	if strings.TrimSpace(out) == "" {
		t.Fatal("no branches returned")
	}
}
