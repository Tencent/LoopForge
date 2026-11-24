// Package credcrypt seals Bot credentials and authorization-bearing reply
// routes with AES-256-GCM. Keys come from AGENTFLOW_CREDENTIAL_KEYS:
//
//	kid:base64(32 bytes)[,kid:base64(32 bytes)...]
//
// The first key encrypts new values; every listed key can decrypt. Sealed
// values are "lf1:<kid>:<base64(nonce||ciphertext)>". Additional data binds a
// value to its owner (configuration, channel, purpose) so a ciphertext copied
// to another row fails to open.
package credcrypt

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
)

const (
	// EnvKeys is the environment variable holding the keyring.
	EnvKeys = "AGENTFLOW_CREDENTIAL_KEYS"
	prefix  = "lf1:"
)

var (
	// ErrKeyMissing means no key is configured for writing, or the key named
	// by a sealed value is not in the keyring.
	ErrKeyMissing = errors.New("credential_key_missing")
	// ErrDecrypt means a sealed value failed authentication.
	ErrDecrypt = errors.New("credential_decrypt_failed")

	kidPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)
)

// Keyring holds the active write key and all decryption keys.
type Keyring struct {
	current string
	keys    map[string]cipher.AEAD
}

// Parse builds a keyring from the environment format. Empty input returns a
// nil keyring, which can still read legacy plaintext but cannot seal.
func Parse(raw string) (*Keyring, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	k := &Keyring{keys: map[string]cipher.AEAD{}}
	for i, part := range strings.Split(raw, ",") {
		kid, encoded, ok := strings.Cut(strings.TrimSpace(part), ":")
		if !ok || !kidPattern.MatchString(kid) {
			return nil, fmt.Errorf("%s: entry %d must be kid:base64key with kid [A-Za-z0-9_-]{1,32}", EnvKeys, i+1)
		}
		if _, dup := k.keys[kid]; dup {
			return nil, fmt.Errorf("%s: duplicate kid %q", EnvKeys, kid)
		}
		key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
		if err != nil || len(key) != 32 {
			return nil, fmt.Errorf("%s: key %q must be base64 of exactly 32 bytes", EnvKeys, kid)
		}
		block, err := aes.NewCipher(key)
		if err != nil {
			return nil, err
		}
		aead, err := cipher.NewGCM(block)
		if err != nil {
			return nil, err
		}
		k.keys[kid] = aead
		if i == 0 {
			k.current = kid
		}
	}
	return k, nil
}

// FromEnv parses AGENTFLOW_CREDENTIAL_KEYS.
func FromEnv() (*Keyring, error) { return Parse(os.Getenv(EnvKeys)) }

// CurrentKID returns the write key id, or "" for a nil keyring.
func (k *Keyring) CurrentKID() string {
	if k == nil {
		return ""
	}
	return k.current
}

// IsSealed reports whether a stored value uses the sealed format. Values with
// the prefix must always be opened as ciphertext, never treated as plaintext.
func IsSealed(value string) bool { return strings.HasPrefix(value, prefix) }

// KID returns the key id of a sealed value, or "" when it is not sealed.
func KID(value string) string {
	if !IsSealed(value) {
		return ""
	}
	kid, _, _ := strings.Cut(strings.TrimPrefix(value, prefix), ":")
	return kid
}

// Seal encrypts plaintext with the current key and binds it to aad.
func (k *Keyring) Seal(plaintext []byte, aad string) (string, error) {
	if k == nil || k.current == "" {
		return "", ErrKeyMissing
	}
	aead := k.keys[k.current]
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := aead.Seal(nonce, nonce, plaintext, []byte(aad))
	return prefix + k.current + ":" + base64.StdEncoding.EncodeToString(sealed), nil
}

// Open decrypts a sealed value. It fails closed on unknown keys, malformed
// values and authentication failures.
func (k *Keyring) Open(value, aad string) ([]byte, error) {
	if !IsSealed(value) {
		return nil, ErrDecrypt
	}
	kid, encoded, ok := strings.Cut(strings.TrimPrefix(value, prefix), ":")
	if !ok {
		return nil, ErrDecrypt
	}
	if k == nil {
		return nil, ErrKeyMissing
	}
	aead, found := k.keys[kid]
	if !found {
		return nil, ErrKeyMissing
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(raw) < aead.NonceSize()+aead.Overhead() {
		return nil, ErrDecrypt
	}
	plain, err := aead.Open(nil, raw[:aead.NonceSize()], raw[aead.NonceSize():], []byte(aad))
	if err != nil {
		return nil, ErrDecrypt
	}
	return plain, nil
}

// NeedsRewrap reports whether a stored value should be re-encrypted: legacy
// plaintext, or a sealed value under a key other than the current one.
func (k *Keyring) NeedsRewrap(value string) bool {
	if !IsSealed(value) {
		return value != ""
	}
	return KID(value) != k.CurrentKID()
}
