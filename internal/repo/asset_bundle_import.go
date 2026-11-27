package repo

import (
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"path"
	"regexp"
	"strings"

	"agentflow/internal/domain"

	"gorm.io/gorm"
)

var bundleIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
var bundleSkillPath = regexp.MustCompile(`\.agentflow/(?:[A-Za-z0-9._-]+/)?skills/[A-Za-z0-9._-]+`)
var bundleSkillConstant = regexp.MustCompile(`(?m)^[A-Z0-9_]*SKILL(?:_ID)?[\t ]*=[\t ]*["'][A-Za-z0-9._-]+["']`)

type bundleImport struct {
	target   string
	ids      map[domain.BundleAssetRef]string
	summary  []domain.BundleAssetSummary
	fileSize int64
}

func (r *CatalogRepo) PreviewProjectAssetBundle(project string, bundle domain.ProjectAssetBundle) ([]domain.BundleAssetSummary, error) {
	plan, err := r.prepareProjectAssetBundle(project, bundle, "preview")
	if err != nil {
		return nil, err
	}
	for i := range plan.summary {
		plan.summary[i].TargetID = ""
	}
	return plan.summary, nil
}

func (r *CatalogRepo) ImportProjectAssetBundle(project string, bundle domain.ProjectAssetBundle) ([]domain.BundleAssetSummary, error) {
	var nonce [12]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	plan, err := r.prepareProjectAssetBundle(project, bundle, fmt.Sprintf("bundle-%x", nonce))
	if err != nil {
		return nil, err
	}
	return plan.summary, r.DB.Transaction(func(tx *gorm.DB) error {
		catalog := *r
		catalog.deps = &deps{DB: tx}
		return plan.save(&catalog, bundle)
	})
}

