package config

import "testing"

func TestHTTPPortFromEnv(t *testing.T) {
	cases := []struct {
		value   string
		want    string
		wantErr bool
	}{
		{value: "", want: defaultAPIPort},
		{value: "   ", want: defaultAPIPort},
		{value: " 9090 ", want: "9090"},
		{value: "1", want: "1"},
		{value: "65535", want: "65535"},
		{value: "0", wantErr: true},
		{value: "65536", wantErr: true},
		{value: "http", wantErr: true},
	}
	for _, tc := range cases {
		t.Setenv("AGENTFLOW_HTTP_PORT", tc.value)
		got, err := httpPortFromEnv()
		if tc.wantErr {
			if err == nil || err.Error() != `invalid AGENTFLOW_HTTP_PORT "`+tc.value+`"` {
				t.Fatalf("httpPortFromEnv(%q) err = %v, want invalid port error", tc.value, err)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Fatalf("httpPortFromEnv(%q) = %q, %v; want %q", tc.value, got, err, tc.want)
		}
	}
}

func TestLoadEndpointsRejectsInvalidPort(t *testing.T) {
	clearEndpointEnv(t)
	t.Setenv("AGENTFLOW_HTTP_PORT", "70000")
	if _, err := LoadEndpoints("api", ""); err == nil {
		t.Fatal("LoadEndpoints accepted an out-of-range AGENTFLOW_HTTP_PORT")
	}
}

func TestEnvURL(t *testing.T) {
	cases := map[string]string{
		"":                          "",
		"  ":                        "",
		"/":                         "",
		" http://api.example/ ":     "http://api.example",
		"http://api.example/base//": "http://api.example/base",
		"http://api.example/base":   "http://api.example/base",
	}
	for value, want := range cases {
		t.Setenv("AGENTFLOW_API_URL", value)
		if got := envURL("AGENTFLOW_API_URL"); got != want {
			t.Fatalf("envURL with %q = %q, want %q", value, got, want)
		}
	}
}
