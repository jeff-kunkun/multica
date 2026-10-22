// Package coderesolve answers one question, in one place, for the whole
// platform: which code does this run use?
//
// Before it, three components answered it independently. The daemon walked the
// project's resources and picked a directory (internal/daemon/local_directory.go);
// the claim path narrowed the resource set to what the daemon could act on
// (internal/handler/daemon.go); and the desktop UI guessed at the answer from
// the resource list so it could tell the user where a run would land. Three
// derivations of one rule is three chances to disagree, and the user only sees
// the disagreement after a run has already written to the wrong directory.
//
// So the rule lives here, as a pure function: no filesystem, no clock, no
// randomness, no database. Given a task, its projects' resources and the
// capabilities the claiming daemon advertised, Resolve returns a Decision. The
// server computes it once per claim, ships it on the task, and both the daemon
// and the frontend read it instead of re-deriving it.
//
// # Purity, and what it costs
//
// The server cannot see the user's disk. Everything this package decides is
// therefore decided from what the project RECORDED about a machine, not from
// what is on that machine right now. "Available on this machine" means "bound
// to the daemon that is claiming", which is the only availability question the
// server can answer without lying. Whether the directory still exists, is
// writable, or is a git repository is the daemon's to verify at run time — and
// a daemon that finds otherwise must FAIL the task with a code, never quietly
// pick somewhere else. Silently re-choosing is the failure mode this package
// exists to remove.
package coderesolve

import (
	"encoding/json"
	"fmt"
)

// Kind names one of the four answers. The set is closed: Decision cannot be
// constructed outside this package, so every value in the wild carries one of
// these and nothing else.
type Kind string

const (
	// KindLocalInPlace runs the agent in the user's own bound folder. Other
	// runs may be in the same folder; nothing serialises them.
	KindLocalInPlace Kind = "local_in_place"
	// KindRemoteCache runs in a Multica-owned checkout of a remote repository,
	// because no directory on the claiming machine holds this project's code.
	KindRemoteCache Kind = "remote_cache"
	// KindSharedScratch: the task bound no directory and named no repository,
	// so there is no code to work in. The daemon runs it in its own workdir
	// under the workspaces root. The wire value predates that and is kept so
	// existing readers keep parsing it.
	KindSharedScratch Kind = "shared_scratch"
	// KindUnresolvable is a first-class answer, not an error channel. The
	// inputs describe a run that cannot be placed, and the task must fail with
	// the carried code rather than fall back to any of the three above.
	KindUnresolvable Kind = "unresolvable"
)

// Kinds an older server stored for the folder modes it used to have. Both
// still name a bound folder, and both read back as KindLocalInPlace.
const (
	legacyKindLocalShared   Kind = "local_shared"
	legacyKindLocalWorktree Kind = "local_worktree"
)

// Code identifies why a Decision is Unresolvable, in a form the UI can
// translate and a test can assert on. The human sentence is carried alongside
// it in Failure.Reason; this is the stable half.
type Code string

const (
	// CodeUnset is the zero Decision: a value that was never computed. It
	// exists so that "nobody resolved this" is one of the answers rather
	// than a nil that each caller re-interprets.
	CodeUnset Code = "unset"
	// CodeMalformedResource is a local_directory row the server cannot read:
	// unparseable ref, no daemon_id, or a local_path that is not absolute. A
	// directory that cannot even be named is not a directory to skip past on
	// the way to someone else's.
	CodeMalformedResource Code = "malformed_resource"
	// CodePinnedResourceNotFound is an explicit resource id that names nothing
	// in the task's projects.
	CodePinnedResourceNotFound Code = "pinned_resource_not_found"
	// CodePinnedResourceOtherMachine is an explicit resource id naming a
	// directory bound to a DIFFERENT daemon. Running it here would mean
	// running against a path that, on this machine, is some other directory or
	// none at all.
	CodePinnedResourceOtherMachine Code = "pinned_resource_other_machine"
	// CodePinnedResourceUnusable is an explicit resource id naming a resource
	// type this run cannot be placed in.
	CodePinnedResourceUnusable Code = "pinned_resource_unusable"
	// CodeUnknownExecutionMode is a decision kind this binary does not know:
	// it was produced by a newer server, and guessing would run the agent
	// somewhere the decision did not say.
	CodeUnknownExecutionMode Code = "unknown_execution_mode"
)

// LocalTarget describes a run that writes in a bound folder on the user's own
// disk.
type LocalTarget struct {
	// ResourceID and ProjectID say which binding produced this target, so the
	// UI can link the answer back to the row the user can edit.
	ResourceID string `json:"resource_id"`
	ProjectID  string `json:"project_id,omitempty"`
	// Path is the bound folder the agent runs in.
	Path string `json:"path"`
	// DisplayName is the label the user gave the directory, or its basename.
	// Never the absolute path: this is what the UI renders into screenshots.
	DisplayName string `json:"display_name,omitempty"`
}

