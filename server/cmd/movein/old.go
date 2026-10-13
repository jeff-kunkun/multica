package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// oldServer reads the old Multica server's public API as one of its members.
// It only ever sends GET requests.
type oldServer struct {
	base      string
	token     string
	workspace string
	client    *http.Client
}

func newOldServer(base, token string) *oldServer {
	return &oldServer{
		base:   strings.TrimRight(base, "/"),
		token:  token,
		client: &http.Client{Timeout: 5 * time.Minute},
	}
}

// get decodes the JSON at path into out, retrying transport failures and 5xx
// answers a few times: the old server sits behind a slow uplink.
func (o *oldServer) get(ctx context.Context, path string, out any) error {
	var last error
	for attempt := 0; attempt < 4; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt*attempt) * 2 * time.Second):
			}
		}
		retry, err := o.try(ctx, path, out)
		if err == nil || !retry {
			return err
		}
		last = err
	}
	return last
}

func (o *oldServer) try(ctx context.Context, path string, out any) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, o.base+path, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", "Bearer "+o.token)
	if o.workspace != "" {
		req.Header.Set("X-Workspace-ID", o.workspace)
	}
	resp, err := o.client.Do(req)
	if err != nil {
		return true, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return true, err
	}
	if resp.StatusCode/100 != 2 {
		err := fmt.Errorf("GET %s: %d %s", path, resp.StatusCode, bytes.TrimSpace(body[:min(len(body), 512)]))
		return resp.StatusCode >= 500, err
	}
	return false, json.Unmarshal(body, out)
}

// list decodes a response that is either a bare array or an object holding
// the array under key.
func (o *oldServer) list(ctx context.Context, path, key string, out any) error {
	var raw json.RawMessage
	if err := o.get(ctx, path, &raw); err != nil {
		return err
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) > 0 && raw[0] == '{' {
		var wrapped map[string]json.RawMessage
		if err := json.Unmarshal(raw, &wrapped); err != nil {
			return err
		}
		raw = wrapped[key]
		if len(raw) == 0 || string(raw) == "null" {
			raw = json.RawMessage("[]")
		}
	}
	return json.Unmarshal(raw, out)
}

type oldWorkspace struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
	Name string `json:"name"`
}

type oldMember struct {
	UserID string `json:"user_id"`
	Name   string `json:"name"`
}

type oldAgent struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	RuntimeID string `json:"runtime_id"`
}

type oldRuntime struct {
	ID       string `json:"id"`
	Provider string `json:"provider"`
}

type oldProject struct {
	ID          string  `json:"id"`
	Title       string  `json:"title"`
	Description *string `json:"description"`
	Icon        *string `json:"icon"`
	Status      string  `json:"status"`
	Priority    string  `json:"priority"`
	StartDate   *string `json:"start_date"`
	DueDate     *string `json:"due_date"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
}

type oldResource struct {
	ResourceType string          `json:"resource_type"`
	ResourceRef  json.RawMessage `json:"resource_ref"`
	Label        *string         `json:"label"`
	Position     int32           `json:"position"`
	ID           string          `json:"id"`
	CreatedAt    string          `json:"created_at"`
}

type oldIssue struct {
	ID             string   `json:"id"`
	Number         int32    `json:"number"`
	Identifier     string   `json:"identifier"`
	Title          string   `json:"title"`
	Description    *string  `json:"description"`
	Status         string   `json:"status"`
	StatusCategory string   `json:"status_category"`
	StatusName     string   `json:"status_name"`
	Priority       string   `json:"priority"`
	AssigneeType   *string  `json:"assignee_type"`
	AssigneeID     *string  `json:"assignee_id"`
	ParentIssueID  *string  `json:"parent_issue_id"`
	ProjectID      *string  `json:"project_id"`
	Position       *float64 `json:"position"`
	CreatedAt      string   `json:"created_at"`
	UpdatedAt      string   `json:"updated_at"`
}

type oldComment struct {
	ID         string  `json:"id"`
	AuthorType string  `json:"author_type"`
	AuthorID   string  `json:"author_id"`
	Content    string  `json:"content"`
	Type       string  `json:"type"`
	ParentID   *string `json:"parent_id"`
	CreatedAt  string  `json:"created_at"`
	UpdatedAt  string  `json:"updated_at"`
	DeletedAt  *string `json:"deleted_at"`
}

type oldChat struct {
	ID        string  `json:"id"`
	AgentID   string  `json:"agent_id"`
	Title     string  `json:"title"`
	Status    string  `json:"status"`
	ProjectID *string `json:"project_id"`
	CreatedAt string  `json:"created_at"`
	UpdatedAt string  `json:"updated_at"`
}

type oldMessage struct {
	ID            string  `json:"id"`
	Role          string  `json:"role"`
	Content       string  `json:"content"`
	CreatedAt     string  `json:"created_at"`
	FailureReason *string `json:"failure_reason"`
	ElapsedMs     *int64  `json:"elapsed_ms"`
	MessageKind   string  `json:"message_kind"`
}
