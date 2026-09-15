package agent

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestParseDshModelID(t *testing.T) {
	got, err := parseDshModelID("deepseek-official/deepseek-v4%2Fflash")
	if err != nil {
		t.Fatal(err)
	}
	if got.Provider != "deepseek-official" || got.ID != "deepseek-v4/flash" {
		t.Fatalf("unexpected model: %#v", got)
	}
	if _, err := parseDshModelID("deepseek-v4-flash"); err == nil {
		t.Fatal("unqualified DSH model should fail")
	}
}

func TestDshModelIDForLookup(t *testing.T) {
	encoded := "deepseek-official/deepseek-v4%2Fflash"
	decoded := "deepseek-official/deepseek-v4/flash"
	if got := dshModelIDForLookup(encoded); got != decoded {
		t.Fatalf("encoded lookup = %q, want %q", got, decoded)
	}
	if got := dshModelIDForLookup(decoded); got != decoded {
		t.Fatalf("decoded lookup = %q, want %q", got, decoded)
	}
	plain := "deepseek-official/deepseek-v4-flash"
	if got := dshModelIDForLookup(plain); got != plain {
		t.Fatalf("plain lookup = %q, want %q", got, plain)
	}
}

func TestBuildDshMCPServers(t *testing.T) {
	raw := json.RawMessage(`{"mcpServers":{"files":{"command":"node","args":["server.js"],"env":{"TOKEN":"value"}},"remote":{"type":"streamable-http","url":"https://mcp.example/rpc","headers":{"Authorization":"Bearer test"}}}}`)
	got, err := buildDshMCPServers(raw, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d MCP servers", len(got))
	}
	if got[0].Transport != "stdio" || got[0].Command != "node" || got[0].Env["TOKEN"] != "value" {
		t.Fatalf("bad stdio server: %#v", got[0])
	}
	if got[1].Transport != "streamable-http" || got[1].Headers["Authorization"] != "Bearer test" {
		t.Fatalf("bad HTTP server: %#v", got[1])
	}
}

func TestBuildDshMCPServersRejectsSSE(t *testing.T) {
	raw := json.RawMessage(`{"mcpServers":{"legacy":{"type":"sse","url":"https://mcp.example/sse"}}}`)
	if _, err := buildDshMCPServers(raw, slog.Default()); err == nil || !strings.Contains(err.Error(), "SSE") {
		t.Fatalf("expected SSE error, got %v", err)
	}
}

