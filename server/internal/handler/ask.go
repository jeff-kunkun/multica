package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type AskOption struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Recommended bool   `json:"recommended,omitempty"`
}
type AskQuestion struct {
	Text    string      `json:"text"`
	Options []AskOption `json:"options"`
}
type AskResponse struct {
	ID          string            `json:"id"`
	WorkspaceID string            `json:"workspace_id"`
	IssueID     *string           `json:"issue_id,omitempty"`
	AskerType   string            `json:"asker_type"`
	AskerID     string            `json:"asker_id"`
	Title       string            `json:"title"`
	Questions   []AskQuestion     `json:"questions"`
	Answers     map[string]string `json:"answers,omitempty"`
	Mode        string            `json:"mode"`
	Status      string            `json:"status"`
	CreatedAt   time.Time         `json:"created_at"`
	AnsweredAt  *time.Time        `json:"answered_at,omitempty"`
}

type answerAskRequest struct {
	Answers map[string]string `json:"answers"`
}

// askInsert is one question card. Recipient is the person it is addressed to
// (the one the Bot speaks for); Kind says what asked it, and a kind other than
// "" resumes the asker when the card is answered by anyone.
type askInsert struct {
	Workspace pgtype.UUID
	Issue     pgtype.UUID
	AskerType string
	AskerID   pgtype.UUID
	Title     string
	Questions []AskQuestion
	Mode      string
	Recipient pgtype.UUID
	Kind      string
	// ChatSession is the chat the card was asked in, when it was.
	ChatSession pgtype.UUID
	// Urgent and Deadline are the asker's own words on how soon the answer
	// is needed; they travel to the Bot with the card.
	Urgent   bool
	Deadline time.Time
}

// askQuestionText is what a Bot is told for a card: the title, then each
// question with its options, in the order the answers are keyed.
func askQuestionText(title string, questions []AskQuestion) string {
	var b strings.Builder
	b.WriteString(strings.TrimSpace(title))
	for i, q := range questions {
		labels := make([]string, 0, len(q.Options))
		for _, o := range q.Options {
			labels = append(labels, o.Label)
		}
		fmt.Fprintf(&b, "\n%d. %s（%s）", i+1, strings.TrimSpace(q.Text), strings.Join(labels, " / "))
	}
	return b.String()
}

// askHumanOptions is the choice list a Bot shows for a single-question card;
// a multi-question card carries its structure in the ask block instead.
func askHumanOptions(questions []AskQuestion) []service.HumanNeededOption {
	if len(questions) != 1 {
		return nil
	}
	out := make([]service.HumanNeededOption, 0, len(questions[0].Options))
	for _, o := range questions[0].Options {
		out = append(out, service.HumanNeededOption{ID: o.ID, Label: o.Label})
	}
	return out
}

// deliverAskToPerson writes the in-app row for a needs_you card and hands the
// call to the person's Bot when they have one (DENE-1933). The card itself
// stays the source of truth: the inbox row only stores its id, so every
// surface renders the same card through GET /api/asks/{id}.
func (h *Handler) deliverAskToPerson(ctx context.Context, ask AskResponse, in askInsert, recipient pgtype.UUID, workspaceID, actorID string) service.HumanNeededResult {
	askID := parseUUID(ask.ID)
	details, _ := json.Marshal(map[string]string{"ask_id": ask.ID})
	item, err := h.Queries.CreateInboxItem(ctx, db.CreateInboxItemParams{
		ID: dbid.NewV7(), WorkspaceID: in.Workspace, RecipientType: "member", RecipientID: recipient,
		Type: service.InboxTypeNeedsYou, Severity: "action_required", IssueID: in.Issue,
		Title: in.Title, Body: pgtype.Text{String: "需要回答提问", Valid: true},
		ActorType: pgtype.Text{String: in.AskerType, Valid: true}, ActorID: in.AskerID, Details: details,
	})
	if err != nil {
		return service.HumanNeededResult{}
	}
	kind := "ask"
	if in.ChatSession.Valid {
		kind = "chat_ask"
	}
	route, routeErr := service.RouteHumanNeeded(ctx, h.Queries, service.HumanNeededInput{
		WorkspaceID:   in.Workspace,
		Target:        recipient,
		Kind:          kind,
		DedupeKey:     "ask:" + ask.ID,
		IssueID:       in.Issue,
		ChatSessionID: in.ChatSession,
		InboxItemID:   item.ID,
		AskID:         askID,
		CallerType:    in.AskerType,
		CallerID:      in.AskerID,
		Question:      askQuestionText(in.Title, in.Questions),
		Options:       askHumanOptions(in.Questions),
		Urgent:        in.Urgent,
		Deadline:      in.Deadline,
	})
	if routeErr != nil {
		slog.Warn("human needed: route ask failed", "error", routeErr, "ask_id", ask.ID)
	} else if route.Silent() {
		if read, readErr := h.Queries.MarkInboxRead(ctx, item.ID); readErr == nil {
			item = read
		}
	}
	h.publishWithSource(ctx, protocol.EventInboxNew, workspaceID, in.AskerType, actorID, map[string]any{"item": inboxToResponse(item)})
	return route
}

