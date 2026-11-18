package critique

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func hashOf(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func modelServer(t *testing.T, answers []string) (*httptest.Server, *int32) {
	t.Helper()
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q", got)
		}
		if r.URL.Path != "/chat/completions" {
			t.Errorf("path = %q", r.URL.Path)
		}
		call := atomic.AddInt32(&calls, 1)
		answer := answers[len(answers)-1]
		if int(call) <= len(answers) {
			answer = answers[call-1]
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": answer}}},
		})
	}))
	t.Cleanup(server.Close)
	return server, &calls
}

func config(server *httptest.Server) Config {
	return Config{URL: server.URL, Key: "test-key", Model: "critic", Timeout: 5 * time.Second, Client: server.Client()}
}

func TestReviewPassesOnlyOnValidPass(t *testing.T) {
	server, _ := modelServer(t, []string{`{"verdict":"PASS","summary":"looks correct","findings":[]}`})
	patch := "diff --git a/A.java b/A.java\n"
	answer, err := Review(context.Background(), config(server), Request{DiffHash: hashOf(patch), Patch: patch})
	if err != nil {
		t.Fatal(err)
	}
	if answer.Verdict != "PASS" || answer.Summary != "looks correct" || answer.DiffHash != hashOf(patch) {
		t.Fatalf("answer = %+v", answer)
	}
}

func TestReviewRetriesInvalidAnswerThenPasses(t *testing.T) {
	server, calls := modelServer(t, []string{
		"not json at all",
		`{"verdict":"PASS","summary":"second try","findings":[]}`,
	})
	patch := "diff --git a/B.java b/B.java\n"
	answer, err := Review(context.Background(), config(server), Request{DiffHash: hashOf(patch), Patch: patch})
	if err != nil {
		t.Fatal(err)
	}
	if answer.Verdict != "PASS" || *calls != 2 {
		t.Fatalf("answer=%+v calls=%d", answer, *calls)
	}
}

func TestReviewRejectsRejectWithoutFinding(t *testing.T) {
	server, calls := modelServer(t, []string{`{"verdict":"REJECT","summary":"bad","findings":[]}`})
	patch := "diff --git a/C.java b/C.java\n"
	answer, err := Review(context.Background(), config(server), Request{DiffHash: hashOf(patch), Patch: patch})
	if err == nil || answer.Verdict != "REJECT" {
		t.Fatalf("answer=%+v err=%v", answer, err)
	}
	if !strings.Contains(answer.Summary, "invalid") || *calls != maxAttempts {
		t.Fatalf("answer=%+v calls=%d", answer, *calls)
	}
}

func TestReviewRejectsWithoutModelCredentials(t *testing.T) {
	patch := "diff --git a/D.java b/D.java\n"
	answer, err := Review(context.Background(), Config{}, Request{DiffHash: hashOf(patch), Patch: patch})
	if err == nil || answer.Verdict != "REJECT" || !strings.Contains(answer.Summary, "not configured") {
		t.Fatalf("answer=%+v err=%v", answer, err)
	}
}

func TestReviewRejectsDiffHashMismatch(t *testing.T) {
	server, _ := modelServer(t, []string{`{"verdict":"PASS","summary":"ok","findings":[]}`})
	answer, err := Review(context.Background(), config(server), Request{DiffHash: strings.Repeat("a", 64), Patch: "diff"})
	if err == nil || answer.Verdict != "REJECT" || !strings.Contains(answer.Summary, "does not match") {
		t.Fatalf("answer=%+v err=%v", answer, err)
	}
}
