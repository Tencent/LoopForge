package runtime

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"agentflow/internal/domain"
)

// Dev server access: the user's own browser renders a dev server that runs
// in the run sandbox, and this side forwards each HTTP request to
// localhost:port in the sandbox's network namespace. Docker sandboxes are
// reached through the browser sidecar (it shares that namespace and ships
// node); host drivers reach the port directly.

const (
	// DevServerBodyLimit keeps a response, base64 encoded, inside the runner
	// socket's 20 MiB message limit.
	DevServerBodyLimit   = 12 << 20
	devServerEndpointTTL = 15 * time.Second
	devServerClientIdle  = 10 * time.Minute
)

// ErrDevServerUnreachable means nothing accepted a connection on the port.
var ErrDevServerUnreachable = errors.New("dev server unreachable")

type devServerClient struct {
	endpoint browserEndpoint
	checked  time.Time
	used     time.Time
	client   *http.Client
}

var devServerClients = struct {
	sync.Mutex
	items map[string]*devServerClient
}{items: map[string]*devServerClient{}}

// devServerDial is swapped in tests.
var devServerDial = func(ctx context.Context, endpoint browserEndpoint, port int) (net.Conn, error) {
	if endpoint.Kind == "docker" {
		return dialDockerPort(ctx, endpoint.Container, port)
	}
	var d net.Dialer
	return d.DialContext(ctx, "tcp", net.JoinHostPort("localhost", strconv.Itoa(port)))
}

// devServerLookup is swapped in tests.
var devServerLookup = lookupBrowserEndpoint

func handleDevServerHTTP(ctx context.Context, runID string, req *domain.BrowserHTTPRequest) (domain.BrowserState, error) {
	if req == nil || req.Port <= 0 || req.Port > 65535 || !strings.HasPrefix(req.Path, "/") {
		return domain.BrowserState{}, errors.New("无效的开发服务器请求")
	}
	client, endpoint, ok := devServerClientFor(ctx, runID)
	if !ok {
		return domain.BrowserState{Available: false, Reason: "Agent 浏览器尚未启动：开发服务器预览需要它所在的沙箱网络"}, nil
	}
	method := strings.ToUpper(strings.TrimSpace(req.Method))
	if method == "" {
		method = http.MethodGet
	}
	target := "http://localhost:" + strconv.Itoa(req.Port) + req.Path
	out, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(req.Body))
	if err != nil {
		return domain.BrowserState{}, err
	}
	for name, values := range req.Header {
		if devServerHopHeader(name) || strings.EqualFold(name, "Host") {
			continue
		}
		for _, v := range values {
			out.Header.Add(name, v)
		}
	}
	// Dev servers check Host against their allow list (Vite allowedHosts).
	out.Host = "localhost:" + strconv.Itoa(req.Port)
	res, err := client.Do(out)
	if err != nil {
		if errors.Is(err, ErrDevServerUnreachable) {
			forgetDevServerEndpoint(runID, endpoint)
		}
		var unreachable devServerDialError
		if errors.As(err, &unreachable) {
			if devServerRefused(unreachable.err) {
				return domain.BrowserState{Available: true, Kind: endpoint.Kind, Reason: fmt.Sprintf("端口 %d 上没有正在监听的开发服务器", req.Port)}, nil
			}
			log.Printf("browser dev server dial run=%s kind=%s container=%s port=%d: %v", runID, endpoint.Kind, endpoint.Container, req.Port, unreachable.err)
			return domain.BrowserState{Available: true, Kind: endpoint.Kind, Reason: fmt.Sprintf("连接端口 %d 失败：%v", req.Port, unreachable.err)}, nil
		}
		if ctx.Err() != nil {
			return domain.BrowserState{Available: true, Kind: endpoint.Kind, Reason: fmt.Sprintf("端口 %d 的开发服务器响应超时", req.Port)}, nil
		}
		return domain.BrowserState{Available: true, Kind: endpoint.Kind, Reason: "访问开发服务器失败：" + err.Error()}, nil
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, DevServerBodyLimit+1))
	if err != nil {
		return domain.BrowserState{Available: true, Kind: endpoint.Kind, Reason: "读取开发服务器响应失败：" + err.Error()}, nil
	}
	if len(body) > DevServerBodyLimit {
		return domain.BrowserState{Available: true, Kind: endpoint.Kind, Reason: fmt.Sprintf("响应超过 %d MB，预览暂不支持", DevServerBodyLimit>>20)}, nil
	}
	header := map[string][]string{}
	for name, values := range res.Header {
		if devServerHopHeader(name) || strings.EqualFold(name, "Content-Length") {
			continue
		}
		header[name] = values
	}
	return domain.BrowserState{Available: true, Kind: endpoint.Kind, HTTP: &domain.BrowserHTTPResponse{Status: res.StatusCode, Header: header, Body: body}}, nil
}

