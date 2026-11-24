package runtime

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"agentflow/internal/attachment"
	"agentflow/internal/domain"
)

// WriteRequirementAttachment installs immutable input bytes outside repository
// checkouts. A scoped directory handle and atomic rename never follow the file
// being replaced, including a link planted by a previous node.
func WriteRequirementAttachment(workspace string, a domain.AssistantAttachment, data []byte) error {
	if !attachment.ValidID(a.ID) || a.Size <= 0 || a.Size > attachment.Limit || int64(len(data)) != a.Size {
		return fmt.Errorf("invalid requirement attachment")
	}
	if _, err := EnsureControlDirectory(workspace, "inputs", "requirement-attachments", a.ID); err != nil {
		return err
	}
	root, err := os.OpenRoot(workspace)
	if err != nil {
		return err
	}
	defer root.Close()
	rel := filepath.FromSlash(attachment.WorkspacePath(a))
	dir, err := root.OpenRoot(filepath.Dir(rel))
	if err != nil {
		return err
	}
	defer dir.Close()
	name := filepath.Base(rel)
	if info, err := dir.Lstat(name); err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("attachment target is not a regular file")
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	token := make([]byte, 16)
	if _, err := rand.Read(token); err != nil {
		return err
	}
	tmp := ".upload-" + hex.EncodeToString(token)
	file, err := dir.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer dir.Remove(tmp)
	_, writeErr := file.Write(data)
	if writeErr == nil {
		writeErr = file.Chmod(0444)
	}
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	return dir.Rename(tmp, name)
}
