package preview

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"

	"agentflow/internal/domain"
)

//go:embed backend_intercept.js
var previewBackendInterceptJS string

//go:embed history_shim.js
var previewHistoryShimJS string

const maxRewrittenHTML = 2 << 20

func init() {
	_ = mime.AddExtensionType(".js", "text/javascript; charset=utf-8")
	_ = mime.AddExtensionType(".mjs", "text/javascript; charset=utf-8")
	_ = mime.AddExtensionType(".css", "text/css; charset=utf-8")
	_ = mime.AddExtensionType(".json", "application/json")
	_ = mime.AddExtensionType(".svg", "image/svg+xml")
	_ = mime.AddExtensionType(".wasm", "application/wasm")
	_ = mime.AddExtensionType(".map", "application/json")
	_ = mime.AddExtensionType(".woff2", "font/woff2")
}

var rootAttrRe = regexp.MustCompile(`(?i)(\s(?:src|href|poster|action)\s*=\s*(['"]))(/[^/][^'"]*)`)
var jsRootAssetRe = regexp.MustCompile("([`'\"])(/(?:assets|static|chunks|media|fonts|css|js|img|images)(?:/[^`'\"]*)?)")
var referrerMetaRe = regexp.MustCompile(`(?i)<meta\b[^>]*?\sname\s*=\s*(?:"referrer"|'referrer'|referrer\b)[^>]*>`)

func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	token, rel, ok := ParsePreviewPath(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}
	prefix := PreviewBasePath(token)
	if strings.TrimSuffix(r.URL.Path, "/") == prefix && !strings.HasSuffix(r.URL.Path, "/") {
		http.Redirect(w, r, prefix+"/", http.StatusFound)
		return
	}
	session, preview, ok := s.LookupSession(token)
	if !ok {
		http.Error(w, "preview session expired", http.StatusForbidden)
		return
	}
	s.RenewSession(session)
	if rel == "" || strings.HasSuffix(r.URL.Path, "/") {
		rel = preview.EntryFile
	}
	body, name, err := s.memberBytes(r, preview, rel)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	ext := strings.ToLower(path.Ext(name))
	if ext == ".html" || ext == ".htm" {
		body = rewritePreviewHTML(body, prefix, session.BackendURL, s.routedBundle(r, preview))
	} else if ext == ".css" {
		body = []byte(rewriteCSSURLs(string(body), prefix))
	} else if ext == ".js" || ext == ".mjs" || ext == ".cjs" {
		body = []byte(rewriteJSRootURLs(string(body), prefix))
	}
	s.writePreviewHeaders(w, name, session.BackendURL)
	http.ServeContent(w, r, name, preview.UpdatedAt, bytes.NewReader(body))
}

// StrayNavigationTarget maps a page navigation that a preview document sent
// to a root path (location.href = "/login", a plain <a href="/x">, a form)
// back under that preview's session path. It relies on preview responses
// sending Referrer-Policy: same-origin.
func (s *Service) StrayNavigationTarget(r *http.Request) (string, bool) {
	if !isNavigation(r) {
		return "", false
	}
	ref, err := url.Parse(r.Referer())
	if err != nil || ref.Host == "" {
		return "", false
	}
	token, _, ok := ParsePreviewPath(ref.Path)
	if !ok {
		return "", false
	}
	switch r.Header.Get("Sec-Fetch-Site") {
	case "same-origin":
	case "":
		// Browsers only send fetch metadata over HTTPS; plain-HTTP
		// deployments fall back to comparing hosts.
		if !strings.EqualFold(ref.Host, r.Host) && !strings.EqualFold(ref.Host, s.cfg.PublicHost) {
			return "", false
		}
	default:
		return "", false
	}
	target := PreviewBasePath(token) + r.URL.EscapedPath()
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	return target, true
}

func isNavigation(r *http.Request) bool {
	if mode := r.Header.Get("Sec-Fetch-Mode"); mode != "" {
		return mode == "navigate"
	}
	return (r.Method == http.MethodGet || r.Method == http.MethodHead) && strings.Contains(r.Header.Get("Accept"), "text/html")
}

func (s *Service) memberBytes(r *http.Request, preview domain.RunPreview, rel string) ([]byte, string, error) {
	safe, err := SanitizeArchiveMember(rel)
	if err != nil {
		return nil, "", err
	}
	body, err := s.readMember(r, preview, safe)
	if err == nil {
		return body, safe, nil
	}
	if preview.SPAFallback && acceptsHTML(r) {
		body, err = s.readMember(r, preview, preview.EntryFile)
		if err == nil {
			return body, preview.EntryFile, nil
		}
	}
	return nil, "", err
}

