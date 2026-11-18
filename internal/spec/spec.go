// Package spec ports AegisOS's acceptance-spec schema, validation and
// deterministic Java compilation. Model-authored spec generation
// (AegisOS SpecGenerator) is not ported: it needs model credentials and is a
// separate review-quality concern.
package spec

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const maxDocumentBytes = 1 << 20

type Document struct {
	SpecVersion       string     `yaml:"spec_version"`
	RequirementID     string     `yaml:"requirement_id"`
	BaseCommit        string     `yaml:"base_commit,omitempty"`
	VerificationLevel int        `yaml:"verification_level,omitempty"`
	Clauses           []Clause   `yaml:"clauses"`
	Scenarios         []Scenario `yaml:"scenarios"`
}

type Clause struct {
	ID           string   `yaml:"id"`
	Text         string   `yaml:"text"`
	ExpectedTags []string `yaml:"expected_tags,omitempty"`
}

type Scenario struct {
	ID      string   `yaml:"id"`
	Covers  []string `yaml:"covers"`
	Primary string   `yaml:"primary"`
	Kind    string   `yaml:"kind"`
	Tags    []string `yaml:"tags,omitempty"`
	Given   Given    `yaml:"given,omitempty"`
	When    When     `yaml:"when"`
	Then    Then     `yaml:"then"`
}

type Given struct {
	Fixtures []string `yaml:"fixtures,omitempty"`
}

type When struct {
	HTTP HTTPCall `yaml:"http"`
}

type HTTPCall struct {
	Method string            `yaml:"method"`
	Path   string            `yaml:"path"`
	Query  map[string]string `yaml:"query,omitempty"`
	Body   string            `yaml:"body,omitempty"`
}

type Then struct {
	HTTP HTTPThen `yaml:"http"`
}

type HTTPThen struct {
	Status    int               `yaml:"status"`
	JSONPaths map[string]string `yaml:"json_paths,omitempty"`
}

// Parse strictly decodes one YAML document: unknown fields, duplicate keys and
// trailing documents are rejected, as in AegisOS SpecParser.
func Parse(raw []byte) (Document, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return Document{}, errors.New("spec is empty")
	}
	if len(raw) > maxDocumentBytes {
		return Document{}, fmt.Errorf("spec exceeds %d bytes", maxDocumentBytes)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	var document Document
	if err := decoder.Decode(&document); err != nil {
		return Document{}, fmt.Errorf("parse spec: %w", err)
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		return Document{}, errors.New("spec contains more than one YAML document")
	}
	return document, nil
}

var pathPattern = regexp.MustCompile(`^\.[A-Za-z0-9_]+(\.[A-Za-z0-9_]+|\[[0-9]+\])*$`)

// Validate applies the deterministic rules AegisOS enforces before compilation.
// Model-quality checks (literal overlap, mutation) stay out of this milestone.
func Validate(document Document, baseCommit string) []string {
	var problems []string
	if strings.TrimSpace(document.SpecVersion) == "" {
		problems = append(problems, "spec_version is required")
	}
	if strings.TrimSpace(document.RequirementID) == "" {
		problems = append(problems, "requirement_id is required")
	}
	if want := strings.TrimSpace(baseCommit); want != "" && strings.TrimSpace(document.BaseCommit) != "" && document.BaseCommit != want {
		problems = append(problems, fmt.Sprintf("base_commit %q does not match %q", document.BaseCommit, want))
	}
	clauses := map[string]bool{}
	for _, clause := range document.Clauses {
		id := strings.TrimSpace(clause.ID)
		if id == "" || strings.TrimSpace(clause.Text) == "" {
			problems = append(problems, "every clause needs id and text")
			continue
		}
		if clauses[id] {
			problems = append(problems, "duplicate clause "+id)
		}
		clauses[id] = true
	}
	covered := map[string]bool{}
	primaryPositive := map[string]bool{}
	primaryNegative := map[string]bool{}
	seen := map[string]bool{}
	for _, scenario := range document.Scenarios {
		id := strings.TrimSpace(scenario.ID)
		if id == "" {
			problems = append(problems, "every scenario needs id")
			continue
		}
		if seen[id] {
			problems = append(problems, "duplicate scenario "+id)
		}
		seen[id] = true
		if len(scenario.Given.Fixtures) > 0 {
			problems = append(problems, fmt.Sprintf("scenario %s: given.fixtures is not supported", id))
		}
		if len(scenario.Covers) == 0 {
			problems = append(problems, fmt.Sprintf("scenario %s: covers is required", id))
		}
		for _, cover := range scenario.Covers {
			if !clauses[cover] {
				problems = append(problems, fmt.Sprintf("scenario %s covers unknown clause %s", id, cover))
			}
			covered[cover] = true
		}
		switch scenario.Kind {
		case "positive", "negative":
		default:
			problems = append(problems, fmt.Sprintf("scenario %s: kind must be positive or negative", id))
		}
		switch scenario.Primary {
		case "positive", "negative":
		default:
			problems = append(problems, fmt.Sprintf("scenario %s: primary must be positive or negative", id))
		}
		for _, cover := range scenario.Covers {
			switch scenario.Primary {
			case "positive":
				primaryPositive[cover] = true
			case "negative":
				primaryNegative[cover] = true
			}
		}
		method := strings.ToUpper(strings.TrimSpace(scenario.When.HTTP.Method))
		switch method {
		case "GET", "POST", "PUT", "PATCH", "DELETE":
		default:
			problems = append(problems, fmt.Sprintf("scenario %s: unsupported method %q", id, scenario.When.HTTP.Method))
		}
		if !strings.HasPrefix(strings.TrimSpace(scenario.When.HTTP.Path), "/") {
			problems = append(problems, fmt.Sprintf("scenario %s: path must start with /", id))
		}
		if scenario.Then.HTTP.Status < 100 || scenario.Then.HTTP.Status > 599 {
			problems = append(problems, fmt.Sprintf("scenario %s: then.http.status must be 100-599", id))
		}
		for path := range scenario.Then.HTTP.JSONPaths {
			if !pathPattern.MatchString(path) {
				problems = append(problems, fmt.Sprintf("scenario %s: unsupported json path %q", id, path))
			}
		}
	}
	for id := range clauses {
		if !covered[id] {
			problems = append(problems, "clause "+id+" is not covered by any scenario")
		}
		if !primaryPositive[id] {
			problems = append(problems, "clause "+id+" has no primary positive scenario")
		}
		if !primaryNegative[id] {
			problems = append(problems, "clause "+id+" has no primary negative scenario")
		}
	}
	sort.Strings(problems)
	return problems
}

