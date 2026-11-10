package lex

import "testing"

func TestTokenizeCJKBigrams(t *testing.T) {
	toks := Tokenize("套餐售罄")
	var hasPair bool
	for _, tk := range toks {
		if tk == "套餐" || tk == "餐售" || tk == "售罄" {
			hasPair = true
		}
	}
	if !hasPair {
		t.Fatalf("期望包含二元组，got %v", toks)
	}
	var hasSingle bool
	for _, tk := range toks {
		if tk == "套" {
			hasSingle = true
		}
	}
	if !hasSingle {
		t.Fatalf("期望包含单字 token，got %v", toks)
	}
}

func TestTokenizeLatin(t *testing.T) {
	toks := Tokenize("EPP 2306 blackList")
	seen := map[string]bool{}
	for _, tk := range toks {
		seen[tk] = true
	}
	if !seen["epp"] || !seen["2306"] || !seen["blacklist"] {
		t.Fatalf("拉丁/数字 token 缺失: %v", toks)
	}
}

func TestSearchRankingAndExplanation(t *testing.T) {
	ix := NewIndex()
	ix.Add(1, "套餐售罄排查手册 按手册逐项排查 静态售罄 库存")
	ix.Add(2, "成功率告警排查 CAPI InternalError backCost")
	ix.Add(3, "域名实名认证 CNNIC 黑名单 2306")

	hits := ix.Search("套餐售罄", 10)
	if len(hits) == 0 || hits[0].ChunkID != 1 {
		t.Fatalf("期望 chunk 1 居首，got %+v", hits)
	}
	if len(hits[0].Matched) == 0 {
		t.Fatalf("命中应带匹配 token 供解释")
	}
	for _, h := range hits {
		if h.ChunkID == 2 {
			t.Fatalf("chunk 2 与查询无关，却被召回")
		}
	}
}

func TestRemove(t *testing.T) {
	ix := NewIndex()
	ix.Add(1, "售罄")
	ix.Add(2, "超时")
	ix.Remove(1)
	if hits := ix.Search("售罄", 10); len(hits) != 0 {
		t.Fatalf("Remove 后仍能搜到 chunk 1: %+v", hits)
	}
	if hits := ix.Search("超时", 10); len(hits) != 1 {
		t.Fatalf("chunk 2 应保留: %+v", hits)
	}
}
