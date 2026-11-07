package gitwork

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// PatchExcludeBasenames are SOP deliverables and executor-local files that
// never belong in a repository change set, wherever they sit in the tree.
var PatchExcludeBasenames = map[string]bool{
	"mcporter.json": true, "e2e-cases.md": true, "e2e-report.md": true, "test-report.md": true,
	"fix-plan.md": true, "fix-report.md": true, "review-report.md": true, "mr-body.md": true,
	"checkers-report.md": true, "comments-report.md": true, "closeout.md": true, "change-report.md": true, "tech-design.md": true,
	"knowledge.md": true, "deploy-report.md": true, "delivery-closeout.md": true, "plan.json": true,
	"human-context.md": true, "decision.md": true,
}

// ExcludePathspecs mirrors Excluded as git pathspecs for diff/add commands.
var ExcludePathspecs = []string{
	":(exclude)config/mcporter.json",
	":(exclude)**/mcporter.json",
	":(exclude).agentflow/**",
	// Go & PHP & Rust & Ruby
	":(exclude)vendor/**",
	":(exclude)**/vendor/**",
	":(exclude)pkg/mod/**",
	":(exclude)**/pkg/mod/**",
	// Node & Frontend
	":(exclude)node_modules/**",
	":(exclude)**/node_modules/**",
	":(exclude)node-modules/**",
	":(exclude)**/node-modules/**",
	":(exclude).pnpm-store/**",
	":(exclude)**/.pnpm-store/**",
	":(exclude).yarn/**",
	":(exclude)**/.yarn/**",
	":(exclude)bower_components/**",
	":(exclude)**/bower_components/**",
	":(exclude).turbo/**",
	":(exclude)**/.turbo/**",
	":(exclude).next/**",
	":(exclude)**/.next/**",
	":(exclude).nuxt/**",
	":(exclude)**/.nuxt/**",
	":(exclude).output/**",
	":(exclude)**/.output/**",
	":(exclude).parcel-cache/**",
	":(exclude)**/.parcel-cache/**",
	":(exclude).svelte-kit/**",
	":(exclude)**/.svelte-kit/**",
	// Python
	":(exclude)venv/**",
	":(exclude)**/venv/**",
	":(exclude).venv/**",
	":(exclude)**/.venv/**",
	":(exclude)env/**",
	":(exclude)**/env/**",
	":(exclude)site-packages/**",
	":(exclude)**/site-packages/**",
	":(exclude)dist-packages/**",
	":(exclude)**/dist-packages/**",
	":(exclude)__pycache__/**",
	":(exclude)**/__pycache__/**",
	":(exclude).pytest_cache/**",
	":(exclude)**/.pytest_cache/**",
	":(exclude).mypy_cache/**",
	":(exclude)**/.mypy_cache/**",
	":(exclude).ruff_cache/**",
	":(exclude)**/.ruff_cache/**",
	":(exclude).tox/**",
	":(exclude)**/.tox/**",
	":(exclude)*.egg-info/**",
	":(exclude)**/*.egg-info/**",
	":(exclude).eggs/**",
	":(exclude)**/.eggs/**",
	// Java & JVM
	":(exclude).gradle/**",
	":(exclude)**/.gradle/**",
	":(exclude).m2/**",
	":(exclude)**/.m2/**",
	// Rust & Java
	":(exclude)target/**",
	":(exclude)**/target/**",
	// C / C++
	":(exclude)vcpkg_installed/**",
	":(exclude)**/vcpkg_installed/**",
	":(exclude).conan/**",
	":(exclude)**/.conan/**",
	":(exclude)CMakeFiles/**",
	":(exclude)**/CMakeFiles/**",
	":(exclude)cmake-build-*/**",
	":(exclude)**/cmake-build-*/**",
	":(exclude).deps/**",
	":(exclude)**/.deps/**",
	":(exclude).libs/**",
	":(exclude)**/.libs/**",
	// Swift / iOS / macOS
	":(exclude)Pods/**",
	":(exclude)**/Pods/**",
	":(exclude)Carthage/**",
	":(exclude)**/Carthage/**",
	":(exclude)DerivedData/**",
	":(exclude)**/DerivedData/**",
	":(exclude).build/**",
	":(exclude)**/.build/**",
	":(exclude).swiftpm/**",
	":(exclude)**/.swiftpm/**",
	":(exclude)*.framework/**",
	":(exclude)**/*.framework/**",
	":(exclude)*.xcframework/**",
	":(exclude)**/*.xcframework/**",
	":(exclude)*.dSYM/**",
	":(exclude)**/*.dSYM/**",
	// Android
	":(exclude).cxx/**",
	":(exclude)**/.cxx/**",
	// .NET / C#
	":(exclude)obj/**",
	":(exclude)**/obj/**",
	":(exclude).nuget/**",
	":(exclude)**/.nuget/**",
	// Ruby
	":(exclude).bundle/**",
	":(exclude)**/.bundle/**",
	// Elixir / Erlang
	":(exclude)deps/**",
	":(exclude)**/deps/**",
	":(exclude)_build/**",
	":(exclude)**/_build/**",
	":(exclude).rebar3/**",
	":(exclude)**/.rebar3/**",
	// Dart / Flutter
	":(exclude).dart_tool/**",
	":(exclude)**/.dart_tool/**",
	// Terraform / IaC
	":(exclude).terraform/**",
	":(exclude)**/.terraform/**",
	":(exclude).terragrunt-cache/**",
	":(exclude)**/.terragrunt-cache/**",
	// Binaries / compiled artifacts
	":(exclude)*.o",
	":(exclude)**/*.o",
	":(exclude)*.obj",
	":(exclude)**/*.obj",
	":(exclude)*.a",
	":(exclude)**/*.a",
	":(exclude)*.lib",
	":(exclude)**/*.lib",
	":(exclude)*.so",
	":(exclude)**/*.so",
	":(exclude)*.dylib",
	":(exclude)**/*.dylib",
	":(exclude)*.dll",
	":(exclude)**/*.dll",
	":(exclude)*.exe",
	":(exclude)**/*.exe",
	":(exclude)*.out",
	":(exclude)**/*.out",
	":(exclude)*.pdb",
	":(exclude)**/*.pdb",
	":(exclude)*.pch",
	":(exclude)**/*.pch",
	":(exclude)*.gch",
	":(exclude)**/*.gch",
	":(exclude)*.class",
	":(exclude)**/*.class",
	":(exclude)*.jar",
	":(exclude)**/*.jar",
	":(exclude)*.war",
	":(exclude)**/*.war",
	":(exclude)*.ear",
	":(exclude)**/*.ear",
	":(exclude)*.aar",
	":(exclude)**/*.aar",
	":(exclude)*.apk",
	":(exclude)**/*.apk",
	":(exclude)*.aab",
	":(exclude)**/*.aab",
	":(exclude)*.dex",
	":(exclude)**/*.dex",
	":(exclude)*.pyc",
	":(exclude)**/*.pyc",
	":(exclude)*.pyo",
	":(exclude)**/*.pyo",
	":(exclude)*.pyd",
	":(exclude)**/*.pyd",
	":(exclude)*.rlib",
	":(exclude)**/*.rlib",
	":(exclude)*.rmeta",
	":(exclude)**/*.rmeta",
	":(exclude)*.beam",
	":(exclude)**/*.beam",
	":(exclude)*.nupkg",
	":(exclude)**/*.nupkg",
	":(exclude)*.gem",
	":(exclude)**/*.gem",
	// Test artifacts, test reports & coverage data
	// PHPUnit & tooling caches
	":(exclude).phpunit.result.cache",
	":(exclude)**/.phpunit.result.cache",
	":(exclude)*.phpunit.result.cache",
	":(exclude)**/*.phpunit.result.cache",
	":(exclude).phpunit.cache/**",
	":(exclude)**/.phpunit.cache/**",
	":(exclude).pest.cache/**",
	":(exclude)**/.pest.cache/**",
	":(exclude).php-cs-fixer.cache",
	":(exclude)**/.php-cs-fixer.cache",
	":(exclude).php_cs.cache",
	":(exclude)**/.php_cs.cache",
	// Test reports & results
	":(exclude)coverage_clover.xml",
	":(exclude)**/coverage_clover.xml",
	":(exclude)clover.xml",
	":(exclude)**/clover.xml",
	":(exclude)cobertura.xml",
	":(exclude)**/cobertura.xml",
	":(exclude)jacoco.xml",
	":(exclude)**/jacoco.xml",
	":(exclude)jacoco.exec",
	":(exclude)**/jacoco.exec",
	":(exclude)junit_unit_test.xml",
	":(exclude)**/junit_unit_test.xml",
	":(exclude)junit.xml",
	":(exclude)**/junit.xml",
	":(exclude)junit-report.xml",
	":(exclude)**/junit-report.xml",
	":(exclude)*junit*.xml",
	":(exclude)**/*junit*.xml",
	":(exclude)*clover*.xml",
	":(exclude)**/*clover*.xml",
	":(exclude)test-results.xml",
	":(exclude)**/test-results.xml",
	":(exclude)test-report.xml",
	":(exclude)**/test-report.xml",
	":(exclude)test-results/**",
	":(exclude)**/test-results/**",
	":(exclude)tests/_output/**",
	":(exclude)**/tests/_output/**",
	// Coverage reports & profiles
	":(exclude)coverage.xml",
	":(exclude)**/coverage.xml",
	":(exclude)coverage.json",
	":(exclude)**/coverage.json",
	":(exclude)coverage.lcov",
	":(exclude)**/coverage.lcov",
	":(exclude)coverage.out",
	":(exclude)**/coverage.out",
	":(exclude)coverage.txt",
	":(exclude)**/coverage.txt",
	":(exclude)profile.out",
	":(exclude)**/profile.out",
	":(exclude)lcov.info",
	":(exclude)**/lcov.info",
	":(exclude)lcov.dat",
	":(exclude)**/lcov.dat",
	":(exclude).coverage",
	":(exclude)**/.coverage",
	":(exclude).coverage.*",
	":(exclude)**/.coverage.*",
	":(exclude)coverage/**",
	":(exclude)**/coverage/**",
	":(exclude)coverage-report/**",
	":(exclude)**/coverage-report/**",
	":(exclude)coverage-reports/**",
	":(exclude)**/coverage-reports/**",
	":(exclude)coverage-xml/**",
	":(exclude)**/coverage-xml/**",
	":(exclude)coverage-html/**",
	":(exclude)**/coverage-html/**",
	":(exclude)htmlcov/**",
	":(exclude)**/htmlcov/**",
	":(exclude).nyc_output/**",
	":(exclude)**/.nyc_output/**",
	// Compiler coverage counters (gcov/lcov)
	":(exclude)*.gcda",
	":(exclude)**/*.gcda",
	":(exclude)*.gcno",
	":(exclude)**/*.gcno",
	":(exclude)*.gcov",
	":(exclude)**/*.gcov",
}