// Compile emits the Java sources for one spec: a JUnit 5 test class plus a
// JDK-only HTTP/JSON helper. Base URL comes from -Daegis.baseUrl or
// LOOPFORGE_SUBJECT_URL.
func Compile(document Document) map[string]string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%#v", document)))
	class := "AegisSpec" + hex.EncodeToString(sum[:])[:8] + "Test"
	var test strings.Builder
	test.WriteString("package aegis.generated;\n\n")
	test.WriteString("import org.junit.jupiter.api.Test;\n")
	test.WriteString("import static org.junit.jupiter.api.Assertions.assertEquals;\n\n")
	test.WriteString("class " + class + " {\n")
	for index, scenario := range document.Scenarios {
		method := strings.ToUpper(strings.TrimSpace(scenario.When.HTTP.Method))
		path := strings.TrimSpace(scenario.When.HTTP.Path)
		query := queryLiteral(scenario.When.HTTP.Query)
		body := strconvLiteral(scenario.When.HTTP.Body)
		fmt.Fprintf(&test, "  @Test void s%d_%s() throws Exception {\n", index+1, javaIdentifier(scenario.ID))
		fmt.Fprintf(&test, "    var response = LoopForgeHttp.call(\"%s\", \"%s\", %s, %s);\n", method, path, query, body)
		fmt.Fprintf(&test, "    assertEquals(%d, response.status(), \"%s status\");\n", scenario.Then.HTTP.Status, scenario.ID)
		paths := make([]string, 0, len(scenario.Then.HTTP.JSONPaths))
		for path := range scenario.Then.HTTP.JSONPaths {
			paths = append(paths, path)
		}
		sort.Strings(paths)
		for _, path := range paths {
			fmt.Fprintf(&test, "    LoopForgeHttp.assertJsonPath(response.body(), %s, %s);\n",
				strconvLiteral(path), strconvLiteral(scenario.Then.HTTP.JSONPaths[path]))
		}
		test.WriteString("  }\n")
	}
	test.WriteString("}\n")
	return map[string]string{
		"aegis/generated/" + class + ".java": test.String(),
		"aegis/generated/LoopForgeHttp.java": helperSource,
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func queryLiteral(query map[string]string) string {
	if len(query) == 0 {
		return "java.util.Map.of()"
	}
	keys := make([]string, 0, len(query))
	for key := range query {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var parts []string
	for _, key := range keys {
		parts = append(parts, strconvLiteral(key)+", "+strconvLiteral(query[key]))
	}
	return "java.util.Map.of(" + strings.Join(parts, ", ") + ")"
}

func strconvLiteral(value string) string {
	quoted := strings.ReplaceAll(value, "\\", "\\\\")
	quoted = strings.ReplaceAll(quoted, "\"", "\\\"")
	quoted = strings.ReplaceAll(quoted, "\n", "\\n")
	quoted = strings.ReplaceAll(quoted, "\r", "\\r")
	quoted = strings.ReplaceAll(quoted, "\t", "\\t")
	return "\"" + quoted + "\""
}

func javaIdentifier(value string) string {
	var out strings.Builder
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' {
			out.WriteRune(r)
		} else {
			out.WriteByte('_')
		}
	}
	if out.Len() == 0 {
		return "scenario"
	}
	return out.String()
}
