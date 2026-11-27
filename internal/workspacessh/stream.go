package workspacessh

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"time"

	"github.com/gorilla/websocket"
)

const (
	PublicKeyHeader   = "X-LoopForge-Workspace-SSH-Key"
	RelayTicketHeader = "X-LoopForge-Workspace-SSH-Ticket"
	RelayPath         = "/runners/workspace-ssh"
	HandshakeTimeout  = 20 * time.Second
	socketFrameLimit  = 64 << 10
	socketReadTimeout = 65 * time.Second
)

// Socket carries an SSH byte stream as binary WebSocket messages.
type Socket struct {
	*websocket.Conn
	reader io.Reader
}

func NewSocket(conn *websocket.Conn) *Socket {
	conn.SetReadLimit(socketFrameLimit)
	_ = conn.SetReadDeadline(time.Now().Add(socketReadTimeout))
	conn.SetPongHandler(func(string) error { return conn.SetReadDeadline(time.Now().Add(socketReadTimeout)) })
	return &Socket{Conn: conn}
}

func (s *Socket) Read(p []byte) (int, error) {
	for {
		if s.reader == nil {
			kind, reader, err := s.NextReader()
			if err != nil {
				return 0, err
			}
			if kind != websocket.BinaryMessage {
				return 0, fmt.Errorf("workspace SSH expects binary WebSocket frames")
			}
			s.reader = reader
		}
		n, err := s.reader.Read(p)
		if err != io.EOF {
			return n, err
		}
		s.reader = nil
		if n > 0 {
			return n, nil
		}
	}
}

func (s *Socket) Write(p []byte) (int, error) {
	written := 0
	for len(p) > 0 {
		size := min(len(p), socketFrameLimit)
		_ = s.SetWriteDeadline(time.Now().Add(15 * time.Second))
		if err := s.WriteMessage(websocket.BinaryMessage, p[:size]); err != nil {
			return written, err
		}
		written += size
		p = p[size:]
	}
	return written, nil
}

func (s *Socket) keepAlive(ctx context.Context) {
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)); err != nil {
				_ = s.Close()
				return
			}
		}
	}
}

// Relay closes both ends on disconnect or cancellation. Data is never stored.
func Relay(ctx context.Context, a, b io.ReadWriteCloser) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	for _, stream := range []io.ReadWriteCloser{a, b} {
		if socket, ok := stream.(*Socket); ok {
			go socket.keepAlive(ctx)
		}
	}
	copyDone := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(a, b); copyDone <- struct{}{} }()
	go func() { _, _ = io.Copy(b, a); copyDone <- struct{}{} }()
	select {
	case <-ctx.Done():
	case <-copyDone:
	}
	cancel()
	_ = a.Close()
	_ = b.Close()
}

func WebSocketURL(base, path string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	default:
		return "", fmt.Errorf("workspace SSH requires an HTTP(S) platform URL")
	}
	return u.JoinPath(path).String(), nil
}
