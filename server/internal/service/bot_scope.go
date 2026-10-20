package service

import (
	"errors"
	"fmt"
	"strings"
)

// The per-person activity scope on a Bot binding. The DENE-1935/1936 activity
// stream that first read it is retired; office events still deliver only to
// people whose scope holds ActivityScopeAll.
const (
	// HumanEventActivity is the event type the retired stream queued. Rows
	// of it still in the outbox are cancelled by the worker, never sent.
	HumanEventActivity = "activity"

	ActivityScopeOwned    = "owned"
	ActivityScopeFollowed = "followed"
	// ActivityScopeAll is everything the person can see in the workspace.
	ActivityScopeAll = "owner_visible_all"

	// The human-needed push carries the top tags of the Bot contract.
	ImportanceCritical = "critical"
	CategoryNeedsHuman = "needs_human"
)

// ErrInvalidActivityScope is returned for a scope name the server does not know.
var ErrInvalidActivityScope = errors.New("activity scope must be owned, followed and/or owner_visible_all")

// NormalizeActivityScope validates and orders a scope list. Empty is valid: it
// switches delivery off.
func NormalizeActivityScope(in []string) ([]string, error) {
	seen := map[string]bool{}
	for _, raw := range in {
		s := strings.ToLower(strings.TrimSpace(raw))
		if s == "" {
			continue
		}
		if s != ActivityScopeOwned && s != ActivityScopeFollowed && s != ActivityScopeAll {
			return nil, fmt.Errorf("%w: %q", ErrInvalidActivityScope, raw)
		}
		seen[s] = true
	}
	out := make([]string, 0, len(seen))
	for _, s := range []string{ActivityScopeOwned, ActivityScopeFollowed, ActivityScopeAll} {
		if seen[s] {
			out = append(out, s)
		}
	}
	return out, nil
}
