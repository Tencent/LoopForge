package procwatch

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

func shell(script string) func(context.Context) *exec.Cmd {
	return func(ctx context.Context) *exec.Cmd { return exec.CommandContext(ctx, "sh", "-c", script) }
}

type recorder struct {
	mu     sync.Mutex
	events []Event
}

func (r *recorder) observe(e Event) { r.mu.Lock(); r.events = append(r.events, e); r.mu.Unlock() }

func (r *recorder) kinds() []Kind {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Kind
	for _, e := range r.events {
		out = append(out, e.Kind)
	}
	return out
}

func (r *recorder) last() Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.events[len(r.events)-1]
}

func TestRunReportsLifecycleAndCollapsesRedrawnCounters(t *testing.T) {
	rec := &recorder{}
	ctx := WithObserver(context.Background(), rec.observe)
	script := `printf 'Cloning into .\n' >&2; for i in 10 50 100; do printf 'Receiving objects: %s%% (1/1)\r' $i >&2; done; printf '\nResolving deltas: 100%%\n' >&2`
	tail, err := Run(ctx, shell(script), Options{Stage: "clone", Label: "克隆仓库 web", Stall: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	want := "Cloning into .\nReceiving objects: 100% (1/1)\nResolving deltas: 100%"
	if tail != want {
		t.Fatalf("tail = %q, want %q", tail, want)
	}
	kinds := rec.kinds()
	// The first line is reported at once; later ones within progressEvery are not.
	if len(kinds) != 3 || kinds[0] != Started || kinds[1] != Progress || kinds[2] != Completed {
		t.Fatalf("events = %v", kinds)
	}
	if e := rec.events[1]; e.Stage != "clone" || e.Message != "克隆仓库 web：Cloning into ." {
		t.Fatalf("progress = %+v", e)
	}
}

func TestRunKeepsOnlyTheTailOfFailingOutput(t *testing.T) {
	rec := &recorder{}
	ctx := WithObserver(context.Background(), rec.observe)
	tail, err := Run(ctx, shell(`i=0; while [ $i -lt 500 ]; do i=$((i+1)); echo "line $i secret-token"; done; exit 3`), Options{
		Stage: "image", Label: "构建沙箱镜像",
		Redact: func(s string) string { return strings.ReplaceAll(s, "secret-token", "***") },
	})
	if err == nil || !strings.Contains(err.Error(), "exit status 3") || Stopped(err) {
		t.Fatalf("err = %v", err)
	}
	lines := strings.Split(tail, "\n")
	if len(lines) != tailLines || lines[0] != "line 481 ***" || lines[len(lines)-1] != "line 500 ***" {
		t.Fatalf("tail = %d lines %q..%q", len(lines), lines[0], lines[len(lines)-1])
	}
	if strings.Contains(tail, "secret-token") {
		t.Fatal("tail not redacted")
	}
	last := rec.last()
	if last.Output != tail {
		t.Fatalf("observer lost final output: %q", last.Output)
	}
	if last.Kind != Failed || !strings.Contains(last.Message, "构建沙箱镜像 失败") || !strings.HasSuffix(last.Message, "line 500 ***") {
		t.Fatalf("failed event = %+v", last)
	}
}

func TestOutputRedactsSplitSecretsAndBoundsUnterminatedLines(t *testing.T) {
	var published []string
	w := &lineWriter{
		redact: func(s string) string { return strings.ReplaceAll(s, "private-secret", "[redacted]") },
		onLine: func(line string, _ time.Time) { published = append(published, line) },
	}
	for _, chunk := range []string{"正在下载 private-", "secret\r", strings.Repeat("x", maxLineBytes+1), "secret-suffix\n", "tail: private-", "secret"} {
		_, _ = w.Write([]byte(chunk))
	}
	w.flush()
	if got := w.tail(); got != "正在下载 [redacted]\ntail: [redacted]" {
		t.Fatalf("lost or leaked output: %q", got)
	}
	if w.pending.Len() != 0 || w.pending.Cap() > 2*maxLineBytes {
		t.Fatal("unterminated output grew without bound")
	}
	for _, line := range published {
		if strings.Contains(line, "private-secret") || strings.Contains(line, "secret-suffix") {
			t.Fatalf("secret leaked to observer: %s", line)
		}
	}
	for i := 0; i < 30; i++ {
		_, _ = w.Write([]byte(strings.Repeat("下载中", 3000) + "\n"))
		if got := w.tail(); len(got) > tailBytes || !utf8.ValidString(got) {
			t.Fatal("output tail must remain bounded UTF8")
		}
	}
}

func TestRunKillsACommandThatStopsPrinting(t *testing.T) {
	rec := &recorder{}
	ctx := WithObserver(context.Background(), rec.observe)
	started := time.Now()
	_, err := Run(ctx, shell(`echo connecting; exec sleep 60`), Options{Stage: "clone", Label: "克隆仓库 web", Stall: 300 * time.Millisecond, StallEnv: "AGENTFLOW_GIT_CLONE_STALL_TIMEOUT"})
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("stalled command ran %s", elapsed)
	}
	if err == nil || !strings.Contains(err.Error(), "没有任何输出") || !strings.Contains(err.Error(), "AGENTFLOW_GIT_CLONE_STALL_TIMEOUT") {
		t.Fatalf("err = %v", err)
	}
	if !Stopped(err) {
		t.Fatalf("stall not marked as stopped: %v", err)
	}
	if last := rec.last(); last.Kind != Failed || !strings.Contains(last.Message, "connecting") {
		t.Fatalf("failed event = %+v", last)
	}
}

func TestRunKeepsASlowCommandThatKeepsPrinting(t *testing.T) {
	// Output every 100ms with a 300ms stall window: slow, but alive.
	tail, err := Run(context.Background(), shell(`for i in 1 2 3 4 5 6 7 8; do echo "Receiving objects: $((i*10))%"; sleep 0.1; done`), Options{Label: "克隆仓库", Stall: 300 * time.Millisecond})
	if err != nil || tail != "Receiving objects: 80%" {
		t.Fatalf("tail=%q err=%v", tail, err)
	}
}

func TestRunStopsAtTheTotalTimeout(t *testing.T) {
	_, err := Run(context.Background(), shell(`while :; do echo tick; sleep 0.05; done`), Options{Label: "构建沙箱镜像", Stall: time.Minute, Timeout: 300 * time.Millisecond, TimeoutEnv: "AGENTFLOW_DOCKER_BUILD_TIMEOUT"})
	if err == nil || !strings.Contains(err.Error(), "超过 300ms 仍未完成") || !strings.Contains(err.Error(), "AGENTFLOW_DOCKER_BUILD_TIMEOUT") {
		t.Fatalf("err = %v", err)
	}
	if !Stopped(err) {
		t.Fatalf("timeout not marked as stopped: %v", err)
	}
}

func TestRunNamesTheCallersDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, err := Run(ctx, shell(`while :; do echo tick; sleep 0.05; done`), Options{Label: "克隆仓库 web", Stall: time.Minute})
	if !Stopped(err) || !strings.Contains(err.Error(), "克隆仓库 web 在调用方时限内没有完成") {
		t.Fatalf("err = %v", err)
	}
	// A caller that cancels (rather than running out of time) is not a stop.
	ctx, cancel = context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	if _, err := Run(ctx, shell(`exec sleep 60`), Options{Label: "克隆仓库"}); err == nil || Stopped(err) {
		t.Fatalf("canceled run = %v", err)
	}
}

