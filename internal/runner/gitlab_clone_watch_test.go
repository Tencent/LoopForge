package runner

import (
	"bytes"
	"context"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/gitwork"
	"agentflow/internal/procwatch"
)

func TestRunnerGitLabCloneWatchRedactsLogsAndStopsChildren(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process-group fixture requires a POSIX shell")
	}
	const token = "runner-watch-secret"
	t.Setenv(gitwork.CloneStallEnv, "100ms")
	t.Setenv(gitwork.CloneTimeoutEnv, "5s")
	marker := filepath.Join(t.TempDir(), "orphaned-child")
	t.Setenv("CHECKOUT_CHILD_MARKER", marker)
	installFakeRunnerClone(t, `    test "${AGENTFLOW_GIT_HTTP_PASSWORD:-}" = runner-watch-secret || exit 61
    test "${AGENTFLOW_GIT_HTTP_USER:-}" = oauth2 || exit 62
    case "$*" in *runner-watch-secret*) exit 63 ;; esac
    case "$*" in *credential.https://gitlab.example.test.helper=*) ;; *) exit 64 ;; esac
    case "$*" in *"clone --progress"*) ;; *) exit 65 ;; esac
    printf 'Receiving objects: 27%% runner-watch-secret\r' >&2
    (sleep 0.8; printf orphan > "$CHECKOUT_CHILD_MARKER") &
    wait`)
	var logs bytes.Buffer
	oldOutput := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(oldOutput) })
	repository := &domain.RunnerRepository{Name: "web", CloneURL: "https://gitlab.example.test/team/repo.git", GitAuthBaseURL: "https://gitlab.example.test", GitToken: token}
	started := time.Now()
	err := cloneRepository(context.Background(), t.TempDir(), repository)
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("stalled GitLab clone held preparation for %s", elapsed)
	}
	if !procwatch.Stopped(err) || !strings.Contains(err.Error(), "Receiving objects: 27% ***") {
		t.Fatalf("missing redacted stall diagnostic: %v", err)
	}
	if strings.Contains(err.Error()+logs.String(), token) {
		t.Fatal("GitLab token leaked through runner logs or clone failure")
	}
	if !strings.Contains(logs.String(), "Receiving objects: 27% ***") || !strings.Contains(logs.String(), "没有任何输出") {
		t.Fatalf("missing redacted progress or stall log: %s", logs.String())
	}
	time.Sleep(time.Second)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("stalled GitLab clone left a Git child running: %v", err)
	}
}
