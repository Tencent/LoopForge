package runtime

import "golang.org/x/sys/unix"

func exchangeOpenSandboxWorkspace(stage, workspace string) error {
	return unix.Renameat2(unix.AT_FDCWD, stage, unix.AT_FDCWD, workspace, unix.RENAME_EXCHANGE)
}
