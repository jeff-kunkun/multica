// Package dbactor tells the database who is writing (DENE-1963).
//
// The office event stream is captured by triggers, inside the transaction of
// the write itself, so a crash after commit cannot lose an event. A trigger
// cannot see the HTTP request, so the pool stamps every connection it hands out
// with the actor carried on the context: the session setting `multica.actor`
// holds a small JSON object the triggers copy into the event. A context without
// an actor (a background job nobody labelled) stamps "{}", which the triggers
// record as a system write, so a value left behind by an earlier request can
// never be blamed on the wrong person.
//
// The setting is only re-sent when it differs from what that connection
// already holds, so a run of queries by the same actor costs nothing extra.
package dbactor

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
)

// Setting is the session setting the triggers read.
const Setting = "multica.actor"

// Actor is who performed a write, as the stream records it. Type is member,
// agent or system. ViaBotID is set when a person's Bot key made the call;
// OnBehalfOf is the person a Bot or an agent run acted for. Source names the
// background job for system writes (routing, sweeper, ...).
type Actor struct {
	Type       string `json:"type,omitempty"`
	ID         string `json:"id,omitempty"`
	ViaBotID   string `json:"via_bot_id,omitempty"`
	OnBehalfOf string `json:"on_behalf_of_user_id,omitempty"`
	TaskID     string `json:"task_id,omitempty"`
	Source     string `json:"source,omitempty"`
}

type ctxKey struct{}

// With returns ctx carrying a. An empty actor is still an actor: it marks the
// context as "system, unlabelled".
func With(ctx context.Context, a Actor) context.Context {
	return context.WithValue(ctx, ctxKey{}, a)
}

// System labels a background job's writes.
func System(ctx context.Context, source string) context.Context {
	return With(ctx, Actor{Type: "system", Source: source})
}

// From returns the actor on ctx, if any.
func From(ctx context.Context) (Actor, bool) {
	a, ok := ctx.Value(ctxKey{}).(Actor)
	return a, ok
}

// encode is the exact text stored in the session setting.
func encode(ctx context.Context) string {
	a, ok := From(ctx)
	if !ok || a == (Actor{}) {
		return "{}"
	}
	raw, err := json.Marshal(a)
	if err != nil {
		return "{}"
	}
	return string(raw)
}

const customKey = "dbactor"

// PrepareConn is a pgxpool PrepareConn hook. It runs before every acquire and
// stamps the connection with the actor on ctx when the connection holds a
// different one. A failed stamp destroys the connection and the query retries
// on another, so a write can never run under a stale actor.
func PrepareConn(ctx context.Context, conn *pgx.Conn) (bool, error) {
	want := encode(ctx)
	data := conn.PgConn().CustomData()
	if have, ok := data[customKey].(string); ok && have == want {
		return true, nil
	}
	if _, err := conn.Exec(ctx, "SELECT set_config($1, $2, false)", Setting, want); err != nil {
		delete(data, customKey)
		if ctx.Err() != nil {
			// The caller gave up; the query fails with its own error.
			return true, ctx.Err()
		}
		return false, nil
	}
	data[customKey] = want
	return true, nil
}
