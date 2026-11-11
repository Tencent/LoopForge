package publication

import (
	"context"
	"errors"
)

var ErrRevisionConflict = errors.New("knowledge revision conflict")

// Fence carries a DB-generated, monotonically increasing source claim token
// into the independent content transaction. Content CAS rejects older claims.
type fenceKey struct{}
type Fence struct {
	Source string
	Token  int64
}

func WithFence(ctx context.Context, source string, token int64) context.Context {
	return context.WithValue(ctx, fenceKey{}, Fence{Source: source, Token: token})
}
func FromContext(ctx context.Context) Fence { f, _ := ctx.Value(fenceKey{}).(Fence); return f }

type intentKey struct{}
type Intent struct {
	Project, Fingerprint, ID string
	Version                  uint64
}

func WithIntent(ctx context.Context, intent Intent) context.Context {
	return context.WithValue(ctx, intentKey{}, intent)
}
func IntentFrom(ctx context.Context) Intent { v, _ := ctx.Value(intentKey{}).(Intent); return v }

type documentGenerationKey struct{}
type DocumentGeneration struct {
	Source, Path string
	Value        int64
	Set          bool
}

func WithDocumentGeneration(ctx context.Context, source, path string, value int64) context.Context {
	return context.WithValue(ctx, documentGenerationKey{}, DocumentGeneration{Source: source, Path: path, Value: value, Set: true})
}
func GenerationFrom(ctx context.Context) DocumentGeneration {
	v, _ := ctx.Value(documentGenerationKey{}).(DocumentGeneration)
	return v
}
