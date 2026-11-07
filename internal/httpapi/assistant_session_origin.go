package httpapi

import (
	"context"
	"strings"
)

// assistantSessionOrigin carries the scheduled task identity through the
// generation context, so every write path that stores the resulting session
// can mark its source without changing executeAssistantTask's signature.
type assistantSessionOrigin struct {
	Source string
	TaskID string
}

type assistantSessionOriginKey struct{}

func withAssistantSessionOrigin(ctx context.Context, source, taskID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, assistantSessionOriginKey{}, assistantSessionOrigin{
		Source: strings.TrimSpace(source),
		TaskID: strings.TrimSpace(taskID),
	})
}

func assistantSessionOriginFrom(ctx context.Context) assistantSessionOrigin {
	if ctx == nil {
		return assistantSessionOrigin{}
	}
	origin, _ := ctx.Value(assistantSessionOriginKey{}).(assistantSessionOrigin)
	return origin
}
