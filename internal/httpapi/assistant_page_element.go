package httpapi

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"agentflow/internal/domain"
)

// Page elements the user picks in the native preview ride along as
// references of type "element". Unlike other references they are not looked
// up: the browser describes the element, so the description is bounded and
// framed as the user's context here.

const pageElementReference = "element"

var pageElementIDPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)

// pageElementStyleLimit bounds the computed styles kept per element.
const pageElementStyleLimit = 32

func pageElementContext(ref domain.AssistantReference) (domain.AssistantReference, string, error) {
	if !pageElementIDPattern.MatchString(ref.ID) || ref.Element == nil {
		return ref, "", fmt.Errorf("页面元素引用无效")
	}
	in := ref.Element
	e := &domain.AssistantPageElement{
		URL:       truncateRunes(strings.TrimSpace(in.URL), 2000),
		Title:     truncateRunes(in.Title, 200),
		Selector:  truncateRunes(in.Selector, 1000),
		Tag:       truncateRunes(in.Tag, 40),
		Text:      truncateRunes(in.Text, 500),
		HTML:      truncateRunes(in.HTML, 4000),
		Rect:      in.Rect,
		Viewport:  in.Viewport,
		Component: truncateRunes(in.Component, 200),
		Framework: truncateRunes(in.Framework, 40),
		Source:    truncateRunes(in.Source, 500),
	}
	if len(in.Styles) > 0 {
		keys := make([]string, 0, len(in.Styles))
		for k := range in.Styles {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		e.Styles = map[string]string{}
		for _, k := range keys {
			if len(e.Styles) == pageElementStyleLimit {
				break
			}
			e.Styles[truncateRunes(k, 60)] = truncateRunes(in.Styles[k], 200)
		}
	}
	name := strings.TrimSpace(truncateRunes(ref.Name, 80))
	if name == "" {
		name = e.Tag
	}
	canonical := domain.AssistantReference{Type: pageElementReference, ID: ref.ID, Name: name, Element: e}
	raw, _ := json.Marshal(e)
	text := "用户在对话侧栏的原生预览里选中的页面元素「" + name + "」，说明这一轮要改的位置。" +
		"有 source/component 时先按它找代码；需要核对时用 workbench_browser 打开 url 并按 selector 查看。元素信息：" + string(raw)
	return canonical, text, nil
}
