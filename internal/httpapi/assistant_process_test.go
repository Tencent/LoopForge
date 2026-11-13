package httpapi

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/runtime"
)

func TestConversationRemoteSyncPreservesCompleteSkillTrees(t *testing.T) {
	source, destination := t.TempDir(), t.TempDir()
	skillDir := filepath.Join(source, "home", ".agents", "skills")
	assets := map[string][]byte{
		"projects/example.txt":  []byte("project asset"),
		"tmp/check.sh":          []byte("#!/bin/sh\nprintf skill-ok\\n"),
		"node_modules/data.bin": {0, 255, 42},
		"sessions/nested.txt":   []byte("session example"),
	}
	refs := []domain.SkillFileRef{}
	for path, data := range assets {
		refs = append(refs, domain.SkillFileRef{Path: path, Size: int64(len(data)), Mode: 0755})
	}
	if _, err := runtime.WriteSkillTree(skillDir, []domain.SkillDefinition{{ID: "sessions", Name: "Sessions", Content: "Use the included assets", FileRefs: refs}}, map[string]map[string][]byte{"sessions": assets}); err != nil {
		t.Fatal(err)
	}
	// Native journal/cache trees with the same names must remain excluded.
	native := filepath.Join(source, "home", ".claude", "projects", "native-journal.jsonl")
	if err := os.MkdirAll(filepath.Dir(native), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(native, []byte("native journal"), 0600); err != nil {
		t.Fatal(err)
	}
	files, err := conversationInputFiles(source)
	if err != nil {
		t.Fatal(err)
	}
	pool := &runtime.ConversationProcessPool{}
	if _, err := pool.Handle(context.Background(), destination, "", nil, runtime.ConversationProcessRequest{Operation: "sync", SourceRoot: source, Files: files}, nil); err != nil {
		t.Fatal(err)
	}
	for path, want := range assets {
		target := filepath.Join(destination, "home", ".agents", "skills", "sessions", path)
		got, err := os.ReadFile(target)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("skill asset %s was not preserved: %v", path, err)
		}
		if info, err := os.Stat(target); err != nil || info.Mode().Perm() != 0755 {
			t.Fatalf("skill asset %s lost executable mode: %v", path, err)
		}
	}
	if _, err := os.Stat(filepath.Join(destination, "home", ".agents", "skills", "sessions", "SKILL.md")); err != nil {
		t.Fatal("skill named sessions lost its entry point", err)
	}
	if _, err := os.Stat(filepath.Join(destination, "home", ".claude", "projects", "native-journal.jsonl")); !os.IsNotExist(err) {
		t.Fatal("native journal was synchronized", err)
	}
}

func TestConversationClaudeGeneratedInvocationSurvivesRemoteSync(t *testing.T) {
	for _, workspace := range []bool{false, true} {
		t.Run(map[bool]string{false: "platform", true: "workspace"}[workspace], func(t *testing.T) {
			source, destination := t.TempDir(), t.TempDir()
			config := filepath.Join(source, "current-mcp.json")
			if err := os.WriteFile(config, []byte(`{"mcpServers":{"session":{"command":"`+source+`/gateway"}}}`), 0600); err != nil {
				t.Fatal(err)
			}
			invocation, err := prepareAssistantClaudeInvocation(assistantEngineInvocationOpts{
				Root: source, Config: config, Bin: "claude", Model: "fixture", SessionID: "remote-claude", Workspace: workspace,
			})
			if err != nil {
				t.Fatal(err)
			}
			files, err := conversationInputFiles(source)
			if err != nil {
				t.Fatal(err)
			}
			pool := &runtime.ConversationProcessPool{}
			if _, err := pool.Handle(context.Background(), destination, "", nil, runtime.ConversationProcessRequest{Operation: "sync", SourceRoot: source, Files: files}, nil); err != nil {
				t.Fatal(err)
			}
			for _, flag := range []string{"--mcp-config", "--settings"} {
				found := false
				for i, arg := range invocation.command.Argv[:len(invocation.command.Argv)-1] {
					if arg != flag {
						continue
					}
					found = true
					sourcePath := invocation.command.Argv[i+1]
					data, err := os.ReadFile(strings.ReplaceAll(sourcePath, source, destination))
					if err != nil {
						t.Fatalf("remote Claude invocation cannot load %s: %v", flag, err)
					}
					original, err := os.ReadFile(sourcePath)
					if err != nil || string(data) != strings.ReplaceAll(string(original), source, destination) {
						t.Fatalf("remote Claude %s was not preserved with remapped paths: %v", flag, err)
					}
				}
				if !found {
					t.Fatalf("fixture omitted %s", flag)
				}
			}
		})
	}
}

