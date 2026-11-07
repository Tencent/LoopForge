package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestLoadWorkbenchAttachmentRejectsInvalidIDs(t *testing.T) {
	t.Setenv("AGENTFLOW_ASSISTANT_SESSION_DIR", t.TempDir())
	for _, id := range []string{
		"", "../" + strings.Repeat("a", 29), "/" + strings.Repeat("a", 31),
		`..\` + strings.Repeat("a", 29), strings.Repeat("g", 32),
		strings.Repeat("a", 31), strings.Repeat("a", 33),
		"%2f" + strings.Repeat("a", 29),
	} {
		if _, _, err := loadWorkbenchAttachment(1, id); !os.IsNotExist(err) {
			t.Errorf("id=%q error=%v; want not-exist", id, err)
		}
	}
}

func TestWorkbenchAttachmentsRejectSymlinkEscapes(t *testing.T) {
	for _, suffix := range []string{".json", ".bin"} {
		t.Run(suffix, func(t *testing.T) {
			h, user := workbenchFixture(t)
			attachment, err := saveWorkbenchImage(user.ID, "note.txt", "text/plain", []byte("private attachment"), "workbench")
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(workbenchAttachmentDir(user.ID), attachment.ID+suffix)
			outside := filepath.Join(t.TempDir(), "outside"+suffix)
			if err := os.Rename(path, outside); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, path); err != nil {
				t.Fatal(err)
			}

			c, rec := workbenchContext(user, http.MethodGet, "/api/assistant/attachments/"+attachment.ID, bytes.NewBuffer(nil))
			c.Params = gin.Params{{Key: "id", Value: attachment.ID}}
			h.downloadAttachment(c)
			if rec.Code != http.StatusNotFound {
				t.Errorf("download status=%d body=%q; want 404", rec.Code, rec.Body.String())
			}
			session := &AgentToolSession{UserID: user.ID, ProjectID: "workbench", Mode: AgentToolAssistant, AttachmentIDs: map[string]bool{attachment.ID: true}}
			raw, _ := json.Marshal(map[string]string{"id": attachment.ID})
			result, err := readWorkbenchAttachmentTool(session, raw)
			if err == nil && result["isError"] != true {
				t.Errorf("tool read escaping symlink: result=%v", result)
			}
		})
	}
}
