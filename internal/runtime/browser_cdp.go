package runtime

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// cdpTransport carries whole CDP JSON messages to the browser-level target.
type cdpTransport interface {
	read() ([]byte, error)
	write([]byte) error
	close() error
}

type cdpMessage struct {
	ID        int64           `json:"id,omitempty"`
	Method    string          `json:"method,omitempty"`
	SessionID string          `json:"sessionId,omitempty"`
	Params    json.RawMessage `json:"params,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	Error     *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

type cdpClient struct {
	transport cdpTransport
	writeMu   sync.Mutex
	nextID    atomic.Int64
	mu        sync.Mutex
	pending   map[int64]chan cdpMessage
	onEvent   func(cdpMessage)
	done      chan struct{}
	closeOnce sync.Once
}

func newCDPClient(transport cdpTransport, onEvent func(cdpMessage)) *cdpClient {
	c := &cdpClient{transport: transport, pending: map[int64]chan cdpMessage{}, onEvent: onEvent, done: make(chan struct{})}
	go c.readLoop()
	return c
}

func (c *cdpClient) readLoop() {
	defer c.close()
	for {
		raw, err := c.transport.read()
		if err != nil {
			return
		}
		var msg cdpMessage
		if json.Unmarshal(raw, &msg) != nil {
			continue
		}
		if msg.ID != 0 {
			c.mu.Lock()
			ch := c.pending[msg.ID]
			delete(c.pending, msg.ID)
			c.mu.Unlock()
			if ch != nil {
				ch <- msg
			}
			continue
		}
		if msg.Method != "" && c.onEvent != nil {
			c.onEvent(msg)
		}
	}
}

func (c *cdpClient) closed() bool {
	select {
	case <-c.done:
		return true
	default:
		return false
	}
}

func (c *cdpClient) close() {
	c.closeOnce.Do(func() {
		close(c.done)
		_ = c.transport.close()
	})
}

func (c *cdpClient) call(ctx context.Context, session, method string, params any, out any) error {
	id := c.nextID.Add(1)
	var raw json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return err
		}
		raw = b
	}
	body, err := json.Marshal(cdpMessage{ID: id, Method: method, SessionID: session, Params: raw})
	if err != nil {
		return err
	}
	ch := make(chan cdpMessage, 1)
	c.mu.Lock()
	c.pending[id] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()
	c.writeMu.Lock()
	err = c.transport.write(body)
	c.writeMu.Unlock()
	if err != nil {
		return err
	}
	select {
	case msg := <-ch:
		if msg.Error != nil {
			return fmt.Errorf("%s: %s", method, msg.Error.Message)
		}
		if out != nil && len(msg.Result) > 0 {
			return json.Unmarshal(msg.Result, out)
		}
		return nil
	case <-c.done:
		return errors.New("浏览器连接已断开")
	case <-ctx.Done():
		return ctx.Err()
	}
}

// pipeTransport speaks newline-delimited JSON with cdp-relay.mjs.
type pipeTransport struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	reader *bufio.Reader
}

func dialDockerCDP(ctx context.Context, container string) (cdpTransport, error) {
	cmd := exec.Command("docker", "exec", "-i", container, "node", "/opt/agentflow/cdp-relay.mjs")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	t := &pipeTransport{cmd: cmd, stdin: stdin, reader: bufio.NewReaderSize(stdout, 1<<20)}
	ready := make(chan error, 1)
	go func() {
		line, err := t.read()
		if err == nil && string(line) != `{"relay":"ready"}` {
			err = fmt.Errorf("unexpected relay handshake")
		}
		ready <- err
	}()
	select {
	case err := <-ready:
		if err != nil {
			_ = t.close()
			return nil, fmt.Errorf("连接浏览器失败: %w", err)
		}
		return t, nil
	case <-ctx.Done():
		_ = t.close()
		return nil, ctx.Err()
	case <-time.After(20 * time.Second):
		_ = t.close()
		return nil, errors.New("连接浏览器超时")
	}
}

func (t *pipeTransport) read() ([]byte, error) {
	line, err := t.reader.ReadBytes('\n')
	if err != nil {
		return nil, err
	}
	return line[:len(line)-1], nil
}

func (t *pipeTransport) write(body []byte) error {
	_, err := t.stdin.Write(append(body, '\n'))
	return err
}

func (t *pipeTransport) close() error {
	_ = t.stdin.Close()
	if t.cmd.Process != nil {
		_ = t.cmd.Process.Kill()
	}
	_ = t.cmd.Wait()
	return nil
}

type wsTransport struct{ conn *websocket.Conn }

func dialWebSocketCDP(ctx context.Context, url string) (cdpTransport, error) {
	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second, ReadBufferSize: 1 << 16, WriteBufferSize: 1 << 16}
	conn, _, err := dialer.DialContext(ctx, url, nil)
	if err != nil {
		return nil, fmt.Errorf("连接浏览器失败: %w", err)
	}
	conn.SetReadLimit(64 << 20)
	return &wsTransport{conn: conn}, nil
}

func (t *wsTransport) read() ([]byte, error) {
	_, body, err := t.conn.ReadMessage()
	return body, err
}

func (t *wsTransport) write(body []byte) error {
	return t.conn.WriteMessage(websocket.TextMessage, body)
}

func (t *wsTransport) close() error { return t.conn.Close() }
