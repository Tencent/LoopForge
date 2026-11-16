package deploy

import (
	"bytes"
	"strings"
	"testing"
)

func TestBindCLIScriptForKeepsPlainHTTPForIntranetInstalls(t *testing.T) {
	script := BindCLIScriptFor(false)
	if !bytes.Contains(script, []byte("\nSTRICT_HTTPS=0\n")) {
		t.Fatal("an installer served over plain HTTP must keep the opt-in default")
	}
}

func TestBindCLIScriptForRefusesDowngradeWhenServedOverHTTPS(t *testing.T) {
	script := BindCLIScriptFor(true)
	if !bytes.Contains(script, []byte("\nSTRICT_HTTPS=1\n")) {
		t.Fatal("an installer served over HTTPS must not fall back to plain HTTP")
	}
	if bytes.Contains(script, []byte("\nSTRICT_HTTPS=0\n")) {
		t.Fatal("the strict default must replace the opt-in assignment, not add a second one")
	}
}

func TestBindCLIScriptDocumentsTheAllowHTTPOptOut(t *testing.T) {
	script := string(BindCLIScript())
	if !strings.Contains(script, "--allow-http) STRICT_HTTPS=0") {
		t.Fatal("--allow-http must be parsed")
	}
	if !strings.Contains(script, "#   --allow-http") {
		t.Fatal("--allow-http must be documented in the usage header")
	}
}