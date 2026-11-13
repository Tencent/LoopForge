package httpapi

import "agentflow/internal/repo"

// 目录资产的反向引用查找。catalogHandlers 的删除校验与 assistantHandlers 的
// 删除审批预警共用同一口径，因此放在无 receiver 的包级 helper 中。

// agentNamesReferencing 返回引用了指定规则（kind="rule"）或技能（kind="skill"）的角色名，
// 同一角色重复引用只计一次。
func agentNamesReferencing(st *repo.Store, kind, id string) []string {
	var names []string
	for _, agent := range st.Agents() {
		refs := agent.Rules
		if kind == "skill" {
			refs = agent.Skills
		}
		for _, ref := range refs {
			if ref == id {
				names = append(names, agent.Name)
				break
			}
		}
	}
	return names
}

// sandboxNamesReferencing 返回引用了指定 Dockerfile（kind="dockerfile"）或
// 环境安装脚本（kind="environment"）的沙箱名。
func sandboxNamesReferencing(st *repo.Store, kind, id string) []string {
	var names []string
	for _, sandbox := range st.Sandboxes() {
		if (kind == "dockerfile" && sandbox.DockerfileID == id) || (kind == "environment" && sandbox.EnvironmentID == id) {
			names = append(names, sandbox.Name)
		}
	}
	return names
}

// workflowNamesReferencingSandbox 返回使用指定沙箱的流程名。
func workflowNamesReferencingSandbox(st *repo.Store, id string) []string {
	var names []string
	for _, workflow := range st.Workflows() {
		if workflow.SandboxProfileID == id {
			names = append(names, workflow.Name)
		}
	}
	return names
}
