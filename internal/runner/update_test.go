package runner

import (
	"context"
	"crypto/sha256"
	"debug/elf"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	flowruntime "agentflow/internal/runtime"

	"github.com/gorilla/websocket"
)

func TestRunnerUpdateReplacesBinaryOnlyAfterVerification(t *testing.T) {
	newBinary := []byte("new runner")
	hash := sha256.Sum256(newBinary)
	expected := hex.EncodeToString(hash[:])
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/runners/agentflow-runner" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("metadata") == "1" {
			fmt.Fprintf(w, `{"sha256":%q}`, expected)
			return
		}
		w.Write(newBinary)
	}))
	defer server.Close()
	d, err := New(Config{ServerURL: server.URL, BootstrapToken: "test"})
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "agentflow-runner")
	if err := os.WriteFile(binary, []byte("old runner"), 0o755); err != nil {
		t.Fatal(err)
	}
	atomic.StoreInt32(&d.running, 1)
	if err := d.checkForUpdate(context.Background(), binary); err != nil {
		t.Fatal(err)
	}
	if d.updateReady.Load() {
		t.Fatal("busy runner must not update")
	}
	atomic.StoreInt32(&d.running, 0)
	if err := d.checkForUpdate(context.Background(), binary); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(newBinary) || !d.updateReady.Load() {
		t.Fatalf("binary=%q updateReady=%v", got, d.updateReady.Load())
	}
}

func TestRunnerUpdateRejectsChecksumMismatch(t *testing.T) {
	newBinary := []byte("new runner")
	hash := sha256.Sum256(newBinary)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("metadata") == "1" {
			fmt.Fprintf(w, `{"sha256":%q}`, hex.EncodeToString(hash[:]))
			return
		}
		w.Write([]byte("different payload"))
	}))
	defer server.Close()
	d, err := New(Config{ServerURL: server.URL, BootstrapToken: "test"})
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "agentflow-runner")
	if err := os.WriteFile(binary, []byte("old runner"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := d.checkForUpdate(context.Background(), binary); err == nil {
		t.Fatal("expected checksum mismatch")
	}
	got, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "old runner" || d.updateReady.Load() {
		t.Fatalf("binary changed after failed verification: %q", got)
	}
}

func TestHeartbeatRenewalPersistsExistingRunnerIdentity(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	d, err := New(Config{ServerURL: "https://runner.example", BootstrapToken: "one-time-code", StatePath: statePath})
	if err != nil {
		t.Fatal(err)
	}
	oldExpiry := time.Now().Add(6 * 24 * time.Hour)
	if err := d.saveState(daemonState{RunnerID: "runner-1", SessionToken: "session-1", ExpiresAt: oldExpiry}); err != nil {
		t.Fatal(err)
	}
	newExpiry := time.Now().Add(30 * 24 * time.Hour)
	d.refreshSessionExpiry(newExpiry)
	got, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	var saved daemonState
	if err := json.Unmarshal(got, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.RunnerID != "runner-1" || saved.SessionToken != "session-1" || !saved.ExpiresAt.Equal(newExpiry) {
		t.Fatalf("renewed state = %+v", saved)
	}
}

func TestGateMCPHelperUpdatesWhileRunnerIdle(t *testing.T) {
	payload := []byte("new gate helper")
	hash := sha256.Sum256(payload)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/runners/agentflow-gate-mcp") {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("metadata") == "1" {
			fmt.Fprintf(w, `{"sha256":%q}`, hex.EncodeToString(hash[:]))
			return
		}
		w.Write(payload)
	}))
	defer server.Close()
	d, err := New(Config{ServerURL: server.URL, BootstrapToken: "test"})
	if err != nil {
		t.Fatal(err)
	}
	pattern := filepath.Join(t.TempDir(), "agentflow-gate-mcp-{os}-{arch}")
	t.Setenv("AGENTFLOW_GATE_MCP_BINARY", pattern)
	atomic.StoreInt32(&d.running, 1)
	if err := d.checkGateMCPUpdates(context.Background()); err != nil {
		t.Fatal(err)
	}
	hostPath := strings.ReplaceAll(strings.ReplaceAll(pattern, "{os}", runtime.GOOS), "{arch}", runtime.GOARCH)
	if _, err := os.Stat(hostPath); !os.IsNotExist(err) {
		t.Fatalf("busy runner updated helper: %v", err)
	}
	atomic.StoreInt32(&d.running, 0)
	if err := d.checkGateMCPUpdates(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, targetOS := range []string{runtime.GOOS, "linux"} {
		if targetOS == "linux" && runtime.GOOS != "darwin" {
			continue
		}
		path := strings.ReplaceAll(strings.ReplaceAll(pattern, "{os}", targetOS), "{arch}", runtime.GOARCH)
		got, err := os.ReadFile(path)
		if err != nil || string(got) != string(payload) {
			t.Fatalf("helper %s = %q, %v", targetOS, got, err)
		}
	}
}

