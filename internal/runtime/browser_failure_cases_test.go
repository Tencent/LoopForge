package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
)

// browserFakeDocker installs a docker CLI stub for the sidecar image cases and
// points the given markers at files in the test's temp dir.
func browserFakeDocker(t *testing.T, script string) (buildStarted, imageReady string) {
	t.Helper()
	bin := t.TempDir()
	dir := t.TempDir()
	buildStarted = filepath.Join(dir, "build-started")
	imageReady = filepath.Join(dir, "image-ready")
	path := filepath.Join(bin, "docker")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CVR_BROWSER_BUILD_STARTED", buildStarted)
	t.Setenv("CVR_BROWSER_IMAGE_READY", imageReady)
	t.Setenv("AGENTFLOW_BROWSER_IMAGE", "")
	t.Setenv("AGENTFLOW_BROWSER", "")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return buildStarted, imageReady
}

// TestAgentBrowserBuildOutlivesRequestTimeout covers an interactive panel
// request timing out while the first sidecar image build is still running: the
// build must run to completion instead of being cancelled with the request, so
// the next attempt finds the image instead of rebuilding from scratch.
func TestAgentBrowserBuildOutlivesRequestTimeout(t *testing.T) {
	buildStarted, imageReady := browserFakeDocker(t, `#!/bin/sh
set -u
case "${1-}" in
  build)
    : > "$CVR_BROWSER_BUILD_STARTED"
    sleep 1
    : > "$CVR_BROWSER_IMAGE_READY"
    exit 0 ;;
  image)
    [ "${2-}" = inspect ] || exit 0
    [ -f "$CVR_BROWSER_IMAGE_READY" ] && exit 0
    exit 1 ;;
  inspect)
    [ "${2-}" = -f ] && printf 'false||\n'
    exit 0 ;;
esac
exit 0
`)
	workspace := t.TempDir()
	sb := &dockerSandbox{name: "box", workspace: workspace, started: true}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := EnsureSandboxBrowserWithError(ctx, sb, workspace, "run", nil); err == nil {
		t.Fatal("want the timed-out request to fail")
	} else if !strings.Contains(err.Error(), "镜像已就绪") {
		t.Fatalf("want a retry hint after the build completes, got: %v", err)
	}
	if _, err := os.Stat(buildStarted); err != nil {
		t.Fatalf("image build did not start: %v", err)
	}
	if _, err := os.Stat(imageReady); err != nil {
		t.Fatalf("image build was cancelled with the request: %v", err)
	}
	server, err := EnsureSandboxBrowserWithError(context.Background(), sb, workspace, "run", nil)
	if err != nil || server == nil {
		t.Fatalf("second attempt after the build: server=%v err=%v", server, err)
	}
}

// TestEnsureSandboxBrowserWithErrorKeepsBuildFailure covers the panel path
// surfacing the real sidecar failure instead of a generic unsupported
// environment message; the node path keeps its failure-tolerant helper.
func TestEnsureSandboxBrowserWithErrorKeepsBuildFailure(t *testing.T) {
	browserFakeDocker(t, `#!/bin/sh
set -u
case "${1-}" in
  build)
    echo "deb.debian.org: connection timed out" >&2
    exit 1 ;;
  image)
    [ "${2-}" = inspect ] && exit 1
    exit 0 ;;
esac
exit 0
`)
	workspace := t.TempDir()
	sb := &dockerSandbox{name: "box", workspace: workspace, started: true}
	server, err := EnsureSandboxBrowserWithError(context.Background(), sb, workspace, "run", nil)
	if server != nil || err == nil {
		t.Fatalf("server=%v err=%v, want a build failure", server, err)
	}
	if !strings.Contains(err.Error(), "docker build browser image") || !strings.Contains(err.Error(), "connection timed out") {
		t.Fatalf("build failure lost its cause: %v", err)
	}
	if got := EnsureSandboxBrowser(context.Background(), sb, workspace, "run", nil); got != nil {
		t.Fatalf("failure-tolerant helper returned %v, want nil", got)
	}
}

// TestEnsureSandboxBrowserWithErrorDisabled keeps the disabled deployment and
// missing workspace cases explicit for interactive callers.
func TestEnsureSandboxBrowserWithErrorDisabled(t *testing.T) {
	t.Setenv("AGENTFLOW_BROWSER", "off")
	if _, err := EnsureSandboxBrowserWithError(context.Background(), nil, "", "run", nil); err != ErrBrowserDisabled {
		t.Fatalf("want ErrBrowserDisabled, got %v", err)
	}
	t.Setenv("AGENTFLOW_BROWSER", "")
	sink := func(domain.TelemetryEvent) {}
	if _, err := EnsureSandboxBrowserWithError(context.Background(), nil, "", " ", sink); err == nil {
		t.Fatal("want a missing-workspace error")
	}
}
