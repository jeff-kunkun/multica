package agent

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

const (
	deepseekBalanceURL = "https://api.deepseek.com/user/balance"

	windowBalanceCNY = "balance_cny"
	windowBalanceUSD = "balance_usd"
)

var (
	deepseekAPIKeyEnvs  = []string{"DEEPSEEK_API_KEY"}
	deepseekAPIKeyFiles = []string{".deepseek/credentials.json", ".dsh/credentials.json"}
)

// ProbeDeepSeek returns prepaid CNY/USD remaining balances. Missing
// credentials yield (nil, nil).
func (p PlanQuotaProbe) ProbeDeepSeek(ctx context.Context) (*protocol.PlanLimitsSnapshot, error) {
	key := p.apiKey("deepseek", deepseekAPIKeyEnvs, deepseekAPIKeyFiles)
	if key == "" {
		return nil, nil
	}
	url := p.DeepSeekBalanceURL
	if url == "" {
		url = deepseekBalanceURL
	}
	body, err := p.getJSON(ctx, url, key, nil)
	if err != nil {
		return nil, err
	}
	return ParseDeepSeekBalanceJSON(body, p.now())
}

// ParseDeepSeekBalanceJSON maps GET /user/balance onto balance_cny /
// balance_usd remaining windows. is_available=false with no remaining
// amount is exhausted.
func ParseDeepSeekBalanceJSON(body []byte, observedAt time.Time) (*protocol.PlanLimitsSnapshot, error) {
	var raw struct {
		IsAvailable  *bool `json:"is_available"`
		BalanceInfos []struct {
			Currency     string          `json:"currency"`
			TotalBalance json.RawMessage `json:"total_balance"`
		} `json:"balance_infos"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	windows := make([]protocol.PlanLimitWindow, 0, 2)
	seen := map[string]struct{}{}
	for _, info := range raw.BalanceInfos {
		name := balanceWindowName(info.Currency)
		if name == "" {
			continue
		}
		if _, exists := seen[name]; exists {
			continue
		}
		amount, ok := jsonFloat(info.TotalBalance)
		if !ok {
			continue
		}
		if amount < 0 {
			amount = 0
		}
		remaining := amount
		seen[name] = struct{}{}
		windows = append(windows, protocol.PlanLimitWindow{Name: name, Remaining: &remaining})
	}
	snapshot := snapshotFromWindows("dsh", windows, observedAt)
	if snapshot == nil && raw.IsAvailable != nil && !*raw.IsAvailable {
		return &protocol.PlanLimitsSnapshot{
			Provider:   "dsh",
			Status:     protocol.PlanLimitsStatusExhausted,
			ObservedAt: observedAt.Unix(),
		}, nil
	}
	if snapshot != nil && raw.IsAvailable != nil && !*raw.IsAvailable {
		snapshot.Status = protocol.PlanLimitsStatusExhausted
	}
	return snapshot, nil
}

func balanceWindowName(currency string) string {
	switch strings.ToUpper(strings.TrimSpace(currency)) {
	case "CNY":
		return windowBalanceCNY
	case "USD":
		return windowBalanceUSD
	default:
		return ""
	}
}
