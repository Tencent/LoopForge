package workbuddy

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"agentflow/internal/domain"
)

const mockReport = "# 验收报告\n\nWorkBuddy 节点产物。\n"

type workBuddyTestState struct {
	promptStarted chan struct{}
	creates       atomic.Int32
	prompts       atomic.Int32
	cancels       atomic.Int32
	modelSwitches atomic.Int32
}

func testWorkBuddy(t *testing.T, stall, corrupt bool, promptUpdates ...map[string]any) (*Client, domain.WorkBuddyConfig, *workBuddyTestState) {
	t.Helper()
	state := &workBuddyTestState{promptStarted: make(chan struct{}, 1)}
	updates := make(chan string, 4)
	sum := sha256.Sum256([]byte(mockReport))
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			if !strings.HasPrefix(r.Header.Get("Authorization"), "TC3-HMAC-SHA256 Credential=demo-id/") ||
				r.Header.Get("X-TC-Token") != "demo-sts-token" || r.Header.Get("X-TC-Version") != "2026-07-09" {
				t.Error("expected Tencent Cloud TC3 authorization and STS token")
				http.Error(w, "bad signing", http.StatusForbidden)
				return
			}
			var input map[string]any
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Error(err)
				http.Error(w, "bad input", http.StatusBadRequest)
				return
			}
			response := map[string]any{"RequestId": "test"}
			switch r.Header.Get("X-TC-Action") {
			case "DescribeBuiltinModelList":
				if input["Offset"] == float64(0) {
					response["BuiltinModelSet"] = []map[string]any{{"ModelId": "demo-default", "Name": "Default"}, {"ModelId": "no-tools", "SupportsToolCall": false}}
				} else if input["Offset"] == float64(2) {
					response["BuiltinModelSet"] = []map[string]any{{"ModelId": "demo-review", "SupportsToolCall": true}}
				} else {
					t.Errorf("unexpected model catalog offset: %v", input["Offset"])
				}
				response["TotalCount"] = 3
			case "DescribeUserAccessToken":
				response["AccessToken"] = "acp-access-token"
			case "CreateAgentSession":
				state.creates.Add(1)
				if input["AgentId"] != "demo-agent" || input["VersionId"] != "v1" {
					t.Errorf("unexpected session request fields: %v", input)
				}
				response["SessionId"] = "session-01"
				response["EndpointSet"] = []map[string]string{{"EndpointType": "PUBLIC", "Url": server.URL}}
			case "DescribeSessionArtifactList":
				if input["SessionId"] != "session-01" {
					t.Errorf("unexpected artifact list request: %v", input)
				}
				response["ArtifactSet"] = []map[string]string{
					{"ArtifactId": "other", "SessionId": "other-session", "FileName": "report.md", "FilePath": "/workspace/out/report.md", "Status": "READY"},
					{"ArtifactId": "file-01", "SessionId": "session-01", "FileName": "report.md", "FilePath": "/workspace/out/report.md", "Status": "READY"},
				}
			case "DescribeSessionArtifactDownloadUrl":
				if input["ArtifactId"] != "file-01" {
					t.Errorf("unexpected artifact download request: %v", input)
				}
				response["DownloadUrl"] = server.URL + "/artifact"
				response["FileName"] = "report.md"
				response["FileSizeBytes"] = len(mockReport)
				response["FileSha256"] = fmt.Sprintf("%x", sum)
			default:
				t.Errorf("unexpected cloud API action %q", r.Header.Get("X-TC-Action"))
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"Response": response})
		case "/artifact":
			if r.Header.Get("Authorization") != "" || r.Header.Get("X-TC-Token") != "" {
				t.Error("artifact download must not forward credentials")
			}
			body := mockReport
			if corrupt {
				body = strings.Replace(mockReport, "验收", "拒收", 1)
			}
			_, _ = fmt.Fprint(w, body)
		case "/v1/agents/acp":
			if r.Header.Get("Authorization") != "Bearer acp-access-token" || r.Header.Get("X-Agent-Session-Id") != "session-01" {
				t.Error("ACP stream is not scoped to the created session")
				http.Error(w, "bad ACP authorization", http.StatusUnauthorized)
				return
			}
			serveTestACP(t, w, r, state, updates, stall, promptUpdates)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	t.Setenv(loopbackEndpointEnv, server.URL)
	config := domain.WorkBuddyConfig{Region: "ap-beijing", AgentID: "demo-agent", VersionID: "v1"}
	client, err := New(config, domain.ProjectAKSK{SecretID: "demo-id", SecretKey: "demo-secret", Token: "demo-sts-token"})
	if err != nil {
		t.Fatal(err)
	}
	return client, config, state
}

