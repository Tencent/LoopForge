package knowledge

import (
	"math"
	"sort"
	"strings"

	"agentflow/internal/apperr"
	"agentflow/internal/domain"
)

// CreateSharedBase is called only by the platform administration endpoint.
func (s *Service) CreateSharedBase(name string, owner int64) (domain.KnowledgeBase, error) {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 120 {
		return domain.KnowledgeBase{}, apperr.New(apperr.Invalid, "知识库名称须为 1–120 个字")
	}
	ms, err := s.managed()
	if err != nil {
		return domain.KnowledgeBase{}, err
	}
	base := domain.KnowledgeBase{ID: "kb-shared-" + s.newID(), Name: name, Layer: "shared", OwnerUserID: owner}
	if err = ms.PutKnowledgeBase(base); err != nil {
		return domain.KnowledgeBase{}, err
	}
	if err = ms.PutKnowledgeSource(domain.KnowledgeSource{ID: "kb-hosted-" + base.ID, BaseID: base.ID, Kind: "hosted", Name: "托管文档", Status: SourceStatusReady, IndexStatus: SourceStatusReady}); err != nil {
		return domain.KnowledgeBase{}, err
	}
	return base, nil
}

type SharedBindingView struct {
	domain.KnowledgeBase
	Loaded bool `json:"loaded"`
}

// SharedBindings exposes only the catalog; unbound source metadata stays hidden.
func (s *Service) SharedBindings(project string) ([]SharedBindingView, error) {
	ms, err := s.managed()
	if err != nil {
		return nil, err
	}
	loaded := map[string]bool{}
	for _, b := range ms.KnowledgeBindings(strings.TrimSpace(project)) {
		loaded[b.BaseID] = true
	}
	out := []SharedBindingView{}
	for _, b := range ms.KnowledgeBases("") {
		if b.Layer == "shared" && b.ProjectID == "" {
			out = append(out, SharedBindingView{KnowledgeBase: b, Loaded: loaded[b.ID]})
		}
	}
	return out, nil
}

func (s *Service) SetSharedBinding(project, baseID string, enabled bool) error {
	project, baseID = strings.TrimSpace(project), strings.TrimSpace(baseID)
	if project == "" {
		return apperr.New(apperr.Invalid, "项目标识不能为空")
	}
	ms, err := s.managed()
	if err != nil {
		return err
	}
	base, ok := ms.KnowledgeBase(baseID)
	if !ok || base.Layer != "shared" || base.ProjectID != "" {
		return apperr.New(apperr.Forbidden, "只能加载公共知识库")
	}
	if enabled {
		return ms.PutKnowledgeBinding(domain.KnowledgeBinding{ProjectID: project, BaseID: baseID, Permission: "read"})
	}
	return ms.DeleteKnowledgeBinding(project, baseID)
}

// ownsBase separates write ownership from read bindings. Empty project scope is
// reserved for platform administration by the HTTP management endpoints.
func ownsBase(base domain.KnowledgeBase, project string) bool {
	project = strings.TrimSpace(project)
	return (project != "" && base.Layer == "project" && base.ProjectID == project) ||
		(project == "" && base.Layer == "shared" && base.ProjectID == "")
}

func sourceWritable(ms ManagedStore, project, source string) bool {
	src, ok := ms.KnowledgeSource(strings.TrimSpace(source))
	if !ok {
		return false
	}
	base, ok := ms.KnowledgeBase(src.BaseID)
	return ok && ownsBase(base, project)
}

// projectWritableSource is for review publishing, never the admin scope.
func (s *Service) projectWritableSource(project, source string) bool {
	if strings.TrimSpace(project) == "" {
		return false
	}
	if ms, err := s.managed(); err == nil {
		return sourceWritable(ms, project, source)
	}
	return SourceAllowed(s.AllowedSources(project), source)
}

func visibleBases(ms ManagedStore, project string) []domain.KnowledgeBase {
	project = strings.TrimSpace(project)
	out := []domain.KnowledgeBase{}
	seen := map[string]bool{}
	for _, b := range ms.KnowledgeBases(project) {
		if ownsBase(b, project) {
			out = append(out, b)
			seen[b.ID] = true
		}
	}
	if project != "" {
		for _, binding := range ms.KnowledgeBindings(project) {
			b, ok := ms.KnowledgeBase(binding.BaseID)
			if ok && !seen[b.ID] && b.Layer == "shared" && b.ProjectID == "" {
				out = append(out, b)
				seen[b.ID] = true
			}
		}
	}
	return out
}

func (s *Service) sourceOrigin(source string) BaseOrigin {
	ms, err := s.managed()
	if err != nil {
		return BaseOrigin{}
	}
	src, ok := ms.KnowledgeSource(source)
	if !ok {
		return BaseOrigin{}
	}
	b, ok := ms.KnowledgeBase(src.BaseID)
	if !ok {
		return BaseOrigin{}
	}
	return BaseOrigin{BaseID: b.ID, BaseName: b.Name, Layer: b.Layer}
}

// Relatedness groups use the highest score as a fixed anchor, so the 5% near
// tie rule is transitive and cannot pull weak local knowledge above strong hits.
// Kept in Service so lexical and hybrid adapters share the same D11 policy.
func (s *Service) rankOrigins(result *SearchResult) {
	for i := range result.Results {
		hit := &result.Results[i]
		hit.BaseOrigin = s.sourceOrigin(hit.Source)
		if hit.URL == "" {
			hit.URL = s.SourceFileURL(hit.Source, hit.Commit, hit.Path, hit.StartLine, hit.EndLine)
		}
	}
	sort.SliceStable(result.Results, func(i, j int) bool { return result.Results[i].Score > result.Results[j].Score })
	for start := 0; start < len(result.Results); {
		anchor := result.Results[start].Score
		end := start + 1
		for end < len(result.Results) && anchor-result.Results[end].Score <= math.Abs(anchor)*0.05 {
			end++
		}
		group := result.Results[start:end]
		sort.SliceStable(group, func(i, j int) bool { return group[i].Layer == "project" && group[j].Layer != "project" })
		start = end
	}
}
