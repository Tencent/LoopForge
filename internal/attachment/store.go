// Package attachment reads the existing project-scoped upload store. Upload
// authorization stays in httpapi; workers use only canonical issue references.
package attachment

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"agentflow/internal/domain"
)

const Limit = 10 << 20

type Record struct {
	domain.AssistantAttachment
	ProjectID string `json:"projectId"`
}

func BaseDir() string {
	if dir := strings.TrimSpace(os.Getenv("AGENTFLOW_ASSISTANT_SESSION_DIR")); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		home = os.TempDir()
	}
	return filepath.Join(home, ".agentflow", "assistant")
}

func OwnerDir(owner int64) string {
	return filepath.Join(BaseDir(), strconv.FormatInt(owner, 10), "attachments")
}

func ValidID(id string) bool {
	if len(id) != 32 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

func Open(owner int64, id string) (Record, *os.File, error) {
	var record Record
	if owner <= 0 || !ValidID(id) {
		return record, nil, os.ErrNotExist
	}
	root, err := os.OpenRoot(OwnerDir(owner))
	if err != nil {
		return record, nil, err
	}
	defer root.Close()
	raw, err := root.ReadFile(id + ".json")
	if err != nil {
		return record, nil, err
	}
	if err = json.Unmarshal(raw, &record); err != nil {
		return record, nil, err
	}
	file, err := root.Open(id + ".bin")
	return record, file, err
}

func Read(a domain.IssueAttachment, project string) ([]byte, error) {
	record, file, err := Open(a.UploadedBy, a.ID)
	if err != nil {
		return nil, fmt.Errorf("需求附件 %q 不可用", a.Name)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || record.ProjectID != project || record.ID != a.ID || record.Name != a.Name || info.Size() != a.Size || a.Size <= 0 || a.Size > Limit {
		return nil, fmt.Errorf("需求附件 %q 的归属或大小无效", a.Name)
	}
	data, err := io.ReadAll(io.LimitReader(file, Limit+1))
	if err != nil || int64(len(data)) != a.Size {
		return nil, fmt.Errorf("需求附件 %q 读取失败", a.Name)
	}
	return data, nil
}

func WorkspacePath(a domain.AssistantAttachment) string {
	name := path.Base(strings.ReplaceAll(a.Name, "\\", "/"))
	if name == "." || name == ".." || name == "/" || name == "" {
		name = "attachment.bin"
	}
	return path.Join(".agentflow", "inputs", "requirement-attachments", a.ID, name)
}
