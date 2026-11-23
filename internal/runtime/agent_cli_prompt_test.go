package runtime

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestAgentCLIConversationLongPrompt(t *testing.T) {
	prompt := strings.Repeat("项目记录 λ\n", 12_000)
	for _, kind := range AgentCLIKinds {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			command, input, err := StageAgentCLIConversationCommand(root, kind, AgentCLICommandOptions{
				Chat: true, Resume: "native-session", Prompt: prompt,
			})
			if err != nil {
				t.Fatal(err)
			}
			defer input.Close()
			for _, arg := range command.Argv {
				if !PromptFitsArgv(len(arg)) {
					t.Fatalf("long prompt remained on argv (%d bytes)", len(arg))
				}
			}
			switch kind {
			case AgentCLICursor:
				if command.Argv[len(command.Argv)-1] != "acp" || input.ACPPrompt == nil || *input.ACPPrompt != prompt || command.Stdin != nil {
					t.Fatal("ACP must receive the complete task from the conversation transport")
				}
			case AgentCLIKimi:
				if !slices.Contains(command.Argv, "native-session") || !slices.Contains(command.Argv, "-p") {
					t.Fatal("Kimi must keep its original print session")
				}
				data, err := os.ReadFile(command.AgentCLI.PromptFile)
				if err != nil || string(data) != prompt {
					t.Fatalf("Kimi hook file contains %d bytes, want %d: %v", len(data), len(prompt), err)
				}
				command.AgentCLI.Model = "test-model"
				command.AgentCLI.ApprovalCommand = "existing-approval-hook"
				home := t.TempDir()
				if _, _, err := ApplyAgentCLICredential(kind, ProviderCredential{APIKey: "test-key"}, command.AgentCLI, HostAgentCLIHome(home), home, command.Argv); err != nil {
					t.Fatal(err)
				}
				config, err := os.ReadFile(filepath.Join(home, ".kimi-code", "config.toml"))
				if err != nil {
					t.Fatal(err)
				}
				for _, expected := range []string{`event = "PreToolUse"`, "existing-approval-hook", `event = "UserPromptSubmit"`, "prompt-1.md"} {
					if !strings.Contains(string(config), expected) {
						t.Errorf("Kimi configuration is missing %q", expected)
					}
				}
			case AgentCLIZCode:
				var text strings.Builder
				for i, arg := range command.Argv[:len(command.Argv)-1] {
					if arg != "--attach" {
						continue
					}
					path := command.Argv[i+1]
					rel, err := filepath.Rel(root, path)
					if err != nil {
						t.Fatal(err)
					}
					if allowed, rewrite := ConversationAsset(filepath.ToSlash(rel)); !allowed || !rewrite {
						t.Fatalf("attachment cannot be synchronized with path rewriting: %s", path)
					}
					part, err := os.ReadFile(path)
					if err != nil || !utf8.Valid(part) {
						t.Fatalf("invalid attachment: %v", err)
					}
					text.Write(part)
				}
				if text.String() != prompt {
					t.Fatalf("attachments contain %d bytes, want %d", text.Len(), len(prompt))
				}
			default:
				if command.Stdin == nil {
					t.Fatal("long prompt must use stdin")
				}
				data, err := io.ReadAll(command.Stdin)
				if err != nil {
					t.Fatal(err)
				}
				got := string(data)
				if kind == AgentCLIAntigravity {
					var frame struct {
						Event   string `json:"event"`
						Message struct {
							Content string `json:"content"`
						} `json:"message"`
					}
					if err := json.Unmarshal(data, &frame); err != nil || frame.Event != "user" || data[len(data)-1] != '\n' {
						t.Fatalf("invalid NDJSON input: %v", err)
					}
					got = frame.Message.Content
				}
				if got != prompt {
					t.Fatalf("received %d bytes, want %d", len(got), len(prompt))
				}
			}
			input.Close()
			for _, path := range input.paths {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("temporary input was not removed: %v", err)
				}
			}
		})
	}
}

func TestAgentCLIConversationShortPromptUsesArgv(t *testing.T) {
	prompt := strings.Repeat("x", maxArgvPromptBytes)
	for _, kind := range AgentCLIKinds {
		command, input, err := StageAgentCLIConversationCommand(t.TempDir(), kind, AgentCLICommandOptions{Chat: true, Prompt: prompt})
		if err != nil {
			t.Fatal(err)
		}
		input.Close()
		if !slices.Contains(command.Argv, prompt) || command.Stdin != nil || input.ACPPrompt != nil || len(input.paths) != 0 {
			t.Errorf("%s: prompt at the threshold should remain on argv", kind)
		}
	}
}
