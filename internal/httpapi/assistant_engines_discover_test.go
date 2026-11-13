package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestAssistantModelsEndpoint(t *testing.T) {
	cases := []struct {
		base      string
		anthropic bool
		want      string
	}{
		{"https://gw.example.com/v1/", false, "https://gw.example.com/v1/models"},
		{"https://gw.example.com", true, "https://gw.example.com/v1/models"},
		{"http://127.0.0.1:8080//", true, "http://127.0.0.1:8080/v1/models"},
	}
	for _, tc := range cases {
		got, err := assistantModelsEndpoint(tc.base, tc.anthropic)
		if err != nil || got != tc.want {
			t.Errorf("assistantModelsEndpoint(%q, %v) = %q, %v; want %q", tc.base, tc.anthropic, got, err, tc.want)
		}
	}
	for _, base := range []string{"", "gw.example.com", "ftp://gw.example.com"} {
		if _, err := assistantModelsEndpoint(base, false); err == nil || err.Error() != "Base URL 必须以 http:// 或 https:// 开头" {
			t.Errorf("assistantModelsEndpoint(%q) error = %v", base, err)
		}
	}
}

func TestDecodeAssistantDiscoveredModels(t *testing.T) {
	body := []byte(`{"data":[
		{"id":" model-a ","display_name":" Model A ","name":"ignored"},
		{"id":"model-b","name":" Model B "},
		{"id":"model-a","display_name":"duplicate"},
		{"id":"bad id"},
		{"id":""},
		{"id":"model-c","display_name":"` + strings.Repeat("名", 70) + `"}
	]}`)
	got, err := decodeAssistantDiscoveredModels("https://gw/models", body)
	if err != nil {
		t.Fatal(err)
	}
	want := []assistantDiscoveredModel{
		{ID: "model-a", Name: "Model A"},
		{ID: "model-b", Name: "Model B"},
		{ID: "model-c", Name: strings.Repeat("名", 64) + "…"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("models = %#v\nwant %#v", got, want)
	}
}

func TestDecodeAssistantDiscoveredModelsCapsAndRejectsNonJSON(t *testing.T) {
	var items []string
	for i := 0; i < assistantDiscoveryMaxModels+5; i++ {
		items = append(items, fmt.Sprintf(`{"id":"m-%d"}`, i))
	}
	got, err := decodeAssistantDiscoveredModels("https://gw/models", []byte(`{"data":[`+strings.Join(items, ",")+`]}`))
	if err != nil || len(got) != assistantDiscoveryMaxModels {
		t.Fatalf("len = %d, err = %v; want %d", len(got), err, assistantDiscoveryMaxModels)
	}
	if got[len(got)-1].ID != fmt.Sprintf("m-%d", assistantDiscoveryMaxModels-1) {
		t.Fatalf("last model = %q", got[len(got)-1].ID)
	}
	if _, err := decodeAssistantDiscoveredModels("https://gw/models", []byte("<html>")); err == nil || err.Error() != "https://gw/models 返回的不是模型列表" {
		t.Fatalf("non-JSON error = %v", err)
	}
}

func TestDiscoverAssistantEngineModelsFallsBackToAPIKeyHeader(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "" {
			seen = append(seen, "bearer")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		seen = append(seen, "x-api-key:"+r.Header.Get("x-api-key")+":"+r.Header.Get("anthropic-version"))
		_, _ = w.Write([]byte(`{"data":[{"id":"claude-x","display_name":"Claude X"}]}`))
	}))
	defer srv.Close()

	got, err := discoverAssistantEngineModels(context.Background(), true, "secret", srv.URL+"/")
	if err != nil {
		t.Fatal(err)
	}
	if want := []assistantDiscoveredModel{{ID: "claude-x", Name: "Claude X"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("models = %#v", got)
	}
	if want := []string{"bearer", "x-api-key:secret:2023-06-01"}; !reflect.DeepEqual(seen, want) {
		t.Fatalf("auth attempts = %v, want %v", seen, want)
	}
}

func TestDiscoverAssistantEngineModelsReportsRejectedKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	_, err := discoverAssistantEngineModels(context.Background(), false, "secret", srv.URL)
	if want := fmt.Sprintf("%s/models 拒绝了 API Key（HTTP 403）", srv.URL); err == nil || err.Error() != want {
		t.Fatalf("error = %v, want %q", err, want)
	}
	if _, err := discoverAssistantEngineModels(context.Background(), false, "secret", "gw.example.com"); err == nil {
		t.Fatal("expected Base URL scheme error")
	}
}
