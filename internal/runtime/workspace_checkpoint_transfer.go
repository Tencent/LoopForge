package runtime

import (
	"agentflow/internal/domain"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// CheckpointTransfers keeps immutable exports and unfinished uploads on disk.
// Handles are random, scoped to a run/root, bounded, and expire after inactivity.
// Only the authenticated workspace control channel can reach this protocol.
type CheckpointTransfers struct {
	mu      sync.Mutex
	items   map[string]*checkpointTransfer
	pending int
}
type checkpointTransfer struct {
	mu                sync.Mutex
	runID, root, mode string
	file              *os.File
	info              domain.WorkspaceCheckpointTransfer
	offset            int64
	used              time.Time
}

// WorkspaceCheckpointStatus checks for a surviving checkout without archiving it.
// Callers fence this probe against restoration and execution on the owner.
func WorkspaceCheckpointStatus(root string) domain.RunnerWorkspaceResponse {
	info, err := os.Lstat(root)
	if os.IsNotExist(err) {
		return domain.RunnerWorkspaceResponse{Status: 404, Code: domain.WorkspaceErrorMissing, Error: err.Error()}
	}
	if err != nil {
		return domain.RunnerWorkspaceResponse{Status: 409, Error: err.Error()}
	}
	if !info.IsDir() {
		return domain.RunnerWorkspaceResponse{Status: 409, Error: "workspace is not a directory"}
	}
	return domain.RunnerWorkspaceResponse{Status: 200, Payload: json.RawMessage(`{"exists":true}`)}
}

func (p *CheckpointTransfers) Sweep(now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for id, item := range p.items {
		if item.mu.TryLock() {
			if now.Sub(item.used) > 5*time.Minute {
				item.file.Close()
				removeCheckpointFile(item.file)
				delete(p.items, id)
			}
			item.mu.Unlock()
		}
	}
}
func (p *CheckpointTransfers) Close() {
	p.mu.Lock()
	items := p.items
	p.items = nil
	p.mu.Unlock()
	for _, item := range items {
		item.mu.Lock()
		item.file.Close()
		removeCheckpointFile(item.file)
		item.mu.Unlock()
	}
}

func (p *CheckpointTransfers) Handle(ctx context.Context, root string, req domain.RunnerWorkspaceRequest) (response domain.RunnerWorkspaceResponse) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	response.Status = 200
	value, err := p.handle(ctx, root, req)
	if err != nil {
		response.Status, response.Error = 409, err.Error()
		if os.IsNotExist(err) {
			response.Status, response.Code = 404, domain.WorkspaceErrorMissing
		}
		return
	}
	response.Payload, err = json.Marshal(value)
	if err != nil {
		response.Status, response.Error = 409, err.Error()
	}
	return
}
func (p *CheckpointTransfers) handle(ctx context.Context, root string, req domain.RunnerWorkspaceRequest) (any, error) {
	p.Sweep(time.Now())
	op := req.Operation
	info := req.CheckpointTransfer
	if op == domain.RunnerWorkspaceOperationCheckpointExport || op == domain.RunnerWorkspaceOperationCheckpointUpload {
		if op == domain.RunnerWorkspaceOperationCheckpointUpload {
			if info == nil || info.Size <= 0 || info.Size > WorkspaceCheckpointArchiveMaxBytes || len(info.SHA256) != 64 {
				return nil, fmt.Errorf("invalid checkpoint upload manifest")
			}
			if _, err := hex.DecodeString(info.SHA256); err != nil {
				return nil, err
			}
		}
		p.mu.Lock()
		if len(p.items)+p.pending >= 4 {
			p.mu.Unlock()
			return nil, fmt.Errorf("too many checkpoint transfers")
		}
		p.pending++
		p.mu.Unlock()
		defer func() { p.mu.Lock(); p.pending--; p.mu.Unlock() }()
		f, err := os.CreateTemp("", "agentflow-checkpoint-")
		if err != nil {
			return nil, err
		}
		keep := false
		defer func() {
			if !keep {
				f.Close()
				removeCheckpointFile(f)
			}
		}()
		raw := make([]byte, 24)
		if _, err = rand.Read(raw); err != nil {
			return nil, err
		}
		item := &checkpointTransfer{runID: req.RunID, root: root, file: f, mode: op, used: time.Now()}
		item.info.ID = hex.EncodeToString(raw)
		if op == domain.RunnerWorkspaceOperationCheckpointExport {
			hash := sha256.New()
			if err = CreateWorkspaceCheckpointTo(root, checkpointContextWriter{ctx, io.MultiWriter(f, hash)}); err != nil {
				return nil, err
			}
			stat, err := f.Stat()
			if err != nil {
				return nil, err
			}
			item.info.Size, item.info.SHA256 = stat.Size(), hex.EncodeToString(hash.Sum(nil))
		} else {
			item.info.Size, item.info.SHA256 = info.Size, info.SHA256
		}
		item.used = time.Now()
		p.mu.Lock()
		if p.items == nil {
			p.items = make(map[string]*checkpointTransfer)
		}
		p.items[item.info.ID] = item
		p.mu.Unlock()
		keep = true
		return item.info, nil
	}
	if info == nil {
		return nil, fmt.Errorf("checkpoint transfer is required")
	}
	p.mu.Lock()
	item := p.items[info.ID]
	p.mu.Unlock()
	if item == nil {
		return nil, fmt.Errorf("checkpoint transfer expired or unavailable")
	}
	item.mu.Lock()
	defer item.mu.Unlock()
	if item.runID != req.RunID || item.root != root {
		return nil, fmt.Errorf("checkpoint transfer scope mismatch")
	}
	item.used = time.Now()
	remove := func() {
		p.mu.Lock()
		delete(p.items, info.ID)
		p.mu.Unlock()
		item.file.Close()
		removeCheckpointFile(item.file)
	}
	switch op {
	case domain.RunnerWorkspaceOperationCheckpointClose:
		remove()
		return map[string]bool{"closed": true}, nil
	case domain.RunnerWorkspaceOperationCheckpointRead:
		if item.mode != domain.RunnerWorkspaceOperationCheckpointExport || info.Offset < 0 || info.Offset >= item.info.Size {
			return nil, fmt.Errorf("invalid checkpoint read")
		}
		data := make([]byte, min(int64(WorkspaceCheckpointChunkBytes), item.info.Size-info.Offset))
		if _, err := item.file.ReadAt(data, info.Offset); err != nil {
			return nil, err
		}
		return domain.WorkspaceCheckpointTransfer{ID: info.ID, Offset: info.Offset, Data: data}, nil
	case domain.RunnerWorkspaceOperationCheckpointAppend:
		if item.mode != domain.RunnerWorkspaceOperationCheckpointUpload || len(info.Data) == 0 || len(info.Data) > WorkspaceCheckpointChunkBytes || info.Offset != item.offset || int64(len(info.Data)) > item.info.Size-item.offset {
			return nil, fmt.Errorf("invalid checkpoint chunk offset or size")
		}
		n, err := item.file.WriteAt(info.Data, item.offset)
		item.offset += int64(n)
		if err != nil {
			return nil, err
		}
		return domain.WorkspaceCheckpointTransfer{ID: info.ID, Offset: item.offset}, nil
	case domain.RunnerWorkspaceOperationCheckpointCommit:
		if item.mode != domain.RunnerWorkspaceOperationCheckpointUpload || item.offset != item.info.Size {
			return nil, fmt.Errorf("incomplete checkpoint upload")
		}
		defer remove()
		if _, err := item.file.Seek(0, io.SeekStart); err != nil {
			return nil, err
		}
		hash := sha256.New()
		if _, err := io.Copy(hash, checkpointContextReader{ctx, item.file}); err != nil {
			return nil, err
		}
		if hex.EncodeToString(hash.Sum(nil)) != item.info.SHA256 {
			return nil, fmt.Errorf("checkpoint checksum mismatch")
		}
		if _, err := item.file.Seek(0, io.SeekStart); err != nil {
			return nil, err
		}
		if err := RestoreWorkspaceCheckpointFrom(root, checkpointContextReader{ctx, item.file}); err != nil {
			return nil, err
		}
		return map[string]bool{"restored": true}, nil
	}
	return nil, fmt.Errorf("unsupported checkpoint transfer operation")
}

