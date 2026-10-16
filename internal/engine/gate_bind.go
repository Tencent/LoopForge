package engine

import (
	"agentflow/internal/domain"
	"agentflow/internal/gitwork"
	"path/filepath"
	"sort"
	"strings"
)

func bindVerificationCommand(cmd string, artifacts []domain.ArtifactContract) string {
	return bindGitDiffQuietExcludes(bindGrepToArtifacts(cmd, artifacts), artifacts)
}

// bindGitDiffQuietExcludes keeps analyze-style cleanliness gates from failing
// when the node writes its declared SOP deliverable over a leftover tracked
// copy (e.g. fix-plan.md committed by a previous run onto the integration branch).
func bindGitDiffQuietExcludes(cmd string, artifacts []domain.ArtifactContract) string {
	fields := strings.Fields(strings.TrimSpace(cmd))
	if len(fields) < 3 || fields[0] != "git" || fields[1] != "diff" {
		return cmd
	}
	quiet := false
	for _, f := range fields[2:] {
		if f == "--quiet" || f == "-q" {
			quiet = true
			break
		}
	}
	if !quiet || strings.Contains(cmd, ":(exclude)") || strings.Contains(cmd, ":!") {
		return cmd
	}
	excludes := map[string]bool{}
	for _, a := range artifacts {
		p := filepath.ToSlash(strings.TrimSpace(a.Path))
		if p != "" && p != "." && !filepath.IsAbs(p) && !strings.HasPrefix(p, "..") {
			excludes[p] = true
		}
	}
	for name := range gitwork.PatchExcludeBasenames {
		excludes[name] = true
	}
	if len(excludes) == 0 {
		return cmd
	}
	paths := make([]string, 0, len(excludes))
	for p := range excludes {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	out := strings.TrimSpace(cmd)
	if !strings.Contains(out, " -- ") {
		out += " -- ."
	}
	for _, p := range paths {
		out += " ':(exclude)" + p + "'"
	}
	return out
}

// bindGrepToArtifacts appends the node's report files when a bare grep/rg
// command forgot its filename. grep with no file reads empty stdin and always fails.
func bindGrepToArtifacts(cmd string, artifacts []domain.ArtifactContract) string {
	fields := strings.Fields(strings.TrimSpace(cmd))
	if len(fields) == 0 {
		return cmd
	}
	switch fields[0] {
	case "grep", "egrep", "fgrep", "rg":
	default:
		return cmd
	}
	for _, a := range artifacts {
		if p := strings.TrimSpace(a.Path); p != "" && strings.Contains(cmd, p) {
			return cmd
		}
	}
	for _, f := range fields[1:] {
		if strings.HasPrefix(f, "-") {
			continue
		}
		if strings.Contains(f, "/") || strings.HasSuffix(f, ".md") || strings.HasSuffix(f, ".txt") || strings.HasSuffix(f, ".json") {
			return cmd
		}
	}
	var files []string
	seen := map[string]bool{}
	for _, a := range artifacts {
		p := strings.TrimSpace(a.Path)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		files = append(files, p)
	}
	if len(files) == 0 {
		return cmd
	}
	return strings.TrimSpace(cmd) + " " + strings.Join(files, " ")
}
