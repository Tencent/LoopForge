package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/runtime"
)

// A missing/mismatched service credential is not a model or node failure.
// Retrying the same node cannot repair it and must not trigger upstream rework.
var ErrWorkspaceControlConfiguration = errors.New("工作区控制通道配置错误")

func workspaceOperationLabel(operation string) string {
	switch operation {
	case domain.RunnerWorkspaceOperationCheckpoint:
		return "保存工作区快照"
	case domain.RunnerWorkspaceOperationRestore:
		return "恢复工作区快照"
	case domain.RunnerWorkspaceOperationReclaim:
		return "回收工作区"
	case domain.RunnerWorkspaceOperationPrepare:
		return "重建工作区环境"
	case domain.RunnerWorkspaceOperationPreparationStatus:
		return "检查工作区环境"
	case domain.RunnerWorkspaceOperationPreparationRecord:
		return "记录工作区准备结果"
	case domain.RunnerWorkspaceOperationArtifacts:
		return "恢复归档产物"
	case domain.RunnerWorkspaceOperationTerminal:
		return "访问工作区终端"
	default:
		return "访问工作区"
	}
}

func (e *Engine) requestWorkspaceCheckpoint(ctx context.Context, run domain.Run, request domain.RunnerWorkspaceRequest) (domain.RunnerWorkspaceResponse, error) {
	e.mu.Lock()
	apiBase, token := e.workspaceControlBase, e.controlToken
	e.mu.Unlock()
	base := ""
	if e.defaultExecutionRunner(run) != "" {
		if e.WorkspaceCheckpointTransport != nil {
			return e.WorkspaceCheckpointTransport(ctx, run, request)
		}
		base = apiBase
	} else if e.foreignWorkspaceBind(run) {
		base = e.repo.WorkspaceHostControlURL(run.WorkspaceHost)
	}
	operation := workspaceOperationLabel(request.Operation)
	if token == "" {
		return domain.RunnerWorkspaceResponse{}, fmt.Errorf("%w：无法%s；请为 API 和 Worker 配置相同的 AGENTFLOW_CONTROL_TOKEN", ErrWorkspaceControlConfiguration, operation)
	}
	if base == "" {
		hint := "工作区所属 Worker 未公布有效的 AGENTFLOW_CONTROL_URL"
		if e.defaultExecutionRunner(run) != "" {
			hint = "Worker 未配置内部 API 地址 AGENTFLOW_API_URL"
		}
		return domain.RunnerWorkspaceResponse{}, fmt.Errorf("%w：无法%s；%s", ErrWorkspaceControlConfiguration, operation, hint)
	}
	body, err := json.Marshal(request)
	if err != nil {
		return domain.RunnerWorkspaceResponse{}, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(base, "/")+"/internal/workspace-checkpoint", bytes.NewReader(body))
	if err != nil {
		return domain.RunnerWorkspaceResponse{}, err
	}
	req.Header.Set(controlTokenHeader, token)
	req.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		return domain.RunnerWorkspaceResponse{}, err
	}
	defer response.Body.Close()
	var result domain.RunnerWorkspaceResponse
	if response.StatusCode != 200 {
		if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
			return result, fmt.Errorf("%w：%s认证失败（HTTP %d）；请核对 API/Worker 的 AGENTFLOW_CONTROL_TOKEN 和内部 API 地址", ErrWorkspaceControlConfiguration, operation, response.StatusCode)
		}
		return result, fmt.Errorf("%s失败：工作区控制服务返回 HTTP %d", operation, response.StatusCode)
	}
	err = json.NewDecoder(io.LimitReader(response.Body, 20<<20)).Decode(&result)
	return result, err
}

