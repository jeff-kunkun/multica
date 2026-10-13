package service

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

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

// issueFieldsCoveredByTicketStream are the ticket changes the ticket stream
// already reports in words (status.changed, assignee.changed).
var issueFieldsCoveredByTicketStream = map[string]bool{
	"status": true, "assignee_type": true, "assignee_id": true,
}

// coveredByTicketStream reports whether the ticket stream already carries this
// write, so the workspace stream does not say it a second time.
func coveredByTicketStream(w WorkspaceWrite, family string) bool {
	if family != FamilyIssue {
		return false
	}
	route := strings.ToLower(w.Route)
	method := strings.ToUpper(w.Method)
	if (method == "POST" || method == "PATCH" || method == "PUT") &&
		(strings.Contains(route, "/comments") || strings.Contains(route, "/chat/sessions/{") && strings.HasSuffix(strings.TrimRight(route, "/"), "/messages")) {
		return true
	}
	if method == "PATCH" && len(w.Fields) > 0 {
		for _, f := range w.Fields {
			if !issueFieldsCoveredByTicketStream[f] {
				return false
			}
		}
		return true
	}
	return false
}

// RouteWorkspaceWrite turns a write into a workspace event for every Bot that
// asked for the whole workspace, and whose owner could open what the write
// touched. visible is that judgement (the handler's, shared with the send gate
// and the pull list); a write aimed at a ticket, project or chat is not pushed
// without it, nor when its target cannot be found.
//
// A write the owner's own Bot was stopped on (Blocked) is not echoed to that
// owner as an activity row: they hear it once through the approval card.
func RouteWorkspaceWrite(ctx context.Context, q *db.Queries, w WorkspaceWrite, visible TargetVisibility) (int, error) {
	// Delivery acknowledgements are bookkeeping, never new news to relay.
	// Keep their audit rows, but do not create a receipt -> alert loop.
	if strings.Contains(w.Route, "/office-events/") && strings.HasSuffix(w.Route, "/receipt") {
		return 0, nil
	}
	if !w.WorkspaceID.Valid {
		return 0, nil
	}
	family := ClassifyWorkspaceRoute(w.Route)
	if !w.Blocked && coveredByTicketStream(w, family) {
		return 0, nil
	}
	var target WriteTarget
	if !w.Blocked {
		if len(w.TargetIDs) > 0 {
			t, ok := ResolveWriteTarget(ctx, q, w.WorkspaceID, w.TargetIDs)
			if !ok {
				return 0, nil // two different tickets (or projects, chats) in one write
			}
			target = t
		}
		if !target.Established(family) {
			return 0, nil // aimed at something we cannot name (deleted, batch): not told
		}
	}
	body := WorkspaceEventBody{
		Family:    family,
		Action:    WorkspaceWriteAction(w.Method),
		Route:     strings.ToUpper(w.Method) + " " + w.Route,
		Fields:    w.Fields,
		EntityID:  util.UUIDToString(w.EntityID),
		ActorKind: "system",
		Dangerous: w.Dangerous,
	}
	if !w.EntityID.Valid {
		body.EntityID = ""
	}
	actorType := "system"
	switch {
	case w.BotID.Valid:
		actorType = "member"
		body.ActorKind = "bot"
		body.ViaBotID = util.UUIDToString(w.BotID)
		body.ViaBotName = w.BotName
		body.OnBehalfOfUserID = util.UUIDToString(w.OnBehalfOf)
		if u, err := q.GetUser(ctx, w.OnBehalfOf); err == nil {
			body.OnBehalfOfName = u.Name
		}
		body.ActorID = util.UUIDToString(w.OnBehalfOf)
		w.ActorID = w.OnBehalfOf
	case w.ActorType == "agent" && w.ActorID.Valid:
		actorType = "agent"
		body.ActorKind = "agent"
		body.ActorID = util.UUIDToString(w.ActorID)
		if a, err := q.GetAgent(ctx, w.ActorID); err == nil {
			body.ActorName = a.Name
		}
	case w.ActorID.Valid:
		actorType = "member"
		body.ActorKind = "member"
		body.ActorID = util.UUIDToString(w.ActorID)
		if u, err := q.GetUser(ctx, w.ActorID); err == nil {
			body.ActorName = u.Name
		}
	}
	body.ActorName = MaskSecrets(body.ActorName)
	body.ViaBotName = MaskSecrets(body.ViaBotName)
	body.OnBehalfOfName = MaskSecrets(body.OnBehalfOfName)

	activityType := ActivityWorkspaceWrite
	if w.Blocked {
		activityType = ActivityBotBlocked
	}
	in := WorkspaceActivityInput{
		WorkspaceID:  w.WorkspaceID,
		ActivityType: activityType,
		DedupeKey:    "ws:write:" + uuid.NewString(),
		Body:         body,
		Target:       target,
		Visible:      visible,
		ActorType:    actorType,
		ActorID:      w.ActorID,
	}
	if w.Blocked {
		in.Importance, in.Categories = ImportanceHigh, []string{CategoryRisk, CategoryNeedsHuman}
	}
	return RouteWorkspaceActivity(ctx, q, in)
}
