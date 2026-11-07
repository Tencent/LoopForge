package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestSDNotifySendsReadyAndMainPID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notify.sock")
	conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	t.Setenv("NOTIFY_SOCKET", path)
	t.Setenv(envReadyFD, "")
	signalReady(handoverStatus)
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 512)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	got := string(buf[:n])
	want := fmt.Sprintf("MAINPID=%d\nREADY=1\nSTATUS=%s\n", os.Getpid(), handoverStatus)
	if got != want || !strings.Contains(got, "handover") {
		t.Fatalf("sd_notify payload %q, want %q", got, want)
	}
	t.Setenv("NOTIFY_SOCKET", "")
	if err := sdNotify("READY=1"); err != nil {
		t.Fatal("outside systemd sd_notify must be a no-op", err)
	}
}

func TestListenMainBindsWithoutInheritedFD(t *testing.T) {
	t.Setenv(envListenFD, "")
	ln, inherited, err := listenMain("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if inherited {
		t.Fatal("normal start reported an inherited listener")
	}
	t.Setenv(envListenFD, "nope")
	if _, _, err := listenMain("127.0.0.1:0"); err == nil {
		t.Fatal("invalid inherited fd accepted")
	}
}

func TestResolveDrainTimeoutDefaultsToTwoHours(t *testing.T) {
	t.Setenv("AGENTFLOW_DRAIN_TIMEOUT", "")
	if got := resolveDrainTimeout(); got != 120*time.Minute {
		t.Fatal(got)
	}
	t.Setenv("AGENTFLOW_DRAIN_TIMEOUT", "90s")
	if got := resolveDrainTimeout(); got != 90*time.Second {
		t.Fatal(got)
	}
	t.Setenv("AGENTFLOW_DRAIN_TIMEOUT", "bogus")
	if got := resolveDrainTimeout(); got != 120*time.Minute {
		t.Fatal(got)
	}
}

// The helper is a miniature API process that uses the production handover
// primitives: it serves /api/health with its pid and runs one long task.
const helperEnv = "AGENTFLOW_HANDOVER_TEST_HELPER"

func TestHandoverHelperProcess(t *testing.T) {
	if os.Getenv(helperEnv) != "1" {
		t.Skip("subprocess helper")
	}
	ln, inherited, err := listenMain("127.0.0.1:0")
	if err != nil {
		fmt.Println("listen error:", err)
		os.Exit(2)
	}
	if inherited && os.Getenv("HELPER_SUCCESSOR_HANGS") == "1" {
		select {} // never reports ready
	}
	var tasks sync.WaitGroup
	mux := http.NewServeMux()
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, `{"pid":%d}`, os.Getpid())
	})
	mux.HandleFunc("/task", func(w http.ResponseWriter, r *http.Request) {
		tasks.Add(1)
		go func() {
			defer tasks.Done()
			time.Sleep(1500 * time.Millisecond)
			fmt.Printf("task done pid=%d\n", os.Getpid())
		}()
		w.WriteHeader(http.StatusAccepted)
	})
	srv := &http.Server{Handler: mux}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGHUP)
	go func() {
		for range sig {
			timeout := 10 * time.Second
			if raw := os.Getenv("HELPER_READY_TIMEOUT"); raw != "" {
				timeout, _ = time.ParseDuration(raw)
			}
			child, err := handover(ln, timeout)
			if err != nil {
				fmt.Printf("handover failed pid=%d: %v\n", os.Getpid(), err)
				continue
			}
			srv.SetKeepAlivesEnabled(false)
			_ = ln.Close()
			fmt.Printf("handed over pid=%d successor=%d\n", os.Getpid(), child.Pid)
			tasks.Wait()
			fmt.Printf("exiting pid=%d\n", os.Getpid())
			os.Exit(0)
		}
	}()
	fmt.Printf("listening addr=%s pid=%d inherited=%t\n", ln.Addr(), os.Getpid(), inherited)
	signalReady(handoverStatus)
	_ = srv.Serve(ln)
	select {}
}

type helperProcess struct {
	cmd   *exec.Cmd
	lines chan string
	addr  string
}

