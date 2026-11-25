package httpapi

import "testing"

func TestTrailingSlashEntity(t *testing.T) {
	for _, tc := range []struct {
		rest string
		want string
	}{
		{"", ""},
		{"users/", "users"},
		{"users", "users"},
		{"users/42/", "users"},
		{"auth/", "auth"},
		{"auth/tokens/", "auth/tokens"},
		{"auth/tokens/abc/", "auth/tokens"},
		{"auth/tokens", "auth/tokens"},
		{"auth/other/", "auth"},
		{"otel/v1/traces/", "otel"},
		{"unknown/x/", "unknown"},
	} {
		if got := trailingSlashEntity(tc.rest); got != tc.want {
			t.Errorf("trailingSlashEntity(%q) = %q want %q", tc.rest, got, tc.want)
		}
	}
}
