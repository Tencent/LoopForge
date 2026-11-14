package httpapi

import (
	"reflect"
	"testing"
)

func TestStreamResultEventValueDecodesResultPayload(t *testing.T) {
	const line = `{"type":"result","result":{"summary":"obj"}}`
	cases := []struct {
		name    string
		event   map[string]any
		want    any
		wantSrc string
		wantOK  bool
	}{
		{
			name:    "json string",
			event:   map[string]any{"type": "result", "result": "```json\n{\"summary\":\"s\",\"ready\":true}\n```"},
			want:    map[string]any{"summary": "s", "ready": true},
			wantSrc: "```json\n{\"summary\":\"s\",\"ready\":true}\n```",
			wantOK:  true,
		},
		{
			name:    "prose string",
			event:   map[string]any{"type": "result", "result": "just prose"},
			want:    map[string]any{"summary": "just prose", "questions": []any{}, "ready": false},
			wantSrc: "just prose",
			wantOK:  true,
		},
		{
			name:   "blank string",
			event:  map[string]any{"type": "result", "result": "  \n\t"},
			wantOK: false,
		},
		{
			name:    "object envelope",
			event:   map[string]any{"type": "result", "result": map[string]any{"type": "result", "result": map[string]any{"summary": "obj"}}},
			want:    map[string]any{"summary": "obj"},
			wantSrc: line,
			wantOK:  true,
		},
		{
			name:   "missing result",
			event:  map[string]any{"type": "result"},
			wantOK: false,
		},
		{
			name:   "non-string non-object result",
			event:  map[string]any{"type": "result", "result": float64(1)},
			wantOK: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, src, ok := streamResultEventValue(tc.event, line)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !ok {
				if got != nil || src != "" {
					t.Fatalf("unexpected value on miss: %#v, %q", got, src)
				}
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("value = %#v, want %#v", got, tc.want)
			}
			if src != tc.wantSrc {
				t.Fatalf("src = %q, want %q", src, tc.wantSrc)
			}
		})
	}
}

func TestExtractJSONObjectKeepsEarlierResultWhenLaterResultIsBlank(t *testing.T) {
	raw := `{"type":"result","result":"{\"summary\":\"first\",\"ready\":true}"}` + "\n" +
		`{"type":"result","result":"   "}`
	got, src := extractJSONObject(raw)
	want := map[string]any{"summary": "first", "ready": true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("value = %#v, want %#v", got, want)
	}
	if src != `{"summary":"first","ready":true}` {
		t.Fatalf("src = %q", src)
	}
}

func TestExtractJSONObjectFallsBackToSummaryForPlainText(t *testing.T) {
	got, src := extractJSONObject("  plain words  ")
	want := clarifySummaryFallback("plain words")
	if !reflect.DeepEqual(got, want) || src != "plain words" {
		t.Fatalf("got %#v, %q", got, src)
	}
	if want["ready"] != false || len(want["questions"].([]any)) != 0 {
		t.Fatalf("clarifySummaryFallback shape = %#v", want)
	}
}
