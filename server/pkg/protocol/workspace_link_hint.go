package protocol

// WorkspaceLinkHint is claim-only discovery metadata. Side describes the current
// workspace (source, viewer, or both); it grants no access and carries no project data.
type WorkspaceLinkHint struct {
	Name string `json:"name"`
	Side string `json:"side"`
}
