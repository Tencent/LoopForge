package preview

import (
	"strings"
	"testing"
)

func TestRewriteDevServerHTML(t *testing.T) {
	in := `<!doctype html><html><head><meta name="referrer" content="no-referrer"><script type="module" src="/@vite/client"></script></head><body><img src="/logo.svg"></body></html>`
	out := string(RewriteDevServerHTML([]byte(in), "/devpreview/r/5173/", 5173))
	intercept := strings.Index(out, "<script data-af-dev-server>")
	if intercept < 0 || intercept > strings.Index(out, `src="/devpreview/r/5173/@vite/client"`) {
		t.Fatalf("intercept must run before page scripts: %s", out)
	}
	if !strings.Contains(out, `var root = "/devpreview/r/5173"`) || !strings.Contains(out, `String(5173)`) {
		t.Fatalf("intercept placeholders: %s", out)
	}
	if !strings.Contains(out, "data-af-preview-history") || !strings.Contains(out, `content="same-origin"`) {
		t.Fatalf("history shim / referrer: %s", out)
	}
	if !strings.Contains(out, `src="/devpreview/r/5173/logo.svg"`) || !strings.Contains(out, `src="/devpreview/r/5173/@vite/client"`) || strings.Contains(out, "<base") {
		t.Fatalf("markup references: %s", out)
	}
	if again := string(RewriteDevServerHTML([]byte(out), "/devpreview/r/5173", 5173)); strings.Count(again, "data-af-dev-server") != 1 {
		t.Fatal("rewrite must be idempotent")
	}
}

func TestRewriteDevServerModuleSpecifiers(t *testing.T) {
	prefix := "/devpreview/r/5173"
	html := string(RewriteDevServerHTML([]byte(`<head><script type="module">import { injectIntoGlobalHook } from "/@react-refresh";</script></head>`), prefix, 5173))
	if !strings.Contains(html, `from "/devpreview/r/5173/@react-refresh"`) {
		t.Fatalf("inline module: %s", html)
	}
	js := `import React from "/node_modules/.vite/deps/react.js?v=1";import "/src/a.css";import{x}from'/src/x.ts';` +
		`const m = import ( "/src/lazy.tsx" );export * from "/src/re.ts";` +
		`Array.from("/not/a/module");fetch("/api");importScripts("/sw.js");import "//cdn.example.com/a.js";import "./rel.js";` +
		`import "/devpreview/r/5173/done.js";`
	out := string(RewriteDevServerScript([]byte(js), prefix))
	for _, want := range []string{
		`from "/devpreview/r/5173/node_modules/.vite/deps/react.js?v=1"`,
		`import "/devpreview/r/5173/src/a.css"`,
		`from'/devpreview/r/5173/src/x.ts'`,
		`import ( "/devpreview/r/5173/src/lazy.tsx" )`,
		`export * from "/devpreview/r/5173/src/re.ts"`,
		`Array.from("/not/a/module")`,
		`fetch("/api")`,
		`importScripts("/sw.js")`,
		`import "//cdn.example.com/a.js"`,
		`import "./rel.js"`,
		`import "/devpreview/r/5173/done.js"`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %s in %s", want, out)
		}
	}
	viteCSS := string(RewriteDevServerScript([]byte(`const __vite__css = ".card{background:url(/bg.svg)}"`), prefix))
	if !strings.Contains(viteCSS, `url(/devpreview/r/5173/bg.svg)`) {
		t.Fatalf("vite css module: %s", viteCSS)
	}
	if plain := string(RewriteDevServerScript([]byte(`const s = "url(/x)"`), prefix)); plain != `const s = "url(/x)"` {
		t.Fatalf("plain script urls must stay: %s", plain)
	}
	if css := string(RewriteDevServerCSS([]byte(`.a{background:url(/bg.svg)}`), prefix)); css != `.a{background:url(/devpreview/r/5173/bg.svg)}` {
		t.Fatalf("css: %s", css)
	}
}

func TestRewriteDevServerLocation(t *testing.T) {
	prefix := "/devpreview/r/5173"
	for raw, want := range map[string]string{
		"/login?next=/a":                prefix + "/login?next=/a",
		"http://localhost:5173/home#x":  prefix + "/home#x",
		"http://127.0.0.1:5173":         prefix,
		prefix + "/already":             prefix + "/already",
		"http://localhost:3000/other":   "http://localhost:3000/other",
		"https://sso.example.com/login": "https://sso.example.com/login",
		"relative/path":                 "relative/path",
	} {
		if got := RewriteDevServerLocation(raw, prefix, 5173); got != want {
			t.Fatalf("%q: got %q want %q", raw, got, want)
		}
	}
}

func TestDevServerCookiePrefixSeparatesRunsOnOnePort(t *testing.T) {
	a, b := DevServerCookiePrefix("/devpreview/run-a/3000"), DevServerCookiePrefix("/devpreview/run-b/3000/")
	if a != "af_devpreview_run-a_3000_" || b != "af_devpreview_run-b_3000_" {
		t.Fatalf("prefixes %q %q", a, b)
	}
	if got := DevServerCookiePrefix("/devpreview/r.x;y/file"); got != "af_devpreview_r_x_y_file_" {
		t.Fatalf("cookie names allow only token characters: %q", got)
	}
	html := string(RewriteDevServerHTML([]byte("<html><head></head><body></body></html>"), "/devpreview/run-a/3000", 3000))
	if !strings.Contains(html, `var cookiePrefix = "af_devpreview_run-a_3000_"`) || strings.Contains(html, "__AF_") {
		t.Fatalf("intercept script must carry the run's cookie namespace")
	}
}
