package knowledge

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"agentflow/internal/domain"
)

const (
	connectorSecretPrefix = "v1:"
	localDevSecret        = "loopforge-local-dev-knowledge-secret"
)

func authRequiredEnv() bool {
	v := strings.TrimSpace(os.Getenv("AGENTFLOW_AUTH_REQUIRED"))
	if v == "" {
		return false
	}
	req, err := strconv.ParseBool(v)
	return err == nil && req
}

func platformSecretMaterial() (string, error) {
	for _, key := range []string{
		strings.TrimSpace(os.Getenv("LOOPFORGE_KNOWLEDGE_SECRET_KEY")),
		strings.TrimSpace(os.Getenv("AGENTFLOW_SESSION_SECRET")),
		strings.TrimSpace(os.Getenv("SESSION_SECRET")),
	} {
		if key != "" {
			return key, nil
		}
	}
	if authRequiredEnv() {
		return "", errors.New("未配置知识库凭据加密密钥（LOOPFORGE_KNOWLEDGE_SECRET_KEY 或 AGENTFLOW_SESSION_SECRET）")
	}
	return localDevSecret, nil
}

func connectorSecretAEAD() (cipher.AEAD, error) {
	material, err := platformSecretMaterial()
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte("loopforge/knowledge/connector-secret/v1\x00" + material))
	block, err := aes.NewCipher(sum[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func connectorSecretAAD(ownerUserID int64, connectorID string) []byte {
	return []byte(fmt.Sprintf("kbconn:%d:%s", ownerUserID, strings.TrimSpace(connectorID)))
}

// SealSecret encrypts secret JSON using AES-256-GCM bound to (ownerUserID, connectorID).
// An empty secret returns "", nil so keyless connectors store NULL/empty cleanly.
func SealSecret(ownerUserID int64, connectorID string, secret domain.KnowledgeConnectorSecret) (string, error) {
	if !secret.HasCredential() {
		return "", nil
	}
	raw, err := json.Marshal(secret)
	if err != nil {
		return "", err
	}
	aead, err := connectorSecretAEAD()
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := aead.Seal(nonce, nonce, raw, connectorSecretAAD(ownerUserID, connectorID))
	return connectorSecretPrefix + base64.StdEncoding.EncodeToString(sealed), nil
}

// OpenSecret decrypts a sealed connector secret bound to (ownerUserID, connectorID).
func OpenSecret(ownerUserID int64, connectorID, stored string) (domain.KnowledgeConnectorSecret, error) {
	stored = strings.TrimSpace(stored)
	if stored == "" {
		return domain.KnowledgeConnectorSecret{}, nil
	}
	if !strings.HasPrefix(stored, connectorSecretPrefix) {
		return domain.KnowledgeConnectorSecret{}, errors.New("知识库连接器凭据格式无法识别")
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(stored, connectorSecretPrefix))
	if err != nil {
		return domain.KnowledgeConnectorSecret{}, err
	}
	aead, err := connectorSecretAEAD()
	if err != nil {
		return domain.KnowledgeConnectorSecret{}, err
	}
	if len(raw) < aead.NonceSize() {
		return domain.KnowledgeConnectorSecret{}, errors.New("知识库连接器凭据内容不完整")
	}
	nonce, ciphertext := raw[:aead.NonceSize()], raw[aead.NonceSize():]
	plain, err := aead.Open(nil, nonce, ciphertext, connectorSecretAAD(ownerUserID, connectorID))
	if err != nil {
		return domain.KnowledgeConnectorSecret{}, fmt.Errorf("知识库连接器凭据解密失败：%w", err)
	}
	var out domain.KnowledgeConnectorSecret
	if err := json.Unmarshal(plain, &out); err != nil {
		return domain.KnowledgeConnectorSecret{}, fmt.Errorf("知识库连接器凭据内容损坏：%w", err)
	}
	return out, nil
}

// RedactSecrets replaces any sensitive secret material in text with ***.
func RedactSecrets(text string, secret domain.KnowledgeConnectorSecret) string {
	candidates := make([]string, 0, 2+len(secret.Headers))
	if s := strings.TrimSpace(secret.Token); s != "" {
		candidates = append(candidates, s)
	}
	if s := strings.TrimSpace(secret.RefreshToken); s != "" {
		candidates = append(candidates, s)
	}
	if s := strings.TrimSpace(secret.OAuthClientSecret); s != "" {
		candidates = append(candidates, s)
	}
	for _, v := range secret.Headers {
		if s := strings.TrimSpace(v); s != "" {
			candidates = append(candidates, s)
		}
	}
	for _, s := range candidates {
		text = strings.ReplaceAll(text, s, "***")
	}
	return text
}
