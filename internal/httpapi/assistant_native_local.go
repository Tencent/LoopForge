package httpapi

import (
	"encoding/json"
	"path"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// A local workspace runs on its owner's machine without a sandbox, so its
// native actions follow Claude Code's default permission mode: reads and
// searches inside the workspace, and Claude Code's built-in read-only
// commands there, run without asking; everything else follows the approval
// mode. Like Claude Code's, these rules prevent unintended actions; they are
// not a security boundary (a symlink inside the workspace can still point
// outside it).

// nativeReadTools are the CLIs' file read and search tools.
var nativeReadTools = map[string]bool{
	"read": true, "read_file": true, "readfile": true, "glob": true, "grep": true,
	"list": true, "ls": true, "list_directory": true, "view_image": true, "readmediafile": true,
}

// nativeLocalScope is where a local workspace action runs: the run workspace
// and the CLI's current directory, both reported by the runner's hook.
type nativeLocalScope struct{ workspace, cwd string }

func newNativeLocalScope(workspace, cwd string) (nativeLocalScope, bool) {
	if !strings.HasPrefix(workspace, "/") || path.Clean(workspace) == "/" {
		return nativeLocalScope{}, false
	}
	scope := nativeLocalScope{workspace: path.Clean(workspace), cwd: path.Clean(workspace)}
	if cwd = strings.TrimSpace(cwd); cwd != "" {
		if !strings.HasPrefix(cwd, "/") {
			return nativeLocalScope{}, false
		}
		scope.cwd = path.Clean(cwd)
	}
	return scope, true
}

// nativeLocalInside reports whether arg, resolved from dir, names a path in
// the workspace. "~" paths are the owner's home, outside it.
func nativeLocalInside(scope nativeLocalScope, dir, arg string) bool {
	if strings.HasPrefix(arg, "~") {
		return false
	}
	if !strings.HasPrefix(arg, "/") {
		arg = path.Join(dir, arg)
	}
	arg = path.Clean(arg)
	return arg == scope.workspace || strings.HasPrefix(arg, scope.workspace+"/")
}

// nativeLocalReadAllowed reports whether a read or search tool stays inside
// the workspace. A tool naming no path reads from its current directory.
func nativeLocalReadAllowed(tool string, input json.RawMessage, scope nativeLocalScope) bool {
	var args map[string]any
	if len(input) > 0 && json.Unmarshal(input, &args) != nil {
		return false
	}
	keys := []string{"file_path", "filePath", "path", "paths", "absolute_path", "dir_path", "directory", "dir", "target_file", "target_directory", "notebook_path", "file", "files"}
	if tool == "glob" {
		keys = append(keys, "pattern")
	}
	found := false
	for _, key := range keys {
		var values []any
		switch value := args[key].(type) {
		case nil:
			continue
		case string:
			values = []any{value}
		case []any:
			values = value
		default:
			return false
		}
		for _, value := range values {
			text, ok := value.(string)
			if !ok {
				return false
			}
			if text = strings.TrimSpace(text); text == "" {
				continue
			}
			found = true
			if !nativeLocalInside(scope, scope.cwd, text) {
				return false
			}
		}
	}
	return found || nativeLocalInside(scope, scope.cwd, scope.cwd)
}

// nativeLocalCommandReadOnly reports whether a command tool runs only
// Claude Code's built-in read-only commands inside the workspace. Every
// subcommand of a compound command must qualify; output redirects (except to
// /dev/null), expansions, substitutions, subshells and environment
// assignments never do.
func nativeLocalCommandReadOnly(input json.RawMessage, scope nativeLocalScope) bool {
	var args map[string]any
	if json.Unmarshal(input, &args) != nil {
		return false
	}
	script, ok := nativeLocalScript(args)
	if !ok {
		return false
	}
	if dir, ok := args["workdir"].(string); ok && strings.TrimSpace(dir) != "" {
		if !strings.HasPrefix(dir, "/") {
			return false
		}
		scope.cwd = path.Clean(dir)
	}
	if !nativeLocalInside(scope, scope.cwd, scope.cwd) {
		return false
	}
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(script), "")
	if err != nil || len(file.Stmts) == 0 {
		return false
	}
	check := nativeLocalShell{scope: scope, cwd: scope.cwd}
	for _, stmt := range file.Stmts {
		if !check.stmt(stmt) {
			return false
		}
	}
	return true
}

