package runner

import (
	"context"
	"crypto/sha256"
	"debug/elf"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	flowruntime "agentflow/internal/runtime"
)

// ErrUpdateReady tells the entry point to exec the newly installed binary.
var ErrUpdateReady = errors.New("runner update ready")

const maxRunnerBinarySize = 512 << 20

func (d *Daemon) checkUpdatesBeforeClaim(ctx context.Context) {
	if err := d.checkGateMCPUpdates(ctx); err != nil && ctx.Err() == nil {
		log.Printf("gate MCP update: %v", err)
	}
	binary, err := currentRunnerExecutable()
	if err != nil {
		log.Printf("runner update: locate executable: %v", err)
		return
	}
	if err := d.checkForUpdate(ctx, binary); err != nil && ctx.Err() == nil {
		log.Printf("runner update: %v", err)
	}
}

// The gate helper is a separate executable in the installed runner bundle.
// A macOS runner also needs the Linux helper for Docker sandboxes.
func (d *Daemon) checkGateMCPUpdates(ctx context.Context) error {
	pattern := strings.TrimSpace(os.Getenv("AGENTFLOW_GATE_MCP_BINARY"))
	if pattern == "" || atomic.LoadInt32(&d.running) != 0 {
		return nil
	}
	targetOSes := []string{runtime.GOOS}
	if runtime.GOOS == "darwin" && strings.Contains(pattern, "{os}") {
		targetOSes = append(targetOSes, "linux")
	}
	for _, targetOS := range targetOSes {
		path := strings.ReplaceAll(strings.ReplaceAll(pattern, "{os}", targetOS), "{arch}", runtime.GOARCH)
		endpoint := fmt.Sprintf("%s/runners/agentflow-gate-mcp?os=%s&arch=%s", d.client.base, targetOS, runtime.GOARCH)
		if err := d.checkCompanionBinaryUpdate(ctx, "gate MCP", endpoint, path, nil, true); err != nil {
			return fmt.Errorf("%s/%s: %w", targetOS, runtime.GOARCH, err)
		}
	}
	return nil
}

// Until the first successful sync, a rescan must retry the version check even
// when an older helper is usable. Downloads use the normal updater's timeout.
func (d *Daemon) refreshDockerRunnerHelper(ctx context.Context, repairOnly bool) bool {
	if err := d.checkDockerRunnerHelperUpdate(ctx, repairOnly); err != nil {
		if ctx.Err() == nil {
			log.Printf("Docker Git/MCP helper update: %v", err)
		}
		return false
	}
	return true
}

func (d *Daemon) checkDockerRunnerHelperUpdate(ctx context.Context, repairOnly bool) error {
	path, err := flowruntime.DockerRunnerHelperPath()
	if err != nil {
		return err
	}
	validateInstalled := func(path string) error {
		return validateDockerRunnerHelper(path, false)
	}
	if repairOnly && validateInstalled(path) == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create Docker Git/MCP helper directory: %w", err)
	}
	// Sandboxes copy this executable into the workspace, so an atomic replacement
	// can run alongside active tasks and requests waiting for the first download.
	return d.checkCompanionBinaryUpdate(ctx, "Docker Git/MCP helper", d.runnerUpdateEndpoint("linux"), path, validateInstalled, false)
}

func (d *Daemon) withDockerRunnerHelperWait(ctx context.Context) context.Context {
	if d.dockerHelperSyncDone == nil {
		return ctx
	}
	return flowruntime.WithDockerRunnerHelperWait(ctx, d.waitForDockerRunnerHelper)
}

func (d *Daemon) waitForDockerRunnerHelper(ctx context.Context, path string) error {
	select {
	case <-d.dockerHelperSyncDone:
		return nil
	default:
	}
	// A workspace copy gets its own executable permissions.
	if validateDockerRunnerHelper(path, true) == nil {
		return nil
	}
	// A workspace request can give up while the download continues. It never
	// controls the downloader's deadline.
	waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	select {
	case <-d.dockerHelperSyncDone:
		return nil
	case <-waitCtx.Done():
		return fmt.Errorf("Docker Git/MCP helper is still syncing: %w", waitCtx.Err())
	}
}

