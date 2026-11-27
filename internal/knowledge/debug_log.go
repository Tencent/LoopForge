package knowledge

import (
	"encoding/json"
	"log"
	"os"
	"strconv"
	"strings"

	"agentflow/internal/domain"
)

const debugPayloadMaxRunes = 2000

// DebugEnabled reports whether verbose knowledge connector request/response
// payload logging is enabled via LOOPFORGE_KNOWLEDGE_DEBUG or AGENTFLOW_KNOWLEDGE_DEBUG.
func DebugEnabled() bool {
	for _, key := range []string{"LOOPFORGE_KNOWLEDGE_DEBUG", "AGENTFLOW_KNOWLEDGE_DEBUG"} {
		raw := strings.TrimSpace(os.Getenv(key))
		if raw == "" {
			continue
		}
		if ok, err := strconv.ParseBool(raw); err == nil {
			return ok
		}
	}
	return false
}

// sanitizeForLog marshals v (or formats string) and scrubs any secret material
// before truncating to maxRunes for safe ops/debug logging.
func sanitizeForLog(v any, secret domain.KnowledgeConnectorSecret, maxRunes int) string {
	var raw string
	switch x := v.(type) {
	case string:
		raw = x
	default:
		b, err := json.Marshal(v)
		if err != nil {
			raw = "{}"
		} else {
			raw = string(b)
		}
	}
	raw = RedactSecrets(raw, secret)
	if maxRunes <= 0 {
		maxRunes = debugPayloadMaxRunes
	}
	return truncateRunes(strings.TrimSpace(raw), maxRunes)
}

func logKnowledgeMCPTools(spec ConnectorSpec, rawCount, allowedCount int, blocked []string, err error) {
	if err != nil {
		log.Printf("knowledge-mcp-tools connector=%s type=%s user=%d ok=false err=%q",
			spec.ID, spec.Type, spec.OwnerUserID, RedactSecrets(err.Error(), spec.Secret))
		return
	}
	if len(blocked) > 0 || allowedCount == 0 || DebugEnabled() {
		log.Printf("knowledge-mcp-tools connector=%s type=%s user=%d ok=true raw=%d allowed=%d blocked=%q",
			spec.ID, spec.Type, spec.OwnerUserID, rawCount, allowedCount, strings.Join(blocked, ","))
	}
}

func logKnowledgeDebugCall(spec ConnectorSpec, origin, tool string, scope SourceScope, args map[string]any, res CallResult, callErr error) {
	if !DebugEnabled() {
		return
	}
	reqText := sanitizeForLog(args, spec.Secret, 800)
	if callErr != nil {
		log.Printf("knowledge-debug-call connector=%s type=%s user=%d origin=%s tool=%s scope=%s:%s latency=%dms ok=false req=%s err=%q",
			spec.ID, spec.Type, spec.OwnerUserID, origin, tool, scope.Mode, strings.Join(scope.Sources, ","),
			res.LatencyMs, reqText, RedactSecrets(callErr.Error(), spec.Secret))
		return
	}
	respText := sanitizeForLog(res.Text, spec.Secret, debugPayloadMaxRunes)
	log.Printf("knowledge-debug-call connector=%s type=%s user=%d origin=%s tool=%s scope=%s:%s latency=%dms ok=true status=%s hits=%d evidences=%d req=%s resp=%s",
		spec.ID, spec.Type, spec.OwnerUserID, origin, tool, scope.Mode, strings.Join(scope.Sources, ","),
		res.LatencyMs, res.Status, res.Hits, len(res.Evidences), reqText, respText)
}
