package httpapi

import (
	"testing"

	"agentflow/internal/domain"
)

func TestClarifyTAPDReadInstruction(t *testing.T) {
	project := domain.Project{TAPDWorkspaceID: "20000000"}
	cases := []struct {
		name         string
		externalType string
		want         string
	}{
		{
			name:         "story",
			externalType: "story",
			want:         "这是 TAPD story。先静默读取原单和评论。调用 tapd__proxy_execute_tool：tool_name=stories_get，tool_args.workspace_id=20000000，tool_args.id=1010（字符串），tool_args.limit=1。评论同样调用该工具，tool_name=comments_get，tool_args.workspace_id=20000000，tool_args.entry_type=story，tool_args.entry_id=1010。不要用 params，必须用 tool_args。\n",
		},
		{
			name:         "bug",
			externalType: " BUG ",
			want:         "这是 TAPD bug。先静默读取原单和评论。调用 tapd__proxy_execute_tool：tool_name=bugs_get，tool_args.workspace_id=20000000，tool_args.id=1010（字符串），tool_args.limit=1。评论同样调用该工具，tool_name=comments_get，tool_args.workspace_id=20000000，tool_args.entry_type=bug，tool_args.entry_id=1010。不要用 params，必须用 tool_args。\n",
		},
		{
			name:         "task",
			externalType: "task",
			want:         "这是 TAPD task。先静默读取原单和评论。调用 tapd__proxy_execute_tool：tool_name=tasks_get，tool_args.workspace_id=20000000，tool_args.id=1010（字符串），tool_args.limit=1。评论同样调用该工具，tool_name=comments_get，tool_args.workspace_id=20000000，tool_args.entry_type=task，tool_args.entry_id=1010。不要用 params，必须用 tool_args。\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			issue := domain.Issue{Source: "tapd", ExternalRef: &domain.ExternalRef{ExternalID: " 1010 ", ExternalType: tc.externalType}}
			if got := clarifyTAPDReadInstruction(issue, project, false); got != tc.want {
				t.Fatalf("instruction mismatch\n got: %q\nwant: %q", got, tc.want)
			}
		})
	}
}

func TestClarifyTAPDReadInstructionResumed(t *testing.T) {
	issue := domain.Issue{Source: "tapd", ExternalRef: &domain.ExternalRef{ExternalID: "1010", ExternalType: "bug"}}
	want := "这是 TAPD bug。对话里已有此前分析，不要再重拉原单和评论。仅当出现尚未读取的新文档链接时才用对应 MCP 读取。\n"
	if got := clarifyTAPDReadInstruction(issue, domain.Project{TAPDWorkspaceID: "20000000"}, true); got != want {
		t.Fatalf("resumed instruction mismatch\n got: %q\nwant: %q", got, want)
	}
}

func TestClarifyTAPDReadInstructionPrefersURLWorkspace(t *testing.T) {
	issue := domain.Issue{Source: "tapd", ExternalRef: &domain.ExternalRef{
		ExternalID:   "1010000000100000555",
		ExternalType: "story",
		ExternalURL:  "https://tapd.example.com/tapd_fe/10000000/story/detail/1010000000100000555",
	}}
	want := "这是 TAPD story。先静默读取原单和评论。调用 tapd__proxy_execute_tool：tool_name=stories_get，tool_args.workspace_id=10000000，tool_args.id=1010000000100000555（字符串），tool_args.limit=1。评论同样调用该工具，tool_name=comments_get，tool_args.workspace_id=10000000，tool_args.entry_type=story，tool_args.entry_id=1010000000100000555。不要用 params，必须用 tool_args。\n"
	if got := clarifyTAPDReadInstruction(issue, domain.Project{TAPDWorkspaceID: "fallback"}, false); got != want {
		t.Fatalf("instruction mismatch\n got: %q\nwant: %q", got, want)
	}
}

func TestClarifyGongfengReadInstruction(t *testing.T) {
	issue := domain.Issue{Source: "gongfeng", ExternalRef: &domain.ExternalRef{ExternalID: " 9001 ", ExternalIID: 12}}
	want := "这是工蜂 Issue。必须读取原单：调用 gongfeng__get_issue_detail（project_id=group/repo, issue_iid=12），并同步 gongfeng__get_issue_notes（project_id=group/repo, issue_id=9001）看评论。\n"
	if got := clarifyGongfengReadInstruction(issue, "group/repo", false); got != want {
		t.Fatalf("instruction mismatch\n got: %q\nwant: %q", got, want)
	}

	noRef := "这是工蜂 Issue。必须读取原单：调用 gongfeng__get_issue_detail（project_id=group/repo, issue_iid=0），并同步 gongfeng__get_issue_notes（project_id=group/repo, issue_id=）看评论。\n"
	if got := clarifyGongfengReadInstruction(domain.Issue{Source: "gongfeng"}, "group/repo", false); got != noRef {
		t.Fatalf("instruction without external ref mismatch\n got: %q\nwant: %q", got, noRef)
	}

	resumed := "这是工蜂 Issue。对话里已有此前分析，不要再重拉原单和评论。\n"
	if got := clarifyGongfengReadInstruction(issue, "group/repo", true); got != resumed {
		t.Fatalf("resumed instruction mismatch\n got: %q\nwant: %q", got, resumed)
	}
}
