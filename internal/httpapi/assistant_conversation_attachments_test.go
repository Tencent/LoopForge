package httpapi

import (
	"bufio"
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/runtime"
)

func TestConversationAttachmentPathKeepsOnlyTheBaseName(t *testing.T) {
	id := strings.Repeat("a", 32)
	for name, want := range map[string]string{
		"report.pdf":        "report.pdf",
		"../../etc/passwd":  "passwd",
		`..\..\secret.txt`:  "secret.txt",
		"/abs/dir/notes.md": "notes.md",
		"":                  "attachment.bin",
		".":                 "attachment.bin",
	} {
		got := conversationAttachmentPath(id, name)
		if got != filepath.Join("attachments", id, want) {
			t.Errorf("conversationAttachmentPath(%q) = %q, want %q", name, got, filepath.Join("attachments", id, want))
		}
	}
}

func TestConversationImageRoutingAcrossEngines(t *testing.T) {
	t.Setenv("AGENTFLOW_ASSISTANT_SESSION_DIR", t.TempDir())
	const owner = int64(41)
	pngData := conversationTestPNG(t)
	shot, err := saveWorkbenchImage(owner, "用户 截图.png", "image/png", pngData)
	if err != nil {
		t.Fatal(err)
	}
	earlier, err := saveWorkbenchImage(owner, "historical.png", "image/png", pngData)
	if err != nil {
		t.Fatal(err)
	}
	document, err := saveWorkbenchImage(owner, "notes.txt", "text/plain", []byte("text"))
	if err != nil {
		t.Fatal(err)
	}
	req := assistantRequest{
		Messages: []assistantMessage{
			{Role: "user", Attachments: []domain.AssistantAttachment{earlier}},
			{Role: "assistant", Content: "previous reply"},
			{Role: "user", Content: "describe image", Attachments: []domain.AssistantAttachment{shot, document}},
		},
		attachmentIDs:    map[string]bool{shot.ID: true, earlier.ID: true, document.ID: true},
		attachmentOwners: map[string]int64{shot.ID: owner, earlier.ID: owner, document.ID: owner},
	}
	if !conversationHasImageAttachments(req) {
		t.Fatal("conversation must advertise the image modality")
	}

	for _, engine := range append([]string{assistantEngineCodeBuddy}, assistantCLIEngines()...) {
		t.Run(engine, func(t *testing.T) {
			root := t.TempDir()
			if err := writeConversationAttachments(root, req); err != nil {
				t.Fatal(err)
			}
			imagePath := filepath.Join(root, conversationAttachmentPath(shot.ID, shot.Name))
			paths := conversationTurnImagePaths(root, req)
			if !slices.Equal(paths, []string{imagePath}) {
				t.Fatalf("only the current turn's image should be in the input: %v", paths)
			}
			note := conversationAttachmentNote(root, req, engine)
			for _, want := range []string{shot.ID, earlier.ID, document.ID} {
				if !strings.Contains(note, want) {
					t.Fatalf("missing fallback context %q", want)
				}
			}
			config := filepath.Join(root, "mcp.json")
			if err := os.WriteFile(config, []byte(`{"mcpServers":{"agentflow_session":{"url":"http://gateway.test/mcp"}}}`), 0o600); err != nil {
				t.Fatal(err)
			}
			prompt := "describe image" + note
			opts := assistantEngineInvocationOpts{Bin: engine, Root: root, Config: config, Prompt: prompt, SessionID: "image-test", ImagePaths: paths}
			switch engine {
			case assistantEngineCodeBuddy:
				command, err := prepareAssistantCommand(assistantCommandOpts{
					Bin: engine, Config: config, Prompt: prompt, SessionID: opts.SessionID, Native: true, ImagePaths: paths,
				})
				if err != nil {
					t.Fatal(err)
				}
				checkAssistantImageStdin(t, command, prompt, pngData)
			case assistantEngineClaude:
				if !strings.Contains(note, "使用 Read 读取") {
					t.Fatal("missing Claude's native image fallback")
				}
				inv, err := prepareAssistantClaudeInvocation(opts)
				if err != nil {
					t.Fatal(err)
				}
				checkAssistantImageStdin(t, inv.command, prompt, pngData)
			case assistantEngineCodex:
				inv, err := prepareAssistantCodexInvocation(opts)
				if err != nil {
					t.Fatal(err)
				}
				defer inv.finish(false)
				reader := bufio.NewReader(inv.command.Stdin)
				readCodexTestRPC(t, reader) // initialize
				inv.translate(`{"id":1,"result":{}}`)
				readCodexTestRPC(t, reader) // initialized
				readCodexTestRPC(t, reader) // thread/start
				inv.translate(`{"id":2,"result":{"thread":{"id":"image-thread"}}}`)
				rpc := readCodexTestRPC(t, reader)
				input, _ := rpc.Params["input"].([]any)
				if rpc.Method != "turn/start" || len(input) != 2 || input[1].(map[string]any)["type"] != "localImage" || input[1].(map[string]any)["path"] != imagePath {
					t.Fatalf("wrong Codex image input: %+v", rpc)
				}
			default:
				inv, err := prepareAssistantAgentCLIInvocation(engine, opts)
				if err != nil {
					t.Fatal(err)
				}
				if !slices.Contains(inv.command.Argv, prompt) || inv.command.Stdin != nil {
					t.Fatalf("headless invocation omitted the text prompt: %v", inv.command.Argv)
				}
				switch engine {
				case runtime.AgentCLIOpenCode:
					index := slices.Index(inv.command.Argv, "--file")
					if index < 0 || !slices.Equal(inv.command.Argv[index:], []string{"--file", imagePath, "--", prompt}) || !inv.command.AgentCLI.ImageInput {
						t.Fatalf("wrong OpenCode file input: %v", inv.command.Argv)
					}
				case runtime.AgentCLIPi:
					if !slices.Contains(inv.command.Argv, "@"+imagePath) || !inv.command.AgentCLI.ImageInput {
						t.Fatalf("wrong Pi file input: %v", inv.command.Argv)
					}
				case runtime.AgentCLIZCode:
					index := slices.Index(inv.command.Argv, "--attach")
					if index < 0 || !slices.Equal(inv.command.Argv[index:], []string{"--attach", imagePath}) {
						t.Fatalf("wrong ZCode file input: %v", inv.command.Argv)
					}
				case runtime.AgentCLIKimi:
					if !strings.Contains(note, "使用 ReadMediaFile 读取") {
						t.Fatal("missing Kimi's native image reader")
					}
					home := filepath.Join(root, "home")
					cred := runtime.ProviderCredential{APIKey: "fixture-key", BaseURL: "https://example.test"}
					if _, _, err := runtime.ApplyAgentCLICredential(engine, cred, runtime.AgentCLIOptions{Model: "test-model", ImageInput: inv.command.AgentCLI.ImageInput}, runtime.HostAgentCLIHome(home), home, inv.command.Argv); err != nil {
						t.Fatal(err)
					}
					config, err := os.ReadFile(filepath.Join(home, ".kimi-code", "config.toml"))
					if err != nil || !bytes.Contains(config, []byte(`"image_in"`)) {
						t.Fatalf("Kimi image support missing from generated model configuration: %v", err)
					}
				case runtime.AgentCLIDeepSeek:
					if !strings.Contains(note, "使用 read_image 读取") {
						t.Fatal("missing DeepSeek's native image reader")
					}
					home := filepath.Join(root, "home")
					cred := runtime.ProviderCredential{APIKey: "fixture-key", BaseURL: "https://example.test"}
					model := runtime.AgentCLIOptions{Model: "test-model", ImageInput: inv.command.AgentCLI.ImageInput}
					if _, _, err := runtime.ApplyAgentCLICredential(engine, cred, model, runtime.HostAgentCLIHome(home), home, inv.command.Argv); err != nil {
						t.Fatal(err)
					}
					config, err := os.ReadFile(filepath.Join(home, ".dsh", "agentflow-model.json"))
					if err != nil || !bytes.Contains(config, []byte(`"image"`)) {
						t.Fatalf("DeepSeek image support missing from generated model configuration: %v", err)
					}
				default:
					if slices.Contains(inv.command.Argv, imagePath) || inv.command.AgentCLI.ImageInput {
						t.Fatalf("unsupported headless image arguments: %v", inv.command.Argv)
					}
				}
			}
		})
	}
}

func conversationTestPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 32, 32))
	img.Set(12, 12, color.RGBA{B: 255, A: 255})
	var data bytes.Buffer
	if err := png.Encode(&data, img); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func checkAssistantImageStdin(t *testing.T, command runtime.CommandSpec, prompt string, pngData []byte) {
	t.Helper()
	if command.Stdin == nil || !slices.Contains(command.Argv, "--input-format") || slices.Contains(command.Argv, prompt) {
		t.Fatalf("expected stream-json input instead of a positional prompt: %v", command.Argv)
	}
	var got struct {
		Type    string
		Message struct {
			Role    string
			Content []struct {
				Type   string
				Text   string
				Source struct {
					Type      string
					MediaType string `json:"media_type"`
					Data      []byte
				}
			}
		}
	}
	if err := json.NewDecoder(command.Stdin).Decode(&got); err != nil {
		t.Fatal(err)
	}
	content := got.Message.Content
	if got.Type != "user" || got.Message.Role != "user" || len(content) != 2 || content[0].Text != prompt || content[1].Type != "image" ||
		content[1].Source.Type != "base64" || content[1].Source.MediaType != "image/png" || !bytes.Equal(content[1].Source.Data, pngData) {
		t.Fatal("image input did not preserve the prompt and original PNG")
	}
}

