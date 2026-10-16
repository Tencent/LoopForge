package domain

import (
	"fmt"
	"strings"
)

// StripPreviewBuild removes the synthetic CodeBuddy preview node so the SOP
// catalog never persists an engine-injected step.
func StripPreviewBuild(wf WorkflowDefinition) WorkflowDefinition {
	nodes := make([]WorkflowNode, 0, len(wf.Nodes))
	for _, node := range wf.Nodes {
		if IsPreviewBuildNode(node.ID) {
			continue
		}
		nodes = append(nodes, node)
	}
	edges := make([]WorkflowEdge, 0, len(wf.Edges))
	for _, edge := range wf.Edges {
		if IsPreviewBuildNode(edge.Source) || IsPreviewBuildNode(edge.Target) {
			continue
		}
		edges = append(edges, edge)
	}
	wf.Nodes = nodes
	wf.Edges = edges
	return wf
}

// AttachPreviewBuild guarantees the preview build node exists when the
// contract is enabled. A user-placed node keeps its own position and edges
// (the canvas allows inserting the build anywhere in the flow); only when the
// node is missing is it appended after every current success sink.
func AttachPreviewBuild(wf WorkflowDefinition) (WorkflowDefinition, error) {
	if err := wf.Preview.Validate(); err != nil {
		return wf, err
	}
	if !wf.Preview.Static() {
		return StripPreviewBuild(wf), nil
	}
	for _, node := range wf.Nodes {
		if IsPreviewBuildNode(node.ID) {
			return wf, nil
		}
	}
	node := PreviewBuildNode(wf)
	sinks := previewSuccessSinks(wf)
	wf.Nodes = append(wf.Nodes, node)
	for i, source := range sinks {
		wf.Edges = append(wf.Edges, WorkflowEdge{
			ID:     fmt.Sprintf("%s-%d", PreviewBuildEdgeID, i+1),
			Source: source,
			Target: node.ID,
			When:   "success",
		})
	}
	return wf, nil
}

func PreviewBuildNode(wf WorkflowDefinition) WorkflowNode {
	contract := wf.Preview
	x, y := previewBuildPosition(wf.Nodes)
	return WorkflowNode{
		ID:              PreviewBuildNodeID,
		Name:            "构建静态站点",
		Type:            "agent",
		AgentID:         PreviewBuildAgentID,
		ExecutorID:      PreviewBuildExecutor,
		ExecutionScope:  ExecutionScopeRun,
		ArtifactOutputs: []ArtifactContract{PreviewBuildArtifact(contract)},
		OnFailure:       "fail",
		RetryStrategy:   "targeted_fix",
		CarryOver:       []string{"last_error"},
		MaxAttempts:     2,
		MaxReworks:      1,
		Position:        Position{X: x, Y: y},
	}
}

func PreviewBuildAgent(contract *PreviewContract) AgentDefinition {
	contract.Normalize()
	return AgentDefinition{
		ID:              PreviewBuildAgentID,
		Name:            "构建静态站点",
		Description:     "系统步骤：用 CodeBuddy 产出可托管的静态站点，再由预览收尾归档。",
		ExecutorID:      PreviewBuildExecutor,
		Model:           contract.ModelOrDefault(),
		TaskPrompt:      PreviewBuildTaskPrompt(contract),
		ArtifactOutputs: []ArtifactContract{PreviewBuildArtifact(contract)},
		Loop: AgentLoopPolicy{
			Enabled:       true,
			MaxIterations: 2,
			ExitCondition: "all_gates_pass",
			OnExhausted:   "fail",
		},
	}
}

func PreviewBuildArtifact(contract *PreviewContract) ArtifactContract {
	return ArtifactContract{
		Name:     "frontend-preview",
		Path:     contract.EntryRel(),
		Required: true,
	}
}

func PreviewBuildTaskPrompt(contract *PreviewContract) *TaskPromptSpec {
	source := contract.SourceOrDist()
	index := contract.Index()
	entry := contract.EntryRel()
	return &TaskPromptSpec{
		Role:      "前端构建工程师",
		Objective: "把当前工作区里的前端应用打成可独立托管的静态站点，不要让用户再手写构建脚本。",
		Context:   "这是 AgentFlow 在 SOP 勾选「前端预览」后自动追加的 CodeBuddy 步骤。工作区可能包含一个或多个代码仓库子目录。构建完成后平台会打包静态目录并删除工作区。",
		Inputs: []string{
			"工作区根目录及其中的仓库检出",
			"上游节点已经完成的实现与测试结果",
		},
		Actions: []string{
			"识别前端工程（package.json、vite、webpack、rspack、next 静态导出等）",
			"若工作区没有前端工程（无 Vite/webpack/Next 等配置、无前端依赖的 package.json、无 index.html），不要另起项目或手写预览页，不要创建 `" + source + "/" + index + "`，直接说明原因并结束",
			"安装依赖并执行生产构建，不要启动开发服务器",
			"生产构建必须使用相对资源根：Vite 设 `base: './'`，webpack/rspack 设 `publicPath: 'auto'` 或 `'./'`，禁止 `base: '/'` 或把资源写死成网站根路径",
			"不要依赖开发服务器；预览打开时用户可配置后端地址，网关会拦截 fetch/XHR 转到该地址，不要为了预览另写一套绝对 API",
			"把最终可浏览的静态文件放到工作区根目录 `" + source + "/`，入口文件必须是 `" + index + "`",
			"若产物落在仓库子目录内，复制或同步到工作区根的 `" + source + "/`",
			"不要修改 git remote，不要提交无关文件；构建失败时根据日志修正后重试",
		},
		Deliverables: []string{
			"静态站点入口 `" + entry + "` 必须存在，且同目录包含页面引用的 JS/CSS/资源",
		},
		AcceptanceCriteria: []string{
			"`" + entry + "` 可被直接打开",
			"入口和 chunk 的资源地址必须是相对路径（`./assets/...`），不能依赖网站根 `/assets/`",
			"不要留下需要 `npm run dev` 才能访问的开发服务器",
		},
		Constraints: []string{
			"只使用现有仓库，不要另起无关项目",
			"禁止在非前端仓库里编造静态站点来满足产物契约",
			"不要把源码、node_modules 或 .git 拷进 " + source,
		},
		RetryInstructions: "只修复构建失败项，确保 `" + entry + "` 最终可访问。",
	}
}

func previewSuccessSinks(wf WorkflowDefinition) []string {
	outgoing := map[string]bool{}
	for _, edge := range wf.Edges {
		if strings.TrimSpace(edge.When) != "" && !strings.EqualFold(edge.When, "success") {
			continue
		}
		outgoing[edge.Source] = true
	}
	sinks := make([]string, 0, len(wf.Nodes))
	for _, node := range wf.Nodes {
		if outgoing[node.ID] {
			continue
		}
		sinks = append(sinks, node.ID)
	}
	return sinks
}

func previewBuildPosition(nodes []WorkflowNode) (float64, float64) {
	if len(nodes) == 0 {
		return 110, 120
	}
	maxX, maxY := nodes[0].Position.X, nodes[0].Position.Y
	for _, node := range nodes[1:] {
		if node.Position.X > maxX {
			maxX = node.Position.X
		}
		if node.Position.Y > maxY {
			maxY = node.Position.Y
		}
	}
	return maxX + 280, maxY
}
