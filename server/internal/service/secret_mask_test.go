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

func TestMaskSecretsCoversEscapedQuotesBackslashesAndNewlines(t *testing.T) {
	// A password the way json.Marshal writes it: escaped quote, backslash and
	// newline in the value, then a tail that must not survive.
	for _, pw := range []string{"ab\"TAILONE9", `x\y\"TAILTWO8`, "line1\nTAILTHREE7", `a\\"b'c TAILFOUR6`} {
		field, _ := json.Marshal(map[string]string{"password": pw})
		for name, text := range map[string]string{
			"json":      string(field),
			"in text":   "改了 " + string(field) + " 完成",
			"in string": func() string { b, _ := json.Marshal(string(field)); return string(b) }(),
		} {
			got := MaskSecrets(text)
			if strings.Contains(got, "TAIL") {
				t.Errorf("%s %q: tail leaked: %s", name, pw, got)
			}
		}
	}
	// A raw (undecoded) quoted value with a newline inside.
	if got := MaskSecrets("password: \"first\nTAILFIVE5\" done"); strings.Contains(got, "TAILFIVE5") {
		t.Errorf("newline value leaked: %s", got)
	}
	// Ordinary text around it stays.
	if got := MaskSecrets(`他说 "你好" 然后 password: "p\"q" 结束`); !strings.Contains(got, "结束") || !strings.Contains(got, "你好") || strings.Contains(got, "p\\") {
		t.Errorf("unexpected: %s", got)
	}
	// Masked JSON stays valid.
	field, _ := json.Marshal(map[string]string{"password": "ab\"cd\\ef"})
	var back map[string]string
	if err := json.Unmarshal([]byte(MaskSecrets(string(field))), &back); err != nil {
		t.Errorf("masked JSON no longer parses: %v", err)
	}
}

func TestScrubPayloadDoesNotTrustAKeyName(t *testing.T) {
	const secret = "sk-ReviewOnlySyntheticKey1234567890"
	in := map[string]any{
		"event_id": "01a12fc8-ec8b-734b-9b03-13f01c9d710d",
		"time":     "2026-10-12T12:00:00Z",
		"ack_url":  "https://example.com/api/workspaces/01a12fc8-ec8b-734b-9b03-13f01c9d710d/bots/01a12fc8-ec8b-734b-9b03-13f01c9d710d/wake/events/01a12fc8-ec8b-734b-9b03-13f01c9d710d/ack",
		"web_url":  "https://example.com/" + secret + "/issues",
		"id":       secret,
		"asker_id": "password=" + secret,
		"human_needed": map[string]any{
			"kind":    "ask_human",
			"options": []HumanNeededOption{{ID: secret, Label: "yes"}, {ID: "a", Label: "no"}},
		},
	}
	out, err := ScrubPayload(in)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(out)
	if strings.Contains(string(raw), "ReviewOnlySynthetic") {
		t.Errorf("secret kept because of the key it sat under: %s", raw)
	}
	for _, k := range []string{"event_id", "time", "ack_url"} {
		if out[k] != in[k] {
			t.Errorf("%s changed: %v", k, out[k])
		}
	}
	if !strings.Contains(string(raw), `"id":"a"`) {
		t.Errorf("ordinary option id lost: %s", raw)
	}
}
