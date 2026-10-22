package main

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"time"

	"github.com/multica-ai/multica/server/internal/cli"
)

// seatReport is what `issue status <id> blocked` learned about the executor
// slot of a ticket that had none.
type seatReport struct {
	Seated       bool   `json:"seated"`
	AssigneeType string `json:"assignee_type,omitempty"`
	AssigneeID   string `json:"assignee_id,omitempty"`
	Executor     string `json:"executor,omitempty"`
	Reason       string `json:"reason,omitempty"`
}

// seatPollWindow bounds how long the CLI waits for the routing pass the
// status change already started; a variable so tests need not sleep.
var (
	seatPollWindow   = 10 * time.Second
	seatPollInterval = time.Second
)

// reportBlockedSeat waits for the routing pass the status write started to
// fill an empty executor slot. If the slot is still empty after the window,
// it runs the same Route synchronously (the seat write only lands on an
// empty slot, so a late detached pass cannot double-assign) and keeps its
// reason, so the caller hears why nobody was seated.
func reportBlockedSeat(ctx context.Context, client *cli.APIClient, issueID string) seatReport {
	path := "/api/issues/" + url.PathEscape(issueID)
	deadline := time.Now().Add(seatPollWindow)
	for {
		var issue map[string]any
		if err := client.GetJSON(ctx, path, &issue); err == nil {
			if id, _ := issue["assignee_id"].(string); id != "" {
				kind, _ := issue["assignee_type"].(string)
				return seatReport{Seated: true, AssigneeType: kind, AssigneeID: id}
			}
		}
		if !time.Now().Before(deadline) {
			break
		}
		select {
		case <-ctx.Done():
			return seatReport{Reason: ctx.Err().Error()}
		case <-time.After(seatPollInterval):
		}
	}
	var out map[string]any
	if err := client.PostJSON(ctx, path+"/route", map[string]any{}, &out); err != nil {
		return seatReport{Reason: err.Error()}
	}
	report := seatReport{}
	report.Executor, _ = out["executor"].(string)
	report.Reason, _ = out["reason"].(string)
	if report.Executor != "" {
		report.Seated = true
	}
	return report
}

func printBlockedSeat(w io.Writer, display string, r seatReport) {
	switch {
	case r.Seated && r.Executor != "":
		fmt.Fprintf(w, "Executor was empty; routing seated %s. It stays parked until the wait ends.\n", r.Executor)
	case r.Seated:
		fmt.Fprintf(w, "Executor was empty; routing seated %s %s. It stays parked until the wait ends.\n", r.AssigneeType, r.AssigneeID)
	default:
		reason := r.Reason
		if reason == "" {
			reason = "no reason given"
		}
		fmt.Fprintf(w, "Executor is still empty (routing: %s). When the wait ends nobody can be woken.\n"+
			"  Pick one: multica issue assign %s --to <agent-name> --no-start\n", reason, display)
	}
}
