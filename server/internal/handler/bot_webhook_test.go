package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/middleware"
)

// webhookCapture is a local receiver that records each request by path.
type webhookCapture struct {
	mu   sync.Mutex
	reqs []capturedWebhook
	srv  *httptest.Server
}

type capturedWebhook struct {
	path    string
	headers http.Header
	body    map[string]any
}

func newWebhookCapture(t *testing.T) *webhookCapture {
	t.Helper()
	c := &webhookCapture{}
	c.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body := map[string]any{}
		_ = json.Unmarshal(raw, &body)
		c.mu.Lock()
		c.reqs = append(c.reqs, capturedWebhook{path: r.URL.Path, headers: r.Header.Clone(), body: body})
		c.mu.Unlock()
		w.WriteHeader(200)
	}))
	t.Cleanup(c.srv.Close)
	t.Setenv("MULTICA_PLUGIN_DEV_ORIGINS", c.srv.URL)
	return c
}

func (c *webhookCapture) byPath(path string) []capturedWebhook {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []capturedWebhook
	for _, r := range c.reqs {
		if r.path == path {
			out = append(out, r)
		}
	}
	return out
}

func TestBotWakeCustomHeadersAreSentAndReservedOnesRefused(t *testing.T) {
	if testHandler == nil {
		t.Skip("requires test database")
	}
	f := newHumanNeededFixture(t, true)
	capture := newWebhookCapture(t)
	path := "/bots/" + uuidToString(f.bot.ID) + "/wake"

	code, data := f.call(t, "PUT", path, map[string]any{
		"webhook_url": capture.srv.URL + "/main", "webhook_key": "Bearer k",
		"webhook_headers": map[string]string{"Authorization": "Bearer other"},
	})
	if code != 400 || !strings.Contains(string(data), "webhook_key") {
		t.Fatalf("reserved header: %d %s", code, data)
	}
	code, data = f.call(t, "PUT", path, map[string]any{"webhook_headers": map[string]string{"X-Gateway": "g"}})
	if code != 400 {
		t.Fatalf("headers without url: %d %s", code, data)
	}
	code, data = f.call(t, "PUT", path, map[string]any{
		"webhook_url": capture.srv.URL + "/main", "webhook_key": "Bearer k",
		"webhook_headers": map[string]string{"X-Gateway": "g", "Content-Type": "text/plain"},
	})
	if code != 400 {
		t.Fatalf("content-type is the office's: %d %s", code, data)
	}
	code, data = f.call(t, "PUT", path, map[string]any{
		"webhook_url": capture.srv.URL + "/main", "webhook_key": "Bearer k",
		"webhook_headers": map[string]string{"X-Gateway": "g"},
	})
	if code != 200 || strings.Contains(string(data), "X-Gateway") && strings.Contains(string(data), `"g"`) {
		t.Fatalf("set headers: %d %s", code, data)
	}

	ctx := context.Background()
	c, err := testHandler.Queries.LockBotWake(ctx, f.bot.ID)
	if err != nil {
		t.Fatal(err)
	}
	target, why := testHandler.openWakeTarget(ctx, c.WebhookSecret)
	if why != "" {
		t.Fatal(why)
	}
	worker := NewBotWakeWorker(testHandler)
	worker.client = func(*url.URL) *http.Client { return capture.srv.Client() }
	if status, reason := worker.deliver(ctx, target, []byte(`{"event_id":"e1"}`)); status != "received" {
		t.Fatalf("deliver: %s %s", status, reason)
	}
	got := capture.byPath("/main")
	if len(got) != 1 || got[0].headers.Get("X-Gateway") != "g" || got[0].headers.Get("Authorization") != "Bearer k" ||
		got[0].headers.Get("Content-Type") != "application/json" || got[0].headers.Get("X-Multica-Event-ID") != "e1" {
		t.Fatalf("headers: %+v", got)
	}
}

