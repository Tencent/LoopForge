package runtime

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"agentflow/internal/domain"
)

type dockerBuildProgressKey struct{}

func WithDockerBuildProgress(ctx context.Context, report func(domain.WorkspaceImageBuildProgress)) context.Context {
	return context.WithValue(ctx, dockerBuildProgressKey{}, report)
}

func DockerBuildProgressObserver(ctx context.Context) func(domain.WorkspaceImageBuildProgress) {
	report, _ := ctx.Value(dockerBuildProgressKey{}).(func(domain.WorkspaceImageBuildProgress))
	return report
}

var (
	buildKitLine = regexp.MustCompile(`^#[0-9]+ `)
	buildKitStep = regexp.MustCompile(`^\[(?:[^\[\]]+ )?([0-9]{1,6})/([0-9]{1,6})\] ([A-Z]+)(?: |$)`)
	legacyStep   = regexp.MustCompile(`^Step ([0-9]{1,6})/([0-9]{1,6}) : ([A-Z]+)(?: |$)`)
)

// Only recognize Docker's own step headers. RUN output and instruction
// arguments can contain credentials and are never copied into chat.
func dockerBuildStep(line string) string {
	if step := legacyStep.FindStringSubmatch(line); step != nil {
		return dockerInstructionStep(step)
	}
	header := buildKitLine.FindStringIndex(line)
	if header == nil {
		return ""
	}
	line = line[header[1]:]
	if step := buildKitStep.FindStringSubmatch(line); step != nil {
		return dockerInstructionStep(step)
	}
	switch {
	case strings.HasPrefix(line, "[internal] load build definition "):
		return "读取 Dockerfile"
	case strings.HasPrefix(line, "[internal] load metadata for "):
		return "读取基础镜像信息"
	case strings.HasPrefix(line, "[internal] load build context"):
		return "传输构建上下文"
	case strings.HasPrefix(line, "exporting to "):
		return "导出镜像"
	}
	return ""
}

func dockerInstructionStep(step []string) string {
	if step[3] == "FROM" {
		return "准备基础镜像"
	}
	return fmt.Sprintf("执行 Dockerfile 第 %s/%s 步", step[1], step[2])
}
