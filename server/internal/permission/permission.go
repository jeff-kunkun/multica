// Package permission is the decision matrix for workspace tiers and resource
// visibility. It is pure: callers load the facts (the caller's tier, the
// resource's visibility, how the caller relates to the resource) and this
// package turns them into one answer.
//
// Two layers, never mixed:
//
//   - Visibility decides whether a resource exists for the caller at all.
//   - Tier decides what the caller may do with a resource that exists for them.
//
// Sharing never grants write access and a tier never grants sight of an
// unshared resource. The human-readable matrix is docs/kun/permission-model.md;
// this file is its executable form and the tests keep the two honest.
//
// Agents and squads are deliberately absent. Their access is owned by
// agent.permission_mode and agent_invocation_target, and giving them a second
// source here would leave conflicts with no winner.
package permission

// Role is a workspace tier, stored in member.role.
type Role string

const (
	RoleOwner  Role = "owner"
	RoleAdmin  Role = "admin"
	RoleMember Role = "member"
	RoleGuest  Role = "guest"
)

// Roles lists every tier, strongest first. It must match the member.role
// CHECK constraint.
var Roles = []Role{RoleOwner, RoleAdmin, RoleMember, RoleGuest}

// Visibility is a resource's sharing scope. The vocabulary is issue_view's,
// so the whole product speaks one set of scope names.
type Visibility string

const (
	VisibilityPrivate   Visibility = "private"
	VisibilityProject   Visibility = "project"
	VisibilityWorkspace Visibility = "workspace"
)

// Visibilities lists every scope, narrowest first.
var Visibilities = []Visibility{VisibilityPrivate, VisibilityProject, VisibilityWorkspace}

// DefaultVisibility is what a newly created resource gets: zero trust.
const DefaultVisibility = VisibilityPrivate

// Relation is how the caller relates to one resource. Callers fill it from
// data they already hold; nothing here touches the database.
type Relation struct {
	// IsCreator: the caller created the resource.
	IsCreator bool
	// InProject: the resource belongs to a project and that project is in the
	// caller's accessible set, as returned by the handler's
	// listAccessibleProjectIDs (project_member rows, led projects, and every
	// project for owner/admin). Always false for a resource with no project.
	InProject bool
	// LeadsProject: the caller is the lead of the resource's project. Only
	// consulted for ActionManageProjectMembers.
	LeadsProject bool
}

// Action is something a caller does to a visible resource.
type Action string

const (
	// ActionView reads the resource and everything hanging off it.
	ActionView Action = "view"
	// ActionComment posts or edits a comment or reply.
	ActionComment Action = "comment"
	// ActionEdit creates or edits the resource, changes status, uploads or
	// removes attachments, assigns it.
	ActionEdit Action = "edit"
	// ActionChangeVisibility changes the resource's sharing scope.
	ActionChangeVisibility Action = "change_visibility"
	// ActionManageProjectMembers adds or removes people on a project.
	ActionManageProjectMembers Action = "manage_project_members"
)

// ResourceActions lists every Action.
var ResourceActions = []Action{
	ActionView, ActionComment, ActionEdit, ActionChangeVisibility, ActionManageProjectMembers,
}

// WorkspaceAction is something done to the workspace itself. These never
// consult visibility: there is no resource to share.
type WorkspaceAction string

const (
	// WorkspaceCreateResource creates an issue, project or repo.
	WorkspaceCreateResource WorkspaceAction = "create_resource"
	// WorkspaceBeAssigned: the caller can be an assignee or be @-mentioned
	// into triggering a run.
	WorkspaceBeAssigned WorkspaceAction = "be_assigned"
	// WorkspaceManageMembers invites people and changes other people's tier.
	WorkspaceManageMembers WorkspaceAction = "manage_members"
	// WorkspaceManageSettings covers workspace settings, integrations, keys.
	WorkspaceManageSettings WorkspaceAction = "manage_settings"
	// WorkspaceOwn covers billing and transferring or deleting the workspace.
	WorkspaceOwn WorkspaceAction = "own"
)

// WorkspaceActions lists every WorkspaceAction.
var WorkspaceActions = []WorkspaceAction{
	WorkspaceCreateResource, WorkspaceBeAssigned, WorkspaceManageMembers,
	WorkspaceManageSettings, WorkspaceOwn,
}

// Valid reports whether r is a known tier.
func (r Role) Valid() bool {
	switch r {
	case RoleOwner, RoleAdmin, RoleMember, RoleGuest:
		return true
	}
	return false
}

// Valid reports whether v is a known scope.
func (v Visibility) Valid() bool {
	switch v {
	case VisibilityPrivate, VisibilityProject, VisibilityWorkspace:
		return true
	}
	return false
}

func (r Role) isManager() bool { return r == RoleOwner || r == RoleAdmin }

// CanWrite reports whether the tier may write anything at all. Guest is the
// only tier that may not; this is the single fact the global read-only
// interceptor needs.
func (r Role) CanWrite() bool {
	return r == RoleOwner || r == RoleAdmin || r == RoleMember
}

// CanSee is layer one: does the resource exist for this caller. A false
// answer must surface as "not found", never as "forbidden", and must hold in
// lists, search, aggregates and notifications alike.
//
// Unknown tiers and unknown scopes see nothing.
func CanSee(role Role, vis Visibility, rel Relation) bool {
	if !role.Valid() {
		return false
	}
	if rel.IsCreator {
		return true
	}
	switch vis {
	case VisibilityWorkspace:
		// Guests are not part of "the whole workspace". A project is the only
		// way to show a guest anything.
		return role != RoleGuest
	case VisibilityProject:
		return rel.InProject
	default:
		// private, and anything unrecognised.
		return false
	}
}

// Allowed is the full two-layer answer for one caller, one resource, one
// action. Visibility is checked first; tier only matters for a resource the
// caller can see.
func Allowed(role Role, action Action, vis Visibility, rel Relation) bool {
	if !CanSee(role, vis, rel) {
		return false
	}
	switch action {
	case ActionView:
		return true
	case ActionComment, ActionEdit:
		return role.CanWrite()
	case ActionChangeVisibility:
		return role.isManager() || (role == RoleMember && rel.IsCreator)
	case ActionManageProjectMembers:
		return role.isManager() || (role == RoleMember && rel.LeadsProject)
	default:
		return false
	}
}

// AllowedInWorkspace answers workspace-level actions from tier alone.
func AllowedInWorkspace(role Role, action WorkspaceAction) bool {
	switch action {
	case WorkspaceCreateResource, WorkspaceBeAssigned:
		return role.CanWrite()
	case WorkspaceManageMembers, WorkspaceManageSettings:
		return role.isManager()
	case WorkspaceOwn:
		return role == RoleOwner
	default:
		return false
	}
}

// CanSetVisibility reports whether a resource may be given scope vis.
// hasProject is whether the resource belongs to a project: "project" scope
// names that project's people, so without one it names nobody.
func CanSetVisibility(vis Visibility, hasProject bool) bool {
	if !vis.Valid() {
		return false
	}
	return vis != VisibilityProject || hasProject
}
