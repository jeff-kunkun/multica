package service

import (
	"strings"
	"testing"
)

func TestScanTransferContent_Canaries(t *testing.T) {
	in := strings.Join([]string{
		"token ghp_CANARYCHATTOKEN12",
		"-----BEGIN OPENSSH PRIVATE KEY-----\nfake\n-----END OPENSSH PRIVATE KEY-----",
		"API_KEY=CANARY_CHAT_5e6f",
		"plain conversation text",
	}, "\n")
	got := ScanTransferContent(in)
	if !got.Changed {
		t.Fatal("expected content to change")
	}
	for _, needle := range []string{"ghp_CANARYCHATTOKEN12", "BEGIN OPENSSH PRIVATE KEY", "CANARY_CHAT_5e6f"} {
		if strings.Contains(got.Text, needle) {
			t.Errorf("leaked %q in %q", needle, got.Text)
		}
	}
	if !strings.Contains(got.Text, "[REDACTED:github_token]") {
		t.Errorf("missing github redaction: %q", got.Text)
	}
	if !strings.Contains(got.Text, "[REDACTED:private_key]") {
		t.Errorf("missing private_key redaction: %q", got.Text)
	}
	if !strings.Contains(got.Text, "[REDACTED:assignment]") {
		t.Errorf("missing assignment redaction: %q", got.Text)
	}
	if !strings.Contains(got.Text, "plain conversation text") {
		t.Errorf("dropped surrounding text: %q", got.Text)
	}
}

func TestScanTransferContent_MulticaPrefixes(t *testing.T) {
	in := "pat mul_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa daemon mdt_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	got := ScanTransferContent(in)
	if strings.Contains(got.Text, "mul_") || strings.Contains(got.Text, "mdt_") {
		t.Errorf("leaked multica token: %q", got.Text)
	}
	if !strings.Contains(got.Text, "[REDACTED:multica_token]") {
		t.Errorf("missing multica redaction: %q", got.Text)
	}
}

func TestShouldExportAttachmentBody(t *testing.T) {
	ok, _ := shouldExportAttachmentBody("image/png", "a.png", 100)
	if !ok {
		t.Fatal("png should export")
	}
	ok, reason := shouldExportAttachmentBody("application/pdf", "a.pdf", 100)
	if ok || reason != "attachment_body_not_exported" {
		t.Fatalf("pdf export=%v reason=%q", ok, reason)
	}
	ok, reason = shouldExportAttachmentBody("image/png", "a.png", TransferAttachmentBodyMax+1)
	if ok || reason != "attachment_body_not_exported" {
		t.Fatalf("oversize export=%v reason=%q", ok, reason)
	}
	if !isTextAttachment("text/plain", "prod.env") {
		t.Fatal("env should be text")
	}
}

func TestTransferIDsAreStableAndWorkspaceScoped(t *testing.T) {
	wsA := "11111111-1111-1111-1111-111111111111"
	wsB := "22222222-2222-2222-2222-222222222222"
	src := "33333333-3333-3333-3333-333333333333"
	a1 := TransferChatSessionID(wsA, src)
	a2 := TransferChatSessionID(wsA, src)
	b := TransferChatSessionID(wsB, src)
	if a1 != a2 {
		t.Fatal("same workspace+source must be stable")
	}
	if a1 == b {
		t.Fatal("different workspaces must not collide")
	}
	if TransferChatMessageID(wsA, src) == TransferChatSessionID(wsA, src) {
		t.Fatal("message and session namespaces must differ")
	}
}
