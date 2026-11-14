package preview

import (
	_ "embed"
	"encoding/json"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

//go:embed devserver_intercept.js
var devServerInterceptJS string

// devServerScriptMax bounds rewritten scripts; pre-bundled dependencies of a
// dev server are larger than built HTML.
const devServerScriptMax = 16 << 20

// Root-absolute ES module specifiers: `from "/x"`, `import "/x"`, `import("/x")`.
var devServerImportRe = regexp.MustCompile(`(\bfrom\s*|\bimport\s*\(?\s*)(["'])(/[^/"'\s][^"'\s]*)(["'])`)
var devServerScriptRe = regexp.MustCompile(`(?is)(<script\b[^>]*>)(.*?)(</script>)`)

// RewriteDevServerHTML prepares a dev server document for its path prefix,
// the way build previews are served: root references in markup move under
// the prefix, the history shim hides the prefix from client routers, and the
// intercept script keeps runtime requests, sockets and cookies inside it. No
// <base> is injected: pages resolve relative URLs against their own address.
//
// Module specifiers must be rewritten, not just redirected: a module loaded
// through a redirect keys the module map by the root URL while import.meta.url
// is the prefixed one, and anything importing import.meta.url (React Refresh
// does) would evaluate the module a second time. Requests that still go to
// root paths are redirected under the prefix by the gateway.
func RewriteDevServerHTML(body []byte, prefix string, port int) []byte {
	if len(body) > maxRewrittenHTML {
		return body
	}
	prefix = strings.TrimRight(prefix, "/")
	html := rewriteReferrerMeta(string(body))
	html = devServerScriptRe.ReplaceAllStringFunc(html, func(m string) string {
		parts := devServerScriptRe.FindStringSubmatch(m)
		if len(parts) != 4 || strings.Contains(parts[1], "data-af-") {
			return m
		}
		return parts[1] + rewriteDevServerImports(parts[2], prefix) + parts[3]
	})
	html = rewriteRootRelativeRefs(html, prefix)
	html = rewriteCSSURLs(html, prefix)
	html = injectDevServerIntercept(html, prefix, port)
	html = injectPreviewHistory(html, prefix, true)
	return []byte(html)
}

// RewriteDevServerScript moves root-absolute module specifiers of a dev
// server script under the prefix.
func RewriteDevServerScript(body []byte, prefix string) []byte {
	if len(body) > devServerScriptMax {
		return body
	}
	prefix = strings.TrimRight(prefix, "/")
	src := rewriteDevServerImports(string(body), prefix)
	if strings.Contains(src, "__vite__css") {
		// Vite serves imported stylesheets as modules that inject a <style>.
		src = rewriteCSSURLs(src, prefix)
	}
	return []byte(src)
}

// RewriteDevServerCSS moves root url() references under the prefix.
func RewriteDevServerCSS(body []byte, prefix string) []byte {
	if len(body) > devServerScriptMax {
		return body
	}
	return []byte(rewriteCSSURLs(string(body), strings.TrimRight(prefix, "/")))
}

func rewriteDevServerImports(src, prefix string) string {
	if prefix == "" || !strings.Contains(src, "/") {
		return src
	}
	return devServerImportRe.ReplaceAllStringFunc(src, func(m string) string {
		parts := devServerImportRe.FindStringSubmatch(m)
		if len(parts) != 5 || parts[2] != parts[4] {
			return m
		}
		ref := parts[3]
		if ref == prefix || strings.HasPrefix(ref, prefix+"/") {
			return m
		}
		return parts[1] + parts[2] + prefix + ref + parts[4]
	})
}

// DevServerCookiePrefix namespaces the cookies of one dev server preview
// (its /devpreview/{run}/{port} prefix): the gateway forwards only these to
// the dev server, and the intercept script shows only these to the page.
// With the run in the name, two tasks serving the same port never share a
// cookie, whatever path it was set for.
func DevServerCookiePrefix(prefix string) string {
	var b strings.Builder
	b.WriteString("af_")
	for _, r := range strings.Trim(prefix, "/") {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	b.WriteByte('_')
	return b.String()
}

func injectDevServerIntercept(html, prefix string, port int) string {
	if strings.Contains(html, `data-af-dev-server`) || strings.Contains(devServerInterceptJS, "</script>") {
		return html
	}
	prefixJSON, err := json.Marshal(prefix)
	if err != nil {
		return html
	}
	cookieJSON, _ := json.Marshal(DevServerCookiePrefix(prefix))
	script := strings.NewReplacer("__AF_PREFIX__", string(prefixJSON), "__AF_PORT__", strconv.Itoa(port),
		"__AF_COOKIE__", string(cookieJSON)).Replace(devServerInterceptJS)
	tag := `<script data-af-dev-server>` + script + `</script>`
	// First thing in the document: module scripts and inline boot code must
	// already see the patched fetch/WebSocket.
	if out, ok := insertAfterHeadOpen(html, tag); ok {
		return out
	}
	if i := strings.Index(strings.ToLower(html), "<html"); i >= 0 {
		if end := strings.Index(html[i:], ">"); end >= 0 {
			at := i + end + 1
			return html[:at] + tag + html[at:]
		}
	}
	return tag + html
}

// RewriteDevServerLocation maps a redirect target of the dev server back
// under the prefix: root paths and http://localhost:PORT addresses move,
// other origins stay as they are.
func RewriteDevServerLocation(raw, prefix string, port int) string {
	raw = strings.TrimSpace(raw)
	prefix = strings.TrimRight(prefix, "/")
	if raw == "" {
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	if u.IsAbs() || strings.HasPrefix(raw, "//") {
		host := strings.ToLower(u.Hostname())
		local := host == "localhost" || host == "127.0.0.1" || host == "0.0.0.0" || host == "::1"
		p := u.Port()
		if p == "" {
			p = map[string]string{"https": "443"}[u.Scheme]
			if p == "" {
				p = "80"
			}
		}
		if !local || p != strconv.Itoa(port) {
			return raw
		}
		u.Scheme, u.Host, u.User = "", "", nil
	} else if !strings.HasPrefix(u.Path, "/") {
		return raw
	}
	if u.Path != prefix && !strings.HasPrefix(u.Path, prefix+"/") {
		u.Path = prefix + u.Path
		if u.RawPath != "" {
			u.RawPath = prefix + u.RawPath
		}
	}
	return u.String()
}