// askViewer is the person looking at cards; a card asked in a chat is only
// visible to the chat's owner. Without a person it is the zero id, which sees
// no chat cards.
func askViewer(r *http.Request) pgtype.UUID {
	id, err := util.ParseUUID(requestUserID(r))
	if err != nil {
		return pgtype.UUID{}
	}
	return id
}

func (h *Handler) GetAsk(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	ws, err := util.ParseUUID(workspaceID)
	if err != nil {
		writeError(w, 400, "invalid workspace_id")
		return
	}
	id, err := util.ParseUUID(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, 400, "invalid ask id")
		return
	}
	var out AskResponse
	var qraw, araw []byte
	var issueID, askerID pgtype.UUID
	var answered *time.Time
	err = h.DB.QueryRow(r.Context(), `SELECT id,issue_id,asker_type,asker_id,title,questions,answers,mode,status,created_at,answered_at FROM agent_ask WHERE id=$1 AND workspace_id=$2 AND (chat_session_id IS NULL OR recipient_id=$3)`, id, ws, askViewer(r)).Scan(&id, &issueID, &out.AskerType, &askerID, &out.Title, &qraw, &araw, &out.Mode, &out.Status, &out.CreatedAt, &answered)
	if err != nil {
		if err == pgx.ErrNoRows {
			writeError(w, 404, "ask not found")
		} else {
			writeError(w, 500, "failed to read ask")
		}
		return
	}
	out.ID = id.String()
	out.WorkspaceID = workspaceID
	out.AskerID = askerID.String()
	_ = json.Unmarshal(qraw, &out.Questions)
	if len(araw) > 0 {
		_ = json.Unmarshal(araw, &out.Answers)
	}
	if issueID.Valid {
		s := issueID.String()
		out.IssueID = &s
	}
	out.AnsweredAt = answered
	writeJSON(w, 200, out)
}

func (h *Handler) AnswerAsk(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := h.resolveWorkspaceID(r)
	ws, err := util.ParseUUID(workspaceID)
	if err != nil {
		writeError(w, 400, "invalid workspace_id")
		return
	}
	id, err := util.ParseUUID(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, 400, "invalid ask id")
		return
	}
	var req answerAskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid JSON")
		return
	}
	answerer, _ := util.ParseUUID(userID)
	answers, askErr := h.answerAskCore(r.Context(), askAnswer{Workspace: ws, AskID: id, Answers: req.Answers, Answerer: answerer})
	if askErr != nil {
		writeError(w, askErr.Code, askErr.Msg)
		return
	}
	writeJSON(w, 200, map[string]any{"id": id.String(), "status": "answered", "answers": answers})
}

type askError struct {
	Code int
	Msg  string
}

// askAnswer is one answer to a card. Bot is set when the answer came through
// a person's Bot: the Bot acts as its owner, so Answerer is the owner and the
// audit trail says which Bot spoke for them.
type askAnswer struct {
	Workspace pgtype.UUID
	AskID     pgtype.UUID
	Answers   map[string]string
	Answerer  pgtype.UUID
	Bot       *db.PersonalAccessToken
}

