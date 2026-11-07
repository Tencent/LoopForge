package domain

import "strings"

const (
	SOPLaneProduct  = "product"
	SOPLaneFrontend = "frontend"
	SOPLaneBackend  = "backend"
	SOPLaneDesign   = "design"
	SOPLaneRepair   = "repair"
	SOPLaneOther    = "other"
)

// RequirementLaneOrder is the display order for a requirement's role chains.
var RequirementLaneOrder = []string{SOPLaneProduct, SOPLaneFrontend, SOPLaneBackend}

func NormalizeSOPLane(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case SOPLaneProduct, "产品", "prd":
		return SOPLaneProduct
	case SOPLaneFrontend, "front", "前端":
		return SOPLaneFrontend
	case SOPLaneBackend, "back", "后端":
		return SOPLaneBackend
	case SOPLaneDesign, "设计", "uiux":
		return SOPLaneDesign
	case SOPLaneRepair, "fix", "issue", "bugfix", "修复", "缺陷":
		return SOPLaneRepair
	case SOPLaneOther:
		return SOPLaneOther
	default:
		return SOPLaneOther
	}
}

func SOPLaneLabel(lane string) string {
	switch NormalizeSOPLane(lane) {
	case SOPLaneProduct:
		return "产品"
	case SOPLaneFrontend:
		return "前端"
	case SOPLaneBackend:
		return "后端"
	case SOPLaneDesign:
		return "设计"
	case SOPLaneRepair:
		return "修复"
	default:
		return "其他"
	}
}

// InferSOPLane prefers the explicit field, then ID / name / generic-source tags.
func InferSOPLane(wf WorkflowDefinition) string {
	if strings.TrimSpace(wf.SOPLane) != "" {
		return NormalizeSOPLane(wf.SOPLane)
	}
	parts := []string{wf.ID, wf.Name}
	parts = append(parts, wf.Tags...)
	hay := strings.ToLower(strings.Join(parts, " "))
	switch {
	case strings.Contains(hay, "generic-product") || strings.Contains(hay, "产品交付") ||
		(strings.Contains(hay, "product") && (strings.Contains(hay, "delivery") || strings.Contains(hay, "prd"))):
		return SOPLaneProduct
	case strings.Contains(hay, "generic-frontend") || strings.Contains(hay, "前端交付") ||
		(strings.Contains(hay, "frontend") && strings.Contains(hay, "delivery")):
		return SOPLaneFrontend
	case strings.Contains(hay, "generic-design") || strings.Contains(hay, "设计交付") ||
		(strings.Contains(hay, "设计") && !strings.Contains(hay, "需求开发")):
		return SOPLaneDesign
	case strings.Contains(hay, "generic-backend") || strings.Contains(hay, "后端交付") ||
		strings.Contains(hay, "requirement-development") || strings.Contains(hay, "loopforge") ||
		strings.Contains(hay, "需求开发"):
		return SOPLaneBackend
	case strings.Contains(hay, "issue-fix") || strings.Contains(hay, "缺陷修复") ||
		(strings.Contains(hay, "修复") && !strings.Contains(hay, "交付")):
		return SOPLaneRepair
	}
	return SOPLaneOther
}

func IsRequirementWorkItem(issue Issue) bool {
	kind := strings.ToLower(strings.TrimSpace(issue.Kind))
	source := strings.ToLower(strings.TrimSpace(issue.Source))
	if source == "gongfeng" || kind == "issue" {
		return false
	}
	return kind == "requirement" || source == "tapd" || source == "local" || source == ""
}
