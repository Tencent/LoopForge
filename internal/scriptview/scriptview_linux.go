//go:build linux

package scriptview

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

type processIdentity struct {
	uid, gid int
	groups   []int
}

func execute(config Config) error {
	identity, err := readProcessIdentity("/proc/1/status")
	if err != nil {
		return fmt.Errorf("read workspace container identity: %w", err)
	}
	root, err := unix.Open("/proc/1/root", unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("open workspace container root: %w", err)
	}
	defer unix.Close(root)
	namespace, err := unix.Open("/proc/1/ns/mnt", unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("open workspace mount namespace: %w", err)
	}
	defer unix.Close(namespace)
	ipc, err := unix.Open("/proc/1/ns/ipc", unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("open workspace IPC namespace: %w", err)
	}
	defer unix.Close(ipc)
	uts, err := unix.Open("/proc/1/ns/uts", unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("open workspace hostname namespace: %w", err)
	}
	defer unix.Close(uts)

	// Namespace and credential changes apply only to this OS thread. Never
	// return it to the Go thread pool: on success exec replaces the process, and
	// on failure the command entry point terminates this dedicated helper.
	runtime.LockOSThread()
	// Go runtime threads initially share CLONE_FS. Linux refuses setns into
	// a mount namespace until this thread has its own root/cwd state.
	if err := unix.Unshare(unix.CLONE_FS); err != nil {
		return fmt.Errorf("isolate workspace script filesystem state: %w", err)
	}
	if err := unix.Setns(ipc, unix.CLONE_NEWIPC); err != nil {
		return fmt.Errorf("enter workspace IPC namespace: %w", err)
	}
	if err := unix.Setns(uts, unix.CLONE_NEWUTS); err != nil {
		return fmt.Errorf("enter workspace hostname namespace: %w", err)
	}
	if err := unix.Setns(namespace, unix.CLONE_NEWNS); err != nil {
		return fmt.Errorf("enter workspace mount namespace: %w", err)
	}
	if err := unix.Fchdir(root); err != nil {
		return fmt.Errorf("enter workspace container root: %w", err)
	}
	if err := unix.Chroot("."); err != nil {
		return fmt.Errorf("adopt workspace container root: %w", err)
	}
	if err := unix.Chdir("/"); err != nil {
		return fmt.Errorf("enter workspace container root: %w", err)
	}
	if config.Refresh {
		// This narrowly scoped operation may only ADD verified read-only masks
		// to the canonical namespace. It never detaches a mount or runs user
		// commands. Agents still execute in their original hardened container.
		return applyMasks(config.Hide)
	}
	// Adopt the target root before copying its mount namespace: unshare then
	// remaps root/cwd onto the copied mounts. Fchdir to the old root descriptor
	// after unshare would instead point back outside the copied namespace.
	if err := unix.Unshare(unix.CLONE_NEWNS); err != nil {
		return fmt.Errorf("clone workspace mount namespace: %w", err)
	}
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		return fmt.Errorf("make workspace script mounts private: %w", err)
	}
	if err := unix.Chdir("/workspace"); err != nil {
		return fmt.Errorf("enter workspace script directory: %w", err)
	}
	if err := removeMasks(config.Masks); err != nil {
		return err
	}
	if err := dropPrivileges(identity); err != nil {
		return fmt.Errorf("drop workspace script bootstrap privileges: %w", err)
	}
	binary, err := exec.LookPath(config.Argv[0])
	if err != nil {
		return fmt.Errorf("locate workspace script command: %w", err)
	}
	if err := unix.Exec(binary, config.Argv, os.Environ()); err != nil {
		return fmt.Errorf("execute workspace script command: %w", err)
	}
	return nil
}

