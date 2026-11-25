package observability

import (
	"math"
	"testing"
)

func TestOTLPTraceScalarAttributes(t *testing.T) {
	protoKV := func(key string, value []byte) []byte {
		return cvSProtoBytes(9, append(cvSProtoBytes(1, []byte(key)), cvSProtoBytes(2, value)...))
	}
	span := cvSProtoBytes(5, []byte("scalar-check"))
	span = append(span, protoKV("instrumentation.enabled", cvSProtoVarint(2, 0))...)
	span = append(span, protoKV("instrumentation.correct", cvSProtoVarint(2, 1))...)
	span = append(span, protoKV("gen_ai.usage.input_tokens", cvSProtoVarint(3, 42))...)
	span = append(span, protoKV("instrumentation.balance", cvSProtoVarint(3, ^uint64(0)))...)
	span = append(span, protoKV("instrumentation.gauge", cvSProtoFixed64(4, math.Float64bits(0.5)))...)
	span = append(span, protoKV("instrumentation.zero", cvSProtoFixed64(4, 0))...)
	body := cvSProtoBytes(1, cvSProtoBytes(2, cvSProtoBytes(2, span)))

	for name, input := range map[string][]byte{
		"protobuf": body,
		"json": []byte(`{"resourceSpans":[{"scopeSpans":[{"spans":[{"name":"scalar-check","attributes":[
			{"key":"instrumentation.enabled","value":{"boolValue":false}},
			{"key":"instrumentation.correct","value":{"boolValue":true}},
			{"key":"gen_ai.usage.input_tokens","value":{"intValue":"42"}},
			{"key":"instrumentation.balance","value":{"intValue":"-1"}},
			{"key":"instrumentation.gauge","value":{"doubleValue":0.5}},
			{"key":"instrumentation.zero","value":{"doubleValue":0}}
		]}]}]}]}`),
	} {
		t.Run(name, func(t *testing.T) {
			events := ParseOTLPTraces(input)
			if len(events) != 1 {
				t.Fatalf("events = %d, want one", len(events))
			}
			for key, want := range map[string]string{
				"instrumentation.enabled":   "false",
				"instrumentation.correct":   "true",
				"gen_ai.usage.input_tokens": "42",
				"instrumentation.balance":   "-1",
				"instrumentation.gauge":     "0.5",
				"instrumentation.zero":      "0",
			} {
				if got := events[0].Attributes[key]; got != want {
					t.Errorf("attribute %q = %v, want %q", key, got, want)
				}
			}
		})
	}
}
