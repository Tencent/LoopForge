package httpapi

import (
	"context"
	"errors"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const assistantMinDSHVersion = "0.2.2-rc.1"

var (
	errAssistantDSHVersionUnsupported = errors.New("unsupported DeepSeek Harness version")
	dshVersionPattern                 = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)(?:-rc\.(\d+))?$`)
)

type assistantDSHVersionError struct {
	version string
}

func (e assistantDSHVersionError) Error() string {
	if e.version != "" {
		return "DeepSeek Harness 版本过旧（当前 " + e.version + "），聊天需要 " + assistantMinDSHVersion + " 或更高版本。请升级服务器上的 dsh 后重试。"
	}
	return "无法确认 DeepSeek Harness 版本；聊天需要 " + assistantMinDSHVersion + " 或更高版本。请检查并升级服务器上的 dsh 后重试。"
}

func (e assistantDSHVersionError) Is(target error) bool {
	return target == errAssistantDSHVersionUnsupported
}

type dshVersion struct {
	major, minor, patch int
	rc                  int
	prerelease          bool
}

func parseDSHVersion(output string) (dshVersion, string, bool) {
	for _, field := range strings.Fields(output) {
		candidate := strings.Trim(field, ",()[]")
		parts := dshVersionPattern.FindStringSubmatch(candidate)
		if parts == nil {
			continue
		}
		values := [4]int{}
		for i := 1; i < len(parts); i++ {
			if parts[i] == "" {
				continue
			}
			value, err := strconv.Atoi(parts[i])
			if err != nil {
				return dshVersion{}, "", false
			}
			values[i-1] = value
		}
		version := dshVersion{major: values[0], minor: values[1], patch: values[2], rc: values[3], prerelease: parts[4] != ""}
		return version, candidate, true
	}
	return dshVersion{}, "", false
}

func dshVersionAtLeast(version, minimum dshVersion) bool {
	left := [...]int{version.major, version.minor, version.patch}
	right := [...]int{minimum.major, minimum.minor, minimum.patch}
	for i := range left {
		if left[i] != right[i] {
			return left[i] > right[i]
		}
	}
	if version.prerelease != minimum.prerelease {
		return !version.prerelease
	}
	if version.prerelease {
		return version.rc >= minimum.rc
	}
	return true
}

func ensureAssistantDSHVersion(ctx context.Context, bin string) error {
	minimum, _, _ := parseDSHVersion(assistantMinDSHVersion)
	versionCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	output, err := exec.CommandContext(versionCtx, bin, "--version").Output()
	if err != nil {
		return assistantDSHVersionError{}
	}
	version, rendered, ok := parseDSHVersion(string(output))
	if !ok || !dshVersionAtLeast(version, minimum) {
		return assistantDSHVersionError{version: rendered}
	}
	return nil
}
