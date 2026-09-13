package agent

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

const (
	grokOIDCScopePrefix    = "https://auth.x.ai::"
	grokLegacySessionScope = "https://accounts.x.ai/sign-in"
	grokWindowCredits      = "credits"
	grokEmptyGRPCWebFrame  = "\x00\x00\x00\x00\x00"
)

// ProbeGrok returns SuperGrok credit usage from grok.com's billing RPC, or
// (nil, nil) when ~/.grok/auth.json has no usable OIDC bearer. The response
// has no published .proto; credit percent and reset are recovered with a
// bounded protobuf scan.
func (p PlanQuotaProbe) ProbeGrok(ctx context.Context) (*protocol.PlanLimitsSnapshot, error) {
	token := readGrokAccessToken(p.Home)
	if token == "" {
		return nil, nil
	}
	url := p.GrokBillingURL
	if url == "" {
		url = grokBillingURL
	}
	body, err := p.postBytes(ctx, url, token, "application/grpc-web+proto", []byte(grokEmptyGRPCWebFrame), map[string]string{
		"Origin":       "https://grok.com",
		"Referer":      "https://grok.com/?_s=usage",
		"Accept":       "*/*",
		"x-grpc-web":   "1",
		"x-user-agent": "connect-es/2.1.1",
		"User-Agent":   "multica-daemon",
	})
	if err != nil {
		return nil, err
	}
	return ParseGrokCreditsProtobuf(body, p.now())
}

func readGrokAccessToken(home string) string {
	path := filepath.Join(grokHomeDir(home), "auth.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return parseGrokAuthJSON(string(raw))
}

func grokHomeDir(home string) string {
	if dir := strings.TrimSpace(os.Getenv("GROK_HOME")); dir != "" {
		return dir
	}
	return filepath.Join(homeDir(home), ".grok")
}

// parseGrokAuthJSON prefers SuperGrok OIDC entries over the legacy session
// scope. An expired token is still returned so a small clock skew can succeed.
func parseGrokAuthJSON(content string) string {
	var root map[string]json.RawMessage
	if err := json.Unmarshal([]byte(content), &root); err != nil {
		return ""
	}
	var oidc, legacy string
	for scope, raw := range root {
		token := grokEntryToken(raw)
		if token == "" {
			continue
		}
		switch {
		case strings.HasPrefix(scope, grokOIDCScopePrefix):
			oidc = token
		case scope == grokLegacySessionScope || strings.Contains(scope, "/sign-in"):
			legacy = token
		}
	}
	if oidc != "" {
		return oidc
	}
	return legacy
}

func grokEntryToken(raw json.RawMessage) string {
	var entry struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(raw, &entry); err != nil {
		return ""
	}
	return strings.TrimSpace(entry.Key)
}

type grokBillingSnapshot struct {
	usedPercent float64
	resetsAt    *int64
}

type grokProtobufScan struct {
	fixed32 []grokFixed32Field
	varints []grokVarintField
}

type grokFixed32Field struct {
	path  []uint64
	value float32
	order int
}

type grokVarintField struct {
	path  []uint64
	value uint64
}

func readProtobufVarint(bytes []byte, index *int) (uint64, bool) {
	var value uint64
	var shift uint
	for *index < len(bytes) && shift < 64 {
		b := bytes[*index]
		*index++
		value |= uint64(b&0x7F) << shift
		if b&0x80 == 0 {
			return value, true
		}
		shift += 7
	}
	return 0, false
}

func scanGrokProtobuf(data []byte, depth int, path []uint64, order int, scan *grokProtobufScan) int {
	index := 0
	nextOrder := order
	for index < len(data) {
		fieldStart := index
		key, ok := readProtobufVarint(data, &index)
		if !ok || key == 0 {
			index = fieldStart + 1
			continue
		}
		fieldNumber := key >> 3
		wireType := key & 0x07
		fieldPath := append(append([]uint64{}, path...), fieldNumber)
		switch wireType {
		case 0:
			value, ok := readProtobufVarint(data, &index)
			if !ok {
				index = fieldStart + 1
				continue
			}
			scan.varints = append(scan.varints, grokVarintField{path: fieldPath, value: value})
		case 1:
			if index+8 > len(data) {
				return nextOrder
			}
			index += 8
		case 2:
			length, ok := readProtobufVarint(data, &index)
			if !ok || int(length) > len(data)-index {
				index = fieldStart + 1
				continue
			}
			end := index + int(length)
			if depth < 4 {
				nextOrder = scanGrokProtobuf(data[index:end], depth+1, fieldPath, nextOrder, scan)
			}
			index = end
		case 5:
			if index+4 > len(data) {
				return nextOrder
			}
			bits := binary.LittleEndian.Uint32(data[index : index+4])
			scan.fixed32 = append(scan.fixed32, grokFixed32Field{
				path:  fieldPath,
				value: math.Float32frombits(bits),
				order: nextOrder,
			})
			nextOrder++
			index += 4
		default:
			index = fieldStart + 1
		}
	}
	return nextOrder
}

func grokGRPCWebDataFrames(data []byte) [][]byte {
	var frames [][]byte
	index := 0
	for index < len(data) {
		if index+5 > len(data) {
			return nil
		}
		flags := data[index]
		length := int(binary.BigEndian.Uint32(data[index+1 : index+5]))
		start := index + 5
		end := start + length
		if end > len(data) {
			return nil
		}
		if flags&0x80 == 0 {
			frames = append(frames, data[start:end])
		}
		index = end
	}
	return frames
}

func looksLikeProtobufPayload(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	fieldNumber := data[0] >> 3
	wireType := data[0] & 0x07
	return fieldNumber > 0 && (wireType == 0 || wireType == 1 || wireType == 2 || wireType == 5)
}

func grokGRPCWebTrailerFields(data []byte) map[string]string {
	fields := map[string]string{}
	index := 0
	for index+5 <= len(data) {
		flags := data[index]
		length := int(binary.BigEndian.Uint32(data[index+1 : index+5]))
		start := index + 5
		end := start + length
		if end > len(data) {
			break
		}
		if flags&0x80 != 0 {
			text := string(data[start:end])
			for _, line := range strings.Split(text, "\n") {
				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}
				key, value, ok := strings.Cut(line, ":")
				if !ok {
					continue
				}
				fields[strings.ToLower(strings.TrimSpace(key))] = grokPercentDecode(strings.TrimSpace(value))
			}
		}
		index = end
	}
	return fields
}

