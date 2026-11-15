package httpapi

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestAssistantNativeFileLockExcludesHandlesAndReleases(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.lock")
	open := func() *os.File {
		t.Helper()
		file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = file.Close() })
		return file
	}
	first, second := open(), open()
	if err := lockAssistantNativeFile(first); err != nil {
		t.Fatal(err)
	}
	if err := lockAssistantNativeFile(second); !errors.Is(err, errAssistantNativeBusy) {
		t.Fatalf("second handle must report busy without waiting: %v", err)
	}
	assertAssistantNativeLockChild(t, path, "busy")
	if err := unlockAssistantNativeFile(first); err != nil {
		t.Fatal(err)
	}
	if err := lockAssistantNativeFile(second); err != nil {
		t.Fatalf("released file could not be locked: %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	assertAssistantNativeLockChild(t, path, "available")
}

func assertAssistantNativeLockChild(t *testing.T, path, expected string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestAssistantNativeFileLockChild$")
	cmd.Env = append(os.Environ(), "AGENTFLOW_TEST_NATIVE_LOCK_PATH="+path, "AGENTFLOW_TEST_NATIVE_LOCK_EXPECTED="+expected)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("separate process lock expectation %s: %v\n%s", expected, err, output)
	}
}

func TestAssistantNativeFileLockChild(t *testing.T) {
	path := os.Getenv("AGENTFLOW_TEST_NATIVE_LOCK_PATH")
	if path == "" {
		t.Skip("subprocess helper")
	}
	file, err := os.OpenFile(path, os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	err = lockAssistantNativeFile(file)
	if os.Getenv("AGENTFLOW_TEST_NATIVE_LOCK_EXPECTED") == "busy" {
		if !errors.Is(err, errAssistantNativeBusy) {
			t.Fatalf("another process must report busy: %v", err)
		}
		return
	}
	if err != nil {
		t.Fatalf("closed owner must release the lock: %v", err)
	}
	if err := unlockAssistantNativeFile(file); err != nil {
		t.Fatal(err)
	}
}
