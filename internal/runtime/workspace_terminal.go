package runtime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"agentflow/internal/domain"
	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

// TerminalPool owns transient PTYs on the machine that owns the workspace.
// HTTP ops require the run to be paused for human editing; no exclusive editor lease.
// A disconnected browser loses its shell after 15 seconds without polling.
type TerminalPool struct {
	mu       sync.Mutex
	sessions map[string]*workspaceTerminal
}
type workspaceTerminal struct {
	mu      sync.Mutex
	scope   string
	file    *os.File
	cmd     *exec.Cmd
	home    string
	output  []byte
	exited  bool
	timer   *time.Timer
	done    chan struct{}
	release func()
	once    sync.Once
}
type TerminalResult struct {
	ID     string `json:"id"`
	Data   []byte `json:"data,omitempty"`
	Exited bool   `json:"exited"`
}

// Handle is the legacy local-only terminal entry point. Production callers
// should use HandleSandbox so terminals share the run's actual isolation driver.
func (p *TerminalPool) Handle(root, scope string, req domain.WorkspaceTerminalRequest, release func()) (TerminalResult, error) {
	return p.handle(scope, req, release, func() (*exec.Cmd, string, error) {
		if info, err := os.Stat(root); err != nil || !info.IsDir() {
			return nil, "", fmt.Errorf("工作区不可用")
		}
		home, err := os.MkdirTemp("", "agentflow-terminal-home-")
		if err != nil {
			return nil, "", err
		}
		cmd := exec.Command("/bin/sh", "-c", terminalShellLaunch)
		cmd.Dir = root
		cmd.Env = terminalBaseEnvironment(home)
		return cmd, home, nil
	})
}

// HandleSandbox opens a PTY through the same driver and environment overlay as
// the SOP sandbox. ctx covers opening/driver preparation only; after a successful
// open the pool's expiry/close owns the shell lifetime, not an HTTP request.
// A successful open transfers release to the pool (called exactly once on close).
// On a failed open release is NOT called; the caller still owns that cleanup.
// Non-open operations do not need a sandbox and ignore release.
func (p *TerminalPool) HandleSandbox(ctx context.Context, sandbox Sandbox, scope string, req domain.WorkspaceTerminalRequest, release func()) (TerminalResult, error) {
	if req.Operation != "open" {
		return p.handle(scope, req, nil, nil)
	}
	if _, err := terminalTTL(req); err != nil {
		return TerminalResult{}, err
	}
	cmd, home, err := sandboxTerminalCommand(ctx, sandbox)
	if err != nil {
		return TerminalResult{}, err
	}
	ownedRelease := func() {
		sandboxTerminalCleanup(sandbox, home)
		if release != nil {
			release()
		}
	}
	result, err := p.handle(scope, req, ownedRelease, func() (*exec.Cmd, string, error) { return cmd, home, nil })
	if err != nil {
		sandboxTerminalCleanup(sandbox, home)
		_ = removeTerminalHome(home)
	}
	return result, err
}

