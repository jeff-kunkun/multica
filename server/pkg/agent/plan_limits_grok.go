package agent

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

const (
	grokBillingURL = "https://cli-chat-proxy.grok.com/v1/billing?format=credits"
	grokCreditsURL = "https://grok.com/grok_api_v2.GrokBuildBilling/GetGrokCreditsConfig"

	grokWindowCredits = "credits"
)

// ProbeGrok returns SuperGrok / Grok Build credit usage from local OIDC
// credentials. Missing or expired credentials yield (nil, nil).
func (p PlanQuotaProbe) ProbeGrok(ctx context.Context) (*protocol.PlanLimitsSnapshot, error) {
	token := readGrokAccessToken(p.Home, p.now())
	if token == "" {
		return nil, nil
	}

	billingURL := p.GrokBillingURL
	if billingURL == "" {
		billingURL = grokBillingURL
	}
	if raw, err := p.getJSON(ctx, billingURL, token, map[string]string{
		"x-xai-token-auth": "xai-grok-cli",
		"User-Agent":       "grok-cli",
	}); err == nil {
		if snapshot, err := ParseGrokBillingJSON(raw, p.now()); err == nil && snapshot != nil {
			return snapshot, nil
		}
	}

	creditsURL := p.GrokCreditsURL
	if creditsURL == "" {
		creditsURL = grokCreditsURL
	}
	raw, err := p.doHTTP(ctx, http.MethodPost, creditsURL, token, map[string]string{
		"Content-Type": "application/grpc-web+proto",
		"x-grpc-web":   "1",
		"Origin":       "https://grok.com",
		"Referer":      "https://grok.com/",
		"Accept":       "application/grpc-web+proto",
	}, []byte{0, 0, 0, 0, 0})
	if err != nil {
		return nil, err
	}
	return ParseGrokCreditsProtobuf(raw, p.now()), nil
}

// ParseGrokBillingJSON maps cli-chat-proxy /v1/billing?format=credits onto a
// single credits window. creditUsagePercent is already used %.
func ParseGrokBillingJSON(body []byte, observedAt time.Time) (*protocol.PlanLimitsSnapshot, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	cfg := raw
	if nested, ok := raw["config"]; ok {
		var inner map[string]json.RawMessage
		if json.Unmarshal(nested, &inner) == nil {
			cfg = inner
		}
	}
	used, ok := grokUsedPercentFromJSON(cfg)
	if !ok {
		return nil, nil
	}
	window := protocol.PlanLimitWindow{Name: grokWindowCredits, UsedPercent: &used}
	if resetsAt, ok := grokResetFromJSON(cfg); ok {
		window.ResetsAt = &resetsAt
	}
	if minutes, ok := grokWindowMinutesFromJSON(cfg); ok {
		window.WindowMinutes = &minutes
	}
	return snapshotFromWindows("grok", []protocol.PlanLimitWindow{window}, observedAt), nil
}

func grokUsedPercentFromJSON(cfg map[string]json.RawMessage) (float64, bool) {
	for _, key := range []string{"creditUsagePercent", "credit_usage_percent", "usedPercent", "used_percent"} {
		if msg, ok := cfg[key]; ok {
			var value float64
			if json.Unmarshal(msg, &value) == nil {
				return clampPercent(value), true
			}
		}
	}
	used := jsonNumberish(cfg, "used", "onDemandUsed", "on_demand_used", "totalUsed", "total_used")
	limit := jsonNumberish(cfg, "monthlyLimit", "monthly_limit", "onDemandCap", "on_demand_cap")
	if limit > 0 && used >= 0 {
		return clampPercent(used / limit * 100), true
	}
	return 0, false
}

func grokResetFromJSON(cfg map[string]json.RawMessage) (int64, bool) {
	for _, key := range []string{"billingPeriodEnd", "billing_period_end"} {
		if msg, ok := cfg[key]; ok {
			var value string
			if json.Unmarshal(msg, &value) == nil {
				if ts, ok := parseRFC3339Unix(value); ok {
					return ts, true
				}
			}
			var unix int64
			if json.Unmarshal(msg, &unix) == nil && unix > 0 {
				return unix, true
			}
		}
	}
	if msg, ok := cfg["currentPeriod"]; ok {
		return grokPeriodEnd(msg)
	}
	if msg, ok := cfg["current_period"]; ok {
		return grokPeriodEnd(msg)
	}
	return 0, false
}

