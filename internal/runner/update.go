package runner

import (
	"context"
	"crypto/sha256"
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
		if err := d.checkGateMCPUpdate(ctx, endpoint, path); err != nil {
			return fmt.Errorf("%s/%s: %w", targetOS, runtime.GOARCH, err)
		}
	}
	return nil
}

func (d *Daemon) checkGateMCPUpdate(ctx context.Context, endpoint, path string) error {
	metadata, err := runnerBinaryMetadata(ctx, endpoint)
	if err != nil {
		return err
	}
	localHash, err := hashFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if localHash == metadata.SHA256 {
		return nil
	}
	tmp, err := downloadRunnerBinary(ctx, endpoint, path, metadata.SHA256)
	if err != nil {
		return err
	}
	defer os.Remove(tmp)
	d.updateMu.Lock()
	defer d.updateMu.Unlock()
	if d.updateReady.Load() || atomic.LoadInt32(&d.running) != 0 || ctx.Err() != nil {
		return nil
	}
	current, err := hashFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if current != localHash {
		return errors.New("gate MCP executable changed during update")
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("replace gate MCP executable: %w", err)
	}
	log.Printf("gate MCP update installed (%s) at %s", metadata.SHA256[:12], path)
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

// checkForUpdate only commits while no task is running. The new binary is
// downloaded and verified before acquiring the claim lock, and a failed
// download leaves the previous executable untouched.
func (d *Daemon) checkForUpdate(ctx context.Context, binary string) error {
	if atomic.LoadInt32(&d.running) != 0 {
		return nil
	}
	endpoint := fmt.Sprintf("%s/runners/agentflow-runner?os=%s&arch=%s", d.client.base, runtime.GOOS, runtime.GOARCH)
	metadata, err := runnerBinaryMetadata(ctx, endpoint)
	if err != nil {
		return err
	}
	localHash, err := hashFile(binary)
	if err != nil {
		return err
	}
	if localHash == metadata.SHA256 {
		return nil
	}
	tmp, err := downloadRunnerBinary(ctx, endpoint, binary, metadata.SHA256)
	if err != nil {
		return err
	}
	defer os.Remove(tmp)

	d.updateMu.Lock()
	defer d.updateMu.Unlock()
	if d.updateReady.Load() || atomic.LoadInt32(&d.running) != 0 || ctx.Err() != nil {
		return nil
	}
	// A manual upgrade may have happened while the download was in progress.
	if current, err := hashFile(binary); err != nil {
		return fmt.Errorf("check runner executable before update: %w", err)
	} else if current != localHash {
		return errors.New("runner executable changed during update")
	}
	if err := os.Rename(tmp, binary); err != nil {
		return fmt.Errorf("replace runner executable: %w", err)
	}
	d.updateReady.Store(true)
	log.Printf("runner update installed (%s); restarting", metadata.SHA256[:12])
	return nil
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
