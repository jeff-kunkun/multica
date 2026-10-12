package handler

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/botsource"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// EnsureOwnerConfirmation is the handler side of the owner's go-ahead for the
// four dangerous Bot actions (DENE-1936): find the request that is still open
// for this exact call, or open one. Opening one is the single notice to the
// owner: the question card lands in their inbox and, when they connected a Bot,
// is pushed to it through the reliable human-needed channel; the dedupe key is
// the card, so a Bot that keeps retrying does not keep ringing.
func (h *Handler) EnsureOwnerConfirmation(ctx context.Context, req service.OwnerConfirmationRequest) (service.OwnerConfirmation, error) {
	open, err := h.Queries.FindOpenBotActionConfirmation(ctx, db.FindOpenBotActionConfirmationParams{BotID: req.BotID, ScopeHash: req.ScopeHash})
	if err == nil {
		return ownerConfirmationFromRow(open, false), nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return service.OwnerConfirmation{}, err
	}
	title := service.OwnerConfirmationTitle(req.BotName, req.Action)
	questions := []AskQuestion{{
		Text: service.OwnerConfirmationQuestion(req),
		Options: []AskOption{
			{ID: service.OwnerConfirmDeny, Label: "不允许", Recommended: true},
			{ID: service.OwnerConfirmApprove, Label: "允许这一次"},
		},
	}}
	rawQuestions, _ := json.Marshal(questions)
	row, err := h.Queries.CreateBotActionConfirmation(ctx, db.CreateBotActionConfirmationParams{
		WorkspaceID: req.WorkspaceID,
		OwnerUserID: req.OwnerUserID,
		Title:       title,
		Questions:   rawQuestions,
		BotID:       req.BotID,
		BotName:     req.BotName,
		Action:      req.Action,
		Method:      req.Method,
		ScopeHash:   req.ScopeHash,
		Route:       req.Route,
		TtlSeconds:  int32(service.OwnerConfirmationTTL / time.Second),
	})
	if err != nil {
		return service.OwnerConfirmation{}, err
	}
	insert := askInsert{
		Workspace: req.WorkspaceID,
		AskerType: "member",
		AskerID:   req.OwnerUserID,
		Title:     title,
		Questions: questions,
		Mode:      "needs_you",
		Recipient: req.OwnerUserID,
		Kind:      service.OwnerConfirmationKind,
		Urgent:    true,
		Deadline:  row.ExpiresAt.Time,
	}
	ask := AskResponse{
		ID: uuidToString(row.AskID), WorkspaceID: uuidToString(req.WorkspaceID), AskerType: "member", AskerID: uuidToString(req.OwnerUserID),
		Title: title, Questions: questions, Mode: "needs_you", Status: "open", CreatedAt: row.CreatedAt.Time,
	}
	h.deliverAskToPerson(ctx, ask, insert, req.OwnerUserID, uuidToString(req.WorkspaceID), uuidToString(req.OwnerUserID))
	return ownerConfirmationFromRow(row, true), nil
}

func ownerConfirmationFromRow(row db.BotActionConfirmation, created bool) service.OwnerConfirmation {
	return service.OwnerConfirmation{
		ID: row.ID, AskID: row.AskID, Status: row.Decision, ExpiresAt: row.ExpiresAt.Time, Created: created,
	}
}

// errConfirmationGone: the owner answered a request that is no longer live
// (expired, or decided already), so the answer changed nothing.
var errConfirmationGone = errors.New("owner confirmation is no longer live")

// decideOwnerConfirmation carries the owner's own answer into the request it
// belongs to, in the same transaction that closes the card. Only reached for an
// answer the ask code already established came from the owner personally.
func (h *Handler) decideOwnerConfirmation(ctx context.Context, in askAnswer, answers map[string]string, closeAsk func(*db.Queries) error) error {
	decision := service.OwnerConfirmDenied
	if answers["0"] == service.OwnerConfirmApprove {
		decision = service.OwnerConfirmApproved
	}
	write := func(q *db.Queries) error {
		if err := closeAsk(q); err != nil {
			return err
		}
		_, err := q.DecideBotActionConfirmation(ctx, db.DecideBotActionConfirmationParams{
			Decision: decision, DecidedBy: in.Answerer, AskID: in.AskID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return errConfirmationGone
		}
		return err
	}
	err := func() error {
		if h.TxStarter == nil {
			return write(h.Queries)
		}
		tx, err := h.TxStarter.Begin(ctx)
		if err != nil {
			return err
		}
		// Rolled back (and its row locks released) before the caller looks at the
		// outcome, so the fallback below never waits on this transaction.
		defer tx.Rollback(ctx)
		if err := write(h.Queries.WithTx(tx)); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}()
	if errors.Is(err, errConfirmationGone) {
		// An expired request still has an open card; close it so it stops
		// asking, and say the answer did not count.
		if closeErr := closeAsk(h.Queries); closeErr != nil && !errors.Is(closeErr, errAskTaken) {
			return closeErr
		}
	}
	return err
}

// ownerAnsweredPersonally is true only for the owner's own session: not a Bot
// key, not an agent run (even one started through a Bot).
func ownerAnsweredPersonally(ctx context.Context, in askAnswer) bool {
	if in.Bot != nil {
		return false
	}
	return !botsource.FromContext(ctx).ID.Valid && botsource.ActorFromContext(ctx).Type != "agent"
}
