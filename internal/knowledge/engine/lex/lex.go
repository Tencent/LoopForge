// Package lex 是内存倒排的关键词适配器：连续 CJK 切成二元组，拉丁和数字按词切开。
// 它不是默认检索。默认检索走 MySQL 全文；本包只供对照，不接入 Search。
package lex

import (
	"sort"
	"strings"
	"unicode"
)

// Tokenize 把文本切成词法单元：连续的 CJK 字符切为二元组
// （单字 token 保留以支持单字查询兜底），拉丁/数字连续段作为整体词。
// 二元组选择是为了在没有中文分词器的前提下保证召回与可解释性。
func Tokenize(s string) []string {
	var tokens []string
	var latin strings.Builder
	var cjkRunes []rune

	flushLatin := func() {
		if latin.Len() > 0 {
			t := strings.ToLower(latin.String())
			if len(t) >= 2 {
				tokens = append(tokens, t)
			}
			latin.Reset()
		}
	}
	flushCJK := func() {
		for i, r := range cjkRunes {
			tokens = append(tokens, string(r))
			if i+1 < len(cjkRunes) {
				tokens = append(tokens, string([]rune{cjkRunes[i], cjkRunes[i+1]}))
			}
		}
		cjkRunes = cjkRunes[:0]
	}

	for _, r := range s {
		switch {
		case r >= 0x4E00 && r <= 0x9FFF: // CJK 统一表意
			flushLatin()
			cjkRunes = append(cjkRunes, unicode.ToLower(r))
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			flushCJK()
			latin.WriteRune(r)
		default:
			flushLatin()
			flushCJK()
		}
	}
	flushLatin()
	flushCJK()
	return tokens
}

// Posting 记录一个 token 在一个 chunk 中的词频。
type Posting struct {
	ChunkID int64
	Freq    int
}

// Index 是内存倒排索引。调用方负责并发读写互斥。
type Index struct {
	postings map[string][]Posting
	sizes    map[int64]int // chunkID → token 数（归一化用）
}

func NewIndex() *Index {
	return &Index{postings: map[string][]Posting{}, sizes: map[int64]int{}}
}

func (ix *Index) Add(chunkID int64, text string) {
	terms := Tokenize(text)
	ix.sizes[chunkID] += len(terms)
	counts := map[string]int{}
	for _, t := range terms {
		counts[t]++
	}
	for t, f := range counts {
		ix.postings[t] = append(ix.postings[t], Posting{ChunkID: chunkID, Freq: f})
	}
}

func (ix *Index) Remove(chunkID int64) {
	delete(ix.sizes, chunkID)
	for t, ps := range ix.postings {
		kept := ps[:0]
		for _, p := range ps {
			if p.ChunkID != chunkID {
				kept = append(kept, p)
			}
		}
		if len(kept) == 0 {
			delete(ix.postings, t)
		} else {
			ix.postings[t] = kept
		}
	}
}

// Hit 是一次词法命中：score 由命中 token 数与归一化词频构成，可解释。
type Hit struct {
	ChunkID int64
	Score   float64
	Matched []string
}

// Search 返回按分数降序的命中；distinct token 全覆盖的 chunk 额外加权。
func (ix *Index) Search(query string, limit int) []Hit {
	qTerms := Tokenize(query)
	if len(qTerms) == 0 {
		return nil
	}
	seen := map[string]bool{}
	uniq := make([]string, 0, len(qTerms))
	for _, t := range qTerms {
		if !seen[t] {
			seen[t] = true
			uniq = append(uniq, t)
		}
	}
	type agg struct {
		score   float64
		matched map[string]bool
	}
	aggs := map[int64]*agg{}
	for _, t := range uniq {
		for _, p := range ix.postings[t] {
			a := aggs[p.ChunkID]
			if a == nil {
				a = &agg{matched: map[string]bool{}}
				aggs[p.ChunkID] = a
			}
			a.matched[t] = true
			size := ix.sizes[p.ChunkID]
			norm := 1.0
			if size > 0 {
				norm = float64(p.Freq) / float64(size)
			}
			a.score += 1 + 4*norm
		}
	}
	out := make([]Hit, 0, len(aggs))
	for id, a := range aggs {
		h := Hit{ChunkID: id, Score: a.score}
		for t := range a.matched {
			h.Matched = append(h.Matched, t)
		}
		// 全 token 命中的加权，体现联合命中比散点命中更可靠。
		if len(h.Matched) == len(uniq) && len(uniq) > 1 {
			h.Score *= 1.8
		}
		sort.Strings(h.Matched)
		out = append(out, h)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].ChunkID < out[j].ChunkID
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}