// RemoteTarget describes a run that works in a Multica-owned checkout.
type RemoteTarget struct {
	// URL is the repository this run's checkout is for. A project may name
	// several repositories; the run is placed against the first, and the rest
	// stay reachable through `multica repo checkout`.
	URL string `json:"url"`
	// ResourceID is the github_repo binding, when the URL came from one. Repos
	// inherited from the workspace carry no resource row and leave it empty.
	ResourceID string `json:"resource_id,omitempty"`
	ProjectID  string `json:"project_id,omitempty"`
}

// ScratchTarget describes a run with no code at all: a question, a plan, a
// conversation. The daemon gives it its own workdir.
type ScratchTarget struct {
	// SessionID is the conversation the run belongs to, or the task id.
	SessionID string `json:"session_id"`
}

// Failure is the Unresolvable payload. Code is for machines, Reason for the
// person who has to fix the binding.
type Failure struct {
	Code   Code   `json:"code"`
	Reason string `json:"reason"`
}

// Dir is one local directory the run may READ but not write. One run writes
// one directory (DENE-617 invariant 1); the others are named rather than
// hidden, so an agent that needs to look at one does not discover it by
// accident or check out a second copy of it.
type Dir struct {
	ResourceID string `json:"resource_id"`
	ProjectID  string `json:"project_id,omitempty"`
	Path       string `json:"path"`
	Name       string `json:"name,omitempty"`
}

// Decision is the closed sum type: exactly one of the four kinds, with only
// that kind's payload reachable.
//
// The fields are unexported and there is no literal form, so the only
// Decisions that exist are the ones the constructors below produce. The zero
// value is not a fifth state — it reads as Unresolvable{CodeUnset}, which is
// the true statement about a Decision nobody computed.
type Decision struct {
	kind     Kind
	local    LocalTarget
	remote   RemoteTarget
	scratch  ScratchTarget
	failure  Failure
	readOnly []Dir
}

// NewLocalInPlace and friends are the only ways to build a Decision.
func NewLocalInPlace(t LocalTarget) Decision {
	return Decision{kind: KindLocalInPlace, local: t}
}

func NewRemoteCache(t RemoteTarget) Decision {
	return Decision{kind: KindRemoteCache, remote: t}
}

func NewSharedScratch(t ScratchTarget) Decision {
	return Decision{kind: KindSharedScratch, scratch: t}
}

// NewUnresolvable builds the failure answer. It is a Decision like any other:
// callers switch on it, the wire carries it, and the UI renders it.
func NewUnresolvable(code Code, reason string) Decision {
	return Decision{kind: KindUnresolvable, failure: Failure{Code: code, Reason: reason}}
}

// WithReadOnly attaches the directories this run may read. Returned as a new
// Decision so a Decision is never mutated after it has been handed out.
func (d Decision) WithReadOnly(dirs []Dir) Decision {
	d.readOnly = append([]Dir(nil), dirs...)
	return d
}

// Kind reports which of the four this is. A zero Decision reports
// KindUnresolvable, so no caller ever has to handle a fifth case.
func (d Decision) Kind() Kind {
	if d.kind == "" {
		return KindUnresolvable
	}
	return d.kind
}

// Local returns the local target and true for KindLocalInPlace.
func (d Decision) Local() (LocalTarget, bool) {
	if d.Kind() == KindLocalInPlace {
		return d.local, true
	}
	return LocalTarget{}, false
}

// Remote returns the remote target and true for KindRemoteCache.
func (d Decision) Remote() (RemoteTarget, bool) {
	if d.Kind() == KindRemoteCache {
		return d.remote, true
	}
	return RemoteTarget{}, false
}

// Scratch returns the scratch target and true for KindSharedScratch.
func (d Decision) Scratch() (ScratchTarget, bool) {
	if d.Kind() == KindSharedScratch {
		return d.scratch, true
	}
	return ScratchTarget{}, false
}

// Failure returns the failure and true for KindUnresolvable. A zero Decision
// yields CodeUnset rather than an empty-but-valid-looking failure.
func (d Decision) Failure() (Failure, bool) {
	if d.Kind() != KindUnresolvable {
		return Failure{}, false
	}
	if d.kind == "" {
		return Failure{Code: CodeUnset, Reason: "no code source decision was computed for this run"}, true
	}
	return d.failure, true
}

// ReadOnly lists the local directories this run may read but not write.
func (d Decision) ReadOnly() []Dir {
	return append([]Dir(nil), d.readOnly...)
}

// Visitor is one arm per kind. Every field is required: Fold panics on a nil
// arm before it looks at the decision, so a caller that forgot a case fails on
// its first call rather than on the one input that happens to reach it.
type Visitor[T any] struct {
	LocalInPlace  func(LocalTarget) T
	RemoteCache   func(RemoteTarget) T
	SharedScratch func(ScratchTarget) T
	Unresolvable  func(Failure) T
}

