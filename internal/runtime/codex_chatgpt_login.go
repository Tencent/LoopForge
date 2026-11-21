package runtime

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const codexAuthFile = "auth.json"

// InstallCodexChatGPTLogin writes a ChatGPT login (already stripped of its
// refresh token by the platform) as auth.json of the private Codex home
// .agentflow/private-home/<name>/.codex and returns that directory on the
// host. Every path component is checked: the sandbox can write the workspace.
func InstallCodexChatGPTLogin(workspace, name, auth string) (string, error) {
	if strings.TrimSpace(auth) == "" {
		return "", errors.New("missing Codex ChatGPT login")
	}
	dir, err := EnsureControlDirectory(workspace, "private-home", name, ".codex")
	if err != nil {
		return "", fmt.Errorf("create Codex home: %w", err)
	}
	if err := writeManagedFile(workspace, filepath.Join(dir, codexAuthFile), []byte(auth), 0o600); err != nil {
		return "", fmt.Errorf("write Codex login: %w", err)
	}
	return dir, nil
}

// RemoveCodexChatGPTLogin deletes the auth.json InstallCodexChatGPTLogin
// wrote. A missing file is not an error.
func RemoveCodexChatGPTLogin(workspace, name string) error {
	root, rel, err := openWorkspacePath(workspace, filepath.Join(workspace, ".agentflow", "private-home", name, ".codex", codexAuthFile))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	defer root.Close()
	if err := root.Remove(rel); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
