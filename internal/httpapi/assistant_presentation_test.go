package httpapi

import "testing"

func TestAssistantPresentationPageCountIncludesCover(t *testing.T) {
	slides := "```slides\n# 封面\n\n---\n\n# 准备\n\n---\n\n# 方法\n\n---\n\n# 注意\n```"
	for _, request := range []string{"帮我做三页 PPT", "总共3页，不另加封面"} {
		want, got := assistantPresentationPageMismatch(slides, request)
		if want != 3 || got != 4 {
			t.Fatalf("page mismatch lost: %d %d", want, got)
		}
	}
	if want, _ := assistantPresentationPageMismatch(slides, "做四页 PPT"); want != 0 {
		t.Fatal("valid pages rejected")
	}
	if want, _ := assistantPresentationPageMismatch("```html\n<p>3页</p>\n```", "做3页网页"); want != 0 {
		t.Fatal("HTML was treated as slides")
	}
	for _, request := range []string{"修改第2页的标题", "把第二页换成图片", "修改第 2 页，不改变其他内容"} {
		if want, _ := assistantPresentationPageMismatch(slides, request); want != 0 {
			t.Fatal("editing a page was treated as changing the page count")
		}
	}
	if want, _ := assistantPresentationPageMismatch(slides, "从5页缩减为4页，修改第2页标题"); want != 0 {
		t.Fatal("the revised total page count was ignored")
	}
}
