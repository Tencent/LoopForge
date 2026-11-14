package httpapi

import "agentflow/internal/repo"

// 目录资产的引用方查询。删除前校验（catalogHandlers）与助手草稿告警
// （assistantHandlers）共用同一套引用规则，因此放在包级 helper 中。

// agentNamesReferencing 返回在 Rules（kind="rule"）或 Skills（kind="skill"）
// 中引用了 id 的角色名称。
func agentNamesReferencing(store *repo.Store, kind, id string) []string {
	var names []string
	for _, agent := range store.Agents() {
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

// sandboxNamesReferencing 返回引用了 Dockerfile（kind="dockerfile"）或环境安装
// 脚本（kind="environment"）id 的沙箱名称。
func sandboxNamesReferencing(store *repo.Store, kind, id string) []string {
	var names []string
	for _, sandbox := range store.Sandboxes() {
		if (kind == "dockerfile" && sandbox.DockerfileID == id) || (kind == "environment" && sandbox.EnvironmentID == id) {
			names = append(names, sandbox.Name)
		}
	}
	return names
}

// workflowNamesReferencingSandbox 返回使用沙箱 id 的流程名称。
func workflowNamesReferencingSandbox(store *repo.Store, id string) []string {
	var names []string
	for _, workflow := range store.Workflows() {
		if workflow.SandboxProfileID == id {
			names = append(names, workflow.Name)
		}
	}
	return names
}
