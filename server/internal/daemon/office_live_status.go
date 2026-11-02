package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/multica-ai/multica/server/pkg/agent"
	"github.com/multica-ai/multica/server/pkg/redact"
)

// Live status (DENE-2667 PR 4): while a run executes, the daemon reports every
// 15 seconds the command in flight, the last output line, and how long the
// run has made no activity, measured on the idle watchdog's own clock. Every
// text is redacted before it leaves this process. Only the office's test
// desktop build turns it on (MULTICA_LIVE_STATUS=on).
const (
	liveStatusOn = "on"

	liveStatusInterval = 15 * time.Second
	// liveTailBytes is how much of the latest output is kept to find its last
	// line, so a reply streamed word by word still yields a whole line.
	liveTailBytes        = 4096
	liveCommandMaxRunes  = 300
	liveOutputMaxRunes   = 200
	liveMaxTrackedCalls  = 64
	liveReportTimeout    = 5 * time.Second
	livePrivateKeyMarker = "PRIVATE KEY-----"
)

func liveStatusFromEnv() (bool, error) {
	switch value := strings.TrimSpace(os.Getenv("MULTICA_LIVE_STATUS")); value {
	case "", "off":
		return false, nil
	case liveStatusOn:
		return true, nil
	default:
		return false, fmt.Errorf("MULTICA_LIVE_STATUS: unknown value %q (want \"on\" or \"off\")", value)
	}
}

// liveTracker follows one execution's messages: the tool calls still in
// flight, the latest call, and the tail of the latest output. The live report
// reads it, and so does the idle watchdog's stop note (DENE-2900).
type liveTracker struct {
	// running lists the calls the backend itself reports as running (nil when
	// its stream already shows every running call).
	running func() []agent.RunningTool

	mu    sync.Mutex
	calls []liveCall
	last  liveCall
	// steps are the run's latest tool calls, oldest first, for the stop
	// summary a resumed or fresh session gets (DENE-2900).
	steps    []string
	tailKind agent.MessageType
	tail     string
}

type liveCall struct {
	callID    string
	tool      string
	command   string
	startedAt time.Time
}

// observe records one backend message. A nil tracker ignores it, so the
// drain loop calls it unconditionally.
func (t *liveTracker) observe(msg agent.Message) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	switch msg.Type {
	case agent.MessageToolUse:
		t.last = liveCall{callID: msg.CallID, tool: msg.Tool, command: liveCommand(msg.Tool, msg.Input), startedAt: time.Now()}
		t.calls = append(t.calls, t.last)
		if len(t.calls) > liveMaxTrackedCalls {
			t.calls = t.calls[len(t.calls)-liveMaxTrackedCalls:]
		}
		t.steps = append(t.steps, cutRunes(t.last.command, stopStepMaxRunes))
		if len(t.steps) > stopMaxSteps {
			t.steps = t.steps[len(t.steps)-stopMaxSteps:]
		}
	case agent.MessageToolResult:
		t.finish(msg.CallID)
		t.setTail(msg.Type, liveToolOutput(msg.Output))
	case agent.MessageText, agent.MessageThinking:
		// A streamed reply arrives in pieces; join them so its last line is
		// whole. A different kind of output starts a new tail.
		if msg.Type == t.tailKind {
			if msg.Content != "" {
				t.tail = trimLiveTail(t.tail + msg.Content)
			}
		} else {
			t.setTail(msg.Type, msg.Content)
		}
	case agent.MessageError:
		t.setTail(msg.Type, msg.Content)
	}
}

// finish drops the newest in-flight call the result answers. A result without
// a call id answers the newest call that has none.
func (t *liveTracker) finish(callID string) {
	for i := len(t.calls) - 1; i >= 0; i-- {
		if t.calls[i].callID == callID {
			t.calls = append(t.calls[:i], t.calls[i+1:]...)
			return
		}
	}
}

