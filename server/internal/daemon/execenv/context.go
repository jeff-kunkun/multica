package execenv

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// TaskContextMarkerRelPath is a non-secret marker the daemon writes under the
// task workdir. The CLI uses it as a fallback daemon-task signal when a child
// sandbox strips all MULTICA_* env vars before invoking `multica`.
const TaskContextMarkerRelPath = ".multica/daemon_task_context.json"

// TaskContextMarkerManagedBy is the marker discriminator the CLI checks before
// treating TaskContextMarkerRelPath as daemon-owned.
const TaskContextMarkerManagedBy = "multica-daemon-task"

type taskContextMarkerFile struct {
	ManagedBy     string `json:"managed_by"`
	AgentID       string `json:"agent_id,omitempty"`
	IssueID       string `json:"issue_id,omitempty"`
	ChatSessionID string `json:"chat_session_id,omitempty"`
}

// EnsureWorkspacesRootMarker writes a persistent daemon-task marker at
// {workspacesRoot}/.multica/daemon_task_context.json.
//
// The per-workdir marker only protects `multica` invocations whose cwd is
// inside the workdir, because the CLI discovers markers by walking *up* from
// cwd. A sandboxed subprocess that lost every MULTICA_* env var and escaped
// to the workdir's parent directory sits above that marker, finds no daemon
// signal, and would fall back to the user's config PAT — a confirmed
// impersonation path. Every directory under workspacesRoot is daemon-owned,
// so a marker at the root puts the entire tree back under the fail-closed
// guard without touching any directory a user works in.
//
// A pre-existing marker owned by the daemon is left untouched, so the shared,
// node-wide file is not rewritten on every task start. A truncated or otherwise
// unparseable file — the signature of a torn os.WriteFile from a daemon killed
// mid-write — is reclaimed and rewritten, so a crash can never brick the guard
// for the whole node; only a *parseable* marker owned by something else is
// treated as genuinely foreign and refused. The (re)write is atomic
// (temp file + rename): a concurrent CLI walking up from an escaped cwd sees
// either the old bytes or the complete new file, never a half-written marker it
// would misread as "no signal" and fall open on.
func EnsureWorkspacesRootMarker(workspacesRoot string) error {
	if strings.TrimSpace(workspacesRoot) == "" {
		return errors.New("execenv: workspaces root is required")
	}
	path := filepath.Join(workspacesRoot, TaskContextMarkerRelPath)
	if existing, err := os.ReadFile(path); err == nil {
		var marker taskContextMarkerFile
		if json.Unmarshal(existing, &marker) == nil {
			if marker.ManagedBy == TaskContextMarkerManagedBy {
				return nil
			}
			// Parseable but owned by something else: never clobber it.
			return fmt.Errorf("foreign file at workspaces root marker path %s; refusing to overwrite", path)
		}
		// Unparseable content is almost certainly a torn write of our own
		// marker; fall through to reclaim it below.
	} else if !os.IsNotExist(err) {
		// A real read error (e.g. a directory at the path) is not a signal we
		// can safely overwrite; surface it. Callers degrade non-fatally.
		return fmt.Errorf("read workspaces root marker %s: %w", path, err)
	}
	payload := taskContextMarkerFile{ManagedBy: TaskContextMarkerManagedBy}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal workspaces root marker: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create workspaces root marker dir: %w", err)
	}
	if err := writeWorkspacesRootMarkerAtomic(path, data); err != nil {
		return fmt.Errorf("write workspaces root marker: %w", err)
	}
	return nil
}

// writeWorkspacesRootMarkerAtomic writes data to path via a same-directory temp
// file plus a rename, so a concurrent reader observes either the old file or the
// complete new one — never a partial write. Mirrors the daemon-id / CLI-config
// write idiom (see writeDaemonIDFile). Perm is 0644 because the CLI's upward
// walk must be able to read the marker from a subprocess that may run under a
// different uid; the payload is non-secret.
func writeWorkspacesRootMarkerAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".daemon_task_context-*.json.tmp")
	if err != nil {
		return fmt.Errorf("create temp workspaces root marker: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("write temp workspaces root marker: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("close temp workspaces root marker: %w", err)
	}
	if err := os.Chmod(tmpPath, 0o644); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("chmod temp workspaces root marker: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename workspaces root marker: %w", err)
	}
	return nil
}

// writeContextFiles writes the task's sidecar files: the task-context marker
// and project resources.
//
// It deliberately writes no per-task Markdown brief. There used to be an
// .agent_context/issue_context.md carrying the issue id, trigger comment id,
// handoff note, and other per-task input — every one of
// which the runtime brief and the per-turn user message already carry. No
// provider read the file (nothing in either surface pointed at it), so it was
// a third copy that had to be kept in sync with the two that agents actually
// see, for no reader at all (MUL-6984).
//
// manifest, when non-nil, is populated with every file we created and every
// intermediate directory we had to MkdirAll (skipping any that pre-existed).
// CleanupSidecars uses it to roll the workdir back to its pre-Prepare
// state for local_directory tasks. Callers that don't need cleanup —
// cloud-mode tasks whose envRoot is wiped wholesale by the GC loop — may
// pass nil to skip the bookkeeping entirely.
func writeContextFiles(workDir, provider string, ctx TaskContextForEnv, manifest *sidecarManifest) error {
	if err := writeTaskContextMarker(workDir, ctx, manifest); err != nil {
		return err
	}

	// Project resources are best-effort: a write failure logs but does not
	// block task startup. Missing resources surface as the agent simply not
	// seeing the file, which matches the "scoped, not dumped" design (the
	// runtime brief always lists what the agent should expect).
	if err := writeProjectResources(workDir, ctx, manifest); err != nil {
		// Caller logs warnings; avoid noisy returns for non-fatal context.
		return fmt.Errorf("write project resources: %w", err)
	}

	return nil
}

