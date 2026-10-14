package daemon

import (
	"fmt"
	"strings"

	"github.com/multica-ai/multica/server/internal/daemon/execenv"
)

// sessionContinuityNoticeFor picks the notice matching what this surface
// actually lost. An issue's comments and a chat's chat_message transcript are
// both still readable (MUL-5722).
func sessionContinuityNoticeFor(task Task) string {
	if task.ChatSessionID == "" {
		return execenv.SessionContinuityNoticeIssue
	}
	return execenv.SessionContinuityNoticeChatTranscript
}

// backendResumeContinuityNotice returns the notice the BACKEND should inject if
// it lands on a fresh thread, or "" when the prompt already carries one.
//
// Only one notice may reach a turn. Two paths can produce it — the daemon,
// which appends it to the prompt whenever it already knows the resume is gone,
// and the backend, which is the only one that can see a live resume RPC being
// rejected mid-run. Before MUL-5722 both fired on the codex overflow retry, so
// the same paragraph was paid for twice in one turn and maintained as two
// hand-written strings. Deriving the backend's copy from the daemon's, and
// suppressing it exactly when the prompt already said it, makes a duplicate
// structurally impossible rather than merely unlikely.
func backendResumeContinuityNotice(task Task) string {
	if task.PriorSessionResumeUnavailable {
		return ""
	}
	return sessionContinuityNoticeFor(task)
}

// perTurnContextBlocks renders the run-scoped context blocks that used to live
// in the runtime brief (CLAUDE.md / AGENTS.md).
//
// Every value here changes from one run to the next on the same issue — the
// authorization human differs between runs, the continuity notice is
// true of one run and false of the next. Claude Code loads the brief into
// messages[0], ahead of the entire conversation, so rendering these there threw
// away the prompt cache for the whole history on every resume. Appending them
// to the per-turn user message puts them after the cached prefix instead, where
// changing them costs only this turn's own tokens (MUL-5377).
//
// Returns "" when none of the blocks apply.
func perTurnContextBlocks(task Task, opts promptOpts) string {
	var b strings.Builder
	b.WriteString(buildSharedLocalDirectoryBlock(opts.sharedLocalDirectory))
	if task.PriorSessionResumeUnavailable {
		b.WriteString(sessionContinuityNoticeFor(task))
	}
	b.WriteString(execenv.BuildOnBehalfOfBlock(task.InitiatorName, task.InitiatorEmail))
	return b.String()
}

// promptOpts carries per-run facts the claimed Task does not: things only the
// daemon's own execution context can answer. Kept behind PromptOption so the
// common BuildPrompt(task, provider) call sites stay unchanged.
type promptOpts struct {
	sharedLocalDirectory bool
}

// PromptOption tunes per-turn prompt copy with run-scoped context.
type PromptOption func(*promptOpts)

// WithSharedLocalDirectory marks a turn that runs inside the user's own
// directory through a local_directory resource. Nothing queues tasks on that
// directory, so a sibling task may be working in the same tree, and the agent
// has to be told to isolate its own code changes.
func WithSharedLocalDirectory() PromptOption {
	return func(o *promptOpts) { o.sharedLocalDirectory = true }
}

