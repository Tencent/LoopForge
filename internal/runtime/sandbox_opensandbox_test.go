package runtime

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"agentflow/internal/domain"
	opensandbox "github.com/alibaba/OpenSandbox/sdks/sandbox/go"
)

func TestOpenSandboxReusesWorkspaceAndReleasesInstance(t *testing.T) {
	t.Setenv("AGENTFLOW_ISOLATED_IMAGE", "")
	root := t.TempDir()
	workspace := filepath.Join(root, "run-a")
	var creates, commands, uploads, downloads, deletes atomic.Int32
	var archive atomic.Pointer[[]byte]
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if !strings.HasPrefix(r.URL.Path, "/v1/") && r.Header.Get("OPEN-SANDBOX-API-KEY") != "" {
			t.Error("lifecycle API key was forwarded to the sandbox")
		}
		switch {
		case r.URL.Path == "/v1/sandboxes" && r.Method == http.MethodPost:
			if r.Header.Get("OPEN-SANDBOX-API-KEY") != "test-control-key" {
				t.Error("missing lifecycle API key")
			}
			var request opensandbox.CreateSandboxRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode sandbox request: %v", err)
			}
			if len(request.Volumes) != 0 || request.Image == nil || request.Image.URI != defaultOpenSandboxImage {
				t.Errorf("expected built-in image and no workspace mount, got image %+v, volumes %+v", request.Image, request.Volumes)
			}
			creates.Add(1)
			w.WriteHeader(http.StatusAccepted)
			fmt.Fprint(w, `{"id":"sandbox-a","status":{"state":"Pending"}}`)
		case r.URL.Path == "/v1/sandboxes/sandbox-a" && r.Method == http.MethodGet:
			fmt.Fprint(w, `{"id":"sandbox-a","status":{"state":"Running"}}`)
		case r.URL.Path == "/v1/sandboxes/sandbox-a/endpoints/44772":
			fmt.Fprintf(w, `{"endpoint":%q}`, "http://"+r.Host)
		case r.URL.Path == "/ping":
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/files/upload" && r.Method == http.MethodPost:
			metadata, _, err := r.FormFile("metadata")
			if err != nil {
				t.Error(err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			defer metadata.Close()
			var permissions opensandbox.FileMetadata
			if err := json.NewDecoder(metadata).Decode(&permissions); err != nil || permissions.Mode != 600 {
				t.Errorf("upload must set owner-only octal permissions: %+v, error %v", permissions, err)
			}
			file, _, err := r.FormFile("file")
			if err != nil {
				t.Error(err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			defer file.Close()
			content, err := io.ReadAll(file)
			if err != nil || len(content) == 0 {
				t.Errorf("expected a workspace archive: %v", err)
			}
			archive.Store(&content)
			uploads.Add(1)
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/files/download" && r.Method == http.MethodGet:
			content := archive.Load()
			if content == nil {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			downloads.Add(1)
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(*content)
		case r.URL.Path == "/files" && r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/command" && r.Method == http.MethodPost:
			var request opensandbox.RunCommandRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode command request: %v", err)
			}
			w.Header().Set("Content-Type", "text/event-stream")
			if len(request.Argv) >= 4 && (request.Argv[3] == "loopforge-workspace-import" || request.Argv[3] == "loopforge-workspace-export") {
				fmt.Fprint(w, "data: {\"type\":\"execution_complete\",\"exit_code\":0}\n\n")
				return
			}
			if request.Cwd != workspace || len(request.Argv) != 2 || request.Argv[1] != "literal argument" {
				t.Errorf("unexpected command request: %+v", request)
			}
			commands.Add(1)
			fmt.Fprint(w, "data: {\"type\":\"init\",\"text\":\"command-a\"}\n\n"+
				"data: {\"type\":\"stdout\",\"text\":\"remote \"}\n\n"+
				"data: {\"type\":\"stdout\",\"text\":\"output\\n\"}\n\n"+
				"data: {\"type\":\"execution_complete\",\"exit_code\":0}\n\n")
		case r.URL.Path == "/v1/sandboxes/sandbox-a" && r.Method == http.MethodDelete:
			deletes.Add(1)
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	manager := NewManager(root)
	config, err := NormalizeOpenSandboxConnection(domain.OpenSandboxConnection{URL: server.URL + "/v1", APIKey: "test-control-key"})
	if err != nil {
		t.Fatal(err)
	}
	manager.SetOpenSandboxSource(func(_ context.Context, id, project string) (domain.OpenSandboxConnection, error) {
		if id != "cluster-a" || project != "project-a" {
			t.Errorf("unexpected placement: %q in %q", id, project)
		}
		return config, nil
	})
	ctx := context.Background()
	if backend, err := manager.openSandboxCluster(ctx, SandboxResources{}); err != nil || backend != nil {
		t.Fatal("a machine placement must not resolve a cluster")
	}
	sandbox, err := manager.AcquireRun(ctx, "run-a", domain.SandboxProfile{Driver: domain.SandboxDriverNative}, SandboxRequirements{}, RunCredentials{}, SandboxResources{ClusterID: "cluster-a", ProjectID: "project-a"})
	if err != nil {
		t.Fatal(err)
	}
	defer sandbox.Close(ctx)
	for range 2 {
		out, err := sandbox.Exec(ctx, CommandSpec{Argv: []string{"printf", "literal argument"}}, nil)
		if err != nil || strings.TrimSpace(out) != "remote output" {
			t.Fatalf("remote exec output %q, error %v", out, err)
		}
	}
	if id, err := SandboxEnvironmentIdentity(ctx, sandbox); err != nil || id != "opensandbox:sandbox-a" {
		t.Fatalf("execution environment identity %q, error %v", id, err)
	}
	if err := sandbox.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if creates.Load() != 1 || commands.Load() != 2 || uploads.Load() != 2 || downloads.Load() != 2 || deletes.Load() != 1 {
		t.Fatalf("got %d creates, %d commands, %d uploads, %d downloads and %d deletes",
			creates.Load(), commands.Load(), uploads.Load(), downloads.Load(), deletes.Load())
	}
}

func TestOpenSandboxWorkspaceMergePreservesConcurrentEdits(t *testing.T) {
	parent := t.TempDir()
	localPath, stagePath := filepath.Join(parent, "local"), filepath.Join(parent, "stage")
	for _, path := range []string{localPath, stagePath} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	write := func(dir, path, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, path), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	assertFile := func(dir, path, want string) {
		t.Helper()
		got, err := os.ReadFile(filepath.Join(dir, path))
		if err != nil || string(got) != want {
			t.Fatalf("%s = %q, want %q, err %v", path, got, want, err)
		}
	}
	read := func(path string) map[string]workspaceTransferEntry {
		t.Helper()
		base, err := os.OpenRoot(path)
		if err != nil {
			t.Fatal(err)
		}
		defer base.Close()
		entries, err := readWorkspaceTransfer(t.Context(), base, nil, workspaceTransferSkips)
		if err != nil {
			t.Fatal(err)
		}
		return entries
	}
	publish := func(before map[string]workspaceTransferEntry) error {
		t.Helper()
		stage, err := os.OpenRoot(stagePath)
		if err != nil {
			t.Fatal(err)
		}
		defer stage.Close()
		return publishOpenSandboxWorkspace(t.Context(), localPath, stage, before, workspaceTransferSkips)
	}
	write(localPath, "original.txt", "start")
	before := read(localPath)
	write(localPath, "original.txt", "local edit")
	write(stagePath, "original.txt", "start")
	write(stagePath, "result.txt", "remote result")
	if err := publish(before); err != nil {
		t.Fatal(err)
	}
	assertFile(localPath, "original.txt", "local edit")
	assertFile(localPath, "result.txt", "remote result")

	before = read(localPath)
	write(localPath, "original.txt", "next local edit")
	write(stagePath, "original.txt", "conflicting remote edit")
	if err := publish(before); err == nil {
		t.Fatal("expected a conflict")
	}
	assertFile(localPath, "original.txt", "next local edit")
	assertFile(localPath, "result.txt", "remote result")
}

func TestOpenSandboxWorkspaceTransferSkipsCodexScratch(t *testing.T) {
	base, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	scratch := ".agentflow/private-home/script/.codex/tmp"
	if err := base.MkdirAll(scratch, 0700); err != nil {
		t.Fatal(err)
	}
	link := scratch + "/binary"
	if err := base.Symlink("/usr/local/bin/codex", link); err != nil {
		t.Fatal(err)
	}
	entries, err := readWorkspaceTransfer(t.Context(), base, nil, workspaceTransferSkips)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := entries[scratch]; exists {
		t.Fatal("CLI scratch files must stay out of the upload")
	}

	var archive bytes.Buffer
	zipped := gzip.NewWriter(&archive)
	writer := tar.NewWriter(zipped)
	for _, header := range []tar.Header{
		{Name: link, Typeflag: tar.TypeSymlink, Linkname: "/usr/local/bin/codex"},
		{Name: "result.txt", Typeflag: tar.TypeReg, Mode: 0600},
	} {
		if err := writer.WriteHeader(&header); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zipped.Close(); err != nil {
		t.Fatal(err)
	}
	stage, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer stage.Close()
	if _, err := unpackWorkspaceArchive(t.Context(), stage, &archive, workspaceTransferSkips); err != nil {
		t.Fatal(err)
	}
	if _, err := stage.Stat("result.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := stage.Lstat(link); !os.IsNotExist(err) {
		t.Fatalf("CLI scratch path was restored: %v", err)
	}
}