func serveTestACP(t *testing.T, w http.ResponseWriter, r *http.Request, state *workBuddyTestState, updates chan string, stall bool, promptUpdates []map[string]any) {
	t.Helper()
	if r.Method == http.MethodGet {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Acp-Connection-Id", "conn-01")
		w.(http.Flusher).Flush()
		for {
			select {
			case update := <-updates:
				_, _ = fmt.Fprintf(w, "data: %s\r\n\r\n", update)
				w.(http.Flusher).Flush()
			case <-r.Context().Done():
				return
			}
		}
	}
	if r.Header.Get("Acp-Connection-Id") != "conn-01" {
		t.Error("ACP request missing connection ID")
	}
	if r.Method == http.MethodDelete {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	var request acpMessage
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		t.Error(err)
		http.Error(w, "invalid RPC", http.StatusBadRequest)
		return
	}
	switch request.Method {
	case "initialize":
		updates <- fmt.Sprintf(`{"id":%s,"result":{"agentCapabilities":{"loadSession":true}}}`, request.ID)
		w.WriteHeader(http.StatusAccepted)
	case "session/load":
		updates <- fmt.Sprintf(`{"id":%s,"result":{"models":{"currentModelId":"demo-default"}}}`, request.ID)
		w.WriteHeader(http.StatusAccepted)
	case "session/set_model":
		var selection struct {
			SessionID string `json:"sessionId"`
			ModelID   string `json:"modelId"`
		}
		if err := json.Unmarshal(request.Params, &selection); err != nil || selection.SessionID != "session-01" || state.prompts.Load() != 0 {
			t.Errorf("unexpected model selection at prompt count %d: %s", state.prompts.Load(), request.Params)
		}
		state.modelSwitches.Add(1)
		if selection.ModelID == "demo-review" {
			updates <- fmt.Sprintf(`{"id":%s,"result":{}}`, request.ID)
		} else {
			updates <- fmt.Sprintf(`{"id":%s,"error":{"code":-32602,"message":"unknown model"}}`, request.ID)
		}
		w.WriteHeader(http.StatusAccepted)
	case "session/prompt":
		state.prompts.Add(1)
		state.promptStarted <- struct{}{}
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		if stall {
			<-r.Context().Done()
			return
		}
		_, _ = fmt.Fprint(w, "data: {\"method\":\"session/update\",\"params\":{\"sessionId\":\"session-01\",\"update\":{\"sessionUpdate\":\"agent_message_chunk\",\"content\":{\"text\":\"完成\"}}}}\r\n\r\n")
		for _, update := range promptUpdates {
			event, err := json.Marshal(map[string]any{
				"method": "session/update", "params": map[string]any{"sessionId": "session-01", "update": update},
			})
			if err != nil {
				t.Error(err)
				return
			}
			_, _ = fmt.Fprintf(w, "data: %s\r\n\r\n", event)
		}
		_, _ = fmt.Fprintf(w, "data: {\"id\":%s,\"result\":{\"stopReason\":\"end_turn\",\"usage\":{\"inputTokens\":12,\"outputTokens\":5}}}\r\n\r\n", request.ID)
	case "session/cancel":
		state.cancels.Add(1)
		w.WriteHeader(http.StatusAccepted)
	default:
		t.Errorf("unexpected ACP method %q", request.Method)
		http.Error(w, "unsupported", http.StatusBadRequest)
	}
}

