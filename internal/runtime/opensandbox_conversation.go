package runtime

import (
	"bytes"
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"agentflow/internal/domain"
	opensandbox "github.com/alibaba/OpenSandbox/sdks/sandbox/go"
)

//go:embed opensandbox_conversation.py
var openSandboxConversationScript string

const openSandboxConversationUID, openSandboxConversationGID = 1000, 1000

type openSandboxConversation struct {
	root, token string
	port        int
	forward     ConversationToolForward
}

func newOpenSandboxConversation(root, token string, forward ConversationToolForward) *openSandboxConversation {
	var entropy [2]byte
	rand.Read(entropy[:])
	port := 20000 + int(binary.BigEndian.Uint16(entropy[:]))%40000
	return &openSandboxConversation{root: root, token: token, port: port, forward: forward}
}

func (c *openSandboxConversation) gateway() string {
	return fmt.Sprintf("http://127.0.0.1:%d", c.port)
}

func openSandboxConversationHomeSkips(path string) bool {
	path = filepath.ToSlash(path)
	for _, scratch := range []string{"tmp", ".codex/tmp", ".claude/debug"} {
		if path == scratch || strings.HasPrefix(path, scratch+"/") {
			return true
		}
	}
	return workspaceTransferSkips(path)
}

func openSandboxConversationRootSkips(path string) bool {
	path = filepath.ToSlash(path)
	if path == "ws" || strings.HasPrefix(path, "ws/") {
		return true
	}
	if rel, ok := strings.CutPrefix(path, "home/"); ok {
		return openSandboxConversationHomeSkips(rel)
	}
	return workspaceTransferSkips(path)
}

func (s *openSandbox) ensureConversationCLI(ctx context.Context, provider, home string) (string, error) {
	s.gate.RLock()
	defer s.gate.RUnlock()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	instance, err := s.ensure(ctx)
	if err != nil {
		return "", err
	}
	binary := conversationBinary(provider)
	setup, err := dockerCLISetup(provider, binary)
	if err != nil {
		return "", err
	}
	// Use the selected image's CLI first. The official installer runs in the
	// sandbox and its installation stays available for subsequent turns.
	script := dockerCLISharedPathSetup + `
if command -v "$5" >/dev/null 2>&1; then
	set -- "$1" "$2" "$3" reuse "$5"
fi
` + setup + "\n" + `set -- "$5"` + "\n" + dockerCLIProbe
	proxies := map[string]string{}
	for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "all_proxy", "no_proxy"} {
		if value, ok := s.profile.Environment[key]; ok {
			proxies[key] = value
		}
	}
	output, err := s.runCommand(ctx, instance, opensandbox.RunCommandRequest{
		Cwd: "/", Argv: []string{"/bin/sh", "-ec", script, "loopforge-conversation-cli",
			strconv.Itoa(openSandboxConversationUID), strconv.Itoa(openSandboxConversationGID), home, "install", binary},
		Envs: proxies,
	}, CommandSpec{}, nil)
	if err != nil {
		return "", fmt.Errorf("准备 OpenSandbox CLI %s：%w：%s", provider, err, conversationSetupOutput([]byte(output)))
	}
	path := strings.TrimSpace(output)
	path = path[strings.LastIndexByte(path, '\n')+1:]
	if !strings.HasPrefix(path, "/") {
		return "", fmt.Errorf("工作区未返回 CLI %s 的可执行路径", binary)
	}
	return path, nil
}

var errOpenSandboxConversationSync = errors.New("取回 OpenSandbox 对话状态")

