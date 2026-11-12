package domain

import (
	"strings"
	"testing"
)

func TestSharedScheduleParameters(t *testing.T) {
	v := SharedScheduleVersion{Name: "巡检", Prompt: "{{params.days}} 天：{{ params.team }}；{{params.days}}", Parameters: []ScheduleParameter{
		{Name: "days", Label: "天数", Type: "integer", Required: true, Default: "3"},
		{Name: "team", Label: "业务", Type: "string", Required: true},
	}}
	prompt, values, err := v.Render(map[string]string{"team": " {{params.days}} "})
	if err != nil || prompt != "3 天：{{params.days}}；3" || values["days"] != "3" {
		t.Fatalf("render: %q %+v %v", prompt, values, err)
	}
	for _, input := range []map[string]string{
		{}, {"team": "pay", "days": ""}, {"team": "pay", "days": "1.5"}, {"team": "pay", "unknown": "value"}, {"team": strings.Repeat("x", 2001)},
	} {
		if _, _, err := v.Render(input); err == nil {
			t.Errorf("accepted invalid parameters: %+v", input)
		}
	}
	for _, p := range []ScheduleParameter{
		{Name: "bad-name", Label: "Bad", Type: "string"}, {Name: "x", Type: "string"}, {Name: "x", Label: "X", Type: "secret"}, {Name: "x", Label: "X", Type: "boolean", Default: "yes"}, {Name: "x", Label: "X", Type: "number", Default: "NaN"},
	} {
		candidate := SharedScheduleVersion{Name: "X", Prompt: "Test", Parameters: []ScheduleParameter{p}}
		if candidate.Validate() == nil {
			t.Errorf("accepted invalid definition %+v", p)
		}
	}
	duplicate := v
	duplicate.Parameters = append(append([]ScheduleParameter{}, v.Parameters...), v.Parameters[0])
	if duplicate.Validate() == nil {
		t.Fatal("duplicate parameter accepted")
	}
	unknown := v
	unknown.Prompt = "{{params.missing}}"
	if unknown.Validate() == nil {
		t.Fatal("undefined reference accepted")
	}
	for _, typ := range []string{"boolean", "number"} {
		p := ScheduleParameter{Name: "value", Label: "Value", Type: typ, Required: true}
		candidate := SharedScheduleVersion{Name: "X", Prompt: "{{params.value}}", Parameters: []ScheduleParameter{p}}
		good, bad := "false", "yes"
		if typ == "number" {
			good, bad = "1.25", "+Inf"
		}
		if _, _, err := candidate.Render(map[string]string{"value": good}); err != nil {
			t.Fatal(err)
		}
		if _, _, err := candidate.Render(map[string]string{"value": bad}); err == nil {
			t.Errorf("accepted %s %s", typ, bad)
		}
	}
	long := v
	long.Prompt = strings.Repeat("{{params.team}}", 10) + strings.Repeat("说明", 2000)
	got, _, err := long.Render(map[string]string{"team": strings.Repeat("x", 500)})
	want := strings.Repeat(strings.Repeat("x", 500), 10) + strings.Repeat("说明", 2000)
	if err != nil || got != want {
		t.Fatalf("long prompt rejected: %v len=%d", err, len(got))
	}
}
