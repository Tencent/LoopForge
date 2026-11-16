package runtime

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func requireConversationSubreaper(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("Docker child subreaper runs on Linux")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("Python3 is required by the Docker conversation supervisor")
	}
}

func waitConversationMarker(t *testing.T, file string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(file); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("native child did not reach its ready marker")
}

func stopConversationSupervisor(t *testing.T, file string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 9*time.Second)
	defer cancel()
	argv := dockerConversationStopArgv(file)
	if output, err := exec.CommandContext(ctx, argv[0], argv[1:]...).CombinedOutput(); err != nil {
		t.Fatalf("stop failed: %s %v", output, err)
	}
}

func TestDockerConversationStopOwnsDetachedDescendants(t *testing.T) {
	requireConversationSubreaper(t)
	for _, parentExits := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel", true: "normal_exit"}[parentExits], func(t *testing.T) {
			dir := t.TempDir()
			file, ready, effect := filepath.Join(dir, "process.pid"), filepath.Join(dir, "ready"), filepath.Join(dir, "effect")
			// The tool starts in a separate session and double-forks, so neither
			// the CLI's process group nor its surviving parent owns it anymore.
			tool := `import os,sys,time
if os.fork(): os._exit(0)
os.setsid()
if os.fork(): os._exit(0)
open(sys.argv[1],'w').close()
time.sleep(1)
open(sys.argv[2],'w').write('escaped')
`
			parent := `import os,subprocess,sys,time
subprocess.run([sys.executable,'-c',sys.argv[1],sys.argv[2],sys.argv[3]],check=True)
while not os.path.exists(sys.argv[2]): time.sleep(0.01)
if sys.argv[4]=='true': sys.exit(0)
time.sleep(30)
`
			mode := "false"
			if parentExits {
				mode = "true"
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			argv := dockerConversationProcessArgv(file, []string{"python3", "-c", parent, tool, ready, effect, mode})
			cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer stopConversationSupervisor(t, file)
			waitConversationMarker(t, ready)
			if !parentExits {
				stopConversationSupervisor(t, file)
			}
			err := cmd.Wait()
			if parentExits && err != nil {
				t.Fatalf("normal CLI exit failed: %v", err)
			}
			if !parentExits && err == nil {
				t.Fatal("cancelled CLI exited successfully")
			}
			// Cross the child's scheduled effect time; an immediate absence
			// check missed the real CLI cancellation bug.
			time.Sleep(1100 * time.Millisecond)
			if _, err := os.Stat(effect); !os.IsNotExist(err) {
				t.Fatalf("detached native tool outlived its phase: %v", err)
			}
		})
	}
}

func TestDockerConversationStopBeforeLaunchFencesLateStart(t *testing.T) {
	requireConversationSubreaper(t)
	dir := t.TempDir()
	file, effect := filepath.Join(dir, "process.pid"), filepath.Join(dir, "effect")
	stopConversationSupervisor(t, file)
	argv := dockerConversationProcessArgv(file, []string{"sh", "-c", `printf escaped > "$1"`, "sh", effect})
	if err := exec.Command(argv[0], argv[1:]...).Run(); err == nil {
		t.Fatal("late native CLI launch succeeded after cancellation")
	}
	if _, err := os.Stat(effect); !os.IsNotExist(err) {
		t.Fatalf("late launch caused an effect: %v", err)
	}
	stopConversationSupervisor(t, file)
	for _, path := range []string{file, file + ".stop"} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("completed control file retained: %s %v", path, err)
		}
	}
}

func TestDockerConversationStopRejectsReusedPID(t *testing.T) {
	requireConversationSubreaper(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sibling := exec.CommandContext(ctx, "sleep", "30")
	if err := sibling.Start(); err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() { finished <- sibling.Wait() }()
	reaped := false
	defer func() {
		_ = sibling.Process.Kill()
		if !reaped {
			<-finished
		}
	}()
	file := filepath.Join(t.TempDir(), "process.pid")
	data, _ := json.Marshal(map[string]any{"pid": sibling.Process.Pid, "start": "stale", "status": "running"})
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	argv := dockerConversationStopArgv(file)
	if err := exec.CommandContext(ctx, argv[0], argv[1:]...).Run(); err == nil {
		t.Fatal("stale process identity accepted")
	}
	select {
	case err := <-finished:
		reaped = true
		t.Fatalf("unrelated process was stopped: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
}
