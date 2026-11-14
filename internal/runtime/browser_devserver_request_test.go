package runtime

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"syscall"
	"testing"
	"time"
)

func TestDevServerMethodNormalizesAllowlist(t *testing.T) {
	for raw, want := range map[string]string{
		"":          http.MethodGet,
		"  ":        http.MethodGet,
		"get":       http.MethodGet,
		" head ":    http.MethodHead,
		"post":      http.MethodPost,
		"Put":       http.MethodPut,
		"patch":     http.MethodPatch,
		"DELETE":    http.MethodDelete,
		"\toptions": http.MethodOptions,
	} {
		got, ok := devServerMethod(raw)
		if !ok || got != want {
			t.Fatalf("devServerMethod(%q) = %q, %v; want %q, true", raw, got, ok, want)
		}
	}
	for _, raw := range []string{"CONNECT", "trace", "PROPFIND", "GET\r\nX-Injected: 1", "G ET"} {
		if got, ok := devServerMethod(raw); ok || got != "" {
			t.Fatalf("devServerMethod(%q) = %q, %v; want rejected", raw, got, ok)
		}
	}
}

func TestDevServerFailureStateReasons(t *testing.T) {
	endpoint := browserEndpoint{Kind: "docker", Container: "sidecar"}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	cases := []struct {
		name string
		ctx  context.Context
		err  error
		want string
	}{
		{"refused", context.Background(), fmt.Errorf("dial: %w", devServerDialError{syscall.ECONNREFUSED}), "端口 5173 上没有正在监听的开发服务器"},
		{"dial failure", context.Background(), devServerDialError{errors.New("EHOSTUNREACH")}, "连接端口 5173 失败：EHOSTUNREACH"},
		{"dial failure wins over timeout", cancelled, devServerDialError{errors.New("EHOSTUNREACH")}, "连接端口 5173 失败：EHOSTUNREACH"},
		{"timeout", cancelled, context.Canceled, "端口 5173 的开发服务器响应超时"},
		{"other", context.Background(), errors.New("boom"), "访问开发服务器失败：boom"},
	}
	for _, tc := range cases {
		state := devServerFailureState(tc.ctx, "dev-failure-run", endpoint, 5173, tc.err)
		if !state.Available || state.Kind != "docker" || state.HTTP != nil || state.Reason != tc.want {
			t.Fatalf("%s: state=%+v, want reason %q", tc.name, state, tc.want)
		}
	}
}

func TestDevServerFailureStateForgetsUnreachableEndpoint(t *testing.T) {
	const runID = "dev-failure-forget-run"
	endpoint := browserEndpoint{Kind: "docker", Container: "sidecar"}
	checked := time.Now()
	devServerClients.Lock()
	devServerClients.items[runID] = &devServerClient{endpoint: endpoint, checked: checked, used: checked, client: &http.Client{}}
	devServerClients.Unlock()
	t.Cleanup(func() {
		devServerClients.Lock()
		delete(devServerClients.items, runID)
		devServerClients.Unlock()
	})
	checkedAt := func() time.Time {
		devServerClients.Lock()
		defer devServerClients.Unlock()
		return devServerClients.items[runID].checked
	}

	devServerFailureState(context.Background(), runID, endpoint, 5173, errors.New("boom"))
	if !checkedAt().Equal(checked) {
		t.Fatal("non-dial failure forgot the endpoint")
	}
	devServerFailureState(context.Background(), runID, endpoint, 5173, devServerDialError{syscall.ECONNREFUSED})
	if !checkedAt().IsZero() {
		t.Fatal("unreachable endpoint was not forgotten")
	}
}
