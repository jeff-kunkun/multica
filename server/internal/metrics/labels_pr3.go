package metrics

import (
	"encoding/json"
	"strings"
)

// PR3 normalizers. All inputs go through fixed allow-lists so a misbehaving
// caller cannot inflate metric cardinality. Every "unknown" / "other" bucket
// keeps the series count bounded even under enum drift.

var (
	knownPlatforms = map[string]string{
		"server":  "server",
		"web":     "web",
		"desktop": "desktop",
		"cli":     "cli",
		"mobile":  "mobile",
		"ios":     "ios",
		"unknown": "unknown",
	}

	// knownSignupSources is the fixed bucket set for the signup_source
	// metric label. The PostHog event still ships the raw cookie value
	// so analytics keeps the long tail; the Prometheus side gets the
	// bucketed version so cardinality stays bounded even if a misbehaving
	// frontend writes a unique-per-visitor cookie. Empty cookie collapses
	// to "direct" (no attribution = direct visit), unknown channels to
	// "other".
	knownSignupSources = map[string]string{
		"direct":       "direct",
		"google":       "google",
		"bing":         "bing",
		"duckduckgo":   "duckduckgo",
		"twitter":      "twitter",
		"x":            "twitter",
		"linkedin":     "linkedin",
		"facebook":     "facebook",
		"instagram":    "instagram",
		"github":       "github",
		"gitlab":       "gitlab",
		"hacker_news":  "hacker_news",
		"hackernews":   "hacker_news",
		"reddit":       "reddit",
		"youtube":      "youtube",
		"discord":      "discord",
		"slack":        "slack",
		"product_hunt": "product_hunt",
		"producthunt":  "product_hunt",
		"medium":       "medium",
		"dev_to":       "dev_to",
		"devto":        "dev_to",
		"email":        "email",
		"newsletter":   "email",
		"organic":      "organic",
		"referral":     "referral",
		"partner":      "partner",
		"affiliate":    "affiliate",
		"ad":           "paid",
		"ads":          "paid",
		"paid":         "paid",
		"cpc":          "paid",
		"sem":          "paid",
		"other":        "other",
	}

	knownEmailRateLimitActions = map[string]string{
		"workspace_invitation": "workspace_invitation",
		"other":                "other",
	}

	knownEmailRateLimitGates = map[string]string{
		"actor":     "actor",
		"workspace": "workspace",
		"recipient": "recipient",
		"other":     "other",
	}

	knownGithubEventKinds = map[string]string{
		"pull_request":              "pull_request",
		"pull_request_review":       "pull_request_review",
		"issues":                    "issues",
		"issue_comment":             "issue_comment",
		"push":                      "push",
		"installation":              "installation",
		"installation_repositories": "installation_repositories",
		"check_run":                 "check_run",
		"check_suite":               "check_suite",
		"ping":                      "ping",
		"other":                     "other",
	}

	knownGithubActions = map[string]string{
		"opened":      "opened",
		"closed":      "closed",
		"reopened":    "reopened",
		"merged":      "merged",
		"synchronize": "synchronize",
		"edited":      "edited",
		"submitted":   "submitted",
		"created":     "created",
		"deleted":     "deleted",
		"labeled":     "labeled",
		"unlabeled":   "unlabeled",
		"assigned":    "assigned",
		"unassigned":  "unassigned",
		"requested":   "requested",
		"completed":   "completed",
		"none":        "none",
		"other":       "other",
	}

	knownGithubPRReviewResults = map[string]string{
		"approved":          "approved",
		"changes_requested": "changes_requested",
		"commented":         "commented",
		"dismissed":         "dismissed",
		"other":             "other",
	}

	knownDaemonWSKinds = map[string]string{
		"heartbeat":     "heartbeat",
		"task_claim":    "task_claim",
		"task_complete": "task_complete",
		"task_usage":    "task_usage",
		"task_progress": "task_progress",
		"task_messages": "task_messages",
		"log":           "log",
		"other":         "other",
	}

	knownFeedbackKinds = map[string]string{
		"bug":     "bug",
		"feature": "feature",
		"general": "general",
		"praise":  "praise",
		"other":   "other",
	}

	// Evidence kinds for multica_chat_output_local_path_total (MUL-4899). A
	// closed allowlist is what keeps the offending path out of Prometheus: the
	// caller passes a classification, never a fragment of the reply.
	knownChatOutputLocalPathKinds = map[string]string{
		"file_url":     "file_url",
		"workdir_path": "workdir_path",
	}
)

