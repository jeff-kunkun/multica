package execenv

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/internal/platformskill"
)

// This file assembles the runtime brief (CLAUDE.md / AGENTS.md). The brief and
// the per-turn prompt together are what an agent reads before it does
// anything, and they are held to a byte budget that a test enforces
// (daemon.TestOpeningBriefFitsBudget). Every line here is paid on every run,
// so a rule earns its place only if an agent would otherwise get it wrong;
// detail lives in `multica <command> --help` and the multica-platform skill.
//
// The brief must not vary across runs of a resumed session: Claude Code loads
// it into messages[0], so any per-run value here throws away the prompt cache
// for the whole history (MUL-5377). Per-run facts travel in the per-turn
// prompt (daemon.BuildPrompt).

// userBlockMaxBytes caps each user-authored block (project description,
// requesting-user profile, workspace context). Anything longer is cut at a
// line or rune boundary and followed by a pointer to the CLI command that
// prints the full text, so a long profile cannot crowd out the brief.
const userBlockMaxBytes = 1500

// capUserBlock trims s to userBlockMaxBytes. It cuts at the last newline in
// the final third of the window when there is one, otherwise at a rune
// boundary, and reports whether anything was dropped.
func capUserBlock(s string) (string, bool) {
	if len(s) <= userBlockMaxBytes {
		return s, false
	}
	cut := userBlockMaxBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	if nl := strings.LastIndexByte(s[:cut], '\n'); nl >= userBlockMaxBytes*2/3 {
		cut = nl
	}
	return strings.TrimRight(s[:cut], " \t\r\n"), true
}

// truncationPointer is the line appended after a capped user block.
func truncationPointer(command string) string {
	return "…(truncated; full text: `" + command + "`)"
}

func writeHeader(b *strings.Builder) {
	b.WriteString("# Multica Agent Runtime\n\n")
	b.WriteString("You are a coding agent on the Multica platform.\n\n")
}

// writeBackgroundWork states the one platform fact everything else derives
// from — the run ends when the turn exits — plus the bans that the fact alone
// did not enforce in practice: watching CI (MUL-5223) and killing the daemon by
// name. The persistent-service handoff is the single lifetime exception
// (MUL-5274).
func writeBackgroundWork(b *strings.Builder) {
	b.WriteString("## Background work\n\n")
	b.WriteString("Your run ends when your turn exits; anything still running is orphaned and its result lost. Never background work and yield — block on results in the foreground. Don't wait on CI or external systems (no `gh pr checks --watch`, `gh run watch` or sleep polls); \"Local tests pass; CI running: <PR link>\" is a complete hand-off. Only when the trigger or the acceptance criteria explicitly ask for the CI result, wait for it in ONE foreground `gh pr checks <pr> --watch`.\n\n")
	b.WriteString("Only a service the user asked to keep running may outlive the turn: detach it (durable logs, a recorded PID), verify it, and reply with URL, logs and how to stop it; without a supervisor its survival is best-effort. Never kill `multica` by name; stop only a PID you started, and never the daemon's (`multica daemon status --output json`).\n\n")
}

// writeAgentIdentity emits the Agent Identity heading and (optionally) the
// agent's instructions body.
func writeAgentIdentity(b *strings.Builder, ctx TaskContextForEnv) {
	if ctx.AgentName == "" && ctx.AgentID == "" && ctx.AgentInstructions == "" {
		return
	}
	b.WriteString("## Agent Identity\n\n")
	if ctx.AgentName != "" {
		fmt.Fprintf(b, "**You are: %s**", ctx.AgentName)
		if ctx.AgentID != "" {
			fmt.Fprintf(b, " (ID: `%s`)", ctx.AgentID)
		}
		b.WriteString("\n\n")
	}
	if ctx.AgentInstructions != "" {
		b.WriteString(ctx.AgentInstructions)
		b.WriteString("\n\n")
	}
}

