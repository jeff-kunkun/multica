package service

import "github.com/jackc/pgx/v5/pgtype"

// WorkspaceWrite is one successful (or door-stopped) write as the HTTP layer saw
// it: the route pattern, the names of the fields in the body, who did it. The
// middleware that builds it sits in front of every authenticated route, so a
// route added tomorrow is covered the day it is added.
type WorkspaceWrite struct {
	WorkspaceID pgtype.UUID
	Method      string
	Route       string // chi pattern, never the raw path
	Fields      []string
	EntityID    pgtype.UUID
	// TargetIDs are every id the request pointed at (path, a few body fields, the
	// id of what it created); the access check resolves them to a ticket,
	// project and chat.
	TargetIDs  []pgtype.UUID
	Status     int
	ActorType  string // member | agent | ""
	ActorID    pgtype.UUID
	BotID      pgtype.UUID
	BotName    string
	OnBehalfOf pgtype.UUID
	Dangerous  string
	Blocked    bool
}
