package spec

import (
	"strings"
	"testing"
)

const sampleSpec = `spec_version: "1.1"
requirement_id: R-1
base_commit: abc
verification_level: 2
clauses:
  - id: C1
    text: reading a resource returns it
scenarios:
  - id: S1
    covers: [C1]
    primary: positive
    kind: positive
    when:
      http: {method: GET, path: /items/1}
    then:
      http:
        status: 200
        json_paths:
          ".ok": "true"
          ".items[0].id": "1"
  - id: S2
    covers: [C1]
    primary: negative
    kind: negative
    when:
      http: {method: GET, path: /items/999}
    then:
      http: {status: 404}
`

func TestParseAndValidateSample(t *testing.T) {
	document, err := Parse([]byte(sampleSpec))
	if err != nil {
		t.Fatal(err)
	}
	if problems := Validate(document, "abc"); len(problems) != 0 {
		t.Fatalf("problems = %v", problems)
	}
	if problems := Validate(document, "other"); len(problems) == 0 {
		t.Fatal("base_commit mismatch accepted")
	}
}

func TestParseRejectsUnknownFieldsAndTrailingDocuments(t *testing.T) {
	if _, err := Parse([]byte("spec_version: 1\nsurprise: true\n")); err == nil {
		t.Fatal("unknown field accepted")
	}
	if _, err := Parse([]byte(sampleSpec + "\n---\nspec_version: 2\n")); err == nil {
		t.Fatal("second document accepted")
	}
}

func TestValidateRejectsMissingCoverageAndFixtures(t *testing.T) {
	document, err := Parse([]byte(`spec_version: "1"
requirement_id: R
clauses: [{id: C1, text: x}]
scenarios:
  - id: S1
    covers: [C1]
    primary: positive
    kind: positive
    given: {fixtures: [seed.sql]}
    when: {http: {method: GET, path: /x}}
    then: {http: {status: 200}}
`))
	if err != nil {
		t.Fatal(err)
	}
	problems := strings.Join(Validate(document, ""), "\n")
	for _, want := range []string{"fixtures", "primary negative", "no primary negative"} {
		if !strings.Contains(problems, want) {
			t.Fatalf("problems missing %q: %s", want, problems)
		}
	}
}

func TestCompileEmitsTestAndHelper(t *testing.T) {
	document, err := Parse([]byte(sampleSpec))
	if err != nil {
		t.Fatal(err)
	}
	files := Compile(document)
	if len(files) != 2 {
		t.Fatalf("files = %v", files)
	}
	var test string
	for name, content := range files {
		if strings.HasSuffix(name, "Test.java") {
			test = content
		}
		if strings.Contains(name, "LoopForgeHttp") && !strings.Contains(content, "class LoopForgeHttp") {
			t.Fatal("helper source is not the HTTP helper")
		}
	}
	if !strings.Contains(test, "@Test void s1_S1") || !strings.Contains(test, "assertJsonPath") {
		t.Fatalf("compiled test:\n%s", test)
	}
	if !strings.Contains(test, "assertEquals(404") {
		t.Fatalf("negative scenario missing:\n%s", test)
	}
}
