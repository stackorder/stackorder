package runs

import (
	"context"
	"time"
)

type receivedAtKey struct{}

// WithReceivedAt returns ctx carrying the time a webhook delivery was
// received. Event handlers use it to order installation state changes by
// receive time rather than by the order workers happen to process them.
func WithReceivedAt(ctx context.Context, t time.Time) context.Context {
	return context.WithValue(ctx, receivedAtKey{}, t)
}

func receivedAt(ctx context.Context) time.Time {
	t, _ := ctx.Value(receivedAtKey{}).(time.Time)
	return t
}