func removeMasks(targets []string) error {
	if len(targets) == 0 {
		return nil
	}
	root, err := os.OpenRoot("/workspace")
	if err != nil {
		return fmt.Errorf("open script instruction mask root: %w", err)
	}
	defer root.Close()
	sources := make([]unix.Stat_t, 0, 2)
	for _, name := range []string{".agentflow/isolation/empty-file", ".agentflow/isolation/empty-dir"} {
		source, err := root.Open(name)
		if err != nil {
			return fmt.Errorf("open platform instruction mask source: %w", err)
		}
		var stat unix.Stat_t
		var fs unix.Statfs_t
		statErr := unix.Fstat(int(source.Fd()), &stat)
		fsErr := unix.Fstatfs(int(source.Fd()), &fs)
		_ = source.Close()
		if statErr != nil {
			return fmt.Errorf("inspect platform instruction mask source: %w", statErr)
		}
		if fsErr != nil {
			return fmt.Errorf("inspect platform instruction mask source: %w", fsErr)
		}
		if fs.Flags&unix.ST_RDONLY == 0 {
			return fmt.Errorf("platform instruction mask source is not read-only")
		}
		sources = append(sources, stat)
	}
	// /proc/self refers to the Go process leader, which still belongs to the
	// helper's original namespace. Only the locked calling thread entered the
	// private workspace view.
	file, err := os.Open("/proc/thread-self/mountinfo")
	if err != nil {
		return fmt.Errorf("read workspace script mounts: %w", err)
	}
	defer file.Close()
	mounts := make(map[string]bool)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) >= 6 {
			mounts[decodeMountPath(fields[4])] = true
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read workspace script mounts: %w", err)
	}
	for _, target := range targets {
		if !mounts[target] {
			// A package manager can replace the parent directory in a prior
			// script. Docker still lists the detached old dentry as a mask,
			// although it no longer exists at this path in the namespace.
			continue
		}
		file, err := root.Open(strings.TrimPrefix(target, "/workspace/"))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("inspect script instruction mask %s: %w", target, err)
		}
		var stat unix.Stat_t
		var fs unix.Statfs_t
		statErr := unix.Fstat(int(file.Fd()), &stat)
		fsErr := unix.Fstatfs(int(file.Fd()), &fs)
		_ = file.Close()
		if statErr != nil || fsErr != nil {
			return fmt.Errorf("inspect script instruction mask %s: %w", target, errors.Join(statErr, fsErr))
		}
		owned := false
		for _, source := range sources {
			if stat.Dev == source.Dev && stat.Ino == source.Ino && stat.Mode&unix.S_IFMT == source.Mode&unix.S_IFMT && fs.Flags&unix.ST_RDONLY != 0 {
				owned = true
				break
			}
		}
		if !owned {
			// A stale dentry or unrelated user-provided mount is not ours to
			// detach, even when Docker once reported a mask at the same path.
			continue
		}
		// Even in this private namespace, never follow a replacement final
		// symlink and accidentally detach a different mount.
		if err := unix.Unmount(target, unix.MNT_DETACH|unix.UMOUNT_NOFOLLOW); err != nil {
			return fmt.Errorf("detach workspace instruction mask %s: %w", target, err)
		}
	}
	return nil
}

func applyMasks(targets []string) error {
	if len(targets) == 0 {
		return nil
	}
	root, err := os.OpenRoot("/workspace")
	if err != nil {
		return fmt.Errorf("open Agent instruction mask root: %w", err)
	}
	defer root.Close()
	for _, target := range targets {
		if err := applyMask(root, target); err != nil {
			return err
		}
	}
	return nil
}

func applyMask(root *os.Root, target string) error {
	resolved, err := filepath.EvalSymlinks(target)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("resolve Agent instruction mask %s: %w", target, err)
	}
	if !ValidMask(resolved) {
		return fmt.Errorf("Agent instruction mask resolves outside the project configuration set: %s", target)
	}
	if info, err := os.Lstat(target); err != nil {
		return fmt.Errorf("inspect Agent instruction mask %s: %w", target, err)
	} else if info.Mode()&os.ModeSymlink != 0 && instructionDirectory(filepath.Base(target)) {
		return fmt.Errorf("refusing symlinked Agent instruction directory: %s", target)
	}
	relative := strings.TrimPrefix(resolved, "/workspace/")
	destination, err := root.Open(relative)
	if err != nil {
		return fmt.Errorf("open Agent instruction mask %s: %w", target, err)
	}
	defer destination.Close()
	info, err := destination.Stat()
	if err != nil {
		return err
	}
	sourceName := ".agentflow/isolation/empty-file"
	if info.IsDir() {
		sourceName = ".agentflow/isolation/empty-dir"
	} else if !info.Mode().IsRegular() {
		return fmt.Errorf("Agent instruction mask is not a regular file or directory: %s", target)
	}
	source, err := root.Open(sourceName)
	if err != nil {
		return fmt.Errorf("open platform instruction mask source: %w", err)
	}
	defer source.Close()
	var sourceFS unix.Statfs_t
	if err := unix.Fstatfs(int(source.Fd()), &sourceFS); err != nil {
		return fmt.Errorf("inspect platform instruction mask source: %w", err)
	}
	if sourceFS.Flags&unix.ST_RDONLY == 0 {
		return fmt.Errorf("platform instruction mask source is not read-only")
	}
	var sourceStat, targetStat unix.Stat_t
	var targetFS unix.Statfs_t
	if err := unix.Fstat(int(source.Fd()), &sourceStat); err != nil {
		return fmt.Errorf("inspect platform instruction mask source: %w", err)
	}
	if err := unix.Fstat(int(destination.Fd()), &targetStat); err != nil {
		return fmt.Errorf("inspect Agent instruction mask %s: %w", target, err)
	}
	if err := unix.Fstatfs(int(destination.Fd()), &targetFS); err != nil {
		return fmt.Errorf("inspect Agent instruction mask %s: %w", target, err)
	}
	if sourceStat.Dev == targetStat.Dev && sourceStat.Ino == targetStat.Ino && targetFS.Flags&unix.ST_RDONLY != 0 {
		// Refresh is idempotent: repeated Agent invocations must not build an
		// ever-growing stack of identical mounts in the canonical namespace.
		return nil
	}
	// Bind from the existing read-only platform source. Bind mounts inherit its
	// read-only flag; descriptor paths prevent target symlink substitution from
	// redirecting the operation outside the securely opened workspace root.
	sourcePath := fmt.Sprintf("/proc/thread-self/fd/%d", source.Fd())
	targetPath := fmt.Sprintf("/proc/thread-self/fd/%d", destination.Fd())
	if err := unix.Mount(sourcePath, targetPath, "", unix.MS_BIND, ""); err != nil {
		return fmt.Errorf("apply Agent instruction mask %s: %w", target, err)
	}
	masked, err := root.Open(relative)
	if err != nil {
		return fmt.Errorf("verify Agent instruction mask %s: %w", target, err)
	}
	defer masked.Close()
	var maskedFS unix.Statfs_t
	if err := unix.Fstatfs(int(masked.Fd()), &maskedFS); err != nil {
		return fmt.Errorf("verify Agent instruction mask %s: %w", target, err)
	}
	if maskedFS.Flags&unix.ST_RDONLY == 0 {
		return fmt.Errorf("Agent instruction mask did not remain read-only: %s", target)
	}
	return nil
}

