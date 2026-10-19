package preview

import (
	"errors"
	"fmt"
	"path"
	"strings"
	"unicode/utf8"
)

var (
	ErrUnsafePath       = errors.New("preview path is outside the run workspace")
	ErrUnsupportedEntry = errors.New("preview archive rejected a non-regular file")
	ErrMissingIndex     = errors.New("preview source is missing the entry file")
	ErrLimitExceeded    = errors.New("preview archive exceeded size or file limits")
	ErrInvalidKind      = errors.New("preview kind is not supported")
	ErrPreviewBusy      = errors.New("preview archive already in progress")
	ErrPreviewGone      = errors.New("preview is no longer archiving")
	ErrPreviewNotFound  = errors.New("preview not found")
)

const (
	HTTPPathPrefix = "/preview/"
	tokenHexLen    = 32
)

func PreviewBasePath(token string) string {
	return strings.TrimSuffix(HTTPPathPrefix, "/") + "/" + token
}

func validSessionToken(token string) bool {
	if len(token) != tokenHexLen {
		return false
	}
	for i := 0; i < len(token); i++ {
		c := token[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// ParsePreviewPath extracts the session token and archive-relative path from
// /preview/{token}/...
func ParsePreviewPath(raw string) (token, rel string, ok bool) {
	cleaned := path.Clean("/" + strings.TrimSpace(raw))
	rest, found := strings.CutPrefix(cleaned, strings.TrimSuffix(HTTPPathPrefix, "/")+"/")
	if !found {
		return "", "", false
	}
	token, remainder, _ := strings.Cut(rest, "/")
	if !validSessionToken(token) {
		return "", "", false
	}
	return token, remainder, true
}

func SanitizeRelPath(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", fmt.Errorf("%w: empty path", ErrUnsafePath)
	}
	if strings.ContainsRune(value, 0) || !utf8.ValidString(value) {
		return "", fmt.Errorf("%w: invalid encoding", ErrUnsafePath)
	}
	if strings.Contains(value, `\`) {
		return "", fmt.Errorf("%w: backslash is not allowed", ErrUnsafePath)
	}
	replaced := strings.NewReplacer("%2e", ".", "%2E", ".", "%2f", "/", "%2F", "/", "%5c", "/", "%5C", "/").Replace(value)
	if replaced != value {
		return "", fmt.Errorf("%w: encoded path traversal", ErrUnsafePath)
	}
	if path.IsAbs(value) || strings.HasPrefix(value, "/") {
		return "", fmt.Errorf("%w: absolute path", ErrUnsafePath)
	}
	for _, part := range strings.Split(value, "/") {
		if part == ".." {
			return "", fmt.Errorf("%w: %s", ErrUnsafePath, raw)
		}
	}
	clean := path.Clean(value)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("%w: %s", ErrUnsafePath, raw)
	}
	for _, part := range strings.Split(clean, "/") {
		if part == "" || part == "." || part == ".." {
			return "", fmt.Errorf("%w: %s", ErrUnsafePath, raw)
		}
	}
	return clean, nil
}

func SanitizeArchiveMember(name string) (string, error) {
	name = strings.TrimSpace(strings.ReplaceAll(name, `\`, "/"))
	name = strings.TrimPrefix(name, "./")
	if name == "" || strings.HasSuffix(name, "/") {
		return "", fmt.Errorf("%w: empty member", ErrUnsafePath)
	}
	if strings.ContainsRune(name, 0) {
		return "", fmt.Errorf("%w: NUL in member", ErrUnsafePath)
	}
	if path.IsAbs(name) || strings.HasPrefix(name, "/") {
		return "", fmt.Errorf("%w: absolute member", ErrUnsafePath)
	}
	clean := path.Clean(name)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("%w: %s", ErrUnsafePath, name)
	}
	return clean, nil
}
