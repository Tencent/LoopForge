package botinteraction

import (
	"agentflow/internal/domain"
	"context"
	"encoding/json"
	"errors"
	"log"
	"strconv"
	"strings"
	"sync"
)

// WeComVerifyFunc checks a WeCom Bot ID and secret through the signed
// bootstrap API, without opening a long connection.
type WeComVerifyFunc func(ctx context.Context, botID, secret string) error

// WeComPushFunc sends a proactive message through the official wecom-cli path
// and classifies the outcome.
type WeComPushFunc func(ctx context.Context, botID, secret, target, content string) SendResult

// wecomChannel wraps the existing WeCom AI Bot long-connection protocol.
type wecomChannel struct {
	svc    *Service
	verify WeComVerifyFunc
	push   WeComPushFunc
}

func (w *wecomChannel) Kind() string { return ChannelWeCom }

func (w *wecomChannel) Verify(ctx context.Context, bot domain.NotificationBot, cred Credential) (Verification, error) {
	if w.verify == nil {
		return Verification{}, ErrChannelNotReady
	}
	return Verification{}, w.verify(ctx, bot.BotID, cred.Secret)
}

func (w *wecomChannel) Push(ctx context.Context, bot domain.NotificationBot, cred Credential, msg OutboundMessage) SendResult {
	if w.push == nil {
		return SendResult{Outcome: NotDelivered, Code: ErrChannelNotReady.Error(), SafeDetail: "企业微信主动发送未配置"}
	}
	return w.push(ctx, bot.BotID, cred.Secret, msg.ChatID, msg.Text)
}

func (w *wecomChannel) Connect(ctx context.Context, bot domain.NotificationBot, cred Credential, sink InboundSink) (Conn, error) {
	ws, err := dial(ctx, w.svc.URL, bot.BotID, cred.Secret, func(received *socket, f Frame) {
		w.svc.wecomFrame(ctx, bot, f, received, sink)
	})
	if err != nil {
		return nil, err
	}
	return &wecomConn{ws: ws}, nil
}

// wecomConn adapts a socket to Conn and exposes progress and card capabilities.
type wecomConn struct {
	ws        *socket
	closeOnce sync.Once
}

func (c *wecomConn) Done() <-chan struct{} { return c.ws.done }
func (c *wecomConn) Close() error {
	c.closeOnce.Do(c.ws.close)
	return nil
}

// Reply finishes the stream of a user message. The proactive send fallback
// runs at most once and only when the stream provably did not go out: stream
// unavailable, request never written, or an explicit platform rejection of
// the stream (for example an expired callback). A stream that may have been
// delivered is reported as unknown instead of being sent twice. Card actions
// send directly.
func (c *wecomConn) Reply(ctx context.Context, msg OutboundMessage) SendResult {
	defer c.ws.streams.Delete(msg.InteractionID)
	if msg.Action != "" {
		return classifyWeComSend(c.ws.send(ctx, msg.ChatID, msg.Text))
	}
	streamed := classifyWeComSend(c.ws.stream(ctx, domain.BotInteraction{ID: msg.InteractionID, RequestID: msg.Route.RequestID}, msg.Text, true))
	if streamed.Outcome == Delivered || streamed.Outcome == Unknown {
		return streamed
	}
	return classifyWeComSend(c.ws.send(ctx, msg.ChatID, msg.Text))
}

func (c *wecomConn) progressTurn(ctx context.Context, item domain.BotInteraction) (context.Context, func()) {
	return c.ws.progressTurn(ctx, item)
}

func (c *wecomConn) sendCard(ctx context.Context, chatID string, card map[string]any) SendResult {
	return classifyWeComSend(c.ws.request(ctx, "aibot_send_msg", "", map[string]any{"chatid": chatID, "msgtype": "template_card", "template_card": card}))
}

func (c *wecomConn) updateActionCard(ctx context.Context, item domain.BotInteraction, result string, finish bool) error {
	return c.ws.updateActionCard(ctx, item, result, finish)
}

// classifyWeComSend maps socket errors to delivery outcomes by how far the
// request got. Business error codes are explicit platform rejections.
func classifyWeComSend(err error) SendResult {
	if err == nil {
		return SendResult{Outcome: Delivered}
	}
	var business *ReplyError
	if errors.As(err, &business) {
		return SendResult{Outcome: Rejected, Code: strconv.Itoa(business.Code), SafeDetail: business.Error()}
	}
	if errors.Is(err, errorsStreamUnavailable) {
		return SendResult{Outcome: NotDelivered, Code: "stream_unavailable", Retryable: true, SafeDetail: err.Error()}
	}
	var sendErr *sendError
	if errors.As(err, &sendErr) {
		switch sendErr.phase {
		case phaseBeforeWrite:
			return SendResult{Outcome: NotDelivered, Code: "not_written", Retryable: true, SafeDetail: err.Error()}
		case phaseWrite:
			return SendResult{Outcome: Unknown, Code: "write_interrupted", SafeDetail: err.Error()}
		default:
			return SendResult{Outcome: Unknown, Code: "receipt_missing", SafeDetail: err.Error()}
		}
	}
	return SendResult{Outcome: Unknown, Code: "unclassified", SafeDetail: err.Error()}
}

