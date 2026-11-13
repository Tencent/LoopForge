package httpapi

import (
	"agentflow/internal/domain"
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"github.com/gin-gonic/gin"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestAssistantSSERecoversEvictedTokensAndDisconnectKeepsModelRunning(t *testing.T) {
	h, user := workbenchFixture(t)
	ready, next, finish, nextSent := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	h.generate = func(ctx context.Context, _ string, _ domain.User) (string, error) {
		sink := ctx.Value(assistantGenerationSinkKey{}).(func(string, any))
		for i := 0; i < 700; i++ {
			sink("thought", assistantStreamEvent{Kind: "answer", Text: "字"})
		}
		close(ready)
		select {
		case <-next:
		case <-ctx.Done():
			return "", ctx.Err()
		}
		sink("thought", assistantStreamEvent{Kind: "answer", Text: "后"})
		close(nextSent)
		select {
		case <-finish:
		case <-ctx.Done():
			return "", ctx.Err()
		}
		return strings.Repeat("字", 700) + "后", nil
	}
	c, rec := workbenchContext(user, "POST", "/api/assistant/generations", bytes.NewBufferString(`{"sessionId":"sse-recovery","projectId":"workbench","messages":[{"role":"user","content":"写一段话"}]}`))
	h.startGeneration(c)
	if rec.Code != 202 {
		t.Fatal(rec.Body.String())
	}
	select {
	case <-ready:
	case <-time.After(3 * time.Second):
		t.Fatal("model did not start")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _ := gin.CreateTestContext(w)
		c.Request = r.WithContext(context.WithValue(r.Context(), requestUserContextKey{}, domainUserToAuth(user)))
		c.Params = gin.Params{{Key: "id", Value: "sse-recovery"}}
		h.generation(c)
	}))
	defer server.Close()
	client := server.Client()
	client.Timeout = 3 * time.Second
	response, err := client.Get(server.URL + "?stream=1")
	if err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 4096), 2<<20)
	read := func() domain.AssistantGeneration {
		t.Helper()
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "data: ") {
				var g domain.AssistantGeneration
				if err := json.Unmarshal([]byte(line[6:]), &g); err != nil {
					t.Fatal(err)
				}
				return g
			}
		}
		t.Fatal("missing SSE frame", scanner.Err())
		return domain.AssistantGeneration{}
	}
	first := read()
	if first.Stream == nil || first.Stream.Sequence < 700 || first.Stream.Answer != strings.Repeat("字", 700) || len(first.Events) != 0 {
		t.Fatal("reconnect lost prefix", first.Stream)
	}
	close(next)
	<-nextSent
	update := read()
	if update.Status != "running" || len(update.Events) != 1 || update.Events[0].Sequence != first.Stream.Sequence+1 {
		t.Fatalf("token was not delivered immediately: %+v", update)
	}
	response.Body.Close()
	close(finish)
	deadline := time.Now().Add(3 * time.Second)
	for {
		g, _ := h.repo.AssistantGeneration(user.ID, "sse-recovery")
		if g.Status == "completed" {
			if g.Stream == nil || g.Stream.Sequence != first.Stream.Sequence+1 || g.Stream.Answer != strings.Repeat("字", 700)+"后" {
				t.Fatal("persisted snapshot lost output")
			}
			break
		}
		if g.Status != "running" || time.Now().After(deadline) {
			t.Fatal("browser disconnect stopped model", g.Status, g.Error)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestAssistantProgressReplyContinuesAndHasBoundedFailure(t *testing.T) {
	for _, stuck := range []bool{false, true} {
		t.Run(map[bool]string{false: "continues", true: "bounded"}[stuck], func(t *testing.T) {
			h, user := workbenchFixture(t)
			calls := 0
			h.generate = func(_ context.Context, _ string, _ domain.User) (string, error) {
				calls++
				if stuck || calls == 1 {
					return "稍等，我先查看项目里的 SOP。", nil
				}
				return "项目中已有后端研发 SOP，共四个节点。", nil
			}
			c, rec := workbenchContext(user, "POST", "/api/assistant/chat", bytes.NewBufferString(`{"sessionId":"progress-check","projectId":"workbench","messages":[{"role":"user","content":"查看已有 SOP 并给出结果"}]}`))
			h.chat(c)
			if stuck {
				if rec.Code != 502 || calls != 3 {
					t.Fatalf("unbounded/false completion: calls=%d %s", calls, rec.Body.String())
				}
			} else if rec.Code != 200 || calls != 2 || !strings.Contains(rec.Body.String(), "四个节点") {
				t.Fatal(calls, rec.Body.String())
			}
		})
	}
	for _, input := range []string{`{"message":"第一行\n第二行\u4f`, `{"drafts":[],"message":"你好\\`, `{"message":"你好"}`} {
		got := assistantDraftMessagePrefix(input)
		if strings.Contains(got, `\u4f`) {
			t.Fatal("partial JSON escape leaked", got)
		}
	}
	if _, err := decodeAssistantCLIResult("max turns reached", true, nil, nil); err == nil {
		t.Fatal("max turns became a successful answer")
	}
	if assistantProgressOnly("稍等", []assistantMessage{{Role: "user", Content: "只回复：稍等"}}) {
		t.Fatal("literal response retried")
	}
}

func TestPersistedGenerationStreamPublishesCancelWithoutNewTokens(t *testing.T) {
	var cancelled, finished atomic.Bool
	g := domain.AssistantGeneration{ID: "remote-cancel-only", Status: "running"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _ := gin.CreateTestContext(w)
		c.Request = r
		serveAssistantGenerationStream(c, g, func() (domain.AssistantGeneration, bool) {
			current := g
			current.CancelRequested = cancelled.Load()
			if finished.Load() {
				current.Status = "cancelled"
			}
			return current, true
		})
	}))
	defer server.Close()
	client := server.Client()
	client.Timeout = 3 * time.Second
	response, err := client.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	scanner := bufio.NewScanner(response.Body)
	read := func() domain.AssistantGeneration {
		t.Helper()
		for scanner.Scan() {
			if strings.HasPrefix(scanner.Text(), "data: ") {
				var current domain.AssistantGeneration
				if err := json.Unmarshal([]byte(strings.TrimPrefix(scanner.Text(), "data: ")), &current); err != nil {
					t.Fatal(err)
				}
				return current
			}
		}
		t.Fatal("missing state-only SSE update", scanner.Err())
		return domain.AssistantGeneration{}
	}
	if read().CancelRequested {
		t.Fatal("initial state unexpectedly stopping")
	}
	cancelled.Store(true)
	if current := read(); !current.CancelRequested || current.Status != "running" {
		t.Fatalf("missing stopping state: %+v", current)
	}
	finished.Store(true)
	if read().Status != "cancelled" {
		t.Fatal("missing actual stopped state")
	}
}
