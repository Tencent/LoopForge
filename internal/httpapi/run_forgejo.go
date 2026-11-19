package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/forge"

	"github.com/gin-gonic/gin"
)

// forgejoRunMCP authenticates the active SOP node, then narrows PR writes to
// its frozen repositories and work branches. The node receives no Forgejo PAT.
func (h *runsHandlers) forgejoRunMCP(c *gin.Context) {
	token, _ := bearerToken(c.Request)
	grant, err := forge.VerifyRunGrant(token, time.Now())
	run, ok := h.repo.RunState(grant.RunID)
	if err != nil || !ok || !activeForgejoNode(run, grant) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Forgejo 节点凭据已失效"})
		return
	}
	var request agentToolRPCRequest
	if err := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20)).Decode(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid JSON-RPC request"})
		return
	}
	if strings.HasPrefix(request.Method, "notifications/") {
		c.Status(http.StatusAccepted)
		return
	}
	response := agentToolRPCResponse{JSONRPC: "2.0", ID: request.ID}
	switch request.Method {
	case "initialize":
		response.Result = map[string]any{
			"protocolVersion": agentToolProtocolVersion(request.Params),
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]string{"name": domain.ForgeMCPServerID, "version": "1.0.0"},
		}
	case "ping":
		response.Result = map[string]any{}
	case "tools/list":
		response.Result = map[string]any{"tools": []map[string]any{{
			"name": "forgejo_pr_submit", "description": "Create or update this SOP node's Forgejo PR after pushing its frozen work branch.",
			"inputSchema": json.RawMessage(`{"type":"object","properties":{"repositoryId":{"type":"string"},"commit":{"type":"string"},"title":{"type":"string","minLength":1,"maxLength":200},"description":{"type":"string","maxLength":20000}},"required":["repositoryId","commit","title"],"additionalProperties":false}`),
		}}}
	case "tools/call":
		var params struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if json.Unmarshal(request.Params, &params) != nil || params.Name != "forgejo_pr_submit" {
			response.Error = &agentToolRPCError{Code: -32602, Message: "unknown Forgejo tool"}
			break
		}
		result, err := h.submitRunForgejoPR(c.Request.Context(), run, grant, params.Arguments)
		if err != nil {
			response.Result = mcpToolTextResult(err.Error(), true)
		} else {
			response.Result = mcpToolTextResult(result, false)
		}
	default:
		response.Error = &agentToolRPCError{Code: -32601, Message: "method not found"}
	}
	c.JSON(http.StatusOK, response)
}

func activeForgejoNode(run domain.Run, grant forge.RunGrant) bool {
	node := run.Nodes[grant.NodeID]
	return run.Status == "running" && !run.CancelRequested && !run.PauseRequested && run.Generation == grant.Generation &&
		run.ExecutionContext.ResponsibleUserID == grant.UserID &&
		node != nil && node.Status == "running" && node.Attempt == grant.Attempt
}

func (h *runsHandlers) submitRunForgejoPR(ctx context.Context, run domain.Run, grant forge.RunGrant, raw json.RawMessage) (string, error) {
	var input struct {
		RepositoryID string `json:"repositoryId"`
		Commit       string `json:"commit"`
		Title        string `json:"title"`
		Description  string `json:"description"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return "", fmt.Errorf("无效的 Forgejo PR 参数")
	}
	input.Title = strings.TrimSpace(input.Title)
	if input.Title == "" || len([]rune(input.Title)) > 200 || len([]rune(input.Description)) > 20000 || !forgejoCommitSHA.MatchString(input.Commit) {
		return "", fmt.Errorf("请填写有效标题、描述和已推送的完整提交 SHA")
	}
	var binding domain.RepositoryContext
	for _, rc := range run.RepositoryContexts() {
		if rc.RepositoryID == input.RepositoryID && !rc.ReadOnly && rc.WorkBranch != "" {
			binding = rc
			break
		}
	}
	ref, err := forge.RunRepository(binding)
	if err != nil {
		return "", err
	}
	userID := run.ExecutionContext.ResponsibleUserID
	user, ok := h.repo.User(userID)
	if !ok || !h.speakerProjectWriter(run.ExecutionContext.ProjectID, user, userID) {
		return "", errAgentToolForbidden
	}
	current, ok := h.repo.RunState(run.ID)
	if !ok || !activeForgejoNode(current, grant) {
		return "", fmt.Errorf("当前 SOP 节点已停止")
	}
	gateway := &agentToolGateway{deps: h.deps}
	pr, err := gateway.writeWorkspaceForgejoPR(ctx, userID, ref, binding.WorkBranch, repositoryBaseBranchName(binding), input.Commit, input.Title, input.Description)
	if err != nil {
		return "", err
	}
	out := map[string]any{"mrUrl": ref.PullURL(pr.Pull.Number), "sourceBranch": binding.WorkBranch,
		"targetBranch": pr.Pull.Delivery(ref).TargetBranch, "commit": input.Commit, "existing": pr.Existing, "updated": pr.Updated}
	if pr.UpdateError != "" {
		out["updateError"] = pr.UpdateError
	}
	if run.ExecutionContext.IssueID != "" {
		if err := h.repo.EnsureForgeDeliveryState(ref, pr.Pull.Number, run.ExecutionContext.IssueID, userID); err != nil {
			out["trackingError"] = "PR 已提交，登记需求交付失败"
		}
	}
	data, err := json.Marshal(out)
	return string(data), err
}
