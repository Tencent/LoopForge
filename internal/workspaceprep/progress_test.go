package workspaceprep

import (
	"context"
	"errors"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"agentflow/internal/domain"
	"agentflow/internal/procwatch"
)

func TestObserverStreamsBeforeExitAndCancelsProcessGroup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX process group test")
	}
	t.Setenv("AGENTFLOW_WORKSPACE_CLONE_TIMEOUT", "250ms")
	var updates []domain.WorkspaceOpening
	ctx := WithSink(context.Background(), func(p domain.WorkspaceOpening) { updates = append(updates, p) })
	started := time.Now()
	err := Do(ctx, "clone", "正在克隆仓库", func(ctx context.Context) error {
		_, err := procwatch.Run(Observe(ctx), func(ctx context.Context) *exec.Cmd {
			return exec.CommandContext(ctx, "sh", "-c", "printf 'Receiving objects: 10%% (10/100), 1 MiB | 50 KiB/s\\r'; sleep 30 & wait")
		}, procwatch.Options{Stage: "clone", Label: "克隆仓库"})
		return err
	})
	Result(ctx, err)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > 3*time.Second {
		t.Fatalf("timeout did not stop command: %v (%s)", err, time.Since(started))
	}
	if len(updates) < 3 || !strings.Contains(updates[1].Output, "50 KiB/s") {
		t.Fatalf("no live Git progress: %+v", updates)
	}
	last := updates[len(updates)-1]
	if last.State != "failed" || last.Phase != "clone" || !strings.Contains(last.Message, "250ms") {
		t.Fatalf("failure lost phase/deadline: %+v", last)
	}
}

func TestSanitizeCredentialsAndUTF8(t *testing.T) {
	if got := Sanitize("https://user:p@ss@example.com/repo"); strings.Contains(got, "p@ss") || strings.Contains(got, "ss@example") {
		t.Fatalf("userinfo with @ leaked: %s", got)
	}
	got := Sanitize("正在下载 https://user:password@example.com/r?token=private-secret\nAuthorization: Bearer abcdef")
	for _, secret := range []string{"password", "private-secret", "abcdef"} {
		if strings.Contains(got, secret) {
			t.Fatalf("secret leaked: %s", got)
		}
	}
	if !strings.Contains(got, "正在下载") {
		t.Fatalf("lost UTF8: %s", got)
	}
	if got := Sanitize(strings.Repeat("下载中", 2000)); !utf8.ValidString(got) || len(got) > 8195 {
		t.Fatal("truncated tail must remain bounded UTF8")
	}
}

func TestTimeoutConfiguration(t *testing.T) {
	t.Setenv("AGENTFLOW_WORKSPACE_BUILD_TIMEOUT", "37m")
	if Timeout("build") != 37*time.Minute {
		t.Fatal("configured timeout ignored")
	}
	for _, value := range []string{"0", "-1m", "bad"} {
		t.Setenv("AGENTFLOW_WORKSPACE_BUILD_TIMEOUT", value)
		if Timeout("build") != 15*time.Minute {
			t.Fatalf("unbounded invalid timeout %s", value)
		}
	}
}
