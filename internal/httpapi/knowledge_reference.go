package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/knowledge"
	"agentflow/internal/mcpclient"
)

// Resolve credentials and visibility per call. External servers support their
// credential scope only. Only explicitly read-only tools are offered.
func referenceTools(ctx context.Context, d *deps, project string, uid int64, ref domain.KnowledgeRef) ([]mcpclient.Tool, *mcpclient.Client, error) {
	if err := domain.ValidateKnowledgeRefs([]domain.KnowledgeRef{ref}); err != nil {
		return nil, nil, err
	}
	if authRequired() {
		user, ok := d.repo.User(uid)
		p, found := d.repo.Project(project)
		if !ok || !found || !workbenchProjectAllowsUser(p, uid, d.repo.IsPlatformAdmin(user.LoginName)) {
			return nil, nil, fmt.Errorf("知识服务项目授权已失效")
		}
	}
	if ref.ServerID == domain.KnowledgeMCPServerID {
		s := d.repo.KnowledgeSettings()
		if !s.Ready() {
			return nil, nil, knowledge.ErrNotConfigured
		}
		c, err := knowledge.NewClient(s)
		if err != nil {
			return nil, nil, err
		}
		tools, err := c.Tools(ctx)
		out := []mcpclient.Tool{}
		for _, tool := range tools {
			if knowledge.IsReadOnlyTool(tool.Name) {
				out = append(out, tool)
			}
		}
		return out, nil, err
	}
	server, ok := d.repo.MCPServer(ref.ServerID)
	if !ok || len(assistantOutboundMCPServers([]domain.MCPServerDefinition{server}, project, uid)) != 1 || server.Transport != "http" {
		return nil, nil, fmt.Errorf("知识服务不可用或无权限")
	}
	user, ok := d.repo.User(uid)
	if !ok {
		return nil, nil, fmt.Errorf("知识服务调用身份无效")
	}
	bound, err := bindAssistantMCP(server, user)
	if err != nil {
		return nil, nil, fmt.Errorf("知识服务凭据不可用")
	}
	timeout := knowledge.CallTimeout
	if server.TimeoutSec > 0 && time.Duration(server.TimeoutSec)*time.Second < timeout {
		timeout = time.Duration(server.TimeoutSec) * time.Second
	}
	client := mcpclient.New(bound.URL, bound.Headers).WithTimeout(timeout)
	tools, err := client.ListToolsContext(ctx)
	if err != nil {
		return nil, nil, knowledgeCallError(err)
	}
	out := []mcpclient.Tool{}
	for _, tool := range tools {
		var hints struct {
			ReadOnly bool `json:"readOnlyHint"`
		}
		if json.Unmarshal(tool.Annotations, &hints) == nil && hints.ReadOnly {
			out = append(out, tool)
		}
	}
	return out, client, nil
}
func callKnowledgeReference(ctx context.Context, d *deps, project string, uid int64, ref domain.KnowledgeRef, name string, args map[string]any) (string, int, error) {
	tools, client, err := referenceTools(ctx, d, project, uid, ref)
	if err != nil {
		return "", 0, err
	}
	for _, tool := range tools {
		if tool.Name != name {
			continue
		}
		if client != nil {
			text, err := client.CallToolContext(ctx, name, args)
			if err != nil {
				return "", 0, knowledgeCallError(err)
			}
			return truncateRunes(strings.TrimSpace(text), 32000), 0, nil
		}
		settings := d.repo.KnowledgeSettings()
		if !settings.Ready() {
			return "", 0, knowledge.ErrNotConfigured
		}
		if len(ref.Sources) > 0 {
			scoped, err := knowledge.ScopeSources(settings.SourcesForProject(project), ref.Sources)
			if err != nil || len(scoped) == 0 {
				return "", 0, fmt.Errorf("所选知识库范围已失效")
			}
			settings.ProjectSources = map[string][]string{project: scoped}
		}
		return (&knowledgeHandlers{deps: d}).callScoped(ctx, settings, project, name, args)
	}
	return "", 0, fmt.Errorf("知识服务未开放该只读工具")
}

func knowledgeReadResult(serverID, tool, text string, hits int, err error) (domain.KnowledgeRead, string) {
	read := domain.KnowledgeRead{ID: rand.Text(), ServerID: serverID, Tool: tool, Status: "已返回"}
	switch {
	case err != nil:
		read.Status = "调用失败"
	case strings.TrimSpace(text) == "" || (serverID == domain.KnowledgeMCPServerID && tool == knowledge.ToolSearch && hits == 0):
		read.Status = "无匹配"
	default:
		text += "\n若输出引用本次结果，请附上原文出处和 [[knowledge:" + read.ID + "]]。"
	}
	return read, text
}

// Keep upstream response bodies and headers out of diagnostics.
func knowledgeCallError(err error) error {
	var timed net.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded) || errors.As(err, &timed) && timed.Timeout():
		return fmt.Errorf("知识服务超时")
	case strings.Contains(err.Error(), "gateway HTTP 403:") || strings.Contains(err.Error(), "gateway HTTP 401:"):
		return fmt.Errorf("知识服务拒绝访问")
	default:
		return fmt.Errorf("知识服务不可用或调用失败")
	}
}