func validateDockerRunnerHelper(path string, forWorkspace bool) error {
	var machine elf.Machine
	switch runtime.GOARCH {
	case "arm64":
		machine = elf.EM_AARCH64
	case "amd64":
		machine = elf.EM_X86_64
	default:
		return fmt.Errorf("unsupported Linux helper architecture %s", runtime.GOARCH)
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("expected regular Linux/%s helper file", runtime.GOARCH)
	}
	if !forWorkspace && info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("expected executable Linux/%s helper", runtime.GOARCH)
	}
	source, err := os.Open(path)
	if err != nil {
		return err
	}
	defer source.Close()
	if forWorkspace {
		// Match the installer for local custom wrappers. Platform artifacts must be ELF.
		var header [2]byte
		if _, err := source.ReadAt(header[:], 0); err == nil && header == [2]byte{'#', '!'} {
			return nil
		}
	}
	file, err := elf.NewFile(source)
	if err != nil {
		return fmt.Errorf("expected Linux/%s ELF helper: %w", runtime.GOARCH, err)
	}
	if file.Class != elf.ELFCLASS64 || file.Data != elf.ELFDATA2LSB || file.Machine != machine ||
		(file.OSABI != elf.ELFOSABI_NONE && file.OSABI != elf.ELFOSABI_LINUX) ||
		(file.Type != elf.ET_EXEC && file.Type != elf.ET_DYN) {
		return fmt.Errorf("expected Linux/%s executable, got ELF %s %s %s %s %s", runtime.GOARCH,
			file.Class, file.Data, file.Machine, file.OSABI, file.Type)
	}
	size := uint64(info.Size())
	hasExecutableSegment := false
	for _, segment := range file.Progs {
		if segment.Type != elf.PT_NULL && segment.Filesz > 0 &&
			(segment.Off > size || segment.Filesz > size-segment.Off) {
			return fmt.Errorf("incomplete Linux/%s ELF helper segment", runtime.GOARCH)
		}
		if segment.Type == elf.PT_LOAD {
			if segment.Filesz > segment.Memsz {
				return fmt.Errorf("invalid Linux/%s ELF helper load segment", runtime.GOARCH)
			}
			hasExecutableSegment = hasExecutableSegment || segment.Filesz > 0 && segment.Flags&elf.PF_X != 0
		}
	}
	if !hasExecutableSegment {
		return fmt.Errorf("Linux/%s ELF helper has no executable load segment", runtime.GOARCH)
	}
	return nil
}

func (d *Daemon) checkCompanionBinaryUpdate(ctx context.Context, name, endpoint, path string, validate func(string) error, requireIdle bool) error {
	metadata, err := runnerBinaryMetadata(ctx, endpoint)
	if err != nil {
		return err
	}
	localHash, err := hashFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if strings.EqualFold(localHash, metadata.SHA256) && (validate == nil || validate(path) == nil) {
		return nil
	}
	tmp, err := downloadRunnerBinary(ctx, endpoint, path, metadata.SHA256)
	if err != nil {
		return err
	}
	defer os.Remove(tmp)
	if validate != nil {
		if err := validate(tmp); err != nil {
			return fmt.Errorf("validate %s: %w", name, err)
		}
	}
	if requireIdle {
		if !d.updateMu.TryLock() {
			return nil
		}
		defer d.updateMu.Unlock()
		if d.runnerExecutionActive() {
			return nil
		}
	}
	if d.updateReady.Load() || ctx.Err() != nil {
		return nil
	}
	current, err := hashFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if current != localHash {
		return fmt.Errorf("%s executable changed during update", name)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("replace %s executable: %w", name, err)
	}
	log.Printf("%s update installed (%s) at %s", name, metadata.SHA256[:12], path)
	return nil
}

func currentRunnerExecutable() (string, error) {
	binary, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(binary); err == nil {
		return resolved, nil
	}
	return binary, nil
}

var errRunnerUpdateBusy = errors.New("runner is busy")

func (d *Daemon) runnerUpdateEndpoint(targetOS string) string {
	return fmt.Sprintf("%s/runners/agentflow-runner?os=%s&arch=%s", d.client.base, targetOS, runtime.GOARCH)
}

func (d *Daemon) runnerUpdateHashes(ctx context.Context, binary string) (local, expected string, err error) {
	local, err = hashFile(binary)
	if err != nil {
		return "", "", err
	}
	remote, err := runnerBinaryMetadata(ctx, d.runnerUpdateEndpoint(runtime.GOOS))
	return local, remote.SHA256, err
}

