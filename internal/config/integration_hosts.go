package config

import (
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

var integrationHostname = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9.-]*[a-z0-9])?$`)

func integrationHost(raw string) string {
	value := strings.ToLower(strings.TrimSpace(raw))
	parsed, err := url.Parse("https://" + value)
	if err != nil || parsed.Host != value || parsed.User != nil || parsed.Path != "" ||
		!integrationHostname.MatchString(parsed.Hostname()) || strings.Contains(parsed.Hostname(), "..") {
		return ""
	}
	if port := parsed.Port(); port != "" {
		if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
			return ""
		}
	} else if strings.Contains(value, ":") {
		return ""
	}
	return parsed.Host
}

// GongfengHost returns the explicitly configured managed Git host.
func GongfengHost() string {
	return integrationHost(env("AGENTFLOW_GONGFENG_HOST"))
}

func TAPDWebHost() string {
	if host := integrationHost(env("AGENTFLOW_TAPD_HOST")); host != "" {
		return host
	}
	return "www.tapd.cn"
}