func grokPercentDecode(input string) string {
	bytes := []byte(input)
	out := make([]byte, 0, len(bytes))
	for i := 0; i < len(bytes); {
		if bytes[i] == '%' && i+2 < len(bytes) {
			if value, err := strconv.ParseUint(string(bytes[i+1:i+3]), 16, 8); err == nil {
				out = append(out, byte(value))
				i += 3
				continue
			}
		}
		out = append(out, bytes[i])
		i++
	}
	return string(out)
}

func parseGrokBillingPayload(data []byte, nowSecs int64) (grokBillingSnapshot, error) {
	payloads := grokGRPCWebDataFrames(data)
	if len(payloads) == 0 && looksLikeProtobufPayload(data) {
		payloads = [][]byte{data}
	}
	if len(payloads) == 0 {
		return grokBillingSnapshot{}, fmt.Errorf("grok billing response contained no protobuf payload")
	}

	var scan grokProtobufScan
	for _, payload := range payloads {
		scanGrokProtobuf(payload, 0, nil, 0, &scan)
	}

	var parsedPercent *float64
	bestPathLen := 0
	bestOrder := 0
	for _, field := range scan.fixed32 {
		if len(field.path) == 0 || field.path[len(field.path)-1] != 1 {
			continue
		}
		if math.IsNaN(float64(field.value)) || math.IsInf(float64(field.value), 0) {
			continue
		}
		if field.value < 0 || field.value > 100 {
			continue
		}
		if parsedPercent == nil || len(field.path) < bestPathLen || (len(field.path) == bestPathLen && field.order < bestOrder) {
			value := float64(field.value)
			parsedPercent = &value
			bestPathLen = len(field.path)
			bestOrder = field.order
		}
	}

	type resetCandidate struct {
		path []uint64
		ts   int64
	}
	var resets []resetCandidate
	for _, field := range scan.varints {
		if field.value < 1_700_000_000 || field.value > 2_100_000_000 {
			continue
		}
		ts := int64(field.value)
		if ts <= nowSecs {
			continue
		}
		resets = append(resets, resetCandidate{path: field.path, ts: ts})
	}
	var reset *int64
	for _, candidate := range resets {
		if len(candidate.path) == 3 && candidate.path[0] == 1 && candidate.path[1] == 5 && candidate.path[2] == 1 {
			ts := candidate.ts
			if reset == nil || ts < *reset {
				reset = &ts
			}
		}
	}
	if reset == nil {
		for _, candidate := range resets {
			ts := candidate.ts
			if reset == nil || ts < *reset {
				reset = &ts
			}
		}
	}

	hasUsagePeriod := false
	for _, field := range scan.varints {
		if grokPathHasPrefix(field.path, []uint64{1, 6}) {
			hasUsagePeriod = true
			break
		}
		if len(field.path) == 3 && field.path[0] == 1 && field.path[1] == 8 && field.path[2] == 1 && (field.value == 1 || field.value == 2) {
			hasUsagePeriod = true
			break
		}
	}
	noUsageYet := parsedPercent == nil && len(scan.fixed32) == 0 && reset != nil && hasUsagePeriod
	if parsedPercent == nil && noUsageYet {
		zero := 0.0
		parsedPercent = &zero
	}
	if parsedPercent == nil {
		return grokBillingSnapshot{}, fmt.Errorf("could not locate usage percent in grok billing response")
	}
	return grokBillingSnapshot{usedPercent: *parsedPercent, resetsAt: reset}, nil
}

