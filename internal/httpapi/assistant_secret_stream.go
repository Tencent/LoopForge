package httpapi

import (
	"sort"
	"strings"
	"sync"

	"agentflow/internal/domain"
)

type assistantSecretFragment struct {
	event assistantStreamEvent
	text  string
}

// assistantSecretStream holds only a suffix that could become a credential.
// Redacting individual model deltas is insufficient: a token often spans
// several deltas, which the browser and generation projection join again.
type assistantSecretStream struct {
	mu      sync.Mutex
	pending map[string]assistantSecretFragment
}

func assistantSecretField(event *assistantStreamEvent) *string {
	switch event.Kind {
	case "answer", "thinking", "agentText":
		return &event.Text
	case "draftDelta":
		return &event.Detail
	default:
		return nil
	}
}

func (s *assistantSecretStream) push(event assistantStreamEvent, user domain.User) []assistantStreamEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	if event.Kind == "answerReset" || event.Kind == "draftStart" {
		for key := range s.pending {
			if strings.HasPrefix(key, "draftDelta\x00") || (event.Kind == "answerReset" && strings.HasPrefix(key, "answer\x00")) {
				delete(s.pending, key)
			}
		}
	}
	field := assistantSecretField(&event)
	if field == nil {
		return []assistantStreamEvent{publicAssistantEvent(event, user)}
	}
	if s.pending == nil {
		s.pending = map[string]assistantSecretFragment{}
	}
	key := event.Kind + "\x00" + event.Parent + "\x00" + event.ID
	if event.Full {
		delete(s.pending, key)
		return []assistantStreamEvent{publicAssistantEvent(event, user)}
	}
	text := redactAssistantSecrets(s.pending[key].text+*field, user)
	secrets := gitSecretRedactions.streamSecrets(user)
	keep := 0
	for _, secret := range secrets {
		for n := min(len(secret)-1, len(text)); n > keep; n-- {
			if strings.HasSuffix(text, secret[:n]) {
				keep = n
				break
			}
		}
	}
	*field = text[:len(text)-keep]
	if keep > 0 {
		s.pending[key] = assistantSecretFragment{event: event, text: text[len(text)-keep:]}
	} else {
		delete(s.pending, key)
	}
	if *field == "" {
		return nil
	}
	return []assistantStreamEvent{publicAssistantEvent(event, user)}
}

func (s *assistantSecretStream) flush(user domain.User) []assistantStreamEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	keys := make([]string, 0, len(s.pending))
	for key := range s.pending {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var events []assistantStreamEvent
	for _, key := range keys {
		fragment := s.pending[key]
		if field := assistantSecretField(&fragment.event); field != nil {
			*field = fragment.text
			events = append(events, publicAssistantEvent(fragment.event, user))
		}
	}
	s.pending = nil
	return events
}

func (r *gitSecretRegistry) streamSecrets(user domain.User) []string {
	r.mu.Lock()
	secrets := append([]string(nil), r.sorted...)
	r.mu.Unlock()
	for _, secret := range []string{user.CodeBuddyAPIKey, user.CodexAPIKey, user.ClaudeCodeAPIKey, user.GitToken} {
		if secret = strings.TrimSpace(secret); secret != "" {
			secrets = append(secrets, secret)
		}
	}
	return secrets
}
