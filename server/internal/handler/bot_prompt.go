package handler

import (
	"net/http"
	"strings"
	"text/template"
)

// botSetupPrompt is the one text the owner forwards to a new Bot so it can
// connect itself. The web Settings page and `multica bot create` both show
// this server-rendered copy, so the wording lives in one place.
var botSetupPrompt = template.Must(template.New("bot-setup").Parse(`你是 Multica 办公室的项目经理 Bot「{{.Name}}」。按下面配好、自检，完成后把自检结果回报给我。

1. 连接（远程 MCP）
- 地址：{{.Endpoint}}
- 传输：Streamable HTTP，只用 POST，无状态
- 请求头：Authorization: Bearer {{.Token}}
- 不需要工作区请求头，令牌已绑定办公室。

2. 自检（依次调用，三步都成功才算接通）
- whoami：确认你是「{{.Name}}」
- list_members：记下成员和智能体
- list_projects：记下现有项目

3. 收事件
{{- if .WebhookURL}}
- 办公室会把事件 POST 到 {{.WebhookURL}}，每批最多 50 条，按顺序。
- 每个请求都校验 X-Multica-Signature：值是 "sha256=" 加上用推送密钥对原始请求体做 HMAC-SHA256 的十六进制。
{{- if .WebhookSecret}}
- 推送密钥：{{.WebhookSecret}}
{{- else}}
- 推送密钥不变，沿用之前保存的那个。
{{- end}}
- 推送断过就用 get_events 从最后处理的 next_cursor 补读。
{{- else}}
- 没有配推送地址，靠 get_events 补读：参数 after 填上次的 next_cursor（第一次填 0），has_more 为 true 就接着读。
- 把 next_cursor 持久化保存，重启后从它接着读，不要回到 0。事件保留 7 天。
{{- end}}

4. 工作循环
- create_project 建项目 → open_topic 交给一位成员智能体出方案 → 收到方案事件后 read_plan → request_approval 请主人批准{{if .CanRelease}}（你有放行权，也可以用 approve_plan 批准你读过的那一版）{{else}}（你没有放行权，等主人批准）{{end}}。
- 批准后按方案建工单（create_issue / assign_issue），用 get_events 跟进，完成后 close_issue。
- 指派或 @ 智能体都会启动一次付费运行，只在要真干活时做。

5. 令牌保密
- 令牌就是你的登录凭据，只存进你的密钥配置。
- 不要写进聊天、评论、工单、日志或代码，也不要转给任何人。
- 怀疑泄露就立刻告诉主人，主人会换新令牌。
`))

type botSetupPromptData struct {
	Name          string
	Endpoint      string
	Token         string
	WebhookURL    string
	WebhookSecret string
	CanRelease    bool
}

func renderBotSetupPrompt(d botSetupPromptData) string {
	var b strings.Builder
	if err := botSetupPrompt.Execute(&b, d); err != nil {
		return ""
	}
	return b.String()
}

// botMCPEndpoint is the MCP URL a Bot dials. The configured public API URL
// wins, then the app origin (it proxies /api), then the origin this request
// arrived on — the prompt only goes back to the owner who sent it.
func (h *Handler) botMCPEndpoint(r *http.Request) string {
	base := normalizePublicURL(h.cfg.PublicURL)
	if base == "" {
		base = normalizePublicURL(h.cfg.AppURL)
	}
	if base == "" {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		if p := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0]); p == "https" || p == "http" {
			scheme = p
		}
		base = scheme + "://" + r.Host
	}
	return base + "/api/mcp"
}
