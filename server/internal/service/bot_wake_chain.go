package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const DefaultBotWakeChainLimit = 4

func BotWakeChainLimit(settings []byte) int {
	var values map[string]json.RawMessage
	if json.Unmarshal(settings, &values) != nil {
		return DefaultBotWakeChainLimit
	}
	var n int
	if json.Unmarshal(values["bot_wake_chain_limit"], &n) != nil || n < 1 || n > 100 {
		return DefaultBotWakeChainLimit
	}
	return n
}

var ErrBotWakeChainLimit = errors.New("bot_wake_chain_limit")

// A cancelled insert is a durable refusal, never a successful enqueue.
func botWakeTaskError(task db.AgentTaskQueue) error {
	if task.Status == "cancelled" && task.Error.Valid && strings.HasPrefix(task.Error.String, "bot wake chain limit (") {
		return fmt.Errorf("%w: %s", ErrBotWakeChainLimit, task.Error.String)
	}
	if task.Status == "cancelled" && task.Error.String == "bot wake source unavailable" {
		return errors.New(task.Error.String)
	}
	return nil
}

func BotWakeBlocked(task db.AgentTaskQueue) bool {
	return errors.Is(botWakeTaskError(task), ErrBotWakeChainLimit)
}

func BotWakeWarning(task db.AgentTaskQueue, agentName string) string {
	return fmt.Sprintf("Agent %s (%s): Automatic wake blocked after %d unanswered bot wakes. Wait for an agent reply or completed run, or send a human message. A workspace admin can change the limit.", agentName, util.UUIDToString(task.AgentID), task.WakeHop)
}

type botWakeQuoteKey struct{}

// Only the handler, after verifying the original human message, sets this.
func WithBotWakeHumanQuote(ctx context.Context, quote string) context.Context {
	return context.WithValue(ctx, botWakeQuoteKey{}, quote)
}
func botWakeHumanQuote(ctx context.Context) string {
	quote, _ := ctx.Value(botWakeQuoteKey{}).(string)
	return quote
}
