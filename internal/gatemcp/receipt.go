package gatemcp

import (
	"bytes"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

const ReceiptPrefix = "LOOPFORGE_GATE_V1."
const MaxReceiptPayload = 32 << 10
const TicketKeyEnv = "AGENTFLOW_GATE_MCP_TICKET_KEY_FILE"

type receiptPayload struct {
	Ticket string          `json:"ticket"`
	Result json.RawMessage `json:"result"`
}

// OpenSigningKey reads the PKCS #8 Ed25519 private key for the WMA-side Gate.
func OpenSigningKey(path string) (ed25519.PrivateKey, error) {
	raw, err := readKeyPEM(path, "PRIVATE KEY")
	if err != nil {
		return nil, err
	}
	parsed, err := x509.ParsePKCS8PrivateKey(raw)
	key, ok := parsed.(ed25519.PrivateKey)
	if err != nil || !ok {
		return nil, errors.New("Gate MCP signing key must be an Ed25519 private key")
	}
	return key, nil
}

// OpenVerificationKey reads the corresponding SubjectPublicKeyInfo PEM.
func OpenVerificationKey(path string) (ed25519.PublicKey, error) {
	raw, err := readKeyPEM(path, "PUBLIC KEY")
	if err != nil {
		return nil, err
	}
	parsed, err := x509.ParsePKIXPublicKey(raw)
	key, ok := parsed.(ed25519.PublicKey)
	if err != nil || !ok {
		return nil, errors.New("Gate MCP verification key must be an Ed25519 public key")
	}
	return key, nil
}

func readKeyPEM(path, blockType string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, rest := pem.Decode(raw)
	if block == nil || block.Type != blockType || len(bytes.TrimSpace(rest)) != 0 {
		return nil, errors.New("Gate MCP key file has an invalid PEM format")
	}
	return block.Bytes, nil
}

// OpenTicketKey loads the optional key used to authorize submissions when the
// WMA manifest only supplies a URL. Only the MAC, never the key, reaches WMA.
func OpenTicketKey() ([]byte, error) {
	path := strings.TrimSpace(os.Getenv(TicketKeyEnv))
	if path == "" {
		return nil, nil
	}
	key, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(key) != sha256.Size {
		return nil, errors.New("Gate MCP ticket key must contain 32 random bytes")
	}
	return key, nil
}

func IssueTicket(key []byte) string {
	nonce := rand.Text()
	if len(key) == 0 {
		return nonce
	}
	return nonce + "." + ticketMAC(key, nonce)
}

func ticketMAC(key []byte, nonce string) string {
	signer := hmac.New(sha256.New, key)
	_, _ = signer.Write([]byte("LoopForge Gate Ticket v1:" + nonce))
	return base64.RawURLEncoding.EncodeToString(signer.Sum(nil))
}

func validTicket(key []byte, ticket string) bool {
	nonce, mac, hasMAC := strings.Cut(ticket, ".")
	if len(nonce) != 26 || strings.Trim(nonce, "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567") != "" {
		return false
	}
	if len(key) == 0 {
		return !hasMAC
	}
	return hmac.Equal([]byte(mac), []byte(ticketMAC(key, nonce)))
}

// VerifyReceipt authenticates the tool result and binds it to this invocation.
func VerifyReceipt(publicKey ed25519.PublicKey, ticket, receipt string) (json.RawMessage, error) {
	compact, ok := strings.CutPrefix(receipt, ReceiptPrefix)
	payloadText, signatureText, separated := strings.Cut(compact, ".")
	if !ok || !separated || len(payloadText) > base64.RawURLEncoding.EncodedLen(MaxReceiptPayload) ||
		len(signatureText) != base64.RawURLEncoding.EncodedLen(ed25519.SignatureSize) || len(publicKey) != ed25519.PublicKeySize {
		return nil, errors.New("invalid Gate MCP receipt format")
	}
	signature, err := base64.RawURLEncoding.Strict().DecodeString(signatureText)
	if err != nil || !ed25519.Verify(publicKey, []byte(ReceiptPrefix+payloadText), signature) {
		return nil, errors.New("invalid Gate MCP receipt signature")
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(payloadText)
	var payload receiptPayload
	if err != nil || json.Unmarshal(raw, &payload) != nil || payload.Ticket != ticket || len(payload.Result) == 0 {
		return nil, errors.New("Gate MCP receipt does not match this attempt")
	}
	return payload.Result, nil
}

type signedAttempt struct {
	expires time.Time
	digest  [sha256.Size]byte
}

// NewSignedHTTP exposes just the MCP tool. The caller brings the signed result
// back through the agent's existing response channel.
func NewSignedHTTP(connectorToken string, key ed25519.PrivateKey, ticketKey []byte) (http.Handler, error) {
	connectorToken = strings.TrimSpace(connectorToken)
	if len(key) != ed25519.PrivateKeySize || (connectorToken == "" && len(ticketKey) == 0) {
		return nil, errors.New("Gate MCP needs a signing key plus a connector token or authenticated tickets")
	}
	if len(ticketKey) != 0 && len(ticketKey) != sha256.Size {
		return nil, errors.New("Gate MCP ticket key must contain 32 random bytes")
	}
	var mu sync.Mutex
	submissions := make(map[string]signedAttempt)
	submit := func(raw json.RawMessage) (string, error) {
		ticket, result, err := decodeRemoteSubmission(raw)
		if err != nil {
			return "", err
		}
		if !validTicket(ticketKey, ticket) {
			return "", errors.New("invalid Gate MCP ticket")
		}
		resultJSON, err := json.Marshal(result)
		if err != nil {
			return "", err
		}
		payload, err := json.Marshal(receiptPayload{Ticket: ticket, Result: resultJSON})
		if err != nil {
			return "", err
		}
		if len(payload) > MaxReceiptPayload {
			return "", errors.New("Gate MCP result exceeds the signed receipt size limit")
		}

		mu.Lock()
		defer mu.Unlock()
		now := time.Now()
		for id, attempt := range submissions {
			if !now.Before(attempt.expires) {
				delete(submissions, id)
			}
		}
		digest := sha256.Sum256(payload)
		if existing, ok := submissions[ticket]; ok {
			if existing.digest != digest {
				return "", errors.New("gate result already submitted")
			}
		} else {
			if len(submissions) >= maxAttempts {
				return "", errors.New("Gate MCP is at capacity")
			}
			submissions[ticket] = signedAttempt{expires: now.Add(attemptTTL), digest: digest}
		}
		message := ReceiptPrefix + base64.RawURLEncoding.EncodeToString(payload)
		return message + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(key, []byte(message))), nil
	}

	mux := http.NewServeMux()
	mux.Handle("/mcp", gateToolHTTP(connectorToken, submit))
	return mux, nil
}
