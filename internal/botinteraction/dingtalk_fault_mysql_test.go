package botinteraction

// Fault tests of the DingTalk private-chat loop against a real MySQL and real
// receiver processes running the production Service and Stream adapter. The
// parent plays the DingTalk gateway (connection open, Stream frames, session
// webhook) and the model; receivers are child processes killed with SIGKILL.
//
//	AGENTFLOW_BOT_FAULT_TEST_MYSQL_DSN='root:root@tcp(127.0.0.1:33384)/bot_fault_dt?parseTime=true' \
//	  go test -run TestDingTalkFaultMySQL -v ./internal/botinteraction
//
// Takeover waits for the 45 s connection lease; the test runs ~2 minutes.

import (
	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/open-dingtalk/dingtalk-stream-sdk-go/payload"
)

// TestDingTalkFaultChild is the receiver process; a no-op unless started by
// TestDingTalkFaultMySQL.
func TestDingTalkFaultChild(t *testing.T) {
	if os.Getenv(faultChildEnv) != "dingtalk" {
		t.Skip("receiver child process only")
	}
	store, err := repo.Open(os.Getenv(faultDSNEnv))
	if err != nil {
		t.Fatal(err)
	}
	store.SetCredentialKeyring(faultKeyring(t))
	platform := os.Getenv(faultURLEnv)
	svc := New(store)
	ch := newDingTalkChannel()
	ch.api = platform
	ch.allowInsecureStream = true
	// Short keepalive so a silent gateway is detected within seconds.
	ch.keepAlive, ch.pongWait = time.Second, time.Second
	host := strings.TrimPrefix(platform, "http://")
	ch.webhookAllowed = func(u *url.URL) bool { return u.Host == host && u.Path == "/robot/sendBySession" }
	svc.RegisterChannel(ch)
	svc.Generate = func(ctx context.Context, _ domain.NotificationBot, _ domain.BotConversation, _ []domain.BotInteraction, item domain.BotInteraction) (string, error) {
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, platform+"/model", strings.NewReader(item.Content))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return string(body), nil
	}
	svc.Start(context.Background())
	select {}
}

type dtGateway struct {
	t   *testing.T
	srv *httptest.Server

	mu      sync.Mutex
	conn    *websocket.Conn
	conns   int
	silent  bool // stop answering WebSocket pings
	acks    map[string]int
	sent    []string // session webhook posts: "<session>|<text>"
	hang    map[string]time.Duration
	model   map[string]int
	block   map[string]chan struct{}
	writeMu sync.Mutex
}

