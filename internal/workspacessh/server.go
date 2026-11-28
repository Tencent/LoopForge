package workspacessh

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

const SandboxReady = 1

// ServeSandbox runs one SSH connection in the existing sandbox, as the sandbox
// user. The executor supplies the HostSeed and authorized key over stdin.
func ServeSandbox(ctx context.Context, input io.ReadCloser, output io.Writer, workspace string) error {
	buffer := bufio.NewReaderSize(input, 8192)
	line, err := buffer.ReadSlice('\n')
	if err != nil {
		return err
	}
	var keys Keys
	if err := json.Unmarshal(line, &keys); err != nil {
		return err
	}
	signer, err := hostSigner(keys.HostSeed)
	if err != nil {
		return err
	}
	config := &ssh.ServerConfig{
		MaxAuthTries: 3,
		PublicKeyCallback: func(metadata ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if metadata.User() != "loopforge" || !bytes.Equal(key.Marshal(), keys.ClientKey) {
				return nil, fmt.Errorf("workspace SSH key rejected")
			}
			return nil, nil
		},
	}
	config.AddHostKey(signer)
	if _, err := output.Write([]byte{SandboxReady}); err != nil {
		return err
	}
	transport := &stdioConn{Reader: buffer, Writer: output, input: input}
	stop := context.AfterFunc(ctx, func() { _ = transport.Close() })
	defer stop()
	timeout := time.AfterFunc(HandshakeTimeout, func() { _ = transport.Close() })
	conn, channels, requests, err := ssh.NewServerConn(transport, config)
	timeout.Stop()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	var active sync.WaitGroup
	defer func() {
		cancel()
		_ = conn.Close()
		active.Wait()
	}()
	go ssh.DiscardRequests(requests)
	slots := make(chan struct{}, 64)
	for channel := range channels {
		switch channel.ChannelType() {
		case "session", "direct-tcpip":
		default:
			_ = channel.Reject(ssh.UnknownChannelType, "unsupported workspace SSH channel")
			continue
		}
		select {
		case slots <- struct{}{}:
			active.Go(func() {
				defer func() { <-slots }()
				if channel.ChannelType() == "session" {
					serveSession(ctx, channel, workspace)
				} else {
					forwardLoopback(ctx, channel)
				}
			})
		default:
			_ = channel.Reject(ssh.ResourceShortage, "too many workspace SSH channels")
		}
	}
	return nil
}

func serveSession(ctx context.Context, incoming ssh.NewChannel, workspace string) {
	channel, requests, err := incoming.Accept()
	if err != nil {
		return
	}
	defer channel.Close()
	for request := range requests {
		var command *exec.Cmd
		ctx, cancel := context.WithCancel(ctx)
		switch request.Type {
		case "exec":
			var payload struct{ Command string }
			if ssh.Unmarshal(request.Payload, &payload) == nil {
				command = exec.CommandContext(ctx, "/bin/sh", "-c", payload.Command)
			}
		case "shell":
			command = exec.CommandContext(ctx, "/bin/sh")
		}
		if command == nil {
			cancel()
			_ = request.Reply(false, nil)
			continue
		}
		defer cancel()
		command.Dir = workspace
		command.Stdout, command.Stderr = channel, channel.Stderr()
		command.WaitDelay = 2 * time.Second
		stdin, err := command.StdinPipe()
		if err != nil {
			_ = request.Reply(false, nil)
			return
		}
		if err := command.Start(); err != nil {
			_ = request.Reply(false, nil)
			return
		}
		_ = request.Reply(true, nil)
		go func() { _, _ = io.Copy(stdin, channel); _ = stdin.Close() }()
		go func() { ssh.DiscardRequests(requests); cancel() }()
		_ = command.Wait()
		code := command.ProcessState.ExitCode()
		if code < 0 {
			code = 255
		}
		_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Code uint32 }{uint32(code)}))
		return
	}
}

func forwardLoopback(ctx context.Context, incoming ssh.NewChannel) {
	var address struct {
		Host       string
		Port       uint32
		Origin     string
		OriginPort uint32
	}
	if ssh.Unmarshal(incoming.ExtraData(), &address) != nil || address.Port == 0 || address.Port > 65535 {
		_ = incoming.Reject(ssh.Prohibited, "invalid workspace SSH forwarding destination")
		return
	}
	if strings.EqualFold(address.Host, "localhost") {
		address.Host = "127.0.0.1"
	}
	if ip := net.ParseIP(address.Host); ip == nil || !ip.IsLoopback() {
		_ = incoming.Reject(ssh.Prohibited, "workspace SSH forwarding is limited to sandbox loopback")
		return
	}
	dialer := net.Dialer{Timeout: 5 * time.Second}
	tcp, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(address.Host, strconv.Itoa(int(address.Port))))
	if err != nil {
		_ = incoming.Reject(ssh.ConnectionFailed, "sandbox port is unavailable")
		return
	}
	defer tcp.Close()
	stop := context.AfterFunc(ctx, func() { _ = tcp.Close() })
	defer stop()
	channel, requests, err := incoming.Accept()
	if err != nil {
		return
	}
	defer channel.Close()
	go ssh.DiscardRequests(requests)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = io.Copy(tcp, channel)
		_ = tcp.(*net.TCPConn).CloseWrite()
	}()
	_, _ = io.Copy(channel, tcp)
	_ = channel.CloseWrite()
	<-done
}

type stdioConn struct {
	io.Reader
	io.Writer
	input io.Closer
}

func (c *stdioConn) Close() error                     { return c.input.Close() }
func (c *stdioConn) LocalAddr() net.Addr              { return &net.UnixAddr{Name: "sandbox", Net: "unix"} }
func (c *stdioConn) RemoteAddr() net.Addr             { return &net.UnixAddr{Name: "platform", Net: "unix"} }
func (c *stdioConn) SetDeadline(time.Time) error      { return nil }
func (c *stdioConn) SetReadDeadline(time.Time) error  { return nil }
func (c *stdioConn) SetWriteDeadline(time.Time) error { return nil }
