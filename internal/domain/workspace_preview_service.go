package domain

import (
	"errors"
	"regexp"
	"strings"
)

var workspacePreviewServiceName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// WorkspacePreviewServiceRequest controls a named development server in the
// current workspace's sandbox. Command is only supplied when starting it.
type WorkspacePreviewServiceRequest struct {
	Operation string `json:"operation"`
	Name      string `json:"name"`
	Command   string `json:"command,omitempty"`
}

type WorkspacePreviewServiceState struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

func (r WorkspacePreviewServiceRequest) Validate() error {
	if !workspacePreviewServiceName.MatchString(r.Name) {
		return errors.New("服务名必须为 1 到 64 个小写字母、数字或连字符，并以字母或数字开头")
	}
	switch r.Operation {
	case "start":
		if strings.TrimSpace(r.Command) == "" || len(r.Command) > 16<<10 || strings.ContainsRune(r.Command, 0) {
			return errors.New("启动预览需要有效的前台命令（最多 16 KiB）")
		}
	case "status", "stop":
		if r.Command != "" {
			return errors.New("只有 start 可以指定启动命令")
		}
	default:
		return errors.New("预览服务操作必须为 start、status 或 stop")
	}
	return nil
}
