package runtime

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"agentflow/internal/domain"
)

func TestDevServerPipeScriptRelaysHTTP(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	dev := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "hello "+r.URL.Path)
	}))
	defer dev.Close()
	_, rawPort, _ := net.SplitHostPort(strings.TrimPrefix(dev.URL, "http://"))
	conn, err := dialExecPort(context.Background(), "node", "-e", devServerPipeScript, rawPort)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	for i := 0; i < 2; i++ { // keep-alive: two requests on one relay
		req, _ := http.NewRequest("GET", "http://localhost:"+rawPort+"/p"+strconv.Itoa(i), nil)
		if err := req.Write(conn); err != nil {
			t.Fatal(err)
		}
		res, err := http.ReadResponse(bufio.NewReader(conn), req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		if string(body) != "hello /p"+strconv.Itoa(i) {
			t.Fatalf("body %q", body)
		}
	}

	closed, _ := net.Listen("tcp", "127.0.0.1:0")
	_, idle, _ := net.SplitHostPort(closed.Addr().String())
	closed.Close()
	if _, err := dialExecPort(context.Background(), "node", "-e", devServerPipeScript, idle); err == nil || !strings.Contains(err.Error(), "ECONNREFUSED") {
		t.Fatalf("refused port: %v", err)
	}
}

func TestHandleDevServerHTTPThroughEndpoint(t *testing.T) {
	dev := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Connection", "keep-alive")
		_, _ = io.WriteString(w, r.Method+" "+r.URL.RequestURI()+" "+r.Host)
	}))
	defer dev.Close()
	_, rawPort, _ := net.SplitHostPort(strings.TrimPrefix(dev.URL, "http://"))
	port, _ := strconv.Atoi(rawPort)
	lookup := devServerLookup
	devServerLookup = func(context.Context, string) (browserEndpoint, bool) { return browserEndpoint{Kind: "local"}, true }
	t.Cleanup(func() { devServerLookup = lookup })

	state, err := HandleBrowser(context.Background(), "dev-run", domain.BrowserRequest{Operation: "http", HTTP: &domain.BrowserHTTPRequest{Port: port, Method: "POST", Path: "/a?b=1"}})
	if err != nil || state.HTTP == nil {
		t.Fatalf("state=%+v err=%v", state, err)
	}
	if got := string(state.HTTP.Body); got != "POST /a?b=1 localhost:"+rawPort {
		t.Fatalf("body %q", got)
	}
	if _, hop := state.HTTP.Header["Connection"]; hop {
		t.Fatal("hop-by-hop header leaked")
	}
	if _, err := HandleBrowser(context.Background(), "dev-run", domain.BrowserRequest{Operation: "http", HTTP: &domain.BrowserHTTPRequest{Port: port, Path: "no-slash"}}); err == nil {
		t.Fatal("invalid path accepted")
	}
	if _, err := HandleBrowser(context.Background(), "dev-run", domain.BrowserRequest{Operation: "http", HTTP: &domain.BrowserHTTPRequest{Port: port, Path: "/a\r\nb: c"}}); err == nil {
		t.Fatal("CRLF path accepted")
	}
	if _, err := HandleBrowser(context.Background(), "dev-run", domain.BrowserRequest{Operation: "http", HTTP: &domain.BrowserHTTPRequest{Port: port, Method: "INVALID", Path: "/"}}); err == nil {
		t.Fatal("invalid method accepted")
	}
	if _, err := HandleBrowser(context.Background(), "dev-run", domain.BrowserRequest{Operation: "http", HTTP: &domain.BrowserHTTPRequest{Port: port, Method: "TRACE", Path: "/"}}); err == nil {
		t.Fatal("TRACE method accepted")
	}
	devServerLookup = func(context.Context, string) (browserEndpoint, bool) { return browserEndpoint{}, false }
	devServerClients.Lock()
	delete(devServerClients.items, "other-run")
	devServerClients.Unlock()
	if state, _ := HandleBrowser(context.Background(), "other-run", domain.BrowserRequest{Operation: "http", HTTP: &domain.BrowserHTTPRequest{Port: port, Path: "/"}}); state.Available || state.HTTP != nil {
		t.Fatalf("missing browser: %+v", state)
	}
}

func TestDevServerDialFailuresAreNotReportedAsIdlePorts(t *testing.T) {
	for raw, want := range map[string]bool{
		"ECONNREFUSED,ECONNREFUSED":  true,
		"ECONNREFUSED,EADDRNOTAVAIL": true,
		"EADDRNOTAVAIL,ENETUNREACH":  false,
		"ETIMEDOUT,ECONNREFUSED":     false,
		"exit status 126":            false,
		"连接开发服务器超时":                  false,
	} {
		if got := devServerRefused(errors.New(raw)); got != want {
			t.Fatalf("%q: %v", raw, got)
		}
	}
	if !devServerRefused(fmt.Errorf("dial: %w", syscall.ECONNREFUSED)) {
		t.Fatal("host refusal")
	}

	lookup, dial := devServerLookup, devServerDial
	t.Cleanup(func() { devServerLookup, devServerDial = lookup, dial })
	devServerLookup = func(context.Context, string) (browserEndpoint, bool) {
		return browserEndpoint{Kind: "docker", Container: "dial-sidecar"}, true
	}
	devServerDial = func(context.Context, browserEndpoint, int) (net.Conn, error) {
		return nil, errors.New(`OCI runtime exec failed: exec: "node": executable file not found`)
	}
	state, err := HandleBrowser(context.Background(), "dial-run", domain.BrowserRequest{Operation: "http", HTTP: &domain.BrowserHTTPRequest{Port: 8000, Path: "/"}})
	if err != nil || state.HTTP != nil || !strings.Contains(state.Reason, "连接端口 8000 失败") || !strings.Contains(state.Reason, "executable file not found") {
		t.Fatalf("state=%+v err=%v", state, err)
	}
}
