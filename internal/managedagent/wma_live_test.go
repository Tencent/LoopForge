package managedagent

import (
	"context"
	"os"
	"strings"
	"testing"
)

// Run with an existing dedicated Flash session and Tencent Cloud test credentials.
func TestWMATencentCloudLive(t *testing.T) {
	session := os.Getenv("LF_WMA_E2E_SESSION_ID")
	if session == "" {
		t.Skip("set LF_WMA_E2E_SESSION_ID to run the live WMA test")
	}
	client, err := NewWMA(Config{
		AgentID: os.Getenv("LF_WMA_E2E_AGENT_ID"), SessionID: session,
		TencentCloud: &TencentCloudConfig{
			SecretID: os.Getenv("AGENTFLOW_WMA_SECRET_ID"), SecretKey: os.Getenv("AGENTFLOW_WMA_SECRET_KEY"),
			Token: os.Getenv("AGENTFLOW_WMA_SECURITY_TOKEN"), Region: os.Getenv("AGENTFLOW_WMA_REGION"),
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	const expected = "WMA_E2E_OK"
	var streamed strings.Builder
	result, err := client.Run(context.Background(),
		"Only output WMA_E2E_OK. Do not call tools.", func(text string) { streamed.WriteString(text) })
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(result) != expected || result != streamed.String() {
		t.Fatalf("live WMA returned an unexpected result (%d bytes, %d streamed bytes)", len(result), streamed.Len())
	}
}
