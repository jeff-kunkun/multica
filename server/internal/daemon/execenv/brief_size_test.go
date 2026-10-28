package execenv

import (
	"fmt"
	"strings"
	"testing"
)

// The runtime brief is the first thing every run loads, on every turn, so its
// size is a cost paid by every task. These ceilings are the gate from
// docs/adr/0007-context-injection-principles.md: a change that makes a brief
// thicker has to fail here and be argued for, not slip in one paragraph at a
// time.
//
// maxBytes is the ceiling enforced today: the measured size of the brief plus
// a small margin, so the brief cannot grow but small wording fixes still fit.
// targetBytes is where the slimming work is headed; when a slimming change
// lands, lower maxBytes to just above the new measured size. Never raise
// maxBytes to make a failing change pass — move the content out of the brief
// instead (see the ADR's three questions). DENE-1329 brought issue and chat
// under their targets and DENE-2679 trimmed every kind again; the ratchet
// below still keeps each ceiling just above the measured size, so the gap to
// the target is not free room to grow into.
//
// The minimal fixtures measure only the platform's own fixed text. "office" is
// a real office issue run (officeIssueBrief below), because the sections that
// wrap per-task data are where a real brief's weight was.
var briefSizeBudgets = []struct {
	name        string
	ctx         TaskContextForEnv
	maxBytes    int
	targetBytes int // 0 = no target set yet
}{
	{"issue", TaskContextForEnv{IssueID: "i-1", AgentID: "a-1", AgentName: "Agent"}, 4250, 8 << 10},
	{"chat", TaskContextForEnv{ChatSessionID: "c-1", AgentID: "a-1", AgentName: "Agent"}, 2900, 6 << 10},
	{"quick-create", TaskContextForEnv{QuickCreatePrompt: "create an issue", AgentID: "a-1", AgentName: "Agent"}, 4950, 0},
	{"autopilot", TaskContextForEnv{AutopilotRunID: "r-1", AgentID: "a-1", AgentName: "Agent"}, 2600, 0},
	{"consult", TaskContextForEnv{IsConsult: true, AgentID: "a-1", AgentName: "Agent"}, 2600, 0},
	{"office", officeIssueBrief, 5800, 5 << 10},
}

