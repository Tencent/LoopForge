package auth

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// 声明为 JSON（带参数）时也要按内容嗅探，不能直接落到 image/png 兜底。
func TestPhotoContentTypeSniffsJSONDeclaredGIF(t *testing.T) {
	if got := photoContentType("application/json; charset=utf-8", []byte("GIF89a......")); got != "image/gif" {
		t.Fatalf("photoContentType(json, gif) = %q, want image/gif", got)
	}
}

// 只有 200 算成功：其它 2xx（如 206 部分内容）即使有响应体也按异常状态处理。
func TestReadPhotoResponseRejectsNon200Success(t *testing.T) {
	for _, status := range []int{http.StatusCreated, http.StatusPartialContent} {
		_, _, err := readPhotoResponse(photoResponse(status, "image/png", "GIF89a......"))
		if err == nil || errors.Is(err, ErrPhotoNotFound) {
			t.Fatalf("status %d: err = %v, want unexpected status error", status, err)
		}
		if !strings.HasPrefix(err.Error(), "unexpected status ") {
			t.Fatalf("status %d: err = %v, want unexpected status prefix", status, err)
		}
	}
}

// 异常状态的错误信息只截取响应体前 1024 字节。
func TestReadPhotoResponseTruncatesErrorExcerpt(t *testing.T) {
	body := strings.Repeat("a", 1024) + strings.Repeat("b", 2048)
	_, _, err := readPhotoResponse(photoResponse(http.StatusInternalServerError, "", body))
	if err == nil {
		t.Fatal("expected error")
	}
	want := "unexpected status 500 from SSO photo: " + strings.Repeat("a", 1024)
	if err.Error() != want {
		t.Fatalf("err length = %d, want %d (excerpt must stop at 1024 bytes)", len(err.Error()), len(want))
	}
}

// 头像最多读取 5MB，超出部分被截断而不是整块读入内存。
func TestReadPhotoResponseCapsBodyAt5MB(t *testing.T) {
	const limit = 5 << 20
	body := append([]byte("GIF89a"), bytes.Repeat([]byte{0}, limit)...)
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"image/gif"}}, Body: io.NopCloser(bytes.NewReader(body))}
	data, ct, err := readPhotoResponse(resp)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(data) != limit {
		t.Fatalf("len(data) = %d, want %d", len(data), limit)
	}
	if ct != "image/gif" {
		t.Fatalf("ct = %q, want image/gif", ct)
	}
}

type failingPhotoBody struct{ err error }

func (b failingPhotoBody) Read([]byte) (int, error) { return 0, b.err }
func (failingPhotoBody) Close() error               { return nil }

// 读取响应体失败时返回包装后的错误，保留原始错误链，不当作 404。
func TestReadPhotoResponseWrapsReadError(t *testing.T) {
	cause := errors.New("connection reset")
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: failingPhotoBody{err: cause}}
	data, ct, err := readPhotoResponse(resp)
	if !errors.Is(err, cause) || !strings.Contains(err.Error(), "read photo response body failed") {
		t.Fatalf("err = %v, want wrapped read error", err)
	}
	if errors.Is(err, ErrPhotoNotFound) || data != nil || ct != "" {
		t.Fatalf("data=%q ct=%q err=%v, want nil/empty and non-sentinel error", data, ct, err)
	}
}

// 404、空响应体、异常状态都不能写入缓存：上游恢复后下一次请求应重新拉取并拿到头像。
func TestFetchPhotoDoesNotCacheFailedResponses(t *testing.T) {
	gif := []byte("GIF89a......")
	failures := []struct {
		name   string
		status int
		body   []byte
		want   func(error) bool
	}{
		{"not found", http.StatusNotFound, nil, func(err error) bool { return errors.Is(err, ErrPhotoNotFound) }},
		{"empty body", http.StatusOK, nil, func(err error) bool { return errors.Is(err, ErrPhotoNotFound) }},
		{"bad gateway", http.StatusBadGateway, []byte("down"), func(err error) bool {
			return err != nil && strings.HasPrefix(err.Error(), "unexpected status 502")
		}},
	}
	for _, tc := range failures {
		t.Run(tc.name, func(t *testing.T) {
			var calls int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if atomic.AddInt32(&calls, 1) == 1 {
					w.WriteHeader(tc.status)
					_, _ = w.Write(tc.body)
					return
				}
				w.Header().Set("Content-Type", "image/gif")
				_, _ = w.Write(gif)
			}))
			defer server.Close()

			client := NewClient(Config{PaaSID: "test_paas", AppToken: "test_token", AuthHost: server.URL, PhotoURI: "/sso/photo"})
			ctx := context.Background()

			if _, _, err := client.FetchPhoto(ctx, "lisi", 100); !tc.want(err) {
				t.Fatalf("first FetchPhoto err = %v", err)
			}
			if _, _, ok := client.photoCache.get("100:lisi"); ok {
				t.Fatal("failed response must not be cached")
			}
			data, ct, err := client.FetchPhoto(ctx, "lisi", 100)
			if err != nil || !bytes.Equal(data, gif) || ct != "image/gif" {
				t.Fatalf("second FetchPhoto = data %q ct %q err %v, want fresh gif", data, ct, err)
			}
			if got := atomic.LoadInt32(&calls); got != 2 {
				t.Fatalf("server calls = %d, want 2", got)
			}
		})
	}
}
