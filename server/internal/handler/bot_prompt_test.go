package handler

import (
	"crypto/tls"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRenderBotSetupPrompt(t *testing.T) {
	pull := renderBotSetupPrompt(botSetupPromptData{
		Name: "Grok", Endpoint: "https://office.example/api/mcp", Token: "mul_abc",
	})
	for _, want := range []string{
		"https://office.example/api/mcp", "Authorization: Bearer mul_abc",
		"whoami", "list_members", "list_projects",
		"get_events", "持久化", "set_webhook", "create_project", "request_approval", "close_issue",
		"你没有放行权", "令牌保密", "自动建好工单", "不要再建或再指派", "方案之外",
	} {
		if !strings.Contains(pull, want) {
			t.Errorf("pull prompt missing %q", want)
		}
	}
	// Approval already creates and starts the step tickets; the loop must
	// not tell the Bot to create them again (duplicate paid runs).
	if strings.Contains(pull, "批准后按方案建工单") {
		t.Errorf("pull prompt asks the Bot to recreate plan tickets:\n%s", pull)
	}
	if strings.Contains(pull, "approve_plan") {
		t.Errorf("pull prompt mentions release it does not have:\n%s", pull)
	}

	push := renderBotSetupPrompt(botSetupPromptData{
		Name: "Grok", Endpoint: "https://office.example/api/mcp", Token: "mul_abc",
		WebhookURL: "https://grok.example/hook", WebhookSecret: "s3cret", CanRelease: true,
	})
	for _, want := range []string{"先确认网络通", "Tailscale", "令牌永不过期"} {
		if !strings.Contains(push, want) {
			t.Errorf("prompt missing network/expiry line %q", want)
		}
	}
	for _, want := range []string{"https://grok.example/hook", "X-Multica-Signature", "推送密钥：s3cret", "approve_plan"} {
		if !strings.Contains(push, want) {
			t.Errorf("push prompt missing %q", want)
		}
	}

	rotated := renderBotSetupPrompt(botSetupPromptData{
		Name: "Grok", Endpoint: "https://office.example/api/mcp", Token: "mul_new",
		WebhookURL: "https://grok.example/hook",
	})
	if !strings.Contains(rotated, "沿用之前保存") {
		t.Errorf("rotated prompt should keep the saved webhook secret:\n%s", rotated)
	}
}

func TestBotMCPEndpoint(t *testing.T) {
	r := httptest.NewRequest("POST", "/api/bots", nil)
	r.Host = "office.example"

	cases := []struct {
		name string
		h    *Handler
		prep func()
		want string
	}{
		{"public url wins", &Handler{cfg: Config{PublicURL: "https://api.example/", AppURL: "https://app.example"}}, nil, "https://api.example/api/mcp"},
		{"app url next", &Handler{cfg: Config{AppURL: "https://app.example"}}, nil, "https://app.example/api/mcp"},
		{"request origin", &Handler{}, nil, "http://office.example/api/mcp"},
		{"forwarded https", &Handler{}, func() { r.Header.Set("X-Forwarded-Proto", "https") }, "https://office.example/api/mcp"},
		{"tls", &Handler{}, func() { r.Header.Del("X-Forwarded-Proto"); r.TLS = &tls.ConnectionState{} }, "https://office.example/api/mcp"},
	}
	for _, c := range cases {
		if c.prep != nil {
			c.prep()
		}
		if got := c.h.botMCPEndpoint(r); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