func grokPeriodEnd(msg json.RawMessage) (int64, bool) {
	var period struct {
		End   string `json:"end"`
		Type  string `json:"type"`
		EndAt int64  `json:"end_at"`
	}
	if json.Unmarshal(msg, &period) != nil {
		return 0, false
	}
	if ts, ok := parseRFC3339Unix(period.End); ok {
		return ts, true
	}
	if period.EndAt > 0 {
		return period.EndAt, true
	}
	return 0, false
}

func grokWindowMinutesFromJSON(cfg map[string]json.RawMessage) (int64, bool) {
	if msg, ok := cfg["currentPeriod"]; ok {
		return grokPeriodMinutes(msg)
	}
	if msg, ok := cfg["current_period"]; ok {
		return grokPeriodMinutes(msg)
	}
	return 0, false
}

func grokPeriodMinutes(msg json.RawMessage) (int64, bool) {
	var period struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(msg, &period) != nil {
		return 0, false
	}
	switch strings.ToLower(strings.TrimSpace(period.Type)) {
	case "week", "weekly", "7d":
		return sevenDayMinutes, true
	case "month", "monthly":
		return 43_200, true
	default:
		return 0, false
	}
}

func jsonNumberish(cfg map[string]json.RawMessage, keys ...string) float64 {
	for _, key := range keys {
		msg, ok := cfg[key]
		if !ok {
			continue
		}
		var value float64
		if json.Unmarshal(msg, &value) == nil {
			return value
		}
		var wrapped struct {
			Val *float64 `json:"val"`
		}
		if json.Unmarshal(msg, &wrapped) == nil && wrapped.Val != nil {
			return *wrapped.Val
		}
	}
	return 0
}

// ParseGrokCreditsProtobuf recovers used % and reset time from a gRPC-web
// GetGrokCreditsConfig payload without a .proto dependency.
func ParseGrokCreditsProtobuf(body []byte, observedAt time.Time) *protocol.PlanLimitsSnapshot {
	used, resetsAt, ok := parseGrokCreditsFields(stripGRPCWebFrame(body))
	if !ok {
		return nil
	}
	window := protocol.PlanLimitWindow{Name: grokWindowCredits, UsedPercent: &used}
	if resetsAt > 0 {
		window.ResetsAt = &resetsAt
	}
	return snapshotFromWindows("grok", []protocol.PlanLimitWindow{window}, observedAt)
}

func stripGRPCWebFrame(body []byte) []byte {
	if len(body) >= 5 && body[0] == 0 {
		n := int(binary.BigEndian.Uint32(body[1:5]))
		if 5+n <= len(body) {
			return body[5 : 5+n]
		}
	}
	return body
}

func parseGrokCreditsFields(msg []byte) (used float64, resetsAt int64, ok bool) {
	fields := decodeProtoMessage(msg)
	if len(fields) == 0 {
		return 0, 0, false
	}
	root := fields
	if inner, found := protoBytesField(fields, 1); found {
		if nested := decodeProtoMessage(inner); len(nested) > 0 {
			root = nested
		}
	}
	if pct, found := protoFloatField(root, 1); found {
		used = normalizeGrokPercent(pct)
		ok = true
	}
	if !ok {
		if pct, found := firstProtoPercent(root); found {
			used = pct
			ok = true
		}
	}
	if ts, found := protoIntField(root, 5); found && looksLikeUnix(ts) {
		resetsAt = ts
	} else if ts, found := protoIntField(root, 4); found && looksLikeUnix(ts) {
		resetsAt = ts
	} else if ts, found := firstProtoUnix(root); found {
		resetsAt = ts
	}
	return used, resetsAt, ok
}

func normalizeGrokPercent(value float64) float64 {
	if value >= 0 && value <= 1 {
		return clampPercent(value * 100)
	}
	return clampPercent(value)
}

type protoField struct {
	num  int
	wire int
	u64  uint64
	raw  []byte
}

