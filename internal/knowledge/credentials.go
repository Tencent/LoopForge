package knowledge

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"

	"agentflow/internal/knowledge/gitremote"
	"golang.org/x/crypto/ssh"
)

// GitCredential belongs to a source, independently of any user's credentials.
// CanWrite records the provisioned grant for future write-back operations.
type GitCredential struct {
	Kind       string `json:"kind"`
	Username   string `json:"username,omitempty"`
	Token      string `json:"token,omitempty"`
	PrivateKey string `json:"privateKey,omitempty"`
	KnownHosts string `json:"knownHosts,omitempty"`
	CanWrite   bool   `json:"canWrite"`
}

func credentialCipher() (cipher.AEAD, error) {
	secret := SessionSecret()
	if secret == "" {
		return nil, errors.New("请先配置 AGENTFLOW_SESSION_SECRET，再设置来源凭据")
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("agentflow-knowledge-source-credential-v1"))
	block, err := aes.NewCipher(mac.Sum(nil))
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func validateGitCredential(c GitCredential, remote string) error {
	switch c.Kind {
	case "https":
		if !strings.HasPrefix(remote, "https://") || c.Username == "" || strings.Contains(c.Username, ":") || c.Token == "" || strings.ContainsAny(c.Username+c.Token, "\r\n\x00") || c.PrivateKey != "" || c.KnownHosts != "" {
			return errors.New("HTTPS 凭据需要用户名和令牌，并使用 HTTPS 地址")
		}
	case "ssh":
		u, err := gitremote.Parse(remote)
		if err != nil || u.Scheme != "ssh" || c.Token != "" || c.Username != "" {
			return errors.New("SSH 部署密钥需要 SSH 地址")
		}
		if _, err := ssh.ParsePrivateKey([]byte(c.PrivateKey)); err != nil {
			return errors.New("SSH 私钥无效，请使用未加密的只读部署密钥")
		}
		rest := []byte(strings.TrimSpace(c.KnownHosts))
		if len(rest) == 0 {
			return errors.New("请填写仓库服务器的 known_hosts 公钥记录")
		}
		for len(rest) > 0 {
			_, _, _, _, next, err := ssh.ParseKnownHosts(rest)
			if err != nil {
				return errors.New("known_hosts 公钥记录无效")
			}
			rest = next
		}
	default:
		return errors.New("不支持的来源凭据类型")
	}
	return nil
}

func encryptGitCredential(sourceID string, c GitCredential) (string, error) {
	aead, err := credentialCipher()
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return "", err
	}
	return "v1." + base64.RawStdEncoding.EncodeToString(aead.Seal(nonce, nonce, body, []byte(sourceID))), nil
}

// DecryptGitCredential only runs in the sync worker. Source ID authenticates the
// ciphertext so copying an encrypted value to another source cannot reuse it.
func DecryptGitCredential(sourceID, encoded string) (*GitCredential, error) {
	if encoded == "" {
		return nil, nil
	}
	fail := errors.New("来源凭据无法解密，请检查服务会话密钥或重新配置凭据")
	if !strings.HasPrefix(encoded, "v1.") {
		return nil, fail
	}
	aead, err := credentialCipher()
	if err != nil {
		return nil, fail
	}
	raw, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(encoded, "v1."))
	if err != nil || len(raw) < aead.NonceSize() {
		return nil, fail
	}
	body, err := aead.Open(nil, raw[:aead.NonceSize()], raw[aead.NonceSize():], []byte(sourceID))
	if err != nil {
		return nil, fail
	}
	var c GitCredential
	if json.Unmarshal(body, &c) != nil {
		return nil, fail
	}
	return &c, nil
}
