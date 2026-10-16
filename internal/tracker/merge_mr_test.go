package tracker

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	stdsync "sync"
	"sync/atomic"
	"testing"
)

func TestParseGongfengMROpenID(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantID  int
		wantIID int
		wantOK  bool
	}{
		{
			name:   "wrapped data array with opened MR",
			raw:    `{"data":[{"id":26947209,"iid":59,"state":"opened"},{"id":1,"iid":2,"state":"merged"}]}`,
			wantID: 26947209, wantIID: 59, wantOK: true,
		},
		{
			name:   "top level array",
			raw:    `[{"id":10,"iid":3,"state":"opened"}]`,
			wantID: 10, wantIID: 3, wantOK: true,
		},
		{
			name:   "single object",
			raw:    `{"id":7,"iid":1,"state":"opened"}`,
			wantID: 7, wantIID: 1, wantOK: true,
		},
		{
			name:   "skips merged takes opened",
			raw:    `{"data":[{"id":1,"iid":1,"state":"merged"},{"id":2,"iid":2,"state":"opened"}]}`,
			wantID: 2, wantIID: 2, wantOK: true,
		},
		{
			name:   "all merged means no open MR",
			raw:    `{"data":[{"id":1,"iid":1,"state":"merged"}]}`,
			wantOK: false,
		},
		{
			name:   "empty list",
			raw:    `{"data":[]}`,
			wantOK: false,
		},
		{
			name:   "garbage payload",
			raw:    `<html>gateway error</html>`,
			wantOK: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, iid, ok := parseGongfengMROpenID(tt.raw)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v (id=%d iid=%d)", ok, tt.wantOK, id, iid)
			}
			if ok && (id != tt.wantID || iid != tt.wantIID) {
				t.Fatalf("got (%d, %d), want (%d, %d)", id, iid, tt.wantID, tt.wantIID)
			}
		})
	}
}

func TestMergeSourceBranchMRRequiresClient(t *testing.T) {
	s := &Service{}
	if _, err := s.MergeSourceBranchMR("proj", "feature/x"); err == nil || errors.Is(err, ErrNoOpenMR) {
		t.Fatalf("want configuration error, got %v", err)
	}
}

func TestParseGongfengOpenMRSkipsWrongTarget(t *testing.T) {
	raw := `{"data":[{"id":1,"iid":10,"state":"opened","target_branch":"master"},{"id":2,"iid":11,"state":"opened","target_branch":"agentflow/integration"}]}`
	id, iid, ok := parseGongfengOpenMR(raw, "agentflow/integration")
	if !ok || id != 2 || iid != 11 {
		t.Fatalf("got (%d, %d, %v), want integrate MR", id, iid, ok)
	}
	id, iid, ok = parseGongfengOpenMR(raw, "")
	if !ok || id != 1 || iid != 10 {
		t.Fatalf("empty target should take first open MR, got (%d, %d, %v)", id, iid, ok)
	}
}

func TestEnsureAndMergeBranchMRValidatesPromotion(t *testing.T) {
	s := &Service{}
	if ref, err := s.EnsureAndMergeBranchMR("proj", "main", "main", "", ""); err != nil || ref != "" {
		t.Fatalf("same-branch promotion should be a no-op, ref=%q err=%v", ref, err)
	}
	if _, err := s.EnsureAndMergeBranchMR("proj", "agentflow/integration", "main", "", ""); err == nil || errors.Is(err, ErrNoOpenMR) {
		t.Fatalf("want configuration error, got %v", err)
	}
	if _, err := s.EnsureAndMergeBranchMR("", "agentflow/integration", "main", "", ""); err == nil {
		t.Fatal("empty project must be rejected")
	}
}

