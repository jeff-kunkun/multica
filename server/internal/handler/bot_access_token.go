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
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/botonboarding"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// External bot keys (DENE-1788). A workspace admin adds an external bot
// (Grok and the like) and gets a key with the admin's own power: the key is a
// mul_ personal access token marked kind = 'bot'. There is no separate
// permission system — Kun's call was "everything open". The routes sit behind
// owner/admin and RequireHumanActor, so neither a member nor an agent's task
// token can mint one.

// botTypes is the closed list of bot kinds the page offers.
var botTypes = map[string]bool{"grok": true}

const botNameMaxRunes = 60

// botKeyInstallCommand installs this fork's CLI; Homebrew only carries the
// upstream build (CLI_INSTALL.md).
const botKeyInstallCommand = "curl -fsSL https://raw.githubusercontent.com/jeff-kunkun/multica/kun/scripts/install.sh | bash"

type BotResponse struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	Type       string  `json:"type"`
	Status     string  `json:"status"`
	Prefix     string  `json:"token_prefix"`
	ExpiresAt  *string `json:"expires_at"`
	LastUsedAt *string `json:"last_used_at"`
	CreatedAt  string  `json:"created_at"`
	StoppedAt  *string `json:"stopped_at"`
}

// BotKeyResponse is returned when a key is minted (create or new key). The
// raw token and the setup text carrying it are shown this one time only.
type BotKeyResponse struct {
	Bot       BotResponse `json:"bot"`
	Token     string      `json:"token"`
	SetupText string      `json:"setup_text"`
}

type CreateBotRequest struct {
	Name          string `json:"name"`
	Type          string `json:"type"`
	ExpiresInDays *int   `json:"expires_in_days"`
}

type RotateBotRequest struct {
	ExpiresInDays *int `json:"expires_in_days"`
}

func botToResponse(pat db.PersonalAccessToken, now time.Time) BotResponse {
	status := "active"
	switch {
	case pat.Revoked:
		status = "stopped"
	case pat.ExpiresAt.Valid && !pat.ExpiresAt.Time.After(now):
		status = "expired"
	}
	botType := "grok"
	if pat.BotType.Valid && pat.BotType.String != "" {
		botType = pat.BotType.String
	}
	return BotResponse{
		ID:         uuidToString(pat.ID),
		Name:       pat.Name,
		Type:       botType,
		Status:     status,
		Prefix:     pat.TokenPrefix,
		ExpiresAt:  timestampToPtr(pat.ExpiresAt),
		LastUsedAt: timestampToPtr(pat.LastUsedAt),
		CreatedAt:  timestampToString(pat.CreatedAt),
		StoppedAt:  timestampToPtr(pat.RevokedAt),
	}
}

// botExpiry turns the optional lifetime into an expires_at; nil or <= 0 means
// the key never expires (the page default).
func botExpiry(days *int, now time.Time) pgtype.Timestamptz {
	if days == nil || *days <= 0 {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: now.Add(time.Duration(*days) * 24 * time.Hour), Valid: true}
}

func mintBotToken() (raw, hash, prefix string, err error) {
	raw, err = auth.GeneratePATToken()
	if err != nil {
		return "", "", "", err
	}
	prefix = raw
	if len(prefix) > 12 {
		prefix = prefix[:12]
	}
	return raw, auth.HashToken(raw), prefix, nil
}