var dependencyDirNames = map[string]bool{
	// Go & PHP & Rust & Ruby
	"vendor": true,
	// Node & Frontend
	"node_modules":     true,
	"node-modules":     true,
	".pnpm-store":      true,
	".yarn":            true,
	"bower_components": true,
	".turbo":           true,
	".next":            true,
	".nuxt":            true,
	".output":          true,
	".parcel-cache":    true,
	".svelte-kit":      true,
	// Python
	"venv":          true,
	".venv":         true,
	"env":           true,
	"site-packages": true,
	"dist-packages": true,
	"__pycache__":   true,
	".pytest_cache": true,
	".mypy_cache":   true,
	".ruff_cache":   true,
	".tox":          true,
	".eggs":         true,
	// Java & JVM
	".gradle": true,
	".m2":     true,
	// Rust & Java
	"target": true,
	// C / C++
	"vcpkg_installed": true,
	".conan":          true,
	"CMakeFiles":      true,
	".deps":           true,
	".libs":           true,
	// Swift / iOS / macOS
	"Pods":        true,
	"Carthage":    true,
	"DerivedData": true,
	".build":      true,
	".swiftpm":    true,
	// Android
	".cxx": true,
	// .NET / C#
	"obj":    true,
	".nuget": true,
	// Ruby
	".bundle": true,
	// Elixir / Erlang
	"deps":    true,
	"_build":  true,
	".rebar3": true,
	// Dart / Flutter
	".dart_tool": true,
	// Terraform / IaC
	".terraform":        true,
	".terragrunt-cache": true,
}

