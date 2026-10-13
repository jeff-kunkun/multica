package execenv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBuildCommentReplyInstructionsCodexLinux pins that the Linux/macOS
// reply template now mandates `--content-file` (post-#4182). The previous
// `--content-stdin` + HEREDOC mandate (#1795 / #1851 / MUL-2904) was kept
// for years to defend against backtick / `$()` substitution in the body,
// but the heredoc/flag boundary turned out to be fragile in its own right:
// when a model wrapped extra flags around the heredoc on `multica issue
// create`, the flags got swallowed into stdin and silently dropped (OXY-78,
// OXY-76). The file path defeats both classes — the body never reaches the
// shell, and all flags live on one shell-token line.
//
// Not parallel: mutates the package-level runtimeGOOS.
func TestBuildCommentReplyInstructionsCodexLinux(t *testing.T) {
	saved := runtimeGOOS
	t.Cleanup(func() { runtimeGOOS = saved })
	runtimeGOOS = "linux"

	issueID := "11111111-1111-1111-1111-111111111111"
	triggerID := "22222222-2222-2222-2222-222222222222"

	got := BuildCommentReplyInstructions("codex", issueID, triggerID)

	for _, want := range []string{
		"multica issue comment add " + issueID + " --parent " + triggerID + " --content-file ./reply.md --output table && rm ./reply.md",
		"Write the body file first",
		"--content-file ./reply.md",
		"## Writing bodies",
		"do NOT reuse --parent values from previous turns",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("codex/linux reply instructions missing %q\n---\n%s", want, got)
		}
	}

	for _, banned := range []string{
		"--content \"...\"",
		"<<'COMMENT'",
		"cat <<",
		"--parent " + triggerID + " --content-stdin",
	} {
		if strings.Contains(got, banned) {
			t.Fatalf("codex/linux reply instructions should not contain %q\n---\n%s", banned, got)
		}
	}
}

// TestBuildCommentReplyInstructionsNonCodexLinux pins that EVERY provider on
// Linux/macOS — not just Codex — gets the `--content-file` template. Two
// shell-driven failure classes motivate the uniform file path:
//   - MUL-2904 / OKK-497: an agent inlined a backtick-wrapped table name into
//     `--content`; the shell ran it as a command substitution, silently deleted
//     it, the stored comment no longer matched the model's intent, and the
//     model retried forever.
//   - GitHub #4182 (OXY-78 / OXY-76): an agent wrapped extra flags around an
//     `--content-stdin` HEREDOC; the bash heredoc/flag boundary swallowed
//     `--assignee` / `--project` into stdin or dropped them as failed
//     standalone shell statements, while the create still exited 0 with nulls.
//
// Both classes are shell-driven, so the guardrail is uniform across providers
// and across hosts.
//
// Not parallel: mutates the package-level runtimeGOOS.
func TestBuildCommentReplyInstructionsNonCodexLinux(t *testing.T) {
	saved := runtimeGOOS
	t.Cleanup(func() { runtimeGOOS = saved })

	issueID := "11111111-1111-1111-1111-111111111111"
	triggerID := "22222222-2222-2222-2222-222222222222"

	for _, host := range []string{"linux", "darwin"} {
		for _, provider := range []string{"claude", "opencode", "openclaw", "hermes", "kimi", "reasonix", "kiro", "cursor"} {
			name := provider + "/" + host
			t.Run(name, func(t *testing.T) {
				runtimeGOOS = host
				got := BuildCommentReplyInstructions(provider, issueID, triggerID)

				for _, want := range []string{
					"multica issue comment add " + issueID + " --parent " + triggerID + " --content-file ./reply.md --output table && rm ./reply.md",
					// MUL-5442 cross-channel dedup: shell-hazard mechanics live in
					// the brief's Writing bodies; the cookbook keeps the
					// file-first order, the command, and the pointer.
					"Write the body file first",
					"## Writing bodies",
					"do NOT reuse --parent values from previous turns",
					"Post your reply as a comment",
				} {
					if !strings.Contains(got, want) {
						t.Errorf("%s reply instructions missing %q\n---\n%s", name, want, got)
					}
				}

				// The two regressions: agent-authored comments must never be
				// steered at inline `--content "..."` (MUL-2904) and never at
				// `--content-stdin` HEREDOC on multi-flag commands (#4182).
				for _, banned := range []string{
					"--content \"...\"",
					"<<'COMMENT'",
					"cat <<",
					"--parent " + triggerID + " --content-stdin",
				} {
					if strings.Contains(got, banned) {
						t.Errorf("%s reply instructions still contains %q\n---\n%s", name, banned, got)
					}
				}
			})
		}
	}
}

