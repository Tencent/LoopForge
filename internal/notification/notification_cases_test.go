package notification

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestCLIProviderTokenAndCommand(t *testing.T) {
	code := 0
	token := "tok"
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if code != 0 {
			_, _ = w.Write([]byte(`{"errcode":` + string(rune('0'+code)) + `}`))
			return
		}
		_, _ = w.Write([]byte(`{"errcode":0,"token":"` + token + `"}`))
	}))
	defer auth.Close()
	dir := t.TempDir()
	binary := filepath.Join(dir, "wecom-cli")
	script := `#!/bin/sh
case "$*" in
  *sessions*) printf '%s' '{"errcode":0,"sessions":[{"chat_id":"c","chat_name":"C","chat_type":"group"},{"chat_id":"","chat_type":"single"},{"chat_id":"x","chat_type":"bad"}]}' ;;
  *send*) printf '%s' '{"errcode":0,"success":true}' ;;
  *) printf '%s' '{"errcode":0}' ;;
esac
`
	if err := os.WriteFile(binary, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	p := &CLIProvider{HTTP: auth.Client(), Binary: binary, authURL: auth.URL}
	if got, err := p.token(context.Background(), "bot", "secret"); err != nil || got != "tok" {
		t.Fatalf("token = %q, %v", got, err)
	}
	if chats, err := p.Sessions(context.Background(), "bot", "secret"); err != nil || len(chats) != 1 {
		t.Fatalf("chats = %#v, %v", chats, err)
	}
	if err := p.Send(context.Background(), "bot", "secret", "c", "hello"); err != nil {
		t.Fatal(err)
	}
	for _, content := range []string{"", strings.Repeat("x", 20481)} {
		if err := p.Send(context.Background(), "bot", "secret", "c", content); err == nil {
			t.Fatal("invalid content accepted")
		}
	}
	counter := filepath.Join(dir, "counter")
	retryBinary := filepath.Join(dir, "retry-cli")
	retryScript := fmt.Sprintf(`#!/bin/sh
n=$(cat %q 2>/dev/null || echo 0)
n=$((n+1))
echo "$n" > %q
if [ "$n" = 1 ]; then printf '%%s' '{"errcode":853004}'; else printf '%%s' '{"errcode":0,"success":true}'; fi
`, counter, counter)
	if err := os.WriteFile(retryBinary, []byte(retryScript), 0o755); err != nil {
		t.Fatal(err)
	}
	p.Binary = retryBinary
	if _, err := p.call(context.Background(), "bot", "secret", "send"); err != nil {
		t.Fatalf("retry call = %v", err)
	}
	p.Binary = filepath.Join(dir, "missing")
	if _, err := p.call(context.Background(), "bot", "secret", "x"); err == nil {
		t.Fatal("missing binary accepted")
	}
}

func TestServiceSendBranches(t *testing.T) {
	svc, provider, bot, _ := serviceFixture(t)
	if _, err := svc.secret(domain.NotificationBot{ID: "missing"}); err == nil {
		t.Fatal("missing credential accepted")
	}
	if err := svc.Send(context.Background(), bot, "chat", "hello"); err != nil || provider.sent != 1 {
		t.Fatalf("send = %v, count=%d", err, provider.sent)
	}
	bot.Enabled = false
	if err := svc.Send(context.Background(), bot, "chat", "hello"); err == nil {
		t.Fatal("disabled bot sent")
	}
	bot.Enabled = true
	if err := svc.Send(context.Background(), bot, "missing", "hello"); err == nil {
		t.Fatal("missing chat accepted")
	}
}

func TestProcessOneCancellationBranches(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Service, domain.NotificationBot, domain.Run)
	}{
		{"expired", func(s *Service, _ domain.NotificationBot, _ domain.Run) {
			_ = s.Store.DB.Model(&domain.NotificationDelivery{}).Where("status = ?", "pending").Update("created_at", time.Now().Add(-25*time.Hour)).Error
		}},
		{"missing bot", func(s *Service, bot domain.NotificationBot, _ domain.Run) {
			_ = s.Store.DB.Where("id = ?", bot.ID).Delete(&domain.NotificationBot{}).Error
		}},
		{"project mismatch", func(s *Service, bot domain.NotificationBot, _ domain.Run) {
			_ = s.Store.DB.Model(&domain.NotificationBot{}).Where("id = ?", bot.ID).Update("project_id", "other").Error
		}},
		{"missing run", func(s *Service, _ domain.NotificationBot, run domain.Run) {
			_ = s.Store.DB.Where("id = ?", run.ID).Delete(&domain.Run{}).Error
		}},
		{"missing schedule", func(s *Service, _ domain.NotificationBot, _ domain.Run) {
			_ = s.Store.DB.Model(&domain.NotificationDelivery{}).Where("status = ?", "pending").Updates(map[string]any{"source_type": "schedule_job", "source_id": "missing"}).Error
		}},
		{"missing assistant", func(s *Service, _ domain.NotificationBot, _ domain.Run) {
			_ = s.Store.DB.Model(&domain.NotificationDelivery{}).Where("status = ?", "pending").Updates(map[string]any{"source_type": "assistant_task", "source_id": "missing"}).Error
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc, _, bot, run := serviceFixture(t)
			tc.mutate(svc, bot, run)
			processed, err := svc.ProcessOne(context.Background())
			if err != nil || !processed {
				t.Fatalf("processed=%v err=%v", processed, err)
			}
		})
	}
}

