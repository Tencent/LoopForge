package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestNormalizePhotoSpec(t *testing.T) {
	tests := []struct {
		input int
		want  int
	}{
		{input: 0, want: 100},
		{input: -10, want: 100},
		{input: 30, want: 48},
		{input: 48, want: 48},
		{input: 60, want: 48},
		{input: 80, want: 100},
		{input: 100, want: 100},
		{input: 120, want: 100},
		{input: 130, want: 150},
		{input: 150, want: 150},
		{input: 200, want: 150},
		{input: 500, want: 500},
		{input: 800, want: 500},
	}

	for _, tt := range tests {
		if got := NormalizePhotoSpec(tt.input); got != tt.want {
			t.Errorf("NormalizePhotoSpec(%d) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestSanitizeUsername(t *testing.T) {
	tests := []struct {
		input   string
		want    string
		wantErr bool
	}{
		{input: "zhangsan", want: "zhangsan", wantErr: false},
		{input: "  zhangsan.png  ", want: "zhangsan", wantErr: false},
		{input: "lisi.PNG", want: "lisi", wantErr: false},
		{input: "john.doe", want: "john.doe", wantErr: false},
		{input: "user-123_test", want: "user-123_test", wantErr: false},
		{input: "", wantErr: true},
		{input: "   ", wantErr: true},
		{input: "../admin", wantErr: true},
		{input: "admin/../../etc", wantErr: true},
		{input: "user;cat /etc/passwd", wantErr: true},
		{input: "user<script>", wantErr: true},
	}

	for _, tt := range tests {
		got, err := SanitizeUsername(tt.input)
		if (err != nil) != tt.wantErr {
			t.Errorf("SanitizeUsername(%q) err = %v, wantErr %v", tt.input, err, tt.wantErr)
			continue
		}
		if !tt.wantErr && got != tt.want {
			t.Errorf("SanitizeUsername(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestAvatarURL(t *testing.T) {
	if got := AvatarURL("zhangsan", 100); got != "/api/auth/photo/100/zhangsan.png" {
		t.Errorf("AvatarURL got %q, want /api/auth/photo/100/zhangsan.png", got)
	}
	if got := AvatarURL("lisi.png", 48); got != "/api/auth/photo/48/lisi.png" {
		t.Errorf("AvatarURL got %q, want /api/auth/photo/48/lisi.png", got)
	}
	if got := AvatarURL("bad/user", 100); got != "" {
		t.Errorf("AvatarURL for invalid username got %q, want empty", got)
	}
}

func TestFetchPhoto_SuccessAndCache(t *testing.T) {
	var callCount int32
	fakePNG := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR...")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&callCount, 1)

		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if r.URL.Path != "/ebus/rhrc/photo/100/zhangsan.png" {
			t.Errorf("expected path /ebus/rhrc/photo/100/zhangsan.png, got %s", r.URL.Path)
		}
		if r.Header.Get("x-rio-paasid") != "test_paas" {
			t.Errorf("expected x-rio-paasid=test_paas, got %s", r.Header.Get("x-rio-paasid"))
		}
		if r.Header.Get("x-rio-signature") == "" {
			t.Errorf("expected non-empty signature")
		}

		w.Header().Set("Content-Type", "image/png")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(fakePNG)
	}))
	defer server.Close()

	client := NewClient(Config{
		PaaSID:   "test_paas",
		AppToken: "test_token",
		AuthHost: server.URL,
	})

	ctx := context.Background()

	// 第一次调用：网络请求
	data, ct, err := client.FetchPhoto(ctx, "zhangsan", 100)
	if err != nil {
		t.Fatalf("first FetchPhoto failed: %v", err)
	}
	if string(data) != string(fakePNG) {
		t.Errorf("data mismatch")
	}
	if ct != "image/png" {
		t.Errorf("expected image/png, got %s", ct)
	}
	if atomic.LoadInt32(&callCount) != 1 {
		t.Fatalf("expected 1 server call, got %d", callCount)
	}

	// 第二次调用：应当命中缓存，不发起网络请求
	dataCached, ctCached, err := client.FetchPhoto(ctx, "zhangsan.png", 100)
	if err != nil {
		t.Fatalf("cached FetchPhoto failed: %v", err)
	}
	if string(dataCached) != string(fakePNG) {
		t.Errorf("cached data mismatch")
	}
	if ctCached != "image/png" {
		t.Errorf("expected image/png, got %s", ctCached)
	}
	if atomic.LoadInt32(&callCount) != 1 {
		t.Fatalf("expected still 1 server call due to cache, got %d", callCount)
	}

	// 清理缓存后再调用
	client.ClearPhotoCache()
	_, _, err = client.FetchPhoto(ctx, "zhangsan", 100)
	if err != nil {
		t.Fatalf("post-clear FetchPhoto failed: %v", err)
	}
	if atomic.LoadInt32(&callCount) != 2 {
		t.Fatalf("expected 2 server calls after cache clear, got %d", callCount)
	}
}

func TestFetchPhoto_NotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	client := NewClient(Config{
		PaaSID:   "test_paas",
		AppToken: "test_token",
		AuthHost: server.URL,
	})

	_, _, err := client.FetchPhoto(context.Background(), "unknown_user", 100)
	if err != ErrPhotoNotFound {
		t.Errorf("expected ErrPhotoNotFound, got %v", err)
	}
}

func TestFetchPhoto_NoCredentials(t *testing.T) {
	client := NewClient(Config{
		PaaSID:   "",
		AppToken: "",
	})

	_, _, err := client.FetchPhoto(context.Background(), "zhangsan", 100)
	if err != ErrCredentialsNotConfigured {
		t.Errorf("expected ErrCredentialsNotConfigured, got %v", err)
	}
}
