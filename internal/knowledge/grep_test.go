package knowledge

import (
	"context"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func grepFixture(body string) *Service {
	store := newFakeStore()
	store.docs = map[string][]domain.KnowledgeDocument{
		"docs":   {{SourceID: "docs", Path: "guides/a.md", Commit: "1", Body: body}},
		"secret": {{SourceID: "secret", Path: "secret.md", Commit: "9", Body: "SECRET-TOKEN\n" + body}},
	}
	return newTestService(store, &fakeUpstream{})
}

func TestGrepStaysInsideProjectSources(t *testing.T) {
	svc := grepFixture("错误码 E_DEMO_42 见手册\n下一行\n")
	out, err := svc.Grep(context.Background(), "p1", GrepQuery{Pattern: "E_DEMO_42"})
	if err != nil || len(out.Hits) != 1 {
		t.Fatalf("grep = %+v %v", out, err)
	}
	hit := out.Hits[0]
	if hit.Source != "docs" || hit.Path != "guides/a.md" || hit.Line != 1 || hit.StartLine != 1 || hit.EndLine != 1 ||
		!strings.Contains(hit.Text, "E_DEMO_42") || hit.Citation != "docs@1:guides/a.md#L1" {
		t.Fatalf("hit = %+v", hit)
	}
	if _, err := svc.Grep(context.Background(), "p1", GrepQuery{Pattern: "SECRET-TOKEN", Sources: []string{"secret"}}); err == nil || !strings.Contains(err.Error(), "授权范围") {
		t.Fatalf("unbound source must be rejected: %v", err)
	}
	other, err := svc.Grep(context.Background(), "other", GrepQuery{Pattern: "E_DEMO_42"})
	if err != nil || len(other.Hits) != 0 {
		t.Fatalf("unbound project must see nothing: %+v %v", other, err)
	}
	literal, err := svc.Grep(context.Background(), "p1", GrepQuery{Pattern: "E_DEMO_42."})
	if err != nil || len(literal.Hits) != 0 {
		t.Fatalf("literal dot must not match any character: %+v %v", literal, err)
	}
	regex, err := svc.Grep(context.Background(), "p1", GrepQuery{Pattern: `E_DEMO_\d+`, Regex: true})
	if err != nil || len(regex.Hits) != 1 || regex.Hits[0].Line != 1 {
		t.Fatalf("regex = %+v %v", regex, err)
	}
}

func TestGrepRejectsLongOrInvalidPatternsAndCapsHits(t *testing.T) {
	svc := grepFixture(strings.Repeat("HIT line\n", GrepHitLimit+5))
	long := strings.Repeat("字", GrepPatternMaxRunes+1)
	if _, err := svc.Grep(context.Background(), "p1", GrepQuery{Pattern: long, Regex: true}); err == nil || !strings.Contains(err.Error(), "最长") {
		t.Fatalf("long pattern: %v", err)
	}
	if _, err := svc.Grep(context.Background(), "p1", GrepQuery{Pattern: `(\w+)\1`, Regex: true}); err == nil || !strings.Contains(err.Error(), "正则无效") {
		t.Fatalf("unsafe regexp must be rejected: %v", err)
	}
	if _, err := svc.Grep(context.Background(), "p1", GrepQuery{Pattern: "   "}); err == nil {
		t.Fatal("empty pattern accepted")
	}
	out, err := svc.Grep(context.Background(), "p1", GrepQuery{Pattern: "HIT"})
	if err != nil || len(out.Hits) != GrepHitLimit || !out.Truncated {
		t.Fatalf("hit cap = %d truncated=%t err=%v", len(out.Hits), out.Truncated, err)
	}
	skipped, err := svc.Grep(context.Background(), "p1", GrepQuery{Pattern: "NOTE"})
	if err != nil {
		t.Fatal(err)
	}
	store := svc.store.(*fakeStore)
	store.docs["docs"] = append(store.docs["docs"], domain.KnowledgeDocument{SourceID: "docs", Path: "notes.txt", Commit: "1", Body: "NOTE only in text"})
	skipped, err = svc.Grep(context.Background(), "p1", GrepQuery{Pattern: "NOTE"})
	if err != nil || len(skipped.Hits) != 0 {
		t.Fatalf("non-markdown must be skipped: %+v %v", skipped, err)
	}
}

func TestGrepStopsWhenTimeLimitPasses(t *testing.T) {
	var body strings.Builder
	for i := 0; i < 8; i++ {
		body.WriteString("HIT line\n")
	}
	svc := grepFixture(body.String())
	start := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	calls := 0
	prev := grepNow
	grepNow = func() time.Time {
		calls++
		// 加载来源/文档和逐行匹配共用同一个截止时间；前 6 次取时钟
		// 尚未到期，之后返回超时时间，验证扫描会在最后一行前停止。
		if calls > 6 {
			return start.Add(GrepTimeout + time.Second)
		}
		return start
	}
	t.Cleanup(func() { grepNow = prev })

	out, err := svc.Grep(context.Background(), "p1", GrepQuery{Pattern: "HIT"})
	if err == nil || !strings.Contains(err.Error(), "时间上限") {
		t.Fatalf("timeout = %+v %v", out, err)
	}
	if len(out.Hits) == 0 || len(out.Hits) >= 8 {
		t.Fatalf("deadline should stop before the last line, hits=%d", len(out.Hits))
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	stopped, err := svc.Grep(ctx, "p1", GrepQuery{Pattern: "HIT"})
	if err == nil || !strings.Contains(err.Error(), "时间上限") || len(stopped.Hits) != 0 {
		t.Fatalf("cancelled context = %+v %v", stopped, err)
	}
}

func TestGrepStopsLoadingAtScanBudget(t *testing.T) {
	svc := grepFixture(strings.Repeat("x", GrepScanByteLimit+1))
	out, err := svc.Grep(context.Background(), "p1", GrepQuery{Pattern: "not-there"})
	if err == nil || !strings.Contains(err.Error(), "扫描") {
		t.Fatalf("oversized scan must be rejected: hits=%d err=%v", len(out.Hits), err)
	}
}

func TestGrepChecksContextWhileLoadingSources(t *testing.T) {
	store := newFakeStore()
	store.sources = []domain.KnowledgeSource{
		{ID: "one", BaseID: "base"},
		{ID: "two", BaseID: "base"},
	}
	store.docs = map[string][]domain.KnowledgeDocument{
		"one": {{SourceID: "one", Path: "one.md", Commit: "1", Body: "needle\n"}},
		"two": {{SourceID: "two", Path: "two.md", Commit: "2", Body: "needle\n"}},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	loaded := make([]string, 0, 2)
	store.docsHook = func(source string) {
		loaded = append(loaded, source)
		cancel()
	}
	svc := newTestService(store, &fakeUpstream{})
	out, err := svc.Grep(ctx, "p1", GrepQuery{Pattern: "needle"})
	if err == nil || !strings.Contains(err.Error(), "时间上限") {
		t.Fatalf("cancelled load must stop: hits=%d err=%v", len(out.Hits), err)
	}
	if len(loaded) != 1 {
		t.Fatalf("cancelled load must not read later sources: %v", loaded)
	}
	if len(out.Hits) != 0 {
		t.Fatalf("no body was matched before cancel: %+v", out.Hits)
	}
}

func TestGitFileURLMatchesKnownHosts(t *testing.T) {
	const sha = "0123456789abcdef0123456789abcdef01234567"
	cases := []struct {
		raw  string
		want string
	}{
		{"https://github.com/example/repo.git", "https://github.com/example/repo/blob/" + sha + "/docs/a.md#L2-L4"},
		{"https://gitlab.com/group/repo", "https://gitlab.com/group/repo/-/blob/" + sha + "/docs/a.md#L2-L4"},
		{"https://gitee.com/example/repo.git", "https://gitee.com/example/repo/blob/" + sha + "/docs/a.md#L2-L4"},
		{"https://codeberg.org/example/repo", "https://codeberg.org/example/repo/src/commit/" + sha + "/docs/a.md#L2-L4"},
		{"https://bitbucket.org/example/repo.git", "https://bitbucket.org/example/repo/src/" + sha + "/docs/a.md#lines-2:4"},
	}
	for _, tc := range cases {
		if got := GitFileURL(tc.raw, sha, "docs/a.md", 2, 4); got != tc.want {
			t.Fatalf("url %s = %s, want %s", tc.raw, got, tc.want)
		}
	}
	for _, raw := range []string{"git@github.com:example/repo.git", "https://example.com/repo.git", "https://github.com/example/repo.git"} {
		if raw == "https://github.com/example/repo.git" {
			if got := GitFileURL(raw, "3", "docs/a.md", 1, 1); got != "" {
				t.Fatalf("revision must not grow a web link: %s", got)
			}
			continue
		}
		if got := GitFileURL(raw, sha, "docs/a.md", 1, 1); got != "" {
			t.Fatalf("unsupported url %s produced %s", raw, got)
		}
	}
}
