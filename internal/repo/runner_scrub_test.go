package repo

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
)

// The runner task payload carries provider credentials only while the task is
// actionable. Every terminal-state transition must scrub them from durable
// storage — including repositories[*].gitToken, which lives inside a JSON
// array and is scrubbed by round-tripping the typed payload (the JSON side
// cannot express an array wildcard).
func TestRunnerTaskPayloadScrubbedOnTerminalTransitions(t *testing.T) {
	st, err := OpenTest()
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	t.Cleanup(st.Close)

	task := domain.RunnerTask{
		ID:        "task-scrub",
		RunnerID:  "runner-1",
		Provider:  "codebuddy",
		Prompt:    "run the node",
		Workspace: "/tmp/ws",
		RunID:     "run-1",
		NodeID:    "impl",
		Invocation: &domain.RunnerInvocation{
			RunID:             "run-1",
			CodeBuddyAPIKey:   "cb-secret",
			ClaudeCodeAPIKey:  "cc-secret",
			ClaudeCodeBaseURL: "https://cc.example.com",
			TaihuPAT:          "taihu-secret",
			Repository:        &domain.RunnerRepository{CloneURL: "git@example.com/a.git", GitToken: "legacy-token"},
			Repositories: []domain.RunnerRepository{
				{CloneURL: "git@example.com/a.git", GitToken: "token-a", Dir: "a"},
				{CloneURL: "git@example.com/b.git", GitToken: "token-b", Dir: "b"},
			},
		},
	}

	// Queued tasks are canceled outright: the payload is scrubbed and the
	// repository entries (clone URL, dir) survive.
	queued := task
	if err := st.PutRunnerTask(queued); err != nil {
		t.Fatalf("put queued task: %v", err)
	}
	now := time.Now().UTC()
	if err := st.CancelRunnerTask("task-scrub", now); err != nil {
		t.Fatalf("cancel queued task: %v", err)
	}
	assertPayloadScrubbed(t, st, "task-scrub")

	// A terminal failure via lease expiry scrubs too.
	expired := task
	expired.ID = "task-expired"
	expired.Status = domain.RunnerTaskLeased
	expired.LeaseOwner = "runner-1"
	expired.Attempt = 1
	expired.MaxAttempts = 1
	expired.LeaseUntil = &[]time.Time{now.Add(-time.Minute)}[0]
	if err := st.PutRunnerTask(expired); err != nil {
		t.Fatalf("put expired task: %v", err)
	}
	recovered, err := st.ExpireRunnerTasks(now)
	if err != nil {
		t.Fatalf("expire tasks: %v", err)
	}
	if len(recovered) != 1 || recovered[0].Status != domain.RunnerTaskFailed {
		t.Fatalf("expired task must fail terminally: %+v", recovered)
	}
	assertPayloadScrubbed(t, st, "task-expired")

	// Requeued tasks keep their payload: the next runner still needs the
	// credentials.
	requeued := task
	requeued.ID = "task-requeued"
	requeued.Status = domain.RunnerTaskLeased
	requeued.LeaseOwner = "runner-1"
	requeued.Attempt = 0
	requeued.MaxAttempts = 1
	requeued.LeaseUntil = &[]time.Time{now.Add(-time.Minute)}[0]
	if err := st.PutRunnerTask(requeued); err != nil {
		t.Fatalf("put requeued task: %v", err)
	}
	recovered, err = st.ExpireRunnerTasks(now)
	if err != nil {
		t.Fatalf("expire tasks: %v", err)
	}
	if len(recovered) != 1 || recovered[0].Status != domain.RunnerTaskQueued {
		t.Fatalf("task with attempts left must be requeued: %+v", recovered)
	}
	payload := readRawPayload(t, st, "task-requeued")
	if !strings.Contains(string(payload), "token-a") || !strings.Contains(string(payload), "token-b") {
		t.Fatalf("requeued task must keep checkout credentials: %s", payload)
	}

	// Completion by the lease owner scrubs before persisting.
	completed := task
	completed.ID = "task-completed"
	completed.Status = domain.RunnerTaskRunning
	completed.LeaseOwner = "runner-1"
	if err := st.PutRunnerTask(completed); err != nil {
		t.Fatalf("put completed task: %v", err)
	}
	if err := st.CompleteRunnerTask("task-completed", "runner-1", domain.RunnerTaskSuccess, nil, "ok", "", nil, nil, now); err != nil {
		t.Fatalf("complete task: %v", err)
	}
	assertPayloadScrubbed(t, st, "task-completed")
}

// assertPayloadScrubbed reads the durable payload directly from the database
// (bypassing the typed row decode) and checks every credential is gone while
// the non-credential fields survive the round trip.
func assertPayloadScrubbed(t *testing.T, st *Store, id string) {
	t.Helper()
	raw := readRawPayload(t, st, id)
	for _, secret := range []string{"cb-secret", "cc-secret", "taihu-secret", "legacy-token", "token-a", "token-b"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("credential %q leaked into durable payload of %s: %s", secret, id, raw)
		}
	}
	var payload runnerTaskPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("scrubbed payload of %s must stay valid JSON: %v", id, err)
	}
	if payload.Invocation == nil {
		t.Fatalf("invocation must be preserved in %s", id)
	}
	if len(payload.Invocation.Repositories) != 2 ||
		payload.Invocation.Repositories[0].CloneURL != "git@example.com/a.git" ||
		payload.Invocation.Repositories[0].Dir != "a" {
		t.Fatalf("repository entries must survive scrubbing: %+v", payload.Invocation.Repositories)
	}
	if payload.Prompt != "run the node" || payload.RunID != "run-1" || payload.Workspace != "/tmp/ws" {
		t.Fatalf("non-credential payload fields must be preserved in %s: %+v", id, payload)
	}
}

func readRawPayload(t *testing.T, st *Store, id string) []byte {
	t.Helper()
	var raw []byte
	// Row().Scan goes through database/sql, which handles []byte blobs
	// directly (a gorm Scan would try to parse the blob into a uint8).
	if err := st.DB.Raw(`SELECT payload FROM runner_tasks WHERE id = ?`, id).Row().Scan(&raw); err != nil {
		t.Fatalf("read payload of %s: %v", id, err)
	}
	if len(raw) == 0 {
		t.Fatalf("payload of %s is empty", id)
	}
	return raw
}
