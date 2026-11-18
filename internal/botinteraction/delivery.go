package botinteraction

import (
	"agentflow/internal/credcrypt"
	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"context"
	"errors"
	"log"
	"time"
)

// actionSystemNotice marks an interaction whose saved content is replied
// verbatim without the model (for example the tenant confirmation notice).
const actionSystemNotice = "system_notice"

// sendTimeout bounds one platform call; it matches repo.BotSendTimeoutUS so
// the idempotency window check in BeginBotSend covers the whole call.
const sendTimeout = 10 * time.Second

// sendPolicy describes channel delivery guarantees. Feishu de-duplicates a
// reply carrying the same uuid for one hour, so unknown outcomes are retried
// with the persisted key. WeCom and DingTalk have no verified idempotency:
// unknown outcomes stop at reply_unknown for manual review.
func sendPolicy(bot domain.NotificationBot) repo.BotSendPolicy {
	return repo.BotSendPolicy{Idempotent: bot.ChannelKind() == domain.BotChannelFeishu}
}

// replyRoute rebuilds the routing data persisted with the interaction. A
// sealed route is the DingTalk session webhook; plain values are Feishu
// message IDs. A route that cannot be opened yields an empty route, which
// the adapter rejects as route_missing.
func (s *Service) replyRoute(item domain.BotInteraction) ReplyRoute {
	route := ReplyRoute{RequestID: item.RequestID}
	if item.ReplyRoute != "" {
		if credcrypt.IsSealed(item.ReplyRoute) {
			if webhook, err := s.Store.OpenBotRoute(item.BotID, item.ID, item.ReplyRoute); err == nil {
				route.Webhook = webhook
			} else {
				log.Printf("bot reply route unavailable id=%s err=%v", item.ID, err)
			}
		} else {
			route.MessageID = item.ReplyRoute
		}
	}
	if item.RouteExpiresAtUS > 0 {
		route.ExpiresAt = time.UnixMicro(item.RouteExpiresAtUS).UTC()
	}
	return route
}

func outcomeFor(result SendResult) repo.BotSendOutcome {
	out := repo.BotSendOutcome{PlatformMessageID: result.PlatformMessageID, Code: result.Code, Detail: result.SafeDetail}
	switch result.Outcome {
	case Delivered:
		out.Outcome = "delivered"
	case NotDelivered:
		out.Outcome = "not_delivered"
		if !result.Retryable {
			out.Outcome = "rejected"
		}
	case Rejected:
		out.Outcome = "rejected"
	default:
		out.Outcome = "unknown"
	}
	if out.Detail == "" {
		out.Detail = "回复发送结果：" + out.Outcome
	}
	return out
}

// deliver runs one send attempt of a ready interaction: mark it sending in a
// committed transaction, call the platform outside any transaction with a
// bounded timeout, then record the classified outcome. A lease too short to
// cover the call skips this tick; a state change (expired route, closed
// idempotency window) is already recorded by BeginBotSend.
func (s *Service) deliver(ctx context.Context, id string, lease repo.BotLease, policy repo.BotSendPolicy, send func(context.Context, domain.BotInteraction) SendResult) error {
	sending, err := s.Store.BeginBotSend(id, lease, policy)
	switch {
	case errors.Is(err, repo.ErrBotStateConflict), errors.Is(err, repo.ErrBotLeaseLow):
		return nil
	case err != nil:
		return err
	}
	callCtx, cancel := context.WithTimeout(ctx, sendTimeout)
	result := send(callCtx, sending)
	cancel()
	if _, err := s.Store.FinishBotSend(id, lease, policy, outcomeFor(result)); err != nil {
		// The platform result arrived after this worker lost ownership; the
		// new owner resolves the item from its sending state. Keep an audit
		// line with the outcome only.
		log.Printf("bot late send receipt id=%s outcome=%s code=%s err=%v", id, result.Outcome, result.Code, err)
		return err
	}
	return nil
}
