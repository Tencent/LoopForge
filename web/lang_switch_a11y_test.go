package web

import (
	"regexp"
	"strings"
	"testing"
)

func TestLanguageSwitchOptionsAreRadios(t *testing.T) {
	option := regexp.MustCompile(`<button[^>]*data-lang-value="[^"]*"[^>]*>`)
	for _, path := range []string{"index.html", "login.html"} {
		src := readThemeSource(t, path)
		if !strings.Contains(src, `class="lang-switch" role="radiogroup"`) {
			t.Errorf("%s: language switch should be a radiogroup", path)
		}
		buttons := option.FindAllString(src, -1)
		if len(buttons) != 2 {
			t.Fatalf("%s: language options = %d, want 2", path, len(buttons))
		}
		for _, button := range buttons {
			if !strings.Contains(button, `role="radio"`) || !strings.Contains(button, `aria-checked=`) {
				t.Errorf("%s: %s should expose role=radio with aria-checked", path, button)
			}
		}
	}
}