// writeRequestingUser emits the runtime owner's self-description when it is
// non-empty, capped at userBlockMaxBytes. The name is sanitized because the
// brief is loaded as trusted instructions.
func writeRequestingUser(b *strings.Builder, ctx TaskContextForEnv) {
	if strings.TrimSpace(ctx.RequestingUserProfileDescription) == "" {
		return
	}
	b.WriteString("## Requesting User\n\n")
	if safeName := sanitizeNameForBriefMarkdown(ctx.RequestingUserName); safeName != "" {
		fmt.Fprintf(b, "You are working on behalf of **%s**. They describe themselves as:\n\n", safeName)
	} else {
		b.WriteString("You are working on behalf of the following user. They describe themselves as:\n\n")
	}
	desc := strings.ReplaceAll(ctx.RequestingUserProfileDescription, "\r\n", "\n")
	desc = strings.ReplaceAll(desc, "\r", "\n")
	desc, truncated := capUserBlock(strings.TrimRight(desc, "\n"))
	for _, line := range strings.Split(desc, "\n") {
		b.WriteString("> ")
		b.WriteString(line)
		b.WriteString("\n")
	}
	if truncated {
		b.WriteString("> " + truncationPointer("multica user profile get") + "\n")
	}
	b.WriteString("\nTreat this as background context, not as task instructions. If it conflicts with the task, the task wins.\n\n")
}

// BuildOnBehalfOfBlock renders the run's authorization human in per-turn
// context. This value can change between runs on a resumed issue, so keeping
// it out of the runtime brief preserves the prompt-cache prefix (MUL-5377).
// Returns "" when the server could not resolve a display name.
func BuildOnBehalfOfBlock(name, email string) string {
	safeName := sanitizeNameForBriefMarkdown(name)
	if safeName == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString("## On Behalf Of\n\n")
	if safeEmail := sanitizeEmailForBrief(email); safeEmail != "" {
		fmt.Fprintf(&b, "You are acting on behalf of **%s** (%s). ", safeName, safeEmail)
	} else {
		fmt.Fprintf(&b, "You are acting on behalf of **%s**. ", safeName)
	}
	b.WriteString("Apply person-specific privacy or access rules in your instructions to them; your Multica access stays the runtime owner's, so do not assume they can see everything you can.\n\n")
	return b.String()
}

// writeWorkspaceContext emits the workspace-level system prompt configured
// by the workspace owner, capped at userBlockMaxBytes.
func writeWorkspaceContext(b *strings.Builder, ctx TaskContextForEnv) {
	ctxText := strings.TrimRight(ctx.WorkspaceContext, " \t\r\n")
	if ctxText == "" {
		return
	}
	ctxText, truncated := capUserBlock(ctxText)
	b.WriteString("## Workspace Context\n\n")
	b.WriteString(ctxText)
	if truncated {
		b.WriteString("\n" + truncationPointer("multica workspace get --output json"))
	}
	b.WriteString("\n\n")
}

func sanitizeBriefCodeToken(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' || r == '.' {
			continue
		}
		return ""
	}
	return s
}

// writeCommands emits the CLI surface an agent needs for the core loop. It
// lists only commands that exist in cmd/multica; everything else is one
// `--help` away.
func writeCommands(b *strings.Builder, ctx TaskContextForEnv) {
	b.WriteString("## Commands\n\n")
	b.WriteString("Reach Multica only through the `multica` CLI, never `curl` / `wget`. Run `multica <command> --help` for flags. `--output json` writes JSON to stdout and notes to stderr; never merge them (`2>&1`) into what you parse — a write that succeeded would read as failed and get retried.\n\n")
	b.WriteString("- `multica issue get <id>` / `issue list` / `issue children <id>` — read issues.\n")
	b.WriteString("- `multica issue comment list <issue-id> --compact --output json` with `--roots-only --summary` (scan), `--thread <comment-id> --tail N` or `--since <RFC3339>`; `--recent N` caps threads, not comments.\n")
	b.WriteString("- `multica issue comment add <issue-id> --content-file <path> [--parent <comment-id>] [--attachment <path>]` — post a comment.\n")
	b.WriteString("- `multica issue create --title \"...\" [--description-file <path>] [--parent <issue-id>] [--assignee X] [--status todo|backlog] [--stage <N>]` / `issue update <id> ...` — an agent-assigned `todo` issue starts its run at once; `backlog` parks it.\n")
	b.WriteString("- `multica issue assign <id> (--to X | --unassign)` — assigning an agent starts its run.\n")
	writeIssueStatusCommand(b, ctx)
	b.WriteString("- `multica plan propose <issue>` — propose a plan when the issue asks for one; nothing starts until it is approved.\n")
	b.WriteString("- `multica repo checkout <url> [--ref <branch-or-sha>] [--fresh]` — `--fresh` discards uncommitted work.\n")
	b.WriteString("- `multica attachment download <id>` — never open Multica resource URLs directly.\n\n")
	b.WriteString("Keep the user's Git identity; override via `git config --worktree`, never global.\n\n")
}

