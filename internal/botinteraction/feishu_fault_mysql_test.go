package botinteraction

// Fault tests of the Feishu private-chat loop against a real MySQL and real
// receiver processes. The parent test plays the Feishu open platform (token,
// long-connection bootstrap, protobuf event frames, Message Reply with
// one-hour uuid de-duplication) and the model; each receiver is a child
// process running the production Service and Feishu adapter, killed with
// SIGKILL to simulate crashes. Run with a dedicated, empty database:
//
//	AGENTFLOW_BOT_FAULT_TEST_MYSQL_DSN='root:root@tcp(127.0.0.1:33384)/bot_fault' \
//	  go test -run TestFeishuFaultMySQL -v ./internal/botinteraction
//
// Takeover waits for the 45 s connection lease, so the test runs ~3 minutes.

import (
	"agentflow/internal/credcrypt"
	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
)

const (
	faultDSNEnv   = "AGENTFLOW_BOT_FAULT_TEST_MYSQL_DSN"
	faultChildEnv = "AGENTFLOW_BOT_FAULT_CHILD"
	faultURLEnv   = "AGENTFLOW_BOT_FAULT_PLATFORM"
)

func faultKeyring(t testing.TB) *credcrypt.Keyring {
	k, err := credcrypt.Parse("fault:" + base64.StdEncoding.EncodeToString([]byte("agentflow-fault-test-key-32bytes")))
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// TestFeishuFaultChild is the receiver process. It is a no-op unless started
// by TestFeishuFaultMySQL.
func TestFeishuFaultChild(t *testing.T) {
	if os.Getenv(faultChildEnv) != "1" {
		t.Skip("receiver child process only")
	}
	store, err := repo.Open(os.Getenv(faultDSNEnv))
	if err != nil {
		t.Fatal(err)
	}
	store.SetCredentialKeyring(faultKeyring(t))
	platform := os.Getenv(faultURLEnv)
	svc := New(store)
	ch := newFeishuChannel()
	ch.base = platform
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

// faultPlatform is the simulated Feishu open platform and model.
type faultPlatform struct {
	t   *testing.T
	srv *httptest.Server

	mu       sync.Mutex
	conn     *websocket.Conn
	conns    int
	acks     []faultAck
	frameSeq int
	// replies: every Message Reply request; inbox: what the user sees after
	// uuid de-duplication.
	replies []faultReply
	inbox   map[string][]string
	seen    map[string]bool
	hang    map[string]time.Duration // reply message_id -> hold the response
	model   map[string]int
	block   map[string]chan struct{}
	// pushTimes records when each frame was written, for ack latency.
	pushTimes map[string]time.Time
}

type faultAck struct {
	frame   string
	code    int
	latency time.Duration
}

type faultReply struct {
	messageID, uuid, text string
}

func newFaultPlatform(t *testing.T) *faultPlatform {
	p := &faultPlatform{t: t, inbox: map[string][]string{}, seen: map[string]bool{}, hang: map[string]time.Duration{}, model: map[string]int{}, block: map[string]chan struct{}{}}
	sent := map[string]time.Time{}
	mux := http.NewServeMux()
	mux.HandleFunc("/open-apis/auth/v3/tenant_access_token/internal", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"code": 0, "tenant_access_token": "t-token", "expire": 7200})
	})
	mux.HandleFunc("/callback/ws/endpoint", func(w http.ResponseWriter, r *http.Request) {
		url := "ws" + strings.TrimPrefix(p.srv.URL, "http") + "/ws?device_id=d1&service_id=1"
		json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"URL": url, "ClientConfig": map[string]any{"PingInterval": 120, "ReconnectCount": -1, "ReconnectInterval": 120, "ReconnectNonce": 1}}})
	})
	var upgrader websocket.Upgrader
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		p.mu.Lock()
		p.conn = c
		p.conns++
		p.mu.Unlock()
		for {
			_, raw, err := c.ReadMessage()
			if err != nil {
				return
			}
			var frame larkws.Frame
			if frame.Unmarshal(raw) != nil || frame.Method != int32(larkws.FrameTypeData) {
				continue
			}
			var resp struct {
				Code int `json:"code"`
			}
			json.Unmarshal(frame.Payload, &resp)
			id := larkws.Headers(frame.Headers).GetString("message_id")
			p.mu.Lock()
			p.acks = append(p.acks, faultAck{frame: id, code: resp.Code, latency: time.Since(sent[id])})
			p.mu.Unlock()
		}
	})
	mux.HandleFunc("/open-apis/im/v1/messages/", func(w http.ResponseWriter, r *http.Request) {
		msgID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/open-apis/im/v1/messages/"), "/reply")
		var body struct {
			Content string `json:"content"`
			UUID    string `json:"uuid"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		var content struct {
			Text string `json:"text"`
		}
		json.Unmarshal([]byte(body.Content), &content)
		p.mu.Lock()
		p.replies = append(p.replies, faultReply{msgID, body.UUID, content.Text})
		// Feishu delivers a reply once per uuid within an hour.
		if body.UUID == "" || !p.seen[body.UUID] {
			p.seen[body.UUID] = true
			p.inbox[msgID] = append(p.inbox[msgID], content.Text)
		}
		hold := p.hang[msgID]
		delete(p.hang, msgID)
		p.mu.Unlock()
		if hold > 0 {
			// The message is delivered; the response is lost.
			select {
			case <-time.After(hold):
			case <-r.Context().Done():
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"message_id": "om_r_" + msgID}})
	})
	mux.HandleFunc("/model", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		text := string(b)
		p.mu.Lock()
		p.model[text]++
		gate := p.block[text]
		p.mu.Unlock()
		if gate != nil {
			select {
			case <-gate:
			case <-r.Context().Done():
				return
			}
		}
		fmt.Fprint(w, "答复："+text)
	})
	p.srv = httptest.NewServer(mux)
	p.pushTimes = sent
	return p
}

func (p *faultPlatform) push(eventID, messageID, text string) string {
	p.t.Helper()
	p.mu.Lock()
	c := p.conn
	p.frameSeq++
	frameID := fmt.Sprintf("frame-%d", p.frameSeq)
	p.pushTimes[frameID] = time.Now()
	p.mu.Unlock()
	if c == nil {
		p.t.Fatal("no long connection")
	}
	content, _ := json.Marshal(map[string]string{"text": text})
	payload := feishuEventJSON(eventID, "cli_fault", "tenant-1", messageID, "p2p", "text", string(content))
	frame := larkws.Frame{SeqID: uint64(p.frameSeq), LogID: 1, Service: 1, Method: int32(larkws.FrameTypeData), Headers: []larkws.Header{{Key: "type", Value: "event"}, {Key: "message_id", Value: frameID}, {Key: "sum", Value: "1"}, {Key: "seq", Value: "0"}, {Key: "trace_id", Value: "trace"}}, Payload: []byte(payload)}
	raw, _ := frame.Marshal()
	p.mu.Lock()
	err := c.WriteMessage(websocket.BinaryMessage, raw)
	p.mu.Unlock()
	if err != nil {
		p.t.Fatal(err)
	}
	return frameID
}

func (p *faultPlatform) ackOf(frame string) (faultAck, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, a := range p.acks {
		if a.frame == frame {
			return a, true
		}
	}
	return faultAck{}, false
}

func (p *faultPlatform) gate(text string) chan struct{} {
	p.mu.Lock()
	defer p.mu.Unlock()
	g := make(chan struct{})
	p.block[text] = g
	return g
}

func (p *faultPlatform) snapshot() (inbox map[string][]string, replies []faultReply, model map[string]int, conns int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	inbox = map[string][]string{}
	for k, v := range p.inbox {
		inbox[k] = append([]string(nil), v...)
	}
	model = map[string]int{}
	for k, v := range p.model {
		model[k] = v
	}
	return inbox, append([]faultReply(nil), p.replies...), model, p.conns
}

func (p *faultPlatform) repliesFor(msgID string) []faultReply {
	_, all, _, _ := p.snapshot()
	var out []faultReply
	for _, r := range all {
		if r.messageID == msgID {
			out = append(out, r)
		}
	}
	return out
}

type faultReceiver struct {
	cmd *exec.Cmd
	log string
}

func startFaultReceiver(t *testing.T, dsn, platform, name string) *faultReceiver {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), name+".log")
	out, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestFeishuFaultChild$", "-test.timeout=20m")
	cmd.Env = append(os.Environ(), faultChildEnv+"=1", faultDSNEnv+"="+dsn, faultURLEnv+"="+platform)
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
			if len(lines) > 40 {
				lines = lines[len(lines)-40:]
			}
			t.Logf("%s log:\n%s", name, strings.Join(lines, "\n"))
		}
	})
	return r
}

func (r *faultReceiver) kill() {
	if r.cmd.ProcessState == nil {
		r.cmd.Process.Kill()
		r.cmd.Wait()
	}
}

func waitFor(t *testing.T, what string, timeout time.Duration, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %s", timeout, what)
}

func TestFeishuFaultMySQL(t *testing.T) {
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
	bot := domain.NotificationBot{ID: "cfg-fault", Channel: domain.BotChannelFeishu, BotID: "cli_fault", Name: "fault", ProjectID: "p-fault", OwnerID: 1, Enabled: true, InteractionEnabled: true}
	saved, err := store.SaveNotificationBotChecked(bot, "app-secret", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordBotTenantCandidate(bot.ID, "tenant-1", "seed", saved.CredentialVersion); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ConfirmBotTenant(bot.ID, "tenant-1", saved.ConfigVersion, 1); err != nil {
		t.Fatal(err)
	}
	p := newFaultPlatform(t)
	defer p.srv.Close()
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		var rows []domain.BotInteraction
		if err := store.DB.Where("bot_id = ?", bot.ID).Order("created_at").Find(&rows).Error; err != nil {
			t.Logf("read interactions: %v", err)
		}
		for _, r := range rows {
			t.Logf("interaction %s status=%s attempts=%d gen=%d reason=%s err=%q reply=%q", r.ID, r.Status, r.SendAttempts, r.DeliveryGeneration, r.TerminalReason, r.LastError, r.Reply)
		}
		_, replies, model, conns := p.snapshot()
		t.Logf("platform replies=%+v model=%v conns=%d acks=%+v", replies, model, conns, p.acks)
	})
	item := func(msgID string) domain.BotInteraction {
		got, _ := store.BotInteraction(repo.BotMessageID(bot.ID, msgID))
		return got
	}
	replied := func(msgID string) func() bool {
		return func() bool { return item(msgID).Status == domain.BotReplied }
	}
	report := func(name, msgID, text string) {
		inbox, _, model, _ := p.snapshot()
		it := item(msgID)
		t.Logf("%s: interaction=%s status=%s generation=%d attempts=%d platform_message=%s model_runs=%d reply_requests=%d user_saw=%d",
			name, it.ID, it.Status, it.DeliveryGeneration, it.SendAttempts, it.PlatformMessageID, model[text], len(p.repliesFor(msgID)), len(inbox[msgID]))
	}

	a := startFaultReceiver(t, dsn, p.srv.URL, "receiver-a")
	waitFor(t, "receiver A long connection", 30*time.Second, func() bool { _, _, _, n := p.snapshot(); return n == 1 })

	// F1 duplicate delivery: the same business message arrives three times
	// with different event IDs while the model is still running.
	g1 := p.gate("F1")
	f1 := []string{p.push("e1", "om_f1", "F1")}
	waitFor(t, "F1 model start", 10*time.Second, func() bool { _, _, m, _ := p.snapshot(); return m["F1"] == 1 })
	f1 = append(f1, p.push("e1-redeliver-1", "om_f1", "F1"), p.push("e1-redeliver-2", "om_f1", "F1"))
	waitFor(t, "F1 acks", 10*time.Second, func() bool { _, ok := p.ackOf(f1[2]); return ok })
	close(g1)
	waitFor(t, "F1 replied", 20*time.Second, replied("om_f1"))
	inbox, _, model, _ := p.snapshot()
	if model["F1"] != 1 || len(inbox["om_f1"]) != 1 || len(p.repliesFor("om_f1")) != 1 {
		t.Fatalf("F1 duplicate delivery: model=%d inbox=%v replies=%v", model["F1"], inbox["om_f1"], p.repliesFor("om_f1"))
	}
	for _, f := range f1 {
		if a, _ := p.ackOf(f); a.code != http.StatusOK {
			t.Fatalf("F1 ack %s = %+v", f, a)
		}
	}
	report("F1 duplicate delivery", "om_f1", "F1")

	// F2 slow storage: writes are blocked for 4 s (FLUSH TABLES WITH READ
	// LOCK), longer than Feishu's 3 s ack window. The receiver must answer
	// within the window with a failure; the redelivery after the database
	// recovers is stored once and processed once.
	db, _ := store.DB.DB()
	lockConn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lockConn.ExecContext(context.Background(), "FLUSH TABLES WITH READ LOCK"); err != nil {
		t.Fatal(err)
	}
	slow := p.push("e2", "om_f2", "F2")
	time.Sleep(4 * time.Second)
	ack, acked := p.ackOf(slow)
	if _, err := lockConn.ExecContext(context.Background(), "UNLOCK TABLES"); err != nil {
		t.Fatal(err)
	}
	lockConn.Close()
	if !acked || ack.code == http.StatusOK || ack.latency >= 3*time.Second {
		t.Fatalf("F2 ack under blocked storage = %+v acked=%v (want a failure ack within 3s)", ack, acked)
	}
	redeliver := p.push("e2-redeliver", "om_f2", "F2")
	waitFor(t, "F2 redelivery ack", 10*time.Second, func() bool { _, ok := p.ackOf(redeliver); return ok })
	waitFor(t, "F2 replied", 20*time.Second, replied("om_f2"))
	inbox, _, model, _ = p.snapshot()
	if model["F2"] != 1 || len(inbox["om_f2"]) != 1 {
		t.Fatalf("F2 slow storage: model=%d inbox=%v", model["F2"], inbox["om_f2"])
	}
	t.Logf("F2 slow storage: first ack code=%d after %s, redelivery ack code=%d", ack.code, ack.latency.Round(time.Millisecond), func() int { a, _ := p.ackOf(redeliver); return a.code }())
	report("F2 slow storage", "om_f2", "F2")

	// F3 long connection drop during a model turn: the reply still goes out
	// once over the HTTP API, the receiver reconnects and keeps working.
	g3 := p.gate("F3")
	p.push("e3", "om_f3", "F3")
	waitFor(t, "F3 model start", 10*time.Second, func() bool { _, _, m, _ := p.snapshot(); return m["F3"] == 1 })
	p.mu.Lock()
	p.conn.Close()
	p.mu.Unlock()
	close(g3)
	waitFor(t, "F3 replied", 20*time.Second, replied("om_f3"))
	waitFor(t, "F3 reconnect", 40*time.Second, func() bool { _, _, _, n := p.snapshot(); return n == 2 })
	p.push("e3b", "om_f3b", "F3b")
	waitFor(t, "F3b replied after reconnect", 20*time.Second, replied("om_f3b"))
	inbox, _, model, _ = p.snapshot()
	if model["F3"] != 1 || len(inbox["om_f3"]) != 1 || len(inbox["om_f3b"]) != 1 {
		t.Fatalf("F3 drop: model=%d inbox=%v %v", model["F3"], inbox["om_f3"], inbox["om_f3b"])
	}
	report("F3 connection drop", "om_f3", "F3")

	// F4 receiver crash during the model turn: a new receiver takes over
	// after the lease expires, reports the interruption instead of running
	// the model again, and sends it once.
	p.gate("F4")
	p.push("e4", "om_f4", "F4")
	waitFor(t, "F4 model start", 10*time.Second, func() bool { _, _, m, _ := p.snapshot(); return m["F4"] == 1 })
	a.kill()
	crashAt := time.Now()
	b := startFaultReceiver(t, dsn, p.srv.URL, "receiver-b")
	waitFor(t, "F4 takeover and interruption notice", 90*time.Second, replied("om_f4"))
	inbox, _, model, _ = p.snapshot()
	if model["F4"] != 1 || len(inbox["om_f4"]) != 1 || !strings.Contains(inbox["om_f4"][0], "中断") {
		t.Fatalf("F4 crash in turn: model=%d inbox=%v", model["F4"], inbox["om_f4"])
	}
	conn, _ := store.BotConnectionState(saved.LeaseKey())
	t.Logf("F4 crash in turn: takeover after %s, epoch=%d owner_changes=%d", time.Since(crashAt).Round(time.Second), conn.Epoch, conn.OwnerChanges)
	report("F4 crash in turn", "om_f4", "F4")

	// F5 receiver crash while the reply request is in flight: the platform
	// delivered the message but the response is lost with the process. The
	// next receiver retries with the same uuid; the user sees one message.
	p.mu.Lock()
	p.hang["om_f5"] = time.Minute
	p.mu.Unlock()
	p.push("e5", "om_f5", "F5")
	waitFor(t, "F5 reply request", 20*time.Second, func() bool { return len(p.repliesFor("om_f5")) == 1 })
	if st := item("om_f5").Status; st != domain.BotSending {
		t.Fatalf("F5 status before crash = %s", st)
	}
	b.kill()
	startFaultReceiver(t, dsn, p.srv.URL, "receiver-c")
	waitFor(t, "F5 recovery", 120*time.Second, replied("om_f5"))
	rs := p.repliesFor("om_f5")
	inbox, _, _, _ = p.snapshot()
	if len(rs) != 2 || rs[0].uuid == "" || rs[0].uuid != rs[1].uuid || len(inbox["om_f5"]) != 1 {
		t.Fatalf("F5 crash in send: replies=%+v inbox=%v", rs, inbox["om_f5"])
	}
	report("F5 crash in send", "om_f5", "F5")

	// F6 response timeout without a crash (unknown outcome): retried with the
	// same uuid after 5 s; one message reaches the user.
	p.mu.Lock()
	p.hang["om_f6"] = 12 * time.Second
	p.mu.Unlock()
	p.push("e6", "om_f6", "F6")
	waitFor(t, "F6 retry", 40*time.Second, replied("om_f6"))
	rs = p.repliesFor("om_f6")
	inbox, _, _, _ = p.snapshot()
	if len(rs) != 2 || rs[0].uuid != rs[1].uuid || len(inbox["om_f6"]) != 1 {
		t.Fatalf("F6 unknown outcome: replies=%+v inbox=%v", rs, inbox["om_f6"])
	}
	report("F6 response timeout", "om_f6", "F6")

	var unexpected int64
	store.DB.Model(&domain.BotInteraction{}).Where("bot_id = ? AND status <> ?", bot.ID, domain.BotReplied).Count(&unexpected)
	var total int64
	store.DB.Model(&domain.BotInteraction{}).Where("bot_id = ?", bot.ID).Count(&total)
	if unexpected != 0 || total != 7 {
		t.Fatalf("interactions total=%d not replied=%d", total, unexpected)
	}
}
