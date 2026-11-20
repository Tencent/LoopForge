package repo

import (
	"crypto/sha256"
	"encoding/hex"
)

// hexSHA256 returns the lowercase hex SHA-256 digest of value.
func hexSHA256(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
