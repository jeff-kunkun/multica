package handler

// RepoReach is the single decision returned when the server determines how a
// repository can be reached. Keeping this decision pure makes settings,
// connection prompts, and delivery lookup agree on the same modes.
type RepoReach struct {
	Mode       string `json:"mode"`
	NextAction string `json:"next_action,omitempty"`
}

// DecideRepoReach chooses the strongest available route. App coverage is per
// repository (an installation for account A must not cover account B).
func DecideRepoReach(provider string, hasToken, hasApp, hasCLI bool) RepoReach {
	if hasToken {
		return RepoReach{Mode: "token"}
	}
	if provider == "github" && hasApp {
		return RepoReach{Mode: "app"}
	}
	if hasCLI {
		return RepoReach{Mode: "cli"}
	}
	next := "add_connection"
	if provider == "github" {
		next = "install_app"
	}
	return RepoReach{Mode: "none", NextAction: next}
}
