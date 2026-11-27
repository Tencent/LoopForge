package gatemcp

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	AttemptPath   = "/attempts"
	TicketHeader  = "X-LoopForge-Gate-Ticket"
	attemptTTL    = 5 * time.Minute
	maxAttempts   = 1024
	maxMCPRequest = 1 << 20
)

type remoteAttempt struct {
	expires time.Time
	result  *gateResult
}

type remoteGate struct {
	mu       sync.Mutex
	attempts map[string]remoteAttempt
}

// NewHTTP exposes the gate tool to a remote agent and lets the engine collect
// submissions over its own outbound connection. Both sides have distinct credentials.
func NewHTTP(controlToken, connectorToken string) (http.Handler, error) {
	controlToken, connectorToken = strings.TrimSpace(controlToken), strings.TrimSpace(connectorToken)
	if controlToken == "" || connectorToken == "" || controlToken == connectorToken {
		return nil, errors.New("Gate MCP needs distinct control and connector tokens")
	}
	gate := &remoteGate{attempts: make(map[string]remoteAttempt)}
	mux := http.NewServeMux()
	mux.Handle(AttemptPath, bearerHandler(controlToken, gate.control))
	mux.Handle("/mcp", gateToolHTTP(connectorToken, gate.submit))
	return mux, nil
}

func gateToolHTTP(connectorToken string, submit func(json.RawMessage) (string, error)) http.HandlerFunc {
	schema := maps.Clone(inputSchema)
	properties := maps.Clone(inputSchema["properties"].(map[string]any))
	properties["ticket"] = map[string]any{"type": "string", "description": "One-time ticket supplied with this task"}
	schema["properties"] = properties
	schema["required"] = []string{"decision", "reason", "ticket"}

	return bearerHandler(connectorToken, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxMCPRequest))
		var req request
		if err != nil || json.Unmarshal(raw, &req) != nil || req.JSONRPC != "2.0" || req.Method == "" {
			http.Error(w, "invalid MCP request", http.StatusBadRequest)
			return
		}
		if len(req.ID) == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = handleRequest(json.NewEncoder(w), req, schema, submit)
	})
}

func bearerHandler(token string, handle http.HandlerFunc) http.HandlerFunc {
	want := sha256.Sum256([]byte("Bearer " + token))
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Header.Get("Origin") != "" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		have := sha256.Sum256([]byte(r.Header.Get("Authorization")))
		if token != "" && subtle.ConstantTimeCompare(want[:], have[:]) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		handle(w, r)
	}
}

func (g *remoteGate) control(w http.ResponseWriter, r *http.Request) {
	status, payload := g.attemptResponse(r.Method, r.Header.Get(TicketHeader))
	if status == http.StatusMethodNotAllowed {
		w.Header().Set("Allow", "GET, POST, DELETE")
	}
	if payload != nil {
		w.Header().Set("Content-Type", "application/json")
	}
	w.WriteHeader(status)
	if payload != nil {
		_ = json.NewEncoder(w).Encode(payload)
	}
}

func (g *remoteGate) attemptResponse(method, ticket string) (int, any) {
	now := time.Now()
	g.mu.Lock()
	defer g.mu.Unlock()
	switch method {
	case http.MethodPost:
		for id, attempt := range g.attempts {
			if !now.Before(attempt.expires) {
				delete(g.attempts, id)
			}
		}
		if len(g.attempts) >= maxAttempts {
			return http.StatusServiceUnavailable, nil
		}
		ticket = rand.Text()
		g.attempts[ticket] = remoteAttempt{expires: now.Add(attemptTTL)}
		return http.StatusCreated, map[string]string{"ticket": ticket}
	case http.MethodGet:
		attempt, ok := g.attempts[ticket]
		if !ok || !now.Before(attempt.expires) {
			return http.StatusNotFound, nil
		}
		if attempt.result == nil {
			return http.StatusNoContent, nil
		}
		return http.StatusOK, attempt.result
	case http.MethodDelete:
		delete(g.attempts, ticket)
		return http.StatusNoContent, nil
	default:
		return http.StatusMethodNotAllowed, nil
	}
}

func decodeRemoteSubmission(raw json.RawMessage) (string, gateResult, error) {
	var fields map[string]json.RawMessage
	var ticket string
	if json.Unmarshal(raw, &fields) != nil || json.Unmarshal(fields["ticket"], &ticket) != nil || ticket == "" {
		return "", gateResult{}, errors.New("gate ticket is required")
	}
	delete(fields, "ticket")
	arguments, err := json.Marshal(fields)
	if err != nil {
		return "", gateResult{}, err
	}
	result, problem := decodeResult(arguments)
	if problem != "" {
		return "", gateResult{}, errors.New(problem)
	}
	return ticket, result, nil
}

func (g *remoteGate) submit(raw json.RawMessage) (string, error) {
	ticket, result, err := decodeRemoteSubmission(raw)
	if err != nil {
		return "", err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	attempt, ok := g.attempts[ticket]
	if !ok || !time.Now().Before(attempt.expires) {
		return "", errors.New("gate ticket is invalid or expired")
	}
	if attempt.result != nil {
		return "", errors.New("gate result already submitted")
	}
	attempt.result = &result
	g.attempts[ticket] = attempt
	return gateSubmitted, nil
}
