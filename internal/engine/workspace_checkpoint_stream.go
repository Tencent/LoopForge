package engine

import (
	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type discardNodeCheckpointArchiveKey struct{}

func (e *Engine) saveConversationWorkspace(ctx context.Context, run domain.Run, token string) error {
	discard, _ := ctx.Value(discardNodeCheckpointArchiveKey{}).(bool)
	file, err := os.CreateTemp("", "agentflow-checkpoint-save-")
	if err != nil {
		return err
	}
	defer func() { file.Close(); os.Remove(file.Name()) }()
	if e.defaultExecutionRunner(run) != "" || e.foreignWorkspaceBind(run) {
		if err = e.downloadConversationCheckpoint(ctx, run, file); err != nil {
			return err
		}
		if _, err = file.Seek(0, io.SeekStart); err != nil {
			return err
		}
		if err = runtime.ValidateWorkspaceCheckpointFrom(workspaceCheckpointContextReader{ctx, file}); err != nil {
			return err
		}
	} else {
		root, reason := e.LocalRunWorkspace(run)
		if reason != "" {
			return fmt.Errorf("%s", reason)
		}
		err = runtime.CreateWorkspaceCheckpointToFiltered(root, workspaceCheckpointContextWriter{ctx, file}, discard)
		if os.IsNotExist(err) && run.WorkspaceHost == "" {
			if err = file.Truncate(0); err != nil {
				return err
			}
			if _, err = file.Seek(0, io.SeekStart); err != nil {
				return err
			}
			_, err = file.Write(runtime.EmptyWorkspaceCheckpoint())
		}
		if err != nil {
			return err
		}
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	return e.repo.SaveWorkspaceCheckpointStream(run.ID, token, time.Now().UTC(), workspaceCheckpointContextReader{ctx, file})
}

func (e *Engine) closeCheckpointTransfer(ctx context.Context, run domain.Run, id string) {
	if id == "" {
		return
	}
	budget := 5 * time.Second
	if ctx.Err() != nil {
		budget = time.Second
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), budget)
	defer cancel()
	_, _ = e.requestWorkspaceCheckpoint(ctx, run, domain.RunnerWorkspaceRequest{RunID: run.ID, WorkspaceLayout: run.ExecutionContext.RunnerWorkspaceLayout, Operation: domain.RunnerWorkspaceOperationCheckpointClose, CheckpointTransfer: &domain.WorkspaceCheckpointTransfer{ID: id}})
}

// legacyCheckpointTerminalActive is the message runners sent before checkpoint
// responses carried WorkspaceErrorTerminalActive.
const legacyCheckpointTerminalActive = "workspace terminal is still active"

func workspaceCheckpointResponseError(response domain.RunnerWorkspaceResponse) error {
	if response.Error == "" && response.Status < 400 {
		return nil
	}
	// An open terminal is an execution fence that waiting cannot clear: fail
	// fast as workspace preparation does, never as retryable lease contention.
	if response.Code == domain.WorkspaceErrorTerminalActive || response.Error == legacyCheckpointTerminalActive {
		return fmt.Errorf("%w（%s）", errWorkspaceTerminalActive, response.Error)
	}
	if runnerWorkspaceBusy(response) {
		return repo.ErrWorkspaceInUse
	}
	if response.Code == domain.WorkspaceErrorMissing || response.Status == 404 {
		return fmt.Errorf("%w: %s", os.ErrNotExist, response.Error)
	}
	return fmt.Errorf("workspace checkpoint request failed (HTTP %d): %s", response.Status, response.Error)
}
func (e *Engine) checkpointTransferRequest(ctx context.Context, run domain.Run, op string, transfer *domain.WorkspaceCheckpointTransfer) (domain.WorkspaceCheckpointTransfer, error) {
	response, err := e.requestWorkspaceCheckpoint(ctx, run, domain.RunnerWorkspaceRequest{RunID: run.ID, RunStatus: run.Status, WorkspaceLayout: run.ExecutionContext.RunnerWorkspaceLayout, Operation: op, CheckpointTransfer: transfer})
	if err != nil {
		return domain.WorkspaceCheckpointTransfer{}, err
	}
	if err = workspaceCheckpointResponseError(response); err != nil {
		return domain.WorkspaceCheckpointTransfer{}, err
	}
	var result domain.WorkspaceCheckpointTransfer
	if err = json.Unmarshal(response.Payload, &result); err != nil {
		return result, err
	}
	return result, nil
}
func (e *Engine) beginCheckpointExport(ctx context.Context, run domain.Run) (domain.RunnerWorkspaceResponse, error) {
	discard, _ := ctx.Value(discardNodeCheckpointArchiveKey{}).(bool)
	response, err := e.requestWorkspaceCheckpoint(ctx, run, domain.RunnerWorkspaceRequest{RunID: run.ID, RunStatus: run.Status, WorkspaceLayout: run.ExecutionContext.RunnerWorkspaceLayout, Operation: domain.RunnerWorkspaceOperationCheckpointExport, DiscardNodeCheckpoints: discard})
	if err != nil {
		return response, err
	}
	// Compatibility with runners predating chunked transfers. They can still
	// save small workspaces; a large archive is never retried as a huge RPC.
	if response.Status == 400 || strings.Contains(response.Error, "unsupported") {
		response, err = e.requestWorkspaceCheckpoint(ctx, run, domain.RunnerWorkspaceRequest{RunID: run.ID, RunStatus: run.Status, WorkspaceLayout: run.ExecutionContext.RunnerWorkspaceLayout, Operation: domain.RunnerWorkspaceOperationCheckpoint, DiscardNodeCheckpoints: discard})
		if err != nil {
			return response, err
		}
	}
	return response, nil
}
func (e *Engine) downloadConversationCheckpoint(ctx context.Context, run domain.Run, destination io.Writer) error {
	response, err := e.beginCheckpointExport(ctx, run)
	if err != nil {
		return err
	}
	if err = workspaceCheckpointResponseError(response); err != nil {
		return err
	}
	var manifest domain.WorkspaceCheckpointTransfer
	if err = json.Unmarshal(response.Payload, &manifest); err != nil {
		return err
	}
	if manifest.ID == "" {
		var legacy struct {
			Checkpoint []byte `json:"checkpoint"`
		}
		if err = json.Unmarshal(response.Payload, &legacy); err != nil {
			return err
		}
		if len(legacy.Checkpoint) == 0 || len(legacy.Checkpoint) > runtime.WorkspaceCheckpointMaxBytes {
			return fmt.Errorf("执行机器返回无效工作区快照；请更新执行器以支持分块快照")
		}
		_, err = destination.Write(legacy.Checkpoint)
		return err
	}
	defer e.closeCheckpointTransfer(ctx, run, manifest.ID)
	if manifest.Size <= 0 || manifest.Size > runtime.WorkspaceCheckpointArchiveMaxBytes || len(manifest.SHA256) != 64 {
		return fmt.Errorf("invalid checkpoint export manifest")
	}
	hash := sha256.New()
	writer := workspaceCheckpointContextWriter{ctx, io.MultiWriter(destination, hash)}
	for offset := int64(0); offset < manifest.Size; {
		chunk, err := e.checkpointTransferRequest(ctx, run, domain.RunnerWorkspaceOperationCheckpointRead, &domain.WorkspaceCheckpointTransfer{ID: manifest.ID, Offset: offset})
		if err != nil {
			return err
		}
		if chunk.ID != manifest.ID || chunk.Offset != offset || int64(len(chunk.Data)) != min(int64(runtime.WorkspaceCheckpointChunkBytes), manifest.Size-offset) {
			return fmt.Errorf("invalid checkpoint download chunk")
		}
		if _, err = writer.Write(chunk.Data); err != nil {
			return err
		}
		offset += int64(len(chunk.Data))
	}
	if hex.EncodeToString(hash.Sum(nil)) != manifest.SHA256 {
		return fmt.Errorf("checkpoint download checksum mismatch")
	}
	return nil
}

// Reclamation is recorded before deleting files, and a successful remote
// restore can lose its reply before the binding is updated. Probe the owner
// without exporting the entire checkout. Only a confirmed missing directory
// permits an upload; busy/offline owners must never trigger another restore.
func (e *Engine) restoreOrReuseRemoteConversationCheckpoint(ctx context.Context, run domain.Run) error {
	probe, err := e.requestWorkspaceCheckpoint(ctx, run, domain.RunnerWorkspaceRequest{RunID: run.ID, RunStatus: run.Status, WorkspaceLayout: run.ExecutionContext.RunnerWorkspaceLayout, Operation: domain.RunnerWorkspaceOperationCheckpointStatus})
	if err != nil {
		return err
	}
	if probe.Status == 400 || strings.Contains(probe.Error, "unsupported") {
		// Compatibility with workers/runners deployed before the cheap probe.
		probe, err = e.beginCheckpointExport(ctx, run)
		if err != nil {
			return err
		}
		if err = workspaceCheckpointResponseError(probe); err == nil {
			var transfer domain.WorkspaceCheckpointTransfer
			if err = json.Unmarshal(probe.Payload, &transfer); err != nil {
				return err
			}
			e.closeCheckpointTransfer(ctx, run, transfer.ID)
		} else if legacyCheckpointMissingPath(probe.Error) {
			if e.defaultExecutionRunner(run) != "" {
				// Old transfer handlers returned 409 for all filesystem errors.
				// Their read-only info operation still returns an actual 404 for
				// a missing root; an individual file disappearing during export
				// must not cause the whole checkout to be restored again.
				info, infoErr := e.requestWorkspaceCheckpoint(ctx, run, domain.RunnerWorkspaceRequest{RunID: run.ID, WorkspaceLayout: run.ExecutionContext.RunnerWorkspaceLayout, Operation: domain.RunnerWorkspaceOperationInfo})
				if infoErr != nil {
					return infoErr
				}
				if missing := workspaceCheckpointResponseError(info); errors.Is(missing, os.ErrNotExist) {
					err = missing
				}
			} else {
				// A legacy Worker publishes its workspace root. Match that root
				// exactly rather than treating missing nested files as reclamation.
				root := filepath.Join(run.WorkspaceRoot, safeRunnerPath(workspaceRunID(run)))
				if strings.HasPrefix(probe.Error, "lstat "+root+": ") || strings.HasPrefix(probe.Error, "stat "+root+": ") {
					err = os.ErrNotExist
				}
			}
		}
	} else {
		err = workspaceCheckpointResponseError(probe)
	}
	if err == nil {
		return e.verifyStoredCheckpoint(ctx, run.ID)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return e.restoreConversationCheckpoint(ctx, run, "")
}

func legacyCheckpointMissingPath(message string) bool {
	return strings.HasSuffix(message, ": no such file or directory") || strings.HasSuffix(message, ": The system cannot find the path specified.") || strings.HasSuffix(message, ": The system cannot find the file specified.")
}

func (e *Engine) restoreConversationCheckpoint(ctx context.Context, run domain.Run, root string) error {
	source, err := e.repo.OpenWorkspaceCheckpointArchive(run.ID)
	if err != nil {
		return err
	}
	defer source.Close()
	sourceReader := workspaceCheckpointContextReader{ctx, source}
	if e.defaultExecutionRunner(run) == "" && !e.foreignWorkspaceBind(run) {
		return runtime.RestoreWorkspaceCheckpointFrom(root, sourceReader)
	}
	meta, _ := e.repo.WorkspaceCheckpoint(run.ID)
	if meta.ArchiveBytes <= runtime.WorkspaceCheckpointMaxBytes {
		archive, err := io.ReadAll(io.LimitReader(sourceReader, runtime.WorkspaceCheckpointMaxBytes+1))
		if err != nil {
			return err
		}
		if len(archive) > runtime.WorkspaceCheckpointMaxBytes {
			return fmt.Errorf("invalid inline checkpoint size")
		}
		response, err := e.requestWorkspaceCheckpoint(ctx, run, domain.RunnerWorkspaceRequest{RunID: run.ID, RunStatus: run.Status, WorkspaceLayout: run.ExecutionContext.RunnerWorkspaceLayout, Operation: domain.RunnerWorkspaceOperationRestore, Checkpoint: archive})
		if err != nil {
			return err
		}
		return workspaceCheckpointResponseError(response)
	}
	manifest, err := e.checkpointTransferRequest(ctx, run, domain.RunnerWorkspaceOperationCheckpointUpload, &domain.WorkspaceCheckpointTransfer{Size: meta.ArchiveBytes, SHA256: meta.SHA256})
	if err != nil {
		return err
	}
	defer e.closeCheckpointTransfer(ctx, run, manifest.ID)
	if manifest.ID == "" || manifest.Size != meta.ArchiveBytes || manifest.SHA256 != meta.SHA256 {
		return fmt.Errorf("invalid checkpoint upload response")
	}
	buffer := make([]byte, runtime.WorkspaceCheckpointChunkBytes)
	for offset := int64(0); offset < meta.ArchiveBytes; {
		size := min(int64(len(buffer)), meta.ArchiveBytes-offset)
		n, err := io.ReadFull(sourceReader, buffer[:size])
		if err != nil {
			return err
		}
		ack, err := e.checkpointTransferRequest(ctx, run, domain.RunnerWorkspaceOperationCheckpointAppend, &domain.WorkspaceCheckpointTransfer{ID: manifest.ID, Offset: offset, Data: buffer[:n]})
		if err != nil {
			return err
		}
		offset += int64(n)
		if ack.ID != manifest.ID || ack.Offset != offset {
			return fmt.Errorf("invalid checkpoint upload acknowledgement")
		}
	}
	_, err = e.checkpointTransferRequest(ctx, run, domain.RunnerWorkspaceOperationCheckpointCommit, &domain.WorkspaceCheckpointTransfer{ID: manifest.ID})
	return err
}

func (e *Engine) verifyStoredCheckpoint(ctx context.Context, id string) error {
	source, err := e.repo.OpenWorkspaceCheckpointArchive(id)
	if err != nil {
		return err
	}
	defer source.Close()
	_, err = io.Copy(io.Discard, workspaceCheckpointContextReader{ctx, source})
	return err
}

type workspaceCheckpointContextReader struct {
	ctx    context.Context
	source io.Reader
}

func (r workspaceCheckpointContextReader) Read(data []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.source.Read(data)
}

type workspaceCheckpointContextWriter struct {
	ctx         context.Context
	destination io.Writer
}

func (w workspaceCheckpointContextWriter) Write(data []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	return w.destination.Write(data)
}
