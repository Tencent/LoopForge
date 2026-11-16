//go:build !windows

package gitwork

import (
	"net"
	"os"
	"path/filepath"
)

func sshClientCommand(string) (string, error) { return "ssh", nil }

func listenSSHAgent(dir string) (net.Listener, error) {
	path := filepath.Join(dir, "agent")
	if len(path) < 100 {
		return net.Listen("unix", path)
	}
	// Unix socket addresses have a small fixed length. Bind through a short
	// alias; the socket itself remains in the workspace's private directory.
	alias, err := os.MkdirTemp("/tmp", "lf-ssh-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(alias)
	if err := os.Symlink(dir, filepath.Join(alias, "d")); err != nil {
		return nil, err
	}
	return net.Listen("unix", filepath.Join(alias, "d", "agent"))
}