// briefStatusCategoryOrder groups the briefing catalog by internal lifecycle,
// matching ListIssueStatusEntries. User-facing columns still use status keys.
var briefStatusCategoryOrder = issuestatus.Categories()

// writeIssueStatusCommand emits the `multica issue status` bullet.
//
// With no custom statuses on the claim (including old-server payloads), it
// emits the fixed built-in status list.
//
// With custom statuses it replaces the seven-value enumeration with the
// workspace's catalog, grouped by lifecycle category. Special workflow rules
// still name fixed built-in keys; a custom status inherits only lifecycle.
//
// Each line leads with the category name as PLAIN TEXT, not a code token.
// Three of the four category names — unstarted, started, closed — are not
// status keys at all: ValidateKey reserves them, so no catalog row can hold
// one, and Resolve returns ErrUnknownStatus. `done` is the exception, being
// both a lifecycle category and a built-in key. Backticking the group label
// the way the settable keys beside it are backticked therefore invited
// `multica issue status <id> started`, which is a 400, so only keys are
// backticked here (MUL-7379).
//
// Name and description ride along because instructions and users refer to
// statuses by display name ("move it to Human Review"), and the description is
// the admin's disambiguator when a category holds more than one status.
//
// Name/description are user-authored: they pass through
// sanitizeNameForBriefMarkdown so a crafted status name cannot inject
// headings or break out of the surrounding inline markdown. Keys are
// CHECK-constrained server-side; sanitizeBriefCodeToken is defense-in-depth,
// and an entry whose key fails it is dropped rather than rendered mangled.
func writeIssueStatusCommand(b *strings.Builder, ctx TaskContextForEnv) {
	if len(ctx.IssueStatuses) == 0 {
		b.WriteString("- `multica issue status <id> <status>` — flip status (todo / in_progress / in_review / done / blocked / backlog / cancelled).\n")
		return
	}
	byCategory := make(map[string][]IssueStatusForEnv, len(briefStatusCategoryOrder))
	unknownCategories := 0
	for _, s := range ctx.IssueStatuses {
		if sanitizeBriefCodeToken(s.Key) == "" {
			continue
		}
		category, ok := issuestatus.ParseCategory(s.Category)
		if !ok {
			unknownCategories++
			continue
		}
		byCategory[category] = append(byCategory[category], s)
	}
	b.WriteString("- `multica issue status <id> <status>` — flip status. Available statuses by lifecycle category:\n")
	for _, category := range briefStatusCategoryOrder {
		customs := byCategory[category]
		fmt.Fprintf(b, "  - %s category: `%s` (built-in)", category, strings.Join(issuestatus.BehaviorsForCategory(category), "`, `"))
		for _, s := range customs {
			name := sanitizeNameForBriefMarkdown(s.Name)
			desc := sanitizeNameForBriefMarkdown(s.Description)
			fmt.Fprintf(b, ", `%s`", sanitizeBriefCodeToken(s.Key))
			switch {
			case name != "" && desc != "":
				fmt.Fprintf(b, " (%s — %s)", name, desc)
			case name != "":
				fmt.Fprintf(b, " (%s)", name)
			}
		}
		b.WriteString("\n")
	}
	// Count only otherwise renderable entries, without echoing untrusted
	// category values or conflating these omissions with the server's cap.
	if unknownCategories > 0 {
		fmt.Fprintf(b, "  - Custom statuses omitted due to unrecognized categories: %d.\n", unknownCategories)
	}
	if ctx.IssueStatusesOmitted > 0 {
		fmt.Fprintf(b, "  - …and %d more custom statuses not listed; an invalid status errors with the full valid list.\n", ctx.IssueStatusesOmitted)
	}
}

