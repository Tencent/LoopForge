package managedcli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

const commandOutputLimit = 1 << 20

var errCommandOutputTooLarge = errors.New("CLI validation output exceeds 1 MiB")

func executeCommand(ctx context.Context, name string, args []string, dir string, env []string) ([]byte, error) {
	// Some native CLIs exit before their async pipe writes have drained. On
	// Linux that truncated real --help output to 8192 bytes and falsely rejected
	// supported flags. A regular file gives the child a synchronous sink.
	output, err := os.CreateTemp(dir, ".managed-cli-output-*")
	if err != nil {
		return nil, err
	}
	outputPath := output.Name() // newly created private file, never an existing path
	defer func() { output.Close(); _ = os.Remove(outputPath) }()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir, cmd.Env = dir, env
	if err := configureCommandGroup(cmd); err != nil {
		return nil, err
	}
	cmd.WaitDelay = 5 * time.Second
	cmd.Stdout, cmd.Stderr = output, output
	stopWatch := watchCaptureSize(output, cancel)
	runErr := cmd.Run()
	stopWatch()
	data, err := readCapturedOutput(output)
	if err != nil {
		return nil, err
	}
	if runErr != nil {
		// Never persist arbitrary npm/script output in an error: it can contain
		// credentials or proxy URLs. The private capture file is removed above.
		return data, fmt.Errorf("command %s failed: %w", filepath.Base(name), runErr)
	}
	return data, nil
}

// watchCaptureSize cancels the command group as soon as the capture file grows
// past commandOutputLimit. The returned stop joins the watcher goroutine.
func watchCaptureSize(output *os.File, cancel context.CancelFunc) (stop func()) {
	done, monitorDone := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(monitorDone)
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				if info, err := output.Stat(); err == nil && info.Size() > commandOutputLimit {
					cancel() // kills the entire command group, not just npm's shell
					return
				}
			}
		}
	}()
	return func() {
		close(done)
		<-monitorDone
	}
}

// readCapturedOutput reads the whole capture file from the start, rejecting it
// once it exceeds commandOutputLimit.
func readCapturedOutput(output *os.File) ([]byte, error) {
	info, err := output.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() > commandOutputLimit {
		return nil, errCommandOutputTooLarge
	}
	if _, err := output.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(output, commandOutputLimit+1))
	if err != nil {
		return nil, err
	}
	if len(data) > commandOutputLimit {
		return nil, errCommandOutputTooLarge
	}
	return data, nil
}
