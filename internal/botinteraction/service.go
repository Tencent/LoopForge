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
	// ChannelKind overrides adapter selection in tests; by default the
	// persisted channel of the Bot decides.
	ChannelKind func(domain.NotificationBot) string
	// Retention is the data retention policy applied hourly (stage H);
	// nil disables the cleanup. NativeLogs removes native CLI logs.
	Retention  *repo.BotRetentionPolicy
	NativeLogs NativeLogCleaner
	// PublicURL returns the platform address for links in cards.
	PublicURL func() string
	mu        sync.Mutex
	running   map[string]*connection
	channels  map[string]Channel
	Done      chan struct{}
	start     sync.Once
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
	policy := RetentionPolicyFromEnv()
	s := &Service{Store: store, URL: endpoint, Retention: &policy, running: map[string]*connection{}, channels: map[string]Channel{}, Done: make(chan struct{})}
	s.channels[ChannelWeCom] = &wecomChannel{svc: s}
	s.channels[ChannelFeishu] = newFeishuChannel()
	s.channels[ChannelDingTalk] = newDingTalkChannel()
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
	kind := bot.ChannelKind()
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
					s.retentionTick(ctx, now)
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
		// Only connection-relevant fields reconnect; names, models, tenant
		// locking and write policy are re-read per event and per turn.
		if !ok || b.ConnectionFingerprint() != c.bot.ConnectionFingerprint() {
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
	lease, claimed, err := s.Store.ClaimBotConnection(c.bot.LeaseKey(), owner)
	if err != nil || !claimed {
		return
	}
	defer s.Store.ReleaseBotConnection(lease)
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	policy := sendPolicy(c.bot)
	if err := s.Store.RecoverBotInteractions(c.bot.ID, lease, policy); err != nil {
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
				ok, err := s.Store.RenewBotConnection(lease, "", "")
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
		s.state(lease, "error", "该渠道尚未接入")
		<-runCtx.Done()
		return
	}
	sink := func(ctx context.Context, ev InboundEvent) (InboundAck, error) { return s.accept(ctx, c.bot, ev) }
	backoff := time.Second
	for runCtx.Err() == nil {
		secret, err := s.Store.BotCredential(c.bot.ID)
		if err != nil {
			s.state(lease, "error", "机器人凭据不可用")
			return
		}
		var conn Conn
		conn, err = channel.Connect(runCtx, c.bot, Credential{Secret: secret}, sink)
		if err == nil {
			s.mu.Lock()
			c.conn = conn
			s.mu.Unlock()
			s.state(lease, "connected", "")
			err = s.process(runCtx, c.bot, lease, conn, policy)
			conn.Close()
			s.mu.Lock()
			c.conn = nil
			s.mu.Unlock()
			backoff = time.Second
			if errors.Is(err, repo.ErrBotLeaseLost) {
				return
			}
		}
		if runCtx.Err() != nil {
			return
		}
		s.state(lease, "reconnecting", "连接中断，正在重连")
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
func (s *Service) state(lease repo.BotLease, state, detail string) {
	_, _ = s.Store.RenewBotConnection(lease, state, detail)
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
func (s *Service) process(ctx context.Context, bot domain.NotificationBot, lease repo.BotLease, conn Conn, policy repo.BotSendPolicy) error {
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
		item, err := s.Store.ClaimBotInteraction(bot.ID, lease, policy)
		if err != nil {
			return err
		}
		if item == nil {
			continue
		}
		// Names, models and write policy may change without reconnecting.
		current, err := s.Store.NotificationBot(bot.ID)
		if err != nil {
			return repo.ErrBotLeaseLost
		}
		conv, err := s.Store.BotConversation(item.ConversationID)
		if err != nil {
			return err
		}
		if item.Action == "notification" {
			if err = s.processCard(ctx, current, item, lease, conn, conv, policy); err != nil {
				return err
			}
			continue
		}
		if item.Status == domain.BotRunning {
			if err = s.runTurn(ctx, current, item, lease, conn, conv); err != nil {
				return err
			}
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
		err = s.deliver(ctx, item.ID, lease, policy, func(ctx context.Context, sending domain.BotInteraction) SendResult {
			if sending.Action == ActionChatPost || sending.Action == "notification_details" {
				if rejection := s.notificationSendRejection(current, sending, conv); rejection != nil {
					return *rejection
				}
			}
			var card *PushCard
			if current.ChannelKind() == domain.BotChannelFeishu {
				if sending.Action == "" {
					// A reply with write drafts becomes a confirmation card.
					card = s.DraftCardFor(sending, s.publicURL())
				}
				if card == nil {
					// Clarification questions, conclusions and "is this an
					// answer?" confirmations.
					card = s.clarifyCardFor(sending)
				}
			}
			switch sending.SendRoute {
			case repo.BotSendRoutePush:
				// An expired DingTalk reply the administrator switched to
				// a proactive private message to the original sender.
				return s.Push(ctx, current, OutboundMessage{InteractionID: sending.ID, ChatID: sending.SenderID, ChatType: "single", Text: sending.Reply, SendKey: sending.SendKey})
			case repo.BotSendRoutePushChat:
				if current.ChannelKind() == domain.BotChannelWeCom {
					// WeCom sends proactive messages over the live
					// connection; the card follows the text.
					res := conn.Reply(ctx, OutboundMessage{InteractionID: sending.ID, Action: sending.Action, ChatID: conv.ChatID, ChatType: conv.ChatType, Text: sending.Reply, SendKey: sending.SendKey})
					if res.Outcome == Delivered {
						sendWeComCard(ctx, conn, conv.ChatID, s.clarifyCardFor(sending))
					}
					return res
				}
				// A new message into the conversation: the group itself, or
				// the private chat of the sender.
				msg := OutboundMessage{InteractionID: sending.ID, ChatID: sending.SenderID, ChatType: "single", Text: sending.Reply, SendKey: sending.SendKey, Card: card}
				if conv.ChatType == "group" {
					msg.ChatID, msg.ChatType = conv.ChatID, "group"
				}
				return s.Push(ctx, current, msg)
			}
			out := OutboundMessage{InteractionID: sending.ID, Action: sending.Action, ChatID: conv.ChatID, ChatType: conv.ChatType, Text: sending.Reply, SendKey: sending.SendKey, Route: s.replyRoute(sending), Card: card}
			res := conn.Reply(ctx, out)
			if res.Outcome == Delivered && current.ChannelKind() == domain.BotChannelWeCom && sending.Action == "" {
				// WeCom replies stream text; a write confirmation or an
				// "is this an answer?" question follows as a card.
				followUp := s.DraftCardFor(sending, s.publicURL())
				if followUp == nil {
					followUp = s.clarifyCardFor(sending)
				}
				sendWeComCard(ctx, conn, conv.ChatID, followUp)
			}
			return res
		})
		if err != nil {
			return err
		}
	}
}

// runTurn executes a claimed running item and stores its reply.
func (s *Service) runTurn(ctx context.Context, bot domain.NotificationBot, item *domain.BotInteraction, lease repo.BotLease, conn Conn, conv domain.BotConversation) error {
	reply, detail := "", ""
	var err error
	if item.Action == "notification_details" || item.Action == ActionClarifyPost || item.Action == ActionChatPost {
		// Prepared by the platform; sent as is.
		reply = item.Content
	} else if time.Since(item.CreatedAt) > 10*time.Minute {
		reply = "这条消息等待处理的时间较长，已停止自动执行。请重新发送当前请求。"
	} else if item.Action == actionSystemNotice {
		reply = item.Content
	} else if newConversationCommand(item.Content) {
		if err = s.Store.NewBotConversation(*item, lease); errors.Is(err, repo.ErrBotLeaseLost) {
			return err
		}
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
	if reply, detail, err = s.saveReply(*item, lease, reply, detail); err != nil {
		return err
	}
	item.Reply, item.LastError, item.Status = reply, detail, domain.BotReady
	return nil
}

// processCard sends a WeCom notification card through the delivery state
// machine, or expires it when the node no longer waits.
func (s *Service) processCard(ctx context.Context, bot domain.NotificationBot, item *domain.BotInteraction, lease repo.BotLease, conn Conn, conv domain.BotConversation, policy repo.BotSendPolicy) error {
	delivery, lookupErr := s.Store.NotificationDelivery(item.DeliveryID)
	if lookupErr != nil || len(ActionsForDelivery(s.Store, delivery)) == 0 {
		return s.Store.ExpireBotCard(item.ID)
	}
	var card map[string]any
	if json.Unmarshal([]byte(item.Quote), &card) != nil {
		return errors.New("通知卡片无效")
	}
	if item.Status != domain.BotReady {
		if err := s.Store.SaveBotReply(*item, lease, "", ""); err != nil {
			return err
		}
	}
	cards, ok := conn.(cardConn)
	if !ok {
		return s.Store.FailBotReply(item.ID, lease, "当前渠道暂不支持交互卡片")
	}
	// A task_id may not be reused by the same Bot across sent cards.
	// Keep the delivery prefix for callback routing; suffix each send attempt.
	taskID := item.DeliveryID + "_" + randomID()[:12]
	card["task_id"] = taskID
	if err := s.Store.SetBotCardTaskID(item.ID, lease, taskID); err != nil {
		return err
	}
	return s.deliver(ctx, item.ID, lease, policy, func(ctx context.Context, sending domain.BotInteraction) SendResult {
		if rejection := s.notificationSendRejection(bot, sending, conv); rejection != nil {
			return *rejection
		}
		return cards.sendCard(ctx, conv.ChatID, card)
	})
}
