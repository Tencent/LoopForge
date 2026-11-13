package httpapi

import "testing"

func TestMCPCallMayWriteClassifiesToolNames(t *testing.T) {
	cases := []struct {
		name string
		kind string
		call assistantMCPCall
		want bool
	}{
		{"empty name is not a write", "gongfeng", assistantMCPCall{Tool: "  "}, false},
		{"clarify write name", "gongfeng", assistantMCPCall{Tool: "create_issue"}, true},
		{"write verb segment by underscore", "gongfeng", assistantMCPCall{Tool: "comments_add"}, true},
		{"write verb segment by dash", "gongfeng", assistantMCPCall{Tool: "mr-approve"}, true},
		{"write verb segment by dot", "gongfeng", assistantMCPCall{Tool: "label.remove"}, true},
		{"write verb wins over read word", "gongfeng", assistantMCPCall{Tool: "merge_merge_request"}, true},
		{"write verb dot segment beats read word", "gongfeng", assistantMCPCall{Tool: "detail.set"}, true},
		{"write verb segment beats read word in same name", "gongfeng", assistantMCPCall{Tool: "list_add"}, true},
		{"write verb is case-insensitive", "gongfeng", assistantMCPCall{Tool: "Issue_Close"}, true},
		{"write verb only as substring is not a segment", "gongfeng", assistantMCPCall{Tool: "get_settings"}, false},
		{"read word", "gongfeng", assistantMCPCall{Tool: "get_branch_list"}, false},
		{"read word as substring", "gongfeng", assistantMCPCall{Tool: "user_todo_stories_get"}, false},
		{"unknown name defaults to write", "gongfeng", assistantMCPCall{Tool: "frobnicate"}, true},
		{"tapd inner read tool", "tapd", assistantMCPCall{Tool: "proxy_execute_tool", Arguments: map[string]any{"tool_name": "stories_get"}}, false},
		{"tapd inner write tool", "tapd", assistantMCPCall{Tool: "proxy_execute_tool", Arguments: map[string]any{"tool_name": "comments_add"}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mcpCallMayWrite(tc.kind, tc.call); got != tc.want {
				t.Fatalf("mcpCallMayWrite(%q, %+v)=%v, want %v", tc.kind, tc.call, got, tc.want)
			}
		})
	}
}