func TestDockerRunnerHelperUpdateRepairsMissingBinary(t *testing.T) {
	payload := dockerRunnerFixtureLinuxBinary(runtime.GOARCH, "Docker helper")
	var downloads atomic.Int32
	var damaged atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		query := req.URL.Query()
		if req.URL.Path != "/runners/agentflow-runner" || query.Get("os") != "linux" || query.Get("arch") != runtime.GOARCH {
			http.NotFound(w, req)
			return
		}
		if query.Get("metadata") == "1" {
			expected := payload
			if damaged.Load() {
				expected = "new companion"
			}
			hash := sha256.Sum256([]byte(expected))
			fmt.Fprintf(w, `{"sha256":%q}`, hex.EncodeToString(hash[:]))
			return
		}
		downloads.Add(1)
		if damaged.Load() {
			fmt.Fprint(w, "partial data")
			return
		}
		fmt.Fprint(w, payload)
	}))
	defer server.Close()
	d, err := New(Config{ServerURL: server.URL, BootstrapToken: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	t.Setenv("AGENTFLOW_RUNNER_BINARY", filepath.Join(bin, "{os}", "{arch}", "agentflow-runner"))
	helper := filepath.Join(bin, "linux", runtime.GOARCH, "agentflow-runner")
	ctx := context.Background()
	assertHelper := func() {
		t.Helper()
		if got, err := os.ReadFile(helper); err != nil || string(got) != payload {
			t.Fatalf("Docker helper = %q, %v", got, err)
		}
		if info, err := os.Stat(helper); err != nil || info.Mode().Perm()&0o100 == 0 {
			t.Fatalf("helper is not executable: %v", err)
		}
		if d.updateReady.Load() {
			t.Fatal("updating the companion must not restart the runner")
		}
	}
	// A Docker workspace can already be waiting for the helper under the update fence.
	d.updateMu.RLock()
	atomic.StoreInt32(&d.running, 1)
	err = d.checkDockerRunnerHelperUpdate(ctx, true)
	atomic.StoreInt32(&d.running, 0)
	d.updateMu.RUnlock()
	if err != nil {
		t.Fatal(err)
	}
	assertHelper()
	if err := d.checkDockerRunnerHelperUpdate(ctx, false); err != nil {
		t.Fatal(err)
	}
	if n := downloads.Load(); n != 1 {
		t.Fatalf("already-current helper was re-downloaded, requests = %d", n)
	}
	damaged.Store(true)
	if err := d.checkDockerRunnerHelperUpdate(ctx, false); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("replacement was not rejected: %v", err)
	}
	assertHelper()
	damaged.Store(false)
	if err := os.WriteFile(helper, []byte("invalid existing helper"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := d.checkDockerRunnerHelperUpdate(ctx, true); err != nil {
		t.Fatal(err)
	}
	assertHelper()
	if err := os.Chmod(helper, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := d.checkDockerRunnerHelperUpdate(ctx, true); err != nil {
		t.Fatal(err)
	}
	assertHelper()
}

func TestDockerRunnerHelperUpdateRejectsRunningExecutablePath(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.Symlink(filepath.Dir(self), filepath.Join(root, "linux")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTFLOW_RUNNER_BINARY", filepath.Join(root, "{os}", filepath.Base(self)))
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	d, err := New(Config{ServerURL: server.URL, BootstrapToken: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.checkDockerRunnerHelperUpdate(t.Context(), false); err == nil || !strings.Contains(err.Error(), "aliases the host Runner") {
		t.Fatalf("conflicting helper path was not rejected: %v", err)
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("updater requested an artifact for a conflicting path, requests = %d", got)
	}
}

func TestDockerRunnerHelperUpdateRejectsIncompatibleBinary(t *testing.T) {
	otherArch := "arm64"
	if runtime.GOARCH == otherArch {
		otherArch = "amd64"
	}
	complete := dockerRunnerFixtureLinuxBinary(runtime.GOARCH, "full program")
	for _, tc := range []struct{ name, payload string }{
		{name: "macOS_binary", payload: "\xcf\xfa\xed\xfemacOS runner"},
		{name: "wrong_Linux_architecture", payload: dockerRunnerFixtureLinuxBinary(otherArch, "wrong architecture")},
		{name: "script_wrapper", payload: "#!/bin/sh\nexit 0\n"},
		{name: "only_ELF_header", payload: complete[:64]},
		{name: "truncated_load_segment", payload: complete[:len(complete)-1]},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hash := sha256.Sum256([]byte(tc.payload))
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if req.URL.Query().Get("metadata") == "1" {
					fmt.Fprintf(w, `{"sha256":%q}`, hex.EncodeToString(hash[:]))
					return
				}
				fmt.Fprint(w, tc.payload)
			}))
			defer server.Close()
			d, err := New(Config{ServerURL: server.URL, BootstrapToken: "fixture"})
			if err != nil {
				t.Fatal(err)
			}
			bin := t.TempDir()
			t.Setenv("AGENTFLOW_RUNNER_BINARY", filepath.Join(bin, "agentflow-runner-{os}-{arch}"))
			helper := filepath.Join(bin, "agentflow-runner-linux-"+runtime.GOARCH)
			original := dockerRunnerFixtureLinuxBinary(runtime.GOARCH, "working helper")
			if err := os.WriteFile(helper, []byte(original), 0o755); err != nil {
				t.Fatal(err)
			}
			updateErr := d.checkDockerRunnerHelperUpdate(t.Context(), false)
			got, err := os.ReadFile(helper)
			if err != nil || string(got) != original {
				t.Fatalf("incompatible download replaced the helper: %q, read error %v, update error %v", got, err, updateErr)
			}
			if updateErr == nil || !strings.Contains(updateErr.Error(), "Linux/"+runtime.GOARCH) {
				t.Fatalf("expected a platform validation error, got %v", updateErr)
			}
		})
	}
}

func TestDockerRunnerHelperDownloadDoesNotBlockMacOSStartup(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS Runner downloads the Linux helper at startup")
	}
	t.Run("readable_ELF", func(t *testing.T) {
		testDockerRunnerHelperDuringStartup(t, dockerRunnerFixtureLinuxBinary(runtime.GOARCH, "working helper"), 0o644)
	})
	t.Run("local_script_wrapper", func(t *testing.T) {
		testDockerRunnerHelperDuringStartup(t, "#!/bin/sh\nexec /usr/local/bin/agentflow-runner \"$@\"\n", 0o755)
	})
}

