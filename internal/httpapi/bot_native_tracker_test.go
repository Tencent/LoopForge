package httpapi

import (
	"agentflow/internal/repo"
	"context"
	"errors"
	"strings"
	"testing"
)

func TestBotNativeTrackerMapsBeforeStartAndKeepsEverySession(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	tracker := &botNativeTracker{store: s, in: repo.BotNativeSession{ConfigID: "cfg", ProjectID: "p", UserID: 7, ConversationID: "c", BotSessionID: "bot-1", InteractionID: "i1", Engine: "codebuddy", RequestedSessionID: "af1", StorageScope: "assistant-cli-home"}}
	if err := tracker.start(); err != nil {
		t.Fatal(err)
	}
	if mapped, states, _ := s.BotNativeSessions("i1"); len(mapped) != 1 || states[0] != "|started" {
		t.Fatalf("mapping before start = %v %v", mapped, states)
	}
	// First run reports a native ID different from the requested one.
	tracker.finish(context.Background(), "native-a", nil)
	// The retry resumes the same session: same row.
	tracker.start()
	tracker.finish(context.Background(), "native-a", errors.New("exit 1"))
	// A later retry actually created another session: a second row.
	tracker.start()
	tracker.finish(context.Background(), "native-b", nil)
	mapped, states, _ := s.BotNativeSessions("i1")
	if len(mapped) != 2 || states[0] != "native-a|failed" || states[1] != "native-b|completed" || mapped[0].RequestedSessionID != "af1" {
		t.Fatalf("mappings = %v %v", mapped, states)
	}
}

type failingNativeStore struct{ started int }

func (f *failingNativeStore) StartBotNativeSession(repo.BotNativeSession) (string, error) {
	f.started++
	return "", errors.New("database unavailable")
}
func (f *failingNativeStore) FinishBotNativeSession(string, string, string) (string, error) {
	return "", nil
}

func TestBotNativeTrackerFailureStopsTheTurn(t *testing.T) {
	tracker := &botNativeTracker{store: &failingNativeStore{}}
	if err := tracker.start(); err == nil || !strings.Contains(err.Error(), "本轮未启动") {
		t.Fatalf("start = %v", err)
	}
}
