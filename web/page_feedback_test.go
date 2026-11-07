package web

import (
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
)

func TestPageFeedbackWidgetBehaviour(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		if os.Getenv("CI") != "" {
			t.Fatal("node is required in CI")
		}
		t.Skip("node not installed")
	}
	out, err := exec.Command(node, "page_feedback_test.js").CombinedOutput()
	if err != nil {
		t.Fatalf("page_feedback_test.js: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "page feedback ok") {
		t.Fatalf("page_feedback_test.js did not finish:\n%s", out)
	}
}

func TestPageFeedbackStylesUseHarnessTokensOnly(t *testing.T) {
	raw, err := os.ReadFile("app/page-feedback.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(raw)
	for _, m := range regexp.MustCompile(`var\(--([a-z0-9-]+)`).FindAllStringSubmatch(css, -1) {
		if !strings.HasPrefix(m[1], "hui-") {
			t.Errorf("page-feedback.css references --%s; use --hui-* tokens only", m[1])
		}
	}
	if m := regexp.MustCompile(`#[0-9a-fA-F]{3,8}\b|\brgba?\(|\bhsla?\(|:\s*(?:white|black)\b`).FindString(css); m != "" {
		t.Errorf("page-feedback.css hardcodes the color %q; use --hui-* tokens", m)
	}
	if !strings.Contains(css, "@media (prefers-reduced-motion: reduce)") {
		t.Error("page-feedback.css must turn motion off under prefers-reduced-motion")
	}
}

func TestPageFeedbackIsLoadedAndKeepsNoBrowserState(t *testing.T) {
	html, err := os.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`<link rel="stylesheet" href="/app/page-feedback.css">`, `<script src="/app/page-feedback.js"></script>`} {
		if !strings.Contains(string(html), want) {
			t.Errorf("index.html must include %s", want)
		}
	}
	js, err := os.ReadFile("app/page-feedback.js")
	if err != nil {
		t.Fatal(err)
	}
	if m := regexp.MustCompile(`localStorage\.|sessionStorage\.|document\.cookie|indexedDB`).Find(js); m != nil {
		t.Errorf("page-feedback.js must keep its state in memory only, found %q", m)
	}
}
