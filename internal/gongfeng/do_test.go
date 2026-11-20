package gongfeng

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDoReturnsStatusAndBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("payload"))
	}))
	defer server.Close()
	req, _ := http.NewRequest(http.MethodGet, server.URL, nil)
	status, body, err := do(req)
	if err != nil || status != http.StatusTeapot || string(body) != "payload" {
		t.Fatalf("do = %d, %q, %v", status, body, err)
	}
}

func TestDoLimitsResponseBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", maxResponseBytes+1024)))
	}))
	defer server.Close()
	req, _ := http.NewRequest(http.MethodGet, server.URL, nil)
	status, body, err := do(req)
	if err != nil || status != http.StatusOK || len(body) != maxResponseBytes {
		t.Fatalf("do = %d, len %d, %v", status, len(body), err)
	}
}

func TestDoWrapsConnectionError(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	url := server.URL
	server.Close()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	if _, _, err := do(req); err == nil || !strings.HasPrefix(err.Error(), "无法连接工蜂：") {
		t.Fatalf("do error = %v", err)
	}
}
