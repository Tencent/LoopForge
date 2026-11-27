package domain

// NodeAnswerHint tells a chat how to answer a node question. DingTalk does
// not report which message a reply quotes, so it only offers the prefix.
func NodeAnswerHint(channel, chatType string) string {
	hint := "发送「回答：你的答案」"
	if chatType == "group" {
		hint = "@我 发送「回答：你的答案」"
	}
	if channel != BotChannelDingTalk {
		hint += "，或引用本条消息回复"
	}
	return "回答方式：" + hint + "。答案会提交给这个节点，节点按流程继续。"
}
