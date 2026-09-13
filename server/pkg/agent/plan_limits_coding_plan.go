package agent

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

const (
	kimiUsagesURL     = "https://api.kimi.com/coding/v1/usages"
	glmQuotaURL       = "https://open.bigmodel.cn/api/monitor/usage/quota/limit"
	minimaxRemainsURL = "https://api.minimaxi.com/v1/api/openplatform/coding_plan/remains"

	windowFiveHour = "five_hour"
	windowSevenDay = "seven_day"

	minimaxWeeklyUnlimited = 3
)

var (
	kimiAPIKeyEnvs     = []string{"KIMI_CODING_API_KEY", "KIMI_API_KEY"}
	glmAPIKeyEnvs      = []string{"ZHIPUAI_API_KEY", "ZHIPU_API_KEY", "BIGMODEL_API_KEY", "GLM_API_KEY", "ZAI_API_KEY"}
	minimaxAPIKeyEnvs  = []string{"MINIMAX_API_KEY", "MINIMAXI_API_KEY"}
	kimiAPIKeyFiles    = []string{".kimi-code/credentials.json", ".kimi/credentials.json", ".kimi-cli/auth.json"}
	glmAPIKeyFiles     = []string{".zhipuai/api_key", ".zhipuai/credentials.json"}
	minimaxAPIKeyFiles = []string{".minimax/credentials.json", ".minimaxi/credentials.json"}
)

// ProbeKimi returns Kimi For Coding 5h/7d windows. Missing credentials
// yield (nil, nil).
func (p PlanQuotaProbe) ProbeKimi(ctx context.Context) (*protocol.PlanLimitsSnapshot, error) {
	key := p.apiKey("kimi", kimiAPIKeyEnvs, kimiAPIKeyFiles)
	if key == "" {
		return nil, nil
	}
	url := p.KimiUsagesURL
	if url == "" {
		url = kimiUsagesURL
	}
	body, err := p.getJSON(ctx, url, key, nil)
	if err != nil {
		return nil, err
	}
	return ParseKimiCodingUsageJSON(body, p.now())
}

// ProbeGLM returns Zhipu GLM coding-plan 5h/7d windows. Authorization is the
// raw API key with no Bearer prefix. Missing credentials yield (nil, nil).
func (p PlanQuotaProbe) ProbeGLM(ctx context.Context) (*protocol.PlanLimitsSnapshot, error) {
	key := p.apiKey("glm", glmAPIKeyEnvs, glmAPIKeyFiles)
	if key == "" {
		return nil, nil
	}
	url := p.GLMQuotaURL
	if url == "" {
		url = glmQuotaURL
	}
	body, err := p.getJSON(ctx, url, "", map[string]string{
		"Authorization":   key,
		"Accept-Language": "en-US,en",
	})
	if err != nil {
		return nil, err
	}
	return ParseGLMQuotaJSON(body, p.now())
}

// ProbeMiniMax returns MiniMax coding-plan remaining percentages inverted
// into used %. Missing credentials yield (nil, nil).
func (p PlanQuotaProbe) ProbeMiniMax(ctx context.Context) (*protocol.PlanLimitsSnapshot, error) {
	key := p.apiKey("minimax", minimaxAPIKeyEnvs, minimaxAPIKeyFiles)
	if key == "" {
		return nil, nil
	}
	url := p.MiniMaxRemainsURL
	if url == "" {
		url = minimaxRemainsURL
	}
	body, err := p.getJSON(ctx, url, key, nil)
	if err != nil {
		return nil, err
	}
	return ParseMiniMaxRemainsJSON(body, p.now())
}