var dependencyOrBinaryExtensions = map[string]bool{
	// Compiled objects & libraries (C/C++, Go, Rust)
	".o":     true,
	".obj":   true,
	".a":     true,
	".lib":   true,
	".so":    true,
	".dylib": true,
	".dll":   true,
	".exe":   true,
	".out":   true,
	".pdb":   true,
	".idb":   true,
	".pch":   true,
	".gch":   true,
	".rlib":  true,
	".rmeta": true,
	// Java & JVM
	".class": true,
	".jar":   true,
	".war":   true,
	".ear":   true,
	".nar":   true,
	// Android
	".aar": true,
	".apk": true,
	".aab": true,
	".dex": true,
	// Python
	".pyc": true,
	".pyo": true,
	".pyd": true,
	// Elixir / Erlang
	".beam": true,
	// Package archives
	".nupkg":  true,
	".snupkg": true,
	".gem":    true,
}

func IsDependencyPath(rel string) bool {
	rel = strings.TrimPrefix(filepath.ToSlash(UnquoteLsFilesPath(rel)), "./")
	ext := strings.ToLower(filepath.Ext(rel))
	if dependencyOrBinaryExtensions[ext] {
		return true
	}
	for _, part := range strings.Split(rel, "/") {
		if dependencyDirNames[part] ||
			strings.HasPrefix(part, "cmake-build-") ||
			strings.HasSuffix(part, ".egg-info") ||
			strings.HasSuffix(part, ".framework") ||
			strings.HasSuffix(part, ".xcframework") ||
			strings.HasSuffix(part, ".dSYM") {
			return true
		}
	}
	if strings.Contains(rel, "/pkg/mod/") || strings.HasPrefix(rel, "pkg/mod/") {
		return true
	}
	return false
}

