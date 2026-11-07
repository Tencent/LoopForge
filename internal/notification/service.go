package notification

import (
	"agentflow/internal/botinteraction"
	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/url"
	"os"
	"strings"
	"time"
)

type Service struct {
	Store          *repo.Store
	Provider       Provider
	GenerateReport ReportGenerator
	Interactions   *botinteraction.Service
	PublicURL      string
}

func New(s *repo.Store) *Service {
	base := strings.TrimRight(os.Getenv("AGENTFLOW_PUBLIC_URL"), "/")
	return &Service{Store: s, Provider: NewCLIProvider(), PublicURL: base, Interactions: botinteraction.New(s)}
}
func (s *Service) secret(bot domain.NotificationBot) (string, error) {
	secret, err := s.Store.BotCredential(bot.ID)
	if err != nil || secret == "" {
		return "", errors.New("机器人凭据不可用，请重新填写 Secret")
	}
	return secret, nil
}
func (s *Service) Sessions(ctx context.Context, bot domain.NotificationBot) ([]domain.BotChat, error) {
	secret, err := s.secret(bot)
	if err != nil {
		return nil, err
	}
	chats, err := s.Provider.Sessions(ctx, bot.BotID, secret)
	health := "ready"
	if err != nil {
		health = "error"
		var pe *ProviderError
		if errors.As(err, &pe) && (pe.Code == 850003 || pe.Code == 853004) {
			health = "awaiting_authorization"
		}
	}
	if updateErr := s.Store.SetNotificationBotHealth(bot.ID, health); updateErr != nil && err == nil {
		return nil, updateErr
	}
	return chats, err
}

var errNotificationExpired = errors.New("节点已无需人工处理")

