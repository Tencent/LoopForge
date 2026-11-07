package runtime

import (
	"strings"
)

func safeID(v string) string {
	v = strings.ToLower(v)
	var b strings.Builder
	for _, r := range v {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' {
			b.WriteRune(r)
		}
	}
	if b.Len() > 48 {
		return b.String()[:48]
	}
	return b.String()
}