func TestDshBackendExecuteStreamsProtocol(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	bin := writeDshFixture(t, `
if [ "$1" != "--profile" ] || [ "$2" != "multica" ] || [ "$3" != "--stdio" ]; then exit 9; fi
printf '%s\n' '{"v":1,"type":"ready","runtime":"dsh","plugin_version":"test","capabilities":{}}'
IFS= read -r command
case "$command" in *'"type":"execute"'*) ;; *) exit 8 ;; esac
printf '%s\n' '{"v":1,"type":"session","request_id":"task-1","session_id":"session-1","resumed":false}'
printf '%s\n' '{"v":1,"type":"thinking","request_id":"task-1","content":"checking"}'
printf '%s\n' '{"v":1,"type":"tool_call","request_id":"task-1","call_id":"call-1","name":"bash","arguments":"{\"command\":\"pwd\"}"}'
printf '%s\n' '{"v":1,"type":"tool_result","request_id":"task-1","call_id":"call-1","name":"bash","output":"/work","is_error":false}'
printf '%s\n' '{"v":1,"type":"text","request_id":"task-1","content":"done"}'
printf '%s\n' '{"v":1,"type":"usage","request_id":"task-1","provider":"deepseek-official","model":"deepseek-v4-flash","input_tokens":12,"output_tokens":3,"cache_read_tokens":2}'
printf '%s\n' '{"v":1,"type":"result","request_id":"task-1","status":"completed","session_id":"session-1","output":"done","resume_rejected":false}'
`)
	b, err := New("dsh", Config{ExecutablePath: bin, TaskID: "task-1", Logger: slog.Default()})
	if err != nil {
		t.Fatal(err)
	}
	session, err := b.Execute(context.Background(), "say done", ExecOptions{Cwd: t.TempDir(), Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	var messages []Message
	for message := range session.Messages {
		messages = append(messages, message)
	}
	result := <-session.Result
	if result.Status != "completed" || result.Output != "done" || result.SessionID != "session-1" {
		t.Fatalf("bad result: %#v", result)
	}
	usage := result.Usage["deepseek-official/deepseek-v4-flash"]
	if usage.InputTokens != 12 || usage.OutputTokens != 3 || usage.CacheReadTokens != 2 {
		t.Fatalf("bad usage: %#v", usage)
	}
	if len(messages) != 5 || messages[0].SessionID != "session-1" || messages[2].Tool != "bash" {
		t.Fatalf("bad messages: %#v", messages)
	}
}

func TestDshBackendCancellationUsesProtocol(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	bin := writeDshFixture(t, `
printf '%s\n' '{"v":1,"type":"ready","runtime":"dsh","plugin_version":"test","capabilities":{}}'
IFS= read -r execute
printf '%s\n' '{"v":1,"type":"session","request_id":"task-cancel","session_id":"session-cancel","resumed":false}'
IFS= read -r cancel
case "$cancel" in *'"type":"cancel"'*) ;; *) exit 8 ;; esac
printf '%s\n' '{"v":1,"type":"result","request_id":"task-cancel","status":"cancelled","session_id":"session-cancel","output":"","resume_rejected":false}'
`)
	b, err := New("dsh", Config{ExecutablePath: bin, TaskID: "task-cancel", Logger: slog.Default()})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	session, err := b.Execute(ctx, "wait", ExecOptions{Cwd: t.TempDir(), Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	message := <-session.Messages
	if message.SessionID != "session-cancel" {
		t.Fatalf("bad session message: %#v", message)
	}
	cancel()
	for range session.Messages {
	}
	result := <-session.Result
	if result.Status != "cancelled" || result.SessionID != "session-cancel" {
		t.Fatalf("bad cancellation result: %#v", result)
	}
}

func TestDshBackendPrependsSystemPrompt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	capture := filepath.Join(t.TempDir(), "command.json")
	bin := writeDshFixture(t, `
printf '%s\n' '{"v":1,"type":"ready","runtime":"dsh","plugin_version":"test","capabilities":{}}'
IFS= read -r command
printf '%s\n' "$command" > "$DSH_CAPTURE"
printf '%s\n' '{"v":1,"type":"session","request_id":"task-1","session_id":"s1","resumed":false}'
printf '%s\n' '{"v":1,"type":"result","request_id":"task-1","status":"completed","session_id":"s1","output":"ok","resume_rejected":false}'
`)
	t.Setenv("DSH_CAPTURE", capture)
	b, err := New("dsh", Config{ExecutablePath: bin, TaskID: "task-1", Logger: slog.Default()})
	if err != nil {
		t.Fatal(err)
	}
	session, err := b.Execute(context.Background(), "say done", ExecOptions{
		Cwd:          t.TempDir(),
		Timeout:      5 * time.Second,
		SystemPrompt: "RUNTIME BRIEF",
	})
	if err != nil {
		t.Fatal(err)
	}
	for range session.Messages {
	}
	result := <-session.Result
	if result.Status != "completed" {
		t.Fatalf("bad result: %#v", result)
	}
	raw, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	var command dshExecuteCommand
	if err := json.Unmarshal(raw, &command); err != nil {
		t.Fatalf("decode captured execute command: %v\n%s", err, raw)
	}
	want := "RUNTIME BRIEF\n\n---\n\nsay done"
	if command.Prompt != want {
		t.Fatalf("prompt = %q, want %q", command.Prompt, want)
	}
}

func TestDshBackendEmptySystemPromptLeavesPromptUnchanged(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	capture := filepath.Join(t.TempDir(), "command.json")
	bin := writeDshFixture(t, `
printf '%s\n' '{"v":1,"type":"ready","runtime":"dsh","plugin_version":"test","capabilities":{}}'
IFS= read -r command
printf '%s\n' "$command" > "$DSH_CAPTURE"
printf '%s\n' '{"v":1,"type":"session","request_id":"task-1","session_id":"s1","resumed":false}'
printf '%s\n' '{"v":1,"type":"result","request_id":"task-1","status":"completed","session_id":"s1","output":"ok","resume_rejected":false}'
`)
	t.Setenv("DSH_CAPTURE", capture)
	b, err := New("dsh", Config{ExecutablePath: bin, TaskID: "task-1", Logger: slog.Default()})
	if err != nil {
		t.Fatal(err)
	}
	session, err := b.Execute(context.Background(), "say done", ExecOptions{
		Cwd:     t.TempDir(),
		Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	for range session.Messages {
	}
	result := <-session.Result
	if result.Status != "completed" {
		t.Fatalf("bad result: %#v", result)
	}
	raw, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	var command dshExecuteCommand
	if err := json.Unmarshal(raw, &command); err != nil {
		t.Fatalf("decode captured execute command: %v\n%s", err, raw)
	}
	if command.Prompt != "say done" {
		t.Fatalf("prompt = %q, want %q", command.Prompt, "say done")
	}
}

func TestValidateThinkingLevelDshEncodedModelID(t *testing.T) {
	catalog := Catalog{Models: []Model{{
		ID: "deepseek-official/deepseek-v4%2Fflash",
		Thinking: &ModelThinking{SupportedLevels: []ThinkingLevel{
			{Value: "off", Label: "Off"},
			{Value: "high", Label: "High"},
			{Value: "max", Label: "Max"},
		}},
	}}}
	load := func() (Catalog, error) { return catalog, nil }
	ok, err := ValidateThinkingLevelWith(load, "dsh", "deepseek-official/deepseek-v4/flash", "high")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("decoded agent.model should match the encoded catalog id")
	}
	ok, err = ValidateThinkingLevelWith(load, "dsh", "deepseek-official/deepseek-v4%2Fflash", "max")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("encoded agent.model should match the encoded catalog id")
	}
}

func fieldDshThinking() *ModelThinking {
	return &ModelThinking{
		SupportedLevels: []ThinkingLevel{
			{Value: "off", Label: "Off"},
			{Value: "low", Label: "Low"},
			{Value: "high", Label: "High"},
			{Value: "max", Label: "Max"},
		},
		DefaultLevel: "high",
	}
}

func fieldDshCatalog() []Model {
	ids := []string{
		"deepseek-v4-flash",
		"deepseek-v4-flash-vision-exp",
		"deepseek-v4-pro",
		"deepseek-flash",
	}
	out := make([]Model, 0, len(ids))
	for _, id := range ids {
		out = append(out, Model{
			ID:       "deepseek-official/" + id,
			Label:    id,
			Thinking: fieldDshThinking(),
		})
	}
	return out
}

func TestFindDshRecoverableCatalogEntryFieldModel(t *testing.T) {
	got, ok := findDshRecoverableCatalogEntry(fieldDshCatalog(), "deepseek-official/deepseek-v4.1-flash")
	if !ok {
		t.Fatal("expected a recoverable catalog entry for the persisted V4.1 Flash id")
	}
	if got.ID != "deepseek-official/deepseek-flash" {
		t.Fatalf("recovered id = %q, want deepseek-official/deepseek-flash", got.ID)
	}
}

func TestFindDshRecoverableCatalogEntryLiveLabel(t *testing.T) {
	catalog := []Model{
		{ID: "deepseek-official/deepseek-v4-flash", Label: "DeepSeek-V4-Flash", Thinking: fieldDshThinking()},
		{ID: "deepseek-official/deepseek-flash", Label: "DeepSeek-V41-Flash", Thinking: fieldDshThinking()},
	}
	got, ok := findDshRecoverableCatalogEntry(catalog, "deepseek-official/deepseek-v4.1-flash")
	if !ok || got.ID != "deepseek-official/deepseek-flash" {
		t.Fatalf("got %#v ok=%v", got, ok)
	}
}

func TestFindDshRecoverableCatalogEntryDoesNotBorrowFirstModel(t *testing.T) {
	catalog := []Model{
		{ID: "deepseek-official/deepseek-v4-flash", Label: "deepseek-v4-flash", Thinking: &ModelThinking{SupportedLevels: []ThinkingLevel{{Value: "off"}, {Value: "ultra"}}}},
		{ID: "deepseek-official/deepseek-v4-pro", Label: "deepseek-v4-pro", Thinking: &ModelThinking{SupportedLevels: []ThinkingLevel{{Value: "off"}}}},
	}
	if _, ok := findDshRecoverableCatalogEntry(catalog, "deepseek-official/deepseek-v4.1-flash"); ok {
		t.Fatal("mixed thinking catalogs must fail closed")
	}
}

func TestFindDshRecoverableCatalogEntrySingleRowFailsClosed(t *testing.T) {
	catalog := []Model{{
		ID: "deepseek-official/deepseek-v4-flash", Label: "deepseek-v4-flash", Thinking: fieldDshThinking(),
	}}
	if _, ok := findDshRecoverableCatalogEntry(catalog, "deepseek-official/deepseek-v4.1-flash"); ok {
		t.Fatal("a single catalog row must not donate its thinking levels")
	}
}

func TestFindDshRecoverableCatalogEntryIdenticalPrefixThinking(t *testing.T) {
	catalog := []Model{
		{ID: "deepseek-official/deepseek-v4-flash", Label: "deepseek-v4-flash", Thinking: fieldDshThinking()},
		{ID: "deepseek-official/deepseek-v4-pro", Label: "deepseek-v4-pro", Thinking: fieldDshThinking()},
	}
	got, ok := findDshRecoverableCatalogEntry(catalog, "deepseek-official/stale-unknown-flash")
	if !ok {
		t.Fatal("identical same-prefix thinking should recover")
	}
	if got.ID != "deepseek-official/stale-unknown-flash" {
		t.Fatalf("persisted id rewritten to %q", got.ID)
	}
	if thinkingValuesSignature(got.Thinking) != thinkingValuesSignature(fieldDshThinking()) {
		t.Fatalf("unexpected thinking: %#v", got.Thinking)
	}
}

func TestValidateThinkingLevelDshPersistedV41Flash(t *testing.T) {
	load := func() (Catalog, error) { return Catalog{Models: fieldDshCatalog()}, nil }
	ok, err := ValidateThinkingLevelWith(load, "dsh", "deepseek-official/deepseek-v4.1-flash", "high")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("persisted deepseek-v4.1-flash should accept catalog thinking levels")
	}
	ok, err = ValidateThinkingLevelWith(load, "dsh", "deepseek-official/deepseek-v4.1-flash", "ultra")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("unknown thinking token must still fail closed")
	}
}

func TestDiscoverDshModels(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	bin := writeDshFixture(t, `
printf '%s\n' '{"v":1,"type":"models","models":[{"id":"deepseek-official/deepseek-v4-flash","label":"DeepSeek V4 Flash","provider":"DeepSeek","default":true,"thinking":{"supported_levels":[{"value":"high","label":"High"},{"value":"max","label":"Max"}],"default_level":"high"}}]}'
`)
	models, err := discoverDshModels(context.Background(), Command{Path: bin})
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || !models[0].Default || models[0].Thinking == nil || models[0].Thinking.DefaultLevel != "high" {
		t.Fatalf("bad models: %#v", models)
	}
}

func writeDshFixture(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "dsh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nset -eu\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}