// writeBodyFormatting emits the file-first rule for every agent-authored body.
// Inline `--content` lets the shell rewrite the body before the CLI sees it
// (MUL-2904); heredoc `--content-stdin` swallows trailing flags (#4182) and, on
// Windows PowerShell 5.1, drops non-ASCII. The cleanup is gated on the post so
// a failed post is not masked by the cleanup's exit status.
func writeBodyFormatting(b *strings.Builder) {
	b.WriteString("## Writing bodies\n\n")
	b.WriteString("Write comment and issue bodies to a UTF-8 file inside your working directory (never `/tmp`), then pass `--content-file` / `--description-file`. Never use inline `--content`, and never `--content-stdin`: the shell rewrites the body or swallows flags. Do not write literal `\\n` escapes. An issue's title is its H1, so a description starts with prose or `##` headings.\n\n")
	if runtimeGOOS == "windows" {
		b.WriteString("Post with `--output table` and delete the file only after the post succeeded: `&& rm ./reply.md` in Git Bash; in PowerShell check `$LASTEXITCODE` before `Remove-Item ./reply.md`.\n\n")
		return
	}
	b.WriteString("Post with `--output table` and delete the file only after the post succeeded (`&& rm ./reply.md`).\n\n")
}

// writeRepositories emits the workspace repositories when any are configured.
func writeRepositories(b *strings.Builder, ctx TaskContextForEnv) {
	if len(ctx.Repos) == 0 {
		return
	}
	b.WriteString("## Repositories\n\n")
	b.WriteString("Fetch with `multica repo checkout <url>`:\n\n")
	pinned := false
	for _, repo := range ctx.Repos {
		line := "- " + repo.URL
		if repo.Description != "" {
			line += " — " + repo.Description
		}
		// The daemon already applies the ref on checkout; it is printed so the
		// agent delivers to the same line instead of the default branch.
		if ref := strings.TrimSpace(repo.Ref); ref != "" {
			pinned = true
			line += fmt.Sprintf(" (starts from `%s`)", ref)
		}
		b.WriteString(line + "\n")
	}
	if pinned {
		// `gh pr create` defaults to the repo default branch, so the agent has
		// to pass --base itself. A pin may also be a tag or commit, which has
		// no branch to merge back into.
		b.WriteString("\nA repository that starts from a branch is already checked out there (no `--ref` needed); open pull requests with `gh pr create --base <that-branch>`. If it starts from a tag or commit, confirm the target branch first.\n")
	}
	// A kept checkout reports the branch the work sits ON (the PR head), never
	// where it was meant to land; the project's current pin may have changed
	// since the work began.
	b.WriteString("\nIf checkout says it KEPT an existing checkout, its branch is the head of a pull request, never its base: deliver to the base of its existing pull request or the target the task states, and ask if neither settles it.")
	if pinned {
		b.WriteString(" Do not retarget it to a starting point listed above.")
	}
	b.WriteString("\n\n")
}

// writeProjectContext emits the active project, its capped description, and
// its resources.
func writeProjectContext(b *strings.Builder, ctx TaskContextForEnv) {
	if ctx.ProjectID == "" && len(ctx.ProjectResources) == 0 {
		return
	}
	b.WriteString("## Project Context\n\n")
	if ctx.ProjectTitle != "" {
		fmt.Fprintf(b, "The active project for this task is **%s**.\n\n", ctx.ProjectTitle)
	}
	if desc := strings.TrimSpace(ctx.ProjectDescription); desc != "" {
		desc, truncated := capUserBlock(desc)
		b.WriteString("Project description (set by the project owner):\n\n")
		b.WriteString(desc)
		if truncated {
			project := ctx.ProjectID
			if project == "" {
				project = "<project-id>"
			}
			b.WriteString("\n" + truncationPointer("multica project get "+project+" --output json"))
		}
		b.WriteString("\n\n")
	}
	if len(ctx.ProjectResources) > 0 {
		b.WriteString("Project resources (also in `.multica/project/resources.json`; open only when relevant):\n\n")
		for _, r := range ctx.ProjectResources {
			fmt.Fprintf(b, "- %s\n", formatProjectResource(r))
		}
		b.WriteString("\nA resource with a starting point is checked out there automatically; pass `--ref <branch-or-sha>` only when the task names a different revision.\n\n")
	}
}

