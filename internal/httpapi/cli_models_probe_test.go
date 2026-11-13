package httpapi

import (
	"reflect"
	"testing"

	"agentflow/internal/domain"
)

func TestCursorModelFamiliesFoldGemini4(t *testing.T) {
	output := `
gemini-4-carbon-low - Gemini 4 Carbon Low
gemini-4-carbon-medium - Gemini 4 Carbon Medium
gemini-4-carbon-high - Gemini 4 Carbon High
gemini-4-hydrogen-low - Gemini 4 Hydrogen Low
gemini-4-hydrogen-medium - Gemini 4 Hydrogen Medium
gemini-4-hydrogen-high - Gemini 4 Hydrogen High
gemini-4-hydrogen-xhigh - Gemini 4 Hydrogen Extra High
gemini-4-argon-low - Gemini 4 Argon Low
gemini-4-argon-medium - Gemini 4 Argon Medium (default)
gemini-4-argon-high - Gemini 4 Argon High
`
	got := cursorModelFamilies(parseCursorModels(output))
	want := []domain.CLIModel{
		{ID: "gemini-4-carbon", Name: "Gemini 4 Carbon"},
		{ID: "gemini-4-hydrogen", Name: "Gemini 4 Hydrogen"},
		{ID: "gemini-4-argon", Name: "Gemini 4 Argon"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("families:\n got %v\nwant %v", got, want)
	}
}
