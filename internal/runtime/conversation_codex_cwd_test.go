package runtime

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestConversationCodexStdinUsesWorkspaceCWD(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 unavailable")
	}
	for _, workspaceMode := range []bool{true, false} {
		name := "platform"
		if workspaceMode {
			name = "seatbelt workspace"
		}
		t.Run(name, func(t *testing.T) {
			root, workspace, binDir := t.TempDir(), t.TempDir(), t.TempDir()
			cli := filepath.Join(binDir, "codex-fixture")
			if err := os.WriteFile(cli, []byte("#!/bin/sh\nprintf 'PROCESS_CWD=%s|' \"$(pwd -P)\"\nwhile IFS= read -r frame; do printf '%s|' \"$frame\"; done\n"), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("AGENTFLOW_CODEX_BIN", cli)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			var spec *domain.RunnerSandboxSpec
			var factories []ConversationSandboxFactory
			if workspaceMode {
				// Replace only the OS launcher, so this exercises the real
				// seatbelt command construction and stdin route on Linux too.
				launcher := filepath.Join(binDir, "sandbox-exec-fixture")
				if err := os.WriteFile(launcher, []byte("#!/bin/sh\nshift 2\nexec \"$@\"\n"), 0700); err != nil {
					t.Fatal(err)
				}
				spec = &domain.RunnerSandboxSpec{Profile: domain.SandboxProfile{Driver: domain.SandboxDriverSeatbelt}}
				factories = append(factories, func(ctx context.Context, nativeRoot string) (Sandbox, func(), error) {
					sb := &processSandbox{id: "stdin-cwd", driver: domain.SandboxDriverSeatbelt, binary: launcher, workspace: workspace}
					if err := ConfigureConversationSandbox(ctx, sb, nativeRoot); err != nil {
						return nil, nil, err
					}
					return sb, func() {}, nil
				})
			}
			sourceRoot := "/api/conversation"
			pool := &ConversationProcessPool{}
			started, err := pool.Handle(ctx, root, workspace, spec, ConversationProcessRequest{
				Operation: "start", SourceRoot: sourceRoot,
				Command: &ConversationCommand{Provider: "codex", Argv: []string{cli}, Interactive: true},
			}, nil, factories...)
			if err != nil {
				t.Fatal(err)
			}
			for i, method := range []string{"thread/start", "thread/resume"} {
				request, _ := json.Marshal(map[string]any{
					"id": i + 1, "method": method,
					"params": map[string]any{"cwd": sourceRoot + "/ws", "developerInstructions": "Keep the cache path " + sourceRoot + "/ws/history.txt", "config": map[string]any{"file": sourceRoot + "/ws/config.json"}},
				})
				if _, err = pool.Handle(ctx, root, workspace, spec, ConversationProcessRequest{Operation: "write", ID: started.ID, SourceRoot: sourceRoot, Data: string(request) + "\n"}, nil); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = pool.Handle(ctx, root, workspace, spec, ConversationProcessRequest{Operation: "write", ID: started.ID, EOF: true}, nil); err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(strings.TrimSuffix(conversationPoll(t, pool, ctx, root, started.ID), "|"), "|")
			wantCWD := filepath.Join(root, "ws")
			if workspaceMode {
				wantCWD = workspace
			}
			resolved, err := filepath.EvalSymlinks(wantCWD)
			if err != nil {
				t.Fatal(err)
			}
			if len(lines) != 3 || lines[0] != "PROCESS_CWD="+resolved {
				t.Fatalf("unexpected process cwd or protocol frames: %q", lines)
			}
			for _, line := range lines[1:] {
				var request struct {
					Method string `json:"method"`
					Params struct {
						CWD          string            `json:"cwd"`
						Instructions string            `json:"developerInstructions"`
						Config       map[string]string `json:"config"`
					} `json:"params"`
				}
				if err := json.Unmarshal([]byte(line), &request); err != nil {
					t.Fatal(err)
				}
				if request.Params.CWD != wantCWD {
					t.Errorf("%s cwd = %q, want actual process workspace %q", request.Method, request.Params.CWD, wantCWD)
				}
				if request.Params.Instructions != "Keep the cache path "+filepath.Join(root, "ws/history.txt") || request.Params.Config["file"] != filepath.Join(root, "ws/config.json") {
					t.Fatalf("cwd remapping rewrote unrelated prompt or config paths: %+v", request.Params)
				}
			}
		})
	}
}

func TestConversationCodexStdinPreservesNonCWDData(t *testing.T) {
	source, root, workspace := "/api/conversation", "/runner/cache", "/runner/workspace"
	for _, tc := range []struct {
		name       string
		data       string
		workingDir string
	}{
		{"platform or mounted workspace", `{"method":"thread/resume","params":{"cwd":"/api/conversation/ws"}}` + "\n", ""},
		{"turn input", `{"method":"turn/start","params":{"cwd":"/api/conversation/ws","input":[{"text":"/api/conversation/ws/file"}]}}` + "\n", workspace},
		{"different directory", `{"method":"thread/start","params":{"cwd":"/api/conversation/ws/subdir"}}` + "\n", workspace},
		{"missing cwd", `{"method":"thread/start","params":{"developerInstructions":"/api/conversation/ws"}}` + "\n", workspace},
		{"null params", `{"method":"thread/resume","params":null}` + "\n", workspace},
		{"invalid frame", `{"method":"thread/resume"` + "\n", workspace},
		{"ordinary input", "keep /api/conversation/ws/file\n", workspace},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want := strings.ReplaceAll(tc.data, source, root)
			if got := conversationInput(tc.data, source, root, tc.workingDir); got != want {
				t.Fatalf("rewrote non-target input: got %q, want %q", got, want)
			}
		})
	}
}