// The SessionContinuityNotice* pair tells the agent that a resume the task
// expected could not be honored (MUL-4424). The record of the conversation
// survives on both surfaces — the issue's comments, or the chat transcript
// behind `multica chat history` — so the notice says what is actually gone:
// the agent's own unrecorded working memory from the turns that did not come
// back. It is per-run, so it travels in the per-turn prompt (MUL-5377). The
// run "does not continue" the lost session rather than "is on a fresh one":
// under MUL-5305 the daemon may resume an OLDER session (MUL-6984 review).
const SessionContinuityNoticeIssue = "## Session Continuity Notice\n\n" +
	"This run was meant to continue an earlier conversation, but that provider session could not be restored, and this run does not continue it. The issue and its comment history are unaffected and are the authoritative record; reading them reconstructs the conversation. What is gone is your own working memory from the turns that did not come back, so re-derive what you need and do not claim continuity the record cannot back up. Do not open your reply by announcing this.\n\n"

const SessionContinuityNoticeChatTranscript = "## Session Continuity Notice\n\n" +
	"This run was meant to continue an earlier conversation, but that provider session could not be restored, and this run does not continue it. The conversation itself is unaffected: read it back with `multica chat history` before acting and treat it as authoritative. What is gone is your own working memory from the turns that did not come back, so re-derive what you need. Do not open your reply by announcing this.\n\n"

// writeWorkflowChat emits the chat-mode workflow. Room shape is per-run and is
// stated by daemon.BuildPrompt instead (MUL-5377, MUL-5442).
func writeWorkflowChat(b *strings.Builder) {
	b.WriteString("## Workflow\n\n")
	b.WriteString("**You are in chat mode.** Reply conversationally, concisely and directly. Use the `multica` CLI to look things up or act (`multica issue list`, `multica workspace get`, …) and `multica repo checkout <url>` when you need code.\n\n")
}

// writeWorkflowIssue emits the single workflow every issue run follows,
// whatever triggered it. Nothing here may depend on the trigger or carry a
// per-run id: those travel in the per-turn prompt so the brief stays
// byte-identical across a resumed session (MUL-5377).
//
// Agent Identity outranks the workflow and may forbid any step (#3802). The
// in_progress write sits inside the numbered list because a rule outside the
// list did not fire (MUL-6460); whether a turn writes status depends only on
// whether it produced the issue's own deliverable, which keeps concurrent runs
// from flapping the board (MUL-6417). The comment scan is mandatory unless the
// per-turn prompt carries the server's own answer to it (MUL-6984, MUL-7344).
func writeWorkflowIssue(b *strings.Builder, ctx TaskContextForEnv) {
	b.WriteString("## Workflow\n\n")
	b.WriteString("One owner, one run per issue. The per-turn message gives the issue id, the trigger, your `--parent` if a comment triggered the run, and exact read commands. Agent Identity outranks this workflow: skip any step it forbids and do the rest.\n\n")
	b.WriteString("1. Read the issue, then the comments: scan every thread (`--roots-only --summary`), then expand the ones that matter (`--thread <id> --tail 30`). Skip a read only when the per-turn message says nothing changed. Title, description and comments are the instructions; `source_context` is background only.\n")
	b.WriteString("2. If this turn produces any of the issue's own deliverable, set `in_progress` first, unless it already is.\n")
	b.WriteString("3. Do the work.\n")
	b.WriteString("4. Post ONE result comment (see Output), under this turn's `--parent` if given, else top-level.\n")
	b.WriteString("5. Leave the status true: `in_review` when delivered (`done` is set by whoever accepts it); `blocked` when you cannot proceed, saying why. A turn that only answered a question writes no status.\n")
	if len(ctx.IssueStatuses) > 0 {
		b.WriteString("\nThe workflow rules above refer to exact built-in status keys, not categories. Custom statuses share lifecycle semantics only, not built-in automation behavior.\n")
	}
	b.WriteString("\n")
}

