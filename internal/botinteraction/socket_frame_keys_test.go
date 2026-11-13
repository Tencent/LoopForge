package botinteraction

import (
	"bytes"
	"encoding/json"
	"log"
	"reflect"
	"strings"
	"testing"
)

func TestSortedJSONKeys(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want []string
	}{
		{"object", `{"b":1,"a":2,"c":{}}`, []string{"a", "b", "c"}},
		{"empty input", ``, []string{}},
		{"invalid json", `not-json`, []string{}},
		{"non object", `[1,2]`, []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			keys, _ := sortedJSONKeys(json.RawMessage(tc.raw))
			if keys == nil || !reflect.DeepEqual(keys, tc.want) {
				t.Fatalf("keys = %#v, want %#v", keys, tc.want)
			}
		})
	}
}

func TestLogFrameKeys(t *testing.T) {
	var buf bytes.Buffer
	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	defer func() { log.SetOutput(prevOut); log.SetFlags(prevFlags) }()

	nested := Frame{Cmd: "aibot_event_callback", Body: json.RawMessage(`{"msgid":"m","event":{"eventtype":"x","template_card_event":{"task_id":"t","event_key":"k"}}}`)}
	logFrameKeys(nested)
	want := "wecom frame cmd=aibot_event_callback body_keys=[event msgid] event_keys=[eventtype template_card_event] card_keys=[event_key task_id]"
	if got := strings.TrimSpace(buf.String()); got != want {
		t.Fatalf("log = %q, want %q", got, want)
	}

	buf.Reset()
	logFrameKeys(Frame{Cmd: "ping"})
	want = "wecom frame cmd=ping body_keys=[] event_keys=[] card_keys=[]"
	if got := strings.TrimSpace(buf.String()); got != want {
		t.Fatalf("log = %q, want %q", got, want)
	}
}
