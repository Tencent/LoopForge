// Package processgroup manages child process trees without shell interpolation.
package processgroup

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var processJobs = struct {
	sync.Mutex
	items map[int]windows.Handle
}{items: map[int]windows.Handle{}}

// configure separates console signals and prevents unattended helper windows.
func configure(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_NEW_PROCESS_GROUP
	cmd.SysProcAttr.HideWindow = true
}

// Start assigns the suspended child to a kill-on-close Job before it can spawn descendants.
func Start(cmd *exec.Cmd) (func(), error) {
	configure(cmd)
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("create process job: %w", err)
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		windows.CloseHandle(job)
		return nil, fmt.Errorf("configure process job: %w", err)
	}
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_SUSPENDED
	if err := cmd.Start(); err != nil {
		windows.CloseHandle(job)
		return nil, err
	}
	pid := cmd.Process.Pid
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.SYNCHRONIZE, false, uint32(pid))
	closeJob := func() {
		processJobs.Lock()
		if processJobs.items[pid] == job {
			delete(processJobs.items, pid)
		}
		windows.CloseHandle(job)
		processJobs.Unlock()
	}
	fail := func(err error) (func(), error) {
		_ = cmd.Process.Kill()
		closeJob()
		if process != 0 {
			windows.CloseHandle(process)
		}
		_ = cmd.Wait()
		return nil, err
	}
	if err != nil {
		return fail(fmt.Errorf("open process for job: %w", err))
	}
	if err := windows.AssignProcessToJobObject(job, process); err != nil {
		return fail(fmt.Errorf("assign process job: %w", err))
	}
	processJobs.Lock()
	processJobs.items[pid] = job
	processJobs.Unlock()
	if err := resumeInitialThread(uint32(pid)); err != nil {
		return fail(fmt.Errorf("resume process job: %w", err))
	}

	// A descendant may inherit the executor's output pipes. Reap the job
	// as soon as the root exits so readers can reach EOF before cmd.Wait.
	rootExited := make(chan struct{})
	go func() {
		defer close(rootExited)
		if status, err := windows.WaitForSingleObject(process, windows.INFINITE); err == nil && status == windows.WAIT_OBJECT_0 {
			_ = Kill(pid)
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			closeJob()
			<-rootExited
			windows.CloseHandle(process)
		})
	}, nil
}

func resumeInitialThread(pid uint32) error {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(snapshot)
	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	for err = windows.Thread32First(snapshot, &entry); err == nil; err = windows.Thread32Next(snapshot, &entry) {
		if entry.OwnerProcessID != pid {
			continue
		}
		thread, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
		if err != nil {
			return err
		}
		_, err = windows.ResumeThread(thread)
		windows.CloseHandle(thread)
		return err
	}
	return fmt.Errorf("initial thread for process %d not found: %w", pid, err)
}

// Terminate closes the entire Windows tree; console control signals are unreliable for GUI-launched runners.
func Terminate(pid int) error { return Kill(pid) }

// Kill terminates an owned job. Stale records cannot target an unrelated PID.
func Kill(pid int) error {
	processJobs.Lock()
	defer processJobs.Unlock()
	job, tracked := processJobs.items[pid]
	if !tracked {
		return os.ErrProcessDone
	}
	return windows.TerminateJobObject(job, 1)
}

// Alive checks the native process handle instead of Unix signal zero.
func Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return errors.Is(err, windows.ERROR_ACCESS_DENIED)
	}
	defer windows.CloseHandle(handle)
	status, err := windows.WaitForSingleObject(handle, 0)
	return err == nil && status == uint32(windows.WAIT_TIMEOUT)
}
