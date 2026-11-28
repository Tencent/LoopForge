package workbuddy

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"

	"agentflow/internal/gatemcp"
)

const gatePublicKeyEnv = "AGENTFLOW_WMA_GATE_PUBLIC_KEY_FILE"

var gateReceiptPattern = regexp.MustCompile(regexp.QuoteMeta(gatemcp.ReceiptPrefix) + `[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+`)

type GateAttempt struct {
	Ticket    string
	publicKey ed25519.PublicKey
	relay     *GateClient
	question  *acpQuestionGate
}

// BeginGate generates the ACP receipt challenge locally when a WMA-side Gate
// key is configured. The shared relay remains usable for joint networks.
func BeginGate(ctx context.Context) (*GateAttempt, error) {
	if os.Getenv("AGENTFLOW_WMA_GATE_ACP_QUESTION") == "1" {
		return beginACPQuestionGate()
	}
	if path := strings.TrimSpace(os.Getenv(gatePublicKeyEnv)); path != "" {
		key, err := gatemcp.OpenVerificationKey(path)
		if err != nil {
			return nil, fmt.Errorf("读取 WorkBuddy Gate 验签公钥：%w", err)
		}
		ticketKey, err := gatemcp.OpenTicketKey()
		if err != nil {
			return nil, fmt.Errorf("读取 WorkBuddy Gate 票据密钥：%w", err)
		}
		return &GateAttempt{Ticket: gatemcp.IssueTicket(ticketKey), publicKey: key}, nil
	}
	relay, err := NewGateClient()
	if err != nil {
		return nil, err
	}
	ticket, err := relay.Open(ctx)
	if err != nil {
		return nil, err
	}
	return &GateAttempt{Ticket: ticket, relay: relay}, nil
}

func (a *GateAttempt) ReturnsViaACP() bool { return len(a.publicKey) != 0 || a.UsesACPQuestion() }

func (a *GateAttempt) UsesACPQuestion() bool { return a != nil && a.question != nil }

func (a *GateAttempt) Discard(ctx context.Context) {
	if a.relay != nil {
		a.relay.Discard(ctx, a.Ticket)
	}
	if a.UsesACPQuestion() {
		_ = os.RemoveAll(a.question.directory)
	}
}

func (a *GateAttempt) Result(ctx context.Context, turn Turn) ([]byte, error) {
	if a.relay != nil {
		return a.relay.Result(ctx, a.Ticket)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if a.UsesACPQuestion() {
		return a.question.result()
	}
	receipts := turn.gateReceipts
	receipts.collect(turn.Text)
	if receipts.overflow {
		return nil, errors.New("WorkBuddy ACP 返回过多 Gate 回执")
	}
	var decision []byte
	for _, candidate := range receipts.matches {
		result, err := gatemcp.VerifyReceipt(a.publicKey, a.Ticket, candidate)
		if err != nil {
			continue
		}
		if decision != nil {
			return nil, errors.New("WorkBuddy ACP 返回了多个有效 Gate 终态")
		}
		decision = result
	}
	if decision == nil {
		return nil, errors.New("WorkBuddy 未通过 ACP 回传本次执行的有效 Gate 签名回执")
	}
	return decision, nil
}

// GateDisplayText removes transport data from the run's human-facing output.
func GateDisplayText(text string) string {
	return strings.TrimSpace(gateReceiptPattern.ReplaceAllString(text, ""))
}

type gateReceiptSet struct {
	matches  []string
	overflow bool
}

func (s *gateReceiptSet) collect(text string) {
	if s.overflow {
		return
	}
	const maxCandidates = 8
	maxSize := len(gatemcp.ReceiptPrefix) + base64.RawURLEncoding.EncodedLen(gatemcp.MaxReceiptPayload) + 1 + base64.RawURLEncoding.EncodedLen(ed25519.SignatureSize)
	for _, receipt := range gateReceiptPattern.FindAllString(text, -1) {
		if len(receipt) > maxSize || slices.Contains(s.matches, receipt) {
			continue
		}
		if len(s.matches) == maxCandidates {
			s.overflow = true
			return
		}
		s.matches = append(s.matches, strings.Clone(receipt))
	}
}
