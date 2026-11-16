package runtime

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func TestReadStdioRPCResultSkipsUnrelatedLines(t *testing.T) {
	lines := strings.Join([]string{
		"not json",
		`{"jsonrpc":"2.0","method":"notifications/message","params":{}}`,
		`{"jsonrpc":"2.0","id":1,"result":{"stale":true}}`,
		`{"jsonrpc":"2.0","id":2,"result":{"ok":true}}`,
	}, "\n")
	raw, err := readStdioRPCResult(context.Background(), bufio.NewScanner(strings.NewReader(lines)), &bytes.Buffer{}, 2)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(raw) != `{"ok":true}` {
		t.Fatalf("result = %s", raw)
	}
}

func TestReadStdioRPCResultReturnsRPCError(t *testing.T) {
	line := `{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"method not found"}}`
	_, err := readStdioRPCResult(context.Background(), bufio.NewScanner(strings.NewReader(line)), &bytes.Buffer{}, 1)
	if err == nil || err.Error() != "mcp error [-32601]: method not found" {
		t.Fatalf("err = %v", err)
	}
}

func TestReadStdioRPCResultReportsExitWithStderr(t *testing.T) {
	stderr := bytes.NewBufferString("  boom: missing token \n")
	_, err := readStdioRPCResult(context.Background(), bufio.NewScanner(strings.NewReader("")), stderr, 1)
	if err == nil || err.Error() != "stdio MCP exited before response: boom: missing token" {
		t.Fatalf("err = %v", err)
	}
}

func TestReadStdioRPCResultPrefersContextError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := readStdioRPCResult(ctx, bufio.NewScanner(strings.NewReader("")), bytes.NewBufferString("ignored"), 1)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestWriteStdioRPCWritesOneLine(t *testing.T) {
	var out bytes.Buffer
	if err := writeStdioRPC(&out, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list"}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := out.String(); got != `{"id":2,"jsonrpc":"2.0","method":"tools/list"}`+"\n" {
		t.Fatalf("output = %q", got)
	}
	if err := writeStdioRPC(&out, map[string]any{"bad": make(chan int)}); err == nil {
		t.Fatal("expected marshal error")
	}
}