func (s *openSandbox) execConversation(ctx context.Context, command CommandSpec, sink EventSink) (string, error) {
	conversation := command.openSandboxConversation
	instance, err := s.ensure(ctx)
	if err != nil {
		return "", errors.Join(errProcessNotStarted, err)
	}
	before, err := s.uploadWorkspace(ctx, instance, s.workspace, workspaceTransferSkips, openSandboxConversationUID, openSandboxConversationGID)
	if err != nil {
		return "", fmt.Errorf("%w: 上传对话工作区：%w", errProcessNotStarted, err)
	}
	defer deleteOpenSandboxDirectory(ctx, instance, conversation.root)
	state, err := s.uploadWorkspace(ctx, instance, conversation.root, openSandboxConversationRootSkips, openSandboxConversationUID, openSandboxConversationGID)
	if err != nil {
		return "", fmt.Errorf("%w: 上传对话会话状态：%w", errProcessNotStarted, err)
	}
	homeBefore := make(map[string]workspaceTransferEntry)
	for path, entry := range state {
		if rel, ok := strings.CutPrefix(filepath.ToSlash(path), "home/"); ok {
			homeBefore[filepath.FromSlash(rel)] = entry
		}
	}
	mailbox := "/tmp/loopforge-conversation-" + rand.Text()
	if err := instance.CreateDirectory(ctx, mailbox, 700); err != nil {
		return "", fmt.Errorf("%w: 创建对话传输目录：%w", errProcessNotStarted, err)
	}
	defer deleteOpenSandboxDirectory(ctx, instance, mailbox)
	// Keep user-supplied environment out of the privileged bridge process.
	// The private mailbox carries arguments, model keys and project values to
	// the child without putting them on the bridge's command line.
	config, err := json.Marshal(struct {
		Argv   []string          `json:"argv"`
		Env    map[string]string `json:"env"`
		Token  string            `json:"token"`
		Guards []string          `json:"guards"`
	}{command.Argv, mergeEnvironmentMaps(s.profile.Environment, command.environment, command.projectCredentialEnv), conversation.token, conversationGuardFiles})
	if err != nil {
		return "", errors.Join(errProcessNotStarted, err)
	}
	if err := uploadOpenSandboxFile(ctx, instance, filepath.Join(mailbox, "command.json"), bytes.NewReader(config)); err != nil {
		return "", fmt.Errorf("%w: 准备 OpenSandbox 对话命令：%w", errProcessNotStarted, err)
	}
	bridgeCtx, cancelBridge := context.WithCancel(ctx)
	defer cancelBridge()
	inputDone := make(chan error, 1)
	go func() {
		err := copyOpenSandboxInput(bridgeCtx, instance, mailbox, command.Stdin)
		inputDone <- err
		if err != nil {
			cancelBridge()
		}
	}()
	var calls sync.WaitGroup
	active := make(chan struct{}, 16)
	prefix := filepath.Base(mailbox) + " "
	relaySink := func(ev domain.TelemetryEvent) {
		id, relay := strings.CutPrefix(ev.Message, prefix)
		if ev.Name == "process.stderr" && relay && conversation.forward != nil {
			if _, err := hex.DecodeString(id); err == nil && len(id) == 32 {
				select {
				case active <- struct{}{}:
					calls.Go(func() {
						defer func() { <-active }()
						conversation.handleRequest(bridgeCtx, instance, mailbox, id)
					})
				default:
				}
				return
			}
		}
		if sink != nil {
			sink(ev)
		}
	}
	request := opensandbox.RunCommandRequest{Cwd: "/", Argv: []string{
		"python3", "-I", "-S", "-c", openSandboxConversationScript, conversation.root, command.WorkingDir, mailbox,
		strconv.Itoa(openSandboxConversationUID), strconv.Itoa(openSandboxConversationGID),
		strconv.Itoa(conversation.port),
	}}
	if deadline, ok := ctx.Deadline(); ok {
		request.Timeout = max(1, time.Until(deadline).Milliseconds())
	}
	output, runErr := s.runCommand(bridgeCtx, instance, request, command, relaySink)
	cancelBridge()
	if input, ok := command.Stdin.(io.Closer); ok {
		_ = input.Close()
	}
	if inputErr := <-inputDone; inputErr != nil && !errors.Is(inputErr, context.Canceled) && !errors.Is(inputErr, io.ErrClosedPipe) {
		runErr = errors.Join(runErr, fmt.Errorf("发送 OpenSandbox 对话输入：%w", inputErr))
	}
	calls.Wait()
	// The CLI's turn can end by cancellation after a terminal protocol
	// event. Preserve its edits and native session journal in that case too.
	syncCtx, cancelSync := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
	defer cancelSync()
	workspaceErr := s.downloadWorkspace(syncCtx, instance, s.workspace, workspaceTransferSkips, before)
	homeErr := s.downloadWorkspace(syncCtx, instance, filepath.Join(conversation.root, "home"), openSandboxConversationHomeSkips, homeBefore)
	if err := errors.Join(workspaceErr, homeErr); err != nil {
		runErr = errors.Join(runErr, fmt.Errorf("%w：%w", errOpenSandboxConversationSync, err))
	}
	return output, runErr
}

func copyOpenSandboxInput(ctx context.Context, instance *opensandbox.Sandbox, mailbox string, input io.Reader) error {
	if input != nil {
		if closer, ok := input.(io.Closer); ok {
			stop := context.AfterFunc(ctx, func() { _ = closer.Close() })
			defer stop()
		}
	} else {
		input = strings.NewReader("")
	}
	buffer := make([]byte, 64<<10)
	for index := 0; ; {
		n, err := input.Read(buffer)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if n > 0 {
			if err := sendOpenSandboxMessage(ctx, instance, filepath.Join(mailbox, "stdin-"+strconv.Itoa(index)), buffer[:n]); err != nil {
				return err
			}
			index++
		}
		if errors.Is(err, io.EOF) {
			return sendOpenSandboxMessage(ctx, instance, filepath.Join(mailbox, "stdin-"+strconv.Itoa(index)), nil)
		}
		if err != nil {
			return err
		}
	}
}

func sendOpenSandboxMessage(ctx context.Context, instance *opensandbox.Sandbox, path string, data []byte) error {
	tmp := path + ".upload"
	if err := uploadOpenSandboxFile(ctx, instance, tmp, bytes.NewReader(data)); err != nil {
		return err
	}
	return instance.MoveFiles(ctx, opensandbox.MoveRequest{{Src: tmp, Dest: path}})
}

func (c *openSandboxConversation) handleRequest(ctx context.Context, instance *opensandbox.Sandbox, mailbox, id string) {
	path := filepath.Join(mailbox, id)
	status, result := "502\n", json.RawMessage(nil)
	if source, err := instance.DownloadFile(ctx, path+".request", ""); err == nil {
		data, err := io.ReadAll(io.LimitReader(source, (4<<20)+1))
		_ = source.Close()
		if err == nil && len(data) <= 4<<20 && json.Valid(data) {
			if response, err := c.forward(ctx, c.token, data); err == nil && len(response) <= 16<<20 {
				result, status = response, "200\n"
				if len(response) == 0 {
					status = "202\n"
				}
			}
		}
	}
	_ = sendOpenSandboxMessage(ctx, instance, path+".response", append([]byte(status), result...))
}

func deleteOpenSandboxDirectory(ctx context.Context, instance *opensandbox.Sandbox, path string) {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	_ = instance.DeleteDirectory(cleanup, path)
}
