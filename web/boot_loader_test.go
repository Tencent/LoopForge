package web

import (
	"regexp"
	"strings"
	"testing"
)

func TestBootLoaderIsFirstPaint(t *testing.T) {
	html := readThemeSource(t, "index.html")

	if !regexp.MustCompile(`<body class="booting">\s*<div id="bootLoader"[^>]*>`).MatchString(html) {
		t.Fatal(`index.html should open <body class="booting"> with #bootLoader as its first child`)
	}
	loaderTag := regexp.MustCompile(`<div id="bootLoader"[^>]*>`).FindString(html)
	for _, want := range []string{`role="status"`, `aria-live="polite"`, `aria-busy="true"`} {
		if !strings.Contains(loaderTag, want) {
			t.Errorf("#bootLoader should carry %s", want)
		}
	}
	if !strings.Contains(html, "正在进入工作台…") {
		t.Error("#bootLoader should show 正在进入工作台…")
	}
	if !regexp.MustCompile(`body\.booting \.app-shell\{visibility:hidden\}`).MatchString(html) {
		t.Error("the shell must stay hidden while body.booting is set")
	}
	if !regexp.MustCompile(`@media \(prefers-reduced-motion:reduce\)\{[^}]*\.boot-loader-spin\{animation:none\}`).MatchString(html) {
		t.Error("the spinner must stop under prefers-reduced-motion")
	}

	head := html[:strings.Index(html, "</head>")]
	if strings.Contains(head, "<script>") {
		t.Error("CSP script-src 'self' blocks inline scripts; load the early theme from a file")
	}
	themeTag := regexp.MustCompile(`<script src="/boot-theme\.js(\?[^"]*)?"></script>`).FindStringIndex(head)
	if themeTag == nil {
		t.Fatal("<head> should load /boot-theme.js synchronously")
	}
	if themeTag[0] > strings.Index(head, `<link rel="stylesheet"`) {
		t.Error("early theme script should run before the stylesheets are requested")
	}
	if !strings.Contains(readThemeSource(t, "embed.go"), " boot-theme.js ") {
		t.Error("boot-theme.js must be embedded")
	}
	script := readThemeSource(t, "boot-theme.js")
	for _, want := range []string{"'agentflow.theme.v3'", "'agentflow.native-chat.theme'", "prefers-color-scheme: dark", "document.documentElement.dataset.theme"} {
		if !strings.Contains(script, want) {
			t.Errorf("boot-theme.js should contain %q", want)
		}
	}
	if strings.Contains(script, "setItem") || strings.Contains(script, "removeItem") {
		t.Error("boot-theme.js must only read localStorage; migration stays in app.js applyTheme")
	}
}

func TestBootLoaderClosesAfterShellOrChatReady(t *testing.T) {
	app := readThemeSource(t, "app.js")
	start := readThemeSource(t, "app/start.js")

	if !strings.Contains(app, "BOOT_LOADER_CHAT_TIMEOUT_MS=8000") {
		t.Error("app.js should cap the chat-ready wait at 8000ms")
	}
	if !regexp.MustCompile(`(?s)function settleBootLoader\(\)\{.*chat-next-active.*nativeChatReady.*finishBootLoader\(\).*setTimeout\(finishBootLoader,BOOT_LOADER_CHAT_TIMEOUT_MS\)`).MatchString(app) {
		t.Error("settleBootLoader should close immediately outside the AI workbench and otherwise arm the 8s fallback")
	}
	if !regexp.MustCompile(`switchView\(view\);\s*settleBootLoader\(\);`).MatchString(app) {
		t.Error("boot() should settle the loader right after the first switchView")
	}
	if !regexp.MustCompile(`(?s)message\.type==='agentflow-chat-ready'\)\{\s*nativeChatReady=true;.*?finishBootLoader\(\);`).MatchString(app) {
		t.Error("agentflow-chat-ready should record readiness and close the loader")
	}
	if !regexp.MustCompile(`(?s)function finishBootLoader\(\)\{\s*clearTimeout\(bootLoaderTimer\).*classList\.remove\('booting'\)`).MatchString(app) {
		t.Error("finishBootLoader should clear the fallback timer and drop body.booting")
	}
	if !strings.Contains(app, "addEventListener('pageshow',event=>{if(event.persisted)finishBootLoader()})") {
		t.Error("a bfcache restore should not leave the loader on screen")
	}
	if !regexp.MustCompile(`finishBootLoader\(\);\s*document\.body\.innerHTML=`).MatchString(app) {
		t.Error("the local acceptance login screen should drop the loader before replacing body")
	}
	if !strings.Contains(start, "boot().catch(e=>{finishBootLoader();document.body.innerHTML=") {
		t.Error("the boot failure page should drop the loader before replacing body")
	}
}