func decodeProtoMessage(msg []byte) []protoField {
	var fields []protoField
	i := 0
	for i < len(msg) {
		key, n := consumeVarint(msg[i:])
		if n == 0 {
			break
		}
		i += n
		num := int(key >> 3)
		wire := int(key & 7)
		field := protoField{num: num, wire: wire}
		switch wire {
		case 0:
			v, m := consumeVarint(msg[i:])
			if m == 0 {
				return fields
			}
			field.u64 = v
			i += m
		case 1:
			if i+8 > len(msg) {
				return fields
			}
			field.u64 = binary.LittleEndian.Uint64(msg[i : i+8])
			field.raw = msg[i : i+8]
			i += 8
		case 2:
			ln, m := consumeVarint(msg[i:])
			if m == 0 {
				return fields
			}
			i += m
			end := i + int(ln)
			if end > len(msg) {
				return fields
			}
			field.raw = msg[i:end]
			i = end
		case 5:
			if i+4 > len(msg) {
				return fields
			}
			field.u64 = uint64(binary.LittleEndian.Uint32(msg[i : i+4]))
			field.raw = msg[i : i+4]
			i += 4
		default:
			return fields
		}
		fields = append(fields, field)
	}
	return fields
}

func consumeVarint(b []byte) (uint64, int) {
	var x uint64
	for i := 0; i < len(b) && i < 10; i++ {
		x |= uint64(b[i]&0x7f) << (7 * i)
		if b[i] < 0x80 {
			return x, i + 1
		}
	}
	return 0, 0
}

func protoBytesField(fields []protoField, num int) ([]byte, bool) {
	for _, field := range fields {
		if field.num == num && field.wire == 2 {
			return field.raw, true
		}
	}
	return nil, false
}

func protoFloatField(fields []protoField, num int) (float64, bool) {
	for _, field := range fields {
		if field.num != num {
			continue
		}
		if field.wire == 1 && len(field.raw) == 8 {
			return math.Float64frombits(field.u64), true
		}
		if field.wire == 5 && len(field.raw) == 4 {
			return float64(math.Float32frombits(uint32(field.u64))), true
		}
	}
	return 0, false
}

func protoIntField(fields []protoField, num int) (int64, bool) {
	for _, field := range fields {
		if field.num == num && field.wire == 0 {
			return int64(field.u64), true
		}
	}
	return 0, false
}

func firstProtoPercent(fields []protoField) (float64, bool) {
	for _, field := range fields {
		if field.wire == 1 && len(field.raw) == 8 {
			value := math.Float64frombits(field.u64)
			if value >= 0 && value <= 100 {
				return normalizeGrokPercent(value), true
			}
		}
		if field.wire == 5 && len(field.raw) == 4 {
			value := float64(math.Float32frombits(uint32(field.u64)))
			if value >= 0 && value <= 100 {
				return normalizeGrokPercent(value), true
			}
		}
	}
	return 0, false
}

func firstProtoUnix(fields []protoField) (int64, bool) {
	var latest int64
	found := false
	for _, field := range fields {
		if field.wire != 0 {
			continue
		}
		ts := int64(field.u64)
		if looksLikeUnix(ts) && ts >= latest {
			latest = ts
			found = true
		}
	}
	return latest, found
}

func looksLikeUnix(ts int64) bool {
	return ts > 1_000_000_000 && ts < 4_000_000_000
}

func readGrokAccessToken(home string, now time.Time) string {
	path := filepath.Join(grokHomeDir(home), "auth.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return parseGrokAuthJSON(string(raw), now)
}

func parseGrokAuthJSON(content string, now time.Time) string {
	var parsed map[string]json.RawMessage
	if err := json.Unmarshal([]byte(content), &parsed); err != nil {
		return ""
	}
	type candidate struct {
		token     string
		expiresAt time.Time
	}
	var best candidate
	for _, msg := range parsed {
		var entry struct {
			Key         string `json:"key"`
			AccessToken string `json:"access_token"`
			AuthMode    string `json:"auth_mode"`
			ExpiresAt   string `json:"expires_at"`
		}
		if json.Unmarshal(msg, &entry) != nil {
			continue
		}
		if entry.AuthMode != "" && !strings.EqualFold(entry.AuthMode, "oidc") {
			continue
		}
		token := strings.TrimSpace(entry.AccessToken)
		if token == "" {
			token = strings.TrimSpace(entry.Key)
		}
		if token == "" {
			continue
		}
		expiresAt := parseFlexibleTime(entry.ExpiresAt)
		if !expiresAt.IsZero() && !expiresAt.After(now.Add(-30*time.Second)) {
			continue
		}
		if best.token == "" || expiresAt.After(best.expiresAt) {
			best = candidate{token: token, expiresAt: expiresAt}
		}
	}
	return best.token
}

func grokHomeDir(home string) string {
	if dir := strings.TrimSpace(os.Getenv("GROK_HOME")); dir != "" {
		return dir
	}
	return filepath.Join(homeDir(home), ".grok")
}
