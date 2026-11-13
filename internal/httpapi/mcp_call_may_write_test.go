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

// Every name here also contains a read word, so only the write-verb segment
// check can classify it as a write; the default-to-write fallback cannot.
func TestMCPCallMayWriteVerbSegmentOverridesReadWord(t *testing.T) {
	cases := []struct {
		kind string
		call assistantMCPCall
		want bool
	}{
		{"gongfeng", assistantMCPCall{Tool: "comment_list"}, true},
		{"gongfeng", assistantMCPCall{Tool: "set_view"}, true},
		{"gongfeng", assistantMCPCall{Tool: "mr-approve-detail"}, true},
		{"gongfeng", assistantMCPCall{Tool: "label.assign.info"}, true},
		{"gongfeng", assistantMCPCall{Tool: "issue_close_get"}, true},
		{"gongfeng", assistantMCPCall{Tool: "publish_search_result"}, true},
		{"tapd", assistantMCPCall{Tool: "proxy_execute_tool", Arguments: map[string]any{"tool_name": "stories_assign_query"}}, true},
		// A write verb that is only part of a segment does not count.
		{"gongfeng", assistantMCPCall{Tool: "assignee_list"}, false},
		{"gongfeng", assistantMCPCall{Tool: "postmortem-detail"}, false},
		{"gongfeng", assistantMCPCall{Tool: "closed.issues.query"}, false},
		// The name is lower-cased first, so an upper-case verb segment still wins.
		{"gongfeng", assistantMCPCall{Tool: "Issue_Close_list"}, true},
	}
	for _, tc := range cases {
		if got := mcpCallMayWrite(tc.kind, tc.call); got != tc.want {
			t.Errorf("mcpCallMayWrite(%q, %+v)=%v, want %v", tc.kind, tc.call, got, tc.want)
		}
	}
}