func startHandoverHelper(t *testing.T, extraEnv ...string) *helperProcess {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHandoverHelperProcess$")
	cmd.Env = append(withoutHandoverEnv(os.Environ()), append([]string{helperEnv + "=1", "NOTIFY_SOCKET="}, extraEnv...)...)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	p := &helperProcess{cmd: cmd, lines: make(chan string, 64)}
	go func() {
		scanner := bufio.NewScanner(out)
		for scanner.Scan() {
			p.lines <- scanner.Text()
		}
		close(p.lines)
	}()
	line := p.waitLine(t, "listening addr=", 10*time.Second)
	p.addr = strings.Fields(strings.TrimPrefix(line, "listening addr="))[0]
	return p
}

func (p *helperProcess) waitLine(t *testing.T, prefix string, timeout time.Duration) string {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case line, ok := <-p.lines:
			if !ok {
				t.Fatalf("helper output closed before %q", prefix)
			}
			if strings.HasPrefix(line, prefix) {
				return line
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %q", prefix)
		}
	}
}

func healthPID(addr string) (int, error) {
	client := &http.Client{Timeout: time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
	resp, err := client.Get("http://" + addr + "/api/health")
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var v struct {
		PID int `json:"pid"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return 0, err
	}
	return v.PID, nil
}

func TestHandoverPassesListenerAndDrainsPredecessor(t *testing.T) {
	parent := startHandoverHelper(t)
	defer func() { _ = parent.cmd.Process.Kill(); _ = parent.cmd.Wait() }()
	pid, err := healthPID(parent.addr)
	if err != nil || pid != parent.cmd.Process.Pid {
		t.Fatalf("parent health: pid=%d err=%v", pid, err)
	}
	resp, err := http.Post("http://"+parent.addr+"/task", "text/plain", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if err := parent.cmd.Process.Signal(syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	line := parent.waitLine(t, "listening addr=", 10*time.Second)
	if !strings.Contains(line, "inherited=true") || !strings.Contains(line, parent.addr) {
		t.Fatalf("successor did not inherit the listener: %s", line)
	}
	handed := parent.waitLine(t, "handed over pid=", 10*time.Second)
	var parentPID, childPID int
	if _, err := fmt.Sscanf(handed, "handed over pid=%d successor=%d", &parentPID, &childPID); err != nil {
		t.Fatal(handed, err)
	}
	defer func() { _ = syscall.Kill(childPID, syscall.SIGKILL) }()
	// The predecessor stopped accepting: every new request reaches the successor.
	for i := 0; i < 5; i++ {
		pid, err := healthPID(parent.addr)
		if err != nil || pid != childPID {
			t.Fatalf("request %d after handover served by pid=%d err=%v (successor %d)", i, pid, err, childPID)
		}
	}
	// The in-flight task finishes in the predecessor before it exits.
	parent.waitLine(t, fmt.Sprintf("task done pid=%d", parentPID), 10*time.Second)
	parent.waitLine(t, fmt.Sprintf("exiting pid=%d", parentPID), 10*time.Second)
	exited := make(chan error, 1)
	go func() { exited <- parent.cmd.Wait() }()
	select {
	case err := <-exited:
		if err != nil {
			t.Fatal("predecessor exit:", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("predecessor did not exit after draining")
	}
	if pid, err := healthPID(parent.addr); err != nil || pid != childPID {
		t.Fatalf("successor stopped serving after predecessor exit: pid=%d err=%v", pid, err)
	}
}

func TestHandoverKeepsServingWhenSuccessorNeverReady(t *testing.T) {
	parent := startHandoverHelper(t, "HELPER_SUCCESSOR_HANGS=1", "HELPER_READY_TIMEOUT=1s")
	defer func() { _ = parent.cmd.Process.Kill(); _ = parent.cmd.Wait() }()
	if err := parent.cmd.Process.Signal(syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	parent.waitLine(t, "handover failed pid=", 15*time.Second)
	for i := 0; i < 3; i++ {
		pid, err := healthPID(parent.addr)
		if err != nil || pid != parent.cmd.Process.Pid {
			t.Fatalf("predecessor stopped serving after a failed handover: pid=%d err=%v", pid, err)
		}
	}
}