func bundleKey(preview domain.RunPreview) string {
	if preview.StorageKey != "" {
		return preview.StorageKey
	}
	return preview.ContentSHA256
}

// routedBundle reports whether the preview ships a client-side router; pages
// without one keep their real address from the start. Bundles are
// content-addressed, so each key is scanned once.
func (s *Service) routedBundle(r *http.Request, preview domain.RunPreview) bool {
	key := bundleKey(preview)
	s.routedMu.Lock()
	routed, ok := s.routed[key]
	s.routedMu.Unlock()
	if ok {
		return routed
	}
	obj, _, err := s.store.Open(r.Context(), key)
	if err != nil {
		return true
	}
	defer obj.Close()
	routed, err = UsesHistoryAPI(obj, s.limits())
	if err != nil {
		return true
	}
	s.routedMu.Lock()
	if s.routed == nil {
		s.routed = map[string]bool{}
	}
	s.routed[key] = routed
	s.routedMu.Unlock()
	return routed
}

func (s *Service) readMember(r *http.Request, preview domain.RunPreview, name string) ([]byte, error) {
	key := bundleKey(preview)
	if key == "" {
		return nil, os.ErrNotExist
	}
	obj, _, err := s.store.Open(r.Context(), key)
	if err != nil {
		return nil, err
	}
	defer obj.Close()
	body, err := ReadTarZstMember(obj, name, s.limits())
	if errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return body, err
}

func (s *Service) writePreviewHeaders(w http.ResponseWriter, name, backend string) {
	ext := strings.ToLower(path.Ext(name))
	ctype := mime.TypeByExtension(ext)
	if ctype == "" {
		ctype = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "same-origin")
	w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), clipboard-read=(), clipboard-write=()")
	w.Header().Set("Content-Security-Policy", s.previewCSP(backend))
	w.Header().Set("Cache-Control", "no-cache")
	if ext != ".html" && ext != ".htm" && strings.Contains(name, ".") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	}
}

func (s *Service) previewCSP(backend string) string {
	ancestors := strings.TrimSpace(s.cfg.FrameAncestors)
	if ancestors == "" {
		ancestors = "'self'"
	} else if !strings.Contains(ancestors, "'self'") {
		ancestors = "'self' " + ancestors
	}
	connect := "'none'"
	if origin := backendOrigin(backend); origin != "" {
		connect = "'self' " + origin
	}
	return "sandbox allow-scripts allow-forms allow-modals allow-downloads allow-same-origin; frame-ancestors " + ancestors + "; base-uri 'self'; connect-src " + connect + "; form-action 'self'"
}