// officeIssueBrief is a real office issue run's shape (DENE-2679): a project
// pinned to a local folder that two other runs share, a covered and a remote
// repository, a read-only sibling directory, and the platform skill in shared
// mode. Sections that carry data — Repositories, Project Context, Code Source,
// Skills — were where a real brief's weight was (9.9KB on DENE-2666, section
// 6; this fixture measured 9,764 bytes before DENE-2679 and 5,679 after), so
// its budget keeps the text wrapped around that data from growing back.
var officeIssueBrief = TaskContextForEnv{
	IssueID:     "01a17e23-36f4-7377-a4e4-ca7b75188ee8",
	AgentID:     "51b3b400-dfb7-4eb7-b008-97ad701f6f12",
	AgentName:   "Claude Code",
	SidecarRoot: "/Users/dev/multica_workspaces_example.com/ws-622bd77aa839/dene-2679-6d1d49830923/sidecar",
	SkillsDir:   "/Users/dev/multica_workspaces_example.com/ws-622bd77aa839/dene-2679-6d1d49830923/sidecar/.claude/skills",
	AgentSkills: []SkillContextForEnv{{Name: "multica-platform", Description: "platform contracts"}},
	ProjectRepos: []RepoContextForEnv{
		{URL: "https://github.com/acme/office", Description: "office main line", Reach: &RepoReachForEnv{State: "connected"}},
		{URL: "https://github.com/acme/product", Description: "product main line", Reach: &RepoReachForEnv{State: "connected"}},
	},
	WorkspaceRepoCount:      9,
	OtherWorkspaceRepoCount: 7,
	Projects: []ProjectContextForEnv{{
		ID:        "b02ac908-5d9e-55d4-b4c8-a0a9ca883f20",
		Title:     "Office rework",
		ChatCount: 168,
		Resources: []ProjectResourceForEnv{
			{ResourceType: "github_repo", ResourceRef: []byte(`{"url":"https://github.com/acme/product","default_branch_hint":"kun"}`), Label: "product kun branch"},
			{ResourceType: "local_directory", ResourceRef: []byte(`{"label":"office local main","repo_key":"github.com/acme/office","daemon_id":"01a095c6-3636-7079-a5c5-6ec659c21e53","real_path":"/Volumes/Data/projects/office/main","local_path":"/Volumes/Data/projects/office/main","is_git_repo":true}`), Label: "office local main", Access: "read-write"},
			{ResourceType: "github_repo", ResourceRef: []byte(`{"url":"https://github.com/acme/office","default_branch_hint":"kun"}`), Label: "office kun branch"},
			{ResourceType: "local_directory", ResourceRef: []byte(`{"label":"game local","daemon_id":"01a095c6-3636-7079-a5c5-6ec659c21e53","local_path":"/Volumes/Data/projects/game"}`), Label: "game local", Access: "read-only"},
		},
	}},
	CodeSource: CodeSourceForEnv{
		Kind:        "local_directory",
		LocalPath:   "/Volumes/Data/projects/office/main",
		Branch:      "kun",
		DisplayName: "office local main",
		CoRunners: []CodeSourceCoRunnerForEnv{
			{Identifier: "DENE-2667", Title: "Build the 13 approved PRs one by one, server backward compatible, test Mac build first", Branch: "kun"},
			{Identifier: "DENE-2677", Title: "Office rework: project folder picker (DENE-2667 PR 12)", Branch: "kun"},
		},
		CoveredRepos: []CodeSourceRepoForEnv{{URL: "https://github.com/acme/office"}},
		RemoteRepos:  []CodeSourceRepoForEnv{{URL: "https://github.com/acme/product"}},
		ReadOnlyDirs: []CodeSourceDirForEnv{{Path: "/Volumes/Data/projects/game", Name: "game local"}},
	},
}

// TestBriefSizeBudget renders each budgeted brief for Claude and checks it
// against its ceiling.
func TestBriefSizeBudget(t *testing.T) {
	t.Parallel()
	for _, tc := range briefSizeBudgets {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			brief := buildMetaSkillContent("claude", tc.ctx)
			if len(brief) > tc.maxBytes {
				t.Errorf("%s brief is %d bytes, over the %d-byte ceiling (target %d).\n"+
					"Do not raise the ceiling: apply the three questions in docs/adr/0007-context-injection-principles.md "+
					"(can the server check it? is it only needed for one action? does it change every turn?) "+
					"and move the new text out of the brief.\nSection sizes:\n%s",
					tc.name, len(brief), tc.maxBytes, tc.targetBytes, briefSectionSizes(brief))
			}
		})
	}
}

// briefSectionSizes lists each ## / ### section's byte size, so a failing
// budget shows where the weight is and a ticket can record the baseline.
func briefSectionSizes(brief string) string {
	var out strings.Builder
	title, size := "(preamble)", 0
	flush := func() { fmt.Fprintf(&out, "  %6d  %s\n", size, title) }
	for _, line := range strings.SplitAfter(brief, "\n") {
		if strings.HasPrefix(line, "## ") || strings.HasPrefix(line, "### ") {
			flush()
			title, size = strings.TrimSpace(strings.TrimLeft(line, "# ")), 0
		}
		size += len(line)
	}
	flush()
	return out.String()
}

// TestBriefSizeBudgetIsTight is the ratchet: a ceiling far above the measured
// size would let the brief grow into the slack unnoticed, so when a slimming
// change lands the ceiling above must come down with it.
func TestBriefSizeBudgetIsTight(t *testing.T) {
	t.Parallel()
	const maxSlack = 1200
	for _, tc := range briefSizeBudgets {
		brief := buildMetaSkillContent("claude", tc.ctx)
		if slack := tc.maxBytes - len(brief); slack > maxSlack {
			t.Errorf("%s: ceiling %d is %d bytes above the measured %d; lower maxBytes in briefSizeBudgets to just above %d",
				tc.name, tc.maxBytes, slack, len(brief), len(brief))
		}
	}
}