// answerAskCore is the one place a card gets answered: the app, the CLI and
// the Bot's `answered` receipt all land here. Missing questions use their
// recommendation (or the first option), which is the contract for side
// questions and also makes a one-click answer safe when a client only sends
// the currently visible tab.
func (h *Handler) answerAskCore(ctx context.Context, in askAnswer) (map[string]string, *askError) {
	var qraw []byte
	var askerID, issueID, recipient, chatID pgtype.UUID
	var askerType, mode, kind, title string
	if err := h.DB.QueryRow(ctx, `SELECT questions, asker_type, asker_id, issue_id, mode, kind, title, recipient_id, chat_session_id FROM agent_ask WHERE id=$1 AND workspace_id=$2 AND status='open'`, in.AskID, in.Workspace).Scan(&qraw, &askerType, &askerID, &issueID, &mode, &kind, &title, &recipient, &chatID); err != nil {
		if err == pgx.ErrNoRows {
			return nil, &askError{409, "ask is already answered or not found"}
		}
		return nil, &askError{500, "failed to read ask"}
	}
	if in.Bot != nil && recipient.Valid && recipient != in.Answerer {
		return nil, &askError{403, "这张提问不是问这位 Bot 的主人的"}
	}
	// The owner's go-ahead for a dangerous Bot action is theirs alone: not a
	// Bot's receipt, not an agent run, not another member. This is what makes
	// "the owner approved" something the server can stand behind.
	if kind == service.OwnerConfirmationKind {
		if !ownerAnsweredPersonally(ctx, in) {
			return nil, &askError{403, "这张卡只有主人本人能答：请本人在 Multica 里点这张提问卡，或用 multica ask answer；Bot 和智能体不能代答"}
		}
		if !recipient.Valid || recipient != in.Answerer {
			return nil, &askError{403, "这张卡是问另一个人的，只有对方本人能答"}
		}
	}
	// A card asked in a chat is that chat's owner's to answer: the answer is
	// written back into their chat.
	if chatID.Valid && recipient.Valid && recipient != in.Answerer {
		return nil, &askError{403, "这张提问是在别人的聊天里问的，只有对方能回答"}
	}
	var questions []AskQuestion
	if err := json.Unmarshal(qraw, &questions); err != nil {
		return nil, &askError{500, "stored ask is invalid"}
	}
	answers := map[string]string{}
	for k, v := range in.Answers {
		answers[k] = v
	}
	for i, question := range questions {
		key := fmt.Sprintf("%d", i)
		value := strings.TrimSpace(answers[key])
		if value == "" {
			for _, option := range question.Options {
				if option.Recommended {
					value = option.ID
					break
				}
			}
			if value == "" && len(question.Options) > 0 {
				value = question.Options[0].ID
			}
		}
		if value == "" {
			return nil, &askError{400, fmt.Sprintf("answer for question %d is required", i+1)}
		}
		answers[key] = value
	}
	raw, _ := json.Marshal(answers)
	workspaceID := uuidToString(in.Workspace)
	answererID := uuidToString(in.Answerer)
	// Answering the card and carrying the answer back to whoever asked commit
	// together: a card whose write-back failed is still open, so the same
	// answer can be given again and resumes the asker exactly once.
	closeAsk := func(q *db.Queries) error {
		if _, err := q.AnswerAsk(ctx, db.AnswerAskParams{Answers: raw, ID: in.AskID, WorkspaceID: in.Workspace}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return errAskTaken
			}
			return err
		}
		// The Bot's chase is over, whoever answered.
		return q.SettleHumanNeededByAsk(ctx, in.AskID)
	}
	var err error
	if kind == service.OwnerConfirmationKind {
		err = h.decideOwnerConfirmation(ctx, in, answers, closeAsk)
	} else {
		err = closeAsk(h.Queries)
	}
	if errors.Is(err, errAskTaken) {
		return nil, &askError{409, "ask is already answered or not found"}
	}
	if errors.Is(err, errConfirmationGone) {
		return nil, &askError{410, "这个点头请求已经过期或已有结论，这次回答没有生效；需要的话让 Bot 重新发起"}
	}
	if err != nil {
		slog.Warn("ask: answer not carried back", "error", err, "ask_id", uuidToString(in.AskID))
		return nil, &askError{500, "回答没能送回提问的人，提问卡还开着，请再答一次：" + err.Error()}
	}
	// Inbox delivery is the durable record for the asker.
	if mode == "needs_you" && in.Answerer.Valid && !chatID.Valid && kind != service.OwnerConfirmationKind {
		recipientType := "agent"
		if askerType == "member" {
			recipientType = "member"
		}
		item, inboxErr := h.Queries.CreateInboxItem(ctx, db.CreateInboxItemParams{
			ID: dbid.NewV7(), WorkspaceID: in.Workspace, RecipientType: recipientType, RecipientID: askerID,
			Type: "ask_answered", Severity: "action_required", IssueID: issueID,
			Title: "提问已回答", Body: pgtype.Text{String: string(raw), Valid: true},
			ActorType: pgtype.Text{String: "member", Valid: true}, ActorID: in.Answerer, Details: raw,
		})
		if inboxErr == nil {
			h.publishWithSource(ctx, protocol.EventInboxNew, workspaceID, "member", answererID, map[string]any{"item": inboxToResponse(item)})
		}
	}
	// A platform ask with its own effect (DENE-1647): the answer is carried
	// out here, so the CLI, the issue card and the inbox all act the same.
	h.applyReviewStuckAnswer(ctx, in.Workspace, issueID, answererID, answers["0"])
	return answers, nil
}

// errAskTaken says another caller answered the card first.
var errAskTaken = errors.New("ask is already answered")