// setTail replaces the tail with new output. Empty output keeps the previous
// line, so a command that prints nothing does not blank the view.
func (t *liveTracker) setTail(kind agent.MessageType, content string) {
	if strings.TrimSpace(content) == "" {
		return
	}
	t.tailKind = kind
	t.tail = trimLiveTail(content)
}

// liveToolOutput unwraps a tool result that arrives as raw JSON content — Claude
// passes its tool_result block through as a JSON string or an array of text
// blocks — so the last line is a line of the output, not of its encoding. Any
// other output is returned unchanged.
func liveToolOutput(output string) string {
	trimmed := strings.TrimSpace(output)
	switch {
	case strings.HasPrefix(trimmed, `"`):
		var text string
		if json.Unmarshal([]byte(trimmed), &text) == nil {
			return text
		}
	case strings.HasPrefix(trimmed, `[`):
		var blocks []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal([]byte(trimmed), &blocks) == nil {
			var texts []string
			for _, block := range blocks {
				if block.Type == "text" && block.Text != "" {
					texts = append(texts, block.Text)
				}
			}
			if len(texts) > 0 {
				return strings.Join(texts, "\n")
			}
		}
	}
	return output
}

// snapshot returns the newest in-flight call and the last output line, both
// redacted. With no tool in flight by the watchdog's own count the call is
// left out, so a finished command is never shown as running.
func (t *liveTracker) snapshot(toolsInFlight int32) (tool, command, lastOutput string) {
	var backend []agent.RunningTool
	if toolsInFlight > 0 && t.running != nil {
		backend = t.running()
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if toolsInFlight > 0 {
		if n := len(t.calls); n > 0 {
			tool, command = t.calls[n-1].tool, t.calls[n-1].command
		} else if calls := t.backendCallsLocked(backend); len(calls) > 0 {
			tool, command = calls[len(calls)-1].tool, calls[len(calls)-1].command
		}
	}
	return tool, command, liveLastLine(t.tail)
}

// untrackedRunning counts the backend's running calls that no tool_use
// message has put in flight yet, so the watchdog adds each call once.
func (t *liveTracker) untrackedRunning() int32 {
	if t.running == nil {
		return 0
	}
	backend := t.running()
	if len(backend) == 0 {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return int32(len(t.backendCallsLocked(backend)))
}

// backendCallsLocked converts the backend's running calls that are not
// already in flight by message into calls, in the backend's order.
func (t *liveTracker) backendCallsLocked(backend []agent.RunningTool) []liveCall {
	var calls []liveCall
	for _, running := range backend {
		if running.CallID != "" && t.hasCallLocked(running.CallID) {
			continue
		}
		calls = append(calls, liveCall{callID: running.CallID, tool: running.Tool, command: liveCommand(running.Tool, running.Input), startedAt: running.StartedAt})
	}
	return calls
}

func (t *liveTracker) hasCallLocked(callID string) bool {
	for _, call := range t.calls {
		if call.callID == callID {
			return true
		}
	}
	return false
}

// stopFacts collects what the stop note says when the watchdog fires after
// budget: the call the budget ran out on while a command was running (the
// oldest still in flight), the run's latest call, and its last output line.
func (t *liveTracker) stopFacts(budget time.Duration, commandRunning bool) idleStopFacts {
	var backend []agent.RunningTool
	if commandRunning && t.running != nil {
		backend = t.running()
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	facts := idleStopFacts{
		budget:         budget,
		commandRunning: commandRunning,
		lastStep:       t.last.command,
		recentSteps:    append([]string(nil), t.steps...),
		lastOutput:     liveLastLine(t.tail),
		now:            time.Now(),
	}
	if commandRunning {
		candidates := append(append([]liveCall(nil), t.calls...), t.backendCallsLocked(backend)...)
		for i, call := range candidates {
			if i == 0 || (!call.startedAt.IsZero() && (facts.commandStarted.IsZero() || call.startedAt.Before(facts.commandStarted))) {
				facts.command, facts.commandStarted = call.command, call.startedAt
			}
		}
	}
	return facts
}

// liveCommand renders a tool call as one redacted line: the shell command when
// the input has one, otherwise the tool and its main argument.
func liveCommand(tool string, input map[string]any) string {
	text := ""
	switch v := input["command"].(type) {
	case string:
		text = v
	case []string:
		text = strings.Join(v, " ")
	case []any:
		parts := make([]string, 0, len(v))
		for _, part := range v {
			if s, ok := part.(string); ok {
				parts = append(parts, s)
			}
		}
		text = strings.Join(parts, " ")
	}
	if strings.TrimSpace(text) == "" {
		text = tool
		for _, key := range []string{"cmd", "file_path", "path", "pattern", "url", "query", "description"} {
			if v, ok := input[key].(string); ok && strings.TrimSpace(v) != "" {
				text = tool + " " + v
				break
			}
		}
	}
	// Redact the whole text before folding and cutting it: a cut can split a
	// secret into a shape the patterns no longer match.
	return foldLiveLine(redact.Text(text), liveCommandMaxRunes, false)
}

// liveLastLine returns the last non-empty line of the tail, redacted. Output
// that still shows part of a private key after redaction (its BEGIN line fell
// outside the tail) is withheld entirely.
func liveLastLine(tail string) string {
	redacted := redact.Text(tail)
	if strings.Contains(redacted, livePrivateKeyMarker) {
		return ""
	}
	lines := strings.Split(redacted, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := foldLiveLine(lines[i], liveOutputMaxRunes, true); line != "" {
			return line
		}
	}
	return ""
}

// trimLiveTail keeps the last liveTailBytes of s, starting after a whitespace
// so the cut never lands inside a token (or a multi-byte character). A tail
// with no whitespace at all is dropped rather than cut mid-token.
func trimLiveTail(s string) string {
	if len(s) <= liveTailBytes {
		return s
	}
	cut := s[len(s)-liveTailBytes:]
	i := strings.IndexAny(cut, " \t\r\n")
	if i < 0 {
		return ""
	}
	return cut[i+1:]
}

// foldLiveLine collapses whitespace to single spaces and bounds the line,
// keeping its end (latest output) or its start (a command's verb).
func foldLiveLine(s string, maxRunes int, keepEnd bool) string {
	runes := []rune(strings.Join(strings.Fields(s), " "))
	if len(runes) <= maxRunes {
		return string(runes)
	}
	if keepEnd {
		return "…" + string(runes[len(runes)-maxRunes:])
	}
	return string(runes[:maxRunes]) + "…"
}

// runLiveReporter reports the run's live status every interval until ctx
// ends. activity returns the run's silence and its in-flight tool count on
// the idle watchdog's clock. Reporting stops for the run when the server
// takes no live status (an older server answers 404) or drops the report
// (the run is not an issue run, or no longer running).
func (d *Daemon) runLiveReporter(ctx context.Context, taskID string, tracker *liveTracker, activity func() (time.Duration, int32), taskLog *slog.Logger) {
	interval := d.cfg.LiveStatusInterval
	if interval <= 0 {
		interval = liveStatusInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		idle, inFlight := activity()
		tool, command, lastOutput := tracker.snapshot(inFlight)
		report := TaskLiveData{
			Tool:          tool,
			Command:       command,
			LastOutput:    lastOutput,
			ToolsInFlight: int(max(inFlight, 0)),
			IdleSeconds:   int64(max(idle, 0) / time.Second),
		}
		sendCtx, cancel := context.WithTimeout(ctx, liveReportTimeout)
		accepted, err := d.client.ReportTaskLive(sendCtx, taskID, report)
		cancel()
		var reqErr *requestError
		switch {
		case errors.As(err, &reqErr) && reqErr.StatusCode == http.StatusNotFound:
			taskLog.Debug("server takes no live status for this run; live reporting stopped")
			return
		case err != nil:
			if ctx.Err() == nil {
				taskLog.Debug("report live status failed", "error", err)
			}
		case !accepted:
			taskLog.Debug("server dropped the live status; live reporting stopped")
			return
		}
	}
}