// nativeLocalScript extracts the shell script of a command tool: a string
// command, or an argv that is either a shell's -c script or a plain command.
func nativeLocalScript(args map[string]any) (string, bool) {
	var value any
	for _, key := range []string{"command", "cmd"} {
		if v, ok := args[key]; ok {
			if value != nil {
				return "", false
			}
			value = v
		}
	}
	switch value := value.(type) {
	case string:
		return value, strings.TrimSpace(value) != ""
	case []any:
		argv := make([]string, 0, len(value))
		for _, item := range value {
			text, ok := item.(string)
			if !ok {
				return "", false
			}
			argv = append(argv, text)
		}
		if len(argv) == 0 {
			return "", false
		}
		if len(argv) == 3 && (argv[1] == "-c" || argv[1] == "-lc") {
			switch path.Base(argv[0]) {
			case "bash", "sh", "zsh":
				return argv[2], true
			}
		}
		quoted := make([]string, 0, len(argv))
		for _, arg := range argv {
			q, err := syntax.Quote(arg, syntax.LangBash)
			if err != nil {
				return "", false
			}
			quoted = append(quoted, q)
		}
		return strings.Join(quoted, " "), true
	}
	return "", false
}

type nativeLocalShell struct {
	scope nativeLocalScope
	cwd   string
	// moved records a cd into another directory; a later git then asks, as
	// in Claude Code, since that directory's hooks and config would run.
	moved bool
}

func (c *nativeLocalShell) stmt(stmt *syntax.Stmt) bool {
	if stmt == nil || stmt.Coprocess || stmt.Disown {
		return false
	}
	for _, redirect := range stmt.Redirs {
		if !c.redirect(redirect) {
			return false
		}
	}
	switch cmd := stmt.Cmd.(type) {
	case *syntax.CallExpr:
		return c.call(cmd)
	case *syntax.BinaryCmd:
		return c.stmt(cmd.X) && c.stmt(cmd.Y)
	}
	return false
}

func (c *nativeLocalShell) redirect(r *syntax.Redirect) bool {
	target, literal := nativeLocalWord(r.Word)
	switch r.Op {
	case syntax.DplIn, syntax.DplOut:
		return literal && (target == "-" || strings.Trim(target, "0123456789") == "")
	case syntax.RdrOut, syntax.AppOut, syntax.RdrClob, syntax.RdrAll, syntax.RdrAllClob, syntax.AppAll:
		return literal && target == "/dev/null"
	case syntax.RdrIn:
		return literal && nativeLocalInside(c.scope, c.cwd, target)
	case syntax.Hdoc, syntax.DashHdoc:
		if !literal || r.Hdoc == nil {
			return literal
		}
		for _, part := range r.Hdoc.Parts {
			if _, ok := part.(*syntax.Lit); !ok {
				return false
			}
		}
		return true
	case syntax.WordHdoc:
		return literal
	}
	return false
}

func (c *nativeLocalShell) call(call *syntax.CallExpr) bool {
	if len(call.Assigns) > 0 || len(call.Args) == 0 {
		return false
	}
	argv := make([]string, 0, len(call.Args))
	for _, word := range call.Args {
		text, ok := nativeLocalWord(word)
		if !ok {
			return false
		}
		argv = append(argv, text)
	}
	name, args := argv[0], argv[1:]
	switch name {
	case "echo", "pwd", "which":
		return true
	case "cd":
		if len(args) != 1 || args[0] == "-" || !nativeLocalInside(c.scope, c.cwd, args[0]) {
			return false
		}
		dir := args[0]
		if !strings.HasPrefix(dir, "/") {
			dir = path.Join(c.cwd, dir)
		}
		if dir = path.Clean(dir); dir != c.cwd {
			c.moved = true
		}
		c.cwd = dir
		return true
	case "find":
		for _, arg := range args {
			switch arg {
			case "-exec", "-execdir", "-ok", "-okdir", "-delete", "-fls", "-fprint", "-fprint0", "-fprintf":
				return false
			}
		}
		return c.paths(args)
	case "rg":
		for _, arg := range args {
			if arg == "--pre" || strings.HasPrefix(arg, "--pre=") {
				return false
			}
		}
		return c.paths(args)
	case "ls", "cat", "head", "tail", "wc", "diff", "stat", "du", "grep":
		return c.paths(args)
	case "git":
		return c.git(args)
	}
	return false
}

// paths checks that every argument naming a path stays in the workspace.
// Patterns and revisions are checked too: one that looks like an outside
// path only costs a confirmation.
func (c *nativeLocalShell) paths(args []string) bool {
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") {
			if _, value, ok := strings.Cut(arg, "="); ok && value != "" && !nativeLocalInside(c.scope, c.cwd, value) {
				return false
			}
			continue
		}
		if !nativeLocalInside(c.scope, c.cwd, arg) {
			return false
		}
	}
	return true
}

// nativeLocalGitReads are git subcommands that only read the repository.
var nativeLocalGitReads = map[string]bool{
	"status": true, "log": true, "show": true, "diff": true, "blame": true, "shortlog": true,
	"describe": true, "rev-parse": true, "ls-files": true, "ls-tree": true, "cat-file": true,
	"merge-base": true, "name-rev": true, "for-each-ref": true, "show-ref": true,
	"count-objects": true, "grep": true, "rev-list": true, "whatchanged": true,
}

