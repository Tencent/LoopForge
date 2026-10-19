package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"agentflow/internal/domain"
)

type AgentToolMode string

const (
	AgentToolClarify   AgentToolMode = "clarify"
	AgentToolAssistant AgentToolMode = "assistant"
	AgentToolScheduled AgentToolMode = "scheduled"
	AgentToolReport    AgentToolMode = "report"
)

const (
	agentToolSessionTokenBytes = 32
	agentToolSessionDefaultTTL = 5 * time.Minute
	agentToolJanitorInterval   = 30 * time.Second
)

type AgentTerminalKind string

const (
	AgentTerminalClarification AgentTerminalKind = "clarification"
	AgentTerminalAssistant     AgentTerminalKind = "assistant"
	AgentTerminalReport        AgentTerminalKind = "report"
)

type AgentTerminalResult struct {
	Kind          AgentTerminalKind
	Clarification *clarifyModelResult
	Assistant     *assistantReply
	Report        *domain.AINotificationContent
}

// AgentToolSession is a short-lived, in-process MCP gateway binding for one
// model request. The token is CSPRNG random and is never logged or returned to
// the browser.
type AgentToolSession struct {
	Token            string
	UserID           int64
	ProjectID        string
	Mode             AgentToolMode
	IssueID          string
	WorkspaceToken   string
	RunID            string
	ExpiresAt        time.Time
	MCPServers       []domain.MCPServerDefinition
	User             domain.User
	AttachmentIDs    map[string]bool
	AttachmentOwners map[string]int64
	ExecMode         assistantExecMode
	Final            chan AgentTerminalResult

	mu           sync.Mutex
	catalog      []agentToolDescriptor
	terminalOnly bool
	closed       atomic.Bool
	CancelModel  context.CancelFunc
}

// SetTerminalOnly drops read/query tools so a retry turn can only submit.
func (s *AgentToolSession) SetTerminalOnly() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.terminalOnly = true
	s.catalog = nil
	s.MCPServers = nil
	s.mu.Unlock()
}

type agentToolSessionRegistry struct {
	mu       sync.Mutex
	sessions map[string]*AgentToolSession
}

var (
	agentToolSessions    = newAgentToolSessionRegistry()
	agentToolJanitorOnce sync.Once
)

func newAgentToolSessionRegistry() *agentToolSessionRegistry {
	return &agentToolSessionRegistry{sessions: map[string]*AgentToolSession{}}
}

func resetAgentToolSessionsForTest() {
	agentToolSessions = newAgentToolSessionRegistry()
}

func ensureAgentToolJanitor() {
	agentToolJanitorOnce.Do(func() {
		go agentToolSessions.janitor()
	})
}

func newAgentToolSessionToken() (string, error) {
	var raw [agentToolSessionTokenBytes]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

func agentToolSessionTTL(ctx context.Context) time.Duration {
	if ctx != nil {
		if deadline, ok := ctx.Deadline(); ok {
			d := time.Until(deadline)
			if d < time.Second {
				return time.Second
			}
			return d
		}
	}
	return agentToolSessionDefaultTTL
}

func (r *agentToolSessionRegistry) create(session *AgentToolSession) error {
	if session == nil {
		return errAgentToolInvalidSession
	}
	token := strings.TrimSpace(session.Token)
	if token == "" {
		var err error
		token, err = newAgentToolSessionToken()
		if err != nil {
			return err
		}
		session.Token = token
	}
	if session.Final == nil {
		session.Final = make(chan AgentTerminalResult, 1)
	}
	if session.ExpiresAt.IsZero() {
		session.ExpiresAt = time.Now().Add(agentToolSessionDefaultTTL)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sessions == nil {
		r.sessions = map[string]*AgentToolSession{}
	}
	r.sessions[token] = session
	ensureAgentToolJanitor()
	return nil
}

func (r *agentToolSessionRegistry) get(token string) (*AgentToolSession, bool) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, false
	}
	r.mu.Lock()
	session, ok := r.sessions[token]
	r.mu.Unlock()
	if !ok || session == nil {
		return nil, false
	}
	if session.expired(time.Now()) {
		r.delete(token)
		return nil, false
	}
	return session, true
}

func (r *agentToolSessionRegistry) delete(token string) {
	token = strings.TrimSpace(token)
	if token == "" {
		return
	}
	r.mu.Lock()
	session, ok := r.sessions[token]
	if ok {
		delete(r.sessions, token)
	}
	r.mu.Unlock()
	if session != nil {
		session.shutdown()
	}
}

func (r *agentToolSessionRegistry) janitor() {
	ticker := time.NewTicker(agentToolJanitorInterval)
	defer ticker.Stop()
	for range ticker.C {
		r.purgeExpired(time.Now())
	}
}

func (r *agentToolSessionRegistry) purgeExpired(now time.Time) {
	r.mu.Lock()
	var stale []*AgentToolSession
	for token, session := range r.sessions {
		if session == nil || session.expired(now) {
			delete(r.sessions, token)
			if session != nil {
				stale = append(stale, session)
			}
		}
	}
	r.mu.Unlock()
	for _, session := range stale {
		session.shutdown()
	}
}

func (s *AgentToolSession) expired(now time.Time) bool {
	if s == nil {
		return true
	}
	return !s.ExpiresAt.IsZero() && !s.ExpiresAt.After(now)
}

func (s *AgentToolSession) shutdown() {
	if s == nil {
		return
	}
	s.closed.Store(true)
	if s.CancelModel != nil {
		s.CancelModel()
	}
}

func (s *AgentToolSession) Close() {
	if s == nil {
		return
	}
	agentToolSessions.delete(s.Token)
}

func (s *AgentToolSession) finished() bool {
	return s != nil && s.closed.Load()
}

func (s *AgentToolSession) commitFinal(result AgentTerminalResult) error {
	if s == nil {
		return errAgentToolInvalidSession
	}
	if !s.closed.CompareAndSwap(false, true) {
		return errAgentToolTerminalCommitted
	}
	select {
	case s.Final <- result:
	default:
	}
	if s.CancelModel != nil {
		s.CancelModel()
	}
	logAgentToolEvent("terminal", "mode="+string(s.Mode), "kind="+string(result.Kind), "ok=1")
	return nil
}

func (s *AgentToolSession) takeFinal() (AgentTerminalResult, bool) {
	if s == nil || s.Final == nil {
		return AgentTerminalResult{}, false
	}
	select {
	case result := <-s.Final:
		return result, true
	default:
		return AgentTerminalResult{}, false
	}
}

func logAgentToolEvent(event string, fields ...string) {
	parts := make([]string, 0, 1+len(fields))
	parts = append(parts, event)
	for _, field := range fields {
		if strings.TrimSpace(field) == "" {
			continue
		}
		lower := strings.ToLower(field)
		if strings.Contains(lower, "authorization") || strings.Contains(lower, "token=") || strings.Contains(lower, "bearer ") {
			continue
		}
		parts = append(parts, field)
	}
	log.Printf("agent-tool %s", strings.Join(parts, " "))
}

func logAgentProtocolFailure(mode AgentToolMode, modelID, class string, err error) {
	reason := class
	if err != nil {
		msg := strings.TrimSpace(err.Error())
		if msg != "" {
			reason = class + ":" + truncateRunes(msg, 160)
		}
	}
	logAgentToolEvent("protocol-failure", "mode="+string(mode), "model="+strings.TrimSpace(modelID), "class="+reason)
}

func (s *AgentToolSession) terminalToolsOnly() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.terminalOnly
}