func (h *Handler) ListBots(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "workspace id")
	if !ok {
		return
	}
	rows, err := h.Queries.ListBotAccessTokens(r.Context(), db.ListBotAccessTokensParams{
		WorkspaceID: wsUUID,
		UserID:      parseUUID(userID),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list bots")
		return
	}
	now := time.Now()
	resp := make([]BotResponse, len(rows))
	for i, row := range rows {
		resp[i] = botToResponse(row, now)
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) CreateBot(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "workspace id")
	if !ok {
		return
	}
	var req CreateBotRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if utf8.RuneCountInString(name) > botNameMaxRunes {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("name must be at most %d characters", botNameMaxRunes))
		return
	}
	botType := strings.ToLower(strings.TrimSpace(req.Type))
	if botType == "" {
		botType = "grok"
	}
	if !botTypes[botType] {
		writeError(w, http.StatusBadRequest, "type must be grok")
		return
	}

	raw, hash, prefix, err := mintBotToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to generate key")
		return
	}
	now := time.Now()
	row, err := h.Queries.CreateBotAccessToken(r.Context(), db.CreateBotAccessTokenParams{
		UserID:      parseUUID(userID),
		Name:        name,
		TokenHash:   hash,
		TokenPrefix: prefix,
		ExpiresAt:   botExpiry(req.ExpiresInDays, now),
		BotType:     pgtype.Text{String: botType, Valid: true},
		WorkspaceID: wsUUID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to add bot")
		return
	}
	writeJSON(w, http.StatusCreated, BotKeyResponse{
		Bot:       botToResponse(row, now),
		Token:     raw,
		SetupText: h.botSetupText(r.Context(), wsUUID, name, raw, &row.ExpiresAt),
	})
}

// RotateBotKey gives a bot a new key. The old key stops working at once; a
// stopped bot comes back with the new key.
func (h *Handler) RotateBotKey(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "workspace id")
	if !ok {
		return
	}
	botUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "botId"), "bot id")
	if !ok {
		return
	}
	var req RotateBotRequest
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
	}
	current, err := h.Queries.GetBotAccessToken(r.Context(), db.GetBotAccessTokenParams{
		ID: botUUID, WorkspaceID: wsUUID, UserID: parseUUID(userID),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "bot not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load bot")
		return
	}
	raw, hash, prefix, err := mintBotToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to generate key")
		return
	}
	now := time.Now()
	// Without an explicit lifetime the new key keeps the old one's kind:
	// never-expiring stays never, a dated key gets a fresh year.
	expiresAt := botExpiry(req.ExpiresInDays, now)
	if req.ExpiresInDays == nil && current.ExpiresAt.Valid {
		year := 365
		expiresAt = botExpiry(&year, now)
	}
	row, err := h.Queries.RotateBotAccessToken(r.Context(), db.RotateBotAccessTokenParams{
		ID:          botUUID,
		WorkspaceID: wsUUID,
		UserID:      parseUUID(userID),
		TokenHash:   hash,
		TokenPrefix: prefix,
		ExpiresAt:   expiresAt,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "bot not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to issue a new key")
		return
	}
	h.PATCache.Invalidate(r.Context(), current.TokenHash)
	writeJSON(w, http.StatusOK, BotKeyResponse{
		Bot:       botToResponse(row, now),
		Token:     raw,
		SetupText: h.botSetupText(r.Context(), wsUUID, row.Name, raw, &row.ExpiresAt),
	})
}

// StopBot revokes the bot's key. The auth cache entry is dropped in the same
// request so the bot's next call already gets 401.
func (h *Handler) StopBot(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "workspace id")
	if !ok {
		return
	}
	botUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "botId"), "bot id")
	if !ok {
		return
	}
	row, err := h.Queries.StopBotAccessToken(r.Context(), db.StopBotAccessTokenParams{
		ID: botUUID, WorkspaceID: wsUUID, UserID: parseUUID(userID),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "bot not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to stop bot")
		return
	}
	h.PATCache.Invalidate(r.Context(), row.TokenHash)
	writeJSON(w, http.StatusOK, botToResponse(row, time.Now()))
}

// DeleteBot tombstones a stopped bot, preserving historical provenance and
// wake-event foreign keys while hiding it from the directory. A running bot must be
// stopped first so a delete never silently cuts a bot off.
func (h *Handler) DeleteBot(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "workspace id")
	if !ok {
		return
	}
	botUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "botId"), "bot id")
	if !ok {
		return
	}
	n, err := h.Queries.DeleteStoppedBotAccessToken(r.Context(), db.DeleteStoppedBotAccessTokenParams{
		ID: botUUID, WorkspaceID: wsUUID, UserID: parseUUID(userID),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete bot")
		return
	}
	if n == 0 {
		if _, getErr := h.Queries.GetBotAccessToken(r.Context(), db.GetBotAccessTokenParams{
			ID: botUUID, WorkspaceID: wsUUID, UserID: parseUUID(userID),
		}); getErr == nil {
			writeError(w, http.StatusConflict, "stop the bot before deleting it")
			return
		}
		writeError(w, http.StatusNotFound, "bot not found")
		return
	}
	// Named receivers hold sealed push keys; a deleted Bot keeps none.
	if err := h.Queries.DeleteBotWebhookReceiversByBot(r.Context(), botUUID); err != nil {
		slog.Warn("bot delete: receiver cleanup failed", "bot_id", uuidToString(botUUID), "error", err)
	}
	w.WriteHeader(http.StatusNoContent)
}

