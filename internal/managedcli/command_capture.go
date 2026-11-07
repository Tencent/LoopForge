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
	stop, monitorDone := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(monitorDone)
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				if info, err := output.Stat(); err == nil && info.Size() > commandOutputLimit {
					cancel() // kills the entire command group, not just npm's shell
					return
				}
			}
		}
	}()
	runErr := cmd.Run()
	close(stop)
	<-monitorDone
	info, err := output.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() > commandOutputLimit {
		return nil, errors.New("CLI validation output exceeds 1 MiB")
	}
	if _, err := output.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(output, commandOutputLimit+1))
	if err != nil {
		return nil, err
	}
	if len(data) > commandOutputLimit {
		return nil, errors.New("CLI validation output exceeds 1 MiB")
	}
	if runErr != nil {
		// Never persist arbitrary npm/script output in an error: it can contain
		// credentials or proxy URLs. The private capture file is removed above.
		return data, fmt.Errorf("command %s failed: %w", filepath.Base(name), runErr)
	}
	return data, nil
}