func normalizeFromAllowList(value string, allowList map[string]string, fallback string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if normalized, ok := allowList[value]; ok {
		return normalized
	}
	return fallback
}

func NormalizePlatform(value string) string {
	return normalizeFromAllowList(value, knownPlatforms, "unknown")
}

// NormalizeSignupSource buckets the raw multica_signup_source cookie payload
// into the fixed signup channel allow-list. The cookie carries free-form
// JSON (utm_source / utm_medium / referrer) on the PostHog side; here we
// only need a bounded label, so we look at utm_source / source / referrer
// fields when present, otherwise the bare string. Empty -> "direct".
// Anything not in the allow-list -> "other".
func NormalizeSignupSource(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "direct"
	}
	// JSON shape: {"utm_source":"...","utm_medium":"...","referrer":"..."}
	if strings.HasPrefix(value, "{") {
		var parsed map[string]any
		if err := json.Unmarshal([]byte(value), &parsed); err == nil {
			for _, key := range []string{"utm_source", "source", "referrer", "ref"} {
				if raw, ok := parsed[key]; ok {
					if s, ok := raw.(string); ok && strings.TrimSpace(s) != "" {
						value = s
						break
					}
				}
			}
		}
	}
	return normalizeFromAllowList(canonicaliseSignupChannel(value), knownSignupSources, "other")
}

// canonicaliseSignupChannel collapses the raw signup-source string into a
// shape the allow-list can match: lowercase, trimmed, host-only for URL-ish
// values, and with a few obvious aliases unified ("twitter.com" -> "twitter").
// We deliberately keep this defensive — the cookie is set client-side, so any
// shape is possible.
func canonicaliseSignupChannel(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return ""
	}
	// Strip an optional URL scheme so "https://twitter.com/foo" -> "twitter.com/foo".
	for _, scheme := range []string{"https://", "http://", "//"} {
		if strings.HasPrefix(value, scheme) {
			value = strings.TrimPrefix(value, scheme)
			break
		}
	}
	// Take just the host segment.
	if i := strings.IndexAny(value, "/?#"); i >= 0 {
		value = value[:i]
	}
	value = strings.TrimPrefix(value, "www.")
	// Map well-known hostnames to their channel bucket.
	hostAliases := map[string]string{
		"google.com":           "google",
		"google.co.uk":         "google",
		"bing.com":             "bing",
		"duckduckgo.com":       "duckduckgo",
		"twitter.com":          "twitter",
		"x.com":                "twitter",
		"t.co":                 "twitter",
		"linkedin.com":         "linkedin",
		"lnkd.in":              "linkedin",
		"facebook.com":         "facebook",
		"fb.com":               "facebook",
		"instagram.com":        "instagram",
		"github.com":           "github",
		"gitlab.com":           "gitlab",
		"news.ycombinator.com": "hacker_news",
		"reddit.com":           "reddit",
		"old.reddit.com":       "reddit",
		"youtube.com":          "youtube",
		"youtu.be":             "youtube",
		"discord.com":          "discord",
		"discord.gg":           "discord",
		"slack.com":            "slack",
		"producthunt.com":      "product_hunt",
		"medium.com":           "medium",
		"dev.to":               "dev_to",
	}
	if mapped, ok := hostAliases[value]; ok {
		return mapped
	}
	return value
}

func NormalizeEmailRateLimitAction(value string) string {
	return normalizeFromAllowList(value, knownEmailRateLimitActions, "other")
}

func NormalizeEmailRateLimitGate(value string) string {
	return normalizeFromAllowList(value, knownEmailRateLimitGates, "other")
}

func NormalizeGithubEventKind(value string) string {
	return normalizeFromAllowList(value, knownGithubEventKinds, "other")
}

func NormalizeGithubAction(value string) string {
	if strings.TrimSpace(value) == "" {
		return "none"
	}
	return normalizeFromAllowList(value, knownGithubActions, "other")
}

func NormalizeGithubPRReviewResult(value string) string {
	return normalizeFromAllowList(value, knownGithubPRReviewResults, "other")
}

func NormalizeDaemonWSKind(value string) string {
	return normalizeFromAllowList(value, knownDaemonWSKinds, "other")
}

func NormalizeFeedbackKind(value string) string {
	return normalizeFromAllowList(value, knownFeedbackKinds, "other")
}

func NormalizeChatOutputLocalPathKind(value string) string {
	return normalizeFromAllowList(value, knownChatOutputLocalPathKinds, "other")
}