func TestNotificationStartStopsOnContext(t *testing.T) {
	svc, _, bot, _ := serviceFixture(t)
	svc.Interactions = nil
	ctx, cancel := context.WithCancel(context.Background())
	svc.Start(ctx)
	deadline := time.Now().Add(10 * time.Second)
	for {
		items, _ := svc.Store.NotificationDeliveries(bot.ProjectID)
		if len(items) > 0 && items[0].Status != "pending" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("notification worker did not process pending delivery")
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
}

func TestProcessOneStopsRetryingAfterFiveAttempts(t *testing.T) {
	svc, provider, bot, _ := serviceFixture(t)
	provider.err = errors.New("send failed")
	if err := svc.Store.DB.Model(&domain.NotificationDelivery{}).Where("status = ?", "pending").Update("attempts", 5).Error; err != nil {
		t.Fatal(err)
	}
	if processed, err := svc.ProcessOne(context.Background()); err != nil || !processed {
		t.Fatalf("processed=%v err=%v", processed, err)
	}
	items, _ := svc.Store.NotificationDeliveries(bot.ProjectID)
	if len(items) == 0 || items[0].Status != "failed" {
		t.Fatalf("delivery = %#v", items)
	}
}

func TestGenerateReportMalformedFallback(t *testing.T) {
	svc := &Service{}
	item := &domain.NotificationDelivery{ReportRequest: "{", SourceName: "SOP"}
	if err := svc.generateReport(context.Background(), item); err != nil {
		t.Fatal(err)
	}
	if item.ReportState != "fallback" || item.ContentSource != "fallback" || item.ReportError == "" {
		t.Fatalf("fallback = %#v", item)
	}
	if reportFailureReason(context.DeadlineExceeded) != "整体汇报生成超时" {
		t.Fatal("deadline failure reason")
	}
}

func TestCLIProviderFailuresAndBuffer(t *testing.T) {
	for _, status := range []int{http.StatusInternalServerError, http.StatusOK} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			if status == http.StatusOK {
				_, _ = w.Write([]byte("{"))
			}
		}))
		p := &CLIProvider{HTTP: server.Client(), authURL: server.URL}
		if _, err := p.token(context.Background(), "b", "s"); err == nil {
			t.Fatalf("status %d accepted", status)
		}
		server.Close()
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"errcode":0}`))
	}))
	p := &CLIProvider{HTTP: server.Client(), authURL: server.URL}
	if _, err := p.token(context.Background(), "b", "s"); err == nil {
		t.Fatal("empty token accepted")
	}
	server.Close()
	if failure(850003).Message == failure(853004).Message || failure(1).Error() == "" {
		t.Fatal("failure mapping")
	}
	var b boundedBuffer
	if _, err := b.Write(make([]byte, (2<<20)+1)); err == nil {
		t.Fatal("oversized buffer accepted")
	}
	if NewCLIProvider().Binary == "" {
		t.Fatal("default provider")
	}
}

func TestCLIProviderCallOutputFailures(t *testing.T) {
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"errcode":0,"token":"tok"}`))
	}))
	defer auth.Close()
	for _, tc := range []struct {
		name, output, exit string
	}{
		{"malformed", "not-json", "0"},
		{"provider error", `{"errcode":850003}`, "0"},
		{"command failure", `{"errcode":0}`, "1"},
		{"nested error", `{"error":{"code":850003}}`, "0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			binary := filepath.Join(t.TempDir(), "cli")
			script := "#!/bin/sh\nprintf '%s' " + fmt.Sprintf("%q", tc.output) + "\nexit " + tc.exit + "\n"
			if err := os.WriteFile(binary, []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			p := &CLIProvider{HTTP: auth.Client(), Binary: binary, authURL: auth.URL}
			if _, err := p.call(context.Background(), "bot", "secret", "x"); err == nil {
				t.Fatal("bad output accepted")
			}
		})
	}
}
