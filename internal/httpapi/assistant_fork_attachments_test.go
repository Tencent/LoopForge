package httpapi

import (
	"testing"

	"agentflow/internal/domain"
)

func TestClaimEditedAttachments(t *testing.T) {
	t.Setenv("AGENTFLOW_ASSISTANT_SESSION_DIR", t.TempDir())
	const (
		author = int64(82001)
		editor = int64(82002)
	)
	saved, err := saveWorkbenchImage(author, "notes.txt", "text/plain", []byte("hello"), "fork-p")
	if err != nil {
		t.Fatal(err)
	}

	t.Run("empty", func(t *testing.T) {
		got, err := claimEditedAttachments(nil, author, author, editor, "fork-p")
		if err != nil || len(got) != 0 {
			t.Fatalf("got=%+v err=%v", got, err)
		}
	})

	t.Run("own attachments are kept in a private slice", func(t *testing.T) {
		original := []domain.AssistantAttachment{saved}
		got, err := claimEditedAttachments(original, author, editor, author, "fork-p")
		if err != nil || len(got) != 1 || got[0].ID != saved.ID {
			t.Fatalf("got=%+v err=%v", got, err)
		}
		got[0].ID = "mutated"
		if original[0].ID != saved.ID {
			t.Fatal("original attachments changed")
		}
	})

	t.Run("missing author falls back to session owner", func(t *testing.T) {
		got, err := claimEditedAttachments([]domain.AssistantAttachment{saved}, 0, editor, editor, "fork-p")
		if err != nil || len(got) != 1 || got[0].ID != saved.ID {
			t.Fatalf("got=%+v err=%v", got, err)
		}
	})

	t.Run("foreign attachments are copied to the editor", func(t *testing.T) {
		original := []domain.AssistantAttachment{saved}
		got, err := claimEditedAttachments(original, 0, author, editor, "fork-p")
		if err != nil || len(got) != 1 || got[0].ID == saved.ID || original[0].ID != saved.ID {
			t.Fatalf("got=%+v err=%v", got, err)
		}
		if _, file, err := openWorkbenchAttachment(editor, got[0].ID); err != nil {
			t.Fatalf("copied attachment missing: %v", err)
		} else {
			file.Close()
		}
	})

	t.Run("cross-project attachment is rejected", func(t *testing.T) {
		if got, err := claimEditedAttachments([]domain.AssistantAttachment{saved}, author, author, editor, "other-p"); err == nil {
			t.Fatalf("cross-project attachment copied: %+v", got)
		}
	})
}
