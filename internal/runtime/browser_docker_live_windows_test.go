package runtime

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestWindowsLiveDockerBrowserEntrypoint(t *testing.T) {
	if os.Getenv("LOOPFORGE_WINDOWS_DOCKER_LIVE") != "1" {
		t.Skip("requires the actual local Linux Docker daemon")
	}
	if os.Getenv("AGENTFLOW_ISOLATED_IMAGE") == "" {
		t.Fatal("AGENTFLOW_ISOLATED_IMAGE must name the prepared acceptance image")
	}
	// Exercise the actual embedded build context and ENTRYPOINT; a prebuilt
	// helper image would hide CRLF shebang failures from Windows Go builds.
	t.Setenv("AGENTFLOW_BROWSER_IMAGE", "")
	ctx, cancel := context.WithTimeout(context.Background(), 17*time.Minute)
	defer cancel()
	workspace := filepath.Join(t.TempDir(), "browser workspace ' with spaces")
	if err := os.MkdirAll(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("agentflow-browser-live-qa-%d", time.Now().UnixNano())
	sandbox := &dockerSandbox{name: name, workspace: workspace, profile: domain.SandboxProfile{Driver: "docker"},
		dockerfile: domain.DockerfileDefinition{Content: IsolatedDockerfile}}
	t.Cleanup(func() {
		stopCtx, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		_ = sandbox.Close(stopCtx)
	})
	server, err := sandbox.ensureBrowser(ctx, name, nil)
	if err != nil {
		t.Fatal(err)
	}
	if server.ID != BrowserMCPServerID {
		t.Fatal("production browser sidecar did not expose Playwright MCP")
	}
	probe := `fetch('http://127.0.0.1:9222/json/version').then(r=>r.json()).then(v=>{if(!v.Browser||!v.webSocketDebuggerUrl)process.exit(1);console.log(v.Browser)}).catch(()=>process.exit(1))`
	output, err := exec.CommandContext(ctx, "docker", "exec", dockerBrowserContainerName(name), "node", "-e", probe).CombinedOutput()
	if err != nil || !strings.Contains(string(output), "Chrome/") {
		t.Fatalf("real Chromium did not start through the embedded entrypoint: %s: %v", output, err)
	}
}