// buildSharedLocalDirectoryBlock warns a local_directory turn that its working
// directory is shared live, and points code changes at a worktree of its own:
// switching branches in the shared checkout would yank the tree out from under
// every other task (and the user) working in it.
func buildSharedLocalDirectoryBlock(shared bool) string {
	if !shared {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Shared working directory\n\n")
	b.WriteString("Your working directory is the user's own folder, and other tasks on this machine may be running in it at the same time.\n\n")
	b.WriteString("To change code, create your own branch in a separate git worktree (`git worktree add <path> -b <branch>`) and work there. Do not switch branches, reset, or stash in the original checkout.\n\n")
	return b.String()
}

// BuildPrompt constructs the task prompt for an agent CLI.
// Keep this minimal — detailed instructions live in CLAUDE.md / AGENTS.md
// injected by execenv.InjectRuntimeConfig. The provider string is threaded
// through to comment-triggered tasks' per-turn reply template; that template
// is provider-agnostic AND host-agnostic now (every OS → write a UTF-8 file,
// post with `--content-file`) because the shell-layer corruption it guards
// against is not specific to any one provider or host (MUL-2904, #4182).
func BuildPrompt(task Task, provider string, options ...PromptOption) string {
	var opts promptOpts
	for _, apply := range options {
		apply(&opts)
	}
	body := buildPromptBody(task, provider)
	// Run-scoped context is appended, never prepended: everything ahead of it
	// is stable across runs of a resumed session, and appending keeps it after
	// the cached prefix (MUL-5377).
	if blocks := perTurnContextBlocks(task, opts); blocks != "" {
		if !strings.HasSuffix(body, "\n\n") {
			body += "\n"
		}
		body += blocks
	}
	return body
}

func buildPromptBody(task Task, provider string) string {
	if task.ChatSessionID != "" {
		return buildChatPrompt(task)
	}
	if task.TriggerCommentID != "" {
		return buildCommentPrompt(task, provider)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Your assigned issue ID is: %s\n\n", task.IssueID)
	// Assignment handoff is run-scoped data from legacy clients. Keep it in the
	// per-turn prompt rather than the cached runtime brief (MUL-5377).
	if task.HandoffNote != "" {
		b.WriteString("You were handed this issue with a handoff note. Treat it as the assigner's scoping instruction for this run; follow it before doing anything broader, and do not reply to it as if it were a comment:\n\n")
		fmt.Fprintf(&b, "> %s\n\n", task.HandoffNote)
	}
	// Workflow step 1 owns the read rule for every issue turn; these lines only
	// hand over the exact commands (MUL-6984).
	fmt.Fprintf(&b, "Start with `multica issue get %s --output json`, then scan the comments with `multica issue comment list %s --roots-only --summary --compact --output json` and expand what matters with `--thread <thread-id> --tail 30`.\n", task.IssueID, task.IssueID)
	return b.String()
}

// buildCommentPrompt constructs a prompt for comment-triggered tasks.
// The triggering comment content is embedded directly so the agent cannot
// miss it, even when stale output files exist in a reused workdir.
// The reply instructions (including the current TriggerCommentID as --parent)
// are re-emitted on every turn so resumed sessions cannot carry forward a
// previous turn's --parent UUID.
func buildCommentPrompt(task Task, provider string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Your assigned issue ID is: %s\n\n", task.IssueID)
	if task.TriggerCommentContent != "" {
		authorLabel := "A user"
		if task.TriggerAuthorType == "system" {
			authorLabel = "The platform"
		} else if task.TriggerAuthorType == "agent" {
			name := task.TriggerAuthorName
			if name == "" {
				name = "another agent"
			}
			authorLabel = fmt.Sprintf("Another agent (%s)", name)
		}
		fmt.Fprintf(&b, "[NEW COMMENT] %s just left a new comment. Focus on THIS one, not earlier ones:\n\n", authorLabel)
		fmt.Fprintf(&b, "> %s\n\n", task.TriggerCommentContent)
		// MUL-4195: comments that arrived before this run started were folded
		// into it rather than dropped. The trigger above is the newest; the
		// agent must ALSO address these earlier ones so no deliberate user
		// instruction is silently lost. Prefer the embedded detail so the agent
		// does not have to guess which thread each folded comment lives in
		// (they may span multiple threads — review should-fix #3); fall back to
		// a thread-agnostic issue-wide fetch hint for old servers that only send
		// the ids.
		if len(task.CoalescedComments) > 0 {
			fmt.Fprintf(&b, "This run also covers %d earlier comment(s) posted before it started — you must read and address them too, not just the one above. They may be in different threads, so each is reproduced here with its own thread:\n\n", len(task.CoalescedComments))
			for _, cc := range task.CoalescedComments {
				authorLabel := "A user"
				if cc.AuthorType == "system" {
					authorLabel = "The platform"
				} else if cc.AuthorType == "agent" {
					name := cc.AuthorName
					if name == "" {
						name = "another agent"
					}
					authorLabel = fmt.Sprintf("Another agent (%s)", name)
				} else if cc.AuthorName != "" {
					authorLabel = cc.AuthorName
				}
				fmt.Fprintf(&b, "- comment %s", cc.ID)
				if cc.CreatedAt != "" {
					fmt.Fprintf(&b, " (%s, %s)", authorLabel, cc.CreatedAt)
				} else {
					fmt.Fprintf(&b, " (%s)", authorLabel)
				}
				if cc.ThreadID != "" {
					fmt.Fprintf(&b, " [thread %s]", cc.ThreadID)
				}
				b.WriteString(":\n")
				fmt.Fprintf(&b, "  > %s\n", strings.ReplaceAll(strings.TrimSpace(cc.Content), "\n", "\n  > "))
			}
			fmt.Fprintf(&b, "\nIf you need the surrounding discussion for any of them, fetch its thread with `multica issue comment list %s --thread <thread-id> --tail 30 --compact --output json` using the thread id shown above.\n\n", task.IssueID)
		} else if len(task.CoalescedCommentIDs) > 0 {
			// MUL-5442: this fallback used to send the agent at `--recent 30`.
			// That flag caps THREADS, not comments, and every returned thread
			// carries all of its descendants — so on an issue with fewer than 30
			// root threads it returned the entire comment history to locate a
			// handful of ids. It also contradicted the brief's own catch-up step,
			// which tells the agent to read in two bounded steps and never make
			// one bulk pull (MUL-5372): the platform was recommending exactly the
			// shape it forbids elsewhere.
			//
			// The replacement is a per-id lookup, which is what makes it
			// deterministic: `--thread` accepts ANY comment id, reply or root, and
			// the server resolves it to the containing thread. So each id can be
			// fetched directly and bounded, without knowing its thread and without
			// guessing which threads look recent.
			//
			// `--since` is only a prefetch, never the guarantee. Two ways it can
			// miss an id, so the per-id pass below is unconditional:
			//   - A retry inherits the previous attempt's coalesced_comment_ids
			//     verbatim (queries/agent.sql RetryTask), while the anchor is
			//     recomputed from the last STARTED task's started_at
			//     (the resumed run, via GetLastTaskSession). An inherited id can
			//     therefore predate the anchor.
			//   - The anchor is only populated when some comment landed after it,
			//     which is independent of where these ids sit.
			// It is also not a precise fetch in the other direction: the window
			// carries the trigger comment and unrelated comments too.
			fmt.Fprintf(&b, "This run also covers %d earlier comment(s) posted before it started — you must read and address every one of them, not just the one above: %s. They may be in DIFFERENT threads, so do not assume they share the triggering thread.\n\n",
				len(task.CoalescedCommentIDs), strings.Join(task.CoalescedCommentIDs, ", "))
			if task.NewCommentsSince != "" {
				fmt.Fprintf(&b, "Start with `multica issue comment list %s --since %s --compact --output json`. Treat that as a candidate window, not a guarantee — it also carries unrelated comments, and a retried run can carry ids older than the window. Check every id above against the result.\n\n",
					task.IssueID, task.NewCommentsSince)
			}
			fmt.Fprintf(&b, "Fetch each id you still need directly: `multica issue comment list %s --thread <comment-id> --tail 30 --compact --output json`. `--thread` accepts a reply id, not just a thread root, so you do not need to know which thread the comment lives in. If it is older than those 30 replies, page back with the `Next reply cursor` values (`--before` / `--before-id`) until it appears. Do not finish this turn until every id above is accounted for.\n\n",
				task.IssueID)
		}
	}
	// Issue-reading pointer (MUL-7344). Same gate as the comment hint below —
	// `resumed` is computed once for both, so one turn can never claim the
	// session is warm enough to skip the issue read while treating it as cold
	// for comments. On anything but a real resume with a server-computed
	// comparison this renders the unconditional read, byte for byte.
	resumed := task.PriorSessionID != "" && !task.PriorSessionResumeUnavailable
	b.WriteString(execenv.BuildIssueStateHint(
		task.IssueID, task.IssueStatus, task.IssueAssigneeType, task.IssueAssigneeID,
		task.IssueChangedFields, task.IssueStateDeltaKnown, resumed,
	))
	// Comment-reading pointer. Which hint renders is decided by whether this
	// run actually RESUMES a provider session, and only then by the new-comment
	// delta — never by the delta alone.
	//
	// "Resumes" means the LATEST turn's context came back. Two states fail that
	// and both must take the reconstruction path (the cold hint; the prompt
	// makes no claim about the provider session, because in the second state
	// the daemon does resume the older one). A run with no prior session is the
	// obvious one. The other is PriorSessionResumeUnavailable: the server
	// withheld a more recent Codex session whose rollout was missing and handed
	// back an OLDER fallback session instead (MUL-5305), so PriorSessionID is
	// non-empty while the memory this turn continues from is stale. Reading
	// only PriorSessionID would call that a warm resume and could skip both the
	// full trigger-thread read and the scan, on the strength of context the run
	// does not have. It reconstructs instead, and the continuity notice
	// perTurnContextBlocks renders says why (MUL-6984).
	//
	// Given a real resume, the delta decides:
	//   - count > 0            → issue-wide count, the triggering thread's
	//                            delta, and the scan as the wide read.
	//   - computed and empty   → the trigger is already injected and the
	//                            server looked at the rest of the issue and
	//                            found nothing, which IS the scan's answer.
	//   - not computed         → same session facts, no claim about the issue:
	//                            the scan is handed over as a command. A zero
	//                            count alone cannot be read as "nothing was
	//                            said" — a failed read, a cold start and an old
	//                            server all produce that same zero, and none of
	//                            them looked (NewCommentsDeltaKnown).
	//
	// Whether the scan happens is never decided here — workflow step 1 owns
	// that; these hints carry this turn's facts and exact commands. Final
	// fallback (no trigger id, shouldn't happen here): plain read.
	var hint string
	if resumed {
		hint = execenv.BuildNewCommentsHint(task.IssueID, task.TriggerCommentID, task.TriggerThreadID, task.NewCommentsSince, task.NewCommentCount)
		if hint == "" {
			if task.NewCommentsDeltaKnown {
				hint = execenv.BuildResumedCommentsHint(task.IssueID, task.TriggerCommentID, task.TriggerThreadID)
			} else {
				hint = execenv.BuildResumedUnknownDeltaCommentsHint(task.IssueID, task.TriggerCommentID, task.TriggerThreadID)
			}
		}
	}
	if hint == "" {
		hint = execenv.BuildColdCommentsHint(task.IssueID, task.TriggerCommentID, task.TriggerThreadID)
	}
	if hint != "" {
		b.WriteString(hint)
	} else {
		fmt.Fprintf(&b, "Read the discussion: scan with `multica issue comment list %s --roots-only --summary --compact --output json`, then expand what matters with `--thread <thread-id> --tail 30`.\n\n", task.IssueID)
	}
	// Reply routing. When this run coalesced comments spanning MORE THAN ONE
	// root thread, answer each thread in its own thread instead of dumping one
	// merged comment (MUL-4348). Same-thread follow-ups collapse to a single
	// group upstream, so they keep the ordinary single-parent path below and can
	// never be split into duplicate replies.
	if targets := commentReplyThreads(task); len(targets) >= 2 {
		b.WriteString(execenv.BuildMultiThreadCommentReplyInstructions(task.IssueID, targets))
	} else {
		b.WriteString(execenv.BuildCommentReplyInstructions(provider, task.IssueID, task.TriggerCommentID))
	}
	return b.String()
}

// commentReplyThreads groups this run's trigger + coalesced comments by their
// root thread, in first-seen order (coalesced comments oldest-first, the newest
// trigger last). A run that coalesced several @mentions from the SAME thread
// yields a single group, so same-thread follow-ups get exactly one consolidated
// reply and can never be split into duplicates; comments from different root
// threads yield one group each so the agent replies inside each thread instead
// of merging them into one blob (MUL-4348).
//
// The reply for each thread targets the NEWEST comment that triggered this run
// in that thread (coalesced comments arrive oldest-first and the trigger is the
// newest overall, so a simple last-write-wins yields the newest per thread).
// That nests the answer next to the most recent question in the thread rather
// than at the thread root, and makes the trigger's own thread (--parent =
// trigger comment) consistent with every other thread instead of a special
// case. Returns nil when there is no trigger or only a single distinct thread —
// the caller then keeps the existing single-parent reply path unchanged.
func commentReplyThreads(task Task) []execenv.ThreadReplyTarget {
	if task.TriggerCommentID == "" {
		return nil
	}
	// A comment with no explicit thread id is a root comment: it is its own
	// thread, so fall back to the comment id itself as the thread key.
	threadKey := func(threadID, commentID string) string {
		if threadID != "" {
			return threadID
		}
		return commentID
	}

	order := make([]string, 0, len(task.CoalescedComments)+1)
	parentByThread := make(map[string]string, len(task.CoalescedComments)+1)
	// note records first-seen order but lets the newest comment win the reply
	// target: inputs are chronological (coalesced oldest-first, trigger last),
	// so the last write for a thread is its newest triggering comment.
	note := func(threadID, parentID string) {
		if _, ok := parentByThread[threadID]; !ok {
			order = append(order, threadID)
		}
		parentByThread[threadID] = parentID
	}

	// Coalesced (older) comments first: reply under the specific comment that
	// mentioned the agent, not the thread root, so a mid-thread mention gets its
	// answer next to the question.
	for _, cc := range task.CoalescedComments {
		note(threadKey(cc.ThreadID, cc.ID), cc.ID)
	}
	// The newest trigger last: it always wins its own thread's reply target,
	// overriding any earlier coalesced comment that shared the trigger's thread.
	note(threadKey(task.TriggerThreadID, task.TriggerCommentID), task.TriggerCommentID)

	if len(order) <= 1 {
		return nil
	}
	targets := make([]execenv.ThreadReplyTarget, 0, len(order))
	for _, tid := range order {
		targets = append(targets, execenv.ThreadReplyTarget{ThreadID: tid, ParentID: parentByThread[tid]})
	}
	return targets
}

// buildChatPrompt constructs a prompt for interactive chat tasks.
func buildChatPrompt(task Task) string {
	// Legacy compatibility for historical proactive-introduction sessions.
	// New agent creation no longer creates a chat or runs this prompt.
	if task.ChatIntro {
		var b strings.Builder
		b.WriteString("You are running as a chat assistant for a Multica workspace.\n")
		b.WriteString("You were just created, and this is the very first message in a direct chat with the person who created you. They have not written anything yet — you are opening the conversation. Send a short, warm, first-person introduction: who you are, what you're good at, and how they can work with you. Do NOT phrase it as an answer to a question or repeat any prompt back; just introduce yourself as if you reached out first.\n")
		return b.String()
	}

	var b strings.Builder
	b.WriteString("You are running as a chat assistant for a Multica workspace.\n")
	b.WriteString("Audience: direct room.\n\n")
	fmt.Fprintf(&b, "User message:\n%s\n", task.ChatMessage)
	// List attachments by id + filename so the agent can fetch them via
	// the CLI. We deliberately do NOT inline the URL: chat attachments
	// live behind a signed CDN with a short TTL, so by the time the agent
	// has finished thinking the URL embedded in the markdown body may
	// have expired. `multica attachment download <id>` re-signs at click
	// time and is the only reliable path.
	if len(task.ChatMessageAttachments) > 0 {
		b.WriteString("\nAttachments on this message:\n")
		for _, a := range task.ChatMessageAttachments {
			if a.ContentType != "" {
				fmt.Fprintf(&b, "- id=%s filename=%q content_type=%s\n", a.ID, a.Filename, a.ContentType)
			} else {
				fmt.Fprintf(&b, "- id=%s filename=%q\n", a.ID, a.Filename)
			}
		}
		b.WriteString("Use `multica attachment download <id>` to fetch each file locally before referring to it.\n")
		b.WriteString("When creating an issue that should preserve one of these attachments, pass `--attachment-id <id>` to `multica issue create` in addition to keeping the attachment markdown inline.\n")
	}
	b.WriteString("\n`multica attachment upload <local-path>` binds a file to your reply as a card; paste the `markdown` snippet it returns on its own line to place it inline.\n")
	return b.String()
}