func testDockerRunnerHelperDuringStartup(t *testing.T, original string, mode os.FileMode) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("PATH", "/usr/bin:/bin")
	t.Setenv("AGENTFLOW_RUNNER_BINARY", filepath.Join(root, "agentflow-runner-{os}-{arch}"))
	helper := filepath.Join(root, "agentflow-runner-linux-"+runtime.GOARCH)
	if err := os.WriteFile(helper, []byte(original), mode); err != nil {
		t.Fatal(err)
	}
	updated := dockerRunnerFixtureLinuxBinary(runtime.GOARCH, "updated helper")
	hash := sha256.Sum256([]byte(updated))
	helperRequested := make(chan struct{}, 1)
	allowMetadata := make(chan struct{})
	heartbeat := make(chan struct{}, 1)
	controlHello := make(chan struct{}, 1)
	claim := make(chan struct{}, 1)
	signal := func(ch chan struct{}) {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	upgrader := websocket.Upgrader{}
	ctx, cancel := context.WithCancel(t.Context())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/runners/agentflow-runner":
			if req.URL.Query().Get("os") != "linux" || req.URL.Query().Get("arch") != runtime.GOARCH {
				http.NotFound(w, req)
				return
			}
			if req.URL.Query().Get("metadata") == "1" {
				signal(helperRequested)
				select {
				case <-allowMetadata:
					fmt.Fprintf(w, `{"sha256":%q}`, hex.EncodeToString(hash[:]))
				case <-req.Context().Done():
				}
				return
			}
			fmt.Fprint(w, updated)
		case "/runners/heartbeat":
			signal(heartbeat)
			fmt.Fprint(w, `{}`)
		case "/runners/connect":
			conn, err := upgrader.Upgrade(w, req, nil)
			if err != nil {
				return
			}
			defer conn.Close()
			var hello controlMessage
			if conn.ReadJSON(&hello) == nil && hello.Type == "hello" {
				signal(controlHello)
				_, _, _ = conn.NextReader()
			}
		case "/runners/tasks/claim":
			signal(claim)
			<-ctx.Done()
		case "/runners/disconnect":
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, req)
		}
	}))
	defer func() { cancel(); server.Close() }()
	d, err := New(Config{ServerURL: server.URL, BootstrapToken: "fixture", StatePath: filepath.Join(root, "state.json")})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.saveState(daemonState{RunnerID: "runner", SessionToken: "session", ExpiresAt: time.Now().Add(24 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("run: %v", err)
		}
	}()
	wait := func(ch <-chan struct{}, event string) {
		t.Helper()
		select {
		case <-ch:
		case <-time.After(5 * time.Second):
			t.Fatalf("did not observe %s before the Linux helper download completed", event)
		}
	}
	wait(helperRequested, "Linux helper metadata request")
	wait(heartbeat, "heartbeat")
	wait(controlHello, "control hello")
	wait(claim, "task polling")
	workspaceCtx, cancelWorkspace := context.WithTimeout(ctx, time.Second)
	defer cancelWorkspace()
	workspace := t.TempDir()
	if _, err := flowruntime.PrepareMCPCall(d.withDockerRunnerHelperWait(workspaceCtx), dockerHelperFixtureSandbox{}, workspace, nil); err != nil {
		t.Fatalf("existing readable helper should be available while metadata is pending: %v", err)
	}
	workspaceHelper := filepath.Join(workspace, ".agentflow", "runtime", "bin", "agentflow-helper")
	if got, err := os.ReadFile(workspaceHelper); err != nil || string(got) != original {
		t.Fatalf("workspace helper = %q, %v", got, err)
	}
	if info, err := os.Stat(workspaceHelper); err != nil || info.Mode().Perm()&0o100 == 0 {
		t.Fatalf("workspace helper did not receive executable permissions: %v", err)
	}
	close(allowMetadata)
	waitFor(t, 5*time.Second, "helper update", func() bool {
		got, err := os.ReadFile(helper)
		return err == nil && string(got) == updated
	})
}

