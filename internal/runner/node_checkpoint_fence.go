package runner

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

// The marker lives outside the checkout so agent commands and Git snapshots do
// not include it. It outlives a crashed daemon; a new daemon must not assume
// that processes started by its predecessor have stopped.
func (d *Daemon) nodeCheckpointFencePath(key string) string {
	digest := sha256.Sum256([]byte(key))
	return filepath.Join(d.cfg.DefaultWorkspaceRoot, ".loopforge-checkpoint-fences", hex.EncodeToString(digest[:]))
}

func (d *Daemon) orphanNodeCheckpointFence(key string) error {
	if _, ok := d.nodeCheckpointReservations.Load(key); ok {
		return nil
	}
	_, err := os.Lstat(d.nodeCheckpointFencePath(key))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("check checkpoint workspace fence: %w", err)
	}
	return fmt.Errorf("a previous checkpoint operation did not finish; verify that all old processes have stopped before recovering the workspace")
}

func (d *Daemon) reserveNodeCheckpointFence(key, token string) error {
	if token == "" {
		return fmt.Errorf("checkpoint workspace lease is required")
	}
	if current, ok := d.nodeCheckpointReservations.Load(key); ok && current == token {
		if _, err := os.Stat(d.nodeCheckpointFencePath(key)); err != nil {
			return fmt.Errorf("checkpoint workspace fence is missing: %w", err)
		}
		return nil
	}
	path := d.nodeCheckpointFencePath(key)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if os.IsExist(err) {
		return fmt.Errorf("a previous checkpoint workspace operation did not finish; verify that all old processes have stopped before recovering the workspace")
	}
	if err != nil {
		return err
	}
	// Do not store the bearer lease token in a filesystem file.
	digest := sha256.Sum256([]byte(token))
	_, writeErr := file.WriteString(hex.EncodeToString(digest[:]) + "\n")
	if writeErr == nil {
		writeErr = file.Sync()
	}
	if closeErr := file.Close(); writeErr == nil {
		writeErr = closeErr
	}
	if writeErr != nil {
		// Keep an incomplete marker on error: removing it could hide a crash.
		return fmt.Errorf("persist checkpoint workspace fence: %w", writeErr)
	}
	dir, err := os.Open(filepath.Dir(path))
	if err == nil {
		err = dir.Sync()
		_ = dir.Close()
	}
	if err != nil {
		return fmt.Errorf("persist checkpoint workspace fence directory: %w", err)
	}
	d.nodeCheckpointReservations.Store(key, token)
	return nil
}

func (d *Daemon) releaseNodeCheckpointFence(key, token string) error {
	current, ok := d.nodeCheckpointReservations.Load(key)
	if !ok || token == "" || current != token {
		return nil
	}
	if err := os.Remove(d.nodeCheckpointFencePath(key)); err != nil {
		return fmt.Errorf("release checkpoint workspace fence: %w", err)
	}
	d.nodeCheckpointReservations.Delete(key)
	return nil
}