func IsSOPDeliverableOrControlFile(rel string) bool {
	rel = filepath.ToSlash(UnquoteLsFilesPath(rel))
	if rel == ".agentflow" || strings.HasPrefix(rel, ".agentflow/") {
		return true
	}
	if rel == ".codebuddy" || strings.HasPrefix(rel, ".codebuddy/") {
		return true
	}
	return PatchExcludeBasenames[filepath.Base(rel)]
}

var testArtifactBasenames = map[string]bool{
	"coverage_clover.xml":   true,
	"clover.xml":            true,
	"cobertura.xml":         true,
	"jacoco.xml":            true,
	"jacoco.exec":           true,
	"junit_unit_test.xml":   true,
	"junit.xml":             true,
	"junit-report.xml":      true,
	"test-results.xml":      true,
	"test-report.xml":       true,
	"coverage.xml":          true,
	"coverage.json":         true,
	"coverage.lcov":         true,
	"coverage.out":          true,
	"coverage.txt":          true,
	"profile.out":           true,
	"lcov.info":             true,
	"lcov.dat":              true,
	".coverage":             true,
	".phpunit.result.cache": true,
	".php-cs-fixer.cache":   true,
	".php_cs.cache":         true,
}

var testArtifactDirNames = map[string]bool{
	".phpunit.cache":   true,
	".pest.cache":      true,
	"coverage":         true,
	"coverage-report":  true,
	"coverage-reports": true,
	"coverage-xml":     true,
	"coverage-html":    true,
	"htmlcov":          true,
	".nyc_output":      true,
	"test-results":     true,
}

var testArtifactExtensions = map[string]bool{
	".gcda": true,
	".gcno": true,
	".gcov": true,
}

