package observability

import (
	"encoding/binary"
	"reflect"
	"testing"
)

type protoFieldVisit struct {
	num, wire int
	val       string
}

func TestForEachProtoFieldVisitsFieldsInOrder(t *testing.T) {
	msg := cvSProtoBytes(1, []byte("a"))
	msg = append(msg, cvSProtoVarint(3, 7)...)
	msg = append(msg, cvSProtoFixed64(7, 1)...)
	msg = append(msg, cvSProtoBytes(9, []byte("bc"))...)

	var got []protoFieldVisit
	forEachProtoField(msg, func(num, wire int, val []byte) {
		got = append(got, protoFieldVisit{num, wire, string(val)})
	})
	fixed := string(binary.LittleEndian.AppendUint64(nil, 1))
	want := []protoFieldVisit{{1, 2, "a"}, {3, 0, "\x07"}, {7, 1, fixed}, {9, 2, "bc"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("visits = %#v, want %#v", got, want)
	}
}

func TestForEachProtoFieldStopsAtMalformedFieldKeepingPrefix(t *testing.T) {
	// A valid field followed by a length-delimited field that claims 5 bytes
	// but carries only 1: the walk keeps the first field and stops.
	msg := append(cvSProtoBytes(1, []byte("ok")), 0x12, 5, 1)
	var got []protoFieldVisit
	forEachProtoField(msg, func(num, wire int, val []byte) {
		got = append(got, protoFieldVisit{num, wire, string(val)})
	})
	if want := []protoFieldVisit{{1, 2, "ok"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("visits = %#v, want %#v", got, want)
	}

	calls := 0
	for _, empty := range [][]byte{nil, {}} {
		forEachProtoField(empty, func(int, int, []byte) { calls++ })
	}
	if calls != 0 {
		t.Fatalf("empty input visited %d fields", calls)
	}
}

func TestParseSpanKeepsFieldsBeforeTruncation(t *testing.T) {
	span := cvSProtoBytes(5, []byte("op"))
	span = append(span, cvSProtoBytes(9, append(cvSProtoBytes(1, []byte("k")), cvSProtoBytes(2, cvSProtoBytes(1, []byte("v")))...))...)
	span = append(span, 0x7a, 9) // field 15 (status) truncated
	got := parseSpan(span)
	if got.Name != "op" || len(got.Attributes) != 1 || got.Attributes[0].Key != "k" || got.Attributes[0].Value.StringValue != "v" || got.Status.Code != 0 {
		t.Fatalf("span = %#v", got)
	}
}
