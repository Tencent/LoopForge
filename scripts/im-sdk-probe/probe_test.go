// Package imsdkprobe pins and exercises the Feishu and DingTalk SDK versions
// evaluated for enterprise IM channels (docs/proposals/enterprise-im.md §8).
// It is a separate module so the main go.mod only gains a dependency when an
// adapter actually uses it. Run: go test -count=1 -v ./...
package imsdkprobe

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"

	dtbot "github.com/open-dingtalk/dingtalk-stream-sdk-go/chatbot"
	dtclient "github.com/open-dingtalk/dingtalk-stream-sdk-go/client"

	"github.com/gorilla/websocket"
)

// Compile-time use of the adapter surface Stage E needs.
func feishuSurface() {
	api := lark.NewClient("app", "secret", lark.WithReqTimeout(10*time.Second))
	_ = larkim.NewReplyMessageReqBuilder().MessageId("om_x").Body(larkim.NewReplyMessageReqBodyBuilder().MsgType("text").Content(`{"text":"x"}`).Uuid("send-key").Build()).Build()
	_ = larkim.NewCreateMessageReqBuilder().ReceiveIdType("open_id").Body(larkim.NewCreateMessageReqBodyBuilder().ReceiveId("ou").MsgType("text").Content(`{"text":"x"}`).Uuid("k").Build()).Build()
	_ = api.Tenant.V2.Tenant.Query
	_ = api.Im.V1.Message.Reply
	d := dispatcher.NewEventDispatcher("", "").OnP2MessageReceiveV1(func(ctx context.Context, ev *larkim.P2MessageReceiveV1) error {
		_ = ev.EventV2Base.Header.AppID
		_ = ev.EventV2Base.Header.TenantKey
		_ = ev.EventV2Base.Header.EventID
		_ = *ev.Event.Message.MessageId
		_ = *ev.Event.Sender.SenderId.OpenId
		return nil
	})
	_ = larkws.NewClient("app", "secret", larkws.WithEventHandler(d), larkws.WithLogLevel(larkcore.LogLevelError))
}

func TestFeishuStartStopsOnContextCancel(t *testing.T) {
	feishuSurface()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv.Close()
	d := dispatcher.NewEventDispatcher("", "")
	cli := larkws.NewClient("app", "secret", larkws.WithEventHandler(d), larkws.WithDomain(srv.URL), larkws.WithLogLevel(larkcore.LogLevelError))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- cli.Start(ctx) }()
	time.Sleep(500 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		t.Logf("feishu Start returned after cancel: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("feishu Start did not return after context cancel")
	}
}

// dingtalkServer returns an open API host whose endpoint points at a local
// websocket that never answers pings and later pushes a text frame.
func dingtalkServer(t *testing.T) *httptest.Server {
	upgrader := websocket.Upgrader{}
	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("/v1.0/gateway/connections/open", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"endpoint": "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws", "ticket": "t"})
	})
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		c.SetPingHandler(func(string) error { return nil }) // swallow pings: no pong
		go func() {
			for {
				if _, _, err := c.ReadMessage(); err != nil {
					return
				}
			}
		}()
		time.Sleep(10 * time.Second) // keepAlive 3s + pong timeout 5s
		c.WriteMessage(websocket.TextMessage, []byte(`{"type":"SYSTEM","headers":{"topic":"ping","messageId":"m"},"data":"{}"}`))
		time.Sleep(2 * time.Second)
		c.Close()
	})
	srv = httptest.NewServer(mux)
	return srv
}

// Runs in a child process so an unrecovered SDK goroutine panic is observable.
func TestDingTalkPongTimeoutChild(t *testing.T) {
	if os.Getenv("DT_CHILD") != "1" {
		t.Skip("child only")
	}
	srv := dingtalkServer(t)
	defer srv.Close()
	cli := dtclient.NewStreamClient(
		dtclient.WithAppCredential(dtclient.NewAppCredentialConfig("id", "secret")),
		dtclient.WithOpenApiHost(srv.URL),
		dtclient.WithAutoReconnect(false),
		dtclient.WithKeepAlive(3*time.Second),
	)
	cli.RegisterChatBotCallbackRouter(func(ctx context.Context, d *dtbot.BotCallbackDataModel) ([]byte, error) { return nil, nil })
	if err := cli.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(14 * time.Second)
	cli.Close()
}

// TestDingTalkPongTimeoutThenFrame documents the v0.9.1 defect behind the
// decision in §8.3: after a pong timeout, a late frame panics with "send on
// closed channel" in an SDK goroutine and terminates the host process. If
// this test fails after an SDK upgrade, re-evaluate using StreamClient.
func TestDingTalkPongTimeoutThenFrame(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=TestDingTalkPongTimeoutChild", "-test.v")
	cmd.Env = append(os.Environ(), "DT_CHILD=1")
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "send on closed channel") {
		t.Fatalf("known StreamClient defect no longer reproduces (exit=%v); re-evaluate docs/proposals/enterprise-im.md §8.3\n%s", err, out)
	}
	t.Logf("dingtalk v0.9.1 StreamClient crashed the process as documented (exit=%v)", err)
}
