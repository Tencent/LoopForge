package runtime

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"
)

// Claude's host-refresh control channel stays on process stdin/stdout, including
// docker exec -i. Refresh tokens never cross this channel or enter a sandbox.
type claudeLoginBridge struct {
	cmd            *exec.Cmd
	command        CommandSpec
	input          io.Reader
	reader, writer *os.File
	writeMu        sync.Mutex
	mu             sync.Mutex
	err            error
	ready          chan struct{}
	readyOnce      sync.Once
	stop           context.CancelFunc
	ctx            context.Context
	initializeID   string
}

func newClaudeLoginBridge(cmd *exec.Cmd, command CommandSpec) (*claudeLoginBridge, error) {
	reader, writer, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	ctx, stop := context.WithCancel(context.Background())
	bridge := &claudeLoginBridge{cmd: cmd, command: command, input: cmd.Stdin,
		reader: reader, writer: writer, ready: make(chan struct{}), ctx: ctx, stop: stop,
		initializeID: "runner-auth-" + command.runnerLogin.id}
	cmd.Stdin = reader // A real fd avoids os/exec waiting on an open-ended copier.
	return bridge, nil
}

func (b *claudeLoginBridge) send(value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	b.writeMu.Lock()
	defer b.writeMu.Unlock()
	_, err = b.writer.Write(append(raw, '\n'))
	return err
}

func (b *claudeLoginBridge) fail(err error) {
	b.mu.Lock()
	if b.err == nil {
		b.err = err
	}
	b.mu.Unlock()
	if b.cmd.Process != nil {
		_ = b.cmd.Process.Kill()
	}
}

func (b *claudeLoginBridge) start() {
	go func() {
		if b.command.runnerLoginInitialize {
			if err := b.send(map[string]any{"type": "control_request", "request_id": b.initializeID,
				"request": map[string]string{"subtype": "initialize"}}); err != nil {
				b.fail(errors.New("无法初始化 Claude Code 自动续期接口"))
				return
			}
			select {
			case <-b.ready:
			case <-b.ctx.Done():
				return
			case <-time.After(20 * time.Second):
				b.fail(errors.New("Claude Code 未响应自动续期初始化，请更新 CLI"))
				return
			}
			if b.command.runnerLoginPrompt != "" {
				if err := b.send(map[string]any{"type": "user", "session_id": "", "message": map[string]string{
					"role": "user", "content": b.command.runnerLoginPrompt}}); err != nil {
					b.fail(errors.New("无法向 Claude Code 发送任务"))
					return
				}
			}
		}
		if b.input == nil {
			return
		}
		scanner := bufio.NewScanner(b.input)
		scanner.Buffer(make([]byte, 16<<10), 8<<20)
		for scanner.Scan() {
			b.writeMu.Lock()
			_, err := b.writer.Write(append(append([]byte(nil), scanner.Bytes()...), '\n'))
			b.writeMu.Unlock()
			if err != nil {
				return
			}
		}
		if scanner.Err() != nil && b.ctx.Err() == nil {
			b.fail(errors.New("Claude Code 输入超过限制或读取失败"))
		}
	}()
}

func (b *claudeLoginBridge) filter(line string) bool {
	var event struct {
		Type      string `json:"type"`
		RequestID string `json:"request_id"`
		Request   struct {
			Subtype string `json:"subtype"`
		} `json:"request"`
		Response struct {
			Subtype   string `json:"subtype"`
			RequestID string `json:"request_id"`
		} `json:"response"`
		Subtype string `json:"subtype"`
		Version string `json:"claude_code_version"`
	}
	if json.Unmarshal([]byte(line), &event) != nil {
		return true
	}
	if event.Type == "result" && b.command.runnerLoginInitialize && b.input == nil {
		// Print mode with streaming input waits for EOF even after its result.
		// Interactive conversations retain their caller-owned input instead.
		b.writeMu.Lock()
		_ = b.writer.Close()
		b.writeMu.Unlock()
	}
	if event.Type == "system" && event.Subtype == "init" && event.Version != "" {
		var major, minor, patch int
		if _, err := fmt.Sscanf(event.Version, "%d.%d.%d", &major, &minor, &patch); err == nil &&
			(major < 2 || major == 2 && (minor < 1 || minor == 1 && patch < 326)) {
			b.fail(errors.New("Claude Code 自动续期需要 2.1.326 或更新版本，请更新 CLI"))
		}
	}
	if b.command.runnerLoginInitialize && event.Type == "control_response" && event.Response.RequestID == b.initializeID {
		if event.Response.Subtype != "success" {
			b.fail(errors.New("Claude Code 不支持 Runner 自动续期初始化"))
			return false
		}
		b.readyOnce.Do(func() { close(b.ready) })
		return false
	}
	if event.Type != "control_request" || event.Request.Subtype != "oauth_token_refresh" {
		return true
	}
	// The server may request a distinct cloud-sessions token through the same
	// subtype. Never answer that request with a sign-in token.
	var details struct {
		Request struct {
			Kind string `json:"kind"`
		} `json:"request"`
	}
	_ = json.Unmarshal([]byte(line), &details)
	response := map[string]any{"accessToken": nil}
	if details.Request.Kind == "" {
		ctx, cancel := context.WithTimeout(b.ctx, 30*time.Second)
		token, err := b.command.runnerLogin.renewClaude(ctx)
		cancel()
		if err == nil {
			response["accessToken"] = token
		} else {
			response["reason"] = "temporarily_unavailable"
		}
	}
	if err := b.send(map[string]any{"type": "control_response", "response": map[string]any{
		"subtype": "success", "request_id": event.RequestID, "response": response,
	}}); err != nil && b.ctx.Err() == nil {
		b.fail(errors.New("无法回复 Claude Code 自动续期请求"))
	}
	return false
}

func (b *claudeLoginBridge) close() {
	b.stop()
	_ = b.writer.Close()
	_ = b.reader.Close()
	if closer, ok := b.input.(io.Closer); ok {
		_ = closer.Close()
	}
}

func (b *claudeLoginBridge) result() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.err
}

func isManagedClaude(command CommandSpec) bool {
	return command.Provider == "claude-code" && command.runnerLogin != nil && valueAfter(command.Argv, "--input-format") == "stream-json"
}