func (r *CatalogRepo) prepareProjectAssetBundle(project string, b domain.ProjectAssetBundle, prefix string) (*bundleImport, error) {
	if b.Format != domain.ProjectAssetBundleFormat {
		return nil, fmt.Errorf("不支持的资产包格式")
	}
	p := &bundleImport{target: project, ids: make(map[domain.BundleAssetRef]string)}
	for _, groups := range [][]domain.BundleAssetSummary{
		bundleSummaries("skill", b.Skills, func(v domain.BundleSkill) (string, string) { return v.ID, v.Name }),
		bundleSummaries("mcp", b.MCPServers, func(v domain.BundleMCPServer) (string, string) { return v.ID, v.Name }),
		bundleSummaries("agent", b.Agents, func(v domain.AgentDefinition) (string, string) { return v.ID, v.Name }),
		bundleSummaries("sop", b.Workflows, func(v domain.WorkflowDefinition) (string, string) { return v.ID, v.Name }),
		bundleSummaries("rule", b.Rules, func(v domain.RuleDefinition) (string, string) { return v.ID, v.Name }),
		bundleSummaries("sandbox", b.Sandboxes, func(v domain.SandboxProfile) (string, string) { return v.ID, v.Name }),
	} {
		for _, summary := range groups {
			if !bundleIDPattern.MatchString(summary.ID) || strings.TrimSpace(summary.Name) == "" {
				return nil, fmt.Errorf("%s 需要合法的标识和名称", summary.Kind)
			}
			if _, exists := p.ids[summary.BundleAssetRef]; exists {
				return nil, fmt.Errorf("资产包重复声明 %s/%s", summary.Kind, summary.ID)
			}
			summary.TargetID = fmt.Sprintf("%s-%d", prefix, len(p.summary)+1)
			p.ids[summary.BundleAssetRef] = summary.TargetID
			p.summary = append(p.summary, summary)
		}
	}
	if count := len(p.summary); count == 0 || count > maxBundleAssets {
		return nil, fmt.Errorf("资产包需要 1 至 %d 项资产与依赖", maxBundleAssets)
	}
	for _, v := range b.Skills {
		if len(v.Attachments)+len(v.Files) > domain.SkillMaxFileCount {
			return nil, fmt.Errorf("技能 %q 文件数超出限制", v.ID)
		}
		if err := p.checkFiles(v.Files, v.Attachments); err != nil {
			return nil, err
		}
	}
	for _, v := range b.MCPServers {
		removeBundleMCPBindings(&v.MCPServerDefinition)
		v.ProjectID, v.Scope, v.FileRefs = project, "project", nil
		if err := domain.ValidateMCPServer(v.MCPServerDefinition); err != nil {
			return nil, err
		}
		if len(v.Attachments) > domain.MCPMaxFileCount {
			return nil, fmt.Errorf("MCP %q 文件数超出限制", v.ID)
		}
		if len(v.Attachments) > 0 && v.Transport == "http" {
			return nil, fmt.Errorf("MCP %q 的附件仅适用于 stdio", v.ID)
		}
		if err := p.checkFiles(nil, v.Attachments); err != nil {
			return nil, err
		}
	}
	for _, v := range b.Agents {
		if v.ExecutorID != "" {
			ex, exists := BuiltInExecutor(v.ExecutorID)
			if !exists {
				return nil, fmt.Errorf("角色 %q 的执行器 %q 不可用", v.ID, v.ExecutorID)
			}
			if err := domain.ValidateAgentEffort(v.Effort, ex.Kind); err != nil {
				return nil, err
			}
		}
		if err := domain.ValidateVerificationGates(v.Verification); err != nil {
			return nil, err
		}
		for _, refs := range []struct {
			kind string
			ids  []string
		}{
			{"rule", v.Rules}, {"skill", v.Skills}, {"mcp", v.MCPServers},
		} {
			if err := p.checkRefs(refs.kind, refs.ids); err != nil {
				return nil, err
			}
		}
	}
	for _, w := range b.Workflows {
		if err := domain.ValidateWorkflowPreparation(w); err != nil {
			return nil, err
		}
		if err := domain.ValidateWorkflowMetricStages(w); err != nil {
			return nil, err
		}
		for _, n := range w.Nodes {
			if !domain.IsPreviewBuildAgent(n.AgentID) {
				if err := p.checkOptionalRef("agent", n.AgentID); err != nil {
					return nil, err
				}
			}
			if err := p.checkOptionalRef("sop", n.SOPWorkflowID); err != nil {
				return nil, err
			}
		}
		if !w.AutoSelectSandbox && !builtinBundleSandbox(w.SandboxProfileID) {
			if err := p.checkRefs("sandbox", []string{w.SandboxProfileID}); err != nil {
				return nil, err
			}
		}
	}
	if err := validateBundleSOPDependencies(b.Workflows); err != nil {
		return nil, err
	}
	for _, s := range b.Sandboxes {
		s.Environment = nil
		if err := domain.ValidateSandboxAssets(s); err != nil {
			return nil, fmt.Errorf("沙箱 %q：%w", s.ID, err)
		}
		if s.Dockerfile != nil {
			if err := p.checkFiles(s.Dockerfile.Files, nil); err != nil {
				return nil, err
			}
		}
	}
	for _, rule := range b.Rules {
		if err := p.checkRefs("sop", rule.WorkflowIDs); err != nil {
			return nil, err
		}
	}
	return p, nil
}

func validateBundleSOPDependencies(workflows []domain.WorkflowDefinition) error {
	graph := make(map[string][]string, len(workflows))
	for _, w := range workflows {
		for _, node := range w.Nodes {
			if node.SOPWorkflowID != "" {
				graph[w.ID] = append(graph[w.ID], node.SOPWorkflowID)
			}
		}
	}
	active := make(map[string]bool)
	done := make(map[string]bool)
	var visit func(string) error
	visit = func(id string) error {
		if active[id] {
			return fmt.Errorf("SOP 依赖形成循环：%s", id)
		}
		if done[id] {
			return nil
		}
		active[id] = true
		for _, child := range graph[id] {
			if err := visit(child); err != nil {
				return err
			}
		}
		delete(active, id)
		done[id] = true
		return nil
	}
	for _, w := range workflows {
		if err := visit(w.ID); err != nil {
			return err
		}
	}
	return nil
}

