package repo

import (
	"crypto/sha256"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"agentflow/internal/domain"

	"gorm.io/gorm"
)

const maxBundleAssets = 200
const maxBundleFileBytes = 64 << 20

type bundleExport struct {
	catalog *CatalogRepo
	project string
	seen    map[domain.BundleAssetRef]bool
	bundle  domain.ProjectAssetBundle
	bytes   int64
}

// ExportProjectAssetBundle snapshots the selected assets and their project-owned
// dependencies. A reference to a platform sandbox is resolved on import.
func (r *CatalogRepo) ExportProjectAssetBundle(project string, selection []domain.BundleAssetRef) (domain.ProjectAssetBundle, error) {
	if project == "" || len(selection) == 0 || len(selection) > maxBundleAssets {
		return domain.ProjectAssetBundle{}, fmt.Errorf("请选择 1 至 %d 项项目资产", maxBundleAssets)
	}
	var bundle domain.ProjectAssetBundle
	err := r.DB.Transaction(func(tx *gorm.DB) error {
		catalog := *r
		catalog.deps = &deps{DB: tx}
		x := bundleExport{catalog: &catalog, project: project, seen: make(map[domain.BundleAssetRef]bool)}
		x.bundle.Format = domain.ProjectAssetBundleFormat
		for _, ref := range selection {
			switch ref.Kind {
			case "skill", "mcp", "agent", "sop":
			default:
				return fmt.Errorf("无法选择 %q 类型作为资产包入口", ref.Kind)
			}
			if err := x.add(ref.Kind, ref.ID); err != nil {
				return err
			}
		}
		for i := range x.bundle.Rules {
			rule := &x.bundle.Rules[i]
			rule.WorkflowIDs = slices.DeleteFunc(slices.Clone(rule.WorkflowIDs), func(id string) bool {
				return !x.seen[assetRef("sop", id)]
			})
		}
		bundle = x.bundle
		return nil
	})
	return bundle, err
}

func assetRef(kind, id string) domain.BundleAssetRef {
	return domain.BundleAssetRef{Kind: kind, ID: id}
}

func builtinBundleSandbox(id string) bool {
	return strings.TrimSpace(id) == "" || isDefaultSandboxID(id)
}

func (x *bundleExport) add(kind, id string) error {
	ref := assetRef(kind, id)
	if x.seen[ref] {
		return nil
	}
	if len(x.seen) >= maxBundleAssets {
		return fmt.Errorf("资产与依赖总数超过 %d 项", maxBundleAssets)
	}
	x.seen[ref] = true
	missing := fmt.Errorf("资产或依赖 %s/%s 不属于当前项目或已不存在", kind, id)
	r := x.catalog
	switch kind {
	case "skill":
		v, ok := r.Skill(id)
		if !ok || v.ProjectID != x.project {
			return missing
		}
		files, err := collectBundleFiles(x, v.FileRefs, r.SkillFileContents(id), func(ref domain.SkillFileRef) (string, string, uint32) {
			return ref.Path, ref.SHA256, ref.Mode
		})
		if err != nil {
			return err
		}
		v.FileRefs, v.Source, v.ProjectID, v.Shared = nil, nil, "", false
		x.bundle.Skills = append(x.bundle.Skills, domain.BundleSkill{SkillDefinition: v, Attachments: files})
	case "mcp":
		v, ok := r.MCPServer(id)
		if !ok || v.ProjectID != x.project || v.Scope == "personal" {
			return missing
		}
		files, err := collectBundleFiles(x, v.FileRefs, r.MCPFileContents(id), func(ref domain.MCPFileRef) (string, string, uint32) {
			return ref.Path, ref.SHA256, ref.Mode
		})
		if err != nil {
			return err
		}
		if removeBundleMCPBindings(&v) {
			x.binding("mcp-connection", id)
		}
		v.FileRefs, v.MarketID, v.IconURL, v.ProjectID = nil, "", "", ""
		x.bundle.MCPServers = append(x.bundle.MCPServers, domain.BundleMCPServer{MCPServerDefinition: v, Attachments: files})
	case "agent":
		v, ok := r.Agent(id)
		if !ok || v.ProjectID != x.project {
			return missing
		}
		for _, dependencies := range []struct {
			kind string
			ids  []string
		}{
			{"skill", v.Skills}, {"mcp", v.MCPServers}, {"rule", v.Rules},
		} {
			for _, dependency := range dependencies.ids {
				if err := x.add(dependencies.kind, dependency); err != nil {
					return err
				}
			}
		}
		if removeBundleAgentBindings(&v) {
			x.binding("role-integrations", id)
		}
		v.ProjectID, v.Shared = "", false
		x.bundle.Agents = append(x.bundle.Agents, v)
	case "sop":
		v, ok := r.Workflow(id)
		if !ok || v.ProjectID != x.project || v.Generic {
			return missing
		}
		if !v.AutoSelectSandbox && !builtinBundleSandbox(v.SandboxProfileID) {
			if err := x.add("sandbox", v.SandboxProfileID); err != nil {
				return err
			}
		}
		for _, node := range v.Nodes {
			if node.AgentID != "" && !domain.IsPreviewBuildAgent(node.AgentID) {
				if err := x.add("agent", node.AgentID); err != nil {
					return err
				}
			}
			if node.SOPWorkflowID != "" {
				if err := x.add("sop", node.SOPWorkflowID); err != nil {
					return err
				}
			}
		}
		for _, rule := range r.Rules() {
			if rule.Global && slices.Contains(rule.WorkflowIDs, id) {
				if err := x.add("rule", rule.ID); err != nil {
					return err
				}
			}
		}
		if removeBundleWorkflowBindings(&v) {
			x.binding("loop-integrations", id)
		}
		v.ProjectID, v.Shared = "", false
		x.bundle.Workflows = append(x.bundle.Workflows, v)
	case "rule":
		v, ok := r.Rule(id)
		if !ok || v.ProjectID != x.project {
			return missing
		}
		v.ProjectID, v.Shared = "", false
		x.bundle.Rules = append(x.bundle.Rules, v)
	case "sandbox":
		v, ok := r.Sandbox(id)
		if !ok || v.ProjectID != x.project {
			return missing
		}
		if len(v.Environment) != 0 {
			x.binding("sandbox-environment", id)
		}
		if v.Privileged {
			x.binding("sandbox-privileged", id)
		}
		v.Environment, v.ProjectID, v.Shared, v.DockerfileID = nil, "", false, ""
		x.bundle.Sandboxes = append(x.bundle.Sandboxes, v)
	default:
		return missing
	}
	return nil
}

