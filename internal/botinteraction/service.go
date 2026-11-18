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
	// ChannelKind selects the adapter for a Bot. Stage A connects every Bot
	// through WeCom; stage B reads the persisted channel field.
	ChannelKind func(domain.NotificationBot) string
	mu          sync.Mutex
	running     map[string]*connection
	channels    map[string]Channel
	Done        chan struct{}
	start       sync.Once
}
type connection struct {
	bot    domain.NotificationBot
	cancel context.CancelFunc
	done   chan struct{}
	conn   Conn
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
	s := &Service{Store: store, URL: endpoint, running: map[string]*connection{}, channels: map[string]Channel{}, Done: make(chan struct{})}
	s.channels[ChannelWeCom] = &wecomChannel{svc: s}
	return s
}

// ConfigureWeCom injects the credential check and proactive send used by the
// WeCom channel. The notification service owns the wecom-cli provider.
func (s *Service) ConfigureWeCom(verify WeComVerifyFunc, push WeComPushFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.channels[ChannelWeCom] = &wecomChannel{svc: s, verify: verify, push: push}
}

// RegisterChannel adds or replaces the adapter for c.Kind().
func (s *Service) RegisterChannel(c Channel) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.channels[c.Kind()] = c
}

// Channel returns the adapter for a Bot, or ErrChannelNotReady.
func (s *Service) Channel(bot domain.NotificationBot) (Channel, error) {
	kind := ChannelWeCom
	if s.ChannelKind != nil {
		kind = s.ChannelKind(bot)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, ok := s.channels[kind]; ok {
		return c, nil
	}
	return nil, ErrChannelNotReady
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
	channel, err := s.Channel(c.bot)
	if err != nil {
		// Keep the lease so the error stays visible until the configuration
		// changes or the service stops; releasing would report "offline".
		s.state(c.bot, owner, "error", "该渠道尚未接入")
		<-runCtx.Done()
		return
	}
	sink := func(ctx context.Context, ev InboundEvent) (InboundAck, error) { return s.accept(ctx, c.bot, ev) }
	backoff := time.Second
	for runCtx.Err() == nil {
		secret, err := s.Store.BotCredential(c.bot.ID)
		if err != nil {
			s.state(c.bot, owner, "error", "机器人凭据不可用")
			return
		}
		var conn Conn
		conn, err = channel.Connect(runCtx, c.bot, Credential{Secret: secret}, sink)
		if err == nil {
			s.mu.Lock()
			c.conn = conn
			s.mu.Unlock()
			s.state(c.bot, owner, "connected", "")
			err = s.process(runCtx, c.bot, owner, conn)
			conn.Close()
			s.mu.Lock()
			c.conn = nil
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
func (s *Service) process(ctx context.Context, bot domain.NotificationBot, owner string, conn Conn) error {
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-conn.Done():
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
			cards, ok := conn.(cardConn)
			if !ok {
				if err = s.Store.FailBotReply(item.ID, "当前渠道暂不支持交互卡片"); err != nil {
					return err
				}
				continue
			}
			if err = s.finishSend(item.ID, cards.sendCard(ctx, conv.ChatID, card)); err != nil {
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
			} else if item.Content == unsupportedMessageText {
				reply = item.Content
			} else {
				turnCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
				var stopProgress func()
				if progress, ok := conn.(progressConn); ok && item.Action == "" {
					turnCtx, stopProgress = progress.progressTurn(turnCtx, *item)
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
		cards, hasCards := conn.(cardConn)
		if hasCards && item.Action != "" && item.Action != "notification_details" && item.CardTaskID != "" && item.LastError != ErrCardActionDeferred.Error() {
			if updateErr := cards.updateActionCard(ctx, *item, item.Reply, true); updateErr != nil {
				code := 0
				var business *ReplyError
				if errors.As(updateErr, &business) {
					code = business.Code
				}
				log.Printf("wecom action card update failed action=%s code=%d", item.Action, code)
			}
		}
		result := conn.Reply(ctx, OutboundMessage{InteractionID: item.ID, Action: item.Action, ChatID: conv.ChatID, ChatType: conv.ChatType, Text: item.Reply, Route: ReplyRoute{RequestID: item.RequestID}})
		if err = s.finishSend(item.ID, result); err != nil {
			return err
		}
	}
}

// finishSend records a classified send. Stage A keeps the pre-channel state
// transitions: delivered → replied, rejected → reply_failed, and any other
// outcome drops the connection so the ready reply is sent again after
// reconnecting. Stage C replaces this with the persisted delivery state machine.
func (s *Service) finishSend(id string, result SendResult) error {
	switch result.Outcome {
	case Delivered:
		return s.Store.MarkBotReplySent(id)
	case Rejected:
		return s.Store.FailBotReply(id, result.SafeDetail)
	default:
		return result.err()
	}
}