func bundleSummaries[T any](kind string, items []T, identity func(T) (string, string)) []domain.BundleAssetSummary {
	out := make([]domain.BundleAssetSummary, 0, len(items))
	for _, item := range items {
		id, name := identity(item)
		out = append(out, domain.BundleAssetSummary{BundleAssetRef: assetRef(kind, id), Name: name})
	}
	return out
}

func (p *bundleImport) checkFiles(inline map[string]string, files []domain.BundleAssetFile) error {
	paths := make(map[string]bool, len(inline)+len(files))
	addPath := func(name string) error {
		if err := domain.ValidateRelativeFilePath(name); err != nil {
			return err
		}
		if name != strings.TrimSpace(name) || strings.ContainsAny(name, "\\\x00") || paths[name] {
			return fmt.Errorf("无效或重复的附件路径 %q", name)
		}
		paths[name] = true
		return nil
	}
	for name := range inline {
		if err := addPath(name); err != nil {
			return err
		}
	}
	for _, f := range files {
		if err := addPath(f.Path); err != nil {
			return err
		}
		if int64(len(f.Content)) > domain.SkillFileMaxBytes || f.SHA256 != fmt.Sprintf("%x", sha256.Sum256(f.Content)) {
			return fmt.Errorf("附件 %q 的大小或校验值无效", f.Path)
		}
		p.fileSize += int64(len(f.Content))
		if p.fileSize > maxBundleFileBytes {
			return fmt.Errorf("资产包附件超过 64 MiB")
		}
	}
	for rel := range paths {
		for dir := path.Dir(rel); dir != "."; dir = path.Dir(dir) {
			if paths[dir] {
				return fmt.Errorf("附件路径 %q 同时作为文件及目录", dir)
			}
		}
	}
	return nil
}

func (p *bundleImport) checkRefs(kind string, ids []string) error {
	for _, id := range ids {
		if p.id(kind, id) == "" {
			return fmt.Errorf("资产包缺少依赖 %s/%s", kind, id)
		}
	}
	return nil
}

func (p *bundleImport) checkOptionalRef(kind, id string) error {
	if id == "" {
		return nil
	}
	return p.checkRefs(kind, []string{id})
}

func (p *bundleImport) id(kind, id string) string {
	return p.ids[assetRef(kind, id)]
}

func (p *bundleImport) mapIDs(kind string, ids []string) []string {
	if len(ids) == 0 {
		return nil
	}
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = p.id(kind, id)
	}
	return out
}

func (p *bundleImport) sandboxID(workflow domain.WorkflowDefinition) string {
	if workflow.AutoSelectSandbox {
		return ""
	}
	if builtinBundleSandbox(workflow.SandboxProfileID) {
		return DefaultSandboxProfileID
	}
	return p.id("sandbox", workflow.SandboxProfileID)
}

// Rebind the platform's Skill mount paths and explicit SKILL constants.
// Free text and packaged Skill/MCP files retain their original bytes.
func (p *bundleImport) rebindSkillReferences(text string) string {
	text = bundleSkillPath.ReplaceAllStringFunc(text, func(match string) string {
		parts := strings.Split(match, "/")
		if len(parts) == 4 {
			if id := p.id("agent", parts[1]); id != "" {
				parts[1] = id
			}
		}
		last := len(parts) - 1
		if id := p.id("skill", parts[last]); id != "" {
			parts[last] = id
		}
		return strings.Join(parts, "/")
	})
	return bundleSkillConstant.ReplaceAllStringFunc(text, func(match string) string {
		start := strings.IndexAny(match, "\"'") + 1
		end := len(match) - 1
		if id := p.id("skill", match[start:end]); id != "" {
			return match[:start] + id + match[end:]
		}
		return match
	})
}