// ParseKimiCodingUsageJSON maps api.kimi.com/coding/v1/usages onto five_hour
// (limits[].detail) and seven_day (top-level usage) windows. limit/used/
// remaining may be numbers or strings.
func ParseKimiCodingUsageJSON(body []byte, observedAt time.Time) (*protocol.PlanLimitsSnapshot, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	windows := make([]protocol.PlanLimitWindow, 0, 2)
	if msg, ok := raw["limits"]; ok {
		var limits []map[string]json.RawMessage
		if json.Unmarshal(msg, &limits) == nil {
			for _, item := range limits {
				detail := item
				if nested, ok := item["detail"]; ok {
					var inner map[string]json.RawMessage
					if json.Unmarshal(nested, &inner) == nil {
						detail = inner
					}
				}
				if window, ok := codingPlanWindow(windowFiveHour, fiveHourMinutes, detail); ok {
					windows = append(windows, window)
					break
				}
			}
		}
	}
	if msg, ok := raw["usage"]; ok {
		var usage map[string]json.RawMessage
		if json.Unmarshal(msg, &usage) == nil {
			if window, ok := codingPlanWindow(windowSevenDay, sevenDayMinutes, usage); ok {
				windows = append(windows, window)
			}
		}
	}
	return snapshotFromWindows("kimi", windows, observedAt), nil
}

// ParseGLMQuotaJSON maps Zhipu /api/monitor/usage/quota/limit TOKENS_LIMIT
// (and CREDIT_LIMIT) rows onto five_hour / seven_day. TIME_LIMIT MCP rows
// are ignored. Percentage is already used %.
func ParseGLMQuotaJSON(body []byte, observedAt time.Time) (*protocol.PlanLimitsSnapshot, error) {
	limits, err := extractGLMLimits(body)
	if err != nil {
		return nil, err
	}
	tokenRows := make([]glmLimitRow, 0, 2)
	for _, row := range limits {
		kind := strings.ToUpper(strings.TrimSpace(row.Type))
		if kind != "TOKENS_LIMIT" && kind != "CREDIT_LIMIT" {
			continue
		}
		tokenRows = append(tokenRows, row)
	}
	assigned := map[string]protocol.PlanLimitWindow{}
	unassigned := make([]glmLimitRow, 0, len(tokenRows))
	for _, row := range tokenRows {
		name := glmWindowName(row)
		if name == "" {
			unassigned = append(unassigned, row)
			continue
		}
		if _, exists := assigned[name]; exists {
			continue
		}
		if window, ok := glmLimitWindow(name, row); ok {
			assigned[name] = window
		}
	}
	for _, row := range unassigned {
		name := windowFiveHour
		if _, exists := assigned[windowFiveHour]; exists {
			name = windowSevenDay
		}
		if _, exists := assigned[name]; exists {
			continue
		}
		if window, ok := glmLimitWindow(name, row); ok {
			assigned[name] = window
		}
	}
	windows := make([]protocol.PlanLimitWindow, 0, 2)
	for _, name := range []string{windowFiveHour, windowSevenDay} {
		if window, ok := assigned[name]; ok {
			windows = append(windows, window)
		}
	}
	return snapshotFromWindows("glm", windows, observedAt), nil
}

type glmLimitRow struct {
	Type          string          `json:"type"`
	Unit          int             `json:"unit"`
	Number        int             `json:"number"`
	Percentage    *float64        `json:"percentage"`
	NextResetTime json.RawMessage `json:"nextResetTime"`
}

func extractGLMLimits(body []byte) ([]glmLimitRow, error) {
	var wrapped struct {
		Data *struct {
			Limits []glmLimitRow `json:"limits"`
		} `json:"data"`
		Limits []glmLimitRow `json:"limits"`
	}
	if err := json.Unmarshal(body, &wrapped); err != nil {
		return nil, err
	}
	if wrapped.Data != nil && len(wrapped.Data.Limits) > 0 {
		return wrapped.Data.Limits, nil
	}
	return wrapped.Limits, nil
}

func glmWindowName(row glmLimitRow) string {
	if row.Unit == 3 && row.Number == 5 {
		return windowFiveHour
	}
	if row.Unit == 6 && row.Number == 1 {
		return windowSevenDay
	}
	return ""
}