func (d *Daemon) checkForUpdate(ctx context.Context, binary string) error {
	_, err := d.installRunnerUpdate(ctx, binary)
	if errors.Is(err, errRunnerUpdateBusy) {
		return nil
	}
	return err
}

func (d *Daemon) runnerWorkActive() bool {
	d.maintenance.mu.Lock()
	updatingCLI := d.maintenance.active != "" && d.maintenance.active != runnerMaintenanceKey
	d.maintenance.mu.Unlock()
	return updatingCLI || d.runnerExecutionActive()
}

func (d *Daemon) runnerExecutionActive() bool {
	return atomic.LoadInt32(&d.running) != 0 || d.conversationProcesses.Active() ||
		d.workspaceTerminals.Active() || d.checkpointTransfers.Active()
}

// Download and verify outside the claim fence. Idle activity is checked
// again before replacing the binary, with new workspace requests excluded.
func (d *Daemon) installRunnerUpdate(ctx context.Context, binary string) (bool, error) {
	if d.runnerWorkActive() || !d.updateDownloadMu.TryLock() {
		return false, errRunnerUpdateBusy
	}
	defer d.updateDownloadMu.Unlock()
	if d.updateReady.Load() {
		return true, nil
	}
	localHash, expectedHash, err := d.runnerUpdateHashes(ctx, binary)
	if err != nil {
		return false, err
	}
	if strings.EqualFold(localHash, expectedHash) {
		return false, nil
	}
	tmp, err := downloadRunnerBinary(ctx, d.runnerUpdateEndpoint(runtime.GOOS), binary, expectedHash)
	if err != nil {
		return false, err
	}
	defer os.Remove(tmp)
	if !d.updateMu.TryLock() {
		return false, errRunnerUpdateBusy
	}
	defer d.updateMu.Unlock()
	if d.runnerWorkActive() {
		return false, errRunnerUpdateBusy
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	// A manual upgrade may have happened while the download was in progress.
	if current, err := hashFile(binary); err != nil {
		return false, fmt.Errorf("check runner executable before update: %w", err)
	} else if current != localHash {
		return false, errors.New("runner executable changed during update")
	}
	if err := os.Rename(tmp, binary); err != nil {
		return false, fmt.Errorf("replace runner executable: %w", err)
	}
	d.updateReady.Store(true)
	log.Printf("runner update installed (%s); restarting", expectedHash[:12])
	return true, nil
}

type binaryMetadata struct {
	SHA256 string `json:"sha256"`
}

func runnerBinaryMetadata(ctx context.Context, endpoint string) (binaryMetadata, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"&metadata=1", nil)
	if err != nil {
		return binaryMetadata{}, err
	}
	client := &http.Client{Timeout: 2 * time.Minute}
	resp, err := client.Do(request)
	if err != nil {
		return binaryMetadata{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return binaryMetadata{}, fmt.Errorf("runner metadata: HTTP %d", resp.StatusCode)
	}
	var metadata binaryMetadata
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&metadata); err != nil {
		return binaryMetadata{}, fmt.Errorf("decode runner metadata: %w", err)
	}
	if len(metadata.SHA256) != 64 {
		return binaryMetadata{}, errors.New("invalid runner checksum")
	}
	if _, err := hex.DecodeString(metadata.SHA256); err != nil {
		return binaryMetadata{}, fmt.Errorf("invalid runner checksum: %w", err)
	}
	return metadata, nil
}

func downloadRunnerBinary(ctx context.Context, endpoint, binary, expectedHash string) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Do(request)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download runner: HTTP %d", resp.StatusCode)
	}
	tmp, err := os.CreateTemp(filepath.Dir(binary), ".agentflow-runner-update-*")
	if err != nil {
		return "", err
	}
	defer tmp.Close()
	remove := true
	defer func() {
		if remove {
			os.Remove(tmp.Name())
		}
	}()
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, hash), io.LimitReader(resp.Body, maxRunnerBinarySize+1))
	if err != nil {
		return "", err
	}
	if n == 0 || n > maxRunnerBinarySize {
		return "", errors.New("runner binary is empty or too large")
	}
	if got := hex.EncodeToString(hash.Sum(nil)); !strings.EqualFold(got, expectedHash) {
		return "", fmt.Errorf("runner checksum mismatch: expected %s, got %s", expectedHash, got)
	}
	if err := tmp.Chmod(0o755); err != nil {
		return "", err
	}
	if err := tmp.Sync(); err != nil {
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	remove = false
	return tmp.Name(), nil
}

func hashFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
