package verification

import (
	"crypto/sha256"
	"encoding/hex"
	"runtime"
	"sort"
)

func sha256Hex(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func isGitTreeHash(value string) bool {
	if len(value) != 40 {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' && r < 'a' || r > 'f' {
			return false
		}
	}
	return true
}

type fileEntry struct{ path, hash string }

func sortFileEntries(entries []fileEntry) {
	sort.Slice(entries, func(i, j int) bool { return entries[i].path < entries[j].path })
}

func goVersion() string { return runtime.Version() }
func goOS() string      { return runtime.GOOS }
func goArch() string    { return runtime.GOARCH }
