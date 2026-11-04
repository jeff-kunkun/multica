package issuewatchdog

import (
	"strings"
	"testing"
)

func TestDeliverySummaryMasksSecretsButKeepsEvidence(t *testing.T) {
	sha := "13469735c930b6dbeade3b0c1fcf4bbd7c3b8c9f"
	content := "交付：\n- PR https://github.com/o/r/pull/9。\n- Authorization: Bearer abcdefghijklmnop\n" +
		"- api_key=sk-live-1234567890abcdef, password: hunter2\n- ghp_abcdefghijklmnopqrstuvwxyz 提交 " + sha + strings.Repeat(" 很长", 10)
	content += ` {"password":"pa55-json-value"} 见 https://reviewer:pa55-url-value@example.invalid/result`
	got := summarize(content)
	for _, secret := range []string{"pa55-json-value", "pa55-url-value", "abcdefghijklmnop", "sk-live-1234567890abcdef", "hunter2", "ghp_abcdefghijklmnopqrstuvwxyz"} {
		if strings.Contains(got, secret) {
			t.Fatalf("summary leaks %q: %s", secret, got)
		}
	}
	if !strings.Contains(got, sha) || !strings.Contains(got, "https://github.com/o/r/pull/9") || strings.Contains(got, "\n") {
		t.Fatalf("summary lost evidence or kept newlines: %s", got)
	}
	if n := len([]rune(got)); n > summaryMaxRunes+1 {
		t.Fatalf("summary is %d runes", n)
	}
}

func TestEvidenceLinksTrimDedupeAndCap(t *testing.T) {
	content := "见 https://a.example/x。 再看 (https://a.example/x) 和 https://b.example/y?token=abcd1234&page=2, " +
		"https://c.example/1 https://c.example/2 https://c.example/3 https://c.example/4"
	got := evidenceLinks(content)
	want := []string{"https://a.example/x", "https://b.example/y?token=[已打码]", "https://c.example/1", "https://c.example/2", "https://c.example/3"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("links = %v, want %v", got, want)
	}
}

func TestOnlyDeliveredAndBlockedWakeAtOnce(t *testing.T) {
	for status, want := range map[string]bool{"in_review": true, "blocked": true, "in_progress": false, "todo": false, "done": false, "cancelled": false} {
		if WakesOnStatus(status) != want {
			t.Fatalf("WakesOnStatus(%q) = %v", status, !want)
		}
	}
}

func TestEvidenceLinksMaskURLPasswords(t *testing.T) {
	got := evidenceLinks("结果 https://reviewer:pa55-url-value@example.invalid/result")
	if len(got) != 1 || strings.Contains(got[0], "pa55-url-value") {
		t.Fatalf("links = %v", got)
	}
}

func TestMaskingKeepsCommitLinksAndOldCoverage(t *testing.T) {
	// A made-up opaque path segment, built so scanners do not take the fixture for a key.
	opaque := strings.Repeat("AbCd", 2) + "0123456789" + strings.Repeat("GhIj", 2) + "0123456789MnOp"
	sha := "13469735c930b6dbeade3b0c1fcf4bbd7c3b8c9f"
	commit := "https://github.com/o/r/commit/" + sha
	pr := "https://github.com/jeff-kunkun/multica-office/pull/61"
	issue := "https://ai.ferryway.cc/kunkunya/issues/01a1a4ad-5d5f-7661-98c8-2dd5a6bda734"
	content := "提交 " + commit + " " + pr + " " + issue + " Cookie: sid=ab12 token_value=short1 mcp_abcdefghijklmnop rk_abcdefghijklmnop " +
		`{"password":"pa55-json-value"} {"password":"https://example.com/docs pa55-tail"} https://example.com/mat_AbCdEfGhIjKlMnOp ` +
		"https://example.com/callback#access_token=short-test-secret " +
		"https://h.example/hook/" + opaque + "?page=2&api_key=v4lue9"
	got := summarize(content)
	for _, secret := range []string{"sid=ab12", "short1", "mcp_abcdefghijklmnop", "rk_abcdefghijklmnop", "pa55-json-value",
		"pa55-tail", "mat_AbCdEfGhIjKlMnOp", "short-test-secret", opaque, "v4lue9"} {
		if strings.Contains(got, secret) {
			t.Fatalf("summary leaks %q: %s", secret, got)
		}
	}
	for _, evidence := range []string{commit, pr, issue} {
		if !strings.Contains(got, evidence) {
			t.Fatalf("summary lost %s: %s", evidence, got)
		}
	}
	links := evidenceLinks(content)
	if len(links) != maxEvidenceLinks || links[0] != commit || links[1] != pr || links[2] != issue {
		t.Fatalf("links = %v", links)
	}
	for _, link := range evidenceLinks(content + " " + strings.Repeat("x", 0)) {
		for _, secret := range []string{"mat_AbCdEfGhIjKlMnOp", "short-test-secret", "v4lue9", "pa55-tail"} {
			if strings.Contains(link, secret) {
				t.Fatalf("link leaks %q: %s", secret, link)
			}
		}
	}
}