func TestConversationExecutorDistinguishesPreparationFromProcessFailure(t *testing.T) {
	h, _ := workbenchFixture(t)
	s := &AgentToolSession{ChatOwnerID: 1, ChatSessionID: "first-attempt", RuntimeMode: domain.AssistantModePlatform}
	execute := h.conversationExecutor(s, t.TempDir())
	if _, err := execute(context.Background(), "", runtime.CommandSpec{Argv: []string{"/missing/native-cli"}}, runtime.RunCredentials{}, nil); !errors.Is(err, runtime.ErrConversationNotStarted) {
		t.Fatalf("confirmed missing CLI was not safe to retry: %v", err)
	}
	if _, err := execute(context.Background(), "", runtime.CommandSpec{Argv: []string{"sh", "-c", "exit 1"}}, runtime.RunCredentials{}, nil); err == nil || errors.Is(err, runtime.ErrConversationNotStarted) {
		t.Fatalf("a launched process was considered safe to restart: %v", err)
	}
}

func TestConversationCredentialFailureCanBeRepairedBeforeFirstLaunch(t *testing.T) {
	h, _ := workbenchFixture(t)
	root := t.TempDir()
	s := &AgentToolSession{ChatOwnerID: 1, ChatSessionID: "credential-prestart", RuntimeMode: domain.AssistantModePlatform}
	execute := h.conversationExecutor(s, root)
	command := runtime.CommandSpec{Argv: []string{"sh", "-c", "touch launched"}, Provider: runtime.AgentCLIPi, CredentialBindings: []runtime.CredentialBinding{runtime.BindAgentCLI}, AgentCLI: runtime.AgentCLIOptions{Model: "fixture"}}
	credential := runtime.ProviderCredential{APIKey: "test-only"}
	credentials := runtime.RunCredentials{AgentCLI: map[string]runtime.ProviderCredential{runtime.AgentCLIPi: credential}}
	if _, err := execute(context.Background(), "", command, credentials, nil); !errors.Is(err, runtime.ErrConversationNotStarted) {
		t.Fatalf("credential preparation failure was not safe to retry: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "ws", "launched")); !os.IsNotExist(err) {
		t.Fatal("CLI launched before credentials were ready")
	}
	credential.BaseURL = "http://fixture.invalid/v1"
	credentials.AgentCLI[runtime.AgentCLIPi] = credential
	if _, err := execute(context.Background(), "", command, credentials, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "ws", "launched")); err != nil {
		t.Fatal("fixed credentials did not release native session", err)
	}
}

func TestUnstartedSharedSessionRetainsIdentityAndUsesFullPrompt(t *testing.T) {
	_, a, _, _, saved := sharedWorkbenchFixture(t)
	req := assistantRequest{SessionID: saved.ID, Messages: saved.Messages}
	first, err := openAssistantNativeSession(req, saved, a, "")
	if err != nil {
		t.Fatal(err)
	}
	id := first.checkpoint.SessionID
	if !first.checkpoint.Unstarted || first.resume {
		t.Fatal("new native identity was marked started")
	}
	if err := first.begin(); err != nil {
		t.Fatal(err)
	}
	first.Close()
	retry, err := openAssistantNativeSession(req, saved, a, "")
	if err != nil {
		t.Fatal(err)
	}
	if retry.resume || retry.checkpoint.SessionID != id {
		t.Fatal("preparation failure changed identity or tried to resume a missing journal")
	}
	retry.checkpoint.Unstarted = false // persisted before the start RPC
	if err := retry.writeCheckpoint(); err != nil {
		t.Fatal(err)
	}
	retry.Close()
	uncertain, err := openAssistantNativeSession(req, saved, a, "")
	if err != nil {
		t.Fatal(err)
	}
	defer uncertain.Close()
	if !uncertain.resume || uncertain.checkpoint.SessionID != id {
		t.Fatal("uncertain start discarded its native session")
	}
}
