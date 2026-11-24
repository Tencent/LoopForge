package runner

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/attachment"
	"agentflow/internal/domain"
)

func TestRunnerDownloadsRequirementBeforeInvocation(t *testing.T) {
	data := []byte{0, 255, 128, 0, 10}
	id := strings.Repeat("c", 32)
	var unauthorized bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/runners/tasks/task/attachments/"+id || r.Header.Get("Authorization") != "Bearer test-session" || r.Header.Get("X-Task-Attempt") != "2" {
			t.Errorf("download scope incorrect: %s", r.URL.Path)
		}
		if unauthorized {
			w.WriteHeader(409)
			return
		}
		w.Write(data)
	}))
	defer server.Close()
	client := NewClient(server.URL)
	client.SetSession("test-session")
	d := &Daemon{client: client}
	a := domain.IssueAttachment{AssistantAttachment: domain.AssistantAttachment{ID: id, Name: "需求.pdf", Size: int64(len(data))}, UploadedBy: 42}
	task := domain.RunnerTask{ID: "task", Attempt: 2, Invocation: &domain.RunnerInvocation{RequirementAttachments: []domain.IssueAttachment{a}}}
	root := t.TempDir()
	if err := d.prepareRequirementAttachments(context.Background(), task, root); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(attachment.WorkspacePath(a.AssistantAttachment))))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("binary corrupted: %v %v", got, err)
	}
	unauthorized = true
	if err := d.prepareRequirementAttachments(context.Background(), task, t.TempDir()); err != ErrTaskLeaseLost {
		t.Fatalf("lost lease: %v", err)
	}
}