func TestEnsureAndMergeBranchMRCreatesThenMerges(t *testing.T) {
	var calls []string
	var mergeType, mergeMsg string
	var createdTitle, createdTarget string
	searches := 0
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     int64  `json:"id"`
			Method string `json:"method"`
			Params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "initialize":
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"protocolVersion":"2024-11-05"}}`, req.ID)
			return
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
			return
		case "tools/call":
			calls = append(calls, req.Params.Name)
			switch req.Params.Name {
			case "search_merge_request":
				searches++
				if searches == 1 {
					fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"content":[{"type":"text","text":"{\"data\":[]}"}]}}`, req.ID)
				} else {
					fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"content":[{"type":"text","text":"{\"data\":[{\"id\":99,\"iid\":7,\"state\":\"opened\",\"target_branch\":\"agentflow/integration\"}]}"}]}}`, req.ID)
				}
			case "create_merge_request":
				createdTitle, _ = req.Params.Arguments["title"].(string)
				createdTarget, _ = req.Params.Arguments["target_branch"].(string)
				fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"content":[{"type":"text","text":"{\"id\":99,\"iid\":7,\"state\":\"opened\"}"}]}}`, req.ID)
			case "merge_merge_request":
				mergeType, _ = req.Params.Arguments["merge_type"].(string)
				mergeMsg, _ = req.Params.Arguments["merge_commit_message"].(string)
				fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"content":[{"type":"text","text":"{\"state\":\"merged\"}"}]}}`, req.ID)
			default:
				t.Fatalf("unexpected tool %q", req.Params.Name)
			}
		default:
			t.Fatalf("unexpected method %q", req.Method)
		}
	}))
	defer gateway.Close()

	s := &Service{gongfeng: NewMCPClient(gateway.URL, nil)}
	ref, err := s.EnsureAndMergeBranchMR("proj", "feature/fix-issue-30", "agentflow/integration", "AgentFlow: issue #30 → agentflow/integration", "body")
	if err != nil {
		t.Fatal(err)
	}
	if ref != "!7" {
		t.Fatalf("ref = %q", ref)
	}
	if createdTitle != "AgentFlow: issue #30 → agentflow/integration" || createdTarget != "agentflow/integration" {
		t.Fatalf("created title=%q target=%q", createdTitle, createdTarget)
	}
	if mergeType != "merge" || mergeMsg != "AgentFlow: issue #30 → agentflow/integration" {
		t.Fatalf("merge_type=%q message=%q", mergeType, mergeMsg)
	}
	if len(calls) != 4 || calls[0] != "search_merge_request" || calls[1] != "create_merge_request" || calls[2] != "search_merge_request" || calls[3] != "merge_merge_request" {
		t.Fatalf("calls = %v", calls)
	}
}

func TestEnsureAndMergeBranchMRReusesOpenTargetMR(t *testing.T) {
	var created atomic.Bool
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     int64  `json:"id"`
			Method string `json:"method"`
			Params struct {
				Name string `json:"name"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "initialize":
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"protocolVersion":"2024-11-05"}}`, req.ID)
			return
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
			return
		case "tools/call":
			switch req.Params.Name {
			case "search_merge_request":
				fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"content":[{"type":"text","text":"{\"data\":[{\"id\":88,\"iid\":4,\"state\":\"opened\",\"target_branch\":\"agentflow/integration\"}]}"}]}}`, req.ID)
			case "create_merge_request":
				created.Store(true)
				http.Error(w, "should reuse", http.StatusInternalServerError)
			case "merge_merge_request":
				fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"content":[{"type":"text","text":"{\"state\":\"merged\"}"}]}}`, req.ID)
			default:
				t.Fatalf("unexpected tool %q", req.Params.Name)
			}
		default:
			t.Fatalf("unexpected method %q", req.Method)
		}
	}))
	defer gateway.Close()

	s := &Service{gongfeng: NewMCPClient(gateway.URL, nil)}
	ref, err := s.EnsureAndMergeBranchMR("proj", "feature/x", "agentflow/integration", "title", "body")
	if err != nil {
		t.Fatal(err)
	}
	if ref != "!4" {
		t.Fatalf("ref = %q", ref)
	}
	if created.Load() {
		t.Fatal("should reuse opened MR")
	}
}

func TestEnsureAndMergeBranchMRCreatesReusesAndMerges(t *testing.T) {
	var mu stdsync.Mutex
	var calls []struct {
		Name string
		Args map[string]any
	}
	searches := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     int64  `json:"id"`
			Method string `json:"method"`
			Params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		result := any(map[string]any{})
		if req.Method == "tools/call" {
			mu.Lock()
			calls = append(calls, struct {
				Name string
				Args map[string]any
			}{req.Params.Name, req.Params.Arguments})
			if req.Params.Name == "search_merge_request" {
				searches++
				if searches == 1 {
					result = []map[string]any{}
				} else {
					result = []map[string]any{{"id": 27000042, "iid": 42, "state": "opened"}}
				}
			} else {
				result = map[string]any{"ok": true}
			}
			mu.Unlock()
			text, _ := json.Marshal(result)
			result = map[string]any{"content": []map[string]string{{"type": "text", "text": string(text)}}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}))
	t.Cleanup(server.Close)

	s := NewService()
	s.gongfeng = NewMCPClient(server.URL, nil)
	ref, err := s.EnsureAndMergeBranchMR("team/service", "agentflow/integration", "release/2026", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if ref != "!42" {
		t.Fatalf("promotion ref = %q", ref)
	}
	if len(calls) != 4 || calls[0].Name != "search_merge_request" || calls[1].Name != "create_merge_request" || calls[2].Name != "search_merge_request" || calls[3].Name != "merge_merge_request" {
		t.Fatalf("unexpected MCP calls: %+v", calls)
	}
	if calls[0].Args["source_branch"] != "agentflow/integration" || calls[0].Args["target_branch"] != "release/2026" {
		t.Fatalf("search did not use configured branches: %+v", calls[0].Args)
	}
}
