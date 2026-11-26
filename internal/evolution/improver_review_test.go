package evolution

import (
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func TestCandidateReviewKeepsOnlyGroundedExcerptsAndDiagnosisReferences(t *testing.T) {
	c := Candidate{Content: "先检查测试失败，再检查退出摘要。", Review: &domain.EvoChangeReview{
		Problem: "退出摘要遮蔽失败", Reason: "优先定位具体失败。",
		Changes: []domain.EvoChangePoint{
			{Title: "优先测试失败", Before: "选择首条错误", After: "先检查测试失败", SourceRunIDs: []string{"diagnosis", "holdout", "diagnosis", ""}},
			{Title: "模型编造的旧规则", Before: "禁用安全检查", After: "先检查测试失败"},
			{Title: "模型编造的新规则", Before: "选择首条错误", After: "跳过所有验收"},
			{Title: "没有修改", Before: "相同", After: "相同"},
		},
	}}
	r := CandidateReview(c, "选择首条错误。", []Evidence{{RunID: "diagnosis"}})
	if r == nil || len(r.Changes) != 1 || r.Changes[0].After != "先检查测试失败" {
		t.Fatalf("unsupported excerpts survived: %+v", r)
	}
	if ids := r.Changes[0].SourceRunIDs; len(ids) != 1 || ids[0] != "diagnosis" {
		t.Fatalf("must not link invented or holdout samples: %v", ids)
	}
	if len(c.Review.Changes[0].SourceRunIDs) != 4 {
		t.Fatal("normalization must not mutate the model payload")
	}
}

func TestCandidateReviewIsOptionalAndBounded(t *testing.T) {
	for _, raw := range []string{`{"content":"正文"}`, `{"content":"正文","review":null}`, `{"content":"正文","review":"invalid"}`, `{"content":"正文","review":{"changes":"invalid"}}`} {
		c, err := ParseCandidate(raw)
		if err != nil || c.Content != "正文" || c.Review != nil {
			t.Fatalf("optional metadata blocked a usable candidate: %+v %v", c, err)
		}
	}
	c := Candidate{Content: "新增要求", Review: &domain.EvoChangeReview{Problem: strings.Repeat("长", 90)}}
	for i := 0; i < 8; i++ {
		c.Review.Changes = append(c.Review.Changes, domain.EvoChangePoint{Title: "新增要求", After: "新增要求"})
	}
	r := CandidateReview(c, "原文", nil)
	if len(r.Changes) != 3 || len([]rune(r.Problem)) > 41 {
		t.Fatalf("review metadata must be bounded: %+v", r)
	}
}