func TestRunWithoutObserverStillWorks(t *testing.T) {
	if _, err := Run(context.Background(), shell(`echo ok`), Options{}); err != nil {
		t.Fatal(err)
	}
}

func TestDurationEnv(t *testing.T) {
	const name = "PROCWATCH_TEST_DURATION"
	for _, tc := range []struct {
		raw  string
		want time.Duration
	}{{"", 5 * time.Minute}, {"0", 0}, {"90s", 90 * time.Second}, {"bogus", 5 * time.Minute}, {"-1m", 5 * time.Minute}} {
		t.Run(fmt.Sprintf("%q", tc.raw), func(t *testing.T) {
			t.Setenv(name, tc.raw)
			if got := DurationEnv(name, 5*time.Minute); got != tc.want {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
		})
	}
}

func TestRunKillsTheWholeProcessTree(t *testing.T) {
	// The background sleep stands in for a helper such as git-remote-https: it
	// inherits the output pipe and prints nothing, so only a group kill ends
	// it. Without one, Run also waits out waitDelay on the held pipe.
	pidFile := t.TempDir() + "/child.pid"
	script := `sleep 60 & echo $! > ` + pidFile + `; while :; do echo downloading; sleep 0.05; done`
	started := time.Now()
	_, err := Run(context.Background(), shell(script), Options{Label: "克隆仓库", Timeout: 300 * time.Millisecond})
	if !Stopped(err) {
		t.Fatalf("err = %v", err)
	}
	if elapsed := time.Since(started); elapsed >= waitDelay {
		t.Fatalf("Run waited %s on a helper that still held the pipe", elapsed)
	}
	raw, readErr := os.ReadFile(pidFile)
	if readErr != nil {
		t.Fatal(readErr)
	}
	pid := strings.TrimSpace(string(raw))
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = exec.Command("kill", "-9", pid).Run()
	t.Fatalf("helper %s still running after the step was stopped", pid)
}

// processAlive treats a zombie as dead: a container whose PID 1 does not reap
// orphans keeps the killed helper as a zombie, which kill -0 still finds.
func processAlive(pid string) bool {
	if stat, err := os.ReadFile("/proc/" + pid + "/stat"); err == nil {
		if i := strings.LastIndexByte(string(stat), ')'); i >= 0 && i+2 < len(stat) {
			return stat[i+2] != 'Z'
		}
	}
	return exec.Command("kill", "-0", pid).Run() == nil
}
