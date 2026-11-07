package botinteraction

import (
	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"os"
	"strings"
	"sync"
	"time"
)

type Generate func(context.Context, domain.NotificationBot, domain.BotConversation, []domain.BotInteraction, domain.BotInteraction) (string, error)
type Action func(context.Context, domain.NotificationBot, domain.BotInteraction, domain.BotConversation) (string, error)
type Service struct {
	Store    *repo.Store
	Generate Generate
	Action   Action
	URL      string
	mu       sync.Mutex
	running  map[string]*connection
	Done     chan struct{}
	start    sync.Once
}
type connection struct {
	bot    domain.NotificationBot
	cancel context.CancelFunc
	done   chan struct{}
	socket *socket
}

func randomID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
func New(store *repo.Store) *Service {
	endpoint := strings.TrimSpace(os.Getenv("AGENTFLOW_WECOM_WS_URL"))
	if endpoint == "" {
		endpoint = "wss://openws.work.weixin.qq.com"
	}
	return &Service{Store: store, URL: endpoint, running: map[string]*connection{}, Done: make(chan struct{})}
}
func (s *Service) Start(ctx context.Context) {
	s.start.Do(func() {
		go func() {
			ticker := time.NewTicker(3 * time.Second)
			defer ticker.Stop()
			defer func() {
				s.mu.Lock()
				connections := []*connection{}
				for _, c := range s.running {
					c.cancel()
					connections = append(connections, c)
				}
				s.mu.Unlock()
				for _, c := range connections {
					<-c.done
				}
				close(s.Done)
			}()
			nextCleanup := time.Time{}
			for {
				if ctx.Err() != nil {
					return
				}
				now := time.Now().UTC()
				if !now.Before(nextCleanup) {
					if _, err := s.Store.DeleteExpiredBotBindingChallenges(ctx, now); err != nil {
						log.Printf("bot bindings: expired request cleanup failed: %v", err)
						nextCleanup = now.Add(time.Minute)
					} else {
						nextCleanup = now.Add(time.Hour)
					}
				}
				s.reconcile(ctx)
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
				}
			}
		}()
	})
}
func (s *Service) reconcile(ctx context.Context) {
	bots, err := s.Store.InteractiveNotificationBots()
	if err != nil {
		log.Printf("bot interactions: list unavailable")
		return
	}
	wanted := map[string]domain.NotificationBot{}
	for _, b := range bots {
		wanted[b.ID] = b
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, c := range s.running {
		b, ok := wanted[id]
		if !ok || !b.UpdatedAt.Equal(c.bot.UpdatedAt) {
			c.cancel()
		}
		select {
		case <-c.done:
			delete(s.running, id)
		default:
		}
	}
	for id, b := range wanted {
		if _, exists := s.running[id]; exists {
			continue
		}
		child, cancel := context.WithCancel(ctx)
		c := &connection{bot: b, cancel: cancel, done: make(chan struct{})}
		s.running[id] = c
		go func() { defer close(c.done); s.run(child, c) }()
	}
}
func (s *Service) run(ctx context.Context, c *connection) {
	owner := randomID()
	claimed, err := s.Store.ClaimBotConnection(c.bot.BotID, owner, time.Now().UTC())
	if err != nil || !claimed {
		return
	}
	defer s.Store.ReleaseBotConnection(c.bot.BotID, owner)
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	if err := s.Store.RecoverBotInteractions(c.bot.ID); err != nil {
		return
	}
	// Renew independently of model turns; a lost lease cancels every local action.
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				ok, err := s.Store.RenewBotConnection(c.bot.BotID, owner, "", "", time.Now().UTC())
				if err != nil || !ok {
					cancel()
					return
				}
			}
		}
	}()
	backoff := time.Second
	for runCtx.Err() == nil {
		secret, err := s.Store.BotCredential(c.bot.ID)
		if err != nil {
			s.state(c.bot, owner, "error", "机器人凭据不可用")
			return
		}
		var ws *socket
		ws, err = dial(runCtx, s.URL, c.bot.BotID, secret, func(received *socket, f Frame) {
			s.receive(runCtx, c.bot, f, received)
		})
		if err == nil {
			s.mu.Lock()
			c.socket = ws
			s.mu.Unlock()
			s.state(c.bot, owner, "connected", "")
			err = s.process(runCtx, c.bot, owner, ws)
			ws.close()
			s.mu.Lock()
			c.socket = nil
			s.mu.Unlock()
			backoff = time.Second
		}
		if runCtx.Err() != nil {
			return
		}
		s.state(c.bot, owner, "reconnecting", "连接中断，正在重连")
		timer := time.NewTimer(backoff)
		select {
		case <-runCtx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}
func (s *Service) state(bot domain.NotificationBot, owner, state, detail string) {
	_, _ = s.Store.RenewBotConnection(bot.BotID, owner, state, detail, time.Now().UTC())
}
func normalizeIncoming(bot domain.NotificationBot, f Frame) (domain.BotInteraction, string, string, bool) {
	var incoming Incoming
	if json.Unmarshal(f.Body, &incoming) != nil || incoming.BotID != bot.BotID || incoming.MessageID == "" || incoming.From.UserID == "" {
		return domain.BotInteraction{}, "", "", false
	}
	// Live WeCom callbacks wrap card parameters in template_card_event;
	// also accept the flat structure used by older official SDK type definitions.
	if incoming.Event.TemplateCardEvent.Key != "" {
		incoming.Event.Key = incoming.Event.TemplateCardEvent.Key
	}
	if incoming.Event.TemplateCardEvent.TaskID != "" {
		incoming.Event.TaskID = incoming.Event.TemplateCardEvent.TaskID
	}
	chat := incoming.ChatID
	kind := incoming.ChatType
	if kind == "" && f.Cmd == "aibot_event_callback" {
		kind = "single"
		if incoming.ChatID != "" {
			kind = "group"
		}
	}
	if kind == "single" {
		chat = incoming.From.UserID
	}
	if chat == "" || (kind != "single" && kind != "group") || len(chat) > 191 || len(incoming.From.UserID) > 191 {
		return domain.BotInteraction{}, "", "", false
	}
	text := incoming.Text.Content
	if incoming.Type == "voice" {
		text = incoming.Voice.Content
	}
	action, delivery := "", ""
	if f.Cmd == "aibot_event_callback" {
		if incoming.Event.Type != "template_card_event" {
			return domain.BotInteraction{}, "", "", false
		}
		action, delivery = incoming.Event.Key, incoming.Event.TaskID
		if prefix, _, found := strings.Cut(delivery, "_"); found {
			delivery = prefix
		}
		if action == "" || action == "handled" || delivery == "" {
			return domain.BotInteraction{}, "", "", false
		}
	} else if incoming.Type != "text" && incoming.Type != "voice" {
		text = "[不支持的消息类型，请发送文字]"
	}
	text = domain.NotificationText(text, 16000)
	if strings.TrimSpace(text) == "" && action == "" {
		return domain.BotInteraction{}, "", "", false
	}
	return domain.BotInteraction{ID: repo.BotMessageID(bot.BotID, incoming.MessageID), BotID: bot.ID, SenderID: incoming.From.UserID, RequestID: f.Headers.RequestID, Content: text, Quote: domain.NotificationText(incoming.Quote.Text.Content, 8000), Action: action, DeliveryID: delivery, CardTaskID: incoming.Event.TaskID}, kind, chat, true
}
func (s *Service) receive(ctx context.Context, bot domain.NotificationBot, f Frame, ws *socket) {
	item, kind, chat, ok := normalizeIncoming(bot, f)
	if !ok {
		if f.Cmd == "aibot_event_callback" {
			var event Incoming
			_ = json.Unmarshal(f.Body, &event)
			log.Printf("wecom ignored event type=%s bot_matches=%t message_id=%t sender=%t chat_type=%s action=%s task_id=%t", event.Event.Type, event.BotID == bot.BotID, event.MessageID != "", event.From.UserID != "", event.ChatType, event.Event.Key, event.Event.TaskID != "")
		}
		return
	}
	// Single-chat callbacks identify the actor by internal userid, while CLI
	// discovery targets use openid. Resolve only an exact, server-issued card
	// task to its frozen recipient; group callbacks still require matching chatid.
	if item.Action != "" && kind == "single" {
		delivery, e := s.Store.NotificationDelivery(item.DeliveryID)
		card, cardErr := s.Store.BotInteraction(repo.BotMessageID(bot.ID, "notification:"+item.DeliveryID))
		if e == nil && cardErr == nil && delivery.BotID == bot.ID && delivery.ProjectID == bot.ProjectID && delivery.TargetType == "single" && card.CardTaskID == item.CardTaskID {
			chat = delivery.TargetID
		}
	}
	inserted, err := s.Store.EnqueueBotInteraction(item, kind, chat)
	if err != nil || !inserted {
		if err != nil {
			log.Printf("wecom event enqueue failed action=%s", item.Action)
		}
		return
	}
	// Card operations are direct engine controls. Reply with the actual result
	// within the callback window rather than consuming it with a receipt-only update.

	if item.Action == "" {
		state := ws.streamState(item.ID)
		go func() { _ = ws.streamWithState(ctx, item, "已收到，正在处理…", false, state) }()
	}
}

var ErrCardActionDeferred = errors.New("等待账号确认或项目授权")

func newConversationCommand(content string) bool {
	text := strings.TrimSpace(content)
	if strings.HasPrefix(text, "@") {
		if _, rest, ok := strings.Cut(text, " "); ok {
			text = strings.TrimSpace(rest)
		}
		if _, rest, ok := strings.Cut(text, "\n"); ok {
			text = strings.TrimSpace(rest)
		}
	}
	return text == "/new" || text == "开启新会话" || text == "新会话"
}
func (s *Service) process(ctx context.Context, bot domain.NotificationBot, owner string, ws *socket) error {
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ws.done:
			return errors.New("连接中断")
		case <-ticker.C:
		}
		item, err := s.Store.ClaimBotInteraction(bot.ID, bot.BotID, owner)
		if err != nil {
			return err
		}
		if item == nil {
			continue
		}
		conv, err := s.Store.BotConversation(item.ConversationID)
		if err != nil {
			return err
		}
		if item.Action == "notification" {
			delivery, lookupErr := s.Store.NotificationDelivery(item.DeliveryID)
			valid := lookupErr == nil && len(ActionsForDelivery(s.Store, delivery)) > 0
			if !valid {
				if err = s.Store.ExpireBotCard(item.ID); err != nil {
					return err
				}
				continue
			}
			var card map[string]any
			if json.Unmarshal([]byte(item.Quote), &card) != nil {
				return errors.New("通知卡片无效")
			}
			if item.Status != "ready" {
				if err = s.Store.SaveBotReply(*item, "", ""); err != nil {
					return err
				}
			}
			// A task_id may not be reused by the same Bot across sent cards.
			// Keep the delivery prefix for callback routing; suffix each send attempt.
			taskID := item.DeliveryID + "_" + randomID()[:12]
			card["task_id"] = taskID
			if err = s.Store.SetBotCardTaskID(item.ID, taskID); err != nil {
				return err
			}
			if err = ws.request(ctx, "aibot_send_msg", "", map[string]any{"chatid": conv.ChatID, "msgtype": "template_card", "template_card": card}); err != nil {
				var business *ReplyError
				if errors.As(err, &business) {
					if err = s.Store.FailBotReply(item.ID, business.Error()); err != nil {
						return err
					}
					continue
				}
				return err
			}
			if err = s.Store.MarkBotReplySent(item.ID); err != nil {
				return err
			}
			continue
		}
		if item.Action == "notification_details" {
			delivery, lookupErr := s.Store.NotificationDelivery(item.DeliveryID)
			if lookupErr != nil || len(ActionsForDelivery(s.Store, delivery)) == 0 {
				if err = s.Store.ExpireBotCard(item.ID); err != nil {
					return err
				}
				continue
			}
		}
		if item.Status != "ready" {
			reply, detail := "", ""
			if item.Action == "notification_details" {
				reply = item.Content
			} else if time.Since(item.CreatedAt) > 10*time.Minute {
				reply = "这条消息等待处理的时间较长，已停止自动执行。请重新发送当前请求。"
			} else if newConversationCommand(item.Content) {
				err = s.Store.NewBotConversation(*item)
				reply = "已开启新会话。当前聊天将从新的上下文开始，历史记录保留。"
			} else if item.Content == "[不支持的消息类型，请发送文字]" {
				reply = item.Content
			} else {
				turnCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
				var stopProgress func()
				if item.Action == "" {
					turnCtx, stopProgress = ws.progressTurn(turnCtx, *item)
				}
				if item.Action != "" {
					if s.Action == nil {
						err = errors.New("操作服务未就绪")
					} else {
						reply, err = s.Action(turnCtx, bot, *item, conv)
					}
				} else {
					history, historyErr := s.Store.BotHistory(conv.SessionID)
					err = historyErr
					if err == nil {
						if s.Generate == nil {
							err = errors.New("助手服务未就绪")
						} else {
							reply, err = s.Generate(turnCtx, bot, conv, history, *item)
						}
					}
				}
				if stopProgress != nil {
					stopProgress()
				}
				cancel()
			}
			if errors.Is(err, ErrCardActionDeferred) {
				detail = ErrCardActionDeferred.Error()
			} else if err != nil {
				detail = "本次处理失败"
				reply = "本次处理未完成，请稍后重试或到平台查看状态。"
				if errors.Is(err, context.DeadlineExceeded) {
					detail = "模型回复提交超时"
					reply = "本次处理已超时，未能提交最终回复。请重新发送请求；若涉及修改操作，请先在平台核对状态，避免重复执行。"
				}
			}
			if strings.TrimSpace(reply) == "" {
				detail = "助手未提交回复"
				reply = "本次没有得到有效回复，请稍后重试。"
			}
			reply = domain.NotificationText(reply, 16000)
			if reply, detail, err = s.saveReply(*item, reply, detail); err != nil {
				return err
			}
			item.Reply = reply
			item.LastError = detail
		}
		if item.Action != "" && item.Action != "notification_details" && item.CardTaskID != "" && item.LastError != ErrCardActionDeferred.Error() {
			if updateErr := ws.updateActionCard(ctx, *item, item.Reply, true); updateErr != nil {
				code := 0
				var business *ReplyError
				if errors.As(updateErr, &business) {
					code = business.Code
				}
				log.Printf("wecom action card update failed action=%s code=%d", item.Action, code)
			}
		}
		if item.Action == "" {
			err = ws.stream(ctx, *item, item.Reply, true)
			if err != nil {
				err = ws.send(ctx, conv.ChatID, item.Reply)
			}
		} else {
			err = ws.send(ctx, conv.ChatID, item.Reply)
		}
		ws.streams.Delete(item.ID)
		if err != nil {
			var business *ReplyError
			if errors.As(err, &business) {
				if err = s.Store.FailBotReply(item.ID, business.Error()); err != nil {
					return err
				}
				continue
			}
			return err
		}
		if err = s.Store.MarkBotReplySent(item.ID); err != nil {
			return err
		}
	}
}