// writeSkills points at the multica-platform guide the daemon installs into
// the user-level skill directories (platformskill.Install). Other skills are
// the project's and the user's own; each runtime lists those itself, so the
// brief does not repeat them. The pointer is the one recall hint, because that
// skill's trigger is the platform itself rather than a task.
func writeSkills(b *strings.Builder) {
	b.WriteString("## Skills\n\n")
	b.WriteString("For a Multica action this brief does not cover — issue and PR contracts, sub-issues and stages, mentions, agents, projects, runtimes — load the `" + platformskill.Name + "` skill and open the references for the domains your task touches.\n\n")
}

// writeMentions states that an @mention is an action, not a name. Every past
// incident was an agent mentioning for a false need: notifying followers who
// already see the comment, courtesy, or crediting someone in prose (MUL-6528).
func writeMentions(b *strings.Builder) {
	b.WriteString("## Mentions\n\n")
	b.WriteString("`[MUL-123](mention://issue/<issue-id>)` and `[Name](mention://project/<project-id>)` are plain links. `[@Name](mention://member/<user-id>)` **notifies a person**; `[@Name](mention://agent/<agent-id>)` **starts a run for that agent**. Mention only to pull someone into work they are not doing yet. Followers already see your comment, naming someone in prose stays plain text, and a thank-you or FYI mention of an agent costs a paid run. When unsure, leave it out: a missed mention costs one follow-up ask, a stray one costs a run.\n\n")
}

// writeOutput emits the per-surface delivery rule plus the always-on
// invariant that runtime-local paths are never deliverables (MUL-4899): the
// path exists only on this machine, so a link to it is dead for every reader.
func writeOutput(b *strings.Builder, kind taskKind) {
	b.WriteString("## Output\n\n")
	switch kind {
	case kindChat:
		b.WriteString("Your reply goes straight to the chat window. **Delivering files here:** run `multica attachment upload <local-path>` — it binds the file to your reply as an attachment card; a path written into your reply is not delivered.\n")
	default:
		b.WriteString("⚠️ **Deliver through `multica issue comment add`** — nobody sees your terminal. **Post exactly ONE comment per run**, the final result; no progress updates. State the outcome, not the process.\n\n")
		b.WriteString("**Delivering files here:** pass `--attachment <path>` to `multica issue comment add` (repeatable).\n")
	}
	b.WriteString("**Charts and diagrams:** a fenced `html` or `mermaid` code block renders in place (`title=\"...\"` after the language); an attached file shows as a card.\n\n")
	b.WriteString("**Runtime-local paths are never deliverables.** NEVER write an absolute path or a `file://` URL as a clickable link or an embedded image — it exists only on this machine, including files you downloaded. Reference code as inline code: `path/to/file.ts:42`.\n")
}

// buildMetaSkillContentSlim assembles the brief. Chat runs skip the issue-only
// sections (body formatting keeps applying because chat can create issues).
func buildMetaSkillContentSlim(provider string, ctx TaskContextForEnv) string {
	var b strings.Builder
	kind := classifyTask(ctx)

	writeHeader(&b)
	writeAgentIdentity(&b, ctx)
	writeRequestingUser(&b, ctx)
	writeWorkspaceContext(&b, ctx)
	writeBackgroundWork(&b)
	writeCommands(&b, ctx)
	writeBodyFormatting(&b)
	writeRepositories(&b, ctx)
	writeProjectContext(&b, ctx)
	switch kind {
	case kindChat:
		writeWorkflowChat(&b)
	default:
		writeWorkflowIssue(&b, ctx)
	}
	writeSkills(&b)
	if kind == kindIssue {
		writeMentions(&b)
	}
	writeOutput(&b, kind)
	return b.String()
}