func grokPathHasPrefix(path, prefix []uint64) bool {
	if len(path) < len(prefix) {
		return false
	}
	for i, value := range prefix {
		if path[i] != value {
			return false
		}
	}
	return true
}

// ParseGrokCreditsProtobuf recovers used percent and reset time from a
// gRPC-web or raw protobuf GetGrokCreditsConfig response.
func ParseGrokCreditsProtobuf(body []byte, observedAt time.Time) (*protocol.PlanLimitsSnapshot, error) {
	if status, message := grokTrailerStatus(body); status != 0 {
		return nil, fmt.Errorf("grok billing rpc failed (grpc-status %d): %s", status, message)
	}
	snapshot, err := parseGrokBillingPayload(body, observedAt.Unix())
	if err != nil {
		return nil, err
	}
	used := clampPercent(snapshot.usedPercent)
	window := protocol.PlanLimitWindow{Name: grokWindowCredits, UsedPercent: &used}
	if snapshot.resetsAt != nil && *snapshot.resetsAt > 0 {
		resetsAt := *snapshot.resetsAt
		window.ResetsAt = &resetsAt
		if minutes := grokWindowMinutes(resetsAt, observedAt.Unix()); minutes > 0 {
			window.WindowMinutes = &minutes
		}
	}
	return snapshotFromWindows("grok", []protocol.PlanLimitWindow{window}, observedAt), nil
}

func grokTrailerStatus(body []byte) (int64, string) {
	fields := grokGRPCWebTrailerFields(body)
	raw, ok := fields["grpc-status"]
	if !ok {
		return 0, ""
	}
	status, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fields["grpc-message"]
	}
	return status, fields["grpc-message"]
}

func grokWindowMinutes(resetsAt, nowSecs int64) int64 {
	days := math.Round(float64(resetsAt-nowSecs) / 86400)
	switch {
	case days >= 4 && days <= 12:
		return sevenDayMinutes
	case days >= 20 && days <= 45:
		return 43_200
	default:
		return 0
	}
}
