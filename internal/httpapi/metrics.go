package httpapi

import (
	"crypto/subtle"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// Prometheus text exposition.
//
// The platform already keeps the numbers (runs, queue, runners); this endpoint
// exposes them in the format a scraper expects instead of making an operator
// poll the JSON API. When AGENTFLOW_METRICS_TOKEN is set the endpoint requires
// that bearer token; without it a sidecar scraper on the same host can read it.
const metricsTokenEnv = "AGENTFLOW_METRICS_TOKEN"

// metricsHandlers serves GET /metrics.
type metricsHandlers struct{ *deps }

var metricsStartedAt = time.Now()

func (h *metricsHandlers) metrics(c *gin.Context) {
	if expected := strings.TrimSpace(os.Getenv(metricsTokenEnv)); expected != "" {
		token := strings.TrimSpace(strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer "))
		if subtle.ConstantTimeCompare([]byte(token), []byte(expected)) != 1 {
			c.Header("WWW-Authenticate", `Bearer realm="metrics"`)
			c.Status(401)
			return
		}
	}
	byStatus := map[string]int{}
	for _, run := range h.repo.Runs() {
		byStatus[run.Status]++
	}
	c.Header("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w := c.Writer
	fmt.Fprintf(w, "# HELP loopforge_runs_total Runs by status.\n# TYPE loopforge_runs_total gauge\n")
	for _, status := range []string{"queued", "running", "waiting", "succeeded", "failed", "cancelled"} {
		fmt.Fprintf(w, "loopforge_runs_total{status=%q} %d\n", status, byStatus[status])
	}
	fmt.Fprintf(w, "# HELP loopforge_uptime_seconds Seconds since this process started.\n# TYPE loopforge_uptime_seconds counter\nloopforge_uptime_seconds %.0f\n", time.Since(metricsStartedAt).Seconds())
}