func TestWorkBuddySignedSessionAndArtifact(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		t.Run(fmt.Sprintf("corrupt=%t", corrupt), func(t *testing.T) {
			client, config, state := testWorkBuddy(t, false, corrupt)
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			token, err := client.AccessToken(ctx)
			if err != nil {
				t.Fatal(err)
			}
			session, err := client.CreateSession(ctx, config)
			if err != nil {
				t.Fatal(err)
			}
			turn, err := client.RunTurn(ctx, session, token, "", "write a report", nil, nil)
			if err != nil || turn.Text != "完成" || turn.Usage.Input != 12 || turn.Usage.Output != 5 {
				t.Fatalf("turn = %#v, %v", turn, err)
			}
			dir := t.TempDir()
			err = client.StageArtifacts(ctx, session, []domain.ArtifactContract{{Name: "report", Path: "out/report.md", Required: true}}, dir, nil)
			file, readErr := os.ReadFile(filepath.Join(dir, "out", "report.md"))
			if corrupt {
				if err == nil || !strings.Contains(err.Error(), "SHA-256") || !errors.Is(readErr, os.ErrNotExist) {
					t.Fatalf("expected integrity error without a written file, got %v and %v", err, readErr)
				}
			} else if err != nil || readErr != nil || string(file) != mockReport {
				t.Fatalf("WorkBuddy artifact = %q, %v, %v", file, err, readErr)
			}
			if state.creates.Load() != 1 || state.prompts.Load() != 1 {
				t.Error("session creation and prompt should run once")
			}
		})
	}
}

func TestWorkBuddyModelCatalogAndSessionSelection(t *testing.T) {
	for _, tt := range []struct {
		requested string
		active    string
		switches  int32
		prompts   int32
	}{
		{requested: "", active: "demo-default", prompts: 1},
		{requested: "demo-default", active: "demo-default", prompts: 1},
		{requested: "demo-review", active: "demo-review", switches: 1, prompts: 1},
		{requested: "unavailable", switches: 1},
	} {
		t.Run(tt.requested, func(t *testing.T) {
			client, config, state := testWorkBuddy(t, false, false)
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			models, err := client.Models(ctx)
			if err != nil || len(models) != 2 || models[0] != (Model{ID: "demo-default", Name: "Default"}) || models[1] != (Model{ID: "demo-review", Name: "demo-review"}) || state.creates.Load() != 0 {
				t.Fatalf("models = %#v, err = %v, created sessions = %d", models, err, state.creates.Load())
			}
			session, err := client.CreateSession(ctx, config)
			if err != nil {
				t.Fatal(err)
			}
			turn, err := client.RunTurn(ctx, session, "acp-access-token", tt.requested, "write a report", nil, nil)
			if tt.prompts == 0 {
				if err == nil || !strings.Contains(err.Error(), tt.requested) {
					t.Fatalf("expected model error, got %v", err)
				}
			} else if err != nil || turn.Model != tt.active || turn.Usage.Model != tt.active {
				t.Fatalf("turn = %#v, error = %v, want model %q", turn, err, tt.active)
			}
			if state.modelSwitches.Load() != tt.switches || state.prompts.Load() != tt.prompts {
				t.Fatalf("switches = %d, prompts = %d", state.modelSwitches.Load(), state.prompts.Load())
			}
		})
	}
}

func TestWorkBuddyCancellationSendsRemoteCancel(t *testing.T) {
	client, config, state := testWorkBuddy(t, true, false)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	session, err := client.CreateSession(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := client.RunTurn(ctx, session, "acp-access-token", "", "wait for cancellation", nil, nil)
		done <- err
	}()
	select {
	case <-state.promptStarted:
		cancel()
	case <-ctx.Done():
		t.Fatal("prompt did not start")
	}
	if err := <-done; !errors.Is(err, context.Canceled) || state.cancels.Load() != 1 || state.prompts.Load() != 1 {
		t.Fatalf("expected cancellation of one turn, got %v, prompts %d, cancels %d", err, state.prompts.Load(), state.cancels.Load())
	}
}
