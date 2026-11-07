package web

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestTopbarTitleHiddenWhenViewHasHeading(t *testing.T) {
	read := func(name string) string {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	app, css, html := read("app.js"), read("product-v4.css"), read("index.html")

	toggle := regexp.MustCompile(`\$\('#pageTitle'\)\.textContent=titles\[name\]\|\|name;[\s\S]{0,200}?document\.body\.classList\.toggle\('view-has-heading',!!\$\(` + "`#\\$\\{target\\}View`" + `\)\.querySelector\('\.page-heading,\.page-header,\.kb-head,\.pa-ph'\)\)`)
	if !toggle.MatchString(app) {
		t.Fatal("navigation must keep #pageTitle text and toggle body.view-has-heading from the target view")
	}
	if !strings.Contains(css, "body.view-has-heading .topbar .page-context{display:none}") {
		t.Fatal("product-v4.css must hide the topbar title for views with their own heading")
	}

	starts := regexp.MustCompile(`<section id="(\w+)View" class="view`).FindAllStringSubmatchIndex(html, -1)
	if len(starts) < 20 {
		t.Fatalf("expected the static views in index.html, found %d", len(starts))
	}
	heading := regexp.MustCompile(`class="(?:page-heading|page-header|kb-head|pa-ph)[" ]`)
	withoutHeading := map[string]bool{"flow": true, "collaboration": true}
	for i, loc := range starts {
		end := len(html)
		if i+1 < len(starts) {
			end = starts[i+1][0]
		}
		name, body := html[loc[2]:loc[3]], html[loc[0]:end]
		if got := heading.MatchString(body); got == withoutHeading[name] {
			t.Errorf("%sView: has heading = %v; only flow and collaboration rely on the topbar title", name, got)
		}
	}
}