func (c *nativeLocalShell) git(args []string) bool {
	if c.moved {
		return false
	}
	i := 0
	for ; i < len(args) && strings.HasPrefix(args[i], "-"); i++ {
		switch args[i] {
		case "--no-pager", "-P", "--no-optional-locks", "--literal-pathspecs", "--no-replace-objects":
		default:
			// -c, -C, --git-dir, --work-tree, --exec-path and the like.
			return false
		}
	}
	if i == len(args) {
		return false
	}
	sub, rest := args[i], args[i+1:]
	for _, arg := range rest {
		if arg == "--output" || strings.HasPrefix(arg, "--output=") || strings.HasPrefix(arg, "-O") || strings.HasPrefix(arg, "--open-files-in-pager") || arg == "--ext-diff" {
			return false
		}
	}
	switch {
	case nativeLocalGitReads[sub]:
		return c.paths(rest)
	case sub == "branch":
		return nativeLocalGitListing(rest, "-a", "--all", "-r", "--remotes", "-v", "-vv", "--verbose", "--show-current", "--color", "--no-color", "--column", "--no-column", "--abbrev", "--no-abbrev", "-i", "--ignore-case", "--omit-empty")
	case sub == "tag":
		return nativeLocalGitListing(rest, "-n", "--column", "--no-column", "--color", "-i", "--ignore-case", "--omit-empty")
	case sub == "remote":
		switch len(rest) {
		case 0:
			return true
		case 1:
			return rest[0] == "-v" || rest[0] == "--verbose"
		case 2:
			return (rest[0] == "get-url" || rest[0] == "show") && !strings.HasPrefix(rest[1], "-")
		}
		return false
	case sub == "worktree":
		if len(rest) == 0 || rest[0] != "list" {
			return false
		}
		for _, arg := range rest[1:] {
			if arg != "--porcelain" && arg != "-v" && arg != "--verbose" && arg != "-z" {
				return false
			}
		}
		return true
	case sub == "stash":
		return len(rest) > 0 && (rest[0] == "list" || rest[0] == "show") && c.paths(rest[1:])
	case sub == "config":
		return nativeLocalGitConfigRead(rest)
	}
	return false
}

// nativeLocalGitListing accepts branch/tag invocations that only list refs:
// no positional arguments unless --list asks to filter by pattern.
func nativeLocalGitListing(args []string, flags ...string) bool {
	allowed := map[string]bool{}
	for _, flag := range flags {
		allowed[flag] = true
	}
	valued := map[string]bool{"--contains": true, "--no-contains": true, "--merged": true, "--no-merged": true, "--points-at": true, "--sort": true, "--format": true}
	listing, positional := false, false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		name, _, hasValue := strings.Cut(arg, "=")
		switch {
		case arg == "-l" || arg == "--list":
			listing = true
		case valued[name]:
			if !hasValue {
				i++
			}
		case allowed[name] || (allowed["-n"] && strings.HasPrefix(arg, "-n") && strings.Trim(arg[2:], "0123456789") == ""):
		case strings.HasPrefix(arg, "-"):
			return false
		default:
			positional = true
		}
	}
	return listing || !positional
}

func nativeLocalGitConfigRead(args []string) bool {
	read := false
	for _, arg := range args {
		switch arg {
		case "--get", "--get-all", "--get-regexp", "--get-urlmatch", "--list", "-l":
			read = true
		case "get", "list":
			if read {
				return false
			}
			read = true
		case "--local", "--global", "--system", "--worktree", "--show-origin", "--show-scope", "--name-only", "--null", "-z", "--includes", "--no-includes", "--bool", "--int", "--path":
		default:
			if strings.HasPrefix(arg, "-") || !read {
				return false
			}
		}
	}
	return read
}

// nativeLocalWord returns a word's value when it has no expansion at all.
// Escapes are rejected rather than unquoted, and so are unquoted braces (brace
// expansion can name any path) and dot globs (older shells match "..").
func nativeLocalWord(word *syntax.Word) (string, bool) {
	if word == nil {
		return "", false
	}
	var text strings.Builder
	for _, part := range word.Parts {
		switch part := part.(type) {
		case *syntax.Lit:
			if strings.ContainsAny(part.Value, `\{`) || strings.Contains(part.Value, ".*") || strings.Contains(part.Value, ".?") || strings.Contains(part.Value, ".[") {
				return "", false
			}
			text.WriteString(part.Value)
		case *syntax.SglQuoted:
			if part.Dollar {
				return "", false
			}
			text.WriteString(part.Value)
		case *syntax.DblQuoted:
			if part.Dollar {
				return "", false
			}
			for _, inner := range part.Parts {
				lit, ok := inner.(*syntax.Lit)
				if !ok || strings.Contains(lit.Value, `\`) {
					return "", false
				}
				text.WriteString(lit.Value)
			}
		default:
			return "", false
		}
	}
	return text.String(), true
}