func IsTestArtifactOrCachePath(rel string) bool {
	rel = strings.TrimPrefix(filepath.ToSlash(UnquoteLsFilesPath(rel)), "./")
	base := filepath.Base(rel)
	lowerBase := strings.ToLower(base)
	ext := strings.ToLower(filepath.Ext(rel))

	// 1. Explicitly protect test configurations from being excluded
	if lowerBase == "phpunit.xml" || lowerBase == "phpunit.xml.dist" {
		return false
	}

	// 2. Explicitly protect source code files (never treat source code as test artifact)
	switch ext {
	case ".go", ".php", ".py", ".js", ".ts", ".jsx", ".tsx", ".java", ".rs", ".c", ".cc", ".cpp", ".cxx", ".h", ".hpp", ".cs", ".rb", ".swift", ".kt", ".dart":
		return false
	}

	// 3. Exact matching for known test artifact & coverage basenames
	if testArtifactBasenames[base] || testArtifactBasenames[lowerBase] {
		return true
	}

	// 4. Test artifact extensions (e.g. gcov/lcov counters)
	if testArtifactExtensions[ext] {
		return true
	}

	// 5. Pattern matches for file basenames
	if strings.HasSuffix(lowerBase, ".phpunit.result.cache") ||
		strings.HasSuffix(lowerBase, ".phpunit.cache") {
		return true
	}
	if strings.HasPrefix(lowerBase, ".coverage.") {
		return true
	}
	if strings.HasSuffix(lowerBase, ".xml") {
		if strings.Contains(lowerBase, "junit") ||
			strings.Contains(lowerBase, "clover") ||
			strings.HasPrefix(lowerBase, "coverage") ||
			strings.HasSuffix(lowerBase, "_coverage.xml") ||
			strings.HasSuffix(lowerBase, "-coverage.xml") {
			return true
		}
	}

	// 6. Check directory components
	parts := strings.Split(rel, "/")
	for i, part := range parts {
		if testArtifactDirNames[part] {
			return true
		}
		if part == "_output" && i > 0 && parts[i-1] == "tests" {
			return true
		}
	}
	return false
}

// Excluded reports whether a workspace-relative path stays out of commits and
// change inventories: SOP control files, dependencies, test output, secrets.
func Excluded(rel string) bool {
	return IsSOPDeliverableOrControlFile(rel) || IsDependencyPath(rel) || IsTestArtifactOrCachePath(rel) || IsCredentialFile(rel)
}

// IsCredentialFile matches new files that typically hold tokens. Every node
// pushes the work branch, so an Agent-written .npmrc must never be staged.
func IsCredentialFile(rel string) bool {
	base := filepath.Base(rel)
	switch base {
	case ".env", ".npmrc", ".pypirc", ".netrc", ".git-credentials", ".dockercfg":
		return true
	}
	return strings.HasPrefix(base, ".env.") && !strings.HasSuffix(base, ".example") && !strings.HasSuffix(base, ".sample") && !strings.HasSuffix(base, ".template")
}

// UnquoteLsFilesPath undoes C-style quoting from `git ls-files` without -z,
// e.g. `" .agentflow/.../API\344\275\277.md"` → real UTF-8 path.
func UnquoteLsFilesPath(rel string) string {
	rel = strings.TrimSpace(rel)
	if len(rel) >= 2 && rel[0] == '"' && rel[len(rel)-1] == '"' {
		if unquoted, err := strconv.Unquote(rel); err == nil {
			return unquoted
		}
	}
	return rel
}

// StageUntracked intent-adds untracked sources that are not Excluded, so they
// show up in diffs and are picked up by `git add -u`.
func StageUntracked(workspace string) error {
	cmd := exec.Command("git", "-C", workspace, "ls-files", "-z", "--others", "--exclude-standard")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("list untracked files: %w: %s", err, strings.TrimSpace(string(out)))
	}
	var files []string
	for _, rel := range strings.Split(string(out), "\x00") {
		rel = filepath.ToSlash(UnquoteLsFilesPath(rel))
		if rel == "" || Excluded(rel) {
			continue
		}
		files = append(files, rel)
	}
	if len(files) == 0 {
		return nil
	}
	payload := strings.Join(files, "\x00") + "\x00"
	add := exec.Command("git", "-C", workspace, "add", "-N", "--pathspec-from-file=-", "--pathspec-file-nul")
	add.Stdin = strings.NewReader(payload)
	if out, err := add.CombinedOutput(); err != nil {
		return fmt.Errorf("intent-add untracked files: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
