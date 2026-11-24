package runner

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"agentflow/internal/attachment"
	"agentflow/internal/domain"
	"agentflow/internal/runtime"
)

func (c *Client) requirementAttachment(ctx context.Context, task domain.RunnerTask, a domain.IssueAttachment) ([]byte, error) {
	if !attachment.ValidID(a.ID) || a.Size <= 0 || a.Size > attachment.Limit {
		return nil, fmt.Errorf("invalid requirement attachment")
	}
	path := "/runners/tasks/" + url.PathEscape(task.ID) + "/attachments/" + a.ID
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.sessionToken())
	req.Header.Set("X-Task-Attempt", strconv.Itoa(task.Attempt))
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, ErrUnauthorized
	}
	if resp.StatusCode == http.StatusConflict {
		return nil, ErrTaskLeaseLost
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("需求附件 %q 下载失败（HTTP %d）", a.Name, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, attachment.Limit+1))
	if err != nil || int64(len(data)) != a.Size {
		return nil, fmt.Errorf("需求附件 %q 下载不完整", a.Name)
	}
	return data, nil
}

func (d *Daemon) prepareRequirementAttachments(ctx context.Context, task domain.RunnerTask, workspace string) error {
	if task.Invocation == nil {
		return nil
	}
	for _, a := range task.Invocation.RequirementAttachments {
		data, err := d.client.requirementAttachment(ctx, task, a)
		if err != nil {
			return err
		}
		if err := runtime.WriteRequirementAttachment(workspace, a.AssistantAttachment, data); err != nil {
			return err
		}
	}
	return nil
}
