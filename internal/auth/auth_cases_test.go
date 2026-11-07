package auth

import (
	"strings"
	"testing"
	"time"
)

func TestAuthTypesAndPhotoCacheBranches(t *testing.T) {
	var nilUser *User
	if nilUser.ToDomain().ID != 0 {
		t.Fatal("nil user conversion")
	}
	u := &User{ID: 7, LoginName: "alice"}
	token, err := SignUser(u, "secret")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseSignedUser(token, "secret")
	if err != nil || parsed.ID != 7 {
		t.Fatalf("parsed = %#v, %v", parsed, err)
	}
	for _, bad := range []string{"bad", token + ".x", strings.Split(token, ".")[0] + ".bad"} {
		if _, err := ParseSignedUser(bad, "secret"); err == nil {
			t.Fatalf("token %q accepted", bad)
		}
	}
	if _, err := SignUser(nil, "x"); err == nil {
		t.Fatal("nil user signed")
	}
	cache := newPhotoCache(time.Millisecond, 2)
	cache.set("a", []byte("a"), "image/png")
	cache.set("b", []byte("b"), "image/png")
	cache.set("c", []byte("c"), "image/png")
	if _, _, ok := cache.get("c"); !ok {
		t.Fatal("new cache item missing")
	}
	cache.mu.Lock()
	entry := cache.entries["c"]
	entry.expireAt = time.Now().Add(-time.Second)
	cache.entries["c"] = entry
	cache.mu.Unlock()
	if _, _, ok := cache.get("c"); ok {
		t.Fatal("expired item returned")
	}
	cache.clear()
	var nilCache *photoCache
	nilCache.set("x", nil, "")
	nilCache.clear()
	if _, _, ok := nilCache.get("x"); ok {
		t.Fatal("nil cache hit")
	}
}

func TestPhotoAndLoginHelpers(t *testing.T) {
	for _, spec := range []int{-1, 1, 48, 74, 75, 125, 126, 300, 301} {
		if got := NormalizePhotoSpec(spec); got != 48 && got != 100 && got != 150 && got != 500 {
			t.Fatalf("spec %d = %d", spec, got)
		}
	}
	for _, raw := range []string{"", "../a", "a/b", "a b", ".png"} {
		if _, err := SanitizeUsername(raw); err == nil {
			t.Fatalf("username %q accepted", raw)
		}
	}
	if got, err := SanitizeUsername(" Alice.JPG "); err != nil || got != "Alice" {
		t.Fatalf("username = %q, %v", got, err)
	}
	if AvatarURL("../x", 100) != "" || !strings.Contains(AvatarURL("alice", 125), "/100/alice.png") {
		t.Fatal("avatar URL")
	}
	guest := FallbackGuestUser()
	if !guest.IsGuest || guest.LoginName != "guest" {
		t.Fatal("guest")
	}
	headers := GenerateRIOHeaders("p", "t", "1", "2")
	if len(headers["x-rio-signature"]) != 64 {
		t.Fatal("RIO signature")
	}
}
