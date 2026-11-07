package repo

import (
	"fmt"
	"os"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestAssistantGenerationReplyRejectsCancelledAndReplacedWriters(t *testing.T) {
	for _, backend := range []string{"sqlite", "mysql"} {
		t.Run(backend, func(t *testing.T) {
			var st *Store
			var err error
			if backend == "mysql" {
				dsn := os.Getenv("AGENTFLOW_TEST_MYSQL_DSN")
				if dsn == "" {
					t.Skip("isolated local Docker MySQL required")
				}
				st, err = Open(dsn)
			} else {
				st, err = OpenTest()
			}
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(st.Close)
			session, err := st.EnsureRunAssistantSession(fmt.Sprintf("reply-%d", time.Now().UnixNano()), "reply-project", "共享", 91)
			if err != nil {
				t.Fatal(err)
			}
			session.Messages = []domain.AssistantMessage{{Role: "user", Content: "原始用户消息"}}
			first := domain.AssistantGeneration{ID: "old-writer", SessionID: session.ID, UserID: session.UserID, ActorID: 92, Status: "running"}
			if err := st.BeginAssistantGeneration(first, session); err != nil {
				t.Fatal(err)
			}
			reply := session
			reply.Messages = append(append([]domain.AssistantMessage(nil), session.Messages...), domain.AssistantMessage{Role: "assistant", Content: "旧回复"})
			if err := st.CancelAssistantGeneration(session.UserID, session.ID, first.ID); err != nil {
				t.Fatal(err)
			}
			if err := st.PutAssistantGenerationReply(session.UserID, first.ID, reply); err == nil {
				t.Fatal("cancelled writer saved a reply")
			}
			first.Status = "interrupted"
			if err := st.CheckpointAssistantGeneration(first); err != nil {
				t.Fatal(err)
			}
			next := session
			next.Messages = append(append([]domain.AssistantMessage(nil), session.Messages...), domain.AssistantMessage{Role: "user", Content: "新发言"})
			second := domain.AssistantGeneration{ID: "new-writer", SessionID: session.ID, UserID: session.UserID, ActorID: 93, Status: "running"}
			if err := st.BeginAssistantGeneration(second, next); err != nil {
				t.Fatal(err)
			}
			if err := st.PutAssistantGenerationReply(session.UserID, first.ID, reply); err == nil {
				t.Fatal("replaced writer overwrote a pending turn")
			}
			current, _ := st.AssistantSession(session.UserID, session.ID)
			if len(current.Messages) != 2 || current.Messages[1].Content != "新发言" {
				t.Fatal("new pending turn was lost")
			}
			reply.Messages = append(append([]domain.AssistantMessage(nil), next.Messages...), domain.AssistantMessage{Role: "assistant", Content: "新回复"})
			if err := st.PutAssistantGenerationReply(session.UserID, second.ID, reply); err != nil {
				t.Fatal(err)
			}
			current, _ = st.AssistantSession(session.UserID, session.ID)
			if len(current.Messages) != 3 || current.Messages[2].Content != "新回复" {
				t.Fatal("current writer did not save its reply")
			}
		})
	}
}
