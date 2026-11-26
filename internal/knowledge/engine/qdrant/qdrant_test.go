package qdrant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEnsureCollectionDimMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 已存在但维度是 768。
		_ = json.NewEncoder(w).Encode(map[string]any{
			"result": map[string]any{"config": map[string]any{"params": map[string]any{
				"vectors": map[string]any{"size": 768},
			}}},
		})
	}))
	defer srv.Close()
	c := New(srv.URL, "")
	if err := c.EnsureCollection(context.Background(), "col", 1024); err == nil {
		t.Fatal("维度不一致应报错，提示换 collection 并重建")
	}
}

func TestDeleteByFilterUsesPayloadMatch(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	c := New(srv.URL, "secret-key")
	if err := c.DeleteByFilter(context.Background(), "col", "src1", "a.md"); err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPost || !strings.HasSuffix(gotPath, "/points/delete") {
		t.Fatalf("删除请求错误: %s %s", gotMethod, gotPath)
	}
	filter, _ := gotBody["filter"].(map[string]any)
	must, _ := filter["must"].([]any)
	if len(must) != 2 { // source_id + path
		t.Fatalf("应按 source_id 与 path 过滤: %+v", gotBody)
	}
}

func TestSearchAddsSourceFilterAndApiKey(t *testing.T) {
	var gotKey string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("api-key")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"result": []map[string]any{{"id": 7, "score": 0.9, "payload": map[string]any{"source_id": "s1"}}},
		})
	}))
	defer srv.Close()
	c := New(srv.URL, "k")
	pts, err := c.Search(context.Background(), "col", []float32{1, 0}, []string{"s1", "s2"}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pts) != 1 || pts[0].ID != 7 {
		t.Fatalf("检索结果解析错误: %+v", pts)
	}
	if gotKey != "k" {
		t.Fatalf("应带 api-key 头: %q", gotKey)
	}
	filter, _ := gotBody["filter"].(map[string]any)
	should, _ := filter["should"].([]any)
	if len(should) != 2 {
		t.Fatalf("应按来源过滤: %+v", gotBody["filter"])
	}
}

func TestUpsertBatchesPoints(t *testing.T) {
	count := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	c := New(srv.URL, "")
	points := make([]Point, maxPointsPerUpsert+5)
	for i := range points {
		points[i] = Point{ID: uint64(i + 1), Vector: []float32{1, 0}}
	}
	if err := c.Upsert(context.Background(), "col", points); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("超限应拆成两次 upsert: %d", count)
	}
}
