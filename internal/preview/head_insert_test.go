package preview

import (
	"strings"
	"testing"
)

func TestInsertAfterHeadOpen(t *testing.T) {
	cases := []struct {
		name, html, want string
		ok               bool
	}{
		{"plain head", `<html><head></head></html>`, `<html><head>T</head></html>`, true},
		{"upper head", `<HTML><HEAD></HEAD></HTML>`, `<HTML><HEAD>T</HEAD></HTML>`, true},
		{"head with attrs", `<head lang="en"><title>x</title></head>`, `<head lang="en">T<title>x</title></head>`, true},
		{"prefers bare head", `<head lang="en"></head><head>`, `<head lang="en"></head><head>T`, true},
		{"no head", `<body>x</body>`, `<body>x</body>`, false},
		{"unterminated head", `<head lang="en"`, `<head lang="en"`, false},
		{"header is not head", `<header>x</header>`, `<header>x</header>`, false},
	}
	for _, tc := range cases {
		got, ok := insertAfterHeadOpen(tc.html, "T")
		if got != tc.want || ok != tc.ok {
			t.Errorf("%s: got (%q, %v), want (%q, %v)", tc.name, got, ok, tc.want, tc.ok)
		}
	}
}

func TestInsertBeforeHeadClose(t *testing.T) {
	cases := []struct {
		name, html, want string
		ok               bool
	}{
		{"plain", `<head><title>x</title></head><body>`, `<head><title>x</title>T</head><body>`, true},
		{"upper", `<HEAD></HEAD>`, `<HEAD>T</HEAD>`, true},
		{"no close", `<head><body>`, `<head><body>`, false},
	}
	for _, tc := range cases {
		got, ok := insertBeforeHeadClose(tc.html, "T")
		if got != tc.want || ok != tc.ok {
			t.Errorf("%s: got (%q, %v), want (%q, %v)", tc.name, got, ok, tc.want, tc.ok)
		}
	}
}

// The injectors keep their positions and fallbacks after sharing the helpers.
func TestHeadInjectorsKeepPlacement(t *testing.T) {
	const prefix = "/preview/x"
	base := `<base href="/preview/x/">`
	for html, want := range map[string]string{
		`<head></head>`:           `<head>` + base + `</head>`,
		`<HEAD class="a"></HEAD>`: `<HEAD class="a">` + base + `</HEAD>`,
		`<head`:                   base + `<head`,
		`<p>x</p>`:                base + `<p>x</p>`,
	} {
		if got := injectBaseTag(html, prefix); got != want {
			t.Errorf("injectBaseTag(%q) = %q, want %q", html, got, want)
		}
	}

	viewport := injectPreviewViewport(`<head><title>t</title></head>`)
	if !strings.HasPrefix(viewport, `<head><title>t</title><style data-af-preview-viewport>`) || !strings.HasSuffix(viewport, `</style></head>`) {
		t.Errorf("viewport not placed before </head>: %q", viewport)
	}

	backend := injectPreviewBackend(`<html><head lang="en"><title>t</title></head></html>`, prefix, "https://api.example")
	if !strings.HasPrefix(backend, `<html><head lang="en"><script data-af-preview-backend>`) || !strings.HasSuffix(backend, `</script><title>t</title></head></html>`) {
		t.Errorf("backend script not placed after <head ...>: %q", backend)
	}
	if got := injectPreviewBackend(`<head`, prefix, "https://api.example"); !strings.HasPrefix(got, `<script data-af-preview-backend>`) || !strings.HasSuffix(got, `</script><head`) {
		t.Errorf("backend fallback changed: %q", got)
	}

	history := injectPreviewHistory(`<head><title>t</title></head>`, prefix, false)
	if !strings.HasPrefix(history, `<head><title>t</title><script data-af-preview-history>`) || !strings.HasSuffix(history, `</script></head>`) {
		t.Errorf("history shim not placed before </head>: %q", history)
	}

	for html, check := range map[string]func(string) bool{
		`<head lang="en"></head>`: func(s string) bool {
			return strings.HasPrefix(s, `<head lang="en"><script data-af-dev-server>`) && strings.HasSuffix(s, `</script></head>`)
		},
		`<html lang="en"><body></body></html>`: func(s string) bool {
			return strings.HasPrefix(s, `<html lang="en"><script data-af-dev-server>`) && strings.HasSuffix(s, `</script><body></body></html>`)
		},
		`<p>x</p>`: func(s string) bool {
			return strings.HasPrefix(s, `<script data-af-dev-server>`) && strings.HasSuffix(s, `</script><p>x</p>`)
		},
	} {
		if got := injectDevServerIntercept(html, prefix, 5173); !check(got) {
			t.Errorf("injectDevServerIntercept(%q) placement changed: %q", html, got)
		}
	}
}