// TestBuildCommentReplyInstructionsWindowsUsesContentFile pins that on
// Windows every provider — Codex AND non-Codex — gets the
// `--content-file` template. The bug is shell-layer, not provider-layer:
// any agent on Windows piping HEREDOC through PowerShell loses non-ASCII
// bytes (PS 5.1's `$OutputEncoding` defaults to ASCIIEncoding). Issues
// #2198 (Chinese, Codex), #2236 (Chinese, Codex), #2376 (Cyrillic,
// non-Codex agent name) all match this signature.
//
// Not parallel: mutates the package-level runtimeGOOS.
func TestBuildCommentReplyInstructionsWindowsUsesContentFile(t *testing.T) {
	saved := runtimeGOOS
	t.Cleanup(func() { runtimeGOOS = saved })
	runtimeGOOS = "windows"

	issueID := "11111111-1111-1111-1111-111111111111"
	triggerID := "22222222-2222-2222-2222-222222222222"

	for _, provider := range []string{"codex", "claude", "opencode", "openclaw", "hermes", "kimi", "reasonix", "kiro", "cursor"} {
		t.Run(provider+"/windows", func(t *testing.T) {
			got := BuildCommentReplyInstructions(provider, issueID, triggerID)
			for _, want := range []string{
				"multica issue comment add " + issueID + " --parent " + triggerID + " --content-file ./reply.md --output table",
				// MUL-5442 cross-channel dedup: the $OutputEncoding trap's
				// full mechanics live once, in the brief's Windows Comment
				// Formatting variant; the per-turn cookbook keeps the ban,
				// the one-line consequence, and the pointer.
				"if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }",
				// #8627: the host OS does not fix the agent's shell — Claude
				// Code with CLAUDE_CODE_USE_POWERSHELL_TOOL=0 runs Git Bash —
				// so the Windows cookbook carries a `&&` variant too.
				"Git Bash",
				"multica issue comment add " + issueID + " --parent " + triggerID + " --content-file ./reply.md --output table && rm ./reply.md",
				"Write the body file first",
				"never pipe via `--content-stdin`",
				"PowerShell drops non-ASCII",
				"## Writing bodies",
			} {
				if !strings.Contains(got, want) {
					t.Errorf("%s reply instructions missing %q\n---\n%s", provider, want, got)
				}
			}
			for _, banned := range []string{
				"<<'COMMENT'",
				"--parent " + triggerID + " --content-stdin",
				"cat <<",
			} {
				if strings.Contains(got, banned) {
					t.Errorf("%s/windows reply instructions should not contain %q\n---\n%s", provider, banned, got)
				}
			}
		})
	}
}

func TestBuildCommentReplyInstructionsEmptyWhenNoTrigger(t *testing.T) {
	t.Parallel()

	for _, provider := range []string{"codex", "claude", "opencode"} {
		if got := BuildCommentReplyInstructions(provider, "issue-id", ""); got != "" {
			t.Fatalf("expected empty string when triggerCommentID is empty for %s, got %q", provider, got)
		}
	}
}