func writeTaskContextMarker(workDir string, ctx TaskContextForEnv, manifest *sidecarManifest) error {
	dir := filepath.Dir(filepath.Join(workDir, TaskContextMarkerRelPath))
	if err := recordMkdirAll(dir, 0o755, manifest); err != nil {
		return fmt.Errorf("create .multica dir: %w", err)
	}
	// The sidecar manifest removes this marker on normal local_directory
	// cleanup. If a crash leaves it behind, the CLI intentionally treats it
	// as daemon context and fails closed instead of using a user PAT.
	payload := taskContextMarkerFile{
		ManagedBy:     TaskContextMarkerManagedBy,
		AgentID:       ctx.AgentID,
		IssueID:       ctx.IssueID,
		ChatSessionID: ctx.ChatSessionID,
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal task context marker: %w", err)
	}
	if err := recordWriteFile(filepath.Join(workDir, TaskContextMarkerRelPath), data, 0o644, manifest); err != nil {
		if errors.Is(err, errPathPreExists) {
			path := filepath.Join(workDir, TaskContextMarkerRelPath)
			existing, readErr := os.ReadFile(path)
			if readErr != nil {
				return fmt.Errorf("read existing task context marker: %w", readErr)
			}
			var marker taskContextMarkerFile
			if json.Unmarshal(existing, &marker) != nil || marker.ManagedBy != TaskContextMarkerManagedBy {
				return fmt.Errorf("write task context marker: %w", err)
			}
			if writeErr := os.WriteFile(path, data, 0o644); writeErr != nil {
				return fmt.Errorf("refresh task context marker: %w", writeErr)
			}
			if manifest != nil {
				manifest.Files = append(manifest.Files, path)
			}
			return nil
		}
		return fmt.Errorf("write task context marker: %w", err)
	}
	return nil
}

// projectResourceFile is the on-disk JSON written into the agent's working
// directory. Schema is intentionally a thin pass-through of the API response
// so consumers (skills, future tooling) don't need a separate parser.
type projectResourceFile struct {
	ProjectID          string                  `json:"project_id,omitempty"`
	ProjectTitle       string                  `json:"project_title,omitempty"`
	ProjectDescription string                  `json:"project_description,omitempty"`
	Resources          []ProjectResourceForEnv `json:"resources"`
}

// MarshalJSON renders the resource_ref field as raw JSON instead of a base64
// blob. The struct's other fields are simple strings.
func (p ProjectResourceForEnv) MarshalJSON() ([]byte, error) {
	type alias struct {
		ID           string          `json:"id"`
		ResourceType string          `json:"resource_type"`
		ResourceRef  json.RawMessage `json:"resource_ref"`
		Label        string          `json:"label,omitempty"`
	}
	ref := p.ResourceRef
	if len(ref) == 0 {
		ref = json.RawMessage("{}")
	}
	return json.Marshal(alias{
		ID:           p.ID,
		ResourceType: p.ResourceType,
		ResourceRef:  ref,
		Label:        p.Label,
	})
}

// writeProjectResources writes .multica/project/resources.json into the
// working directory when the task carries project context. The file is
// always written when a project is attached (even with zero resources) so
// agents can rely on its presence as a signal that a project exists.
//
// manifest, when non-nil, is populated with the .multica/project chain
// of created directories and the resources.json file so CleanupSidecars
// can undo them on local_directory teardown.
func writeProjectResources(workDir string, ctx TaskContextForEnv, manifest *sidecarManifest) error {
	if ctx.ProjectID == "" && len(ctx.ProjectResources) == 0 {
		return nil
	}
	dir := filepath.Join(workDir, ".multica", "project")
	if err := recordMkdirAll(dir, 0o755, manifest); err != nil {
		return err
	}
	resources := ctx.ProjectResources
	if resources == nil {
		resources = []ProjectResourceForEnv{}
	}
	payload := projectResourceFile{
		ProjectID:          ctx.ProjectID,
		ProjectTitle:       ctx.ProjectTitle,
		ProjectDescription: ctx.ProjectDescription,
		Resources:          resources,
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	if err := recordWriteFile(filepath.Join(dir, "resources.json"), data, 0o644, manifest); err != nil {
		// .multica/project/resources.json is Multica-owned and a
		// pre-existing path is almost certainly user content the
		// manifest must not destroy. The runtime brief already lists
		// every project resource so the agent runs fine without the
		// JSON sidecar — collision degrades to brief-only mode.
		if !errors.Is(err, errPathPreExists) {
			return err
		}
	}
	return nil
}
