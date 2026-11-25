package repo

import (
	"agentflow/internal/domain"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestBotConnectionLeaseAndInterruptedTurn(t *testing.T) {
	store, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	bot := domain.NotificationBot{ID: "b", BotID: "external", ProjectID: "p", OwnerID: 1, Enabled: true, InteractionEnabled: true}
	if err := store.SaveNotificationBot(bot, "s"); err != nil {
		t.Fatal(err)
	}
	clock := pinDBClock(t, 1_000_000_000_000)
	a, ok, err := store.ClaimBotConnection("external", "owner-a")
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if _, ok, _ := store.ClaimBotConnection("external", "owner-b"); ok {
		t.Fatal("two connections claimed")
	}
	clock(BotConnectionLeaseUS + 1)
	b, ok, _ := store.ClaimBotConnection("external", "owner-b")
	if !ok || b.Epoch != a.Epoch+1 {
		t.Fatalf("expired lease unavailable or epoch kept: %+v %+v", a, b)
	}
	if ok, _ := store.RenewBotConnection(a, "connected", ""); ok {
		t.Fatal("stale owner renewed lease")
	}
	item := domain.BotInteraction{ID: BotMessageID("b", "m1"), BotID: "b", SenderID: "u", Content: "重试"}
	if ok, err := store.EnqueueBotInteraction(item, "group", "g"); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if ok, _ := store.EnqueueBotInteraction(item, "group", "g"); ok {
		t.Fatal("duplicate event")
	}
	if _, err := store.ClaimBotInteraction("b", a, BotSendPolicy{}); err != ErrBotLeaseLost {
		t.Fatalf("old owner claimed work: %v", err)
	}
	claimed, err := store.ClaimBotInteraction("b", b, BotSendPolicy{})
	if err != nil || claimed == nil || claimed.Status != "running" || claimed.LeaseEpoch != b.Epoch {
		t.Fatal(claimed, err)
	}
	// owner-b stops renewing; owner-c takes over and recovers the turn.
	clock(BotConnectionLeaseUS + 1)
	c, ok, _ := store.ClaimBotConnection("external", "owner-c")
	if !ok {
		t.Fatal("takeover failed")
	}
	if err = store.RecoverBotInteractions("b", c, BotSendPolicy{}); err != nil {
		t.Fatal(err)
	}
	recovered, _ := store.ClaimBotInteraction("b", c, BotSendPolicy{})
	if recovered == nil || recovered.Status != "ready" || recovered.Reply == "" {
		t.Fatal("interrupted turn was replayed")
	}
	if err = store.SaveBotReply(*claimed, b, "late reply", ""); err != ErrBotLeaseLost {
		t.Fatalf("stale owner saved: %v", err)
	}
	row, _ := store.BotInteraction(item.ID)
	if row.Reply == "late reply" {
		t.Fatal("stale model overwrote recovered reply")
	}
}

func TestBotCardDecisionCannotOverwriteConcurrentRunWrite(t *testing.T) {
	store, _ := OpenTest()
	defer store.Close()
	run := domain.Run{ID: "card-cas", Status: "waiting", Nodes: map[string]*domain.NodeRun{"gate": {NodeID: "gate", Status: "waiting_approval", Attempt: 1}}}
	if err := store.PutRun(run); err != nil {
		t.Fatal(err)
	}
	old, _ := store.RunState(run.ID)
	newer, _ := store.RunState(run.ID)
	newer.Nodes["gate"].Attempt = 2
	if err := store.PutRun(newer); err != nil {
		t.Fatal(err)
	}
	old.Status = "running"
	old.Nodes["gate"].Status = "succeeded"
	if err := store.PutRunAtRevision(old, old.Revision); err != ErrRunDecisionStale {
		t.Fatalf("stale decision: %v", err)
	}
	final, _ := store.RunState(run.ID)
	if final.Nodes["gate"].Attempt != 2 || final.Nodes["gate"].Status != "waiting_approval" {
		t.Fatal("stale card overwrote concurrent run")
	}
}

func TestBotInteractionMySQLLeaseCompetition(t *testing.T) {
	dsn := os.Getenv("AGENTFLOW_BOT_TEST_MYSQL_DSN")
	if dsn == "" {
		dsn = os.Getenv("AGENTFLOW_TEST_MYSQL_DSN")
	}
	if dsn == "" {
		t.Skip("local Docker MySQL not configured")
	}
	store, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	id := fmt.Sprintf("qa-%d", time.Now().UnixNano())
	defer store.DB.Where("bot_id = ?", id).Delete(&domain.BotConnection{})
	var winners atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, ok, err := store.ClaimBotConnection(id, fmt.Sprintf("owner-%d", i))
			if err != nil {
				t.Error(err)
			}
			if ok {
				winners.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if winners.Load() != 1 {
		t.Fatalf("connection owners=%d", winners.Load())
	}
	item := domain.BotInteraction{ID: BotMessageID(id, "message"), BotID: id, SenderID: "u", Content: "消息"}
	defer store.DB.Where("bot_id = ?", id).Delete(&domain.BotInteraction{})
	defer store.DB.Where("bot_id = ?", id).Delete(&domain.BotConversation{})
	if inserted, err := store.EnqueueBotInteraction(item, "group", "g"); err != nil || !inserted {
		t.Fatal(inserted, err)
	}
	if inserted, err := store.EnqueueBotInteraction(item, "group", "g"); err != nil || inserted {
		t.Fatal("mysql duplicate", inserted, err)
	}
}

func TestBotDefinitionCannotEnableSameExternalBotTwice(t *testing.T) {
	store, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	first := domain.NotificationBot{ID: "first", BotID: "external-shared", ProjectID: "p1", OwnerID: 42, Name: "first", Enabled: true, InteractionEnabled: true}
	second := domain.NotificationBot{ID: "second", BotID: first.BotID, ProjectID: "p2", OwnerID: 43, Name: "second", Enabled: true, InteractionEnabled: true}
	if err = store.SaveNotificationBot(first, "secret-first"); err != nil {
		t.Fatal(err)
	}
	if err = store.SaveNotificationBot(second, "secret-second"); err != ErrInteractiveBotAlreadyExists {
		t.Fatalf("duplicate registration: %v", err)
	}
	preserved, _ := store.NotificationBot(first.ID)
	if preserved.ProjectID != "p1" || preserved.Name != "first" {
		t.Fatal("other registration overwrote first Bot")
	}
	first.Enabled = false
	if err = store.SaveNotificationBot(first, ""); err != nil {
		t.Fatal(err)
	}
	if err = store.SaveNotificationBot(second, "secret-second"); err != nil {
		t.Fatal(err)
	}
}
