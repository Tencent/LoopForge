package domain

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// GroupConversationEntry is a read-only projection of the shared transcript and
// role events. Content is conversation data, never an instruction or permission.
// IDs remain stable as new entries are appended; Kind separates human/agent
// messages from lifecycle notices and legacy task results.
type GroupConversationEntry struct {
	ID          string     `json:"id"`
	Kind        string     `json:"kind"`
	Role        string     `json:"role,omitempty"`
	Speaker     string     `json:"speaker"`
	AgentID     string     `json:"agentId,omitempty"`
	NodeID      string     `json:"nodeId,omitempty"`
	AuthorID    int64      `json:"authorId,omitempty"`
	Timestamp   time.Time  `json:"timestamp"`
	Content     string     `json:"content"`
	Mentions    []string   `json:"mentions,omitempty"`
	Truncated   bool       `json:"truncated,omitempty"`
	Trigger     string     `json:"trigger,omitempty"`
	TriggerID   string     `json:"triggerId,omitempty"`
	CompletedAt *time.Time `json:"completedAt,omitempty"`
}

const groupConversationEntryRunes = 2000
const groupConversationTotalRunes = 24000

// BuildGroupConversation merges every participant's published speech in stable
// chronological order. The newest bounded window is returned; process telemetry,
// hidden approval payloads and save receipts do not become chat messages.
func BuildGroupConversation(run Run, messages []AssistantMessage, limit int) []GroupConversationEntry {
	if limit <= 0 {
		limit = 40
	}
	if limit > 80 {
		limit = 80
	}
	return buildGroupConversation(run, messages, limit, groupConversationEntryRunes, groupConversationTotalRunes)
}

// ArchiveGroupConversation retains all published speech without the recent-chat
// projection's length/window limits. Tool payloads and approval secrets stay out.
func ArchiveGroupConversation(run Run, messages []AssistantMessage) []GroupConversationEntry {
	return buildGroupConversation(run, messages, 0, 0, 0)
}

func buildGroupConversation(run Run, messages []AssistantMessage, limit, entryRunes, totalRunes int) []GroupConversationEntry {
	entries := make([]GroupConversationEntry, 0, len(messages)+len(run.Events))
	appendEntry := func(entry GroupConversationEntry) {
		entry.Content = strings.TrimSpace(entry.Content)
		if entry.Content == "" {
			return
		}
		text := []rune(entry.Content)
		if entryRunes > 0 && len(text) > entryRunes {
			entry.Content = string(text[:entryRunes])
			entry.Truncated = true
		}
		entries = append(entries, entry)
	}
	for i, message := range messages {
		if message.Role != "user" && message.Role != "assistant" {
			continue
		}
		if groupConversationReceipt(message.Content) {
			continue
		}
		text := message.Content
		if message.Role == "assistant" {
			var reply struct {
				Message *string `json:"message"`
			}
			if json.Unmarshal([]byte(text), &reply) == nil && reply.Message != nil {
				text = *reply.Message
			}
		}
		stamp := message.CreatedAt
		if message.StartedAt != nil && !message.StartedAt.IsZero() {
			stamp = *message.StartedAt
		}
		if message.Role == "assistant" && message.Trigger == "" && (message.StartedAt == nil || message.StartedAt.IsZero()) {
			// Legacy replies have no start timestamp. Anchor them after their user turn
			// instead of moving them past the roles dispatched during that reply.
			for j := i - 1; j >= 0; j-- {
				if messages[j].Role == "user" && !messages[j].CreatedAt.IsZero() {
					stamp = messages[j].CreatedAt.Add(time.Duration(i-j) * time.Nanosecond)
					break
				}
			}
		}
		if stamp.IsZero() {
			stamp = run.CreatedAt.Add(time.Duration(i) * time.Nanosecond)
		}
		speaker := message.SpeakerName
		if message.Role == "user" {
			speaker = message.AuthorName
			if speaker == "" {
				speaker = "用户"
			}
		} else if speaker == "" {
			speaker = "Main Bot"
		}
		mentions := []string{}
		for _, ref := range message.References {
			if ref.Type == "agent" && ref.ID != "" {
				mentions = append(mentions, ref.ID)
			}
		}
		appendEntry(GroupConversationEntry{ID: fmt.Sprintf("conversation/%d", i), Kind: "message", Role: message.Role, Speaker: speaker, AgentID: message.SpeakerAgentID, AuthorID: message.AuthorID, Timestamp: stamp, Content: text, Mentions: mentions, Trigger: message.Trigger, TriggerID: message.TriggerID, CompletedAt: &message.CreatedAt})
	}
	nodes := map[string]WorkflowNode{}
	for _, node := range run.WorkflowSnapshot.Nodes {
		nodes[node.ID] = node
	}
	for _, old := range run.WorkflowSnapshot.GroupHistory {
		nodes[old.Node.ID] = old.Node
	}
	published := map[string]bool{}
	for i, event := range run.Events {
		kind := ""
		switch event.Type {
		case "group.message":
			kind = "message"
			published[event.NodeID] = true
		case "group.invited", "group.dispatched":
			kind = "system"
		default:
			continue
		}
		node, ok := nodes[event.NodeID]
		speaker := "Main Bot"
		if ok {
			speaker = node.Name
			if speaker == "" {
				speaker = node.AgentID
			}
		}
		role := "assistant"
		if kind == "system" {
			speaker = "系统"
			role = ""
		}
		id := event.ID
		if id == "" {
			id = fmt.Sprintf("legacy-%d", i)
		}
		appendEntry(GroupConversationEntry{ID: "event/" + id, Kind: kind, Role: role, Speaker: speaker, AgentID: node.AgentID, NodeID: event.NodeID, Timestamp: event.Timestamp, Content: event.Message})
	}
	for _, node := range run.WorkflowSnapshot.Nodes {
		nr := run.Nodes[node.ID]
		if node.RequestID == "" || nr == nil || published[node.ID] || (nr.Status != "succeeded" && nr.Status != "failed" && nr.Status != "cancelled") {
			continue
		}
		text := nr.Output
		if nr.Error != "" {
			text = nr.Error
		}
		appendEntry(GroupConversationEntry{ID: fmt.Sprintf("result/%s/%d", node.ID, nr.Attempt), Kind: "result", Role: "assistant", Speaker: node.Name, AgentID: node.AgentID, NodeID: node.ID, Timestamp: nr.FinishedAt, Content: text})
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].Timestamp.Before(entries[j].Timestamp) })
	if limit > 0 && len(entries) > limit {
		entries = entries[len(entries)-limit:]
	}
	if totalRunes <= 0 {
		return entries
	}
	budget := totalRunes
	start := len(entries)
	for start > 0 {
		size := len([]rune(entries[start-1].Content))
		if size > budget {
			break
		}
		budget -= size
		start--
	}
	return entries[start:]
}

func groupConversationReceipt(text string) bool {
	for _, prefix := range []string{"已通过 MCP 执行 ", "已保存助手定时任务：", "上一个草稿已确认保存成功", "刚才的草稿保存失败："} {
		if strings.HasPrefix(text, prefix) {
			return true
		}
	}
	return false
}
