package httpapi

import (
	"fmt"
	"strings"

	"agentflow/internal/domain"
)

// issueClarificationDimensions rates bug-style work items. Requirements are
// rated against the project's configurable policy instead, because that policy
// also drives the pre-existing SOP gate.
var issueClarificationDimensions = []domain.ClarificationDimension{
	{ID: "phenomenon", Name: "现象与复现", Question: "具体现象是什么，如何稳定复现？", Weight: 25},
	{ID: "impact", Name: "影响范围", Question: "影响哪些模块、用户或环境？", Weight: 20},
	{ID: "expected", Name: "期望行为", Question: "修复后正确的行为是什么？", Weight: 25},
	{ID: "acceptance", Name: "验收标准", Question: "如何用可测试的条件判断已修复？", Weight: 20},
	{ID: "constraints", Name: "约束与依赖", Question: "有哪些技术约束、依赖或时间要求？", Weight: 10},
}

// clarificationRatingDimensions returns the dimensions used to rate how complete
// a work item's clarification is.
func clarificationRatingDimensions(issue domain.Issue, project domain.Project, projectFound bool) []domain.ClarificationDimension {
	if isBugStyleWorkItem(issue) {
		return issueClarificationDimensions
	}
	if projectFound {
		return domain.EffectiveClarificationPolicy(project.Clarification).Dimensions
	}
	return domain.DefaultClarificationPolicy().Dimensions
}

func isBugStyleWorkItem(issue domain.Issue) bool {
	return strings.EqualFold(issue.Source, "gongfeng") || strings.EqualFold(issue.Kind, "issue")
}

func scoreClarificationDimensions(dimensions []domain.ClarificationDimension, ratings map[string]int) (int, map[string]int) {
	return domain.ScoreClarificationDimensions(dimensions, ratings)
}

func trimClarificationMissing(missing []string) []string {
	return domain.TrimClarificationMissing(missing)
}

// clarificationRatingPrompt describes the rating contract appended to the
// clarification prompt once the model believes it has enough information.
func clarificationRatingPrompt(dimensions []domain.ClarificationDimension) string {
	if len(dimensions) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n每轮都必须返回 assessment：对以下维度各打 0-100 分（依据是当前描述加上对话里已确认的信息，没有依据就给低分，不要一律给满分）。ready=false 时也要打分，并在 missing 里列出还缺的关键信息，方便用户判断还要不要继续问。只有目标、范围、验收标准都已经明确到可以开工时，才把 ready 设为 true：\n")
	for _, dimension := range dimensions {
		b.WriteString(fmt.Sprintf("- %s（%s）：%s\n", dimension.ID, dimension.Name, dimension.Question))
	}
	b.WriteString(`格式："assessment":{"dimensions":{"` + dimensions[0].ID + `":80},"missing":["仍然没有确认的关键信息，每条一句话"]}` + "\n")
	return b.String()
}
