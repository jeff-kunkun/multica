package service

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMaskSecretsRemovesCredentialsAndKeepsOrdinaryText(t *testing.T) {
	cases := map[string]string{
		"password: hunter2-super":                                                 "hunter2-super",
		"密码=abc12345":                                                             "abc12345",
		"api_key: AKIAABCDEFGHIJKLMNOP":                                           "AKIAABCDEFGHIJKLMNOP",
		"Authorization: Bearer abcdef1234567890xyz":                               "abcdef1234567890xyz",
		"clone https://user:s3cretpass@example.com/repo":                          "s3cretpass",
		"sk-abcdefghijklmnopqrstuvwx":                                             "sk-abcdefghijklmnopqrstuvwx",
		"mul_AbCdEfGhIjKlMnOp":                                                    "mul_AbCdEfGhIjKlMnOp",
		"ghp_abcdefghijklmnopqrstuvwxyz0123":                                      "ghp_abcdefghijklmnopqrstuvwxyz0123",
		"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.abc12345def":            "eyJhbGciOiJIUzI1NiJ9",
		"-----BEGIN RSA PRIVATE KEY-----\nMIIEabc\n-----END RSA PRIVATE KEY-----": "MIIEabc",
		"blob Zm9vYmFyYmF6cXV4MTIzNDU2Nzg5MGFiY2RlZmdoaWo=":                       "Zm9vYmFyYmF6cXV4MTIzNDU2Nzg5MGFiY2RlZmdoaWo",
		// short values and values with spaces, when the field is a credential
		`{"password":"ab"}`:                  "ab",
		`{"password":"review only secret"}`:  "only secret",
		`{"Password": "review only secret"}`: "review",
		`password = 'two words'`:             "two words",
		`密码：“带 空格 的口令”`:                      "空格",
		`DB_PASSWORD=x9`:                     "x9",
		`{"api_key":"a b","note":"fine"}`:    "a b",
	}
	for in, leak := range cases {
		out := MaskSecrets(in)
		if strings.Contains(out, leak) {
			t.Errorf("%q leaked %q: %q", in, leak, out)
		}
		if !strings.Contains(out, maskedSecret) {
			t.Errorf("%q not masked: %q", in, out)
		}
	}
	keep := []string{
		"线上部署失败，已回滚",
		"见 server/internal/service/activity_stream.go 第 42 行",
		"commit 5f0008b8923c9b4a2b6f1d0e3a7c8d9e0f1a2b3c 已合并",
		"https://github.com/jeff-kunkun/multica/pull/667",
		"event 01a12fc8-ec8b-734b-9b03-13f01c9d710d",
		"token 用量统计页已上线",
	}
	for _, in := range keep {
		if got := MaskSecrets(in); got != in {
			t.Errorf("ordinary text changed: %q -> %q", in, got)
		}
	}
}

func TestClassifyActivityLeansHighOnRiskAndNeverCritical(t *testing.T) {
	type c struct {
		f    ActivityFacts
		imp  string
		cats string
	}
	for _, tc := range []c{
		{ActivityFacts{Type: ActivityStatusChanged, ToStatus: "blocked"}, ImportanceHigh, "risk"},
		{ActivityFacts{Type: ActivityStatusChanged, ToStatus: "done"}, ImportanceNormal, "completion,result"},
		{ActivityFacts{Type: ActivityStatusChanged, ToStatus: "in_progress"}, ImportanceLow, "progress"},
		{ActivityFacts{Type: ActivityAssigneeChanged}, ImportanceLow, "info"},
		{ActivityFacts{Type: ActivityCommentCreated, ActorType: "agent", Text: "部署失败，需要回滚"}, ImportanceHigh, "decision,failure,risk"},
		{ActivityFacts{Type: ActivityCommentCreated, ActorType: "agent", Text: "要你做：请确认是否发布到生产"}, ImportanceHigh, "decision,risk"},
		{ActivityFacts{Type: ActivityChatMessage, ActorType: "agent", Text: "已完成，结论如下"}, ImportanceNormal, "completion,result"},
		{ActivityFacts{Type: ActivityCommentCreated, ActorType: "agent", Text: "正在看第二个文件"}, ImportanceNormal, "progress"},
	} {
		imp, cats := ClassifyActivity(tc.f)
		if imp != tc.imp || strings.Join(cats, ",") != tc.cats {
			t.Errorf("%+v -> %s %v, want %s %s", tc.f, imp, cats, tc.imp, tc.cats)
		}
		if imp == ImportanceCritical {
			t.Errorf("critical is reserved for human_needed: %+v", tc.f)
		}
	}
}

func TestNormalizeActivityScope(t *testing.T) {
	got, err := NormalizeActivityScope([]string{" Followed", "owned", "OWNED", ""})
	if err != nil || strings.Join(got, ",") != "owned,followed" {
		t.Fatalf("%v %v", got, err)
	}
	if got, err := NormalizeActivityScope(nil); err != nil || len(got) != 0 {
		t.Fatalf("%v %v", got, err)
	}
	if _, err := NormalizeActivityScope([]string{"all"}); err == nil {
		t.Fatal("unknown scope accepted")
	}
}

func TestMaskSecretsKeepsMaskedJSONValid(t *testing.T) {
	out := MaskSecrets(`{"password":"review only secret","note":"keep me"}`)
	var v map[string]string
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("masked JSON no longer parses: %v: %s", err, out)
	}
	if v["note"] != "keep me" || v["password"] == "review only secret" {
		t.Fatalf("wrong result: %v", v)
	}
	if again := MaskSecrets(out); again != out {
		t.Fatalf("masking is not idempotent: %q -> %q", out, again)
	}
}

func TestScrubPayloadMasksEveryStringButNotLinksOrIds(t *testing.T) {
	in := map[string]any{
		"summary":  `要你决定：{"password":"review only secret"}`,
		"event_id": "01a12fc8-ec8b-734b-9b03-13f01c9d710d",
		"ack_url":  "https://example.com/api/workspaces/01a12fc8-ec8b-734b-9b03-13f01c9d710d/bots/01a12fc8-ec8b-734b-9b03-13f01c9d710d/wake/events/01a12fc8-ec8b-734b-9b03-13f01c9d710d/ack",
		"sequence": int64(9007199254740993),
		"author":   map[string]any{"type": "member", "name": "token: abc"},
		"human_needed": map[string]any{
			"question": `密码 "ab c"`,
			"options":  []HumanNeededOption{{ID: "yes", Label: `{"password":"ab"}`}},
		},
	}
	out, err := ScrubPayload(in)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(out)
	for _, leak := range []string{"review only secret", `"ab c"`, `\"ab\"`, "token: abc"} {
		if strings.Contains(string(raw), leak) {
			t.Errorf("leaked %q: %s", leak, raw)
		}
	}
	if out["event_id"] != in["event_id"] || out["ack_url"] != in["ack_url"] {
		t.Errorf("ids or links were changed: %v", out)
	}
	if !strings.Contains(string(raw), "9007199254740993") {
		t.Errorf("large number lost precision: %s", raw)
	}
}
