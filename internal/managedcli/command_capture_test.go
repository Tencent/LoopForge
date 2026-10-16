//go:build linux || darwin

package managedcli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCommandCaptureHelperProcess(t *testing.T) {
	mode := os.Getenv("AGENTFLOW_TEST_CAPTURE_HELPER")
	if mode == "" {
		return
	}
	info, err := os.Stdout.Stat()
	if err != nil || !info.Mode().IsRegular() {
		os.Exit(3)
	}
	if mode == "group-child" {
		root, err := os.Getwd()
		must(t, err)
		file, err := lock(context.Background(), root, "updater.lock", true)
		must(t, err)
		defer file.Close()
		must(t, os.WriteFile("child-ready", nil, 0600))
		time.Sleep(30 * time.Second)
		os.Exit(0)
	}
	if mode == "group" {
		child := exec.Command(os.Args[0], "-test.run=^TestCommandCaptureHelperProcess$")
		child.Env = []string{"AGENTFLOW_TEST_CAPTURE_HELPER=group-child"}
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		must(t, child.Run())
		os.Exit(0)
	}
	size := 32 * 1024
	if mode == "oversize" {
		size = 2 * commandOutputLimit
	}
	_, _ = os.Stdout.Write(bytes.Repeat([]byte("x"), size))
	_, _ = os.Stderr.Write([]byte(" --required-tail-flag\n"))
	if mode == "oversize" {
		time.Sleep(30 * time.Second)
	}
	os.Exit(0)
}

func TestRegularFileCaptureAndOversizeCancellation(t *testing.T) {
	for _, mode := range []string{"normal", "oversize"} {
		dir := t.TempDir()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		started := time.Now()
		data, err := executeCommand(ctx, os.Args[0], []string{"-test.run=^TestCommandCaptureHelperProcess$"}, dir, []string{"AGENTFLOW_TEST_CAPTURE_HELPER=" + mode})
		cancel()
		if mode == "normal" {
			must(t, err)
			if len(data) <= 8192 || !bytes.HasSuffix(data, []byte(" --required-tail-flag\n")) {
				t.Fatalf("truncated help: %d bytes", len(data))
			}
		} else if err == nil || !strings.Contains(err.Error(), "exceeds 1 MiB") || time.Since(started) > 2*time.Second {
			t.Fatalf("oversize command not promptly canceled: %v", err)
		}
		entries, err := os.ReadDir(dir)
		must(t, err)
		if len(entries) != 0 {
			t.Fatal("private output capture retained")
		}
	}
}

func TestCancellationKillsRealDescendant(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := executeCommand(ctx, os.Args[0], []string{"-test.run=^TestCommandCaptureHelperProcess$"}, dir, []string{"AGENTFLOW_TEST_CAPTURE_HELPER=group"})
		done <- err
	}()
	waitUntil(t, func() bool { _, err := os.Stat(filepath.Join(dir, "child-ready")); return err == nil })
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled process succeeded")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("command did not join after cancellation")
	}
	// File capture no longer makes Wait depend on descendants closing stdout.
	// Acquiring the child's flock proves it died too, not just its parent.
	check, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	must(t, WithUpdateLock(check, dir, func() error { return nil }))
}

func TestClaudeHiddenFileArgumentRequiresPositiveAndNegativeControls(t *testing.T) {
	for _, mode := range []string{"supported", "unknown-flag", "ignores-unknown"} {
		t.Run(mode, func(t *testing.T) {
			f := newUpdaterFixture(t)
			positiveCalls := 0
			f.manager.RunCommand = func(ctx context.Context, name string, args []string, dir string, env []string) ([]byte, error) {
				if strings.Contains(name, "/claude-code/") && len(args) > 0 {
					switch args[0] {
					case "--help":
						return []byte(strings.ReplaceAll(strings.Join(providerSpecs["claude-code"].Flags, " "), "--system-prompt-file", "")), nil
					case "--agentflow-invalid-option-probe":
						if mode == "ignores-unknown" {
							return []byte("help"), nil
						}
						return []byte("error: unknown option '--agentflow-invalid-option-probe'\n"), errors.New("exit status 1")
					case "--system-prompt-file":
						positiveCalls++
						if len(args) != 3 || args[2] != "--print" {
							t.Fatal("probe supplied a model prompt")
						}
						if _, err := os.Stat(args[1]); !os.IsNotExist(err) {
							t.Fatal("probe file should not exist")
						}
						if mode == "unknown-flag" {
							return []byte("error: unknown option '--system-prompt-file'"), errors.New("exit status 1")
						}
						return []byte("Error: System prompt file not found: " + args[1] + "\n"), errors.New("exit status 1")
					}
				}
				return f.run(ctx, name, args, dir, env)
			}
			_, err := f.manager.Update(context.Background())
			if mode == "supported" {
				if err != nil || positiveCalls != 1 {
					t.Fatalf("err=%v probes=%d", err, positiveCalls)
				}
			} else if err == nil {
				t.Fatal("unsupported or unverifiable hidden flag accepted")
			}
		})
	}
}
