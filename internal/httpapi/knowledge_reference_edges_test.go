package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"reflect"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/knowledge"
)

func TestKnowledgeCallErrorRedactsUpstream(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"deadline", context.DeadlineExceeded, "超时"},
		{"403", errors.New("gateway HTTP 403: secret upstream body"), "拒绝访问"},
		{"401", errors.New("gateway HTTP 401: Bearer leaked-token"), "拒绝访问"},
		{"generic", errors.New("gateway HTTP 500: internal detail https://secret.host"), "不可用"},
		{"plain", errors.New("connection reset by peer"), "不可用"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := knowledgeCallError(tc.err).Error()
			if !strings.Contains(got, tc.want) {
				t.Fatalf("knowledgeCallError(%v) = %q, want containing %q", tc.err, got, tc.want)
			}
			// Must never echo upstream bodies, tokens or hosts.
			for _, leak := range []string{"secret upstream body", "leaked-token", "secret.host", "internal detail"} {
				if strings.Contains(got, leak) {
					t.Fatalf("knowledgeCallError leaked %q into %q", leak, got)
				}
			}
		})
	}
}

type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }
func (timeoutError) Unwrap() error   { return nil }

func TestKnowledgeCallErrorNetTimeout(t *testing.T) {
	var nerr net.Error = timeoutError{}
	got := knowledgeCallError(nerr).Error()
	if !strings.Contains(got, "超时") {
		t.Fatalf("net timeout error = %q, want 超时", got)
	}
}

func TestKnowledgeReadResultStates(t *testing.T) {
	cases := []struct {
		name       string
		serverID   string
		tool       string
		text       string
		hits       int
		err        error
		wantStatus string
		wantCite   bool
	}{
		{"success with text", domain.KnowledgeMCPServerID, knowledge.ToolSearch, "some answer", 1, nil, "已返回", true},
		{"blank text", domain.KnowledgeMCPServerID, knowledge.ToolFetch, "   ", 0, nil, "无匹配", false},
		{"search zero hits", domain.KnowledgeMCPServerID, knowledge.ToolSearch, `{"results":[]}`, 0, nil, "无匹配", false},
		{"external zero hits not forced no-match", "handbook", knowledge.ToolSearch, "raw", 0, nil, "已返回", true},
		{"call failure", domain.KnowledgeMCPServerID, knowledge.ToolSearch, "", 0, errors.New("boom"), "调用失败", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			read, text := knowledgeReadResult(tc.serverID, tc.tool, tc.text, tc.hits, tc.err)
			if read.Status != tc.wantStatus {
				t.Fatalf("status = %q, want %q", read.Status, tc.wantStatus)
			}
			if read.ID == "" {
				t.Fatal("knowledge read id must not be empty")
			}
			if read.ServerID != tc.serverID || read.Tool != tc.tool {
				t.Fatalf("read = %+v, unexpected server/tool", read)
			}
			cited := strings.Contains(text, "[[knowledge:"+read.ID+"]]")
			if cited != tc.wantCite {
				t.Fatalf("citation marker present = %v, want %v (text=%q)", cited, tc.wantCite, text)
			}
		})
	}
}

func TestStringList(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want []string
	}{
		{"nil", nil, nil},
		{"string slice", []string{"a", "b"}, []string{"a", "b"}},
		{"any slice", []any{"a", "b"}, []string{"a", "b"}},
		{"any slice skip non-string", []any{"a", 1, "b"}, []string{"a", "b"}},
		{"comma string", "a,b,c", []string{"a", "b", "c"}},
		{"single string", "a", []string{"a"}},
		{"blank string", "  ", nil},
		{"other type", 42, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := stringList(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("stringList(%v) = %#v, want %#v", tc.in, got, tc.want)
			}
		})
	}
}

func TestValueOr(t *testing.T) {
	if got := valueOr(nil, "fallback"); got != "fallback" {
		t.Fatalf("valueOr(nil) = %v", got)
	}
	if got := valueOr("value", "fallback"); got != "value" {
		t.Fatalf("valueOr(value) = %v", got)
	}
	if got := valueOr(0, 99); got != 0 {
		t.Fatalf("valueOr(0) must keep zero value, got %v", got)
	}
}

func TestFilterSourcesText(t *testing.T) {
	payload := `{"sources":[{"id":"dns","name":"DNS"},{"id":"api","name":"API"},{"name":"no-id"}]}`
	out, n, err := filterSourcesText(payload, []string{"dns"})
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("kept count = %d, want 1", n)
	}
	var parsed struct {
		Sources []struct {
			ID string `json:"id"`
		} `json:"sources"`
	}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("output not valid JSON: %v", err)
	}
	if len(parsed.Sources) != 1 || parsed.Sources[0].ID != "dns" {
		t.Fatalf("filtered sources = %+v, want only dns", parsed.Sources)
	}
	if _, _, err := filterSourcesText("not json", []string{"dns"}); err == nil {
		t.Fatal("invalid JSON must fail")
	}
}
