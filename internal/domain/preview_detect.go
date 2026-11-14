package domain

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const (
	maxPreviewDetectDepth = 6
	maxPreviewDetectFiles = 8000
)

var previewDetectSkipDirs = map[string]bool{
	"node_modules": true,
	".git":         true,
	".svn":         true,
	".hg":          true,
	"vendor":       true,
	".agentflow":   true,
	".next":        true,
	".nuxt":        true,
	".output":      true,
	".cache":       true,
	".turbo":       true,
	"coverage":     true,
	"tmp":          true,
	"temp":         true,
}

var frontendConfigFiles = map[string]bool{
	"vite.config.js":     true,
	"vite.config.ts":     true,
	"vite.config.mjs":    true,
	"vite.config.cjs":    true,
	"webpack.config.js":  true,
	"webpack.config.ts":  true,
	"webpack.config.mjs": true,
	"rspack.config.js":   true,
	"rspack.config.ts":   true,
	"next.config.js":     true,
	"next.config.mjs":    true,
	"next.config.ts":     true,
	"nuxt.config.js":     true,
	"nuxt.config.ts":     true,
	"angular.json":       true,
	"svelte.config.js":   true,
	"astro.config.mjs":   true,
	"astro.config.ts":    true,
	"vue.config.js":      true,
	"remix.config.js":    true,
}

var frontendIndexNames = map[string]bool{
	"index.html": true,
	"index.htm":  true,
}

var frontendDepNames = map[string]bool{
	"react":                true,
	"react-dom":            true,
	"react-scripts":        true,
	"preact":               true,
	"vue":                  true,
	"nuxt":                 true,
	"next":                 true,
	"svelte":               true,
	"@sveltejs/kit":        true,
	"vite":                 true,
	"webpack":              true,
	"rspack":               true,
	"@rspack/core":         true,
	"@angular/core":        true,
	"@angular/cli":         true,
	"solid-js":             true,
	"lit":                  true,
	"astro":                true,
	"gatsby":               true,
	"@remix-run/react":     true,
	"parcel":               true,
	"umi":                  true,
	"@vitejs/plugin-react": true,
	"@vitejs/plugin-vue":   true,
}

var frontendScriptTokens = []string{
	"vite",
	"webpack",
	"rspack",
	"next build",
	"next start",
	"next dev",
	"nuxt",
	"react-scripts",
	"vue-cli-service",
	"ng build",
	"ng serve",
	"svelte-kit",
	"astro build",
	"astro dev",
	"parcel",
}

type npmPackageJSON struct {
	Dependencies     map[string]string `json:"dependencies"`
	DevDependencies  map[string]string `json:"devDependencies"`
	PeerDependencies map[string]string `json:"peerDependencies"`
	Scripts          map[string]string `json:"scripts"`
}

// DetectFrontendProject reports whether workspace looks like a frontend (or
// already-built static site). Non-frontend repos should skip preview instead
// of inventing a page or failing the SOP.
func DetectFrontendProject(root string) (bool, string) {
	root = strings.TrimSpace(root)
	if root == "" {
		return false, "工作区为空"
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return false, "工作区不可读"
	}
	found := false
	evidence := ""
	files := 0
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if found {
			return fs.SkipAll
		}
		if walkErr != nil {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		if rel == "." {
			return nil
		}
		if d.IsDir() {
			if previewDetectSkipDir(d.Name(), rel) {
				return fs.SkipDir
			}
			return nil
		}
		files++
		if files > maxPreviewDetectFiles {
			return fs.SkipAll
		}
		if hit, why := frontendFileEvidence(path, rel, d.Name()); hit {
			found = true
			evidence = why
			return fs.SkipAll
		}
		return nil
	})
	if found {
		return true, evidence
	}
	return false, "未发现 Vite/webpack/Next 配置、前端依赖或 index.html"
}

// previewDetectSkipDir reports whether the directory at rel (relative to the
// workspace root) should be pruned: well-known dependency/cache/VCS folders,
// or anything at or beyond maxPreviewDetectDepth.
func previewDetectSkipDir(name, rel string) bool {
	if previewDetectSkipDirs[strings.ToLower(name)] {
		return true
	}
	return strings.Count(filepath.ToSlash(rel), "/") >= maxPreviewDetectDepth
}

// frontendFileEvidence reports whether the file at path (rel relative to the
// workspace root) marks the workspace as frontend, and the evidence string:
// an index page or bundler config yields its slash path; a package.json with
// frontend dependencies or scripts yields "<rel> (<reason>)".
func frontendFileEvidence(path, rel, name string) (bool, string) {
	lower := strings.ToLower(name)
	if frontendIndexNames[lower] || frontendConfigFiles[lower] {
		return true, filepath.ToSlash(rel)
	}
	if lower != "package.json" {
		return false, ""
	}
	if hit, why := packageJSONFrontend(path); hit {
		return true, filepath.ToSlash(rel) + " (" + why + ")"
	}
	return false, ""
}

func packageJSONFrontend(path string) (bool, string) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return false, ""
	}
	var pkg npmPackageJSON
	if json.Unmarshal(raw, &pkg) != nil {
		return false, ""
	}
	for _, group := range []map[string]string{pkg.Dependencies, pkg.DevDependencies, pkg.PeerDependencies} {
		for name := range group {
			if frontendDepNames[strings.ToLower(name)] {
				return true, name
			}
		}
	}
	for _, cmd := range pkg.Scripts {
		lower := strings.ToLower(cmd)
		for _, token := range frontendScriptTokens {
			if strings.Contains(lower, token) {
				return true, "scripts:" + token
			}
		}
	}
	return false, ""
}