type dockerHelperFixtureSandbox struct{ flowruntime.Sandbox }

func (dockerHelperFixtureSandbox) Driver() string { return "docker" }

func TestDockerRunnerHelperRescanRetriesFailedVersionCheck(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("PATH", "/usr/bin:/bin")
	t.Setenv("AGENTFLOW_RUNNER_BINARY", filepath.Join(root, "agentflow-runner-{os}-{arch}"))
	helper := filepath.Join(root, "agentflow-runner-linux-"+runtime.GOARCH)
	if err := os.WriteFile(helper, []byte(dockerRunnerFixtureLinuxBinary(runtime.GOARCH, "old version")), 0o755); err != nil {
		t.Fatal(err)
	}
	updated := dockerRunnerFixtureLinuxBinary(runtime.GOARCH, "updated version")
	hash := sha256.Sum256([]byte(updated))
	var metadataRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Query().Get("metadata") == "1" {
			if metadataRequests.Add(1) == 1 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			fmt.Fprintf(w, `{"sha256":%q}`, hex.EncodeToString(hash[:]))
			return
		}
		fmt.Fprint(w, updated)
	}))
	defer server.Close()
	d, err := New(Config{ServerURL: server.URL, BootstrapToken: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	d.dockerHelperSyncDone = make(chan struct{})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		d.rescanLoop(ctx, 50*time.Millisecond)
	}()
	defer func() { cancel(); <-done }()
	waitFor(t, 5*time.Second, "rescan to replace the old helper after a failed startup sync", func() bool {
		data, err := os.ReadFile(helper)
		return err == nil && string(data) == updated
	})
	if got := metadataRequests.Load(); got != 2 {
		t.Fatalf("metadata requests = %d, want a failed check and one retry", got)
	}
}

