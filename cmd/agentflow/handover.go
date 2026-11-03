package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// A reload (SIGHUP) hands the API listener to a freshly started binary: the
// successor inherits the socket, reports readiness, and becomes systemd's
// MainPID; the predecessor stops accepting and drains in-process work
// (assistant generations hold CLI subprocesses that cannot move).
const (
	envListenFD = "AGENTFLOW_LISTEN_FD"
	envReadyFD  = "AGENTFLOW_READY_FD"

	// handoverStatus is published as the unit's StatusText; deploy scripts
	// look for "handover" to know the running binary can be reloaded.
	handoverStatus = "serving; reload performs a handover"

	handoverReadyTimeout = 60 * time.Second
)

// inheritedListener returns the listener passed by a predecessor, or nil when
// this process was started normally.
func inheritedListener() (net.Listener, error) {
	raw := strings.TrimSpace(os.Getenv(envListenFD))
	if raw == "" {
		return nil, nil
	}
	os.Unsetenv(envListenFD)
	fd, err := strconv.Atoi(raw)
	if err != nil || fd < 3 {
		return nil, fmt.Errorf("invalid %s %q", envListenFD, raw)
	}
	f := os.NewFile(uintptr(fd), "agentflow-listener")
	defer f.Close()
	ln, err := net.FileListener(f)
	if err != nil {
		return nil, fmt.Errorf("inherit listener fd %d: %w", fd, err)
	}
	return ln, nil
}

// listenMain reuses an inherited listener or binds addr.
func listenMain(addr string) (ln net.Listener, inherited bool, err error) {
	ln, err = inheritedListener()
	if err != nil || ln != nil {
		return ln, ln != nil, err
	}
	if addr == "" {
		addr = ":http"
	}
	ln, err = net.Listen("tcp", addr)
	return ln, false, err
}

// signalReady tells systemd (and a waiting predecessor) that this process
// serves. systemd learns the new MainPID before the predecessor is released,
// so the unit never observes its main process exiting.
func signalReady(status string) {
	if err := sdNotify(fmt.Sprintf("MAINPID=%d\nREADY=1\nSTATUS=%s\n", os.Getpid(), status)); err != nil {
		fmt.Fprintf(os.Stderr, "sd_notify: %v\n", err)
	}
	raw := strings.TrimSpace(os.Getenv(envReadyFD))
	if raw == "" {
		return
	}
	os.Unsetenv(envReadyFD)
	fd, err := strconv.Atoi(raw)
	if err != nil || fd < 3 {
		return
	}
	f := os.NewFile(uintptr(fd), "agentflow-ready")
	_, _ = f.Write([]byte("ready\n"))
	_ = f.Close()
}

// sdNotify sends state to $NOTIFY_SOCKET; it is a no-op outside systemd.
func sdNotify(state string) error {
	socket := os.Getenv("NOTIFY_SOCKET")
	if socket == "" {
		return nil
	}
	if strings.HasPrefix(socket, "@") {
		socket = "\x00" + socket[1:]
	}
	conn, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: socket, Net: "unixgram"})
	if err != nil {
		return err
	}
	defer conn.Close()
	_, err = conn.Write([]byte(state))
	return err
}

// handover starts the binary currently on disk with ln and waits until it is
// ready. On error the successor has been killed and the caller keeps serving.
func handover(ln net.Listener, timeout time.Duration) (*os.Process, error) {
	filer, ok := ln.(interface{ File() (*os.File, error) })
	if !ok {
		return nil, fmt.Errorf("listener %T cannot be inherited", ln)
	}
	lnFile, err := filer.File()
	if err != nil {
		return nil, fmt.Errorf("dup listener: %w", err)
	}
	defer lnFile.Close()
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolve executable: %w", err)
	}
	readyR, readyW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	defer readyR.Close()
	cmd := exec.Command(exe, os.Args[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(withoutHandoverEnv(os.Environ()), envListenFD+"=3", envReadyFD+"=4")
	cmd.ExtraFiles = []*os.File{lnFile, readyW}
	err = cmd.Start()
	readyW.Close()
	if err != nil {
		return nil, fmt.Errorf("start successor %s: %w", exe, err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	ready := make(chan error, 1)
	go func() {
		buf := make([]byte, 16)
		n, err := readyR.Read(buf)
		if n > 0 {
			ready <- nil
			return
		}
		if err == nil {
			err = errors.New("empty readiness signal")
		}
		ready <- err
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err := <-ready:
		if err == nil {
			return cmd.Process, nil
		}
		_ = cmd.Process.Kill()
		return nil, fmt.Errorf("successor pid %d did not report ready: %w", cmd.Process.Pid, err)
	case err := <-exited:
		return nil, fmt.Errorf("successor exited before ready: %v", err)
	case <-timer.C:
		_ = cmd.Process.Kill()
		return nil, fmt.Errorf("successor pid %d not ready within %s", cmd.Process.Pid, timeout)
	}
}

func withoutHandoverEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if strings.HasPrefix(kv, envListenFD+"=") || strings.HasPrefix(kv, envReadyFD+"=") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// predecessorExited closes once the process that handed over to us is gone
// (we get reparented), gating recovery of work it may still be finishing.
func predecessorExited() <-chan struct{} {
	ppid := os.Getppid()
	done := make(chan struct{})
	go func() {
		for os.Getppid() == ppid {
			time.Sleep(time.Second)
		}
		close(done)
	}()
	return done
}