func TestMaskingCoversIDsInCredentialContextAndSignedLinks(t *testing.T) {
	uuid := "01a1a4ad-5d5f-7661-98c8-2dd5a6bda734"
	hex := strings.Repeat("0123456789", 4)
	run := "https://github.com/ExampleOrg/example-service/actions/runs/12345678901"
	content := "Bearer " + uuid + " 和 Bearer " + hex + " 和 sk-" + hex + " 见 " + run +
		" https://x.example/a?auth=demo-value1&sig=demo-value2&session=demo-value3&page=4"
	got := summarize(content)
	for _, secret := range []string{uuid, hex, "demo-value1", "demo-value2", "demo-value3"} {
		if strings.Contains(got, secret) {
			t.Fatalf("summary leaks %q: %s", secret, got)
		}
	}
	links := evidenceLinks(content)
	if !strings.Contains(got, run) || len(links) != 2 || links[0] != run || links[1] != "https://x.example/a?auth="+secretMark {
		t.Fatalf("summary %s\nlinks %v", got, links)
	}
}

func TestLinkQueryValuesStayWholeAndOrdinaryNamesStay(t *testing.T) {
	data := strings.Join([]string{"AbCdEf12", "GhIjKl34", "MnOpQr56", "StUvWx78", "YzAbCd90"}, "/")
	results := "https://example.com/results?author=alice&assigned=bob&design=x"
	content := "https://example.com/download?data=" + data + " " + results + " https://s3.example/o?X-Amz-Signature=demo-value7&access_token=demo-value8"
	got := summarize(content)
	for _, secret := range []string{data, "demo-value7", "demo-value8"} {
		if strings.Contains(got, secret) {
			t.Fatalf("summary leaks %q: %s", secret, got)
		}
	}
	if !strings.Contains(got, results) {
		t.Fatalf("ordinary filter link altered: %s", got)
	}
}

func TestCompoundAndSemicolonCredentialParams(t *testing.T) {
	content := "https://x.example/a?authCode=demo-value1&sessionKey=demo-value2&JSESSIONID=demo-value3&PHPSESSID=demo-value4 " +
		"https://x.example/b?page=1;sig=demo-value5 https://x.example/c?author=alice&keyword=go&assigned=bob"
	got := summarize(content)
	for _, secret := range []string{"demo-value1", "demo-value2", "demo-value3", "demo-value4", "demo-value5"} {
		if strings.Contains(got, secret) {
			t.Fatalf("summary leaks %q: %s", secret, got)
		}
	}
	if !strings.Contains(got, "page=1;") || !strings.Contains(got, "?author=alice&keyword=go&assigned=bob") {
		t.Fatalf("ordinary parameters altered: %s", got)
	}
}

func TestCredentialValueWithSeparatorsIsMaskedToTheEnd(t *testing.T) {
	got := summarize("见 https://x.example/a?page=4&sig=demo-value1;demo-value2&more=demo-value3#frag")
	for _, secret := range []string{"demo-value1", "demo-value2", "demo-value3"} {
		if strings.Contains(got, secret) {
			t.Fatalf("summary leaks %q: %s", secret, got)
		}
	}
	if !strings.Contains(got, "https://x.example/a?page=4&sig="+secretMark) {
		t.Fatalf("summary = %s", got)
	}
}

func TestCredentialParamsOutsideRecognisedLinks(t *testing.T) {
	content := "见 /download?sig=demo-value1 和 HTTPS://x.example/a?sig=demo-value2 和 https://x.example/a(v2)?sig=demo-value3 " +
		"和 https://x.example/a?sig=demo(value4)tail 和 (https://x.example/b?page=2&token=demo-value5) 以及 https://x.example/c?page=3"
	got := summarize(content)
	for _, secret := range []string{"demo-value1", "demo-value2", "demo-value3", "value4", "tail", "demo-value5"} {
		if strings.Contains(got, secret) {
			t.Fatalf("summary leaks %q: %s", secret, got)
		}
	}
	if !strings.Contains(got, "https://x.example/c?page=3") || !strings.Contains(got, "/download?sig="+secretMark) {
		t.Fatalf("summary = %s", got)
	}
	for _, link := range evidenceLinks(content) {
		if strings.Contains(link, "demo") || strings.Contains(link, "value4") {
			t.Fatalf("link leaks: %s", link)
		}
	}
}
