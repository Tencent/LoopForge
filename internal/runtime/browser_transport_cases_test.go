package runtime

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"agentflow/internal/domain"

	"github.com/gorilla/websocket"
)

func TestWebSocketCDPTransportAndHostEndpoint(t *testing.T) {
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/json/version" {
			w.WriteHeader(http.StatusOK)
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			kind, body, err := conn.ReadMessage()
			if err != nil {
				return
			}
			_ = conn.WriteMessage(kind, body)
		}
	}))
	defer server.Close()
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	transport, err := dialWebSocketCDP(context.Background(), wsURL)
	if err != nil {
		t.Fatal(err)
	}
	if err := transport.write([]byte(`{"id":1}`)); err != nil {
		t.Fatal(err)
	}
	if body, err := transport.read(); err != nil || string(body) != `{"id":1}` {
		t.Fatalf("body=%q err=%v", body, err)
	}
	if err := transport.close(); err != nil {
		t.Fatal(err)
	}
	_, portText, _ := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	port, _ := strconv.Atoi(portText)
	runID := "cvR-host-endpoint"
	dir := hostBrowserDir(runID)
	_ = os.RemoveAll(dir)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	record, _ := json.Marshal(hostBrowserRecord{PID: os.Getpid(), Owner: os.Getpid(), WebSocket: wsURL, Port: port, Workspace: t.TempDir()})
	if err := os.WriteFile(filepath.Join(dir, "endpoint.json"), record, 0o600); err != nil {
		t.Fatal(err)
	}
	endpoint, ok := lookupBrowserEndpoint(context.Background(), runID)
	if !ok || endpoint.Kind != "local" || !BrowserRunning(context.Background(), runID) {
		t.Fatalf("endpoint=%+v ok=%v", endpoint, ok)
	}
	if !hostDevToolsAlive(context.Background(), port) || hostDevToolsAlive(context.Background(), 0) {
		t.Fatal("devtools health")
	}
	view, err := openBrowserView(context.Background(), runID)
	if err != nil || view == nil {
		t.Fatalf("open view=%v err=%v", view, err)
	}
	if again, err := openBrowserView(context.Background(), runID); err != nil || again != view {
		t.Fatalf("reopen=%v err=%v", again, err)
	}
	view.client.close()
	if _, err := browserImageName(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTFLOW_BROWSER_IMAGE", "custom-browser")
	if image, err := browserImageName(); err != nil || image != "custom-browser" {
		t.Fatalf("image=%q err=%v", image, err)
	}
}

func TestWaitDevToolsActivePortBranches(t *testing.T) {
	profile := t.TempDir()
	exited := make(chan struct{})
	if err := os.WriteFile(filepath.Join(profile, "DevToolsActivePort"), []byte("9222\n/devtools/browser/id\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	port, path, err := waitDevToolsActivePort(context.Background(), profile, exited)
	if err != nil || port != 9222 || path != "/devtools/browser/id" {
		t.Fatalf("port=%d path=%q err=%v", port, path, err)
	}
	_ = os.Remove(filepath.Join(profile, "DevToolsActivePort"))
	close(exited)
	if _, _, err := waitDevToolsActivePort(context.Background(), profile, exited); err == nil {
		t.Fatal("exited browser accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := waitDevToolsActivePort(ctx, t.TempDir(), make(chan struct{})); err == nil {
		t.Fatal("canceled wait accepted")
	}
}

func TestEnsureHostBrowserWithFakeChromium(t *testing.T) {
	bin := t.TempDir()
	chromium := filepath.Join(bin, "chromium")
	script := `#!/bin/sh
profile=
for arg in "$@"; do
  case "$arg" in --user-data-dir=*) profile="${arg#*=}";; esac
done
mkdir -p "$profile"
printf '9223\n/devtools/browser/cvr\n' > "$profile/DevToolsActivePort"
exec sleep 30
`
	if err := os.WriteFile(chromium, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "playwright-mcp"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTFLOW_CHROMIUM", chromium)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	workspace := t.TempDir()
	runID := "cvR-host-browser"
	server, err := ensureHostBrowser(context.Background(), workspace, runID)
	if err != nil || server.Transport != "stdio" || server.Command == "" {
		t.Fatalf("server=%+v err=%v", server, err)
	}
	again, err := ensureHostBrowser(context.Background(), workspace, runID)
	if err != nil || again.Command != server.Command {
		t.Fatalf("again=%+v err=%v", again, err)
	}
	local := &localSandbox{id: "x", workspace: workspace}
	if got := EnsureSandboxBrowser(context.Background(), local, workspace, runID, nil); got == nil {
		t.Fatal("local sandbox browser unavailable")
	}
	stopHostBrowsers(workspace)
}

func TestDockerBrowserImageAndSidecarWithFakeCLI(t *testing.T) {
	workspace := t.TempDir()
	cvRInstallFakeDocker(t, workspace)
	t.Setenv("AGENTFLOW_BROWSER_IMAGE", "cvR-browser-image")
	image, err := EnsureBrowserImage(context.Background())
	if err != nil || image != "cvR-browser-image" {
		t.Fatalf("image=%q err=%v", image, err)
	}
	sb := &dockerSandbox{name: "box", workspace: workspace, started: true}
	var events []string
	server, err := sb.ensureBrowser(context.Background(), "run", func(event domain.TelemetryEvent) { events = append(events, event.Name) })
	if err != nil || server.Transport != "http" || server.URL == "" {
		t.Fatalf("server=%+v events=%v err=%v", server, events, err)
	}
	if err := waitDockerBrowserReady(context.Background(), "box-browser"); err != nil {
		t.Fatal(err)
	}
	long := []byte(strings.Repeat("x", 2100))
	if got := tailOutput(long); len(got) != 2003 || !strings.HasPrefix(got, "…") {
		t.Fatalf("tail len=%d", len(got))
	}
}

func TestDockerCDPPipeTransport(t *testing.T) {
	bin := t.TempDir()
	docker := filepath.Join(bin, "docker")
	script := `#!/bin/sh
printf '%s\n' '{"relay":"ready"}'
while IFS= read -r line; do
  printf '%s\n' "$line"
done
`
	if err := os.WriteFile(docker, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	transport, err := dialDockerCDP(context.Background(), "box")
	if err != nil {
		t.Fatal(err)
	}
	if err := transport.write([]byte(`{"id":7}`)); err != nil {
		t.Fatal(err)
	}
	if body, err := transport.read(); err != nil || string(body) != `{"id":7}` {
		t.Fatalf("body=%q err=%v", body, err)
	}
	if err := transport.close(); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureEmbeddedBrowserImageBuild(t *testing.T) {
	workspace := t.TempDir()
	cvRInstallFakeDocker(t, workspace)
	t.Setenv("AGENTFLOW_BROWSER_IMAGE", "")
	t.Setenv("AGENTFLOW_BROWSER_NPM_REGISTRY", "https://registry.example")
	image, err := EnsureBrowserImage(context.Background())
	if err != nil || !strings.HasPrefix(image, "agentflow-browser-") {
		t.Fatalf("image=%q err=%v", image, err)
	}
}
