// Package botsource carries authenticated Bot provenance independently of the
// member whose permissions the credential grants. Never populate it from input.
package botsource

import (
	"context"
	"github.com/jackc/pgx/v5/pgtype"
)

type Source struct {
	ID   pgtype.UUID
	Name string
}
type contextKey struct{}

func WithContext(ctx context.Context, source Source) context.Context {
	return context.WithValue(ctx, contextKey{}, source)
}
func FromContext(ctx context.Context) Source {
	source, _ := ctx.Value(contextKey{}).(Source)
	return source
}

// Actor identifies the authenticated actor for audited mutations. It is separate
// from Source: an agent run may itself have originated through a Bot.
type Actor struct {
	Type string
	ID   pgtype.UUID
}
type actorContextKey struct{}

func WithActor(ctx context.Context, actor Actor) context.Context {
	return context.WithValue(ctx, actorContextKey{}, actor)
}
func ActorFromContext(ctx context.Context) Actor {
	actor, _ := ctx.Value(actorContextKey{}).(Actor)
	return actor
}
