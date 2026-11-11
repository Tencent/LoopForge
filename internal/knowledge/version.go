package knowledge

// Version hints on tool results. Citations stay opaque (`source@version:path`),
// but agents and the chat UI also receive a human label so hosted revisions are
// never described as commits.

func (s *Service) sourceKind(id string) string {
	if s == nil || s.store == nil || id == "" {
		return ""
	}
	if ms, err := s.managed(); err == nil {
		if src, ok := ms.KnowledgeSource(id); ok {
			return src.Kind
		}
	}
	return ""
}

func (s *Service) annotateSearchVersions(result *SearchResult) {
	if result == nil {
		return
	}
	kinds := map[string]string{}
	for i := range result.Results {
		hit := &result.Results[i]
		kind, seen := kinds[hit.Source]
		if !seen {
			kind = s.sourceKind(hit.Source)
			kinds[hit.Source] = kind
		}
		hit.Revision, hit.Version = versionLabel(kind, hit.Commit)
	}
}

func (s *Service) annotateGrepVersions(result *GrepResult) {
	if result == nil {
		return
	}
	kinds := map[string]string{}
	for i := range result.Hits {
		hit := &result.Hits[i]
		kind, seen := kinds[hit.Source]
		if !seen {
			kind = s.sourceKind(hit.Source)
			kinds[hit.Source] = kind
		}
		hit.Revision, hit.Version = versionLabel(kind, hit.Commit)
	}
}

func (s *Service) annotateDocumentVersion(doc *Document) {
	if doc == nil {
		return
	}
	doc.Revision, doc.Version = versionLabel(s.sourceKind(doc.Source), doc.Commit)
}
