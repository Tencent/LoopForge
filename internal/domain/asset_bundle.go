package domain

const ProjectAssetBundleFormat = "loopforge.project-assets/v1"

type BundleAssetRef struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

type BundleAssetFile struct {
	Path    string `json:"path"`
	Content []byte `json:"content"`
	Mode    uint32 `json:"mode"`
	SHA256  string `json:"sha256"`
}

type BundleSkill struct {
	SkillDefinition
	Attachments []BundleAssetFile `json:"attachments,omitempty"`
}

type BundleMCPServer struct {
	MCPServerDefinition
	Attachments []BundleAssetFile `json:"attachments,omitempty"`
}

type ProjectAssetBundle struct {
	Format     string               `json:"format"`
	Skills     []BundleSkill        `json:"skills,omitempty"`
	MCPServers []BundleMCPServer    `json:"mcpServers,omitempty"`
	Agents     []AgentDefinition    `json:"agents,omitempty"`
	Workflows  []WorkflowDefinition `json:"workflows,omitempty"`
	Rules      []RuleDefinition     `json:"rules,omitempty"`
	Sandboxes  []SandboxProfile     `json:"sandboxes,omitempty"`
	Bindings   []BundleBinding      `json:"bindings,omitempty"`
}

type BundleBinding struct {
	Kind    string `json:"kind"`
	AssetID string `json:"assetId"`
}

type BundleAssetSummary struct {
	BundleAssetRef
	Name     string `json:"name"`
	TargetID string `json:"targetId,omitempty"`
}
