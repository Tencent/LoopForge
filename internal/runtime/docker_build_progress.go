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
	buildKitStep = regexp.MustCompile(`^\[(?:([a-zA-Z0-9._/-]+(?: [a-zA-Z0-9._-]+)?) )?([0-9]{1,6}/[0-9]{1,6})\] ([A-Z]+)(?: |$)`)
	legacyStep   = regexp.MustCompile(`^Step ([0-9]{1,6}/[0-9]{1,6}) : ([A-Z]+)(?: |$)`)
)

// Only recognize Docker's own step headers. RUN output and instruction
// arguments can contain credentials and are never copied into chat.
func dockerBuildStep(line string) string {
	if step := legacyStep.FindStringSubmatch(line); step != nil {
		return dockerInstructionStep("", step[1], step[2])
	}
	header := buildKitLine.FindStringIndex(line)
	if header == nil {
		return ""
	}
	line = line[header[1]:]
	if step := buildKitStep.FindStringSubmatch(line); step != nil {
		return dockerInstructionStep(step[1], step[2], step[3])
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

func dockerInstructionStep(stage, step, instruction string) string {
	if stage != "" {
		if instruction == "FROM" {
			return fmt.Sprintf("准备 Dockerfile 阶段 %s 的基础镜像", stage)
		}
		return fmt.Sprintf("执行 Dockerfile 阶段 %s 的第 %s 步", stage, step)
	}
	if instruction == "FROM" {
		return "准备基础镜像"
	}
	return fmt.Sprintf("执行 Dockerfile 第 %s 步", step)
}
