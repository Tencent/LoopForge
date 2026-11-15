package dockerproxy

import (
	"os"
	"regexp"
	"testing"
)

// Every field the proxy looks up must be canonicalized first, or a
// differently cased key would hide it from the check.
func TestCheckedFieldsCoverProxyLookups(t *testing.T) {
	source, err := os.ReadFile("proxy.go")
	if err != nil {
		t.Fatal(err)
	}
	lookups := regexp.MustCompile(`\["([A-Z][A-Za-z]*)"\]|setLabel\(body, "([A-Za-z]+)"`).FindAllStringSubmatch(string(source), -1)
	if len(lookups) < 20 {
		t.Fatalf("found only %d lookups; did the pattern break?", len(lookups))
	}
	for _, m := range lookups {
		name := m[1] + m[2]
		if checkedFields[foldKey(name)] != name {
			t.Errorf("proxy.go reads %q, which is missing from checkedFields", name)
		}
	}
}

func TestFoldKeyMatchesJSONFolding(t *testing.T) {
	for _, pair := range [][2]string{{"Links", "linKs"}, {"Labels", "LABELS"}, {"Subpath", "ſubpath"}} {
		if foldKey(pair[0]) != foldKey(pair[1]) {
			t.Errorf("%q and %q decode into the same field", pair[0], pair[1])
		}
	}
	if foldKey("Links") == foldKey("Link") {
		t.Error("different names must not fold together")
	}
}