func TestBotSelfRegistersWebhookAndNamedReceivers(t *testing.T) {
	if testHandler == nil {
		t.Skip("requires test database")
	}
	f := newHumanNeededFixture(t, true)
	capture := newWebhookCapture(t)
	router := chi.NewRouter()
	router.Use(middleware.Auth(testHandler.Queries, nil, nil, nil))
	router.Put("/api/bot/webhook", testHandler.SetBotSelfWebhook)
	router.Get("/api/bot/webhooks", testHandler.ListBotWebhookReceivers)
	router.Put("/api/bot/webhooks", testHandler.SetBotWebhookReceiver)
	api := httptest.NewServer(router)
	defer api.Close()
	call := func(token, method, path string, body any) (int, map[string]any) {
		t.Helper()
		raw, _ := json.Marshal(body)
		req, _ := http.NewRequest(method, api.URL+path, bytes.NewReader(raw))
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := api.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		out := map[string]any{}
		data, _ := io.ReadAll(res.Body)
		_ = json.Unmarshal(data, &out)
		return res.StatusCode, out
	}

	// A person's own key cannot register a Bot address.
	if code, out := call(insertTestPATFor(t, testUserID), "PUT", "/api/bot/webhook", map[string]any{"url": capture.srv.URL + "/main"}); code != 403 {
		t.Fatalf("person key: %d %v", code, out)
	}
	// kun-main's {url, headers:{Authorization}} shape: Authorization becomes the key.
	code, out := call(f.token, "PUT", "/api/bot/webhook", map[string]any{
		"url": capture.srv.URL + "/main", "headers": map[string]string{"Authorization": "Bearer self", "X-Trace": "t"},
	})
	if code != 200 || out["webhook_configured"] != true || out["bot_id"] != uuidToString(f.bot.ID) || out["workspace_id"] != testWorkspaceID {
		t.Fatalf("self register: %d %v", code, out)
	}
	if names, _ := out["header_names"].([]any); len(names) != 1 || names[0] != "X-Trace" {
		t.Fatalf("header names: %v", out["header_names"])
	}
	ctx := context.Background()
	c, err := testHandler.Queries.LockBotWake(ctx, f.bot.ID)
	if err != nil {
		t.Fatal(err)
	}
	target, why := testHandler.openWakeTarget(ctx, c.WebhookSecret)
	if why != "" || target.Key != "Bearer self" || target.Endpoint.Path != "/main" || target.Headers["X-Trace"] != "t" {
		t.Fatalf("stored target: %+v %s", target, why)
	}
	if code, out := call(f.token, "PUT", "/api/bot/webhook", map[string]any{"key": "Bearer x"}); code != 400 {
		t.Fatalf("empty url with key: %d %v", code, out)
	}

	// Named receivers: add two, replace one, list shows hosts only.
	for _, name := range []string{"经理甲", "manager-b"} {
		if code, out := call(f.token, "PUT", "/api/bot/webhooks", map[string]any{
			"name": name, "url": capture.srv.URL + "/r/" + url.PathEscape(name), "key": "Bearer " + url.PathEscape(name),
		}); code != 200 || out["name"] != name {
			t.Fatalf("add receiver %s: %d %v", name, code, out)
		}
	}
	if code, out := call(f.token, "PUT", "/api/bot/webhooks", map[string]any{"name": "", "url": capture.srv.URL}); code != 400 {
		t.Fatalf("nameless receiver: %d %v", code, out)
	}
	code, out = call(f.token, "GET", "/api/bot/webhooks", nil)
	list, _ := out["receivers"].([]any)
	if code != 200 || len(list) != 2 {
		t.Fatalf("list: %d %v", code, out)
	}
	raw, _ := json.Marshal(out)
	if strings.Contains(string(raw), "/r/") || strings.Contains(string(raw), "Bearer") {
		t.Fatalf("list leaks address or key: %s", raw)
	}

	// One push to the main address fans out to each receiver, tagged.
	worker := NewBotWakeWorker(testHandler)
	worker.client = func(*url.URL) *http.Client { return capture.srv.Client() }
	worker.fanOutReceivers(ctx, f.bot.ID, []byte(`{"event_id":"e2","type":"mention"}`))
	worker.waitReceivers()
	a := capture.byPath("/r/经理甲")
	b := capture.byPath("/r/manager-b")
	if len(a) != 1 || len(b) != 1 || a[0].body["receiver"] != "经理甲" || b[0].body["receiver"] != "manager-b" ||
		a[0].headers.Get("Authorization") != "Bearer "+url.PathEscape("经理甲") || a[0].body["event_id"] != "e2" {
		t.Fatalf("fan-out: %+v %+v", a, b)
	}
	var delivered int
	dbfx.QueryRow(t, `SELECT count(*) FROM bot_webhook_receiver WHERE bot_id=$1 AND last_delivered_at IS NOT NULL AND failures=0`, f.bot.ID).Scan(&delivered)
	if delivered != 2 {
		t.Fatalf("recorded deliveries = %d", delivered)
	}

	// An empty url removes the receiver; deleting the Bot removes the rest.
	if code, _ := call(f.token, "PUT", "/api/bot/webhooks", map[string]any{"name": "manager-b"}); code != 200 {
		t.Fatalf("remove: %d", code)
	}
	var left int
	dbfx.QueryRow(t, `SELECT count(*) FROM bot_webhook_receiver WHERE bot_id=$1`, f.bot.ID).Scan(&left)
	if left != 1 {
		t.Fatalf("receivers left = %d", left)
	}
	if err := testHandler.Queries.DeleteBotWebhookReceiversByBot(ctx, f.bot.ID); err != nil {
		t.Fatal(err)
	}

	// Disconnect: an empty url clears the main address.
	if code, out := call(f.token, "PUT", "/api/bot/webhook", map[string]any{"url": ""}); code != 200 || out["webhook_configured"] != false {
		t.Fatalf("disconnect: %d %v", code, out)
	}
}

func TestBotSetupTextChecksNetworkExpiryAndPushAddress(t *testing.T) {
	never := pgtype.Timestamptz{}
	text := renderBotSetupText(botSetupInput{BotName: "b", WorkspaceName: "w", ServerURL: "http://localhost:8080/", Token: "mul_x", KeyExpiry: &never})
	for _, want := range []string{"http://localhost:8080/health", "永不过期", "/api/bot/webhook", "/api/bot/webhooks"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
	at := pgtype.Timestamptz{Time: time.Date(2027, 1, 2, 0, 0, 0, 0, time.UTC), Valid: true}
	if text := renderBotSetupText(botSetupInput{BotName: "b", ServerURL: "https://office.example", KeyExpiry: &at}); !strings.Contains(text, "2027-01-02") {
		t.Errorf("expiry date missing:\n%s", text)
	}
	if text := renderBotSetupText(botSetupInput{BotName: "b", ServerURL: "https://office.example"}); strings.Contains(text, "过期") {
		t.Errorf("no key, no expiry line:\n%s", text)
	}
}
