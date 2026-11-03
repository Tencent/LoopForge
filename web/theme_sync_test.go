package web

import (
	"os"
	"strings"
	"testing"
)

func readThemeSource(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestShellAndChatShareThemePreference(t *testing.T) {
	app := readThemeSource(t, "app.js")
	flows := readThemeSource(t, "app/flows.js")
	theme := readThemeSource(t, "../chatbot/src/theme.js")
	chat := readThemeSource(t, "../chatbot/src/App.jsx")

	for name, src := range map[string]string{"app.js": app, "chatbot/src/theme.js": theme} {
		if !strings.Contains(src, `"agentflow.theme.v3"`) && !strings.Contains(src, `'agentflow.theme.v3'`) {
			t.Errorf("%s should define the shared agentflow.theme.v3 key", name)
		}
	}
	if strings.Contains(chat, "agentflow.native-chat.theme") {
		t.Error("App.jsx must not read or write the legacy agentflow.native-chat.theme key")
	}
	for _, want := range []string{"readThemePreference(localStorage)", "writeThemePreference(localStorage, theme)", "event.key === THEME_KEY"} {
		if !strings.Contains(chat, want) {
			t.Errorf("App.jsx should contain %q", want)
		}
	}

	for _, want := range []string{
		"matchMedia('(prefers-color-scheme: dark)')",
		"themeMedia.addEventListener('change'",
		"window.addEventListener('storage'",
		"event.key===THEME_KEY",
		"localStorage.removeItem(LEGACY_THEME_KEY)",
	} {
		if !strings.Contains(app, want) {
			t.Errorf("app.js should contain %q", want)
		}
	}
	if strings.Contains(app, "dataset.theme=savedTheme") {
		t.Error("app.js should resolve the preference instead of writing it to data-theme directly")
	}

	toggle := flows[strings.Index(flows, "function toggleTheme(){"):]
	toggle = toggle[:strings.Index(toggle, "\n")]
	if !strings.Contains(toggle, "localStorage.setItem(THEME_KEY,theme)") || !strings.Contains(toggle, "applyTheme(theme)") {
		t.Errorf("toggleTheme should write the shared key and apply it, got %s", toggle)
	}
	if strings.Contains(toggle, "toast(") {
		t.Errorf("toggleTheme should switch silently without a toast, got %s", toggle)
	}
}

func sourceLine(t *testing.T, src, prefix string) string {
	t.Helper()
	start := strings.Index(src, prefix)
	if start < 0 {
		t.Fatalf("missing %q", prefix)
	}
	line := src[start:]
	if end := strings.Index(line, "\n"); end >= 0 {
		line = line[:end]
	}
	return line
}

func TestProfilePopoverOwnsThemeSwitcher(t *testing.T) {
	app := readThemeSource(t, "app.js")
	flows := readThemeSource(t, "app/flows.js")
	workbench := readThemeSource(t, "app/workbench.js")
	tabs := readThemeSource(t, "app/sliding-tabs.js")
	platform := readThemeSource(t, "platform.css")
	product := readThemeSource(t, "product-v4.css")

	set := sourceLine(t, flows, "function setThemePreference(")
	for _, want := range []string{"['light','dark','system'].includes(preference)", "localStorage.setItem(THEME_KEY,preference)", "applyTheme(preference)"} {
		if !strings.Contains(set, want) {
			t.Errorf("setThemePreference should contain %q, got %s", want, set)
		}
	}
	segment := sourceLine(t, flows, "function renderThemeSegment(")
	for _, want := range []string{"[data-theme-pref]", "aria-checked", "AgentFlowSlidingTabs", ".refresh()"} {
		if !strings.Contains(segment, want) {
			t.Errorf("renderThemeSegment should contain %q", want)
		}
	}
	if !strings.Contains(sourceLine(t, flows, "function renderThemeToggle("), "renderThemeSegment()") {
		t.Error("renderThemeToggle should refresh the profile switcher so storage and system changes stay in sync")
	}

	apply := sourceLine(t, app, "function applyTheme(")
	for _, want := range []string{"theme-fading", "prefers-reduced-motion: reduce", "previous!==next"} {
		if !strings.Contains(apply, want) {
			t.Errorf("applyTheme should contain %q, got %s", want, apply)
		}
	}
	if !strings.Contains(app, "profileWrap.addEventListener('mouseenter',()=>snapThemeSegment())") || !strings.Contains(app, "renderThemeSegment({snap:true})") {
		t.Error("opening the profile card should re-measure the switcher once it becomes visible")
	}

	popover := workbench[strings.Index(workbench, "function renderUserProfilePopover()"):strings.Index(workbench, "function userAvatarHTML(")]
	if got := strings.Count(popover, "${themeSegmentHTML()}"); got != 2 {
		t.Errorf("guest and signed-in cards should both render the switcher, got %d", got)
	}
	for _, want := range []string{
		`role="radiogroup"`, `role="radio"`, `data-theme-pref="${value}"`,
		`['light', 'sun', '浅色']`, `['dark', 'moon', '深色']`, `['system', 'monitor', '跟随系统']`,
		`<svg class="agent-icon harness-icon" data-icon="${icon}" width="14"`,
		"AgentFlowHarnessIcons?.hydrate(popover)", "setThemePreference(button.dataset.themePref)", "ArrowRight", "ArrowLeft",
		"renderThemeSegment()",
	} {
		if !strings.Contains(popover, want) {
			t.Errorf("profile popover should contain %q", want)
		}
	}
	if strings.Index(popover, "${themeSegmentHTML()}") > strings.Index(popover, `<div class="upp-footer">`) {
		t.Error("switcher should sit above the card footer")
	}

	if !strings.Contains(tabs, "['.upp-theme-seg', '.active']") {
		t.Error("sliding-tabs.js should register .upp-theme-seg")
	}
	for _, want := range []string{".upp-theme{", ".upp-theme-seg>button{", `[data-theme="dark"] .upp-theme-seg`, "html.theme-fading body"} {
		if !strings.Contains(platform, want) {
			t.Errorf("platform.css should contain %q", want)
		}
	}
	if !strings.Contains(product, "@media(min-width:1001px){.topbar #themeToggle{display:none!important}}") {
		t.Error("product-v4.css should hide the top bar theme button on desktop")
	}
}
