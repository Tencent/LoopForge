package engine

import (
	"sort"
	"strings"
	"unicode"
)

// 问句里的疑问词和句末语气词不承载检索意图。它们交给 MySQL ngram 后会
// 和常见二字片段一起放大噪声，所以在下推查询前直接去掉。这里只处理高频、
// 无歧义的口语成分；剩下的单字虚词在打分阶段按低权重处理，避免把“目的”
// 这类正常词拆坏。
var questionPhrases = []string{
	"为什么", "为何", "怎么样", "怎么办", "怎么", "怎样", "如何",
	"是多少", "多少", "叫什么", "叫啥", "什么", "哪些", "哪个", "哪里", "哪儿",
	"是谁", "谁", "是否", "能否", "可不可以", "可以吗", "是不是", "有没有",
	"请问", "帮我", "帮忙", "告诉我", "我想知道", "想知道", "有什么区别", "区别是什么",
	"一下", "一点", "一些", "一种", "这个", "那个", "这些", "那些", "这里", "那里",
	"然后", "那么", "如果", "因为", "所以", "但是", "而且", "并且", "以及", "还有",
	"吗", "呢", "吧", "啊", "呀", "哦", "嗯", "哇", "嘛", "么", "哪",
}

// functionRunes 是打分时要降权的单字虚词。它们仍留在下推查询里，因为
// “的”“和”常出现在正常词组内部（例如“目的”“和平”）；只在计算标题/小节/
// 正文覆盖度时把这些二字片段压到很低权重，既降噪又不破坏召回。
var functionRunes = map[rune]bool{
	'的': true, '了': true, '着': true, '过': true, '是': true, '在': true,
	'把': true, '被': true, '给': true, '和': true, '与': true, '及': true,
	'或': true, '就': true, '都': true, '也': true, '还': true, '很': true,
	'太': true, '更': true, '最': true, '这': true, '那': true, '其': true,
	'之': true, '于': true, '对': true, '从': true, '到': true, '为': true,
	'以': true, '而': true, '且': true, '并': true, '但': true, '跟': true,
	'同': true, '让': true, '使': true, '由': true, '向': true, '往': true,
	'朝': true, '按': true,
}

const functionTermWeight = 0.15

// queryExpansions 把中文技术口语补成文档里常用的英文词或缩写。只放
// 技术语境下含义稳定的词；扩展词同时进入自然语言通道和强标识符通道。
var queryExpansions = map[string][]string{
	"连接串":  {"DSN"},
	"数据源":  {"DSN"},
	"数据库":  {"DATABASE"},
	"环境变量": {"ENV"},
	"本机":   {"LOCAL"},
	"本地":   {"LOCAL"},
	"平台":   {"PLATFORM"},
	"端口":   {"PORT"},
	"默认":   {"DEFAULT"},
	"启动":   {"START"},
	"运行":   {"RUN"},
	"跑起来":  {"RUN"},
}

// normalizeSearchQuery 去掉疑问词和句末语气词，只留下检索意图。替换成
// 空格而不是直接删除，避免把前后两个词拼出新的二字片段。
func normalizeSearchQuery(query string) string {
	out := stripPhrases(query, questionPhrases)
	out = collapseSpace(out)
	out = strings.TrimSpace(strings.Trim(out, "，。！？、；：,.!?;:()（）[]【】<>《》\"'“”‘’"))
	return appendExpansions(out)
}

func appendExpansions(query string) string {
	lower := strings.ToLower(query)
	var extra []string
	for term, expansions := range queryExpansions {
		if !strings.Contains(query, term) {
			continue
		}
		for _, expansion := range expansions {
			if !strings.Contains(lower, strings.ToLower(expansion)) {
				extra = append(extra, expansion)
			}
		}
	}
	if len(extra) == 0 {
		return query
	}
	sort.Strings(extra)
	return strings.TrimSpace(query + " " + strings.Join(extra, " "))
}

func stripPhrases(s string, phrases []string) string {
	if s == "" || len(phrases) == 0 {
		return s
	}
	runes := []rune(s)
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(runes); {
		matched := ""
		for _, phrase := range phrases {
			pr := []rune(phrase)
			if len(pr) == 0 || i+len(pr) > len(runes) {
				continue
			}
			if string(runes[i:i+len(pr)]) == phrase {
				if len(pr) > len([]rune(matched)) {
					matched = phrase
				}
			}
		}
		if matched != "" {
			b.WriteRune(' ')
			i += len([]rune(matched))
			continue
		}
		b.WriteRune(runes[i])
		i++
	}
	return b.String()
}

func collapseSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// termWeight 给含单字虚词的二字片段降权。整个查询只有一个含虚词的片段时
// 覆盖度仍会归一化到 1，不会把“目的”这类查询直接判空。
func termWeight(term string) float64 {
	for _, r := range term {
		if functionRunes[r] {
			return functionTermWeight
		}
	}
	return 1
}

// exactPhrase 取查询里最长的一段连续汉字（至少 3 个字）作为短语信号。
// 只对足够长的连续片段加权，避免“平台”“环境”这类常见词把所有文档抬高。
func exactPhrase(query string) string {
	longest := ""
	var run []rune
	flush := func() {
		if len(run) >= 3 && len(run) > len([]rune(longest)) {
			longest = string(run)
		}
		run = run[:0]
	}
	for _, r := range query {
		if unicode.Is(unicode.Han, r) {
			run = append(run, r)
			continue
		}
		flush()
	}
	flush()
	return longest
}
