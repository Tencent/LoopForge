package preview

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func historyShimFrom(t *testing.T, html string) string {
	t.Helper()
	const open = `<script data-af-preview-history>`
	i := strings.Index(html, open)
	if i < 0 {
		t.Fatalf("history shim missing: %s", html)
	}
	body := html[i+len(open):]
	end := strings.Index(body, "</script>")
	if end < 0 {
		t.Fatalf("history shim unterminated: %s", html)
	}
	return body[:end]
}

func TestRewritePreviewHTMLInjectsHistoryShim(t *testing.T) {
	got := string(rewritePreviewHTML([]byte(`<html><head><title>x</title></head><body>hi</body></html>`), "/preview/tok", "https://api.example.com", true))
	if want := strings.NewReplacer("__AF_PREFIX__", `"/preview/tok"`, "__AF_ROUTED__", "true").Replace(previewHistoryShimJS); historyShimFrom(t, got) != want {
		t.Fatalf("HTML rewrites must leave the history shim intact: %s", got)
	}
	base := strings.Index(got, `<base href="/preview/tok/">`)
	at := strings.Index(got, `<script data-af-preview-history>`)
	if base < 0 || at < base || at > strings.Index(got, "</head>") {
		t.Fatalf("history shim must follow <base> inside <head>: %s", got)
	}
	again := string(rewritePreviewHTML([]byte(got), "/preview/tok", "", true))
	if strings.Count(again, `data-af-preview-history`) != 1 {
		t.Fatalf("history shim should be idempotent: %s", again)
	}
	static := string(rewritePreviewHTML([]byte(`<html><head></head><body></body></html>`), "/preview/tok", "", false))
	if !strings.Contains(historyShimFrom(t, static), "var routed = false;") {
		t.Fatalf("pages without a router must keep the preview path: %s", static)
	}
}

func TestRewritePreviewHTMLPinsReferrerPolicy(t *testing.T) {
	src := `<html><head><meta name="referrer" content="no-referrer"><META content='origin' NAME='Referrer'><meta name=referrer content=strict-origin><meta name="description" content="referrer"><meta data-name="referrer" content="kept"></head><body></body></html>`
	got := string(rewritePreviewHTML([]byte(src), "/preview/tok", "", true))
	if n := strings.Count(got, `<meta name="referrer" content="same-origin">`); n != 3 {
		t.Fatalf("every referrer meta must be pinned to same-origin, got %d: %s", n, got)
	}
	for _, declared := range []string{`content="no-referrer"`, `content='origin'`, `content=strict-origin`} {
		if strings.Contains(got, declared) {
			t.Fatalf("page referrer policy %s survived: %s", declared, got)
		}
	}
	for _, kept := range []string{`<meta name="description" content="referrer">`, `<meta data-name="referrer" content="kept">`} {
		if !strings.Contains(got, kept) {
			t.Fatalf("unrelated meta %s must stay: %s", kept, got)
		}
	}
}

type countingStore struct {
	blobs map[string][]byte
	opens map[string]int
}

func (s *countingStore) Put(context.Context, string, io.Reader, int64, string) error { return nil }

func (s *countingStore) Open(_ context.Context, key string) (io.ReadSeekCloser, Object, error) {
	s.opens[key]++
	data, ok := s.blobs[key]
	if !ok {
		return nil, Object{}, os.ErrNotExist
	}
	return memBlob{bytes.NewReader(data)}, Object{Key: key, Size: int64(len(data))}, nil
}

func (s *countingStore) Delete(context.Context, string) error { return nil }

func TestRoutedBundleScansEachArchiveOnce(t *testing.T) {
	limits := Limits{MaxFiles: 20, MaxCompressed: 1 << 20, MaxExpanded: 1 << 20}
	st := &countingStore{blobs: map[string][]byte{
		"spa":    archiveOf(t, limits, map[string]string{"index.html": `<script type="module" src="/assets/app.js"></script>`, "assets/app.js": `history.pushState(null,"","/x")`}),
		"static": archiveOf(t, limits, map[string]string{"index.html": `<p>hi</p>`, "assets/app.js": `console.log(1)`}),
	}, opens: map[string]int{}}
	svc := &Service{store: st, cfg: Config{MaxFiles: limits.MaxFiles, MaxCompressed: limits.MaxCompressed, MaxExpanded: limits.MaxExpanded}}
	req := httptest.NewRequest("GET", "/preview/tok/", nil)
	for range 2 {
		if !svc.routedBundle(req, domain.RunPreview{StorageKey: "spa"}) {
			t.Fatal("a bundle with a router must boot on the app path")
		}
		if svc.routedBundle(req, domain.RunPreview{ContentSHA256: "static"}) {
			t.Fatal("a bundle without a router must keep the preview path")
		}
	}
	if st.opens["spa"] != 1 || st.opens["static"] != 1 {
		t.Fatalf("each bundle must be scanned once: %v", st.opens)
	}
	if !svc.routedBundle(req, domain.RunPreview{StorageKey: "gone"}) {
		t.Fatal("an unreadable bundle must fall back to the router behaviour")
	}
}