func instructionDirectory(name string) bool {
	return name == ".agents" || name == ".codex" || name == ".claude" || name == ".codebuddy"
}

func decodeMountPath(value string) string {
	return strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`).Replace(value)
}

func readProcessIdentity(path string) (processIdentity, error) {
	file, err := os.Open(path)
	if err != nil {
		return processIdentity{}, err
	}
	defer file.Close()
	var identity processIdentity
	var foundUID, foundGID, foundGroups bool
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 0 {
			continue
		}
		switch fields[0] {
		case "Uid:", "Gid:":
			if len(fields) < 3 {
				return identity, fmt.Errorf("invalid container identity field %s", fields[0])
			}
			value, err := strconv.Atoi(fields[2])
			if err != nil || value < 0 {
				return identity, fmt.Errorf("invalid container identity field %s", fields[0])
			}
			if fields[0] == "Uid:" {
				identity.uid, foundUID = value, true
			} else {
				identity.gid, foundGID = value, true
			}
		case "Groups:":
			foundGroups = true
			for _, raw := range fields[1:] {
				group, err := strconv.Atoi(raw)
				if err != nil || group < 0 {
					return identity, fmt.Errorf("invalid container supplementary group")
				}
				identity.groups = append(identity.groups, group)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return identity, err
	}
	if !foundUID || !foundGID || !foundGroups {
		return identity, fmt.Errorf("container process identity is incomplete")
	}
	return identity, nil
}

func dropPrivileges(identity processIdentity) error {
	if err := unix.Prctl(unix.PR_CAP_AMBIENT, unix.PR_CAP_AMBIENT_CLEAR_ALL, 0, 0, 0); err != nil && !errors.Is(err, unix.EINVAL) {
		return err
	}
	// Invalid capability numbers return EINVAL. Discover the kernel's final
	// supported capability without depending on procfs settings in the image.
	for capability := 0; capability < 256; capability++ {
		if err := unix.Prctl(unix.PR_CAPBSET_DROP, uintptr(capability), 0, 0, 0); err != nil {
			if errors.Is(err, unix.EINVAL) {
				break
			}
			return fmt.Errorf("drop capability %d: %w", capability, err)
		}
	}
	if err := unix.Setgroups(identity.groups); err != nil {
		return fmt.Errorf("restore supplementary groups: %w", err)
	}
	if err := unix.Setresgid(identity.gid, identity.gid, identity.gid); err != nil {
		return fmt.Errorf("restore container GID: %w", err)
	}
	if err := unix.Setresuid(identity.uid, identity.uid, identity.uid); err != nil {
		return fmt.Errorf("restore container UID: %w", err)
	}
	header := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	data := [2]unix.CapUserData{}
	if err := unix.Capset(&header, &data[0]); err != nil {
		return fmt.Errorf("clear process capabilities: %w", err)
	}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("disable privilege escalation: %w", err)
	}
	return nil
}