// BotSetupTextPlaceholder stands in for the key in the setup text handed out
// without minting one (settings page copy, `multica bot guide`).
const BotSetupTextPlaceholder = "<钥匙>"

// BotSetupTextResponse is the setup text without a key.
type BotSetupTextResponse struct {
	SetupText string `json:"setup_text"`
}

// GetBotSetupText returns the same setup text a new key ships with, with the
// key replaced by a placeholder, so the settings page and the CLI can hand a
// bot the onboarding again without issuing a new key. ?bot=<id> names the bot.
func (h *Handler) GetBotSetupText(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "workspace id")
	if !ok {
		return
	}
	botName := strings.TrimSpace(r.URL.Query().Get("name"))
	// A named Bot's copy states its key's lifetime, so it matches the text
	// handed out when the key was minted.
	var expiry *pgtype.Timestamptz
	if botID := strings.TrimSpace(r.URL.Query().Get("bot")); botID != "" {
		botUUID, ok := parseUUIDOrBadRequest(w, botID, "bot id")
		if !ok {
			return
		}
		pat, err := h.Queries.GetBotAccessToken(r.Context(), db.GetBotAccessTokenParams{ID: botUUID, WorkspaceID: wsUUID, UserID: parseUUID(userID)})
		if err != nil {
			writeError(w, http.StatusNotFound, "bot not found")
			return
		}
		botName = pat.Name
		expiry = &pat.ExpiresAt
	}
	if botName == "" {
		botName = "外部 Bot"
	}
	writeJSON(w, http.StatusOK, BotSetupTextResponse{SetupText: h.botSetupText(r.Context(), wsUUID, botName, BotSetupTextPlaceholder, expiry)})
}

// botSetupText is the message the admin pastes to the bot. It is rendered
// here so the settings page and `multica bot create` hand out the same text.
// expiry is the key's lifetime; nil when no Bot is named yet, so the text
// says nothing about it.
func (h *Handler) botSetupText(ctx context.Context, wsUUID pgtype.UUID, botName, token string, expiry *pgtype.Timestamptz) string {
	wsName := ""
	if ws, err := h.Queries.GetWorkspace(ctx, wsUUID); err == nil {
		wsName = ws.Name
	}
	var domains []botSetupDomain
	if rows, err := h.Queries.ListBotSetupDomains(ctx, wsUUID); err == nil {
		for _, row := range rows {
			if n := len(domains); n > 0 && domains[n-1].Name == row.DomainName {
				if row.AgentName != "" {
					domains[n-1].Agents = append(domains[n-1].Agents, row.AgentName)
				}
				continue
			}
			d := botSetupDomain{Name: row.DomainName}
			if row.AgentName != "" {
				d.Agents = []string{row.AgentName}
			}
			domains = append(domains, d)
		}
	}
	serverURL, appURL := daemonSetupURLsFromEnv()
	return renderBotSetupText(botSetupInput{
		BotName:       botName,
		WorkspaceID:   uuidToString(wsUUID),
		WorkspaceName: wsName,
		ServerURL:     serverURL,
		AppURL:        appURL,
		Token:         token,
		KeyExpiry:     expiry,
		Domains:       domains,
	})
}

type botSetupDomain struct {
	Name   string
	Agents []string
}

type botSetupInput struct {
	BotName       string
	WorkspaceID   string
	WorkspaceName string
	ServerURL     string
	AppURL        string
	Token         string
	// KeyExpiry: nil = unknown (no key minted), invalid = never expires.
	KeyExpiry *pgtype.Timestamptz
	Domains   []botSetupDomain
}