// Checkpoint files are created with os.CreateTemp in the process temp
// directory. Later lookups are keyed by a client-supplied id, so the path
// recovered from that object is not a deletion path: only a single temp-dir
// entry with the CreateTemp prefix can be removed, and os.Root keeps the
// operation inside that directory.
func removeCheckpointFile(file *os.File) {
	if file == nil {
		return
	}
	name := filepath.Clean(file.Name())
	base := filepath.Base(name)
	if !checkpointTempBase(base) {
		return
	}
	temp, err := filepath.Abs(os.TempDir())
	if err != nil {
		return
	}
	rel, err := filepath.Rel(temp, name)
	if err != nil || rel != base || !filepath.IsLocal(rel) {
		return
	}
	root, err := os.OpenRoot(temp)
	if err != nil {
		return
	}
	defer root.Close()
	_ = root.Remove(base)
}

func checkpointTempBase(base string) bool {
	rest, ok := strings.CutPrefix(base, "agentflow-checkpoint-")
	if !ok || rest == "" || !filepath.IsLocal(base) || filepath.Base(base) != base {
		return false
	}
	for _, r := range rest {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		default:
			return false
		}
	}
	return true
}

type checkpointContextWriter struct {
	ctx    context.Context
	writer io.Writer
}

func (w checkpointContextWriter) Write(data []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	return w.writer.Write(data)
}

type checkpointContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r checkpointContextReader) Read(data []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(data)
}