func TestStrayNavigationTargetReturnsToPreview(t *testing.T) {
	svc := &Service{cfg: Config{PublicHost: "loopforge.example.com"}}
	token := strings.Repeat("ab", 16)
	page := "https://loopforge.example.com/preview/" + token + "/skills"
	navigate := func(referer string) map[string]string {
		return map[string]string{"Sec-Fetch-Mode": "navigate", "Sec-Fetch-Site": "same-origin", "Referer": referer}
	}
	cases := []struct {
		name, method, target, host string
		headers                    map[string]string
		want                       string
	}{
		{"location.href to a root path", "GET", "/login?next=%2Fskills", "loopforge.example.com", navigate(page), "/preview/" + token + "/login?next=%2Fskills"},
		{"link to the site root", "GET", "/", "loopforge.example.com", navigate(page), "/preview/" + token + "/"},
		{"form post", "POST", "/api/login", "loopforge.example.com", navigate(page), "/preview/" + token + "/api/login"},
		{"plain HTTP without fetch metadata", "GET", "/plugins", "loopforge.example.com", map[string]string{"Accept": "text/html,application/xhtml+xml", "Referer": "http://loopforge.example.com/preview/" + token + "/"}, "/preview/" + token + "/plugins"},
		{"proxy rewrote Host", "GET", "/plugins", "127.0.0.1:8080", map[string]string{"Accept": "text/html", "Referer": "http://loopforge.example.com/preview/" + token + "/"}, "/preview/" + token + "/plugins"},
		{"subresource", "GET", "/logo.png", "loopforge.example.com", map[string]string{"Sec-Fetch-Mode": "no-cors", "Sec-Fetch-Site": "same-origin", "Referer": page}, ""},
		{"api call without fetch metadata", "GET", "/api/runs", "loopforge.example.com", map[string]string{"Accept": "application/json", "Referer": page}, ""},
		{"other site posing as a preview page", "GET", "/", "loopforge.example.com", map[string]string{"Sec-Fetch-Mode": "navigate", "Sec-Fetch-Site": "cross-site", "Referer": "https://evil.example/preview/" + token + "/"}, ""},
		{"other host without fetch metadata", "GET", "/", "loopforge.example.com", map[string]string{"Accept": "text/html", "Referer": "http://evil.example/preview/" + token + "/"}, ""},
		{"platform page", "GET", "/", "loopforge.example.com", navigate("https://loopforge.example.com/#/runs/r1"), ""},
		{"typed into the address bar", "GET", "/", "loopforge.example.com", map[string]string{"Sec-Fetch-Mode": "navigate", "Sec-Fetch-Site": "none"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.target, nil)
			req.Host = tc.host
			for k, v := range tc.headers {
				req.Header.Set(k, v)
			}
			got, ok := svc.StrayNavigationTarget(req)
			if got != tc.want || ok != (tc.want != "") {
				t.Fatalf("got %q %v, want %q", got, ok, tc.want)
			}
		})
	}
}