func newDTGateway(t *testing.T) *dtGateway {
	g := &dtGateway{t: t, acks: map[string]int{}, hang: map[string]time.Duration{}, model: map[string]int{}, block: map[string]chan struct{}{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1.0/gateway/connections/open", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(payload.ConnectionEndpointResponse{Endpoint: "ws" + strings.TrimPrefix(g.srv.URL, "http") + "/stream", Ticket: "t"})
	})
	var upgrader websocket.Upgrader
	mux.HandleFunc("/stream", func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		g.mu.Lock()
		if g.silent {
			c.SetPingHandler(func(string) error { return nil })
		}
		g.conn = c
		g.conns++
		g.mu.Unlock()
		for {
			_, raw, err := c.ReadMessage()
			if err != nil {
				return
			}
			var resp payload.DataFrameResponse
			if json.Unmarshal(raw, &resp) == nil {
				g.mu.Lock()
				g.acks[resp.Headers["messageId"]] = resp.Code
				g.mu.Unlock()
			}
		}
	})
	mux.HandleFunc("/robot/sendBySession", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Text struct {
				Content string `json:"content"`
			} `json:"text"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		session := r.URL.Query().Get("session")
		g.mu.Lock()
		g.sent = append(g.sent, session+"|"+body.Text.Content)
		hold := g.hang[session]
		delete(g.hang, session)
		g.mu.Unlock()
		if hold > 0 {
			select {
			case <-time.After(hold):
			case <-r.Context().Done():
			}
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"errcode": 0, "errmsg": "ok"})
	})
	mux.HandleFunc("/model", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		text := string(b)
		g.mu.Lock()
		g.model[text]++
		gate := g.block[text]
		g.mu.Unlock()
		if gate != nil {
			select {
			case <-gate:
			case <-r.Context().Done():
				return
			}
		}
		fmt.Fprint(w, "答复："+text)
	})
	g.srv = httptest.NewServer(mux)
	return g
}

func (g *dtGateway) push(frameID, msgID, text string, expires time.Time) {
	g.t.Helper()
	g.mu.Lock()
	c := g.conn
	g.mu.Unlock()
	if c == nil {
		g.t.Fatal("no stream connection")
	}
	data, _ := json.Marshal(map[string]any{
		"conversationId": "cid-1", "chatbotCorpId": "ding-corp", "senderCorpId": "ding-corp", "msgId": msgID,
		"senderStaffId": "staff-1", "conversationType": "1", "msgtype": "text", "text": map[string]string{"content": text},
		"sessionWebhook": g.srv.URL + "/robot/sendBySession?session=" + msgID, "sessionWebhookExpiredTime": expires.UnixMilli(),
	})
	frame := payload.DataFrame{SpecVersion: "1.0", Type: "CALLBACK", Headers: payload.DataFrameHeader{"topic": payload.BotMessageCallbackTopic, "messageId": frameID, "contentType": "application/json"}, Data: string(data)}
	g.writeMu.Lock()
	err := c.WriteMessage(websocket.TextMessage, frame.Encode())
	g.writeMu.Unlock()
	if err != nil {
		g.t.Fatal(err)
	}
}

func (g *dtGateway) ack(frameID string) (int, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	code, ok := g.acks[frameID]
	return code, ok
}

func (g *dtGateway) sentFor(msgID string) []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []string
	for _, s := range g.sent {
		if strings.HasPrefix(s, msgID+"|") {
			out = append(out, s)
		}
	}
	return out
}

func (g *dtGateway) modelRuns(text string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.model[text]
}

func (g *dtGateway) gate(text string) chan struct{} {
	g.mu.Lock()
	defer g.mu.Unlock()
	c := make(chan struct{})
	g.block[text] = c
	return c
}

func (g *dtGateway) connCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.conns
}

func startDingTalkReceiver(t *testing.T, dsn, platform, name string) *faultReceiver {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), name+".log")
	out, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestDingTalkFaultChild$", "-test.timeout=20m")
	cmd.Env = append(os.Environ(), faultChildEnv+"=dingtalk", faultDSNEnv+"="+dsn, faultURLEnv+"="+platform)
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	r := &faultReceiver{cmd: cmd, log: logPath}
	t.Cleanup(func() {
		r.kill()
		if t.Failed() {
			raw, _ := os.ReadFile(logPath)
			lines := strings.Split(string(raw), "\n")
			if len(lines) > 30 {
				lines = lines[len(lines)-30:]
			}
			t.Logf("%s log:\n%s", name, strings.Join(lines, "\n"))
		}
	})
	return r
}

func TestDingTalkFaultMySQL(t *testing.T) {
	dsn := os.Getenv(faultDSNEnv)
	if dsn == "" {
		t.Skip("set " + faultDSNEnv + " to a dedicated, empty MySQL database")
	}
	store, err := repo.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	store.SetCredentialKeyring(faultKeyring(t))
	bot := domain.NotificationBot{ID: "cfg-dt-fault", Channel: domain.BotChannelDingTalk, BotID: "ding-fault", Name: "fault", ProjectID: "p-fault", OwnerID: 1, Enabled: true, InteractionEnabled: true, TenantExpected: "ding-corp", TenantExpectedBy: 1}
	saved, err := store.SaveNotificationBotChecked(bot, "client-secret", nil)
	if err != nil {
		t.Fatal(err)
	}
	g := newDTGateway(t)
	defer g.srv.Close()
	item := func(msgID string) domain.BotInteraction {
		it, _ := store.BotInteraction(repo.BotMessageID(bot.ID, msgID))
		return it
	}
	status := func(msgID, want string) func() bool {
		return func() bool { return item(msgID).Status == want }
	}
	report := func(name, msgID, text string) {
		it := item(msgID)
		t.Logf("%s: status=%s reason=%s generation=%d attempts=%d model_runs=%d webhook_posts=%d", name, it.Status, it.TerminalReason, it.DeliveryGeneration, it.SendAttempts, g.modelRuns(text), len(g.sentFor(msgID)))
	}
	later := func() time.Time { return time.Now().Add(time.Hour) }

	a := startDingTalkReceiver(t, dsn, g.srv.URL, "receiver-a")
	waitFor(t, "receiver A stream", 30*time.Second, func() bool { return g.connCount() == 1 })

	// D1 duplicate delivery while the model runs; the first message also
	// locks the enterprise entered by the administrator.
	g1 := g.gate("D1")
	g.push("d1-a", "md1", "D1", later())
	waitFor(t, "D1 model start", 10*time.Second, func() bool { return g.modelRuns("D1") == 1 })
	g.push("d1-b", "md1", "D1", later())
	g.push("d1-c", "md1", "D1", later())
	waitFor(t, "D1 acks", 10*time.Second, func() bool { _, ok := g.ack("d1-c"); return ok })
	close(g1)
	waitFor(t, "D1 replied", 20*time.Second, status("md1", domain.BotReplied))
	for _, f := range []string{"d1-a", "d1-b", "d1-c"} {
		if code, _ := g.ack(f); code != 200 {
			t.Fatalf("D1 ack %s = %d", f, code)
		}
	}
	if g.modelRuns("D1") != 1 || len(g.sentFor("md1")) != 1 {
		t.Fatalf("D1: model=%d posts=%v", g.modelRuns("D1"), g.sentFor("md1"))
	}
	if b, _ := store.NotificationBot(bot.ID); b.TenantState != domain.BotTenantLocked || b.ConfigVersion != saved.ConfigVersion+1 {
		t.Fatalf("D1 tenant = %+v", b)
	}
	report("D1 duplicate delivery", "md1", "D1")

	// D2 storage blocked beyond the ack window.
	db, _ := store.DB.DB()
	lockConn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lockConn.ExecContext(context.Background(), "FLUSH TABLES WITH READ LOCK"); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	g.push("d2-a", "md2", "D2", later())
	waitFor(t, "D2 ack under lock", 5*time.Second, func() bool { _, ok := g.ack("d2-a"); return ok })
	ackAfter := time.Since(start)
	time.Sleep(4*time.Second - ackAfter)
	lockConn.ExecContext(context.Background(), "UNLOCK TABLES")
	lockConn.Close()
	if code, _ := g.ack("d2-a"); code != 500 || ackAfter >= 3*time.Second {
		t.Fatalf("D2 ack = %d after %s", code, ackAfter)
	}
	g.push("d2-b", "md2", "D2", later())
	waitFor(t, "D2 replied", 20*time.Second, status("md2", domain.BotReplied))
	if g.modelRuns("D2") != 1 || len(g.sentFor("md2")) != 1 {
		t.Fatalf("D2: model=%d posts=%v", g.modelRuns("D2"), g.sentFor("md2"))
	}
	t.Logf("D2 blocked storage: first ack 500 after %s", ackAfter.Round(time.Millisecond))
	report("D2 blocked storage", "md2", "D2")

	// D3 gateway goes silent (no pongs) during a model turn, then sends a
	// late frame on the dead connection — the SDK crash scenario. The
	// receiver must survive, reconnect and still deliver the reply.
	g3 := g.gate("D3")
	g.push("d3-a", "md3", "D3", later())
	waitFor(t, "D3 model start", 10*time.Second, func() bool { return g.modelRuns("D3") == 1 })
	g.mu.Lock()
	g.silent = true
	old := g.conn
	g.mu.Unlock()
	old.SetPingHandler(func(string) error { return nil })
	time.Sleep(3 * time.Second)
	g.writeMu.Lock()
	_ = old.WriteMessage(websocket.TextMessage, []byte(`{"type":"SYSTEM","headers":{"topic":"ping","messageId":"late"}}`))
	g.writeMu.Unlock()
	g.mu.Lock()
	g.silent = false
	g.mu.Unlock()
	close(g3)
	waitFor(t, "D3 reconnect", 40*time.Second, func() bool { return g.connCount() >= 2 })
	waitFor(t, "D3 replied", 20*time.Second, status("md3", domain.BotReplied))
	if a.cmd.ProcessState != nil {
		t.Fatalf("receiver exited: %v", a.cmd.ProcessState)
	}
	g.push("d3-b", "md3b", "D3b", later())
	waitFor(t, "D3b replied after reconnect", 20*time.Second, status("md3b", domain.BotReplied))
	if g.modelRuns("D3") != 1 || len(g.sentFor("md3")) != 1 {
		t.Fatalf("D3: model=%d posts=%v", g.modelRuns("D3"), g.sentFor("md3"))
	}
	report("D3 silent gateway + late frame", "md3", "D3")

	// D4 receiver crash with the reply in flight: DingTalk has no
	// idempotency key, so the next receiver records reply_unknown instead of
	// sending again; a manual resend sends the saved reply once more.
	g.mu.Lock()
	g.hang["md4"] = time.Minute
	g.mu.Unlock()
	conns := g.connCount()
	g.push("d4-a", "md4", "D4", later())
	waitFor(t, "D4 webhook post", 20*time.Second, func() bool { return len(g.sentFor("md4")) == 1 })
	a.kill()
	b := startDingTalkReceiver(t, dsn, g.srv.URL, "receiver-b")
	waitFor(t, "D4 reply_unknown after takeover", 120*time.Second, status("md4", domain.BotReplyUnknown))
	waitFor(t, "receiver B stream", 30*time.Second, func() bool { return g.connCount() > conns })
	time.Sleep(2 * time.Second)
	if posts := g.sentFor("md4"); len(posts) != 1 {
		t.Fatalf("D4 resent automatically: %v", posts)
	}
	if _, err := store.ResendBotInteraction(repo.BotMessageID(bot.ID, "md4"), 1, "d4-manual", "核对后补发"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "D4 manual resend", 20*time.Second, status("md4", domain.BotReplied))
	if posts := g.sentFor("md4"); len(posts) != 2 || item("md4").DeliveryGeneration != 2 {
		t.Fatalf("D4 manual resend: posts=%v gen=%d", posts, item("md4").DeliveryGeneration)
	}
	report("D4 crash in send + manual resend", "md4", "D4")

	// D5 session webhook expiring before the reply is ready.
	g5 := g.gate("D5")
	g.push("d5-a", "md5", "D5", time.Now().Add(12*time.Second))
	waitFor(t, "D5 model start", 10*time.Second, func() bool { return g.modelRuns("D5") == 1 })
	time.Sleep(4 * time.Second)
	close(g5)
	waitFor(t, "D5 route_expired", 20*time.Second, status("md5", domain.BotReplyFailed))
	if it := item("md5"); it.TerminalReason != "route_expired" || it.Reply != "答复：D5" || it.ReplyRoute != "" || len(g.sentFor("md5")) != 0 {
		t.Fatalf("D5 = %+v posts=%v", it, g.sentFor("md5"))
	}
	report("D5 expired session webhook", "md5", "D5")
	b.kill()
}