func TestDockerRunnerHelperSlowDownloadOutlivesWorkspaceWait(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AGENTFLOW_RUNNER_BINARY", filepath.Join(root, "agentflow-runner-{os}-{arch}"))
	helper := filepath.Join(root, "agentflow-runner-linux-"+runtime.GOARCH)
	binary := dockerRunnerFixtureLinuxBinary(runtime.GOARCH, "complete slow download")
	hash := sha256.Sum256([]byte(binary))
	downloading := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Query().Get("metadata") == "1" {
			fmt.Fprintf(w, `{"sha256":%q}`, hex.EncodeToString(hash[:]))
			return
		}
		fmt.Fprint(w, binary[:64])
		w.(http.Flusher).Flush()
		close(downloading)
		// Keep the transfer active beyond the old shared 30-second deadline.
		timer := time.NewTimer(31 * time.Second)
		defer timer.Stop()
		select {
		case <-timer.C:
			fmt.Fprint(w, binary[64:])
		case <-req.Context().Done():
		}
	}))
	defer server.Close()
	d, err := New(Config{ServerURL: server.URL, BootstrapToken: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	d.dockerHelperSyncDone = make(chan struct{})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		d.rescanLoop(ctx, time.Hour)
	}()
	defer func() { cancel(); <-done }()
	select {
	case <-downloading:
	case <-time.After(5 * time.Second):
		t.Fatal("helper download did not start")
	}
	if err := d.waitForDockerRunnerHelper(ctx, helper); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("workspace wait = %v, want its own deadline", err)
	}
	select {
	case <-d.dockerHelperSyncDone:
	case <-time.After(40 * time.Second):
		t.Fatal("helper sync did not complete")
	}
	if data, err := os.ReadFile(helper); err != nil || string(data) != binary {
		t.Fatalf("slow transfer did not publish the verified helper: %q, %v", data, err)
	}
}

func dockerRunnerFixtureLinuxBinary(arch, payload string) string {
	const elfHeaderSize, programHeaderSize = 64, 56
	header := make([]byte, elfHeaderSize+programHeaderSize)
	copy(header, "\x7fELF\x02\x01\x01\x00")
	machine := elf.EM_X86_64
	if arch == "arm64" {
		machine = elf.EM_AARCH64
	}
	binary.LittleEndian.PutUint16(header[16:], uint16(elf.ET_EXEC))
	binary.LittleEndian.PutUint16(header[18:], uint16(machine))
	binary.LittleEndian.PutUint32(header[20:], uint32(elf.EV_CURRENT))
	binary.LittleEndian.PutUint64(header[32:], elfHeaderSize)
	binary.LittleEndian.PutUint16(header[52:], elfHeaderSize)
	binary.LittleEndian.PutUint16(header[54:], programHeaderSize)
	binary.LittleEndian.PutUint16(header[56:], 1)
	program := header[elfHeaderSize:]
	binary.LittleEndian.PutUint32(program, uint32(elf.PT_LOAD))
	binary.LittleEndian.PutUint32(program[4:], uint32(elf.PF_R|elf.PF_X))
	size := uint64(len(header) + len(payload))
	binary.LittleEndian.PutUint64(program[32:], size)
	binary.LittleEndian.PutUint64(program[40:], size)
	return string(header) + payload
}

func TestRunnerUpdateDefersWhileWorkspaceRequestIsActive(t *testing.T) {
	const updated = "new runner"
	hash := sha256.Sum256([]byte(updated))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("metadata") == "1" {
			fmt.Fprintf(w, `{"sha256":%q}`, hex.EncodeToString(hash[:]))
			return
		}
		_, _ = w.Write([]byte(updated))
	}))
	defer server.Close()
	d, err := New(Config{ServerURL: server.URL, BootstrapToken: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "agentflow-runner")
	if err := os.WriteFile(binary, []byte("old runner"), 0700); err != nil {
		t.Fatal(err)
	}
	d.updateMu.RLock()
	installed, err := d.installRunnerUpdate(context.Background(), binary)
	d.updateMu.RUnlock()
	if installed || !errors.Is(err, errRunnerUpdateBusy) || d.updateReady.Load() {
		t.Fatalf("update while workspace is active: installed=%t err=%v", installed, err)
	}
	if data, err := os.ReadFile(binary); err != nil || string(data) != "old runner" {
		t.Fatalf("old executable was not preserved: %v", err)
	}
	if installed, err = d.installRunnerUpdate(context.Background(), binary); err != nil || !installed {
		t.Fatalf("idle update: installed=%t err=%v", installed, err)
	}
}