func (p *TerminalPool) handle(scope string, req domain.WorkspaceTerminalRequest, release func(), openCommand func() (*exec.Cmd, string, error)) (TerminalResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	ttl, err := terminalTTL(req)
	if err != nil {
		return TerminalResult{}, err
	}
	if p.sessions == nil {
		p.sessions = map[string]*workspaceTerminal{}
	}
	if req.Operation == "open" {
		count := 0
		for _, s := range p.sessions {
			if s.scope == scope {
				count++
			}
		}
		if count >= 2 || len(p.sessions) >= 64 {
			return TerminalResult{}, fmt.Errorf("终端数量已达上限，请先关闭已有终端")
		}
		cmd, home, err := openCommand()
		if err != nil {
			return TerminalResult{}, err
		}
		random := make([]byte, 24)
		if _, err = rand.Read(random); err != nil {
			_ = removeTerminalHome(home)
			return TerminalResult{}, err
		}
		file, err := pty.StartWithSize(cmd, terminalSize(req))
		if err != nil {
			_ = removeTerminalHome(home)
			return TerminalResult{}, err
		}
		id := hex.EncodeToString(random)
		s := &workspaceTerminal{scope: scope, file: file, cmd: cmd, home: home, done: make(chan struct{}), release: release}
		p.sessions[id] = s
		s.timer = time.AfterFunc(ttl, func() { p.close(id) })
		go func() {
			defer close(s.done)
			buf := make([]byte, 8192)
			for {
				n, err := file.Read(buf)
				if n > 0 {
					s.mu.Lock()
					if len(s.output)+n > 256*1024 {
						s.output = []byte("\r\n[输出过多，较早内容已省略]\r\n")
					}
					s.output = append(s.output, buf[:n]...)
					s.mu.Unlock()
				}
				if err != nil {
					break
				}
			}
		}()
		go func() { cmd.Wait(); s.mu.Lock(); s.exited = true; s.mu.Unlock(); s.stop() }()
		return TerminalResult{ID: id}, nil
	}
	s := p.sessions[req.ID]
	if s == nil || s.scope != scope {
		return TerminalResult{}, fmt.Errorf("终端已关闭或无权访问")
	}
	s.timer.Reset(ttl)
	switch req.Operation {
	case "read":
		s.mu.Lock()
		result := TerminalResult{ID: req.ID, Data: append([]byte(nil), s.output...), Exited: s.exited}
		s.output = nil
		s.mu.Unlock()
		if result.Exited {
			select {
			case <-s.done:
				delete(p.sessions, req.ID)
				s.timer.Stop()
			default:
				result.Exited = false
			}
		}
		return result, nil
	case "input":
		if len(req.Data) > 32768 {
			return TerminalResult{}, fmt.Errorf("终端输入过长")
		}
		written := make(chan error, 1)
		go func() { _, err := s.file.Write([]byte(req.Data)); written <- err }()
		select {
		case err := <-written:
			if err != nil {
				return TerminalResult{}, err
			}
		case <-time.After(2 * time.Second):
			s.stop()
			return TerminalResult{}, fmt.Errorf("终端输入超时，连接已关闭")
		}

	case "resize":
		if err := pty.Setsize(s.file, terminalSize(req)); err != nil {
			return TerminalResult{}, err
		}
	case "close":
		delete(p.sessions, req.ID)
		s.timer.Stop()
		s.stop()
	default:
		return TerminalResult{}, fmt.Errorf("未知终端操作")
	}
	return TerminalResult{ID: req.ID}, nil
}
func terminalTTL(req domain.WorkspaceTerminalRequest) (time.Duration, error) {
	ttl := 15 * time.Second
	if !req.ExpiresAt.IsZero() {
		left := time.Until(req.ExpiresAt) - time.Second
		if left <= 0 {
			return 0, fmt.Errorf("工作区编辑会话已过期，请重新打开终端")
		}
		if left < ttl {
			ttl = left
		}
	}
	return ttl, nil
}

func terminalSize(req domain.WorkspaceTerminalRequest) *pty.Winsize {
	cols, rows := req.Cols, req.Rows
	if cols < 20 || cols > 500 {
		cols = 100
	}
	if rows < 5 || rows > 200 {
		rows = 30
	}
	return &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)}
}
func (s *workspaceTerminal) stop() {
	s.once.Do(func() {
		fg, _ := unix.IoctlGetInt(int(s.file.Fd()), unix.TIOCGPGRP)
		// Closing the controlling PTY also hangs up the foreground process group.
		syscall.Kill(-s.cmd.Process.Pid, syscall.SIGHUP)
		s.file.Close()
		time.Sleep(100 * time.Millisecond)
		if fg > 0 {
			syscall.Kill(-fg, syscall.SIGKILL)
		}
		syscall.Kill(-s.cmd.Process.Pid, syscall.SIGKILL)
		if s.release != nil {
			s.release()
		}
		_ = removeTerminalHome(s.home)
	})
}
func (p *TerminalPool) close(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if s := p.sessions[id]; s != nil {
		delete(p.sessions, id)
		s.stop()
	}
}
func (p *TerminalPool) CloseScope(scope string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for id, s := range p.sessions {
		if s.scope == scope {
			delete(p.sessions, id)
			s.timer.Stop()
			s.stop()
		}
	}
}

func (p *TerminalPool) HasScope(scope string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, s := range p.sessions {
		if s.scope == scope {
			return true
		}
	}
	return false
}

// HasRunScope guards snapshot/reclamation even for group terminals that do not
// hold the ordinary exclusive execution gate.
func (p *TerminalPool) HasRunScope(runID string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, s := range p.sessions {
		if strings.HasPrefix(s.scope, runID+":") {
			return true
		}
	}
	return false
}