func TestPreviewHistoryShimKeepsRoutesUnderPreviewPath(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	prefix := "/preview/" + strings.Repeat("ab", 16)
	shim := func(routed bool) string {
		code, err := json.Marshal(historyShimFrom(t, string(rewritePreviewHTML([]byte(`<html><head></head><body></body></html>`), prefix, "", routed))))
		if err != nil {
			t.Fatal(err)
		}
		return string(code)
	}
	script := `
const assert = require('node:assert/strict');
const vm = require('node:vm');
const routedCode = ` + shim(true) + `;
const staticCode = ` + shim(false) + `;
const origin = 'https://loopforge.test';
const prefix = '` + prefix + `';
const other = '/preview/' + 'cd'.repeat(16);

function open(path, state, code = routedCode) {
  const listeners = [];
  const navigateListeners = [];
  const timers = [];
  const entries = [{ url: new URL(path, origin), state: state === undefined ? null : state }];
  let index = 0;
  const document = { baseURI: origin + prefix + '/' };
  const at = () => entries[index].url;
  const assigned = [];
  const replaced = [];
  const location = {
    get href() { return at().href; },
    get origin() { return at().origin; },
    get protocol() { return at().protocol; },
    get host() { return at().host; },
    get pathname() { return at().pathname; },
    get search() { return at().search; },
    get hash() { return at().hash; },
    assign(url) { assigned.push(new URL(url, document.baseURI).href); },
    replace(url) { replaced.push(new URL(url, document.baseURI).href); },
  };
  const target = url => {
    const next = new URL(String(url), document.baseURI);
    if (next.origin !== origin) throw new Error('SecurityError: ' + next.href);
    return next;
  };
  const history = {
    get state() { return structuredClone(entries[index].state); },
    pushState(state, title, url) {
      const next = url == null ? new URL(at()) : target(url);
      entries.splice(index + 1, entries.length, { url: next, state: structuredClone(state) });
      index++;
    },
    replaceState(state, title, url) {
      entries[index] = { url: url == null ? at() : target(url), state: structuredClone(state) };
    },
  };
  const window = {
    history, location,
    navigation: { addEventListener(type, fn) { if (type === 'navigate') navigateListeners.push(fn); } },
    addEventListener(type, fn, capture) { listeners.push({ type, fn, capture: !!capture }); },
  };
  const fire = async type => {
    for (const capture of [true, false]) {
      for (const l of listeners) {
        if (l.type !== type || l.capture !== capture) continue;
        l.fn({ type });
        // Browsers drain microtasks after each listener of an event they dispatch.
        for (let i = 0; i < 10; i++) await null;
      }
    }
  };
  // Long timers wait for elapse() so the suite never sleeps.
  const timer = (fn, ms) => (ms > 1000 ? timers.push(fn) : setTimeout(fn, ms));
  vm.runInNewContext(code, { window, document, URL, Promise, setTimeout: timer });
  return {
    window, history, location, fire, assigned, replaced,
    go(delta) { index += delta; return fire('popstate'); },
    elapse() { timers.splice(0).forEach(fn => fn()); },
    paths: () => entries.map(e => e.url.pathname + e.url.search + e.url.hash),
    click(attrs, init) {
      const link = {
        closest: () => link,
        getAttribute: name => (name in attrs ? attrs[name] : null),
        hasAttribute: name => name in attrs,
      };
      const ev = Object.assign({ button: 0, metaKey: false, ctrlKey: false, shiftKey: false, altKey: false, defaultPrevented: false, target: link, preventDefault() { ev.defaultPrevented = true; } }, init);
      for (const l of listeners) if (l.type === 'click') l.fn(ev);
      return ev.defaultPrevented;
    },
    navigate(url, init = {}) {
      const { sameDocument = false, ...rest } = init;
      const ev = Object.assign({ destination: { url: new URL(url, document.baseURI).href, sameDocument }, cancelable: true, formData: null, downloadRequest: null, navigationType: 'push', defaultPrevented: false, preventDefault() { ev.defaultPrevented = true; } }, rest);
      for (const fn of navigateListeners) fn(ev);
      return ev.defaultPrevented;
    },
  };
}
const settle = () => new Promise(resolve => setTimeout(resolve, 0));

(async () => {
  const tab = open(prefix + '/');
  assert.equal(tab.location.pathname, '/', 'router boots on its own root');
  tab.history.replaceState({ idx: 0 }, '');
  assert.equal(tab.location.pathname, '/');
  await settle();
  assert.equal(tab.location.pathname, prefix + '/');
  assert.deepEqual(tab.history.state, { idx: 0 });

  tab.history.pushState({ idx: 1 }, '', '/skills?tab=hot#top');
  assert.equal(tab.location.pathname, '/skills', 'router reads its own path right after the write');
  await settle();
  assert.deepEqual(tab.paths(), [prefix + '/', prefix + '/skills?tab=hot#top']);

  tab.history.replaceState({ current: '/skills' }, '', origin + '/skills?tab=hot#top');
  tab.history.pushState({ current: '/plugins' }, '', origin + '/plugins');
  await settle();
  assert.deepEqual(tab.paths(), [prefix + '/', prefix + '/skills?tab=hot#top', prefix + '/plugins']);

  let seen = '';
  tab.window.addEventListener('popstate', () => { seen = tab.location.pathname; });
  await tab.go(-1);
  assert.equal(seen, '/skills', 'popstate listeners see the app path');
  await settle();
  assert.equal(tab.location.pathname, prefix + '/skills');
  assert.deepEqual(tab.history.state, { current: '/skills' });
  await tab.go(-1);
  assert.equal(seen, '/');
  await settle();
  assert.equal(tab.location.pathname, prefix + '/');

  tab.history.pushState(null, '', prefix + '/kept');
  assert.equal(tab.location.pathname, prefix + '/kept');
  tab.history.pushState(null, '', 'relative');
  assert.equal(tab.location.pathname, prefix + '/relative');
  assert.throws(() => tab.history.pushState(null, '', 'https://elsewhere.test/x'), /SecurityError/);

  const redirect = open(prefix + '/');
  redirect.history.pushState(null, '', '/login');
  assert.equal(redirect.location.pathname, '/login');
  await settle();
  assert.deepEqual(redirect.paths(), [prefix + '/', prefix + '/login']);

  const late = open(prefix + '/skills?tab=hot#top', { idx: 2 });
  assert.equal(late.location.href, origin + '/skills?tab=hot#top', 'deep link boots on the app path');
  await late.fire('load');
  await settle();
  assert.equal(late.location.pathname, '/skills', 'a router started after load still reads its own path');
  late.history.replaceState({ idx: 2 }, '');
  await settle();
  assert.equal(late.location.href, origin + prefix + '/skills?tab=hot#top');
  assert.deepEqual(late.history.state, { idx: 2 });
  late.elapse();
  assert.equal(late.location.pathname, prefix + '/skills');

  const resumed = open(prefix + '/skills?tab=hot', { idx: 3 });
  assert.equal(resumed.location.pathname, '/skills', 'a router resuming its own state reads its own path');
  resumed.window.addEventListener('popstate', () => {});
  assert.equal(resumed.location.pathname, '/skills');
  await settle();
  assert.equal(resumed.location.href, origin + prefix + '/skills?tab=hot', 'listening for popstate ends the boot');
  assert.deepEqual(resumed.history.state, { idx: 3 });
  resumed.window.addEventListener('popstate', () => {});
  assert.equal(resumed.location.pathname, prefix + '/skills');

  const idle = open(prefix + '/skills');
  await idle.fire('load');
  idle.elapse();
  assert.equal(idle.location.pathname, prefix + '/skills', 'a router that never starts gives the address back');

  const page = open(prefix + '/about?x=1', null, staticCode);
  assert.equal(page.location.pathname, prefix + '/about', 'pages without a router keep their address');
  await page.fire('load');
  page.elapse();
  assert.equal(page.location.pathname, prefix + '/about');
  assert.equal(page.click({ href: '/contact' }), true);
  assert.deepEqual(page.assigned, [origin + prefix + '/contact']);

  const links = open(prefix + '/');
  assert.equal(links.click({ href: '/guide?x=1#h' }), true, 'the unprefixed address carries no usable Referer');
  assert.deepEqual(links.paths(), [prefix + '/'], 'the page leaves its own entry under the preview path');
  assert.equal(links.click({ href: '/plugins', rel: 'noopener noreferrer' }), true);
  assert.deepEqual(links.assigned, [origin + prefix + '/guide?x=1#h', origin + prefix + '/plugins']);
  for (const [why, attrs, init] of [
    ['router handled the click', { href: '/plugins' }, { defaultPrevented: true }],
    ['modifier click', { href: '/plugins' }, { ctrlKey: true }],
    ['new window', { href: '/plugins', target: '_blank' }],
    ['download', { href: '/report.csv', download: '' }],
    ['already under the preview path', { href: prefix + '/plugins' }],
    ['another preview', { href: other + '/' }],
    ['other origin', { href: 'https://elsewhere.test/' }],
    ['blob URL', { href: 'blob:' + origin + '/0f3c' }],
  ]) {
    assert.equal(links.click(attrs, init), !!(init && init.defaultPrevented), why);
  }
  assert.equal(links.assigned.length, 2);

  const scripted = open(prefix + '/');
  assert.equal(scripted.navigate('/login?next=%2F'), true, 'location.href while booting');
  assert.deepEqual(scripted.paths(), [prefix + '/'], 'the page leaves its own entry under the preview path');
  assert.equal(scripted.navigate('/signin', { navigationType: 'replace' }), true);
  assert.equal(scripted.navigate('/', { navigationType: 'reload' }), true);
  assert.deepEqual(scripted.assigned, [origin + prefix + '/login?next=%2F']);
  assert.deepEqual(scripted.replaced, [origin + prefix + '/signin', origin + prefix + '/']);
  for (const [why, url, init] of [
    ['history write', '/plugins', { sameDocument: true }],
    ['browser-initiated', '/plugins', { cancelable: false }],
    ['traversal', '/plugins', { navigationType: 'traverse' }],
    ['form post', '/api/login', { formData: {} }],
    ['download', '/report.csv', { downloadRequest: '' }],
    ['already under the preview path', prefix + '/plugins'],
    ['another preview', other + '/'],
    ['other origin', 'https://elsewhere.test/'],
    ['blob URL', 'blob:' + origin + '/0f3c'],
  ]) {
    assert.equal(scripted.navigate(url, init), false, why);
  }
  assert.equal(scripted.assigned.length + scripted.replaced.length, 3);
})().catch(err => { console.error(err); process.exit(1); });
`
	out, err := exec.Command(node, "-e", script).CombinedOutput()
	if err != nil {
		t.Fatalf("history shim behaviour: %v\n%s", err, out)
	}
}
