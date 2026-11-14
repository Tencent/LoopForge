package runtime

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func conversationPoll(t *testing.T, p *ConversationProcessPool, ctx context.Context, root, id string) string {
	t.Helper()
	var out strings.Builder
	var after int64
	for {
		r, err := p.Handle(ctx, root, "", nil, ConversationProcessRequest{Operation: "poll", ID: id, After: after}, nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range r.Events {
			out.WriteString(e.Message)
		}
		after = r.Sequence
		if r.Done {
			if r.Error != "" {
				t.Fatal(r.Error)
			}
			return out.String()
		}
	}
}
func TestConversationProcessBidirectionalAndRootFence(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	root := t.TempDir()
	p := &ConversationProcessPool{}
	req := ConversationProcessRequest{Operation: "start", SourceRoot: "/api-state", Command: &ConversationCommand{Argv: []string{"sh", "-c", "read -r line; printf 'out:%s\\n' \"$line\"; printf err >&2"}, Interactive: true}}
	r, err := p.Handle(ctx, root, "", nil, req, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.Handle(ctx, root, "", nil, ConversationProcessRequest{Operation: "sync"}, nil); err == nil {
		t.Fatal("assets changed while running")
	}
	if _, err = p.Handle(ctx, root, "", nil, req, nil); err == nil {
		t.Fatal("overlapping process started")
	}
	if _, err = p.Handle(ctx, root, "", nil, ConversationProcessRequest{Operation: "write", ID: r.ID, SourceRoot: "/api-state", Data: "/api-state/ws/a\n"}, nil); err != nil {
		t.Fatal(err)
	}
	output := conversationPoll(t, p, ctx, root, r.ID)
	if !strings.Contains(filepath.ToSlash(output), filepath.ToSlash(filepath.Join(root, "ws", "a"))) || !strings.Contains(output, "err") {
		t.Fatalf("duplex output: %s", output)
	}
	if _, err = p.Handle(ctx, root, "", nil, ConversationProcessRequest{Operation: "prepare"}, nil); err != nil {
		t.Fatal("root released before completion:", err)
	}
}

func TestConversationPlatformRefreshesPersistentApprovalExtensions(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 unavailable")
	}
	for provider, path := range map[string]string{
		AgentCLIPi:          "home/.pi/agent/extensions/loopforge-approval.ts",
		AgentCLIOpenCode:    "home/.config/opencode/plugins/loopforge-approval.js",
		AgentCLICursor:      "home/.cursor/hooks.json",
		AgentCLIAntigravity: "home/.gemini/config/hooks.json",
	} {
		t.Run(provider, func(t *testing.T) {
			root := t.TempDir()
			path = filepath.Join(root, filepath.FromSlash(path))
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("stale-workspace-hook"), 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			pool := &ConversationProcessPool{}
			result, err := pool.Handle(ctx, root, "", nil, ConversationProcessRequest{Operation: "start", Command: &ConversationCommand{Provider: provider, Argv: []string{"sh", "-c", "true"}}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			conversationPoll(t, pool, ctx, root, result.ID)
			data, err := os.ReadFile(path)
			if err != nil || len(data) == 0 || strings.Contains(string(data), "stale-workspace-hook") {
				t.Fatalf("platform start retained old workspace hook: %s: %v", data, err)
			}
			if provider == AgentCLIPi && !strings.Contains(string(data), `event.toolName==="codemode"`) {
				t.Fatal("platform process did not install concrete-tool Pi approval")
			}
		})
	}
}
func TestConversationProcessCancelUnblocksUnreadInput(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	root := t.TempDir()
	p := &ConversationProcessPool{}
	r, err := p.Handle(ctx, root, "", nil, ConversationProcessRequest{Operation: "start", Command: &ConversationCommand{Argv: []string{"sh", "-c", "sleep 30"}, Interactive: true}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	inputCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		_, err := p.Handle(inputCtx, root, "", nil, ConversationProcessRequest{Operation: "write", ID: r.ID, Data: strings.Repeat("x", 1<<20)}, nil)
		done <- err
	}()
	stop()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled input succeeded")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled input deadlocked")
	}
	if _, err = p.Handle(ctx, root, "", nil, ConversationProcessRequest{Operation: "stop", ID: r.ID}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = p.Handle(ctx, root, "", nil, ConversationProcessRequest{Operation: "prepare"}, nil); err != nil {
		t.Fatal(err)
	}
}
func TestConversationAssetsResetChmodAndRejectSymlink(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	p := &ConversationProcessPool{}
	syncFile := func(path string, data []byte, mode uint32) error {
		_, err := p.Handle(ctx, root, "", nil, ConversationProcessRequest{Operation: "sync", Files: []ConversationFile{{Path: path, Data: data, Mode: mode}}}, nil)
		return err
	}
	rel := "home/.agents/skills/test/run.sh"
	if err := syncFile(rel, []byte("#!/bin/sh\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := syncFile(rel, []byte("#!/bin/sh\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Join(root, rel)); err != nil || info.Mode().Perm() != 0700 {
		t.Fatal("executable mode lost", err)
	}
	native := filepath.Join(root, "home", "history.json")
	if err := os.WriteFile(native, []byte("native"), 0600); err != nil {
		t.Fatal(err)
	}
	binary := []byte{0, 255, 42}
	if err := syncFile("attachments/a/source.pdf", binary, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Handle(ctx, root, "", nil, ConversationProcessRequest{Operation: "resetAssets"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, rel)); !os.IsNotExist(err) {
		t.Fatal("removed skill retained")
	}
	if b, err := os.ReadFile(native); err != nil || string(b) != "native" {
		t.Fatal("native transcript changed")
	}
	if err := os.RemoveAll(filepath.Join(root, "home", ".agents")); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "home", ".agents")); err != nil {
		t.Fatal(err)
	}
	if err := syncFile(rel, []byte("bad"), 0700); err == nil {
		t.Fatal("host followed native symlink")
	}
	if _, err := os.Stat(filepath.Join(outside, "skills/test/run.sh")); !os.IsNotExist(err) {
		t.Fatal("escaped root")
	}
}
func TestConversationRewriteOnlyGeneratedInputs(t *testing.T) {
	root := t.TempDir()
	for _, rel := range []string{"current-mcp.json", "home/.agents/skills/test/SKILL.md", "home/history.json"} {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("http://old"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := replaceConversationGateway(root, "http://old", "http://new"); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"home/.agents/skills/test/SKILL.md", "home/history.json"} {
		b, _ := os.ReadFile(filepath.Join(root, rel))
		if string(b) != "http://old" {
			t.Fatal("rewrote native or user asset")
		}
	}
	b, _ := os.ReadFile(filepath.Join(root, "current-mcp.json"))
	if string(b) != "http://new" {
		t.Fatal("gateway not rebound")
	}
}
func TestConversationGuardPathsRemainPlatformOwned(t *testing.T) {
	root := t.TempDir()
	args, err := conversationGuardMounts(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range conversationGuardFiles {
		path := filepath.Join(root, rel)
		if !strings.Contains(strings.Join(args, "\n"), path+":"+path+":ro") {
			t.Fatal("guard not readonly", rel)
		}
	}
}

func TestConversationKimiFailsClosedWithoutHookInterpreter(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	pool := &ConversationProcessPool{}
	root := t.TempDir()
	_, err := pool.Handle(context.Background(), root, "", nil, ConversationProcessRequest{
		Operation: "start", Command: &ConversationCommand{Provider: AgentCLIKimi, Argv: []string{"kimi"}},
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "Python3") {
		t.Fatalf("missing interpreter did not block process: %v", err)
	}
	if _, err := pool.Handle(context.Background(), root, "", nil, ConversationProcessRequest{Operation: "prepare"}, nil); err != nil {
		t.Fatalf("failed start retained process reservation: %v", err)
	}
}

func TestConversationSlowPreparationDoesNotBlockOtherSessions(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool := &ConversationProcessPool{}
	preparing, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	factory := func(ctx context.Context, _ string) (Sandbox, func(), error) {
		close(preparing)
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil, nil, ctx.Err()
	}
	root := t.TempDir()
	started := make(chan struct{})
	go func() {
		defer close(started)
		_, _ = pool.Handle(ctx, root, "", nil, ConversationProcessRequest{Operation: "start", Command: &ConversationCommand{Argv: []string{"sh", "-c", "true"}}}, nil, factory)
	}()
	<-preparing
	other := t.TempDir()
	done := make(chan error, 1)
	go func() {
		_, err := pool.Handle(ctx, other, "", nil, ConversationProcessRequest{Operation: "prepare"}, nil)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("another session blocked behind slow sandbox preparation")
	}
	cancel()
	<-started
}

func TestConversationCancelledZCodeRemovesCredentialWithoutExitTrap(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 unavailable")
	}
	root := t.TempDir()
	binary := filepath.Join(t.TempDir(), "fake-zcode")
	// No SessionStart event; this process is killed before its wrapper exits.
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf started\\n\nwhile :; do sleep 1; done\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool := &ConversationProcessPool{}
	result, err := pool.Handle(ctx, root, "", nil, ConversationProcessRequest{Operation: "start", Command: &ConversationCommand{Provider: AgentCLIZCode, Argv: []string{binary}, Bindings: []CredentialBinding{BindAgentCLI}, AgentCLI: AgentCLIOptions{Model: "m"}, Credentials: RunCredentials{AgentCLI: map[string]ProviderCredential{AgentCLIZCode: {APIKey: "zcode-cancellation-test-key"}}}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(filepath.Join(root, "home", zcodeProviderDir, "provider_config.*.json"))
	if err != nil || len(files) != 1 {
		t.Fatalf("expected one live per-call key, got %d: %v", len(files), err)
	}
	journal := filepath.Join(root, "home", zcodeProviderDir, "journal.db")
	if err := os.WriteFile(journal, []byte("keep native history"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Handle(ctx, root, "", nil, ConversationProcessRequest{Operation: "stop", ID: result.ID}, nil); err != nil {
		t.Fatal(err)
	}
	files, _ = filepath.Glob(filepath.Join(root, "home", zcodeProviderDir, "provider_config.*.json"))
	if len(files) != 0 {
		t.Fatal("cancelled CLI left credential files")
	}
	if data, err := os.ReadFile(journal); err != nil || string(data) != "keep native history" {
		t.Fatal("credential cleanup changed native history")
	}
}

func TestConversationProcessPollReturnsCancellation(t *testing.T) {
	for _, before := range []bool{true, false} {
		t.Run(map[bool]string{true: "before", false: "during"}[before], func(t *testing.T) {
			root := t.TempDir()
			p := &ConversationProcessPool{entries: map[string]*conversationProcess{"poll": {root: root, wake: make(chan struct{}, 1)}}}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if before {
				cancel()
			} else {
				time.AfterFunc(10*time.Millisecond, cancel)
			}
			_, err := p.Handle(ctx, root, "", nil, ConversationProcessRequest{Operation: "poll", ID: "poll"}, nil)
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("cancelled poll returned %v", err)
			}
		})
	}
}