// The brief must never carry this turn's trigger comment id; it points the
// agent at the per-turn user message instead (MUL-5377).
func TestInjectRuntimeConfigKeepsTriggerCommentOutOfBrief(t *testing.T) {
	saved := runtimeGOOS
	t.Cleanup(func() { runtimeGOOS = saved })
	runtimeGOOS = "linux"

	dir := t.TempDir()
	issueID := "11111111-1111-1111-1111-111111111111"
	triggerID := "22222222-2222-2222-2222-222222222222"

	if _, err := InjectRuntimeConfig(dir, "claude", TaskContextForEnv{
		IssueID:          issueID,
		TriggerCommentID: triggerID,
	}); err != nil {
		t.Fatalf("InjectRuntimeConfig failed: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
	if err != nil {
		t.Fatalf("read CLAUDE.md: %v", err)
	}
	s := string(content)

	if strings.Contains(s, triggerID) {
		t.Errorf("CLAUDE.md must not carry the trigger comment id (MUL-5377)\n---\n%s", s)
	}
	for _, want := range []string{
		// MUL-6417 retired the turn-mode router: one workflow, delivery
		// routed on what the per-turn message carries. Pin both delivery
		// cases — a compression that drops either must fail here.
		"## Workflow",
		"under this turn's `--parent` if given, else top-level",
		// The no-write default that makes the un-routed workflow safe: a
		// turn that moved nothing writes nothing (MUL-6300 → MUL-6417).
		"A turn that only answered a question writes no status.",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("CLAUDE.md missing %q\n---\n%s", want, s)
		}
	}

	// The retired router must not come back in any phrasing.
	for _, banned := range []string{"Turn mode", "mode block", "No mode line"} {
		if strings.Contains(s, banned) {
			t.Errorf("CLAUDE.md still carries retired mode-router text %q (MUL-6417)\n---\n%s", banned, s)
		}
	}
}

// Windows reply instructions are file-first, never stdin. The instructions
// now ship in the per-turn prompt, so pin the helper directly (MUL-5377).
func TestWindowsCommentReplyInstructionsHaveNoStdin(t *testing.T) {
	saved := runtimeGOOS
	t.Cleanup(func() { runtimeGOOS = saved })
	runtimeGOOS = "windows"

	issueID := "11111111-1111-1111-1111-111111111111"
	triggerID := "22222222-2222-2222-2222-222222222222"

	for _, provider := range []string{"claude", "codex", "opencode"} {
		t.Run(provider, func(t *testing.T) {
			s := BuildCommentReplyInstructions(provider, issueID, triggerID)
			for _, want := range []string{
				"multica issue comment add " + issueID + " --parent " + triggerID + " --content-file",
				"--content-file",
			} {
				if !strings.Contains(s, want) {
					t.Errorf("%s reply instructions missing %q\n---\n%s", provider, want, s)
				}
			}
			for _, banned := range []string{
				"--parent " + triggerID + " --content-stdin",
				"always use `--content-stdin` with a HEREDOC, even for short single-line replies",
			} {
				if strings.Contains(s, banned) {
					t.Errorf("%s reply instructions must not prescribe stdin on Windows: %q\n---\n%s", provider, banned, s)
				}
			}
		})
	}
}

// TestInjectRuntimeConfigWindowsAssignmentBriefStaysFileOnly pins the PR #3654
// review fix: on Windows, the ASSIGNMENT-triggered brief must never *recommend*
// `--content-stdin`. Unlike the comment-trigger path, the assignment workflow
// has no BuildCommentReplyInstructions override, so an agent that follows the
// "post your final results" step literally would pipe its final comment through
// PowerShell and drop non-ASCII bytes (#2198 / #2236 / #2376). The OS-aware
// ## Writing bodies section (file-only) is the single source of truth; the
// Commands entry and the workflow must defer to it, not re-offer stdin.
//
// Not parallel: mutates the package-level runtimeGOOS.
func TestInjectRuntimeConfigWindowsAssignmentBriefStaysFileOnly(t *testing.T) {
	saved := runtimeGOOS
	t.Cleanup(func() { runtimeGOOS = saved })
	runtimeGOOS = "windows"

	// Assignment-triggered: IssueID set, no TriggerCommentID.
	ctx := TaskContextForEnv{IssueID: "issue-1"}

	for _, provider := range []string{"claude", "codex", "opencode"} {
		t.Run(provider, func(t *testing.T) {
			dir := t.TempDir()
			if _, err := InjectRuntimeConfig(dir, provider, ctx); err != nil {
				t.Fatalf("InjectRuntimeConfig failed: %v", err)
			}
			fileName := "CLAUDE.md"
			if provider != "claude" {
				fileName = "AGENTS.md"
			}
			data, err := os.ReadFile(filepath.Join(dir, fileName))
			if err != nil {
				t.Fatalf("read %s: %v", fileName, err)
			}
			s := string(data)

			// The Writing bodies section is file-only.
			for _, want := range []string{
				"## Writing bodies",
				"Write comment and issue bodies to a UTF-8 file",
				"Never use inline `--content`, and never `--content-stdin`",
				"Post with `--output table`",
				"delete the file only after the post succeeded",
				"in PowerShell check `$LASTEXITCODE` before `Remove-Item ./reply.md`",
			} {
				if !strings.Contains(s, want) {
					t.Errorf("%s missing Windows file-only guidance %q\n---\n%s", fileName, want, s)
				}
			}

			// No prose may RECOMMEND stdin on Windows. The flag synopsis may
			// still list `--content-stdin`; only the prescriptive "file or
			// stdin" phrasings are banned.
			for _, banned := range []string{
				"or `--content-stdin`",
				"using `--content-file` or `--content-stdin`",
				"use `--content-file <path>` or `--content-stdin`",
			} {
				if strings.Contains(s, banned) {
					t.Errorf("%s recommends stdin on Windows: %q\n---\n%s", fileName, banned, s)
				}
			}
		})
	}
}
