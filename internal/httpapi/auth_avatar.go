package httpapi

import (
	"fmt"
	"hash/fnv"
	"strings"
)

var avatarColors = []string{"#2563eb", "#7c3aed", "#db2777", "#ea580c", "#16a34a", "#0891b2", "#4f46e5", "#ca8a04"}

// initialsAvatar renders a local avatar for deployments without a photo
// service. name must already be sanitized to [A-Za-z0-9_.-].
func initialsAvatar(name string) []byte {
	h := fnv.New32a()
	h.Write([]byte(name))
	color := avatarColors[h.Sum32()%uint32(len(avatarColors))]
	letter := strings.ToUpper(name[:1])
	return []byte(fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 100 100"><rect width="100" height="100" fill="%s"/><text x="50" y="50" dy=".35em" text-anchor="middle" font-family="system-ui,sans-serif" font-size="48" font-weight="600" fill="#fff">%s</text></svg>`, color, letter))
}