// Service-only bounded snapshot transport. A configured control token is
// mandatory; an empty-token local interrupt policy never grants archive access.
func (e *Engine) HandleWorkspaceCheckpoint(w http.ResponseWriter, r *http.Request) {
	e.mu.Lock()
	configured := e.controlToken != ""
	e.mu.Unlock()
	if !configured || !e.controlTokenOK(r.Header.Get(controlTokenHeader)) {
		http.Error(w, "unauthorized", 401)
		return
	}
	var request domain.RunnerWorkspaceRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 20<<20)).Decode(&request); err != nil {
		http.Error(w, "invalid request", 400)
		return
	}
	run, ok := e.repo.RunState(request.RunID)
	if !ok {
		http.Error(w, "run not found", 404)
		return
	}
	if request.Operation != domain.RunnerWorkspaceOperationPreparationRecord && request.Operation != domain.RunnerWorkspaceOperationPrepare && request.Operation != domain.RunnerWorkspaceOperationPreparationStatus && request.Operation != domain.RunnerWorkspaceOperationTerminal && request.Operation != domain.RunnerWorkspaceOperationCheckpoint && request.Operation != domain.RunnerWorkspaceOperationRestore && request.Operation != domain.RunnerWorkspaceOperationReclaim && request.Operation != domain.RunnerWorkspaceOperationArtifacts {
		http.Error(w, "unsupported operation", 400)
		return
	}
	if request.Operation == domain.RunnerWorkspaceOperationTerminal && (request.Terminal == nil || request.TerminalOwner <= 0) {
		http.Error(w, "invalid terminal request", 400)
		return
	}
	artifactsAllowed := request.Operation == domain.RunnerWorkspaceOperationArtifacts && (run.WorkflowSnapshot.CollaborationMode == "group" || run.Status != "running" && run.Status != "queued" && !hasRunningNode(run))
	interactive := request.Operation == domain.RunnerWorkspaceOperationPreparationRecord || request.Operation == domain.RunnerWorkspaceOperationPrepare || request.Operation == domain.RunnerWorkspaceOperationPreparationStatus || request.Operation == domain.RunnerWorkspaceOperationTerminal
	if !interactive && !artifactsAllowed && !e.conversationWorkspaceIdle(run) {
		http.Error(w, "workspace in use", 409)
		return
	}
	// Bind execution flags to persisted state, including requests forwarded to a Runner.
	request.RunStatus = run.Status
	request.CollaborationMode = run.WorkflowSnapshot.CollaborationMode
	request.WorkspaceLayout = run.ExecutionContext.RunnerWorkspaceLayout
	if interactive {
		var err error
		request, err = e.preparedWorkspaceRequest(run, request)
		if err != nil {
			http.Error(w, err.Error(), 409)
			return
		}
	}
	var result domain.RunnerWorkspaceResponse
	if e.defaultExecutionRunner(run) != "" {
		if e.WorkspaceCheckpointTransport == nil {
			http.Error(w, "runner transport unavailable", 503)
			return
		}
		var err error
		result, err = e.WorkspaceCheckpointTransport(r.Context(), run, request)
		if err != nil {
			result.Error = err.Error()
			result.Status = 503
		}

	} else if interactive {
		switch request.Operation {
		case domain.RunnerWorkspaceOperationPreparationRecord:
			env, err := e.acquireConversationEnvironment(r.Context(), run)
			if err != nil {
				result.Error = err.Error()
				result.Status = 409
				break
			}
			env.nodeMu.Lock()
			state, err := runtime.RecordWorkspacePreparation(r.Context(), env.sandbox, *request.Workflow, request.Repositories, request.PreparationNodeID, request.PreparationOutput)
			env.nodeMu.Unlock()
			result.Payload, _ = json.Marshal(state)
			if err != nil {
				result.Error = err.Error()
				result.Status = 409
			}
		case domain.RunnerWorkspaceOperationPrepare:
			state, err := e.EnsureWorkspaceReady(r.Context(), run.ID)
			result.Payload, _ = json.Marshal(state)
			if err != nil {
				result.Error = err.Error()
				result.Status = 409
			}
		case domain.RunnerWorkspaceOperationPreparationStatus:
			state, err := e.WorkspacePreparationStatus(r.Context(), run.ID)
			result.Payload, _ = json.Marshal(state)
			if err != nil {
				result.Error = err.Error()
				result.Status = 409
			}
		case domain.RunnerWorkspaceOperationTerminal:
			if request.Terminal == nil || request.TerminalOwner <= 0 {
				result.Error = "invalid terminal request"
				result.Status = 400
				break
			}
			scope := fmt.Sprintf("%s:%d", run.ID, request.TerminalOwner)
			if request.Terminal.Operation == "close-all" {
				e.workspaceTerminals.CloseScope(scope)
				result.Payload = json.RawMessage(`{"ok":true}`)
				break
			}
			value, err := e.HandlePreparedWorkspaceTerminal(r.Context(), run.ID, &e.workspaceTerminals, scope, *request.Terminal)
			result.Payload, _ = json.Marshal(value)
			if err != nil {
				result.Error = err.Error()
				result.Status = 409
			}
		}
	} else {
		root, reason := e.LocalRunWorkspace(run)
		if reason != "" {
			http.Error(w, reason, 409)
			return
		}
		if request.Operation == domain.RunnerWorkspaceOperationReclaim {
			meta, saved := e.repo.WorkspaceCheckpoint(run.ID)
			if !saved || meta.SavedAt == nil || meta.Error != "" || run.WorkspaceCleanedAt.IsZero() || request.CheckpointLease == "" || meta.LeaseToken != request.CheckpointLease || meta.LeaseUntil == nil || !meta.LeaseUntil.After(time.Now()) {
				http.Error(w, "workspace cleanup not reserved", 409)
				return
			}
			if err := e.releaseRunEnvironment(run.ID, true); err != nil {
				result.Error = err.Error()
			} else {
				result.Payload = json.RawMessage(`{"reclaimed":true}`)
			}
		} else if request.Operation == domain.RunnerWorkspaceOperationArtifacts {
			restored, err := runtime.RestoreMissingWorkspaceArtifacts(root, request.ArtifactFiles)
			if err != nil {
				result.Error = err.Error()
			} else {
				result.Payload, _ = json.Marshal(map[string]any{"restoredArtifacts": restored})
			}
		} else if request.Operation == domain.RunnerWorkspaceOperationCheckpoint {
			archive, err := runtime.CreateWorkspaceCheckpoint(root)
			if err != nil {
				result.Error = err.Error()
			} else {
				result.Payload, _ = json.Marshal(map[string]any{"checkpoint": archive})
			}
		} else if err := runtime.RestoreWorkspaceCheckpoint(root, request.Checkpoint); err != nil {
			result.Error = err.Error()
		} else {
			result.Payload = json.RawMessage(`{"restored":true}`)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}