var devServerHop = map[string]bool{
	"connection": true, "keep-alive": true, "proxy-connection": true, "proxy-authenticate": true,
	"proxy-authorization": true, "te": true, "trailer": true, "transfer-encoding": true, "upgrade": true,
}

func devServerHopHeader(name string) bool { return devServerHop[strings.ToLower(name)] }

func devServerClientFor(ctx context.Context, runID string) (*http.Client, browserEndpoint, bool) {
	now := time.Now()
	devServerClients.Lock()
	for id, c := range devServerClients.items {
		if now.Sub(c.used) > devServerClientIdle {
			c.client.CloseIdleConnections()
			delete(devServerClients.items, id)
		}
	}
	cached := devServerClients.items[runID]
	if cached != nil && now.Sub(cached.checked) < devServerEndpointTTL {
		cached.used = now
		devServerClients.Unlock()
		return cached.client, cached.endpoint, true
	}
	devServerClients.Unlock()
	endpoint, ok := devServerLookup(ctx, runID)
	if !ok {
		return nil, browserEndpoint{}, false
	}
	devServerClients.Lock()
	defer devServerClients.Unlock()
	cached = devServerClients.items[runID]
	if cached != nil && cached.endpoint == endpoint {
		cached.checked, cached.used = now, now
		return cached.client, endpoint, true
	}
	if cached != nil {
		cached.client.CloseIdleConnections()
	}
	client := newDevServerHTTPClient(endpoint)
	devServerClients.items[runID] = &devServerClient{endpoint: endpoint, checked: now, used: now, client: client}
	return client, endpoint, true
}

// newDevServerHTTPClient builds the client that forwards requests to the dev
// server behind endpoint: every connection dials the requested port through
// devServerDial, and redirects are returned to the browser instead of being
// followed here.
func newDevServerHTTPClient(endpoint browserEndpoint) *http.Client {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
			_, rawPort, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			port, err := strconv.Atoi(rawPort)
			if err != nil {
				return nil, err
			}
			conn, err := devServerDial(ctx, endpoint, port)
			if err != nil {
				return nil, devServerDialError{err}
			}
			return conn, nil
		},
		// Each docker connection is a process; keep a few warm per run so a
		// page load does not pay a docker exec per request.
		MaxIdleConnsPerHost:   8,
		IdleConnTimeout:       90 * time.Second,
		ResponseHeaderTimeout: 2 * time.Minute,
		ForceAttemptHTTP2:     false,
	}
	return &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func forgetDevServerEndpoint(runID string, endpoint browserEndpoint) {
	devServerClients.Lock()
	defer devServerClients.Unlock()
	if c := devServerClients.items[runID]; c != nil && c.endpoint == endpoint {
		c.checked = time.Time{}
	}
}

