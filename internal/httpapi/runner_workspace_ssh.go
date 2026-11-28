package httpapi

import (
	"net/http"

	"agentflow/internal/workspacessh"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

func (h *runnersHandlers) connectRunnerWorkspaceSSH(c *gin.Context) {
	runner, ok := h.runnerSessionAuth(c)
	if !ok {
		return
	}
	if workspaceSSHSecret() == "" || h.workspaceSSH == nil || !websocket.IsWebSocketUpgrade(c.Request) || c.GetHeader("Origin") != "" {
		c.JSON(http.StatusNotFound, gin.H{"error": "SSH relay is unavailable"})
		return
	}
	attachment := h.workspaceSSH.claim(c.GetHeader(workspacessh.RelayTicketHeader), runner.ID)
	if attachment == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "SSH relay ticket is invalid or assigned to another Runner"})
		return
	}
	ws, err := runnerWSUpgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	stream := workspacessh.NewSocket(ws)
	select {
	case attachment.stream <- stream:
	case <-attachment.done:
		_ = stream.Close()
	}
}
