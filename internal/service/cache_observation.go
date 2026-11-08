package service

import "context"

type cacheObservationKey struct{}

func WithCacheObservation(ctx context.Context, state *string) context.Context {
	return context.WithValue(ctx, cacheObservationKey{}, state)
}
func observeCache(ctx context.Context, state string) {
	if p, ok := ctx.Value(cacheObservationKey{}).(*string); ok {
		*p = state
	}
}