func backendOrigin(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

func acceptsHTML(r *http.Request) bool {
	accept := r.Header.Get("Accept")
	return accept == "" || strings.Contains(accept, "text/html") || strings.Contains(accept, "*/*")
}

func rewritePreviewHTML(body []byte, prefix, backend string, routed bool) []byte {
	if len(body) > maxRewrittenHTML {
		return body
	}
	html := injectBaseTag(string(body), prefix)
	html = rewriteReferrerMeta(html)
	html = injectPreviewViewport(html)
	html = injectPreviewBackend(html, prefix, backend)
	html = injectPreviewHistory(html, prefix, routed)
	html = rewriteRootRelativeRefs(html, prefix)
	html = rewriteCSSURLs(html, prefix)
	return []byte(html)
}

func injectBaseTag(html, prefix string) string {
	if strings.Contains(strings.ToLower(html), "<base") {
		return html
	}
	href := prefix
	if href == "" {
		href = "/"
	}
	if !strings.HasSuffix(href, "/") {
		href += "/"
	}
	tag := `<base href="` + href + `">`
	lower := strings.ToLower(html)
	if i := strings.Index(lower, "<head>"); i >= 0 {
		at := i + len("<head>")
		return html[:at] + tag + html[at:]
	}
	if i := strings.Index(lower, "<head "); i >= 0 {
		if end := strings.Index(html[i:], ">"); end >= 0 {
			at := i + end + 1
			return html[:at] + tag + html[at:]
		}
	}
	return tag + html
}

// A page's own <meta name="referrer"> overrides the response header, and
// StrayNavigationTarget needs the same-origin Referer.
func rewriteReferrerMeta(html string) string {
	return referrerMetaRe.ReplaceAllLiteralString(html, `<meta name="referrer" content="same-origin">`)
}

func injectPreviewViewport(html string) string {
	const tag = `<style data-af-preview-viewport>html,body{height:100%;min-height:100%;margin:0}</style>`
	if strings.Contains(html, `data-af-preview-viewport`) {
		return html
	}
	lower := strings.ToLower(html)
	if i := strings.Index(lower, "</head>"); i >= 0 {
		return html[:i] + tag + html[i:]
	}
	return tag + html
}

func injectPreviewBackend(html, prefix, backend string) string {
	backend = strings.TrimSpace(backend)
	if backend == "" || strings.Contains(html, `data-af-preview-backend`) {
		return html
	}
	raw, err := json.Marshal(backend)
	if err != nil {
		return html
	}
	prefixJSON, err := json.Marshal(strings.TrimRight(prefix, "/"))
	if err != nil {
		return html
	}
	if strings.Contains(previewBackendInterceptJS, "</script>") {
		return html
	}
	// Classic script at the start of <head> runs before deferred module
	// bundles. Axios/jQuery/ky and the rest go through fetch or XHR;
	// sendBeacon/EventSource/WebSocket are patched the same way. Preview
	// static files under prefix stay local; every other http(s) call is
	// sent to the configured backend origin with path/query unchanged.
	intercept := strings.Replace(previewBackendInterceptJS, "__AF_PREFIX__", string(prefixJSON), 1)
	tag := `<script data-af-preview-backend>window.__AGENTFLOW_PREVIEW_BACKEND__=` + string(raw) + `;` + intercept + `</script>`
	lower := strings.ToLower(html)
	if i := strings.Index(lower, "<head>"); i >= 0 {
		at := i + len("<head>")
		return html[:at] + tag + html[at:]
	}
	if i := strings.Index(lower, "<head "); i >= 0 {
		if end := strings.Index(html[i:], ">"); end >= 0 {
			at := i + end + 1
			return html[:at] + tag + html[at:]
		}
	}
	return tag + html
}

func injectPreviewHistory(html, prefix string, routed bool) string {
	prefix = strings.TrimRight(prefix, "/")
	if prefix == "" || strings.Contains(html, `data-af-preview-history`) || strings.Contains(previewHistoryShimJS, "</script>") {
		return html
	}
	prefixJSON, err := json.Marshal(prefix)
	if err != nil {
		return html
	}
	// SPA routers built for "/" push root paths and read location right
	// after the write, so the shim lets the write land and moves the address
	// back under the preview path once the current task ends. Popstate, and
	// in routed bundles the boot up to the router's first write, hand the
	// router the path without the prefix. It goes at the end of <head> so
	// any <base> has resolved against the real preview URL.
	shim := strings.NewReplacer("__AF_PREFIX__", string(prefixJSON), "__AF_ROUTED__", strconv.FormatBool(routed)).Replace(previewHistoryShimJS)
	tag := `<script data-af-preview-history>` + shim + `</script>`
	lower := strings.ToLower(html)
	if i := strings.Index(lower, "</head>"); i >= 0 {
		return html[:i] + tag + html[i:]
	}
	return tag + html
}

func rewriteRootRelativeRefs(html, prefix string) string {
	if prefix == "" {
		return html
	}
	return rootAttrRe.ReplaceAllStringFunc(html, func(m string) string {
		parts := rootAttrRe.FindStringSubmatch(m)
		if len(parts) != 4 {
			return m
		}
		ref := parts[3]
		if strings.HasPrefix(ref, prefix+"/") || ref == prefix {
			return m
		}
		return parts[1] + prefix + ref
	})
}

func rewriteCSSURLs(s, prefix string) string {
	var b strings.Builder
	i := 0
	for {
		j := strings.Index(s[i:], "url(")
		if j < 0 {
			b.WriteString(s[i:])
			break
		}
		j += i
		b.WriteString(s[i : j+4])
		k := j + 4
		for k < len(s) && (s[k] == ' ' || s[k] == '\t') {
			b.WriteByte(s[k])
			k++
		}
		if k < len(s) && (s[k] == '\'' || s[k] == '"') {
			b.WriteByte(s[k])
			k++
		}
		if k < len(s) && s[k] == '/' && (k+1 >= len(s) || s[k+1] != '/') {
			if !strings.HasPrefix(s[k:], prefix) {
				b.WriteString(prefix)
			}
		}
		i = k
	}
	return b.String()
}

// rewriteJSRootURLs prefixes quoted root-relative asset paths inside JS
// bundles. Dynamic concatenation and non-asset routes such as /api stay
// untouched; builds should still use a relative base for those cases.
func rewriteJSRootURLs(s, prefix string) string {
	if prefix == "" || len(s) > maxRewrittenHTML {
		return s
	}
	return jsRootAssetRe.ReplaceAllStringFunc(s, func(m string) string {
		parts := jsRootAssetRe.FindStringSubmatch(m)
		if len(parts) != 3 {
			return m
		}
		ref := parts[2]
		if strings.HasPrefix(ref, prefix+"/") || ref == prefix {
			return m
		}
		return parts[1] + prefix + ref
	})
}