// Fold is the exhaustive switch. Go has no compiler check for one, so this is
// the substitute: the check is a precondition, not a default branch, because a
// default branch is exactly how a new kind gets silently mishandled.
func Fold[T any](d Decision, v Visitor[T]) T {
	switch {
	case v.LocalInPlace == nil:
		panic("coderesolve: Visitor.LocalInPlace is nil")
	case v.RemoteCache == nil:
		panic("coderesolve: Visitor.RemoteCache is nil")
	case v.SharedScratch == nil:
		panic("coderesolve: Visitor.SharedScratch is nil")
	case v.Unresolvable == nil:
		panic("coderesolve: Visitor.Unresolvable is nil")
	}
	switch d.Kind() {
	case KindLocalInPlace:
		return v.LocalInPlace(d.local)
	case KindRemoteCache:
		return v.RemoteCache(d.remote)
	case KindSharedScratch:
		return v.SharedScratch(d.scratch)
	default:
		f, _ := d.Failure()
		return v.Unresolvable(f)
	}
}

// decisionWire is the JSON shape. Flat rather than nested-per-variant: the
// daemon and the frontend both switch on `kind` and read the fields that kind
// defines, and a flat object keeps an older reader that only understands
// `kind` and `path` working.
type decisionWire struct {
	Kind Kind `json:"kind"`
	// Local fields.
	ResourceID  string `json:"resource_id,omitempty"`
	ProjectID   string `json:"project_id,omitempty"`
	Path        string `json:"path,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
	// RepoPath is read, never written: a stored local_worktree decision put
	// the bound folder here and a worktree path in Path.
	RepoPath string `json:"repo_path,omitempty"`
	// Remote.
	URL string `json:"url,omitempty"`
	// Scratch.
	SessionID string `json:"session_id,omitempty"`
	// Unresolvable.
	Code   Code   `json:"code,omitempty"`
	Reason string `json:"reason,omitempty"`
	// Read-only context, on every kind that can have it.
	ReadOnly []Dir `json:"read_only_dirs,omitempty"`
}

// MarshalJSON writes the flat wire shape. A zero Decision marshals as the
// Unresolvable it reads as, so the wire never carries a kindless object.
func (d Decision) MarshalJSON() ([]byte, error) {
	w := decisionWire{Kind: d.Kind(), ReadOnly: d.readOnly}
	switch d.Kind() {
	case KindLocalInPlace:
		w.ResourceID = d.local.ResourceID
		w.ProjectID = d.local.ProjectID
		w.Path = d.local.Path
		w.DisplayName = d.local.DisplayName
	case KindRemoteCache:
		w.URL = d.remote.URL
		w.ResourceID = d.remote.ResourceID
		w.ProjectID = d.remote.ProjectID
	case KindSharedScratch:
		w.SessionID = d.scratch.SessionID
	default:
		f, _ := d.Failure()
		w.Code = f.Code
		w.Reason = f.Reason
	}
	return json.Marshal(w)
}

// UnmarshalJSON rebuilds a Decision from the wire. A kind this binary does not
// know becomes Unresolvable rather than a zero-valued local run: a daemon
// reading a decision from a newer server must fail loudly, not run somewhere.
func (d *Decision) UnmarshalJSON(data []byte) error {
	var w decisionWire
	if err := json.Unmarshal(data, &w); err != nil {
		return err
	}
	local := LocalTarget{
		ResourceID:  w.ResourceID,
		ProjectID:   w.ProjectID,
		Path:        w.Path,
		DisplayName: w.DisplayName,
	}
	switch w.Kind {
	case KindLocalInPlace, legacyKindLocalShared:
		*d = Decision{kind: KindLocalInPlace, local: local}
	case legacyKindLocalWorktree:
		if w.RepoPath != "" {
			local.Path = w.RepoPath
		}
		*d = Decision{kind: KindLocalInPlace, local: local}
	case KindRemoteCache:
		*d = Decision{kind: w.Kind, remote: RemoteTarget{URL: w.URL, ResourceID: w.ResourceID, ProjectID: w.ProjectID}}
	case KindSharedScratch:
		*d = Decision{kind: w.Kind, scratch: ScratchTarget{SessionID: w.SessionID}}
	case KindUnresolvable:
		*d = NewUnresolvable(w.Code, w.Reason)
	default:
		*d = NewUnresolvable(CodeUnknownExecutionMode, fmt.Sprintf(
			"code source decision kind %q was produced by a newer server than this client; refusing to guess where this run belongs", w.Kind))
	}
	d.readOnly = append([]Dir(nil), w.ReadOnly...)
	return nil
}