func (x *bundleExport) binding(kind, id string) {
	x.bundle.Bindings = append(x.bundle.Bindings, domain.BundleBinding{Kind: kind, AssetID: id})
}

func collectBundleFiles[T any](x *bundleExport, refs []T, contents map[string][]byte, fields func(T) (string, string, uint32)) ([]domain.BundleAssetFile, error) {
	if len(refs) != len(contents) {
		return nil, fmt.Errorf("资产文件清单与存储内容不一致")
	}
	files := make([]domain.BundleAssetFile, 0, len(refs))
	for _, ref := range refs {
		path, digest, mode := fields(ref)
		content, ok := contents[path]
		sum := fmt.Sprintf("%x", sha256.Sum256(content))
		if !ok || (digest != "" && sum != digest) {
			return nil, fmt.Errorf("资产文件 %q 校验失败", path)
		}
		x.bytes += int64(len(content))
		if x.bytes > maxBundleFileBytes {
			return nil, fmt.Errorf("资产包附件超过 64 MiB")
		}
		files = append(files, domain.BundleAssetFile{Path: path, Content: content, Mode: mode, SHA256: sum})
	}
	return files, nil
}

func removeBundleMCPBindings(v *domain.MCPServerDefinition) bool {
	removed := len(v.Headers) > 0 || len(v.Environment) > 0 || v.AuthType != "" || v.CredentialKind != ""
	v.Headers, v.Environment, v.OwnerUserID = nil, nil, 0
	v.AuthType, v.CredentialKind = "", ""
	if u, err := url.Parse(v.URL); err != nil {
		v.URL = ""
		removed = true
	} else if u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.ForceQuery {
		u.User, u.RawQuery, u.Fragment, u.RawFragment, u.ForceQuery = nil, "", "", "", false
		v.URL = u.String()
		removed = true
	}
	return removed
}

func removeBundleAgentBindings(v *domain.AgentDefinition) bool {
	removed := v.PlatformMCP || v.KnowledgeBase
	v.PlatformMCP, v.KnowledgeBase = false, false
	return removed
}

func removeBundleWorkflowBindings(v *domain.WorkflowDefinition) bool {
	removed := len(v.Notifications) != 0 || len(v.ProjectCredentialIDs) != 0
	v.Notifications, v.SandboxSnapshot, v.ExecutorDefaults = nil, nil, nil
	v.ProjectCredentialIDs = nil
	v.GroupSOPHandoff, v.GroupCompletion, v.SOPCheckpoint = nil, nil, nil
	v.GroupMembers, v.GroupHistory, v.GroupPlan = nil, nil, nil
	v.Owner, v.TaskTitle, v.CollaborationMode = "", "", ""
	for i := range v.Nodes {
		n := &v.Nodes[i]
		removed = removed || len(n.Notifications) > 0 || len(n.CredentialEnv) > 0 || len(n.KnowledgeRefs) > 0
		n.Notifications, n.CredentialEnv, n.KnowledgeRefs = nil, nil, nil
		n.RequestedBy, n.RequestID, n.PlanItemID, n.ClaimedFiles = "", "", "", nil
	}
	return removed
}
