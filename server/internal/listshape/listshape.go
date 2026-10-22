// Package listshape owns the opt-in list projections shared by CLI and API.
package listshape

import (
	"fmt"
	"net/url"
	"strings"
)

var Agent = []string{"id", "name", "status", "runtime_id", "runtime_mode", "runtime_availability", "work_enabled", "archived_at", "parent_agent_id"}
var Chat = []string{"id", "title", "project_id", "project_title", "agent_id", "agent_name", "status", "last_active_at", "message_count"}
var Runtime = []string{"id", "name", "custom_name", "runtime_mode", "provider", "status", "visibility", "last_seen_at", "machine_capacity"}

// Fields gives explicit --fields precedence over the compact defaults. The
// server validates names against the resource's public JSON fields, after
// the existing visibility and secret-redaction gates have run.
func Fields(q url.Values, compact []string) ([]string, error) {
	if raw := q.Get("fields"); raw != "" {
		out := []string{}
		seen := map[string]bool{}
		for _, part := range strings.Split(raw, ",") {
			name := strings.TrimSpace(part)
			if name == "" {
				return nil, fmt.Errorf("fields must be a comma-separated list of non-empty field names")
			}
			if !seen[name] {
				out = append(out, name)
				seen[name] = true
			}
		}
		return out, nil
	}
	if q.Get("compact") == "true" || q.Get("compact") == "1" {
		return compact, nil
	}
	return nil, nil
}

// Filter also supports old servers that ignore the projection query.
func Filter(rows []map[string]any, fields []string) {
	if len(fields) == 0 {
		return
	}
	keep := map[string]bool{}
	for _, name := range fields {
		keep[name] = true
	}
	for _, row := range rows {
		for name := range row {
			if !keep[name] {
				delete(row, name)
			}
		}
	}
}
