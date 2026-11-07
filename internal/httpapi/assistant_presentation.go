package httpapi

import (
	"regexp"
	"strconv"
	"strings"
)

var assistantRequestedPages = regexp.MustCompile(`([0-9]{1,2}|[一二两三四五六七八九十]{1,3})\s*页`)
var assistantSlidesFence = regexp.MustCompile("(?s)```(?:slides|ppt)\\b[^\\n]*\\n(.*?)```")
var assistantSlideBreak = regexp.MustCompile(`(?m)^\s*---\s*$`)

func assistantPresentationPageMismatch(message, request string) (want, got int) {
	count := ""
	for _, match := range assistantRequestedPages.FindAllStringSubmatchIndex(request, -1) {
		// Editing “第 2 页” is not a request to change the total to two pages.
		if strings.HasSuffix(strings.TrimSpace(request[:match[0]]), "第") {
			continue
		}
		count = request[match[2]:match[3]]
	}
	if count == "" {
		return 0, 0
	}
	want, _ = strconv.Atoi(count)
	if want == 0 {
		digits := map[rune]int{'一': 1, '二': 2, '两': 2, '三': 3, '四': 4, '五': 5, '六': 6, '七': 7, '八': 8, '九': 9}
		current := 0
		for _, r := range count {
			if r == '十' {
				if current == 0 {
					current = 1
				}
				want += current * 10
				current = 0
			} else {
				current = digits[r]
			}
		}
		want += current
	}
	if want < 1 || want > 50 {
		return 0, 0
	}
	for _, block := range assistantSlidesFence.FindAllStringSubmatch(message, -1) {
		got = 0
		for _, page := range assistantSlideBreak.Split(block[1], -1) {
			if strings.TrimSpace(page) != "" {
				got++
			}
		}
		if got != want {
			return want, got
		}
	}
	return 0, 0
}