// devServerRefused tells a port nobody listens on from a relay that could
// not reach the sandbox at all: only refusals (and a loopback family the
// sandbox lacks) mean the dev server is not up.
func devServerRefused(err error) bool {
	if errors.Is(err, syscall.ECONNREFUSED) {
		return true
	}
	refused := false
	for _, code := range strings.Split(strings.TrimSpace(err.Error()), ",") {
		switch strings.TrimSpace(code) {
		case "ECONNREFUSED":
			refused = true
		case "EADDRNOTAVAIL", "ENETUNREACH", "EAFNOSUPPORT":
		default:
			return false
		}
	}
	return refused
}

type devServerDialError struct{ err error }

func (e devServerDialError) Error() string { return e.err.Error() }
func (e devServerDialError) Unwrap() error { return ErrDevServerUnreachable }

// devServerPipeScript connects stdio to localhost:port inside the sidecar.
// It prints one 0x01 byte once connected, so a refused port fails the dial
// instead of surfacing later as an empty response. Vite and friends may bind
// either loopback family, so ::1 is tried after 127.0.0.1; a failure
// reports each attempt's error code, comma separated.
const devServerPipeScript = `const net=require("net");const port=+process.argv[1];const hosts=["127.0.0.1","::1"];const codes=[];` +
	`(function go(){const s=net.connect({port,host:hosts.shift()});let up=false;` +
	`s.once("connect",()=>{up=true;process.stdout.write("\x01");process.stdin.pipe(s);s.on("data",d=>process.stdout.write(d));s.on("close",()=>process.stdout.write("",()=>process.exit(0)));});` +
	`s.on("error",e=>{if(up)return;codes.push(String(e.code||e));if(hosts.length)return go();process.stderr.write(codes.join(","));process.exit(2);});})();`

func dialDockerPort(ctx context.Context, container string, port int) (net.Conn, error) {
	return dialExecPort(ctx, "docker", "exec", "-i", container, "node", "-e", devServerPipeScript, strconv.Itoa(port))
}

// dialExecPort runs a command that relays its stdio to a TCP port.
func dialExecPort(ctx context.Context, name string, args ...string) (net.Conn, error) {
	cmd := exec.Command(name, args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	conn := &execConn{cmd: cmd, stdin: stdin, stdout: stdout}
	ready := make(chan error, 1)
	go func() {
		var b [1]byte
		_, err := io.ReadFull(stdout, b[:])
		if err == nil && b[0] != 1 {
			err = errors.New("unexpected handshake")
		}
		ready <- err
	}()
	select {
	case err := <-ready:
		if err != nil {
			_ = conn.Close()
			if msg := strings.TrimSpace(stderr.String()); msg != "" {
				return nil, errors.New(msg)
			}
			return nil, err
		}
		return conn, nil
	case <-ctx.Done():
		_ = conn.Close()
		return nil, ctx.Err()
	case <-time.After(15 * time.Second):
		_ = conn.Close()
		return nil, errors.New("连接开发服务器超时")
	}
}

// execConn is a net.Conn over a child process's stdio.
type execConn struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	once   sync.Once
}

func (c *execConn) Read(p []byte) (int, error)  { return c.stdout.Read(p) }
func (c *execConn) Write(p []byte) (int, error) { return c.stdin.Write(p) }
func (c *execConn) Close() error {
	c.once.Do(func() {
		_ = c.stdin.Close()
		if c.cmd.Process != nil {
			_ = c.cmd.Process.Kill()
		}
		go func() { _ = c.cmd.Wait() }()
	})
	return nil
}
func (c *execConn) LocalAddr() net.Addr              { return execAddr{} }
func (c *execConn) RemoteAddr() net.Addr             { return execAddr{} }
func (c *execConn) SetDeadline(time.Time) error      { return nil }
func (c *execConn) SetReadDeadline(time.Time) error  { return nil }
func (c *execConn) SetWriteDeadline(time.Time) error { return nil }

type execAddr struct{}

func (execAddr) Network() string { return "exec" }
func (execAddr) String() string  { return "docker-exec" }
