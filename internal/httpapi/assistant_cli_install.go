package httpapi

import (
	"context"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"agentflow/internal/runtime"

	"github.com/gin-gonic/gin"
)

// A CLI install outlives the browser request. Closing the page cancels the
// request context, and the download must keep going on the server.
const cliInstallTimeout = 10 * time.Minute

type cliInstallJob struct {
	done chan struct{}
	err  error
}

var (
	cliInstallJobsMu sync.Mutex
	cliInstallJobs   = map[string]*cliInstallJob{}
)

// installCLI downloads one allowlisted CLI onto this server after a conversation
// failed because it was missing. The body selects an engine id, not a package.
func (h *assistantHandlers) installCLI(c *gin.Context) {
	if _, ok := h.requestUser(c.Request, true); !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "请先登录后再安装 CLI"})
		return
	}
	var body struct {
		Engine string `json:"engine"`
	}
	if !decode(c, &body) {
		return
	}
	h.answerCLIInstall(c, body.Engine, true)
}

// cliInstallStatus reports an install that continues after the page is closed
// or reloaded. The button reads this instead of remembering the click locally.
func (h *assistantHandlers) cliInstallStatus(c *gin.Context) {
	if _, ok := h.requestUser(c.Request, true); !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "请先登录后再安装 CLI"})
		return
	}
	h.answerCLIInstall(c, c.Query("engine"), false)
}

func (h *assistantHandlers) answerCLIInstall(c *gin.Context, raw string, start bool) {
	engine := assistantInstallEngine(raw)
	if !runtime.OnDemandInstallable(engine) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "这个 CLI 不能从对话里安装"})
		return
	}
	if !start {
		status, errText := h.cliInstallState(engine)
		body := gin.H{"engine": engine, "status": status}
		if errText != "" {
			body["error"] = errText
		}
		c.JSON(http.StatusOK, body)
		return
	}
	job := h.startCLIInstall(engine)
	select {
	case <-job.done:
		if job.err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": job.err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"engine": engine, "installed": true})
	case <-c.Request.Context().Done():
		// The browser went away. startCLIInstall already detached the download.
	}
}

// cliInstallState is what a reloaded page should show for engine.
func (h *assistantHandlers) cliInstallState(engine string) (string, string) {
	if _, err := resolveAssistantEngineBin(engine); err == nil {
		return "installed", ""
	}
	cliInstallJobsMu.Lock()
	job := cliInstallJobs[engine]
	cliInstallJobsMu.Unlock()
	if job == nil {
		return "missing", ""
	}
	select {
	case <-job.done:
		if job.err != nil {
			return "failed", job.err.Error()
		}
		return "installed", ""
	default:
		return "installing", ""
	}
}

// startCLIInstall returns the in-flight install for engine, starting one when
// none is running. A finished attempt does not block the next click. The work
// uses a background context, so a disconnected browser does not stop npm or
// the provider installer.
func (h *assistantHandlers) startCLIInstall(engine string) *cliInstallJob {
	cliInstallJobsMu.Lock()
	defer cliInstallJobsMu.Unlock()
	if job, ok := cliInstallJobs[engine]; ok {
		select {
		case <-job.done:
		default:
			return job
		}
	}
	job := &cliInstallJob{done: make(chan struct{})}
	cliInstallJobs[engine] = job
	go h.runCLIInstall(engine, job)
	return job
}

func (h *assistantHandlers) runCLIInstall(engine string, job *cliInstallJob) {
	ctx, cancel := context.WithTimeout(context.Background(), cliInstallTimeout)
	defer cancel()
	_, err := runtime.InstallOnDemandCLI(ctx, engine)
	if err != nil {
		log.Printf("assistant CLI install %s: %v", engine, err)
	} else {
		h.noteCLIInstalled(engine)
	}
	job.err = err
	close(job.done)
}

// noteCLIInstalled replaces the failure the conversation stored before the
// CLI existed, including the generic generation message for that engine.
func (h *assistantHandlers) noteCLIInstalled(engine string) {
	if h == nil || h.repo == nil {
		return
	}
	missing := (assistantCLIMissingError{engine: engine}).Error()
	notice := assistantCLIInstalledNotice(engine)
	if err := h.repo.ReplaceInstalledCLIFailure(engine, missing, assistantGenerationFailedMessage, notice); err != nil {
		log.Printf("assistant CLI install notice %s: %v", engine, err)
	}
}

func assistantCLIInstalledNotice(engine string) string {
	return assistantEngineLabel(engine) + " 已安装，请重新发送。"
}

func assistantInstallEngine(id string) string {
	switch strings.TrimSpace(id) {
	case "claude", assistantEngineClaude:
		return assistantEngineClaude
	default:
		return strings.TrimSpace(id)
	}
}