// wecomEvent converts one WeCom callback frame into a channel-neutral event.
// It applies the WeCom-specific input rules that existed before the channel
// contract: voice transcription, unsupported types and length limits.
func wecomEvent(bot domain.NotificationBot, f Frame) (InboundEvent, bool) {
	var incoming Incoming
	if json.Unmarshal(f.Body, &incoming) != nil || incoming.BotID != bot.BotID || incoming.MessageID == "" || incoming.From.UserID == "" {
		return InboundEvent{}, false
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
	text := incoming.Text.Content
	if incoming.Type == "voice" {
		text = incoming.Voice.Content
	}
	action, delivery, option := "", "", ""
	var selections map[string]string
	if f.Cmd == "aibot_event_callback" {
		if incoming.Event.Type != "template_card_event" {
			return InboundEvent{}, false
		}
		action, delivery = incoming.Event.Key, incoming.Event.TaskID
		if prefix, rest, found := strings.Cut(delivery, "_"); found {
			delivery = prefix
			if ClarifyAction(action) {
				// "<session>_<ref>_<nonce>": the button reference.
				option, _, _ = strings.Cut(rest, "_")
			}
		}
		if action == DraftActionSubmit {
			// A confirmation card with drop-downs: the operation is a
			// selection, the others are the repository and the SOP.
			selections = wecomCardSelections(incoming.Event.TemplateCardEvent.SelectedItems.SelectedItem)
			action = selections[draftOpKey]
			delete(selections, draftOpKey)
			if !DraftAction(action) || DraftSelectAction(action) {
				return InboundEvent{}, false
			}
		}
		if action == "" || action == "handled" || delivery == "" || (ClarifyAction(action) && option == "") {
			return InboundEvent{}, false
		}
	} else if incoming.Type != "text" && incoming.Type != "voice" {
		text = unsupportedMessageText
	}
	return InboundEvent{
		Channel:    ChannelWeCom,
		AppID:      incoming.BotID,
		MessageID:  incoming.MessageID,
		SenderID:   incoming.From.UserID,
		ChatID:     chat,
		ChatType:   kind,
		MsgType:    incoming.Type,
		Text:       domain.NotificationText(text, 16000),
		Quote:      domain.NotificationText(incoming.Quote.Text.Content, 8000),
		Action:     action,
		DeliveryID: delivery,
		Option:     option,
		Selections: selections,
		CardTaskID: incoming.Event.TaskID,
		Route:      ReplyRoute{RequestID: f.Headers.RequestID},
	}, true
}

// normalizeIncoming is kept for the WeCom frame tests: it returns the
// interaction the common inbound path would persist for this frame.
func normalizeIncoming(bot domain.NotificationBot, f Frame) (domain.BotInteraction, string, string, bool) {
	ev, ok := wecomEvent(bot, f)
	if !ok {
		return domain.BotInteraction{}, "", "", false
	}
	item, reason := interactionFromEvent(bot, ev)
	if reason != "" {
		return domain.BotInteraction{}, "", "", false
	}
	return item, ev.ChatType, ev.ChatID, true
}

// receive handles one WeCom frame with the service's own inbound sink.
func (s *Service) receive(ctx context.Context, bot domain.NotificationBot, f Frame, ws *socket) {
	s.wecomFrame(ctx, bot, f, ws, func(ctx context.Context, ev InboundEvent) (InboundAck, error) {
		return s.accept(ctx, bot, ev)
	})
}

func (s *Service) wecomFrame(ctx context.Context, bot domain.NotificationBot, f Frame, ws *socket, sink InboundSink) {
	ev, ok := wecomEvent(bot, f)
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
	if ev.Action != "" && ev.ChatType == "single" {
		delivery, e := s.Store.NotificationDelivery(ev.DeliveryID)
		if e == nil && delivery.BotID == bot.ID && delivery.ProjectID == bot.ProjectID && delivery.TargetType == "single" &&
			s.notificationCardTaskMatches(bot.ID, delivery, ev.CardTaskID) {
			ev.ChatID = delivery.TargetID
		}
	}
	ack, err := sink(ctx, ev)
	if err == nil && ev.Action != "" && (DraftAction(ev.Action) || ClarifyAction(ev.Action)) {
		// Lock the clicked card (or explain a refusal) within the
		// callback window.
		ws.wecomCardAnswer(ctx, ev, ack)
	}
	if err != nil || !ack.Accepted {
		return
	}
	// Card operations are direct engine controls. Reply with the actual result
	// within the callback window rather than consuming it with a receipt-only update.
	if ev.Action == "" {
		item := domain.BotInteraction{ID: ack.InteractionID, RequestID: ev.Route.RequestID}
		state := ws.streamState(item.ID)
		go func() { _ = ws.streamWithState(ctx, item, "已收到，正在处理…", false, state) }()
	}
}
