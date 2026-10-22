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

type createAskRequest struct {
	Title     string        `json:"title"`
	Questions []AskQuestion `json:"questions"`
	IssueID   string        `json:"issue_id"`
	Mode      string        `json:"mode"`
}
type answerAskRequest struct {
	Answers map[string]string `json:"answers"`
}

func validateAskQuestions(qs []AskQuestion) error {
	if len(qs) < 1 || len(qs) > 4 {
		return fmt.Errorf("questions must contain 1-4 items")
	}
	for i, q := range qs {
		if strings.TrimSpace(q.Text) == "" {
			return fmt.Errorf("question %d text is required", i+1)
		}
		if len(q.Options) < 2 || len(q.Options) > 6 {
			return fmt.Errorf("question %d must contain 2-6 options", i+1)
		}
		recommended := 0
		seen := map[string]bool{}
		for _, o := range q.Options {
			if strings.TrimSpace(o.ID) == "" || strings.TrimSpace(o.Label) == "" {
				return fmt.Errorf("question %d options need id and label", i+1)
			}
			if seen[o.ID] {
				return fmt.Errorf("question %d has duplicate option id %q", i+1, o.ID)
			}
			seen[o.ID] = true
			if o.Recommended {
				recommended++
			}
		}
		if recommended > 1 {
			return fmt.Errorf("question %d has multiple recommended options", i+1)
		}
	}
	return nil
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

func (h *Handler) insertAsk(ctx context.Context, in askInsert) (AskResponse, error) {
	questions, _ := json.Marshal(in.Questions)
	var id, storedIssue, asker pgtype.UUID
	var created time.Time
	err := h.DB.QueryRow(ctx, `INSERT INTO agent_ask(workspace_id,issue_id,asker_type,asker_id,title,questions,mode,recipient_id,kind,chat_session_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) RETURNING id,issue_id,asker_id,created_at`,
		in.Workspace, in.Issue, in.AskerType, in.AskerID, in.Title, questions, in.Mode, in.Recipient, in.Kind, in.ChatSession).Scan(&id, &storedIssue, &asker, &created)
	if err != nil {
		return AskResponse{}, err
	}
	out := AskResponse{ID: id.String(), WorkspaceID: uuidToString(in.Workspace), AskerType: in.AskerType, AskerID: asker.String(), Title: in.Title, Questions: in.Questions, Mode: in.Mode, Status: "open", CreatedAt: created}
	if storedIssue.Valid {
		s := storedIssue.String()
		out.IssueID = &s
	}
	return out, nil
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

func (h *Handler) CreateAsk(w http.ResponseWriter, r *http.Request) {
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
	var req createAskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid JSON")
		return
	}
	if strings.TrimSpace(req.Title) == "" {
		writeError(w, 400, "title is required")
		return
	}
	if err := validateAskQuestions(req.Questions); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	if req.Mode == "" {
		req.Mode = "needs_you"
	}
	if req.Mode != "needs_you" && req.Mode != "side_question" {
		writeError(w, 400, "mode must be needs_you or side_question")
		return
	}
	actorType, actorID := h.resolveActor(r, userID, workspaceID)
	askerID, err := util.ParseUUID(actorID)
	if err != nil {
		writeError(w, 400, "invalid asker")
		return
	}
	var issueID pgtype.UUID
	if strings.TrimSpace(req.IssueID) != "" {
		id, e := util.ParseUUID(req.IssueID)
		if e != nil {
			writeError(w, 400, "invalid issue_id")
			return
		}
		issueID = id
	} else if pathIssue := chi.URLParam(r, "id"); pathIssue != "" {
		id, parseErr := util.ParseUUID(pathIssue)
		if parseErr != nil {
			writeError(w, 400, "invalid issue_id")
			return
		}
		issueID = id
	}
	// A needs_you ask is actionable for the member who initiated the run.
	recipientID, recipientErr := util.ParseUUID(userID)
	if req.Mode == "needs_you" && recipientErr != nil {
		writeError(w, 400, "invalid recipient")
		return
	}
	title := strings.TrimSpace(req.Title)
	insert := askInsert{Workspace: ws, Issue: issueID, AskerType: actorType, AskerID: askerID, Title: title, Questions: req.Questions, Mode: req.Mode}
	if req.Mode == "needs_you" {
		insert.Recipient = recipientID
		// A card for a person with a Bot binding resumes its asker whoever
		// answers it (the Bot, the app, the CLI), also when the channel is
		// broken or mirrored and they answer in the app.
		if _, chErr := h.Queries.GetHumanChannel(r.Context(), db.GetHumanChannelParams{WorkspaceID: ws, UserID: recipientID}); chErr == nil {
			insert.Kind = "ask"
		}
	}
	out, err := h.insertAsk(r.Context(), insert)
	if err != nil {
		writeError(w, 500, "failed to create ask")
		return
	}
	if req.Mode == "needs_you" {
		h.deliverAskToPerson(r.Context(), out, insert, recipientID, workspaceID, actorID)
	}
	writeJSON(w, http.StatusCreated, out)
}

// deliverAskToPerson writes the in-app row for a needs_you card and hands the
// call to the person's Bot when they have one (DENE-1933). The card itself
// stays the source of truth: the inbox row only stores its id, so every
// surface renders the same card through GET /api/asks.
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

func (h *Handler) ListAsks(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	ws, err := util.ParseUUID(workspaceID)
	if err != nil {
		writeError(w, 400, "invalid workspace_id")
		return
	}
	status := r.URL.Query().Get("status")
	if status == "" {
		status = "open"
	}
	issueFilter := r.URL.Query().Get("issue_id")
	var issueID any
	if issueFilter != "" {
		parsed, parseErr := util.ParseUUID(issueFilter)
		if parseErr != nil {
			writeError(w, 400, "invalid issue_id")
			return
		}
		issueID = parsed
	}
	rows, err := h.DB.Query(r.Context(), `SELECT id,issue_id,asker_type,asker_id,title,questions,answers,mode,status,created_at,answered_at FROM agent_ask WHERE workspace_id=$1 AND ($2='' OR status=$2) AND ($3::uuid IS NULL OR issue_id=$3) AND (chat_session_id IS NULL OR recipient_id=$4) ORDER BY created_at DESC LIMIT 100`, ws, status, issueID, askViewer(r))
	if err != nil {
		writeError(w, 500, "failed to list asks")
		return
	}
	defer rows.Close()
	out := []AskResponse{}
	for rows.Next() {
		var a AskResponse
		var id, issueID, askerID pgtype.UUID
		var qraw, araw []byte
		var answered *time.Time
		if err := rows.Scan(&id, &issueID, &a.AskerType, &askerID, &a.Title, &qraw, &araw, &a.Mode, &a.Status, &a.CreatedAt, &answered); err != nil {
			writeError(w, 500, "failed to read asks")
			return
		}
		a.ID = id.String()
		a.WorkspaceID = workspaceID
		a.AskerID = askerID.String()
		_ = json.Unmarshal(qraw, &a.Questions)
		if len(araw) > 0 {
			_ = json.Unmarshal(araw, &a.Answers)
		}
		if issueID.Valid {
			s := issueID.String()
			a.IssueID = &s
		}
		a.AnsweredAt = answered
		out = append(out, a)
	}
	writeJSON(w, 200, map[string]any{"asks": out})
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
	// A card the platform opened for a person (ask-human, a Bot-routed ask)
	// resumes whoever asked, however it was answered: in the app, with
	// `multica ask answer`, or through their Bot.
	var err error
	switch {
	case kind == service.OwnerConfirmationKind:
		err = h.decideOwnerConfirmation(ctx, in, answers, closeAsk)
	case chatID.Valid:
		err = h.resumeChatAsker(ctx, in, chatID, questions, answers, closeAsk)
	case kind != "" || in.Bot != nil:
		err = h.resumeAskAsker(ctx, in, title, questions, answers, askerType, askerID, issueID, closeAsk)
	default:
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
	return answers, nil
}

// errAskTaken says another caller answered the card first.
var errAskTaken = errors.New("ask is already answered")

// resumeChatAsker writes a chat card's answer back into the chat as the
// person's own message, which queues the asking agent's run, and answers the
// card in the same transaction. Several questions read as one line each; the
// Bot, when one carried it, is named in the message.
func (h *Handler) resumeChatAsker(ctx context.Context, in askAnswer, chatID pgtype.UUID, questions []AskQuestion, answers map[string]string, closeAsk func(*db.Queries) error) error {
	var lines []string
	for i, q := range questions {
		value := answers[fmt.Sprintf("%d", i)]
		for _, o := range q.Options {
			if o.ID == value {
				value = o.Label
				break
			}
		}
		if len(questions) == 1 {
			lines = append(lines, value)
		} else {
			lines = append(lines, fmt.Sprintf("%s：%s", strings.TrimSpace(q.Text), value))
		}
	}
	botName := ""
	if in.Bot != nil {
		botName = in.Bot.Name
	}
	err := h.sendChatAnswer(ctx, in.Workspace, chatID, in.Answerer, botName, strings.Join(lines, "\n"), closeAsk)
	// A chat that was archived or turned into a ticket has nobody left to
	// resume: the card is closed on its own rather than left open for good.
	if errors.Is(err, service.ErrChatSessionArchived) || errors.Is(err, service.ErrChatSessionConverted) {
		slog.Warn("ask: chat is closed, card answered without a message", "ask_id", uuidToString(in.AskID))
		return closeAsk(h.Queries)
	}
	return err
}

// resumeAskAsker records the answer on the ticket as the person's own comment
// and wakes whoever is waiting: the open summon is answered (which also lifts
// an ask-human block and wakes its caller), and an agent that asked without a
// summon is woken directly. The comment is the audit line, "Bot X for owner Y"
// when a Bot carried it, and it commits together with the card's answer, so a
// failed write leaves the card open for the same answer to be given again.
func (h *Handler) resumeAskAsker(ctx context.Context, in askAnswer, title string, questions []AskQuestion, answers map[string]string, askerType string, askerID, issueID pgtype.UUID, closeAsk func(*db.Queries) error) error {
	if !issueID.Valid {
		return closeAsk(h.Queries)
	}
	issue, err := h.Queries.GetIssue(ctx, issueID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && issue.WorkspaceID != in.Workspace) {
		return closeAsk(h.Queries)
	}
	if err != nil {
		return err
	}
	var b strings.Builder
	if in.Bot != nil {
		fmt.Fprintf(&b, "**回答了提问「%s」**（Bot「%s」代表本人转达）", strings.TrimSpace(title), in.Bot.Name)
	} else {
		fmt.Fprintf(&b, "**回答了提问「%s」**", strings.TrimSpace(title))
	}
	for i, q := range questions {
		value := answers[fmt.Sprintf("%d", i)]
		for _, o := range q.Options {
			if o.ID == value {
				value = o.Label
				break
			}
		}
		fmt.Fprintf(&b, "\n- %s：%s", strings.TrimSpace(q.Text), value)
	}
	params := db.CreateCommentParams{
		ID: dbid.NewV7(), IssueID: issue.ID, WorkspaceID: issue.WorkspaceID,
		AuthorType: "member", AuthorID: in.Answerer, Content: b.String(), Type: "comment",
	}
	if in.Bot != nil {
		params.ViaBotID = in.Bot.ID
		params.ViaBotName = pgtype.Text{String: in.Bot.Name, Valid: true}
	}
	var created db.CreateCommentRow
	write := func(q *db.Queries) error {
		if err := closeAsk(q); err != nil {
			return err
		}
		var err error
		created, err = q.CreateComment(ctx, params)
		return err
	}
	if h.TxStarter == nil {
		err = write(h.Queries)
	} else {
		var tx pgx.Tx
		if tx, err = h.TxStarter.Begin(ctx); err == nil {
			defer tx.Rollback(ctx)
			if err = write(h.Queries.WithTx(tx)); err == nil {
				err = tx.Commit(ctx)
			}
		}
	}
	if err != nil {
		return err
	}
	comment := created.Comment()
	h.publishWithSource(ctx, protocol.EventCommentCreated, uuidToString(issue.WorkspaceID), "member", uuidToString(in.Answerer), map[string]any{
		"comment":             commentToResponse(comment, nil, nil),
		"issue_title":         issue.Title,
		"issue_assignee_type": textToPtr(issue.AssigneeType),
		"issue_assignee_id":   uuidToPtr(issue.AssigneeID),
		"issue_status":        issue.Status,
		"issue_revision":      created.IssueRevision,
	})
	woken := h.answerSummons(ctx, issue, comment, "member", uuidToString(in.Answerer), false)
	if !woken.Valid && askerType == "agent" {
		h.triggerWaitingOnAgent(ctx, issue, askerID, comment.ID)
	}
	return nil
}
