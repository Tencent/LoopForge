package tracker

import (
	"encoding/json"
	"testing"
)

func TestDecodeCreatedRecordObject(t *testing.T) {
	gi, err := decodeCreatedRecord[gongfengIssue](`{"id":7,"iid":3,"title":"t","state":"opened"}`)
	if err != nil {
		t.Fatalf("decode object: %v", err)
	}
	if gi.ID != 7 || gi.IID != 3 || gi.Title != "t" || gi.State != "opened" {
		t.Fatalf("unexpected issue: %+v", gi)
	}
}

func TestDecodeCreatedRecordArrayFirstElement(t *testing.T) {
	st, err := decodeCreatedRecord[tapdStory](`[{"id":"1001","name":"first"},{"id":"1002","name":"second"}]`)
	if err != nil {
		t.Fatalf("decode array: %v", err)
	}
	if st.ID != "1001" || st.Name != "first" {
		t.Fatalf("expected first element, got %+v", st)
	}
}

func TestDecodeCreatedRecordFailuresReturnObjectError(t *testing.T) {
	for name, raw := range map[string]string{
		"empty array":  `[]`,
		"invalid json": `not json`,
		"scalar":       `"created"`,
	} {
		t.Run(name, func(t *testing.T) {
			var want gongfengIssue
			wantErr := json.Unmarshal([]byte(raw), &want)
			if wantErr == nil {
				t.Fatalf("test payload %q unexpectedly decodes as an object", raw)
			}
			gi, err := decodeCreatedRecord[gongfengIssue](raw)
			if err == nil {
				t.Fatalf("expected error, got %+v", gi)
			}
			if err.Error() != wantErr.Error() {
				t.Fatalf("error = %q, want the object decode error %q", err, wantErr)
			}
			if gi.ID != 0 || gi.IID != 0 || gi.Title != "" {
				t.Fatalf("expected zero value on failure, got %+v", gi)
			}
		})
	}
}