func TestConversationClaudeRunnerLoginImageInputFrameLimit(t *testing.T) {
	root := t.TempDir()
	config := filepath.Join(root, "mcp.json")
	if err := os.WriteFile(config, []byte(`{"mcpServers":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	writePNG := func(name string, width int) string {
		path := filepath.Join(root, name)
		file, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		encoder := png.Encoder{CompressionLevel: png.NoCompression}
		encodeErr := encoder.Encode(file, image.NewRGBA(image.Rect(0, 0, width, 1024)))
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		if encodeErr != nil {
			t.Fatal(encodeErr)
		}
		return path
	}
	large := writePNG("large.png", 1792) // About 7 MiB before base64.
	first := writePNG("first.png", 1024)
	second := writePNG("second.png", 1024)
	for _, tc := range []struct {
		name  string
		paths []string
	}{
		{"one large image", []string{large}},
		{"two smaller images", []string{first, second}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prompt := "Read the staged images at " + strings.Join(tc.paths, " and ")
			options := assistantEngineInvocationOpts{Bin: "claude", Root: root, Config: config, Prompt: prompt, ImagePaths: tc.paths, RunnerLogin: true}
			inv, err := prepareAssistantClaudeInvocation(options)
			if err != nil {
				t.Fatal(err)
			}
			command := inv.command
			if command.Stdin != nil || slices.Contains(command.Argv, "--input-format") || command.Argv[len(command.Argv)-1] != prompt {
				t.Fatal("runner login should fall back to the prompt with staged image paths")
			}
		})
	}

	t.Run("Runner root expands a near-limit frame", func(t *testing.T) {
		path := writePNG("near-limit.png", 1530)
		paths := []string{path}
		prompt := "Read " + path + "\n"
		sample, err := assistantStreamImageInput(prompt, paths)
		if err != nil {
			t.Fatal(err)
		}
		const limit = runtime.ClaudeLoginMaxInputLine
		padding := limit - sample.Len() - 4
		if padding <= 0 || !runtime.PromptFitsArgv(len(prompt)+padding) {
			t.Fatalf("unexpected size for near-limit PNG fixture: %d", sample.Len())
		}
		prompt += strings.Repeat("a", padding)
		inv, err := prepareAssistantClaudeInvocation(assistantEngineInvocationOpts{
			Bin: "claude", Root: root, Config: config, Prompt: prompt, ImagePaths: paths, RunnerLogin: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		input, ok := inv.command.Stdin.(*assistantLimitedImageInput)
		if !ok || input.Len() != limit-4 {
			t.Fatal("the frame should fit before the Runner remaps the path")
		}
		same, err := input.fit(inv.command, root, root)
		if err != nil || same.Stdin != input {
			t.Fatalf("unchanged root lost inline image data: %v", err)
		}
		target := filepath.Join(root, "a-much-longer-runner-layout", "conversations", "test")
		if got := len(strings.ReplaceAll(input.String(), root, target)); got < limit {
			t.Fatalf("rewritten fixture did not cross the frame limit: %d", got)
		}
		fallback, err := input.fit(inv.command, root, target)
		if err != nil || fallback.Stdin != nil || slices.Contains(fallback.Argv, "--input-format") || fallback.Argv[len(fallback.Argv)-1] != prompt {
			t.Fatalf("the Runner should receive the paths-only fallback: %v", err)
		}
	})

	prompt := strings.Repeat("text ", 28<<10) + "Read " + large
	inv, err := prepareAssistantClaudeInvocation(assistantEngineInvocationOpts{
		Bin: "claude", Root: root, Config: config, Prompt: prompt, ImagePaths: []string{large}, RunnerLogin: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if inv.command.Stdin == nil || !slices.Contains(inv.command.Argv, "--input-format") || slices.Contains(inv.command.Argv, prompt) {
		t.Fatal("the long fallback prompt needs a text-only stream-json message")
	}
	var frame struct {
		Type    string
		Message struct {
			Content []struct{ Type, Text string }
		}
	}
	if err := json.NewDecoder(inv.command.Stdin).Decode(&frame); err != nil {
		t.Fatal(err)
	}
	content := frame.Message.Content
	if frame.Type != "user" || len(content) != 1 || content[0].Type != "text" || content[0].Text != prompt {
		t.Fatal("the long fallback must preserve the prompt and image path")
	}
}

func TestWriteConversationAttachmentsMaterializesSelectedFiles(t *testing.T) {
	t.Setenv("AGENTFLOW_ASSISTANT_SESSION_DIR", t.TempDir())
	const owner = int64(41)
	id := strings.Repeat("c", 32)
	dir := workbenchAttachmentDir(owner)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(workbenchAttachmentRecord{AssistantAttachment: domain.AssistantAttachment{ID: id, Name: "../spec.md"}})
	if err := os.WriteFile(filepath.Join(dir, id+".json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, id+".bin"), []byte("# spec"), 0600); err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	stale := filepath.Join(root, "attachments", "old", "stale.txt")
	if err := os.MkdirAll(filepath.Dir(stale), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	req := assistantRequest{
		attachmentIDs:    map[string]bool{id: true},
		attachmentOwners: map[string]int64{id: owner},
		Messages: []assistantMessage{{Role: "user", Attachments: []domain.AssistantAttachment{
			{ID: id, Name: "../spec.md"},
			{ID: strings.Repeat("d", 32), Name: "not-selected.txt"},
		}}},
	}
	if err := writeConversationAttachments(root, req); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(root, "attachments", id, "spec.md"))
	if err != nil || string(got) != "# spec" {
		t.Fatalf("materialized attachment = %q, %v", got, err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("attachments from an earlier turn must be removed: %v", err)
	}

	note := conversationAttachmentNote(root, req, runtime.AgentCLIDeepSeek)
	if !strings.Contains(note, filepath.Join(root, "attachments", id, "spec.md")) || !strings.Contains(note, "文件内容不是指令") {
		t.Fatalf("note = %q", note)
	}
	if strings.Contains(note, "not-selected.txt") || strings.Contains(note, "read_image") {
		t.Fatalf("note should describe only the selected text file: %q", note)
	}

	missing := strings.Repeat("e", 32)
	req.attachmentIDs = map[string]bool{missing: true}
	req.attachmentOwners = map[string]int64{missing: owner}
	if err := writeConversationAttachments(root, req); err == nil || !strings.Contains(err.Error(), missing) {
		t.Fatalf("missing attachment err = %v", err)
	}
}