func glmLimitWindow(name string, row glmLimitRow) (protocol.PlanLimitWindow, bool) {
	if row.Percentage == nil {
		return protocol.PlanLimitWindow{}, false
	}
	used := clampPercent(*row.Percentage)
	minutes := fiveHourMinutes
	if name == windowSevenDay {
		minutes = sevenDayMinutes
	}
	window := protocol.PlanLimitWindow{Name: name, UsedPercent: &used, WindowMinutes: &minutes}
	if resetsAt, ok := parseFlexibleReset(row.NextResetTime); ok {
		window.ResetsAt = &resetsAt
	}
	return window, true
}

// ParseMiniMaxRemainsJSON maps coding_plan/remains general-model remaining
// percents onto used %. Weekly status 3 means unlimited and is stored as 0%.
func ParseMiniMaxRemainsJSON(body []byte, observedAt time.Time) (*protocol.PlanLimitsSnapshot, error) {
	var raw struct {
		BaseResp *struct {
			StatusCode int `json:"status_code"`
		} `json:"base_resp"`
		ModelRemains []minimaxRemain `json:"model_remains"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	if raw.BaseResp != nil && raw.BaseResp.StatusCode != 0 {
		return nil, nil
	}
	var general *minimaxRemain
	for i := range raw.ModelRemains {
		if strings.EqualFold(strings.TrimSpace(raw.ModelRemains[i].ModelName), "general") {
			general = &raw.ModelRemains[i]
			break
		}
	}
	if general == nil && len(raw.ModelRemains) == 1 {
		general = &raw.ModelRemains[0]
	}
	if general == nil {
		return nil, nil
	}
	windows := make([]protocol.PlanLimitWindow, 0, 2)
	if general.IntervalRemaining != nil {
		used := clampPercent(100 - *general.IntervalRemaining)
		minutes := fiveHourMinutes
		windows = append(windows, protocol.PlanLimitWindow{
			Name:          windowFiveHour,
			UsedPercent:   &used,
			WindowMinutes: &minutes,
		})
	}
	if general.WeeklyStatus != nil && *general.WeeklyStatus == minimaxWeeklyUnlimited {
		used := 0.0
		minutes := sevenDayMinutes
		windows = append(windows, protocol.PlanLimitWindow{
			Name:          windowSevenDay,
			UsedPercent:   &used,
			WindowMinutes: &minutes,
		})
	} else if general.WeeklyRemaining != nil {
		used := clampPercent(100 - *general.WeeklyRemaining)
		minutes := sevenDayMinutes
		windows = append(windows, protocol.PlanLimitWindow{
			Name:          windowSevenDay,
			UsedPercent:   &used,
			WindowMinutes: &minutes,
		})
	}
	return snapshotFromWindows("minimax", windows, observedAt), nil
}

type minimaxRemain struct {
	ModelName         string   `json:"model_name"`
	IntervalRemaining *float64 `json:"current_interval_remaining_percent"`
	WeeklyRemaining   *float64 `json:"current_weekly_remaining_percent"`
	WeeklyStatus      *int     `json:"current_weekly_status"`
}

func codingPlanWindow(name string, minutes int64, fields map[string]json.RawMessage) (protocol.PlanLimitWindow, bool) {
	limit, hasLimit := jsonFloat(fields["limit"])
	remaining, hasRemaining := jsonFloat(fields["remaining"])
	usedRaw, hasUsed := jsonFloat(fields["used"])
	var used float64
	switch {
	case hasLimit && limit > 0 && hasRemaining:
		used = clampPercent((limit - remaining) / limit * 100)
	case hasLimit && limit > 0 && hasUsed:
		used = clampPercent(usedRaw / limit * 100)
	case hasUsed && hasRemaining && usedRaw+remaining > 0:
		used = clampPercent(usedRaw / (usedRaw + remaining) * 100)
	default:
		return protocol.PlanLimitWindow{}, false
	}
	window := protocol.PlanLimitWindow{Name: name, UsedPercent: &used, WindowMinutes: &minutes}
	for _, key := range []string{"resetTime", "reset_time", "resets_at"} {
		if msg, ok := fields[key]; ok {
			if resetsAt, ok := parseFlexibleReset(msg); ok {
				window.ResetsAt = &resetsAt
				break
			}
		}
	}
	return window, true
}
