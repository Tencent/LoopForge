package botinteraction

import (
	"context"
	"log"
	"strings"
)

// WeCom versions of the write-confirmation and clarification cards. WeCom
// template cards cannot mix drop-downs with several buttons: a card with
// drop-downs is a multiple_interaction card (at most three drop-downs of
// at most ten options, one submit button), so the repository, the SOP and
// what to do ("确认执行 / 确认并启动 SOP / 取消") are three drop-downs sent
// together; other cards are button_interaction cards. Clicks arrive as
// template_card_event callbacks on the long connection: the task ID starts
// with the draft or clarification session (and, for clarification cards,
// the button reference), the selections come in selected_items.

const (
	// DraftActionSubmit is the submit key of a WeCom confirmation card
	// with drop-downs; the operation comes from the draftOpKey selection.
	DraftActionSubmit = "draft_submit"
	draftOpKey        = "draft_op"
	// wecomSelectLimit is the option limit of a WeCom drop-down.
	wecomSelectLimit = 10
)

// wecomCardTaskID builds a unique task ID that routes the click back:
// "<draft>_<nonce>" or "<session>_<ref>_<nonce>".
func wecomCardTaskID(c *PushCard) string {
	nonce := randomID()[:12]
	if c.ClarifyID != "" {
		ref := ""
		if len(c.Actions) > 0 {
			ref = c.Actions[0].Ref
		}
		return c.ClarifyID + "_" + ref + "_" + nonce
	}
	return c.DraftID + "_" + nonce
}

// wecomSelectOptions keeps a drop-down within the WeCom option limit: the
// first option (the project default) and the selected one always stay.
func wecomSelectOptions(sel *PushCardSelect) ([]map[string]string, bool) {
	out := []map[string]string{}
	seen := map[string]bool{}
	add := func(o PushCardOption) {
		if seen[o.Value] || len(out) == wecomSelectLimit {
			return
		}
		seen[o.Value] = true
		out = append(out, map[string]string{"id": o.Value, "text": cardText(o.Text, 30)})
	}
	if len(sel.Options) > 0 {
		add(sel.Options[0])
	}
	for _, o := range sel.Options {
		if o.Value == sel.Initial {
			add(o)
		}
	}
	for _, o := range sel.Options {
		add(o)
	}
	return out, len(sel.Options) > len(out)
}

// wecomCard renders a confirmation or clarification card as a WeCom
// template card with the given task ID.
func wecomCard(c *PushCard, taskID string) map[string]any {
	title := cardText(c.headerTitle(), 26)
	choices := c.Selects
	if len(choices) == 0 {
		choices = c.lockedSelects
	}
	if len(choices) > 0 && c.DraftID != "" {
		selects := []map[string]any{}
		truncated := false
		for _, sel := range choices {
			options, cut := wecomSelectOptions(sel)
			truncated = truncated || cut
			item := map[string]any{"question_key": sel.Key, "title": sel.Label, "option_list": options}
			if sel.Initial != "" {
				item["selected_id"] = sel.Initial
			}
			selects = append(selects, item)
		}
		if len(c.Actions) > 0 {
			// What to do on submit, as the last drop-down.
			op := map[string]any{"question_key": draftOpKey, "title": "提交后", "selected_id": c.Actions[0].Key}
			options := []map[string]string{}
			for _, a := range c.Actions {
				text := a.Text
				if a.Key == DraftActionCancel {
					text = "取消，不执行"
				}
				options = append(options, map[string]string{"id": a.Key, "text": text})
			}
			op["option_list"] = options
			selects = append(selects, op)
		}
		desc := "选好后点“提交”"
		if truncated {
			desc = "选项未列全时，请用上方链接在平台确认"
		}
		card := map[string]any{"card_type": "multiple_interaction", "task_id": taskID, "main_title": map[string]string{"title": title, "desc": desc}, "submit_button": map[string]string{"text": "提交", "key": DraftActionSubmit}}
		if c.Status != "" {
			// Handled: the drop-downs are locked and the button greyed.
			card["main_title"] = map[string]string{"title": title, "desc": cardText(c.Status, 44)}
			card["replace_text"] = cardText(c.Status, 20)
			for _, s := range selects {
				s["disable"] = true
			}
		}
		card["select_list"] = selects
		return card
	}
	body := strings.TrimSpace(c.Body)
	if c.Reason != "" {
		body = strings.TrimSpace(body + "\n原因：" + c.Reason)
	}
	if c.Required != "" {
		body = strings.TrimSpace(body + "\n" + c.Required)
	}
	card := map[string]any{"card_type": "button_interaction", "task_id": taskID, "main_title": map[string]string{"title": title, "desc": cardText(c.Tag, 26)}}
	if body != "" {
		card["sub_title_text"] = cardText(body, 112)
	}
	rows := []map[string]string{}
	for _, row := range c.Rows {
		if len(rows) == 6 {
			break
		}
		rows = append(rows, map[string]string{"keyname": cardText(row[0], 5), "value": cardText(row[1], 26)})
	}
	if len(rows) > 0 {
		card["horizontal_content_list"] = rows
	}
	buttons := []map[string]any{}
	for _, a := range c.Actions {
		style := 1
		if a.Danger {
			style = 4
		}
		buttons = append(buttons, map[string]any{"text": cardText(a.Text, 10), "key": a.Key, "style": style})
	}
	if c.Status != "" {
		card["sub_title_text"] = cardText(c.Status, 112)
		buttons = []map[string]any{{"text": cardText(c.Status, 10), "key": "handled", "style": 3}}
	}
	if len(buttons) > 6 {
		buttons = buttons[:6]
	}
	card["button_list"] = buttons
	if c.DetailsURL != "" {
		label := c.DetailsLabel
		if label == "" {
			label = "查看详情"
		}
		card["jump_list"] = []map[string]any{{"type": 1, "title": label, "url": c.DetailsURL}}
	}
	return card
}