func (s *Service) Send(ctx context.Context, bot domain.NotificationBot, targetID, content string) error {
	return s.send(ctx, bot, targetID, content, nil)
}
func (s *Service) send(ctx context.Context, bot domain.NotificationBot, targetID, content string, beforeSend func() error, delivery ...domain.NotificationDelivery) error {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	if !bot.Enabled {
		return errors.New("机器人已停用")
	}
	chats, err := s.Sessions(ctx, bot)
	if err != nil {
		return err
	}
	found := false
	for _, chat := range chats {
		if chat.ID == targetID {
			found = true
		}
	}
	if !found {
		return &ProviderError{Code: 404, Message: "接收会话当前不可用，请与机器人互动后刷新列表"}
	}
	secret, err := s.secret(bot)
	if err != nil {
		return err
	}
	if beforeSend != nil {
		if err := beforeSend(); err != nil {
			return err
		}
	}
	if len(delivery) > 0 && bot.InteractionEnabled && s.Interactions != nil && delivery[0].NodeID != "" && delivery[0].TargetType != "" && (delivery[0].EventType == "waiting" || delivery[0].EventType == "failed") {
		detailURL := ""
		if base, err := url.Parse(s.PublicURL); err == nil && (base.Scheme == "https" || base.Scheme == "http") && base.Host != "" {
			detailURL = s.PublicURL + "/#/runs/" + url.PathEscape(delivery[0].RunID)
		}
		return s.Interactions.QueueNotificationCard(ctx, bot, delivery[0], content, detailURL)
	}
	return s.Provider.Send(ctx, bot.BotID, secret, targetID, content)
}
func (s *Service) Start(ctx context.Context) {
	if s.Interactions != nil {
		s.Interactions.Start(ctx)
	}
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				for i := 0; i < 10; i++ {
					processed, err := s.ProcessOne(ctx)
					if err != nil || !processed {
						break
					}
				}
			}
		}
	}()
}
func (s *Service) ProcessOne(ctx context.Context) (bool, error) {
	token := make([]byte, 16)
	if _, err := rand.Read(token); err != nil {
		return false, err
	}
	now := time.Now().UTC()
	item, err := s.Store.ClaimNotification(now, hex.EncodeToString(token))
	if err != nil || item == nil {
		return false, err
	}
	finish := func(status, detail string) (bool, error) {
		return true, s.Store.FinishNotification(*item, status, detail, now)
	}
	if now.Sub(item.CreatedAt) > 24*time.Hour {
		return finish("expired", "通知已超过有效期")
	}
	bot, err := s.Store.NotificationBot(item.BotID)
	if err != nil {
		return finish("cancelled", "机器人已删除")
	}
	if !bot.Enabled {
		return finish("cancelled", "机器人已停用")
	}
	if bot.ProjectID != item.ProjectID {
		return finish("cancelled", "通知项目不匹配")
	}
	if item.SourceType == "schedule_job" {
		job, ok := s.Store.ScheduleJob(item.SourceID)
		if !ok || job.ProjectID != item.ProjectID {
			return finish("cancelled", "定时任务已删除或项目已变更")
		}
	}
	if item.SourceType == "assistant_task" && !s.Store.AssistantTaskExists(item.ProjectID, item.SourceID) {
		return finish("cancelled", "AI 助手任务已删除或项目已变更")
	}
	if item.RunID != "" {
		run, ok := s.Store.RunState(item.RunID)
		if !ok {
			return finish("expired", "运行已删除")
		}
		node := run.Nodes[item.NodeID]
		if item.EventType == "waiting" && item.WaitingPolicy != "event" && (domain.TerminalRunStatus(run.Status) || node == nil || node.Status != "waiting_approval" || node.Attempt != item.Attempt || node.ReworkCount != item.ReworkCount) {
			return finish("expired", "节点已无需人工处理")
		}
	}
	if item.EventType == "waiting" && item.WaitingPolicy == "event" {
		run, _ := s.Store.RunState(item.RunID)
		node := run.Nodes[item.NodeID]
		if domain.TerminalRunStatus(run.Status) || node == nil || node.Status != "waiting_approval" || node.Attempt != item.Attempt || node.ReworkCount != item.ReworkCount {
			item.Content += "\n\n此消息记录进入等待的状态；当前该次等待已结束，无需按旧提醒操作。"
		}
	}
	if item.ReportState == "pending" {
		if err := s.generateReport(ctx, item); err != nil {
			return true, err
		}
		frozen, err := s.Store.FreezeNotificationReport(*item)
		if err != nil || !frozen {
			return true, err
		}
	}
	content := item.Content
	link := "请在平台运行记录中查看"
	if base, parseErr := url.Parse(s.PublicURL); parseErr == nil && (base.Scheme == "https" || base.Scheme == "http") && base.Host != "" {
		if item.SourceType == "schedule_job" {
			link = s.PublicURL + "/#/issues"
		} else if item.SourceType == "assistant_task" {
			link = s.PublicURL + "/#/dashboard"
		} else {
			link = s.PublicURL + "/#/runs/" + url.PathEscape(item.RunID)
		}
	}
	content = strings.ReplaceAll(content, domain.NotificationURLMarker, link)
	if item.ContentSource == "" {
		content = strings.ReplaceAll(content, "{{url}}", link)
	}
	err = s.send(ctx, bot, item.TargetID, content, func() error {
		if item.EventType != "waiting" || item.WaitingPolicy == "event" {
			return nil
		}
		run, ok := s.Store.RunState(item.RunID)
		if !ok {
			return errNotificationExpired
		}
		node := run.Nodes[item.NodeID]
		if domain.TerminalRunStatus(run.Status) || node == nil || node.Status != "waiting_approval" || node.Attempt != item.Attempt || node.ReworkCount != item.ReworkCount {
			return errNotificationExpired
		}
		return nil
	}, *item)
	if errors.Is(err, errNotificationExpired) || errors.Is(err, botinteraction.ErrCardExpired) {
		return finish("expired", err.Error())
	}
	if err == nil {
		return finish("sent", "")
	}
	status := "pending"
	detail := "发送失败，等待重试"
	var pe *ProviderError
	if errors.As(err, &pe) {
		detail = pe.Error()
		switch pe.Code {
		case 850003, 853004:
			status = "awaiting_authorization"
		case 404:
			status = "target_unavailable"
		}
	}
	if item.Attempts >= 5 && status == "pending" {
		status = "failed"
		detail = "多次发送失败，请检查通知通道后重试"
	}
	delay := time.Duration(1<<min(item.Attempts, 6)) * 10 * time.Second
	jitter := make([]byte, 1)
	_, _ = rand.Read(jitter)
	delay += time.Duration(jitter[0]%10) * time.Second
	return true, s.Store.FinishNotification(*item, status, detail, now.Add(delay))
}
