package knowledge

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
)

// 控制台密码以 "v1:" + base64(nonce||ciphertext) 的形式单独成行保存，
// 前缀让未来的密钥轮换或算法变更可以被识别，而不是把旧密文喂给 GCM。
const consoleSecretPrefix = "v1:"

// storedConsoleSecret 是加密前的明文结构：账号与密码一起保存，避免连接
// 后来改了 account 之后抽屉里显示成“别人的账号 + 旧密码”。
type storedConsoleSecret struct {
	Account  string `json:"account"`
	Password string `json:"password"`
}

// consoleSecretMaterials 按优先级返回可作为密钥材料的部署密钥。专用密钥最
// 优先；AGENTFLOW_SESSION_SECRET 每个部署本来就有，因此开启该功能不需要
// 运维新增配置。
func consoleSecretMaterials() []string {
	return []string{
		strings.TrimSpace(os.Getenv("LOOPFORGE_KNOWLEDGE_SECRET_KEY")),
		strings.TrimSpace(os.Getenv("AGENTFLOW_SESSION_SECRET")),
	}
}

func consoleSecretAEAD() (cipher.AEAD, error) {
	for _, material := range consoleSecretMaterials() {
		if material == "" {
			continue
		}
		// 域分离：同一个部署密钥不直接复用，避免与其他用途的密文互换。
		sum := sha256.Sum256([]byte("loopforge/knowledge/console-secret/v1\x00" + material))
		block, err := aes.NewCipher(sum[:])
		if err != nil {
			return nil, err
		}
		return cipher.NewGCM(block)
	}
	return nil, errors.New("未配置控制台凭据加密密钥（LOOPFORGE_KNOWLEDGE_SECRET_KEY 或 AGENTFLOW_SESSION_SECRET）")
}

// sealConsoleSecret 加密一段明文，返回值可直接写入 knowledge_settings.token。
// connID 作为 AAD 绑定，密文无法被复制到另一个连接的行里解开。
func sealConsoleSecret(connID, plain string) (string, error) {
	if strings.TrimSpace(plain) == "" {
		return "", errors.New("控制台密码为空")
	}
	aead, err := consoleSecretAEAD()
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := aead.Seal(nonce, nonce, []byte(plain), consoleSecretAAD(connID))
	return consoleSecretPrefix + base64.StdEncoding.EncodeToString(sealed), nil
}

// openConsoleSecret 解密 sealConsoleSecret 的输出。
func openConsoleSecret(connID, stored string) (string, error) {
	stored = strings.TrimSpace(stored)
	if stored == "" {
		return "", errors.New("控制台密码为空")
	}
	if !strings.HasPrefix(stored, consoleSecretPrefix) {
		return "", errors.New("控制台密码的存储格式无法识别")
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(stored, consoleSecretPrefix))
	if err != nil {
		return "", err
	}
	aead, err := consoleSecretAEAD()
	if err != nil {
		return "", err
	}
	if len(raw) < aead.NonceSize() {
		return "", errors.New("控制台密码的存储内容不完整")
	}
	nonce, ciphertext := raw[:aead.NonceSize()], raw[aead.NonceSize():]
	plain, err := aead.Open(nil, nonce, ciphertext, consoleSecretAAD(connID))
	if err != nil {
		return "", fmt.Errorf("控制台密码解密失败（部署密钥可能已更换）: %w", err)
	}
	return string(plain), nil
}

// consoleSecretAAD 让密文与所属设置行绑定，防止整行密文被搬到别的连接下复用。
func consoleSecretAAD(connID string) []byte {
	return []byte("kbconsole:" + strings.TrimSpace(connID))
}

// redactSecrets 去掉文本里出现的敏感原文。上游可能把提交的密码回显在错误
// body 里，任何要返回给客户端或写日志的文本都必须先过这一层。
func redactSecrets(text string, secrets ...string) string {
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		text = strings.ReplaceAll(text, secret, "***")
	}
	return text
}