// sendWeComCard sends a card after its message went out; best effort (the
// message itself names the platform confirmation link or the commands).
func sendWeComCard(ctx context.Context, conn Conn, chatID string, card *PushCard) {
	cards, ok := conn.(cardConn)
	if !ok || card == nil {
		return
	}
	if res := cards.sendCard(ctx, chatID, wecomCard(card, wecomCardTaskID(card))); res.Outcome != Delivered {
		log.Printf("wecom card not sent outcome=%s code=%s", res.Outcome, res.Code)
	}
}

// wecomCardAnswer answers a WeCom card click inside the callback window:
// the updated card when there is one; a refusal goes to the chat as text,
// since WeCom has no toast.
func (s *socket) wecomCardAnswer(ctx context.Context, ev InboundEvent, ack InboundAck) {
	if s == nil || s.conn == nil || ev.Route.RequestID == "" {
		return
	}
	if ack.Card != nil && ack.Card.Status != "" && (ack.Card.DraftID != "" || ack.Card.ClarifyID != "") {
		if ack.InteractionID != "" {
			// Take the click's response slot first: the queued click must
			// not update the card a second time with the same callback.
			state := s.streamState(ack.InteractionID)
			state.mu.Lock()
			taken := state.finished
			state.finished = true
			state.mu.Unlock()
			if taken {
				return
			}
		}
		card := wecomCard(ack.Card, ev.CardTaskID)
		// The reader delivers the acknowledgement: never wait on it here.
		go func() {
			if err := s.request(ctx, "aibot_respond_update_msg", ev.Route.RequestID, map[string]any{"response_type": "update_template_card", "template_card": card}); err != nil {
				log.Printf("wecom card update failed action=%s", ev.Action)
			}
		}()
		return
	}
	if ack.Ignored != "" && ack.Toast != "" && ev.ChatID != "" {
		go func() {
			if err := s.send(ctx, ev.ChatID, ack.Toast); err != nil {
				log.Printf("wecom card refusal not sent action=%s", ev.Action)
			}
		}()
	}
}

// wecomCardSelections reads the drop-downs of a submitted card.
func wecomCardSelections(items []wecomSelectedItem) map[string]string {
	out := map[string]string{}
	for _, item := range items {
		if len(item.OptionIDs.OptionID) > 0 && item.QuestionKey != "" {
			out[item.QuestionKey] = strings.TrimSpace(item.OptionIDs.OptionID[0])
		}
	}
	return out
}

type wecomSelectedItem struct {
	QuestionKey string `json:"question_key"`
	OptionIDs   struct {
		OptionID []string `json:"option_id"`
	} `json:"option_ids"`
}
