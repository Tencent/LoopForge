package domain

import "testing"

func TestGongfengMRKey(t *testing.T) {
	t.Parallel()
	if got := gongfengMRKey("Team/Frontend", 7); got != "team/frontend#7" {
		t.Fatalf("key=%q", got)
	}
}

func TestAnalyticsMRKey(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		mr   DeliveryMR
		want string
	}{
		{"url wins over fields", DeliveryMR{Project: "other/repo", IID: 9, URL: "http://git.example.com/Team/Frontend/merge_requests/1"}, "team/frontend#1"},
		{"fields when url unparseable", DeliveryMR{Project: "Team/Backend", IID: 2, URL: "local"}, "team/backend#2"},
		{"trimmed url fallback", DeliveryMR{URL: "  local-mr  "}, "local-mr"},
		{"invalid iid falls back to url", DeliveryMR{Project: "team/x", URL: "a"}, "a"},
		{"empty", DeliveryMR{}, ""},
	}
	for _, tc := range cases {
		if got := analyticsMRKey(tc.mr); got != tc.want {
			t.Errorf("%s: key=%q want %q", tc.name, got, tc.want)
		}
	}
}