func renderBotSetupText(in botSetupInput) string {
	var b strings.Builder
	ws := in.WorkspaceName
	if ws == "" {
		ws = in.WorkspaceID
	}
	fmt.Fprintf(&b, "你是「%s」，从现在起接入 Multica 工作区「%s」，权限和添加你的管理员相同。\n\n", in.BotName, ws)

	// Network first (kun-main #693): an office on the admin's own Mac is
	// unreachable from a cloud Bot, and copying its address fails silently.
	b.WriteString("零、先确认网络通\n")
	if in.ServerURL != "" {
		fmt.Fprintf(&b, "- 先从你自己运行的地方访问 %s/health，能打通再往下做。\n", strings.TrimRight(in.ServerURL, "/"))
	} else {
		b.WriteString("- 先从你自己运行的地方访问办公室地址的 /health，能打通再往下做。\n")
	}
	b.WriteString("- 地址如果是 localhost、127.0.0.1 或 192.168./10. 这类内网地址，说明办公室开在管理员的 Mac 上，你直接连不到：不要照抄，先和那台 Mac 进同一个私有组网（如 Tailscale），把地址换成它在组网里的地址；做不到就停下来问管理员要公网 HTTPS 地址。\n\n")

	b.WriteString("一、安装 Multica 命令行（macOS / Linux）\n")
	b.WriteString(botKeyInstallCommand + "\n\n")

	b.WriteString("二、登录（这把钥匙只发这一次，存好，不要贴到别处）\n")
	if in.ServerURL != "" {
		fmt.Fprintf(&b, "multica config set server_url %s\n", in.ServerURL)
	}
	if in.AppURL != "" {
		fmt.Fprintf(&b, "multica config set app_url %s\n", in.AppURL)
	}
	fmt.Fprintf(&b, "multica login --token %s\n", in.Token)
	fmt.Fprintf(&b, "multica workspace switch %s\n", in.WorkspaceID)
	b.WriteString("multica issue list --limit 5   # 能列出任务就接好了\n")
	if in.KeyExpiry != nil {
		if in.KeyExpiry.Valid {
			fmt.Fprintf(&b, "- 这把钥匙 %s 过期，到期前找管理员换新的。\n", in.KeyExpiry.Time.Format("2006-01-02"))
		} else {
			b.WriteString("- 这把钥匙永不过期，不用定期续；只有管理员停用或重新生成时才需要换。\n")
		}
	}
	b.WriteString("- 推送地址：你有办公室能访问到的公网 HTTPS 接收地址，就带这把钥匙调用 PUT <server_url>/api/bot/webhook 登记（只做一次），body 为 {\"url\": \"<接收地址>\", \"key\": \"<接收端要的 Authorization 值>\", \"headers\": {<可选的其他请求头>}}；url 传空字符串就是断开。几位经理共用你时，各自调用 PUT <server_url>/api/bot/webhooks 并带上 \"name\"，每人一个接收地址，互不覆盖。\n\n")

	b.WriteString("三、上手（这段说明由系统生成，命令清单看 multica capabilities，不用背）\n")
	b.WriteString(botonboarding.Render())
	b.WriteString("\n")

	if len(in.Domains) > 0 {
		b.WriteString("四、按方向派活（建任务时带 --domain <方向>，服务端会派给对应的智能体）\n")
		for _, d := range in.Domains {
			if len(d.Agents) > 0 {
				fmt.Fprintf(&b, "- %s → %s\n", d.Name, strings.Join(d.Agents, "、"))
			} else {
				fmt.Fprintf(&b, "- %s\n", d.Name)
			}
		}
		b.WriteString("\n五、规矩\n")
	} else {
		b.WriteString("四、规矩\n")
	}
	b.WriteString("- 发帖、发邮件、付款、对外回复这类对外动作，先写成草稿发给管理员确认，确认后再执行。\n")
	b.WriteString("- 拿不准的事在任务里评论提问，不要猜。\n")
	b.WriteString("- 钥匙失效（命令报 401）说明被停用了，停下来找管理员要新钥匙。\n")
	return b.String()
}
