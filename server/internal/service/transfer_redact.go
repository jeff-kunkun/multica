package service

import (
	"path"
	"regexp"
	"strings"
	"unicode"
)

const secretContentReason = "content_pattern"

type ContentScanHit struct {
	Kind  string
	Count int
}

type ContentScanResult struct {
	Text    string
	Hits    []ContentScanHit
	Changed bool
}

var (
	rePrivateKey = regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?-----END [A-Z0-9 ]*PRIVATE KEY-----`)
	reAnthropic  = regexp.MustCompile(`sk-ant-[A-Za-z0-9_-]{16,}`)
	reOpenAIProj = regexp.MustCompile(`sk-proj-[A-Za-z0-9_-]{16,}`)
	reOpenAI     = regexp.MustCompile(`sk-[A-Za-z0-9]{20,}`)
	reGitHub     = regexp.MustCompile(`(?:ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9_]{16,}|github_pat_[A-Za-z0-9_]{16,}`)
	reSlack      = regexp.MustCompile(`xox[bpas]-[A-Za-z0-9-]{10,}|xapp-[A-Za-z0-9-]{10,}`)
	reAWS        = regexp.MustCompile(`A(?:KIA|SIA)[A-Z0-9]{16}`)
	reGoogle     = regexp.MustCompile(`AIza[0-9A-Za-z_-]{35}`)
	reJWT        = regexp.MustCompile(`eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+`)
	reMultica    = regexp.MustCompile(`(?:mul_|mdt_|mat_|mcn_)[A-Fa-f0-9]{20,}`)
	reBearer     = regexp.MustCompile(`(?i)(?:Authorization:\s*)?Bearer\s+[A-Za-z0-9._~+/=-]{16,}`)
	reAssign     = regexp.MustCompile(`(?i)(?:^|[\s"'` + "`" + `{,])((?:token|secret|password|passwd|api[_-]?key|apikey|authorization|private[_-]?key|credential)s?)\s*[=:]\s*([^\s"'` + "`" + `,}]{8,})`)
)

type scanPattern struct {
	kind string
	re   *regexp.Regexp
}

func contentScanPatterns() []scanPattern {
	return []scanPattern{
		{"private_key", rePrivateKey},
		{"anthropic_key", reAnthropic},
		{"openai_key", reOpenAIProj},
		{"openai_key", reOpenAI},
		{"github_token", reGitHub},
		{"slack_token", reSlack},
		{"aws_access_key", reAWS},
		{"google_api_key", reGoogle},
		{"jwt", reJWT},
		{"multica_token", reMultica},
		{"bearer", reBearer},
	}
}

// ScanTransferContent replaces high-confidence secret material with
// [REDACTED:<kind>] placeholders. There is no opt-out.
func ScanTransferContent(text string) ContentScanResult {
	if text == "" {
		return ContentScanResult{Text: text}
	}
	out := text
	counts := map[string]int{}
	order := []string{}
	for _, p := range contentScanPatterns() {
		n := 0
		out = p.re.ReplaceAllStringFunc(out, func(string) string {
			n++
			return "[REDACTED:" + p.kind + "]"
		})
		if n > 0 {
			counts[p.kind] += n
			if counts[p.kind] == n {
				order = append(order, p.kind)
			}
		}
	}
	out, assignHits := redactAssignments(out)
	if assignHits > 0 {
		counts["assignment"] += assignHits
		if counts["assignment"] == assignHits {
			order = append(order, "assignment")
		}
	}
	hits := make([]ContentScanHit, 0, len(order))
	for _, kind := range order {
		hits = append(hits, ContentScanHit{Kind: kind, Count: counts[kind]})
	}
	return ContentScanResult{Text: out, Hits: hits, Changed: out != text}
}

func redactAssignments(text string) (string, int) {
	n := 0
	out := reAssign.ReplaceAllStringFunc(text, func(m string) string {
		sub := reAssign.FindStringSubmatch(m)
		if len(sub) < 3 {
			return m
		}
		key := sub[1]
		if !secretKeyName(key) {
			return m
		}
		n++
		prefix := m[:len(m)-len(sub[2])]
		return prefix + "[REDACTED:assignment]"
	})
	return out, n
}

func secretOmittedFromHits(entity, sourceID, field string, hits []ContentScanHit) SecretOmitted {
	kinds := make([]string, 0, len(hits))
	count := 0
	for _, h := range hits {
		kinds = append(kinds, h.Kind)
		count += h.Count
	}
	return SecretOmitted{
		Entity:   entity,
		SourceID: sourceID,
		Field:    field,
		Reason:   secretContentReason,
		Hint:     map[string]any{"kinds": kinds, "count": count},
	}
}

func isMaskedSecretValue(v string) bool {
	s := strings.TrimSpace(v)
	return s == "***" || s == "****" || s == "[REDACTED]"
}

func isTextAttachment(contentType, filename string) bool {
	ct := strings.ToLower(strings.TrimSpace(contentType))
	if strings.HasPrefix(ct, "text/") {
		return true
	}
	switch ct {
	case "application/json", "application/x-yaml", "application/xml", "application/toml":
		return true
	}
	ext := strings.ToLower(path.Ext(filename))
	switch ext {
	case ".md", ".txt", ".env", ".json", ".yaml", ".yml", ".toml", ".ini", ".log", ".csv":
		return true
	}
	return false
}

func isImageAttachment(contentType string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(contentType)), "image/")
}

func shouldExportAttachmentBody(contentType, filename string, sizeBytes int64) (export bool, reason string) {
	if sizeBytes > TransferAttachmentBodyMax {
		return false, "attachment_body_not_exported"
	}
	if isImageAttachment(contentType) {
		return true, ""
	}
	if isTextAttachment(contentType, filename) {
		return true, ""
	}
	return false, "attachment_body_not_exported"
}

func looksLikePrintableSecretKey(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) && r != '\t' && r != '\n' && r != '\r' {
			return false
		}
	}
	return true
}