func (p *bundleImport) save(r *CatalogRepo, b domain.ProjectAssetBundle) error {
	for _, v := range b.Skills {
		v.ID, v.ProjectID, v.Shared, v.Source, v.FileRefs = p.id("skill", v.ID), p.target, false, nil, nil
		var err error
		if len(v.Attachments) > 0 {
			err = r.PutSkillSnapshot(v.SkillDefinition, bundleFileInputs(v.Attachments), true)
		} else {
			err = r.PutSkill(v.SkillDefinition)
		}
		if err != nil {
			return err
		}
	}
	for _, v := range b.MCPServers {
		removeBundleMCPBindings(&v.MCPServerDefinition)
		v.ID, v.ProjectID, v.Scope = p.id("mcp", v.ID), p.target, "project"
		v.FileRefs, v.MarketID, v.IconURL = nil, "", ""
		if err := r.PutMCPServer(v.MCPServerDefinition); err != nil {
			return err
		}
		if len(v.Attachments) > 0 {
			if err := r.PutMCPFiles(v.ID, bundleFileInputs(v.Attachments)); err != nil {
				return err
			}
		}
	}
	for _, v := range b.Rules {
		v.ID, v.ProjectID, v.Shared = p.id("rule", v.ID), p.target, false
		v.WorkflowIDs = p.mapIDs("sop", v.WorkflowIDs)
		if err := r.PutRule(v); err != nil {
			return err
		}
	}
	for _, v := range b.Sandboxes {
		v.ID, v.ProjectID, v.Shared = p.id("sandbox", v.ID), p.target, false
		v.Environment, v.DockerfileID = nil, ""
		if err := r.PutSandbox(v); err != nil {
			return err
		}
	}
	for _, v := range b.Agents {
		removeBundleAgentBindings(&v)
		v.ID, v.ProjectID, v.Shared, v.Version = p.id("agent", v.ID), p.target, false, 1
		v.Rules = p.mapIDs("rule", v.Rules)
		v.Skills = p.mapIDs("skill", v.Skills)
		v.MCPServers = p.mapIDs("mcp", v.MCPServers)
		v.Prompt = p.rebindSkillReferences(v.Prompt)
		for i := range v.Hooks {
			v.Hooks[i].Script = p.rebindSkillReferences(v.Hooks[i].Script)
		}
		for i := range v.Verification {
			for j, command := range v.Verification[i].Commands {
				v.Verification[i].Commands[j] = p.rebindSkillReferences(command)
			}
		}
		if err := r.PutAgent(v); err != nil {
			return err
		}
	}
	for _, v := range b.Workflows {
		removeBundleWorkflowBindings(&v)
		v.ID, v.ProjectID, v.Shared, v.Generic = p.id("sop", v.ID), p.target, false, false
		v.Status, v.Version, v.Revision, v.PublishedRevision = "draft", 1, 0, 0
		v.SandboxProfileID = p.sandboxID(v)
		for i := range v.Nodes {
			n := &v.Nodes[i]
			if !domain.IsPreviewBuildAgent(n.AgentID) {
				n.AgentID = p.id("agent", n.AgentID)
			}
			n.SOPWorkflowID = p.id("sop", n.SOPWorkflowID)
			n.Script = p.rebindSkillReferences(n.Script)
			n.Command = p.rebindSkillReferences(n.Command)
		}
		if err := r.PutWorkflow(v); err != nil {
			return err
		}
	}
	return nil
}

func bundleFileInputs(files []domain.BundleAssetFile) []SkillFileInput {
	out := make([]SkillFileInput, 0, len(files))
	for _, f := range files {
		out = append(out, SkillFileInput{Path: f.Path, Content: f.Content, Mode: f.Mode})
	}
	return out
}
