package service

import (
	"bytes"
	"encoding/json"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Credential masking for everything that leaves for a Bot (human-needed
// pushes and pull lists, office events, bot-write audit, owner confirmation):
// a Bot can ask the person for a value, but cannot take back one it was sent.

const maskedSecret = "[已打码]"

var (
	secretPEM    = regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?(-----END [A-Z ]*PRIVATE KEY-----|$)`)
	secretPrefix = regexp.MustCompile(`\b(?:(?:mul|mat|msk|mpat|mbt)_[A-Za-z0-9_\-]{8,}|sk-[A-Za-z0-9_\-]{16,}|gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|AKIA[0-9A-Z]{16}|xox[abprs]-[A-Za-z0-9\-]{10,}|AIza[0-9A-Za-z_\-]{30,}|eyJ[A-Za-z0-9_\-]{8,}\.[A-Za-z0-9_\-]{8,}\.[A-Za-z0-9_\-]{8,})`)
	secretBearer = regexp.MustCompile(`(?i)\b(bearer|basic)\s+[A-Za-z0-9._\-~+/=]{8,}`)
	// A credential field is "name, separator, value". A quoted value is masked
	// whole, whatever it holds (spaces, one character, a newline, an escaped
	// quote or backslash as in a JSON string); an unquoted one runs to the next
	// separator and is masked from its first character: "ab" is a password like
	// any other. The name may itself sit between escaped quotes (JSON inside a
	// JSON string).
	secretAssignQuoted = regexp.MustCompile(`(?i)(password|passwd|pwd|passphrase|secret|token|api[_\- ]?key|apikey|access[_\- ]?key|private[_\- ]?key|credential|authorization|webhook[_\- ]?key|密码|口令|密钥|令牌|钥匙|凭据)(\\*["']?\s*[:=：]\s*)(\\*"(?:[^"\\]|\\[\s\S])*"|\\*'(?:[^'\\]|\\[\s\S])*'|“[^”]*”|「[^」]*」|‘[^’]*’)`)
	secretAssign       = regexp.MustCompile(`(?i)(password|passwd|pwd|passphrase|secret|token|api[_\- ]?key|apikey|access[_\- ]?key|private[_\- ]?key|credential|authorization|webhook[_\- ]?key|密码|口令|密钥|令牌|钥匙|凭据)(\\*["']?\s*[:=：]\s*(?:\\*["'“「‘])?)((?:\\[\s\S]|[^\s"',;，；”」’\\])+)`)
	secretURLPwd       = regexp.MustCompile(`(://[^/\s:@]+:)[^@\s/]+@`)
	secretLong         = regexp.MustCompile(`[A-Za-z0-9_\-+/=]{32,}`)
	gitSHA             = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

// MaskSecrets replaces anything in text that looks like a credential. It errs
// toward masking: a Bot can ask the person for a value, but cannot take back
// one that already left. A git commit id and ordinary paths are kept.
func MaskSecrets(text string) string {
	if text == "" {
		return text
	}
	text = secretPEM.ReplaceAllString(text, maskedSecret)
	text = secretPrefix.ReplaceAllString(text, maskedSecret)
	text = secretBearer.ReplaceAllString(text, "$1 "+maskedSecret)
	text = secretAssignQuoted.ReplaceAllStringFunc(text, maskQuotedValue)
	text = secretAssign.ReplaceAllString(text, "$1$2"+maskedSecret)
	text = secretURLPwd.ReplaceAllString(text, "${1}"+maskedSecret+"@")
	text = secretLong.ReplaceAllStringFunc(text, func(m string) string {
		if looksLikeSecretRun(m) {
			return maskedSecret
		}
		return m
	})
	return text
}

// maskQuotedValue masks the quoted value of a credential field and keeps the
// quote marks, so masked JSON stays valid JSON.
func maskQuotedValue(m string) string {
	sub := secretAssignQuoted.FindStringSubmatch(m)
	if sub == nil {
		return m
	}
	lead := sub[3][:len(sub[3])-len(strings.TrimLeft(sub[3], `\`))]
	quoted := []rune(strings.TrimLeft(sub[3], `\`))
	if len(quoted) <= 2 {
		return m // an empty value hides nothing
	}
	return sub[1] + sub[2] + lead + string(quoted[0]) + maskedSecret + string(quoted[len(quoted)-1])
}

// looksLikeSecretRun decides about one long run of token characters: a path
// or an identifier made of words is fine, a run of mixed letters and digits is
// not.
func looksLikeSecretRun(m string) bool {
	if gitSHA.MatchString(m) {
		return false
	}
	longest, cur := 0, 0
	hasDigit, hasLetter, hasUpper, hasLower := false, false, false, false
	best := struct{ digit, letter bool }{}
	runDigit, runLetter := false, false
	flush := func() {
		if cur > longest {
			longest = cur
			best.digit, best.letter = runDigit, runLetter
		}
		cur, runDigit, runLetter = 0, false, false
	}
	for _, r := range m {
		switch {
		case r >= '0' && r <= '9':
			cur++
			runDigit, hasDigit = true, true
		case r >= 'a' && r <= 'z':
			cur++
			runLetter, hasLetter, hasLower = true, true, true
		case r >= 'A' && r <= 'Z':
			cur++
			runLetter, hasLetter, hasUpper = true, true, true
		default:
			flush()
		}
	}
	flush()
	if longest >= 24 && best.digit && best.letter {
		return true
	}
	return len(m) >= 40 && hasDigit && hasLetter && hasUpper && hasLower
}

// ScrubPayload is the last gate before the network. The builders mask the free
// text they know about; this walks the finished body as it will be sent
// (summary, author, titles, options, the human_needed block, whatever a later
// change adds) and masks every string in it, so protection does not depend on
// each builder remembering. The body goes through JSON first, which flattens
// typed values (option structs, slices of strings) into what the Bot receives.
func ScrubPayload(fields map[string]any) (map[string]any, error) {
	raw, err := json.Marshal(fields)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var out map[string]any
	if err := dec.Decode(&out); err != nil {
		return nil, err
	}
	scrubValue(out)
	return out, nil
}

// ScrubFields masks every string in v in place and returns it.
func ScrubFields(v any) any { return scrubValue(v) }

// scrubString masks one string of a body. What it keeps verbatim it keeps by
// its shape, never by the key it sits under: a key name says nothing about
// who wrote the value (an option id is whatever the asker typed). A real
// UUID or timestamp cannot hold a credential; a link the server built is
// masked piece by piece, so its path is not read as one long token and a
// secret smuggled into it is still caught.
func scrubString(s string) string {
	switch {
	case s == "":
		return s
	case uuidShape.MatchString(s):
		return s
	case isRFC3339(s):
		return s
	case isPlainLink(s):
		return maskLink(s)
	}
	return MaskSecrets(s)
}

var uuidShape = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func isRFC3339(s string) bool {
	if len(s) < 20 || len(s) > 40 {
		return false
	}
	_, err := time.Parse(time.RFC3339Nano, s)
	return err == nil
}

// isPlainLink is an http(s) address with no spaces and no user:password part.
func isPlainLink(s string) bool {
	if !strings.HasPrefix(s, "https://") && !strings.HasPrefix(s, "http://") {
		return false
	}
	if strings.ContainsAny(s, " \t\r\n") {
		return false
	}
	u, err := url.Parse(s)
	return err == nil && u.User == nil && u.Host != ""
}

// maskLink masks each "/"-separated piece on its own.
func maskLink(s string) string {
	parts := strings.Split(s, "/")
	for i, p := range parts {
		parts[i] = MaskSecrets(p)
	}
	return strings.Join(parts, "/")
}

func scrubValue(v any) any {
	switch t := v.(type) {
	case string:
		return scrubString(t)
	case map[string]any:
		for k, val := range t {
			t[k] = scrubValue(val)
		}
		return t
	case []any:
		for i, val := range t {
			t[i] = scrubValue(val)
		}
		return t
	}
	return v
}
